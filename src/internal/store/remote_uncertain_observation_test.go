package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestUncertainRemoteObservationPagesOnlyExpiredRemoteAttempts(t *testing.T) {
	authority := p074Store(t)
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	authority.now = func() time.Time { return now }
	ctx := context.Background()
	for _, suffix := range []string{"a", "b", "active"} {
		input := remoteObservationRunInput(t, suffix)
		if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
			t.Fatal(err)
		}
		lease := time.Minute
		if suffix == "active" {
			lease = time.Hour
		}
		if _, err := authority.ClaimLocalIntent(ctx, input.IntentID, "router", lease); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentUncertain, "lost_reply"); err != nil {
			t.Fatal(err)
		}
	}
	local := p057CreateSessionIntent(t, "intent-observe-local", "session-observe-local", "key-observe-local")
	if _, err := authority.CreateLocalIntent(ctx, local); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ClaimLocalIntent(ctx, local.IntentID, "router", time.Minute); err != nil {
		t.Fatal(err)
	}
	submit := p057SubmitIntent(t, "intent-observe-submit", "session-observe-submit", "command-observe-submit", "key-observe-submit", 1, "echo test")
	if _, err := authority.CreateLocalIntent(ctx, submit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ClaimLocalIntent(ctx, submit.IntentID, "router", time.Minute); err != nil {
		t.Fatal(err)
	}
	dispatching := remoteObservationRunInput(t, "dispatching")
	if _, err := authority.CreateLocalIntent(ctx, dispatching); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ClaimLocalIntent(ctx, dispatching.IntentID, "router", time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	first, err := authority.ListRecoverableRemoteIntentsAfter(ctx, 1, nil)
	if err != nil || len(first) != 1 || first[0].IntentID != "intent-observe-a" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := authority.ListRecoverableRemoteIntentsAfter(ctx, 1, &RemoteIntentCursor{CreatedAt: first[0].CreatedAt, IntentID: first[0].IntentID})
	if err != nil || len(second) != 1 || second[0].IntentID != "intent-observe-b" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	end, err := authority.ListRecoverableRemoteIntentsAfter(ctx, 1, &RemoteIntentCursor{CreatedAt: second[0].CreatedAt, IntentID: second[0].IntentID})
	if err != nil || len(end) != 0 {
		t.Fatalf("end=%+v err=%v", end, err)
	}
	if _, err := authority.ListRecoverableRemoteIntentsAfter(ctx, 1001, nil); !errors.Is(err, ErrInvalidLocalIntent) {
		t.Fatalf("unbounded query=%v", err)
	}
	if _, err := authority.ListRecoverableRemoteIntentsAfter(ctx, 1, &RemoteIntentCursor{IntentID: domain.IntentID("intent-valid")}); !errors.Is(err, ErrInvalidLocalIntent) {
		t.Fatalf("invalid cursor=%v", err)
	}
}

func TestAcceptObservedRemoteIntentRejectsRenewedOrChangedAttempt(t *testing.T) {
	for _, change := range []string{"active", "renewed", "state", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			authority := p074Store(t)
			now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
			authority.now = func() time.Time { return now }
			ctx := context.Background()
			input := remoteObservationRunInput(t, "cas")
			if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
				t.Fatal(err)
			}
			if _, err := authority.ClaimLocalIntent(ctx, input.IntentID, "router", time.Minute); err != nil {
				t.Fatal(err)
			}
			observed, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentUncertain, "lost_reply")
			if err != nil {
				t.Fatal(err)
			}
			if change != "active" {
				now = now.Add(2 * time.Minute)
			}
			switch change {
			case "renewed":
				// A still-active owner can renew between the read and commit.
				now = now.Add(-90 * time.Second)
				if _, err := authority.RenewLocalIntentLease(ctx, input.IntentID, "router", time.Hour); err != nil {
					t.Fatal(err)
				}
				now = now.Add(90 * time.Second)
			case "state":
				if _, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentNotDelivered, "concurrent_disposition"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := authority.AcceptObservedRemoteIntent(ctx, observed)
			if change != "unchanged" {
				if !errors.Is(err, ErrLocalIntentLeaseLost) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || result.DeliveryState != LocalIntentAccepted || result.AttemptCount != 1 || HasRemoteTerminalProof(result) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			lifecycle, err := authority.ListLocalIntentLifecycle(ctx, input.IntentID)
			if err != nil || len(lifecycle) != 4 || lifecycle[2].NewState != LocalIntentUncertain || lifecycle[3].Reason != "remote_observed_target_found" {
				t.Fatalf("lifecycle=%+v err=%v", lifecycle, err)
			}
		})
	}
}

func remoteObservationRunInput(t *testing.T, suffix string) LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"operation": "run", "environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "script": "echo observe"})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return LocalIntentCreate{IntentID: domain.IntentID("intent-observe-" + suffix), Operation: "run", ResourceID: "job-observe-" + suffix, JobID: domain.JobID("job-observe-" + suffix), SessionID: domain.SessionID("session-observe-" + suffix), CommandID: domain.CommandID("command-observe-" + suffix), Target: target, Environment: "linux-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-observe-" + suffix, PayloadJSON: canonical, ScriptBytes: []byte("echo observe")}
}

func TestAcceptObservedRemoteIntentRollsBackWhenLifecycleWriteFails(t *testing.T) {
	authority := p074Store(t)
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	authority.now = func() time.Time { return now }
	ctx := context.Background()
	input := remoteObservationRunInput(t, "interrupted")
	if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ClaimLocalIntent(ctx, input.IntentID, "original", time.Minute); err != nil {
		t.Fatal(err)
	}
	observed, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentUncertain, "lost_reply")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := authority.db.Exec(`CREATE TRIGGER fail_observed_acceptance BEFORE INSERT ON local_intent_lifecycle WHEN NEW.new_state = 'accepted' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AcceptObservedRemoteIntent(ctx, observed); err == nil {
		t.Fatal("interrupted observation committed")
	}
	stored, err := authority.GetLocalIntent(ctx, input.IntentID)
	if err != nil || stored.DeliveryState != LocalIntentUncertain || stored.AttemptCount != 1 || !stored.UpdatedAt.Equal(observed.UpdatedAt) {
		t.Fatalf("partial acceptance=%+v err=%v", stored, err)
	}
	if _, err := authority.db.Exec(`DROP TRIGGER fail_observed_acceptance`); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AcceptObservedRemoteIntent(ctx, observed); err != nil {
		t.Fatal(err)
	}
}
