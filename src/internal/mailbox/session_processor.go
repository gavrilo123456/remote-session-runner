package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var ErrSessionProcessorConfiguration = errors.New("mailbox session processor configuration is invalid")

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
	JobID           string
	SessionID       string
	CommandID       string
	DeliveryState   string
	JobPhase        string
	Command         *CommandSnapshot
	TeardownOutcome string
	ObservedAt      time.Time
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

type SessionProcessorOptions struct {
	Importer   *Importer
	Authority  *store.AuthorityStore
	Controller domain.ControllerIdentity
	Operations SessionOperations
	Outbox     *Outbox
	EventFiles *EventFiles
}

// SessionProcessor wires file mailbox session and command operations through
// the Mac local API boundary. The mailbox remains an ingress/projection only.
type SessionProcessor struct {
	importer   *Importer
	authority  *store.AuthorityStore
	controller domain.ControllerIdentity
	operations SessionOperations
	projector  Projector
	mu         sync.Mutex
}

func NewSessionProcessor(options SessionProcessorOptions) (*SessionProcessor, error) {
	if options.Importer == nil || options.Authority == nil || options.Operations == nil || options.Outbox == nil || options.EventFiles == nil {
		return nil, ErrSessionProcessorConfiguration
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
		importer: options.Importer, authority: options.Authority, controller: controller,
		operations: options.Operations, projector: Projector{Authority: options.Authority, Outbox: options.Outbox, EventFiles: options.EventFiles},
	}, nil
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
	results, err := p.importer.importWithRecorder(ctx, p.process)
	if err != nil {
		return results, err
	}
	if err := p.reconcileAcceptedCreates(ctx); err != nil {
		return results, err
	}
	if err := p.reconcileAcceptedSubmits(ctx); err != nil {
		return results, err
	}
	if err := p.reconcileAcceptedCancels(ctx); err != nil {
		return results, err
	}
	if err := p.reconcileAcceptedCloses(ctx); err != nil {
		return results, err
	}
	if err := p.reconcileAcceptedRuns(ctx); err != nil {
		return results, err
	}
	return results, nil
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
	if err := p.reconcileAcceptedCreates(ctx); err != nil {
		return err
	}
	if err := p.reconcileAcceptedSubmits(ctx); err != nil {
		return err
	}
	if err := p.reconcileAcceptedCancels(ctx); err != nil {
		return err
	}
	if err := p.reconcileAcceptedCloses(ctx); err != nil {
		return err
	}
	return p.reconcileAcceptedRuns(ctx)
}

