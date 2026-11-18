package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

// Individually healthy GETs must not be starved forever because strict proof
// requires job -> command -> events -> job and their combined latency exceeds
// one reconciliation batch. The fixtures contain only synthetic P149 output.
func TestBUG034AcceptedRemoteRunSettlesWhenEachReadFitsBudget(t *testing.T) {
	const (
		readDelay  = 70 * time.Millisecond
		readLimit  = 250 * time.Millisecond
		batchLimit = 110 * time.Millisecond
		maxPasses  = 8
	)
	for _, state := range []domain.CommandState{domain.CommandStateSucceeded, domain.CommandStateLost} {
		t.Run(string(state), func(t *testing.T) {
			authority := p068Authority(t)
			intent := bug034AcceptedRun(t, authority, string(state))
			newCaller := func() *bug034PacedCaller {
				inner := &p149RecoveryCaller{intent: intent, commandState: state}
				inner.mutateJob = func(fields map[string]any) {
					bug034LegacyReadContext(fields)
					if state == domain.CommandStateLost {
						fields["job_phase"] = string(store.JobPhaseLost)
						fields["command_state"] = string(state)
						fields["teardown_state"] = string(store.JobTeardownLost)
						fields["teardown_reason"] = "runtime_cleanup_unconfirmed"
						fields["output_complete"] = false
						fields["output_unavailable_reason"] = "capture_boundary_unconfirmed"
						delete(fields, "exit_code")
					}
				}
				inner.mutateCommand = func(fields map[string]any) {
					bug034LegacyReadContext(fields)
					if state == domain.CommandStateLost {
						fields["output_complete"] = false
						fields["output_unavailable_reason"] = "capture_boundary_unconfirmed"
					}
				}
				return &bug034PacedCaller{inner: inner, delay: readDelay}
			}

			// Establish that all four exchanges succeed within their own read
			// limit, including complete, strictly matching terminal payloads.
			control := newCaller()
			for _, operation := range []sshbridge.Operation{sshbridge.OperationGetJob, sshbridge.OperationGetCommand, sshbridge.OperationStreamCommandEvents, sshbridge.OperationGetJob} {
				ctx, cancel := context.WithTimeout(context.Background(), readLimit)
				frame := sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: "bug034-control", Operation: operation}
				var err error
				if operation == sshbridge.OperationStreamCommandEvents {
					frame.Payload, _ = json.Marshal(map[string]any{"command_id": intent.CommandID, "after_sequence": 0})
					err = control.Stream(ctx, frame, func(sshbridge.ReplyFrame) error { return nil })
				} else {
					var reply sshbridge.ReplyFrame
					reply, err = control.Call(ctx, frame)
					if err == nil {
						var object map[string]json.RawMessage
						err = json.Unmarshal(reply.Payload, &object)
						if err == nil && operation == sshbridge.OperationGetJob {
							_, err = strictRemoteJobProjectionFromReadReply(intent, object, time.Now())
						} else if err == nil {
							_, err = strictRemoteCommandProjectionFromReadReply(intent, object, time.Now())
						}
					}
				}
				cancel()
				if err != nil {
					t.Fatalf("independent healthy %s failed its own read limit: %v", operation, err)
				}
			}

			caller := newCaller()
			driver, err := NewRemoteDriverWithClock(authority, caller, "router-bug034", time.Minute, time.Now,
				RemoteObservationOptions{Timeout: readLimit, BatchTimeout: batchLimit})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var current store.LocalIntentRecord
			var lastErr error
			for pass := 1; pass <= maxPasses; pass++ {
				lastErr = driver.ReconcileAcceptedRemoteRuns(ctx, 1)
				if lastErr != nil && !errors.Is(lastErr, ErrRemoteObservationTimeout) {
					t.Fatalf("pass %d rejected valid terminal evidence: %v", pass, lastErr)
				}
				current, err = authority.GetLocalIntent(ctx, intent.IntentID)
				if err != nil {
					t.Fatal(err)
				}
				if store.HasRemoteTerminalProof(current) {
					break
				}
			}
			if caller.mutations != 0 || current.AttemptCount != intent.AttemptCount {
				t.Fatalf("observation replayed a mutation: sent=%d attempts=%d -> %d", caller.mutations, intent.AttemptCount, current.AttemptCount)
			}
			cursor, err := authority.GetRemoteEventCursor(ctx, intent.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			if !store.HasRemoteTerminalProof(current) {
				t.Fatalf("%d bounded passes starved healthy terminal reads: delivery=%s proof=%d cursor=%d completed_calls=%d completed_streams=%d cancelled_reads=%d last_error=%v", maxPasses, current.DeliveryState, current.RemoteTerminalProofVersion, cursor, caller.inner.getJobCalls+caller.inner.getCommandCalls, caller.inner.streamCalls, caller.cancelled, lastErr)
			}
			command, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID)
			if err != nil || command.State != state || cursor != 4 || command.FinalEventSequence == nil || *command.FinalEventSequence != cursor {
				t.Fatalf("terminal proof lost exact command/event outcome: command=%+v cursor=%d err=%v", command, cursor, err)
			}
			p149AssertReadFrames(t, caller.inner.frames, caller.inner.streamFrames)
		})
	}
}

