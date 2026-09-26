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
	// ErrSessionNotReady means a direct command was submitted before the
	// authoritative session reached ready or busy.
	ErrSessionNotReady = errors.New("session is not ready for commands")
	// ErrCommandNotReady means a command cannot accept the requested lifecycle
	// operation in its current state.
	ErrCommandNotReady = errors.New("command is not ready for this operation")
	// ErrCommandTransport means the command runtime could not report a command
	// outcome. The command is recorded as lost; this is not a shell exit code.
	ErrCommandTransport = errors.New("command transport failed")
	// ErrShellExited means the persistent shell crossed an unsafe boundary.
	// The command and session are recorded as lost rather than recreated.
	ErrShellExited = errors.New("persistent shell exited")
	// ErrStopUnconfirmed means cancellation or close could not prove that the
	// runtime stopped. The affected slot and/or session remains lost.
	ErrStopUnconfirmed = errors.New("runtime stop was not confirmed")
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

// RuntimeCommandRequest identifies one durably started command for the
// command-capable portion of a runtime adapter.
type RuntimeCommandRequest struct {
	Session store.SessionRecord
	Command store.CommandRecord
}

// RuntimeCommandResult is the fake/target runtime's captured result. Output
// is raw bytes and is persisted before terminal completion. ShellExited marks
// an unsafe persistent-shell boundary, distinct from a normal nonzero exit.
type RuntimeCommandResult struct {
	Stdout      []byte
	Stderr      []byte
	ExitCode    int
	ShellExited bool
}

// CommandRuntime is implemented by a command-capable SessionRuntime. It is a
// separate optional interface so P020's create-only fake remains valid while
// later adapters add command execution.
type CommandRuntime interface {
	ExecuteCommand(context.Context, RuntimeCommandRequest) (RuntimeCommandResult, error)
}

// RuntimeCommandStopResult reports the bounded stop boundary for cancel and
// close. Output is persisted before the command terminal transition.
type RuntimeCommandStopResult struct {
	Stdout    []byte
	Stderr    []byte
	Confirmed bool
}

