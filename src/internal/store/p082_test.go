package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP082MailboxExchangeBindsRequestIDAndNewIDRetryByCanonicalHash(t *testing.T) {
	root := testfixture.New(t)
	dbPath := filepath.Join(root.Path(), "state", "mailbox.db")
	db, err := Open(context.Background(), dbPath)
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
	firstRaw := p082RunPayload(t, "req-p082-a", "key-p082", "echo p082")
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", firstRaw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", firstRaw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, duplicate, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p082-a", Operation: "run", Controller: controller, IdempotencyKey: "key-p082", RequestHash: hash, CanonicalPayload: canonical, ResourceID: "job-p082"})
	if err != nil || duplicate || first.State != MailboxExchangeAccepted {
		t.Fatalf("first receipt = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	if got, err := authority.GetMailboxExchange(context.Background(), "req-p082-a"); err != nil || string(got.CanonicalPayload) != string(canonical) || got.RequestHash.String() != hash.String() {
		t.Fatalf("durable receipt = %+v err=%v", got, err)
	}

	sameID, duplicate, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p082-a", Operation: "run", Controller: controller, IdempotencyKey: "key-p082", RequestHash: hash, CanonicalPayload: canonical, ResourceID: "ignored"})
	if err != nil || !duplicate || sameID.ResourceID != "job-p082" {
		t.Fatalf("same request replay = %+v duplicate=%v err=%v", sameID, duplicate, err)
	}
	changedRaw := p082RunPayload(t, "req-p082-a", "key-p082", "echo changed")
	changedCanonical, changedHash, err := canonicalMailboxRun(changedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p082-a", Operation: "run", Controller: controller, IdempotencyKey: "key-p082", RequestHash: changedHash, CanonicalPayload: changedCanonical}); !errors.Is(err, ErrMailboxExchangeConflict) {
		t.Fatalf("same request changed payload error = %v, want ErrMailboxExchangeConflict", err)
	}

	// request_id and idempotency_key are excluded from canonical hashing, so a
	// Generic receipt retries retain the P082 terminal snapshot; the working
	// create/submit mailbox processor uses its dedicated refreshed-snapshot path.
	retryRaw := p082RunPayload(t, "req-p082-b", "key-p082", "echo p082")
	retryCanonical, retryHash, err := canonicalMailboxRun(retryRaw)
	if err != nil {
		t.Fatal(err)
	}
	retry, duplicate, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p082-b", Operation: "run", Controller: controller, IdempotencyKey: "key-p082", RequestHash: retryHash, CanonicalPayload: retryCanonical, ResourceID: "new-job-must-not-win"})
	if err != nil || !duplicate || retry.ResourceID != "job-p082" || retry.State != MailboxExchangeAccepted {
		t.Fatalf("new ID retry = %+v duplicate=%v err=%v", retry, duplicate, err)
	}
	if _, err := authority.CompleteMailboxExchange(context.Background(), "req-p082-a", MailboxExchangeComplete); err != nil {
		t.Fatal(err)
	}
	terminal, duplicate, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p082-c", Operation: "run", Controller: controller, IdempotencyKey: "key-p082", RequestHash: retryHash, CanonicalPayload: retryCanonical})
	if err != nil || !duplicate || terminal.State != MailboxExchangeComplete {
		t.Fatalf("terminal new ID retry = %+v duplicate=%v err=%v, want original terminal state", terminal, duplicate, err)
	}
	conflictRaw := p082RunPayload(t, "req-p082-d", "key-p082", "echo conflict")
	conflictCanonical, conflictHash, err := canonicalMailboxRun(conflictRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), MailboxExchangeCreate{RequestID: "req-p082-d", Operation: "run", Controller: controller, IdempotencyKey: "key-p082", RequestHash: conflictHash, CanonicalPayload: conflictCanonical}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same key changed payload error = %v, want ErrIdempotencyConflict", err)
	}

	_ = json.Valid
}

func p082RunPayload(t *testing.T, requestID, key, script string) []byte {
	t.Helper()
	payload := map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "run", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": script,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func canonicalMailboxRun(raw []byte) ([]byte, domain.CanonicalHash, error) {
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		return nil, domain.CanonicalHash{}, err
	}
	hash, err := domain.HashMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	return canonical, hash, err
}