func (p *SessionProcessor) process(ctx context.Context, request Request) (bool, error) {
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
	if request.Operation != "create_session" && request.Operation != "get_session" {
		return false, fmt.Errorf("%w: unsupported mailbox operation %q", ErrMailboxInput, request.Operation)
	}
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return false, err
	}
	record, duplicate, idempotencyConflict, err := p.authority.AcceptMailboxExchangeWithConflictReceipt(ctx, store.MailboxExchangeCreate{
		RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
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
	if request.Operation == "create_session" {
		intent, err := p.operations.CreateSessionIntent(ctx, request)
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

func (p *SessionProcessor) processCommand(ctx context.Context, request Request) (bool, error) {
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return false, err
	}
	record, duplicate, idempotencyConflict, err := p.authority.AcceptMailboxExchangeWithConflictReceipt(ctx, store.MailboxExchangeCreate{
		RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		return true, p.publishStoredCommandResponse(ctx, record)
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
		intent, err := p.operations.SubmitCommandIntent(ctx, request)
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
	intent, err := p.operations.CancelCommandIntent(ctx, request)
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
	intent, err := p.operations.CloseSessionIntent(ctx, request)
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
	record, duplicate, idempotencyConflict, err := p.acceptMutationExchange(ctx, request)
	if err != nil {
		return false, err
	}
	if duplicate && len(record.ResponseBytes) > 0 {
		return true, p.publishStoredRunResponse(ctx, record)
	}
	if idempotencyConflict || (record.IdempotencyKey != "" && !record.IdempotencyBindingActive) {
		response := runMailboxResponse{
			RequestID: request.RequestID, Operation: request.Operation,
			RequestState: store.MailboxExchangeRejected,
			Error:        &mailboxResponseError{Code: "idempotency_conflict", Message: "idempotency key is already bound to a different request"},
		}
		return p.publishRunResponse(ctx, record, response, nil)
	}
	if record.State != store.MailboxExchangeAccepted {
		return false, fmt.Errorf("%w: terminal run exchange has no response snapshot", ErrOutboxResponse)
	}
	intent, err := p.operations.RunJobIntent(ctx, request)
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

func (p *SessionProcessor) acceptMutationExchange(ctx context.Context, request Request) (store.MailboxExchangeRecord, bool, bool, error) {
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return store.MailboxExchangeRecord{}, false, false, err
	}
	return p.authority.AcceptMailboxExchangeWithConflictReceipt(ctx, store.MailboxExchangeCreate{
		RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
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

func (p *SessionProcessor) publishStoredCommandResponse(ctx context.Context, record store.MailboxExchangeRecord) error {
	if record.AvailableEventSequence != nil && *record.AvailableEventSequence > 0 {
		var response commandMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &response); err != nil || response.CommandID == "" {
			return fmt.Errorf("%w: stored command response is invalid", ErrOutboxResponse)
		}
		commandID, err := domain.NewCommandID(response.CommandID)
		if err != nil {
			return fmt.Errorf("%w: stored command response ID: %v", ErrOutboxResponse, err)
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
	records, err := p.authority.ListMailboxExchanges(ctx, p.controller, "create_session", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(record.ResponseBytes) == 0 {
			continue
		}
		var previous sessionMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "create_session" || previous.SessionID == "" {
			return fmt.Errorf("%w: accepted create response is corrupt", ErrOutboxResponse)
		}
		snapshot, err := p.operations.GetSession(ctx, previous.SessionID)
		if err != nil {
			var operationErr *SessionOperationError
			if errors.As(err, &operationErr) && operationErr.Retryable {
				continue
			}
			// A missing local intent here does not prove that a queued remote
			// create was never delivered. Keep the receipt unresolved.
			if errors.As(err, &operationErr) && operationErr.Code == "resource_not_found" {
				continue
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
			if _, err := p.publish(ctx, record, response, nil); err != nil {
				return err
			}
			continue
		}
		if snapshot.SessionState != "" && snapshot.SessionState != string(domain.SessionStateRequested) && snapshot.SessionState != string(domain.SessionStateCreating) {
			response := responseFromSnapshot(record.RequestID, record.Operation, store.MailboxExchangeComplete, snapshot)
			if _, err := p.publish(ctx, record, response, nil); err != nil {
				return err
			}
			continue
		}
		if snapshot.DeliveryState != "" && snapshot.DeliveryState != previous.DeliveryState {
			response := sessionMailboxResponse{
				RequestID: record.RequestID, Operation: record.Operation,
				RequestState: store.MailboxExchangeAccepted, SessionID: snapshot.SessionID,
				DeliveryState: snapshot.DeliveryState,
				ObservedAt:    mailboxTime(snapshot.ObservedAt),
			}
			if _, err := p.publish(ctx, record, response, nil); err != nil {
				return err
			}
			continue
		}
		if err := p.projector.Publish(ctx, record.RequestID); err != nil {
			return err
		}
	}
	return nil
}

func (p *SessionProcessor) reconcileAcceptedSubmits(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchanges(ctx, p.controller, "submit_command", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(record.ResponseBytes) == 0 {
			continue
		}
		var previous commandMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "submit_command" || previous.CommandID == "" || previous.SessionID == "" {
			return fmt.Errorf("%w: accepted submit response is corrupt", ErrOutboxResponse)
		}
		snapshot, err := p.operations.GetCommandSnapshot(ctx, previous.CommandID)
		if err != nil {
			var operationErr *SessionOperationError
			if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
				continue
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
			if _, err := p.publishCommandResponse(ctx, record, response, nil); err != nil {
				return err
			}
			continue
		}
		if snapshot.State.IsTerminal() {
			response := commandResponseFromSnapshot(record.RequestID, record.Operation, snapshot)
			var cursor *int64
			value := snapshot.AvailableEventSequence
			cursor = &value
			if _, err := p.publishCommandResponse(ctx, record, response, cursor); err != nil {
				return err
			}
			continue
		}
		if snapshot.DeliveryState != previous.DeliveryState {
			response := commandMailboxResponse{
				RequestID: record.RequestID, Operation: record.Operation, RequestState: store.MailboxExchangeAccepted,
				CommandID: previous.CommandID, SessionID: previous.SessionID,
				DeliveryState: snapshot.DeliveryState,
				ObservedAt:    mailboxTime(snapshot.ObservedAt),
			}
			if _, err := p.publishCommandResponse(ctx, record, response, nil); err != nil {
				return err
			}
			continue
		}
		if err := p.projector.Publish(ctx, record.RequestID); err != nil {
			return err
		}
	}
	return nil
}

func (p *SessionProcessor) reconcileAcceptedCancels(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchanges(ctx, p.controller, "cancel_command", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(record.ResponseBytes) == 0 {
			continue
		}
		var previous commandMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "cancel_command" || previous.CommandID == "" || previous.SessionID == "" {
			return fmt.Errorf("%w: accepted cancel response is corrupt", ErrOutboxResponse)
		}
		snapshot, err := p.operations.GetCancelCommandSnapshot(ctx, previous.CommandID, record.IdempotencyKey)
		if err != nil {
			var operationErr *SessionOperationError
			if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
				continue
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
			if err := p.projector.Publish(ctx, record.RequestID); err != nil {
				return err
			}
			continue
		}
		if _, err := p.publishCommandResponse(ctx, record, response, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *SessionProcessor) reconcileAcceptedCloses(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchanges(ctx, p.controller, "close_session", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(record.ResponseBytes) == 0 {
			continue
		}
		var previous sessionMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "close_session" || previous.SessionID == "" {
			return fmt.Errorf("%w: accepted close response is corrupt", ErrOutboxResponse)
		}
		snapshot, err := p.operations.GetCloseSessionSnapshot(ctx, previous.SessionID, record.IdempotencyKey)
		if err != nil {
			var operationErr *SessionOperationError
			if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
				continue
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
			if err := p.projector.Publish(ctx, record.RequestID); err != nil {
				return err
			}
			continue
		}
		if _, err := p.publish(ctx, record, response, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *SessionProcessor) reconcileAcceptedRuns(ctx context.Context) error {
	records, err := p.authority.ListMailboxExchanges(ctx, p.controller, "run", store.MailboxExchangeAccepted)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(record.ResponseBytes) == 0 {
			continue
		}
		var previous runMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &previous); err != nil || previous.RequestID != record.RequestID || previous.Operation != "run" || previous.JobID == "" || previous.SessionID == "" || previous.CommandID == "" {
			return fmt.Errorf("%w: accepted run response is corrupt", ErrOutboxResponse)
		}
		snapshot, err := p.operations.GetRunSnapshot(ctx, previous.JobID)
		if err != nil {
			var operationErr *SessionOperationError
			if errors.As(err, &operationErr) && (operationErr.Retryable || operationErr.Code == "resource_not_found") {
				continue
			}
			return err
		}
		if snapshot.JobID != previous.JobID || snapshot.SessionID != previous.SessionID || snapshot.CommandID != previous.CommandID || !validDeliveryState(snapshot.DeliveryState) {
			return fmt.Errorf("%w: run reconciliation returned invalid identity or delivery state", ErrSessionProcessorConfiguration)
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
				RequestID: record.RequestID, Operation: "run", RequestState: store.MailboxExchangeComplete,
				JobID: previous.JobID, SessionID: previous.SessionID, CommandID: previous.CommandID,
				DeliveryState: snapshot.DeliveryState, TeardownOutcome: "not_created",
			}
			if _, err := p.publishRunResponse(ctx, record, response, nil); err != nil {
				return err
			}
			continue
		}
		if (snapshot.JobPhase != "" && !validJobPhase(snapshot.JobPhase)) || (snapshot.TeardownOutcome != "" && !validTeardownOutcome(snapshot.TeardownOutcome)) ||
			(snapshot.JobPhase == "" && (snapshot.Command != nil || snapshot.TeardownOutcome != "")) ||
			(snapshot.JobPhase == string(store.JobPhaseComplete) && snapshot.Command == nil) {
			return fmt.Errorf("%w: run reconciliation returned an invalid job or teardown state", ErrSessionProcessorConfiguration)
		}
		if terminalJobPhase(snapshot.JobPhase) && snapshot.TeardownOutcome != "" && (snapshot.Command == nil || snapshot.Command.State.IsTerminal()) {
			response, cursor := runResponseFromSnapshot(record.RequestID, snapshot)
			if _, err := p.publishRunResponse(ctx, record, response, cursor); err != nil {
				return err
			}
			continue
		}
		if snapshot.DeliveryState == previous.DeliveryState {
			if err := p.projector.Publish(ctx, record.RequestID); err != nil {
				return err
			}
			continue
		}
		response := runMailboxResponse{
			RequestID: record.RequestID, Operation: "run", RequestState: store.MailboxExchangeAccepted,
			JobID: previous.JobID, SessionID: previous.SessionID, CommandID: previous.CommandID,
			DeliveryState: snapshot.DeliveryState, ObservedAt: mailboxTime(snapshot.ObservedAt),
		}
		if _, err := p.publishRunResponse(ctx, record, response, nil); err != nil {
			return err
		}
	}
	return nil
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

func (p *SessionProcessor) publish(ctx context.Context, current store.MailboxExchangeRecord, response sessionMailboxResponse, cursor *int64) (bool, error) {
	response.ResponseRevision = current.ResponseRevision + 1
	if current.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode session response: %v", ErrOutboxResponse, err)
	}
	updated, err := p.authority.PublishMailboxResponse(ctx, current.RequestID, store.MailboxResponsePublication{
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
	response.ResponseRevision = current.ResponseRevision + 1
	if current.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode command response: %v", ErrOutboxResponse, err)
	}
	updated, err := p.authority.PublishMailboxResponse(ctx, current.RequestID, store.MailboxResponsePublication{
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
	response.ResponseRevision = current.ResponseRevision + 1
	if current.DeduplicationWarning {
		response.IdempotencyWarning = "deduplication_not_guaranteed"
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return false, fmt.Errorf("%w: encode run response: %v", ErrOutboxResponse, err)
	}
	updated, err := p.authority.PublishMailboxResponse(ctx, current.RequestID, store.MailboxResponsePublication{
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

func (p *SessionProcessor) publishStoredRunResponse(ctx context.Context, record store.MailboxExchangeRecord) error {
	if record.AvailableEventSequence != nil && *record.AvailableEventSequence > 0 {
		var response runMailboxResponse
		if err := json.Unmarshal(record.ResponseBytes, &response); err != nil || response.CommandID == "" {
			return fmt.Errorf("%w: stored run response is invalid", ErrOutboxResponse)
		}
		commandID, err := domain.NewCommandID(response.CommandID)
		if err != nil {
			return fmt.Errorf("%w: stored run command ID: %v", ErrOutboxResponse, err)
		}
		return p.projector.PublishCommand(ctx, record.RequestID, commandID)
	}
	return p.projector.Publish(ctx, record.RequestID)
}

type sessionMailboxResponse struct {
	RequestID          string                     `json:"request_id"`
	Operation          string                     `json:"operation"`
	RequestState       store.MailboxExchangeState `json:"request_state"`
	ResponseRevision   int64                      `json:"response_revision"`
	IdempotencyWarning string                     `json:"idempotency_warning,omitempty"`
	SessionID          string                     `json:"session_id,omitempty"`
	SessionState       string                     `json:"session_state,omitempty"`
	DeliveryState      string                     `json:"delivery_state,omitempty"`
	ObservedAt         *time.Time                 `json:"observed_at,omitempty"`
	TeardownOutcome    string                     `json:"teardown_outcome,omitempty"`
	Error              *mailboxResponseError      `json:"error,omitempty"`
}

type mailboxResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type commandMailboxResponse struct {
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
	RequestID               string                     `json:"request_id"`
	Operation               string                     `json:"operation"`
	RequestState            store.MailboxExchangeState `json:"request_state"`
	ResponseRevision        int64                      `json:"response_revision"`
	IdempotencyWarning      string                     `json:"idempotency_warning,omitempty"`
	JobID                   string                     `json:"job_id,omitempty"`
	JobPhase                string                     `json:"job_phase,omitempty"`
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
	TeardownOutcome         string                     `json:"teardown_outcome,omitempty"`
	Error                   *mailboxResponseError      `json:"error,omitempty"`
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
