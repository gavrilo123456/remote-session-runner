package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestConfirmLostRuntimeRecoveryReleasesBothCapacityRecords(t *testing.T) {
	authority, sessionID, commandID := pLostRecoveryFixture(t)
	if err := authority.ConfirmLostRuntimeRecovery(context.Background(), sessionID, commandID); err != nil {
		t.Fatal(err)
	}
	slot, err := authority.GetCommandSlot(context.Background(), commandID)
	if err != nil || slot.StopConfirmedAt == nil || slot.ReleasedAt == nil {
		t.Fatalf("command slot=%+v err=%v", slot, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), sessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("session reservation=%+v err=%v", reservation, err)
	}
	pair := LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID}
	pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background())
	if err != nil || len(pending) != 1 || pending[0] != pair {
		t.Fatalf("pending finalizations=%+v err=%v, want [%+v]", pending, err, pair)
	}
	if err := authority.CompleteLostRuntimeRecoveryFinalization(context.Background(), pair); err != nil {
		t.Fatal(err)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("completed finalizations=%+v err=%v, want none", pending, err)
	}
	if err := authority.CompleteLostRuntimeRecoveryFinalization(context.Background(), pair); err != nil {
		t.Fatalf("idempotent finalization completion: %v", err)
	}
	if err := authority.ConfirmLostRuntimeRecovery(context.Background(), sessionID, commandID); err != nil {
		t.Fatalf("idempotent recovery: %v", err)
	}
}

func TestConfirmLostRuntimeRecoveryRollsBackBothCapacityRecords(t *testing.T) {
	authority, sessionID, commandID := pLostRecoveryFixture(t)
	trigger := fmt.Sprintf(`
CREATE TRIGGER abort_lost_recovery_reservation
BEFORE UPDATE OF cleanup_confirmed_at ON exec_capacity_reservations
WHEN NEW.session_id = %q
BEGIN
  SELECT RAISE(ABORT, 'fixture reservation update failure');
END`, string(sessionID))
	if _, err := authority.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmLostRuntimeRecovery(context.Background(), sessionID, commandID); err == nil {
		t.Fatal("recovery unexpectedly succeeded with abort trigger")
	}
	slot, err := authority.GetCommandSlot(context.Background(), commandID)
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("command slot was partially released: %+v err=%v", slot, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), sessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("session reservation was partially released: %+v err=%v", reservation, err)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("rolled-back recovery finalizations=%+v err=%v, want none", pending, err)
	}
}

func TestConfirmLostRuntimeRecoveryRollsBackWhenUpdateIsIgnored(t *testing.T) {
	cases := []struct {
		name      string
		trigger   string
		commandID bool
	}{
		{
			name:      "command slot",
			commandID: true,
			trigger: `
CREATE TRIGGER ignore_lost_recovery_command
BEFORE UPDATE OF stop_confirmed_at ON exec_command_slots
WHEN NEW.command_id = %q
BEGIN
  SELECT RAISE(IGNORE);
END`,
		},
		{
			name: "session reservation",
			trigger: `
CREATE TRIGGER ignore_lost_recovery_reservation
BEFORE UPDATE OF cleanup_confirmed_at ON exec_capacity_reservations
WHEN NEW.session_id = %q
BEGIN
  SELECT RAISE(IGNORE);
END`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			authority, sessionID, commandID := pLostRecoveryFixture(t)
			matchID := string(sessionID)
			if test.commandID {
				matchID = string(commandID)
			}
			if _, err := authority.db.Exec(fmt.Sprintf(test.trigger, matchID)); err != nil {
				t.Fatal(err)
			}
			if err := authority.ConfirmLostRuntimeRecovery(context.Background(), sessionID, commandID); !errors.Is(err, ErrLostRuntimeRecoveryNotReleasable) {
				t.Fatalf("ignored update recovery error=%v, want unreleasable", err)
			}
			slot, err := authority.GetCommandSlot(context.Background(), commandID)
			if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
				t.Fatalf("command slot was partially released: %+v err=%v", slot, err)
			}
			reservation, err := authority.GetSessionReservation(context.Background(), sessionID)
			if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
				t.Fatalf("session reservation was partially released: %+v err=%v", reservation, err)
			}
			if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 0 {
				t.Fatalf("ignored-update recovery finalizations=%+v err=%v, want none", pending, err)
			}
		})
	}
}

func pLostRecoveryFixture(t *testing.T) (*AuthorityStore, domain.SessionID, domain.CommandID) {
	t.Helper()
	clock := &p019Clock{value: time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	sessionID := p019RuntimeReadySession(t, authority, "session-store-lost-recovery", "key-store-lost-recovery")
	command := p019Command(t, authority, sessionID, "command-store-lost-recovery", "key-command-store-lost-recovery")
	if _, err := authority.StartNextEligibleCommand(context.Background(), DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateLost, "lost_command", false); err != nil {
		t.Fatal(err)
	}
	return authority, sessionID, command.CommandID
}
