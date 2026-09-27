package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP096D05M08MailboxIdempotencyRetryConflictAndExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	root := testfixture.New(t)
	db, err := Open(ctx, filepath.Join(root.Path(), "state", "p096-mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	create := func(requestID, script string, resourceID string) MailboxExchangeCreate {
		payload, requestHash, err := p096CommandPayload(script)
		if err != nil {
			t.Fatal(err)
		}
		return MailboxExchangeCreate{
			RequestID: requestID, Operation: "submit_command", Controller: controller,
			IdempotencyKey: "key-p096", RequestHash: requestHash, CanonicalPayload: payload, ResourceID: resourceID,
		}
	}
	first, duplicate, err := authority.AcceptMailboxExchange(ctx, create("req-p096-first", "echo original", "command-p096-original"))
	if err != nil || duplicate || !first.IdempotencyBindingActive || first.IdempotencyKeyExpiresAt == nil || !first.IdempotencyKeyExpiresAt.Equal(now.Add(DefaultSessionIdempotencyRetention)) {
		t.Fatalf("first receipt=%+v duplicate=%v err=%v", first, duplicate, err)
	}
	originalBytes := []byte(`{"request_id":"req-p096-first","operation":"submit_command","request_state":"complete","response_revision":1,"command_id":"command-p096-original"}`)
	if _, err := authority.PublishMailboxResponse(ctx, first.RequestID, MailboxResponsePublication{State: MailboxExchangeComplete, Bytes: originalBytes}); err != nil {
		t.Fatal(err)
	}
	first, err = authority.GetMailboxExchange(ctx, first.RequestID)
	if err != nil {
		t.Fatal(err)
	}

	retry, duplicate, retryConflict, err := authority.AcceptMailboxExchangeWithConflictReceipt(ctx, create("req-p096-retry", "echo original", "must-not-win"))
	if err != nil || !duplicate || retryConflict || retry.State != MailboxExchangeAccepted || retry.ResponseRevision != 0 || len(retry.ResponseBytes) != 0 || retry.ResourceID != "command-p096-original" || retry.IdempotencyKeyExpiresAt == nil || !retry.IdempotencyKeyExpiresAt.Equal(*first.IdempotencyKeyExpiresAt) {
		t.Fatalf("same-key retry=%+v duplicate=%v conflict=%v err=%v", retry, duplicate, retryConflict, err)
	}

	changed := create("req-p096-conflict", "echo changed", "")
	if _, _, err := authority.AcceptMailboxExchange(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed active-key payload error=%v, want ErrIdempotencyConflict", err)
	}
	conflictRecord, duplicate, idempotencyConflict, err := authority.AcceptMailboxExchangeWithConflictReceipt(ctx, changed)
	if err != nil || duplicate || !idempotencyConflict || conflictRecord.State != MailboxExchangeAccepted || conflictRecord.IdempotencyBindingActive || conflictRecord.IdempotencyKeyExpiresAt != nil {
		t.Fatalf("conflict receipt=%+v duplicate=%v conflict=%v err=%v", conflictRecord, duplicate, idempotencyConflict, err)
	}
	conflictBytes := []byte(`{"request_id":"req-p096-conflict","operation":"submit_command","request_state":"rejected","response_revision":1,"error":{"code":"idempotency_conflict","message":"key payload differs","retryable":false}}`)
	if _, err := authority.PublishMailboxResponse(ctx, conflictRecord.RequestID, MailboxResponsePublication{State: MailboxExchangeRejected, Bytes: conflictBytes}); err != nil {
		t.Fatal(err)
	}
	stillOriginal, duplicate, conflict, err := authority.AcceptMailboxExchangeWithConflictReceipt(ctx, create("req-p096-after-conflict", "echo original", "must-not-win"))
	if err != nil || !duplicate || conflict || stillOriginal.ResourceID != "command-p096-original" || stillOriginal.ResponseRevision != 0 {
		t.Fatalf("original binding after conflict=%+v duplicate=%v conflict=%v err=%v", stillOriginal, duplicate, conflict, err)
	}
	storedOriginal, err := authority.GetMailboxExchange(ctx, first.RequestID)
	if err != nil || string(storedOriginal.ResponseBytes) != string(originalBytes) {
		t.Fatalf("original response changed: %q err=%v", storedOriginal.ResponseBytes, err)
	}

	now = *first.IdempotencyKeyExpiresAt // Expiry is exclusive at the boundary.
	expired, duplicate, idempotencyConflict, err := authority.AcceptMailboxExchangeWithConflictReceipt(ctx, create("req-p096-expired", "echo original", "command-p096-new"))
	if err != nil || duplicate || idempotencyConflict || expired.State != MailboxExchangeAccepted || !expired.IdempotencyBindingActive || !expired.DeduplicationWarning || expired.ResourceID != "command-p096-new" || expired.IdempotencyKeyExpiresAt == nil || !expired.IdempotencyKeyExpiresAt.Equal(now.Add(DefaultSessionIdempotencyRetention)) {
		t.Fatalf("expired-key request=%+v duplicate=%v conflict=%v err=%v", expired, duplicate, idempotencyConflict, err)
	}
	warningRetry, duplicate, idempotencyConflict, err := authority.AcceptMailboxExchangeWithConflictReceipt(ctx, create("req-p096-expired-retry", "echo original", "must-not-win"))
	if err != nil || !duplicate || idempotencyConflict || !warningRetry.DeduplicationWarning || warningRetry.ResourceID != "command-p096-new" || warningRetry.IdempotencyKeyExpiresAt == nil || !warningRetry.IdempotencyKeyExpiresAt.Equal(*expired.IdempotencyKeyExpiresAt) {
		t.Fatalf("warning key retry=%+v duplicate=%v conflict=%v err=%v", warningRetry, duplicate, idempotencyConflict, err)
	}

}

func p096CommandPayload(script string) ([]byte, domain.CanonicalHash, error) {
	raw, err := json.Marshal(map[string]any{"operation": "submit_command", "session_id": "sess-p096", "script": script})
	if err != nil {
		return nil, domain.CanonicalHash{}, err
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", raw, domain.CanonicalizationOptions{})
	if err != nil {
		return nil, domain.CanonicalHash{}, err
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", raw, domain.CanonicalizationOptions{})
	return canonical, hash, err
}
