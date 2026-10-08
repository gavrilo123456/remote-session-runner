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

func TestRecoverLostRuntimeRecoverySetReleasesMixedPairAndCommandlessSessionWithoutReplay(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-commandless-mixed", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-mixed", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-mixed", CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	pairSession, pairCommand := pRecoveryLostPair(t, service, authority, "commandless-mixed")
	runtime.startErr = errors.New("fixture start failure")
	runtime.cleanupErr = errors.New("fixture cleanup failure")
	commandlessJob, commandlessSession := pCommandlessLostRecoveryJob(t, service, authority, "mixed")
	runtime.startErr = nil
	runtime.cleanupErr = nil
	beforeStart, beforeExecute := runtime.startCall, runtime.commandCall

	pairs, sessions, err := service.RecoverLostRuntimeRecoverySet(context.Background(),
		[]LostRuntimeRecoveryRequest{{SessionID: pairSession.SessionID, CommandID: pairCommand.CommandID}},
		[]CommandlessLostRuntimeRecoveryRequest{{SessionID: commandlessSession.SessionID}},
	)
	if err != nil || len(pairs) != 1 || len(sessions) != 1 {
		t.Fatalf("mixed recovery pairs=%+v sessions=%+v err=%v", pairs, sessions, err)
	}
	if runtime.lostRecoveryCall != 2 || runtime.lostFinalizeCall != 2 || runtime.startCall != beforeStart || runtime.commandCall != beforeExecute {
		t.Fatalf("runtime calls recover=%d finalize=%d start=%d execute=%d; recovery must not replay work", runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall)
	}
	pRecoveryAssertReleased(t, authority, pairSession.SessionID, pairCommand.CommandID)
	reservation, err := authority.GetSessionReservation(context.Background(), commandlessSession.SessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("commandless reservation=%+v err=%v", reservation, err)
	}
	job, err := authority.GetJob(context.Background(), commandlessJob.JobID)
	if err != nil || job.Phase != store.JobPhaseLost || job.CommandState != nil || job.FinalEventSequence != nil || job.OutputComplete || job.TeardownState != store.JobTeardownPending || job.TeardownReason != "" {
		t.Fatalf("commandless job changed=%+v err=%v", job, err)
	}
	if _, err := authority.GetCommand(context.Background(), commandlessJob.CommandID); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("commandless planned command lookup error=%v, want %v", err, store.ErrCommandNotFound)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 0 {
		t.Fatalf("live slots=%d err=%v, want 0", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 0 {
		t.Fatalf("live reservations=%d err=%v, want 0", reservations, err)
	}
}

func TestRecoverLostRuntimeRecoverySetKeepsPairOnlyCompatibility(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-commandless-pair-only", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-pair-only", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-pair-only", CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	session, command := pRecoveryLostPair(t, service, authority, "commandless-pair-only")
	beforeStart, beforeExecute := runtime.startCall, runtime.commandCall

	if err := service.CheckLostRuntimeRecoverySet(context.Background(), []LostRuntimeRecoveryRequest{{
		SessionID: session.SessionID,
		CommandID: command.CommandID,
	}}, nil); err != nil {
		t.Fatalf("pair-only recovery preflight: %v", err)
	}
	pairs, sessions, err := service.RecoverLostRuntimeRecoverySet(context.Background(), []LostRuntimeRecoveryRequest{{
		SessionID: session.SessionID,
		CommandID: command.CommandID,
	}}, nil)
	if err != nil || len(pairs) != 1 || len(sessions) != 0 {
		t.Fatalf("pair-only recovery pairs=%+v sessions=%+v err=%v", pairs, sessions, err)
	}
	if runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 || runtime.startCall != beforeStart || runtime.commandCall != beforeExecute {
		t.Fatalf("runtime calls recover=%d finalize=%d start=%d execute=%d; pair-only recovery must not replay work", runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall)
	}
	pRecoveryAssertReleased(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverCommandlessLostRuntimeRetainsCapacityWhenRuntimeProofIsUnconfirmed(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-commandless-unconfirmed"},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-unconfirmed", CleanupConfirmed: false},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-unconfirmed", CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	runtime.startErr = errors.New("fixture start failure")
	runtime.cleanupErr = errors.New("fixture cleanup failure")
	_, session := pCommandlessLostRecoveryJob(t, service, authority, "unconfirmed")
	runtime.startErr = nil
	runtime.cleanupErr = nil

	_, _, err := service.RecoverLostRuntimeRecoverySet(context.Background(), nil, []CommandlessLostRuntimeRecoveryRequest{{SessionID: session.SessionID}})
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("unconfirmed recovery err=%v recover=%d finalize=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	reservation, reservationErr := authority.GetSessionReservation(context.Background(), session.SessionID)
	if reservationErr != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("unconfirmed commandless reservation=%+v err=%v, want retained", reservation, reservationErr)
	}
	if pending, pendingErr := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(context.Background()); pendingErr != nil || len(pending) != 0 {
		t.Fatalf("pending finalizations after unconfirmed proof=%+v err=%v, want none", pending, pendingErr)
	}
}

func TestReconcileStartupFinalizesPendingCommandlessLostRecoveryBeforeOwnershipAudit(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-commandless-finalization"},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-finalization", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-commandless-finalization", CleanupConfirmed: true},
		lostFinalizeErr:    errors.New("fixture finalization failure"),
	}
	service, authority := newP027Service(t, runtime)
	runtime.startErr = errors.New("fixture start failure")
	runtime.cleanupErr = errors.New("fixture cleanup failure")
	_, session := pCommandlessLostRecoveryJob(t, service, authority, "finalization")
	runtime.startErr = nil
	runtime.cleanupErr = nil

	if _, _, err := service.RecoverLostRuntimeRecoverySet(context.Background(), nil, []CommandlessLostRuntimeRecoveryRequest{{SessionID: session.SessionID}}); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed commandless pending finalization error=%v, want %v", err, ErrLostRuntimeRecoveryFinalization)
	}
	if pending, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 1 || pending[0].SessionID != session.SessionID {
		t.Fatalf("seed pending commandless finalization=%+v err=%v", pending, err)
	}
	runtime.lostFinalizeErr = nil
	runtime.ownershipHook = func() error {
		pending, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(context.Background())
		if err != nil {
			return err
		}
		if len(pending) != 0 {
			return fmt.Errorf("pending commandless finalizations remain before ownership audit: %v", pending)
		}
		return nil
	}
	if report, err := service.ReconcileStartup(context.Background()); err != nil || report.SessionsInspected != 0 {
		t.Fatalf("startup reconciliation=%+v err=%v, want only commandless finalization", report, err)
	}
	if runtime.ownershipAudit != 1 || runtime.lostFinalizeCall != 2 {
		t.Fatalf("startup calls audit=%d finalization=%d, want 1/2", runtime.ownershipAudit, runtime.lostFinalizeCall)
	}
}

func pCommandlessLostRecoveryJob(t *testing.T, service *Service, authority *store.AuthorityStore, suffix string) (store.JobRecord, store.SessionRecord) {
	t.Helper()
	request := pCommandlessLostRunJobRequest(t, suffix)
	if _, err := service.RunJob(context.Background(), request); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("seed commandless lost run error=%v, want %v", err, ErrRuntimeUnavailable)
	}
	job, err := authority.GetJob(context.Background(), request.Acceptance.JobID)
	if err != nil || job.Phase != store.JobPhaseLost || job.CommandState != nil || job.ExitCode != nil || job.FinalEventSequence != nil || job.OutputComplete || job.OutputTruncated || job.TeardownState != store.JobTeardownPending || job.TeardownReason != "" {
		t.Fatalf("seed commandless lost job=%+v err=%v", job, err)
	}
	session, err := authority.GetSession(context.Background(), request.Acceptance.SessionID)
	if err != nil || session.State != domain.SessionStateLost || session.RuntimeGeneration == "" {
		t.Fatalf("seed commandless lost session=%+v err=%v", session, err)
	}
	if _, err := authority.GetCommand(context.Background(), request.Acceptance.CommandID); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("seed commandless command lookup error=%v, want %v", err, store.ErrCommandNotFound)
	}
	return job, session
}

func pCommandlessLostRunJobRequest(t *testing.T, suffix string) RunJobRequest {
	t.Helper()
	target := p020Target(t, domain.TargetKindLocal, "mac-workstation")
	controller := p020Controller(t, domain.ControllerTypeLocalUser)
	jobID := domain.JobID("job-commandless-execution-" + suffix)
	sessionID := domain.SessionID("sess-commandless-execution-" + suffix)
	commandID := domain.CommandID("cmd-commandless-execution-" + suffix)
	script := "printf commandless"
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return RunJobRequest{Acceptance: store.JobAcceptance{
		JobID:                jobID,
		SessionID:            sessionID,
		CommandID:            commandID,
		Controller:           controller,
		IdempotencyKey:       "key-commandless-execution-" + suffix,
		RequestHash:          hash,
		Environment:          "mac-dev",
		Target:               target,
		Source:               domain.NewEmptySource(),
		Script:               script,
		CanonicalPayload:     canonical,
		IdempotencyRetention: time.Hour,
	}, MaxActiveSessions: store.DefaultActiveSessionLimit, IdempotencyRetention: time.Hour}
}
