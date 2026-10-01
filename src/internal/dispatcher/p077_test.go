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
	for _, state := range []domain.CommandState{
		domain.CommandStateQueued,
		domain.CommandStateRunning,
		domain.CommandStateCancelling,
	} {
		t.Run(string(state), func(t *testing.T) {
			authority := p068Authority(t)
			intent := p077RunIntent(t)
			if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
				t.Fatal(err)
			}
			caller := &p077JobCaller{commandState: state}
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
			if err != nil || projection.JobID != intent.JobID || projection.SessionID != intent.SessionID || projection.CommandID != intent.CommandID ||
				projection.Phase != store.JobPhaseAwaitingCommand || projection.CommandState == nil || *projection.CommandState != state ||
				projection.TeardownState != store.JobTeardownPending || projection.ExitCode != nil || projection.FinalEventSequence != nil ||
				projection.OutputComplete || projection.OutputTruncated || projection.OutputUnavailableReason != "" || projection.IsStale ||
				caller.getJobCalls != 1 || caller.runCalls != 1 {
				t.Fatalf("job projection = %+v, err=%v get_job=%d run=%d", projection, err, caller.getJobCalls, caller.runCalls)
			}
			stored, err := authority.GetRemoteJobProjection(context.Background(), intent.JobID)
			if err != nil || stored.Phase != projection.Phase || stored.CommandState == nil || *stored.CommandState != state || stored.IsStale {
				t.Fatalf("durable job projection = %+v, err=%v", stored, err)
			}
			marked, err := authority.MarkAcceptedRemoteRunProjectionsStale(context.Background())
			if err != nil || marked != 1 {
				t.Fatalf("mark accepted remote projection stale=%d err=%v", marked, err)
			}
			stale, err := authority.GetRemoteJobProjection(context.Background(), intent.JobID)
			if err != nil || !stale.IsStale {
				t.Fatalf("marked remote job projection=%+v err=%v", stale, err)
			}
			refreshed, err := driver.RefreshJobProjection(context.Background(), intent.JobID, intent.Controller)
			if err != nil || refreshed.IsStale || refreshed.CommandState == nil || *refreshed.CommandState != state ||
				caller.getJobCalls != 2 || caller.runCalls != 1 {
				t.Fatalf("fresh strict read=%+v err=%v get_job=%d run=%d", refreshed, err, caller.getJobCalls, caller.runCalls)
			}
		})
	}
}

type p077JobCaller struct {
	commandState domain.CommandState
	getJobCalls  int
	runCalls     int
}

func (c *p077JobCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if frame.Operation == sshbridge.OperationRunOrResumeJob {
		c.runCalls++
		return p069AcceptedReply(frame), nil
	}
	if frame.Operation != sshbridge.OperationGetJob {
		return sshbridge.ReplyFrame{}, fmt.Errorf("unexpected P077 operation %s", frame.Operation)
	}
	c.getJobCalls++
	payload := map[string]any{
		"job_id": "job-p077-job", "session_id": "session-p077-job", "command_id": "command-p077-job", "job_phase": "awaiting_command",
		"command_state": string(c.commandState), "output_complete": false, "output_truncated": false,
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
