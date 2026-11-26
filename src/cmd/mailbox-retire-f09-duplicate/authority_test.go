package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

const originalExecutionKey = "original-execution-key"

type syntheticAuthority struct {
	exchange store.MailboxExchangeRecord
	intent   store.LocalIntentRecord
	command  store.CommandRecord
	events   []store.CommandEventRecord
	reads    int
}

func (a *syntheticAuthority) GetMailboxExchangeInMailbox(_ context.Context, ref store.MailboxExchangeRef) (store.MailboxExchangeRecord, error) {
	a.reads++
	if ref.MailboxID != mailboxID || ref.ClientRequestID != requestID {
		return store.MailboxExchangeRecord{}, errors.New("unknown exchange")
	}
	return a.exchange, nil
}

func (a *syntheticAuthority) GetLocalIntentByIdempotency(_ context.Context, operation, key string, owner domain.ControllerIdentity) (store.LocalIntentRecord, error) {
	a.reads++
	if operation != "run" || key != originalExecutionKey || owner != a.exchange.Controller {
		return store.LocalIntentRecord{}, errors.New("unknown intent")
	}
	return a.intent, nil
}

func (a *syntheticAuthority) GetCommandWithEvents(_ context.Context, id domain.CommandID) (store.CommandRecord, []store.CommandEventRecord, error) {
	a.reads++
	if id != commandID {
		return store.CommandRecord{}, nil, errors.New("unknown command")
	}
	return a.command, a.events, nil
}

func originalAuthority(t *testing.T) (*syntheticAuthority, string, string) {
	t.Helper()
	exchange := originalExchange(t)
	exchange.ExecutionIdempotencyKey = originalExecutionKey
	exit, cursor := 0, int64(7)
	a := &syntheticAuthority{
		exchange: exchange,
		intent: store.LocalIntentRecord{LocalIntentCreate: store.LocalIntentCreate{
			Operation: "run", IdempotencyKey: originalExecutionKey, ResourceID: jobID, JobID: jobID,
			CommandID: commandID, SessionID: sessionID, Environment: exchange.Selection.Environment,
			Target: exchange.Selection.Target, Controller: exchange.Controller, DeliveryState: store.LocalIntentAccepted,
		}},
		command: store.CommandRecord{
			CommandID: commandID, SessionID: sessionID, Ordinal: 1, State: domain.CommandStateSucceeded,
			ExitCode: &exit, FinalEventSequence: &cursor, OutputComplete: true,
		},
	}
	for i, kind := range []string{"command_queued", "command_running", "stdout", "stdout", "stdout", "stdout", "command_succeeded"} {
		seq := int64(i + 1)
		e := store.CommandEventRecord{CommandID: commandID, Sequence: seq, Type: kind, OccurredAt: time.Unix(seq, 0).UTC()}
		if kind == "stdout" {
			e.Payload = []byte("original output\n")
			e.ByteCount = int64(len(e.Payload))
		}
		a.events = append(a.events, e)
	}
	projected, err := mailbox.RenderCommandEvents(a.command, a.events)
	if err != nil {
		t.Fatal(err)
	}
	return a, digest(exchange.ResponseBytes), digest(projected)
}

func TestVerifyAuthority(t *testing.T) {
	for _, state := range []store.LocalIntentDeliveryState{store.LocalIntentAccepted, store.LocalIntentReconciled} {
		t.Run(string(state), func(t *testing.T) {
			a, response, events := originalAuthority(t)
			a.intent.DeliveryState = state
			if err := verifyAuthority(context.Background(), a, response, events); err != nil || a.reads != 3 {
				t.Fatalf("valid binding: reads=%d, error=%v", a.reads, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*syntheticAuthority)
		reads  int
	}{
		{"unacknowledged", func(a *syntheticAuthority) { a.exchange.AcknowledgedAt = nil }, 1},
		{"unbound execution key", func(a *syntheticAuthority) { a.exchange.ExecutionIdempotencyKey = "other-key" }, 2},
		{"different intent key", func(a *syntheticAuthority) { a.intent.IdempotencyKey = "other-key" }, 2},
		{"different resource", func(a *syntheticAuthority) { a.intent.ResourceID = "job-other" }, 2},
		{"different job", func(a *syntheticAuthority) { a.intent.JobID = "job-other" }, 2},
		{"different intent command", func(a *syntheticAuthority) { a.intent.CommandID = "cmd-other" }, 2},
		{"different intent session", func(a *syntheticAuthority) { a.intent.SessionID = "sess-other" }, 2},
		{"uncertain intent", func(a *syntheticAuthority) { a.intent.DeliveryState = store.LocalIntentUncertain }, 2},
		{"different command", func(a *syntheticAuthority) { a.command.CommandID = "cmd-other" }, 3},
		{"different command session", func(a *syntheticAuthority) { a.command.SessionID = "sess-other" }, 3},
		{"incomplete output", func(a *syntheticAuthority) { a.command.OutputComplete = false }, 3},
		{"absent final event", func(a *syntheticAuthority) { a.events = a.events[:6] }, 3},
		{"other final event", func(a *syntheticAuthority) { a.events[6].Type = "command_failed" }, 3},
		{"altered event output", func(a *syntheticAuthority) { a.events[2].Payload[0] = 'x' }, 3},
		{"noncontiguous events", func(a *syntheticAuthority) { a.events[2].Sequence++ }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, response, events := originalAuthority(t)
			tc.change(a)
			if err := verifyAuthority(context.Background(), a, response, events); err == nil || a.reads != tc.reads {
				t.Fatalf("reads=%d, want %d; error=%v", a.reads, tc.reads, err)
			}
		})
	}
	t.Run("fixture cannot authorize the production incident", func(t *testing.T) {
		a, _, _ := originalAuthority(t)
		if err := verifyAuthority(context.Background(), a, responseSHA256, eventsSHA256); err == nil || a.reads != 1 {
			t.Fatalf("reads=%d, error=%v", a.reads, err)
		}
	})
}
