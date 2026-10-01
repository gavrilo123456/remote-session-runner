package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestBUG008QueuePreservingLostRecoveryReleasesOnlySelectedLostPairs(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	pairs := pBUG008LostPairs(t, authority, "success")
	firstQueued := pBUG008QueuedOneOff(t, authority, "first")
	secondQueued := pBUG008QueuedOneOff(t, authority, "second")
	firstBefore := pBUG008QueuedOneOffSnapshot(t, authority, firstQueued)
	secondBefore := pBUG008QueuedOneOffSnapshot(t, authority, secondQueued)

	if err := authority.CheckLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), pairs); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), pairs); err != nil {
		t.Fatal(err)
	}

	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairReleased(t, authority, pair)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 0 {
		t.Fatalf("live slots=%d err=%v, want 0", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 2 {
		t.Fatalf("live reservations=%d err=%v, want two queued one-offs only", reservations, err)
	}
	pBUG008AssertQueuedOneOffUnchanged(t, authority, firstQueued, firstBefore)
	pBUG008AssertQueuedOneOffUnchanged(t, authority, secondQueued, secondBefore)

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), pairs); err != nil {
		t.Fatalf("idempotent confirmation: %v", err)
	}
	pBUG008AssertQueuedOneOffUnchanged(t, authority, firstQueued, firstBefore)
	pBUG008AssertQueuedOneOffUnchanged(t, authority, secondQueued, secondBefore)
}

func TestBUG008QueuePreservingLostRecoveryRefusesFreeCommandCapacityWithoutWrites(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 18, 0, 30, 0, time.UTC)}
	authority := newP019Store(t, clock)
	firstSessionID, firstCommandID := pStalledRecoveryLostPair(t, authority, "bug008-free-first")
	secondSessionID, secondCommandID := pStalledRecoveryLostPair(t, authority, "bug008-free-second")
	queued := pBUG008QueuedOneOff(t, authority, "free-capacity")
	before := pBUG008QueuedOneOffSnapshot(t, authority, queued)
	pairs := []LostRuntimeRecoveryPair{
		{SessionID: firstSessionID, CommandID: firstCommandID},
		{SessionID: secondSessionID, CommandID: secondCommandID},
	}

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), pairs); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("confirmation error=%v, want unreleasable", err)
	}
	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
	pBUG008AssertQueuedOneOffUnchanged(t, authority, queued, before)
}

func TestBUG008QueuePreservingLostRecoveryRejectsUnsafeStateWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(t *testing.T, authority *AuthorityStore, pairs []LostRuntimeRecoveryPair)
		selectPairs func([]LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair
	}{
		{
			name: "unselected live lost slot",
			selectPairs: func(pairs []LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair {
				return pairs[:len(pairs)-1]
			},
		},
		{
			name: "running command",
			mutate: func(t *testing.T, authority *AuthorityStore, _ []LostRuntimeRecoveryPair) {
				t.Helper()
				sessionID := p019ReadySession(t, authority, "session-bug008-running", "key-bug008-running")
				command := p019Command(t, authority, sessionID, "command-bug008-running", "key-bug008-running")
				if _, err := authority.db.Exec(`UPDATE exec_commands SET state = ? WHERE command_id = ?`, string(domain.CommandStateRunning), string(command.CommandID)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ordinary queued reservation without one-off job",
			mutate: func(t *testing.T, authority *AuthorityStore, _ []LostRuntimeRecoveryPair) {
				t.Helper()
				sessionID := p019ReadySession(t, authority, "session-bug008-ordinary-queued", "key-bug008-ordinary-queued")
				p019Command(t, authority, sessionID, "command-bug008-ordinary-queued", "key-bug008-ordinary-queued")
			},
		},
		{
			name: "nonterminal job before queued command",
			mutate: func(t *testing.T, authority *AuthorityStore, _ []LostRuntimeRecoveryPair) {
				t.Helper()
				input := p024Acceptance(t, "job-bug008-creating", "session-bug008-creating", "command-bug008-creating", "key-bug008-creating", "echo never-run")
				if _, duplicate, err := authority.AcceptJob(context.Background(), input); err != nil || duplicate {
					t.Fatalf("accept creating job duplicate=%v err=%v", duplicate, err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &p019Clock{value: time.Date(2026, 10, 1, 18, 1, 0, 0, time.UTC)}
			authority := newP019Store(t, clock)
			pairs := pBUG008LostPairs(t, authority, "reject")
			if test.mutate != nil {
				test.mutate(t, authority, pairs)
			}
			queued := pBUG008QueuedOneOff(t, authority, "reject")
			before := pBUG008QueuedOneOffSnapshot(t, authority, queued)
			selected := pairs
			if test.selectPairs != nil {
				selected = test.selectPairs(pairs)
			}

			if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), selected); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
				t.Fatalf("confirmation error=%v, want unreleasable", err)
			}
			for _, pair := range pairs {
				pStalledRecoveryAssertLostPairRetained(t, authority, pair)
			}
			pBUG008AssertQueuedOneOffUnchanged(t, authority, queued, before)
		})
	}
}

func TestBUG008QueuePreservingLostRecoveryRejectsSchedulerEligibleDirectCommandWithReleasedReservation(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 18, 1, 30, 0, time.UTC)}
	authority := newP019Store(t, clock)
	pairs := pBUG008LostPairs(t, authority, "released-direct")
	queued := pBUG008QueuedOneOff(t, authority, "released-direct-safe")
	queuedBefore := pBUG008QueuedOneOffSnapshot(t, authority, queued)

	directSessionID := p019ReadySession(t, authority, "session-bug008-released-direct", "key-bug008-released-direct")
	directCommand := p019Command(t, authority, directSessionID, "command-bug008-released-direct", "key-bug008-released-direct")
	if err := authority.ConfirmSessionCleanup(context.Background(), directSessionID); err != nil {
		t.Fatalf("release direct-command reservation: %v", err)
	}

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), pairs); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
		t.Fatalf("confirmation error=%v, want unreleasable", err)
	}
	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
	pBUG008AssertQueuedOneOffUnchanged(t, authority, queued, queuedBefore)

	directSession, err := authority.GetSession(context.Background(), directSessionID)
	if err != nil || directSession.State != domain.SessionStateReady {
		t.Fatalf("direct session=%+v err=%v, want ready", directSession, err)
	}
	directAfter, err := authority.GetCommand(context.Background(), directCommand.CommandID)
	if err != nil || directAfter.State != domain.CommandStateQueued {
		t.Fatalf("direct command=%+v err=%v, want queued", directAfter, err)
	}
	directReservation, err := authority.GetSessionReservation(context.Background(), directSessionID)
	if err != nil || directReservation.CleanupConfirmedAt == nil || directReservation.ReleasedAt == nil {
		t.Fatalf("direct reservation=%+v err=%v, want released", directReservation, err)
	}
}

