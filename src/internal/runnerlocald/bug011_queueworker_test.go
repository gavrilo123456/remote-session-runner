package runnerlocald

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/queueworker"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

// bug011P4Runtime is a host-free local-runtime fixture. P3 covers Darwin
// process proof itself; these tests prove that runner-locald gives the shared
// queue worker the only path from durable acceptance to execution.
type bug011P4Runtime struct {
	mu                 sync.Mutex
	generation         string
	retainedSessions   map[domain.SessionID]bool
	cleanupConfirmed   bool
	executed           map[domain.CommandID]int
	started            chan domain.CommandID
	recoveryCalls      int
	finalizeCalls      int
	recoveryEntered    chan struct{}
	recoveryRelease    <-chan struct{}
	recoveryErr        error
	executionEntered   chan domain.CommandID
	executionRelease   <-chan struct{}
	blockedExecutionID domain.CommandID
}

var _ execution.LostRuntimeRecoverer = (*bug011P4Runtime)(nil)

func newBUG011P4Runtime(cleanupConfirmed bool) *bug011P4Runtime {
	return &bug011P4Runtime{
		generation:       "generation-bug011-p4",
		retainedSessions: make(map[domain.SessionID]bool),
		cleanupConfirmed: cleanupConfirmed,
		executed:         make(map[domain.CommandID]int),
		started:          make(chan domain.CommandID, 16),
	}
}

func (r *bug011P4Runtime) Prepare(_ context.Context, _ execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *bug011P4Runtime) StartAgent(_ context.Context, _ execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (*bug011P4Runtime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (r *bug011P4Runtime) ExecuteCommand(_ context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.mu.Lock()
	r.executed[request.Command.CommandID]++
	entered := r.executionEntered
	release := r.executionRelease
	blocked := r.blockedExecutionID == request.Command.CommandID
	r.mu.Unlock()
	select {
	case r.started <- request.Command.CommandID:
	default:
	}
	if blocked && entered != nil {
		select {
		case entered <- request.Command.CommandID:
		default:
		}
	}
	if blocked && release != nil {
		<-release
	}
	return execution.RuntimeCommandResult{Stdout: []byte("bug011-p4\n"), ExitCode: 0}, nil
}

func (r *bug011P4Runtime) CancelCommand(_ context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	r.mu.Lock()
	retained := r.retainedSessions[request.Session.SessionID]
	r.mu.Unlock()
	return execution.RuntimeCommandStopResult{Confirmed: !retained}, nil
}

func (r *bug011P4Runtime) StopSession(_ context.Context, session store.SessionRecord) (bool, error) {
	r.mu.Lock()
	retained := r.retainedSessions[session.SessionID]
	r.mu.Unlock()
	return !retained, nil
}

func (r *bug011P4Runtime) ReconcileLostRuntime(_ context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	r.mu.Lock()
	r.recoveryCalls++
	confirmed := r.cleanupConfirmed
	entered := r.recoveryEntered
	release := r.recoveryRelease
	recoveryErr := r.recoveryErr
	r.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return execution.RuntimeReconcileResult{RuntimeGeneration: request.Session.RuntimeGeneration, CleanupConfirmed: confirmed}, recoveryErr
}

func (r *bug011P4Runtime) FinalizeLostRuntime(_ context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	r.mu.Lock()
	r.finalizeCalls++
	r.mu.Unlock()
	return execution.RuntimeReconcileResult{RuntimeGeneration: request.Session.RuntimeGeneration, CleanupConfirmed: true}, nil
}

func (r *bug011P4Runtime) markRetained(sessionID domain.SessionID) {
	r.mu.Lock()
	r.retainedSessions[sessionID] = true
	r.mu.Unlock()
}

func (r *bug011P4Runtime) executionCount(commandID domain.CommandID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.executed[commandID]
}

func (r *bug011P4Runtime) recoveryCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recoveryCalls
}

func (r *bug011P4Runtime) finalizationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finalizeCalls
}

func (r *bug011P4Runtime) blockRecovery(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	r.mu.Lock()
	r.recoveryEntered = entered
	r.recoveryRelease = release
	r.mu.Unlock()
	return entered, func() { once.Do(func() { close(release) }) }
}

func (r *bug011P4Runtime) setRecoveryError(err error) {
	r.mu.Lock()
	r.recoveryErr = err
	r.mu.Unlock()
}

