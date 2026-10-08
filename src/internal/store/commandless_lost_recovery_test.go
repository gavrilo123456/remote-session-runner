package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestConfirmCommandlessLostRuntimeRecoveryReleasesOnlySessionReservation(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	recovery := pCommandlessLostRecoveryFixture(t, authority, "single")

	if slots, err := authority.CountLiveCommandSlots(ctx); err != nil || slots != 0 {
		t.Fatalf("live command slots=%d err=%v, want 0", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(ctx); err != nil || reservations != 1 {
		t.Fatalf("live session reservations=%d err=%v, want 1", reservations, err)
	}
	if err := authority.ConfirmLostRuntimeRecoverySet(ctx, nil, []domain.SessionID{recovery.SessionID}); err != nil {
		t.Fatal(err)
	}

	reservation, err := authority.GetSessionReservation(ctx, recovery.SessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("released commandless session reservation=%+v err=%v", reservation, err)
	}
	if slots, err := authority.CountLiveCommandSlots(ctx); err != nil || slots != 0 {
		t.Fatalf("live command slots after release=%d err=%v, want 0", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(ctx); err != nil || reservations != 0 {
		t.Fatalf("live session reservations after release=%d err=%v, want 0", reservations, err)
	}
	pending, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx)
	if err != nil || len(pending) != 1 || pending[0].SessionID != recovery.SessionID || pending[0].JobID != recovery.JobID {
		t.Fatalf("pending commandless finalizations=%+v err=%v", pending, err)
	}
	job, err := authority.GetJob(ctx, recovery.JobID)
	if err != nil || job.Phase != JobPhaseLost || job.CommandState != nil || job.ExitCode != nil || job.FinalEventSequence != nil || job.OutputComplete || job.OutputTruncated || job.TeardownState != JobTeardownPending || job.TeardownReason != "" {
		t.Fatalf("commandless lost job changed=%+v err=%v", job, err)
	}
	if _, err := authority.GetCommand(ctx, recovery.CommandID); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("planned command lookup error=%v, want %v", err, ErrCommandNotFound)
	}
	if err := authority.CompleteCommandlessLostRuntimeRecoveryFinalization(ctx, recovery.SessionID); err != nil {
		t.Fatal(err)
	}
	if pending, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("completed commandless finalizations=%+v err=%v, want none", pending, err)
	}
	if err := authority.CompleteCommandlessLostRuntimeRecoveryFinalization(ctx, recovery.SessionID); err != nil {
		t.Fatalf("idempotent commandless finalization completion: %v", err)
	}
}

func TestConfirmLostRuntimeRecoverySetRollsBackMixedPairAndCommandlessRelease(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	pairSession, pairCommand := pCommandlessLostPair(t, authority, "mixed")
	commandless := pCommandlessLostRecoveryFixture(t, authority, "mixed")
	trigger := fmt.Sprintf(`
CREATE TRIGGER abort_commandless_lost_recovery
BEFORE UPDATE OF cleanup_confirmed_at ON exec_capacity_reservations
WHEN NEW.session_id = %q
BEGIN
  SELECT RAISE(ABORT, 'fixture commandless reservation update failure');
END`, string(commandless.SessionID))
	if _, err := authority.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmLostRuntimeRecoverySet(ctx,
		[]LostRuntimeRecoveryPair{{SessionID: pairSession, CommandID: pairCommand}},
		[]domain.SessionID{commandless.SessionID},
	); err == nil {
		t.Fatal("mixed recovery unexpectedly succeeded with abort trigger")
	}
	pairSlot, err := authority.GetCommandSlot(ctx, pairCommand)
	if err != nil || pairSlot.StopConfirmedAt != nil || pairSlot.ReleasedAt != nil {
		t.Fatalf("pair slot partially released=%+v err=%v", pairSlot, err)
	}
	for _, sessionID := range []domain.SessionID{pairSession, commandless.SessionID} {
		reservation, err := authority.GetSessionReservation(ctx, sessionID)
		if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
			t.Fatalf("session %s partially released=%+v err=%v", sessionID, reservation, err)
		}
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pair finalizations after rollback=%+v err=%v, want none", pending, err)
	}
	if pending, err := authority.ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("commandless finalizations after rollback=%+v err=%v, want none", pending, err)
	}
}

