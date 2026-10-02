package dispatcher

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG009StrictRemoteJobProjectionAcceptsOnlySafeQueueBlockedReason(t *testing.T) {
	intent := store.LocalIntentRecord{LocalIntentCreate: p149RemoteRunIntent(t, "bug009-queue-block")}
	object := bug009QueuedJobReadObject(intent)
	object["queue_blocked_reason"] = json.RawMessage(`"lost_capacity_recovery_pending"`)

	projection, err := strictRemoteJobProjectionFromReadReply(intent, object, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if projection.JobID != intent.JobID || projection.SessionID != intent.SessionID || projection.CommandID != intent.CommandID ||
		projection.Phase != store.JobPhaseAwaitingCommand || projection.CommandState == nil || *projection.CommandState != domain.CommandStateQueued ||
		projection.QueueBlockedReason != store.QueueBlockedReasonLostCapacityRecoveryPending {
		t.Fatalf("strict projection=%+v", projection)
	}
}

func TestBUG009StrictRemoteJobProjectionRejectsUnsafeQueueBlockedReason(t *testing.T) {
	intent := store.LocalIntentRecord{LocalIntentCreate: p149RemoteRunIntent(t, "bug009-queue-block-invalid")}
	for _, fixture := range []struct {
		name   string
		mutate func(map[string]json.RawMessage)
	}{
		{
			name: "unknown literal",
			mutate: func(object map[string]json.RawMessage) {
				object["queue_blocked_reason"] = json.RawMessage(`"unknown"`)
			},
		},
		{
			name: "running state",
			mutate: func(object map[string]json.RawMessage) {
				object["queue_blocked_reason"] = json.RawMessage(`"lost_capacity_recovery_pending"`)
				object["command_state"] = json.RawMessage(`"running"`)
			},
		},
		{
			name: "terminal field",
			mutate: func(object map[string]json.RawMessage) {
				object["queue_blocked_reason"] = json.RawMessage(`"lost_capacity_recovery_pending"`)
				object["final_event_sequence"] = json.RawMessage(`3`)
			},
		},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			object := bug009QueuedJobReadObject(intent)
			fixture.mutate(object)
			if _, err := strictRemoteJobProjectionFromReadReply(intent, object, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)); !errors.Is(err, ErrRemoteResponse) {
				t.Fatalf("unsafe queue-block projection error=%v, want %v", err, ErrRemoteResponse)
			}
		})
	}
}

func bug009QueuedJobReadObject(intent store.LocalIntentRecord) map[string]json.RawMessage {
	payload := map[string]any{
		"job_id":           string(intent.JobID),
		"session_id":       string(intent.SessionID),
		"command_id":       string(intent.CommandID),
		"job_phase":        string(store.JobPhaseAwaitingCommand),
		"command_state":    string(domain.CommandStateQueued),
		"output_complete":  false,
		"output_truncated": false,
		"teardown_state":   string(store.JobTeardownPending),
		"execution_target": map[string]string{"kind": "remote", "profile": intent.Target.Profile()},
		"authority":        "remote",
		"controller":       map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"environment":      intent.Environment,
		"source":           map[string]string{"mode": "empty"},
		"capabilities":     map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{}},
		"observed_at":      "2026-10-02T09:00:00Z",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		panic(err)
	}
	return object
}
