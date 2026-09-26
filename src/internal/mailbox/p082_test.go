package mailbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP082ReceiptProcessorPersistsBeforeCallbackAndDeduplicatesRetries(t *testing.T) {
	root := p081MailboxRoot(t)
	dbPath := filepath.Join(root, "state", "mailbox.db")
	db, err := store.Open(context.Background(), dbPath)
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
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeP082MailboxPair(t, importer, "req-p082-complete", "key-p082-complete", "echo complete")
	callbackCount := 0
	processor, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner, Handler: func(context.Context, Request) error {
		callbackCount++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if results, err := processor.Import(context.Background()); err != nil || len(results) != 1 || results[0].Status != ResultAccepted {
		t.Fatalf("first receipt import results=%+v err=%v", results, err)
	}
	if callbackCount != 1 {
		t.Fatalf("first callback count=%d", callbackCount)
	}
	record, err := authority.GetMailboxExchange(context.Background(), "req-p082-complete")
	if err != nil || record.State != store.MailboxExchangeComplete {
		t.Fatalf("completed receipt=%+v err=%v", record, err)
	}
	if _, err := processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	if callbackCount != 1 {
		t.Fatalf("same request replay called callback=%d times", callbackCount)
	}
	writeP082MailboxPair(t, importer, "req-p082-complete-retry", "key-p082-complete", "echo complete")
	if _, err := processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	if callbackCount != 1 {
		t.Fatalf("new ID retry called callback=%d times", callbackCount)
	}
	retry, err := authority.GetMailboxExchange(context.Background(), "req-p082-complete-retry")
	if err != nil || retry.State != store.MailboxExchangeComplete || retry.ResourceID != record.ResourceID {
		t.Fatalf("retry receipt=%+v err=%v", retry, err)
	}

	// Simulate a process crash after the receipt transaction and before the
	// processing callback. The next process resumes the accepted receipt.
	writeP082MailboxPair(t, importer, "req-p082-crash", "key-p082-crash", "echo crash")
	crashed, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crashed.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	accepted, err := authority.GetMailboxExchange(context.Background(), "req-p082-crash")
	if err != nil || accepted.State != store.MailboxExchangeAccepted {
		t.Fatalf("pre-callback receipt=%+v err=%v", accepted, err)
	}
	resumedCallbacks := 0
	resumed, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner, Handler: func(context.Context, Request) error {
		resumedCallbacks++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resumedCallbacks != 1 {
		t.Fatalf("crash recovery callback count=%d", resumedCallbacks)
	}
	completed, err := authority.GetMailboxExchange(context.Background(), "req-p082-crash")
	if err != nil || completed.State != store.MailboxExchangeComplete {
		t.Fatalf("crash recovery receipt=%+v err=%v", completed, err)
	}
}

func writeP082MailboxPair(t *testing.T, importer *Importer, requestID, key, script string) {
	t.Helper()
	value := map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "run", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": script,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+RequestSuffix), raw, 0o600)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+ReadySuffix), nil, 0o600)
}
