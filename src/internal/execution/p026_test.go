package execution

import (
	"context"
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP026I04OneOffSuccessClosesSessionAndCompletesJob(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p026-success",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{Stdout: []byte("done\n"), ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p026-success", "session-p026-success", "command-p026-success", "run-p026-success", "echo done")
	result, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Phase != store.JobPhaseComplete || result.Job.TeardownState != store.JobTeardownClosed || result.Session.State != domain.SessionStateClosed || result.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("completed job = %+v", result)
	}
	if result.Job.CommandState == nil || *result.Job.CommandState != domain.CommandStateSucceeded || result.Job.ExitCode == nil || *result.Job.ExitCode != 0 || !result.Job.OutputComplete {
		t.Fatalf("job command result was not preserved = %+v", result.Job)
	}
	if runtime.commandCall != 1 || runtime.stopCall != 1 || len(runtime.scripts) != 1 {
		t.Fatalf("runtime calls command=%d stop=%d scripts=%q", runtime.commandCall, runtime.stopCall, runtime.scripts)
	}
	second, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Job.Phase != store.JobPhaseComplete || second.Session.SessionID != result.Session.SessionID || second.Command.CommandID != result.Command.CommandID || runtime.commandCall != 1 || runtime.stopCall != 1 {
		t.Fatalf("completed retry changed outcome or reran work: %+v command=%d stop=%d", second, runtime.commandCall, runtime.stopCall)
	}
	if got, err := authority.GetJob(context.Background(), request.Acceptance.JobID); err != nil || got.TeardownState != store.JobTeardownClosed {
		t.Fatalf("durable completed job = %+v err=%v", got, err)
	}
}

func TestP026I04NonzeroCommandOutcomeSurvivesConfirmedTeardown(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p026-command-failed",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{Stderr: []byte("failed\n"), ExitCode: 17},
	}}
	service, _, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p026-command-failed", "session-p026-command-failed", "command-p026-command-failed", "run-p026-command-failed", "exit 17")
	result, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Phase != store.JobPhaseComplete || result.Job.TeardownState != store.JobTeardownClosed || result.Command.State != domain.CommandStateFailed || result.Command.ExitCode == nil || *result.Command.ExitCode != 17 {
		t.Fatalf("failed command job = %+v", result)
	}
	if result.Job.CommandState == nil || *result.Job.CommandState != domain.CommandStateFailed || result.Job.ExitCode == nil || *result.Job.ExitCode != 17 {
		t.Fatalf("failed command result not retained in job = %+v", result.Job)
	}
}

func TestP026I04TeardownFailurePreservesCommandOutcomeAndStableRetry(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p026-teardown-lost",
		stopConfirmed: false,
		commandResult: RuntimeCommandResult{Stdout: []byte("ran once\n"), ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p026-teardown-lost", "session-p026-teardown-lost", "command-p026-teardown-lost", "run-p026-teardown-lost", "echo ran once")
	result, err := service.RunJob(context.Background(), request)
	if !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("teardown error = %v, want ErrStopUnconfirmed", err)
	}
	if result.Job.Phase != store.JobPhaseLost || result.Job.TeardownState != store.JobTeardownLost || result.Session.State != domain.SessionStateLost || result.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("teardown-loss result = %+v", result)
	}
	if result.Job.CommandState == nil || *result.Job.CommandState != domain.CommandStateSucceeded || result.Job.ExitCode == nil || *result.Job.ExitCode != 0 || !result.Job.OutputComplete {
		t.Fatalf("command outcome lost with teardown failure = %+v", result.Job)
	}
	second, retryErr := service.RunJob(context.Background(), request)
	if retryErr != nil {
		t.Fatalf("lost job retry error = %v", retryErr)
	}
	if second.Job.Phase != store.JobPhaseLost || second.Job.TeardownState != store.JobTeardownLost || second.Command.CommandID != request.Acceptance.CommandID || runtime.commandCall != 1 || runtime.stopCall != 1 {
		t.Fatalf("lost retry changed stable result or reran work: %+v command=%d stop=%d", second, runtime.commandCall, runtime.stopCall)
	}
	if got, err := authority.GetJob(context.Background(), request.Acceptance.JobID); err != nil || got.CommandState == nil || *got.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("durable lost job = %+v err=%v", got, err)
	}
}

func TestP026I04ResumeAfterCloseCommitOnlyCheckpointsResult(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p026-close-barrier",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p026-close-barrier", "session-p026-close-barrier", "command-p026-close-barrier", "run-p026-close-barrier", "echo close")
	job, _, err := authority.AcceptJob(context.Background(), request.Acceptance)
	if err != nil {
		t.Fatal(err)
	}
	createHash, err := oneOffCreateHash(job, request)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateSession(context.Background(), CreateSessionRequest{
		SessionID:         job.SessionID,
		IdempotencyKey:    jobStepKey(job.JobID, "create_session"),
		RequestHash:       createHash,
		Environment:       job.Environment,
		Target:            job.Target,
		Controller:        job.Controller,
		Source:            job.Source,
		MaxActiveSessions: request.MaxActiveSessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseAcceptingCommand}); err != nil {
		t.Fatal(err)
	}
	commandHash, err := oneOffCommandHash(job, created.Session.Limits.CommandTimeout)
	if err != nil {
		t.Fatal(err)
	}
	commandResult, err := service.SubmitCommand(context.Background(), SubmitCommandRequest{
		CommandID:            job.CommandID,
		SessionID:            job.SessionID,
		Controller:           job.Controller,
		IdempotencyKey:       jobStepKey(job.JobID, "submit_command"),
		RequestHash:          commandHash,
		Script:               string(job.ScriptBytes),
		Timeout:              created.Session.Limits.CommandTimeout,
		IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		if _, err := authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAcceptingCommand, NextPhase: store.JobPhaseAwaitingCommand, Command: &commandResult.Command}); err != nil {
			return err
		}
		_, err := authority.CheckpointJob(context.Background(), job.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAwaitingCommand, NextPhase: store.JobPhaseClosingSession, Command: &commandResult.Command})
		return err
	}(); err != nil {
		t.Fatal(err)
	}
	closeHash, err := oneOffCloseHash(job)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := service.CloseSession(context.Background(), CloseSessionRequest{
		SessionID:            job.SessionID,
		Controller:           job.Controller,
		IdempotencyKey:       jobStepKey(job.JobID, "close_session"),
		RequestHash:          closeHash,
		Policy:               "one_off",
		IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil || closed.Session.State != domain.SessionStateClosed {
		t.Fatalf("close barrier = %+v err=%v", closed, err)
	}
	resumed, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Job.Phase != store.JobPhaseComplete || resumed.Job.TeardownState != store.JobTeardownClosed || resumed.Command.State != domain.CommandStateSucceeded || resumed.Session.State != domain.SessionStateClosed {
		t.Fatalf("close barrier resume = %+v", resumed)
	}
	if runtime.commandCall != 1 || runtime.stopCall != 1 {
		t.Fatalf("close barrier reran work: command=%d stop=%d", runtime.commandCall, runtime.stopCall)
	}
}
