package runnerlocal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

// This composition uses the real marker-last client, SQLite binding, service
// cycle and mailbox projector. Only the remote authority is synthetic. Its
// capacity is not represented in the local database: the test proves that
// delivery sends no close/cancel/recovery operation, not that cleanup occurred.
func TestBUG034ServiceDeliversOriginalRemoteResultsAcrossSlowHealthyReads(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "succeeded"
		if lost {
			name = "lost_with_unconfirmed_cleanup"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := t.TempDir()
			dbPath := filepath.Join(root, "state", "authority.db")
			now := time.Now().UTC().Truncate(time.Second)
			inner := newP162RemoteCaller(func() time.Time { return now })
			caller := &bug034MailboxCaller{inner: inner, lost: lost, delay: 70 * time.Millisecond}
			h := newP162Harness(t, root, dbPath, &now, inner)
			bug034InstallMailboxCaller(t, h, caller, &now)
			client := p162Client(t, h.mailboxRoot)
			const requestID = "req-bug034-original-result"
			p162WriteRunRequest(t, client, requestID, "key-bug034-original-result", "printf 'BUG034_SYNTHETIC_OUTPUT\\n'")
			marker, err := os.Stat(filepath.Join(h.mailboxRoot, "inbox", requestID+mailbox.ReadySuffix))
			if err != nil || marker.Size() != 0 {
				t.Fatalf("native marker-last input is missing or nonempty: marker=%v err=%v", marker, err)
			}

			// Establish a genuine accepted receipt before exercising live-cycle
			// result delivery. This does not resubmit the immutable RUN.
			h.service.runMailboxCycles(ctx, io.Discard)
			if _, _, err := h.service.remoteDriver.DispatchNext(ctx); err != nil {
				t.Fatal(err)
			}
			h.service.runMailboxCycles(ctx, io.Discard)
			accepted := p162ReadResponse(t, client, requestID)
			before := p162Exchange(t, h.authority, requestID)
			if accepted.RequestState != string(store.MailboxExchangeAccepted) || accepted.DeliveryState != string(store.LocalIntentAccepted) ||
				accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" || accepted.AvailableEventSequence != nil {
				t.Fatalf("missing original identity-only acceptance: %+v", accepted)
			}

			// Four individually healthy 70ms exchanges exceed the 110ms batch
			// window. They still fit their own 250ms read limits and must form a
			// strict terminal proof instead of stranding the accepted request.
			h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
			terminal := p162ReadResponse(t, client, requestID)
			wantState, wantPhase, wantTeardown := string(domain.CommandStateSucceeded), string(store.JobPhaseComplete), "closed"
			wantOutput, wantEvent := "BUG034_SUCCESS_OUTPUT\n", "command_succeeded"
			if lost {
				wantState, wantPhase, wantTeardown = string(domain.CommandStateLost), string(store.JobPhaseLost), "lost"
				wantOutput, wantEvent = "BUG034_PARTIAL_OUTPUT\n", "command_lost"
			}
			if terminal.RequestState != string(store.MailboxExchangeComplete) || terminal.CommandState != wantState ||
				terminal.JobPhase != wantPhase || terminal.TeardownOutcome != wantTeardown || terminal.Stdout != wantOutput ||
				terminal.JobID != accepted.JobID || terminal.SessionID != accepted.SessionID || terminal.CommandID != accepted.CommandID ||
				terminal.ResponseRevision <= accepted.ResponseRevision || terminal.AvailableEventSequence == nil || *terminal.AvailableEventSequence != 4 ||
				terminal.FinalEventSequence == nil || *terminal.FinalEventSequence != 4 || terminal.EventsFile == "" ||
				terminal.OutputComplete == nil || *terminal.OutputComplete == lost || terminal.OutputTruncated == nil || *terminal.OutputTruncated {
				t.Fatalf("original result was not delivered truthfully: %+v", terminal)
			}
			if lost {
				if terminal.ExitCode != nil || terminal.OutputUnavailableReason != "capture_boundary_unconfirmed" {
					t.Fatalf("LOST result invented an exit or complete capture: %+v", terminal)
				}
			} else if terminal.ExitCode == nil || *terminal.ExitCode != 0 || terminal.OutputUnavailableReason != "" {
				t.Fatalf("successful result lost its complete exit evidence: %+v", terminal)
			}
			events, err := client.ReadEventsThroughCursor(terminal)
			if err != nil || len(events) != 4 || events[2].Text != wantOutput || events[3].Type != wantEvent {
				t.Fatalf("advertised original event prefix was not delivered: events=%+v err=%v", events, err)
			}
			after := p162Exchange(t, h.authority, requestID)
			if after.ExchangeID != before.ExchangeID || after.RequestID != before.RequestID || after.MailboxID != before.MailboxID ||
				after.Controller != before.Controller || after.RequestHash != before.RequestHash || after.ResourceID != before.ResourceID ||
				after.IdempotencyKey != before.IdempotencyKey || after.ExecutionIdempotencyKey != before.ExecutionIdempotencyKey ||
				!bytes.Equal(after.CanonicalPayload, before.CanonicalPayload) || !reflect.DeepEqual(after.Selection, before.Selection) || after.AcknowledgedAt != nil {
				t.Fatal("result reconciliation changed the original binding or acknowledged the exchange")
			}
			for _, suffix := range []string{mailbox.RequestSuffix, mailbox.ReadySuffix} {
				if _, err := os.Stat(filepath.Join(h.mailboxRoot, "acks", requestID+suffix)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("result reconciliation created an ACK artifact: %v", err)
				}
			}
			if caller.mutations != 1 || inner.totalMutations() != 1 || caller.controlFrames != 0 || caller.completedReads < 4 {
				t.Fatalf("delivery replayed work or changed remote lifecycle: mutations=%d inner=%d controls=%d reads=%d", caller.mutations, inner.totalMutations(), caller.controlFrames, caller.completedReads)
			}

			// Reopening the real authority must retain the same terminal bytes
			// and unACKed exchange without another RUN or lifecycle mutation.
			frozen := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))
			h.close()
			h = newP162Harness(t, root, dbPath, &now, inner)
			bug034InstallMailboxCaller(t, h, caller, &now)
			h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
			if !bytes.Equal(frozen, p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", requestID+mailbox.RequestSuffix))) ||
				p162Exchange(t, h.authority, requestID).AcknowledgedAt != nil || caller.mutations != 1 || caller.controlFrames != 0 {
				t.Fatal("restart altered the terminal exchange, replayed work or sent a lifecycle control")
			}
		})
	}
}

