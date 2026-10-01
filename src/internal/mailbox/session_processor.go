package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var ErrSessionProcessorConfiguration = errors.New("mailbox session processor configuration is invalid")

// DefaultTerminalArtifactRecoveryBatchLimit bounds one background repair pass.
// Terminal projection remains synchronous for ordinary work; this limit applies
// only to recovery after SQLite committed a terminal receipt before the derived
// filesystem artifacts could be published.
const DefaultTerminalArtifactRecoveryBatchLimit = 4

// SessionIntent is the durable Mac-side acceptance returned by create_session.
type SessionIntent struct {
	SessionID     string
	DeliveryState string
}

// SessionSnapshot is one owner-scoped local intent or authoritative/projection
// snapshot returned by the Mac session API boundary.
type SessionSnapshot struct {
	SessionID     string
	SessionState  string
	DeliveryState string
	ObservedAt    time.Time
}

// CommandIntent is the durable Mac-side acceptance returned by
// submit_command. It is not a target-authoritative command state.
type CommandIntent struct {
	CommandID     string
	SessionID     string
	DeliveryState string
}

// CancelCommandSnapshot keeps the cancel-intent delivery state separate from
// the submitted command's state and delivery state.
type CancelCommandSnapshot struct {
	CommandID            string
	SessionID            string
	CancelDeliveryState  string
	CommandDeliveryState string
	CommandState         string
	ObservedAt           time.Time
}

// CloseSessionSnapshot keeps close-intent delivery separate from the
// authoritative session lifecycle and original create-intent delivery.
type CloseSessionSnapshot struct {
	SessionID            string
	CloseDeliveryState   string
	SessionDeliveryState string
	SessionState         string
	ObservedAt           time.Time
}

// RunIntent is the stable Mac-side acceptance of a one-off job. Its IDs are
// known before any target-authoritative session or command state exists.
type RunIntent struct {
	JobID         string
	SessionID     string
	CommandID     string
	DeliveryState string
}

// RunSnapshot combines the owner-scoped run intent with any matching job and
// command snapshot. Command is nil until a command outcome is available.
type RunSnapshot struct {
	JobID                   string
	SessionID               string
	CommandID               string
	DeliveryState           string
	JobPhase                string
	Command                 *CommandSnapshot
	TeardownOutcome         string
	ObservedAt              time.Time
	RemoteStatusFailureAt   *time.Time
	RemoteStatusFailureCode string
}

// SessionOperationError is a safe, structured error from the Mac session
// operation boundary. Retryable failures leave the inbox pair available.
type SessionOperationError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *SessionOperationError) Error() string {
	if e == nil {
		return "session operation failed"
	}
	return e.Message
}

// SessionOperations is implemented by the Mac API adapter. The file client
// never calls this interface; only the local importer does.
type SessionOperations interface {
	SessionController() domain.ControllerIdentity
	CreateSessionIntent(context.Context, Request) (SessionIntent, error)
	GetSession(context.Context, string) (SessionSnapshot, error)
	SubmitCommandIntent(context.Context, Request) (CommandIntent, error)
	GetCommandSnapshot(context.Context, string) (CommandSnapshot, error)
	CancelCommandIntent(context.Context, Request) (CommandIntent, error)
	GetCancelCommandSnapshot(context.Context, string, string) (CancelCommandSnapshot, error)
	CloseSessionIntent(context.Context, Request) (SessionIntent, error)
	GetCloseSessionSnapshot(context.Context, string, string) (CloseSessionSnapshot, error)
	RunJobIntent(context.Context, Request) (RunIntent, error)
	GetRunSnapshot(context.Context, string) (RunSnapshot, error)
}

// MailboxExecutionResolver resolves an immutable configured context for a
// mailbox new-work request. The production implementation is config.Config;
// the interface keeps mailbox policy tests independent of a host config file.
type MailboxExecutionResolver interface {
	ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error)
}

type SessionProcessorOptions struct {
	MailboxID  string
	Importer   *Importer
	Authority  *store.AuthorityStore
	Controller domain.ControllerIdentity
	Operations SessionOperations
	Outbox     *Outbox
	EventFiles *EventFiles
	// ExecutionResolver resolves trusted mailbox defaults and allow-listed
	// overrides for create_session and run. It is mandatory: allowing a nil
	// resolver would let a miswired mailbox runtime bypass configured policy.
	ExecutionResolver       MailboxExecutionResolver
	Now                     func() time.Time
	RemoteUncertaintyWindow time.Duration
	// TerminalArtifactRecoveryBatchLimit bounds a background repair pass. Zero
	// selects DefaultTerminalArtifactRecoveryBatchLimit.
	TerminalArtifactRecoveryBatchLimit int
	// DeferTerminalArtifactRecovery lets a process-level scheduler place
	// recovery after fresh input, ACK, and cleanup work across every mailbox.
	// The default preserves the standalone processor reconciliation contract.
	DeferTerminalArtifactRecovery bool
}

// SessionProcessor wires file mailbox session and command operations through
// the Mac local API boundary. The mailbox remains an ingress/projection only.
type SessionProcessor struct {
	mailboxID                          string
	importer                           *Importer
	authority                          *store.AuthorityStore
	controller                         domain.ControllerIdentity
	operations                         SessionOperations
	projector                          Projector
	executionResolver                  MailboxExecutionResolver
	now                                func() time.Time
	uncertaintyWindow                  time.Duration
	terminalArtifactRecoveryBatchLimit int
	terminalArtifactRecoveryCursor     *store.MailboxTerminalArtifactCursor
	deferTerminalArtifactRecovery      bool
	mu                                 sync.Mutex
}

func NewSessionProcessor(options SessionProcessorOptions) (*SessionProcessor, error) {
	if options.Importer == nil || options.Authority == nil || options.Operations == nil || options.Outbox == nil || options.EventFiles == nil || options.ExecutionResolver == nil {
		return nil, ErrSessionProcessorConfiguration
	}
	if options.RemoteUncertaintyWindow < 0 {
		return nil, fmt.Errorf("%w: negative remote uncertainty window", ErrSessionProcessorConfiguration)
	}
	if options.RemoteUncertaintyWindow == 0 {
		options.RemoteUncertaintyWindow = store.DefaultRemoteUncertaintyWindow
	}
	if options.TerminalArtifactRecoveryBatchLimit < 0 {
		return nil, fmt.Errorf("%w: negative terminal artifact recovery batch limit", ErrSessionProcessorConfiguration)
	}
	if options.TerminalArtifactRecoveryBatchLimit == 0 {
		options.TerminalArtifactRecoveryBatchLimit = DefaultTerminalArtifactRecoveryBatchLimit
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	mailboxID := options.MailboxID
	if mailboxID == "" {
		mailboxID = options.Importer.MailboxID()
	}
	if _, ok := safeMailboxID(mailboxID); !ok || mailboxID != options.Importer.MailboxID() {
		return nil, fmt.Errorf("%w: mailbox ID", ErrSessionProcessorConfiguration)
	}
	controller, err := domain.NewControllerIdentity(options.Controller.Type(), options.Controller.ID())
	if err != nil || controller.Type() != domain.ControllerTypeLocalUser {
		return nil, fmt.Errorf("%w: controller must be a local user", ErrSessionProcessorConfiguration)
	}
	providedOperationsController := options.Operations.SessionController()
	operationsController, err := domain.NewControllerIdentity(providedOperationsController.Type(), providedOperationsController.ID())
	if err != nil || operationsController.Type() != controller.Type() || operationsController.ID() != controller.ID() {
		return nil, fmt.Errorf("%w: processor and Mac session-operation controllers must match", ErrSessionProcessorConfiguration)
	}
	return &SessionProcessor{
		mailboxID: mailboxID, importer: options.Importer, authority: options.Authority, controller: controller,
		operations: options.Operations, projector: Projector{MailboxID: mailboxID, Authority: options.Authority, Outbox: options.Outbox, EventFiles: options.EventFiles},
		executionResolver: options.ExecutionResolver, now: options.Now, uncertaintyWindow: options.RemoteUncertaintyWindow,
		terminalArtifactRecoveryBatchLimit: options.TerminalArtifactRecoveryBatchLimit,
		deferTerminalArtifactRecovery:      options.DeferTerminalArtifactRecovery,
	}, nil
}

func (p *SessionProcessor) exchangeRef(requestID string) (store.MailboxExchangeRef, error) {
	if p == nil {
		return store.MailboxExchangeRef{}, ErrSessionProcessorConfiguration
	}
	return store.NewMailboxExchangeRef(p.mailboxID, requestID)
}

func (p *SessionProcessor) recordRef(record store.MailboxExchangeRecord) (store.MailboxExchangeRef, error) {
	if p == nil || record.MailboxID != p.mailboxID {
		return store.MailboxExchangeRef{}, ErrSessionProcessorConfiguration
	}
	return store.NewMailboxExchangeRef(record.MailboxID, record.RequestID)
}

func (p *SessionProcessor) scopedRequest(request Request, record store.MailboxExchangeRecord) (Request, error) {
	if request.MailboxID != p.mailboxID || record.MailboxID != p.mailboxID || record.RequestID != request.RequestID ||
		(record.ExecutionIdempotencyKey == "" && request.IdempotencyKey != "") {
		return Request{}, ErrSessionProcessorConfiguration
	}
	request.ExecutionIdempotencyKey = record.ExecutionIdempotencyKey
	request.ExecutionSelection = cloneMailboxExecutionSelection(record.Selection)
	return request, nil
}

func cloneMailboxExecutionSelection(input *store.MailboxExecutionSelection) *store.MailboxExecutionSelection {
	if input == nil {
		return nil
	}
	clone := *input
	clone.RepositoryAliases = append([]string(nil), input.RepositoryAliases...)
	return &clone
}

// Import records and projects the implemented file-only operations, then
// reconciles accepted asynchronous intents and one-off run outcomes.
func (p *SessionProcessor) Import(ctx context.Context) ([]Result, error) {
	if p == nil || p.importer == nil || p.authority == nil || p.operations == nil {
		return nil, ErrSessionProcessorConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var result error
	if !p.deferTerminalArtifactRecovery {
		if err := p.reconcileTerminalArtifacts(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			result = errors.Join(result, mailboxStageError(p.mailboxID, "terminal_artifact", err))
		}
	}
	results, err := p.importer.importWithRecorder(ctx, p.process)
	if err != nil {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		result = errors.Join(result, mailboxStageError(p.mailboxID, "import", err))
	}
	for _, stage := range []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "accepted_create", run: p.reconcileAcceptedCreates},
		{name: "accepted_submit", run: p.reconcileAcceptedSubmits},
		{name: "accepted_cancel", run: p.reconcileAcceptedCancels},
		{name: "accepted_close", run: p.reconcileAcceptedCloses},
		{name: "accepted_run", run: p.reconcileAcceptedRuns},
	} {
		if err := stage.run(ctx); err != nil {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			result = errors.Join(result, mailboxStageError(p.mailboxID, stage.name, err))
		}
	}
	return results, result
}

