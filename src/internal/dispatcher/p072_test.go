package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

type p072Caller struct {
	mu          sync.Mutex
	frames      []sshbridge.RequestFrame
	createState domain.SessionState
	remoteState domain.SessionState
}

func (c *p072Caller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.mu.Lock()
	c.frames = append(c.frames, frame)
	state := c.remoteState
	if state == "" {
		state = c.createState
	}
	c.mu.Unlock()
	switch frame.Operation {
	case sshbridge.OperationCreateOrResumeSession:
		return p072SessionReply(frame, c.createState), nil
	case sshbridge.OperationGetSession:
		return p072SessionReply(frame, state), nil
	case sshbridge.OperationSubmitOrResumeCommand:
		return p069AcceptedReply(frame), nil
	default:
		return sshbridge.ReplyFrame{}, fmt.Errorf("unexpected P072 operation %s", frame.Operation)
	}
}

func TestP072EarlySubmitWaitsForCreateReadinessWithoutTargetMutation(t *testing.T) {
	authority := p068Authority(t)
	create := p072CreateIntent(t, "intent-p072-create-wait", "session-p072-create-wait", domain.SessionStateCreating)
	submit := p068SubmitIntent(t, "intent-p072-submit-wait", "session-p072-create-wait", "command-p072-submit-wait", domain.TargetKindRemote, "echo wait")
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	caller := &p072Caller{createState: domain.SessionStateCreating, remoteState: domain.SessionStateCreating}
	driver, err := NewRemoteDriver(authority, caller, "router-p072", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		caller.mu.Lock()
		frames := append([]sshbridge.RequestFrame(nil), caller.frames...)
		caller.mu.Unlock()
		t.Fatalf("first dispatch error=%v frames=%+v", err, frames)
	}
	if len(caller.frames) != 1 || caller.frames[0].Operation != sshbridge.OperationCreateOrResumeSession {
		t.Fatalf("first dispatch frames = %+v", caller.frames)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoRemoteDispatchWork) {
		t.Fatalf("pre-ready dispatch = %v, want no work", err)
	}
	current, err := authority.GetLocalIntent(context.Background(), submit.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("pre-ready submit state = %s, want recorded", current.DeliveryState)
	}
	caller.mu.Lock()
	caller.remoteState = domain.SessionStateReady
	caller.mu.Unlock()
	record, _, err := driver.DispatchNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if record.IntentID != submit.IntentID || record.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("ready submit result = %+v", record)
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if len(caller.frames) != 4 || caller.frames[1].Operation != sshbridge.OperationGetSession || caller.frames[2].Operation != sshbridge.OperationGetSession || caller.frames[3].Operation != sshbridge.OperationSubmitOrResumeCommand {
		t.Fatalf("readiness/submit frames = %+v", caller.frames)
	}
}

func TestP072FailedCreateProvesDependentCommandNotDelivered(t *testing.T) {
	authority := p068Authority(t)
	create := p072CreateIntent(t, "intent-p072-create-failed", "session-p072-create-failed", domain.SessionStateFailed)
	submit := p068SubmitIntent(t, "intent-p072-submit-failed", "session-p072-create-failed", "command-p072-submit-failed", domain.TargetKindRemote, "echo failed")
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	caller := &p072Caller{createState: domain.SessionStateFailed, remoteState: domain.SessionStateFailed}
	driver, err := NewRemoteDriver(authority, caller, "router-p072", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoRemoteDispatchWork) {
		t.Fatalf("failed-create follow-up = %v", err)
	}
	current, err := authority.GetLocalIntent(context.Background(), submit.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState != store.LocalIntentNotDelivered || current.Reason != "remote_session_create_failed" {
		t.Fatalf("dependent failed-create state = %+v", current)
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	for _, frame := range caller.frames {
		if frame.Operation == sshbridge.OperationSubmitOrResumeCommand {
			t.Fatalf("failed create reached submit target: %+v", caller.frames)
		}
	}
}

func TestP072DirectSubmitBeforeCreateAcceptanceDoesNotClaimOrSend(t *testing.T) {
	authority := p068Authority(t)
	create := p072CreateIntent(t, "intent-p072-create-race", "session-p072-create-race", domain.SessionStateCreating)
	submit := p068SubmitIntent(t, "intent-p072-submit-race", "session-p072-create-race", "command-p072-submit-race", domain.TargetKindRemote, "echo race")
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	caller := &p072Caller{createState: domain.SessionStateCreating, remoteState: domain.SessionStateCreating}
	driver, err := NewRemoteDriver(authority, caller, "router-p072", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), submit.IntentID); !errors.Is(err, ErrRemoteSessionNotReady) {
		t.Fatalf("early direct submit error = %v", err)
	}
	current, err := authority.GetLocalIntent(context.Background(), submit.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("early direct submit state = %s", current.DeliveryState)
	}
	caller.mu.Lock()
	defer caller.mu.Unlock()
	if len(caller.frames) != 0 {
		t.Fatalf("early direct submit reached bridge: %+v", caller.frames)
	}
}

func p072CreateIntent(t *testing.T, intentID, sessionID string, state domain.SessionState) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"dev","execution_target":{"kind":"remote","profile":"linux-host"},"operation":"create_session","session_id":%q}`, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: operationCreateSession, ResourceID: sessionID, SessionID: domain.SessionID(sessionID), Target: target, Environment: "dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-" + intentID, PayloadJSON: canonical, Reason: string(state)}
}

func p072SessionReply(frame sshbridge.RequestFrame, state domain.SessionState) sshbridge.ReplyFrame {
	payload, _ := json.Marshal(map[string]any{
		"session_id":       string(frame.ResourceID),
		"session_state":    string(state),
		"environment":      "dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
	})
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: payload}
}
