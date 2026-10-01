package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestBUG004PublishableTerminalMailboxExchangePagesUseStableKeysetOrder(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "bug004-page-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	clock := &p019Clock{value: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	authority, err := NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	controller := p163Controller(t, "tomasz.walczuk")
	const mailboxID = "bug004-page-order"

	first := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-page-a")
	second := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-page-b")
	if !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("same-clock records have created_at %s and %s, want a keyset tie", first.CreatedAt, second.CreatedAt)
	}
	clock.Advance(time.Second)
	p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-page-c")
	clock.Advance(time.Second)
	fourth := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-page-d")

	pageOne, err := authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, mailboxID, controller, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	p163AssertMailboxRequestIDs(t, pageOne, "req-p163-page-a", "req-p163-page-b")

	pageTwo, err := authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, mailboxID, controller, &MailboxTerminalArtifactCursor{
		CreatedAt:  pageOne[len(pageOne)-1].CreatedAt,
		ExchangeID: pageOne[len(pageOne)-1].ExchangeID,
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	p163AssertMailboxRequestIDs(t, pageTwo, "req-p163-page-c", "req-p163-page-d")

	afterTail, err := authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, mailboxID, controller, &MailboxTerminalArtifactCursor{
		CreatedAt:  fourth.CreatedAt,
		ExchangeID: fourth.ExchangeID,
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	p163AssertMailboxRequestIDs(t, afterTail)

	unbounded, err := authority.ListPublishableTerminalMailboxExchangesInMailbox(ctx, mailboxID, controller)
	if err != nil {
		t.Fatal(err)
	}
	p163AssertMailboxRequestIDs(t, unbounded, "req-p163-page-a", "req-p163-page-b", "req-p163-page-c", "req-p163-page-d")

	if _, err := authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, mailboxID, controller, nil, 0); !errors.Is(err, ErrMailboxExchangeInvalid) {
		t.Fatalf("zero page limit error=%v, want ErrMailboxExchangeInvalid", err)
	}
	if _, err := authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, mailboxID, controller, &MailboxTerminalArtifactCursor{}, 1); !errors.Is(err, ErrMailboxExchangeInvalid) {
		t.Fatalf("zero cursor error=%v, want ErrMailboxExchangeInvalid", err)
	}
}

func TestBUG004PublishableTerminalMailboxExchangePageKeepsCleanupAndLegacyDeadlineGates(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "bug004-page-gates.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	clock := &p019Clock{value: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	authority, err := NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	controller := p163Controller(t, "tomasz.walczuk")
	const mailboxID = "bug004-page-gates"

	legacyExpired := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-legacy-expired")
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_cleanup_at = NULL WHERE exchange_id = ?`, legacyExpired.ExchangeID); err != nil {
		t.Fatalf("clear legacy cleanup deadline: %v", err)
	}
	clock.Advance(MailboxUnackedResponseLifetime + time.Hour)

	visible := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-visible")
	cleanupStarted := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-cleanup-started")
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_cleanup_started_at = ? WHERE exchange_id = ?`, formatStoredTime(clock.Now()), cleanupStarted.ExchangeID); err != nil {
		t.Fatalf("mark cleanup started: %v", err)
	}
	removed := p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, mailboxID, "req-p163-removed")
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_file_removed_at = ? WHERE exchange_id = ?`, formatStoredTime(clock.Now()), removed.ExchangeID); err != nil {
		t.Fatalf("mark response removed: %v", err)
	}
	p163AcceptTerminalMailboxExchange(t, ctx, authority, controller, "bug004-other-mailbox", "req-p163-other-mailbox")
	p163AcceptTerminalMailboxExchange(t, ctx, authority, p163Controller(t, "other-user"), mailboxID, "req-p163-other-controller")

	// A one-row page must skip the expired legacy row in SQL. Otherwise that
	// stale row would consume every bounded recovery pass and block visible
	// retained work behind it.
	page, err := authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, mailboxID, controller, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	p163AssertMailboxRequestIDs(t, page, visible.RequestID)
}

func p163AcceptTerminalMailboxExchange(t *testing.T, ctx context.Context, authority *AuthorityStore, controller domain.ControllerIdentity, mailboxID, requestID string) MailboxExchangeRecord {
	t.Helper()
	ref, err := NewMailboxExchangeRef(mailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	raw := p082RunPayload(t, requestID, "key-"+requestID, "printf bug004")
	canonical, hash, err := canonicalMailboxRun(raw)
	if err != nil {
		t.Fatal(err)
	}
	accepted, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, ref, MailboxExchangeCreate{
		MailboxID: ref.MailboxID, RequestID: ref.ClientRequestID,
		Operation: "run", Controller: controller, IdempotencyKey: "key-" + requestID,
		RequestHash: hash, CanonicalPayload: canonical, ResourceID: "job-" + requestID,
	})
	if err != nil || duplicate || accepted.State != MailboxExchangeAccepted {
		t.Fatalf("accept %s=%+v duplicate=%v err=%v", requestID, accepted, duplicate, err)
	}
	terminal, err := authority.PublishMailboxResponseInMailbox(ctx, ref, MailboxResponsePublication{
		State: MailboxExchangeComplete,
		Bytes: []byte(`{"request_id":"` + requestID + `","request_state":"complete","response_revision":1}`),
	})
	if err != nil || terminal.State != MailboxExchangeComplete {
		t.Fatalf("publish %s=%+v err=%v", requestID, terminal, err)
	}
	return terminal
}

func p163Controller(t *testing.T, value string) domain.ControllerIdentity {
	t.Helper()
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID(value))
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func p163AssertMailboxRequestIDs(t *testing.T, records []MailboxExchangeRecord, want ...string) {
	t.Helper()
	if len(records) != len(want) {
		t.Fatalf("record count=%d records=%+v, want %d request IDs %v", len(records), records, len(want), want)
	}
	for index, record := range records {
		if record.RequestID != want[index] {
			t.Fatalf("record[%d].RequestID=%q, want %q (all=%+v)", index, record.RequestID, want[index], records)
		}
	}
}