func (r *bug011P4Runtime) blockExecution(t *testing.T, commandID domain.CommandID) (<-chan domain.CommandID, func()) {
	t.Helper()
	entered := make(chan domain.CommandID, 1)
	release := make(chan struct{})
	var once sync.Once
	r.mu.Lock()
	r.executionEntered = entered
	r.executionRelease = release
	r.blockedExecutionID = commandID
	r.mu.Unlock()
	return entered, func() { once.Do(func() { close(release) }) }
}

func newBUG011P4Service(t *testing.T, cleanupConfirmed bool) (*store.AuthorityStore, *execution.Service, *bug011P4Runtime) {
	t.Helper()
	root := testfixture.New(t)
	database, err := store.Open(t.Context(), filepath.Join(root.Path(), "state", "bug011-p4.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBUG011P4Runtime(cleanupConfirmed)
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return authority, service, runtime
}

func newBUG011P4Worker(t *testing.T, service *execution.Service, authority *store.AuthorityStore, gate *lifecycle.Gate, interval time.Duration) *queueworker.Worker {
	t.Helper()
	worker, err := queueworker.New(queueworker.Options{
		Service: service, Authority: authority, DispatchGate: gate,
		RecoveryInterval: interval, RetainedLostCapacityRecoveryInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func stopBUG011P4Worker(t *testing.T, worker *queueworker.Worker, gate *lifecycle.Gate) {
	t.Helper()
	gate.Stop()
	worker.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Wait(ctx); err != nil {
		t.Fatalf("wait shared queue worker: %v", err)
	}
	if err := gate.Wait(ctx); err != nil {
		t.Fatalf("wait shared dispatch gate: %v", err)
	}
}

// startBUG011P4PrivateServer runs the same server/worker composition as
// runner-locald.Run, but only against a temporary test authority. Existing
// private-API tests use it where they need execution rather than acceptance
// alone.
func startBUG011P4PrivateServer(t *testing.T, authority *store.AuthorityStore, service *execution.Service, socketPath string) (*PrivateServer, *queueworker.Worker) {
	t.Helper()
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile B011-P4 temporary locald startup: %v", err)
	}
	gate := lifecycle.NewGate()
	worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: socketPath, DispatchGate: gate, QueueWake: worker.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		_ = server.Close(context.Background())
		t.Fatal(err)
	}
	worker.Start(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.StopAccepting(); err != nil {
			t.Errorf("stop B011-P4 private server acceptance: %v", err)
		}
		server.StopDispatch()
		worker.Stop()
		if err := worker.Wait(closeContext); err != nil {
			t.Errorf("wait B011-P4 shared worker: %v", err)
		}
		if err := server.Drain(closeContext); err != nil {
			t.Errorf("drain B011-P4 private server: %v", err)
		}
		if err := server.CloseStreams(); err != nil {
			t.Errorf("close B011-P4 private server streams: %v", err)
		}
		if err := <-serveErr; err != nil {
			t.Errorf("serve B011-P4 private server: %v", err)
		}
	})
	return server, worker
}

func TestBUG011LocalSubmitQueuesBeforeWorkerStartsAndUsesWake(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	gate := lifecycle.NewGate()
	var wakes int
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t), DispatchGate: gate,
		QueueWake: func() { wakes++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.dispatchGate != gate {
		t.Fatal("private server did not retain the supplied shared dispatch gate")
	}
	create := p060CreateIntent(t, authority, "intent-bug011-p4-submit-create", "session-bug011-p4-submit", "key-bug011-p4-submit-create")
	if _, err := server.acceptIntent(context.Background(), create); err != nil {
		t.Fatalf("accept local session: %v", err)
	}
	submitInput := p060SubmitIntent(t, "intent-bug011-p4-submit", string(create.SessionID), "command-bug011-p4-submit", "key-bug011-p4-submit", "printf should-stay-queued")
	submit, err := authority.CreateLocalIntent(context.Background(), submitInput)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := server.acceptIntent(context.Background(), submit)
	if err != nil || accepted.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("accepted local command=%+v err=%v, want queued", accepted, err)
	}
	server.wakeQueueAfterAcceptedIntent(accepted)
	if wakes != 1 {
		t.Fatalf("queue wakes=%d, want one after durable acceptance", wakes)
	}
	select {
	case started := <-runtime.started:
		t.Fatalf("detached local execution started %s before a worker existed", started)
	case <-time.After(100 * time.Millisecond):
	}
	command, err := authority.GetCommand(context.Background(), submit.CommandID)
	if err != nil || command.State != domain.CommandStateQueued || runtime.executionCount(submit.CommandID) != 0 {
		t.Fatalf("durable queued command=%+v executions=%d err=%v", command, runtime.executionCount(submit.CommandID), err)
	}
	gate.Stop()
}

// TestBUG011LocalWakeFollowsAcceptanceWrite keeps local ingress aligned with
// runnerd: durable acceptance becomes visible before its shared worker may
// inspect or execute the accepted record.
func TestBUG011LocalWakeFollowsAcceptanceWrite(t *testing.T) {
	authority, service, _ := newBUG011P4Service(t, true)
	writer := &bug011P4OrderingResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	var wokeBeforeAcceptance atomic.Bool
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t), DispatchGate: lifecycle.NewGate(),
		QueueWake: func() {
			if !writer.headerWritten.Load() {
				wokeBeforeAcceptance.Store(true)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent := p078LocalRunIntent(t, authority)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/accept-intent", bytes.NewBufferString(`{"intent_id":"`+string(intent.IntentID)+`","request_hash":"`+intent.RequestHash.String()+`"}`))
	server.serveHTTP(writer, request)
	if wokeBeforeAcceptance.Load() {
		t.Fatal("local queue wake ran before the accepted response was written")
	}
	if writer.Code != http.StatusAccepted {
		t.Fatalf("local acceptance status=%d body=%s", writer.Code, writer.Body.String())
	}
}

func TestBUG011LocalRejectsQueueWakeWithoutSharedDispatchGate(t *testing.T) {
	authority, service, _ := newBUG011P4Service(t, true)
	_, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t), QueueWake: func() {},
	})
	if !errors.Is(err, ErrPrivateAPIConfiguration) {
		t.Fatalf("queue wake without shared dispatch gate error=%v, want ErrPrivateAPIConfiguration", err)
	}
}

