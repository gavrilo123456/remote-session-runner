package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP104RemoteSessionReadinessProbeMatchesFrozenGetSessionFrame(t *testing.T) {
	create := store.LocalIntentRecord{LocalIntentCreate: store.LocalIntentCreate{
		IntentID:   domain.IntentID("intent-p104-readiness"),
		Operation:  operationCreateSession,
		ResourceID: "session-p104-readiness",
		SessionID:  domain.SessionID("session-p104-readiness"),
	}}
	frame, err := readinessFrameForCreateIntent(create)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Operation != sshbridge.OperationGetSession || frame.ResourceID != "" || frame.IdempotencyKey != "" {
		t.Fatalf("readiness frame=%+v, want get_session without mutation-only fields", frame)
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := sshbridge.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("readiness frame does not satisfy the frozen bridge schema: %v", err)
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil || payload.SessionID != string(create.SessionID) {
		t.Fatalf("readiness payload=%+v err=%v", payload, err)
	}
}

func TestP104RemoteProjectionValidatesQueuedPrincipalAndRetainsMailboxOwner(t *testing.T) {
	create := p076CreateIntent(t)
	localOwner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	create.Controller = localOwner
	intent := store.LocalIntentRecord{LocalIntentCreate: create}
	frame := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       "p104-controller-projection",
		Operation:       sshbridge.OperationCreateOrResumeSession,
		ResourceID:      string(create.SessionID),
		Payload:         json.RawMessage(`{}`),
	}
	reply, err := (p076ProjectionCaller{}).Call(context.Background(), frame)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(reply.Payload, &object); err != nil {
		t.Fatal(err)
	}
	projection, present, err := remoteSessionProjectionFromReply(intent, object, time.Now())
	if err != nil || !present {
		t.Fatalf("mailbox projection present=%v err=%v", present, err)
	}
	if projection.Controller != localOwner {
		t.Fatalf("remote projection controller=%+v, want local mailbox owner %+v", projection.Controller, localOwner)
	}

	object["controller"], _ = json.Marshal(map[string]string{
		"controller_type": string(domain.ControllerTypeQueuedMac),
		"controller_id":   "different-user",
	})
	if _, _, err := remoteSessionProjectionFromReply(intent, object, time.Now()); !errors.Is(err, ErrRemoteResponse) {
		t.Fatalf("projection with a different authenticated controller err=%v, want ErrRemoteResponse", err)
	}
}