// RuntimeCommandControl is optional in P021's fake runtime and is used by
// P022 to model cancellation without coupling the service to OS signals.
type RuntimeCommandControl interface {
	CancelCommand(context.Context, RuntimeCommandRequest) (RuntimeCommandStopResult, error)
	StopSession(context.Context, store.SessionRecord) (bool, error)
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

// SubmitCommandRequest is the shared service input for an authoritative
// command. The stable command ID, key, and canonical hash are reused on
// retries; the session target is inherited from the stored session.
type SubmitCommandRequest struct {
	CommandID            domain.CommandID
	SessionID            domain.SessionID
	Controller           domain.ControllerIdentity
	IdempotencyKey       string
	RequestHash          domain.CanonicalHash
	Script               string
	Timeout              time.Duration
	IntentOrdinal        int64
	IdempotencyRetention time.Duration
}

// SubmitCommandResult contains the authoritative command snapshot. A queued
// result can remain queued when another command/session owns the scheduler;
// no runtime call is made until this command is durably started.
type SubmitCommandResult struct {
	Command   store.CommandRecord
	Duplicate bool
}

// CancelCommandRequest identifies a keyed cancellation mutation.
type CancelCommandRequest struct {
	CommandID            domain.CommandID
	Controller           domain.ControllerIdentity
	IdempotencyKey       string
	RequestHash          domain.CanonicalHash
	IdempotencyRetention time.Duration
}

// CancelCommandResult contains the command snapshot after the cancellation
// request or its terminal race winner.
type CancelCommandResult struct {
	Command   store.CommandRecord
	Duplicate bool
}

// CloseSessionRequest identifies a keyed session close policy. The policy is
// represented in the canonical request hash, so changing it under one key is
// an idempotency conflict.
type CloseSessionRequest struct {
	SessionID            domain.SessionID
	Controller           domain.ControllerIdentity
	IdempotencyKey       string
	RequestHash          domain.CanonicalHash
	Policy               string
	IdempotencyRetention time.Duration
}

// CloseSessionResult contains the final or lost session snapshot.
type CloseSessionResult struct {
	Session   store.SessionRecord
	Duplicate bool
}

// Service is the shared execution orchestration core. It performs policy
// validation before the store's creating transaction and never starts a
// runtime before that transaction commits.
type Service struct {
	store      *store.AuthorityStore
	runtime    SessionRuntime
	resolver   EnvironmentResolver
	clock      Clock
	publisher  EventPublisher
	mutationMu sync.Mutex
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

// SubmitCommand accepts one script, starts it only after the durable scheduler
// transaction commits, persists raw stdout/stderr events, and records one
// terminal outcome. A normal nonzero exit is a failed command with a ready
// session; a transport error or shell exit is a lost command/session and is
// returned as a distinct service error.
func (s *Service) SubmitCommand(ctx context.Context, request SubmitCommandRequest) (SubmitCommandResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return SubmitCommandResult{}, ErrExecutionServiceConfiguration
	}
	session, err := s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return SubmitCommandResult{}, err
	}
	if session.Controller.Type() != request.Controller.Type() || session.Controller.ID() != request.Controller.ID() {
		return SubmitCommandResult{}, ErrSessionController
	}
	if session.State != domain.SessionStateReady && session.State != domain.SessionStateBusy {
		return SubmitCommandResult{}, fmt.Errorf("%w: current state %q", ErrSessionNotReady, session.State)
	}
	if err := domain.ValidateScriptUTF8(request.Script); err != nil {
		return SubmitCommandResult{}, err
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = session.Limits.CommandTimeout
	}
	if timeout <= 0 {
		return SubmitCommandResult{}, domain.ErrInvalidRequestedLimits
	}
	if timeout > session.Limits.CommandTimeout {
		return SubmitCommandResult{}, fmt.Errorf("%w: command timeout", domain.ErrLimitExceedsServiceCeiling)
	}
	accepted, duplicate, err := s.store.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID:            request.CommandID,
		SessionID:            request.SessionID,
		RequestHash:          request.RequestHash,
		IdempotencyKey:       request.IdempotencyKey,
		IdempotencyRetention: request.IdempotencyRetention,
		Script:               request.Script,
		Timeout:              timeout,
		IntentOrdinal:        request.IntentOrdinal,
	})
	if err != nil {
		return SubmitCommandResult{}, err
	}
	result := SubmitCommandResult{Command: accepted, Duplicate: duplicate}
	if duplicate {
		return result, nil
	}
	started, startErr := s.store.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if startErr != nil {
		if errors.Is(startErr, store.ErrCommandSlotsFull) || errors.Is(startErr, store.ErrCommandNotEligible) {
			return result, nil
		}
		return result, startErr
	}
	if started.CommandID != accepted.CommandID {
		return result, nil
	}
	commandRuntime, ok := s.runtime.(CommandRuntime)
	if !ok {
		return s.finishCommandFailure(ctx, session, started, ErrCommandTransport, "command_transport_failed")
	}
	currentSession, err := s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	runtimeResult, runtimeErr := commandRuntime.ExecuteCommand(ctx, RuntimeCommandRequest{Session: currentSession, Command: started})
	if runtimeErr != nil {
		return s.finishCommandFailure(ctx, currentSession, started, runtimeErr, "command_transport_failed")
	}
	for _, output := range []struct {
		eventType string
		payload   []byte
	}{
		{eventType: "stdout", payload: runtimeResult.Stdout},
		{eventType: "stderr", payload: runtimeResult.Stderr},
	} {
		if len(output.payload) == 0 {
			continue
		}
		if _, err := s.store.AppendCommandEvent(ctx, store.CommandEventAppend{
			CommandID: started.CommandID,
			Type:      output.eventType,
			Payload:   output.payload,
			ByteCount: int64(len(output.payload)),
		}); err != nil {
			return s.finishCommandFailure(ctx, currentSession, started, err, "command_output_persistence_failed")
		}
	}
	if runtimeResult.ShellExited {
		return s.finishCommandFailure(ctx, currentSession, started, ErrShellExited, "shell_exited")
	}
	nextState := domain.CommandStateSucceeded
	if runtimeResult.ExitCode != 0 {
		nextState = domain.CommandStateFailed
	}
	exitCode := runtimeResult.ExitCode
	completed, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID:      started.CommandID,
		NextState:      nextState,
		ExitCode:       &exitCode,
		OutputComplete: true,
	}, domain.SessionStateReady, "command_completed", true)
	if err != nil {
		return result, err
	}
	result.Command = completed
	return result, nil
}

