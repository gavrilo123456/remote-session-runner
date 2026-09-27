package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP104RemoteSessionReadinessProbeMatchesFrozenGetSessionFrame(t *testing.T) {
	create := store.LocalIntentRecord{LocalIntentCreate: store.LocalIntentCreate{
		IntentID:   domain.IntentID("intent-p104-readiness"),
		Operation:  operationCreateSession,
		ResourceID: "session-p104-readiness",
		SessionID:  domain.SessionID("session-p104-readiness"),
	}}
	frame, err := readinessFrameForCreateIntent(create)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Operation != sshbridge.OperationGetSession || frame.ResourceID != "" || frame.IdempotencyKey != "" {
		t.Fatalf("readiness frame=%+v, want get_session without mutation-only fields", frame)
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := sshbridge.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("readiness frame does not satisfy the frozen bridge schema: %v", err)
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil || payload.SessionID != string(create.SessionID) {
		t.Fatalf("readiness payload=%+v err=%v", payload, err)
	}
}

func TestP104RemoteProjectionValidatesQueuedPrincipalAndRetainsMailboxOwner(t *testing.T) {
	create := p076CreateIntent(t)
	localOwner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	create.Controller = localOwner
	intent := store.LocalIntentRecord{LocalIntentCreate: create}
	frame := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       "p104-controller-projection",
		Operation:       sshbridge.OperationCreateOrResumeSession,
		ResourceID:      string(create.SessionID),
		Payload:         json.RawMessage(`{}`),
	}
	reply, err := (p076ProjectionCaller{}).Call(context.Background(), frame)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(reply.Payload, &object); err != nil {
		t.Fatal(err)
	}
	projection, present, err := remoteSessionProjectionFromReply(intent, object, time.Now())
	if err != nil || !present {
		t.Fatalf("mailbox projection present=%v err=%v", present, err)
	}
	if projection.Controller != localOwner {
		t.Fatalf("remote projection controller=%+v, want local mailbox owner %+v", projection.Controller, localOwner)
	}

	object["controller"], _ = json.Marshal(map[string]string{
		"controller_type": string(domain.ControllerTypeQueuedMac),
		"controller_id":   "different-user",
	})
	if _, _, err := remoteSessionProjectionFromReply(intent, object, time.Now()); !errors.Is(err, ErrRemoteResponse) {
		t.Fatalf("projection with a different authenticated controller err=%v, want ErrRemoteResponse", err)
	}
}

func TestP104RefreshCommandProjectionReadsAuthoritativeTerminalState(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	intent := p068SubmitIntent(t, "intent-p104-refresh", "session-p104-refresh", "command-p104-refresh", domain.TargetKindRemote, "printf done")
	localOwner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	intent.Controller = localOwner
	if _, err := authority.CreateLocalIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentDispatching, "p104-test-dispatch"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "p104-test-accepted"); err != nil {
		t.Fatal(err)
	}
	caller := &p104RefreshCommandCaller{intent: intent}
	driver, err := NewRemoteDriver(authority, caller, "router-p104-refresh", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := driver.RefreshCommandProjection(ctx, intent.CommandID, localOwner)
	if err != nil {
		t.Fatal(err)
	}
	if projection.CommandID != intent.CommandID || projection.SessionID != intent.SessionID || projection.State != domain.CommandStateSucceeded || projection.FinalEventSequence == nil || *projection.FinalEventSequence != 4 || !projection.OutputComplete || projection.IsStale {
		t.Fatalf("refreshed command projection=%+v", projection)
	}
	reconciled, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || reconciled.DeliveryState != store.LocalIntentReconciled {
		t.Fatalf("terminal target read did not reconcile the intent: state=%s err=%v", reconciled.DeliveryState, err)
	}
	if len(caller.frames) != 1 || caller.frames[0].Operation != sshbridge.OperationGetCommand || caller.frames[0].ResourceID != "" || caller.frames[0].IdempotencyKey != "" {
		t.Fatalf("command state read frame=%+v, want frozen read fields only", caller.frames)
	}
	raw, err := json.Marshal(caller.frames[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sshbridge.DecodeRequest(raw); err != nil {
		t.Fatalf("command state read frame violates bridge protocol: %v", err)
	}
}

type p104RefreshCommandCaller struct {
	intent store.LocalIntentCreate
	frames []sshbridge.RequestFrame
}

func (c *p104RefreshCommandCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	if frame.Operation != sshbridge.OperationGetCommand {
		return sshbridge.ReplyFrame{}, fmt.Errorf("unexpected operation %s", frame.Operation)
	}
	payload, err := json.Marshal(map[string]any{
		"command_id": string(c.intent.CommandID), "session_id": string(c.intent.SessionID), "ordinal": 1,
		"command_state": "succeeded", "exit_code": 0, "final_event_sequence": 4,
		"output_complete": true, "output_truncated": false,
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"controller":       map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"environment":      "dev", "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{
			"host_class": "linux-host", "isolation": "os-user", "effective_account": "ubuntu",
			"service_limits": map[string]any{"running_commands_per_host": 4},
		},
		"observed_at": time.Now().UTC(),
	})
	if err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: payload}, nil
}
