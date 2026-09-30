package localapi

import (
	"context"
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// TestP153MailboxProcessorComposesSelectionWithRealLocalAPI proves the
// mailbox processor passes its trusted decision into the real local API
// boundary. The local API is the only component allowed to create an intent,
// so a rejected selector cannot queue local or remote work.
func TestP153MailboxProcessorComposesSelectionWithRealLocalAPI(t *testing.T) {
	ctx := context.Background()
	h := newP095Harness(t)

	defaultRequestID := "req-p153-composed-default"
	defaultKey := "key-p153-composed-default"
	writeP094Request(t, h.importer, defaultRequestID, map[string]any{
		"request_id": defaultRequestID, "idempotency_key": defaultKey, "operation": "create_session",
		"source": map[string]string{"mode": "empty"},
	})
	p153ImportComposedRequest(t, ctx, h, defaultRequestID)
	defaultResponse := readP095Response(t, h.outbox, defaultRequestID)
	if defaultResponse.RequestState != string(store.MailboxExchangeAccepted) || defaultResponse.SessionID == "" {
		t.Fatalf("default response=%+v", defaultResponse)
	}
	defaultIntent := pMailboxIntentForRequest(t, h.authority, "create_session", defaultRequestID)
	p153AssertIntentSelection(t, defaultIntent, "mac-dev", p153Target(t, domain.TargetKindLocal, "mac-workstation"), "repository_alias")
	p153AssertComposedExchangeSelection(t, h.authority, defaultRequestID, store.MailboxExecutionSelectionInboxDefault, "mac-dev", domain.TargetKindLocal, "mac-workstation")

	overrideRequestID := "req-p153-composed-override"
	overrideKey := "key-p153-composed-override"
	writeP094Request(t, h.importer, overrideRequestID, map[string]any{
		"request_id": overrideRequestID, "idempotency_key": overrideKey, "operation": "run",
		"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"script": "printf p153-composed", "source": map[string]string{"mode": "empty"},
	})
	p153ImportComposedRequest(t, ctx, h, overrideRequestID)
	overrideResponse := readP095Response(t, h.outbox, overrideRequestID)
	if overrideResponse.RequestState != string(store.MailboxExchangeAccepted) || overrideResponse.CommandID == "" {
		t.Fatalf("override response=%+v", overrideResponse)
	}
	overrideIntent := pMailboxIntentForRequest(t, h.authority, "run", overrideRequestID)
	p153AssertIntentSelection(t, overrideIntent, "linux-dev", p153Target(t, domain.TargetKindRemote, "linux-host"), "repository_alias")
	p153AssertComposedExchangeSelection(t, h.authority, overrideRequestID, store.MailboxExecutionSelectionRequestOverride, "linux-dev", domain.TargetKindRemote, "linux-host")

	// A partial selector is rejected before the real local API can persist an
	// intent. That also means the Router has no local intent to dispatch to a
	// remote target.
	rejectedRequestID := "req-p153-composed-rejected"
	rejectedKey := "key-p153-composed-rejected"
	writeP094Request(t, h.importer, rejectedRequestID, map[string]any{
		"request_id": rejectedRequestID, "idempotency_key": rejectedKey, "operation": "run",
		"environment": "linux-dev", "script": "printf should-not-run",
	})
	p153ImportComposedRequest(t, ctx, h, rejectedRequestID)
	rejectedResponse := readP095Response(t, h.outbox, rejectedRequestID)
	if rejectedResponse.RequestState != string(store.MailboxExchangeRejected) || rejectedResponse.Error == nil || rejectedResponse.Error.Code != "invalid_request" {
		t.Fatalf("rejected response=%+v", rejectedResponse)
	}
	rejectedReceipt, err := h.authority.GetMailboxExchangeInMailbox(ctx, store.MailboxExchangeRef{MailboxID: store.DefaultMailboxID, ClientRequestID: rejectedRequestID})
	if err != nil || rejectedReceipt.Selection != nil || rejectedReceipt.SelectionState != store.MailboxExecutionSelectionRejected {
		t.Fatalf("rejected receipt=%+v err=%v", rejectedReceipt, err)
	}
	if _, err := h.authority.GetLocalIntentByIdempotency(ctx, "run", rejectedReceipt.ExecutionIdempotencyKey, p063Owner(t)); !errors.Is(err, store.ErrLocalIntentNotFound) {
		t.Fatalf("rejected selection created a local intent: %v", err)
	}
}

func p153ImportComposedRequest(t *testing.T, ctx context.Context, h *p095Harness, requestID string) {
	t.Helper()
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("mailbox import %s results=%+v err=%v", requestID, results, err)
	}
}

func p153AssertComposedExchangeSelection(t *testing.T, authority *store.AuthorityStore, requestID string, source string, environment string, kind domain.TargetKind, profile string) {
	t.Helper()
	record, err := authority.GetMailboxExchangeInMailbox(context.Background(), store.MailboxExchangeRef{MailboxID: store.DefaultMailboxID, ClientRequestID: requestID})
	if err != nil || record.Selection == nil || record.SelectionState != store.MailboxExecutionSelectionResolved ||
		record.Selection.Source != source || record.Selection.Environment != environment ||
		record.Selection.Target.Kind() != kind || record.Selection.Target.Profile() != profile {
		t.Fatalf("mailbox selection record=%+v err=%v", record, err)
	}
}