func TestCommandlessLostRuntimeRecoveryRejectsChangedTerminalShape(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	recovery := pCommandlessLostRecoveryFixture(t, authority, "reject")
	if _, err := authority.db.ExecContext(ctx, `UPDATE exec_jobs SET teardown_reason = 'unexpected' WHERE job_id = ?`, string(recovery.JobID)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckCommandlessLostRuntimeRecoveryBatch(ctx, []domain.SessionID{recovery.SessionID}); !errors.Is(err, ErrCommandlessLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("changed terminal shape error=%v, want %v", err, ErrCommandlessLostRuntimeRecoveryNotReleasable)
	}
	if err := authority.ConfirmLostRuntimeRecoverySet(ctx, nil, []domain.SessionID{recovery.SessionID}); !errors.Is(err, ErrCommandlessLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("changed terminal shape confirmation error=%v, want %v", err, ErrCommandlessLostRuntimeRecoveryNotReleasable)
	}
	reservation, err := authority.GetSessionReservation(ctx, recovery.SessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("changed terminal shape reservation=%+v err=%v, want retained", reservation, err)
	}
}

func TestCommandlessLostRuntimeRecoveryAllowsPreStartEmptyGenerationForRuntimeProof(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	recovery := pCommandlessLostRecoveryFixtureWithGeneration(t, authority, "pre-start", "")
	if recovery.Session.RuntimeGeneration != "" {
		t.Fatalf("pre-start recovery generation=%q, want empty", recovery.Session.RuntimeGeneration)
	}
	checked, err := authority.CheckCommandlessLostRuntimeRecoveryBatch(ctx, []domain.SessionID{recovery.SessionID})
	if err != nil || len(checked) != 1 || checked[0].SessionID != recovery.SessionID || checked[0].CommandID != recovery.CommandID {
		t.Fatalf("pre-start recovery check=%+v err=%v", checked, err)
	}
	reservation, err := authority.GetSessionReservation(ctx, recovery.SessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("pre-start recovery check changed reservation=%+v err=%v", reservation, err)
	}
}

func TestLostRuntimeRecoveryPairRejectsEmptyRuntimeGeneration(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	sessionID, commandID := pCommandlessLostPair(t, authority, "empty-generation")
	if _, err := authority.db.ExecContext(ctx, `UPDATE exec_sessions SET runtime_generation = '' WHERE session_id = ?`, string(sessionID)); err != nil {
		t.Fatal(err)
	}
	err := authority.ConfirmLostRuntimeRecoverySet(ctx, []LostRuntimeRecoveryPair{{SessionID: sessionID, CommandID: commandID}}, nil)
	if !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("empty-generation pair recovery err=%v, want %v", err, ErrLostRuntimeRecoveryNotReleasable)
	}
	reservation, reservationErr := authority.GetSessionReservation(ctx, sessionID)
	if reservationErr != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("empty-generation pair reservation=%+v err=%v, want retained", reservation, reservationErr)
	}
}

