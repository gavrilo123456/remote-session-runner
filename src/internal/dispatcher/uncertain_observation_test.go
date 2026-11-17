package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestUncertainRemoteObservationFindsExistingRunAfterRouterRestart(t *testing.T) {
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	authority := p071Authority(t, &now)
	intent := uncertainObservationSeed(t, authority, "restart", true)
	now = now.Add(2 * time.Minute)
	var frames []sshbridge.RequestFrame
	caller := bug029Caller{call: func(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		frames = append(frames, frame)
		if frame.Operation != sshbridge.OperationGetJob || frame.ResourceID != "" || frame.IdempotencyKey != "" {
			t.Fatalf("mutation sent: %+v", frame)
		}
		payload, _ := json.Marshal(map[string]any{"job_id": intent.JobID, "session_id": intent.SessionID, "command_id": intent.CommandID, "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "phase": "complete"})
		return sshbridge.ReplyFrame{ProtocolVersion: 1, RequestID: frame.RequestID, ResponseType: "result", Payload: payload}, nil
	}}
	// A new Router instance must discover the durable uncertain intent.
	driver, err := NewRemoteDriverWithClock(authority, caller, "restarted-router", time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileUncertainRemoteIntents(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || stored.DeliveryState != store.LocalIntentAccepted || stored.AttemptCount != 1 || store.HasRemoteTerminalProof(stored) || len(frames) != 1 {
		t.Fatalf("stored=%+v calls=%d err=%v", stored, len(frames), err)
	}
	if _, err := authority.GetRemoteJobProjection(context.Background(), intent.JobID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("sparse read fabricated terminal projection: %v", err)
	}
	if err := driver.ReconcileUncertainRemoteIntents(context.Background(), 64); err != nil || len(frames) != 1 {
		t.Fatalf("accepted intent re-observed: calls=%d err=%v", len(frames), err)
	}
}

func TestUncertainRemoteObservationFailuresNeverReplayOrChangeLifecycle(t *testing.T) {
	for _, mode := range []string{"not_found", "mismatch", "timeout", "active_lease", "expired", "expires_during_read", "changed_during_read"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
			authority := p071Authority(t, &now)
			intent := uncertainObservationSeed(t, authority, mode, true)
			before, _ := authority.ListLocalIntentLifecycle(context.Background(), intent.IntentID)
			if mode != "active_lease" {
				now = now.Add(2 * time.Minute)
			}
			if mode == "expired" {
				now = now.Add(RemoteUncertaintyWindow)
			}
			calls := 0
			caller := bug029Caller{call: func(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
				calls++
				if frame.Operation != sshbridge.OperationGetJob || frame.ResourceID != "" || frame.IdempotencyKey != "" {
					t.Fatalf("mutation: %+v", frame)
				}
				if mode == "timeout" {
					<-ctx.Done()
					return sshbridge.ReplyFrame{}, ctx.Err()
				}
				if mode == "changed_during_read" {
					// The source read began after expiry, but another actor changed
					// the lifecycle before the observation could be committed. That
					// independent control transaction does not inherit the SSH GET's
					// short observation context.
					if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentNotDelivered, "concurrent_disposition"); err != nil {
						t.Fatal(err)
					}
				}
				payload := []byte(`{"code":"resource_not_found","message":"missing"}`)
				kind := "error"
				if mode == "expires_during_read" {
					now = now.Add(RemoteUncertaintyWindow)
				}
				if mode == "mismatch" || mode == "changed_during_read" || mode == "expires_during_read" {
					job := intent.JobID
					if mode == "mismatch" {
						job = "job-other"
					}
					payload, _ = json.Marshal(map[string]any{"job_id": job, "session_id": intent.SessionID, "command_id": intent.CommandID, "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}})
					kind = "result"
				}
				return sshbridge.ReplyFrame{ProtocolVersion: 1, RequestID: frame.RequestID, ResponseType: kind, Payload: payload}, nil
			}}
			// Only the timeout case needs an artificially short read budget.
			// Other cases exercise the normal shipped observer and must prove
			// their intended guard rather than pass through an accidental timeout.
			options := RemoteObservationOptions{}
			if mode == "timeout" {
				options.Timeout = 10 * time.Millisecond
			}
			driver, err := NewRemoteDriverWithClock(authority, caller, "observer", time.Minute, func() time.Time { return now }, options)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = driver.ObserveUncertainIntent(context.Background(), intent.IntentID)
			if err == nil {
				t.Fatal("failed observation accepted")
			}
			if mode == "changed_during_read" && !errors.Is(err, store.ErrLocalIntentLeaseLost) {
				t.Fatalf("concurrent disposition did not reach the stale-observation guard: %v", err)
			}
			if mode == "active_lease" || mode == "expired" {
				if calls != 0 {
					t.Fatalf("ineligible observation called target %d times", calls)
				}
			} else if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			after, _ := authority.ListLocalIntentLifecycle(context.Background(), intent.IntentID)
			if mode == "changed_during_read" && (len(after) != len(before)+1 || after[len(after)-1].Reason != "concurrent_disposition") {
				t.Fatalf("observer changed the concurrent writer's lifecycle evidence: %+v", after)
			}
			if mode != "changed_during_read" && !reflect.DeepEqual(before, after) {
				t.Fatalf("failure changed lifecycle: before=%+v after=%+v", before, after)
			}
			stored, _ := authority.GetLocalIntent(context.Background(), intent.IntentID)
			if mode == "changed_during_read" && stored.DeliveryState != store.LocalIntentNotDelivered {
				t.Fatalf("observer overwrote the concurrent disposition: %+v", stored)
			}
			if stored.AttemptCount != 1 || store.HasRemoteTerminalProof(stored) {
				t.Fatalf("fabricated attempt/proof: %+v", stored)
			}
		})
	}
}

func TestUncertainRemoteObservationRotatesAfterBlockedHeadAndPreservesTail(t *testing.T) {
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	authority := p071Authority(t, &now)
	first := uncertainObservationSeed(t, authority, "a-head", true)
	later := uncertainObservationSeed(t, authority, "b-tail", true)
	now = now.Add(2 * time.Minute)
	var observed []string
	caller := bug029Caller{call: func(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		if frame.Operation != sshbridge.OperationGetJob || frame.ResourceID != "" || frame.IdempotencyKey != "" {
			t.Fatalf("mutation: %+v", frame)
		}
		var payload map[string]string
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		observed = append(observed, payload["job_id"])
		if payload["job_id"] == string(first.JobID) {
			<-ctx.Done()
			return sshbridge.ReplyFrame{}, ctx.Err()
		}
		return sshbridge.ReplyFrame{ProtocolVersion: 1, RequestID: frame.RequestID, ResponseType: "error", Payload: []byte(`{"code":"resource_not_found","message":"missing"}`)}, nil
	}}
	driver, err := NewRemoteDriverWithClock(authority, caller, "observer", time.Minute, func() time.Time { return now }, RemoteObservationOptions{Timeout: time.Second, BatchTimeout: 15 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_ = driver.ReconcileUncertainRemoteIntents(context.Background(), 2)
	if len(observed) != 1 || observed[0] != string(first.JobID) {
		t.Fatalf("first batch=%+v", observed)
	}
	_ = driver.ReconcileUncertainRemoteIntents(context.Background(), 2)
	if len(observed) != 2 || observed[1] != string(later.JobID) {
		t.Fatalf("tail starved=%+v", observed)
	}
	_ = driver.ReconcileUncertainRemoteIntents(context.Background(), 2)
	if len(observed) != 3 || observed[2] != string(first.JobID) {
		t.Fatalf("scan did not wrap=%+v", observed)
	}
}

func TestUncertainRemoteObservationExcludesOtherMutationsAndLocalRuns(t *testing.T) {
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	authority := p071Authority(t, &now)
	create := p072CreateIntent(t, "intent-observe-excluded-create", "session-observe-excluded-create", domain.SessionStateReady)
	submit := p068SubmitIntent(t, "intent-observe-excluded-submit", "session-observe-excluded-submit", "command-observe-excluded-submit", domain.TargetKindRemote, "echo test")
	inputs := []store.LocalIntentCreate{create, submit, p073CancelIntent(t, "intent-observe-excluded-cancel", submit), p073CloseIntent(t, "intent-observe-excluded-close", create), p068RunIntent(t, "intent-observe-excluded-local", "job-observe-excluded-local", domain.TargetKindLocal)}
	for _, input := range inputs {
		if _, err := authority.CreateLocalIntent(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.ClaimLocalIntent(context.Background(), input.IntentID, "original", time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.TransitionLocalIntent(context.Background(), input.IntentID, store.LocalIntentUncertain, "lost_reply"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Minute)
	caller := bug029Caller{call: func(context.Context, sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		t.Fatal("excluded intent reached transport")
		return sshbridge.ReplyFrame{}, nil
	}}
	driver, err := NewRemoteDriverWithClock(authority, caller, "observer", time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		observed, _, err := driver.ObserveUncertainIntent(context.Background(), input.IntentID)
		if err != nil || observed.DeliveryState != store.LocalIntentUncertain {
			t.Fatalf("excluded mutation changed: %+v err=%v", observed, err)
		}
	}
	if err := driver.ReconcileUncertainRemoteIntents(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
}

func TestUncertainRemoteObservationExcludesAbandonedDispatchingRun(t *testing.T) {
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	authority := p071Authority(t, &now)
	intent := uncertainObservationSeed(t, authority, "dispatching", false)
	now = now.Add(2 * time.Minute)
	caller := bug029Caller{call: func(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		t.Fatalf("abandoned dispatching attempt reached transport: %+v", frame)
		return sshbridge.ReplyFrame{}, nil
	}}
	driver, err := NewRemoteDriverWithClock(authority, caller, "observer", time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileUncertainRemoteIntents(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.ObserveUncertainIntent(context.Background(), intent.IntentID); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || stored.DeliveryState != store.LocalIntentDispatching || stored.AttemptCount != 1 || store.HasRemoteTerminalProof(stored) {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func uncertainObservationSeed(t *testing.T, authority *store.AuthorityStore, suffix string, uncertain bool) store.LocalIntentRecord {
	t.Helper()
	ctx := context.Background()
	input := p149RemoteRunIntent(t, "observe-"+suffix)
	if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.ClaimLocalIntent(ctx, input.IntentID, "original-router", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if uncertain {
		intent, err = authority.TransitionLocalIntent(ctx, input.IntentID, store.LocalIntentUncertain, "remote_transport_uncertain")
		if err != nil {
			t.Fatal(err)
		}
	}
	return intent
}