// Reconcile advances accepted mutation responses after local intents or remote
// projections change. It is safe to call after restart.
func (p *SessionProcessor) Reconcile(ctx context.Context) error {
	if p == nil || p.authority == nil || p.operations == nil {
		return ErrSessionProcessorConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	stages := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "accepted_create", run: p.reconcileAcceptedCreates},
		{name: "accepted_submit", run: p.reconcileAcceptedSubmits},
		{name: "accepted_cancel", run: p.reconcileAcceptedCancels},
		{name: "accepted_close", run: p.reconcileAcceptedCloses},
		{name: "accepted_run", run: p.reconcileAcceptedRuns},
	}
	if !p.deferTerminalArtifactRecovery {
		stages = append([]struct {
			name string
			run  func(context.Context) error
		}{{name: "terminal_artifact", run: p.reconcileTerminalArtifacts}}, stages...)
	}
	var result error
	for _, stage := range stages {
		if err := stage.run(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			result = errors.Join(result, mailboxStageError(p.mailboxID, stage.name, err))
		}
	}
	return result
}

// RecoverTerminalArtifacts repairs a bounded page of derived outbox and event
// artifacts after fresh mailbox intake, reconciliation, ACK, and cleanup work
// have had their turn. It never calls a session operation or target mutation.
func (p *SessionProcessor) RecoverTerminalArtifacts(ctx context.Context) error {
	if p == nil || p.authority == nil || p.operations == nil {
		return ErrSessionProcessorConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reconcileTerminalArtifacts(ctx)
}

func (p *SessionProcessor) process(ctx context.Context, request Request) (bool, error) {
	if request.MailboxID != p.mailboxID {
		return false, ErrSessionProcessorConfiguration
	}
	if request.Operation == "submit_command" || request.Operation == "get_command" {
		return p.processCommand(ctx, request)
	}
	if request.Operation == "cancel_command" {
		return p.processCancelCommand(ctx, request)
	}
	if request.Operation == "close_session" {
		return p.processCloseSession(ctx, request)
	}
	if request.Operation == "run" {
		return p.processRun(ctx, request)
	}
	if request.Operation == "create_session" {
		return p.processCreateSession(ctx, request)
	}
	if request.Operation != "get_session" {
		return false, fmt.Errorf("%w: unsupported mailbox operation %q", ErrMailboxInput, request.Operation)
	}
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return false, err
	}
	ref, err := p.exchangeRef(request.RequestID)
	if err != nil {
		return false, err
	}
	record, duplicate, idempotencyConflict, err := p.authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: p.mailboxID, RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		if err := p.projector.Publish(ctx, request.RequestID); err != nil {
			return false, err
		}
		return true, nil
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := sessionMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publish(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal exchange has no response snapshot", ErrOutboxResponse)
	}
	snapshot, err := p.operations.GetSession(ctx, request.SessionID)
	if err != nil {
		return p.publishOperationError(ctx, record, request.Operation, err)
	}
	if snapshot.SessionID == "" {
		snapshot.SessionID = request.SessionID
	}
	if snapshot.SessionID != request.SessionID {
		return false, fmt.Errorf("%w: get operation returned a different session ID", ErrSessionProcessorConfiguration)
	}
	response := responseFromSnapshot(request.RequestID, request.Operation, store.MailboxExchangeComplete, snapshot)
	return p.publish(ctx, record, response, nil)
}

func (p *SessionProcessor) processCreateSession(ctx context.Context, request Request) (bool, error) {
	record, duplicate, idempotencyConflict, selectionError, legacyExplicitSelection, err := p.acceptNewWorkExchange(ctx, request)
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		if err := p.projector.Publish(ctx, request.RequestID); err != nil {
			return false, err
		}
		return true, nil
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := sessionMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publish(ctx, record, response, nil)
	}
	if selectionError != nil {
		response := sessionMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected, Error: selectionError,
		}
		return p.publish(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal create exchange has no response snapshot", ErrOutboxResponse)
	}
	scopedRequest, err := p.scopedRequest(request, record)
	if err != nil {
		return false, err
	}
	if scopedRequest.ExecutionSelection == nil && !legacyExplicitSelection {
		return false, fmt.Errorf("%w: resolved create selection was not persisted", ErrSessionProcessorConfiguration)
	}
	intent, err := p.operations.CreateSessionIntent(ctx, scopedRequest)
	if err != nil {
		return p.publishOperationError(ctx, record, request.Operation, err)
	}
	if intent.SessionID == "" {
		return false, fmt.Errorf("%w: create operation returned no session ID", ErrSessionProcessorConfiguration)
	}
	response := sessionMailboxResponse{
		RequestID: request.RequestID, Operation: request.Operation,
		RequestState: store.MailboxExchangeAccepted, SessionID: intent.SessionID,
		DeliveryState: intent.DeliveryState,
	}
	return p.publish(ctx, record, response, nil)
}

func (p *SessionProcessor) processCommand(ctx context.Context, request Request) (bool, error) {
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return false, err
	}
	ref, err := p.exchangeRef(request.RequestID)
	if err != nil {
		return false, err
	}
	record, duplicate, idempotencyConflict, err := p.authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: p.mailboxID, RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		return true, p.publishStoredTerminalResponse(ctx, record)
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := commandMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publishCommandResponse(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal command exchange has no response snapshot", ErrOutboxResponse)
	}
	if request.Operation == "submit_command" {
		scopedRequest, err := p.scopedRequest(request, record)
		if err != nil {
			return false, err
		}
		intent, err := p.operations.SubmitCommandIntent(ctx, scopedRequest)
		if err != nil {
			return p.publishOperationError(ctx, record, request.Operation, err)
		}
		if intent.CommandID == "" || intent.SessionID == "" {
			return false, fmt.Errorf("%w: submit operation returned no command/session ID", ErrSessionProcessorConfiguration)
		}
		response := commandMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation, RequestState: store.MailboxExchangeAccepted,
			CommandID: intent.CommandID, SessionID: intent.SessionID, DeliveryState: intent.DeliveryState,
		}
		return p.publishCommandResponse(ctx, record, response, nil)
	}

	snapshot, err := p.operations.GetCommandSnapshot(ctx, request.CommandID)
	if err != nil {
		return p.publishOperationError(ctx, record, request.Operation, err)
	}
	commandID, err := domain.NewCommandID(request.CommandID)
	if err != nil {
		return false, err
	}
	if err := ValidateCommandSnapshot(snapshot, commandID); err != nil {
		return false, fmt.Errorf("%w: %v", ErrSessionProcessorConfiguration, err)
	}
	response := commandResponseFromSnapshot(request.RequestID, request.Operation, snapshot)
	var cursor *int64
	if snapshot.State != "" || snapshot.AvailableEventSequence > 0 {
		value := snapshot.AvailableEventSequence
		cursor = &value
	}
	return p.publishCommandResponse(ctx, record, response, cursor)
}

func (p *SessionProcessor) processCancelCommand(ctx context.Context, request Request) (bool, error) {
	record, duplicate, idempotencyConflict, err := p.acceptMutationExchange(ctx, request)
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		return true, p.projector.Publish(ctx, request.RequestID)
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := commandMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publishCommandResponse(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal cancel exchange has no response snapshot", ErrOutboxResponse)
	}
	scopedRequest, err := p.scopedRequest(request, record)
	if err != nil {
		return false, err
	}
	intent, err := p.operations.CancelCommandIntent(ctx, scopedRequest)
	if err != nil {
		return p.publishCommandOperationError(ctx, record, request.CommandID, request.Operation, err)
	}
	if intent.CommandID != request.CommandID || intent.SessionID == "" {
		return false, fmt.Errorf("%w: cancel operation returned a different command or no session ID", ErrSessionProcessorConfiguration)
	}
	response := commandMailboxResponse{
		RequestID: request.RequestID, Operation: request.Operation, RequestState: store.MailboxExchangeAccepted,
		CommandID: intent.CommandID, SessionID: intent.SessionID, DeliveryState: intent.DeliveryState,
	}
	return p.publishCommandResponse(ctx, record, response, nil)
}

func (p *SessionProcessor) processCloseSession(ctx context.Context, request Request) (bool, error) {
	record, duplicate, idempotencyConflict, err := p.acceptMutationExchange(ctx, request)
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		return true, p.projector.Publish(ctx, request.RequestID)
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := sessionMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publish(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal close exchange has no response snapshot", ErrOutboxResponse)
	}
	scopedRequest, err := p.scopedRequest(request, record)
	if err != nil {
		return false, err
	}
	intent, err := p.operations.CloseSessionIntent(ctx, scopedRequest)
	if err != nil {
		return p.publishSessionOperationError(ctx, record, request.SessionID, request.Operation, err)
	}
	if intent.SessionID != request.SessionID {
		return false, fmt.Errorf("%w: close operation returned a different session ID", ErrSessionProcessorConfiguration)
	}
	response := sessionMailboxResponse{
		RequestID: request.RequestID, Operation: request.Operation,
		RequestState: store.MailboxExchangeAccepted, SessionID: intent.SessionID,
		DeliveryState: intent.DeliveryState,
	}
	return p.publish(ctx, record, response, nil)
}