type bug011P4OrderingResponseWriter struct {
	*httptest.ResponseRecorder
	headerWritten atomic.Bool
}

func (w *bug011P4OrderingResponseWriter) WriteHeader(status int) {
	w.headerWritten.Store(true)
	w.ResponseRecorder.WriteHeader(status)
}

func TestBUG011LocalRunIsDurablyAcceptedBeforeWorkerExecution(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	gate := lifecycle.NewGate()
	worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
	defer stopBUG011P4Worker(t, worker, gate)
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t), DispatchGate: gate, QueueWake: worker.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.dispatchGate != gate {
		t.Fatal("run ingress and worker do not share a dispatch gate")
	}
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile local runtime before queue worker: %v", err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		t.Fatalf("settle pre-existing durable jobs: %v", err)
	}
	intent := p078LocalRunIntent(t, authority)
	accepted, err := server.acceptIntent(context.Background(), intent)
	if err != nil || accepted.JobPhase != string(store.JobPhaseCreatingSession) || accepted.SessionState != "" || accepted.CommandState != "" {
		t.Fatalf("durable run acceptance=%+v err=%v", accepted, err)
	}
	server.wakeQueueAfterAcceptedIntent(accepted)
	job, err := authority.GetJob(context.Background(), intent.JobID)
	if err != nil || job.JobID != intent.JobID || job.SessionID != intent.SessionID || job.CommandID != intent.CommandID || string(job.ScriptBytes) != string(intent.ScriptBytes) || job.Phase != store.JobPhaseCreatingSession {
		t.Fatalf("accepted job=%+v err=%v", job, err)
	}
	if runtime.executionCount(intent.CommandID) != 0 {
		t.Fatal("durable run acceptance executed before the worker started")
	}

	worker.Start(context.Background())
	completed := waitBUG011P4Job(t, authority, intent.JobID, store.JobPhaseComplete)
	if completed.SessionID != intent.SessionID || completed.CommandID != intent.CommandID {
		t.Fatalf("completed job changed durable identities: %+v", completed)
	}
	command, err := authority.GetCommand(context.Background(), intent.CommandID)
	if err != nil || command.State != domain.CommandStateSucceeded || runtime.executionCount(intent.CommandID) != 1 {
		t.Fatalf("completed command=%+v executions=%d err=%v", command, runtime.executionCount(intent.CommandID), err)
	}
	events, err := authority.ListCommandEvents(context.Background(), intent.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, event := range events {
		if event.Type == "command_started" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("command_started events=%d, want one", started)
	}
	select {
	case startedCommand := <-runtime.started:
		if startedCommand != intent.CommandID {
			t.Fatalf("started command=%s, want %s", startedCommand, intent.CommandID)
		}
	case <-time.After(time.Second):
		t.Fatal("completed run did not report its one execution")
	}
	replay, err := server.acceptIntent(context.Background(), intent)
	if err != nil || !replay.Duplicate || replay.JobPhase != string(store.JobPhaseComplete) {
		t.Fatalf("replayed durable run=%+v err=%v", replay, err)
	}
	server.wakeQueueAfterAcceptedIntent(replay)
	select {
	case duplicate := <-runtime.started:
		t.Fatalf("replayed run started a second command %s", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBUG011LocalSharedDispatchGateStopsWorkerClaims(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	gate := lifecycle.NewGate()
	worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
	defer stopBUG011P4Worker(t, worker, gate)
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t), DispatchGate: gate, QueueWake: worker.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile local runtime before queue worker: %v", err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	worker.Start(context.Background())
	server.StopDispatch()
	create := p060CreateIntent(t, authority, "intent-bug011-p4-stop-create", "session-bug011-p4-stop", "key-bug011-p4-stop-create")
	if _, err := server.acceptIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	submitInput := p060SubmitIntent(t, "intent-bug011-p4-stop", string(create.SessionID), "command-bug011-p4-stop", "key-bug011-p4-stop", "printf must-not-start-after-stop")
	submit, err := authority.CreateLocalIntent(context.Background(), submitInput)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := server.acceptIntent(context.Background(), submit)
	if err != nil {
		t.Fatal(err)
	}
	server.wakeQueueAfterAcceptedIntent(accepted)
	select {
	case started := <-runtime.started:
		t.Fatalf("shared stopped dispatch gate allowed command %s to start", started)
	case <-time.After(100 * time.Millisecond):
	}
	command, err := authority.GetCommand(context.Background(), submit.CommandID)
	if err != nil || command.State != domain.CommandStateQueued || runtime.executionCount(submit.CommandID) != 0 {
		t.Fatalf("stopped-gate command=%+v executions=%d err=%v", command, runtime.executionCount(submit.CommandID), err)
	}
	shutdown := &privateServerShutdown{server: server, worker: worker}
	shutdown.StopAccepting()
	shutdown.StopDispatch()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shutdown.Drain(ctx); err != nil {
		t.Fatalf("drain stopped shared worker: %v", err)
	}
}

func TestBUG011LocalShutdownDrainsActiveWorkerAndStopsFurtherClaims(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	gate := lifecycle.NewGate()
	worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t), DispatchGate: gate, QueueWake: worker.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdown := &privateServerShutdown{server: server, worker: worker}
		shutdown.StopAccepting()
		shutdown.StopDispatch()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := shutdown.Drain(ctx); err != nil {
			t.Errorf("drain active B011-P4 local worker: %v", err)
		}
		if err := shutdown.CloseStreams(ctx); err != nil {
			t.Errorf("close active B011-P4 local worker streams: %v", err)
		}
	})
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile local runtime before queue worker: %v", err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	worker.Start(context.Background())

	firstCreate := p060CreateIntent(t, authority, "intent-bug011-p4-active-create", "session-bug011-p4-active", "key-bug011-p4-active-create")
	if _, err := server.acceptIntent(context.Background(), firstCreate); err != nil {
		t.Fatal(err)
	}
	firstSubmitInput := p060SubmitIntent(t, "intent-bug011-p4-active-submit", string(firstCreate.SessionID), "command-bug011-p4-active", "key-bug011-p4-active-submit", "printf active")
	firstSubmit, err := authority.CreateLocalIntent(context.Background(), firstSubmitInput)
	if err != nil {
		t.Fatal(err)
	}
	executionEntered, releaseExecution := runtime.blockExecution(t, firstSubmit.CommandID)
	defer releaseExecution()
	accepted, err := server.acceptIntent(context.Background(), firstSubmit)
	if err != nil || accepted.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("accept active command=%+v err=%v", accepted, err)
	}
	server.wakeQueueAfterAcceptedIntent(accepted)
	select {
	case got := <-executionEntered:
		if got != firstSubmit.CommandID {
			t.Fatalf("active execution command=%s, want %s", got, firstSubmit.CommandID)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not enter controlled active command")
	}
	if running := waitBUG011P4Command(t, authority, firstSubmit.CommandID, domain.CommandStateRunning); running.CommandID != firstSubmit.CommandID {
		t.Fatalf("active command=%+v", running)
	}

	shutdown := &privateServerShutdown{server: server, worker: worker}
	shutdown.StopAccepting()
	shutdown.StopDispatch()
	secondCreate := p060CreateIntent(t, authority, "intent-bug011-p4-poststop-create", "session-bug011-p4-poststop", "key-bug011-p4-poststop-create")
	if _, err := server.acceptIntent(context.Background(), secondCreate); err != nil {
		t.Fatal(err)
	}
	secondSubmitInput := p060SubmitIntent(t, "intent-bug011-p4-poststop-submit", string(secondCreate.SessionID), "command-bug011-p4-poststop", "key-bug011-p4-poststop-submit", "printf must-not-run")
	secondSubmit, err := authority.CreateLocalIntent(context.Background(), secondSubmitInput)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err = server.acceptIntent(context.Background(), secondSubmit)
	if err != nil || accepted.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("accept post-stop command=%+v err=%v", accepted, err)
	}
	server.wakeQueueAfterAcceptedIntent(accepted)

	blockedDrainContext, cancelBlockedDrain := context.WithTimeout(context.Background(), 25*time.Millisecond)
	blockedDrainErr := shutdown.Drain(blockedDrainContext)
	cancelBlockedDrain()
	if !errors.Is(blockedDrainErr, context.DeadlineExceeded) {
		t.Fatalf("shutdown drain while active execution was blocked = %v, want context deadline", blockedDrainErr)
	}
	if runtime.executionCount(secondSubmit.CommandID) != 0 {
		t.Fatal("shared stopped dispatch gate claimed post-stop queued work")
	}
	releaseExecution()
	drainContext, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err := shutdown.Drain(drainContext); err != nil {
		t.Fatalf("shutdown did not drain active worker: %v", err)
	}
	first := waitBUG011P4Command(t, authority, firstSubmit.CommandID, domain.CommandStateSucceeded)
	if !first.OutputComplete || runtime.executionCount(firstSubmit.CommandID) != 1 {
		t.Fatalf("drained active command=%+v executions=%d", first, runtime.executionCount(firstSubmit.CommandID))
	}
	if runtime.executionCount(secondSubmit.CommandID) != 0 {
		t.Fatal("post-stop queued work executed during shutdown drain")
	}
	if err := server.CloseStreams(); err != nil {
		t.Fatalf("close stopped locald streams: %v", err)
	}
}

