package execution

import (
	"context"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p025Runtime struct {
	*p020FakeRuntime
	scripts []string
}

func (r *p025Runtime) ExecuteCommand(ctx context.Context, request RuntimeCommandRequest) (RuntimeCommandResult, error) {
	r.scripts = append(r.scripts, string(request.Command.ScriptBytes))
	return r.p020FakeRuntime.ExecuteCommand(ctx, request)
}

func TestP025I04OneOffRunCreatesAndExecutesExactlyOneCommand(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p025-success",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{Stdout: []byte("one-off output\n"), ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p025-success", "session-p025-success", "command-p025-success", "run-p025-success", "printf 'one-off output\\n'")
	result, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Phase != store.JobPhaseComplete || result.Session.State != domain.SessionStateClosed || result.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("one-off result = %+v", result)
	}
	if result.Command.ExitCode == nil || *result.Command.ExitCode != 0 || !result.Command.OutputComplete {
		t.Fatalf("one-off command outcome = %+v", result.Command)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 || len(runtime.scripts) != 1 || runtime.scripts[0] != request.Acceptance.Script {
		t.Fatalf("runtime calls prepare=%d start=%d command=%d scripts=%q", runtime.prepareCall, runtime.startCall, runtime.commandCall, runtime.scripts)
	}
	retry, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Job.JobID != result.Job.JobID || retry.Session.SessionID != result.Session.SessionID || retry.Command.CommandID != result.Command.CommandID {
		t.Fatalf("retry changed stable IDs: first=%+v retry=%+v", result, retry)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 || len(runtime.scripts) != 1 {
		t.Fatalf("retry reran runtime prepare=%d start=%d command=%d scripts=%q", runtime.prepareCall, runtime.startCall, runtime.commandCall, runtime.scripts)
	}
	if got, err := authority.GetCommand(context.Background(), request.Acceptance.CommandID); err != nil || got.State != domain.CommandStateSucceeded {
		t.Fatalf("authoritative command after retry = %+v err=%v", got, err)
	}
}

func TestP025I04ResumeAfterSessionCommitUsesStableCreateStep(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p025-session-barrier",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p025-session-barrier", "session-p025-session-barrier", "command-p025-session-barrier", "run-p025-session-barrier", "echo barrier")
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
	if err != nil || created.Session.State != domain.SessionStateReady {
		t.Fatalf("session barrier create = %+v err=%v", created, err)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 {
		t.Fatalf("barrier runtime create calls = prepare %d start %d", runtime.prepareCall, runtime.startCall)
	}

	resumed, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Job.Phase != store.JobPhaseComplete || resumed.Session.State != domain.SessionStateClosed || resumed.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("resumed session barrier = %+v", resumed)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 {
		t.Fatalf("session barrier reran create/command: prepare=%d start=%d command=%d", runtime.prepareCall, runtime.startCall, runtime.commandCall)
	}
	if got, err := authority.GetJob(context.Background(), job.JobID); err != nil || got.SessionID != job.SessionID || got.CommandID != job.CommandID {
		t.Fatalf("durable job after session barrier = %+v err=%v", got, err)
	}
}

func TestP025I04ReopenAfterSessionCommitKeepsStableIDs(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p025-reopen",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{ExitCode: 0},
	}}
	databasePath := t.TempDir() + "/state/p025-reopen.db"
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	clock := &p020Clock{now: time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(p025Environment(t))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, clock, &p020Publisher{})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	request := p025Request(t, "job-p025-reopen", "session-p025-reopen", "command-p025-reopen", "run-p025-reopen", "echo reopen")
	job, _, err := authority.AcceptJob(context.Background(), request.Acceptance)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	createHash, err := oneOffCreateHash(job, request)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := service.CreateSession(context.Background(), CreateSessionRequest{
		SessionID:         job.SessionID,
		IdempotencyKey:    jobStepKey(job.JobID, "create_session"),
		RequestHash:       createHash,
		Environment:       job.Environment,
		Target:            job.Target,
		Controller:        job.Controller,
		Source:            job.Source,
		MaxActiveSessions: request.MaxActiveSessions,
	}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopenedAuthority, err := store.NewAuthorityStoreWithClock(reopenedDB, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	reopenedService, err := NewService(reopenedAuthority, runtime, registry, clock, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := reopenedService.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Job.JobID != job.JobID || resumed.Session.SessionID != job.SessionID || resumed.Command.CommandID != job.CommandID || resumed.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("reopened one-off result = %+v", resumed)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 {
		t.Fatalf("reopen reran create or command: prepare=%d start=%d command=%d", runtime.prepareCall, runtime.startCall, runtime.commandCall)
	}
}

func TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-p025-command-barrier",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{Stdout: []byte("queued once\n"), ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-p025-command-barrier", "session-p025-command-barrier", "command-p025-command-barrier", "run-p025-command-barrier", "echo queued once")
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
	accepted, duplicate, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID:      job.CommandID,
		SessionID:      job.SessionID,
		RequestHash:    commandHash,
		IdempotencyKey: jobStepKey(job.JobID, "submit_command"),
		Script:         string(job.ScriptBytes),
		Timeout:        created.Session.Limits.CommandTimeout,
	})
	if err != nil || duplicate || accepted.State != domain.CommandStateQueued {
		t.Fatalf("command barrier accept = %+v duplicate=%v err=%v", accepted, duplicate, err)
	}

	resumed, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Job.Phase != store.JobPhaseComplete || resumed.Session.State != domain.SessionStateClosed || resumed.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("resumed command barrier = %+v", resumed)
	}
	if runtime.commandCall != 1 || len(runtime.scripts) != 1 || runtime.scripts[0] != request.Acceptance.Script {
		t.Fatalf("command was sourced more than once: calls=%d scripts=%q", runtime.commandCall, runtime.scripts)
	}
	second, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Command.State != domain.CommandStateSucceeded || runtime.commandCall != 1 || len(runtime.scripts) != 1 {
		t.Fatalf("retry after terminal command reran source: %+v calls=%d scripts=%q", second, runtime.commandCall, runtime.scripts)
	}
	if got, err := authority.ListCommandEvents(context.Background(), job.CommandID); err != nil || len(got) < 3 {
		t.Fatalf("command events after resume = %v err=%v", got, err)
	}
}

