package runnerlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

// TestBUG008ActiveRemoteProjectionRequiresFreshReadAfterRestart uses the real
// marker-last mailbox, SQLite authority, local API, dispatcher, and service
// scheduler with an in-memory bridge. It proves an old accepted projection is
// withdrawn at a new relay process boundary and becomes visible again only
// after a new strict GET-job read. No remote host or network listener is used.
func TestBUG008ActiveRemoteProjectionRequiresFreshReadAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	now := time.Now().UTC().Truncate(time.Second)
	caller := newP162RemoteCaller(func() time.Time { return now })
	h := newP162Harness(t, root, databasePath, &now, caller)
	client := p162Client(t, h.mailboxRoot)

	const requestID = "req-bug008-active-projection-restart"
	p162WriteRunRequest(t, client, requestID, "key-bug008-active-projection-restart", "printf P162_ACTIVE_STATUS")
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	active := p162ReadResponse(t, client, requestID)
	if active.RequestState != string(store.MailboxExchangeAccepted) || active.DeliveryState != string(store.LocalIntentAccepted) ||
		active.JobPhase != string(store.JobPhaseAwaitingCommand) || active.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("fresh strict status did not create active mailbox receipt: %+v", active)
	}
	p166AssertNoActiveResultFields(t, p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix)))
	intent := p162IntentForRequest(t, h.authority, h.owner, requestID)
	if caller.mutationCount(string(intent.JobID)) != 1 || caller.statusReadCount(string(intent.JobID)) != 1 {
		t.Fatalf("initial remote frames mutation=%d get_job=%d, want one each", caller.mutationCount(string(intent.JobID)), caller.statusReadCount(string(intent.JobID)))
	}
	projection, err := h.authority.GetRemoteJobProjection(ctx, intent.JobID)
	if err != nil || projection.IsStale || projection.Controller != h.owner {
		t.Fatalf("initial fresh projection=%+v err=%v", projection, err)
	}

	// Model a new runner-local process while the target status read is
	// unavailable. The historic active receipt must not survive this boundary.
	job := caller.jobs[string(intent.JobID)]
	if job == nil {
		t.Fatal("active remote job was not retained by the fake bridge")
	}
	job.statusFailure = true
	h.close()
	h = newP162Harness(t, root, databasePath, &now, caller)
	client = p162Client(t, h.mailboxRoot)
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	withdrawn := p162ReadResponse(t, client, requestID)
	if withdrawn.RequestState != string(store.MailboxExchangeAccepted) || withdrawn.DeliveryState != string(store.LocalIntentAccepted) ||
		withdrawn.JobPhase != "" || withdrawn.CommandState != "" || withdrawn.Error != nil ||
		withdrawn.ResponseRevision != active.ResponseRevision+1 {
		t.Fatalf("restart with unavailable status did not withdraw active receipt: %+v", withdrawn)
	}
	withdrawnBytes := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))
	p166AssertNoActiveResultFields(t, withdrawnBytes)
	projection, err = h.authority.GetRemoteJobProjection(ctx, intent.JobID)
	if err != nil || !projection.IsStale {
		t.Fatalf("failed restart status did not retain stale projection: %+v err=%v", projection, err)
	}
	if caller.mutationCount(string(intent.JobID)) != 1 || caller.statusReadCount(string(intent.JobID)) != 2 {
		t.Fatalf("restart replayed remote mutation or skipped strict status: mutation=%d get_job=%d", caller.mutationCount(string(intent.JobID)), caller.statusReadCount(string(intent.JobID)))
	}

	// A later successful read-only GET-job clears staleness and may safely
	// restore the same narrow active receipt. It never sends RUN again.
	job.statusFailure = false
	h.service.remoteReconcileMu.Lock()
	h.service.lastRemoteReconcile = time.Time{}
	h.service.remoteReconcileMu.Unlock()
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	refreshed := p162ReadResponse(t, client, requestID)
	if refreshed.RequestState != string(store.MailboxExchangeAccepted) || refreshed.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		refreshed.CommandState != string(domain.CommandStateQueued) || refreshed.ResponseRevision != withdrawn.ResponseRevision+1 {
		t.Fatalf("fresh strict GET-job did not restore active receipt: %+v", refreshed)
	}
	p166AssertNoActiveResultFields(t, p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix)))
	projection, err = h.authority.GetRemoteJobProjection(ctx, intent.JobID)
	if err != nil || projection.IsStale {
		t.Fatalf("successful strict status did not clear stale projection: %+v err=%v", projection, err)
	}
	if caller.mutationCount(string(intent.JobID)) != 1 || caller.statusReadCount(string(intent.JobID)) != 3 {
		t.Fatalf("fresh status replayed remote mutation: mutation=%d get_job=%d", caller.mutationCount(string(intent.JobID)), caller.statusReadCount(string(intent.JobID)))
	}

	// A subsequent unchanged poll has no reason to advance the response.
	stableBytes := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	if got := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix)); !bytes.Equal(stableBytes, got) {
		t.Fatalf("unchanged fresh active receipt churned after restart:\nwant=%s\n got=%s", stableBytes, got)
	}
}

