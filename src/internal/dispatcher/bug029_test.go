package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestBUG029ReadTimeoutPersistsDiagnosticWithoutReplaying(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	authority := p068Authority(t)
	intent := bug029AcceptIntent(t, authority, "timeout", false)
	caller := bug029Caller{call: func(readCtx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		if frame.Operation != sshbridge.OperationGetJob || frame.ResourceID != "" || frame.IdempotencyKey != "" {
			t.Fatalf("observation sent a mutation: %+v", frame)
		}
		<-readCtx.Done()
		return sshbridge.ReplyFrame{}, readCtx.Err()
	}}
	driver := bug029Driver(t, authority, caller)
	err := driver.ReconcileAcceptedRun(ctx, intent.IntentID)
	if !errors.Is(err, ErrRemoteObservationTimeout) || !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("error=%v parent=%v, want only the observation deadline", err, ctx.Err())
	}
	stored, readErr := authority.GetLocalIntent(ctx, intent.IntentID)
	if readErr != nil || stored.DeliveryState != store.LocalIntentAccepted || stored.AttemptCount != 1 ||
		stored.RemoteStatusFailureAt == nil || stored.RemoteStatusFailureAttempts != 1 || store.HasRemoteTerminalProof(stored) {
		t.Fatalf("timeout fabricated delivery/proof or lost its durable diagnostic: %+v err=%v", stored, readErr)
	}
	issues := RemoteReconciliationIssues(err)
	if len(issues) != 1 || issues[0].FailureClass != store.RemoteStatusFailureCodeUnavailable {
		t.Fatalf("unsafe/incorrect timeout diagnostic: %+v", issues)
	}
	if _, err := authority.GetRemoteJobProjection(ctx, intent.JobID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("timeout created a job result: %v", err)
	}
}

