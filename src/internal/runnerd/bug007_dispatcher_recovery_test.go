package runnerd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
)

// TestBUG007DispatcherResumesReadySessionAfterCreateCheckpointFailure proves
// the queued dispatcher repairs the narrow crash window after session
// creation committed but before the coordinator checkpoint advanced. The
// existing stable session-create key must return the durable ready session;
// recovery may only run the one stored command once.
func TestBUG007DispatcherResumesReadySessionAfterCreateCheckpointFailure(t *testing.T) {
	ctx := context.Background()
	runtime := &bug007RecoveryRuntime{base: &p108Runtime{
		generation:     "bug007-recovery-ready-session",
		commandStarted: make(chan domain.CommandID, 1),
		commandDone:    make(chan domain.CommandID, 1),
	}}
	service, authority := newP046Service(t, runtime)
	controller := p107DirectController(t, "tomasz.walczuk")
	request := bug007DispatcherRunJobRequest(t, controller, "job-bug007-recovery-ready", "sess-bug007-recovery-ready", "cmd-bug007-recovery-ready", "printf recovered-ready-session")
	accepted, err := service.AcceptJob(ctx, request.Acceptance)
	if err != nil || accepted.Duplicate {
		t.Fatalf("accept job=%+v err=%v", accepted, err)
	}

	created := bug007RecoveryCreateReadySession(t, service, accepted.Job, request)
	if created.State != domain.SessionStateReady {
		t.Fatalf("durable pre-checkpoint session=%+v, want ready", created)
	}
	pending, err := authority.GetJob(ctx, accepted.Job.JobID)
	if err != nil || pending.Phase != store.JobPhaseCreatingSession {
		t.Fatalf("job before queued recovery=%+v err=%v, want creating_session", pending, err)
	}

	dispatcher := bug007NewDispatcher(t, service, authority, lifecycle.NewGate(), time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	completed := bug007WaitForTerminalJob(t, authority, accepted.Job.JobID)
	if completed.Phase != store.JobPhaseComplete || completed.CommandState == nil || *completed.CommandState != domain.CommandStateSucceeded || completed.TeardownState != store.JobTeardownClosed {
		t.Fatalf("recovered ready-session job=%+v", completed)
	}
	if got := runtime.prepareCalls.Load(); got != 1 {
		t.Fatalf("runtime prepare calls=%d, want original session creation only", got)
	}
	if got := runtime.startCalls.Load(); got != 1 {
		t.Fatalf("runtime start calls=%d, want original session creation only", got)
	}
	if got := runtime.base.commandCalls.Load(); got != 1 {
		t.Fatalf("runtime command calls=%d, want exactly one recovered command", got)
	}
	bug007AssertExecutedOnce(t, authority, accepted.Job.CommandID)
}

// TestBUG007DispatcherResumesClosingSessionAfterTransientCloseFailure proves
// a queued dispatcher can finish a durable one-off job stranded after the
// close boundary. The terminal command must be observed, not executed again;
// the existing closing session receives the same stable close key and reaches
// closed/complete.
func TestBUG007DispatcherResumesClosingSessionAfterTransientCloseFailure(t *testing.T) {
	ctx := context.Background()
	runtime := &bug007RecoveryRuntime{base: &p108Runtime{
		generation:     "bug007-recovery-closing-session",
		commandStarted: make(chan domain.CommandID, 1),
		commandDone:    make(chan domain.CommandID, 1),
	}}
	service, authority := newP046Service(t, runtime)
	controller := p107DirectController(t, "tomasz.walczuk")
	request := bug007DispatcherRunJobRequest(t, controller, "job-bug007-recovery-closing", "sess-bug007-recovery-closing", "cmd-bug007-recovery-closing", "printf recovered-closing-session")
	accepted, err := service.AcceptJob(ctx, request.Acceptance)
	if err != nil || accepted.Duplicate {
		t.Fatalf("accept job=%+v err=%v", accepted, err)
	}

	session := bug007RecoveryCreateReadySession(t, service, accepted.Job, request)
	queued := bug007RecoveryAcceptCommand(t, service, accepted.Job, session)
	claim, err := service.ClaimNextEligibleCommand(ctx)
	if err != nil || claim.CommandID != queued.CommandID {
		t.Fatalf("claim durable setup command=%+v err=%v", claim, err)
	}
	completedCommand, err := service.ExecuteClaimedCommand(ctx, claim)
	if err != nil || completedCommand.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("complete durable setup command=%+v err=%v", completedCommand, err)
	}

	if _, err := authority.CheckpointJob(ctx, accepted.Job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseCreatingSession,
		NextPhase:     store.JobPhaseAcceptingCommand,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckpointJob(ctx, accepted.Job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseAcceptingCommand,
		NextPhase:     store.JobPhaseAwaitingCommand,
		Command:       &completedCommand.Command,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckpointJob(ctx, accepted.Job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseAwaitingCommand,
		NextPhase:     store.JobPhaseClosingSession,
		Command:       &completedCommand.Command,
	}); err != nil {
		t.Fatal(err)
	}
	closing, err := authority.TransitionSession(ctx, session.SessionID, domain.SessionStateClosing, "close_requested")
	if err != nil || closing.State != domain.SessionStateClosing {
		t.Fatalf("simulate transient close checkpoint gap=%+v err=%v", closing, err)
	}

	dispatcher := bug007NewDispatcher(t, service, authority, lifecycle.NewGate(), time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	completed := bug007WaitForTerminalJob(t, authority, accepted.Job.JobID)
	if completed.Phase != store.JobPhaseComplete || completed.CommandState == nil || *completed.CommandState != domain.CommandStateSucceeded || completed.TeardownState != store.JobTeardownClosed {
		t.Fatalf("recovered closing-session job=%+v", completed)
	}
	closed, err := authority.GetSession(ctx, accepted.Job.SessionID)
	if err != nil || closed.State != domain.SessionStateClosed {
		t.Fatalf("recovered closing-session state=%+v err=%v, want closed", closed, err)
	}
	if got := runtime.prepareCalls.Load(); got != 1 {
		t.Fatalf("runtime prepare calls=%d, want original session creation only", got)
	}
	if got := runtime.startCalls.Load(); got != 1 {
		t.Fatalf("runtime start calls=%d, want original session creation only", got)
	}
	if got := runtime.base.commandCalls.Load(); got != 1 {
		t.Fatalf("runtime command calls=%d, want original terminal command only", got)
	}
	bug007AssertExecutedOnce(t, authority, accepted.Job.CommandID)
}

// bug007RecoveryRuntime counts runtime admission separately from command
// execution so the tests can prove replay uses the durable child resources.
type bug007RecoveryRuntime struct {
	base         *p108Runtime
	prepareCalls atomic.Int32
	startCalls   atomic.Int32
}

func (r *bug007RecoveryRuntime) Prepare(ctx context.Context, request execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	r.prepareCalls.Add(1)
	return r.base.Prepare(ctx, request)
}

func (r *bug007RecoveryRuntime) StartAgent(ctx context.Context, request execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	r.startCalls.Add(1)
	return r.base.StartAgent(ctx, request)
}

func (r *bug007RecoveryRuntime) Cleanup(ctx context.Context, request execution.RuntimeCleanupRequest) error {
	return r.base.Cleanup(ctx, request)
}

func (r *bug007RecoveryRuntime) ExecuteCommand(ctx context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	return r.base.ExecuteCommand(ctx, request)
}

func bug007RecoveryCreateReadySession(t *testing.T, service *execution.Service, job store.JobRecord, request execution.RunJobRequest) store.SessionRecord {
	t.Helper()
	createHash := bug007RecoveryCreateHash(t, job, request)
	created, err := service.CreateSession(context.Background(), execution.CreateSessionRequest{
		SessionID:            job.SessionID,
		IdempotencyKey:       bug007RecoveryStepKey(job.JobID, "create_session"),
		RequestHash:          createHash,
		Environment:          job.Environment,
		Target:               job.Target,
		Controller:           job.Controller,
		Source:               job.Source,
		RequestedLimits:      request.RequestedLimits,
		Isolation:            request.Isolation,
		MaxActiveSessions:    request.MaxActiveSessions,
		IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Session
}

func bug007RecoveryAcceptCommand(t *testing.T, service *execution.Service, job store.JobRecord, session store.SessionRecord) store.CommandRecord {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"operation":  "submit_command",
		"session_id": string(job.SessionID),
		"script":     string(job.ScriptBytes),
		"timeout_ns": session.Limits.CommandTimeout.Nanoseconds(),
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptCommand(context.Background(), execution.SubmitCommandRequest{
		CommandID:            job.CommandID,
		SessionID:            job.SessionID,
		Controller:           job.Controller,
		IdempotencyKey:       bug007RecoveryStepKey(job.JobID, "submit_command"),
		RequestHash:          hash,
		Script:               string(job.ScriptBytes),
		Timeout:              session.Limits.CommandTimeout,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	return accepted.Command
}

func bug007RecoveryCreateHash(t *testing.T, job store.JobRecord, request execution.RunJobRequest) domain.CanonicalHash {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"operation":        "create_session",
		"session_id":       string(job.SessionID),
		"environment":      job.Environment,
		"execution_target": map[string]any{"kind": string(job.Target.Kind()), "profile": job.Target.Profile()},
		"source":           bug007RecoverySourcePayload(job.Source),
		"requested_limits": request.RequestedLimits,
		"isolation":        request.Isolation,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func bug007RecoverySourcePayload(source domain.Source) map[string]any {
	payload := map[string]any{"mode": string(source.Mode())}
	if source.RepositoryAlias() != "" {
		payload["repository_alias"] = source.RepositoryAlias()
	}
	if source.RequestedRevision() != "" {
		payload["requested_revision"] = source.RequestedRevision()
	}
	if source.Path() != "" {
		payload["path"] = source.Path()
	}
	return payload
}

func bug007RecoveryStepKey(jobID domain.JobID, step string) string {
	digest := sha256.Sum256([]byte(string(jobID) + "\x00" + step))
	return "job-step-" + hex.EncodeToString(digest[:])
}
