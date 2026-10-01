package execution

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG008RecoverLostRuntimeBatchPreservingQueuedOneOffsKeepsQueuedIdentity(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug008-queue-preserving"},
		lostRecoveryResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-queue-preserving",
			CleanupConfirmed:  true,
		},
		lostFinalizeResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-queue-preserving",
			CleanupConfirmed:  true,
		},
	}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug008-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug008-second")
	thirdSession, thirdCommand := pRecoveryLostPair(t, service, authority, "bug008-third")
	fourthSession, fourthCommand := pRecoveryLostPair(t, service, authority, "bug008-fourth")
	firstQueued, firstBefore := pBUG008ExecutionQueuedOneOff(t, service, authority, "success-first")
	secondQueued, secondBefore := pBUG008ExecutionQueuedOneOff(t, service, authority, "success-second")
	beforeStart, beforeCommand := runtime.startCall, runtime.commandCall

	results, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
		{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
		{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
	})
	if err != nil || len(results) != 4 {
		t.Fatalf("queue-preserving recovery results=%+v err=%v", results, err)
	}
	if runtime.lostRecoveryCall != 4 || runtime.lostFinalizeCall != 4 || runtime.startCall != beforeStart || runtime.commandCall != beforeCommand {
		t.Fatalf("runtime calls recovery=%d finalize=%d start=%d command=%d, want 4/4/%d/%d", runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall, beforeStart, beforeCommand)
	}
	pRecoveryAssertReleased(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertReleased(t, authority, secondSession.SessionID, secondCommand.CommandID)
	pRecoveryAssertReleased(t, authority, thirdSession.SessionID, thirdCommand.CommandID)
	pRecoveryAssertReleased(t, authority, fourthSession.SessionID, fourthCommand.CommandID)
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, firstQueued, firstBefore)
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, secondQueued, secondBefore)

	repeat, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
		{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
		{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
	})
	if err != nil || len(repeat) != 4 || !repeat[0].AlreadyRecovered || !repeat[1].AlreadyRecovered || !repeat[2].AlreadyRecovered || !repeat[3].AlreadyRecovered || runtime.lostRecoveryCall != 4 || runtime.lostFinalizeCall != 8 {
		t.Fatalf("idempotent queue-preserving recovery=%+v err=%v recovery=%d finalization=%d", repeat, err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, firstQueued, firstBefore)
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, secondQueued, secondBefore)
}

func TestBUG008QueuePreservingRecoveryRefusesFreeCapacityBeforeRuntimeProof(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug008-free-capacity"},
		lostRecoveryResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-free-capacity",
			CleanupConfirmed:  true,
		},
		lostFinalizeResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-free-capacity",
			CleanupConfirmed:  true,
		},
	}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug008-free-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug008-free-second")
	queued, before := pBUG008ExecutionQueuedOneOff(t, service, authority, "free-capacity")

	if _, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
	}); !errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("queue-preserving recovery error=%v, want unreleasable", err)
	}
	if runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("free-capacity recovery invoked runtime proof=%d finalization=%d", runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertRetained(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertRetained(t, authority, secondSession.SessionID, secondCommand.CommandID)
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, queued, before)
}

func TestBUG008QueuePreservingRecoveryRejectsUnselectedLiveSlotBeforeRuntimeProof(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug008-reject"},
		lostRecoveryResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-reject",
			CleanupConfirmed:  true,
		},
		lostFinalizeResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-reject",
			CleanupConfirmed:  true,
		},
	}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug008-reject-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug008-reject-second")
	thirdSession, thirdCommand := pRecoveryLostPair(t, service, authority, "bug008-reject-third")
	fourthSession, fourthCommand := pRecoveryLostPair(t, service, authority, "bug008-reject-fourth")
	queued, before := pBUG008ExecutionQueuedOneOff(t, service, authority, "reject")
	requests := []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
	}

	if _, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), requests); !errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("queue-preserving recovery error=%v, want unreleasable", err)
	}
	if runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("unsafe recovery invoked runtime proof=%d finalization=%d", runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertRetained(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertRetained(t, authority, secondSession.SessionID, secondCommand.CommandID)
	pRecoveryAssertRetained(t, authority, thirdSession.SessionID, thirdCommand.CommandID)
	pRecoveryAssertRetained(t, authority, fourthSession.SessionID, fourthCommand.CommandID)
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, queued, before)
}

