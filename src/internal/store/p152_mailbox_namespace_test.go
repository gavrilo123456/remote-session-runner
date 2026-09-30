package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

// TestP152MailboxNamespaceScopesReceiptsResponsesAcksRetriesAndCleanup proves
// that one client-visible request ID and idempotency key can safely exist in
// two mailbox namespaces.  The test stays at the authority-store boundary so
// P154 can separately prove that independent filesystem runtimes bind those
// namespaces to distinct roots.
func TestP152MailboxNamespaceScopesReceiptsResponsesAcksRetriesAndCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	root := testfixture.New(t)
	db, err := Open(ctx, filepath.Join(root.Path(), "state", "p152-mailbox-namespaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}

	const (
		requestID = "req-p152-shared"
		clientKey = "key-p152-shared"
		otherBox  = "repo-alpha"
	)
	defaultRef, err := NewMailboxExchangeRef(DefaultMailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	otherRef, err := NewMailboxExchangeRef(otherBox, requestID)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": clientKey,
		"operation": "run", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"script":           "printf p152",
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	create := func(ref MailboxExchangeRef, resourceID string) MailboxExchangeCreate {
		return MailboxExchangeCreate{
			MailboxID: ref.MailboxID, RequestID: ref.ClientRequestID,
			Operation: "run", Controller: controller, IdempotencyKey: clientKey,
			RequestHash: hash, CanonicalPayload: canonical, ResourceID: resourceID,
		}
	}

	defaultRecord, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, defaultRef, create(defaultRef, "job-p152-default"))
	if err != nil || duplicate || defaultRecord.State != MailboxExchangeAccepted {
		t.Fatalf("default receipt=%+v duplicate=%v err=%v", defaultRecord, duplicate, err)
	}
	otherRecord, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, otherRef, create(otherRef, "job-p152-repo-alpha"))
	if err != nil || duplicate || otherRecord.State != MailboxExchangeAccepted {
		t.Fatalf("other receipt=%+v duplicate=%v err=%v", otherRecord, duplicate, err)
	}
	if defaultRecord.ExchangeID == otherRecord.ExchangeID || defaultRecord.ExecutionIdempotencyKey == otherRecord.ExecutionIdempotencyKey {
		t.Fatalf("namespace identities collided: default=%+v other=%+v", defaultRecord, otherRecord)
	}
	if defaultRecord.ExecutionIdempotencyKey == clientKey || otherRecord.ExecutionIdempotencyKey == clientKey {
		t.Fatalf("client idempotency key leaked into execution identity: default=%q other=%q", defaultRecord.ExecutionIdempotencyKey, otherRecord.ExecutionIdempotencyKey)
	}

	response := []byte(`{"request_id":"req-p152-shared","request_state":"complete","response_revision":1}`)
	published, err := authority.PublishMailboxResponseInMailbox(ctx, defaultRef, MailboxResponsePublication{
		State: MailboxExchangeComplete, Bytes: response,
	})
	if err != nil || published.State != MailboxExchangeComplete || published.ResponseRevision != 1 {
		t.Fatalf("default terminal response=%+v err=%v", published, err)
	}
	otherAfterResponse, err := authority.GetMailboxExchangeInMailbox(ctx, otherRef)
	if err != nil || otherAfterResponse.State != MailboxExchangeAccepted || otherAfterResponse.ResponseRevision != 0 || len(otherAfterResponse.ResponseBytes) != 0 {
		t.Fatalf("other mailbox changed by default response: %+v err=%v", otherAfterResponse, err)
	}

	acked, err := authority.AcknowledgeMailboxExchangeInMailbox(ctx, defaultRef, MailboxAcknowledgement{
		MailboxID: defaultRef.MailboxID, RequestID: defaultRef.ClientRequestID, ResponseRevision: published.ResponseRevision,
	})
	if err != nil || acked.AcknowledgedAt == nil {
		t.Fatalf("default ACK=%+v err=%v", acked, err)
	}
	otherAfterACK, err := authority.GetMailboxExchangeInMailbox(ctx, otherRef)
	if err != nil || otherAfterACK.AcknowledgedAt != nil || otherAfterACK.ResponseCleanupAt != nil {
		t.Fatalf("other mailbox changed by default ACK: %+v err=%v", otherAfterACK, err)
	}

	defaultRetryRef, err := NewMailboxExchangeRef(DefaultMailboxID, "req-p152-default-retry")
	if err != nil {
		t.Fatal(err)
	}
	otherRetryRef, err := NewMailboxExchangeRef(otherBox, "req-p152-other-retry")
	if err != nil {
		t.Fatal(err)
	}
	defaultRetry, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, defaultRetryRef, create(defaultRetryRef, "must-not-win-default"))
	if err != nil || !duplicate || defaultRetry.State != MailboxExchangeComplete || defaultRetry.ResourceID != defaultRecord.ResourceID {
		t.Fatalf("default same-key retry=%+v duplicate=%v err=%v", defaultRetry, duplicate, err)
	}
	var rebound struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(defaultRetry.ResponseBytes, &rebound); err != nil || rebound.RequestID != defaultRetryRef.ClientRequestID {
		t.Fatalf("default retry response correlation=%q err=%v", defaultRetry.ResponseBytes, err)
	}
	otherRetry, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, otherRetryRef, create(otherRetryRef, "must-not-win-other"))
	if err != nil || !duplicate || otherRetry.State != MailboxExchangeAccepted || otherRetry.ResponseRevision != 0 || otherRetry.ResourceID != otherRecord.ResourceID {
		t.Fatalf("other same-key retry=%+v duplicate=%v err=%v", otherRetry, duplicate, err)
	}

	// The default response was ACKed, so its 24-hour response retention is
	// independent from repo-alpha's still-accepted exchange.
	now = now.Add(MailboxAckedResponseLifetime + time.Hour)
	claimed, err := authority.ClaimMailboxResponsesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(claimed) != 1 || claimed[0] != defaultRef {
		t.Fatalf("default cleanup claims=%+v err=%v, want only %+v", claimed, err, defaultRef)
	}
	if err := authority.MarkMailboxResponseFileRemovedInMailbox(ctx, defaultRef); err != nil {
		t.Fatal(err)
	}
	defaultAfterCleanup, err := authority.GetMailboxExchangeInMailbox(ctx, defaultRef)
	if err != nil || defaultAfterCleanup.ResponseCleanupStartedAt == nil || defaultAfterCleanup.ResponseFileRemovedAt == nil {
		t.Fatalf("default cleanup marker=%+v err=%v", defaultAfterCleanup, err)
	}
	otherAfterCleanup, err := authority.GetMailboxExchangeInMailbox(ctx, otherRef)
	if err != nil || otherAfterCleanup.ResponseCleanupStartedAt != nil || otherAfterCleanup.ResponseFileRemovedAt != nil {
		t.Fatalf("other mailbox changed by default cleanup: %+v err=%v", otherAfterCleanup, err)
	}
	otherClaims, err := authority.ClaimMailboxResponsesForCleanupInMailbox(ctx, otherBox)
	if err != nil || len(otherClaims) != 0 {
		t.Fatalf("other cleanup claims=%+v err=%v, want none", otherClaims, err)
	}

	var countBeforeInvalid int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges`).Scan(&countBeforeInvalid); err != nil {
		t.Fatal(err)
	}
	invalidRef := MailboxExchangeRef{MailboxID: "bad/namespace", ClientRequestID: "req-p152-invalid-namespace"}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, invalidRef, create(defaultRef, "must-not-insert")); !errors.Is(err, ErrMailboxExchangeInvalid) {
		t.Fatalf("invalid mailbox namespace error=%v, want ErrMailboxExchangeInvalid", err)
	}
	invalidKeyRef, err := NewMailboxExchangeRef(DefaultMailboxID, "req-p152-invalid-key")
	if err != nil {
		t.Fatal(err)
	}
	invalidKey := create(invalidKeyRef, "must-not-insert")
	invalidKey.IdempotencyKey = strings.Repeat("x", 257)
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, invalidKeyRef, invalidKey); !errors.Is(err, ErrMailboxExchangeInvalid) {
		t.Fatalf("invalid mailbox idempotency key error=%v, want ErrMailboxExchangeInvalid", err)
	}
	var countAfterInvalid int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges`).Scan(&countAfterInvalid); err != nil {
		t.Fatal(err)
	}
	if countAfterInvalid != countBeforeInvalid {
		t.Fatalf("invalid mailbox input inserted durable row: before=%d after=%d", countBeforeInvalid, countAfterInvalid)
	}
}
