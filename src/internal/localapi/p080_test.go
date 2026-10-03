package localapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/queueworker"
	"remote-session-runner/src/internal/runnerlocald"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP080CrossComponentRestartPreservesExactScriptAndExecutesOnce(t *testing.T) {
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "local.db")
	owner := p080Owner(t)
	runtime := &p080Runtime{}
	socketDir := p080SocketDir(t)

	db, authority := p080Open(t, databasePath)
	api, apiClient, apiErr := p080StartAPI(t, authority, owner, filepath.Join(socketDir, "api.sock"))
	requestBody := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"printf 'p080 exact\\n'"}`
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p080-restart")
	response, err := apiClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		JobID    string `json:"job_id"`
		IntentID string `json:"intent_id"`
	}
	responseBody := p080ReadResponse(t, response, &accepted)
	if response.StatusCode != http.StatusAccepted || accepted.JobID == "" || accepted.IntentID == "" {
		t.Fatalf("API acceptance status=%d body=%s accepted=%+v", response.StatusCode, responseBody, accepted)
	}

	// The API receipt is durable before Router delivery. Stop that process and
	// reopen the same SQLite WAL before locald/executor dispatch starts.
	if err := api.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-apiErr; err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, authority = p080Open(t, databasePath)
	service := p080Service(t, authority, runtime, owner)
	locald := p080StartLocald(t, authority, service, owner, filepath.Join(socketDir, "locald.sock"))
	localdClient, err := dispatcher.NewLocaldClient(locald.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dispatcher.NewLocalDriver(authority, localdClient, "router-p080", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	intent, acceptance, err := driver.DispatchNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if acceptance.IntentID != accepted.IntentID || acceptance.JobPhase != string(store.JobPhaseCreatingSession) || acceptance.AcceptanceScope != "target_authority" {
		t.Fatalf("target acceptance = %+v", acceptance)
	}
	if intent.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("local intent after Router delivery = %q", intent.DeliveryState)
	}

	storedIntent, err := authority.GetLocalIntent(context.Background(), intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := domain.NewJobID(accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	job := p080WaitJobPhase(t, authority, jobID, store.JobPhaseComplete)
	command, err := authority.GetCommand(context.Background(), job.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	const exactScript = "printf 'p080 exact\\n'"
	if string(storedIntent.ScriptBytes) != exactScript || string(job.ScriptBytes) != exactScript || string(command.ScriptBytes) != exactScript {
		t.Fatalf("script bytes changed across barriers: intent=%q job=%q command=%q", storedIntent.ScriptBytes, job.ScriptBytes, command.ScriptBytes)
	}
	if runtime.CallCount() != 1 || runtime.Script(0) != exactScript {
		t.Fatalf("runtime execution = calls=%d script=%q", runtime.CallCount(), runtime.Script(0))
	}

	// Restart locald/executor after the job is complete and replay the same
	// identity-only request. The durable job checkpoint makes this a duplicate,
	// so the executor must not run the script a second time.
	closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	closeErr := locald.Close(closeContext)
	cancelClose()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, authority = p080Open(t, databasePath)
	service = p080Service(t, authority, runtime, owner)
	locald = p080StartLocald(t, authority, service, owner, filepath.Join(socketDir, "locald-restarted.sock"))
	defer func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		closeErr := locald.Close(closeContext)
		cancelClose()
		if closeErr != nil {
			t.Error(closeErr)
		}
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	replayClient, err := dispatcher.NewLocaldClient(locald.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := replayClient.AcceptIntent(context.Background(), dispatcher.AcceptIntentRequest{IntentID: intent.IntentID, RequestHash: intent.RequestHash})
	if err != nil {
		t.Fatal(err)
	}
	if replay.JobPhase != string(store.JobPhaseComplete) || replay.IntentID != string(intent.IntentID) {
		t.Fatalf("replayed acceptance = %+v", replay)
	}
	if runtime.CallCount() != 1 {
		t.Fatalf("replay executed script again: calls=%d", runtime.CallCount())
	}
}

func TestP080CorruptStoredScriptRejectsBeforeRouterOrExecutor(t *testing.T) {
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "local.db")
	owner := p080Owner(t)
	socketDir := p080SocketDir(t)
	db, authority := p080Open(t, databasePath)
	api, apiClient, apiErr := p080StartAPI(t, authority, owner, filepath.Join(socketDir, "api.sock"))
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"printf 'p080 exact\\n'"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p080-corrupt")
	response, err := apiClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		IntentID string `json:"intent_id"`
	}
	p080ReadResponse(t, response, &accepted)
	if response.StatusCode != http.StatusAccepted || accepted.IntentID == "" {
		t.Fatalf("corruption fixture API status=%d intent=%q", response.StatusCode, accepted.IntentID)
	}
	if err := api.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-apiErr; err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE local_intents SET script_bytes = ? WHERE intent_id = ?`, []byte("different bytes"), accepted.IntentID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, authority = p080Open(t, databasePath)
	defer db.Close()
	if _, err := authority.GetLocalIntent(context.Background(), domain.IntentID(accepted.IntentID)); !errors.Is(err, store.ErrLocalIntentPayloadCorrupt) {
		t.Fatalf("corrupt intent read = %v, want ErrLocalIntentPayloadCorrupt", err)
	}
	tripwire := &p080TripwireAcceptor{}
	driver, err := dispatcher.NewLocalDriver(authority, tripwire, "router-p080-corrupt", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := driver.DispatchNext(context.Background()); !errors.Is(err, store.ErrLocalIntentPayloadCorrupt) {
		t.Fatalf("corrupt dispatch = %v, want ErrLocalIntentPayloadCorrupt", err)
	}
	if tripwire.calls != 0 {
		t.Fatalf("Router called target after corrupt payload: calls=%d", tripwire.calls)
	}
}

