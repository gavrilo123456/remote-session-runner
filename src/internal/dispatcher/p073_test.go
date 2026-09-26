package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

func TestP073CancelBeforeSubmitDeliveryProvesBothLocalIntentsNotDelivered(t *testing.T) {
	authority := p068Authority(t)
	submit := p068SubmitIntent(t, "intent-p073-submit-cancel", "session-p073-submit-cancel", "command-p073-submit-cancel", domain.TargetKindRemote, "echo cancel")
	cancel := p073CancelIntent(t, "intent-p073-cancel", submit)
	if _, err := authority.CreateLocalIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), cancel); err != nil {
		t.Fatal(err)
	}
	caller := &p073Caller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p073", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoRemoteDispatchWork) {
		t.Fatalf("cancel-before-delivery dispatch = %v", err)
	}
	assertP073State(t, authority, submit.IntentID, store.LocalIntentNotDelivered, "cancelled_before_dispatch")
	assertP073State(t, authority, cancel.IntentID, store.LocalIntentNotDelivered, "command_not_delivered")
	if len(caller.frames) != 0 {
		t.Fatalf("cancel-before-delivery reached bridge: %+v", caller.frames)
	}
}

func TestP073CloseBeforeCreateDeliverySuppressesSessionAndCommands(t *testing.T) {
	authority := p068Authority(t)
	create := p072CreateIntent(t, "intent-p073-create-close", "session-p073-create-close", domain.SessionStateCreating)
	submit := p068SubmitIntent(t, "intent-p073-submit-close", "session-p073-create-close", "command-p073-submit-close", domain.TargetKindRemote, "echo close")
	closeIntent := p073CloseIntent(t, "intent-p073-close", create)
	for _, input := range []store.LocalIntentCreate{create, submit, closeIntent} {
		if _, err := authority.CreateLocalIntent(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	caller := &p073Caller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p073", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoRemoteDispatchWork) {
		t.Fatalf("close-before-delivery dispatch = %v", err)
	}
	assertP073State(t, authority, create.IntentID, store.LocalIntentNotDelivered, "closed_before_dispatch")
	assertP073State(t, authority, submit.IntentID, store.LocalIntentNotDelivered, "closed_before_dispatch")
	assertP073State(t, authority, closeIntent.IntentID, store.LocalIntentNotDelivered, "session_not_delivered")
	if len(caller.frames) != 0 {
		t.Fatalf("close-before-delivery reached bridge: %+v", caller.frames)
	}
}

func TestP073UncertainSubmitStillAllowsStableCancelWithoutInventingOutcome(t *testing.T) {
	authority := p068Authority(t)
	submit := p068SubmitIntent(t, "intent-p073-submit-uncertain", "session-p073-submit-uncertain", "command-p073-submit-uncertain", domain.TargetKindRemote, "echo uncertain")
	cancel := p073CancelIntent(t, "intent-p073-cancel-uncertain", submit)
	if _, err := authority.CreateLocalIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	caller := &p073Caller{firstErr: &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}}
	driver, err := NewRemoteDriver(authority, caller, "router-p073", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), submit.IntentID); err == nil {
		t.Fatal("uncertain submit unexpectedly succeeded")
	}
	if _, err := authority.CreateLocalIntent(context.Background(), cancel); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertP073State(t, authority, submit.IntentID, store.LocalIntentUncertain, "remote_transport_uncertain")
	assertP073State(t, authority, cancel.IntentID, store.LocalIntentAccepted, "remote_target_accepted")
	if len(caller.frames) != 2 || caller.frames[0].Operation != sshbridge.OperationSubmitOrResumeCommand || caller.frames[1].Operation != sshbridge.OperationCancelCommand {
		t.Fatalf("uncertain submit/cancel frames = %+v", caller.frames)
	}
}

func TestP073UncertainCreateStillAllowsStableCloseWithoutInventingSessionState(t *testing.T) {
	authority := p068Authority(t)
	create := p072CreateIntent(t, "intent-p073-create-uncertain", "session-p073-create-uncertain", domain.SessionStateCreating)
	closeIntent := p073CloseIntent(t, "intent-p073-close-uncertain", create)
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	caller := &p073Caller{firstErr: &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}}
	driver, err := NewRemoteDriver(authority, caller, "router-p073", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), create.IntentID); err == nil {
		t.Fatal("uncertain create unexpectedly succeeded")
	}
	if _, err := authority.CreateLocalIntent(context.Background(), closeIntent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertP073State(t, authority, create.IntentID, store.LocalIntentUncertain, "remote_transport_uncertain")
	assertP073State(t, authority, closeIntent.IntentID, store.LocalIntentAccepted, "remote_target_accepted")
	if len(caller.frames) != 2 || caller.frames[0].Operation != sshbridge.OperationCreateOrResumeSession || caller.frames[1].Operation != sshbridge.OperationCloseSession {
		t.Fatalf("uncertain create/close frames = %+v", caller.frames)
	}
}

type p073Caller struct {
	mu       sync.Mutex
	frames   []sshbridge.RequestFrame
	firstErr error
}

func (c *p073Caller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.mu.Lock()
	c.frames = append(c.frames, frame)
	first := len(c.frames) == 1
	err := c.firstErr
	c.mu.Unlock()
	if first && err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	return p069AcceptedReply(frame), nil
}

func p073CancelIntent(t *testing.T, intentID string, submit store.LocalIntentCreate) store.LocalIntentCreate {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"command_id":%q}`, submit.CommandID))
	canonical, err := domain.CanonicalizeMutationRequestJSON(operationCancelCommand, payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON(operationCancelCommand, canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: operationCancelCommand, ResourceID: string(submit.CommandID), SessionID: submit.SessionID, CommandID: submit.CommandID, Target: submit.Target, Environment: submit.Environment, Controller: submit.Controller, Source: submit.Source, RequestHash: hash, IdempotencyKey: "key-" + intentID, PayloadJSON: canonical}
}

func p073CloseIntent(t *testing.T, intentID string, create store.LocalIntentCreate) store.LocalIntentCreate {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"policy":"cancel","session_id":%q}`, create.SessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON(operationCloseSession, payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON(operationCloseSession, canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: operationCloseSession, ResourceID: string(create.SessionID), SessionID: create.SessionID, Target: create.Target, Environment: create.Environment, Controller: create.Controller, Source: create.Source, RequestHash: hash, IdempotencyKey: "key-" + intentID, PayloadJSON: canonical}
}

func assertP073State(t *testing.T, authority *store.AuthorityStore, id domain.IntentID, state store.LocalIntentDeliveryState, reason string) {
	t.Helper()
	current, err := authority.GetLocalIntent(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState != state || current.Reason != reason {
		t.Fatalf("intent %s = state %s reason %q, want %s/%q", id, current.DeliveryState, current.Reason, state, reason)
	}
}
