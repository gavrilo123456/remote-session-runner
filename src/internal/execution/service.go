package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	// ErrExecutionServiceConfiguration means a required shared-core seam was
	// not supplied when constructing the service.
	ErrExecutionServiceConfiguration = errors.New("execution service configuration is incomplete")
	// ErrEnvironmentUnavailable means the named environment could not be
	// resolved before authoritative session acceptance.
	ErrEnvironmentUnavailable = errors.New("environment unavailable")
	// ErrRuntimeUnavailable means the selected runtime could not prepare or
	// start a session.
	ErrRuntimeUnavailable = errors.New("runtime unavailable")
	// ErrRuntimeHandshake means a runtime did not return the generation needed
	// to identify the started agent safely across restarts.
	ErrRuntimeHandshake = errors.New("runtime handshake is invalid")
	// ErrSessionController means a caller tried to read a session it does not
	// control in the authority's controller namespace.
	ErrSessionController = errors.New("session controller mismatch")
)

// Clock is the small wall-clock seam shared by service orchestration and fake
// runtime tests. The store has its own injected clock for transaction times.
type Clock interface {
	Now() time.Time
}

// RealClock uses the process wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now().UTC() }

// EnvironmentResolver resolves immutable policy definitions by name. It must
// not inspect a filesystem or start a runtime while resolving a policy.
type EnvironmentResolver interface {
	ResolveEnvironment(context.Context, string) (domain.Environment, error)
}

// EnvironmentRegistry is a deterministic in-memory resolver useful for the
// PoC service and hermetic tests. The map is copied at construction.
type EnvironmentRegistry struct {
	mu           sync.RWMutex
	environments map[string]domain.Environment
}

// NewEnvironmentRegistry constructs a resolver from immutable environments.
func NewEnvironmentRegistry(environments ...domain.Environment) (*EnvironmentRegistry, error) {
	registry := &EnvironmentRegistry{environments: make(map[string]domain.Environment, len(environments))}
	for _, environment := range environments {
		if strings.TrimSpace(environment.Name()) == "" {
			return nil, fmt.Errorf("%w: environment name is empty", ErrEnvironmentUnavailable)
		}
		if _, exists := registry.environments[environment.Name()]; exists {
			return nil, fmt.Errorf("%w: duplicate environment %q", domain.ErrInvalidEnvironment, environment.Name())
		}
		registry.environments[environment.Name()] = environment
	}
	return registry, nil
}

// ResolveEnvironment implements EnvironmentResolver.
func (r *EnvironmentRegistry) ResolveEnvironment(_ context.Context, name string) (domain.Environment, error) {
	if r == nil {
		return domain.Environment{}, ErrEnvironmentUnavailable
	}
	r.mu.RLock()
	environment, ok := r.environments[name]
	r.mu.RUnlock()
	if !ok {
		return domain.Environment{}, fmt.Errorf("%w: %q", ErrEnvironmentUnavailable, name)
	}
	return environment, nil
}

// RuntimePrepareRequest contains the immutable request and the authoritative
// creating record. Prepare may create a workspace, but must not report ready
// until StartAgent has completed its generation handshake.
type RuntimePrepareRequest struct {
	Session store.SessionRecord
}

// RuntimePrepared is the output of source preparation. A generation is
// optional until StartAgent returns its handshake; a resolved revision is
// recorded when a git source is prepared.
type RuntimePrepared struct {
	RuntimeGeneration string
	ResolvedRevision  string
}

// RuntimeStartRequest identifies the prepared session that should start its
// persistent agent and Bash process.
type RuntimeStartRequest struct {
	Session           store.SessionRecord
	Prepared          RuntimePrepared
	RuntimeGeneration string
}

// RuntimeStarted is the successful agent handshake.
type RuntimeStarted struct {
	RuntimeGeneration string
}

// RuntimeCleanupRequest identifies a partially-created runtime. Cleanup is a
// confirmed boundary only when it returns nil.
type RuntimeCleanupRequest struct {
	Session           store.SessionRecord
	Prepared          RuntimePrepared
	RuntimeGeneration string
}

