package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP075ConfirmedRemoteGapSettlesPredecessorAndUnblocksNextOrdinal(t *testing.T) {
	authority := p068Authority(t)
	first := p068SubmitIntent(t, "intent-p075-gap-1", "session-p075-gap", "command-p075-gap-1", domain.TargetKindRemote, "echo one")
	second := p068SubmitIntent(t, "intent-p075-gap-2", "session-p075-gap", "command-p075-gap-2", domain.TargetKindRemote, "echo two")
	*second.IntentOrdinal = 2
	if _, err := authority.CreateLocalIntent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []store.RemoteEventRecord{{CommandID: first.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: when}}); err != nil {
		t.Fatal(err)
	}
	caller := &p075GapCaller{terminalState: domain.CommandStateSucceeded, finalSequence: 4, gapAvailable: 1, includeLast: true}
	driver, err := NewRemoteDriverWithClock(authority, caller, "router-p075", time.Minute, func() time.Time { return when })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), first.IntentID); err != nil {
		t.Fatal(err)
	}
	result, err := driver.MirrorCommandEvents(context.Background(), first.CommandID, first.Controller)
	if err != nil || result.Gap == nil || result.Gap.MissingFrom != 2 || result.Gap.MissingTo != 4 {
		t.Fatalf("confirmed gap = %+v, %v", result, err)
	}
	settled, err := authority.GetLocalIntent(context.Background(), first.IntentID)
	if err != nil || settled.DeliveryState != store.LocalIntentReconciled {
		t.Fatalf("gap predecessor = %+v, %v", settled, err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatalf("later ordinal remained blocked: %v", err)
	}
	secondState, err := authority.GetLocalIntent(context.Background(), second.IntentID)
	if err != nil || secondState.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("later ordinal state = %+v, %v", secondState, err)
	}
	if len(caller.calls) != 3 || caller.calls[0].Operation != sshbridge.OperationSubmitOrResumeCommand || caller.calls[1].Operation != sshbridge.OperationGetCommand || caller.calls[2].Operation != sshbridge.OperationSubmitOrResumeCommand {
		t.Fatalf("terminal confirmation/ordered calls = %+v", caller.calls)
	}
	if _, err := authority.GetRemoteEventCursor(context.Background(), first.CommandID); err != nil {
		t.Fatal(err)
	} else if cursor, _ := authority.GetRemoteEventCursor(context.Background(), first.CommandID); cursor != 1 {
		t.Fatalf("gap incorrectly advanced cursor to %d", cursor)
	}
}

func TestP075UnknownOrUnconfirmedGapKeepsLaterOrdinalBlocked(t *testing.T) {
	for _, test := range []struct {
		name          string
		terminalState domain.CommandState
		includeLast   bool
	}{
		{name: "unknown-gap", terminalState: domain.CommandStateSucceeded, includeLast: false},
		{name: "nonterminal-confirmation", terminalState: domain.CommandStateRunning, includeLast: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := p068Authority(t)
			first := p068SubmitIntent(t, "intent-p075-block-1-"+test.name, "session-p075-block-"+test.name, "command-p075-block-1-"+test.name, domain.TargetKindRemote, "echo one")
			second := p068SubmitIntent(t, "intent-p075-block-2-"+test.name, "session-p075-block-"+test.name, "command-p075-block-2-"+test.name, domain.TargetKindRemote, "echo two")
			*second.IntentOrdinal = 2
			if _, err := authority.CreateLocalIntent(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			if _, err := authority.CreateLocalIntent(context.Background(), second); err != nil {
				t.Fatal(err)
			}
			caller := &p075GapCaller{terminalState: test.terminalState, finalSequence: 3, gapAvailable: 0, includeLast: test.includeLast}
			driver, err := NewRemoteDriver(authority, caller, "router-p075", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := driver.DispatchIntent(context.Background(), first.IntentID); err != nil {
				t.Fatal(err)
			}
			if _, err := driver.MirrorCommandEvents(context.Background(), first.CommandID, first.Controller); err == nil {
				t.Fatal("unconfirmed gap unexpectedly succeeded")
			}
			current, err := authority.GetLocalIntent(context.Background(), first.IntentID)
			if err != nil || current.DeliveryState != store.LocalIntentAccepted {
				t.Fatalf("unconfirmed predecessor = %+v, %v", current, err)
			}
			if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoRemoteDispatchWork) {
				t.Fatalf("later ordinal dispatch = %v, want blocked", err)
			}
			if _, err := authority.GetRemoteEventGap(context.Background(), first.CommandID); !errors.Is(err, store.ErrRemoteGapNotFound) {
				t.Fatalf("unconfirmed gap record = %v", err)
			}
		})
	}
}

type p075GapCaller struct {
	mu            sync.Mutex
	calls         []sshbridge.RequestFrame
	terminalState domain.CommandState
	finalSequence int64
	gapAvailable  int64
	includeLast   bool
}

func (c *p075GapCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.mu.Lock()
	c.calls = append(c.calls, frame)
	c.mu.Unlock()
	if frame.Operation == sshbridge.OperationGetCommand {
		payload, _ := json.Marshal(map[string]any{
			"command_id": string(frame.ResourceID), "session_id": "session-p075-gap", "command_state": string(c.terminalState),
			"final_event_sequence": c.finalSequence, "output_complete": true, "output_truncated": false,
		})
		return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: payload}, nil
	}
	return p069AcceptedReply(frame), nil
}

func (c *p075GapCaller) Stream(_ context.Context, request sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	payload := map[string]any{"code": "event_history_unavailable", "message": "missing remote event range", "retryable": false, "resource_id": request.ResourceID}
	if c.includeLast {
		payload["details"] = map[string]any{"last_sequence": c.gapAvailable}
	}
	raw, _ := json.Marshal(payload)
	return receive(sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: request.RequestID, ResponseType: "error", Payload: raw})
}