func TestBUG029ParentCancellationDoesNotStartStatusFailureWindow(t *testing.T) {
	authority := p068Authority(t)
	intent := bug029AcceptIntent(t, authority, "parent-cancel", false)
	started := make(chan struct{})
	caller := bug029Caller{call: func(ctx context.Context, _ sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		close(started)
		<-ctx.Done()
		return sshbridge.ReplyFrame{}, ctx.Err()
	}}
	driver := bug029Driver(t, authority, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- driver.ReconcileAcceptedRun(ctx, intent.IntentID) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("observer never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRemoteObservationTimeout) {
			t.Fatalf("service cancellation misclassified as target failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled observer did not return")
	}
	stored, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || stored.RemoteStatusFailureAt != nil || stored.DeliveryState != store.LocalIntentAccepted || stored.AttemptCount != 1 {
		t.Fatalf("parent cancellation changed execution semantics: %+v err=%v", stored, err)
	}
}

func TestBUG029BatchDeadlinePreservesUnattemptedTail(t *testing.T) {
	for _, submit := range []bool{false, true} {
		name := "runs"
		if submit {
			name = "submits"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			authority := p068Authority(t)
			bug029AcceptIntent(t, authority, "page-first", submit)
			bug029AcceptIntent(t, authority, "page-later", submit)
			var ordered []store.LocalIntentRecord
			var err error
			if submit {
				ordered, err = authority.ListAcceptedRemoteSubmitIntents(ctx, 64)
			} else {
				ordered, err = authority.ListAcceptedRemoteRunIntents(ctx, 64)
			}
			if err != nil || len(ordered) != 2 {
				t.Fatalf("accepted page=%+v err=%v", ordered, err)
			}
			first, later := ordered[0], ordered[1]
			inner := newP149PagedRunCaller(ordered, later)
			blockedCalls := 0
			var attemptedResources []string
			caller := bug029Caller{
				call: func(readCtx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
					var payload map[string]string
					if err := json.Unmarshal(frame.Payload, &payload); err != nil {
						return sshbridge.ReplyFrame{}, err
					}
					resource := payload["job_id"]
					if resource == "" {
						resource = payload["command_id"]
					}
					attemptedResources = append(attemptedResources, resource)
					if payload["job_id"] == string(first.JobID) && first.JobID != "" || payload["command_id"] == string(first.CommandID) {
						blockedCalls++
						<-readCtx.Done()
						return sshbridge.ReplyFrame{}, readCtx.Err()
					}
					return inner.Call(readCtx, frame)
				},
				stream: inner.Stream,
			}
			// The healthy tail includes real SQLite projection/event/proof writes.
			// Use the shipped budgets rather than requiring that work to finish
			// inside the 100ms used by the single-stalled-read fixtures.
			driver, err := NewRemoteDriver(authority, caller, "router-bug029-batch", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			reconcile := driver.ReconcileAcceptedRemoteRuns
			if submit {
				reconcile = driver.ReconcileAcceptedRemoteSubmits
			}
			if err := reconcile(ctx, 64); !errors.Is(err, ErrRemoteObservationTimeout) || ctx.Err() != nil {
				t.Fatalf("first batch error=%v observation_timeout=%t parent=%v issues=%+v", err, errors.Is(err, ErrRemoteObservationTimeout), ctx.Err(), RemoteReconciliationIssues(err))
			}
			firstResource, laterResource := string(first.JobID), string(later.JobID)
			cursor := driver.runCursor
			if submit {
				firstResource, laterResource = string(first.CommandID), string(later.CommandID)
				cursor = driver.submitCursor
			}
			if len(attemptedResources) != 1 || attemptedResources[0] != firstResource || cursor == nil || cursor.IntentID != first.IntentID {
				t.Fatalf("expired batch advanced beyond attempted prefix: resources=%v cursor=%+v", attemptedResources, cursor)
			}
			tailErr := reconcile(ctx, 64)
			if len(attemptedResources) < 2 || attemptedResources[1] != laterResource {
				t.Fatalf("next batch did not start at the unattempted tail: resources=%v err=%v", attemptedResources, tailErr)
			}
			if tailErr != nil {
				t.Fatalf("attempted tail did not reconcile: %v observation_timeout=%t parent=%v jobs=%v commands=%v streams=%d issues=%+v", tailErr, errors.Is(tailErr, ErrRemoteObservationTimeout), ctx.Err(), inner.getJobCalls, inner.getCommandCalls, inner.streamCalls, RemoteReconciliationIssues(tailErr))
			}
			cursor = driver.runCursor
			if submit {
				cursor = driver.submitCursor
			}
			if cursor == nil || cursor.IntentID != later.IntentID {
				t.Fatalf("successful tail did not retain its attempted cursor: %+v", cursor)
			}
			if blockedCalls != 1 || inner.mutationCalls != 0 {
				t.Fatalf("retried the blocked prefix or replayed work: blocked=%d mutations=%d", blockedCalls, inner.mutationCalls)
			}
			got, err := authority.GetLocalIntent(ctx, later.IntentID)
			if err != nil || !store.HasRemoteTerminalProof(got) || got.AttemptCount != 1 {
				t.Fatalf("later result was starved or replayed: %+v err=%v", got, err)
			}
		})
	}
}

func TestBUG029SubmitEventTimeoutKeepsProjectionStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	authority := p068Authority(t)
	intent := bug029AcceptIntent(t, authority, "submit-stream", true)
	inner := &p149RecoveryCaller{intent: intent}
	caller := bug029Caller{call: inner.Call, stream: func(readCtx context.Context, _ sshbridge.RequestFrame, _ func(sshbridge.ReplyFrame) error) error {
		<-readCtx.Done()
		return readCtx.Err()
	}}
	driver := bug029Driver(t, authority, caller)
	err := driver.ReconcileAcceptedRemoteSubmits(ctx, 1)
	if !errors.Is(err, ErrRemoteObservationTimeout) || ctx.Err() != nil {
		t.Fatalf("event timeout error=%v parent=%v", err, ctx.Err())
	}
	issues := RemoteReconciliationIssues(err)
	if len(issues) != 1 || issues[0].FailureClass != store.RemoteStatusFailureCodeUnavailable {
		t.Fatalf("event timeout lost safe diagnostic class: %+v", issues)
	}
	projection, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID)
	if err != nil || !projection.IsStale {
		t.Fatalf("failed stream remained a fresh result: %+v err=%v", projection, err)
	}
	stored, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || stored.DeliveryState != store.LocalIntentAccepted || stored.AttemptCount != 1 || store.HasRemoteTerminalProof(stored) {
		t.Fatalf("stream timeout settled or replayed submit: %+v err=%v", stored, err)
	}
}