// SessionRuntime is the shared execution boundary used by Mac and Linux
// service instances. P020 uses it with a fake adapter; real adapters arrive
// in the later runtime phases.
type SessionRuntime interface {
	Prepare(context.Context, RuntimePrepareRequest) (RuntimePrepared, error)
	StartAgent(context.Context, RuntimeStartRequest) (RuntimeStarted, error)
	Cleanup(context.Context, RuntimeCleanupRequest) error
}

// EventPublisher receives committed session lifecycle records. Publication
// occurs after the store transaction; a publisher failure cannot roll back an
// authoritative state transition.
type EventPublisher interface {
	PublishSessionLifecycle(context.Context, store.SessionLifecycleRecord)
}

// CreateSessionRequest is the shared service input for authoritative session
// creation. The caller supplies stable IDs and a canonical mutation hash so a
// retry can be reconciled without allocating another session.
type CreateSessionRequest struct {
	SessionID            domain.SessionID
	IdempotencyKey       string
	RequestHash          domain.CanonicalHash
	Environment          string
	Target               domain.ExecutionTarget
	Controller           domain.ControllerIdentity
	Source               domain.Source
	RequestedLimits      domain.RequestedLimits
	Isolation            domain.IsolationRequirements
	MaxActiveSessions    int
	IdempotencyRetention time.Duration
}

// CreateSessionResult contains the authoritative snapshot and whether the
// request reused a retained idempotency record.
type CreateSessionResult struct {
	Session   store.SessionRecord
	Duplicate bool
}

// Service is the shared execution orchestration core. It performs policy
// validation before the store's creating transaction and never starts a
// runtime before that transaction commits.
type Service struct {
	store     *store.AuthorityStore
	runtime   SessionRuntime
	resolver  EnvironmentResolver
	clock     Clock
	publisher EventPublisher
}

// ExecutionService is the design-level name for Service.
type ExecutionService = Service

// NewService constructs the shared execution service.
func NewService(authority *store.AuthorityStore, runtime SessionRuntime, resolver EnvironmentResolver, clock Clock, publisher EventPublisher) (*Service, error) {
	if authority == nil || runtime == nil || resolver == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Service{store: authority, runtime: runtime, resolver: resolver, clock: clock, publisher: publisher}, nil
}

// NewExecutionService is an explicit constructor alias matching the design.
func NewExecutionService(authority *store.AuthorityStore, runtime SessionRuntime, resolver EnvironmentResolver, clock Clock, publisher EventPublisher) (*ExecutionService, error) {
	return NewService(authority, runtime, resolver, clock, publisher)
}