func (p *SessionProcessor) processRun(ctx context.Context, request Request) (bool, error) {
	record, duplicate, idempotencyConflict, selectionError, legacyExplicitSelection, err := p.acceptNewWorkExchange(ctx, request)
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		return true, p.publishStoredTerminalResponse(ctx, record)
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := runMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publishRunResponse(ctx, record, response, nil)
	}
	if selectionError != nil {
		response := runMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected, Error: selectionError,
		}
		return p.publishRunResponse(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal run exchange has no response snapshot", ErrOutboxResponse)
	}
	scopedRequest, err := p.scopedRequest(request, record)
	if err != nil {
		return false, err
	}
	if scopedRequest.ExecutionSelection == nil && !legacyExplicitSelection {
		return false, fmt.Errorf("%w: resolved run selection was not persisted", ErrSessionProcessorConfiguration)
	}
	intent, err := p.operations.RunJobIntent(ctx, scopedRequest)
	if err != nil {
		return p.publishRunOperationError(ctx, record, err)
	}
	jobID, jobErr := domain.NewJobID(intent.JobID)
	sessionID, sessionErr := domain.NewSessionID(intent.SessionID)
	commandID, commandErr := domain.NewCommandID(intent.CommandID)
	if jobErr != nil || sessionErr != nil || commandErr != nil || !validDeliveryState(intent.DeliveryState) {
		return false, fmt.Errorf("%w: run operation returned invalid IDs or delivery state", ErrSessionProcessorConfiguration)
	}
	response := runMailboxResponse{
		RequestID: request.RequestID, Operation: request.Operation,
		RequestState: store.MailboxExchangeAccepted,
		JobID:        string(jobID), SessionID: string(sessionID), CommandID: string(commandID),
		DeliveryState: intent.DeliveryState,
	}
	return p.publishRunResponse(ctx, record, response, nil)
}

func (p *SessionProcessor) publishRunOperationError(ctx context.Context, record store.MailboxExchangeRecord, err error) (bool, error) {
	var operationErr *SessionOperationError
	if !errors.As(err, &operationErr) {
		return false, err
	}
	if operationErr.Retryable {
		return false, err
	}
	response := runMailboxResponse{
		RequestID: record.RequestID, Operation: record.Operation,
		RequestState: store.MailboxExchangeRejected, Error: safeSessionMailboxError(operationErr),
	}
	return p.publishRunResponse(ctx, record, response, nil)
}

// acceptNewWorkExchange resolves the trusted execution selection before it
// forms the receipt hash, then persists both in the same durable exchange.
// A selection rejection is itself recorded as a terminal mailbox result, but
// it never reaches the local intent or target-operation boundary.
func (p *SessionProcessor) acceptNewWorkExchange(ctx context.Context, request Request) (store.MailboxExchangeRecord, bool, bool, *mailboxResponseError, bool, error) {
	ref, err := p.exchangeRef(request.RequestID)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, nil, false, err
	}
	if record, duplicate, handled, err := p.pendingSelectionRejection(ctx, ref, request); err != nil {
		return store.MailboxExchangeRecord{}, false, false, nil, false, err
	} else if handled {
		return record, duplicate, false, rejectedSelectionRecoveryError(), false, nil
	}
	selection, selectionError, err := p.resolveNewWorkSelection(ctx, request)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, nil, false, err
	}
	payload, hash, err := receiptCanonicalWithSelection(request, selection)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, nil, false, err
	}
	selectionState := store.MailboxExecutionSelectionResolved
	if selectionError != nil {
		selectionState = store.MailboxExecutionSelectionRejected
	}
	record, duplicate, idempotencyConflict, err := p.authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: p.mailboxID, RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
		Selection: selection, SelectionState: selectionState,
	})
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, nil, false, err
	}
	// A migrated v25 record has no selection fields and is marked legacy. It
	// can resume only when the current resolver accepts its original complete
	// explicit pair. A new P153 rejection uses the rejected state, so it cannot
	// be mistaken for a legacy record after a crash before publication.
	legacyExplicitSelection := selectionError == nil && selection != nil &&
		record.Selection == nil && record.SelectionState == store.MailboxExecutionSelectionLegacy &&
		selection.Source == store.MailboxExecutionSelectionRequestOverride &&
		legacyExplicitNewWorkRequest(request) && resolverMailboxExecutionSelectionMatchesRequest(request, selection)
	return record, duplicate, idempotencyConflict, selectionError, legacyExplicitSelection, nil
}

// pendingSelectionRejection finds a P153 selection rejection that was
// durably accepted before the process crashed but has no response snapshot.
// It deliberately compares the original raw canonical receipt, before any
// now-allowed default can be injected. This keeps retries terminal even when
// configuration changes between the failed attempt and recovery.
func (p *SessionProcessor) pendingSelectionRejection(ctx context.Context, ref store.MailboxExchangeRef, request Request) (store.MailboxExchangeRecord, bool, bool, error) {
	existing, err := p.authority.GetMailboxExchangeInMailbox(ctx, ref)
	if err == nil {
		if selectionRejectionMatchesRequest(request, existing) {
			return existing, true, true, nil
		}
		return store.MailboxExchangeRecord{}, false, false, nil
	}
	if !errors.Is(err, store.ErrMailboxExchangeNotFound) {
		return store.MailboxExchangeRecord{}, false, false, err
	}
	if request.IdempotencyKey == "" {
		return store.MailboxExchangeRecord{}, false, false, nil
	}
	existing, found, err := p.authority.FindActiveMailboxExchangeByKeyInMailbox(ctx, p.mailboxID, p.controller, request.Operation, request.IdempotencyKey)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, err
	}
	if !found || !selectionRejectionMatchesRequest(request, existing) {
		return store.MailboxExchangeRecord{}, false, false, nil
	}
	recorded, duplicate, _, err := p.authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: p.mailboxID, RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: existing.RequestHash, CanonicalPayload: existing.CanonicalPayload,
		SelectionState: store.MailboxExecutionSelectionRejected,
	})
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, err
	}
	return recorded, duplicate, true, nil
}

func selectionRejectionMatchesRequest(request Request, record store.MailboxExchangeRecord) bool {
	if record.Selection != nil || record.SelectionState != store.MailboxExecutionSelectionRejected {
		return false
	}
	// An accepted record with no response models a crash before publication. A
	// rejected record is the same durable decision after publication. Either
	// one must remain terminal for a same-key retry even if its configuration
	// later changes to allow the request.
	if record.State != store.MailboxExchangeRejected &&
		(record.State != store.MailboxExchangeAccepted || len(record.ResponseBytes) != 0) {
		return false
	}
	payload, hash, err := receiptCanonicalWithSelection(request, nil)
	return err == nil && domain.CompareIdempotency(hash, record.RequestHash) == domain.IdempotencySamePayload && bytes.Equal(payload, record.CanonicalPayload)
}

func rejectedSelectionRecoveryError() *mailboxResponseError {
	return &mailboxResponseError{Code: "invalid_request", Message: "mailbox execution selection was rejected"}
}

func (p *SessionProcessor) resolveNewWorkSelection(ctx context.Context, request Request) (*store.MailboxExecutionSelection, *mailboxResponseError, error) {
	if request.Operation != "create_session" && request.Operation != "run" {
		return nil, nil, fmt.Errorf("%w: selection operation %q", ErrSessionProcessorConfiguration, request.Operation)
	}
	if request.IdempotencyKey != "" {
		existing, found, err := p.authority.FindActiveMailboxExchangeByKeyInMailbox(ctx, p.mailboxID, p.controller, request.Operation, request.IdempotencyKey)
		if err != nil {
			return nil, nil, err
		}
		if found && existing.Selection != nil && retainedMailboxExecutionSelectionMatchesRequest(request, existing.Selection) {
			return cloneMailboxExecutionSelection(existing.Selection), nil, nil
		}
	}
	resolved, err := p.executionResolver.ResolveMailboxExecution(
		p.mailboxID,
		request.EnvironmentPresent,
		request.Environment,
		request.ExecutionTargetPresent,
		request.ExecutionTarget,
		request.RepositoryAlias,
	)
	if err != nil {
		return nil, mailboxSelectionError(err), nil
	}
	selection, err := mailboxExecutionSelectionFromConfig(p.mailboxID, resolved)
	if err != nil {
		return nil, nil, err
	}
	if !resolverMailboxExecutionSelectionMatchesRequest(request, selection) {
		return nil, nil, fmt.Errorf("%w: resolver selection does not match mailbox request", ErrSessionProcessorConfiguration)
	}
	return selection, nil, nil
}

// retainedMailboxExecutionSelectionMatchesRequest recognizes a retained
// same-key selection before receipt canonicalization. An omitted pair is a
// retry request for the selection that was already bound to its key, whether
// that original selection came from an inbox default or an explicit override.
func retainedMailboxExecutionSelectionMatchesRequest(request Request, selection *store.MailboxExecutionSelection) bool {
	if selection == nil || request.RepositoryAlias != selection.RepositoryAlias || request.EnvironmentPresent != request.ExecutionTargetPresent {
		return false
	}
	if !request.EnvironmentPresent {
		return true
	}
	return request.Environment == selection.Environment && request.ExecutionTarget.Kind() == selection.Target.Kind() && request.ExecutionTarget.Profile() == selection.Target.Profile()
}

// resolverMailboxExecutionSelectionMatchesRequest rejects a malformed or
// miswired resolver output. A fresh resolution must accurately describe the
// raw request form; only a retained key may omit an originally explicit pair.
func resolverMailboxExecutionSelectionMatchesRequest(request Request, selection *store.MailboxExecutionSelection) bool {
	if !retainedMailboxExecutionSelectionMatchesRequest(request, selection) {
		return false
	}
	if !request.EnvironmentPresent {
		return selection.Source == store.MailboxExecutionSelectionInboxDefault
	}
	return selection.Source == store.MailboxExecutionSelectionRequestOverride
}

// legacyExplicitNewWorkRequest identifies an active pre-P153 create/run
// receipt. P153 never creates a selection-free new-work exchange, but v25
// rows legitimately have no selection snapshot. They may resume only when
// their original request still carries the complete explicit pair; omitted or
// partial selection cannot be inferred without inventing a default.
func legacyExplicitNewWorkRequest(request Request) bool {
	if (request.Operation != "create_session" && request.Operation != "run") ||
		!request.EnvironmentPresent || !request.ExecutionTargetPresent || request.Environment == "" {
		return false
	}
	_, err := domain.NewExecutionTarget(request.ExecutionTarget.Kind(), request.ExecutionTarget.Profile())
	return err == nil
}