func TestBUG029ObservationBudgetNeverLeaksIntoMutation(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "dispatch"
		if retry {
			name = "existing-explicit-retry-path"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			authority := p068Authority(t)
			create := p149RemoteRunIntent(t, "no-mutation-deadline")
			if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
				t.Fatal(err)
			}
			intent, err := authority.GetLocalIntent(ctx, create.IntentID)
			if err != nil {
				t.Fatal(err)
			}
			inner := &p149RecoveryCaller{intent: intent}
			reads, mutations := 0, 0
			caller := bug029Caller{call: func(callCtx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
				if frame.Operation == sshbridge.OperationGetJob {
					reads++
					if _, ok := callCtx.Deadline(); !ok {
						t.Fatal("status read has no finite deadline")
					}
					payload, _ := json.Marshal(sshbridge.ErrorPayload{Code: "resource_not_found"})
					return p149Reply(frame.RequestID, "error", payload), nil
				}
				mutations++
				if frame.Operation != sshbridge.OperationRunOrResumeJob || callCtx.Err() != nil {
					t.Fatalf("unexpected or canceled mutation context: %+v err=%v", frame, callCtx.Err())
				}
				if _, ok := callCtx.Deadline(); ok {
					t.Fatal("read-only deadline leaked into execution mutation")
				}
				return inner.Call(callCtx, frame)
			}}
			driver := bug029Driver(t, authority, caller)
			if retry {
				if _, err := authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentDispatching, "fixture-dispatch"); err != nil {
					t.Fatal(err)
				}
				if _, err := authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentUncertain, "fixture-uncertain"); err != nil {
					t.Fatal(err)
				}
				_, _, err = driver.ReconcileIntent(ctx, intent.IntentID)
			} else {
				_, _, err = driver.DispatchIntent(ctx, intent.IntentID)
			}
			if err != nil || mutations != 1 || reads != map[bool]int{false: 0, true: 1}[retry] {
				t.Fatalf("mutation boundary changed: reads=%d mutations=%d err=%v", reads, mutations, err)
			}
		})
	}
}

type bug029Caller struct {
	call   func(context.Context, sshbridge.RequestFrame) (sshbridge.ReplyFrame, error)
	stream func(context.Context, sshbridge.RequestFrame, func(sshbridge.ReplyFrame) error) error
}

func (c bug029Caller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	return c.call(ctx, frame)
}

func (c bug029Caller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	if c.stream == nil {
		return errors.New("unexpected event read")
	}
	return c.stream(ctx, frame, receive)
}

func bug029Driver(t *testing.T, authority *store.AuthorityStore, caller RemoteCaller) *RemoteDriver {
	t.Helper()
	driver, err := NewRemoteDriverWithClock(authority, caller, "router-bug029", time.Minute, time.Now,
		RemoteObservationOptions{Timeout: 100 * time.Millisecond, BatchTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func bug029AcceptIntent(t *testing.T, authority *store.AuthorityStore, suffix string, submit bool) store.LocalIntentRecord {
	t.Helper()
	create := p149RemoteRunIntent(t, "bug029-"+suffix)
	if submit {
		create = p068SubmitIntent(t, "intent-bug029-"+suffix, "session-bug029-"+suffix, "command-bug029-"+suffix, domain.TargetKindRemote, "printf bug029-submit")
	}
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.GetLocalIntent(context.Background(), create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewRemoteDriver(authority, &p149RecoveryCaller{intent: intent}, "router-bug029-seed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := driver.DispatchIntent(context.Background(), intent.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted || accepted.AttemptCount != 1 {
		t.Fatalf("seed accepted intent=%+v err=%v", accepted, err)
	}
	return accepted
}
