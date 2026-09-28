package localapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const DefaultMaxBodyBytes int64 = domain.MaxSerializedRequestBytes

const localAPIEventLastSequenceHeader = "X-Runner-Last-Sequence"

var (
	ErrConfiguration = errors.New("local API configuration is invalid")
	ErrSocketPath    = errors.New("local API socket path is invalid")
)

// ServerOptions configures the Mac-local owner-only API. The server uses only
// local SQLite and an AF_UNIX listener; it has no remote transport dependency.
type ServerOptions struct {
	Authority    *store.AuthorityStore
	Owner        domain.ControllerIdentity
	SocketPath   string
	MaxBodyBytes int64
}

// Server is the Mac-local HTTP/JSON adapter over an owner-only Unix socket.
type Server struct {
	authority    *store.AuthorityStore
	owner        domain.ControllerIdentity
	socketPath   string
	maxBodyBytes int64
	httpServer   *http.Server
	listener     net.Listener
	closed       bool
}

func NewServer(options ServerOptions) (*Server, error) {
	if options.Authority == nil {
		return nil, ErrConfiguration
	}
	if err := validateSocketPath(options.SocketPath); err != nil {
		return nil, err
	}
	owner := options.Owner
	if owner.Type() == "" && owner.ID() == "" {
		var err error
		owner, err = domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
		if err != nil {
			return nil, ErrConfiguration
		}
	}
	if owner.Type() != domain.ControllerTypeLocalUser || owner.ID() == "" {
		return nil, fmt.Errorf("%w: owner must be a local_user controller", ErrConfiguration)
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if options.MaxBodyBytes > domain.MaxSerializedRequestBytes {
		return nil, fmt.Errorf("%w: body limit exceeds shared request ceiling", ErrConfiguration)
	}
	return &Server{
		authority:    options.Authority,
		owner:        owner,
		socketPath:   options.SocketPath,
		maxBodyBytes: options.MaxBodyBytes,
		httpServer:   &http.Server{},
	}, nil
}

func (s *Server) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.socketPath
}