func TestBUG008QueuePreservingLostRecoveryRejectsDuplicateMismatchedAndPartialPairsWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name  string
		pairs func(LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair
		setup func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair)
	}{
		{
			name: "duplicate",
			pairs: func(pair LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair {
				return []LostRuntimeRecoveryPair{pair, pair}
			},
		},
		{
			name: "mismatched session",
			pairs: func(pair LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair {
				return []LostRuntimeRecoveryPair{{SessionID: "session-bug008-mismatch", CommandID: pair.CommandID}}
			},
		},
		{
			name: "partial paired release",
			setup: func(t *testing.T, authority *AuthorityStore, pair LostRuntimeRecoveryPair) {
				t.Helper()
				if err := authority.ConfirmCommandSlotRelease(context.Background(), pair.CommandID); err != nil {
					t.Fatal(err)
				}
			},
			pairs: func(pair LostRuntimeRecoveryPair) []LostRuntimeRecoveryPair {
				return []LostRuntimeRecoveryPair{pair}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &p019Clock{value: time.Date(2026, 10, 1, 18, 2, 0, 0, time.UTC)}
			authority := newP019Store(t, clock)
			sessionID, commandID := pStalledRecoveryLostPair(t, authority, "bug008-invalid")
			queued := pBUG008QueuedOneOff(t, authority, "invalid")
			pair := LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID}
			before := pBUG008QueuedOneOffSnapshot(t, authority, queued)
			if test.setup != nil {
				test.setup(t, authority, pair)
			}

			if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), test.pairs(pair)); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
				t.Fatalf("confirmation error=%v, want unreleasable", err)
			}
			pBUG008AssertQueuedOneOffUnchanged(t, authority, queued, before)
			if test.name != "partial paired release" {
				pStalledRecoveryAssertLostPairRetained(t, authority, pair)
			}
		})
	}
}

func TestBUG008QueuePreservingLostRecoveryRollsBackSelectedPairsTogether(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 18, 3, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	pairs := pBUG008LostPairs(t, authority, "rollback")
	firstQueued := pBUG008QueuedOneOff(t, authority, "rollback-first")
	secondQueued := pBUG008QueuedOneOff(t, authority, "rollback-second")
	firstBefore := pBUG008QueuedOneOffSnapshot(t, authority, firstQueued)
	secondBefore := pBUG008QueuedOneOffSnapshot(t, authority, secondQueued)
	trigger := fmt.Sprintf(`
CREATE TRIGGER abort_bug008_second_pair
BEFORE UPDATE OF cleanup_confirmed_at ON exec_capacity_reservations
WHEN NEW.session_id = %q
BEGIN
  SELECT RAISE(ABORT, 'fixture second pair failure');
END`, string(pairs[1].SessionID))
	if _, err := authority.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(context.Background(), pairs); err == nil {
		t.Fatal("confirmation unexpectedly succeeded")
	}
	for _, pair := range pairs {
		pStalledRecoveryAssertLostPairRetained(t, authority, pair)
	}
	pBUG008AssertQueuedOneOffUnchanged(t, authority, firstQueued, firstBefore)
	pBUG008AssertQueuedOneOffUnchanged(t, authority, secondQueued, secondBefore)
}