func mailboxExecutionSelectionFromConfig(mailboxID string, resolved config.MailboxExecutionSelection) (*store.MailboxExecutionSelection, error) {
	if resolved.MailboxID != mailboxID {
		return nil, fmt.Errorf("%w: resolver returned a selection for another mailbox", ErrSessionProcessorConfiguration)
	}
	source := ""
	switch resolved.Source {
	case config.MailboxExecutionSelectionSourceInboxDefault:
		source = store.MailboxExecutionSelectionInboxDefault
	case config.MailboxExecutionSelectionSourceRequestOverride:
		source = store.MailboxExecutionSelectionRequestOverride
	default:
		return nil, fmt.Errorf("%w: unknown mailbox selection source", ErrSessionProcessorConfiguration)
	}
	return &store.MailboxExecutionSelection{
		ContextName:       resolved.ContextName,
		Environment:       resolved.Environment,
		Target:            resolved.Target,
		Source:            source,
		RepositoryAlias:   resolved.RepositoryAlias,
		RepositoryAliases: append([]string(nil), resolved.RepositoryAliases...),
	}, nil
}

func mailboxSelectionError(err error) *mailboxResponseError {
	switch {
	case errors.Is(err, config.ErrMailboxExecutionContextNotFound):
		return &mailboxResponseError{Code: "environment_target_mismatch", Message: "environment and execution target do not identify a configured context"}
	case errors.Is(err, config.ErrMailboxExecutionPairRequired):
		return &mailboxResponseError{Code: "invalid_request", Message: "environment and execution target must be supplied together"}
	case errors.Is(err, config.ErrMailboxExecutionContextNotAllowed):
		return &mailboxResponseError{Code: "invalid_request", Message: "execution context is not allowed for this mailbox"}
	case errors.Is(err, config.ErrMailboxRepositoryAliasNotAllowed):
		return &mailboxResponseError{Code: "invalid_request", Message: "repository alias is not allowed for this mailbox"}
	case errors.Is(err, config.ErrMailboxNotConfigured):
		return &mailboxResponseError{Code: "invalid_request", Message: "mailbox is not configured"}
	default:
		return &mailboxResponseError{Code: "invalid_request", Message: "mailbox execution selection is invalid"}
	}
}

func (p *SessionProcessor) acceptMutationExchange(ctx context.Context, request Request) (store.MailboxExchangeRecord, bool, bool, error) {
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, err
	}
	ref, err := p.exchangeRef(request.RequestID)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, err
	}
	return p.authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: p.mailboxID, RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
}

func (p *SessionProcessor) publishCommandOperationError(ctx context.Context, record store.MailboxExchangeRecord, commandID, operation string, err error) (bool, error) {
	var operationErr *SessionOperationError
	if !errors.As(err, &operationErr) {
		return false, err
	}
	if operationErr.Retryable {
		return false, err
	}
	response := commandMailboxResponse{
		RequestID: record.RequestID, Operation: operation, RequestState: store.MailboxExchangeRejected,
		CommandID: commandID, Error: safeSessionMailboxError(operationErr),
	}
	return p.publishCommandResponse(ctx, record, response, nil)
}

func (p *SessionProcessor) publishSessionOperationError(ctx context.Context, record store.MailboxExchangeRecord, sessionID, operation string, err error) (bool, error) {
	var operationErr *SessionOperationError
	if !errors.As(err, &operationErr) {
		return false, err
	}
	if operationErr.Retryable {
		return false, err
	}
	response := sessionMailboxResponse{
		RequestID: record.RequestID, Operation: operation, RequestState: store.MailboxExchangeRejected,
		SessionID: sessionID, Error: safeSessionMailboxError(operationErr),
	}
	return p.publish(ctx, record, response, nil)
}

// reconcileTerminalArtifacts rebuilds derived files after a process stop once
// the terminal SQLite receipt is durable. It performs no operation call or
// target mutation. Cleanup claims deliberately suppress this repair so an
// expired result cannot return after its removal has begun.
func (p *SessionProcessor) reconcileTerminalArtifacts(ctx context.Context) error {
	records, err := p.authority.ListPublishableTerminalMailboxExchangesPageInMailbox(ctx, p.mailboxID, p.controller, p.terminalArtifactRecoveryCursor, p.terminalArtifactRecoveryBatchLimit)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		// A completed sweep starts again from the oldest retained receipt on a
		// later bounded pass. This keeps recovery eventual after a transient
		// filesystem error without allowing a full scan to monopolize intake.
		p.terminalArtifactRecoveryCursor = nil
		return nil
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.reconcileTerminalArtifact(ctx, record); err != nil {
			result = errors.Join(result, mailboxReconciliationIssue(record, "terminal_artifact", err))
		}
		// A malformed or unavailable single artifact must not keep later
		// retained receipts behind it from being repaired. The durable response
		// is unchanged; a later full sweep can retry this record.
		p.terminalArtifactRecoveryCursor = &store.MailboxTerminalArtifactCursor{CreatedAt: record.CreatedAt, ExchangeID: record.ExchangeID}
	}
	if len(records) < p.terminalArtifactRecoveryBatchLimit {
		// This completed page was the end of the current sweep. Reset now so
		// a durable proof or transient filesystem condition that changes after
		// this pass is eligible on the next bounded turn.
		p.terminalArtifactRecoveryCursor = nil
	}
	return result
}

func (p *SessionProcessor) reconcileTerminalArtifact(ctx context.Context, record store.MailboxExchangeRecord) error {
	eligible, err := p.terminalResponseEligibleForRecovery(ctx, record)
	if err != nil {
		return fmt.Errorf("validate terminal mailbox response %s: %w", record.RequestID, err)
	}
	if !eligible {
		// This recovery pass only rebuilds absent derived files. A legacy
		// terminal response that was already published before P149 remains an
		// immutable delivered artifact; do not retroactively remove or rewrite it.
		return nil
	}
	current, err := p.terminalArtifactsCurrent(ctx, record)
	if err != nil {
		// Cleanup owns a concurrent expiry claim, so it is not a failed cycle.
		if errors.Is(err, store.ErrMailboxResponseExpired) {
			return nil
		}
		return fmt.Errorf("inspect terminal mailbox response %s: %w", record.RequestID, err)
	}
	if current {
		return nil
	}
	if err := p.publishStoredTerminalResponse(ctx, record); err != nil {
		if errors.Is(err, store.ErrMailboxResponseExpired) {
			return nil
		}
		return fmt.Errorf("rebuild terminal mailbox response %s: %w", record.RequestID, err)
	}
	return nil
}

// terminalResponseEligibleForRecovery prevents a pre-P149 reconciled remote
// terminal result from being republished solely because a derived file is
// absent. The Router must first revalidate it with the strict remote proof.
func (p *SessionProcessor) terminalResponseEligibleForRecovery(ctx context.Context, record store.MailboxExchangeRecord) (bool, error) {
	if record.State == store.MailboxExchangeAccepted {
		return true, nil
	}
	var reference struct {
		JobID           string `json:"job_id"`
		CommandID       string `json:"command_id"`
		CommandState    string `json:"command_state"`
		JobPhase        string `json:"job_phase"`
		TeardownOutcome string `json:"teardown_outcome"`
	}
	if err := json.Unmarshal(record.ResponseBytes, &reference); err != nil {
		return false, fmt.Errorf("%w: terminal command response is invalid", ErrOutboxResponse)
	}
	// A run can reach a terminal job/teardown outcome before there is a
	// command snapshot. Older Router versions could freeze that response with
	// delivery_state=reconciled. It is still a target outcome, so it must not
	// bypass the P149 proof boundary merely because command_state is absent.
	if record.Operation == "run" && terminalJobPhase(reference.JobPhase) && reference.TeardownOutcome != "" {
		if reference.JobID == "" || reference.CommandID == "" {
			return false, fmt.Errorf("%w: terminal run response has no job or command ID", ErrOutboxResponse)
		}
		commandID, err := domain.NewCommandID(reference.CommandID)
		if err != nil {
			return false, fmt.Errorf("%w: terminal run response command ID: %v", ErrOutboxResponse, err)
		}
		intent, err := p.authority.GetLocalIntentByResource(ctx, "run", reference.JobID, p.controller)
		if err != nil {
			return false, err
		}
		if intent.CommandID != commandID {
			return false, fmt.Errorf("%w: terminal run response command does not match its intent", ErrOutboxResponse)
		}
		return intent.Target.Kind() != domain.TargetKindRemote || store.HasRemoteTerminalProof(intent), nil
	}
	// Only a response that claims a terminal command result crosses the P149
	// proof boundary. Generic terminal errors, resource-free outcomes, and
	// active snapshots remain safely reproducible from their immutable receipt.
	if reference.CommandID == "" || !domain.CommandState(reference.CommandState).IsTerminal() {
		return true, nil
	}
	commandID, err := domain.NewCommandID(reference.CommandID)
	if err != nil {
		return false, fmt.Errorf("%w: terminal command response ID: %v", ErrOutboxResponse, err)
	}
	if record.Operation == "run" {
		if reference.JobID == "" {
			return false, fmt.Errorf("%w: terminal run response has no job ID", ErrOutboxResponse)
		}
		intent, err := p.authority.GetLocalIntentByResource(ctx, "run", reference.JobID, p.controller)
		if err != nil {
			return false, err
		}
		if intent.CommandID != commandID {
			return false, fmt.Errorf("%w: terminal run response command does not match its intent", ErrOutboxResponse)
		}
		return intent.Target.Kind() != domain.TargetKindRemote || store.HasRemoteTerminalProof(intent), nil
	}
	intent, err := p.authority.GetLocalIntentByResource(ctx, "submit_command", string(commandID), p.controller)
	if err != nil {
		return false, err
	}
	return intent.Target.Kind() != domain.TargetKindRemote || store.HasRemoteTerminalProof(intent), nil
}

