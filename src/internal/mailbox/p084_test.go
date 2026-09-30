package mailbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP084OutboxAtomicReplacementAndProjectorRepair(t *testing.T) {
	root := p081MailboxRoot(t)
	outbox, err := NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-p084"
	first := []byte(`{"request_id":"req-p084","request_state":"accepted","response_revision":1}`)
	second := []byte(`{"request_id":"req-p084","request_state":"accepted","response_revision":2}`)
	if err := outbox.Replace(context.Background(), requestID, first); err != nil {
		t.Fatal(err)
	}
	path, err := outbox.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != MailboxFileMode {
		t.Fatalf("outbox response mode=%v err=%v", info, err)
	}
	if got, err := outbox.Read(requestID); err != nil || string(got) != string(first) {
		t.Fatalf("first response=%q err=%v", got, err)
	}
	if err := os.Chmod(path, MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Read(requestID); err == nil {
		t.Fatal("outbox read accepted workspace ingress mode")
	}
	if err := outbox.Replace(context.Background(), requestID, second); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != MailboxFileMode {
		t.Fatalf("replaced outbox response mode=%v err=%v", info, err)
	}
	if got, err := outbox.Read(requestID); err != nil || string(got) != string(second) {
		t.Fatalf("replaced response=%q err=%v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			t.Fatalf("temporary publication artifact remains: %q", entry.Name())
		}
	}

	db, err := store.Open(context.Background(), filepath.Join(root, "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"request_id":"req-projector","idempotency_key":"key-projector","operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"echo projector"}`)
	hash, err := domain.HashMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), store.MailboxExchangeCreate{RequestID: "req-projector", Operation: "run", Controller: owner, IdempotencyKey: "key-projector", RequestHash: hash, CanonicalPayload: canonical}); err != nil {
		t.Fatal(err)
	}
	response := []byte(`{"request_id":"req-projector","request_state":"complete","response_revision":1}`)
	if _, err := authority.PublishMailboxResponse(context.Background(), "req-projector", store.MailboxResponsePublication{State: store.MailboxExchangeComplete, Bytes: response}); err != nil {
		t.Fatal(err)
	}
	projector := Projector{Authority: authority, Outbox: outbox}
	if err := projector.Publish(context.Background(), "req-projector"); err != nil {
		t.Fatal(err)
	}
	projectorPath, err := outbox.Path("req-projector")
	if err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(projectorPath)
	if err != nil || info.Mode().Perm() != MailboxFileMode {
		t.Fatalf("projected response mode=%v err=%v", info, err)
	}
	if err := os.WriteFile(projectorPath, []byte(`{"partial":`), MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if err := projector.Publish(context.Background(), "req-projector"); err != nil {
		t.Fatal(err)
	}
	got, err := outbox.Read("req-projector")
	if err != nil || string(got) != string(response) || !json.Valid(got) {
		t.Fatalf("repaired response=%q err=%v", got, err)
	}
}

func TestP084OutboxRejectsUnsafeAndOversizeResponses(t *testing.T) {
	root := p081MailboxRoot(t)
	outbox, err := NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Path("../escape"); err == nil {
		t.Fatal("unsafe request ID accepted")
	}
	if err := outbox.Replace(context.Background(), "req-invalid", []byte(`{"partial":`)); err == nil {
		t.Fatal("invalid JSON response accepted")
	}
	if err := outbox.Replace(context.Background(), "req-large", append([]byte(`{"x":"`), make([]byte, domain.MaxSerializedRequestBytes)...)); err == nil {
		t.Fatal("oversize response accepted")
	}
}
