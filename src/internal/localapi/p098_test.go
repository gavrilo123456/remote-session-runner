package localapi

import (
	"context"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP098MailboxCancelReplayAndDeliveryBoundary(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
	commandID := h.submitQueuedCommand(t, "req-p098-submit", "key-p098-submit", sessionID)

	request := func(requestID string) map[string]any {
		return map[string]any{
			"request_id": requestID, "idempotency_key": "key-p098-cancel", "operation": "cancel_command", "command_id": commandID,
		}
	}
	p098Import(t, h, "req-p098-cancel-first", request("req-p098-cancel-first"))
	first := readP095Response(t, h.outbox, "req-p098-cancel-first")
	if first.RequestState != "accepted" || first.CommandID != commandID || first.SessionID != sessionID || first.DeliveryState != string(store.LocalIntentRecorded) || first.CommandState != "" {
		t.Fatalf("initial cancel response invented target state: %+v", first)
	}
	cancelIntent := pMailboxIntentForRequest(t, h.authority, "cancel_command", "req-p098-cancel-first")
	if cancelIntent.ResourceID != commandID || cancelIntent.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("cancel intent=%+v", cancelIntent)
	}

	p098Import(t, h, "req-p098-cancel-replay", request("req-p098-cancel-replay"))
	replay := readP095Response(t, h.outbox, "req-p098-cancel-replay")
	replayedIntent := pMailboxIntentForRequest(t, h.authority, "cancel_command", "req-p098-cancel-replay")
	if replay.RequestState != "accepted" || replay.CommandID != commandID || replayedIntent.IntentID != cancelIntent.IntentID {
		t.Fatalf("same-key cancel replay=%+v intent=%+v", replay, replayedIntent)
	}

	cancelIntent, err := h.authority.TransitionLocalIntent(ctx, cancelIntent.IntentID, store.LocalIntentDispatching, "p098-cancel-dispatching")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, cancelIntent.IntentID, store.LocalIntentUncertain, "p098-cancel-uncertain"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	uncertain := readP095Response(t, h.outbox, "req-p098-cancel-first")
	if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.CommandState != "" || uncertain.Error != nil {
		t.Fatalf("uncertain cancel became a target outcome: %+v", uncertain)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, cancelIntent.IntentID, store.LocalIntentAccepted, "p098-cancel-accepted"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	completed := readP095Response(t, h.outbox, "req-p098-cancel-first")
	if completed.RequestState != "complete" || completed.DeliveryState != string(store.LocalIntentAccepted) || completed.CommandState != "" || completed.SessionState != "" {
		t.Fatalf("accepted cancel fabricated command/session authority state: %+v", completed)
	}
}

func TestP098MailboxCancelNotDeliveredBoundaries(t *testing.T) {
	t.Run("submit proven never delivered", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		sessionID := h.createSession(t, domain.TargetKindRemote, "linux-host", "linux-dev", false)
		p098Import(t, h, "req-p098-remote-submit", map[string]any{
			"request_id": "req-p098-remote-submit", "idempotency_key": "key-p098-remote-submit", "operation": "submit_command",
			"session_id": sessionID, "script": "echo never sent", "timeout_seconds": 30,
		})
		submitted := readP095Response(t, h.outbox, "req-p098-remote-submit")
		p098Import(t, h, "req-p098-remote-cancel", map[string]any{
			"request_id": "req-p098-remote-cancel", "idempotency_key": "key-p098-remote-cancel", "operation": "cancel_command", "command_id": submitted.CommandID,
		})
		submitIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", submitted.CommandID, p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		cancelIntent := pMailboxIntentForRequest(t, h.authority, "cancel_command", "req-p098-remote-cancel")
		if _, err := h.authority.TransitionLocalIntent(ctx, submitIntent.IntentID, store.LocalIntentNotDelivered, "p098-submit-proven-not-delivered"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.authority.TransitionLocalIntent(ctx, cancelIntent.IntentID, store.LocalIntentNotDelivered, "p098-cancel-not-delivered"); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		submitResult := readP095Response(t, h.outbox, "req-p098-remote-submit")
		cancelResult := readP095Response(t, h.outbox, "req-p098-remote-cancel")
		if submitResult.RequestState != "rejected" || submitResult.DeliveryState != string(store.LocalIntentNotDelivered) || submitResult.CommandState != "" {
			t.Fatalf("not-delivered submit response=%+v", submitResult)
		}
		if cancelResult.RequestState != "complete" || cancelResult.DeliveryState != string(store.LocalIntentNotDelivered) || cancelResult.CommandState != "" || cancelResult.Error != nil {
			t.Fatalf("cancel of a never-delivered command fabricated command state: %+v", cancelResult)
		}
	})

	t.Run("cancel proven not delivered for live command", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
		commandID := h.submitQueuedCommand(t, "req-p098-live-submit", "key-p098-live-submit", sessionID)
		p098Import(t, h, "req-p098-live-cancel", map[string]any{
			"request_id": "req-p098-live-cancel", "idempotency_key": "key-p098-live-cancel", "operation": "cancel_command", "command_id": commandID,
		})
		cancelIntent := pMailboxIntentForRequest(t, h.authority, "cancel_command", "req-p098-live-cancel")
		if _, err := h.authority.TransitionLocalIntent(ctx, cancelIntent.IntentID, store.LocalIntentNotDelivered, "p098-cancel-proven-not-delivered"); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		response := readP095Response(t, h.outbox, "req-p098-live-cancel")
		if response.RequestState != "rejected" || response.Error == nil || response.Error.Code != "runtime_unavailable" || response.DeliveryState != string(store.LocalIntentNotDelivered) || response.CommandState != "" {
			t.Fatalf("unavailable cancel response=%+v", response)
		}
	})
}

