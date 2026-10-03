package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

// TestBUG013SchedulerWaitsForOneOffCoordinatorHandoff prevents a dispatcher
// from claiming a just-accepted one-off command before its coordinator has
// durably recorded awaiting_command. That checkpoint is the handoff between
// acceptance and the shared worker, and avoids an accepting_command snapshot
// being overwritten by a racing running claim.
func TestBUG013SchedulerWaitsForOneOffCoordinatorHandoff(t *testing.T) {
	authority := newP019Store(t, &p019Clock{value: time.Date(2026, 10, 3, 16, 0, 0, 0, time.UTC)})
	input := p024Acceptance(t, "job-bug013-handoff", "session-bug013-handoff", "command-bug013-handoff", "key-bug013-handoff", "printf handoff")
	job, duplicate, err := authority.AcceptJob(context.Background(), input)
	if err != nil || duplicate {
		t.Fatalf("accept one-off job=%+v duplicate=%v err=%v", job, duplicate, err)
	}
	if _, err := authority.CreateSession(context.Background(), SessionCreate{
		SessionID: job.SessionID, Target: job.Target, Environment: job.Environment,
		Controller: job.Controller, Source: job.Source, Limits: testSessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), job.SessionID, domain.SessionStateReady, "bug013-ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseCreatingSession, NextPhase: JobPhaseAcceptingCommand}); err != nil {
		t.Fatal(err)
	}
	command, duplicate, err := authority.AcceptCommand(context.Background(), CommandAcceptance{
		CommandID: job.CommandID, SessionID: job.SessionID, IdempotencyKey: "key-bug013-handoff-command",
		RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-bug013-handoff","script":"printf handoff"}`),
		Script:      string(job.ScriptBytes), Timeout: time.Minute,
	})
	if err != nil || duplicate || command.State != domain.CommandStateQueued {
		t.Fatalf("accept one-off command=%+v duplicate=%v err=%v", command, duplicate, err)
	}
	if _, err := authority.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit); !errors.Is(err, ErrCommandNotEligible) {
		t.Fatalf("claim before coordinator handoff error=%v, want ErrCommandNotEligible", err)
	}
	if current, err := authority.GetCommand(context.Background(), command.CommandID); err != nil || current.State != domain.CommandStateQueued {
		t.Fatalf("pre-handoff command=%+v err=%v, want queued", current, err)
	}

	if _, err := authority.CheckpointJob(context.Background(), job.JobID, JobCheckpoint{ExpectedPhase: JobPhaseAcceptingCommand, NextPhase: JobPhaseAwaitingCommand, Command: &command}); err != nil {
		t.Fatal(err)
	}
	claimed, err := authority.StartNextDispatchableCommand(context.Background(), DefaultRunningCommandLimit)
	if err != nil || claimed.CommandID != command.CommandID || claimed.State != domain.CommandStateRunning {
		t.Fatalf("claim after coordinator handoff=%+v err=%v", claimed, err)
	}
}