type p080Runtime struct {
	mu      sync.Mutex
	scripts []string
}

func (*p080Runtime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: "p080-generation"}, nil
}

func (*p080Runtime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: "p080-generation"}, nil
}

func (*p080Runtime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (r *p080Runtime) ExecuteCommand(_ context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.mu.Lock()
	r.scripts = append(r.scripts, string(request.Command.ScriptBytes))
	r.mu.Unlock()
	return execution.RuntimeCommandResult{Stdout: []byte("p080 output\n"), ExitCode: 0}, nil
}

func (*p080Runtime) CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	return execution.RuntimeCommandStopResult{Confirmed: true}, nil
}

func (*p080Runtime) StopSession(context.Context, store.SessionRecord) (bool, error) { return true, nil }

func (r *p080Runtime) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.scripts)
}

func (r *p080Runtime) Script(index int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.scripts) {
		return ""
	}
	return r.scripts[index]
}

type p080TripwireAcceptor struct{ calls int }

func (a *p080TripwireAcceptor) AcceptIntent(context.Context, dispatcher.AcceptIntentRequest) (dispatcher.IntentAcceptance, error) {
	a.calls++
	return dispatcher.IntentAcceptance{}, fmt.Errorf("tripwire target must not be called")
}

func p080Open(t *testing.T, path string) (*sql.DB, *store.AuthorityStore) {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db, authority
}

func p080Owner(t *testing.T) domain.ControllerIdentity {
	t.Helper()
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func p080Service(t *testing.T, authority *store.AuthorityStore, runtime execution.SessionRuntime, owner domain.ControllerIdentity) *execution.Service {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk", AllowedTargets: []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty}, AllowedControllers: []domain.ControllerIdentity{owner}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func p080StartAPI(t *testing.T, authority *store.AuthorityStore, owner domain.ControllerIdentity, socketPath string) (*Server, *http.Client, <-chan error) {
	t.Helper()
	p080PrivateSocketParent(t, socketPath)
	server, err := NewServer(ServerOptions{Authority: authority, Owner: owner, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}}
	return server, client, serveErr
}

type p080LocaldHarness struct {
	server *runnerlocald.PrivateServer
	worker *queueworker.Worker
	serve  <-chan error

	closeOnce sync.Once
	closeErr  error
}

func (h *p080LocaldHarness) SocketPath() string {
	if h == nil || h.server == nil {
		return ""
	}
	return h.server.SocketPath()
}

func (h *p080LocaldHarness) Close(ctx context.Context) error {
	if h == nil || h.server == nil || h.worker == nil {
		return errors.New("P080 locald harness is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.closeOnce.Do(func() {
		var result []error
		if err := h.server.StopAccepting(); err != nil {
			result = append(result, err)
		}
		// This is the same claim barrier ordering as runner-locald.Run: stop
		// the shared gate before the wake loop, then drain already admitted work.
		h.server.StopDispatch()
		h.worker.Stop()
		if err := h.worker.Wait(ctx); err != nil {
			result = append(result, err)
		}
		if err := h.server.Drain(ctx); err != nil {
			result = append(result, err)
		}
		if err := h.server.Flush(ctx); err != nil {
			result = append(result, err)
		}
		if err := h.server.CloseStreams(); err != nil {
			result = append(result, err)
		}
		if err := <-h.serve; err != nil {
			result = append(result, err)
		}
		h.closeErr = errors.Join(result...)
	})
	return h.closeErr
}

func p080StartLocald(t *testing.T, authority *store.AuthorityStore, service *execution.Service, owner domain.ControllerIdentity, socketPath string) *p080LocaldHarness {
	t.Helper()
	p080PrivateSocketParent(t, socketPath)
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile temporary locald startup: %v", err)
	}
	gate := lifecycle.NewGate()
	worker, err := queueworker.New(queueworker.Options{Service: service, Authority: authority, DispatchGate: gate})
	if err != nil {
		t.Fatal(err)
	}
	server, err := runnerlocald.NewPrivateServer(runnerlocald.PrivateServerOptions{
		Authority: authority, Service: service, Owner: owner, SocketPath: socketPath,
		DispatchGate: gate, QueueWake: worker.Wake,
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
	harness := &p080LocaldHarness{server: server, worker: worker, serve: serveErr}
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := harness.Close(closeContext); err != nil {
			t.Errorf("close temporary P080 locald harness: %v", err)
		}
	})
	return harness
}

func p080WaitJobPhase(t *testing.T, authority *store.AuthorityStore, jobID domain.JobID, phase store.JobPhase) store.JobRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := authority.GetJob(context.Background(), jobID)
		if err == nil && job.Phase == phase {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s phase %s", jobID, phase)
	return store.JobRecord{}
}

func p080PrivateSocketParent(t *testing.T, socketPath string) {
	t.Helper()
	parent := filepath.Dir(socketPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
}

func p080SocketDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "rsr-p080-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func p080ReadResponse(t *testing.T, response *http.Response, target any) string {
	t.Helper()
	defer response.Body.Close()
	data, err := ioReadAll(response)
	if err != nil {
		t.Fatal(err)
	}
	if target != nil && len(data) > 0 {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
	return string(data)
}

func ioReadAll(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	return io.ReadAll(response.Body)
}