func TestBUG011LocalWorkerRecoversConfirmedLostCapacityThenRunsQueuedIdentityOnce(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	var lost []store.CommandRecord
	for _, suffix := range []string{"first", "second", "third", "fourth"} {
		_, command := seedBUG011P4LostPair(t, authority, service, runtime, suffix)
		lost = append(lost, command)
	}
	queued := acceptBUG011P4QueuedOneOff(t, authority, service)
	gate := lifecycle.NewGate()
	worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
	defer stopBUG011P4Worker(t, worker, gate)
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile local runtime before queue worker: %v", err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitBUG011P4Job(t, authority, queued.JobID, store.JobPhaseAwaitingCommand)
	recoveryEntered, releaseRecovery := runtime.blockRecovery(t)
	defer releaseRecovery()
	worker.Start(context.Background())
	select {
	case <-recoveryEntered:
	case <-time.After(time.Second):
		t.Fatal("shared worker did not begin retained-capacity proof")
	}
	blocked, err := authority.GetCommand(context.Background(), queued.CommandID)
	if err != nil || blocked.State != domain.CommandStateQueued || runtime.executionCount(queued.CommandID) != 0 {
		t.Fatalf("queued one-off changed before paired release: command=%+v executions=%d err=%v", blocked, runtime.executionCount(queued.CommandID), err)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != store.DefaultRunningCommandLimit {
		t.Fatalf("live slots during retained-capacity proof=%d err=%v, want %d", slots, err, store.DefaultRunningCommandLimit)
	}
	releaseRecovery()
	completed := waitBUG011P4Job(t, authority, queued.JobID, store.JobPhaseComplete)
	command := waitBUG011P4Command(t, authority, queued.CommandID, domain.CommandStateSucceeded)
	if completed.CommandID != queued.CommandID || command.CommandID != queued.CommandID || runtime.executionCount(queued.CommandID) != 1 {
		t.Fatalf("recovered queued command=%+v executions=%d", command, runtime.executionCount(queued.CommandID))
	}
	for _, retained := range lost {
		current, err := authority.GetCommand(context.Background(), retained.CommandID)
		if err != nil || current.State != domain.CommandStateLost || runtime.executionCount(retained.CommandID) != 0 {
			t.Fatalf("lost command=%+v executions=%d err=%v", current, runtime.executionCount(retained.CommandID), err)
		}
	}
	if runtime.recoveryCount() != len(lost) || runtime.finalizationCount() != len(lost) {
		t.Fatalf("lost proof/finalization calls=%d/%d, want %d/%d", runtime.recoveryCount(), runtime.finalizationCount(), len(lost), len(lost))
	}
}

func TestBUG011LocalWorkerRetainsQueuedIdentityForUnconfirmedOrMismatchedLoss(t *testing.T) {
	for _, test := range []struct {
		name        string
		confirmed   bool
		recoveryErr error
	}{
		{name: "unconfirmed_runtime_proof", confirmed: false},
		{name: "mismatched_runtime_ownership", confirmed: true, recoveryErr: fmt.Errorf("test mismatched runtime ownership: %w", hostruntime.ErrRuntimeOwnershipRecord)},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority, service, runtime := newBUG011P4Service(t, test.confirmed)
			runtime.setRecoveryError(test.recoveryErr)
			for index := 0; index < store.DefaultRunningCommandLimit; index++ {
				seedBUG011P4LostPair(t, authority, service, runtime, string(rune('a'+index)))
			}
			queued := acceptBUG011P4QueuedOneOff(t, authority, service)
			gate := lifecycle.NewGate()
			worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
			defer stopBUG011P4Worker(t, worker, gate)
			if _, err := service.ReconcileStartup(context.Background()); err != nil {
				t.Fatalf("reconcile local runtime before queue worker: %v", err)
			}
			if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitBUG011P4Job(t, authority, queued.JobID, store.JobPhaseAwaitingCommand)
			worker.Start(context.Background())
			waitBUG011P4Condition(t, func() bool { return runtime.recoveryCount() >= 1 })
			command, err := authority.GetCommand(context.Background(), queued.CommandID)
			if err != nil || command.State != domain.CommandStateQueued || runtime.executionCount(queued.CommandID) != 0 {
				t.Fatalf("blocked queued command=%+v executions=%d err=%v", command, runtime.executionCount(queued.CommandID), err)
			}
			if runtime.recoveryCount() != 1 || runtime.finalizationCount() != 0 {
				t.Fatalf("runtime recovery/finalization calls=%d/%d, want 1/0", runtime.recoveryCount(), runtime.finalizationCount())
			}
			slots, err := authority.CountLiveCommandSlots(context.Background())
			if err != nil || slots != store.DefaultRunningCommandLimit {
				t.Fatalf("retained command slots=%d err=%v, want %d", slots, err, store.DefaultRunningCommandLimit)
			}
		})
	}
}

