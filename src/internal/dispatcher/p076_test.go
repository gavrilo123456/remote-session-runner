package dispatcher

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP076RemoteDriverPersistsCompleteSessionAndCommandProjections(t *testing.T) {
	authority := p068Authority(t)
	create := p076CreateIntent(t)
	if _, err := authority.CreateLocalIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	command := p068SubmitIntent(t, "intent-p076-command", string(create.SessionID), "command-p076", domain.TargetKindRemote, "echo projection")
	if _, err := authority.CreateLocalIntent(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	caller := &p076ProjectionCaller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p076", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), create.IntentID); err != nil {
		t.Fatal(err)
	}
	session, err := authority.GetRemoteSessionProjection(context.Background(), create.SessionID)
	if err != nil || session.State != domain.SessionStateReady || session.Capabilities.EffectiveAccount != "ubuntu" {
		t.Fatalf("session projection = %+v, %v", session, err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), command.IntentID); err != nil {
		t.Fatal(err)
	}
	projection, err := authority.GetRemoteCommandProjection(context.Background(), command.CommandID)
	if err != nil || projection.State != domain.CommandStateQueued || projection.Ordinal != 1 || projection.SessionID != create.SessionID {
		t.Fatalf("command projection = %+v, %v", projection, err)
	}
}

type p076ProjectionCaller struct{}

func (p076ProjectionCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	now := "2026-09-27T13:10:00Z"
	base := map[string]any{
		"execution_target": map[string]any{"kind": "remote", "profile": "linux-host"},
		"authority":        "remote", "controller": map[string]any{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"observed_at": now, "environment": "dev", "source": map[string]any{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "linux-host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands_per_host": 4}},
	}
	switch frame.Operation {
	case sshbridge.OperationCreateOrResumeSession, sshbridge.OperationGetSession:
		base["session_id"] = frame.ResourceID
		base["session_state"] = "ready"
	case sshbridge.OperationSubmitOrResumeCommand:
		var payload struct {
			SessionID     string `json:"session_id"`
			IntentOrdinal int64  `json:"intent_ordinal"`
		}
		_ = json.Unmarshal(frame.Payload, &payload)
		base["command_id"] = frame.ResourceID
		base["session_id"] = payload.SessionID
		base["ordinal"] = payload.IntentOrdinal
		base["command_state"] = "queued"
		base["output_complete"] = false
		base["output_truncated"] = false
	default:
		base["session_id"] = frame.ResourceID
	}
	raw, _ := json.Marshal(base)
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: raw}, nil
}

func p076CreateIntent(t *testing.T) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"environment":"dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"}}`)
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: "intent-p076-create", Operation: "create_session", ResourceID: "session-p076", SessionID: "session-p076", Target: target, Environment: "dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-intent-p076-create", PayloadJSON: canonical}
}
