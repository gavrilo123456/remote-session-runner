package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"remote-session-runner/src/internal/store"
)

func TestBUG021RunTimeoutAliasNormalizesForCanonicalIdempotency(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID := "req-bug021-timeout-alias"
	key := "key-bug021-timeout-canonical"
	alias := p100RunRequest(requestID, key, "remote", "linux-host", "linux-dev", "printf bounded")
	alias["timeout_seconds"] = 5
	writeP094Request(t, h.importer, requestID, alias)
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("alias request import=%+v err=%v", results, err)
	}
	first := readP100Response(t, h, requestID)
	if first.RequestState != "accepted" || first.JobID == "" {
		t.Fatalf("alias request response=%+v", first)
	}
	intent := pMailboxIntentForRequest(t, h.authority, "run", requestID)
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(intent.PayloadJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored["timeout_seconds"]; ok {
		t.Fatalf("persisted run intent retained alias: %s", intent.PayloadJSON)
	}
	var limits map[string]json.RawMessage
	if err := json.Unmarshal(stored["limits"], &limits); err != nil || string(limits["command_timeout_seconds"]) != "5e0" {
		t.Fatalf("persisted normalized limits=%v err=%v payload=%s", limits, err, intent.PayloadJSON)
	}

	// An explicit spelling of the same deadline must hash to the same local
	// intent and keep the original job/session/command identities.
	retryID := requestID + "-explicit"
	explicit := p100RunRequest(retryID, key, "remote", "linux-host", "linux-dev", "printf bounded")
	delete(explicit, "timeout_seconds")
	explicit["limits"] = map[string]int64{"command_timeout_seconds": 5}
	writeP094Request(t, h.importer, retryID, explicit)
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("explicit retry import=%+v err=%v", results, err)
	}
	retried := readP100Response(t, h, retryID)
	if retried.RequestState != "accepted" || retried.JobID != first.JobID || retried.SessionID != first.SessionID || retried.CommandID != first.CommandID {
		detail := readP095Response(t, h.outbox, retryID)
		message := ""
		if detail.Error != nil {
			message = detail.Error.Message
		}
		exchange, _ := h.authority.GetMailboxExchange(ctx, retryID)
		t.Fatalf("equivalent timeout retry changed stable identities: first=%+v retry=%+v error=%s first_payload=%s retry_receipt=%s", first, retried, message, intent.PayloadJSON, exchange.CanonicalPayload)
	}
	retriedIntent := pMailboxIntentForRequest(t, h.authority, "run", retryID)
	if retriedIntent.IntentID != intent.IntentID || string(retriedIntent.PayloadJSON) != string(intent.PayloadJSON) {
		t.Fatalf("equivalent timeout retry changed immutable intent: first=%+v retry=%+v", intent, retriedIntent)
	}
}

func TestBUG021ConflictingRunTimeoutIsRejectedBeforeIntentCreation(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID := "req-bug021-timeout-conflict"
	key := "key-bug021-timeout-conflict"
	request := p100RunRequest(requestID, key, "remote", "linux-host", "linux-dev", "printf must-not-dispatch")
	request["timeout_seconds"] = 5
	request["limits"] = map[string]int64{"command_timeout_seconds": 6}
	writeP094Request(t, h.importer, requestID, request)
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("conflicting request import=%+v err=%v", results, err)
	}
	response := readP100Response(t, h, requestID)
	if response.RequestState != "rejected" || response.Error == nil || response.Error.Code != "invalid_request" {
		detail := readP095Response(t, h.outbox, requestID)
		message := ""
		if detail.Error != nil {
			message = detail.Error.Message
		}
		t.Fatalf("conflicting timeout response=%+v error=%s", response, message)
	}
	exchange, err := h.authority.GetMailboxExchange(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.GetLocalIntentByIdempotency(ctx, "run", exchange.ExecutionIdempotencyKey, p063Owner(t)); !errors.Is(err, store.ErrLocalIntentNotFound) {
		t.Fatalf("conflicting request created a durable run intent: %v", err)
	}
}
