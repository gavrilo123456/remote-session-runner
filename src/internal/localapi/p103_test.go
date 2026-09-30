package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

func TestP103F02BeforeSendIsKnownNonDeliveryInMailboxResponse(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	h := p103Harness(t, &now)
	accepted, intent := p103MailboxRun(t, h, "req-p103-before-send", "key-p103-before-send")
	transport := &p103RemoteTransport{mode: "before_send"}
	driver := p103Driver(t, h, transport, &now)
	_, _, err := driver.DispatchIntent(context.Background(), intent.IntentID)
	var transportErr *sshclient.TransportError
	if !errors.As(err, &transportErr) || transportErr.Phase != sshclient.PhaseBeforeSend {
		t.Fatalf("dispatch error=%v, want proven before-send transport failure", err)
	}
	if transport.mutationCommits != 0 {
		t.Fatalf("before-send failure committed %d remote jobs", transport.mutationCommits)
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := p101ReadResponse(t, h, accepted.RequestID)
	if response.RequestState != "rejected" || response.DeliveryState != string(store.LocalIntentNotDelivered) ||
		response.JobID != accepted.JobID || response.SessionID != accepted.SessionID || response.CommandID != accepted.CommandID ||
		response.JobPhase != "" || response.CommandState != "" || response.TeardownOutcome != "" || response.AvailableEventSequence != nil || response.Error == nil {
		t.Fatalf("proven non-delivery response invented a target outcome: %+v", response)
	}
}

func TestP103F02AfterCommitAndStreamLossReconcileStableRunThroughMailbox(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	h := p103Harness(t, &now)
	accepted, intent := p103MailboxRun(t, h, "req-p103-after-commit", "key-p103-after-commit")
	transport := &p103RemoteTransport{mode: "commit_then_drop", now: func() time.Time { return now }}
	driver := p103Driver(t, h, transport, &now)
	if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); err == nil {
		t.Fatal("post-commit lost reply unexpectedly returned success")
	}
	uncertain, err := h.authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil || uncertain.DeliveryState != store.LocalIntentUncertain || transport.mutationCommits != 1 {
		t.Fatalf("post-commit state=%+v commits=%d err=%v", uncertain, transport.mutationCommits, err)
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := p101ReadResponse(t, h, accepted.RequestID)
	if response.RequestState != "accepted" || response.DeliveryState != string(store.LocalIntentUncertain) || response.JobID != accepted.JobID || response.CommandState != "" || response.Error != nil {
		t.Fatalf("lost reply was presented as a terminal result: %+v", response)
	}

	reconciled, reply, err := driver.ReconcileIntent(context.Background(), intent.IntentID)
	if err != nil || reconciled.DeliveryState != store.LocalIntentAccepted || reply.ResponseType != "result" {
		t.Fatalf("post-commit reconciliation=%+v reply=%+v err=%v", reconciled, reply, err)
	}
	var reconciliationRequest struct {
		JobID string `json:"job_id"`
	}
	if len(transport.frames) != 2 || transport.frames[0].Operation != sshbridge.OperationRunOrResumeJob || transport.frames[1].Operation != sshbridge.OperationGetJob ||
		transport.frames[0].ResourceID != accepted.JobID || json.Unmarshal(transport.frames[1].Payload, &reconciliationRequest) != nil || reconciliationRequest.JobID != accepted.JobID ||
		transport.frames[0].IdempotencyKey != intent.IdempotencyKey || transport.mutationCommits != 1 {
		t.Fatalf("reconciliation changed mutation identity or resent it: frames=%+v commits=%d", transport.frames, transport.mutationCommits)
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	response = p101ReadResponse(t, h, accepted.RequestID)
	if response.RequestState != "accepted" || response.DeliveryState != string(store.LocalIntentAccepted) || response.JobID != accepted.JobID || response.CommandState != "" {
		t.Fatalf("confirmed target acceptance response=%+v", response)
	}

	// The first event stream delivers a durable prefix and then disconnects.
	controller := intent.Controller
	if _, err := driver.MirrorCommandEvents(context.Background(), intent.CommandID, controller); err == nil {
		t.Fatal("simulated first stream loss unexpectedly completed")
	}
	cursor, err := h.authority.GetRemoteEventCursor(context.Background(), intent.CommandID)
	if err != nil || cursor != 2 {
		t.Fatalf("durable stream prefix cursor=%d err=%v, want 2", cursor, err)
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	response = p101ReadResponse(t, h, accepted.RequestID)
	if response.RequestState != "accepted" || response.CommandState != "" || response.AvailableEventSequence != nil || response.OutputComplete != nil {
		t.Fatalf("stream loss was reported as a terminal/full command result: %+v", response)
	}

	// Reconnect resumes from sequence 2, after which the independently
	// confirmed terminal projection lets the file response publish all events.
	mirrored, err := driver.MirrorCommandEvents(context.Background(), intent.CommandID, controller)
	if err != nil || mirrored.LastSequence != 4 || len(transport.streamRequests) != 2 {
		t.Fatalf("resumed stream mirror=%+v requests=%d err=%v", mirrored, len(transport.streamRequests), err)
	}
	var resume struct {
		AfterSequence int64 `json:"after_sequence"`
	}
	if err := json.Unmarshal(transport.streamRequests[1].Payload, &resume); err != nil || resume.AfterSequence != 2 {
		t.Fatalf("resumed stream request=%s err=%v, want after_sequence 2", transport.streamRequests[1].Payload, err)
	}
	p101CompleteRemoteRun(t, h, intent, "p103 recovered\n")
	p101SetIntentDelivery(t, h, intent, store.LocalIntentReconciled)
	p101MarkRemoteTerminalProof(t, h, intent)
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminal := p101ReadResponse(t, h, accepted.RequestID)
	if terminal.RequestState != "complete" || terminal.DeliveryState != string(store.LocalIntentReconciled) || terminal.JobPhase != string(store.JobPhaseComplete) ||
		terminal.CommandState != string(domain.CommandStateSucceeded) || terminal.AvailableEventSequence == nil || *terminal.AvailableEventSequence != 4 ||
		terminal.FinalEventSequence == nil || *terminal.FinalEventSequence != 4 || terminal.OutputComplete == nil || !*terminal.OutputComplete || terminal.EventsFile != "events/"+string(intent.CommandID)+".ndjson" {
		t.Fatalf("reconciled mailbox run response=%+v", terminal)
	}
	eventBytes, eventCursor, err := h.eventFiles.Read(intent.CommandID)
	if err != nil || eventCursor != 4 || len(eventBytes) == 0 {
		t.Fatalf("reconciled event file cursor=%d bytes=%d err=%v", eventCursor, len(eventBytes), err)
	}

	// A new file exchange with the same key and payload returns the same result;
	// it cannot make the Router submit or execute the one-off job again.
	retryID := "req-p103-after-commit-retry"
	p101Import(t, h, retryID, p100RunRequest(retryID, "key-p103-after-commit", "remote", "linux-host", "linux-dev", "printf 'p103 recovered\\n'"))
	retry := p101ReadResponse(t, h, retryID)
	if retry.RequestState != "complete" || retry.JobID != terminal.JobID || retry.SessionID != terminal.SessionID || retry.CommandID != terminal.CommandID || retry.CommandState != terminal.CommandState || retry.AvailableEventSequence == nil || *retry.AvailableEventSequence != 4 {
		t.Fatalf("same-key mailbox retry=%+v, original=%+v", retry, terminal)
	}
	if transport.mutationCommits != 1 {
		t.Fatalf("reconciliation/retry caused %d remote job commits, want one", transport.mutationCommits)
	}
}

func TestP103F02UnresolvedRunPublishesImmutableIndeterminateAtDeadline(t *testing.T) {
	now := time.Now().UTC()
	h := p103Harness(t, &now)
	accepted, intent := p103MailboxRun(t, h, "req-p103-deadline", "key-p103-deadline")
	transport := &p103RemoteTransport{mode: "commit_then_drop", now: func() time.Time { return now }}
	driver := p103Driver(t, h, transport, &now)
	if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); err == nil {
		t.Fatal("post-commit lost reply unexpectedly returned success")
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	beforeDeadline := p101ReadResponse(t, h, accepted.RequestID)
	if beforeDeadline.RequestState != "accepted" || beforeDeadline.DeliveryState != string(store.LocalIntentUncertain) {
		t.Fatalf("pre-deadline mailbox response=%+v", beforeDeadline)
	}

	now = now.Add(dispatcher.RemoteUncertaintyWindow + time.Hour)
	if _, _, err := driver.ReconcileIntent(context.Background(), intent.IntentID); !errors.Is(err, dispatcher.ErrRemoteUncertaintyDeadline) {
		t.Fatalf("post-deadline Router result=%v, want ErrRemoteUncertaintyDeadline", err)
	}
	if len(transport.frames) != 1 || transport.mutationCommits != 1 {
		t.Fatalf("deadline retried a mutation: frames=%+v commits=%d", transport.frames, transport.mutationCommits)
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminal := p101ReadResponse(t, h, accepted.RequestID)
	if terminal.RequestState != "indeterminate" || terminal.DeliveryState != string(store.LocalIntentUncertain) || terminal.JobID != accepted.JobID ||
		terminal.SessionID != accepted.SessionID || terminal.CommandID != accepted.CommandID || terminal.JobPhase != "" || terminal.CommandState != "" ||
		terminal.TeardownOutcome != "" || terminal.AvailableEventSequence != nil || terminal.Error != nil || terminal.ResponseRevision <= beforeDeadline.ResponseRevision {
		t.Fatalf("deadline guessed a target outcome instead of publishing indeterminate: %+v", terminal)
	}
	firstBytes, err := h.outbox.Read(accepted.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondBytes, err := h.outbox.Read(accepted.RequestID)
	if err != nil || string(firstBytes) != string(secondBytes) {
		t.Fatalf("indeterminate terminal response changed: err=%v first=%s second=%s", err, firstBytes, secondBytes)
	}
}

func p103Harness(t *testing.T, now *time.Time) *p095Harness {
	t.Helper()
	h := newP095Harness(t)
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: h.importer, Authority: h.authority, Controller: p063Owner(t), Operations: h.server,
		Outbox: h.outbox, EventFiles: h.eventFiles, ExecutionResolver: testMailboxExecutionResolver{}, Now: func() time.Time { return *now },
		RemoteUncertaintyWindow: dispatcher.RemoteUncertaintyWindow,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.processor = processor
	return h
}

func p103MailboxRun(t *testing.T, h *p095Harness, requestID, key string) (p101Response, store.LocalIntentRecord) {
	t.Helper()
	p101Import(t, h, requestID, p100RunRequest(requestID, key, "remote", "linux-host", "linux-dev", "printf 'p103 recovered\\n'"))
	response := p101ReadResponse(t, h, requestID)
	if response.RequestState != "accepted" || response.JobID == "" || response.SessionID == "" || response.CommandID == "" {
		t.Fatalf("initial mailbox run receipt=%+v", response)
	}
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "run", response.JobID, p063Owner(t))
	if err != nil || intent.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("mailbox run intent=%+v err=%v", intent, err)
	}
	return response, intent
}

func p103Driver(t *testing.T, h *p095Harness, transport *p103RemoteTransport, now *time.Time) *dispatcher.RemoteDriver {
	t.Helper()
	driver, err := dispatcher.NewRemoteDriverWithClock(h.authority, transport, "router-p103", time.Minute, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

type p103RemoteTransport struct {
	mode            string
	now             func() time.Time
	frames          []sshbridge.RequestFrame
	streamRequests  []sshbridge.RequestFrame
	projection      json.RawMessage
	mutationCommits int
	streamCalls     int
}

func (c *p103RemoteTransport) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	switch frame.Operation {
	case sshbridge.OperationRunOrResumeJob:
		if c.mode == "before_send" {
			return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseBeforeSend, Err: errors.New("simulated connect failure")}
		}
		c.mutationCommits++
		projection, err := p103RunProjection(frame)
		if err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		c.projection = projection
		if c.mode == "commit_then_drop" {
			return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("simulated reply loss after remote commit")}
		}
		return p103Reply(frame.RequestID, "result", projection), nil
	case sshbridge.OperationGetJob:
		if c.mutationCommits != 1 || len(c.projection) == 0 {
			return sshbridge.ReplyFrame{}, errors.New("fake remote job was not committed")
		}
		return p103Reply(frame.RequestID, "result", c.projection), nil
	default:
		return sshbridge.ReplyFrame{}, errors.New("unexpected fake remote operation: " + string(frame.Operation))
	}
}

func (c *p103RemoteTransport) Stream(_ context.Context, request sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	c.streamRequests = append(c.streamRequests, request)
	c.streamCalls++
	if request.ResourceID != "" || request.IdempotencyKey != "" {
		return errors.New("read-only event stream carried mutation identity fields")
	}
	var payload struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(request.Payload, &payload); err != nil || payload.CommandID == "" {
		return errors.New("read-only event stream omitted command ID from payload")
	}
	commandID := payload.CommandID
	when := time.Date(2026, 9, 27, 12, 3, 0, 0, time.UTC)
	if c.streamCalls == 1 {
		for _, reply := range []sshbridge.ReplyFrame{
			p103EventReply(request.RequestID, commandID, 1, "command_queued", when, nil),
			p103EventReply(request.RequestID, commandID, 2, "command_started", when.Add(time.Second), nil),
		} {
			if err := receive(reply); err != nil {
				return err
			}
		}
		return errors.New("simulated event stream loss")
	}
	if c.streamCalls == 2 {
		for _, reply := range []sshbridge.ReplyFrame{
			p103EventReply(request.RequestID, commandID, 3, "stdout", when.Add(2*time.Second), []byte("p103 recovered\n")),
			p103EventReply(request.RequestID, commandID, 4, "command_succeeded", when.Add(3*time.Second), nil),
			p103Reply(request.RequestID, "stream_end", mustP103JSON(map[string]int64{"last_sequence": 4})),
		} {
			if err := receive(reply); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unexpected additional fake stream")
}

func p103RunProjection(frame sshbridge.RequestFrame) (json.RawMessage, error) {
	var request struct {
		SessionID       string `json:"session_id"`
		CommandID       string `json:"command_id"`
		Environment     string `json:"environment"`
		ExecutionTarget struct {
			Kind    string `json:"kind"`
			Profile string `json:"profile"`
		} `json:"execution_target"`
	}
	if err := json.Unmarshal(frame.Payload, &request); err != nil {
		return nil, err
	}
	projection := map[string]any{
		"job_id": frame.ResourceID, "session_id": request.SessionID, "command_id": request.CommandID,
		"job_phase": string(store.JobPhaseAwaitingCommand), "teardown_state": string(store.JobTeardownPending),
		"environment": request.Environment, "execution_target": request.ExecutionTarget,
		"capabilities": p076APICapabilities(), "observed_at": time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(projection)
	return raw, err
}

func p103Reply(requestID, responseType string, payload json.RawMessage) sshbridge.ReplyFrame {
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, ResponseType: responseType, Payload: payload}
}

func p103EventReply(requestID, commandID string, sequence int64, eventType string, occurredAt time.Time, output []byte) sshbridge.ReplyFrame {
	payload := map[string]any{
		"command_id": commandID, "sequence": sequence, "type": eventType, "timestamp": occurredAt,
	}
	if len(output) > 0 {
		payload["encoding"] = "base64"
		payload["data_base64"] = base64.StdEncoding.EncodeToString(output)
		payload["byte_count"] = len(output)
	}
	return p103Reply(requestID, "event", mustP103JSON(payload))
}

func mustP103JSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