// CreateSession validates policy, accepts a creating session, and drives the
// fake/target runtime through prepare and ready. Runtime failures become a
// durable failed or lost session and return the resulting snapshot with an
// error describing the runtime outcome. Duplicate idempotent requests return
// the stored snapshot without invoking the runtime again.
func (s *Service) CreateSession(ctx context.Context, request CreateSessionRequest) (CreateSessionResult, error) {
	if s == nil || s.store == nil || s.runtime == nil || s.resolver == nil {
		return CreateSessionResult{}, ErrExecutionServiceConfiguration
	}
	environment, err := s.resolver.ResolveEnvironment(ctx, request.Environment)
	if err != nil {
		return CreateSessionResult{}, fmt.Errorf("%w: %v", ErrEnvironmentUnavailable, err)
	}
	source := request.Source
	if source.Mode() == "" {
		source = domain.NewEmptySource()
	}
	limits, err := environment.ValidateSessionPolicy(domain.SessionPolicyRequest{
		Target:     request.Target,
		Source:     source,
		Controller: request.Controller,
		Limits:     request.RequestedLimits,
		Isolation:  request.Isolation,
	})
	if err != nil {
		return CreateSessionResult{}, err
	}
	accepted, duplicate, err := s.store.AcceptSessionCreate(ctx, store.SessionCreateAcceptance{
		SessionCreate: store.SessionCreate{
			SessionID:   request.SessionID,
			Target:      request.Target,
			Environment: environment.Name(),
			Controller:  request.Controller,
			Source:      source,
			Limits:      limits,
			Reason:      "session_created",
		},
		IdempotencyKey:       request.IdempotencyKey,
		RequestHash:          request.RequestHash,
		MaxActiveSessions:    request.MaxActiveSessions,
		IdempotencyRetention: request.IdempotencyRetention,
	})
	if err != nil {
		return CreateSessionResult{}, err
	}
	result := CreateSessionResult{Session: accepted, Duplicate: duplicate}
	if duplicate {
		return result, nil
	}
	// The creating acceptance is already committed. Publish it before any
	// runtime call so observers never see a ready/failure transition without
	// its durable predecessor.
	s.publishLatestLifecycle(ctx, accepted.SessionID)

	prepared, prepareErr := s.runtime.Prepare(ctx, RuntimePrepareRequest{Session: accepted})
	if prepareErr != nil {
		return s.finishRuntimeFailure(ctx, result, RuntimePrepared{}, prepareErr)
	}
	started, startErr := s.runtime.StartAgent(ctx, RuntimeStartRequest{
		Session:           accepted,
		Prepared:          prepared,
		RuntimeGeneration: prepared.RuntimeGeneration,
	})
	if startErr != nil {
		return s.finishRuntimeFailure(ctx, result, prepared, startErr)
	}
	generation := started.RuntimeGeneration
	if generation == "" {
		generation = prepared.RuntimeGeneration
	}
	if generation == "" {
		return s.finishRuntimeFailure(ctx, result, prepared, ErrRuntimeHandshake)
	}
	ready, err := s.store.CompleteSessionCreation(ctx, accepted.SessionID, domain.SessionStateReady, generation, prepared.ResolvedRevision, "runtime_ready")
	if err != nil {
		return CreateSessionResult{}, err
	}
	result.Session = ready
	s.publishLatestLifecycle(ctx, ready.SessionID)
	return result, nil
}

func (s *Service) finishRuntimeFailure(ctx context.Context, result CreateSessionResult, prepared RuntimePrepared, cause error) (CreateSessionResult, error) {
	cleanupErr := s.runtime.Cleanup(ctx, RuntimeCleanupRequest{
		Session:           result.Session,
		Prepared:          prepared,
		RuntimeGeneration: prepared.RuntimeGeneration,
	})
	next := domain.SessionStateFailed
	reason := "runtime_failed"
	if cleanupErr != nil {
		next = domain.SessionStateLost
		reason = "runtime_cleanup_unconfirmed"
	}
	completed, transitionErr := s.store.CompleteSessionCreation(ctx, result.Session.SessionID, next, prepared.RuntimeGeneration, prepared.ResolvedRevision, reason)
	if transitionErr != nil {
		return CreateSessionResult{}, fmt.Errorf("%w: record %s state: %v", ErrRuntimeUnavailable, next, transitionErr)
	}
	result.Session = completed
	s.publishLatestLifecycle(ctx, completed.SessionID)
	if cleanupErr != nil {
		return result, fmt.Errorf("%w: %v; cleanup: %v", ErrRuntimeUnavailable, cause, cleanupErr)
	}
	return result, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, cause)
}

// GetSession returns an as-of authoritative snapshot after checking the
// immutable controller namespace.
func (s *Service) GetSession(ctx context.Context, id domain.SessionID, controller domain.ControllerIdentity) (store.SessionRecord, error) {
	if s == nil || s.store == nil {
		return store.SessionRecord{}, ErrExecutionServiceConfiguration
	}
	record, err := s.store.GetSession(ctx, id)
	if err != nil {
		return store.SessionRecord{}, err
	}
	if record.Controller.Type() != controller.Type() || record.Controller.ID() != controller.ID() {
		return store.SessionRecord{}, ErrSessionController
	}
	return record, nil
}

func (s *Service) publishLatestLifecycle(ctx context.Context, id domain.SessionID) {
	if s.publisher == nil {
		return
	}
	lifecycle, err := s.store.ListSessionLifecycle(ctx, id)
	if err != nil || len(lifecycle) == 0 {
		return
	}
	s.publisher.PublishSessionLifecycle(ctx, lifecycle[len(lifecycle)-1])
}