func (s *Service) finishCommandFailure(ctx context.Context, session store.SessionRecord, command store.CommandRecord, cause error, reason string) (SubmitCommandResult, error) {
	completed, transitionErr := s.store.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID:      command.CommandID,
		NextState:      domain.CommandStateLost,
		OutputComplete: false,
	}, domain.SessionStateLost, reason, false)
	if transitionErr != nil {
		return SubmitCommandResult{Command: command}, fmt.Errorf("%w: record lost command: %v", ErrCommandTransport, transitionErr)
	}
	_ = session
	if errors.Is(cause, ErrShellExited) {
		return SubmitCommandResult{Command: completed}, fmt.Errorf("%w: %v", ErrShellExited, cause)
	}
	return SubmitCommandResult{Command: completed}, fmt.Errorf("%w: %v", ErrCommandTransport, cause)
}

// CancelCommand records a keyed cancellation request and applies it to queued
// or running work. A queued command is terminal without a runtime call; a
// running command enters cancelling first and only becomes cancelled after a
// confirmed stop/output boundary. Otherwise it becomes lost and retains its
// durable slot.
func (s *Service) CancelCommand(ctx context.Context, request CancelCommandRequest) (CancelCommandResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return CancelCommandResult{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	command, err := s.store.GetCommand(ctx, request.CommandID)
	if err != nil {
		return CancelCommandResult{}, err
	}
	session, err := s.store.GetSession(ctx, command.SessionID)
	if err != nil {
		return CancelCommandResult{}, err
	}
	if session.Controller.Type() != request.Controller.Type() || session.Controller.ID() != request.Controller.ID() {
		return CancelCommandResult{}, ErrSessionController
	}
	_, duplicate, err := s.store.EnsureIdempotency(ctx, request.Controller, "cancel_command", request.IdempotencyKey, request.RequestHash, string(command.CommandID), request.IdempotencyRetention)
	if err != nil {
		return CancelCommandResult{}, err
	}
	if duplicate || command.State.IsTerminal() {
		return CancelCommandResult{Command: command, Duplicate: duplicate}, nil
	}
	if command.State == domain.CommandStateQueued {
		cancelled, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true})
		if err != nil {
			return CancelCommandResult{}, err
		}
		return CancelCommandResult{Command: cancelled}, nil
	}
	if command.State != domain.CommandStateRunning && command.State != domain.CommandStateCancelling {
		return CancelCommandResult{}, fmt.Errorf("%w: current command state %q", ErrCommandNotReady, command.State)
	}
	if command.State == domain.CommandStateRunning {
		if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelling}); err != nil {
			return CancelCommandResult{}, err
		}
		command.State = domain.CommandStateCancelling
	}
	control, ok := s.runtime.(RuntimeCommandControl)
	if !ok {
		return s.finishCancelledCommand(ctx, session, command, RuntimeCommandStopResult{}, ErrStopUnconfirmed)
	}
	stopped, stopErr := control.CancelCommand(ctx, RuntimeCommandRequest{Session: session, Command: command})
	if stopErr != nil {
		return s.finishCancelledCommand(ctx, session, command, stopped, stopErr)
	}
	return s.finishCancelledCommand(ctx, session, command, stopped, nil)
}

func (s *Service) finishCancelledCommand(ctx context.Context, session store.SessionRecord, command store.CommandRecord, stopped RuntimeCommandStopResult, stopErr error) (CancelCommandResult, error) {
	if err := s.appendStopOutput(ctx, command.CommandID, stopped); err != nil {
		stopErr = err
		stopped.Confirmed = false
	}
	next := domain.CommandStateCancelled
	nextSession := domain.SessionStateReady
	reason := "command_cancelled"
	release := true
	resultErr := stopErr
	if stopErr != nil || !stopped.Confirmed {
		next = domain.CommandStateLost
		nextSession = domain.SessionStateLost
		reason = "command_stop_unconfirmed"
		release = false
		if resultErr == nil {
			resultErr = ErrStopUnconfirmed
		}
	}
	completed, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: next, OutputComplete: stopped.Confirmed}, nextSession, reason, release)
	if err != nil {
		return CancelCommandResult{Command: command}, err
	}
	result := CancelCommandResult{Command: completed}
	if resultErr != nil {
		return result, fmt.Errorf("%w: %v", ErrStopUnconfirmed, resultErr)
	}
	return result, nil
}

