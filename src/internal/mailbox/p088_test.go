package mailbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP088SyncedEventFileRepairsTrailingLineAndPreservesCursor(t *testing.T) {
	authority, command := p085CommandAuthority(t)
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("p088\n"), ByteCount: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointerP085(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	root := p081MailboxRoot(t)
	files, err := NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	projector := EventProjector{Authority: authority}
	cursor, err := projector.Publish(context.Background(), files, command.CommandID)
	if err != nil || cursor != 4 {
		t.Fatalf("initial publish cursor=%d err=%v", cursor, err)
	}
	original, originalCursor, err := files.Read(command.CommandID)
	if err != nil || originalCursor != 4 {
		t.Fatalf("initial read cursor=%d err=%v", originalCursor, err)
	}
	path, err := files.Path(command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, MailboxFileMode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.WriteString(`{"command_id":"command-p088","sequence":5`); err != nil {
		_ = partial.Close()
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := files.Read(command.CommandID); !errors.Is(err, ErrEventFileIncomplete) {
		t.Fatalf("partial read error=%v, want ErrEventFileIncomplete", err)
	}
	if repairedCursor, err := projector.Publish(context.Background(), files, command.CommandID); err != nil || repairedCursor != 4 {
		t.Fatalf("repair publish cursor=%d err=%v", repairedCursor, err)
	}
	repaired, repairedCursor, err := files.Read(command.CommandID)
	if err != nil || repairedCursor != originalCursor || string(repaired) != string(original) {
		t.Fatalf("repaired file cursor=%d err=%v equal=%v", repairedCursor, err, string(repaired) == string(original))
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("event publication artifacts=%v", entries)
	}
}

func TestP088PublishesEventPrefixBeforeStoredResponseAndRepeatsAfterCrash(t *testing.T) {
	authority, command := p085CommandAuthority(t)
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("reply"), ByteCount: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointerP085(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	controllerID, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, controllerID)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"operation":"get_command","command_id":"command-p085"}`)
	digest := sha256.Sum256(payload)
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), store.MailboxExchangeCreate{RequestID: "req-p088", Operation: "get_command", Controller: controller, RequestHash: hash, CanonicalPayload: payload, ResourceID: string(command.CommandID)}); err != nil {
		t.Fatal(err)
	}
	response := []byte(`{"request_id":"req-p088","operation":"get_command","request_state":"complete","response_revision":1,"command_id":"command-p085","available_event_sequence":4,"events_file":"events/command-p085.ndjson"}`)
	cursor := int64(4)
	if _, err := authority.PublishMailboxResponse(context.Background(), "req-p088", store.MailboxResponsePublication{State: store.MailboxExchangeComplete, Bytes: response, AvailableEventSequence: &cursor}); err != nil {
		t.Fatal(err)
	}
	root := p081MailboxRoot(t)
	outbox, err := NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	combined := Projector{Authority: authority, Outbox: outbox, EventFiles: eventFiles}
	if err := combined.PublishCommand(context.Background(), "req-p088", command.CommandID); err != nil {
		t.Fatal(err)
	}
	if got, err := outbox.Read("req-p088"); err != nil || string(got) != string(response) {
		t.Fatalf("published response=%q err=%v", got, err)
	}
	if _, cursor, err := eventFiles.Read(command.CommandID); err != nil || cursor != 4 {
		t.Fatalf("published event file cursor=%d err=%v", cursor, err)
	}
	if err := os.WriteFile(mustEventPath(t, eventFiles, command.CommandID), []byte(`{"partial":`), MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if err := combined.PublishCommand(context.Background(), "req-p088", command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, cursor, err := eventFiles.Read(command.CommandID); err != nil || cursor != 4 {
		t.Fatalf("re-published event file cursor=%d err=%v", cursor, err)
	}
}

func mustEventPath(t *testing.T, files *EventFiles, commandID domain.CommandID) string {
	t.Helper()
	path, err := files.Path(commandID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