func TestBUG008QueuePreservingRecoveryRetainsAllCapacityWhenProofIsUnconfirmed(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug008-unconfirmed"},
		lostRecoveryResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-unconfirmed",
			CleanupConfirmed:  false,
		},
		lostFinalizeResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-unconfirmed",
			CleanupConfirmed:  true,
		},
	}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug008-unconfirmed-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug008-unconfirmed-second")
	thirdSession, thirdCommand := pRecoveryLostPair(t, service, authority, "bug008-unconfirmed-third")
	fourthSession, fourthCommand := pRecoveryLostPair(t, service, authority, "bug008-unconfirmed-fourth")
	queued, before := pBUG008ExecutionQueuedOneOff(t, service, authority, "unconfirmed")
	beforeStart, beforeCommand := runtime.startCall, runtime.commandCall

	_, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
		{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
		{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
	})
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 0 || runtime.startCall != beforeStart || runtime.commandCall != beforeCommand {
		t.Fatalf("unconfirmed recovery err=%v proof=%d finalization=%d start=%d command=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall)
	}
	pRecoveryAssertRetained(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertRetained(t, authority, secondSession.SessionID, secondCommand.CommandID)
	pRecoveryAssertRetained(t, authority, thirdSession.SessionID, thirdCommand.CommandID)
	pRecoveryAssertRetained(t, authority, fourthSession.SessionID, fourthCommand.CommandID)
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, queued, before)
}

