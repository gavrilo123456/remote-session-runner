package dispatcher

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
)

func TestP124RefreshSessionProjectionReadsCurrentAuthorityState(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	create := p076CreateIntent(t)
	if _, err := authority.CreateLocalIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	caller := &p124SessionProjectionCaller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p124-session-refresh", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(ctx, create.IntentID); err != nil {
		t.Fatal(err)
	}
	initial, err := authority.GetRemoteSessionProjection(ctx, create.SessionID)
	if err != nil || initial.State != domain.SessionStateCreating {
		t.Fatalf("create response projection=%+v err=%v, want creating", initial, err)
	}

	refreshed, err := driver.RefreshSessionProjection(ctx, create.SessionID, create.Controller)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.SessionID != create.SessionID || refreshed.State != domain.SessionStateReady || refreshed.IsStale || refreshed.Capabilities.EffectiveAccount != "ubuntu" {
		t.Fatalf("refreshed session projection=%+v, want current ready authority state", refreshed)
	}
	if len(caller.frames) != 2 || caller.frames[1].Operation != sshbridge.OperationGetSession || caller.frames[1].ResourceID != "" || caller.frames[1].IdempotencyKey != "" {
		t.Fatalf("session refresh frames=%+v, want create followed by get_session", caller.frames)
	}
	raw, err := json.Marshal(caller.frames[1])
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := sshbridge.DecodeRequest(raw)
	if err != nil {
		t.Fatalf("session refresh frame violates bridge protocol: %v", err)
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil || payload.SessionID != string(create.SessionID) {
		t.Fatalf("session refresh payload=%+v err=%v", payload, err)
	}
}

type p124SessionProjectionCaller struct {
	frames []sshbridge.RequestFrame
}

func (c *p124SessionProjectionCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	sessionID := frame.ResourceID
	state := string(domain.SessionStateCreating)
	observedAt := "2026-09-27T13:10:00Z"
	if frame.Operation == sshbridge.OperationGetSession {
		var payload struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		sessionID = payload.SessionID
		state = string(domain.SessionStateReady)
		observedAt = "2026-09-27T13:11:00Z"
	}
	result, err := json.Marshal(map[string]any{
		"session_id": sessionID, "session_state": state,
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"controller":       map[string]string{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"},
		"observed_at":      observedAt, "environment": "dev", "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{
			"host_class": "linux-host", "isolation": "os-user", "effective_account": "ubuntu",
			"service_limits": map[string]any{"running_commands_per_host": 4},
		},
	})
	if err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	return sshbridge.ReplyFrame{
		ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID,
		ResponseType: "result", Payload: result,
	}, nil
}
