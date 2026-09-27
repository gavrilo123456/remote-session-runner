package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

type localOperationFailure struct {
	status  int
	code    string
	message string
}

// SessionController identifies the local owner used by the shared API and
// mailbox session operations.
func (s *Server) SessionController() domain.ControllerIdentity {
	if s == nil {
		return domain.ControllerIdentity{}
	}
	return s.owner
}

func localFailure(status int, code, message string) *localOperationFailure {
	return &localOperationFailure{status: status, code: code, message: sanitizeErrorString(message)}
}

// CreateSessionIntent uses the same acceptance logic as Unix-socket API
// ingress. The file-only client itself remains filesystem-only.
func (s *Server) CreateSessionIntent(ctx context.Context, request mailbox.Request) (mailbox.SessionIntent, error) {
	body, err := mailboxCreateSessionBody(request.RawJSON)
	if err != nil {
		return mailbox.SessionIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox create request is invalid"}
	}
	acceptance, failure := s.acceptCreateSessionIntent(ctx, request.IdempotencyKey, body)
	if failure != nil {
		return mailbox.SessionIntent{}, mailboxOperationError(failure)
	}
	return mailbox.SessionIntent{SessionID: acceptance.SessionID, DeliveryState: acceptance.KnownState.DeliveryState}, nil
}

