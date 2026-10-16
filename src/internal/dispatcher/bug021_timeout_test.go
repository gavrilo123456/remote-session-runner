package dispatcher

import (
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestBUG021RemoteRunFrameCarriesNormalizedDeadlineAndStableIdentity(t *testing.T) {
	intent := store.LocalIntentRecord{
		IntentID:       domain.IntentID("intent-bug021-timeout"),
		Operation:      "run",
		ResourceID:     "job-bug021-timeout",
		IdempotencyKey: "key-bug021-timeout",
		JobID:          domain.JobID("job-bug021-timeout"),
		SessionID:      domain.SessionID("sess-bug021-timeout"),
		CommandID:      domain.CommandID("cmd-bug021-timeout"),
		PayloadJSON:    json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf bounded","limits":{"command_timeout_seconds":5}}`),
	}
	frame, err := frameForRemoteIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Operation != sshbridge.OperationRunOrResumeJob || frame.ResourceID != intent.ResourceID || frame.IdempotencyKey != intent.IdempotencyKey || frame.RequestID != string(intent.IntentID) {
		t.Fatalf("run frame identity=%+v", frame)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var limits map[string]int64
	if err := json.Unmarshal(payload["limits"], &limits); err != nil || limits["command_timeout_seconds"] != 5 {
		t.Fatalf("run frame lost normalized deadline: limits=%v err=%v payload=%s", limits, err, frame.Payload)
	}
	if _, ok := payload["timeout_seconds"]; ok {
		t.Fatalf("run frame contains unsupported timeout alias: %s", frame.Payload)
	}
	second, err := frameForRemoteIntent(intent)
	if err != nil || second.RequestID != frame.RequestID || second.ResourceID != frame.ResourceID || second.IdempotencyKey != frame.IdempotencyKey || string(second.Payload) != string(frame.Payload) {
		t.Fatalf("run retry changed frame identity/payload: first=%+v second=%+v err=%v", frame, second, err)
	}
}