func TestP098MailboxClosePolicyReplayConflictAndTerminalBoundary(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)

	closeRequest := func(requestID string, policy any, includePolicy bool) map[string]any {
		request := map[string]any{
			"request_id": requestID, "idempotency_key": "key-p098-close", "operation": "close_session", "session_id": sessionID,
		}
		if includePolicy {
			request["close_policy"] = policy
		}
		return request
	}
	p098Import(t, h, "req-p098-close-first", closeRequest("req-p098-close-first", nil, false))
	first := readP095Response(t, h.outbox, "req-p098-close-first")
	if first.RequestState != "accepted" || first.SessionID != sessionID || first.DeliveryState != string(store.LocalIntentRecorded) || first.SessionState != "" {
		t.Fatalf("initial close response invented target state: %+v", first)
	}
	closeIntent := pMailboxIntentForRequest(t, h.authority, "close_session", "req-p098-close-first")
	if string(closeIntent.SessionID) != sessionID || string(closeIntent.PayloadJSON) == "" {
		t.Fatalf("close intent=%+v", closeIntent)
	}

	p098Import(t, h, "req-p098-close-replay", closeRequest("req-p098-close-replay", map[string]any{"policy": "cancel"}, true))
	replay := readP095Response(t, h.outbox, "req-p098-close-replay")
	replayedIntent := pMailboxIntentForRequest(t, h.authority, "close_session", "req-p098-close-replay")
	if replay.RequestState != "accepted" || replay.SessionID != sessionID || replayedIntent.IntentID != closeIntent.IntentID {
		t.Fatalf("same-key default/cancel replay=%+v intent=%+v", replay, replayedIntent)
	}

	p098Import(t, h, "req-p098-close-conflict", closeRequest("req-p098-close-conflict", map[string]any{"policy": "drain"}, true))
	conflict := readP095Response(t, h.outbox, "req-p098-close-conflict")
	if conflict.RequestState != "rejected" || conflict.Error == nil || conflict.Error.Code != "idempotency_conflict" || conflict.SessionState != "" {
		t.Fatalf("changed close policy did not conflict: %+v", conflict)
	}

	closeIntent, err := h.authority.TransitionLocalIntent(ctx, closeIntent.IntentID, store.LocalIntentDispatching, "p098-close-dispatching")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, closeIntent.IntentID, store.LocalIntentAccepted, "p098-close-accepted"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	accepted := readP095Response(t, h.outbox, "req-p098-close-first")
	if accepted.RequestState != "accepted" || accepted.DeliveryState != string(store.LocalIntentAccepted) || accepted.SessionState != "" || accepted.TeardownOutcome != "" {
		t.Fatalf("accepted close was reported as completed before target teardown: %+v", accepted)
	}
	if _, err := h.authority.TransitionSession(ctx, domain.SessionID(sessionID), domain.SessionStateClosing, "p098-session-closing"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, domain.SessionID(sessionID), domain.SessionStateClosed, "p098-session-closed"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	completed := readP095Response(t, h.outbox, "req-p098-close-first")
	if completed.RequestState != "complete" || completed.SessionState != string(domain.SessionStateClosed) || completed.TeardownOutcome != "closed" {
		t.Fatalf("terminal close response=%+v", completed)
	}
}

