package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

// TestBUG009GetJobStatusExplainsOnlyTheExactRetainedCapacityBoundary proves
// the optional status is derived from the same authority snapshot as the job.
// It never becomes durable job data and disappears before a queued command can
// be started after the existing P2 paired recovery releases capacity.
func TestBUG009GetJobStatusExplainsOnlyTheExactRetainedCapacityBoundary(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	pairs := pBUG008LostPairs(t, authority, "bug009-status")
	queued := pBUG008QueuedOneOff(t, authority, "bug009-status")

	status, err := authority.GetJobStatus(ctx, queued.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.JobID != queued.JobID || status.SessionID != queued.SessionID || status.CommandID != queued.CommandID ||
		status.Phase != JobPhaseAwaitingCommand || status.CommandState == nil || *status.CommandState != domain.CommandStateQueued ||
		status.QueueBlockedReason != QueueBlockedReasonLostCapacityRecoveryPending {
		t.Fatalf("exact retained-capacity job status=%+v", status)
	}

	// The explanation is a status projection only. The ordinary durable read
	// stays free of it, so no idempotency, event, or job record is mutated by a
	// status request.
	durable, err := authority.GetJob(ctx, queued.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.QueueBlockedReason != "" || durable.JobID != queued.JobID || durable.SessionID != queued.SessionID || durable.CommandID != queued.CommandID {
		t.Fatalf("status explanation became durable job data: %+v", durable)
	}
	if slots, err := authority.CountLiveCommandSlots(ctx); err != nil || slots != DefaultRunningCommandLimit {
		t.Fatalf("status read changed retained command capacity=%d err=%v", slots, err)
	}

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	cleared, err := authority.GetJobStatus(ctx, queued.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.QueueBlockedReason != "" || cleared.CommandState == nil || *cleared.CommandState != domain.CommandStateQueued {
		t.Fatalf("status after capacity release=%+v; queue reason must clear before start", cleared)
	}

	started, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit)
	if err != nil || started.CommandID != queued.CommandID {
		t.Fatalf("start preserved queued command=%+v err=%v", started, err)
	}
	afterStart, err := authority.GetJobStatus(ctx, queued.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStart.QueueBlockedReason != "" {
		t.Fatalf("status after command start=%+v; queue reason must be absent", afterStart)
	}
}

func TestBUG009GetJobStatusOmitsReasonWithoutTheFullRetainedInventory(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 2, 8, 31, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	// Three retained terminal-lost pairs cannot prove that the selected host
	// has exhausted the configured four command slots.
	for index := 0; index < DefaultRunningCommandLimit-1; index++ {
		pStalledRecoveryLostPair(t, authority, fmt.Sprintf("bug009-incomplete-%d", index))
	}
	queued := pBUG008QueuedOneOff(t, authority, "bug009-incomplete")
	status, err := authority.GetJobStatus(ctx, queued.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.QueueBlockedReason != "" || status.CommandState == nil || *status.CommandState != domain.CommandStateQueued {
		t.Fatalf("incomplete retained inventory exposed queue reason: %+v", status)
	}
}
