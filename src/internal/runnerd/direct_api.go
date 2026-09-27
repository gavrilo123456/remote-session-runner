package runnerd

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

var ErrDirectHTTPSAPIConfiguration = errors.New("direct HTTPS API configuration is incomplete")

type directHTTPSAPI struct {
	service      *execution.Service
	maxBodyBytes int64
}

type directCreateSessionRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget targetRequest   `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

type directKnownState struct {
	SessionState string `json:"session_state"`
}

type directSessionAcceptance struct {
	ResourceID      string           `json:"resource_id"`
	SessionID       string           `json:"session_id"`
	AcceptanceScope string           `json:"acceptance_scope"`
	ExecutionTarget targetResponse   `json:"execution_target"`
	KnownState      directKnownState `json:"known_state"`
}

type directSessionResource struct {
	SessionID       string                      `json:"session_id"`
	SessionState    string                      `json:"session_state"`
	ExecutionTarget targetResponse              `json:"execution_target"`
	Authority       string                      `json:"authority"`
	Controller      controllerRequest           `json:"controller"`
	ObservedAt      time.Time                   `json:"observed_at"`
	Environment     string                      `json:"environment"`
	Source          sourceResponse              `json:"source"`
	Capabilities    commandCapabilitiesResponse `json:"capabilities"`
}

type directSessionReadResponse struct {
	View     string                `json:"view"`
	IsStale  bool                  `json:"is_stale"`
	Resource directSessionResource `json:"resource"`
}

type directAPIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// NewDirectHTTPSAPIHandler creates the public v1 session routes over the same
// authoritative service used by runnerd's private API and SSH bridge.
func NewDirectHTTPSAPIHandler(service *execution.Service) (http.Handler, error) {
	if service == nil {
		return nil, ErrDirectHTTPSAPIConfiguration
	}
	return &directHTTPSAPI{service: service, maxBodyBytes: domain.MaxSerializedRequestBytes}, nil
}

func (s *directHTTPSAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request == nil || request.URL == nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "request URL is required")
		return
	}
	if request.URL.Path == "/v1/sessions" {
		if request.Method != http.MethodPost {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleCreateSession(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/sessions/") {
		if request.Method != http.MethodGet {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleGetSession(response, request)
		return
	}
	writeDirectError(response, http.StatusNotFound, "resource_not_found", "route not found")
}

func (s *directHTTPSAPI) handleCreateSession(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 || strings.IndexByte(idempotencyKey, 0) >= 0 {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "a valid Idempotency-Key header is required")
		return
	}
	body, err := s.readRequestBody(request)
	if err != nil {
		if errors.Is(err, domain.ErrSerializedInputTooLarge) {
			writeDirectError(response, http.StatusRequestEntityTooLarge, "invalid_request", "serialized request exceeds the 1 MiB limit")
		} else {
			writeDirectError(response, http.StatusBadRequest, "invalid_request", "request body could not be read")
		}
		return
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input directCreateSessionRequest
	if err := decoder.Decode(&input); err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported session request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value")
		return
	}
	if strings.TrimSpace(input.Environment) == "" || strings.IndexByte(input.Environment, 0) >= 0 {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "environment is required")
		return
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(input.ExecutionTarget.Kind), input.ExecutionTarget.Profile)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "execution_target must name a supported kind and profile")
		return
	}
	if target.Kind() != domain.TargetKindRemote {
		writeDirectError(response, http.StatusUnprocessableEntity, "environment_target_mismatch", "direct HTTPS accepts remote targets only")
		return
	}

	sourceInput, err := decodeOptionalDirectObject[sourceRequest](input.Source)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "source must be a supported JSON object")
		return
	}
	if len(input.Source) > 0 && (sourceInput == nil || sourceInput.Mode == "") {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "source.mode is required when source is provided")
		return
	}
	limitsInput, err := decodeOptionalDirectObject[limitsRequest](input.Limits)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "limits must be a supported JSON object")
		return
	}
	policyInput, err := decodeOptionalDirectObject[isolationRequest](input.Policy)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "policy must be a supported JSON object")
		return
	}
	source, err := parseSource(sourceInput)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "source is invalid for a session request")
		return
	}
	limits, err := parseDirectRequestedLimits(limitsInput)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "requested limits are invalid")
		return
	}
	canonicalOptions := domain.CanonicalizationOptions{Defaults: map[string]json.RawMessage{
		"limits": json.RawMessage(`{}`),
		"policy": json.RawMessage(`{}`),
	}}
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", body, canonicalOptions)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "session request cannot be canonicalized")
		return
	}
	if err := domain.ValidateSerializedRequest(canonical); err != nil {
		if errors.Is(err, domain.ErrSerializedInputTooLarge) {
			writeDirectError(response, http.StatusRequestEntityTooLarge, "invalid_request", "canonical request exceeds the 1 MiB limit")
		} else {
			writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "session request is invalid")
		}
		return
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, canonicalOptions)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "session request cannot be hashed")
		return
	}
	sessionID, err := newDirectSessionID()
	if err != nil {
		writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "could not allocate a session identity")
		return
	}
	result, serviceErr := s.service.CreateSession(request.Context(), execution.CreateSessionRequest{
		SessionID:            sessionID,
		IdempotencyKey:       idempotencyKey,
		RequestHash:          hash,
		Environment:          input.Environment,
		Target:               target,
		Controller:           principal.Controller,
		Source:               source,
		RequestedLimits:      limits,
		Isolation:            parseIsolation(policyInput),
		MaxActiveSessions:    store.DefaultActiveSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil && result.Session.SessionID == "" {
		status, code, message := directSessionError(serviceErr)
		writeDirectError(response, status, code, message)
		return
	}
	// Once the service returns a durable session record, acceptance has
	// happened even if runtime preparation already moved that record to a
	// terminal state. Report its stable ID and current known state.
	writeJSON(response, http.StatusAccepted, directSessionAcceptanceFromRecord(result.Session))
}

func (s *directHTTPSAPI) handleGetSession(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	sessionID, err := directSessionIDFromPath(request.URL)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "session path contains an invalid ID")
		return
	}
	record, err := s.service.GetSession(request.Context(), sessionID, principal.Controller)
	if err != nil {
		status, code, message := directSessionError(err)
		writeDirectError(response, status, code, message)
		return
	}
	environment, err := s.service.ResolveEnvironment(request.Context(), record.Environment)
	if err != nil {
		writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "session capabilities are unavailable")
		return
	}
	resource := directSessionResource{
		SessionID:       string(record.SessionID),
		SessionState:    string(record.State),
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		Authority:       "remote",
		Controller:      controllerRequest{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt:      record.UpdatedAt.UTC(),
		Environment:     record.Environment,
		Source:          sourceResponseFromRecord(record),
		Capabilities:    capabilitiesResponseFromEnvironment(environment),
	}
	writeJSON(response, http.StatusOK, directSessionReadResponse{View: "authority", IsStale: false, Resource: resource})
}

func (s *directHTTPSAPI) readRequestBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, errors.New("request body is required")
	}
	defer request.Body.Close()
	if request.ContentLength > s.maxBodyBytes {
		return nil, domain.ErrSerializedInputTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, s.maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > s.maxBodyBytes {
		return nil, domain.ErrSerializedInputTooLarge
	}
	if err := domain.ValidateSerializedRequest(body); err != nil {
		return nil, err
	}
	return body, nil
}

func decodeOptionalDirectObject[T any](raw json.RawMessage) (*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("value must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("object has trailing data")
	}
	return &value, nil
}

func parseDirectRequestedLimits(input *limitsRequest) (domain.RequestedLimits, error) {
	if input == nil {
		return domain.RequestedLimits{}, nil
	}
	const maxDurationSeconds int64 = (1<<63 - 1) / int64(time.Second)
	for _, seconds := range []int64{input.CommandTimeoutSeconds, input.IdleTimeoutSeconds, input.SessionMaxLifetimeSeconds} {
		if seconds > maxDurationSeconds {
			return domain.RequestedLimits{}, domain.ErrInvalidRequestedLimits
		}
	}
	return parseRequestedLimits(input)
}

func newDirectSessionID() (domain.SessionID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return domain.NewSessionID("sess-" + hex.EncodeToString(random[:]))
}

func directSessionIDFromPath(requestURL *url.URL) (domain.SessionID, error) {
	const prefix = "/v1/sessions/"
	if requestURL == nil || !strings.HasPrefix(requestURL.Path, prefix) {
		return "", errors.New("session path prefix is invalid")
	}
	rawID := strings.TrimPrefix(requestURL.EscapedPath(), prefix)
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("session path ID is invalid")
	}
	return domain.NewSessionID(idText)
}

func directSessionAcceptanceFromRecord(record store.SessionRecord) directSessionAcceptance {
	return directSessionAcceptance{
		ResourceID:      string(record.SessionID),
		SessionID:       string(record.SessionID),
		AcceptanceScope: "target_authority",
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState:      directKnownState{SessionState: string(record.State)},
	}
}

func directSessionError(err error) (int, string, string) {
	switch {
	case errors.Is(err, store.ErrSessionNotFound):
		return http.StatusNotFound, "resource_not_found", "session not found"
	case errors.Is(err, execution.ErrSessionController):
		return http.StatusForbidden, "controller_mismatch", "session belongs to another controller"
	case errors.Is(err, domain.ErrControllerMismatch):
		return http.StatusForbidden, "environment_forbidden", "controller is not authorized for this environment"
	case errors.Is(err, domain.ErrEnvironmentTargetMismatch):
		return http.StatusUnprocessableEntity, "environment_target_mismatch", "environment does not allow the requested target"
	case errors.Is(err, store.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different request"
	case errors.Is(err, store.ErrSessionCapacityExceeded):
		return http.StatusTooManyRequests, "quota_exceeded", "active session capacity is full"
	case errors.Is(err, execution.ErrEnvironmentUnavailable):
		return http.StatusUnprocessableEntity, "invalid_request", "environment is not configured"
	case errors.Is(err, domain.ErrEnvironmentSourceMismatch), errors.Is(err, domain.ErrRepositoryAliasNotAllowed), errors.Is(err, domain.ErrUnsupportedIsolationRequirement), errors.Is(err, domain.ErrInvalidRequestedLimits), errors.Is(err, domain.ErrLimitExceedsServiceCeiling), errors.Is(err, domain.ErrInvalidSource), errors.Is(err, domain.ErrInvalidTargetKind), errors.Is(err, domain.ErrEmptyTargetProfile), errors.Is(err, store.ErrIdempotencyKey):
		return http.StatusUnprocessableEntity, "invalid_request", "session request is not allowed by the selected environment"
	case errors.Is(err, execution.ErrRuntimeUnavailable), errors.Is(err, execution.ErrExecutionServiceConfiguration):
		return http.StatusServiceUnavailable, "runtime_unavailable", "session runtime is unavailable"
	default:
		return http.StatusServiceUnavailable, "runtime_unavailable", "session request could not be completed"
	}
}

func writeDirectError(response http.ResponseWriter, status int, code, message string) {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\n", " "))
	if message == "" {
		message = "request failed"
	}
	writeJSON(response, status, directAPIError{Code: code, Message: message, Retryable: status >= 500})
}

var _ http.Handler = (*directHTTPSAPI)(nil)
