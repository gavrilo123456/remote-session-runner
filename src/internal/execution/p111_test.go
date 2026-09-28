package execution

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP111I04JobRestartResumesEveryDurableBarrier(t *testing.T) {
	for _, barrier := range []string{"job_row", "session_commit", "command_acceptance", "teardown_commit"} {
		t.Run(barrier, func(t *testing.T) {
			ctx := context.Background()
			databasePath := filepath.Join(t.TempDir(), "state", "p111.db")
			runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
				generation:    "generation-p111-" + barrier,
				stopConfirmed: true,
				commandResult: RuntimeCommandResult{Stdout: []byte("p111 exact output\n"), ExitCode: 0},
			}}
			clock := &p020Clock{now: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}
			request := p025Request(t,
				"job-p111-"+barrier,
				"session-p111-"+barrier,
				"command-p111-"+barrier,
				"run-p111-"+barrier,
				"printf 'p111 exact output\\n'",
			)

			db, authority, service := p111OpenService(t, databasePath, runtime, clock)
			job, duplicate, err := authority.AcceptJob(ctx, request.Acceptance)
			if err != nil || duplicate {
				t.Fatalf("accept one-off row: duplicate=%v err=%v", duplicate, err)
			}
			if job.Phase != store.JobPhaseCreatingSession || job.JobID != request.Acceptance.JobID || job.SessionID != request.Acceptance.SessionID || job.CommandID != request.Acceptance.CommandID {
				t.Fatalf("initial durable job row = %+v", job)
			}

			if barrier != "job_row" {
				created := p111CreateJobSession(t, ctx, service, job, request)
				if barrier == "command_acceptance" || barrier == "teardown_commit" {
					job, err = authority.CheckpointJob(ctx, job.JobID, store.JobCheckpoint{
						ExpectedPhase: store.JobPhaseCreatingSession,
						NextPhase:     store.JobPhaseAcceptingCommand,
					})
					if err != nil {
						t.Fatal(err)
					}
					commandHash, hashErr := oneOffCommandHash(job, created.Limits.CommandTimeout)
					if hashErr != nil {
						t.Fatal(hashErr)
					}
					commandRequest := SubmitCommandRequest{
						CommandID: job.CommandID, SessionID: job.SessionID, Controller: job.Controller,
						IdempotencyKey: jobStepKey(job.JobID, "submit_command"), RequestHash: commandHash,
						Script: string(job.ScriptBytes), Timeout: created.Limits.CommandTimeout,
						IdempotencyRetention: request.IdempotencyRetention,
					}
					if barrier == "command_acceptance" {
						queued, acceptErr := service.AcceptCommand(ctx, commandRequest)
						if acceptErr != nil || queued.Command.State != domain.CommandStateQueued || queued.Duplicate {
							t.Fatalf("command acceptance barrier = %+v duplicate=%v err=%v", queued.Command, queued.Duplicate, acceptErr)
						}
					} else {
						commandResult, submitErr := service.SubmitCommand(ctx, commandRequest)
						if submitErr != nil || commandResult.Command.State != domain.CommandStateSucceeded {
							t.Fatalf("command before teardown barrier = %+v err=%v", commandResult.Command, submitErr)
						}
						job, err = authority.CheckpointJob(ctx, job.JobID, store.JobCheckpoint{
							ExpectedPhase: store.JobPhaseAcceptingCommand,
							NextPhase:     store.JobPhaseAwaitingCommand,
							Command:       &commandResult.Command,
						})
						if err != nil {
							t.Fatal(err)
						}
						job, err = authority.CheckpointJob(ctx, job.JobID, store.JobCheckpoint{
							ExpectedPhase: store.JobPhaseAwaitingCommand,
							NextPhase:     store.JobPhaseClosingSession,
							Command:       &commandResult.Command,
						})
						if err != nil {
							t.Fatal(err)
						}
						closeHash, hashErr := oneOffCloseHash(job)
						if hashErr != nil {
							t.Fatal(hashErr)
						}
						closed, closeErr := service.CloseSession(ctx, CloseSessionRequest{
							SessionID: job.SessionID, Controller: job.Controller,
							IdempotencyKey: jobStepKey(job.JobID, "close_session"), RequestHash: closeHash,
							Policy: "one_off", IdempotencyRetention: request.IdempotencyRetention,
						})
						if closeErr != nil || closed.Session.State != domain.SessionStateClosed {
							t.Fatalf("teardown commit barrier = %+v err=%v", closed.Session, closeErr)
						}
					}
				}
			}

			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, authority, service = p111OpenService(t, databasePath, runtime, clock)
			defer db.Close()

			resumed, err := service.ResumeJob(ctx, request.Acceptance.JobID, request.Acceptance.Controller, request)
			if err != nil {
				t.Fatalf("resume after %s commit: %v", barrier, err)
			}
			if resumed.Job.Phase != store.JobPhaseComplete || resumed.Job.TeardownState != store.JobTeardownClosed ||
				resumed.Session.State != domain.SessionStateClosed || resumed.Command.State != domain.CommandStateSucceeded ||
				resumed.Job.CommandState == nil || *resumed.Job.CommandState != domain.CommandStateSucceeded ||
				resumed.Job.ExitCode == nil || *resumed.Job.ExitCode != 0 || !resumed.Job.OutputComplete {
				t.Fatalf("resumed one-off result after %s commit = %+v", barrier, resumed)
			}
			if resumed.Job.JobID != request.Acceptance.JobID || resumed.Session.SessionID != request.Acceptance.SessionID || resumed.Command.CommandID != request.Acceptance.CommandID {
				t.Fatalf("restart changed stable IDs after %s commit: %+v", barrier, resumed)
			}
			if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 || runtime.stopCall != 1 ||
				len(runtime.scripts) != 1 || runtime.scripts[0] != request.Acceptance.Script {
				t.Fatalf("restart repeated or changed work after %s commit: prepare=%d start=%d command=%d stop=%d scripts=%q",
					barrier, runtime.prepareCall, runtime.startCall, runtime.commandCall, runtime.stopCall, runtime.scripts)
			}
			stored, err := authority.GetJob(ctx, request.Acceptance.JobID)
			if err != nil || string(stored.ScriptBytes) != request.Acceptance.Script || stored.JobID != resumed.Job.JobID || stored.SessionID != resumed.Session.SessionID || stored.CommandID != resumed.Command.CommandID {
				t.Fatalf("durable job after restart = %+v err=%v", stored, err)
			}
			events, err := authority.ListCommandEvents(ctx, request.Acceptance.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			started := 0
			var stdout []byte
			for _, event := range events {
				if event.Type == "command_started" {
					started++
				}
				if event.Type == "stdout" {
					stdout = append(stdout, event.Payload...)
				}
			}
			if started != 1 {
				t.Fatalf("command_started events after %s restart = %d, want exactly one", barrier, started)
			}
			if string(stdout) != "p111 exact output\n" {
				t.Fatalf("stdout after %s restart = %q, want exact durable bytes", barrier, stdout)
			}
			if _, err := service.RunJob(ctx, request); err != nil {
				t.Fatalf("same-key replay after %s restart: %v", barrier, err)
			}
			if runtime.commandCall != 1 || len(runtime.scripts) != 1 || runtime.stopCall != 1 {
				t.Fatalf("same-key retry repeated one-off work after %s: command=%d stop=%d scripts=%q", barrier, runtime.commandCall, runtime.stopCall, runtime.scripts)
			}
		})
	}
}

func p111CreateJobSession(t *testing.T, ctx context.Context, service *Service, job store.JobRecord, request RunJobRequest) store.SessionRecord {
	t.Helper()
	requestHash, err := oneOffCreateHash(job, request)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateSession(ctx, CreateSessionRequest{
		SessionID: job.SessionID, IdempotencyKey: jobStepKey(job.JobID, "create_session"),
		RequestHash: requestHash, Environment: job.Environment, Target: job.Target,
		Controller: job.Controller, Source: job.Source, RequestedLimits: request.RequestedLimits,
		Isolation: request.Isolation, MaxActiveSessions: request.MaxActiveSessions,
		IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil || created.Session.State != domain.SessionStateReady {
		t.Fatalf("create one-off session barrier = %+v err=%v", created.Session, err)
	}
	return created.Session
}

func p111OpenService(t *testing.T, databasePath string, runtime *p025Runtime, clock *p020Clock) (*sql.DB, *store.AuthorityStore, *Service) {
	t.Helper()
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close P111 test database: %v", err)
		}
	})
	authority, err := store.NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(p025Environment(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, clock, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	return db, authority, service
}
