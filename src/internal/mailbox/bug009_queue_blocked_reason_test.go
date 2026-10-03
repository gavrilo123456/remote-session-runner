package mailbox

import (
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG009MailboxQueueBlockedReasonIsNarrowAndRetractable(t *testing.T) {
	queued := domain.CommandStateQueued
	snapshot := RunSnapshot{
		JobID: "job-bug009-mailbox", SessionID: "sess-bug009-mailbox", CommandID: "cmd-bug009-mailbox",
		DeliveryState: string(store.LocalIntentAccepted),
		ActiveRunProjection: &ActiveRunProjection{
			JobPhase:           store.JobPhaseAwaitingCommand,
			CommandState:       &queued,
			QueueBlockedReason: store.QueueBlockedReasonLostCapacityRecoveryPending,
		},
	}
	if err := validateActiveRunProjection(snapshot); err != nil {
		t.Fatalf("valid queue-block projection rejected: %v", err)
	}
	response := acceptedRunProgressResponse("req-bug009-mailbox", snapshot)
	response.ResponseRevision = 1
	if response.RequestState != store.MailboxExchangeAccepted || response.DeliveryState != string(store.LocalIntentAccepted) ||
		response.JobID != snapshot.JobID || response.SessionID != snapshot.SessionID || response.CommandID != snapshot.CommandID ||
		response.JobPhase != string(store.JobPhaseAwaitingCommand) || response.CommandState != string(domain.CommandStateQueued) ||
		response.QueueBlockedReason != store.QueueBlockedReasonLostCapacityRecoveryPending {
		t.Fatalf("queue-block response=%+v", response)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	value, err := p004MailboxDecode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := p004MailboxSchemas(t)["response"].Validate(value); err != nil {
		t.Fatalf("queue-block response does not match schema: %v", err)
	}
	if err := p004MailboxSemantic("response", value); err != nil {
		t.Fatalf("queue-block response does not match mailbox semantics: %v", err)
	}
	p166AssertNoQueueBlockedResultFields(t, raw)
	if !sameAcceptedRunProgress(response, snapshot) {
		t.Fatalf("identical queue-block status should be stable: %+v", response)
	}

	// Recovery can free capacity while the target has not yet claimed the
	// command. The next response must remove the reason even though phase and
	// command state are unchanged.
	cleared := snapshot
	cleared.ActiveRunProjection = &ActiveRunProjection{JobPhase: store.JobPhaseAwaitingCommand, CommandState: &queued}
	if sameAcceptedRunProgress(response, cleared) {
		t.Fatal("clearing queue-block reason did not require a new response revision")
	}
	clearedResponse := acceptedRunProgressResponse("req-bug009-mailbox", cleared)
	clearedResponse.ResponseRevision = response.ResponseRevision + 1
	clearedRaw, err := json.Marshal(clearedResponse)
	if err != nil {
		t.Fatal(err)
	}
	var clearedWire map[string]json.RawMessage
	if err := json.Unmarshal(clearedRaw, &clearedWire); err != nil {
		t.Fatal(err)
	}
	if _, present := clearedWire["queue_blocked_reason"]; present {
		t.Fatalf("cleared active receipt retained queue reason: %s", clearedRaw)
	}
	p166AssertNoQueueBlockedResultFields(t, clearedRaw)
}

func TestBUG009MailboxQueueBlockedReasonRejectsUnsafeState(t *testing.T) {
	queued := domain.CommandStateQueued
	running := domain.CommandStateRunning
	for _, fixture := range []struct {
		name     string
		snapshot RunSnapshot
	}{
		{
			name: "unknown literal",
			snapshot: RunSnapshot{DeliveryState: string(store.LocalIntentAccepted), ActiveRunProjection: &ActiveRunProjection{
				JobPhase: store.JobPhaseAwaitingCommand, CommandState: &queued, QueueBlockedReason: "unknown",
			}},
		},
		{
			name: "running command",
			snapshot: RunSnapshot{DeliveryState: string(store.LocalIntentAccepted), ActiveRunProjection: &ActiveRunProjection{
				JobPhase: store.JobPhaseAwaitingCommand, CommandState: &running, QueueBlockedReason: store.QueueBlockedReasonLostCapacityRecoveryPending,
			}},
		},
		{
			name: "wrong phase",
			snapshot: RunSnapshot{DeliveryState: string(store.LocalIntentAccepted), ActiveRunProjection: &ActiveRunProjection{
				JobPhase: store.JobPhaseCreatingSession, CommandState: &queued, QueueBlockedReason: store.QueueBlockedReasonLostCapacityRecoveryPending,
			}},
		},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			if err := validateActiveRunProjection(fixture.snapshot); err == nil {
				t.Fatalf("unsafe queue-block projection accepted: %+v", fixture.snapshot)
			}
		})
	}
}

func p166AssertNoQueueBlockedResultFields(t *testing.T, raw []byte) {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"observed_at", "exit_code", "stdout", "stderr", "final_event_sequence", "available_event_sequence",
		"output_complete", "output_truncated", "output_unavailable_reason", "events_file", "teardown_outcome", "error",
	} {
		if _, present := wire[field]; present {
			t.Fatalf("queue-block receipt exposed %q: %s", field, raw)
		}
	}
}
