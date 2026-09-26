package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p020Clock struct{ now time.Time }

func (c *p020Clock) Now() time.Time { return c.now }

type p020FakeRuntime struct {
	prepareErr    error
	startErr      error
	cleanupErr    error
	commandErr    error
	commandResult RuntimeCommandResult
	generation    string
	prepareCall   int
	startCall     int
	cleanupCall   int
	commandCall   int
	prepareSaw    domain.SessionState
}

func (r *p020FakeRuntime) Prepare(ctx context.Context, request RuntimePrepareRequest) (RuntimePrepared, error) {
	r.prepareCall++
	if record, err := requestSessionForP020(ctx, request.Session); err == nil {
		r.prepareSaw = record.State
	}
	if r.prepareErr != nil {
		return RuntimePrepared{}, r.prepareErr
	}
	return RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *p020FakeRuntime) StartAgent(context.Context, RuntimeStartRequest) (RuntimeStarted, error) {
	r.startCall++
	if r.startErr != nil {
		return RuntimeStarted{}, r.startErr
	}
	return RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (r *p020FakeRuntime) Cleanup(context.Context, RuntimeCleanupRequest) error {
	r.cleanupCall++
	return r.cleanupErr
}

func (r *p020FakeRuntime) ExecuteCommand(context.Context, RuntimeCommandRequest) (RuntimeCommandResult, error) {
	r.commandCall++
	return r.commandResult, r.commandErr
}

func requestSessionForP020(_ context.Context, record store.SessionRecord) (store.SessionRecord, error) {
	return record, nil
}

type p020Publisher struct {
	records []store.SessionLifecycleRecord
}

func (p *p020Publisher) PublishSessionLifecycle(_ context.Context, record store.SessionLifecycleRecord) {
	p.records = append(p.records, record)
}

func TestP020D02PolicyRejectsBeforeAuthoritativeAcceptance(t *testing.T) {
	service, authority, runtime := newP020Service(t, &p020FakeRuntime{generation: "gen-policy"})
	target := p020Target(t, domain.TargetKindRemote, "linux-host")
	request := p020Request(t, "session-policy", "key-policy", target)
	if _, err := service.CreateSession(context.Background(), request); !errors.Is(err, domain.ErrEnvironmentTargetMismatch) {
		t.Fatalf("policy error = %v, want target mismatch", err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live reservations = %d, err = %v, want 0", got, err)
	}
	if runtime.prepareCall != 0 || runtime.startCall != 0 || runtime.cleanupCall != 0 {
		t.Fatalf("runtime calls after policy rejection = prepare %d start %d cleanup %d", runtime.prepareCall, runtime.startCall, runtime.cleanupCall)
	}
}

func TestP020D02UnsupportedIsolationRejectsBeforeAcceptance(t *testing.T) {
	service, authority, _ := newP020Service(t, &p020FakeRuntime{generation: "gen-isolation"})
	request := p020Request(t, "session-isolation", "key-isolation", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	request.Isolation.FilesystemBoundary = true
	if _, err := service.CreateSession(context.Background(), request); !errors.Is(err, domain.ErrUnsupportedIsolationRequirement) {
		t.Fatalf("isolation error = %v, want unsupported isolation", err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live reservations = %d, err = %v, want 0", got, err)
	}
}

func TestP020I01CreateReadyAndReadUseSharedService(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-ready"}
	service, authority, _ := newP020Service(t, runtime)
	request := p020Request(t, "session-ready", "key-ready", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	result, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Session.State != domain.SessionStateReady || result.Session.RuntimeGeneration != "generation-ready" {
		t.Fatalf("create result = %+v duplicate=%v", result.Session, result.Duplicate)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.cleanupCall != 0 || runtime.prepareSaw != domain.SessionStateCreating {
		t.Fatalf("runtime calls = prepare %d start %d cleanup %d saw %q", runtime.prepareCall, runtime.startCall, runtime.cleanupCall, runtime.prepareSaw)
	}
	read, err := service.GetSession(context.Background(), request.SessionID, request.Controller)
	if err != nil {
		t.Fatal(err)
	}
	if read.State != domain.SessionStateReady || read.Target.Kind() != domain.TargetKindLocal {
		t.Fatalf("read session = %+v", read)
	}
	if _, err := service.GetSession(context.Background(), request.SessionID, p020Controller(t, domain.ControllerTypeDirectMTLS)); !errors.Is(err, ErrSessionController) {
		t.Fatalf("wrong controller read error = %v, want %v", err, ErrSessionController)
	}
	lifecycle, err := authority.ListSessionLifecycle(context.Background(), request.SessionID)
	if err != nil || len(lifecycle) != 2 || lifecycle[1].NewState != domain.SessionStateReady {
		t.Fatalf("lifecycle = %+v, err = %v", lifecycle, err)
	}
}

func TestP020D11PrepareFailureCommitsFailedAfterCleanup(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-prepare", prepareErr: errors.New("fixture prepare failed")}
	service, authority, _ := newP020Service(t, runtime)
	request := p020Request(t, "session-prepare-fail", "key-prepare-fail", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	result, err := service.CreateSession(context.Background(), request)
	if !errors.Is(err, ErrRuntimeUnavailable) || result.Session.State != domain.SessionStateFailed {
		t.Fatalf("result=%+v err=%v, want failed runtime error", result.Session, err)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 0 || runtime.cleanupCall != 1 {
		t.Fatalf("runtime calls = prepare %d start %d cleanup %d", runtime.prepareCall, runtime.startCall, runtime.cleanupCall)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("failed session reservation = %d, err = %v, want retained 1", got, err)
	}
}

func TestP020D11StartFailureCleanupFailureCommitsLost(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-start", startErr: errors.New("fixture start failed"), cleanupErr: errors.New("fixture cleanup uncertain")}
	service, _, _ := newP020Service(t, runtime)
	request := p020Request(t, "session-lost", "key-lost", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	result, err := service.CreateSession(context.Background(), request)
	if !errors.Is(err, ErrRuntimeUnavailable) || result.Session.State != domain.SessionStateLost {
		t.Fatalf("result=%+v err=%v, want lost runtime error", result.Session, err)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.cleanupCall != 1 {
		t.Fatalf("runtime calls = prepare %d start %d cleanup %d", runtime.prepareCall, runtime.startCall, runtime.cleanupCall)
	}
}

func TestP020CreateRetryDoesNotStartRuntimeTwice(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-idempotent"}
	service, _, _ := newP020Service(t, runtime)
	request := p020Request(t, "session-idempotent", "key-idempotent", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	first, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Session.SessionID != first.Session.SessionID || second.Session.State != domain.SessionStateReady {
		t.Fatalf("retry = %+v duplicate=%v", second.Session, second.Duplicate)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 {
		t.Fatalf("runtime calls after retry = prepare %d start %d", runtime.prepareCall, runtime.startCall)
	}
}

func newP020Service(t *testing.T, runtime *p020FakeRuntime) (*Service, *store.AuthorityStore, *p020FakeRuntime) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir()+"/state/p020.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := &p020Clock{now: time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(db, now.Now)
	if err != nil {
		t.Fatal(err)
	}
	mac := p020Environment(t)
	registry, err := NewEnvironmentRegistry(mac)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, now, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	return service, authority, runtime
}

func p020Environment(t *testing.T) domain.Environment {
	t.Helper()
	target := p020Target(t, domain.TargetKindLocal, "mac-workstation")
	controller := p020Controller(t, domain.ControllerTypeLocalUser)
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name:               "mac-dev",
		HostClass:          "macOS workstation",
		EffectiveAccount:   "tomasz.walczuk",
		AllowedTargets:     []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty, domain.SourceModeLocalWorktree},
		AllowedControllers: []domain.ControllerIdentity{controller},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func p020Target(t *testing.T, kind domain.TargetKind, profile string) domain.ExecutionTarget {
	t.Helper()
	target, err := domain.NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func p020Controller(t *testing.T, kind domain.ControllerType) domain.ControllerIdentity {
	t.Helper()
	controller, err := domain.NewControllerIdentity(kind, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func p020Request(t *testing.T, sessionID, key string, target domain.ExecutionTarget) CreateSessionRequest {
	t.Helper()
	raw := []byte(fmt.Sprintf(`{"operation":"create_session","environment":"mac-dev","session_id":%q}`, sessionID))
	hash, err := domain.HashMutationRequestJSON("create_session", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return CreateSessionRequest{
		SessionID:      domain.SessionID(sessionID),
		IdempotencyKey: key,
		RequestHash:    hash,
		Environment:    "mac-dev",
		Target:         target,
		Controller:     p020Controller(t, domain.ControllerTypeLocalUser),
		Source:         domain.NewEmptySource(),
	}
}
