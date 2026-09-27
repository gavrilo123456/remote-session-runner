package mailbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP092M07RebuildsFrozenMailboxEventsAfterOutputExpiry(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC)
	now := base
	root := testfixture.New(t)
	db, err := store.Open(ctx, filepath.Join(root.Path(), "state", "p092-mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	command := p091CreateTerminalCommand(t, authority)
	mailboxRoot := p081MailboxRoot(t)
	outbox, err := NewOutbox(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := NewEventFiles(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	projector := Projector{Authority: authority, Outbox: outbox, EventFiles: eventFiles}
	p091PublishTerminalCommandResponse(t, authority, "req-p092-rebuild", command.CommandID)
	if err := projector.PublishCommand(ctx, "req-p092-rebuild", command.CommandID); err != nil {
		t.Fatal(err)
	}
	originalEvents, originalCursor, err := eventFiles.Read(command.CommandID)
	if err != nil || originalCursor != 4 {
		t.Fatalf("initial event projection cursor=%d err=%v", originalCursor, err)
	}
	originalResponse, err := outbox.Read("req-p092-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	eventPath, err := eventFiles.Path(command.CommandID)
	if err != nil {
		t.Fatal(err)
	}

	// A shorter fake retention interval places output expiry before the
	// response's ordinary seven-day cleanup deadline.
	now = base.Add(48 * time.Hour)
	report, err := authority.CollectGarbage(ctx, store.GarbageCollectionOptions{OutputRetention: 24 * time.Hour})
	if err != nil || report.CommandsOutputExpired != 1 || report.CommandEventsDeleted != 0 {
		t.Fatalf("output expiry report=%+v err=%v", report, err)
	}
	snapshot, err := (EventProjector{Authority: authority}).TerminalSnapshot(ctx, command.CommandID)
	if err != nil || snapshot.OutputUnavailableReason != "retention_expired" || snapshot.AvailableEventSequence != 0 || snapshot.EventsFile != "" || snapshot.OutputComplete {
		t.Fatalf("fresh terminal snapshot=%+v err=%v", snapshot, err)
	}
	if _, err := authority.ListCommandEvents(ctx, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventPath, []byte("corrupt event file"), MailboxFileMode); err != nil {
		t.Fatal(err)
	}

	// Constructing a new projector simulates restart. It uses only the stored
	// response reference and cursor to reconstruct the old event image.
	restarted := Projector{Authority: authority, Outbox: outbox, EventFiles: eventFiles}
	if err := restarted.PublishCommand(ctx, "req-p092-rebuild", command.CommandID); err != nil {
		t.Fatalf("rebuild frozen response after retention expiry: %v", err)
	}
	rebuiltEvents, rebuiltCursor, err := eventFiles.Read(command.CommandID)
	if err != nil || rebuiltCursor != originalCursor || string(rebuiltEvents) != string(originalEvents) {
		t.Fatalf("rebuilt event file cursor=%d err=%v bytes match=%v", rebuiltCursor, err, string(rebuiltEvents) == string(originalEvents))
	}
	rebuiltResponse, err := outbox.Read("req-p092-rebuild")
	if err != nil || string(rebuiltResponse) != string(originalResponse) {
		t.Fatalf("terminal response changed on repair: %q err=%v", rebuiltResponse, err)
	}

	now = base.Add(8 * 24 * time.Hour)
	report, err = authority.CollectGarbage(ctx, store.GarbageCollectionOptions{OutputRetention: 24 * time.Hour})
	if err != nil || report.CommandEventsDeleted != 4 {
		t.Fatalf("post-deadline payload cleanup report=%+v err=%v", report, err)
	}
	if err := restarted.PublishCommand(ctx, "req-p092-rebuild", command.CommandID); !errors.Is(err, store.ErrMailboxResponseExpired) {
		t.Fatalf("expired response rebuild error=%v, want ErrMailboxResponseExpired", err)
	}
}
