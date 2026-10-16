package mailboxclient

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/mailboxfence"
)

func TestLanesL108RequestAndAckClientRefuseMigrationFence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	for _, directory := range []string{"", "inbox", "outbox", "events", "acks"} {
		path := root
		if directory != "" {
			path = filepath.Join(root, directory)
		}
		if err := os.Mkdir(path, directoryMode); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, mailboxfence.MigrationFenceFileName), []byte(`{"state":"fencing"}`), fileMode); err != nil {
		t.Fatal(err)
	}
	requestID := "req-l108-blocked"
	err = client.WriteRequest(requestID, []byte(`{"request_id":"req-l108-blocked","operation":"get_command"}`))
	if !errors.Is(err, mailboxfence.ErrMigrationFenced) {
		t.Fatalf("request publication error=%v, want migration fence", err)
	}
	err = client.WriteAcknowledgment(requestID, Response{RequestID: requestID, ResponseRevision: 1})
	if !errors.Is(err, mailboxfence.ErrMigrationFenced) {
		t.Fatalf("ACK publication error=%v, want migration fence", err)
	}
	for _, directory := range []string{"inbox", "acks"} {
		entries, err := os.ReadDir(filepath.Join(root, directory))
		if err != nil || len(entries) != 0 {
			t.Fatalf("fenced publisher created %s entries=%v err=%v", directory, entries, err)
		}
	}
}
