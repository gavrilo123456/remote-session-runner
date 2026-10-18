package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestMixedRuntimeRecoveryChecksAndAtomicallyReleasesExactSet(t *testing.T) {
	ctx := context.Background()
	authority, lostSession, lostCommand := pLostRecoveryFixture(t)
	teardown, jobID := completedTeardownFixtureWithOutcome(t, authority, "mixed-store", domain.CommandStateFailed, 2)
	before := completedTeardownHistory(t, authority, teardown, jobID)
	lost := []LostRuntimeRecoveryPair{{SessionID: lostSession, CommandID: lostCommand}}
	teardowns := []CompletedTeardownRecoveryPair{teardown}
	checked, err := authority.CheckMixedRuntimeRecoverySet(ctx, lost, teardowns)
	if err != nil || len(checked.Lost) != 1 || len(checked.Teardown) != 1 || checked.Lost[0].AlreadyRecovered || checked.Teardown[0].AlreadyRecovered {
		t.Fatalf("mixed inventory=%+v err=%v", checked, err)
	}
	if err := authority.ConfirmMixedRuntimeRecoverySet(ctx, lost, teardowns); err != nil {
		t.Fatal(err)
	}
	if got, err := authority.CountLiveCommandSlots(ctx); err != nil || got != 0 {
		t.Fatalf("live command slots=%d err=%v", got, err)
	}
	if got, err := authority.CountLiveSessionReservations(ctx); err != nil || got != 0 {
		t.Fatalf("live session reservations=%d err=%v", got, err)
	}
	if after := completedTeardownHistory(t, authority, teardown, jobID); !reflect.DeepEqual(before, after) {
		t.Fatalf("completed failure history changed during mixed release: before=%#v after=%#v", before, after)
	}
	lostResult, err := authority.GetCommand(ctx, lostCommand)
	if err != nil || lostResult.State != domain.CommandStateLost || lostResult.OutputComplete {
		t.Fatalf("lost command changed=%+v err=%v", lostResult, err)
	}
	if err := authority.ConfirmMixedRuntimeRecoverySet(ctx, lost, teardowns); err != nil {
		t.Fatalf("idempotent mixed release retry: %v", err)
	}
}

func TestMixedRuntimeRecoveryRejectsInconsistentTerminalEvidence(t *testing.T) {
	ctx := context.Background()
	authority, lostSession, lostCommand := pLostRecoveryFixture(t)
	teardown, _ := completedTeardownFixtureWithOutcome(t, authority, "mixed-corrupt", domain.CommandStateFailed, 2)
	if _, err := authority.db.ExecContext(ctx, `UPDATE exec_command_events SET event_type='command_succeeded' WHERE command_id=? AND sequence=(SELECT MAX(sequence) FROM exec_command_events WHERE command_id=?)`, string(teardown.CommandID), string(teardown.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckMixedRuntimeRecoverySet(ctx,
		[]LostRuntimeRecoveryPair{{SessionID: lostSession, CommandID: lostCommand}},
		[]CompletedTeardownRecoveryPair{teardown},
	); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
		t.Fatalf("mismatched final event error=%v, want %v", err, ErrCompletedTeardownRecoveryNotReleasable)
	}
	if got, err := authority.CountLiveCommandSlots(ctx); err != nil || got != 1 {
		t.Fatalf("inconsistent evidence changed live slots=%d err=%v", got, err)
	}
	if got, err := authority.CountLiveSessionReservations(ctx); err != nil || got != 2 {
		t.Fatalf("inconsistent evidence changed live reservations=%d err=%v", got, err)
	}
}