// terminalArtifactsCurrent checks the exact durable response image before a
// recovery rewrite. Normal mailbox cycles therefore do not continuously
// rewrite retained outbox files merely because they are eligible for recovery.
func (p *SessionProcessor) terminalArtifactsCurrent(ctx context.Context, record store.MailboxExchangeRecord) (bool, error) {
	response, err := p.projector.Outbox.Read(record.RequestID)
	if err != nil || !bytes.Equal(response, record.ResponseBytes) {
		return false, nil
	}
	if record.AvailableEventSequence == nil || *record.AvailableEventSequence < 1 {
		return true, nil
	}
	var reference struct {
		CommandID  string `json:"command_id"`
		EventsFile string `json:"events_file"`
	}
	if err := json.Unmarshal(record.ResponseBytes, &reference); err != nil || reference.CommandID == "" {
		return false, fmt.Errorf("%w: stored terminal command response is invalid", ErrOutboxResponse)
	}
	commandID, err := domain.NewCommandID(reference.CommandID)
	if err != nil {
		return false, fmt.Errorf("%w: stored terminal command response ID: %v", ErrOutboxResponse, err)
	}
	expectedReference, err := commandEventsFileReference(commandID)
	if err != nil || reference.EventsFile != expectedReference {
		return false, fmt.Errorf("%w: stored terminal response event-file reference", ErrOutboxResponse)
	}
	current, cursor, err := p.projector.EventFiles.Read(commandID)
	if err != nil {
		return false, nil
	}
	ref, err := p.recordRef(record)
	if err != nil {
		return false, err
	}
	expected, expectedCursor, err := (EventProjector{Authority: p.authority}).ProjectMailboxResponseThroughInMailbox(ctx, ref, commandID)
	if err != nil {
		return false, err
	}
	if expectedCursor != *record.AvailableEventSequence {
		return false, fmt.Errorf("%w: projected cursor %d differs from response cursor %d", ErrOutboxResponse, expectedCursor, *record.AvailableEventSequence)
	}
	return cursor >= expectedCursor && bytes.HasPrefix(current, expected), nil
}

// publishStoredTerminalResponse republishes one already durable response. A
// frozen event cursor identifies a command event-file projection; every other
// response has only the outbox JSON artifact.
func (p *SessionProcessor) publishStoredTerminalResponse(ctx context.Context, record store.MailboxExchangeRecord) error {
	if record.State != store.MailboxExchangeAccepted {
		eligible, err := p.terminalResponseEligibleForRecovery(ctx, record)
		if err != nil {
			return err
		}
		if !eligible {
			return nil
		}
	}
	if record.AvailableEventSequence != nil && *record.AvailableEventSequence > 0 {
		var response struct {
			CommandID  string `json:"command_id"`
			EventsFile string `json:"events_file"`
		}
		if err := json.Unmarshal(record.ResponseBytes, &response); err != nil || response.CommandID == "" {
			return fmt.Errorf("%w: stored terminal command response is invalid", ErrOutboxResponse)
		}
		commandID, err := domain.NewCommandID(response.CommandID)
		if err != nil {
			return fmt.Errorf("%w: stored terminal command response ID: %v", ErrOutboxResponse, err)
		}
		expectedReference, err := commandEventsFileReference(commandID)
		if err != nil || response.EventsFile != expectedReference {
			return fmt.Errorf("%w: stored terminal response event-file reference", ErrOutboxResponse)
		}
		return p.projector.PublishCommand(ctx, record.RequestID, commandID)
	}
	return p.projector.Publish(ctx, record.RequestID)
}

func (p *SessionProcessor) publishOperationError(ctx context.Context, record store.MailboxExchangeRecord, operation string, err error) (bool, error) {
	var operationErr *SessionOperationError
	if errors.As(err, &operationErr) {
		if operationErr.Retryable {
			return false, err
		}
		response := sessionMailboxResponse{
			RequestID: record.RequestID, Operation: operation, RequestState: store.MailboxExchangeRejected,
			Error: safeSessionMailboxError(operationErr),
		}
		return p.publish(ctx, record, response, nil)
	}
	return false, err
}

func (p *SessionProcessor) reconcileAcceptedCreates(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchangesInMailbox(ctx, p.mailboxID, p.controller, "create_session", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.reconcileAcceptedCreate(ctx, record); err != nil {
			result = errors.Join(result, mailboxReconciliationIssue(record, "accepted_create", err))
		}
	}
	return result
}

// reconcileAcceptedCreate advances one accepted create receipt. A malformed
// or temporarily unavailable record remains available for a later cycle
// without blocking an independent create request in the same mailbox.
func (p *SessionProcessor) reconcileAcceptedCreate(ctx context.Context, record store.MailboxExchangeRecord) error {
	if len(record.ResponseBytes) == 0 {
		return nil
	}
	var previous sessionMailboxResponse
	if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "create_session" || previous.SessionID == "" {
		return fmt.Errorf("%w: accepted create response is corrupt", ErrOutboxResponse)
	}
	snapshot, err := p.operations.GetSession(ctx, previous.SessionID)
	if err != nil {
		var operationErr *SessionOperationError
		if errors.As(err, &operationErr) && operationErr.Retryable {
			return nil
		}
		// A missing local intent here does not prove that a queued remote
		// create was never delivered. Keep the receipt unresolved.
		if errors.As(err, &operationErr) && operationErr.Code == "resource_not_found" {
			return nil
		}
		return err
	}
	if snapshot.SessionID == "" {
		snapshot.SessionID = previous.SessionID
	}
	if string(snapshot.SessionID) != previous.SessionID {
		return fmt.Errorf("%w: reconciliation returned a different session ID", ErrSessionProcessorConfiguration)
	}
	if snapshot.DeliveryState == string(store.LocalIntentNotDelivered) {
		response := sessionMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation,
			RequestState: store.MailboxExchangeRejected, SessionID: snapshot.SessionID,
			DeliveryState: snapshot.DeliveryState,
			Error:         &mailboxResponseError{Code: "resource_not_found", Message: "session creation was proven not delivered"},
		}
		_, err := p.publish(ctx, record, response, nil)
		return err
	}
	if snapshot.SessionState != "" && snapshot.SessionState != string(domain.SessionStateRequested) && snapshot.SessionState != string(domain.SessionStateCreating) {
		response := responseFromSnapshot(record.RequestID, record.Operation, store.MailboxExchangeComplete, snapshot)
		_, err := p.publish(ctx, record, response, nil)
		return err
	}
	if published, err := p.publishIndeterminateIfExpired(ctx, record, snapshot.DeliveryState); err != nil {
		return err
	} else if published {
		return nil
	}
	if snapshot.DeliveryState != "" && snapshot.DeliveryState != previous.DeliveryState {
		response := sessionMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation,
			RequestState: store.MailboxExchangeAccepted, SessionID: snapshot.SessionID,
			DeliveryState: snapshot.DeliveryState,
			ObservedAt:    mailboxTime(snapshot.ObservedAt),
		}
		_, err := p.publish(ctx, record, response, nil)
		return err
	}
	return p.projector.Publish(ctx, record.RequestID)
}

func (p *SessionProcessor) reconcileAcceptedSubmits(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchangesInMailbox(ctx, p.mailboxID, p.controller, "submit_command", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.reconcileAcceptedSubmit(ctx, record); err != nil {
			result = errors.Join(result, mailboxReconciliationIssue(record, "accepted_submit", err))
		}
	}
	return result
}

// reconcileAcceptedSubmit advances one accepted submit receipt independently
// so a bad response image cannot starve a later command submission.
func (p *SessionProcessor) reconcileAcceptedSubmit(ctx context.Context, record store.MailboxExchangeRecord) error {
	if len(record.ResponseBytes) == 0 {
		return nil
	}
	var previous commandMailboxResponse
	if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "submit_command" || previous.CommandID == "" || previous.SessionID == "" {
		return fmt.Errorf("%w: accepted submit response is corrupt", ErrOutboxResponse)
	}
	snapshot, err := p.operations.GetCommandSnapshot(ctx, previous.CommandID)
	if err != nil {
		var operationErr *SessionOperationError
		if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
			return nil
		}
		return err
	}
	commandID, err := domain.NewCommandID(previous.CommandID)
	if err != nil {
		return fmt.Errorf("%w: accepted submit command ID: %v", ErrOutboxResponse, err)
	}
	if err := ValidateCommandSnapshot(snapshot, commandID); err != nil {
		return fmt.Errorf("%w: %v", ErrSessionProcessorConfiguration, err)
	}
	if string(snapshot.SessionID) != previous.SessionID {
		return fmt.Errorf("%w: submit reconciliation returned a different session ID", ErrSessionProcessorConfiguration)
	}
	if snapshot.DeliveryState == string(store.LocalIntentNotDelivered) {
		response := commandMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation, RequestState: store.MailboxExchangeRejected,
			CommandID: previous.CommandID, SessionID: previous.SessionID, DeliveryState: snapshot.DeliveryState,
			Error: &mailboxResponseError{Code: "resource_not_found", Message: "command submission was proven not delivered"},
		}
		_, err := p.publishCommandResponse(ctx, record, response, nil)
		return err
	}
	if snapshot.State.IsTerminal() {
		response := commandResponseFromSnapshot(record.RequestID, record.Operation, snapshot)
		value := snapshot.AvailableEventSequence
		_, err := p.publishCommandResponse(ctx, record, response, &value)
		return err
	}
	if published, err := p.publishIndeterminateIfExpired(ctx, record, snapshot.DeliveryState); err != nil {
		return err
	} else if published {
		return nil
	}
	if snapshot.DeliveryState != previous.DeliveryState {
		response := commandMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation, RequestState: store.MailboxExchangeAccepted,
			CommandID: previous.CommandID, SessionID: previous.SessionID,
			DeliveryState: snapshot.DeliveryState,
			ObservedAt:    mailboxTime(snapshot.ObservedAt),
		}
		_, err := p.publishCommandResponse(ctx, record, response, nil)
		return err
	}
	return p.projector.Publish(ctx, record.RequestID)
}

func (p *SessionProcessor) reconcileAcceptedCancels(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchangesInMailbox(ctx, p.mailboxID, p.controller, "cancel_command", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.reconcileAcceptedCancel(ctx, record); err != nil {
			result = errors.Join(result, mailboxReconciliationIssue(record, "accepted_cancel", err))
		}
	}
	return result
}

