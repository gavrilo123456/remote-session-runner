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
