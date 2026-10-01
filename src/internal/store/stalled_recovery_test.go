package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestConfirmLostRuntimeRecoveryBatchReleasesExactPairsAtomically(t *testing.T) {
	authority, firstSessionID, firstCommandID := pLostRecoveryFixture(t)
	secondSessionID, secondCommandID := pStalledRecoveryLostPair(t, authority, "second")

	pairs := []LostRuntimeRecoveryPair{
		{SessionID: firstSessionID, CommandID: firstCommandID},
		{SessionID: secondSessionID, CommandID: secondCommandID},
	}
	if err := authority.ConfirmLostRuntimeRecoveryBatch(context.Background(), pairs); err != nil {
		t.Fatal(err)
	}
	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairReleased(t, authority, pair)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 0 {
		t.Fatalf("live slots=%d err=%v, want 0", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 0 {
		t.Fatalf("live reservations=%d err=%v, want 0", reservations, err)
	}
}

func TestConfirmLostRuntimeRecoveryBatchRejectsUnselectedLivePairWithoutWrites(t *testing.T) {
	authority, firstSessionID, firstCommandID := pLostRecoveryFixture(t)
	secondSessionID, secondCommandID := pStalledRecoveryLostPair(t, authority, "second")
	thirdSessionID, thirdCommandID := pStalledRecoveryLostPair(t, authority, "third")
	pairs := []LostRuntimeRecoveryPair{
		{SessionID: firstSessionID, CommandID: firstCommandID},
		{SessionID: secondSessionID, CommandID: secondCommandID},
	}
	if err := authority.ConfirmLostRuntimeRecoveryBatch(context.Background(), pairs); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("batch recovery error=%v, want unreleasable", err)
	}
	for _, pair := range append(pairs, LostRuntimeRecoveryPair{SessionID: thirdSessionID, CommandID: thirdCommandID}) {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
}

func TestConfirmLostRuntimeRecoveryBatchRollsBackWhenLaterPairWriteFails(t *testing.T) {
	authority, firstSessionID, firstCommandID := pLostRecoveryFixture(t)
	secondSessionID, secondCommandID := pStalledRecoveryLostPair(t, authority, "second")
	trigger := fmt.Sprintf(`
CREATE TRIGGER abort_stalled_recovery_second_pair
BEFORE UPDATE OF cleanup_confirmed_at ON exec_capacity_reservations
WHEN NEW.session_id = %q
BEGIN
  SELECT RAISE(ABORT, 'fixture second pair failure');
END`, string(secondSessionID))
	if _, err := authority.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	pairs := []LostRuntimeRecoveryPair{
		{SessionID: firstSessionID, CommandID: firstCommandID},
		{SessionID: secondSessionID, CommandID: secondCommandID},
	}
	if err := authority.ConfirmLostRuntimeRecoveryBatch(context.Background(), pairs); err == nil {
		t.Fatal("batch recovery unexpectedly succeeded")
	}
	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
}

func TestConfirmLostRuntimeRecoveryBatchRejectsWrongHostAndEmptyInput(t *testing.T) {
	authority, sessionID, commandID := pLostRecoveryFixture(t)
	if err := authority.ConfirmLostRuntimeRecoveryBatch(context.Background(), nil); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("empty batch error=%v, want unreleasable", err)
	}
	if _, err := authority.db.Exec(`UPDATE exec_command_slots SET host_key = 'other-host' WHERE command_id = ?`, string(commandID)); err != nil {
		t.Fatal(err)
	}
	pair := LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID}
	if err := authority.ConfirmLostRuntimeRecoveryBatch(context.Background(), []LostRuntimeRecoveryPair{pair}); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("wrong-host batch error=%v, want unreleasable", err)
	}
	pStalledRecoveryAssertLostPairRetained(t, authority, pair)
}

