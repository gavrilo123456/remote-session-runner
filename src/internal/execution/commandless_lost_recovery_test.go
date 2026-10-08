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

func TestRecoverPreStartCommandlessLostRuntimeReleasesAfterProofWithoutReplay(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: ""},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	job, session := pPreStartCommandlessLostRecoveryJob(t, authority, "release")
	beforeStart, beforeExecute := runtime.startCall, runtime.commandCall

	_, sessions, err := service.RecoverLostRuntimeRecoverySet(context.Background(), nil, []CommandlessLostRuntimeRecoveryRequest{{SessionID: session.SessionID}})
	if err != nil || len(sessions) != 1 {
		t.Fatalf("pre-start recovery sessions=%+v err=%v", sessions, err)
	}
	if runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 || len(runtime.lostRecoverySaw) != 1 || runtime.lostRecoverySaw[0].RuntimeGeneration != "" || runtime.startCall != beforeStart || runtime.commandCall != beforeExecute {
		t.Fatalf("pre-start calls recover=%d finalize=%d saw=%+v start=%d execute=%d; recovery must not replay work", runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.lostRecoverySaw, runtime.startCall, runtime.commandCall)
	}
	reservation, reservationErr := authority.GetSessionReservation(context.Background(), session.SessionID)
	if reservationErr != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("released pre-start reservation=%+v err=%v", reservation, reservationErr)
	}
	if _, commandErr := authority.GetCommand(context.Background(), job.CommandID); !errors.Is(commandErr, store.ErrCommandNotFound) {
		t.Fatalf("pre-start planned command lookup=%v, want %v", commandErr, store.ErrCommandNotFound)
	}
}

func TestRecoverPreStartCommandlessLostRuntimeRetainsCapacityWithoutProof(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: ""},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: false},
		lostFinalizeResult: RuntimeReconcileResult{CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	_, session := pPreStartCommandlessLostRecoveryJob(t, authority, "retain")

	_, _, err := service.RecoverLostRuntimeRecoverySet(context.Background(), nil, []CommandlessLostRuntimeRecoveryRequest{{SessionID: session.SessionID}})
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 0 || len(runtime.lostRecoverySaw) != 1 || runtime.lostRecoverySaw[0].RuntimeGeneration != "" {
		t.Fatalf("unconfirmed pre-start recovery err=%v recover=%d finalize=%d saw=%+v", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.lostRecoverySaw)
	}
	reservation, reservationErr := authority.GetSessionReservation(context.Background(), session.SessionID)
	if reservationErr != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("unconfirmed pre-start reservation=%+v err=%v, want retained", reservation, reservationErr)
	}
}

func TestRecoverLostRuntimePairRejectsBlankGenerationBeforeRuntimeProof(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-pair-empty"},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{CleanupConfirmed: true},
	}
	service, authority, database := newP027ServiceWithDatabase(t, runtime)
	session, command := pRecoveryLostPair(t, service, authority, "blank-generation")
	if _, err := database.Exec(`UPDATE exec_sessions SET runtime_generation = '' WHERE session_id = ?`, string(session.SessionID)); err != nil {
		t.Fatal(err)
	}
	_, _, err := service.RecoverLostRuntimeRecoverySet(context.Background(), []LostRuntimeRecoveryRequest{{SessionID: session.SessionID, CommandID: command.CommandID}}, nil)
	if !errors.Is(err, ErrLostRuntimeRecoveryIneligible) || runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("blank pair recovery err=%v recover=%d finalize=%d, want early refusal", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertRetained(t, authority, session.SessionID, command.CommandID)
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

// pPreStartCommandlessLostRecoveryJob creates the exact durable shape written
// by the former faulty path. Current CreateSession no longer produces it.
func pPreStartCommandlessLostRecoveryJob(t *testing.T, authority *store.AuthorityStore, suffix string) (store.JobRecord, store.SessionRecord) {
	t.Helper()
	request := pCommandlessLostRunJobRequest(t, "pre-start-"+suffix)
	ctx := context.Background()
	job, duplicate, err := authority.AcceptJob(ctx, request.Acceptance)
	if err != nil || duplicate {
		t.Fatalf("seed pre-start lost job=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	environment := p020Environment(t)
	limits, err := environment.ValidateSessionPolicy(domain.SessionPolicyRequest{
		Target:     job.Target,
		Source:     job.Source,
		Controller: job.Controller,
		Limits:     request.RequestedLimits,
		Isolation:  request.Isolation,
	})
	if err != nil {
		t.Fatal(err)
	}
	createHash, err := oneOffCreateHash(job, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := authority.AcceptSessionCreate(ctx, store.SessionCreateAcceptance{
		SessionCreate: store.SessionCreate{
			SessionID:   job.SessionID,
			Target:      job.Target,
			Environment: job.Environment,
			Controller:  job.Controller,
			Source:      job.Source,
			Limits:      limits,
			Reason:      "session_created",
		},
		IdempotencyKey:       jobStepKey(job.JobID, "create_session"),
		RequestHash:          createHash,
		MaxActiveSessions:    request.MaxActiveSessions,
		IdempotencyRetention: request.IdempotencyRetention,
	}); err != nil || duplicate {
		t.Fatalf("seed pre-start creating session duplicate=%v err=%v", duplicate, err)
	}
	session, err := authority.CompleteSessionCreation(ctx, job.SessionID, domain.SessionStateLost, "", "", "runtime_cleanup_unconfirmed")
	if err != nil {
		t.Fatal(err)
	}
	job, err = authority.CheckpointJob(ctx, job.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseLost})
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != store.JobPhaseLost || job.CommandState != nil || job.ExitCode != nil || job.FinalEventSequence != nil || job.OutputComplete || job.OutputTruncated || job.TeardownState != store.JobTeardownPending || job.TeardownReason != "" {
		t.Fatalf("seed pre-start lost job=%+v err=%v", job, err)
	}
	if session.State != domain.SessionStateLost || session.RuntimeGeneration != "" {
		t.Fatalf("seed pre-start lost session=%+v err=%v", session, err)
	}
	if _, err := authority.GetCommand(ctx, request.Acceptance.CommandID); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("seed pre-start command lookup=%v, want %v", err, store.ErrCommandNotFound)
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