func seedBUG011P4LostPair(t *testing.T, authority *store.AuthorityStore, service *execution.Service, runtime *bug011P4Runtime, suffix string) (store.SessionRecord, store.CommandRecord) {
	t.Helper()
	session, command := seedBUG011P4QueuedCommand(t, authority, service, "lost-"+suffix)
	claimed, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit)
	if err != nil || claimed.CommandID != command.CommandID {
		t.Fatalf("claim retained lost command=%+v err=%v, want %s", claimed, err, command.CommandID)
	}
	runtime.markRetained(session.SessionID)
	requestHash, err := localMutationHash("close_session", map[string]string{"session_id": string(session.SessionID), "policy": "graceful"})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := service.CloseSession(context.Background(), execution.CloseSessionRequest{
		SessionID: session.SessionID, Controller: session.Controller, IdempotencyKey: "key-bug011-p4-close-" + suffix,
		RequestHash: requestHash, Policy: "graceful", IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if !errors.Is(err, execution.ErrStopUnconfirmed) || closed.Session.State != domain.SessionStateLost {
		t.Fatalf("seed retained lost session=%+v err=%v", closed.Session, err)
	}
	lost, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || lost.State != domain.CommandStateLost {
		t.Fatalf("seed retained lost command=%+v err=%v", lost, err)
	}
	return closed.Session, lost
}

func seedBUG011P4QueuedCommand(t *testing.T, authority *store.AuthorityStore, service *execution.Service, suffix string) (store.SessionRecord, store.CommandRecord) {
	t.Helper()
	create := p060CreateIntent(t, authority, "intent-bug011-p4-create-"+suffix, "session-bug011-p4-"+suffix, "key-bug011-p4-create-"+suffix)
	created, err := service.CreateSession(context.Background(), execution.CreateSessionRequest{
		SessionID: create.SessionID, IdempotencyKey: create.IdempotencyKey, RequestHash: create.RequestHash,
		Environment: create.Environment, Target: create.Target, Controller: create.Controller, Source: create.Source,
		MaxActiveSessions: store.DefaultActiveSessionLimit, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	submit := p060SubmitIntent(t, "intent-bug011-p4-submit-"+suffix, string(create.SessionID), "command-bug011-p4-"+suffix, "key-bug011-p4-submit-"+suffix, "printf bug011-p4")
	accepted, err := service.AcceptCommand(context.Background(), execution.SubmitCommandRequest{
		CommandID: submit.CommandID, SessionID: submit.SessionID, Controller: submit.Controller,
		IdempotencyKey: submit.IdempotencyKey, RequestHash: submit.RequestHash, Script: string(submit.ScriptBytes),
		IntentOrdinal: dereferenceOrdinal(submit.IntentOrdinal), IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil || accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("seed queued command=%+v err=%v", accepted.Command, err)
	}
	return created.Session, accepted.Command
}

// acceptBUG011P4QueuedOneOff uses the same durable one-off record that the
// private API accepts for a local run. The worker must make its session and
// command child rows itself; a regular queued command is intentionally not a
// safe target for the full-capacity recovery predicate.
func acceptBUG011P4QueuedOneOff(t *testing.T, authority *store.AuthorityStore, service *execution.Service) store.JobRecord {
	t.Helper()
	intent := p078LocalRunIntent(t, authority)
	accepted, err := service.AcceptJob(context.Background(), store.JobAcceptance{
		JobID:                intent.JobID,
		SessionID:            intent.SessionID,
		CommandID:            intent.CommandID,
		Controller:           intent.Controller,
		IdempotencyKey:       intent.IdempotencyKey,
		RequestHash:          intent.RequestHash,
		Environment:          intent.Environment,
		Target:               intent.Target,
		Source:               intent.Source,
		Script:               string(intent.ScriptBytes),
		CanonicalPayload:     intent.PayloadJSON,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil || accepted.Duplicate || accepted.Job.Phase != store.JobPhaseCreatingSession {
		t.Fatalf("accept queued one-off=%+v duplicate=%v err=%v", accepted.Job, accepted.Duplicate, err)
	}
	return accepted.Job
}

func waitBUG011P4Command(t *testing.T, authority *store.AuthorityStore, id domain.CommandID, state domain.CommandState) store.CommandRecord {
	t.Helper()
	var result store.CommandRecord
	waitBUG011P4Condition(t, func() bool {
		command, err := authority.GetCommand(context.Background(), id)
		if err != nil {
			return false
		}
		result = command
		return command.State == state
	})
	return result
}

func waitBUG011P4Job(t *testing.T, authority *store.AuthorityStore, id domain.JobID, phase store.JobPhase) store.JobRecord {
	t.Helper()
	var result store.JobRecord
	waitBUG011P4Condition(t, func() bool {
		job, err := authority.GetJob(context.Background(), id)
		if err != nil {
			return false
		}
		result = job
		return job.Phase == phase
	})
	return result
}

func waitBUG011P4Condition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for B011-P4 fixture state")
}