// SubmitCommandIntent uses the same immutable target, owner, script, timeout,
// canonical-hash, and idempotency path as the Unix-socket API. It records only
// a local intent; Router delivery remains a separate authority boundary.
func (s *Server) SubmitCommandIntent(ctx context.Context, request mailbox.Request) (mailbox.CommandIntent, error) {
	var input struct {
		RequestID      string          `json:"request_id"`
		IdempotencyKey string          `json:"idempotency_key"`
		Operation      string          `json:"operation"`
		SessionID      string          `json:"session_id"`
		Script         *string         `json:"script"`
		TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.RawJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Operation != "submit_command" || input.RequestID != request.RequestID || input.IdempotencyKey != request.IdempotencyKey || input.SessionID != request.SessionID || input.Script == nil {
		return mailbox.CommandIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox submit request is invalid"}
	}
	body, err := json.Marshal(struct {
		Script         *string         `json:"script"`
		TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
	}{Script: input.Script, TimeoutSeconds: input.TimeoutSeconds})
	if err != nil {
		return mailbox.CommandIntent{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "mailbox submit request is invalid"}
	}
	acceptance, failure := s.acceptSubmitCommandIntent(ctx, request.IdempotencyKey, request.SessionID, body)
	if failure != nil {
		return mailbox.CommandIntent{}, mailboxOperationError(failure)
	}
	return mailbox.CommandIntent{
		CommandID: acceptance.CommandID, SessionID: acceptance.SessionID,
		DeliveryState: acceptance.KnownState.DeliveryState,
	}, nil
}

func mailboxCreateSessionBody(raw []byte) ([]byte, error) {
	var input struct {
		Environment     json.RawMessage `json:"environment"`
		ExecutionTarget json.RawMessage `json:"execution_target"`
		Source          json.RawMessage `json:"source,omitempty"`
		Limits          json.RawMessage `json:"limits,omitempty"`
		Policy          json.RawMessage `json:"policy,omitempty"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || len(input.Environment) == 0 || len(input.ExecutionTarget) == 0 {
		return nil, ErrConfiguration
	}
	fields := map[string]json.RawMessage{
		"environment": input.Environment, "execution_target": input.ExecutionTarget,
	}
	if len(input.Source) > 0 {
		fields["source"] = input.Source
	}
	if len(input.Limits) > 0 {
		fields["limits"] = input.Limits
	}
	if len(input.Policy) > 0 {
		fields["policy"] = input.Policy
	}
	return json.Marshal(fields)
}

// GetSession uses the same owner-scoped session read logic as Unix-socket API
// ingress, without creating a socket client or network request.
func (s *Server) GetSession(ctx context.Context, sessionID string) (mailbox.SessionSnapshot, error) {
	read, failure := s.readSession(ctx, sessionID)
	if failure != nil {
		return mailbox.SessionSnapshot{}, mailboxOperationError(failure)
	}
	var snapshot mailbox.SessionSnapshot
	switch read.View {
	case "local_intent":
		resource, ok := read.Resource.(sessionIntentResource)
		if !ok {
			return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local session intent snapshot is invalid", Retryable: true}
		}
		snapshot.SessionID, snapshot.DeliveryState, snapshot.ObservedAt = resource.SessionID, resource.DeliveryState, resource.ObservedAt
	case "projection", "authority":
		resource, ok := read.Resource.(sessionProjectionResource)
		if !ok {
			return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "session authority snapshot is invalid", Retryable: true}
		}
		snapshot.SessionID, snapshot.SessionState, snapshot.ObservedAt = resource.SessionID, resource.SessionState, resource.ObservedAt
	case "":
		return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local session API returned no view", Retryable: true}
	default:
		return mailbox.SessionSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local session API returned an unsupported view", Retryable: true}
	}
	return snapshot, nil
}

// GetCommandSnapshot returns only a command intent owned by the local Mac
// controller, its local authority snapshot, or its queued-remote projection.
// Direct-created Linux resources have no local intent and remain inaccessible.
func (s *Server) GetCommandSnapshot(ctx context.Context, commandIDText string) (mailbox.CommandSnapshot, error) {
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "invalid_request", Message: "command ID is invalid"}
	}
	intent, err := s.authority.GetLocalIntentByResource(ctx, "submit_command", string(commandID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "resource_not_found", Message: "command is not available through this Mac ingress"}
		}
		status, code := statusForStoreError(err)
		return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	base := mailbox.CommandSnapshot{
		CommandID: intent.CommandID, SessionID: intent.SessionID,
		DeliveryState: string(intent.DeliveryState), ObservedAt: intent.UpdatedAt.UTC(),
	}
	if intent.DeliveryState == store.LocalIntentNotDelivered {
		return base, nil
	}
	if remoteProjectionEligible(intent) {
		projection, events, projectionErr := s.authority.GetRemoteCommandWithEvents(ctx, commandID)
		if errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
			return base, nil
		}
		if projectionErr != nil {
			status, code := statusForStoreError(projectionErr)
			return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(projectionErr)))
		}
		if projection.CommandID != commandID || projection.SessionID != intent.SessionID || projection.Target.Kind() != domain.TargetKindRemote || projection.Target.Profile() != intent.Target.Profile() ||
			projection.Controller.Type() != s.owner.Type() || projection.Controller.ID() != s.owner.ID() || projection.Environment != intent.Environment {
			return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote command projection does not match its accepted intent", Retryable: true}
		}
		snapshot, err := mailbox.SnapshotRemoteCommand(projection, events, time.Now().UTC())
		if err != nil {
			return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "remote command snapshot is temporarily unavailable", Retryable: true}
		}
		snapshot.DeliveryState = string(intent.DeliveryState)
		return snapshot, nil
	}
	if intent.Target.Kind() != domain.TargetKindLocal || (intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) {
		return base, nil
	}
	command, events, err := s.authority.GetCommandWithEvents(ctx, commandID)
	if err != nil {
		status, code := statusForStoreError(err)
		return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	session, err := s.authority.GetSession(ctx, command.SessionID)
	if err != nil {
		status, code := statusForStoreError(err)
		return mailbox.CommandSnapshot{}, mailboxOperationError(localFailure(status, code, sanitizeError(err)))
	}
	if command.CommandID != intent.CommandID || command.SessionID != intent.SessionID || session.SessionID != intent.SessionID ||
		session.Target.Kind() != domain.TargetKindLocal || session.Target.Profile() != intent.Target.Profile() ||
		session.Controller.Type() != s.owner.Type() || session.Controller.ID() != s.owner.ID() ||
		domain.CompareIdempotency(command.RequestHash, intent.RequestHash) != domain.IdempotencySamePayload || !bytes.Equal(command.ScriptBytes, intent.ScriptBytes) {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local command authority does not match its accepted intent", Retryable: true}
	}
	if intent.IntentOrdinal != nil && (command.IntentOrdinal == nil || *command.IntentOrdinal != *intent.IntentOrdinal) {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local command ordinal does not match its accepted intent", Retryable: true}
	}
	snapshot, err := mailbox.SnapshotCommand(command, events, time.Now().UTC())
	if err != nil {
		return mailbox.CommandSnapshot{}, &mailbox.SessionOperationError{Code: "runtime_unavailable", Message: "local command snapshot is temporarily unavailable", Retryable: true}
	}
	snapshot.DeliveryState = string(intent.DeliveryState)
	return snapshot, nil
}

func mailboxOperationError(failure *localOperationFailure) *mailbox.SessionOperationError {
	code := failure.code
	switch code {
	case "session_not_found":
		code = "resource_not_found"
	case "invalid_script":
		code = "invalid_request"
	case "database_unavailable":
		code = "runtime_unavailable"
	}
	return &mailbox.SessionOperationError{Code: code, Message: failure.message, Retryable: failure.status >= 500}
}