func pBUG008LostPairs(t *testing.T, authority *AuthorityStore, prefix string) []LostRuntimeRecoveryPair {
	t.Helper()
	pairs := make([]LostRuntimeRecoveryPair, 0, DefaultRunningCommandLimit)
	for index := 0; index < DefaultRunningCommandLimit; index++ {
		sessionID, commandID := pStalledRecoveryLostPair(t, authority, fmt.Sprintf("bug008-%s-%d", prefix, index))
		pairs = append(pairs, LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID})
	}
	return pairs
}

func pBUG008QueuedOneOff(t *testing.T, authority *AuthorityStore, suffix string) JobRecord {
	t.Helper()
	input := p024Acceptance(t,
		"job-bug008-queued-"+suffix,
		"session-bug008-queued-"+suffix,
		"command-bug008-queued-"+suffix,
		"key-bug008-queued-"+suffix,
		"echo never-run",
	)
	job, duplicate, err := authority.AcceptJob(context.Background(), input)
	if err != nil || duplicate {
		t.Fatalf("accept queued one-off=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	if _, duplicate, err := authority.AcceptSessionCreate(context.Background(), p013Acceptance(t, string(job.SessionID), "create-bug008-queued-"+suffix, "linux-dev")); err != nil || duplicate {
		t.Fatalf("create queued one-off session duplicate=%v err=%v", duplicate, err)
	}
	if _, err := authority.TransitionSession(context.Background(), job.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command := p019Command(t, authority, job.SessionID, job.CommandID, "command-bug008-queued-"+suffix)
	job, err = authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseCreatingSession, NextPhase: JobPhaseAcceptingCommand})
	if err != nil {
		t.Fatal(err)
	}
	job, err = authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseAcceptingCommand, NextPhase: JobPhaseAwaitingCommand, Command: &command})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

type bug008QueuedOneOffSnapshot struct {
	job     JobRecord
	session SessionRecord
	command CommandRecord
	events  []CommandEventRecord
}

func pBUG008QueuedOneOffSnapshot(t *testing.T, authority *AuthorityStore, job JobRecord) bug008QueuedOneOffSnapshot {
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
	return bug008QueuedOneOffSnapshot{job: storedJob, session: session, command: command, events: events}
}

func pBUG008AssertQueuedOneOffUnchanged(t *testing.T, authority *AuthorityStore, job JobRecord, before bug008QueuedOneOffSnapshot) {
	t.Helper()
	after := pBUG008QueuedOneOffSnapshot(t, authority, job)
	if after.job.JobID != before.job.JobID || after.job.SessionID != before.job.SessionID || after.job.CommandID != before.job.CommandID ||
		after.job.IdempotencyKey != before.job.IdempotencyKey || after.job.Phase != JobPhaseAwaitingCommand ||
		after.job.CommandState == nil || *after.job.CommandState != domain.CommandStateQueued ||
		after.job.FinalEventSequence != nil || after.job.ExitCode != nil || after.job.OutputComplete || after.job.OutputTruncated ||
		after.job.TeardownState != JobTeardownPending || after.job.TeardownReason != "" {
		t.Fatalf("queued one-off job changed: before=%+v after=%+v", before.job, after.job)
	}
	if after.session.SessionID != before.session.SessionID || after.session.State != domain.SessionStateReady ||
		after.command.CommandID != before.command.CommandID || after.command.SessionID != before.command.SessionID ||
		after.command.Ordinal != before.command.Ordinal || after.command.State != domain.CommandStateQueued ||
		after.command.FinalEventSequence != nil || after.command.ExitCode != nil || after.command.OutputComplete || after.command.OutputTruncated {
		t.Fatalf("queued one-off child changed: before session=%+v command=%+v after session=%+v command=%+v", before.session, before.command, after.session, after.command)
	}
	if len(after.events) != 1 || after.events[0].Sequence != 1 || after.events[0].Type != "command_queued" {
		t.Fatalf("queued one-off events changed: before=%+v after=%+v", before.events, after.events)
	}
	if _, err := authority.GetCommandSlot(context.Background(), job.CommandID); !errors.Is(err, ErrCommandSlotNotFound) {
		t.Fatalf("queued one-off slot=%v, want no slot", err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), job.SessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("queued one-off reservation=%+v err=%v", reservation, err)
	}
}
