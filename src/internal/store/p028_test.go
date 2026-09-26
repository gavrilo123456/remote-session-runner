package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP028D09OutputAndIdempotencyRetentionKeepMetadataUntilNinetyDays(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	session := p019ReadySession(t, authority, "session-p028-retention", "key-p028-retention-session")
	command := p019Command(t, authority, session, "command-p028-retention", "key-p028-retention-command")
	started, err := authority.StartNextEligibleCommand(context.Background(), DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID != command.CommandID {
		t.Fatalf("started command = %+v", started)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("retained output"), ByteCount: int64(len("retained output"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}, domain.SessionStateReady, "command_complete", true); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), session, domain.SessionStateClosing, "close_requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), session, domain.SessionStateClosed, "runtime_closed"); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmSessionCleanup(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	clock.Advance(31 * 24 * time.Hour)
	report, err := authority.CollectGarbage(context.Background(), GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.CommandsOutputExpired != 1 || report.CommandEventsDeleted != 4 || report.CommandsDeleted != 0 || report.SessionsDeleted != 0 || report.IdempotencyRecordsDeleted != 0 {
		t.Fatalf("30-day GC report = %+v", report)
	}
	gcCommand, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if gcCommand.OutputComplete || gcCommand.OutputUnavailableReason != "retention_expired" {
		t.Fatalf("expired command metadata = %+v", gcCommand)
	}
	if events, err := authority.ListCommandEvents(context.Background(), command.CommandID); err != nil || len(events) != 0 {
		t.Fatalf("expired command events = %+v err=%v, want empty", events, err)
	}

	clock.Advance(60 * 24 * time.Hour)
	report, err = authority.GarbageCollect(context.Background(), GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.IdempotencyRecordsDeleted != 2 || report.CommandsDeleted != 1 || report.SessionsDeleted != 1 {
		t.Fatalf("90-day GC report = %+v", report)
	}
	if _, err := authority.GetCommand(context.Background(), command.CommandID); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("expired command lookup = %v, want ErrCommandNotFound", err)
	}
	if _, err := authority.GetSession(context.Background(), session); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expired session lookup = %v, want ErrSessionNotFound", err)
	}
}

func TestP028D15LiveSessionAndCommandSlotsPinMetadataBeyondNinetyDays(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	session := p019ReadySession(t, authority, "session-p028-live-pin", "key-p028-live-pin-session")
	command := p019Command(t, authority, session, "command-p028-live-pin", "key-p028-live-pin-command")
	if _, err := authority.StartNextEligibleCommand(context.Background(), DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), session, domain.SessionStateLost, "runtime_lost"); err != nil {
		t.Fatal(err)
	}

	clock.Advance(91 * 24 * time.Hour)
	report, err := authority.CollectGarbage(context.Background(), GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.CommandsDeleted != 0 || report.SessionsDeleted != 0 {
		t.Fatalf("live-pin GC deleted metadata: %+v", report)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live command slots = %d err=%v, want 1", got, err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live session reservations = %d err=%v, want 1", got, err)
	}
	if _, err := authority.GetSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetCommand(context.Background(), command.CommandID); err != nil {
		t.Fatal(err)
	}

	if err := authority.ConfirmCommandSlotRelease(context.Background(), command.CommandID); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmSessionCleanup(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	report, err = authority.CollectGarbage(context.Background(), GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.CommandsDeleted != 1 || report.SessionsDeleted != 1 {
		t.Fatalf("post-confirmation GC report = %+v", report)
	}
	if _, err := authority.GetSession(context.Background(), session); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("pinned session after cleanup = %v, want ErrSessionNotFound", err)
	}
	if _, err := authority.GetCommand(context.Background(), command.CommandID); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("pinned command after cleanup = %v, want ErrCommandNotFound", err)
	}
}
