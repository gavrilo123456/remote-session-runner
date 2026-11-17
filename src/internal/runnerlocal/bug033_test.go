package runnerlocal

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

// A RUN can reach the worker while its acceptance reply is lost. The ordinary
// service cycle must subsequently observe that same job, including after a Mac
// restart, without sending the mutation a second time. A missing worker record
// must remain uncertain until truthful expiry; it cannot authorize a replay.
func TestBUG033InitialUncertaintyIsObservedWithoutReplay(t *testing.T) {
	for _, delivered := range []bool{true, false} {
		name := "target_found_after_restart"
		if !delivered {
			name = "target_absent_stays_uncertain_then_expires"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			dbPath := filepath.Join(root, "state", "authority.db")
			now := time.Now().UTC().Truncate(time.Second)
			inner := newP162RemoteCaller(func() time.Time { return now })
			caller := &bug033UncertainCaller{inner: inner, delivered: delivered}
			h := newP162Harness(t, root, dbPath, &now, inner)
			installBUG033Caller(t, h, caller, &now)
			client := p162Client(t, h.mailboxRoot)
			const requestID = "req-bug033-original-smoke"
			p162WriteRunRequest(t, client, requestID, "key-bug033-original-smoke", "printf 'P162_COMPLETE_OK\\n'")
			h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
			initial := p162ReadResponse(t, client, requestID)
			intent := p162IntentForRequest(t, h.authority, h.owner, requestID)
			if initial.RequestState != string(store.MailboxExchangeAccepted) || intent.DeliveryState != store.LocalIntentUncertain || caller.mutations != 1 {
				t.Fatalf("initial response=%+v intent=%+v mutations=%d", initial, intent, caller.mutations)
			}
			if intent.LeaseExpiresAt == nil {
				t.Fatal("uncertain dispatch has no lease boundary")
			}
			h.close()
			now = intent.LeaseExpiresAt.Add(time.Second)
			h = newP162Harness(t, root, dbPath, &now, inner)
			installBUG033Caller(t, h, caller, &now)
			client = p162Client(t, h.mailboxRoot)
			h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
			result := p162ReadResponse(t, client, requestID)
			if caller.mutations != 1 || caller.reads == 0 || result.JobID != initial.JobID || result.SessionID != initial.SessionID || result.CommandID != initial.CommandID {
				t.Fatalf("identity/replay: result=%+v mutations=%d reads=%d", result, caller.mutations, caller.reads)
			}
			if delivered {
				bug029AssertTerminal(t, client, result)
				if result.ResponseRevision <= initial.ResponseRevision {
					t.Fatal("recovered result did not advance original response revision")
				}
			} else {
				if result.RequestState != string(store.MailboxExchangeAccepted) || result.AvailableEventSequence != nil || result.ExitCode != nil || result.JobPhase != "" || result.TeardownOutcome != "" {
					t.Fatalf("target absence fabricated result: %+v", result)
				}
				now = intent.UpdatedAt.Add(dispatcher.RemoteUncertaintyWindow + time.Second)
				h.service.lastRemoteReconcile = time.Time{}
				h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
				result = p162ReadResponse(t, client, requestID)
				if result.RequestState != string(store.MailboxExchangeIndeterminate) || result.AvailableEventSequence != nil || result.ExitCode != nil || result.JobPhase != "" || result.TeardownOutcome != "" || caller.mutations != 1 {
					t.Fatalf("expiry invented execution or replayed work: %+v mutations=%d", result, caller.mutations)
				}
			}
			exchange := p162Exchange(t, h.authority, requestID)
			if exchange.AcknowledgedAt != nil {
				t.Fatal("reconciliation acknowledged the exchange")
			}
			for _, suffix := range []string{mailbox.RequestSuffix, mailbox.ReadySuffix} {
				if _, err := os.Stat(filepath.Join(h.mailboxRoot, "acks", requestID+suffix)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("reconciliation wrote ACK artifact: %v", err)
				}
			}
		})
	}
}

func installBUG033Caller(t *testing.T, h *p162Harness, caller *bug033UncertainCaller, now *time.Time) {
	t.Helper()
	resolver, err := dispatcher.NewRemoteCallerResolver(map[string]dispatcher.RemoteCaller{p162TargetProfile: caller})
	if err != nil {
		t.Fatal(err)
	}
	h.service.remoteDriver, err = dispatcher.NewRemoteDriverWithResolverAndClock(h.authority, resolver, "router-bug033", time.Minute, func() time.Time { return now.UTC() })
	if err != nil {
		t.Fatal(err)
	}
}

type bug033UncertainCaller struct {
	inner     *p162RemoteCaller
	delivered bool
	mutations int
	reads     int
}

func (c *bug033UncertainCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if frame.Operation == sshbridge.OperationRunOrResumeJob {
		c.mutations++
		if c.delivered {
			if _, err := c.inner.Call(ctx, frame); err != nil {
				return sshbridge.ReplyFrame{}, err
			}
		}
		return sshbridge.ReplyFrame{}, errors.New("BUG033 acceptance reply was lost")
	}
	if frame.Operation == sshbridge.OperationGetJob {
		c.reads++
		if !c.delivered {
			return p162Reply(frame.RequestID, "error", map[string]any{"code": "resource_not_found", "message": "job not found", "retryable": false}), nil
		}
	}
	return c.inner.Call(ctx, frame)
}

func (c *bug033UncertainCaller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	return c.inner.Stream(ctx, frame, receive)
}
