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

// Import records and projects the implemented file-only session and command
// operations, then reconciles accepted asynchronous create/submit intents.
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
	return results, nil
}

// Reconcile advances accepted create_session responses after the local intent
// or remote projection changes. It is safe to call after restart.
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
	return p.reconcileAcceptedSubmits(ctx)
}

func (p *SessionProcessor) process(ctx context.Context, request Request) (bool, error) {
	if request.Operation == "submit_command" || request.Operation == "get_command" {
		return p.processCommand(ctx, request)
	}
	if request.Operation != "create_session" && request.Operation != "get_session" {
		return false, fmt.Errorf("%w: operation %q is outside P095", ErrMailboxInput, request.Operation)
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
	FinalEventSequence      *int64                     `json:"final_event_sequence,omitempty"`
	AvailableEventSequence  *int64                     `json:"available_event_sequence,omitempty"`
	OutputComplete          *bool                      `json:"output_complete,omitempty"`
	OutputTruncated         *bool                      `json:"output_truncated,omitempty"`
	OutputUnavailableReason string                     `json:"output_unavailable_reason,omitempty"`
	EventsFile              string                     `json:"events_file,omitempty"`
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
