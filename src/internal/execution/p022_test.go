package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP022QueuedCancelIsTerminalAndCannotStart(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-queued-cancel", stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-queued-cancel", "key-queued-cancel")
	command := p022QueueCommand(t, authority, session, "command-queued-cancel", "submit-queued-cancel")
	request := p022CancelRequest(t, command.CommandID, "cancel-queued-cancel", "cancel queued")
	result, err := service.CancelCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Command.State != domain.CommandStateCancelled || runtime.cancelCall != 0 {
		t.Fatalf("cancel result = %+v, runtime cancel calls = %d", result.Command, runtime.cancelCall)
	}
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); !errors.Is(err, store.ErrCommandNotEligible) {
		t.Fatalf("start after queued cancel = %v, want not eligible", err)
	}
	events, err := authority.ListCommandEvents(context.Background(), command.CommandID)
	if err != nil || len(events) != 2 || events[1].Type != "command_cancelled" {
		t.Fatalf("cancel events = %+v, err = %v", events, err)
	}
}

func TestP022RunningCancelConfirmedReleasesSlotAndReturnsReady(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-running-cancel", cancelResult: RuntimeCommandStopResult{Stdout: []byte("stopped"), Confirmed: true}}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-running-cancel", "key-running-cancel")
	command := p022QueueCommand(t, authority, session, "command-running-cancel", "submit-running-cancel")
	started, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != command.CommandID {
		t.Fatalf("start = %+v, err = %v", started, err)
	}
	result, err := service.CancelCommand(context.Background(), p022CancelRequest(t, command.CommandID, "cancel-running-cancel", "cancel running"))
	if err != nil || result.Command.State != domain.CommandStateCancelled {
		t.Fatalf("cancel result = %+v, err = %v", result.Command, err)
	}
	if runtime.cancelCall != 1 {
		t.Fatalf("runtime cancel calls = %d, want 1", runtime.cancelCall)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || updated.State != domain.SessionStateReady {
		t.Fatalf("session after cancel = %+v, err = %v", updated, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 0 {
		t.Fatalf("live slots = %d, err = %v, want 0", got, err)
	}
}

func TestP022RunningCancelUnconfirmedLosesSessionAndRetainsSlot(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-cancel-lost", cancelResult: RuntimeCommandStopResult{Confirmed: false}}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-cancel-lost", "key-cancel-lost")
	command := p022QueueCommand(t, authority, session, "command-cancel-lost", "submit-cancel-lost")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	result, err := service.CancelCommand(context.Background(), p022CancelRequest(t, command.CommandID, "cancel-lost", "cancel lost"))
	if !errors.Is(err, ErrStopUnconfirmed) || result.Command.State != domain.CommandStateLost {
		t.Fatalf("cancel result = %+v, err = %v, want lost/stop-unconfirmed", result.Command, err)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || updated.State != domain.SessionStateLost {
		t.Fatalf("session after uncertain cancel = %+v, err = %v", updated, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live slots = %d, err = %v, want 1", got, err)
	}
}

func TestP022CancelKeyReplayAndChangedPayloadConflict(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-cancel-key"}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-cancel-key", "key-cancel-key")
	command := p022QueueCommand(t, authority, session, "command-cancel-key", "submit-cancel-key")
	request := p022CancelRequest(t, command.CommandID, "cancel-key", "policy-a")
	first, err := service.CancelCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CancelCommand(context.Background(), request)
	if err != nil || !second.Duplicate || second.Command.CommandID != first.Command.CommandID {
		t.Fatalf("same-key retry = %+v duplicate=%v err=%v", second.Command, second.Duplicate, err)
	}
	conflict := request
	conflict.RequestHash = p022Hash(t, "cancel_command", command.CommandID, "policy-b")
	if _, err := service.CancelCommand(context.Background(), conflict); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("changed cancel key error = %v, want conflict", err)
	}
}

func TestP022CloseCancelsQueuedWorkAndBlocksScheduler(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-close-queued", stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-close-queued", "key-close-queued")
	command := p022QueueCommand(t, authority, session, "command-close-queued", "submit-close-queued")
	request := p022CloseRequest(t, session.SessionID, "close-queued", "graceful")
	closed, err := service.CloseSession(context.Background(), request)
	if err != nil || closed.Session.State != domain.SessionStateClosed {
		t.Fatalf("close result = %+v, err = %v", closed.Session, err)
	}
	commandAfter, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || commandAfter.State != domain.CommandStateCancelled {
		t.Fatalf("queued command after close = %+v, err = %v", commandAfter, err)
	}
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); !errors.Is(err, store.ErrCommandSlotsFull) && !errors.Is(err, store.ErrCommandNotEligible) {
		t.Fatalf("start after close = %v, want no eligible command", err)
	}
	if runtime.stopCall != 1 {
		t.Fatalf("stop calls = %d, want 1", runtime.stopCall)
	}
}