func bug034InstallMailboxCaller(t *testing.T, h *p162Harness, caller *bug034MailboxCaller, now *time.Time) {
	t.Helper()
	resolver, err := dispatcher.NewRemoteCallerResolver(map[string]dispatcher.RemoteCaller{p162TargetProfile: caller})
	if err != nil {
		t.Fatal(err)
	}
	h.service.remoteDriver, err = dispatcher.NewRemoteDriverWithResolverAndClock(h.authority, resolver, "router-bug034-mailbox", time.Minute,
		func() time.Time { return now.UTC() }, dispatcher.RemoteObservationOptions{Timeout: 250 * time.Millisecond, BatchTimeout: 110 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
}

type bug034MailboxCaller struct {
	inner                                    *p162RemoteCaller
	lost                                     bool
	delay                                    time.Duration
	mutations, controlFrames, completedReads int
}

func (c *bug034MailboxCaller) pace(ctx context.Context) error {
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		c.completedReads++
		return nil
	}
}

func (c *bug034MailboxCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	switch frame.Operation {
	case sshbridge.OperationRunOrResumeJob:
		c.mutations++
		return c.inner.Call(ctx, frame)
	case sshbridge.OperationGetJob, sshbridge.OperationGetCommand:
		if frame.ResourceID != "" || frame.IdempotencyKey != "" {
			c.controlFrames++
			return sshbridge.ReplyFrame{}, errors.New("BUG034 read contained mutation fields")
		}
	default:
		c.controlFrames++
		return sshbridge.ReplyFrame{}, errors.New("BUG034 delivery attempted a lifecycle control")
	}
	if err := c.pace(ctx); err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	reply, err := c.inner.Call(ctx, frame)
	if err != nil || !c.lost {
		return reply, err
	}
	var payload map[string]any
	if err := json.Unmarshal(reply.Payload, &payload); err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	payload["command_state"] = string(domain.CommandStateLost)
	payload["output_complete"] = false
	payload["output_unavailable_reason"] = "capture_boundary_unconfirmed"
	delete(payload, "exit_code")
	if frame.Operation == sshbridge.OperationGetJob {
		payload["job_phase"] = string(store.JobPhaseLost)
		payload["teardown_state"] = string(store.JobTeardownLost)
		payload["teardown_reason"] = "runtime_cleanup_unconfirmed"
	}
	reply.Payload, err = json.Marshal(payload)
	return reply, err
}

func (c *bug034MailboxCaller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	if frame.Operation != sshbridge.OperationStreamCommandEvents || frame.ResourceID != "" || frame.IdempotencyKey != "" {
		c.controlFrames++
		return errors.New("BUG034 delivery attempted a lifecycle control stream")
	}
	if err := c.pace(ctx); err != nil {
		return err
	}
	return c.inner.Stream(ctx, frame, func(reply sshbridge.ReplyFrame) error {
		if reply.ResponseType != "event" {
			return receive(reply)
		}
		var payload map[string]any
		if err := json.Unmarshal(reply.Payload, &payload); err != nil {
			return err
		}
		if payload["type"] == "stdout" {
			output := []byte("BUG034_SUCCESS_OUTPUT\n")
			if c.lost {
				output = []byte("BUG034_PARTIAL_OUTPUT\n")
			}
			payload["data_base64"], payload["byte_count"] = base64.StdEncoding.EncodeToString(output), len(output)
		}
		if c.lost && payload["type"] == "command_succeeded" {
			payload["type"] = "command_lost"
		}
		var err error
		reply.Payload, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		return receive(reply)
	})
}
