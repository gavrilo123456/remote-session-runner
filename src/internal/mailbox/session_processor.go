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
}

type SessionProcessorOptions struct {
	Importer   *Importer
	Authority  *store.AuthorityStore
	Controller domain.ControllerIdentity
	Operations SessionOperations
	Outbox     *Outbox
}

// SessionProcessor wires only create_session/get_session. Create receipts
// remain accepted until a later local/remote snapshot reaches the design's
// readiness or known-failure boundary.
type SessionProcessor struct {
	importer   *Importer
	authority  *store.AuthorityStore
	controller domain.ControllerIdentity
	operations SessionOperations
	projector  Projector
	mu         sync.Mutex
}

func NewSessionProcessor(options SessionProcessorOptions) (*SessionProcessor, error) {
	if options.Importer == nil || options.Authority == nil || options.Operations == nil || options.Outbox == nil {
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
		operations: options.Operations, projector: Projector{Authority: options.Authority, Outbox: options.Outbox},
	}, nil
}

// Import records and projects create_session/get_session requests. Unsupported
// mailbox operations remain outside this phase's handler boundary.
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
	return p.reconcileAcceptedCreates(ctx)
}

func (p *SessionProcessor) process(ctx context.Context, request Request) (bool, error) {
	if request.Operation != "create_session" && request.Operation != "get_session" {
		return false, fmt.Errorf("%w: operation %q is outside P094", ErrMailboxInput, request.Operation)
	}
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return false, err
	}
	record, duplicate, err := p.authority.AcceptMailboxExchange(ctx, store.MailboxExchangeCreate{
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
		if snapshot.SessionID != previous.SessionID {
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

func (p *SessionProcessor) publish(ctx context.Context, current store.MailboxExchangeRecord, response sessionMailboxResponse, cursor *int64) (bool, error) {
	response.ResponseRevision = current.ResponseRevision + 1
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

type sessionMailboxResponse struct {
	RequestID        string                     `json:"request_id"`
	Operation        string                     `json:"operation"`
	RequestState     store.MailboxExchangeState `json:"request_state"`
	ResponseRevision int64                      `json:"response_revision"`
	SessionID        string                     `json:"session_id,omitempty"`
	SessionState     string                     `json:"session_state,omitempty"`
	DeliveryState    string                     `json:"delivery_state,omitempty"`
	ObservedAt       *time.Time                 `json:"observed_at,omitempty"`
	Error            *mailboxResponseError      `json:"error,omitempty"`
}

type mailboxResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

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