func TestSettleClosedCancelledOneOffJobsIsAtomicAndNeverChangesCommandHistory(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	first := pStalledClosedCancelledJob(t, authority, "first")
	second := pStalledClosedCancelledJob(t, authority, "second")
	checked, err := authority.CheckClosedCancelledOneOffJobs(context.Background(), []domain.JobID{first.JobID, second.JobID})
	if err != nil || len(checked) != 2 || checked[0].AlreadySettled || checked[1].AlreadySettled {
		t.Fatalf("pre-settlement check=%+v err=%v", checked, err)
	}
	beforeFirst, err := authority.ListCommandEvents(context.Background(), first.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	results, err := authority.SettleClosedCancelledOneOffJobs(context.Background(), []domain.JobID{first.JobID, second.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].AlreadySettled || results[1].AlreadySettled {
		t.Fatalf("settlement results=%+v", results)
	}
	for _, original := range []JobRecord{first, second} {
		settled, err := authority.GetJob(context.Background(), original.JobID)
		if err != nil || settled.Phase != JobPhaseComplete || settled.CommandState == nil || *settled.CommandState != domain.CommandStateCancelled || settled.ExitCode != nil || settled.FinalEventSequence == nil || *settled.FinalEventSequence != 2 || !settled.OutputComplete || settled.OutputTruncated || settled.TeardownState != JobTeardownClosed || settled.TeardownReason != "runtime_closed" {
			t.Fatalf("settled job=%+v err=%v", settled, err)
		}
	}
	afterFirst, err := authority.ListCommandEvents(context.Background(), first.CommandID)
	if err != nil || len(afterFirst) != len(beforeFirst) || afterFirst[0].Type != "command_queued" || afterFirst[1].Type != "command_cancelled" {
		t.Fatalf("command events changed: before=%+v after=%+v err=%v", beforeFirst, afterFirst, err)
	}
	repeat, err := authority.SettleClosedCancelledOneOffJobs(context.Background(), []domain.JobID{first.JobID, second.JobID})
	if err != nil || len(repeat) != 2 || !repeat[0].AlreadySettled || !repeat[1].AlreadySettled {
		t.Fatalf("repeat settlement=%+v err=%v", repeat, err)
	}
	checked, err = authority.CheckClosedCancelledOneOffJobs(context.Background(), []domain.JobID{first.JobID, second.JobID})
	if err != nil || len(checked) != 2 || !checked[0].AlreadySettled || !checked[1].AlreadySettled {
		t.Fatalf("post-settlement check=%+v err=%v", checked, err)
	}
}

func TestSettleClosedCancelledOneOffJobsRejectsHistoryAndRollsBackBatch(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	first := pStalledClosedCancelledJob(t, authority, "first")
	second := pStalledClosedCancelledJob(t, authority, "second")
	if _, err := authority.db.Exec(`
INSERT INTO exec_command_events(command_id, sequence, event_type, payload, byte_count, occurred_at)
VALUES (?, 3, 'command_started', X'', 0, ?)
`, string(second.CommandID), formatStoredTime(clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SettleClosedCancelledOneOffJobs(context.Background(), []domain.JobID{first.JobID, second.JobID}); !errors.Is(err, ErrClosedCancelledJobSettlementNotEligible) {
		t.Fatalf("settlement error=%v, want not eligible", err)
	}
	for _, jobID := range []domain.JobID{first.JobID, second.JobID} {
		job, err := authority.GetJob(context.Background(), jobID)
		if err != nil || job.Phase != JobPhaseAwaitingCommand {
			t.Fatalf("job after rejected batch=%+v err=%v", job, err)
		}
	}
}

func pStalledRecoveryLostPair(t *testing.T, authority *AuthorityStore, suffix string) (domain.SessionID, domain.CommandID) {
	t.Helper()
	sessionID := domain.SessionID("session-stalled-recovery-" + suffix)
	commandID := domain.CommandID("command-stalled-recovery-" + suffix)
	p019ReadySession(t, authority, sessionID, "key-stalled-recovery-session-"+suffix)
	command := p019Command(t, authority, sessionID, commandID, "key-stalled-recovery-command-"+suffix)
	if _, err := authority.StartNextEligibleCommand(context.Background(), DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateLost, "lost_command", false); err != nil {
		t.Fatal(err)
	}
	return sessionID, commandID
}

func pStalledRecoveryAssertLostPairRetained(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
	t.Helper()
	slot, err := authority.GetCommandSlot(context.Background(), pair.CommandID)
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("retained slot=%+v err=%v", slot, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), pair.SessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("retained reservation=%+v err=%v", reservation, err)
	}
}

func pStalledRecoveryAssertLostPairReleased(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
	t.Helper()
	slot, err := authority.GetCommandSlot(context.Background(), pair.CommandID)
	if err != nil || slot.StopConfirmedAt == nil || slot.ReleasedAt == nil {
		t.Fatalf("released slot=%+v err=%v", slot, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), pair.SessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("released reservation=%+v err=%v", reservation, err)
	}
}

func pStalledClosedCancelledJob(t *testing.T, authority *AuthorityStore, suffix string) JobRecord {
	t.Helper()
	input := p024Acceptance(t, "job-stalled-recovery-"+suffix, "session-stalled-job-"+suffix, "command-stalled-job-"+suffix, "run-stalled-job-"+suffix, "echo must-never-run")
	job, duplicate, err := authority.AcceptJob(context.Background(), input)
	if err != nil || duplicate {
		t.Fatalf("accept job=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	if _, _, err := authority.AcceptSessionCreate(context.Background(), p013Acceptance(t, string(job.SessionID), "create-stalled-job-"+suffix, "linux-dev")); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), job.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command := p019Command(t, authority, job.SessionID, job.CommandID, "command-stalled-job-"+suffix)
	job, err = authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseCreatingSession, NextPhase: JobPhaseAcceptingCommand})
	if err != nil {
		t.Fatal(err)
	}
	job, err = authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseAcceptingCommand, NextPhase: JobPhaseAwaitingCommand, Command: &command})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), job.SessionID, domain.SessionStateClosing, "close_requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), job.SessionID, domain.SessionStateClosed, "runtime_closed"); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmSessionCleanup(context.Background(), job.SessionID); err != nil {
		t.Fatal(err)
	}
	return job
}