func TestBUG008QueuePreservingRecoveryFinalizesAlreadyReleasedPairsAfterQueuedWorkStarts(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug008-finalize-retry"},
		lostRecoveryResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-finalize-retry",
			CleanupConfirmed:  true,
		},
		lostFinalizeResult: RuntimeReconcileResult{
			RuntimeGeneration: "generation-bug008-finalize-retry",
			CleanupConfirmed:  true,
		},
		lostFinalizeErr: errors.New("fixture finalization failure"),
	}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug008-finalize-retry-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug008-finalize-retry-second")
	thirdSession, thirdCommand := pRecoveryLostPair(t, service, authority, "bug008-finalize-retry-third")
	fourthSession, fourthCommand := pRecoveryLostPair(t, service, authority, "bug008-finalize-retry-fourth")
	queued, _ := pBUG008ExecutionQueuedOneOff(t, service, authority, "finalize-retry")
	requests := []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
		{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
		{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
	}

	if _, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), requests); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("initial recovery error=%v, want finalization error", err)
	}
	for _, request := range requests {
		pRecoveryAssertReleased(t, authority, request.SessionID, request.CommandID)
	}
	started, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != queued.CommandID {
		t.Fatalf("start queued work after release=%+v err=%v, want %s", started, err, queued.CommandID)
	}

	runtime.lostFinalizeErr = nil
	retry, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), requests)
	if err != nil || len(retry) != len(requests) {
		t.Fatalf("finalization retry results=%+v err=%v", retry, err)
	}
	for _, result := range retry {
		if !result.AlreadyRecovered {
			t.Fatalf("finalization retry result=%+v, want already recovered", result)
		}
	}
	if runtime.lostRecoveryCall != 4 || runtime.lostFinalizeCall != 5 {
		t.Fatalf("runtime calls recovery=%d finalization=%d, want 4/5", runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
}

func TestBUG008QueuePreservingRecoveryRejectsUnsafeDirectOrActiveCommandBeforeRuntimeProof(t *testing.T) {
	for _, test := range []struct {
		name               string
		releaseReservation bool
		activeState        domain.CommandState
	}{
		{name: "ready queued direct reservation"},
		{name: "ready queued direct command with released reservation", releaseReservation: true},
		{name: "running direct command", activeState: domain.CommandStateRunning},
		{name: "cancelling direct command", activeState: domain.CommandStateCancelling},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &p027Runtime{
				p020FakeRuntime: p020FakeRuntime{generation: "generation-bug008-direct-reject"},
				lostRecoveryResult: RuntimeReconcileResult{
					RuntimeGeneration: "generation-bug008-direct-reject",
					CleanupConfirmed:  true,
				},
				lostFinalizeResult: RuntimeReconcileResult{
					RuntimeGeneration: "generation-bug008-direct-reject",
					CleanupConfirmed:  true,
				},
			}
			service, authority, database := newBUG008QueuePreservingServiceWithDatabase(t, runtime)
			firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug008-direct-reject-first")
			secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug008-direct-reject-second")
			thirdSession, thirdCommand := pRecoveryLostPair(t, service, authority, "bug008-direct-reject-third")
			fourthSession, fourthCommand := pRecoveryLostPair(t, service, authority, "bug008-direct-reject-fourth")
			directSession, directCommand := pBUG008ExecutionDirectQueued(t, service, authority, "reject")
			expectedDirectState := domain.CommandStateQueued
			if test.releaseReservation {
				if err := authority.ConfirmSessionCleanup(context.Background(), directSession.SessionID); err != nil {
					t.Fatalf("release direct session reservation: %v", err)
				}
			}
			if test.activeState != "" {
				expectedDirectState = test.activeState
				if _, err := database.Exec(`UPDATE exec_commands SET state = ? WHERE command_id = ?`, string(test.activeState), string(directCommand.CommandID)); err != nil {
					t.Fatalf("make direct command %s: %v", test.activeState, err)
				}
			}

			requests := []LostRuntimeRecoveryRequest{
				{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
				{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
				{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
				{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
			}
			if _, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), requests); !errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable) {
				t.Fatalf("queue-preserving recovery error=%v, want unreleasable", err)
			}
			if runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
				t.Fatalf("unsafe recovery invoked runtime proof=%d finalization=%d", runtime.lostRecoveryCall, runtime.lostFinalizeCall)
			}
			for _, request := range requests {
				pRecoveryAssertRetained(t, authority, request.SessionID, request.CommandID)
			}
			directAfter, err := authority.GetCommand(context.Background(), directCommand.CommandID)
			if err != nil || directAfter.State != expectedDirectState {
				t.Fatalf("direct command=%+v err=%v, want state %q", directAfter, err, expectedDirectState)
			}
			reservation, err := authority.GetSessionReservation(context.Background(), directSession.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if test.releaseReservation && (reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil) {
				t.Fatalf("released direct reservation=%+v", reservation)
			}
			if !test.releaseReservation && (reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil) {
				t.Fatalf("retained direct reservation=%+v", reservation)
			}
		})
	}
}

type bug008ExecutionQueuedSnapshot struct {
	job     store.JobRecord
	session store.SessionRecord
	command store.CommandRecord
	events  []store.CommandEventRecord
}

func newBUG008QueuePreservingService(t *testing.T, runtime *p027Runtime) (*Service, *store.AuthorityStore) {
	t.Helper()
	service, authority, _ := newBUG008QueuePreservingServiceWithDatabase(t, runtime)
	return service, authority
}

