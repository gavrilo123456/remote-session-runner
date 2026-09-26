package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP083MailboxResponseRevisionsAndTerminalSnapshotAreImmutable(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), filepath.Join(root.Path(), "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", []byte(`{"request_id":"req-p083","idempotency_key":"key-p083","operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"echo p083"}`), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record, duplicate, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p083", Operation: "run", Controller: controller, IdempotencyKey: "key-p083", RequestHash: hash, CanonicalPayload: []byte(`{"operation":"run","script":"echo p083"}`), ResourceID: "job-p083"})
	if err != nil || duplicate || record.ResponseRevision != 0 {
		t.Fatalf("initial exchange=%+v duplicate=%v err=%v", record, duplicate, err)
	}

	first, err := authority.PublishMailboxResponse(context.Background(), "req-p083", MailboxResponsePublication{State: MailboxExchangeAccepted, Bytes: []byte(`{"request_state":"accepted","response_revision":1}`)})
	if err != nil || first.ResponseRevision != 1 || string(first.ResponseBytes) == "" || first.State != MailboxExchangeAccepted {
		t.Fatalf("first response=%+v err=%v", first, err)
	}
	second, err := authority.PublishMailboxResponse(context.Background(), "req-p083", MailboxResponsePublication{State: MailboxExchangeAccepted, Bytes: []byte(`{"request_state":"accepted","response_revision":2}`)})
	if err != nil || second.ResponseRevision != 2 || string(second.ResponseBytes) == string(first.ResponseBytes) {
		t.Fatalf("second response=%+v err=%v", second, err)
	}
	cursor := int64(4)
	terminalBytes := []byte(`{"request_state":"complete","response_revision":3,"available_event_sequence":4}`)
	terminal, err := authority.PublishMailboxResponse(context.Background(), "req-p083", MailboxResponsePublication{State: MailboxExchangeComplete, Bytes: terminalBytes, AvailableEventSequence: &cursor})
	if err != nil || terminal.State != MailboxExchangeComplete || terminal.ResponseRevision != 3 || string(terminal.TerminalResponseBytes) != string(terminalBytes) || terminal.AvailableEventSequence == nil || *terminal.AvailableEventSequence != cursor {
		t.Fatalf("terminal response=%+v err=%v", terminal, err)
	}
	replayed, err := authority.PublishMailboxResponse(context.Background(), "req-p083", MailboxResponsePublication{State: MailboxExchangeComplete, Bytes: terminalBytes, AvailableEventSequence: &cursor})
	if err != nil || replayed.ResponseRevision != terminal.ResponseRevision || string(replayed.TerminalResponseBytes) != string(terminal.TerminalResponseBytes) {
		t.Fatalf("identical terminal replay=%+v err=%v", replayed, err)
	}
	if _, err := authority.PublishMailboxResponse(context.Background(), "req-p083", MailboxResponsePublication{State: MailboxExchangeComplete, Bytes: []byte(`{"request_state":"complete","response_revision":4}`)}); !errors.Is(err, ErrMailboxTerminalImmutable) {
		t.Fatalf("changed terminal response error=%v, want ErrMailboxTerminalImmutable", err)
	}
	if _, err := authority.PublishMailboxResponse(context.Background(), "req-p083", MailboxResponsePublication{State: MailboxExchangeAccepted, Bytes: []byte(`{"request_state":"accepted","response_revision":4}`)}); !errors.Is(err, ErrMailboxTerminalImmutable) {
		t.Fatalf("post-terminal nonterminal response error=%v, want ErrMailboxTerminalImmutable", err)
	}

	// New request IDs remain correlated to the immutable terminal snapshot.
	retry, duplicate, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p083-retry", Operation: "run", Controller: controller, IdempotencyKey: "key-p083", RequestHash: hash, CanonicalPayload: []byte(`{"operation":"run","script":"echo p083"}`)})
	if err != nil || !duplicate || retry.ResponseRevision != terminal.ResponseRevision || string(retry.ResponseBytes) != string(terminalBytes) || string(retry.TerminalResponseBytes) != string(terminalBytes) {
		t.Fatalf("terminal retry=%+v duplicate=%v err=%v", retry, duplicate, err)
	}
}
