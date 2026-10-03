package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestControlledRestartRehydratesOnlyPlannedQueuedOneOffBeforeGenericStartup(t *testing.T) {
	ctx := context.Background()
	runtime := &p027Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-controlled-restart"}}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	pairs := pControlledRestartExecutionLostPairs(t, service, authority, "rehydrate")
	queued := pControlledRestartExecutionQueuedOneOff(t, service, authority, "rehydrate")
	before := pBUG008ExecutionQueuedSnapshot(t, authority, queued)
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}

	report, startup, err := service.ReconcileStartupWithControlledRestartPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !startup.Rehydrated || startup.Plan == nil || startup.Plan.JobID != plan.JobID || startup.Plan.SessionID != queued.SessionID || startup.Plan.CommandID != queued.CommandID {
		t.Fatalf("controlled startup=%+v, want planned queued identity", startup)
	}
	if report.SessionsInspected != 0 || runtime.controlledRestartCalls != 1 || len(runtime.controlledRestartSaw) != 1 || runtime.controlledRestartSaw[0].SessionID != queued.SessionID {
		t.Fatalf("controlled startup report=%+v rebuild_calls=%d saw=%+v", report, runtime.controlledRestartCalls, runtime.controlledRestartSaw)
	}
	if runtime.commandCall != 0 || runtime.reconcileCall != 0 {
		t.Fatalf("controlled rehydration executed or generically reconciled work: commands=%d reconcile=%d", runtime.commandCall, runtime.reconcileCall)
	}
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, queued, before)
	if stored, err := authority.ReadControlledRestartPlan(ctx); err != nil || stored.CommandID != plan.CommandID {
		t.Fatalf("plan after shell rehydration=%+v err=%v", stored, err)
	}

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	claimed, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if err != nil || claimed.CommandID != queued.CommandID {
		t.Fatalf("claim planned queued command=%+v err=%v", claimed, err)
	}
	if _, err := authority.ReadControlledRestartPlan(ctx); !errors.Is(err, store.ErrControlledRestartPlanNotFound) {
		t.Fatalf("plan after exact command claim error=%v, want not found", err)
	}
}

func TestControlledRestartLeavesPlanAndQueuedCommandWhenRehydrationFails(t *testing.T) {
	ctx := context.Background()
	runtime := &p027Runtime{
		p020FakeRuntime:      p020FakeRuntime{generation: "generation-controlled-restart-failure"},
		controlledRestartErr: errors.New("fixture shell ownership remains unproven"),
	}
	service, authority := newBUG008QueuePreservingService(t, runtime)
	pairs := pControlledRestartExecutionLostPairs(t, service, authority, "rehydrate-failure")
	queued := pControlledRestartExecutionQueuedOneOff(t, service, authority, "rehydrate-failure")
	before := pBUG008ExecutionQueuedSnapshot(t, authority, queued)
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := service.ReconcileStartupWithControlledRestartPlan(ctx); !errors.Is(err, ErrControlledRestartUnavailable) {
		t.Fatalf("controlled startup error=%v, want %v", err, ErrControlledRestartUnavailable)
	}
	if runtime.controlledRestartCalls != 1 || runtime.commandCall != 0 || runtime.reconcileCall != 0 {
		t.Fatalf("failed controlled rehydration calls rebuild=%d command=%d reconcile=%d", runtime.controlledRestartCalls, runtime.commandCall, runtime.reconcileCall)
	}
	pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, queued, before)
	if stored, err := authority.ReadControlledRestartPlan(ctx); err != nil || stored.CommandID != plan.CommandID {
		t.Fatalf("plan after failed rehydration=%+v err=%v", stored, err)
	}
}

func pControlledRestartExecutionLostPairs(t *testing.T, service *Service, authority *store.AuthorityStore, suffix string) []store.LostRuntimeRecoveryPair {
	t.Helper()
	pairs := make([]store.LostRuntimeRecoveryPair, 0, store.DefaultRunningCommandLimit)
	for index := 0; index < store.DefaultRunningCommandLimit; index++ {
		session, command := pRecoveryLostPair(t, service, authority, fmt.Sprintf("controlled-restart-%s-%d", suffix, index))
		pairs = append(pairs, store.LostRuntimeRecoveryPair{SessionID: session.SessionID, CommandID: command.CommandID})
	}
	return pairs
}

func pControlledRestartExecutionQueuedOneOff(t *testing.T, service *Service, authority *store.AuthorityStore, suffix string) store.JobRecord {
	t.Helper()
	target := p020Target(t, domain.TargetKindLocal, "mac-workstation")
	controller := p020Controller(t, domain.ControllerTypeLocalUser)
	jobID := domain.JobID("job-controlled-restart-local-" + suffix)
	sessionID := domain.SessionID("session-controlled-restart-local-" + suffix)
	commandID := domain.CommandID("command-controlled-restart-local-" + suffix)
	script := "printf controlled-restart-test"
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := RunJobRequest{Acceptance: store.JobAcceptance{
		JobID: jobID, SessionID: sessionID, CommandID: commandID, Controller: controller,
		IdempotencyKey: "key-controlled-restart-local-" + suffix, RequestHash: hash,
		Environment: "mac-dev", Target: target, Source: domain.NewEmptySource(), Script: script,
		CanonicalPayload: canonical, IdempotencyRetention: time.Hour,
	}, MaxActiveSessions: store.DefaultActiveSessionLimit, IdempotencyRetention: time.Hour}
	job, duplicate, err := authority.AcceptJob(context.Background(), request.Acceptance)
	if err != nil || duplicate {
		t.Fatalf("accept local queued one-off=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	session := p111CreateJobSession(t, context.Background(), service, job, request)
	job, err = authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseAcceptingCommand,
	})
	if err != nil {
		t.Fatal(err)
	}
	commandHash, err := oneOffCommandHash(job, session.Limits.CommandTimeout)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptCommand(context.Background(), SubmitCommandRequest{
		CommandID: job.CommandID, SessionID: job.SessionID, Controller: job.Controller,
		IdempotencyKey: jobStepKey(job.JobID, "submit_command"), RequestHash: commandHash,
		Script: string(job.ScriptBytes), Timeout: session.Limits.CommandTimeout, IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil || accepted.Duplicate || accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("accept local queued command=%+v duplicate=%v err=%v", accepted.Command, accepted.Duplicate, err)
	}
	job, err = authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseAcceptingCommand, NextPhase: store.JobPhaseAwaitingCommand, Command: &accepted.Command,
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
