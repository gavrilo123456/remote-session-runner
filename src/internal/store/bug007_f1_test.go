package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestBUG007F1ListsNonterminalJobsAndFindsJobByCommand(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)

	creatingInput := p024Acceptance(t, "job-bug007-f1-creating", "session-bug007-f1-creating", "command-bug007-f1-creating", "run-bug007-f1-creating", "echo creating")
	creating, duplicate, err := authority.AcceptJob(context.Background(), creatingInput)
	if err != nil || duplicate {
		t.Fatalf("accept creating job = %+v duplicate=%v err=%v", creating, duplicate, err)
	}
	clock.Advance(time.Second)

	awaitingInput := p024Acceptance(t, "job-bug007-f1-awaiting", "session-bug007-f1-awaiting", "command-bug007-f1-awaiting", "run-bug007-f1-awaiting", "echo awaiting")
	awaiting, duplicate, err := authority.AcceptJob(context.Background(), awaitingInput)
	if err != nil || duplicate {
		t.Fatalf("accept awaiting job = %+v duplicate=%v err=%v", awaiting, duplicate, err)
	}
	awaiting, err = authority.CheckpointJob(context.Background(), awaiting.JobID, JobCheckpoint{
		ExpectedPhase: JobPhaseCreatingSession,
		NextPhase:     JobPhaseAwaitingCommand,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)

	terminalInput := p024Acceptance(t, "job-bug007-f1-complete", "session-bug007-f1-complete", "command-bug007-f1-complete", "run-bug007-f1-complete", "echo complete")
	terminal, duplicate, err := authority.AcceptJob(context.Background(), terminalInput)
	if err != nil || duplicate {
		t.Fatalf("accept terminal job = %+v duplicate=%v err=%v", terminal, duplicate, err)
	}
	terminal, err = authority.CheckpointJob(context.Background(), terminal.JobID, JobCheckpoint{
		ExpectedPhase: JobPhaseCreatingSession,
		NextPhase:     JobPhaseComplete,
	})
	if err != nil {
		t.Fatal(err)
	}

	jobs, err := authority.ListNonterminalJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].JobID != creating.JobID || jobs[1].JobID != awaiting.JobID {
		t.Fatalf("nonterminal jobs = %+v, want creating then awaiting", jobs)
	}
	for _, job := range jobs {
		if job.Phase == JobPhaseComplete || job.Phase == JobPhaseFailed || job.Phase == JobPhaseLost {
			t.Fatalf("terminal job leaked into nonterminal list: %+v", job)
		}
	}

	byCommand, err := authority.GetJobByCommandID(context.Background(), awaiting.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if byCommand.JobID != awaiting.JobID || byCommand.CommandID != awaiting.CommandID {
		t.Fatalf("job by command = %+v, want %s", byCommand, awaiting.JobID)
	}
	if _, err := authority.GetJobByCommandID(context.Background(), domain.CommandID("command-bug007-f1-missing")); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("missing job by command error = %v, want ErrJobNotFound", err)
	}
}