// reconcileAcceptedCancel advances one accepted cancellation receipt
// independently so one corrupted or temporarily unreadable record does not
// prevent a later cancellation from reaching its durable mailbox response.
func (p *SessionProcessor) reconcileAcceptedCancel(ctx context.Context, record store.MailboxExchangeRecord) error {
	if len(record.ResponseBytes) == 0 {
		return nil
	}
	var previous commandMailboxResponse
	if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "cancel_command" || previous.CommandID == "" || previous.SessionID == "" {
		return fmt.Errorf("%w: accepted cancel response is corrupt", ErrOutboxResponse)
	}
	snapshot, err := p.operations.GetCancelCommandSnapshot(ctx, previous.CommandID, record.ExecutionIdempotencyKey)
	if err != nil {
		var operationErr *SessionOperationError
		if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
			return nil
		}
		return err
	}
	if snapshot.CommandID != previous.CommandID || snapshot.SessionID != previous.SessionID {
		return fmt.Errorf("%w: cancel reconciliation returned a different resource", ErrSessionProcessorConfiguration)
	}
	if snapshot.CommandState != "" && !domain.CommandState(snapshot.CommandState).Valid() {
		return fmt.Errorf("%w: cancel reconciliation returned invalid command state %q", ErrSessionProcessorConfiguration, snapshot.CommandState)
	}
	if !validDeliveryState(snapshot.CancelDeliveryState) || (snapshot.CommandDeliveryState != "" && !validDeliveryState(snapshot.CommandDeliveryState)) {
		return fmt.Errorf("%w: cancel reconciliation returned invalid delivery state", ErrSessionProcessorConfiguration)
	}
	if !domain.CommandState(snapshot.CommandState).IsTerminal() && snapshot.CommandDeliveryState != string(store.LocalIntentNotDelivered) {
		if published, err := p.publishIndeterminateIfExpired(ctx, record, snapshot.CancelDeliveryState); err != nil {
			return err
		} else if published {
			return nil
		}
	}
	response := commandMailboxResponse{
		RequestID: record.RequestID, Operation: record.Operation,
		CommandID: previous.CommandID, SessionID: previous.SessionID,
		DeliveryState: snapshot.CancelDeliveryState,
	}
	switch {
	case domain.CommandState(snapshot.CommandState).IsTerminal():
		response.RequestState = store.MailboxExchangeComplete
	case snapshot.CommandDeliveryState == string(store.LocalIntentNotDelivered):
		// The dispatcher atomically settled the original submit before it
		// could reach the target, so no authoritative command state exists.
		response.RequestState = store.MailboxExchangeComplete
		response.DeliveryState = string(store.LocalIntentNotDelivered)
	case snapshot.CancelDeliveryState == string(store.LocalIntentAccepted) || snapshot.CancelDeliveryState == string(store.LocalIntentReconciled):
		// Target acceptance confirms the cancellation request, not its eventual
		// effect on a command that may still be running.
		response.RequestState = store.MailboxExchangeComplete
	case snapshot.CancelDeliveryState == string(store.LocalIntentNotDelivered):
		response.RequestState = store.MailboxExchangeRejected
		response.Error = &mailboxResponseError{Code: "runtime_unavailable", Message: "cancellation request was proven not delivered"}
	default:
		response.RequestState = store.MailboxExchangeAccepted
	}
	if response.RequestState == store.MailboxExchangeAccepted && response.DeliveryState == previous.DeliveryState {
		return p.projector.Publish(ctx, record.RequestID)
	}
	_, err = p.publishCommandResponse(ctx, record, response, nil)
	return err
}

func (p *SessionProcessor) reconcileAcceptedCloses(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchangesInMailbox(ctx, p.mailboxID, p.controller, "close_session", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.reconcileAcceptedClose(ctx, record); err != nil {
			result = errors.Join(result, mailboxReconciliationIssue(record, "accepted_close", err))
		}
	}
	return result
}

// reconcileAcceptedClose advances one accepted close receipt independently so
// a malformed or temporarily unavailable record cannot starve a later close
// request in the same mailbox.
func (p *SessionProcessor) reconcileAcceptedClose(ctx context.Context, record store.MailboxExchangeRecord) error {
	if len(record.ResponseBytes) == 0 {
		return nil
	}
	var previous sessionMailboxResponse
	if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "close_session" || previous.SessionID == "" {
		return fmt.Errorf("%w: accepted close response is corrupt", ErrOutboxResponse)
	}
	snapshot, err := p.operations.GetCloseSessionSnapshot(ctx, previous.SessionID, record.ExecutionIdempotencyKey)
	if err != nil {
		var operationErr *SessionOperationError
		if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
			return nil
		}
		return err
	}
	if snapshot.SessionID != previous.SessionID || !validDeliveryState(snapshot.CloseDeliveryState) ||
		(snapshot.SessionDeliveryState != "" && !validDeliveryState(snapshot.SessionDeliveryState)) {
		return fmt.Errorf("%w: close reconciliation returned invalid identity or delivery state", ErrSessionProcessorConfiguration)
	}
	if snapshot.SessionState != "" && !domain.SessionState(snapshot.SessionState).Valid() {
		return fmt.Errorf("%w: close reconciliation returned invalid session state %q", ErrSessionProcessorConfiguration, snapshot.SessionState)
	}
	if !domain.SessionState(snapshot.SessionState).IsTerminal() && !(snapshot.SessionState == "" && snapshot.SessionDeliveryState == string(store.LocalIntentNotDelivered)) {
		if published, err := p.publishIndeterminateIfExpired(ctx, record, snapshot.CloseDeliveryState); err != nil {
			return err
		} else if published {
			return nil
		}
	}
	response := sessionMailboxResponse{
		RequestID: record.RequestID, Operation: record.Operation,
		SessionID: previous.SessionID, DeliveryState: snapshot.CloseDeliveryState,
	}
	switch {
	case domain.SessionState(snapshot.SessionState).IsTerminal():
		response.RequestState = store.MailboxExchangeComplete
		response.SessionState = snapshot.SessionState
		if snapshot.SessionState == string(domain.SessionStateLost) {
			response.TeardownOutcome = "lost"
		} else {
			response.TeardownOutcome = "closed"
		}
	case snapshot.SessionState == "" && snapshot.SessionDeliveryState == string(store.LocalIntentNotDelivered):
		// The create intent was proven never delivered; closure is a local
		// no-op and must not invent a target session state.
		response.RequestState = store.MailboxExchangeComplete
		response.DeliveryState = string(store.LocalIntentNotDelivered)
		response.TeardownOutcome = "not_created"
	case snapshot.CloseDeliveryState == string(store.LocalIntentAccepted) || snapshot.CloseDeliveryState == string(store.LocalIntentReconciled):
		response.RequestState = store.MailboxExchangeAccepted
	case snapshot.CloseDeliveryState == string(store.LocalIntentNotDelivered):
		response.RequestState = store.MailboxExchangeRejected
		response.Error = &mailboxResponseError{Code: "runtime_unavailable", Message: "close request was proven not delivered"}
	default:
		response.RequestState = store.MailboxExchangeAccepted
	}
	if response.RequestState == store.MailboxExchangeAccepted && response.DeliveryState == previous.DeliveryState {
		return p.projector.Publish(ctx, record.RequestID)
	}
	_, err = p.publish(ctx, record, response, nil)
	return err
}

func (p *SessionProcessor) reconcileAcceptedRuns(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchangesInMailbox(ctx, p.mailboxID, p.controller, "run", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.reconcileAcceptedRun(ctx, record); err != nil {
			result = errors.Join(result, mailboxReconciliationIssue(record, "accepted_run", err))
		}
	}
	return result
}

// reconcileAcceptedRun advances one accepted one-off receipt. Keeping this
// per-record lets a corrupted or temporarily unreadable receipt remain intact
// without preventing a later independent mailbox request from being projected.
func (p *SessionProcessor) reconcileAcceptedRun(ctx context.Context, record store.MailboxExchangeRecord) error {
	if len(record.ResponseBytes) == 0 {
		return nil
	}
	var previous runMailboxResponse
	if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "run" || previous.JobID == "" || previous.SessionID == "" || previous.CommandID == "" {
		return fmt.Errorf("%w: accepted run response is corrupt", ErrOutboxResponse)
	}
	snapshot, err := p.operations.GetRunSnapshot(ctx, previous.JobID)
	if err != nil {
		var operationErr *SessionOperationError
		if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
			return nil
		}
		return err
	}
	if snapshot.JobID != previous.JobID || snapshot.SessionID != previous.SessionID || snapshot.CommandID != previous.CommandID || !validDeliveryState(snapshot.DeliveryState) {
		return fmt.Errorf("%w: run reconciliation returned invalid identity or delivery state", ErrSessionProcessorConfiguration)
	}
	if (snapshot.RemoteStatusFailureAt == nil) != (snapshot.RemoteStatusFailureCode == "") ||
		(snapshot.RemoteStatusFailureAt != nil && (snapshot.RemoteStatusFailureAt.IsZero() || snapshot.RemoteStatusFailureCode != store.RemoteStatusFailureCodeUnavailable || snapshot.DeliveryState != string(store.LocalIntentAccepted))) {
		return fmt.Errorf("%w: run reconciliation returned an invalid remote status failure marker", ErrSessionProcessorConfiguration)
	}
	if snapshot.RemoteStatusFailureAt != nil {
		if published, err := p.publishAcceptedRemoteStatusUnavailableIfExpired(ctx, record, previous, *snapshot.RemoteStatusFailureAt); err != nil {
			return err
		} else if published {
			return nil
		}
	}
	if snapshot.Command != nil {
		commandID, idErr := domain.NewCommandID(previous.CommandID)
		if idErr != nil || ValidateCommandSnapshot(*snapshot.Command, commandID) != nil || string(snapshot.Command.SessionID) != previous.SessionID {
			return fmt.Errorf("%w: run reconciliation returned an invalid command snapshot", ErrSessionProcessorConfiguration)
		}
	}
	if snapshot.DeliveryState == string(store.LocalIntentNotDelivered) {
		if snapshot.JobPhase != "" || snapshot.Command != nil || snapshot.TeardownOutcome != "" {
			return fmt.Errorf("%w: never-delivered run contains authoritative job state", ErrSessionProcessorConfiguration)
		}
		response := runMailboxResponse{
			RequestID: record.RequestID, Operation: "run", RequestState: store.MailboxExchangeRejected,
			JobID: previous.JobID, SessionID: previous.SessionID, CommandID: previous.CommandID,
			DeliveryState: snapshot.DeliveryState,
			Error:         &mailboxResponseError{Code: "runtime_unavailable", Message: "run was proven not delivered"},
		}
		_, err := p.publishRunResponse(ctx, record, response, nil)
		return err
	}
	if (snapshot.JobPhase != "" && !validJobPhase(snapshot.JobPhase)) || (snapshot.TeardownOutcome != "" && !validTeardownOutcome(snapshot.TeardownOutcome)) ||
		(snapshot.JobPhase == "" && (snapshot.Command != nil || snapshot.TeardownOutcome != "")) ||
		(snapshot.JobPhase == string(store.JobPhaseComplete) && snapshot.Command == nil) {
		return fmt.Errorf("%w: run reconciliation returned an invalid job or teardown state", ErrSessionProcessorConfiguration)
	}
	if terminalJobPhase(snapshot.JobPhase) && snapshot.TeardownOutcome != "" && (snapshot.Command == nil || snapshot.Command.State.IsTerminal()) {
		response, cursor := runResponseFromSnapshot(record.RequestID, snapshot)
		_, err := p.publishRunResponse(ctx, record, response, cursor)
		return err
	}
	if published, err := p.publishIndeterminateIfExpired(ctx, record, snapshot.DeliveryState); err != nil {
		return err
	} else if published {
		return nil
	}
	if snapshot.DeliveryState == previous.DeliveryState {
		return p.projector.Publish(ctx, record.RequestID)
	}
	response := runMailboxResponse{
		RequestID: record.RequestID, Operation: "run", RequestState: store.MailboxExchangeAccepted,
		JobID: previous.JobID, SessionID: previous.SessionID, CommandID: previous.CommandID,
		DeliveryState: snapshot.DeliveryState, ObservedAt: mailboxTime(snapshot.ObservedAt),
	}
	_, err = p.publishRunResponse(ctx, record, response, nil)
	return err
}

