package localapi

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const DefaultMaxBodyBytes int64 = domain.MaxSerializedRequestBytes

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
	View     string                `json:"view"`
	IsStale  bool                  `json:"is_stale"`
	Resource sessionIntentResource `json:"resource"`
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
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

type errorEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (s *Server) serveHTTP(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/sessions":
		s.handleCreateSession(response, request)
	case request.Method == http.MethodPost:
		if sessionID, ok := sessionCommandPath(request.URL.Path); ok {
			s.handleSubmitCommand(response, request, sessionID)
			return
		}
		writeError(response, http.StatusNotFound, "resource_not_found", "local API route not found")
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/commands/"):
		s.handleGetCommand(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/sessions/"):
		s.handleGetSession(response, request)
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

type commandRead struct {
	View     string                `json:"view"`
	IsStale  bool                  `json:"is_stale"`
	Resource commandIntentResource `json:"resource"`
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
	sessionID, err := domain.NewSessionID(sessionIDText)
	if err != nil {
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
	var input submitCommandRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(response, http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
		return
	}
	if input.Script == nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "script is required")
		return
	}
	script := *input.Script
	if err := domain.ValidateScriptUTF8(script); err != nil {
		if errors.Is(err, domain.ErrScriptTooLarge) {
			writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		} else {
			writeError(response, http.StatusBadRequest, "invalid_script", err.Error())
		}
		return
	}
	timeoutSeconds, hasTimeout, err := parseTimeoutSeconds(input.TimeoutSeconds)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	session, err := s.authority.GetLocalIntentByResource(request.Context(), "create_session", string(sessionID), s.owner)
	if err != nil {
		if errors.Is(err, store.ErrLocalIntentNotFound) {
			writeError(response, http.StatusNotFound, "session_not_found", "local session intent was not found")
			return
		}
		status, code := statusForStoreError(err)
		writeError(response, status, code, sanitizeError(err))
		return
	}
	payload := map[string]any{"session_id": string(sessionID), "script": script}
	if hasTimeout {
		payload["timeout_seconds"] = timeoutSeconds
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "could not encode command request")
		return
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payloadJSON, domain.CanonicalizationOptions{})
	if err != nil || int64(len(canonical)) > s.maxBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "canonical command request exceeds the configured body limit")
		return
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
		return
	}
	commandIDText, err := newOpaqueID("cmd-")
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "could not allocate command identity")
		return
	}
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "could not validate command identity")
		return
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
		return
	}
	record, _, err := s.authority.AcceptLocalIntent(request.Context(), store.LocalIntentCreate{
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
		writeError(response, status, code, sanitizeError(err))
		return
	}
	writeJSON(response, http.StatusAccepted, commandAcceptance{
		ResourceID: string(record.CommandID), CommandID: string(record.CommandID), SessionID: string(record.SessionID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	})
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
	writeJSON(response, http.StatusOK, commandRead{View: "local_intent", IsStale: false, Resource: commandIntentResourceFromRecord(record)})
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
	var input createSessionRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "malformed or unsupported request JSON")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(response, http.StatusBadRequest, "invalid_request", "request body contains multiple JSON values")
		return
	}
	if err := validateObjectField(input.Limits, "limits"); err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if err := validateObjectField(input.Policy, "policy"); err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(input.Environment) == "" || strings.IndexByte(input.Environment, 0) >= 0 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "environment is required")
		return
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(input.ExecutionTarget.Kind), input.ExecutionTarget.Profile)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	source, err := parseSource(input.Source)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if target.Kind() == domain.TargetKindRemote && source.Mode() == domain.SourceModeLocalWorktree {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "remote sessions cannot use a local_worktree source")
		return
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", body, domain.CanonicalizationOptions{})
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "canonical request is invalid")
		return
	}
	if err := domain.ValidateSerializedRequest(canonical); err != nil {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	if int64(len(canonical)) > s.maxBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "canonical request exceeds the configured body limit")
		return
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_request", "canonical request hash is invalid")
		return
	}
	sessionIDText, err := newOpaqueID("sess-")
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "could not allocate session identity")
		return
	}
	sessionID, err := domain.NewSessionID(sessionIDText)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "could not validate session identity")
		return
	}
	intentID, err := newOpaqueID("intent-")
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "database_unavailable", "could not allocate intent identity")
		return
	}
	record, duplicate, err := s.authority.AcceptLocalIntent(request.Context(), store.LocalIntentCreate{
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
		writeError(response, status, code, sanitizeError(err))
		return
	}
	writeJSON(response, http.StatusAccepted, sessionAcceptance{
		ResourceID: string(record.SessionID), SessionID: string(record.SessionID), IntentID: string(record.IntentID),
		AcceptanceScope: "local_intent", ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		KnownState: knownState{DeliveryState: string(record.DeliveryState)},
	})
	_ = duplicate // The stable response identity is the durable idempotency result.
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
	sessionID, err := domain.NewSessionID(idText)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	record, err := s.authority.GetLocalIntentByResource(request.Context(), "create_session", string(sessionID), s.owner)
	if err != nil {
		status, code := statusForStoreError(err)
		writeError(response, status, code, sanitizeError(err))
		return
	}
	writeJSON(response, http.StatusOK, sessionRead{View: "local_intent", IsStale: false, Resource: sessionIntentResourceFromRecord(record)})
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