func bug034AcceptedRun(t *testing.T, authority *store.AuthorityStore, suffix string) store.LocalIntentRecord {
	t.Helper()
	create := p149RemoteRunIntent(t, "bug034-"+suffix)
	var payload map[string]any
	if err := json.Unmarshal(create.PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	payload["environment"] = "sandbox-dev"
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	create.PayloadJSON, err = domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	create.RequestHash, err = domain.HashMutationRequestJSON("run", create.PayloadJSON, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	create.Environment = "sandbox-dev"
	ctx := context.Background()
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	seed := &p149RecoveryCaller{intent: stored}
	driver, err := NewRemoteDriver(authority, seed, "router-bug034-seed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := driver.DispatchIntent(ctx, stored.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted || seed.runCalls != 1 {
		t.Fatalf("synthetic initial acceptance failed: intent=%+v err=%v", accepted, err)
	}
	return accepted
}

func bug034LegacyReadContext(fields map[string]any) {
	fields["source"] = map[string]any{"mode": "empty", "portable": true}
	fields["capabilities"] = map[string]any{
		"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu",
		"service_limits": map[string]any{"active_sessions": 20, "running_commands": 4, "command_timeout_seconds": 1800, "session_max_lifetime_seconds": 14400},
	}
}

type bug034PacedCaller struct {
	inner                *p149RecoveryCaller
	delay                time.Duration
	mutations, cancelled int
}

func (c *bug034PacedCaller) wait(ctx context.Context) error {
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		c.cancelled++
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *bug034PacedCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if (frame.Operation != sshbridge.OperationGetJob && frame.Operation != sshbridge.OperationGetCommand) || frame.ResourceID != "" || frame.IdempotencyKey != "" {
		c.mutations++
		return sshbridge.ReplyFrame{}, errors.New("BUG034 observation attempted a mutation")
	}
	if err := c.wait(ctx); err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	return c.inner.Call(ctx, frame)
}

func (c *bug034PacedCaller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	if frame.Operation != sshbridge.OperationStreamCommandEvents || frame.ResourceID != "" || frame.IdempotencyKey != "" {
		c.mutations++
		return errors.New("BUG034 observation attempted a mutation stream")
	}
	if err := c.wait(ctx); err != nil {
		return err
	}
	return c.inner.Stream(ctx, frame, receive)
}

// The longest existing proof path includes the independent terminal read
// after a history gap. Runs also normalize the command and re-read the job.
// Every exchange fits its own deadline even though five/three allowances
// would truncate these six/four-exchange paths.
func TestBUG034AcceptedGapProofFinishesItsBoundedReadSequence(t *testing.T) {
	for _, submit := range []bool{false, true} {
		name := "run"
		if submit {
			name = "submit"
		}
		t.Run(name, func(t *testing.T) {
			authority := p068Authority(t)
			intent := bug029AcceptIntent(t, authority, "bug034-gap-"+name, submit)
			// Keep enough wall-clock headroom for real SQLite work on the
			// native workers while still exceeding the old five/three limits.
			caller := &bug034PacedCaller{inner: &p149RecoveryCaller{intent: intent, gap: true}, delay: 875 * time.Millisecond}
			driver, err := NewRemoteDriverWithClock(authority, caller, "router-bug034-gap", time.Minute, time.Now,
				RemoteObservationOptions{Timeout: time.Second, BatchTimeout: 110 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			if submit {
				err = driver.ReconcileAcceptedRemoteSubmits(ctx, 1)
			} else {
				err = driver.ReconcileAcceptedRemoteRuns(ctx, 1)
			}
			if err != nil {
				t.Fatalf("healthy %s gap proof was cut short: %v", name, err)
			}
			current, err := authority.GetLocalIntent(ctx, intent.IntentID)
			if err != nil || !store.HasRemoteTerminalProof(current) || current.AttemptCount != intent.AttemptCount || caller.mutations != 0 {
				t.Fatalf("gap proof or no-replay boundary failed: intent=%+v mutations=%d err=%v", current, caller.mutations, err)
			}
			gap, err := authority.GetRemoteEventGap(ctx, intent.CommandID)
			if err != nil || gap.AvailableSequence != 1 || gap.FinalSequence != 4 || gap.MissingFrom != 2 || gap.MissingTo != 4 {
				t.Fatalf("gap proof invented a cursor: gap=%+v err=%v", gap, err)
			}
			command, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID)
			if err != nil || command.OutputComplete || command.OutputUnavailableReason != "remote_event_gap" {
				t.Fatalf("gap lost incomplete-output qualification: command=%+v err=%v", command, err)
			}
			p149AssertReadFrames(t, caller.inner.frames, caller.inner.streamFrames)
		})
	}
}

// Giving a started proof enough time must not weaken terminal evidence or
// allow a final job read to disagree with the command/event snapshot.
func TestBUG034LongerProofStillRejectsMissingOrChangedTerminalEvidence(t *testing.T) {
	for _, missingEvent := range []bool{true, false} {
		name, stage, category := "final_job_changed", "final_job", "invalid_response"
		if missingEvent {
			name, stage, category = "terminal_event_missing", "events", "event_cursor_invalid"
		}
		t.Run(name, func(t *testing.T) {
			authority := p068Authority(t)
			intent := bug034AcceptedRun(t, authority, name)
			inner := &p149RecoveryCaller{intent: intent}
			paced := &bug034PacedCaller{inner: inner, delay: 15 * time.Millisecond}
			caller := bug029Caller{call: paced.Call, stream: paced.Stream}
			if missingEvent {
				caller.stream = func(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
					return paced.Stream(ctx, frame, func(reply sshbridge.ReplyFrame) error {
						var event struct {
							Type string `json:"type"`
						}
						if reply.ResponseType == "event" && json.Unmarshal(reply.Payload, &event) == nil && event.Type == "command_succeeded" {
							return nil
						}
						return receive(reply)
					})
				}
			} else {
				inner.mutateJob = func(fields map[string]any) {
					if inner.getJobCalls >= 2 {
						fields["command_state"], fields["exit_code"] = string(domain.CommandStateFailed), 2
					}
				}
			}
			driver, err := NewRemoteDriverWithClock(authority, caller, "router-bug034-refusal", time.Minute, time.Now,
				RemoteObservationOptions{Timeout: 150 * time.Millisecond, BatchTimeout: 5 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			err = driver.ReconcileAcceptedRemoteRuns(context.Background(), 1)
			issues := RemoteReconciliationIssues(err)
			if err == nil || len(issues) != 1 || issues[0].Stage != stage || issues[0].ObservationCategory != category {
				t.Fatalf("invalid proof lost its safe failure stage: error=%v issues=%+v", err, issues)
			}
			current, readErr := authority.GetLocalIntent(context.Background(), intent.IntentID)
			if readErr != nil || current.DeliveryState != store.LocalIntentAccepted || store.HasRemoteTerminalProof(current) || current.AttemptCount != intent.AttemptCount || paced.mutations != 0 {
				t.Fatalf("invalid evidence changed original acceptance: intent=%+v mutations=%d err=%v", current, paced.mutations, readErr)
			}
		})
	}
}

func TestBUG034ParentCancellationDuringLaterReadDoesNotBecomeTargetFailure(t *testing.T) {
	authority := p068Authority(t)
	intent := bug034AcceptedRun(t, authority, "parent-cancel-later-read")
	inner := &p149RecoveryCaller{intent: intent}
	started := make(chan struct{})
	caller := bug029Caller{call: func(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		if frame.Operation == sshbridge.OperationGetCommand {
			close(started)
			<-ctx.Done()
			return sshbridge.ReplyFrame{}, ctx.Err()
		}
		return inner.Call(ctx, frame)
	}, stream: inner.Stream}
	driver, err := NewRemoteDriver(authority, caller, "router-bug034-cancel", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- driver.ReconcileAcceptedRun(ctx, intent.IntentID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("proof did not reach its later command read")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRemoteObservationTimeout) {
			t.Fatalf("parent cancellation was misclassified: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent cancellation did not bound the admitted proof")
	}
	current, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || current.DeliveryState != store.LocalIntentAccepted || current.RemoteStatusFailureAt != nil || store.HasRemoteTerminalProof(current) || current.AttemptCount != intent.AttemptCount {
		t.Fatalf("parent cancellation invented a target failure or proof: intent=%+v err=%v", current, err)
	}
}

func TestBUG034GapConfirmationPreservesTimeoutCategoryAndPrivateCause(t *testing.T) {
	authority := p068Authority(t)
	intent := bug034AcceptedRun(t, authority, "gap-timeout")
	inner := &p149RecoveryCaller{intent: intent, gap: true}
	private := errors.New("BUG034_PRIVATE_TRANSPORT_DETAIL")
	transport := &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: private}
	caller := bug029Caller{call: func(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
		if frame.Operation == sshbridge.OperationGetCommand && inner.getCommandCalls == 1 {
			return sshbridge.ReplyFrame{}, errors.Join(ErrRemoteObservationTimeout, context.DeadlineExceeded, transport)
		}
		return inner.Call(ctx, frame)
	}, stream: inner.Stream}
	driver, err := NewRemoteDriver(authority, caller, "router-bug034-gap-timeout", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = driver.ReconcileAcceptedRun(context.Background(), intent.IntentID)
	var retained *sshclient.TransportError
	issues := RemoteReconciliationIssues(err)
	if !errors.Is(err, ErrRemoteObservationTimeout) || !errors.Is(err, private) || !errors.As(err, &retained) || retained != transport ||
		len(issues) != 1 || issues[0].Stage != "events" || issues[0].ObservationCategory != "timeout" || strings.Contains(err.Error(), private.Error()) {
		t.Fatalf("nested gap failure lost its safe category or private cause: error=%v issues=%+v retained=%t", err, issues, retained == transport)
	}
	current, readErr := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if readErr != nil || current.DeliveryState != store.LocalIntentAccepted || store.HasRemoteTerminalProof(current) || current.AttemptCount != intent.AttemptCount {
		t.Fatalf("timed out gap confirmation settled or replayed intent: intent=%+v err=%v", current, readErr)
	}
}