func TestP098MailboxCloseNotDeliveredAndNeverCreatedOutcomes(t *testing.T) {
	t.Run("close not delivered while session remains live", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
		p098Import(t, h, "req-p098-live-close", map[string]any{
			"request_id": "req-p098-live-close", "idempotency_key": "key-p098-live-close", "operation": "close_session", "session_id": sessionID,
		})
		intent := pMailboxIntentForRequest(t, h.authority, "close_session", "req-p098-live-close")
		if _, err := h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentNotDelivered, "p098-close-not-delivered"); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		response := readP095Response(t, h.outbox, "req-p098-live-close")
		if response.RequestState != "rejected" || response.Error == nil || response.Error.Code != "runtime_unavailable" || response.DeliveryState != string(store.LocalIntentNotDelivered) || response.SessionState != "" {
			t.Fatalf("not-delivered close response=%+v", response)
		}
	})

	t.Run("create proven not delivered", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		sessionID := h.createSession(t, domain.TargetKindRemote, "linux-host", "linux-dev", false)
		p098Import(t, h, "req-p098-never-created-close", map[string]any{
			"request_id": "req-p098-never-created-close", "idempotency_key": "key-p098-never-created-close", "operation": "close_session", "session_id": sessionID,
		})
		createIntent, err := h.authority.GetLocalIntentByResource(ctx, "create_session", sessionID, p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		closeIntent := pMailboxIntentForRequest(t, h.authority, "close_session", "req-p098-never-created-close")
		if _, err := h.authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentNotDelivered, "p098-create-not-delivered"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.authority.TransitionLocalIntent(ctx, closeIntent.IntentID, store.LocalIntentNotDelivered, "p098-close-not-delivered"); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		response := readP095Response(t, h.outbox, "req-p098-never-created-close")
		if response.RequestState != "complete" || response.DeliveryState != string(store.LocalIntentNotDelivered) || response.TeardownOutcome != "not_created" || response.SessionState != "" {
			t.Fatalf("never-created close response=%+v", response)
		}
	})
}

func TestP098MailboxCancelAndCloseDenyDirectCreatedResources(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "p098-direct-client")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("sess-p098-direct")
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: sessionID, Target: target, Environment: "linux-dev", Controller: controller,
		Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, sessionID, domain.SessionStateReady, "p098-direct-ready"); err != nil {
		t.Fatal(err)
	}
	script := "echo direct"
	submitPayload := []byte(`{"operation":"submit_command","script":"echo direct","timeout_seconds":30}`)
	hash, err := domain.HashMutationRequestJSON("submit_command", submitPayload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	commandID := domain.CommandID("cmd-p098-direct")
	if _, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: commandID, SessionID: sessionID, RequestHash: hash, IdempotencyKey: "direct-submit-key",
		Script: script, Timeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	p098Import(t, h, "req-p098-direct-cancel", map[string]any{
		"request_id": "req-p098-direct-cancel", "idempotency_key": "key-p098-direct-cancel", "operation": "cancel_command", "command_id": commandID,
	})
	cancel := readP095Response(t, h.outbox, "req-p098-direct-cancel")
	if cancel.RequestState != "rejected" || cancel.Error == nil || cancel.Error.Code != "resource_not_found" || cancel.CommandState != "" {
		t.Fatalf("direct-created command was exposed to Mac cancel: %+v", cancel)
	}
	if _, err := h.authority.GetLocalIntentByIdempotency(ctx, "cancel_command", "key-p098-direct-cancel", p063Owner(t)); err == nil {
		t.Fatal("direct-created command created a Mac cancel intent")
	}

	p098Import(t, h, "req-p098-direct-close", map[string]any{
		"request_id": "req-p098-direct-close", "idempotency_key": "key-p098-direct-close", "operation": "close_session", "session_id": sessionID,
	})
	close := readP095Response(t, h.outbox, "req-p098-direct-close")
	if close.RequestState != "rejected" || close.Error == nil || close.Error.Code != "resource_not_found" || close.SessionState != "" {
		t.Fatalf("direct-created session was exposed to Mac close: %+v", close)
	}
	if _, err := h.authority.GetLocalIntentByIdempotency(ctx, "close_session", "key-p098-direct-close", p063Owner(t)); err == nil {
		t.Fatal("direct-created session created a Mac close intent")
	}
}

func p098Import(t *testing.T, h *p095Harness, requestID string, request map[string]any) {
	t.Helper()
	writeP094Request(t, h.importer, requestID, request)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("P098 mailbox import %s results=%+v err=%v", requestID, results, err)
	}
}
