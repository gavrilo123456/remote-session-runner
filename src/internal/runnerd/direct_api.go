package runnerd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

var ErrDirectHTTPSAPIConfiguration = errors.New("direct HTTPS API configuration is incomplete")

type directHTTPSAPI struct {
	service        *execution.Service
	requestGate    *lifecycle.Gate
	dispatchGate   *lifecycle.Gate
	maxBodyBytes   int64
	allocateRunIDs func() (domain.JobID, domain.SessionID, domain.CommandID, error)
}

const (
	directEventsLastSequenceHeader = "X-Runner-Last-Sequence"
	directEventsSubscriberCapacity = 256
)

type directCreateSessionRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget targetRequest   `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

type directKnownState struct {
	SessionState string `json:"session_state,omitempty"`
	CommandState string `json:"command_state,omitempty"`
}

type directSessionAcceptance struct {
	ResourceID         string           `json:"resource_id"`
	SessionID          string           `json:"session_id"`
	AcceptanceScope    string           `json:"acceptance_scope"`
	ExecutionTarget    targetResponse   `json:"execution_target"`
	KnownState         directKnownState `json:"known_state"`
	IdempotencyWarning string           `json:"idempotency_warning,omitempty"`
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

type directSubmitCommandRequest struct {
	Script         *string         `json:"script"`
	TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
}

type directRunJobRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget targetRequest   `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Script          *string         `json:"script"`
	TimeoutSeconds  json.RawMessage `json:"timeout_seconds,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

type directCommandAcceptance struct {
	ResourceID         string           `json:"resource_id"`
	CommandID          string           `json:"command_id"`
	SessionID          string           `json:"session_id"`
	AcceptanceScope    string           `json:"acceptance_scope"`
	ExecutionTarget    targetResponse   `json:"execution_target"`
	KnownState         directKnownState `json:"known_state"`
	IdempotencyWarning string           `json:"idempotency_warning,omitempty"`
}

type directCommandResource struct {
	CommandID               string                      `json:"command_id"`
	SessionID               string                      `json:"session_id"`
	Ordinal                 int64                       `json:"ordinal,omitempty"`
	CommandState            string                      `json:"command_state"`
	ExitCode                *int                        `json:"exit_code,omitempty"`
	FinalEventSequence      *int64                      `json:"final_event_sequence,omitempty"`
	OutputComplete          bool                        `json:"output_complete"`
	OutputTruncated         bool                        `json:"output_truncated"`
	OutputUnavailableReason string                      `json:"output_unavailable_reason,omitempty"`
	ExecutionTarget         targetResponse              `json:"execution_target"`
	Authority               string                      `json:"authority"`
	Controller              controllerRequest           `json:"controller"`
	ObservedAt              time.Time                   `json:"observed_at"`
	Environment             string                      `json:"environment"`
	Source                  sourceResponse              `json:"source"`
	Capabilities            commandCapabilitiesResponse `json:"capabilities"`
}

type directCommandReadResponse struct {
	View     string                `json:"view"`
	IsStale  bool                  `json:"is_stale"`
	Resource directCommandResource `json:"resource"`
}

type directJobAcceptance struct {
	ResourceID         string           `json:"resource_id"`
	JobID              string           `json:"job_id"`
	SessionID          string           `json:"session_id"`
	CommandID          string           `json:"command_id"`
	AcceptanceScope    string           `json:"acceptance_scope"`
	ExecutionTarget    targetResponse   `json:"execution_target"`
	KnownState         directKnownState `json:"known_state"`
	IdempotencyWarning string           `json:"idempotency_warning,omitempty"`
}

type directJobResource struct {
	JobID                   string                      `json:"job_id"`
	SessionID               string                      `json:"session_id"`
	CommandID               string                      `json:"command_id"`
	Phase                   string                      `json:"phase"`
	CommandState            *string                     `json:"command_state,omitempty"`
	ExitCode                *int                        `json:"exit_code,omitempty"`
	FinalEventSequence      *int64                      `json:"final_event_sequence,omitempty"`
	OutputComplete          bool                        `json:"output_complete"`
	OutputTruncated         bool                        `json:"output_truncated"`
	OutputUnavailableReason string                      `json:"output_unavailable_reason,omitempty"`
	TeardownState           string                      `json:"teardown_state"`
	ExecutionTarget         targetResponse              `json:"execution_target"`
	Authority               string                      `json:"authority"`
	Controller              controllerRequest           `json:"controller"`
	ObservedAt              time.Time                   `json:"observed_at"`
	Environment             string                      `json:"environment"`
	Source                  sourceResponse              `json:"source"`
	Capabilities            commandCapabilitiesResponse `json:"capabilities"`
}

type directJobReadResponse struct {
	View     string            `json:"view"`
	IsStale  bool              `json:"is_stale"`
	Resource directJobResource `json:"resource"`
}

type directAPIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type directCommandEvent struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Ordinal    int64     `json:"ordinal,omitempty"`
	Encoding   string    `json:"encoding,omitempty"`
	DataBase64 string    `json:"data_base64,omitempty"`
	ByteCount  int64     `json:"byte_count,omitempty"`
}

type directEventHistoryDetails struct {
	OutputComplete          bool   `json:"output_complete"`
	OutputUnavailableReason string `json:"output_unavailable_reason"`
}

type directEventHistoryError struct {
	Code       string                    `json:"code"`
	Message    string                    `json:"message"`
	Retryable  bool                      `json:"retryable"`
	ResourceID string                    `json:"resource_id"`
	Details    directEventHistoryDetails `json:"details"`
}

// NewDirectHTTPSAPIHandler creates the public v1 session and job routes over
// the same authoritative service used by runnerd's private API and SSH bridge.
func NewDirectHTTPSAPIHandler(service *execution.Service) (http.Handler, error) {
	return newDirectHTTPSAPIHandler(service, nil, nil)
}

func newDirectHTTPSAPIHandler(service *execution.Service, requestGate, dispatchGate *lifecycle.Gate) (http.Handler, error) {
	if service == nil {
		return nil, ErrDirectHTTPSAPIConfiguration
	}
	return &directHTTPSAPI{
		service: service, requestGate: requestGate, dispatchGate: dispatchGate,
		maxBodyBytes: domain.MaxSerializedRequestBytes, allocateRunIDs: newDirectRunIDs,
	}, nil
}

func (s *directHTTPSAPI) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request == nil || request.URL == nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "request URL is required")
		return
	}
	release, admitted := admitRunnerRequest(s.requestGate, request.URL.Path, request.Method, func() {
		writeDirectError(response, http.StatusServiceUnavailable, "shutting_down", "runnerd is shutting down")
	})
	if !admitted {
		return
	}
	defer release()
	request = request.WithContext(audit.WithIngress(request.Context(), audit.IngressDirectMTLS))
	if request.URL.Path == "/v1/sessions" {
		if request.Method != http.MethodPost {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleCreateSession(response, request)
		return
	}
	if request.URL.Path == "/v1/jobs" {
		if request.Method != http.MethodPost {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleRunJob(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/jobs/") {
		if request.Method != http.MethodGet {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleGetJob(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/sessions/") && strings.HasSuffix(request.URL.Path, "/commands") {
		if request.Method != http.MethodPost {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleSubmitCommand(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/commands/") && strings.HasSuffix(request.URL.Path, "/cancel") {
		if request.Method != http.MethodPost {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleCancelCommand(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/commands/") && strings.HasSuffix(request.URL.Path, "/events") {
		if request.Method != http.MethodGet {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleGetCommandEvents(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/sessions/") {
		switch request.Method {
		case http.MethodGet:
			s.handleGetSession(response, request)
		case http.MethodDelete:
			s.handleCloseSession(response, request)
		default:
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		}
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/commands/") {
		if request.Method != http.MethodGet {
			writeDirectError(response, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		s.handleGetCommand(response, request)
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
	writeJSON(response, http.StatusAccepted, directSessionAcceptanceFromRecord(result.Session, result.IdempotencyWarning))
}

func (s *directHTTPSAPI) handleRunJob(response http.ResponseWriter, request *http.Request) {
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
		writeDirectRequestBodyError(response, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input directRunJobRequest
	if err := decoder.Decode(&input); err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported job request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value")
		return
	}
	if _, err := domain.CanonicalizeMutationRequestJSON("run", body, domain.CanonicalizationOptions{}); err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "job request JSON is not canonicalizable")
		return
	}
	if strings.TrimSpace(input.Environment) == "" || strings.IndexByte(input.Environment, 0) >= 0 || input.Script == nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "environment and script are required")
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
	if err != nil || len(input.Source) > 0 && (sourceInput == nil || sourceInput.Mode == "") {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "source must be a supported JSON object")
		return
	}
	source, err := parseSource(sourceInput)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "source is invalid for a job request")
		return
	}
	limitsInput, err := decodeOptionalDirectObject[limitsRequest](input.Limits)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "limits must be a supported JSON object")
		return
	}
	limits, err := parseDirectRequestedLimits(limitsInput)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "requested limits are invalid")
		return
	}
	commandTimeout, err := directCommandTimeout(input.TimeoutSeconds)
	if err != nil || commandTimeout > 0 && limits.CommandTimeout > 0 && commandTimeout != limits.CommandTimeout {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "timeout_seconds conflicts with limits.command_timeout_seconds")
		return
	}
	if commandTimeout > 0 {
		limits.CommandTimeout = commandTimeout
	}
	policy, err := decodeOptionalDirectObject[map[string]any](input.Policy)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "policy must be a JSON object")
		return
	}
	var policyValue map[string]any
	if policy != nil {
		policyValue = *policy
	}
	if err := domain.ValidateScriptUTF8(*input.Script); err != nil {
		if errors.Is(err, domain.ErrScriptTooLarge) {
			writeDirectError(response, http.StatusRequestEntityTooLarge, "invalid_request", "script exceeds the 128 KiB UTF-8 limit")
		} else {
			writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "script must be valid UTF-8")
		}
		return
	}
	canonical, err := canonicalRunPayload(input.Environment, target, source, *input.Script, limits, domain.IsolationRequirements{}, policyValue)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "job request cannot be canonicalized")
		return
	}
	if err := domain.ValidateSerializedRequest(canonical); err != nil {
		if errors.Is(err, domain.ErrSerializedInputTooLarge) {
			writeDirectError(response, http.StatusRequestEntityTooLarge, "invalid_request", "canonical job request exceeds the 1 MiB limit")
		} else {
			writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "job request is invalid")
		}
		return
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "job request cannot be hashed")
		return
	}
	jobID, sessionID, commandID, err := s.allocateRunIDs()
	if err != nil {
		writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "could not allocate job identities")
		return
	}
	result, serviceErr := s.service.RunJob(request.Context(), execution.RunJobRequest{
		Acceptance: store.JobAcceptance{
			JobID: jobID, SessionID: sessionID, CommandID: commandID, Controller: principal.Controller,
			IdempotencyKey: idempotencyKey, RequestHash: hash, Environment: input.Environment,
			Target: target, Source: source, Script: *input.Script, CanonicalPayload: canonical,
			IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		},
		RequestedLimits: limits, MaxActiveSessions: store.DefaultActiveSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil && result.Job.JobID == "" {
		status, code, message := directJobError(serviceErr)
		writeDirectError(response, status, code, message)
		return
	}
	// A durable job row is the acceptance boundary. The read route returns its
	// command and teardown outcomes even when the coordinator reports failure.
	writeJSON(response, http.StatusAccepted, directJobAcceptanceFromRecord(result.Job, result.IdempotencyWarning))
}

func (s *directHTTPSAPI) handleGetJob(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	jobID, err := directJobIDFromPath(request.URL)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "job path contains an invalid ID")
		return
	}
	record, err := s.service.GetJob(request.Context(), jobID, principal.Controller)
	if err != nil {
		status, code, message := directJobError(err)
		writeDirectError(response, status, code, message)
		return
	}
	environment, err := s.service.ResolveEnvironment(request.Context(), record.Environment)
	if err != nil {
		writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "job capabilities are unavailable")
		return
	}
	writeJSON(response, http.StatusOK, directJobReadResponse{
		View: "authority", IsStale: false, Resource: directJobResourceFromRecord(record, environment),
	})
}

func directJobAcceptanceFromRecord(record store.JobRecord, idempotencyWarning bool) directJobAcceptance {
	known := directKnownState{}
	if record.CommandState != nil {
		known.CommandState = string(*record.CommandState)
	}
	return directJobAcceptance{
		ResourceID: string(record.JobID), JobID: string(record.JobID), SessionID: string(record.SessionID),
		CommandID: string(record.CommandID), AcceptanceScope: "target_authority",
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()}, KnownState: known,
		IdempotencyWarning: directIdempotencyWarning(idempotencyWarning),
	}
}

func directJobResourceFromRecord(record store.JobRecord, environment domain.Environment) directJobResource {
	authority := "remote"
	if record.Target.Kind() == domain.TargetKindLocal {
		authority = "local"
	}
	return directJobResource{
		JobID: string(record.JobID), SessionID: string(record.SessionID), CommandID: string(record.CommandID),
		Phase: string(record.Phase), CommandState: commandStatePointer(record.CommandState), ExitCode: record.ExitCode,
		FinalEventSequence: record.FinalEventSequence, OutputComplete: record.OutputComplete, OutputTruncated: record.OutputTruncated,
		OutputUnavailableReason: record.OutputUnavailableReason, TeardownState: string(record.TeardownState),
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()}, Authority: authority,
		Controller: controllerRequest{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt: record.UpdatedAt.UTC(), Environment: record.Environment, Source: sourceResponseFromJobRecord(record),
		Capabilities: capabilitiesResponseFromEnvironment(environment),
	}
}

func sourceResponseFromJobRecord(record store.JobRecord) sourceResponse {
	portable := record.Source.Portable()
	return sourceResponse{
		Mode: string(record.Source.Mode()), RepositoryAlias: record.Source.RepositoryAlias(),
		RequestedRevision: record.Source.RequestedRevision(), Path: record.Source.Path(), Portable: &portable,
	}
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

func (s *directHTTPSAPI) handleSubmitCommand(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	sessionID, err := directSessionCommandIDFromPath(request.URL)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "session command path contains an invalid session ID")
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
	var input directSubmitCommandRequest
	if err := decoder.Decode(&input); err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported command request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value")
		return
	}
	// Run the domain canonicalizer on the exact request bytes as well as the
	// decoded value. It rejects duplicate object keys, invalid UTF-8, and
	// malformed Unicode escapes that encoding/json otherwise accepts loosely.
	if _, err := domain.CanonicalizeMutationRequestJSON("submit_command", body, domain.CanonicalizationOptions{}); err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "command request JSON is not canonicalizable")
		return
	}
	// Check ownership before detailed request-dependent validation or writes.
	// This also supplies the immutable session timeout used to normalize the
	// idempotency hash when timeout_seconds is omitted.
	mutationCtx := audit.WithActionHint(request.Context(), audit.ActionSubmit)
	session, err := s.service.GetSession(mutationCtx, sessionID, principal.Controller)
	if err != nil {
		status, code, message := directCommandError(err)
		writeDirectError(response, status, code, message)
		return
	}
	if input.Script == nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "script is required")
		return
	}
	if err := domain.ValidateScriptUTF8(*input.Script); err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "script must be valid UTF-8 and no larger than 128 KiB")
		return
	}
	timeout, err := directCommandTimeout(input.TimeoutSeconds)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "timeout_seconds must be a positive whole number of seconds")
		return
	}
	effectiveTimeout := timeout
	if effectiveTimeout == 0 {
		effectiveTimeout = session.Limits.CommandTimeout
	}
	if effectiveTimeout <= 0 {
		status, code, message := directCommandError(domain.ErrInvalidRequestedLimits)
		writeDirectError(response, status, code, message)
		return
	}
	// Include both the path session and effective timeout. The session path is
	// part of the mutation even though it is not repeated in the HTTP body.
	hashPayload, err := json.Marshal(struct {
		SessionID          string `json:"session_id"`
		Script             string `json:"script"`
		TimeoutNanoseconds int64  `json:"timeout_nanoseconds"`
	}{SessionID: string(sessionID), Script: *input.Script, TimeoutNanoseconds: int64(effectiveTimeout)})
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "command request cannot be canonicalized")
		return
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", hashPayload, domain.CanonicalizationOptions{})
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "command request cannot be hashed")
		return
	}
	commandID, err := newDirectCommandID()
	if err != nil {
		writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "could not allocate a command identity")
		return
	}
	result, serviceErr := s.service.AcceptCommand(mutationCtx, execution.SubmitCommandRequest{
		CommandID:            commandID,
		SessionID:            sessionID,
		Controller:           principal.Controller,
		IdempotencyKey:       idempotencyKey,
		RequestHash:          hash,
		Script:               *input.Script,
		Timeout:              effectiveTimeout,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil {
		status, code, message := directCommandError(serviceErr)
		writeDirectError(response, status, code, message)
		return
	}
	if result.Command.State == domain.CommandStateQueued {
		acceptedID := result.Command.CommandID
		controller := principal.Controller
		launchRunnerWork(s.dispatchGate, func() {
			_, _ = s.service.ResumeCommand(context.Background(), acceptedID, controller)
		})
	}
	writeJSON(response, http.StatusAccepted, directCommandAcceptanceFromRecord(result.Command, session.Target, result.IdempotencyWarning))
}

func (s *directHTTPSAPI) handleGetCommand(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	commandID, err := directCommandIDFromPath(request.URL)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "command path contains an invalid ID")
		return
	}
	command, err := s.service.GetCommand(request.Context(), commandID, principal.Controller)
	if err != nil {
		status, code, message := directCommandError(err)
		writeDirectError(response, status, code, message)
		return
	}
	session, err := s.service.GetSession(request.Context(), command.SessionID, principal.Controller)
	if err != nil {
		status, code, message := directCommandError(err)
		writeDirectError(response, status, code, message)
		return
	}
	environment, err := s.service.ResolveEnvironment(request.Context(), session.Environment)
	if err != nil {
		writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "command capabilities are unavailable")
		return
	}
	resource := directCommandResource{
		CommandID:               string(command.CommandID),
		SessionID:               string(command.SessionID),
		Ordinal:                 command.Ordinal,
		CommandState:            string(command.State),
		ExitCode:                command.ExitCode,
		FinalEventSequence:      command.FinalEventSequence,
		OutputComplete:          command.OutputComplete,
		OutputTruncated:         command.OutputTruncated,
		OutputUnavailableReason: command.OutputUnavailableReason,
		ExecutionTarget:         targetResponse{Kind: string(session.Target.Kind()), Profile: session.Target.Profile()},
		Authority:               "remote",
		Controller:              controllerRequest{Type: string(session.Controller.Type()), ID: string(session.Controller.ID())},
		ObservedAt:              command.UpdatedAt.UTC(),
		Environment:             session.Environment,
		Source:                  sourceResponseFromRecord(session),
		Capabilities:            capabilitiesResponseFromEnvironment(environment),
	}
	writeJSON(response, http.StatusOK, directCommandReadResponse{View: "authority", IsStale: false, Resource: resource})
}

func (s *directHTTPSAPI) handleGetCommandEvents(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	commandID, err := directCommandEventsIDFromPath(request.URL)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "command events path contains an invalid ID")
		return
	}
	after, follow, err := directEventCursor(request.URL.Query())
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	command, err := s.service.GetCommand(request.Context(), commandID, principal.Controller)
	if err != nil {
		status, code, message := directCommandError(err)
		writeDirectError(response, status, code, message)
		return
	}
	if command.OutputUnavailableReason == "retention_expired" {
		writeDirectEventHistoryError(response, commandID, "retention_expired")
		return
	}
	if follow {
		if command.FinalEventSequence != nil && after >= *command.FinalEventSequence {
			setDirectEventHeaders(response)
			response.Header().Set(directEventsLastSequenceHeader, strconv.FormatInt(after, 10))
			response.WriteHeader(http.StatusOK)
			return
		}
		subscription, err := s.service.SubscribeCommandEvents(request.Context(), commandID, principal.Controller, after, directEventsSubscriberCapacity)
		if err != nil {
			writeDirectCommandEventReadError(response, commandID, err)
			return
		}
		defer subscription.Close()
		s.writeDirectCommandEventFollow(response, request, subscription, command.Ordinal, after)
		return
	}

	events, err := s.service.ReplayCommandEvents(request.Context(), commandID, principal.Controller, after)
	if err != nil {
		writeDirectCommandEventReadError(response, commandID, err)
		return
	}

	// Validate every frame before committing response headers. This ensures a
	// corrupt or overlarge stored event cannot turn an unavailable range into
	// a successful partial NDJSON response.
	for _, event := range events {
		if _, err := encodeDirectCommandEvent(event, command.Ordinal); err != nil {
			writeDirectError(response, http.StatusServiceUnavailable, "runtime_unavailable", "stored command event exceeds the supported frame limit")
			return
		}
	}
	lastSequence := after
	if len(events) > 0 {
		lastSequence = events[len(events)-1].Sequence
	}
	setDirectEventHeaders(response)
	response.Header().Set(directEventsLastSequenceHeader, strconv.FormatInt(lastSequence, 10))
	response.WriteHeader(http.StatusOK)
	for _, event := range events {
		frame, err := encodeDirectCommandEvent(event, command.Ordinal)
		if err != nil {
			return
		}
		if _, err := response.Write(frame); err != nil {
			return
		}
	}
}

func (s *directHTTPSAPI) writeDirectCommandEventFollow(response http.ResponseWriter, request *http.Request, subscription *store.CommandEventSubscription, ordinal, after int64) {
	setDirectEventHeaders(response)
	response.Header().Set("Trailer", directEventsLastSequenceHeader)
	response.WriteHeader(http.StatusOK)
	lastWrittenSequence := after
	defer func() {
		response.Header().Set(directEventsLastSequenceHeader, strconv.FormatInt(lastWrittenSequence, 10))
	}()
	flusher, _ := response.(http.Flusher)
	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				// On overflow the subscription closes only after preserving its
				// bounded prefix. Drain that prefix before publishing the cursor.
				return
			}
			frame, err := encodeDirectCommandEvent(event, ordinal)
			if err != nil {
				return
			}
			written, err := response.Write(frame)
			if err != nil || written != len(frame) {
				return
			}
			lastWrittenSequence = event.Sequence
			if flusher != nil {
				flusher.Flush()
			}
			subscription.Acknowledge(event)
			if isTerminalCommandEvent(event.Type) {
				return
			}
		case <-request.Context().Done():
			return
		}
	}
}

func setDirectEventHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "application/x-ndjson")
	response.Header().Set("X-Runner-View", "authority")
	response.Header().Set("X-Runner-Stale", "false")
}

func writeDirectCommandEventReadError(response http.ResponseWriter, commandID domain.CommandID, err error) {
	switch {
	case errors.Is(err, store.ErrCommandReplayExpired):
		writeDirectEventHistoryError(response, commandID, "retention_expired")
	case errors.Is(err, store.ErrCommandReplayGap):
		writeDirectEventHistoryError(response, commandID, "remote_event_gap")
	default:
		status, code, message := directCommandError(err)
		writeDirectError(response, status, code, message)
	}
}

func directEventCursor(query url.Values) (int64, bool, error) {
	for key, values := range query {
		if key != "after" && key != "follow" {
			return 0, false, errors.New("unsupported event query parameter")
		}
		if len(values) != 1 {
			return 0, false, errors.New("event query parameter must occur once")
		}
	}
	follow := false
	if values, exists := query["follow"]; exists {
		switch values[0] {
		case "true":
			follow = true
		case "false":
		default:
			return 0, false, errors.New("follow must be true or false")
		}
	}
	value := query.Get("after")
	if value == "" {
		return 0, follow, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, false, errors.New("invalid event cursor")
	}
	return cursor, follow, nil
}

func encodeDirectCommandEvent(event store.CommandEventRecord, ordinal int64) ([]byte, error) {
	value := directCommandEvent{
		CommandID: string(event.CommandID), Sequence: event.Sequence,
		Type: event.Type, Timestamp: event.OccurredAt.UTC(),
	}
	if event.Type == "command_queued" {
		value.Ordinal = ordinal
	}
	if event.Type == "stdout" || event.Type == "stderr" {
		if len(event.Payload) == 0 || len(event.Payload) > hostruntime.MaxOutputChunkBytes || int64(len(event.Payload)) != event.ByteCount {
			return nil, domain.ErrSerializedInputTooLarge
		}
		value.Encoding = "base64"
		value.DataBase64 = base64.StdEncoding.EncodeToString(event.Payload)
		value.ByteCount = event.ByteCount
	}
	frame, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateSerializedFrame(frame); err != nil {
		return nil, err
	}
	return append(frame, '\n'), nil
}

func directCommandEventsIDFromPath(requestURL *url.URL) (domain.CommandID, error) {
	const prefix = "/v1/commands/"
	const suffix = "/events"
	if requestURL == nil || !strings.HasPrefix(requestURL.Path, prefix) || !strings.HasSuffix(requestURL.Path, suffix) {
		return "", errors.New("command events path is invalid")
	}
	rawID := strings.TrimSuffix(strings.TrimPrefix(requestURL.EscapedPath(), prefix), suffix)
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("command events path ID is invalid")
	}
	return domain.NewCommandID(idText)
}

func writeDirectEventHistoryError(response http.ResponseWriter, commandID domain.CommandID, reason string) {
	writeJSON(response, http.StatusGone, directEventHistoryError{
		Code: "event_history_unavailable", Message: "requested event history is unavailable", Retryable: false,
		ResourceID: string(commandID),
		Details:    directEventHistoryDetails{OutputComplete: false, OutputUnavailableReason: reason},
	})
}

func (s *directHTTPSAPI) handleCancelCommand(response http.ResponseWriter, request *http.Request) {
	principal, ok := DirectPrincipalFromContext(request.Context())
	if !ok || principal.Controller.Type() != domain.ControllerTypeDirectMTLS || principal.Controller.ID() == "" {
		writeDirectError(response, http.StatusForbidden, "environment_forbidden", "a mapped direct client identity is required")
		return
	}
	commandID, err := directCommandCancelIDFromPath(request.URL)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "command cancellation path contains an invalid ID")
		return
	}
	idempotencyKey, ok := directIdempotencyKey(request)
	if !ok {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "a valid Idempotency-Key header is required")
		return
	}
	body, err := s.readOptionalRequestBody(request)
	if err != nil {
		writeDirectRequestBodyError(response, err)
		return
	}
	if len(body) != 0 {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "command cancellation does not accept a request body")
		return
	}
	requestHash, err := directCancellationRequestHash(commandID)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "cancellation request cannot be hashed")
		return
	}
	result, serviceErr := s.service.CancelCommand(request.Context(), execution.CancelCommandRequest{
		CommandID: commandID, Controller: principal.Controller, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil && result.Command.CommandID == "" {
		status, code, message := directCommandError(serviceErr)
		writeDirectError(response, status, code, message)
		return
	}
	session, err := s.service.GetSession(request.Context(), result.Command.SessionID, principal.Controller)
	if err != nil {
		status, code, message := directSessionError(err)
		writeDirectError(response, status, code, message)
		return
	}
	// A populated record means the authority durably accepted the cancel request.
	// Report its current state even when the runtime could not confirm a stop.
	writeJSON(response, http.StatusAccepted, directCommandAcceptanceFromRecord(result.Command, session.Target, result.IdempotencyWarning))
}

func (s *directHTTPSAPI) handleCloseSession(response http.ResponseWriter, request *http.Request) {
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
	idempotencyKey, ok := directIdempotencyKey(request)
	if !ok {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "a valid Idempotency-Key header is required")
		return
	}
	body, err := s.readOptionalRequestBody(request)
	if err != nil {
		writeDirectRequestBodyError(response, err)
		return
	}
	policy, err := directClosePolicy(body)
	if err != nil {
		writeDirectError(response, http.StatusBadRequest, "invalid_request", "close policy body must contain one supported JSON object with a nonempty policy")
		return
	}
	requestHash, err := directCloseRequestHash(sessionID, policy)
	if err != nil {
		writeDirectError(response, http.StatusUnprocessableEntity, "invalid_request", "close request cannot be hashed")
		return
	}
	result, serviceErr := s.service.CloseSession(request.Context(), execution.CloseSessionRequest{
		SessionID: sessionID, Controller: principal.Controller, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, Policy: policy, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil && result.Session.SessionID == "" {
		status, code, message := directSessionError(serviceErr)
		writeDirectError(response, status, code, message)
		return
	}
	// The session record is the acceptance result; closed/lost is reported as
	// known state, not hidden behind a transport-shaped error.
	writeJSON(response, http.StatusAccepted, directSessionAcceptanceFromRecord(result.Session, result.IdempotencyWarning))
}

func directCommandTimeout(raw json.RawMessage) (time.Duration, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, domain.ErrInvalidRequestedLimits
	}
	var seconds int64
	if err := json.Unmarshal(trimmed, &seconds); err != nil || seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		return 0, domain.ErrInvalidRequestedLimits
	}
	return time.Duration(seconds) * time.Second, nil
}

func (s *directHTTPSAPI) readRequestBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, errors.New("request body is required")
	}
	return s.readOptionalRequestBody(request)
}

func (s *directHTTPSAPI) readOptionalRequestBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
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
	if len(body) > 0 {
		if err := domain.ValidateSerializedRequest(body); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func writeDirectRequestBodyError(response http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrSerializedInputTooLarge) {
		writeDirectError(response, http.StatusRequestEntityTooLarge, "invalid_request", "serialized request exceeds the 1 MiB limit")
		return
	}
	writeDirectError(response, http.StatusBadRequest, "invalid_request", "request body could not be read or is not valid UTF-8")
}

func directIdempotencyKey(request *http.Request) (string, bool) {
	key := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 256 || strings.IndexByte(key, 0) >= 0 {
		return "", false
	}
	return key, true
}

func directClosePolicy(body []byte) (string, error) {
	policy := "graceful"
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return policy, nil
	}
	if _, err := domain.CanonicalizeMutationRequestJSON("close_session", body, domain.CanonicalizationOptions{}); err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input struct {
		Policy json.RawMessage `json:"policy,omitempty"`
	}
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", errors.New("close policy body must contain one JSON value")
	}
	if len(input.Policy) == 0 {
		return policy, nil
	}
	if bytes.Equal(bytes.TrimSpace(input.Policy), []byte("null")) {
		return "", errors.New("close policy must be a string")
	}
	if err := json.Unmarshal(input.Policy, &policy); err != nil {
		return "", err
	}
	policy = strings.TrimSpace(policy)
	if policy == "" || strings.IndexByte(policy, 0) >= 0 {
		return "", errors.New("close policy must be nonempty")
	}
	return policy, nil
}

func directCancellationRequestHash(commandID domain.CommandID) (domain.CanonicalHash, error) {
	payload, err := json.Marshal(struct {
		Operation string `json:"operation"`
		CommandID string `json:"command_id"`
	}{Operation: "cancel_command", CommandID: string(commandID)})
	if err != nil {
		return domain.CanonicalHash{}, err
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("cancel_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		return domain.CanonicalHash{}, err
	}
	return domain.HashMutationRequestJSON("cancel_command", canonical, domain.CanonicalizationOptions{})
}

func directCloseRequestHash(sessionID domain.SessionID, policy string) (domain.CanonicalHash, error) {
	payload, err := json.Marshal(struct {
		Operation string `json:"operation"`
		SessionID string `json:"session_id"`
		Policy    string `json:"policy"`
	}{Operation: "close_session", SessionID: string(sessionID), Policy: policy})
	if err != nil {
		return domain.CanonicalHash{}, err
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("close_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		return domain.CanonicalHash{}, err
	}
	return domain.HashMutationRequestJSON("close_session", canonical, domain.CanonicalizationOptions{})
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

func newDirectCommandID() (domain.CommandID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return domain.NewCommandID("cmd-" + hex.EncodeToString(random[:]))
}

func newDirectRunIDs() (domain.JobID, domain.SessionID, domain.CommandID, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", "", err
	}
	jobID, err := domain.NewJobID("job-" + hex.EncodeToString(random[:]))
	if err != nil {
		return "", "", "", err
	}
	sessionID, err := newDirectSessionID()
	if err != nil {
		return "", "", "", err
	}
	commandID, err := newDirectCommandID()
	if err != nil {
		return "", "", "", err
	}
	return jobID, sessionID, commandID, nil
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

func directSessionCommandIDFromPath(requestURL *url.URL) (domain.SessionID, error) {
	const prefix = "/v1/sessions/"
	const suffix = "/commands"
	if requestURL == nil || !strings.HasPrefix(requestURL.Path, prefix) || !strings.HasSuffix(requestURL.Path, suffix) {
		return "", errors.New("session command path is invalid")
	}
	rawID := strings.TrimSuffix(strings.TrimPrefix(requestURL.EscapedPath(), prefix), suffix)
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("session command path ID is invalid")
	}
	return domain.NewSessionID(idText)
}

func directCommandIDFromPath(requestURL *url.URL) (domain.CommandID, error) {
	const prefix = "/v1/commands/"
	if requestURL == nil || !strings.HasPrefix(requestURL.Path, prefix) {
		return "", errors.New("command path is invalid")
	}
	rawID := strings.TrimPrefix(requestURL.EscapedPath(), prefix)
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("command path ID is invalid")
	}
	return domain.NewCommandID(idText)
}

func directJobIDFromPath(requestURL *url.URL) (domain.JobID, error) {
	const prefix = "/v1/jobs/"
	if requestURL == nil || !strings.HasPrefix(requestURL.Path, prefix) {
		return "", errors.New("job path prefix is invalid")
	}
	rawID := strings.TrimPrefix(requestURL.EscapedPath(), prefix)
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("job path ID is invalid")
	}
	return domain.NewJobID(idText)
}

func directCommandCancelIDFromPath(requestURL *url.URL) (domain.CommandID, error) {
	const prefix = "/v1/commands/"
	const suffix = "/cancel"
	if requestURL == nil || !strings.HasPrefix(requestURL.Path, prefix) || !strings.HasSuffix(requestURL.Path, suffix) {
		return "", errors.New("command cancellation path is invalid")
	}
	rawID := strings.TrimSuffix(strings.TrimPrefix(requestURL.EscapedPath(), prefix), suffix)
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("command cancellation path ID is invalid")
	}
	return domain.NewCommandID(idText)
}

func directSessionAcceptanceFromRecord(record store.SessionRecord, idempotencyWarning bool) directSessionAcceptance {
	return directSessionAcceptance{
		ResourceID:         string(record.SessionID),
		SessionID:          string(record.SessionID),
		AcceptanceScope:    "target_authority",
		ExecutionTarget:    targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState:         directKnownState{SessionState: string(record.State)},
		IdempotencyWarning: directIdempotencyWarning(idempotencyWarning),
	}
}

func directCommandAcceptanceFromRecord(record store.CommandRecord, target domain.ExecutionTarget, idempotencyWarning bool) directCommandAcceptance {
	return directCommandAcceptance{
		ResourceID:         string(record.CommandID),
		CommandID:          string(record.CommandID),
		SessionID:          string(record.SessionID),
		AcceptanceScope:    "target_authority",
		ExecutionTarget:    targetResponse{Kind: string(target.Kind()), Profile: target.Profile()},
		KnownState:         directKnownState{CommandState: string(record.State)},
		IdempotencyWarning: directIdempotencyWarning(idempotencyWarning),
	}
}

func directIdempotencyWarning(warning bool) string {
	if warning {
		return "deduplication_not_guaranteed"
	}
	return ""
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

func directCommandError(err error) (int, string, string) {
	switch {
	case errors.Is(err, store.ErrSessionNotFound), errors.Is(err, store.ErrCommandNotFound):
		return http.StatusNotFound, "resource_not_found", "session or command not found"
	case errors.Is(err, execution.ErrSessionController):
		return http.StatusForbidden, "controller_mismatch", "resource belongs to another controller"
	case errors.Is(err, execution.ErrSessionNotReady), errors.Is(err, execution.ErrCommandNotReady):
		return http.StatusUnprocessableEntity, "session_not_ready", "session is not ready to accept commands"
	case errors.Is(err, store.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different request"
	case errors.Is(err, domain.ErrScriptTooLarge), errors.Is(err, domain.ErrScriptInvalidUTF8), errors.Is(err, domain.ErrInvalidRequestedLimits), errors.Is(err, domain.ErrLimitExceedsServiceCeiling), errors.Is(err, store.ErrIdempotencyKey), errors.Is(err, store.ErrCommandSessionState):
		return http.StatusUnprocessableEntity, "invalid_request", "command request is invalid for the session"
	case errors.Is(err, execution.ErrRuntimeUnavailable), errors.Is(err, execution.ErrExecutionServiceConfiguration):
		return http.StatusServiceUnavailable, "runtime_unavailable", "command authority is unavailable"
	default:
		return http.StatusServiceUnavailable, "runtime_unavailable", "command request could not be completed"
	}
}

func directJobError(err error) (int, string, string) {
	switch {
	case errors.Is(err, store.ErrJobNotFound):
		return http.StatusNotFound, "resource_not_found", "job not found"
	case errors.Is(err, execution.ErrSessionController):
		return http.StatusForbidden, "controller_mismatch", "job belongs to another controller"
	case errors.Is(err, domain.ErrControllerMismatch):
		return http.StatusForbidden, "environment_forbidden", "controller is not authorized for this environment"
	case errors.Is(err, store.ErrIdempotencyConflict), errors.Is(err, store.ErrJobExists):
		return http.StatusConflict, "idempotency_conflict", "Idempotency-Key or job identity conflicts with an existing request"
	case errors.Is(err, domain.ErrScriptTooLarge):
		return http.StatusRequestEntityTooLarge, "invalid_request", "script exceeds the 128 KiB UTF-8 limit"
	case errors.Is(err, store.ErrSessionCapacityExceeded):
		return http.StatusTooManyRequests, "quota_exceeded", "active session capacity is full"
	case errors.Is(err, execution.ErrEnvironmentUnavailable):
		return http.StatusUnprocessableEntity, "invalid_request", "environment is not configured"
	case errors.Is(err, domain.ErrEnvironmentTargetMismatch):
		return http.StatusUnprocessableEntity, "environment_target_mismatch", "environment does not allow the requested target"
	case errors.Is(err, domain.ErrEnvironmentSourceMismatch), errors.Is(err, domain.ErrRepositoryAliasNotAllowed), errors.Is(err, domain.ErrUnsupportedIsolationRequirement), errors.Is(err, domain.ErrInvalidRequestedLimits), errors.Is(err, domain.ErrLimitExceedsServiceCeiling), errors.Is(err, domain.ErrInvalidSource), errors.Is(err, domain.ErrInvalidTargetKind), errors.Is(err, domain.ErrEmptyTargetProfile), errors.Is(err, domain.ErrScriptInvalidUTF8), errors.Is(err, store.ErrIdempotencyKey), errors.Is(err, store.ErrInvalidJob):
		return http.StatusUnprocessableEntity, "invalid_request", "job request is invalid for the selected environment"
	case errors.Is(err, execution.ErrRuntimeUnavailable), errors.Is(err, execution.ErrExecutionServiceConfiguration):
		return http.StatusServiceUnavailable, "runtime_unavailable", "job authority is unavailable"
	default:
		return http.StatusServiceUnavailable, "runtime_unavailable", "job request could not be completed"
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