func TestP022CloseRunningConfirmedCancelsAndClosesAtomically(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-close-running", cancelResult: RuntimeCommandStopResult{Confirmed: true}, stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-close-running", "key-close-running")
	command := p022QueueCommand(t, authority, session, "command-close-running", "submit-close-running")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	closed, err := service.CloseSession(context.Background(), p022CloseRequest(t, session.SessionID, "close-running", "graceful"))
	if err != nil || closed.Session.State != domain.SessionStateClosed {
		t.Fatalf("close result = %+v, err = %v", closed.Session, err)
	}
	commandAfter, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || commandAfter.State != domain.CommandStateCancelled {
		t.Fatalf("running command after close = %+v, err = %v", commandAfter, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 0 {
		t.Fatalf("live slots after close = %d, err = %v, want 0", got, err)
	}
}

func TestP022CloseUnconfirmedStopLosesSessionAndRetainsRunningSlot(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-close-lost", cancelResult: RuntimeCommandStopResult{Confirmed: false}, stopConfirmed: false}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-close-lost", "key-close-lost")
	command := p022QueueCommand(t, authority, session, "command-close-lost", "submit-close-lost")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	closed, err := service.CloseSession(context.Background(), p022CloseRequest(t, session.SessionID, "close-lost", "graceful"))
	if !errors.Is(err, ErrStopUnconfirmed) || closed.Session.State != domain.SessionStateLost {
		t.Fatalf("close result = %+v, err = %v", closed.Session, err)
	}
	commandAfter, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || commandAfter.State != domain.CommandStateLost {
		t.Fatalf("command after uncertain close = %+v, err = %v", commandAfter, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live slots after uncertain close = %d, err = %v, want 1", got, err)
	}
}

func p022QueueCommand(t *testing.T, authority *store.AuthorityStore, session store.SessionRecord, commandID, key string) store.CommandRecord {
	t.Helper()
	request := p021SubmitRequest(t, session.SessionID, commandID, key, "echo queued")
	request.Timeout = session.Limits.CommandTimeout
	command, duplicate, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: request.CommandID, SessionID: request.SessionID, RequestHash: request.RequestHash,
		IdempotencyKey: request.IdempotencyKey, IdempotencyRetention: request.IdempotencyRetention,
		Script: request.Script, Timeout: request.Timeout,
	})
	if err != nil || duplicate {
		t.Fatalf("queue command = %+v duplicate=%v err=%v", command, duplicate, err)
	}
	return command
}

func p022CancelRequest(t *testing.T, commandID domain.CommandID, key, label string) CancelCommandRequest {
	t.Helper()
	return CancelCommandRequest{CommandID: commandID, Controller: p020Controller(t, domain.ControllerTypeLocalUser), IdempotencyKey: key, RequestHash: p022Hash(t, "cancel_command", commandID, label)}
}

func p022CloseRequest(t *testing.T, sessionID domain.SessionID, key, policy string) CloseSessionRequest {
	t.Helper()
	raw := []byte(fmt.Sprintf(`{"operation":"close_session","session_id":%q,"policy":%q}`, sessionID, policy))
	hash, err := domain.HashMutationRequestJSON("close_session", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return CloseSessionRequest{SessionID: sessionID, Controller: p020Controller(t, domain.ControllerTypeLocalUser), IdempotencyKey: key, RequestHash: hash, Policy: policy}
}

func p022Hash(t *testing.T, operation string, id domain.CommandID, label string) domain.CanonicalHash {
	t.Helper()
	raw := []byte(fmt.Sprintf(`{"operation":%q,"command_id":%q,"label":%q}`, operation, id, label))
	hash, err := domain.HashMutationRequestJSON(operation, raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