// TestBUG009QueueBlockedReasonProjectsToMatchingMailboxThenClearsOnStart
// uses the complete marker-last mailbox path and the in-memory remote bridge
// to model the only permitted P3 status transition. A queued remote one-off
// is briefly blocked while lost-capacity recovery is pending; once its command
// starts, the same accepted nonterminal mailbox receipt keeps its identity and
// loses the explanatory field.
func TestBUG009QueueBlockedReasonProjectsToMatchingMailboxThenClearsOnStart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	now := time.Now().UTC().Truncate(time.Second)
	caller := newP162RemoteCaller(func() time.Time { return now })
	h := newP162Harness(t, root, databasePath, &now, caller)
	client := p162Client(t, h.mailboxRoot)

	const requestID = "req-bug009-queue-blocked-reason"
	const idempotencyKey = "key-bug009-queue-blocked-reason"
	p162WriteRunRequest(t, client, requestID, idempotencyKey, "printf P3_QUEUE_BLOCKED_STATUS")
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)

	queued := p162ReadResponse(t, client, requestID)
	if queued.RequestState != string(store.MailboxExchangeAccepted) ||
		queued.DeliveryState != string(store.LocalIntentAccepted) ||
		queued.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		queued.CommandState != string(domain.CommandStateQueued) ||
		queued.QueueBlockedReason != store.QueueBlockedReasonLostCapacityRecoveryPending ||
		queued.JobID == "" || queued.SessionID == "" || queued.CommandID == "" {
		t.Fatalf("queued retained-capacity receipt=%+v, want accepted queued reason", queued)
	}
	queuedRaw := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))
	p166AssertQueueBlockedReason(t, queuedRaw, store.QueueBlockedReasonLostCapacityRecoveryPending)
	p166AssertNoActiveResultFields(t, queuedRaw)
	queuedExchange := p162Exchange(t, h.authority, requestID)
	if !bytes.Equal(queuedRaw, queuedExchange.ResponseBytes) {
		t.Fatalf("queued outbox differs from durable mailbox response:\noutbox=%s\ndurable=%s", queuedRaw, queuedExchange.ResponseBytes)
	}
	queuedCanonicalPayload := append([]byte(nil), queuedExchange.CanonicalPayload...)
	if queuedExchange.IdempotencyKey != idempotencyKey {
		t.Fatalf("queued exchange changed immutable request binding: %+v", queuedExchange)
	}
	intent := p162IntentForRequest(t, h.authority, h.owner, requestID)
	if caller.mutationCount(string(intent.JobID)) != 1 {
		t.Fatalf("queued status sent %d remote mutations, want exactly one", caller.mutationCount(string(intent.JobID)))
	}
	job := caller.jobs[string(intent.JobID)]
	if job == nil || !job.queueBlockedStatus || !job.activeStatus {
		t.Fatalf("fake remote job=%+v, want queued blocked active status", job)
	}

	// An unreadable target status immediately withdraws the field with the
	// rest of the active projection. The durable request identity remains the
	// same, but an old retained-capacity observation cannot survive as current.
	job.statusFailure = true
	h.service.remoteReconcileMu.Lock()
	h.service.lastRemoteReconcile = time.Time{}
	h.service.remoteReconcileMu.Unlock()
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	withdrawn := p162ReadResponse(t, client, requestID)
	if withdrawn.RequestState != string(store.MailboxExchangeAccepted) ||
		withdrawn.DeliveryState != string(store.LocalIntentAccepted) ||
		withdrawn.JobPhase != "" || withdrawn.CommandState != "" ||
		withdrawn.QueueBlockedReason != "" || withdrawn.ResponseRevision != queued.ResponseRevision+1 {
		t.Fatalf("unreadable status retained queue-block explanation: %+v", withdrawn)
	}
	withdrawnRaw := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))
	p166AssertQueueBlockedReason(t, withdrawnRaw, "")
	p166AssertNoActiveResultFields(t, withdrawnRaw)
	failedIntent, err := h.authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || failedIntent.RemoteStatusFailureAt == nil ||
		failedIntent.RemoteStatusFailureCode != store.RemoteStatusFailureCodeUnavailable {
		t.Fatalf("unreadable status did not retain the safe status-failure marker: %+v err=%v", failedIntent, err)
	}

	// The remote command has started. It remains nonterminal, but the exact
	// retained-capacity explanation is no longer true and must disappear.
	job.statusFailure = false
	job.queueBlockedStatus = false
	job.runningStatus = true
	h.service.remoteReconcileMu.Lock()
	h.service.lastRemoteReconcile = time.Time{}
	h.service.remoteReconcileMu.Unlock()
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)

	started := p162ReadResponse(t, client, requestID)
	if started.RequestState != string(store.MailboxExchangeAccepted) ||
		started.DeliveryState != string(store.LocalIntentAccepted) ||
		started.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		started.CommandState != string(domain.CommandStateRunning) ||
		started.QueueBlockedReason != "" ||
		started.ResponseRevision != withdrawn.ResponseRevision+1 {
		t.Fatalf("started receipt=%+v, want accepted running receipt with cleared reason and one revision", started)
	}
	if started.RequestID != requestID || started.JobID != queued.JobID || started.SessionID != queued.SessionID || started.CommandID != queued.CommandID {
		t.Fatalf("started receipt changed immutable identifiers: queued=%+v started=%+v", queued, started)
	}
	startedRaw := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))
	p166AssertQueueBlockedReason(t, startedRaw, "")
	p166AssertNoActiveResultFields(t, startedRaw)
	startedExchange := p162Exchange(t, h.authority, requestID)
	if !bytes.Equal(startedRaw, startedExchange.ResponseBytes) {
		t.Fatalf("started outbox differs from durable mailbox response:\noutbox=%s\ndurable=%s", startedRaw, startedExchange.ResponseBytes)
	}
	if startedExchange.IdempotencyKey != idempotencyKey || startedExchange.ResourceID != queuedExchange.ResourceID ||
		!bytes.Equal(startedExchange.CanonicalPayload, queuedCanonicalPayload) {
		t.Fatalf("started status changed immutable mailbox binding: queued=%+v started=%+v", queuedExchange, startedExchange)
	}
	projection, err := h.authority.GetRemoteJobProjection(ctx, intent.JobID)
	if err != nil || projection.QueueBlockedReason != "" || projection.CommandState == nil || *projection.CommandState != domain.CommandStateRunning {
		t.Fatalf("started remote projection=%+v err=%v, want running with no blocked reason", projection, err)
	}
	if caller.mutationCount(string(intent.JobID)) != 1 {
		t.Fatalf("status transition replayed remote mutation=%d, want one", caller.mutationCount(string(intent.JobID)))
	}
	refreshedIntent, err := h.authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || refreshedIntent.RemoteStatusFailureAt != nil || refreshedIntent.RemoteStatusFailureCode != "" {
		t.Fatalf("fresh running status did not clear the temporary status-failure marker: %+v err=%v", refreshedIntent, err)
	}
}

func p166AssertQueueBlockedReason(t *testing.T, raw []byte, want string) {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	gotRaw, present := wire["queue_blocked_reason"]
	if want == "" {
		if present {
			t.Fatalf("active receipt retained cleared queue_blocked_reason: %s", raw)
		}
		return
	}
	var got string
	if !present || json.Unmarshal(gotRaw, &got) != nil || got != want {
		t.Fatalf("queue_blocked_reason raw=%s, want %q in %s", gotRaw, want, raw)
	}
}

func p166AssertNoActiveResultFields(t *testing.T, raw []byte) {
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
			t.Fatalf("active or withdrawn receipt exposed %q: %s", field, raw)
		}
	}
}
