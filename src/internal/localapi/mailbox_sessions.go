package localapi

import (
	"context"
	"encoding/json"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
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

func mailboxOperationError(failure *localOperationFailure) *mailbox.SessionOperationError {
	code := failure.code
	switch code {
	case "session_not_found":
		code = "resource_not_found"
	case "database_unavailable":
		code = "runtime_unavailable"
	}
	return &mailbox.SessionOperationError{Code: code, Message: failure.message, Retryable: failure.status >= 500}
}
