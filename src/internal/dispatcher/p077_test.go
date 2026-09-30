package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP077RemoteDriverPersistsStrictJobProjection(t *testing.T) {
	authority := p068Authority(t)
	intent := p077RunIntent(t)
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p077JobCaller{}
	driver, err := NewRemoteDriver(authority, caller, "router-p077", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetRemoteJobProjection(context.Background(), intent.JobID); err == nil {
		t.Fatal("run mutation reply created a trusted job projection")
	}
	projection, err := driver.RefreshJobProjection(context.Background(), intent.JobID, intent.Controller)
	if err != nil || projection.Phase != store.JobPhaseAwaitingCommand || projection.CommandState == nil || *projection.CommandState != domain.CommandStateQueued || projection.TeardownState != store.JobTeardownPending || caller.getJobCalls != 1 {
		t.Fatalf("job projection = %+v, %v", projection, err)
	}
}

type p077JobCaller struct{ getJobCalls int }

func (c *p077JobCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if frame.Operation == sshbridge.OperationRunOrResumeJob {
		return p069AcceptedReply(frame), nil
	}
	if frame.Operation != sshbridge.OperationGetJob {
		return sshbridge.ReplyFrame{}, fmt.Errorf("unexpected P077 operation %s", frame.Operation)
	}
	c.getJobCalls++
	payload := map[string]any{
		"job_id": "job-p077-job", "session_id": "session-p077-job", "command_id": "command-p077-job", "job_phase": "awaiting_command",
		"command_state": "queued", "output_complete": false, "output_truncated": false,
		"teardown_state": "pending", "execution_target": map[string]any{"kind": "remote", "profile": "linux-host"}, "authority": "remote",
		"controller": map[string]any{"controller_type": "queued_mac", "controller_id": "tomasz.walczuk"}, "observed_at": "2026-09-27T14:10:00Z", "environment": "dev",
		"source": map[string]any{"mode": "empty"}, "capabilities": map[string]any{"host_class": "linux-host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands_per_host": 4}},
	}
	raw, _ := json.Marshal(payload)
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "result", Payload: raw}, nil
}

func p077RunIntent(t *testing.T) store.LocalIntentCreate {
	t.Helper()
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	payload := []byte(fmt.Sprintf(`{"environment":"dev","execution_target":{"kind":"remote","profile":"linux-host"},"job_id":"job-p077-job","session_id":"session-p077-job","command_id":"command-p077-job","script":"echo job","source":{"mode":"empty"}}`))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: "intent-p077-job", Operation: "run", ResourceID: "job-p077-job", SessionID: "session-p077-job", CommandID: "command-p077-job", JobID: "job-p077-job", Target: target, Environment: "dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-intent-p077-job", PayloadJSON: canonical, ScriptBytes: []byte("echo job")}
}