// CloseSession blocks new dispatch, terminally cancels queued commands, asks
// the runtime to stop active work, and closes only after cleanup is confirmed.
// An unconfirmed stop transitions the session to lost and preserves live
// command slots.
func (s *Service) CloseSession(ctx context.Context, request CloseSessionRequest) (CloseSessionResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return CloseSessionResult{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	session, err := s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return CloseSessionResult{}, err
	}
	if session.Controller.Type() != request.Controller.Type() || session.Controller.ID() != request.Controller.ID() {
		return CloseSessionResult{}, ErrSessionController
	}
	_, duplicate, err := s.store.EnsureIdempotency(ctx, request.Controller, "close_session", request.IdempotencyKey, request.RequestHash, string(session.SessionID), request.IdempotencyRetention)
	if err != nil {
		return CloseSessionResult{}, err
	}
	if duplicate || session.State.IsTerminal() {
		return CloseSessionResult{Session: session, Duplicate: duplicate}, nil
	}
	commands, err := s.store.ListSessionCommands(ctx, session.SessionID)
	if err != nil {
		return CloseSessionResult{}, err
	}
	if _, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateClosing, "close_requested"); err != nil {
		return CloseSessionResult{}, err
	}
	closing, err := s.store.GetSession(ctx, session.SessionID)
	if err != nil {
		return CloseSessionResult{}, err
	}
	control, hasControl := s.runtime.(RuntimeCommandControl)
	for _, command := range commands {
		switch command.State {
		case domain.CommandStateQueued:
			if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true}); err != nil {
				return CloseSessionResult{}, err
			}
		case domain.CommandStateRunning, domain.CommandStateCancelling:
			if command.State == domain.CommandStateRunning {
				if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelling}); err != nil {
					return CloseSessionResult{}, err
				}
				command.State = domain.CommandStateCancelling
			}
			if !hasControl {
				_, _ = s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateClosing, "close_stop_unconfirmed", false)
				return s.markClosingLost(ctx, closing, ErrStopUnconfirmed)
			}
			stopped, stopErr := control.CancelCommand(ctx, RuntimeCommandRequest{Session: closing, Command: command})
			if err := s.appendStopOutput(ctx, command.CommandID, stopped); err != nil {
				stopErr = err
				stopped.Confirmed = false
			}
			if stopErr != nil || !stopped.Confirmed {
				_, _ = s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateClosing, "close_stop_unconfirmed", false)
				return s.markClosingLost(ctx, closing, fmt.Errorf("%w: %v", ErrStopUnconfirmed, stopErr))
			}
			if _, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true}, domain.SessionStateClosing, "close_command_cancelled", true); err != nil {
				return CloseSessionResult{}, err
			}
		}
	}
	if hasControl {
		confirmed, stopErr := control.StopSession(ctx, closing)
		if stopErr != nil || !confirmed {
			if stopErr == nil {
				stopErr = ErrStopUnconfirmed
			}
			return s.markClosingLost(ctx, closing, stopErr)
		}
	}
	closed, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateClosed, "runtime_closed")
	if err != nil {
		return CloseSessionResult{}, err
	}
	if err := s.store.ConfirmSessionCleanup(ctx, session.SessionID); err != nil {
		return CloseSessionResult{}, err
	}
	return CloseSessionResult{Session: closed}, nil
}

func (s *Service) markClosingLost(ctx context.Context, session store.SessionRecord, cause error) (CloseSessionResult, error) {
	lost, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateLost, "runtime_cleanup_unconfirmed")
	if err != nil {
		return CloseSessionResult{}, err
	}
	return CloseSessionResult{Session: lost}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, cause)
}

func (s *Service) appendStopOutput(ctx context.Context, commandID domain.CommandID, stopped RuntimeCommandStopResult) error {
	for _, output := range []struct {
		typ  string
		data []byte
	}{
		{typ: "stdout", data: stopped.Stdout},
		{typ: "stderr", data: stopped.Stderr},
	} {
		if len(output.data) == 0 {
			continue
		}
		if _, err := s.store.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: commandID, Type: output.typ, Payload: output.data, ByteCount: int64(len(output.data))}); err != nil {
			return err
		}
	}
	return nil
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
