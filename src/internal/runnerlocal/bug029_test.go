package runnerlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/mailboxclient"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

const bug029StalledScript = "printf 'BUG029_STALLED_OBSERVER\\n'"

// TestBUG029StalledObservationPreservesMailboxProgressAndOriginalResults
// reproduces the incident using the actual mailbox/authority/driver cycle.
// A transport-level stall must neither wait for the service to stop nor block
// a later original ready pair. Recovery must consume retained terminal proof,
// without a second RUN, a replacement identity or an acknowledgment.
func TestBUG029StalledObservationPreservesMailboxProgressAndOriginalResults(t *testing.T) {
	for _, stage := range []string{"status", "events"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			now := time.Now().UTC().Truncate(time.Second)
			inner := newP162RemoteCaller(func() time.Time { return now })
			h := newP162Harness(t, root, filepath.Join(root, "state", "authority.db"), &now, inner)
			caller := &bug029ObserverCaller{inner: inner, stage: stage}
			caller.blocked.Store(true)
			resolver, err := dispatcher.NewRemoteCallerResolver(map[string]dispatcher.RemoteCaller{p162TargetProfile: caller})
			if err != nil {
				t.Fatal(err)
			}
			driver, err := dispatcher.NewRemoteDriverWithResolverAndClock(h.authority, resolver, "router-bug029", time.Minute,
				func() time.Time { return now.UTC() }, dispatcher.RemoteObservationOptions{
					Timeout: 100 * time.Millisecond, BatchTimeout: 100 * time.Millisecond,
				})
			if err != nil {
				t.Fatal(err)
			}
			h.service.remoteDriver = driver
			client := p162Client(t, h.mailboxRoot)
			const originalID = "req-bug029-original-observation"
			const laterID = "req-bug029-later-original-ready"
			p162WriteRunRequest(t, client, originalID, "key-bug029-original", bug029StalledScript)
			bug029RunBoundedCycle(t, h.service)

			accepted := p162ReadResponse(t, client, originalID)
			if accepted.RequestState != string(store.MailboxExchangeAccepted) || accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" ||
				accepted.AvailableEventSequence != nil || accepted.ExitCode != nil || accepted.TeardownOutcome != "" {
				t.Fatalf("stalled observation response=%+v, want original nonterminal accepted receipt", accepted)
			}
			originalIntent := p162IntentForRequest(t, h.authority, h.owner, originalID)
			if caller.timeouts.Load() == 0 || originalIntent.DeliveryState != store.LocalIntentAccepted ||
				originalIntent.AttemptCount != 1 || originalIntent.RemoteStatusFailureAt == nil || store.HasRemoteTerminalProof(originalIntent) {
				t.Fatalf("stalled intent=%+v timeouts=%d, want accepted identity with durable observation failure only", originalIntent, caller.timeouts.Load())
			}
			originalBinding := p162Exchange(t, h.authority, originalID)

			// This pair arrives only after the first observer has stalled. It must
			// be picked up by the existing loop, without republishing either input.
			p162WriteRunRequest(t, client, laterID, "key-bug029-later", "printf 'P162_COMPLETE_OK\\n'")
			// Match the supported workspace-publisher modes in the incident. No
			// service cycle runs between publication and this fixture adjustment.
			for _, suffix := range []string{".json", ".ready"} {
				if err := os.Chmod(filepath.Join(h.mailboxRoot, "inbox", laterID+suffix), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var later mailboxclient.Response
			for cycle := 0; cycle < 4; cycle++ {
				bug029RunBoundedCycle(t, h.service)
				later = p162ReadResponse(t, client, laterID)
				if later.RequestState == string(store.MailboxExchangeComplete) {
					break
				}
			}
			bug029AssertTerminal(t, client, later)
			for _, suffix := range []string{".json", ".ready"} {
				if _, err := os.Stat(filepath.Join(h.mailboxRoot, "inbox", laterID+suffix)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("original later inbox pair was not consumed: suffix=%s err=%v", suffix, err)
				}
			}
			stillAccepted := p162ReadResponse(t, client, originalID)
			if stillAccepted.RequestState != string(store.MailboxExchangeAccepted) || stillAccepted.JobID != accepted.JobID ||
				stillAccepted.SessionID != accepted.SessionID || stillAccepted.CommandID != accepted.CommandID || stillAccepted.AvailableEventSequence != nil {
				t.Fatalf("independent progress altered or settled the unproved original request: before=%+v after=%+v", accepted, stillAccepted)
			}

			// A qualified read later returns the already retained target result.
			// It must restore the original event boundary and teardown, not RUN.
			caller.blocked.Store(false)
			var restored mailboxclient.Response
			for cycle := 0; cycle < 4; cycle++ {
				bug029RunBoundedCycle(t, h.service)
				restored = p162ReadResponse(t, client, originalID)
				if restored.RequestState == string(store.MailboxExchangeComplete) {
					break
				}
			}
			bug029AssertTerminal(t, client, restored)
			if restored.JobID != accepted.JobID || restored.SessionID != accepted.SessionID || restored.CommandID != accepted.CommandID || restored.ResponseRevision <= accepted.ResponseRevision {
				t.Fatalf("qualified recovery changed original identity or did not advance its response: before=%+v after=%+v", accepted, restored)
			}
			binding := p162Exchange(t, h.authority, originalID)
			if binding.IdempotencyKey != originalBinding.IdempotencyKey || binding.ExecutionIdempotencyKey != originalBinding.ExecutionIdempotencyKey ||
				binding.ResourceID != originalBinding.ResourceID || !bytes.Equal(binding.CanonicalPayload, originalBinding.CanonicalPayload) {
				t.Fatal("observation timeout or recovery changed the immutable original mailbox binding")
			}
			for _, requestID := range []string{originalID, laterID} {
				intent := p162IntentForRequest(t, h.authority, h.owner, requestID)
				if intent.DeliveryState != store.LocalIntentReconciled || intent.AttemptCount != 1 || !store.HasRemoteTerminalProof(intent) ||
					inner.mutationCount(string(intent.JobID)) != 1 {
					t.Fatalf("request %s was replayed or lacks strict terminal proof: %+v mutations=%d", requestID, intent, inner.mutationCount(string(intent.JobID)))
				}
				if p162Exchange(t, h.authority, requestID).AcknowledgedAt != nil {
					t.Fatalf("request %s acquired an unrequested ACK", requestID)
				}
			}
			if inner.totalMutations() != 2 {
				t.Fatalf("observer recovery sent %d mutations, want only the two original requests", inner.totalMutations())
			}
			if record, err := h.authority.GetLocalIntent(ctx, originalIntent.IntentID); err != nil || record.RemoteStatusFailureAt != nil {
				t.Fatalf("strict terminal recovery did not clear the temporary observation failure: intent=%+v err=%v", record, err)
			}
		})
	}
}

