package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

type p069Caller struct {
	mu       sync.Mutex
	frames   []sshbridge.RequestFrame
	err      error
	response sshbridge.ReplyFrame
}

func (c *p069Caller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.mu.Lock()
	c.frames = append(c.frames, frame)
	c.mu.Unlock()
	if c.err != nil {
		return sshbridge.ReplyFrame{}, c.err
	}
	if c.response.ResponseType != "" {
		return c.response, nil
	}
	return p069AcceptedReply(frame), nil
}

func TestP069RemoteDriverUsesStableBridgeIdentityAndTargetOrdinal(t *testing.T) {
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p069-submit", "session-p069-submit", "command-p069-submit", domain.TargetKindRemote, "printf 'remote bytes\\n'")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p069Caller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p069", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record, reply, err := driver.DispatchNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if record.DeliveryState != store.LocalIntentAccepted || reply.ResponseType != "result" {
		t.Fatalf("dispatch = %+v/%+v", record, reply)
	}
	if len(caller.frames) != 1 {
		t.Fatalf("frames = %+v", caller.frames)
	}
	frame := caller.frames[0]
	if frame.ProtocolVersion != sshbridge.ProtocolVersion || frame.RequestID != string(intent.IntentID) || frame.Operation != sshbridge.OperationSubmitOrResumeCommand || frame.ResourceID != string(intent.CommandID) || frame.IdempotencyKey != intent.IdempotencyKey {
		t.Fatalf("stable frame identity = %+v", frame)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var script string
	var sessionID string
	var ordinal int64
	if err := json.Unmarshal(payload["script"], &script); err != nil || script != string(intent.ScriptBytes) || json.Unmarshal(payload["session_id"], &sessionID) != nil || sessionID != string(intent.SessionID) || json.Unmarshal(payload["intent_ordinal"], &ordinal) != nil || ordinal != *intent.IntentOrdinal {
		t.Fatalf("remote payload = %s", frame.Payload)
	}
}

func TestP069RemoteDriverBlocksLaterSameSessionOrdinalUntilReconciled(t *testing.T) {
	authority := p068Authority(t)
	first := p068SubmitIntent(t, "intent-p069-order-1", "session-p069-order", "command-p069-order-1", domain.TargetKindRemote, "echo one")
	second := p068SubmitIntent(t, "intent-p069-order-2", "session-p069-order", "command-p069-order-2", domain.TargetKindRemote, "echo two")
	*second.IntentOrdinal = 2
	if _, err := authority.CreateLocalIntent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	caller := &p069Caller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p069", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, ErrNoRemoteDispatchWork) {
		t.Fatalf("later ordinal dispatch = %v, want blocked", err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), first.IntentID, store.LocalIntentReconciled, "terminal_output_reconciled"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(caller.frames) != 2 || caller.frames[1].ResourceID != string(second.CommandID) {
		t.Fatalf("ordered frames = %+v", caller.frames)
	}
}

func TestP069RemoteDriverClassifiesBeforeAndAfterSendFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase sshclient.TransportPhase
		state store.LocalIntentDeliveryState
	}{
		{name: "before_send", phase: sshclient.PhaseBeforeSend, state: store.LocalIntentNotDelivered},
		{name: "after_send", phase: sshclient.PhaseAfterSend, state: store.LocalIntentUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := p068Authority(t)
			intent := p068SubmitIntent(t, "intent-p069-"+test.name, "session-p069-"+test.name, "command-p069-"+test.name, domain.TargetKindRemote, "echo failure")
			if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
				t.Fatal(err)
			}
			caller := &p069Caller{err: &sshclient.TransportError{Phase: test.phase, Err: errors.New("fault")}}
			driver, err := NewRemoteDriver(authority, caller, "router-p069", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); err == nil {
				t.Fatal("failure was swallowed")
			}
			current, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
			if err != nil {
				t.Fatal(err)
			}
			if current.DeliveryState != test.state {
				t.Fatalf("state = %s, want %s", current.DeliveryState, test.state)
			}
		})
	}
}

func TestP069RemoteDriverBuildsStableRunFrame(t *testing.T) {
	authority := p068Authority(t)
	intent := p068RunIntent(t, "intent-p069-run", "job-p069-run", domain.TargetKindRemote)
	intent.Target, _ = domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	intent.SessionID = "session-p069-run"
	intent.CommandID = "command-p069-run"
	// Rebuild the run payload with the exact child identities used by the frame.
	payload := fmt.Sprintf(`{"environment":"dev","execution_target":{"kind":"remote","profile":"linux-host"},"job_id":%q,"session_id":%q,"command_id":%q,"operation":"run","script":"echo run"}`, intent.JobID, intent.SessionID, intent.CommandID)
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", []byte(payload), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadJSON = canonical
	intent.RequestHash, err = domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p069Caller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p069", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(caller.frames) != 1 || caller.frames[0].Operation != sshbridge.OperationRunOrResumeJob || caller.frames[0].ResourceID != string(intent.JobID) {
		t.Fatalf("run frame = %+v", caller.frames)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(caller.frames[0].Payload, &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "command_id", "script"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("run frame missing %s: %s", key, caller.frames[0].Payload)
		}
	}
}

func p069AcceptedReply(frame sshbridge.RequestFrame) sshbridge.ReplyFrame {
	field := "session_id"
	if frame.Operation == sshbridge.OperationSubmitOrResumeCommand || frame.Operation == sshbridge.OperationCancelCommand {
		field = "command_id"
	}
	if frame.Operation == sshbridge.OperationRunOrResumeJob {
		field = "job_id"
	}
	value := frame.ResourceID
	payload := map[string]any{field: value}
	if frame.Operation == sshbridge.OperationSubmitOrResumeCommand {
		var input struct {
			IntentOrdinal int64 `json:"intent_ordinal"`
		}
		_ = json.Unmarshal(frame.Payload, &input)
		payload["ordinal"] = input.IntentOrdinal
	}
	body, _ := json.Marshal(payload)
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: body}
}