func newBUG008QueuePreservingServiceWithDatabase(t *testing.T, runtime *p027Runtime) (*Service, *store.AuthorityStore, *sql.DB) {
	t.Helper()
	database, err := store.Open(context.Background(), t.TempDir()+"/state/bug008-queue-preserving.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	clock := &p020Clock{now: time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(p020Environment(t), p025Environment(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, clock, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	return service, authority, database
}

func pBUG008ExecutionDirectQueued(t *testing.T, service *Service, authority *store.AuthorityStore, suffix string) (store.SessionRecord, store.CommandRecord) {
	t.Helper()
	request := p020Request(t,
		"session-bug008-direct-"+suffix,
		"key-bug008-direct-"+suffix,
		p020Target(t, domain.TargetKindLocal, "mac-workstation"),
	)
	created, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	command := p022QueueCommand(t, authority, created.Session, "command-bug008-direct-"+suffix, "command-key-bug008-direct-"+suffix)
	return created.Session, command
}

func pBUG008ExecutionQueuedOneOff(t *testing.T, service *Service, authority *store.AuthorityStore, suffix string) (store.JobRecord, bug008ExecutionQueuedSnapshot) {
	t.Helper()
	request := p025Request(t,
		"job-bug008-execution-queued-"+suffix,
		"session-bug008-execution-queued-"+suffix,
		"command-bug008-execution-queued-"+suffix,
		"key-bug008-execution-queued-"+suffix,
		"echo must-never-run",
	)
	job, duplicate, err := authority.AcceptJob(context.Background(), request.Acceptance)
	if err != nil || duplicate {
		t.Fatalf("accept queued one-off=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	session := p111CreateJobSession(t, context.Background(), service, job, request)
	job, err = authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseCreatingSession,
		NextPhase:     store.JobPhaseAcceptingCommand,
	})
	if err != nil {
		t.Fatal(err)
	}
	commandHash, err := oneOffCommandHash(job, session.Limits.CommandTimeout)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptCommand(context.Background(), SubmitCommandRequest{
		CommandID:            job.CommandID,
		SessionID:            job.SessionID,
		Controller:           job.Controller,
		IdempotencyKey:       jobStepKey(job.JobID, "submit_command"),
		RequestHash:          commandHash,
		Script:               string(job.ScriptBytes),
		Timeout:              session.Limits.CommandTimeout,
		IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil || accepted.Duplicate || accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("accept queued one-off command=%+v duplicate=%v err=%v", accepted.Command, accepted.Duplicate, err)
	}
	job, err = authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseAcceptingCommand,
		NextPhase:     store.JobPhaseAwaitingCommand,
		Command:       &accepted.Command,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job, pBUG008ExecutionQueuedSnapshot(t, authority, job)
}

func pBUG008ExecutionQueuedSnapshot(t *testing.T, authority *store.AuthorityStore, job store.JobRecord) bug008ExecutionQueuedSnapshot {
	t.Helper()
	storedJob, err := authority.GetJob(context.Background(), job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := authority.GetSession(context.Background(), job.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	command, err := authority.GetCommand(context.Background(), job.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := authority.ListCommandEvents(context.Background(), job.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	return bug008ExecutionQueuedSnapshot{job: storedJob, session: session, command: command, events: events}
}

func pBUG008AssertExecutionQueuedOneOffUnchanged(t *testing.T, authority *store.AuthorityStore, job store.JobRecord, before bug008ExecutionQueuedSnapshot) {
	t.Helper()
	after := pBUG008ExecutionQueuedSnapshot(t, authority, job)
	if after.job.JobID != before.job.JobID || after.job.SessionID != before.job.SessionID || after.job.CommandID != before.job.CommandID ||
		after.job.IdempotencyKey != before.job.IdempotencyKey || after.job.Phase != store.JobPhaseAwaitingCommand ||
		after.job.CommandState == nil || *after.job.CommandState != domain.CommandStateQueued || after.job.ExitCode != nil ||
		after.job.FinalEventSequence != nil || after.job.OutputComplete || after.job.OutputTruncated ||
		after.job.TeardownState != store.JobTeardownPending || after.job.TeardownReason != "" {
		t.Fatalf("queued job changed: before=%+v after=%+v", before.job, after.job)
	}
	if after.session.SessionID != before.session.SessionID || after.session.State != domain.SessionStateReady ||
		after.command.CommandID != before.command.CommandID || after.command.SessionID != before.command.SessionID ||
		after.command.Ordinal != before.command.Ordinal || after.command.State != domain.CommandStateQueued ||
		after.command.ExitCode != nil || after.command.FinalEventSequence != nil || after.command.OutputComplete || after.command.OutputTruncated ||
		len(after.events) != 1 || after.events[0].Sequence != 1 || after.events[0].Type != "command_queued" {
		t.Fatalf("queued child changed: before=%+v after=%+v", before, after)
	}
	if _, err := authority.GetCommandSlot(context.Background(), job.CommandID); !errors.Is(err, store.ErrCommandSlotNotFound) {
		t.Fatalf("queued job has a command slot: %v", err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), job.SessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("queued job reservation=%+v err=%v", reservation, err)
	}
}
