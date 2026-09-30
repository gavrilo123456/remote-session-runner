package localapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// TestP149RestartDoesNotRepublishLegacyRetentionExpiredTerminalReceipt proves
// that an old reconciled marker is not enough to recreate a terminal mailbox
// result after the DB commit/file-publication crash seam. Retention expiry has
// a zero event cursor, so this specifically protects the zero-cursor path.
func TestP149RestartDoesNotRepublishLegacyRetentionExpiredTerminalReceipt(t *testing.T) {
	ctx := context.Background()
	h, sessionID, commandID := p097RemoteTerminalWithProof(t, domain.CommandStateSucceeded, "retention_expired", false)
	intent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", commandID, p063Owner(t))
	if err != nil || intent.DeliveryState != store.LocalIntentReconciled || intent.RemoteTerminalProofVersion != 0 {
		t.Fatalf("legacy remote terminal intent=%+v err=%v", intent, err)
	}

	requestID := "req-p149-legacy-retention-repair"
	payload, err := json.Marshal(map[string]string{"operation": "get_command", "command_id": commandID})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authority.AcceptMailboxExchange(ctx, store.MailboxExchangeCreate{
		RequestID: requestID, Operation: "get_command", Controller: p063Owner(t), RequestHash: hash,
		CanonicalPayload: payload, ResourceID: commandID,
	}); err != nil {
		t.Fatal(err)
	}
	zero, final := int64(0), int64(4)
	responseBytes, err := json.Marshal(map[string]any{
		"request_id": requestID, "operation": "get_command", "request_state": store.MailboxExchangeComplete, "response_revision": 1,
		"command_id": commandID, "session_id": sessionID, "delivery_state": string(store.LocalIntentReconciled), "command_state": string(domain.CommandStateSucceeded),
		"final_event_sequence": final, "available_event_sequence": zero, "output_complete": false, "output_truncated": false,
		"output_unavailable_reason": "retention_expired",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.PublishMailboxResponse(ctx, requestID, store.MailboxResponsePublication{
		State: store.MailboxExchangeComplete, Bytes: responseBytes, AvailableEventSequence: &zero,
	}); err != nil {
		t.Fatal(err)
	}

	// Model a stop after the terminal SQLite commit and before the outbox rename.
	if _, err := h.outbox.Read(requestID); err == nil {
		t.Fatal("legacy test unexpectedly has an outbox artifact before recovery")
	}
	restarted := p099NewProcessor(t, h, h.server)
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.outbox.Read(requestID); err == nil {
		t.Fatal("legacy weak terminal receipt was republished without strict proof")
	}

	if _, err := h.authority.MarkRemoteIntentTerminalProof(ctx, intent.IntentID, "p149-test-strict-proof"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := h.outbox.Read(requestID)
	if err != nil || !bytes.Equal(restored, responseBytes) {
		t.Fatalf("strictly proven retention receipt restored=%v err=%v", bytes.Equal(restored, responseBytes), err)
	}
}

// TestP149RestartDoesNotRepublishLegacyPreCommandTerminalRunReceipt covers a
// historical one-off receipt whose job reached a terminal teardown outcome
// before a command snapshot was available. It has no command_state, which
// must not turn the pre-P149 reconciled marker into proof after a restart.
// A legacy artifact already published before P149 remains deliverable; the
// recovery boundary only prevents a missing one from being regenerated.
func TestP149RestartDoesNotRepublishLegacyPreCommandTerminalRunReceipt(t *testing.T) {
	ctx := context.Background()
	h := newP095Harness(t)
	requestID := "req-p149-legacy-pre-command-run"
	p101Import(t, h, requestID, p100RunRequest(requestID, "key-p149-legacy-pre-command-run", "remote", "linux-host", "linux-dev", "printf p149"))
	accepted := p101ReadResponse(t, h, requestID)
	intent, err := h.authority.GetLocalIntentByResource(ctx, "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
	p101SetIntentDelivery(t, h, intent, store.LocalIntentReconciled)

	responseBytes, err := json.Marshal(map[string]any{
		"request_id": requestID, "operation": "run", "request_state": store.MailboxExchangeComplete, "response_revision": 2,
		"job_id": accepted.JobID, "session_id": accepted.SessionID, "command_id": accepted.CommandID,
		"job_phase": string(store.JobPhaseLost), "delivery_state": string(store.LocalIntentReconciled),
		"teardown_outcome": string(store.JobTeardownLost),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.PublishMailboxResponse(ctx, requestID, store.MailboxResponsePublication{
		State: store.MailboxExchangeComplete, Bytes: responseBytes,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.outbox.Replace(ctx, requestID, responseBytes); err != nil {
		t.Fatal(err)
	}

	restarted := p099NewProcessor(t, h, h.server)
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	existing, err := h.outbox.Read(requestID)
	if err != nil || !bytes.Equal(existing, responseBytes) {
		t.Fatalf("existing legacy terminal artifact changed=%v err=%v", !bytes.Equal(existing, responseBytes), err)
	}
	if err := h.outbox.Remove(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.outbox.Read(requestID); err == nil {
		t.Fatal("legacy pre-command terminal run receipt was republished without strict proof")
	}

	if _, err := h.authority.MarkRemoteIntentTerminalProof(ctx, intent.IntentID, "p149-test-future-pre-command-proof"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := h.outbox.Read(requestID)
	if err != nil || !bytes.Equal(restored, responseBytes) {
		t.Fatalf("strictly proven pre-command receipt restored=%v err=%v", bytes.Equal(restored, responseBytes), err)
	}
}

// TestP149RestartRepublishesTerminalRemoteMailboxArtifacts models a process
// stop after the durable terminal receipt commits but before its derived files
// survive. The fresh processor must recreate the frozen outbox and event
// artifacts without invoking a target mutation.
func TestP149RestartRepublishesTerminalRemoteMailboxArtifacts(t *testing.T) {
	ctx := context.Background()
	h, _, commandID := p097RemoteTerminal(t, domain.CommandStateSucceeded, "remote_event_gap")
	requestID := "req-p149-terminal-artifact-recovery"
	response := p097GetCommand(t, h, requestID, commandID)
	if response.RequestState != string(store.MailboxExchangeComplete) || response.CommandID != commandID || response.AvailableEventSequence == nil || *response.AvailableEventSequence < 1 {
		t.Fatalf("terminal remote response=%+v", response)
	}

	receipt, err := h.authority.GetMailboxExchange(ctx, requestID)
	if err != nil || receipt.State != store.MailboxExchangeComplete || len(receipt.ResponseBytes) == 0 || receipt.AvailableEventSequence == nil {
		t.Fatalf("durable terminal receipt=%+v err=%v", receipt, err)
	}
	beforeIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", commandID, p063Owner(t))
	if err != nil || beforeIntent.DeliveryState != store.LocalIntentReconciled {
		t.Fatalf("remote intent before restart=%+v err=%v", beforeIntent, err)
	}
	originalOutbox, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	originalEvents, originalCursor, err := h.eventFiles.Read(domain.CommandID(commandID))
	if err != nil || originalCursor != *receipt.AvailableEventSequence {
		t.Fatalf("original event projection cursor=%d receipt=%v err=%v", originalCursor, receipt.AvailableEventSequence, err)
	}

	// This leaves exactly the durable state visible after a stop in the gap
	// between SQLite publication and the final filesystem rename.
	if err := h.outbox.Remove(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := h.eventFiles.Remove(ctx, domain.CommandID(commandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.outbox.Read(requestID); err == nil {
		t.Fatal("outbox artifact still exists after simulated stop")
	}
	if _, _, err := h.eventFiles.Read(domain.CommandID(commandID)); err == nil {
		t.Fatal("event artifact still exists after simulated stop")
	}

	restarted := p099NewProcessor(t, h, h.server)
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	restoredOutbox, err := h.outbox.Read(requestID)
	if err != nil || !bytes.Equal(restoredOutbox, originalOutbox) || !bytes.Equal(restoredOutbox, receipt.ResponseBytes) {
		t.Fatalf("restored outbox exact=%v durable=%v err=%v", bytes.Equal(restoredOutbox, originalOutbox), bytes.Equal(restoredOutbox, receipt.ResponseBytes), err)
	}
	restoredEvents, restoredCursor, err := h.eventFiles.Read(domain.CommandID(commandID))
	if err != nil || restoredCursor != originalCursor || !bytes.Equal(restoredEvents, originalEvents) {
		t.Fatalf("restored events exact=%v cursor=%d want=%d err=%v", bytes.Equal(restoredEvents, originalEvents), restoredCursor, originalCursor, err)
	}
	afterIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", commandID, p063Owner(t))
	if err != nil || afterIntent.IntentID != beforeIntent.IntentID || afterIntent.DeliveryState != beforeIntent.DeliveryState || afterIntent.AttemptCount != beforeIntent.AttemptCount {
		t.Fatalf("artifact recovery changed remote mutation intent: before=%+v after=%+v err=%v", beforeIntent, afterIntent, err)
	}
	restoredReceipt, err := h.authority.GetMailboxExchange(ctx, requestID)
	if err != nil || restoredReceipt.ResponseRevision != receipt.ResponseRevision || !bytes.Equal(restoredReceipt.ResponseBytes, receipt.ResponseBytes) {
		t.Fatalf("artifact recovery changed terminal receipt: before=%+v after=%+v err=%v", receipt, restoredReceipt, err)
	}
}