func (s *Server) Listen() error {
	if s == nil {
		return ErrConfiguration
	}
	if s.listener != nil {
		return nil
	}
	if info, err := os.Lstat(s.socketPath); err == nil {
		return fmt.Errorf("%w: socket path already exists as %s", ErrSocketPath, info.Mode().Type())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect socket path: %v", ErrSocketPath, err)
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("%w: listen: %v", ErrSocketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("%w: chmod socket: %v", ErrSocketPath, err)
	}
	s.listener = listener
	s.httpServer.Handler = http.HandlerFunc(s.serveHTTP)
	return nil
}

func (s *Server) Serve() error {
	if s == nil || s.listener == nil {
		return ErrSocketPath
	}
	err := s.httpServer.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if ctx == nil {
		ctx = context.Background()
	}
	shutdownErr := s.httpServer.Shutdown(ctx)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	removeErr := os.Remove(s.socketPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return removeErr
}

type createSessionRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget targetRequest   `json:"execution_target"`
	Source          *sourceRequest  `json:"source,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

type targetRequest struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

type sourceRequest struct {
	Mode              string `json:"mode"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	ResolvedCommit    string `json:"resolved_commit,omitempty"`
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

type sessionAcceptance struct {
	ResourceID      string         `json:"resource_id"`
	SessionID       string         `json:"session_id"`
	IntentID        string         `json:"intent_id,omitempty"`
	AcceptanceScope string         `json:"acceptance_scope"`
	ExecutionTarget targetResponse `json:"execution_target"`
	KnownState      knownState     `json:"known_state"`
}

type knownState struct {
	DeliveryState string `json:"delivery_state,omitempty"`
}

type sessionRead struct {
	View     string `json:"view"`
	IsStale  bool   `json:"is_stale"`
	Resource any    `json:"resource"`
}

type sessionIntentResource struct {
	SessionID       string         `json:"session_id"`
	ExecutionTarget targetResponse `json:"execution_target"`
	Controller      controllerView `json:"controller"`
	ObservedAt      time.Time      `json:"observed_at"`
	Environment     string         `json:"environment"`
	Source          sourceResponse `json:"source"`
	DeliveryState   string         `json:"delivery_state"`
	Reason          string         `json:"reason,omitempty"`
}

type sessionProjectionResource struct {
	SessionID         string               `json:"session_id"`
	SessionState      string               `json:"session_state"`
	ExecutionTarget   targetResponse       `json:"execution_target"`
	Authority         string               `json:"authority"`
	Controller        controllerView       `json:"controller"`
	ObservedAt        time.Time            `json:"observed_at"`
	Environment       string               `json:"environment"`
	Source            sourceResponse       `json:"source"`
	Capabilities      capabilitiesResponse `json:"capabilities"`
	RuntimeGeneration string               `json:"runtime_generation,omitempty"`
	ResolvedRevision  string               `json:"resolved_revision,omitempty"`
	IsStale           bool                 `json:"is_stale,omitempty"`
}

type capabilitiesResponse struct {
	HostClass        string         `json:"host_class"`
	Isolation        string         `json:"isolation"`
	EffectiveAccount string         `json:"effective_account"`
	ServiceLimits    map[string]any `json:"service_limits"`
}

type targetResponse struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

type controllerView struct {
	Type string `json:"controller_type"`
	ID   string `json:"controller_id"`
}

type sourceResponse struct {
	Mode              string `json:"mode"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	ResolvedCommit    string `json:"resolved_commit,omitempty"`
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

type errorEnvelope struct {
	Code       string                    `json:"code"`
	Message    string                    `json:"message"`
	Retryable  bool                      `json:"retryable"`
	ResourceID string                    `json:"resource_id,omitempty"`
	Details    *localEventHistoryDetails `json:"details,omitempty"`
}

type localEventHistoryDetails struct {
	OutputComplete          bool   `json:"output_complete"`
	OutputUnavailableReason string `json:"output_unavailable_reason"`
}

func (s *Server) serveHTTP(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/sessions":
		s.handleCreateSession(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/jobs":
		s.handleCreateJob(response, request)
	case request.Method == http.MethodPost:
		if commandID, ok := commandCancelPath(request.URL.Path); ok {
			s.handleCancelCommand(response, request, commandID)
			return
		}
		if sessionID, ok := sessionCommandPath(request.URL.Path); ok {
			s.handleSubmitCommand(response, request, sessionID)
			return
		}
		writeError(response, http.StatusNotFound, "resource_not_found", "local API route not found")
	case request.Method == http.MethodDelete:
		if sessionID, ok := sessionResourcePath(request.URL.Path); ok {
			s.handleCloseSession(response, request, sessionID)
			return
		}
		writeError(response, http.StatusNotFound, "resource_not_found", "local API route not found")
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/events") && strings.HasPrefix(request.URL.Path, "/v1/commands/"):
		s.handleCommandEvents(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/commands/"):
		s.handleGetCommand(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/sessions/"):
		s.handleGetSession(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/jobs/"):
		s.handleGetJob(response, request)
	default:
		writeError(response, http.StatusNotFound, "resource_not_found", "local API route not found")
	}
}

type submitCommandRequest struct {
	Script         *string         `json:"script"`
	TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
}

type commandAcceptance struct {
	ResourceID      string         `json:"resource_id"`
	CommandID       string         `json:"command_id"`
	SessionID       string         `json:"session_id"`
	IntentID        string         `json:"intent_id,omitempty"`
	AcceptanceScope string         `json:"acceptance_scope"`
	ExecutionTarget targetResponse `json:"execution_target"`
	KnownState      knownState     `json:"known_state"`
}

type closeAcceptance struct {
	ResourceID      string         `json:"resource_id"`
	SessionID       string         `json:"session_id"`
	IntentID        string         `json:"intent_id,omitempty"`
	AcceptanceScope string         `json:"acceptance_scope"`
	ExecutionTarget targetResponse `json:"execution_target"`
	KnownState      knownState     `json:"known_state"`
}

type cancelCommandRequest struct {
	CommandID string `json:"command_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type closeSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Policy    string `json:"policy,omitempty"`
}

type createJobRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget targetRequest   `json:"execution_target"`
	Source          *sourceRequest  `json:"source,omitempty"`
	Script          *string         `json:"script"`
	TimeoutSeconds  json.RawMessage `json:"timeout_seconds,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

type jobAcceptance struct {
	ResourceID      string         `json:"resource_id"`
	JobID           string         `json:"job_id"`
	SessionID       string         `json:"session_id"`
	CommandID       string         `json:"command_id"`
	IntentID        string         `json:"intent_id,omitempty"`
	AcceptanceScope string         `json:"acceptance_scope"`
	ExecutionTarget targetResponse `json:"execution_target"`
	KnownState      knownState     `json:"known_state"`
}

type jobRead struct {
	View     string            `json:"view"`
	IsStale  bool              `json:"is_stale"`
	Resource jobIntentResource `json:"resource"`
}

type jobAuthorityRead struct {
	View     string                `json:"view"`
	IsStale  bool                  `json:"is_stale"`
	Resource jobProjectionResource `json:"resource"`
}

type jobProjectionRead struct {
	View     string                `json:"view"`
	IsStale  bool                  `json:"is_stale"`
	Resource jobProjectionResource `json:"resource"`
}

type jobIntentResource struct {
	JobID           string         `json:"job_id"`
	SessionID       string         `json:"session_id"`
	CommandID       string         `json:"command_id"`
	ExecutionTarget targetResponse `json:"execution_target"`
	Controller      controllerView `json:"controller"`
	ObservedAt      time.Time      `json:"observed_at"`
	Environment     string         `json:"environment"`
	Source          sourceResponse `json:"source"`
	DeliveryState   string         `json:"delivery_state"`
	Reason          string         `json:"reason,omitempty"`
}

type jobProjectionResource struct {
	JobID                   string               `json:"job_id"`
	SessionID               string               `json:"session_id"`
	CommandID               string               `json:"command_id"`
	JobPhase                string               `json:"job_phase"`
	CommandState            *string              `json:"command_state,omitempty"`
	ExitCode                *int                 `json:"exit_code,omitempty"`
	FinalEventSequence      *int64               `json:"final_event_sequence,omitempty"`
	OutputComplete          bool                 `json:"output_complete"`
	OutputTruncated         bool                 `json:"output_truncated"`
	OutputUnavailableReason string               `json:"output_unavailable_reason,omitempty"`
	TeardownState           string               `json:"teardown_state"`
	TeardownReason          string               `json:"teardown_reason,omitempty"`
	ExecutionTarget         targetResponse       `json:"execution_target"`
	Authority               string               `json:"authority"`
	Controller              controllerView       `json:"controller"`
	ObservedAt              time.Time            `json:"observed_at"`
	Environment             string               `json:"environment"`
	Source                  sourceResponse       `json:"source"`
	Capabilities            capabilitiesResponse `json:"capabilities"`
	IsStale                 bool                 `json:"is_stale,omitempty"`
}

type commandRead struct {
	View     string `json:"view"`
	IsStale  bool   `json:"is_stale"`
	Resource any    `json:"resource"`
}

// localAPICommandEvent is the public v1 event shape. Output payloads are
// always base64 encoded so arbitrary command bytes survive the JSON stream;
// lifecycle events carry only the common identity fields.
type localAPICommandEvent struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Ordinal    int64     `json:"ordinal,omitempty"`
	Encoding   string    `json:"encoding,omitempty"`
	DataBase64 string    `json:"data_base64,omitempty"`
	ByteCount  int64     `json:"byte_count,omitempty"`
}

type commandIntentResource struct {
	CommandID       string         `json:"command_id"`
	SessionID       string         `json:"session_id"`
	ExecutionTarget targetResponse `json:"execution_target"`
	Controller      controllerView `json:"controller"`
	ObservedAt      time.Time      `json:"observed_at"`
	Environment     string         `json:"environment"`
	Source          sourceResponse `json:"source"`
	DeliveryState   string         `json:"delivery_state"`
	Reason          string         `json:"reason,omitempty"`
}

type commandProjectionResource struct {
	CommandID               string               `json:"command_id"`
	SessionID               string               `json:"session_id"`
	Ordinal                 int64                `json:"ordinal"`
	CommandState            string               `json:"command_state"`
	ExitCode                *int                 `json:"exit_code,omitempty"`
	FinalEventSequence      *int64               `json:"final_event_sequence,omitempty"`
	OutputComplete          bool                 `json:"output_complete"`
	OutputTruncated         bool                 `json:"output_truncated"`
	OutputUnavailableReason string               `json:"output_unavailable_reason,omitempty"`
	ExecutionTarget         targetResponse       `json:"execution_target"`
	Authority               string               `json:"authority"`
	Controller              controllerView       `json:"controller"`
	ObservedAt              time.Time            `json:"observed_at"`
	Environment             string               `json:"environment"`
	Source                  sourceResponse       `json:"source"`
	Capabilities            capabilitiesResponse `json:"capabilities"`
	IsStale                 bool                 `json:"is_stale,omitempty"`
}

func (s *Server) handleSubmitCommand(response http.ResponseWriter, request *http.Request, rawSessionID string) {
	key := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
		return
	}
	sessionIDText, err := url.PathUnescape(rawSessionID)
	if err != nil || sessionIDText == "" || strings.Contains(sessionIDText, "/") {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid session path")
		return
	}
	if _, err := domain.NewSessionID(sessionIDText); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body, err := s.readBody(request)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_request"
		if errors.Is(err, domain.ErrSerializedInputTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "request_too_large"
		}
		writeError(response, status, code, err.Error())
		return
	}
	acceptance, failure := s.acceptSubmitCommandIntent(request.Context(), key, sessionIDText, body)
	if failure != nil {
		writeError(response, failure.status, failure.code, failure.message)
		return
	}
	writeJSON(response, http.StatusAccepted, acceptance)
}

func (s *Server) acceptSubmitCommandIntent(ctx context.Context, key, sessionIDText string, body []byte) (commandAcceptance, *localOperationFailure) {
	if strings.TrimSpace(key) == "" {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
	}
	var input submitCommandRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "malformed or unsupported request JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
	}
	sessionID, err := domain.NewSessionID(sessionIDText)
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if input.Script == nil {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "script is required")
	}
	script := *input.Script
	if err := domain.ValidateScriptUTF8(script); err != nil {
		if errors.Is(err, domain.ErrScriptTooLarge) {
			return commandAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		}
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_script", err.Error())
	}
	timeoutSeconds, hasTimeout, err := parseTimeoutSeconds(input.TimeoutSeconds)
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	session, err := s.authority.GetLocalIntentByResource(ctx, "create_session", string(sessionID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return commandAcceptance{}, localFailure(http.StatusNotFound, "session_not_found", "local session intent was not found")
		}
		status, code := statusForStoreError(err)
		return commandAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	payload := map[string]any{"session_id": string(sessionID), "script": script}
	if hasTimeout {
		payload["timeout_seconds"] = timeoutSeconds
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "could not encode command request")
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payloadJSON, domain.CanonicalizationOptions{})
	if err != nil || int64(len(canonical)) > s.maxBodyBytes {
		return commandAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", "canonical command request exceeds the configured body limit")
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
	}
	commandIDText, err := newOpaqueID("cmd-")
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate command identity")
	}
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not validate command identity")
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
	}
	record, _, err := s.authority.AcceptLocalIntent(ctx, store.LocalIntentCreate{
		IntentID:       domain.IntentID(intentID),
		Operation:      "submit_command",
		ResourceID:     string(commandID),
		SessionID:      sessionID,
		CommandID:      commandID,
		Target:         session.Target,
		Environment:    session.Environment,
		Controller:     s.owner,
		Source:         session.Source,
		RequestHash:    hash,
		IdempotencyKey: key,
		PayloadJSON:    canonical,
		ScriptBytes:    []byte(script),
		DeliveryState:  store.LocalIntentRecorded,
	})
	if err != nil {
		status, code := statusForStoreError(err)
		return commandAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	return commandAcceptance{
		ResourceID: string(record.CommandID), CommandID: string(record.CommandID), SessionID: string(record.SessionID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	}, nil
}

func (s *Server) handleCancelCommand(response http.ResponseWriter, request *http.Request, rawCommandID string) {
	if strings.TrimSpace(request.Header.Get("Idempotency-Key")) == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
		return
	}
	commandIDText, err := url.PathUnescape(rawCommandID)
	if err != nil || commandIDText == "" || strings.Contains(commandIDText, "/") {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid command path")
		return
	}
	if _, err := domain.NewCommandID(commandIDText); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body, err := s.readBody(request)
	if err != nil {
		status, code := requestReadError(err)
		writeError(response, status, code, err.Error())
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	var input cancelCommandRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported cancel request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(response, http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
		return
	}
	if input.CommandID != "" && input.CommandID != commandIDText {
		writeError(response, http.StatusBadRequest, "invalid_request", "command path and body command_id differ")
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if len(reason) > 256 || strings.IndexByte(reason, 0) >= 0 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "reason is invalid")
		return
	}
	accepted, failure := s.acceptCancelCommandIntent(request.Context(), request.Header.Get("Idempotency-Key"), commandIDText, reason)
	if failure != nil {
		writeError(response, failure.status, failure.code, failure.message)
		return
	}
	writeJSON(response, http.StatusAccepted, accepted)
}

func (s *Server) acceptCancelCommandIntent(ctx context.Context, key, commandIDText, reason string) (commandAcceptance, *localOperationFailure) {
	if strings.TrimSpace(key) == "" {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
	}
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if len(reason) > 256 || strings.IndexByte(reason, 0) >= 0 {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "reason is invalid")
	}
	submitIntent, err := s.authority.GetLocalIntentByResource(ctx, "submit_command", commandIDText, s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return commandAcceptance{}, localFailure(http.StatusNotFound, "command_not_found", "local command intent was not found")
		}
		status, code := statusForStoreError(err)
		if code == "session_not_found" {
			code = "command_not_found"
		}
		return commandAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	payload := map[string]any{"command_id": commandIDText}
	if reason != "" {
		payload["reason"] = reason
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "could not encode cancel request")
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("cancel_command", payloadJSON, domain.CanonicalizationOptions{})
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical cancel request is invalid")
	}
	if int64(len(canonical)) > s.maxBodyBytes {
		return commandAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", "canonical cancel request exceeds the configured body limit")
	}
	hash, err := domain.HashMutationRequestJSON("cancel_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		return commandAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
	}
	record, _, err := s.authority.AcceptLocalIntent(ctx, store.LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "cancel_command", ResourceID: commandIDText,
		SessionID: submitIntent.SessionID, CommandID: commandID, Target: submitIntent.Target,
		Environment: submitIntent.Environment, Controller: s.owner, Source: submitIntent.Source,
		RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical,
		DeliveryState: store.LocalIntentRecorded,
	})
	if err != nil {
		status, code := statusForStoreError(err)
		return commandAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	return commandAcceptance{
		ResourceID: string(record.CommandID), CommandID: string(record.CommandID), SessionID: string(record.SessionID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	}, nil
}

func (s *Server) handleCloseSession(response http.ResponseWriter, request *http.Request, rawSessionID string) {
	if strings.TrimSpace(request.Header.Get("Idempotency-Key")) == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
		return
	}
	sessionIDText, err := url.PathUnescape(rawSessionID)
	if err != nil || sessionIDText == "" || strings.Contains(sessionIDText, "/") {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid session path")
		return
	}
	if _, err := domain.NewSessionID(sessionIDText); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "mode" || len(query[key]) != 1 {
			writeError(response, http.StatusBadRequest, "invalid_request", "unsupported or repeated close query parameter")
			return
		}
	}
	body, err := s.readBody(request)
	if err != nil {
		status, code := requestReadError(err)
		writeError(response, status, code, err.Error())
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	var input closeSessionRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported close request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(response, http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
		return
	}
	if input.SessionID != "" && input.SessionID != sessionIDText {
		writeError(response, http.StatusBadRequest, "invalid_request", "session path and body session_id differ")
		return
	}
	policy := strings.TrimSpace(input.Policy)
	if queryPolicy := strings.TrimSpace(query.Get("mode")); queryPolicy != "" {
		if policy != "" && policy != queryPolicy {
			writeError(response, http.StatusUnprocessableEntity, "invalid_request", "close mode and policy differ")
			return
		}
		policy = queryPolicy
	}
	if policy == "" {
		policy = "cancel"
	}
	if len(policy) > 64 || strings.IndexByte(policy, 0) >= 0 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "close policy is invalid")
		return
	}
	accepted, failure := s.acceptCloseSessionIntent(request.Context(), request.Header.Get("Idempotency-Key"), sessionIDText, policy)
	if failure != nil {
		writeError(response, failure.status, failure.code, failure.message)
		return
	}
	writeJSON(response, http.StatusAccepted, accepted)
}

func (s *Server) acceptCloseSessionIntent(ctx context.Context, key, sessionIDText, policy string) (closeAcceptance, *localOperationFailure) {
	if strings.TrimSpace(key) == "" {
		return closeAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
	}
	sessionID, err := domain.NewSessionID(sessionIDText)
	if err != nil {
		return closeAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", err.Error())
	}
	policy = strings.TrimSpace(policy)
	if policy == "" {
		policy = "cancel"
	}
	if len(policy) > 64 || strings.IndexByte(policy, 0) >= 0 {
		return closeAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "close policy is invalid")
	}
	createIntent, err := s.authority.GetLocalIntentByResource(ctx, "create_session", sessionIDText, s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			return closeAcceptance{}, localFailure(http.StatusNotFound, "session_not_found", "local session intent was not found")
		}
		status, code := statusForStoreError(err)
		return closeAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	payloadJSON, err := json.Marshal(map[string]any{"session_id": sessionIDText, "policy": policy})
	if err != nil {
		return closeAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "could not encode close request")
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("close_session", payloadJSON, domain.CanonicalizationOptions{})
	if err != nil {
		return closeAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical close request is invalid")
	}
	if int64(len(canonical)) > s.maxBodyBytes {
		return closeAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", "canonical close request exceeds the configured body limit")
	}
	hash, err := domain.HashMutationRequestJSON("close_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		return closeAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		return closeAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
	}
	record, _, err := s.authority.AcceptLocalIntent(ctx, store.LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "close_session", ResourceID: sessionIDText,
		SessionID: sessionID, Target: createIntent.Target, Environment: createIntent.Environment,
		Controller: s.owner, Source: createIntent.Source, RequestHash: hash,
		IdempotencyKey: key, PayloadJSON: canonical, DeliveryState: store.LocalIntentRecorded,
	})
	if err != nil {
		status, code := statusForStoreError(err)
		return closeAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	return closeAcceptance{
		ResourceID: string(record.SessionID), SessionID: string(record.SessionID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	}, nil
}

func (s *Server) canonicalLocalMutation(response http.ResponseWriter, operation string, payload map[string]any) ([]byte, domain.CanonicalHash, bool) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "could not encode mutation request")
		return nil, domain.CanonicalHash{}, false
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON(operation, payloadJSON, domain.CanonicalizationOptions{})
	if err != nil || int64(len(canonical)) > s.maxBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "canonical mutation request exceeds the configured body limit")
		return nil, domain.CanonicalHash{}, false
	}
	hash, err := domain.HashMutationRequestJSON(operation, canonical, domain.CanonicalizationOptions{})
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
		return nil, domain.CanonicalHash{}, false
	}
	return canonical, hash, true
}

func (s *Server) handleGetCommand(response http.ResponseWriter, request *http.Request) {
	if len(request.URL.Query()) != 0 {
		writeError(response, http.StatusBadRequest, "invalid_request", "controller query parameters are not accepted")
		return
	}
	rawID := strings.TrimPrefix(request.URL.Path, "/v1/commands/")
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid command path")
		return
	}
	commandID, err := domain.NewCommandID(idText)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	record, err := s.authority.GetLocalIntentByResource(request.Context(), "submit_command", string(commandID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			writeError(response, http.StatusNotFound, "command_not_found", "local command intent was not found")
			return
		}
		status, code := statusForStoreError(err)
		if code == "session_not_found" {
			code = "command_not_found"
		}
		writeError(response, status, code, sanitizeError(err))
		return
	}
	if remoteProjectionEligible(record) {
		projection, projectionErr := s.authority.GetRemoteCommandProjection(request.Context(), commandID)
		if projectionErr == nil {
			writeJSON(response, http.StatusOK, commandRead{View: "projection", IsStale: projection.IsStale, Resource: commandProjectionResourceFromProjection(projection)})
			return
		}
		if !errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
			status, code := statusForStoreError(projectionErr)
			writeError(response, status, code, sanitizeError(projectionErr))
			return
		}
	}
	writeJSON(response, http.StatusOK, commandRead{View: "local_intent", IsStale: false, Resource: commandIntentResourceFromRecord(record)})
}

func (s *Server) handleCommandEvents(response http.ResponseWriter, request *http.Request) {
	commandID, err := commandEventsPathID(request.URL.Path)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "after" && key != "follow" {
			writeError(response, http.StatusBadRequest, "invalid_request", "unsupported event query parameter")
			return
		}
		if len(query[key]) != 1 {
			writeError(response, http.StatusBadRequest, "invalid_request", "event query parameter must occur once")
			return
		}
	}
	after, err := parseLocalEventCursor(query.Get("after"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	follow, err := parseLocalEventFollow(query.Get("follow"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	intent, err := s.authority.GetLocalIntentByResource(request.Context(), "submit_command", string(commandID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			writeError(response, http.StatusNotFound, "command_not_found", "local command intent was not found")
			return
		}
		status, code := statusForCommandEventError(err)
		writeError(response, status, code, sanitizeError(err))
		return
	}
	if intent.Target.Kind() != domain.TargetKindLocal {
		if !remoteProjectionEligible(intent) {
			writeError(response, http.StatusConflict, "events_unavailable", "remote command events are not available before remote acceptance")
			return
		}
		s.handleMirroredRemoteEvents(response, request, commandID, after, follow)
		return
	}
	command, err := s.authority.GetCommand(request.Context(), commandID)
	if err != nil {
		status, code := statusForCommandEventError(err)
		writeError(response, status, code, sanitizeError(err))
		return
	}
	session, err := s.authority.GetSession(request.Context(), command.SessionID)
	if err != nil {
		status, code := statusForCommandEventError(err)
		writeError(response, status, code, sanitizeError(err))
		return
	}
	if command.SessionID != intent.SessionID || session.Target.Kind() != domain.TargetKindLocal || session.Controller.Type() != s.owner.Type() || session.Controller.ID() != s.owner.ID() ||
		domain.CompareIdempotency(command.RequestHash, intent.RequestHash) != domain.IdempotencySamePayload || !bytes.Equal(command.ScriptBytes, intent.ScriptBytes) {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "local authority command does not match its accepted intent")
		return
	}
	if command.OutputUnavailableReason == "retention_expired" {
		writeLocalEventHistoryError(response, commandID, "retention_expired")
		return
	}
	if !follow {
		events, err := s.authority.ReplayCommandEvents(request.Context(), commandID, after)
		if err != nil {
			s.writeCommandEventReadError(response, commandID, err)
			return
		}
		frames, err := encodeLocalAPIEvents(events, command.Ordinal)
		if err != nil {
			writeError(response, http.StatusServiceUnavailable, "database_unavailable", "stored command events could not be encoded")
			return
		}
		last := after
		if len(events) > 0 {
			last = events[len(events)-1].Sequence
		}
		setLocalAPIEventHeaders(response, "authority", false)
		response.Header().Set(localAPIEventLastSequenceHeader, strconv.FormatInt(last, 10))
		response.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			if err := writeLocalAPIEventFrame(response, frame); err != nil {
				return
			}
		}
		return
	}
	if command.FinalEventSequence != nil && after >= *command.FinalEventSequence {
		setLocalAPIEventHeaders(response, "authority", false)
		response.Header().Set(localAPIEventLastSequenceHeader, strconv.FormatInt(after, 10))
		response.WriteHeader(http.StatusOK)
		return
	}
	subscription, err := s.authority.SubscribeCommandEvents(request.Context(), commandID, after, 256)
	if err != nil {
		s.writeCommandEventReadError(response, commandID, err)
		return
	}
	defer subscription.Close()
	setLocalAPIEventHeaders(response, "authority", false)
	response.Header().Set("Trailer", localAPIEventLastSequenceHeader)
	response.WriteHeader(http.StatusOK)
	lastWritten := after
	defer func() {
		response.Header().Set(localAPIEventLastSequenceHeader, strconv.FormatInt(lastWritten, 10))
	}()
	flusher, _ := response.(http.Flusher)
	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				// Overflow closes the channel after preserving the bounded prefix.
				// Draining it keeps the published cursor resumable.
				return
			}
			frame, err := encodeLocalAPIEvent(event, command.Ordinal)
			if err != nil || writeLocalAPIEventFrame(response, frame) != nil {
				return
			}
			lastWritten = event.Sequence
			if flusher != nil {
				flusher.Flush()
			}
			if isTerminalLocalAPIEvent(event.Type) {
				return
			}
		case <-request.Context().Done():
			return
		}
	}
}

// handleMirroredRemoteEvents serves only the durable Mac mirror. It does not
// contact the remote authority; the Router owns transport and advances the
// cursor before an event becomes visible here.
func (s *Server) handleMirroredRemoteEvents(response http.ResponseWriter, request *http.Request, commandID domain.CommandID, after int64, follow bool) {
	projection, projectionErr := s.authority.GetRemoteCommandProjection(request.Context(), commandID)
	if projectionErr != nil && !errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
		status, code := statusForStoreError(projectionErr)
		writeError(response, status, code, sanitizeError(projectionErr))
		return
	}
	ordinal := int64(0)
	stale := true
	if projectionErr == nil {
		ordinal = projection.Ordinal
		stale = projection.IsStale
	}
	if projectionErr == nil && projection.OutputUnavailableReason == "retention_expired" {
		writeLocalEventHistoryError(response, commandID, "retention_expired")
		return
	}
	if projectionErr == nil && projection.OutputUnavailableReason == "remote_event_gap" && projection.FinalEventSequence != nil && after < *projection.FinalEventSequence {
		writeLocalEventHistoryError(response, commandID, "remote_event_gap")
		return
	}
	gap, gapErr := s.authority.GetRemoteEventGap(request.Context(), commandID)
	if gapErr == nil && after < gap.MissingTo {
		writeLocalEventHistoryError(response, commandID, "remote_event_gap")
		return
	}
	if gapErr != nil && !errors.Is(gapErr, store.ErrRemoteGapNotFound) {
		status, code := statusForStoreError(gapErr)
		writeError(response, status, code, sanitizeError(gapErr))
		return
	}
	if projectionErr == nil && projection.FinalEventSequence != nil && after >= *projection.FinalEventSequence {
		setLocalAPIEventHeaders(response, "projection", stale)
		response.Header().Set(localAPIEventLastSequenceHeader, strconv.FormatInt(after, 10))
		response.WriteHeader(http.StatusOK)
		return
	}
	events, err := s.authority.ListRemoteEvents(request.Context(), commandID, after)
	if err != nil {
		s.writeRemoteEventReadError(response, commandID, err)
		return
	}
	frames, err := encodeRemoteAPIEvents(events, ordinal)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "stored remote events could not be encoded")
		return
	}
	lastSequence := after
	if len(events) > 0 {
		lastSequence = events[len(events)-1].Sequence
	}
	setLocalAPIEventHeaders(response, "projection", stale)
	if !follow {
		response.Header().Set(localAPIEventLastSequenceHeader, strconv.FormatInt(lastSequence, 10))
		response.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			if writeLocalAPIEventFrame(response, frame) != nil {
				return
			}
		}
		return
	}
	response.Header().Set("Trailer", localAPIEventLastSequenceHeader)
	response.WriteHeader(http.StatusOK)
	flusher, _ := response.(http.Flusher)
	last := after
	defer func() {
		response.Header().Set(localAPIEventLastSequenceHeader, strconv.FormatInt(last, 10))
	}()
	writeEvents := func(values []store.RemoteEventRecord, prepared [][]byte) bool {
		for index, event := range values {
			if event.Sequence <= last {
				continue
			}
			var frame []byte
			if index < len(prepared) {
				frame = prepared[index]
			} else {
				frame, err = encodeRemoteAPIEvent(event, ordinal)
				if err != nil {
					return false
				}
			}
			if writeLocalAPIEventFrame(response, frame) != nil {
				return false
			}
			last = event.Sequence
			if flusher != nil {
				flusher.Flush()
			}
			if isTerminalLocalAPIEvent(event.Type) {
				return false
			}
		}
		return true
	}
	if !writeEvents(events, frames) || !follow {
		return
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
			newEvents, listErr := s.authority.ListRemoteEvents(request.Context(), commandID, last)
			if listErr != nil {
				// Headers are already committed; the cursor trailer lets a client
				// resume and receive a structured 410 if history is unavailable.
				return
			}
			if !writeEvents(newEvents, nil) {
				return
			}
		}
	}
}

func encodeRemoteAPIEvent(event store.RemoteEventRecord, ordinal int64) ([]byte, error) {
	value := localAPICommandEvent{CommandID: string(event.CommandID), Sequence: event.Sequence, Type: event.Type, Timestamp: event.OccurredAt.UTC()}
	if event.Type == "command_queued" {
		value.Ordinal = ordinal
	}
	if event.Type == "stdout" || event.Type == "stderr" {
		value.Encoding = "base64"
		value.DataBase64 = base64.StdEncoding.EncodeToString(event.Payload)
		value.ByteCount = event.ByteCount
	}
	return encodeLocalAPIEventValue(value)
}

func commandEventsPathID(path string) (domain.CommandID, error) {
	const prefix = "/v1/commands/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/events") {
		return "", errors.New("invalid command events path")
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/events")
	idText, err := url.PathUnescape(raw)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		return "", errors.New("invalid command events path")
	}
	return domain.NewCommandID(idText)
}

func parseLocalEventCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("invalid event cursor")
	}
	return cursor, nil
}

func parseLocalEventFollow(value string) (bool, error) {
	switch value {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New("follow must be true or false")
	}
}

func encodeLocalAPIEvents(events []store.CommandEventRecord, ordinal int64) ([][]byte, error) {
	frames := make([][]byte, len(events))
	for index, event := range events {
		frame, err := encodeLocalAPIEvent(event, ordinal)
		if err != nil {
			return nil, err
		}
		frames[index] = frame
	}
	return frames, nil
}

func encodeRemoteAPIEvents(events []store.RemoteEventRecord, ordinal int64) ([][]byte, error) {
	frames := make([][]byte, len(events))
	for index, event := range events {
		frame, err := encodeRemoteAPIEvent(event, ordinal)
		if err != nil {
			return nil, err
		}
		frames[index] = frame
	}
	return frames, nil
}

func encodeLocalAPIEvent(event store.CommandEventRecord, ordinal int64) ([]byte, error) {
	value := localAPICommandEvent{CommandID: string(event.CommandID), Sequence: event.Sequence, Type: event.Type, Timestamp: event.OccurredAt.UTC()}
	if event.Type == "command_queued" {
		value.Ordinal = ordinal
	}
	if event.Type == "stdout" || event.Type == "stderr" {
		value.Encoding = "base64"
		value.DataBase64 = base64.StdEncoding.EncodeToString(event.Payload)
		value.ByteCount = event.ByteCount
	}
	return encodeLocalAPIEventValue(value)
}

func encodeLocalAPIEventValue(value localAPICommandEvent) ([]byte, error) {
	frame, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(frame, '\n'), nil
}

func writeLocalAPIEventFrame(response http.ResponseWriter, frame []byte) error {
	written, err := response.Write(frame)
	if err != nil {
		return err
	}
	if written != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

func setLocalAPIEventHeaders(response http.ResponseWriter, view string, stale bool) {
	response.Header().Set("Content-Type", "application/x-ndjson")
	response.Header().Set("X-Runner-View", view)
	response.Header().Set("X-Runner-Stale", strconv.FormatBool(stale))
}

func (s *Server) writeCommandEventReadError(response http.ResponseWriter, commandID domain.CommandID, err error) {
	switch {
	case errors.Is(err, store.ErrCommandReplayExpired):
		writeLocalEventHistoryError(response, commandID, "retention_expired")
	case errors.Is(err, store.ErrCommandReplayGap):
		writeLocalEventHistoryError(response, commandID, "remote_event_gap")
	default:
		status, code := statusForCommandEventError(err)
		writeError(response, status, code, sanitizeError(err))
	}
}

func (s *Server) writeRemoteEventReadError(response http.ResponseWriter, commandID domain.CommandID, err error) {
	switch {
	case errors.Is(err, store.ErrRemoteEventRetentionExpired):
		writeLocalEventHistoryError(response, commandID, "retention_expired")
	case errors.Is(err, store.ErrRemoteEventGap):
		writeLocalEventHistoryError(response, commandID, "remote_event_gap")
	default:
		status, code := statusForRemoteEventError(err)
		writeError(response, status, code, sanitizeError(err))
	}
}

func writeLocalEventHistoryError(response http.ResponseWriter, commandID domain.CommandID, reason string) {
	writeJSON(response, http.StatusGone, errorEnvelope{
		Code: "event_history_unavailable", Message: "requested event history is unavailable", Retryable: false,
		ResourceID: string(commandID), Details: &localEventHistoryDetails{OutputComplete: false, OutputUnavailableReason: reason},
	})
}

func isTerminalLocalAPIEvent(eventType string) bool {
	switch eventType {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func statusForCommandEventError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrCommandNotFound), errors.Is(err, store.ErrSessionNotFound):
		return http.StatusNotFound, "command_not_found"
	case errors.Is(err, store.ErrCommandReplayGap), errors.Is(err, store.ErrCommandReplayExpired):
		return http.StatusGone, "event_history_unavailable"
	case errors.Is(err, store.ErrCommandEvent), errors.Is(err, store.ErrCommandPayloadCorrupt), errors.Is(err, store.ErrLocalIntentPayloadCorrupt):
		return http.StatusServiceUnavailable, "database_unavailable"
	default:
		return statusForStoreError(err)
	}
}

func statusForRemoteEventError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrRemoteEventGap):
		return http.StatusGone, "event_history_unavailable"
	case errors.Is(err, store.ErrRemoteEventRetentionExpired):
		return http.StatusGone, "event_history_unavailable"
	case errors.Is(err, store.ErrRemoteEventNotFound):
		return http.StatusNotFound, "command_not_found"
	default:
		return statusForStoreError(err)
	}
}

func commandIntentResourceFromRecord(record store.LocalIntentRecord) commandIntentResource {
	return commandIntentResource{
		CommandID: string(record.CommandID), SessionID: string(record.SessionID),
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		Controller:      controllerView{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt:      record.UpdatedAt.UTC(), Environment: record.Environment,
		Source: sourceResponseFromDomain(record.Source), DeliveryState: string(record.DeliveryState), Reason: record.Reason,
	}
}

func jobIntentResourceFromRecord(record store.LocalIntentRecord) jobIntentResource {
	return jobIntentResource{
		JobID: string(record.JobID), SessionID: string(record.SessionID), CommandID: string(record.CommandID),
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		Controller:      controllerView{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt:      record.UpdatedAt.UTC(), Environment: record.Environment,
		Source: sourceResponseFromDomain(record.Source), DeliveryState: string(record.DeliveryState), Reason: record.Reason,
	}
}

func sessionCommandPath(path string) (string, bool) {
	const prefix = "/v1/sessions/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/commands") {
		return "", false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/commands")
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	return raw, true
}

func commandCancelPath(path string) (string, bool) {
	const prefix = "/v1/commands/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/cancel") {
		return "", false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/cancel")
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	return raw, true
}

func sessionResourcePath(path string) (string, bool) {
	const prefix = "/v1/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	return raw, true
}

func jobResourcePath(path string) (string, bool) {
	const prefix = "/v1/jobs/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	return raw, true
}

func parseTimeoutSeconds(raw json.RawMessage) (int64, bool, error) {
	if len(raw) == 0 {
		return 0, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false, errors.New("timeout_seconds must be a positive integer")
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return 0, false, errors.New("timeout_seconds must be a positive integer")
	}
	return value, true, nil
}

func (s *Server) handleCreateSession(response http.ResponseWriter, request *http.Request) {
	key := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
		return
	}
	body, err := s.readBody(request)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_request"
		if errors.Is(err, domain.ErrSerializedInputTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "request_too_large"
		}
		writeError(response, status, code, err.Error())
		return
	}
	acceptance, failure := s.acceptCreateSessionIntent(request.Context(), key, body)
	if failure != nil {
		writeError(response, failure.status, failure.code, failure.message)
		return
	}
	writeJSON(response, http.StatusAccepted, acceptance)
}

func (s *Server) acceptCreateSessionIntent(ctx context.Context, key string, body []byte) (sessionAcceptance, *localOperationFailure) {
	var input createSessionRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return sessionAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "malformed or unsupported request JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return sessionAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
	}
	if err := validateObjectField(input.Limits, "limits"); err != nil {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	if err := validateObjectField(input.Policy, "policy"); err != nil {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	if strings.TrimSpace(input.Environment) == "" || strings.IndexByte(input.Environment, 0) >= 0 {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "environment is required")
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(input.ExecutionTarget.Kind), input.ExecutionTarget.Profile)
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	source, err := parseSource(input.Source)
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	if target.Kind() == domain.TargetKindRemote && source.Mode() == domain.SourceModeLocalWorktree {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "remote sessions cannot use a local_worktree source")
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", body, domain.CanonicalizationOptions{})
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request is invalid")
	}
	if err := domain.ValidateSerializedRequest(canonical); err != nil {
		return sessionAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
	}
	if int64(len(canonical)) > s.maxBodyBytes {
		return sessionAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", "canonical request exceeds the configured body limit")
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
	}
	sessionIDText, err := newOpaqueID("sess-")
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate session identity")
	}
	sessionID, err := domain.NewSessionID(sessionIDText)
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not validate session identity")
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		return sessionAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
	}
	record, duplicate, err := s.authority.AcceptLocalIntent(ctx, store.LocalIntentCreate{
		IntentID:       domain.IntentID(intentID),
		Operation:      "create_session",
		ResourceID:     string(sessionID),
		SessionID:      sessionID,
		Target:         target,
		Environment:    strings.TrimSpace(input.Environment),
		Controller:     s.owner,
		Source:         source,
		RequestHash:    hash,
		IdempotencyKey: key,
		PayloadJSON:    canonical,
		DeliveryState:  store.LocalIntentRecorded,
	})
	if err != nil {
		status, code := statusForStoreError(err)
		return sessionAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	_ = duplicate // The stable response identity is the durable idempotency result.
	return sessionAcceptance{
		ResourceID: string(record.SessionID), SessionID: string(record.SessionID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	}, nil
}

func (s *Server) handleCreateJob(response http.ResponseWriter, request *http.Request) {
	key := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" {
		writeError(response, http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
		return
	}
	body, err := s.readBody(request)
	if err != nil {
		status, code := requestReadError(err)
		writeError(response, status, code, err.Error())
		return
	}
	acceptance, failure := s.acceptCreateJobIntent(request.Context(), key, body)
	if failure != nil {
		writeError(response, failure.status, failure.code, failure.message)
		return
	}
	writeJSON(response, http.StatusAccepted, acceptance)
}

func (s *Server) acceptCreateJobIntent(ctx context.Context, key string, body []byte) (jobAcceptance, *localOperationFailure) {
	var input createJobRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return jobAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "malformed or unsupported job request JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return jobAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
	}
	if err := validateObjectField(input.Limits, "limits"); err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	if err := validateObjectField(input.Policy, "policy"); err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	if strings.TrimSpace(input.Environment) == "" || strings.IndexByte(input.Environment, 0) >= 0 {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "environment is required")
	}
	if input.Script == nil {
		return jobAcceptance{}, localFailure(http.StatusBadRequest, "invalid_request", "script is required")
	}
	script := *input.Script
	if err := domain.ValidateScriptUTF8(script); err != nil {
		if errors.Is(err, domain.ErrScriptTooLarge) {
			return jobAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		} else {
			return jobAcceptance{}, localFailure(http.StatusBadRequest, "invalid_script", err.Error())
		}
	}
	if _, _, err := parseTimeoutSeconds(input.TimeoutSeconds); err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(input.ExecutionTarget.Kind), input.ExecutionTarget.Profile)
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	source, err := parseSource(input.Source)
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", err.Error())
	}
	if target.Kind() == domain.TargetKindRemote && source.Mode() == domain.SourceModeLocalWorktree {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "remote jobs cannot use a local_worktree source")
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", body, domain.CanonicalizationOptions{})
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request is invalid")
	}
	if err := domain.ValidateSerializedRequest(canonical); err != nil {
		return jobAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
	}
	if int64(len(canonical)) > s.maxBodyBytes {
		return jobAcceptance{}, localFailure(http.StatusRequestEntityTooLarge, "request_too_large", "canonical request exceeds the configured body limit")
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
	}
	jobIDText, err := newOpaqueID("job-")
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate job identity")
	}
	sessionIDText, err := newOpaqueID("sess-")
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate session identity")
	}
	commandIDText, err := newOpaqueID("cmd-")
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate command identity")
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
	}
	jobID, err := domain.NewJobID(jobIDText)
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not validate job identity")
	}
	sessionID, err := domain.NewSessionID(sessionIDText)
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not validate session identity")
	}
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		return jobAcceptance{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "could not validate command identity")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, _, err := s.authority.AcceptLocalIntent(ctx, store.LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "run", ResourceID: jobIDText,
		SessionID: sessionID, CommandID: commandID, JobID: jobID, Target: target,
		Environment: strings.TrimSpace(input.Environment), Controller: s.owner, Source: source,
		RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script),
		DeliveryState: store.LocalIntentRecorded,
	})
	if err != nil {
		status, code := statusForStoreError(err)
		return jobAcceptance{}, localFailure(status, code, sanitizeError(err))
	}
	return jobAcceptance{
		ResourceID: string(record.JobID), JobID: string(record.JobID), SessionID: string(record.SessionID), CommandID: string(record.CommandID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	}, nil
}

func (s *Server) handleGetJob(response http.ResponseWriter, request *http.Request) {
	if len(request.URL.Query()) != 0 {
		writeError(response, http.StatusBadRequest, "invalid_request", "controller query parameters are not accepted")
		return
	}
	rawID, ok := jobResourcePath(request.URL.Path)
	if !ok {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid job path")
		return
	}
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid job path")
		return
	}
	jobID, err := domain.NewJobID(idText)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	record, err := s.authority.GetLocalIntentByResource(request.Context(), "run", string(jobID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			writeError(response, http.StatusNotFound, "job_not_found", "local job intent was not found")
			return
		}
		status, code := statusForStoreError(err)
		if code == "session_not_found" {
			code = "job_not_found"
		}
		writeError(response, status, code, sanitizeError(err))
		return
	}
	if remoteProjectionEligible(record) {
		projection, projectionErr := s.authority.GetRemoteJobProjection(request.Context(), jobID)
		if projectionErr == nil {
			writeJSON(response, http.StatusOK, jobProjectionRead{View: "projection", IsStale: projection.IsStale, Resource: jobProjectionResourceFromProjection(projection)})
			return
		}
		if !errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
			status, code := statusForStoreError(projectionErr)
			writeError(response, status, code, sanitizeError(projectionErr))
			return
		}
	}
	if record.Target.Kind() == domain.TargetKindLocal && (record.DeliveryState == store.LocalIntentAccepted || record.DeliveryState == store.LocalIntentReconciled) {
		job, jobErr := s.authority.GetJob(request.Context(), jobID)
		if jobErr == nil {
			writeJSON(response, http.StatusOK, jobAuthorityRead{View: "authority", IsStale: false, Resource: jobAuthorityResourceFromRecord(job)})
			return
		}
		if !errors.Is(jobErr, store.ErrJobNotFound) {
			status, code := statusForStoreError(jobErr)
			writeError(response, status, code, sanitizeError(jobErr))
			return
		}
	}
	writeJSON(response, http.StatusOK, jobRead{View: "local_intent", IsStale: false, Resource: jobIntentResourceFromRecord(record)})
}

func (s *Server) handleGetSession(response http.ResponseWriter, request *http.Request) {
	if len(request.URL.Query()) != 0 {
		writeError(response, http.StatusBadRequest, "invalid_request", "controller query parameters are not accepted")
		return
	}
	rawID := strings.TrimPrefix(request.URL.Path, "/v1/sessions/")
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid session path")
		return
	}
	read, failure := s.readSession(request.Context(), idText)
	if failure != nil {
		writeError(response, failure.status, failure.code, failure.message)
		return
	}
	writeJSON(response, http.StatusOK, read)
}

func (s *Server) readSession(ctx context.Context, idText string) (sessionRead, *localOperationFailure) {
	sessionID, err := domain.NewSessionID(idText)
	if err != nil {
		return sessionRead{}, localFailure(http.StatusBadRequest, "invalid_request", err.Error())
	}
	record, err := s.authority.GetLocalIntentByResource(ctx, "create_session", string(sessionID), s.owner)
	if err != nil {
		status, code := statusForStoreError(err)
		return sessionRead{}, localFailure(status, code, sanitizeError(err))
	}
	if remoteProjectionEligible(record) {
		projection, projectionErr := s.authority.GetRemoteSessionProjection(ctx, sessionID)
		if projectionErr == nil {
			return sessionRead{View: "projection", IsStale: projection.IsStale, Resource: sessionProjectionResourceFromProjection(projection)}, nil
		}
		if !errors.Is(projectionErr, store.ErrRemoteProjectionNotFound) {
			status, code := statusForStoreError(projectionErr)
			return sessionRead{}, localFailure(status, code, sanitizeError(projectionErr))
		}
	}
	if record.Target.Kind() == domain.TargetKindLocal && (record.DeliveryState == store.LocalIntentAccepted || record.DeliveryState == store.LocalIntentReconciled) {
		authority, authorityErr := s.authority.GetSession(ctx, sessionID)
		if authorityErr == nil {
			if authority.Controller.Type() != record.Controller.Type() || authority.Controller.ID() != record.Controller.ID() ||
				authority.Target.Kind() != record.Target.Kind() || authority.Target.Profile() != record.Target.Profile() {
				return sessionRead{}, localFailure(http.StatusServiceUnavailable, "database_unavailable", "local session authority identity does not match its accepted intent")
			}
			return sessionRead{View: "authority", IsStale: false, Resource: sessionAuthorityResourceFromRecord(authority)}, nil
		}
		if !errors.Is(authorityErr, store.ErrSessionNotFound) {
			status, code := statusForStoreError(authorityErr)
			return sessionRead{}, localFailure(status, code, sanitizeError(authorityErr))
		}
	}
	return sessionRead{View: "local_intent", IsStale: false, Resource: sessionIntentResourceFromRecord(record)}, nil
}

func (s *Server) readBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, errors.New("request body is required")
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, s.maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(body)) > s.maxBodyBytes {
		return nil, fmt.Errorf("%w: request is %d bytes, maximum %d", domain.ErrSerializedInputTooLarge, len(body), s.maxBodyBytes)
	}
	if err := domain.ValidateSerializedRequest(body); err != nil {
		return nil, err
	}
	return body, nil
}

func requestReadError(err error) (int, string) {
	if errors.Is(err, domain.ErrSerializedInputTooLarge) {
		return http.StatusRequestEntityTooLarge, "request_too_large"
	}
	return http.StatusBadRequest, "invalid_request"
}

func parseSource(input *sourceRequest) (domain.Source, error) {
	if input == nil || input.Mode == "" || input.Mode == string(domain.SourceModeEmpty) {
		if input != nil && (input.RepositoryAlias != "" || input.RequestedRevision != "" || input.ResolvedCommit != "" || input.Path != "" || input.Portable != nil) {
			return domain.Source{}, fmt.Errorf("%w: empty source has no additional fields", domain.ErrInvalidSource)
		}
		return domain.NewEmptySource(), nil
	}
	switch domain.SourceMode(input.Mode) {
	case domain.SourceModeGitRevision:
		if input.ResolvedCommit != "" || input.Path != "" || input.Portable != nil {
			return domain.Source{}, fmt.Errorf("%w: git_revision request has unsupported fields", domain.ErrInvalidSource)
		}
		return domain.NewGitRevisionSource(input.RepositoryAlias, input.RequestedRevision)
	case domain.SourceModeLocalWorktree:
		if input.ResolvedCommit != "" || input.RepositoryAlias != "" || input.RequestedRevision != "" || input.Portable == nil || *input.Portable {
			return domain.Source{}, fmt.Errorf("%w: local_worktree requires portable=false and a path", domain.ErrInvalidSource)
		}
		return domain.NewLocalWorktreeSource(input.Path)
	default:
		return domain.Source{}, fmt.Errorf("%w: unknown source mode %q", domain.ErrInvalidSource, input.Mode)
	}
}

func sessionIntentResourceFromRecord(record store.LocalIntentRecord) sessionIntentResource {
	return sessionIntentResource{
		SessionID:       string(record.SessionID),
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		Controller:      controllerView{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt:      record.UpdatedAt.UTC(), Environment: record.Environment,
		Source: sourceResponseFromDomain(record.Source), DeliveryState: string(record.DeliveryState), Reason: record.Reason,
	}
}

func remoteProjectionEligible(record store.LocalIntentRecord) bool {
	return record.Target.Kind() == domain.TargetKindRemote && (record.DeliveryState == store.LocalIntentAccepted || record.DeliveryState == store.LocalIntentReconciled)
}

func capabilitiesResponseFromProjection(capabilities store.RemoteCapabilities) capabilitiesResponse {
	limits := capabilities.ServiceLimits
	if limits == nil {
		limits = map[string]any{}
	}
	return capabilitiesResponse{HostClass: capabilities.HostClass, Isolation: capabilities.Isolation, EffectiveAccount: capabilities.EffectiveAccount, ServiceLimits: limits}
}

func sessionProjectionResourceFromProjection(projection store.RemoteSessionProjection) sessionProjectionResource {
	source := sourceResponseFromDomain(projection.Source)
	source.ResolvedCommit = projection.ResolvedRevision
	return sessionProjectionResource{
		SessionID: string(projection.SessionID), SessionState: string(projection.State),
		ExecutionTarget: targetResponse{Kind: string(projection.Target.Kind()), Profile: projection.Target.Profile()}, Authority: "remote",
		Controller: controllerView{Type: string(projection.Controller.Type()), ID: string(projection.Controller.ID())}, ObservedAt: projection.ObservedAt.UTC(),
		Environment: projection.Environment, Source: source, Capabilities: capabilitiesResponseFromProjection(projection.Capabilities),
		RuntimeGeneration: projection.RuntimeGeneration, ResolvedRevision: projection.ResolvedRevision, IsStale: projection.IsStale,
	}
}

func sessionAuthorityResourceFromRecord(record store.SessionRecord) sessionProjectionResource {
	return sessionProjectionResource{
		SessionID: string(record.SessionID), SessionState: string(record.State),
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()}, Authority: "local",
		Controller: controllerView{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())}, ObservedAt: record.UpdatedAt.UTC(),
		Environment: record.Environment, Source: sourceResponseFromDomain(record.Source),
		Capabilities:      capabilitiesResponse{HostClass: record.Target.Profile(), Isolation: string(domain.IsolationOSUser), EffectiveAccount: string(record.Controller.ID()), ServiceLimits: map[string]any{}},
		RuntimeGeneration: record.RuntimeGeneration, ResolvedRevision: record.ResolvedRevision,
	}
}

func commandProjectionResourceFromProjection(projection store.RemoteCommandProjection) commandProjectionResource {
	return commandProjectionResource{
		CommandID: string(projection.CommandID), SessionID: string(projection.SessionID), Ordinal: projection.Ordinal, CommandState: string(projection.State),
		ExitCode: projection.ExitCode, FinalEventSequence: projection.FinalEventSequence, OutputComplete: projection.OutputComplete,
		OutputTruncated: projection.OutputTruncated, OutputUnavailableReason: projection.OutputUnavailableReason,
		ExecutionTarget: targetResponse{Kind: string(projection.Target.Kind()), Profile: projection.Target.Profile()}, Authority: "remote",
		Controller: controllerView{Type: string(projection.Controller.Type()), ID: string(projection.Controller.ID())}, ObservedAt: projection.ObservedAt.UTC(),
		Environment: projection.Environment, Source: sourceResponseFromDomain(projection.Source), Capabilities: capabilitiesResponseFromProjection(projection.Capabilities), IsStale: projection.IsStale,
	}
}

func jobProjectionResourceFromProjection(projection store.RemoteJobProjection) jobProjectionResource {
	var commandState *string
	if projection.CommandState != nil {
		value := string(*projection.CommandState)
		commandState = &value
	}
	return jobProjectionResource{
		JobID: string(projection.JobID), SessionID: string(projection.SessionID), CommandID: string(projection.CommandID), JobPhase: string(projection.Phase),
		CommandState: commandState, ExitCode: projection.ExitCode, FinalEventSequence: projection.FinalEventSequence,
		OutputComplete: projection.OutputComplete, OutputTruncated: projection.OutputTruncated, OutputUnavailableReason: projection.OutputUnavailableReason,
		TeardownState: string(projection.TeardownState), TeardownReason: projection.TeardownReason,
		ExecutionTarget: targetResponse{Kind: string(projection.Target.Kind()), Profile: projection.Target.Profile()}, Authority: "remote",
		Controller: controllerView{Type: string(projection.Controller.Type()), ID: string(projection.Controller.ID())}, ObservedAt: projection.ObservedAt.UTC(),
		Environment: projection.Environment, Source: sourceResponseFromDomain(projection.Source), Capabilities: capabilitiesResponseFromProjection(projection.Capabilities), IsStale: projection.IsStale,
	}
}

func jobAuthorityResourceFromRecord(record store.JobRecord) jobProjectionResource {
	var commandState *string
	if record.CommandState != nil {
		value := string(*record.CommandState)
		commandState = &value
	}
	return jobProjectionResource{
		JobID: string(record.JobID), SessionID: string(record.SessionID), CommandID: string(record.CommandID), JobPhase: string(record.Phase),
		CommandState: commandState, ExitCode: record.ExitCode, FinalEventSequence: record.FinalEventSequence,
		OutputComplete: record.OutputComplete, OutputTruncated: record.OutputTruncated, OutputUnavailableReason: record.OutputUnavailableReason,
		TeardownState: string(record.TeardownState), TeardownReason: record.TeardownReason,
		ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()}, Authority: "local",
		Controller: controllerView{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())}, ObservedAt: record.UpdatedAt.UTC(),
		Environment: record.Environment, Source: sourceResponseFromDomain(record.Source), Capabilities: capabilitiesResponse{HostClass: record.Target.Profile(), Isolation: string(domain.IsolationOSUser), EffectiveAccount: string(record.Controller.ID()), ServiceLimits: map[string]any{}},
	}
}

func sourceResponseFromDomain(source domain.Source) sourceResponse {
	response := sourceResponse{Mode: string(source.Mode()), RepositoryAlias: source.RepositoryAlias(), RequestedRevision: source.RequestedRevision(), Path: source.Path()}
	if source.Mode() == domain.SourceModeLocalWorktree {
		portable := false
		response.Portable = &portable
	}
	return response
}

func validateObjectField(raw json.RawMessage, name string) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	return nil
}

func statusForStoreError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, store.ErrLocalIntentNotFound):
		return http.StatusNotFound, "session_not_found"
	case errors.Is(err, store.ErrLocalIntentPayloadCorrupt):
		return http.StatusServiceUnavailable, "database_unavailable"
	case errors.Is(err, store.ErrInvalidLocalIntent), errors.Is(err, store.ErrLocalIntentExists):
		return http.StatusUnprocessableEntity, "invalid_request"
	default:
		return http.StatusServiceUnavailable, "database_unavailable"
	}
}

func newOpaqueID(prefix string) (string, error) {
	var bytesValue [16]byte
	if _, err := rand.Read(bytesValue[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytesValue[:]), nil
}

func validateSocketPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%w: path must be absolute and clean", ErrSocketPath)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || !parentInfo.IsDir() {
		return fmt.Errorf("%w: parent directory unavailable", ErrSocketPath)
	}
	if parentInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: parent directory must exclude group and other access", ErrSocketPath)
	}
	stat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: parent directory owner mismatch", ErrSocketPath)
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, errorEnvelope{Code: code, Message: sanitizeErrorString(message), Retryable: status >= 500})
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeErrorString(err.Error())
}

func sanitizeErrorString(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\n", " ")
	if value == "" {
		return "request failed"
	}
	return value
}