func runResponseFromSnapshot(requestID string, snapshot RunSnapshot) (runMailboxResponse, *int64) {
	response := runMailboxResponse{
		RequestID: requestID, Operation: "run", RequestState: store.MailboxExchangeComplete,
		JobID: snapshot.JobID, SessionID: snapshot.SessionID, CommandID: snapshot.CommandID,
		JobPhase: snapshot.JobPhase, DeliveryState: snapshot.DeliveryState,
		TeardownOutcome: snapshot.TeardownOutcome, ObservedAt: mailboxTime(snapshot.ObservedAt),
	}
	if snapshot.Command == nil {
		return response, nil
	}
	command := snapshot.Command
	response.CommandState = string(command.State)
	response.ExitCode = command.ExitCode
	response.Stdout = command.StdoutPreview
	response.Stderr = command.StderrPreview
	response.FinalEventSequence = command.FinalEventSequence
	response.AvailableEventSequence = int64PointerMailbox(command.AvailableEventSequence)
	response.OutputComplete = boolPointerMailbox(command.OutputComplete)
	response.OutputTruncated = boolPointerMailbox(command.OutputTruncated)
	response.OutputUnavailableReason = command.OutputUnavailableReason
	response.EventsFile = command.EventsFile
	if command.AvailableEventSequence > 0 {
		cursor := command.AvailableEventSequence
		return response, &cursor
	}
	return response, nil
}

func boolPointerMailbox(value bool) *bool { return &value }

func validJobPhase(value string) bool {
	return store.JobPhase(value).Valid()
}

func terminalJobPhase(value string) bool {
	phase := store.JobPhase(value)
	return phase == store.JobPhaseComplete || phase == store.JobPhaseFailed || phase == store.JobPhaseLost
}

func validTeardownOutcome(value string) bool {
	switch value {
	case "closed", "not_created", "lost":
		return true
	default:
		return false
	}
}

func validDeliveryState(state string) bool {
	switch store.LocalIntentDeliveryState(state) {
	case store.LocalIntentRecorded, store.LocalIntentDispatching, store.LocalIntentUncertain,
		store.LocalIntentAccepted, store.LocalIntentReconciled, store.LocalIntentNotDelivered:
		return true
	default:
		return false
	}
}

// publishIndeterminateIfExpired freezes a remote mutation response after its
// durable uncertain transition has exceeded the configured reconciliation
// window. It deliberately publishes only stable intent IDs and uncertainty;
// the mailbox never guesses target acceptance, command state, or output.
func (p *SessionProcessor) publishIndeterminateIfExpired(ctx context.Context, record store.MailboxExchangeRecord, deliveryState string) (bool, error) {
	if deliveryState != string(store.LocalIntentUncertain) {
		return false, nil
	}
	intent, err := p.authority.GetLocalIntentByIdempotency(ctx, record.Operation, record.ExecutionIdempotencyKey, record.Controller)
	if err != nil {
		return false, fmt.Errorf("%w: load uncertain mailbox intent: %v", ErrSessionProcessorConfiguration, err)
	}
	if intent.Target.Kind() != domain.TargetKindRemote || intent.DeliveryState != store.LocalIntentUncertain {
		return false, nil
	}
	lifecycle, err := p.authority.ListLocalIntentLifecycle(ctx, intent.IntentID)
	if err != nil {
		return false, fmt.Errorf("%w: read uncertain mailbox lifecycle: %v", ErrSessionProcessorConfiguration, err)
	}
	var uncertainAt time.Time
	for index := len(lifecycle) - 1; index >= 0; index-- {
		if lifecycle[index].NewState == store.LocalIntentUncertain {
			uncertainAt = lifecycle[index].OccurredAt.UTC()
			break
		}
	}
	if uncertainAt.IsZero() {
		return false, fmt.Errorf("%w: uncertain intent has no durable uncertainty transition", ErrSessionProcessorConfiguration)
	}
	if p.now().UTC().Before(uncertainAt.Add(p.uncertaintyWindow)) {
		return false, nil
	}

	switch record.Operation {
	case "create_session":
		var previous sessionMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.SessionID == "" {
			return false, fmt.Errorf("%w: uncertain create response is corrupt", ErrOutboxResponse)
		}
		response := sessionMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation,
			RequestState: store.MailboxExchangeIndeterminate, SessionID: previous.SessionID,
			DeliveryState: string(store.LocalIntentUncertain),
		}
		_, err = p.publish(ctx, record, response, nil)
	case "submit_command", "cancel_command":
		var previous commandMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.CommandID == "" || previous.SessionID == "" {
			return false, fmt.Errorf("%w: uncertain command response is corrupt", ErrOutboxResponse)
		}
		response := commandMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation,
			RequestState: store.MailboxExchangeIndeterminate,
			CommandID:    previous.CommandID, SessionID: previous.SessionID,
			DeliveryState: string(store.LocalIntentUncertain),
		}
		_, err = p.publishCommandResponse(ctx, record, response, nil)
	case "close_session":
		var previous sessionMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.SessionID == "" {
			return false, fmt.Errorf("%w: uncertain close response is corrupt", ErrOutboxResponse)
		}
		response := sessionMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation,
			RequestState: store.MailboxExchangeIndeterminate, SessionID: previous.SessionID,
			DeliveryState: string(store.LocalIntentUncertain),
		}
		_, err = p.publish(ctx, record, response, nil)
	case "run":
		var previous runMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.JobID == "" || previous.SessionID == "" || previous.CommandID == "" {
			return false, fmt.Errorf("%w: uncertain run response is corrupt", ErrOutboxResponse)
		}
		response := runMailboxResponse{
			RequestID: record.RequestID, Operation: record.Operation,
			RequestState: store.MailboxExchangeIndeterminate,
			JobID:        previous.JobID, SessionID: previous.SessionID, CommandID: previous.CommandID,
			DeliveryState: string(store.LocalIntentUncertain),
		}
		_, err = p.publishRunResponse(ctx, record, response, nil)
	default:
		return false, fmt.Errorf("%w: unsupported uncertain mailbox mutation %q", ErrSessionProcessorConfiguration, record.Operation)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// publishAcceptedRemoteStatusUnavailableIfExpired freezes a distinct bounded
// outcome: target acceptance is known, but read-only status reconciliation
// could not establish a terminal result. It carries stable IDs only and never
// guesses job, command, teardown, output, or event state.
func (p *SessionProcessor) publishAcceptedRemoteStatusUnavailableIfExpired(ctx context.Context, record store.MailboxExchangeRecord, previous runMailboxResponse, firstObservedAt time.Time) (bool, error) {
	if p.now().UTC().Before(firstObservedAt.UTC().Add(p.uncertaintyWindow)) {
		return false, nil
	}
	response := runMailboxResponse{
		RequestID: record.RequestID, Operation: "run", RequestState: store.MailboxExchangeIndeterminate,
		JobID: previous.JobID, SessionID: previous.SessionID, CommandID: previous.CommandID,
		DeliveryState: string(store.LocalIntentAccepted),
		Error: &mailboxResponseError{
			Code:      store.RemoteStatusFailureCodeUnavailable,
			Message:   "remote target accepted the request but its terminal status could not be verified",
			Retryable: false,
		},
	}
	response.InboxID = p.mailboxID
	applyRunResponseSelection(&response, record.Selection)
	response.ResponseRevision = record.ResponseRevision + 1
	if record.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode accepted remote status failure response: %v", ErrOutboxResponse, err)
	}
	ref, err := p.recordRef(record)
	if err != nil {
		return false, err
	}
	updated, err := p.authority.PublishAcceptedRemoteStatusUnavailableInMailbox(ctx, ref, store.MailboxResponsePublication{
		State: store.MailboxExchangeIndeterminate, Bytes: responseBytes,
	})
	if errors.Is(err, store.ErrMailboxRemoteStatusFailureNotEligible) {
		return false, nil
	}
	if errors.Is(err, store.ErrMailboxTerminalImmutable) {
		if publishErr := p.projector.Publish(ctx, record.RequestID); publishErr != nil {
			return false, publishErr
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err := p.projector.Publish(ctx, record.RequestID); err != nil {
		return false, err
	}
	return updated.ResponseRevision > 0, nil
}

func (p *SessionProcessor) publish(ctx context.Context, current store.MailboxExchangeRecord, response sessionMailboxResponse, cursor *int64) (bool, error) {
	response.InboxID = p.mailboxID
	applySessionResponseSelection(&response, current.Selection)
	response.ResponseRevision = current.ResponseRevision + 1
	if current.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode session response: %v", ErrOutboxResponse, err)
	}
	ref, err := p.recordRef(current)
	if err != nil {
		return false, err
	}
	updated, err := p.authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{
		State: response.RequestState, Bytes: responseBytes, AvailableEventSequence: cursor,
	})
	if err != nil {
		return false, err
	}
	if err := p.projector.Publish(ctx, current.RequestID); err != nil {
		return false, err
	}
	return updated.ResponseRevision > 0, nil
}

