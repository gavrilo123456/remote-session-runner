package dispatcher

import (
	"encoding/json"
	"testing"

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
