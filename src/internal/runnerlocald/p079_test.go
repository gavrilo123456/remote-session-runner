package runnerlocald

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type p079UnconfirmedRuntime struct {
	mu           sync.Mutex
	commandCalls int
	stopCalls    int
}

func (*p079UnconfirmedRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: "p079-generation"}, nil
}
func (*p079UnconfirmedRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: "p079-generation"}, nil
}
func (*p079UnconfirmedRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error {
	return nil
}
func (r *p079UnconfirmedRuntime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.mu.Lock()
	r.commandCalls++
	r.mu.Unlock()
	return execution.RuntimeCommandResult{Stdout: []byte("p079-output\n"), ExitCode: 0}, nil
}
func (*p079UnconfirmedRuntime) CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	return execution.RuntimeCommandStopResult{Confirmed: false}, nil
}
func (r *p079UnconfirmedRuntime) StopSession(context.Context, store.SessionRecord) (bool, error) {
	r.mu.Lock()
	r.stopCalls++
	r.mu.Unlock()
	return false, nil
}

func (r *p079UnconfirmedRuntime) callCounts() (commands, stops int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commandCalls, r.stopCalls
}

func TestP079LocaldReturnsDurableLostJobAfterTeardownFailure(t *testing.T) {
	authority, service, runtime := newP079Service(t)
	intent := p078LocalRunIntent(t, authority)
	server, _ := startBUG011P4PrivateServer(t, authority, service, p060SocketPath(t))
	client := p060UnixClient(server.SocketPath())
	body := []byte(`{"intent_id":"intent-p078-local","request_hash":"` + intent.RequestHash.String() + `"}`)
	first := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("teardown failure status = %d body=%s", first.StatusCode, p060ReadBody(t, first))
	}
	var accepted intentAcceptanceResponse
	p060DecodeJSON(t, first, &accepted)
	if accepted.JobPhase != string(store.JobPhaseCreatingSession) || accepted.SessionState != "" || accepted.CommandState != "" {
		t.Fatalf("teardown failure acceptance = %+v", accepted)
	}
	job := waitBUG011P4Job(t, authority, intent.JobID, store.JobPhaseLost)
	var err error
	if err != nil || job.Phase != store.JobPhaseLost || job.TeardownState != store.JobTeardownLost || job.CommandState == nil || *job.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("durable lost job = %+v err=%v", job, err)
	}
	second := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("lost job retry status = %d body=%s", second.StatusCode, p060ReadBody(t, second))
	}
	commands, stops := runtime.callCounts()
	if commands != 1 || stops != 1 {
		t.Fatalf("lost retry reran work: commands=%d stops=%d", commands, stops)
	}
}

func newP079Service(t *testing.T) (*store.AuthorityStore, *execution.Service, *p079UnconfirmedRuntime) {
	t.Helper()
	root := testfixture.New(t)
	db, err := store.Open(context.Background(), root.Path()+"/state/p079.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
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
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk", AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty}, AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits()})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &p079UnconfirmedRuntime{}
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return authority, service, runtime
}