// The authority, mailbox, dispatcher and service scheduler remain real. This
// bridge wrapper models an observation that makes no progress until its caller
// is canceled; the sshclient tests separately exercise the actual pipe/child
// transport boundaries that produced this production failure.
type bug029ObserverCaller struct {
	inner    *p162RemoteCaller
	stage    string
	blocked  atomic.Bool
	timeouts atomic.Int64
}

func (c *bug029ObserverCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if c.stage == "status" && c.blocked.Load() && frame.Operation == sshbridge.OperationGetJob {
		var payload struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		if job := c.inner.jobs[payload.JobID]; job != nil && strings.Contains(job.script, "BUG029_STALLED_OBSERVER") {
			<-ctx.Done()
			c.timeouts.Add(1)
			return sshbridge.ReplyFrame{}, ctx.Err()
		}
	}
	return c.inner.Call(ctx, frame)
}

func (c *bug029ObserverCaller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	if c.stage == "events" && c.blocked.Load() {
		var payload struct {
			CommandID string `json:"command_id"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return err
		}
		if job := c.inner.jobForCommand(payload.CommandID); job != nil && strings.Contains(job.script, "BUG029_STALLED_OBSERVER") {
			// The target already supplied its complete terminal prefix and end
			// marker, but the transport never reaches EOF/exit. Those bytes alone
			// must not settle the accepted request before the call succeeds.
			if err := c.inner.Stream(ctx, frame, receive); err != nil {
				return err
			}
			<-ctx.Done()
			c.timeouts.Add(1)
			return ctx.Err()
		}
	}
	return c.inner.Stream(ctx, frame, receive)
}

func bug029RunBoundedCycle(t *testing.T, service *Service) {
	t.Helper()
	// This is only a last-resort test safety boundary. Production's shorter
	// observation boundary must return while this service context is still live.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	service.remoteReconcileMu.Lock()
	service.lastRemoteReconcile = time.Time{}
	service.remoteReconcileMu.Unlock()
	service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	if err := ctx.Err(); err != nil {
		t.Fatalf("mailbox cycle exhausted its service context instead of bounding the observation: %v", err)
	}
}

func bug029AssertTerminal(t *testing.T, client *mailboxclient.Client, response mailboxclient.Response) {
	t.Helper()
	if response.RequestState != string(store.MailboxExchangeComplete) || response.CommandState != string(domain.CommandStateSucceeded) ||
		response.ExitCode == nil || *response.ExitCode != 0 || response.AvailableEventSequence == nil || *response.AvailableEventSequence != 4 ||
		response.OutputComplete == nil || !*response.OutputComplete || response.OutputTruncated == nil || *response.OutputTruncated || response.TeardownOutcome != "closed" {
		t.Fatalf("terminal response=%+v, want original success with complete event prefix and closed teardown", response)
	}
	events, err := client.ReadEventsThroughCursor(response)
	if err != nil || len(events) != 4 || events[2].Text != "P162_COMPLETE_OK\\n" || events[3].Type != "command_succeeded" {
		t.Fatalf("retained terminal event prefix=%+v err=%v", events, err)
	}
}