func newP025Service(t *testing.T, runtime *p025Runtime) (*Service, *store.AuthorityStore, *p025Runtime) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir()+"/state/p025.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &p020Clock{now: time.Date(2026, 9, 26, 20, 0, 0, 0, time.UTC)}
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
	return service, authority, runtime
}

func p025Environment(t *testing.T) domain.Environment {
	t.Helper()
	target := p020Target(t, domain.TargetKindRemote, "linux-host")
	controller := p020Controller(t, domain.ControllerTypeDirectMTLS)
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name:               "linux-dev",
		HostClass:          "Linux host process",
		EffectiveAccount:   "ubuntu",
		AllowedTargets:     []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func p025Request(t *testing.T, jobID, sessionID, commandID, key, script string) RunJobRequest {
	t.Helper()
	target := p020Target(t, domain.TargetKindRemote, "linux-host")
	controller := p020Controller(t, domain.ControllerTypeDirectMTLS)
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return RunJobRequest{
		Acceptance: store.JobAcceptance{
			JobID:                domain.JobID(jobID),
			SessionID:            domain.SessionID(sessionID),
			CommandID:            domain.CommandID(commandID),
			Controller:           controller,
			IdempotencyKey:       key,
			RequestHash:          hash,
			Environment:          "linux-dev",
			Target:               target,
			Source:               domain.NewEmptySource(),
			Script:               script,
			CanonicalPayload:     canonical,
			IdempotencyRetention: time.Hour,
		},
		MaxActiveSessions:    store.DefaultActiveSessionLimit,
		IdempotencyRetention: time.Hour,
	}
}
