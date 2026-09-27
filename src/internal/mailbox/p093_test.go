package mailbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP093M07M09SharedResponseRebuildAcrossOutputExpiryAndLastCleanup(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	now := base
	root := testfixture.New(t)
	db, err := store.Open(ctx, filepath.Join(root.Path(), "state", "p093-mailbox.db"))
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
	// Publish just before the normal 30-day output boundary so the unacknowledged
	// response remains live after ordinary event retention expires.
	now = base.Add(29 * 24 * time.Hour)
	for _, requestID := range []string{"req-p093-acked", "req-p093-unacked"} {
		p091PublishTerminalCommandResponse(t, authority, requestID, command.CommandID)
		if err := projector.PublishCommand(ctx, requestID, command.CommandID); err != nil {
			t.Fatalf("publish %s: %v", requestID, err)
		}
	}
	originalEvents, cursor, err := eventFiles.Read(command.CommandID)
	if err != nil || cursor != 4 {
		t.Fatalf("initial shared event file cursor=%d err=%v", cursor, err)
	}
	originalResponse, err := outbox.Read("req-p093-unacked")
	if err != nil {
		t.Fatal(err)
	}
	eventPath, err := eventFiles.Path(command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	firstResponsePath, err := outbox.Path("req-p093-acked")
	if err != nil {
		t.Fatal(err)
	}
	secondResponsePath, err := outbox.Path("req-p093-unacked")
	if err != nil {
		t.Fatal(err)
	}

	now = base.Add(29*24*time.Hour + time.Hour)
	if _, err := authority.AcknowledgeMailboxExchange(ctx, store.MailboxAcknowledgement{
		RequestID: "req-p093-acked", ResponseRevision: 1, AvailableEventSequence: &cursor,
	}); err != nil {
		t.Fatal(err)
	}
	cleaner := ArtifactCleaner{Authority: authority, Outbox: outbox, EventFiles: eventFiles}
	// Expire ordinary output while the no-ACK response still pins the frozen
	// event prefix. New status must not inherit that response's cursor or claim
	// a complete answer.
	now = base.Add(store.DefaultOutputRetention + 2*time.Hour)
	gc, err := authority.CollectGarbage(ctx, store.GarbageCollectionOptions{})
	if err != nil || gc.CommandsOutputExpired != 1 || gc.CommandEventsDeleted != 0 {
		t.Fatalf("output-expiry GC=%+v err=%v", gc, err)
	}
	snapshot, err := (EventProjector{Authority: authority}).TerminalSnapshot(ctx, command.CommandID)
	if err != nil || snapshot.OutputComplete || snapshot.OutputUnavailableReason != "retention_expired" || snapshot.AvailableEventSequence != 0 || snapshot.EventsFile != "" || snapshot.FullAnswer("complete") {
		t.Fatalf("fresh expired terminal snapshot=%+v err=%v", snapshot, err)
	}
	if err := os.WriteFile(eventPath, []byte("corrupt P093 event file"), MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	now = base.Add(store.DefaultOutputRetention + 3*time.Hour)
	report, err := cleaner.Run(ctx)
	if err != nil || report.ResponsesRemoved != 1 || report.EventFilesRemoved != 0 {
		t.Fatalf("early ACK cleanup report=%+v err=%v", report, err)
	}
	assertPathAbsent(t, firstResponsePath)
	assertPathPresent(t, secondResponsePath)
	assertPathPresent(t, eventPath)

	// A fresh projector simulates restart. The still-live response rebuilds the
	// exact bytes through its stored cursor, without changing the response.
	restarted := Projector{Authority: authority, Outbox: outbox, EventFiles: eventFiles}
	if err := restarted.PublishCommand(ctx, "req-p093-unacked", command.CommandID); err != nil {
		t.Fatalf("rebuild response after normal output expiry: %v", err)
	}
	rebuiltEvents, rebuiltCursor, err := eventFiles.Read(command.CommandID)
	if err != nil || rebuiltCursor != cursor || !bytes.Equal(rebuiltEvents, originalEvents) {
		t.Fatalf("rebuilt event file cursor=%d err=%v bytes_match=%v", rebuiltCursor, err, bytes.Equal(rebuiltEvents, originalEvents))
	}
	rebuiltResponse, err := outbox.Read("req-p093-unacked")
	if err != nil || !bytes.Equal(rebuiltResponse, originalResponse) {
		t.Fatalf("frozen response changed: bytes_match=%v err=%v", bytes.Equal(rebuiltResponse, originalResponse), err)
	}

	// The event file remains until the final no-ACK deadline; then cleanup and
	// GC remove the physical file and its SQLite payload.
	now = base.Add(29*24*time.Hour + store.MailboxUnackedResponseLifetime)
	report, err = cleaner.Run(ctx)
	if err != nil || report.ResponsesRemoved != 1 || report.EventFilesRemoved != 1 {
		t.Fatalf("last-response cleanup report=%+v err=%v", report, err)
	}
	assertPathAbsent(t, secondResponsePath)
	assertPathAbsent(t, eventPath)
	gc, err = authority.CollectGarbage(ctx, store.GarbageCollectionOptions{})
	if err != nil || gc.CommandEventsDeleted != 4 {
		t.Fatalf("post-deadline payload GC=%+v err=%v", gc, err)
	}
	if err := restarted.PublishCommand(ctx, "req-p093-unacked", command.CommandID); !errors.Is(err, store.ErrMailboxResponseExpired) {
		t.Fatalf("expired response rebuild error=%v, want ErrMailboxResponseExpired", err)
	}
}
