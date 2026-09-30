package dispatcher

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

// TestP149AcceptedRemoteRunRecoversAfterRouterRestartWithoutResubmission
// models the Mac restart failure: the target accepted a one-off run but the
// first reply was only an identity envelope. Recovery must use only target
// reads and one event stream, never send the script again.
func TestP149AcceptedRemoteRunRecoversAfterRouterRestartWithoutResubmission(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p149RemoteRunIntent(t, "restart")
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	caller := &p149RecoveryCaller{intent: stored}
	driver, err := NewRemoteDriver(authority, caller, "router-p149", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := driver.DispatchIntent(ctx, create.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("sparse accepted run=%+v err=%v", accepted, err)
	}
	if _, err := authority.GetRemoteJobProjection(ctx, create.JobID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("sparse acceptance created a job projection: %v", err)
	}

	if err := driver.ReconcileAcceptedRemoteRuns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	reconciled, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil || reconciled.DeliveryState != store.LocalIntentReconciled || !store.HasRemoteTerminalProof(reconciled) {
		t.Fatalf("recovered intent=%+v err=%v", reconciled, err)
	}
	job, err := authority.GetRemoteJobProjection(ctx, create.JobID)
	if err != nil || job.Phase != store.JobPhaseComplete || job.CommandState == nil || *job.CommandState != domain.CommandStateSucceeded || !job.OutputComplete {
		t.Fatalf("recovered job=%+v err=%v", job, err)
	}
	command, err := authority.GetRemoteCommandProjection(ctx, create.CommandID)
	if err != nil || command.State != domain.CommandStateSucceeded || command.FinalEventSequence == nil || *command.FinalEventSequence != 4 || !command.OutputComplete {
		t.Fatalf("recovered command=%+v err=%v", command, err)
	}
	cursor, err := authority.GetRemoteEventCursor(ctx, create.CommandID)
	if err != nil || cursor != 4 {
		t.Fatalf("recovered remote event cursor=%d err=%v", cursor, err)
	}
	if caller.runCalls != 1 || caller.getJobCalls != 2 || caller.getCommandCalls != 1 || caller.streamCalls != 1 {
		t.Fatalf("recovery calls run=%d get_job=%d get_command=%d stream=%d", caller.runCalls, caller.getJobCalls, caller.getCommandCalls, caller.streamCalls)
	}
	p149AssertReadFrames(t, caller.frames, caller.streamFrames)
}

// TestP149RemoteRunMutationNeverPersistsItsStatusReply keeps the mutation
// boundary explicit even when a fast target happens to return a complete job
// object. Only strict GETs and the durable command event boundary may create
// the Mac-side job/command projections.
func TestP149RemoteRunMutationNeverPersistsItsStatusReply(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p149RemoteRunIntent(t, "full-mutation-reply")
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	caller := &p149RecoveryCaller{intent: stored, fullRunMutationReply: true}
	driver, err := NewRemoteDriver(authority, caller, "router-p149-full-mutation", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := driver.DispatchIntent(ctx, create.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("full mutation acceptance=%+v err=%v", accepted, err)
	}
	if _, err := authority.GetRemoteJobProjection(ctx, create.JobID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("run mutation created a job projection: %v", err)
	}
	if _, err := authority.GetRemoteCommandProjection(ctx, create.CommandID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("run mutation created a command projection: %v", err)
	}
}

// TestP149RemoteRunRecoveryRotatesPastBlockedPrefix prevents a bounded
// recovery scan from repeatedly revisiting the same older active work while a
// later terminal run waits indefinitely for its strict read. The second page
// must reach item 65 without emitting any mutation frame.
func TestP149RemoteRunRecoveryRotatesPastBlockedPrefix(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	for index := 0; index < 65; index++ {
		p149AcceptRemoteRun(t, authority, fmt.Sprintf("page-%03d", index))
	}
	ordered, err := authority.ListAcceptedRemoteRunIntents(ctx, 1000)
	if err != nil || len(ordered) != 65 {
		t.Fatalf("ordered accepted runs=%d err=%v", len(ordered), err)
	}
	terminal := ordered[len(ordered)-1]
	caller := newP149PagedRunCaller(ordered, terminal)
	driver, err := NewRemoteDriver(authority, caller, "router-p149-pages", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileAcceptedRemoteRuns(ctx, 64); err != nil {
		t.Fatal(err)
	}
	before, err := authority.GetLocalIntent(ctx, terminal.IntentID)
	if err != nil || before.DeliveryState != store.LocalIntentAccepted || before.RemoteTerminalProofVersion != 0 {
		t.Fatalf("terminal run settled in first page=%+v err=%v", before, err)
	}
	if err := driver.ReconcileAcceptedRemoteRuns(ctx, 64); err != nil {
		t.Fatal(err)
	}
	after, err := authority.GetLocalIntent(ctx, terminal.IntentID)
	if err != nil || !store.HasRemoteTerminalProof(after) {
		t.Fatalf("terminal run was starved behind first page=%+v err=%v", after, err)
	}
	if caller.mutationCalls != 0 || caller.getJobCalls[string(terminal.JobID)] < 2 || caller.getCommandCalls[string(terminal.CommandID)] != 1 || caller.streamCalls != 1 {
		t.Fatalf("paged recovery calls mutations=%d get_job=%d get_command=%d stream=%d", caller.mutationCalls, caller.getJobCalls[string(terminal.JobID)], caller.getCommandCalls[string(terminal.CommandID)], caller.streamCalls)
	}
}

// TestP149AcceptedRemoteSubmitRecoversOnlyFromStrictReads proves that a
// session command accepted before a Router restart never trusts a mutation
// reply as status and never sends its script a second time. Active output may
// be mirrored while the intent remains accepted; terminal settlement waits for
// a strict target read and matching final event.
func TestP149AcceptedRemoteSubmitRecoversOnlyFromStrictReads(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p068SubmitIntent(t, "intent-p149-submit-restart", "session-p149-submit-restart", "command-p149-submit-restart", domain.TargetKindRemote, "printf p149-submit")
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	caller := &p149RecoveryCaller{intent: stored, commandState: domain.CommandStateRunning}
	driver, err := NewRemoteDriver(authority, caller, "router-p149-submit", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := driver.DispatchIntent(ctx, create.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("identity-only accepted submit=%+v err=%v", accepted, err)
	}
	if _, err := authority.GetRemoteCommandProjection(ctx, create.CommandID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
		t.Fatalf("submit mutation created a command projection: %v", err)
	}

	if err := driver.ReconcileAcceptedRemoteSubmits(ctx, 1); err != nil {
		t.Fatal(err)
	}
	active, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil || active.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("active submit settled prematurely: %+v err=%v", active, err)
	}
	projection, err := authority.GetRemoteCommandProjection(ctx, create.CommandID)
	if err != nil || projection.State != domain.CommandStateRunning || projection.FinalEventSequence != nil {
		t.Fatalf("active strict projection=%+v err=%v", projection, err)
	}
	if cursor, err := authority.GetRemoteEventCursor(ctx, create.CommandID); err != nil || cursor != 3 {
		t.Fatalf("active cursor=%d err=%v", cursor, err)
	}

	caller.commandState = domain.CommandStateSucceeded
	if err := driver.ReconcileAcceptedRemoteSubmits(ctx, 1); err != nil {
		t.Fatal(err)
	}
	reconciled, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil || reconciled.DeliveryState != store.LocalIntentReconciled || !store.HasRemoteTerminalProof(reconciled) {
		t.Fatalf("terminal submit did not reconcile: %+v err=%v", reconciled, err)
	}
	projection, err = authority.GetRemoteCommandProjection(ctx, create.CommandID)
	if err != nil || projection.State != domain.CommandStateSucceeded || projection.FinalEventSequence == nil || *projection.FinalEventSequence != 4 || !projection.OutputComplete {
		t.Fatalf("terminal strict projection=%+v err=%v", projection, err)
	}
	if caller.submitCalls != 1 || caller.runCalls != 0 || caller.getCommandCalls != 4 || caller.streamCalls != 2 {
		t.Fatalf("submit recovery calls submit=%d run=%d get_command=%d stream=%d", caller.submitCalls, caller.runCalls, caller.getCommandCalls, caller.streamCalls)
	}
	p149AssertReadFrames(t, caller.frames, caller.streamFrames)
}

// TestP149AcceptedRemoteRunRecoversConfirmedEventGap verifies that a one-off
// run owns the strict confirmation context for an unavailable event suffix.
func TestP149AcceptedRemoteRunRecoversConfirmedEventGapWithoutResubmission(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	intent := p149AcceptRemoteRun(t, authority, "gap")
	caller := &p149RecoveryCaller{intent: intent, gap: true}
	driver, err := NewRemoteDriver(authority, caller, "router-p149-gap", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileAcceptedRun(ctx, intent.IntentID); err != nil {
		t.Fatal(err)
	}
	reconciled, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || reconciled.DeliveryState != store.LocalIntentReconciled || !store.HasRemoteTerminalProof(reconciled) {
		t.Fatalf("gap-reconciled intent=%+v err=%v", reconciled, err)
	}
	gap, err := authority.GetRemoteEventGap(ctx, intent.CommandID)
	if err != nil || gap.AvailableSequence != 1 || gap.MissingFrom != 2 || gap.MissingTo != 4 || gap.FinalSequence != 4 || gap.TerminalState != domain.CommandStateSucceeded {
		t.Fatalf("gap=%+v err=%v", gap, err)
	}
	command, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID)
	if err != nil || command.OutputComplete || command.OutputUnavailableReason != "remote_event_gap" {
		t.Fatalf("gap-normalized command=%+v err=%v", command, err)
	}
	job, err := authority.GetRemoteJobProjection(ctx, intent.JobID)
	if err != nil || job.OutputComplete || job.OutputUnavailableReason != "remote_event_gap" {
		t.Fatalf("gap-normalized job=%+v err=%v", job, err)
	}
	if caller.runCalls != 0 || caller.getJobCalls != 2 || caller.getCommandCalls != 3 || caller.streamCalls != 1 {
		t.Fatalf("gap recovery calls run=%d get_job=%d get_command=%d stream=%d", caller.runCalls, caller.getJobCalls, caller.getCommandCalls, caller.streamCalls)
	}
	p149AssertReadFrames(t, caller.frames, caller.streamFrames)
}

// TestP149AcceptedRemoteRunRejectsRetentionExpiredContradictoryPrefix proves
// that target-side output expiry cannot erase event evidence already mirrored
// by the Mac. The strict target status may settle an empty or unfinished
// retained prefix, but it must never contradict a terminal state, final
// boundary, or truncation marker in that prefix.
func TestP149AcceptedRemoteRunRejectsRetentionExpiredContradictoryPrefix(t *testing.T) {
	for _, test := range []struct {
		name   string
		events func(domain.CommandID, time.Time) []store.RemoteEventRecord
	}{
		{
			name: "terminal-state-mismatch",
			events: func(commandID domain.CommandID, when time.Time) []store.RemoteEventRecord {
				return []store.RemoteEventRecord{
					{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
					{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
					{CommandID: commandID, Sequence: 3, Type: "stdout", Payload: []byte("prefix"), ByteCount: 6, OccurredAt: when.Add(2 * time.Second)},
					{CommandID: commandID, Sequence: 4, Type: "command_failed", OccurredAt: when.Add(3 * time.Second)},
				}
			},
		},
		{
			name: "unfinished-prefix-reaches-final-boundary",
			events: func(commandID domain.CommandID, when time.Time) []store.RemoteEventRecord {
				return []store.RemoteEventRecord{
					{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
					{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
					{CommandID: commandID, Sequence: 3, Type: "stdout", Payload: []byte("first"), ByteCount: 5, OccurredAt: when.Add(2 * time.Second)},
					{CommandID: commandID, Sequence: 4, Type: "stdout", Payload: []byte("last"), ByteCount: 4, OccurredAt: when.Add(3 * time.Second)},
				}
			},
		},
		{
			name: "retained-truncation-lost-by-target-status",
			events: func(commandID domain.CommandID, when time.Time) []store.RemoteEventRecord {
				return []store.RemoteEventRecord{
					{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
					{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
					{CommandID: commandID, Sequence: 3, Type: "output_truncated", OccurredAt: when.Add(2 * time.Second)},
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			authority := p068Authority(t)
			intent := p149AcceptRemoteRun(t, authority, "retention-"+test.name)
			when := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			if _, err := authority.MirrorRemoteEvents(ctx, test.events(intent.CommandID, when)); err != nil {
				t.Fatal(err)
			}
			retentionExpired := func(fields map[string]any) {
				fields["output_complete"] = false
				fields["output_unavailable_reason"] = "retention_expired"
			}
			caller := &p149RecoveryCaller{intent: intent, mutateJob: retentionExpired, mutateCommand: retentionExpired}
			driver, err := NewRemoteDriver(authority, caller, "router-p149-retention", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := driver.ReconcileAcceptedRun(ctx, intent.IntentID); !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("reconciliation error=%v, want strict retention contradiction failure", err)
			}
			current, err := authority.GetLocalIntent(ctx, intent.IntentID)
			if err != nil || current.DeliveryState != store.LocalIntentAccepted {
				t.Fatalf("contradictory retention proof settled intent=%+v err=%v", current, err)
			}
			if caller.runCalls != 0 || caller.streamCalls != 0 {
				t.Fatalf("retention recovery resent work: run=%d stream=%d", caller.runCalls, caller.streamCalls)
			}
		})
	}
}

// TestP149StrictProjectionRejectsTerminalBeforeRequiredEventBoundary protects
// recovery when no event rows survive on either host. A strict terminal GET
// still has to be compatible with the immutable v1 lifecycle: queued first,
// then started for states that require it, then the terminal event.
func TestP149StrictProjectionRejectsTerminalBeforeRequiredEventBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		state domain.CommandState
		final int64
	}{
		{name: "succeeded-before-start", state: domain.CommandStateSucceeded, final: 2},
		{name: "failed-before-start", state: domain.CommandStateFailed, final: 2},
		{name: "timed-out-before-start", state: domain.CommandStateTimedOut, final: 2},
		{name: "lost-before-start", state: domain.CommandStateLost, final: 2},
		{name: "cancelled-before-queued", state: domain.CommandStateCancelled, final: 1},
		{name: "rejected-before-queued", state: domain.CommandStateRejected, final: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			authority := p068Authority(t)
			create := p149RemoteRunIntent(t, "impossible-final-"+test.name)
			if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
				t.Fatal(err)
			}
			intent, err := authority.GetLocalIntent(ctx, create.IntentID)
			if err != nil {
				t.Fatal(err)
			}
			caller := &p149RecoveryCaller{
				intent:       intent,
				commandState: test.state,
				mutateCommand: func(fields map[string]any) {
					fields["final_event_sequence"] = test.final
					fields["output_complete"] = false
					fields["output_unavailable_reason"] = "retention_expired"
					fields["output_truncated"] = false
					switch test.state {
					case domain.CommandStateSucceeded:
						fields["exit_code"] = 0
					case domain.CommandStateFailed:
						fields["exit_code"] = 1
					default:
						delete(fields, "exit_code")
					}
				},
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(caller.commandPayload(), &object); err != nil {
				t.Fatal(err)
			}
			if _, err := strictRemoteCommandProjectionFromReadReply(intent, object, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)); !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("strict projection error=%v, want impossible terminal boundary rejection", err)
			}
		})
	}
}

// TestP149AcceptedRemoteSubmitRejectsImpossibleZeroPrefixGapTerminal drives
// the actual history-unavailable branch with no durable event prefix. It
// proves a target cannot turn an accepted request into a completed mailbox
// result by claiming a succeeded terminal at sequence one.
func TestP149AcceptedRemoteSubmitRejectsImpossibleZeroPrefixGapTerminal(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p068SubmitIntent(t, "intent-p149-zero-prefix", "session-p149-zero-prefix", "command-p149-zero-prefix", domain.TargetKindRemote, "printf zero-prefix")
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	stored, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	caller := &p149RecoveryCaller{
		intent:               stored,
		commandState:         domain.CommandStateRunning,
		commandStateAfterGap: domain.CommandStateSucceeded,
		gap:                  true,
		gapAvailable:         &zero,
		mutateCommand: func(fields map[string]any) {
			if fields["command_state"] != string(domain.CommandStateSucceeded) {
				return
			}
			fields["final_event_sequence"] = int64(1)
			fields["output_complete"] = false
			fields["output_unavailable_reason"] = "remote_event_gap"
		},
	}
	driver, err := NewRemoteDriver(authority, caller, "router-p149-zero-prefix", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(ctx, create.IntentID); err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileAcceptedRemoteSubmit(ctx, create.IntentID); !errors.Is(err, ErrRemoteEventHistoryUnavailable) {
		t.Fatalf("zero-prefix gap reconciliation error=%v, want history-unavailable terminal boundary rejection", err)
	}
	current, err := authority.GetLocalIntent(ctx, create.IntentID)
	if err != nil || current.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("impossible zero-prefix terminal settled intent=%+v err=%v", current, err)
	}
	if cursor, err := authority.GetRemoteEventCursor(ctx, create.CommandID); err != nil || cursor != 0 {
		t.Fatalf("zero-prefix cursor=%d err=%v", cursor, err)
	}
	if _, err := authority.GetRemoteEventGap(ctx, create.CommandID); !errors.Is(err, store.ErrRemoteGapNotFound) {
		t.Fatalf("impossible zero-prefix terminal stored a gap: %v", err)
	}
	if caller.submitCalls != 1 || caller.runCalls != 0 || caller.streamCalls != 1 {
		t.Fatalf("zero-prefix recovery calls submit=%d run=%d stream=%d", caller.submitCalls, caller.runCalls, caller.streamCalls)
	}
}

func TestP149AcceptedRemoteRunKeepsAcceptedOnMalformedOrContradictoryRecoveryProof(t *testing.T) {
	for _, test := range []struct {
		name              string
		mutateJob         func(map[string]any)
		mutateCommand     func(map[string]any)
		terminalEventType string
	}{
		{
			name: "missing-script-hash",
			mutateCommand: func(fields map[string]any) {
				delete(fields, "script_sha256")
			},
		},
		{
			name: "missing-source-mode",
			mutateCommand: func(fields map[string]any) {
				fields["source"] = map[string]any{}
			},
		},
		{
			name: "succeeded-with-nonzero-exit",
			mutateJob: func(fields map[string]any) {
				fields["exit_code"] = 7
			},
			mutateCommand: func(fields map[string]any) {
				fields["exit_code"] = 7
			},
		},
		{
			name: "lost-with-complete-output",
			mutateJob: func(fields map[string]any) {
				fields["job_phase"] = string(store.JobPhaseLost)
				fields["command_state"] = string(domain.CommandStateLost)
				fields["exit_code"] = nil
				fields["teardown_state"] = string(store.JobTeardownLost)
			},
			mutateCommand: func(fields map[string]any) {
				fields["command_state"] = string(domain.CommandStateLost)
				fields["exit_code"] = nil
			},
			terminalEventType: "command_lost",
		},
		{
			name: "capture-boundary-on-succeeded",
			mutateJob: func(fields map[string]any) {
				fields["output_complete"] = false
				fields["output_unavailable_reason"] = "capture_boundary_unconfirmed"
			},
			mutateCommand: func(fields map[string]any) {
				fields["output_complete"] = false
				fields["output_unavailable_reason"] = "capture_boundary_unconfirmed"
			},
		},
		{name: "wrong-terminal-event", terminalEventType: "command_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			authority := p068Authority(t)
			intent := p149AcceptRemoteRun(t, authority, test.name)
			caller := &p149RecoveryCaller{intent: intent, mutateJob: test.mutateJob, mutateCommand: test.mutateCommand, terminalEventType: test.terminalEventType}
			driver, err := NewRemoteDriver(authority, caller, "router-p149-invalid", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := driver.ReconcileAcceptedRun(ctx, intent.IntentID); !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("reconciliation error=%v, want strict remote response failure", err)
			}
			current, err := authority.GetLocalIntent(ctx, intent.IntentID)
			if err != nil || current.DeliveryState != store.LocalIntentAccepted {
				t.Fatalf("invalid proof settled intent=%+v err=%v", current, err)
			}
			if caller.runCalls != 0 {
				t.Fatalf("recovery resubmitted run %d times", caller.runCalls)
			}
		})
	}
}

// TestP149AcceptedRemoteRunRejectsRejectedAfterStart proves a terminal
// projection cannot settle a run when the immutable event stream violates
// D-01 by rejecting work after it had already started.
func TestP149AcceptedRemoteRunRejectsRejectedAfterStart(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	intent := p149AcceptRemoteRun(t, authority, "rejected-after-start")
	caller := &p149RecoveryCaller{
		intent:            intent,
		terminalEventType: "command_rejected",
		mutateJob: func(fields map[string]any) {
			fields["command_state"] = string(domain.CommandStateRejected)
			fields["exit_code"] = nil
		},
		mutateCommand: func(fields map[string]any) {
			fields["command_state"] = string(domain.CommandStateRejected)
			fields["exit_code"] = nil
		},
	}
	driver, err := NewRemoteDriver(authority, caller, "router-p149-rejected-after-start", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ReconcileAcceptedRun(ctx, intent.IntentID); !errors.Is(err, store.ErrRemoteEventBatch) {
		t.Fatalf("rejected-after-start reconciliation error=%v, want event lifecycle failure", err)
	}
	current, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || current.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("invalid rejected-after-start proof settled intent=%+v err=%v", current, err)
	}
	if caller.runCalls != 0 {
		t.Fatalf("rejected-after-start recovery resubmitted run %d times", caller.runCalls)
	}
}

// TestP149AcceptedRemoteRunRejectsRetainedTruncationConflict keeps a remotely
// accepted run blocked when an immutable retained event says output was
// truncated but the target's terminal projection claims otherwise. The normal
// stream and unavailable-history branches must both fail closed.
func TestP149AcceptedRemoteRunRejectsRetainedTruncationConflict(t *testing.T) {
	for _, test := range []struct {
		name string
		gap  bool
	}{
		{name: "normal-stream", gap: false},
		{name: "history-gap", gap: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			authority := p068Authority(t)
			intent := p149AcceptRemoteRun(t, authority, "truncation-"+test.name)
			if test.gap {
				p149SeedTruncatedPrefix(t, authority, intent.CommandID)
			}
			caller := &p149RecoveryCaller{intent: intent, gap: test.gap, truncatedEvents: true}
			driver, err := NewRemoteDriver(authority, caller, "router-p149-truncation", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			err = driver.ReconcileAcceptedRun(ctx, intent.IntentID)
			if test.gap {
				if !errors.Is(err, ErrRemoteTerminalUnconfirmed) {
					t.Fatalf("truncation history-gap error=%v, want strict terminal proof failure", err)
				}
			} else if !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("truncation stream error=%v, want strict remote response failure", err)
			}
			current, err := authority.GetLocalIntent(ctx, intent.IntentID)
			if err != nil || current.DeliveryState != store.LocalIntentAccepted {
				t.Fatalf("truncation conflict settled intent=%+v err=%v", current, err)
			}
			if test.gap {
				if _, err := authority.GetRemoteEventGap(ctx, intent.CommandID); !errors.Is(err, store.ErrRemoteGapNotFound) {
					t.Fatalf("history-gap truncation conflict stored a gap: %v", err)
				}
			}
			if caller.runCalls != 0 {
				t.Fatalf("truncation conflict recovery resubmitted run %d times", caller.runCalls)
			}
		})
	}
}

func p149SeedTruncatedPrefix(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID) {
	t.Helper()
	when := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []store.RemoteEventRecord{
		{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
		{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
		{CommandID: commandID, Sequence: 3, Type: "output_truncated", OccurredAt: when.Add(2 * time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestP149AcceptedRemoteRunKeepsAcceptedForPreCommandTerminalWithoutTeardownProof
// covers target jobs that failed or were lost while creating their one-off
// session. The target owns no command result and reports teardown=pending, so
// recovery may persist the strict job view but must not invent a mailbox
// terminal outcome or retry the run mutation.
func TestP149AcceptedRemoteRunKeepsAcceptedForPreCommandTerminalWithoutTeardownProof(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase store.JobPhase
	}{
		{name: "failed", phase: store.JobPhaseFailed},
		{name: "lost", phase: store.JobPhaseLost},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			authority := p068Authority(t)
			intent := p149AcceptRemoteRun(t, authority, "pre-command-"+test.name)
			caller := &p149RecoveryCaller{intent: intent, preCommandTerminalPhase: test.phase}
			driver, err := NewRemoteDriver(authority, caller, "router-p149-pre-command", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := driver.ReconcileAcceptedRun(ctx, intent.IntentID); err != nil {
				t.Fatalf("pre-command terminal recovery = %v", err)
			}
			current, err := authority.GetLocalIntent(ctx, intent.IntentID)
			if err != nil || current.DeliveryState != store.LocalIntentAccepted {
				t.Fatalf("pre-command terminal settled intent=%+v err=%v", current, err)
			}
			job, err := authority.GetRemoteJobProjection(ctx, intent.JobID)
			if err != nil || job.Phase != test.phase || job.CommandState != nil || job.TeardownState != store.JobTeardownPending || job.OutputComplete || job.OutputTruncated || job.OutputUnavailableReason != "" {
				t.Fatalf("pre-command terminal job=%+v err=%v", job, err)
			}
			if _, err := authority.GetRemoteCommandProjection(ctx, intent.CommandID); !errors.Is(err, store.ErrRemoteProjectionNotFound) {
				t.Fatalf("pre-command terminal created command projection: %v", err)
			}
			if caller.runCalls != 0 || caller.getJobCalls != 1 || caller.getCommandCalls != 0 || caller.streamCalls != 0 {
				t.Fatalf("pre-command recovery calls run=%d get_job=%d get_command=%d stream=%d", caller.runCalls, caller.getJobCalls, caller.getCommandCalls, caller.streamCalls)
			}
		})
	}
}

func p149RemoteRunIntent(t *testing.T, suffix string) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	jobID := domain.JobID("job-p149-" + suffix)
	sessionID := domain.SessionID("session-p149-" + suffix)
	commandID := domain.CommandID("command-p149-" + suffix)
	script := "printf p149-" + suffix
	payload, err := json.Marshal(map[string]any{
		"operation": "run", "environment": "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
		"script":           script,
	})
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
	return store.LocalIntentCreate{
		IntentID:             domain.IntentID("intent-p149-" + suffix),
		Operation:            "run",
		ResourceID:           string(jobID),
		JobID:                jobID,
		SessionID:            sessionID,
		CommandID:            commandID,
		Target:               target,
		Environment:          "linux-dev",
		Controller:           controller,
		Source:               domain.NewEmptySource(),
		RequestHash:          hash,
		IdempotencyKey:       "key-p149-" + suffix,
		PayloadJSON:          canonical,
		ScriptBytes:          []byte(script),
		IdempotencyRetention: time.Hour,
	}
}

func p149AcceptRemoteRun(t *testing.T, authority *store.AuthorityStore, suffix string) store.LocalIntentRecord {
	t.Helper()
	create := p149RemoteRunIntent(t, suffix)
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), create.IntentID, store.LocalIntentDispatching, "p149-test-dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), create.IntentID, store.LocalIntentAccepted, "p149-test-accepted"); err != nil {
		t.Fatal(err)
	}
	record, err := authority.GetLocalIntent(context.Background(), create.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

type p149RecoveryCaller struct {
	intent                  store.LocalIntentRecord
	frames                  []sshbridge.RequestFrame
	streamFrames            []sshbridge.RequestFrame
	runCalls                int
	submitCalls             int
	getJobCalls             int
	getCommandCalls         int
	streamCalls             int
	observations            int
	gap                     bool
	truncatedEvents         bool
	preCommandTerminalPhase store.JobPhase
	mutateJob               func(map[string]any)
	mutateCommand           func(map[string]any)
	terminalEventType       string
	commandState            domain.CommandState
	commandStateAfterGap    domain.CommandState
	gapAvailable            *int64
	fullRunMutationReply    bool
}

type p149PagedRunCaller struct {
	intents         map[string]store.LocalIntentRecord
	commands        map[string]store.LocalIntentRecord
	terminal        store.LocalIntentRecord
	mutationCalls   int
	getJobCalls     map[string]int
	getCommandCalls map[string]int
	streamCalls     int
}

func newP149PagedRunCaller(records []store.LocalIntentRecord, terminal store.LocalIntentRecord) *p149PagedRunCaller {
	caller := &p149PagedRunCaller{
		intents:         make(map[string]store.LocalIntentRecord, len(records)),
		commands:        make(map[string]store.LocalIntentRecord, len(records)),
		terminal:        terminal,
		getJobCalls:     make(map[string]int),
		getCommandCalls: make(map[string]int),
	}
	for _, record := range records {
		caller.intents[string(record.JobID)] = record
		caller.commands[string(record.CommandID)] = record
	}
	return caller
}

func (c *p149PagedRunCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	switch frame.Operation {
	case sshbridge.OperationRunOrResumeJob, sshbridge.OperationSubmitOrResumeCommand:
		c.mutationCalls++
		return sshbridge.ReplyFrame{}, errors.New("unexpected mutation during paged recovery")
	case sshbridge.OperationGetJob:
		var request struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(frame.Payload, &request); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		intent, found := c.intents[request.JobID]
		if !found {
			return sshbridge.ReplyFrame{}, errors.New("unknown paged recovery job")
		}
		c.getJobCalls[request.JobID]++
		if intent.IntentID == c.terminal.IntentID {
			return p149Reply(frame.RequestID, "result", (&p149RecoveryCaller{intent: intent}).jobPayload()), nil
		}
		return p149Reply(frame.RequestID, "result", p149PendingJobPayload(intent)), nil
	case sshbridge.OperationGetCommand:
		var request struct {
			CommandID string `json:"command_id"`
		}
		if err := json.Unmarshal(frame.Payload, &request); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		intent, found := c.commands[request.CommandID]
		if !found || intent.IntentID != c.terminal.IntentID {
			return sshbridge.ReplyFrame{}, errors.New("unexpected nonterminal command read during paged recovery")
		}
		c.getCommandCalls[request.CommandID]++
		return p149Reply(frame.RequestID, "result", (&p149RecoveryCaller{intent: intent}).commandPayload()), nil
	default:
		return sshbridge.ReplyFrame{}, errors.New("unexpected paged recovery bridge operation")
	}
}

func (c *p149PagedRunCaller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	var request struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(frame.Payload, &request); err != nil {
		return err
	}
	intent, found := c.commands[request.CommandID]
	if !found || intent.IntentID != c.terminal.IntentID {
		return errors.New("unexpected nonterminal event stream during paged recovery")
	}
	c.streamCalls++
	return (&p149RecoveryCaller{intent: intent}).Stream(ctx, frame, receive)
}

func p149PendingJobPayload(intent store.LocalIntentRecord) []byte {
	payload, _ := json.Marshal(map[string]any{
		"job_id": string(intent.JobID), "session_id": string(intent.SessionID), "command_id": string(intent.CommandID),
		"job_phase": string(store.JobPhaseAwaitingCommand), "output_complete": false, "output_truncated": false, "teardown_state": string(store.JobTeardownPending),
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "authority": "remote",
		"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"environment": intent.Environment, "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
		"observed_at":  time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	})
	return payload
}

func (c *p149RecoveryCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	switch frame.Operation {
	case sshbridge.OperationSubmitOrResumeCommand:
		c.submitCalls++
		payload, _ := json.Marshal(map[string]any{"command_id": string(c.intent.CommandID), "ordinal": 1})
		return p149Reply(frame.RequestID, "result", payload), nil
	case sshbridge.OperationRunOrResumeJob:
		c.runCalls++
		if c.fullRunMutationReply {
			return p149Reply(frame.RequestID, "result", c.jobPayload()), nil
		}
		payload, _ := json.Marshal(map[string]any{"job_id": string(c.intent.JobID), "session_id": string(c.intent.SessionID), "command_id": string(c.intent.CommandID)})
		return p149Reply(frame.RequestID, "result", payload), nil
	case sshbridge.OperationGetJob:
		c.getJobCalls++
		return p149Reply(frame.RequestID, "result", c.jobPayload()), nil
	case sshbridge.OperationGetCommand:
		c.getCommandCalls++
		return p149Reply(frame.RequestID, "result", c.commandPayload()), nil
	default:
		return sshbridge.ReplyFrame{}, errors.New("unexpected P149 bridge operation")
	}
}

func (c *p149RecoveryCaller) Stream(_ context.Context, request sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	c.streamCalls++
	c.streamFrames = append(c.streamFrames, request)
	var input struct {
		AfterSequence int64 `json:"after_sequence"`
	}
	if err := json.Unmarshal(request.Payload, &input); err != nil {
		return err
	}
	when := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	events := []sshbridge.ReplyFrame{
		p149EventReply(request.RequestID, c.intent.CommandID, 1, "command_queued", when, nil),
		p149EventReply(request.RequestID, c.intent.CommandID, 2, "command_started", when.Add(time.Second), nil),
	}
	if c.truncatedEvents {
		events = append(events,
			p149EventReply(request.RequestID, c.intent.CommandID, 3, "output_truncated", when.Add(2*time.Second), nil),
		)
	} else {
		events = append(events,
			p149EventReply(request.RequestID, c.intent.CommandID, 3, "stdout", when.Add(2*time.Second), []byte("P149_RECOVERED\n")),
		)
	}
	if c.effectiveCommandState().IsTerminal() {
		events = append(events, p149EventReply(request.RequestID, c.intent.CommandID, 4, c.terminalEvent(), when.Add(3*time.Second), nil))
	}
	if c.gap {
		available := input.AfterSequence
		if c.gapAvailable != nil {
			available = *c.gapAvailable
		} else if available < 1 {
			if err := receive(events[0]); err != nil {
				return err
			}
			available = 1
		}
		if c.commandStateAfterGap != "" {
			c.commandState = c.commandStateAfterGap
		}
		payload, _ := json.Marshal(sshbridge.ErrorPayload{Code: "event_history_unavailable", Message: "P149 missing suffix", Details: map[string]any{"last_sequence": available}})
		return receive(p149Reply(request.RequestID, "error", payload))
	}
	for _, reply := range events {
		var event struct {
			Sequence int64 `json:"sequence"`
		}
		if err := json.Unmarshal(reply.Payload, &event); err != nil {
			return err
		}
		if event.Sequence <= input.AfterSequence {
			continue
		}
		if err := receive(reply); err != nil {
			return err
		}
	}
	return receive(p149StreamEndReply(request.RequestID, int64(len(events))))
}

func (c *p149RecoveryCaller) jobPayload() []byte {
	if c.preCommandTerminalPhase != "" {
		payload, _ := json.Marshal(map[string]any{
			"job_id": string(c.intent.JobID), "session_id": string(c.intent.SessionID), "command_id": string(c.intent.CommandID),
			"job_phase": string(c.preCommandTerminalPhase), "output_complete": false, "output_truncated": false, "teardown_state": string(store.JobTeardownPending),
			"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "authority": "remote",
			"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
			"environment": c.intent.Environment, "source": map[string]string{"mode": "empty"},
			"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
			"observed_at":  c.nextObservedAt(),
		})
		return payload
	}
	state := string(domain.CommandStateSucceeded)
	fields := map[string]any{
		"job_id": string(c.intent.JobID), "session_id": string(c.intent.SessionID), "command_id": string(c.intent.CommandID),
		"job_phase": string(store.JobPhaseComplete), "command_state": state, "exit_code": 0, "final_event_sequence": 4,
		"output_complete": true, "output_truncated": false, "teardown_state": string(store.JobTeardownClosed),
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "authority": "remote",
		"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"environment": c.intent.Environment, "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
		"observed_at":  c.nextObservedAt(),
	}
	if c.mutateJob != nil {
		c.mutateJob(fields)
	}
	payload, _ := json.Marshal(fields)
	return payload
}

func (c *p149RecoveryCaller) commandPayload() []byte {
	digest := sha256.Sum256(c.intent.ScriptBytes)
	state := c.effectiveCommandState()
	fields := map[string]any{
		"command_id": string(c.intent.CommandID), "session_id": string(c.intent.SessionID), "ordinal": 1,
		"command_state": string(state), "output_complete": false, "output_truncated": false,
		"script_sha256": hex.EncodeToString(digest[:]), "script_byte_count": len(c.intent.ScriptBytes),
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "authority": "remote",
		"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"environment": c.intent.Environment, "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
		"observed_at":  c.nextObservedAt(),
	}
	if state.IsTerminal() {
		fields["final_event_sequence"] = 4
		fields["output_complete"] = true
		if state == domain.CommandStateSucceeded {
			fields["exit_code"] = 0
		}
	}
	if c.mutateCommand != nil {
		c.mutateCommand(fields)
	}
	payload, _ := json.Marshal(fields)
	return payload
}

func (c *p149RecoveryCaller) nextObservedAt() time.Time {
	c.observations++
	return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).Add(time.Duration(c.observations) * time.Second)
}

func (c *p149RecoveryCaller) terminalEvent() string {
	if c.terminalEventType != "" {
		return c.terminalEventType
	}
	switch c.effectiveCommandState() {
	case domain.CommandStateFailed:
		return "command_failed"
	case domain.CommandStateCancelled:
		return "command_cancelled"
	case domain.CommandStateTimedOut:
		return "command_timed_out"
	case domain.CommandStateRejected:
		return "command_rejected"
	case domain.CommandStateLost:
		return "command_lost"
	default:
		return "command_succeeded"
	}
}

func (c *p149RecoveryCaller) effectiveCommandState() domain.CommandState {
	if c.commandState != "" {
		return c.commandState
	}
	return domain.CommandStateSucceeded
}

func p149Reply(requestID, responseType string, payload []byte) sshbridge.ReplyFrame {
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, ResponseType: responseType, Payload: payload}
}

func p149EventReply(requestID string, commandID domain.CommandID, sequence int64, eventType string, occurredAt time.Time, output []byte) sshbridge.ReplyFrame {
	payload := map[string]any{"command_id": string(commandID), "sequence": sequence, "type": eventType, "timestamp": occurredAt}
	if len(output) != 0 {
		payload["encoding"] = "base64"
		payload["data_base64"] = base64.StdEncoding.EncodeToString(output)
		payload["byte_count"] = len(output)
	}
	raw, _ := json.Marshal(payload)
	return p149Reply(requestID, "event", raw)
}

func p149StreamEndReply(requestID string, lastSequence int64) sshbridge.ReplyFrame {
	payload, _ := json.Marshal(map[string]int64{"last_sequence": lastSequence})
	return p149Reply(requestID, "stream_end", payload)
}

func p149AssertReadFrames(t *testing.T, calls, streams []sshbridge.RequestFrame) {
	t.Helper()
	for _, frame := range append(append([]sshbridge.RequestFrame(nil), calls...), streams...) {
		if frame.Operation == sshbridge.OperationRunOrResumeJob || frame.Operation == sshbridge.OperationSubmitOrResumeCommand {
			continue
		}
		if frame.ResourceID != "" || frame.IdempotencyKey != "" {
			t.Fatalf("read frame carried mutation fields: %+v", frame)
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sshbridge.DecodeRequest(raw); err != nil {
			t.Fatalf("read frame violates frozen bridge protocol: %v", err)
		}
	}
}

var _ RemoteCaller = (*p149RecoveryCaller)(nil)
var _ RemoteEventStreamer = (*p149RecoveryCaller)(nil)
