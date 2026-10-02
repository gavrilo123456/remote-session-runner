package runnerd

import (
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG009JobReadResponsesCarryOnlyTheSafeQueueBlockedReason(t *testing.T) {
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	queued := domain.CommandStateQueued
	record := store.JobRecord{
		JobID:              "job-bug009-read",
		SessionID:          "sess-bug009-read",
		CommandID:          "cmd-bug009-read",
		Phase:              store.JobPhaseAwaitingCommand,
		CommandState:       &queued,
		QueueBlockedReason: store.QueueBlockedReasonLostCapacityRecoveryPending,
		TeardownState:      store.JobTeardownPending,
		Target:             target,
		Controller:         controller,
		Environment:        "linux-dev",
		Source:             domain.NewEmptySource(),
		UpdatedAt:          time.Date(2026, 10, 2, 9, 10, 0, 0, time.UTC),
	}
	private := jobResponseFromRecordWithCapabilities(record, false, commandCapabilitiesResponse{})
	direct := directJobResourceFromRecordWithCapabilities(record, commandCapabilitiesResponse{})
	if private.QueueBlockedReason != store.QueueBlockedReasonLostCapacityRecoveryPending || direct.QueueBlockedReason != store.QueueBlockedReasonLostCapacityRecoveryPending {
		t.Fatalf("job read response lost queue reason: private=%+v direct=%+v", private, direct)
	}

	acceptance, err := json.Marshal(jobAcceptanceResponseFromRecord(record, false))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(acceptance, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["queue_blocked_reason"]; present {
		t.Fatalf("identity-only mutation acceptance exposed status-only reason: %s", acceptance)
	}
}