func TestPendingCommandlessLostRuntimeFinalizationPinsMetadataUntilComplete(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	recovery := pCommandlessLostRecoveryFixture(t, authority, "gc")
	if err := authority.ConfirmLostRuntimeRecoverySet(ctx, nil, []domain.SessionID{recovery.SessionID}); err != nil {
		t.Fatal(err)
	}

	clock.Advance(91 * 24 * time.Hour)
	report, err := authority.CollectGarbage(ctx, GarbageCollectionOptions{})
	if err != nil {
		t.Fatalf("GC with pending commandless finalization: %v", err)
	}
	if report.JobsDeleted != 0 || report.SessionsDeleted != 0 {
		t.Fatalf("GC deleted pending commandless-finalization metadata: %+v", report)
	}
	if _, err := authority.GetJob(ctx, recovery.JobID); err != nil {
		t.Fatalf("pending commandless job after GC: %v", err)
	}
	if _, err := authority.GetSession(ctx, recovery.SessionID); err != nil {
		t.Fatalf("pending commandless session after GC: %v", err)
	}

	if err := authority.CompleteCommandlessLostRuntimeRecoveryFinalization(ctx, recovery.SessionID); err != nil {
		t.Fatal(err)
	}
	report, err = authority.CollectGarbage(ctx, GarbageCollectionOptions{})
	if err != nil {
		t.Fatalf("GC after commandless finalization completion: %v", err)
	}
	if report.JobsDeleted != 1 || report.SessionsDeleted != 1 {
		t.Fatalf("GC after commandless finalization completion=%+v, want one job and session", report)
	}
	if _, err := authority.GetJob(ctx, recovery.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("commandless job after finalization completion=%v, want %v", err, ErrJobNotFound)
	}
	if _, err := authority.GetSession(ctx, recovery.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("commandless session after finalization completion=%v, want %v", err, ErrSessionNotFound)
	}
}

func pCommandlessLostRecoveryFixture(t *testing.T, authority *AuthorityStore, suffix string) CommandlessLostRuntimeRecovery {
	return pCommandlessLostRecoveryFixtureWithGeneration(t, authority, suffix, "generation-commandless-lost-recovery-"+suffix)
}

func pCommandlessLostRecoveryFixtureWithGeneration(t *testing.T, authority *AuthorityStore, suffix, generation string) CommandlessLostRuntimeRecovery {
	t.Helper()
	ctx := context.Background()
	jobID := domain.JobID("job-commandless-lost-recovery-" + suffix)
	sessionID := domain.SessionID("sess-commandless-lost-recovery-" + suffix)
	commandID := domain.CommandID("cmd-commandless-lost-recovery-" + suffix)
	job, duplicate, err := authority.AcceptJob(ctx, p024Acceptance(t, string(jobID), string(sessionID), string(commandID), "key-commandless-lost-recovery-"+suffix, "printf commandless"))
	if err != nil || duplicate {
		t.Fatalf("accept commandless job=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	created, duplicate, err := authority.AcceptSessionCreate(ctx, p013Acceptance(t, string(sessionID), "key-commandless-lost-session-"+suffix, "linux-dev"))
	if err != nil || duplicate {
		t.Fatalf("accept commandless session=%+v duplicate=%v err=%v", created, duplicate, err)
	}
	if _, err := authority.CompleteSessionCreation(ctx, sessionID, domain.SessionStateLost, generation, "", "runtime_cleanup_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckpointJob(ctx, jobID, JobCheckpoint{ExpectedPhase: JobPhaseCreatingSession, NextPhase: JobPhaseLost}); err != nil {
		t.Fatal(err)
	}
	recoveries, err := authority.CheckCommandlessLostRuntimeRecoveryBatch(ctx, []domain.SessionID{sessionID})
	if err != nil || len(recoveries) != 1 {
		t.Fatalf("read commandless recovery=%+v err=%v", recoveries, err)
	}
	return recoveries[0]
}

func pCommandlessLostRecoveryStore(t *testing.T) *AuthorityStore {
	t.Helper()
	return newP019Store(t, &p019Clock{value: time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)})
}

func pCommandlessLostPair(t *testing.T, authority *AuthorityStore, suffix string) (domain.SessionID, domain.CommandID) {
	t.Helper()
	ctx := context.Background()
	sessionID := p019RuntimeReadySession(t, authority, domain.SessionID("sess-commandless-pair-"+suffix), "key-commandless-pair-"+suffix)
	command := p019Command(t, authority, sessionID, domain.CommandID("cmd-commandless-pair-"+suffix), "key-commandless-pair-command-"+suffix)
	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(ctx, CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateLost, "lost_command", false); err != nil {
		t.Fatal(err)
	}
	return sessionID, command.CommandID
}