func (p *SessionProcessor) publishCommandResponse(ctx context.Context, current store.MailboxExchangeRecord, response commandMailboxResponse, cursor *int64) (bool, error) {
	response.InboxID = p.mailboxID
	response.ResponseRevision = current.ResponseRevision + 1
	if current.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode command response: %v", ErrOutboxResponse, err)
	}
	ref, err := p.recordRef(current)
	if err != nil {
		return false, err
	}
	updated, err := p.authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{
		State: response.RequestState, Bytes: responseBytes, AvailableEventSequence: cursor,
	})
	if err != nil {
		return false, err
	}
	if cursor != nil && *cursor > 0 {
		if err := p.projector.PublishCommand(ctx, current.RequestID, domain.CommandID(response.CommandID)); err != nil {
			return false, err
		}
	} else if err := p.projector.Publish(ctx, current.RequestID); err != nil {
		return false, err
	}
	return updated.ResponseRevision > 0, nil
}

func (p *SessionProcessor) publishRunResponse(ctx context.Context, current store.MailboxExchangeRecord, response runMailboxResponse, cursor *int64) (bool, error) {
	response.InboxID = p.mailboxID
	applyRunResponseSelection(&response, current.Selection)
	response.ResponseRevision = current.ResponseRevision + 1
	if current.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode run response: %v", ErrOutboxResponse, err)
	}
	ref, err := p.recordRef(current)
	if err != nil {
		return false, err
	}
	updated, err := p.authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{
		State: response.RequestState, Bytes: responseBytes, AvailableEventSequence: cursor,
	})
	if err != nil {
		return false, err
	}
	if cursor != nil && *cursor > 0 {
		commandID, idErr := domain.NewCommandID(response.CommandID)
		if idErr != nil {
			return false, fmt.Errorf("%w: run response command ID: %v", ErrOutboxResponse, idErr)
		}
		if err := p.projector.PublishCommand(ctx, current.RequestID, commandID); err != nil {
			return false, err
		}
	} else if err := p.projector.Publish(ctx, current.RequestID); err != nil {
		return false, err
	}
	return updated.ResponseRevision > 0, nil
}

type sessionMailboxResponse struct {
	InboxID                  string                     `json:"inbox_id,omitempty"`
	RequestID                string                     `json:"request_id"`
	Operation                string                     `json:"operation"`
	RequestState             store.MailboxExchangeState `json:"request_state"`
	ResponseRevision         int64                      `json:"response_revision"`
	IdempotencyWarning       string                     `json:"idempotency_warning,omitempty"`
	SessionID                string                     `json:"session_id,omitempty"`
	SessionState             string                     `json:"session_state,omitempty"`
	DeliveryState            string                     `json:"delivery_state,omitempty"`
	ObservedAt               *time.Time                 `json:"observed_at,omitempty"`
	TeardownOutcome          string                     `json:"teardown_outcome,omitempty"`
	ExecutionSelectionSource string                     `json:"execution_selection_source,omitempty"`
	ResolvedEnvironment      string                     `json:"resolved_environment,omitempty"`
	ResolvedExecutionTarget  *mailboxResponseTarget     `json:"resolved_execution_target,omitempty"`
	Error                    *mailboxResponseError      `json:"error,omitempty"`
}

type mailboxResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type commandMailboxResponse struct {
	InboxID                 string                     `json:"inbox_id,omitempty"`
	RequestID               string                     `json:"request_id"`
	Operation               string                     `json:"operation"`
	RequestState            store.MailboxExchangeState `json:"request_state"`
	ResponseRevision        int64                      `json:"response_revision"`
	IdempotencyWarning      string                     `json:"idempotency_warning,omitempty"`
	CommandID               string                     `json:"command_id,omitempty"`
	SessionID               string                     `json:"session_id,omitempty"`
	DeliveryState           string                     `json:"delivery_state,omitempty"`
	CommandState            string                     `json:"command_state,omitempty"`
	ObservedAt              *time.Time                 `json:"observed_at,omitempty"`
	ExitCode                *int                       `json:"exit_code,omitempty"`
	Stdout                  string                     `json:"stdout,omitempty"`
	Stderr                  string                     `json:"stderr,omitempty"`
	FinalEventSequence      *int64                     `json:"final_event_sequence,omitempty"`
	AvailableEventSequence  *int64                     `json:"available_event_sequence,omitempty"`
	OutputComplete          *bool                      `json:"output_complete,omitempty"`
	OutputTruncated         *bool                      `json:"output_truncated,omitempty"`
	OutputUnavailableReason string                     `json:"output_unavailable_reason,omitempty"`
	EventsFile              string                     `json:"events_file,omitempty"`
	Error                   *mailboxResponseError      `json:"error,omitempty"`
}

type runMailboxResponse struct {
	InboxID                  string                     `json:"inbox_id,omitempty"`
	RequestID                string                     `json:"request_id"`
	Operation                string                     `json:"operation"`
	RequestState             store.MailboxExchangeState `json:"request_state"`
	ResponseRevision         int64                      `json:"response_revision"`
	IdempotencyWarning       string                     `json:"idempotency_warning,omitempty"`
	JobID                    string                     `json:"job_id,omitempty"`
	JobPhase                 string                     `json:"job_phase,omitempty"`
	CommandID                string                     `json:"command_id,omitempty"`
	SessionID                string                     `json:"session_id,omitempty"`
	DeliveryState            string                     `json:"delivery_state,omitempty"`
	CommandState             string                     `json:"command_state,omitempty"`
	ObservedAt               *time.Time                 `json:"observed_at,omitempty"`
	ExitCode                 *int                       `json:"exit_code,omitempty"`
	Stdout                   string                     `json:"stdout,omitempty"`
	Stderr                   string                     `json:"stderr,omitempty"`
	FinalEventSequence       *int64                     `json:"final_event_sequence,omitempty"`
	AvailableEventSequence   *int64                     `json:"available_event_sequence,omitempty"`
	OutputComplete           *bool                      `json:"output_complete,omitempty"`
	OutputTruncated          *bool                      `json:"output_truncated,omitempty"`
	OutputUnavailableReason  string                     `json:"output_unavailable_reason,omitempty"`
	EventsFile               string                     `json:"events_file,omitempty"`
	TeardownOutcome          string                     `json:"teardown_outcome,omitempty"`
	ExecutionSelectionSource string                     `json:"execution_selection_source,omitempty"`
	ResolvedEnvironment      string                     `json:"resolved_environment,omitempty"`
	ResolvedExecutionTarget  *mailboxResponseTarget     `json:"resolved_execution_target,omitempty"`
	Error                    *mailboxResponseError      `json:"error,omitempty"`
}

type mailboxResponseTarget struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

func applySessionResponseSelection(response *sessionMailboxResponse, selection *store.MailboxExecutionSelection) {
	if response == nil || selection == nil {
		return
	}
	response.ExecutionSelectionSource = selection.Source
	response.ResolvedEnvironment = selection.Environment
	response.ResolvedExecutionTarget = &mailboxResponseTarget{Kind: string(selection.Target.Kind()), Profile: selection.Target.Profile()}
}

func applyRunResponseSelection(response *runMailboxResponse, selection *store.MailboxExecutionSelection) {
	if response == nil || selection == nil {
		return
	}
	response.ExecutionSelectionSource = selection.Source
	response.ResolvedEnvironment = selection.Environment
	response.ResolvedExecutionTarget = &mailboxResponseTarget{Kind: string(selection.Target.Kind()), Profile: selection.Target.Profile()}
}

func commandResponseFromSnapshot(requestID, operation string, snapshot CommandSnapshot) commandMailboxResponse {
	response := commandMailboxResponse{
		RequestID: requestID, Operation: operation, RequestState: store.MailboxExchangeComplete,
		CommandID: string(snapshot.CommandID), SessionID: string(snapshot.SessionID),
		DeliveryState: snapshot.DeliveryState, ObservedAt: mailboxTime(snapshot.ObservedAt),
	}
	if snapshot.State == "" {
		return response
	}
	state := string(snapshot.State)
	complete, truncated := snapshot.OutputComplete, snapshot.OutputTruncated
	response.CommandState = state
	response.ExitCode = snapshot.ExitCode
	response.Stdout = snapshot.StdoutPreview
	response.Stderr = snapshot.StderrPreview
	response.FinalEventSequence = snapshot.FinalEventSequence
	response.AvailableEventSequence = int64PointerMailbox(snapshot.AvailableEventSequence)
	response.OutputComplete = &complete
	response.OutputTruncated = &truncated
	response.OutputUnavailableReason = snapshot.OutputUnavailableReason
	response.EventsFile = snapshot.EventsFile
	return response
}

func int64PointerMailbox(value int64) *int64 { return &value }

func responseFromSnapshot(requestID, operation string, state store.MailboxExchangeState, snapshot SessionSnapshot) sessionMailboxResponse {
	return sessionMailboxResponse{
		RequestID: requestID, Operation: operation, RequestState: state,
		SessionID: snapshot.SessionID, SessionState: snapshot.SessionState,
		DeliveryState: snapshot.DeliveryState, ObservedAt: mailboxTime(snapshot.ObservedAt),
	}
}

func mailboxTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func safeSessionMailboxError(operationErr *SessionOperationError) *mailboxResponseError {
	code := operationErr.Code
	switch code {
	case "invalid_request", "environment_target_mismatch", "environment_forbidden", "controller_mismatch", "session_not_ready", "idempotency_conflict", "resource_not_found", "quota_exceeded", "runtime_unavailable", "transport_uncertain", "event_history_unavailable", "retention_expired":
	default:
		code = "runtime_unavailable"
	}
	message := operationErr.Message
	if message == "" {
		message = "session operation failed"
	}
	return &mailboxResponseError{Code: code, Message: message, Retryable: operationErr.Retryable}
}
