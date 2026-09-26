// Package runnerd contains the remote worker's private control-plane wiring.
// The socket in this package is intentionally owner-only; public HTTPS and
// the SSH bridge are later ingress adapters over the same execution service.
package runnerd

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
	"sync"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

const (
	// DefaultPrivateRequestBytes is the shared serialized request ceiling. The
	// body is bounded before JSON decoding or any service/store mutation.
	DefaultPrivateRequestBytes int64 = 1 << 20
	privateSessionsPath              = "/internal/v1/sessions"
	privateSessionsAliasPath         = "/v1/sessions"
)

var (
	ErrPrivateAPIConfiguration = errors.New("private runnerd API configuration is invalid")
	ErrPrivateSocketPath       = errors.New("private runnerd socket path is invalid")
)

// PrivateServerOptions wires the already-constructed shared execution core to
// the owner-only Unix API. The server does not create a second execution path.
type PrivateServerOptions struct {
	Service      *execution.Service
	SocketPath   string
	MaxBodyBytes int64
}

// PrivateServer serves only the P046 create/read session subset.
type PrivateServer struct {
	service      *execution.Service
	socketPath   string
	maxBodyBytes int64
	httpServer   *http.Server
	listener     net.Listener
	mu           sync.Mutex
	closed       bool
}

// NewPrivateServer validates the owner-only socket location but does not
// create a listener until Listen is called.
func NewPrivateServer(options PrivateServerOptions) (*PrivateServer, error) {
	if options.Service == nil {
		return nil, ErrPrivateAPIConfiguration
	}
	if err := validatePrivateSocketPath(options.SocketPath); err != nil {
		return nil, err
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = DefaultPrivateRequestBytes
	}
	return &PrivateServer{
		service:      options.Service,
		socketPath:   options.SocketPath,
		maxBodyBytes: options.MaxBodyBytes,
		httpServer:   &http.Server{Handler: nil},
	}, nil
}

// SocketPath returns the configured private socket path.
func (s *PrivateServer) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.socketPath
}

// Listen creates the Unix listener and applies the required mode 0600 after
// bind. An existing path is rejected instead of being unlinked blindly.
func (s *PrivateServer) Listen() error {
	if s == nil {
		return ErrPrivateAPIConfiguration
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return nil
	}
	if info, err := os.Lstat(s.socketPath); err == nil {
		return fmt.Errorf("%w: socket path already exists as %s", ErrPrivateSocketPath, info.Mode().Type())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect socket path: %v", ErrPrivateSocketPath, err)
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("%w: listen: %v", ErrPrivateSocketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("%w: chmod socket: %v", ErrPrivateSocketPath, err)
	}
	s.listener = listener
	s.httpServer.Handler = http.HandlerFunc(s.serveHTTP)
	return nil
}

// Serve runs the HTTP server after Listen. A normal listener close returns
// nil so callers can use Close as the lifecycle boundary.
func (s *PrivateServer) Serve() error {
	if s == nil {
		return ErrPrivateAPIConfiguration
	}
	s.mu.Lock()
	listener := s.listener
	httpServer := s.httpServer
	s.mu.Unlock()
	if listener == nil {
		return ErrPrivateSocketPath
	}
	err := httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Close stops the listener and removes only the socket created by this
// server. The parent directory is deployment-owned and is never removed.
func (s *PrivateServer) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	httpServer := s.httpServer
	listener := s.listener
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	shutdownErr := httpServer.Shutdown(ctx)
	if listener != nil {
		_ = listener.Close()
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

func validatePrivateSocketPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%w: path must be absolute and clean", ErrPrivateSocketPath)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || !parentInfo.IsDir() {
		return fmt.Errorf("%w: parent directory is unavailable", ErrPrivateSocketPath)
	}
	if parentInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: parent directory must exclude group and other access", ErrPrivateSocketPath)
	}
	return nil
}

type createSessionRequest struct {
	SessionID       string            `json:"session_id"`
	IdempotencyKey  string            `json:"idempotency_key"`
	RequestID       string            `json:"request_id,omitempty"`
	Environment     string            `json:"environment"`
	ExecutionTarget targetRequest     `json:"execution_target"`
	Controller      controllerRequest `json:"controller"`
	Source          *sourceRequest    `json:"source,omitempty"`
	Limits          *limitsRequest    `json:"limits,omitempty"`
	Isolation       *isolationRequest `json:"isolation,omitempty"`
}

type targetRequest struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

type controllerRequest struct {
	Type string `json:"controller_type"`
	ID   string `json:"controller_id"`
}

type sourceRequest struct {
	Mode              string `json:"mode"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	Path              string `json:"path,omitempty"`
}

type limitsRequest struct {
	CommandTimeoutSeconds     int64 `json:"command_timeout_seconds,omitempty"`
	IdleTimeoutSeconds        int64 `json:"idle_timeout_seconds,omitempty"`
	SessionMaxLifetimeSeconds int64 `json:"session_max_lifetime_seconds,omitempty"`
	OutputBytesPerCommand     int64 `json:"output_bytes_per_command,omitempty"`
}

type isolationRequest struct {
	FilesystemBoundary bool `json:"filesystem_boundary,omitempty"`
	CPUControl         bool `json:"cpu_control,omitempty"`
	MemoryControl      bool `json:"memory_control,omitempty"`
	PIDControl         bool `json:"pid_control,omitempty"`
	DiskControl        bool `json:"disk_control,omitempty"`
	NetworkControl     bool `json:"network_control,omitempty"`
	Mounts             bool `json:"mounts,omitempty"`
	Volumes            bool `json:"volumes,omitempty"`
	PrivilegeControl   bool `json:"privilege_control,omitempty"`
}

type sessionResponse struct {
	SessionID         string            `json:"session_id"`
	SessionState      string            `json:"session_state"`
	ExecutionTarget   targetResponse    `json:"execution_target"`
	Authority         string            `json:"authority"`
	Controller        controllerRequest `json:"controller"`
	ObservedAt        time.Time         `json:"observed_at"`
	Environment       string            `json:"environment"`
	Source            sourceResponse    `json:"source"`
	RuntimeGeneration string            `json:"runtime_generation,omitempty"`
	ResolvedRevision  string            `json:"resolved_revision,omitempty"`
	Duplicate         bool              `json:"duplicate,omitempty"`
}

type targetResponse struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

type sourceResponse struct {
	Mode              string `json:"mode"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	ResolvedCommit    string `json:"resolved_commit,omitempty"`
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

func (s *PrivateServer) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if isJobCollectionPath(request.URL.Path) && request.Method == http.MethodPost {
		s.handleRunJob(response, request)
		return
	}
	if jobPathPrefix(request.URL.Path) != "" && request.Method == http.MethodGet {
		s.handleGetJob(response, request)
		return
	}
	if commandPathPrefix(request.URL.Path) != "" {
		prefix := commandPathPrefix(request.URL.Path)
		remainder := strings.TrimPrefix(request.URL.Path, prefix+"/")
		parts := strings.Split(remainder, "/")
		if len(parts) == 2 && parts[1] == "commands" && request.Method == http.MethodPost {
			s.handleSubmitCommand(response, request, parts[0])
			return
		}
	}
	if commandResourcePathPrefix(request.URL.Path) != "" {
		prefix := commandResourcePathPrefix(request.URL.Path)
		remainder := strings.TrimPrefix(request.URL.Path, prefix+"/")
		parts := strings.Split(remainder, "/")
		if len(parts) == 1 && request.Method == http.MethodGet {
			s.handleGetCommand(response, request, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "events" && request.Method == http.MethodGet {
			s.handleCommandEvents(response, request, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "cancel" && request.Method == http.MethodPost {
			s.handleCancelCommand(response, request, parts[0])
			return
		}
	}
	if isSessionCollectionPath(request.URL.Path) {
		if request.Method != http.MethodPost {
			writePrivateError(response, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleCreateSession(response, request)
		return
	}
	if sessionPathPrefix(request.URL.Path) != "" {
		if request.Method == http.MethodDelete {
			s.handleCloseSession(response, request)
			return
		}
		if request.Method != http.MethodGet {
			writePrivateError(response, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleGetSession(response, request)
		return
	}
	writePrivateError(response, http.StatusNotFound, "route not found")
}

type submitCommandRequest struct {
	CommandID      string            `json:"command_id"`
	SessionID      string            `json:"session_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	RequestID      string            `json:"request_id,omitempty"`
	Controller     controllerRequest `json:"controller"`
	Script         string            `json:"script"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
	IntentOrdinal  int64             `json:"intent_ordinal,omitempty"`
}

type commandResponse struct {
	CommandID               string `json:"command_id"`
	SessionID               string `json:"session_id"`
	Ordinal                 int64  `json:"ordinal"`
	CommandState            string `json:"command_state"`
	ExitCode                *int   `json:"exit_code,omitempty"`
	FinalEventSequence      *int64 `json:"final_event_sequence,omitempty"`
	OutputComplete          bool   `json:"output_complete"`
	OutputTruncated         bool   `json:"output_truncated"`
	OutputUnavailableReason string `json:"output_unavailable_reason,omitempty"`
	ScriptByteCount         int    `json:"script_byte_count"`
	ScriptSHA256            string `json:"script_sha256"`
	Duplicate               bool   `json:"duplicate,omitempty"`
}

type commandEventResponse struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	ByteCount  int64     `json:"byte_count"`
	DataBase64 string    `json:"data_base64,omitempty"`
}

type cancelCommandRequest struct {
	CommandID      string            `json:"command_id,omitempty"`
	IdempotencyKey string            `json:"idempotency_key"`
	RequestID      string            `json:"request_id,omitempty"`
	Controller     controllerRequest `json:"controller"`
}

type closeSessionRequest struct {
	SessionID      string            `json:"session_id,omitempty"`
	IdempotencyKey string            `json:"idempotency_key"`
	RequestID      string            `json:"request_id,omitempty"`
	Controller     controllerRequest `json:"controller"`
	Policy         string            `json:"policy,omitempty"`
}

type runJobRequest struct {
	JobID           string            `json:"job_id"`
	SessionID       string            `json:"session_id"`
	CommandID       string            `json:"command_id"`
	IdempotencyKey  string            `json:"idempotency_key"`
	RequestID       string            `json:"request_id,omitempty"`
	Environment     string            `json:"environment"`
	ExecutionTarget targetRequest     `json:"execution_target"`
	Controller      controllerRequest `json:"controller"`
	Source          *sourceRequest    `json:"source,omitempty"`
	Script          string            `json:"script"`
	Limits          *limitsRequest    `json:"limits,omitempty"`
	Isolation       *isolationRequest `json:"isolation,omitempty"`
	Policy          map[string]any    `json:"policy,omitempty"`
}

type jobResponse struct {
	JobID                   string            `json:"job_id"`
	SessionID               string            `json:"session_id"`
	CommandID               string            `json:"command_id"`
	JobPhase                string            `json:"job_phase"`
	CommandState            *string           `json:"command_state,omitempty"`
	ExitCode                *int              `json:"exit_code,omitempty"`
	FinalEventSequence      *int64            `json:"final_event_sequence,omitempty"`
	OutputComplete          bool              `json:"output_complete"`
	OutputTruncated         bool              `json:"output_truncated"`
	OutputUnavailableReason string            `json:"output_unavailable_reason,omitempty"`
	TeardownState           string            `json:"teardown_state"`
	TeardownReason          string            `json:"teardown_reason,omitempty"`
	ExecutionTarget         targetResponse    `json:"execution_target"`
	Authority               string            `json:"authority"`
	Controller              controllerRequest `json:"controller"`
	ObservedAt              time.Time         `json:"observed_at"`
	Environment             string            `json:"environment"`
	Source                  sourceResponse    `json:"source"`
	Duplicate               bool              `json:"duplicate,omitempty"`
}

func (s *PrivateServer) handleSubmitCommand(response http.ResponseWriter, request *http.Request, pathSessionID string) {
	if request.Body == nil {
		writePrivateError(response, http.StatusBadRequest, "request body is required")
		return
	}
	limited := http.MaxBytesReader(response, request.Body, s.maxBodyBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input submitCommandRequest
	if err := decoder.Decode(&input); err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writePrivateError(response, http.StatusBadRequest, "request body contains multiple JSON values")
		return
	}
	if input.CommandID == "" || input.SessionID == "" || input.IdempotencyKey == "" {
		writePrivateError(response, http.StatusBadRequest, "command_id, session_id, and idempotency_key are required")
		return
	}
	if pathSessionID != input.SessionID {
		writePrivateError(response, http.StatusBadRequest, "session path and body session_id differ")
		return
	}
	commandID, err := domain.NewCommandID(input.CommandID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	sessionID, err := domain.NewSessionID(input.SessionID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromRequest(input.Controller)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	if input.TimeoutSeconds < 0 || input.IntentOrdinal < 0 {
		writePrivateError(response, http.StatusBadRequest, "timeout_seconds and intent_ordinal must not be negative")
		return
	}
	if err := domain.ValidateScriptUTF8(input.Script); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", body, domain.CanonicalizationOptions{})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	result, serviceErr := s.service.SubmitCommand(request.Context(), execution.SubmitCommandRequest{
		CommandID:            commandID,
		SessionID:            sessionID,
		Controller:           controller,
		IdempotencyKey:       input.IdempotencyKey,
		RequestHash:          hash,
		Script:               input.Script,
		Timeout:              secondsDuration(input.TimeoutSeconds),
		IntentOrdinal:        input.IntentOrdinal,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil {
		status := privateStatusForError(serviceErr)
		if result.Command.CommandID != "" {
			writeJSON(response, status, commandResponseFromRecord(result.Command, result.Duplicate))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, commandResponseFromRecord(result.Command, result.Duplicate))
}

func (s *PrivateServer) handleCancelCommand(response http.ResponseWriter, request *http.Request, pathCommandID string) {
	input, body, err := decodePrivateJSON[cancelCommandRequest](s, response, request)
	if err != nil {
		return
	}
	if input.IdempotencyKey == "" {
		writePrivateError(response, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	if input.CommandID != "" && input.CommandID != pathCommandID {
		writePrivateError(response, http.StatusBadRequest, "command path and body command_id differ")
		return
	}
	commandID, err := domain.NewCommandID(pathCommandID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromRequest(input.Controller)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := domain.HashMutationRequestJSON("cancel_command", body, domain.CanonicalizationOptions{})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	result, serviceErr := s.service.CancelCommand(request.Context(), execution.CancelCommandRequest{
		CommandID: commandID, Controller: controller, IdempotencyKey: input.IdempotencyKey,
		RequestHash: hash, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil {
		status := privateStatusForError(serviceErr)
		if result.Command.CommandID != "" {
			writeJSON(response, status, commandResponseFromRecord(result.Command, result.Duplicate))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, commandResponseFromRecord(result.Command, result.Duplicate))
}

func (s *PrivateServer) handleCloseSession(response http.ResponseWriter, request *http.Request) {
	input, _, err := decodePrivateJSON[closeSessionRequest](s, response, request)
	if err != nil {
		return
	}
	if input.IdempotencyKey == "" {
		writePrivateError(response, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	prefix := sessionPathPrefix(request.URL.Path)
	rawID := strings.TrimPrefix(request.URL.Path, prefix+"/")
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writePrivateError(response, http.StatusBadRequest, "invalid session path")
		return
	}
	if input.SessionID != "" && input.SessionID != idText {
		writePrivateError(response, http.StatusBadRequest, "session path and body session_id differ")
		return
	}
	sessionID, err := domain.NewSessionID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromRequest(input.Controller)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	policy := input.Policy
	if policy == "" {
		policy = "graceful"
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("close_session", []byte(fmt.Sprintf(`{"operation":"close_session","session_id":%q,"policy":%q}`, sessionID, policy)), domain.CanonicalizationOptions{})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	hash, err := domain.HashMutationRequestJSON("close_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	result, serviceErr := s.service.CloseSession(request.Context(), execution.CloseSessionRequest{
		SessionID: sessionID, Controller: controller, IdempotencyKey: input.IdempotencyKey,
		RequestHash: hash, Policy: policy, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil {
		status := privateStatusForError(serviceErr)
		if result.Session.SessionID != "" {
			writeJSON(response, status, sessionResponseFromRecord(result.Session, result.Duplicate))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, sessionResponseFromRecord(result.Session, result.Duplicate))
}

func (s *PrivateServer) handleRunJob(response http.ResponseWriter, request *http.Request) {
	input, _, err := decodePrivateJSON[runJobRequest](s, response, request)
	if err != nil {
		return
	}
	if input.JobID == "" || input.SessionID == "" || input.CommandID == "" || input.IdempotencyKey == "" || input.Environment == "" {
		writePrivateError(response, http.StatusBadRequest, "job_id, session_id, command_id, idempotency_key, and environment are required")
		return
	}
	jobID, err := domain.NewJobID(input.JobID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	sessionID, err := domain.NewSessionID(input.SessionID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	commandID, err := domain.NewCommandID(input.CommandID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(input.ExecutionTarget.Kind), input.ExecutionTarget.Profile)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	if target.Kind() != domain.TargetKindRemote {
		writePrivateError(response, http.StatusBadRequest, "runnerd private API accepts remote targets only")
		return
	}
	controller, err := controllerFromRequest(input.Controller)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	source, err := parseSource(input.Source)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	limits, err := parseRequestedLimits(input.Limits)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	isolation := parseIsolation(input.Isolation)
	environment := strings.TrimSpace(input.Environment)
	if environment == "" {
		writePrivateError(response, http.StatusBadRequest, "environment is required")
		return
	}
	if err := domain.ValidateScriptUTF8(input.Script); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	canonical, err := canonicalRunPayload(environment, target, source, input.Script, limits, isolation, input.Policy)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	result, serviceErr := s.service.RunJob(request.Context(), execution.RunJobRequest{
		Acceptance: store.JobAcceptance{
			JobID: jobID, SessionID: sessionID, CommandID: commandID, Controller: controller,
			IdempotencyKey: input.IdempotencyKey, RequestHash: hash, Environment: environment,
			Target: target, Source: source, Script: input.Script, CanonicalPayload: canonical,
			IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		},
		RequestedLimits: limits, Isolation: isolation, MaxActiveSessions: store.DefaultActiveSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil {
		status := privateStatusForError(serviceErr)
		if result.Job.JobID != "" {
			writeJSON(response, status, jobResponseFromRecord(result.Job, false))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, jobResponseFromRecord(result.Job, false))
}

func (s *PrivateServer) handleGetJob(response http.ResponseWriter, request *http.Request) {
	prefix := jobPathPrefix(request.URL.Path)
	rawID := strings.TrimPrefix(request.URL.Path, prefix+"/")
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writePrivateError(response, http.StatusBadRequest, "invalid job path")
		return
	}
	jobID, err := domain.NewJobID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromQuery(request)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.service.GetJob(request.Context(), jobID, controller)
	if err != nil {
		writePrivateError(response, privateStatusForError(err), err.Error())
		return
	}
	writeJSON(response, http.StatusOK, jobResponseFromRecord(job, false))
}

func canonicalRunPayload(environment string, target domain.ExecutionTarget, source domain.Source, script string, limits domain.RequestedLimits, isolation domain.IsolationRequirements, policy map[string]any) ([]byte, error) {
	payload := map[string]any{
		"operation":        "run",
		"environment":      environment,
		"execution_target": map[string]any{"kind": string(target.Kind()), "profile": target.Profile()},
		"source":           sourcePayload(source),
		"script":           script,
		"requested_limits": limits,
		"isolation":        isolation,
	}
	if policy != nil {
		payload["policy"] = policy
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
}

func sourcePayload(source domain.Source) map[string]any {
	payload := map[string]any{"mode": string(source.Mode())}
	if source.RepositoryAlias() != "" {
		payload["repository_alias"] = source.RepositoryAlias()
	}
	if source.RequestedRevision() != "" {
		payload["requested_revision"] = source.RequestedRevision()
	}
	if source.Path() != "" {
		payload["path"] = source.Path()
	}
	return payload
}

func controllerFromRequest(input controllerRequest) (domain.ControllerIdentity, error) {
	id, err := domain.NewControllerID(input.ID)
	if err != nil {
		return domain.ControllerIdentity{}, err
	}
	return domain.NewControllerIdentity(domain.ControllerType(input.Type), id)
}

func (s *PrivateServer) handleGetCommand(response http.ResponseWriter, request *http.Request, rawID string) {
	id, err := domain.NewCommandID(rawID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromQuery(request)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	command, err := s.service.GetCommand(request.Context(), id, controller)
	if err != nil {
		writePrivateError(response, privateStatusForError(err), err.Error())
		return
	}
	writeJSON(response, http.StatusOK, commandResponseFromRecord(command, false))
}

func (s *PrivateServer) handleCommandEvents(response http.ResponseWriter, request *http.Request, rawID string) {
	id, err := domain.NewCommandID(rawID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromQuery(request)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	after, err := parseEventCursor(request.URL.Query().Get("after"))
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	follow := request.URL.Query().Get("follow") == "true"
	if _, err := s.service.GetCommand(request.Context(), id, controller); err != nil {
		writePrivateError(response, privateStatusForError(err), err.Error())
		return
	}
	var replay []store.CommandEventRecord
	var subscription *store.CommandEventSubscription
	if !follow {
		replay, err = s.service.ReplayCommandEvents(request.Context(), id, controller, after)
		if err != nil {
			writePrivateError(response, privateStatusForError(err), err.Error())
			return
		}
	} else {
		subscription, err = s.service.SubscribeCommandEvents(request.Context(), id, controller, after, 256)
		if err != nil {
			writePrivateError(response, privateStatusForError(err), err.Error())
			return
		}
		defer subscription.Close()
	}
	response.Header().Set("Content-Type", "application/x-ndjson")
	response.WriteHeader(http.StatusOK)
	flusher, _ := response.(http.Flusher)
	writeEvent := func(event store.CommandEventRecord) error {
		value := commandEventResponse{CommandID: string(event.CommandID), Sequence: event.Sequence, Type: event.Type, OccurredAt: event.OccurredAt.UTC(), ByteCount: event.ByteCount}
		if event.Type == "stdout" || event.Type == "stderr" {
			value.DataBase64 = base64.StdEncoding.EncodeToString(event.Payload)
		}
		if err := json.NewEncoder(response).Encode(value); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	if !follow {
		for _, event := range replay {
			if err := writeEvent(event); err != nil {
				return
			}
		}
		return
	}
	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				return
			}
			if err := writeEvent(event); err != nil || isTerminalCommandEvent(event.Type) {
				return
			}
		case <-subscription.Errors():
			return
		case <-request.Context().Done():
			return
		}
	}
}

func parseEventCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, fmt.Errorf("invalid event cursor")
	}
	return cursor, nil
}

func isTerminalCommandEvent(eventType string) bool {
	switch eventType {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func commandResponseFromRecord(record store.CommandRecord, duplicate bool) commandResponse {
	return commandResponse{
		CommandID:               string(record.CommandID),
		SessionID:               string(record.SessionID),
		Ordinal:                 record.Ordinal,
		CommandState:            string(record.State),
		ExitCode:                record.ExitCode,
		FinalEventSequence:      record.FinalEventSequence,
		OutputComplete:          record.OutputComplete,
		OutputTruncated:         record.OutputTruncated,
		OutputUnavailableReason: record.OutputUnavailableReason,
		ScriptByteCount:         len(record.ScriptBytes),
		ScriptSHA256:            hex.EncodeToString(record.ScriptSHA256),
		Duplicate:               duplicate,
	}
}

func jobResponseFromRecord(record store.JobRecord, duplicate bool) jobResponse {
	portable := record.Source.Portable()
	source := sourceResponse{
		Mode:              string(record.Source.Mode()),
		RepositoryAlias:   record.Source.RepositoryAlias(),
		RequestedRevision: record.Source.RequestedRevision(),
		Path:              record.Source.Path(),
		Portable:          &portable,
	}
	authority := "remote"
	if record.Target.Kind() == domain.TargetKindLocal {
		authority = "local"
	}
	return jobResponse{
		JobID: string(record.JobID), SessionID: string(record.SessionID), CommandID: string(record.CommandID),
		JobPhase: string(record.Phase), CommandState: commandStatePointer(record.CommandState),
		ExitCode: record.ExitCode, FinalEventSequence: record.FinalEventSequence,
		OutputComplete: record.OutputComplete, OutputTruncated: record.OutputTruncated,
		OutputUnavailableReason: record.OutputUnavailableReason, TeardownState: string(record.TeardownState),
		TeardownReason: record.TeardownReason, ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		Authority: authority, Controller: controllerRequest{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt: record.UpdatedAt.UTC(), Environment: record.Environment, Source: source, Duplicate: duplicate,
	}
}

func commandStatePointer(state *domain.CommandState) *string {
	if state == nil {
		return nil
	}
	value := string(*state)
	return &value
}

func (s *PrivateServer) handleCreateSession(response http.ResponseWriter, request *http.Request) {
	if request.Body == nil {
		writePrivateError(response, http.StatusBadRequest, "request body is required")
		return
	}
	limited := http.MaxBytesReader(response, request.Body, s.maxBodyBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input createSessionRequest
	if err := decoder.Decode(&input); err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			writePrivateError(response, http.StatusBadRequest, "request body contains multiple JSON values")
		} else {
			writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		}
		return
	}
	if input.SessionID == "" || input.IdempotencyKey == "" || input.Environment == "" {
		writePrivateError(response, http.StatusBadRequest, "session_id, idempotency_key, and environment are required")
		return
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(input.ExecutionTarget.Kind), input.ExecutionTarget.Profile)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	if target.Kind() != domain.TargetKindRemote {
		writePrivateError(response, http.StatusBadRequest, "runnerd private API accepts remote targets only")
		return
	}
	controllerID, err := domain.NewControllerID(input.Controller.ID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerType(input.Controller.Type), controllerID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	source, err := parseSource(input.Source)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	limits, err := parseRequestedLimits(input.Limits)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	isolation := parseIsolation(input.Isolation)
	sessionID, err := domain.NewSessionID(input.SessionID)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := domain.HashMutationRequestJSON("create_session", body, domain.CanonicalizationOptions{})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("canonical request: %v", err))
		return
	}
	result, serviceErr := s.service.CreateSession(request.Context(), execution.CreateSessionRequest{
		SessionID:            sessionID,
		IdempotencyKey:       input.IdempotencyKey,
		RequestHash:          hash,
		Environment:          input.Environment,
		Target:               target,
		Controller:           controller,
		Source:               source,
		RequestedLimits:      limits,
		Isolation:            isolation,
		MaxActiveSessions:    store.DefaultActiveSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if serviceErr != nil {
		status := privateStatusForError(serviceErr)
		if result.Session.SessionID != "" {
			writeJSON(response, status, sessionResponseFromRecord(result.Session, result.Duplicate))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, sessionResponseFromRecord(result.Session, result.Duplicate))
}

func (s *PrivateServer) handleGetSession(response http.ResponseWriter, request *http.Request) {
	prefix := sessionPathPrefix(request.URL.Path)
	rawID := strings.TrimPrefix(request.URL.Path, prefix+"/")
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writePrivateError(response, http.StatusBadRequest, "invalid session path")
		return
	}
	sessionID, err := domain.NewSessionID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	controller, err := controllerFromQuery(request)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	record, err := s.service.GetSession(request.Context(), sessionID, controller)
	if err != nil {
		writePrivateError(response, privateStatusForError(err), err.Error())
		return
	}
	writeJSON(response, http.StatusOK, sessionResponseFromRecord(record, false))
}

func isSessionCollectionPath(path string) bool {
	return path == privateSessionsPath || path == privateSessionsAliasPath
}

func isJobCollectionPath(path string) bool {
	return path == "/internal/v1/jobs" || path == "/v1/jobs"
}

func jobPathPrefix(path string) string {
	for _, prefix := range []string{"/internal/v1/jobs", "/v1/jobs"} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

func commandPathPrefix(path string) string {
	for _, prefix := range []string{privateSessionsPath, privateSessionsAliasPath} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

func commandResourcePathPrefix(path string) string {
	for _, prefix := range []string{"/internal/v1/commands", "/v1/commands"} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

func sessionPathPrefix(path string) string {
	for _, prefix := range []string{privateSessionsPath, privateSessionsAliasPath} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

func controllerFromQuery(request *http.Request) (domain.ControllerIdentity, error) {
	controllerID, err := domain.NewControllerID(request.URL.Query().Get("controller_id"))
	if err != nil {
		return domain.ControllerIdentity{}, err
	}
	return domain.NewControllerIdentity(domain.ControllerType(request.URL.Query().Get("controller_type")), controllerID)
}

func parseSource(input *sourceRequest) (domain.Source, error) {
	if input == nil || input.Mode == "" || input.Mode == string(domain.SourceModeEmpty) {
		if input != nil && (input.RepositoryAlias != "" || input.RequestedRevision != "" || input.Path != "") {
			return domain.Source{}, fmt.Errorf("%w: empty source has no additional fields", domain.ErrInvalidSource)
		}
		return domain.NewEmptySource(), nil
	}
	switch domain.SourceMode(input.Mode) {
	case domain.SourceModeGitRevision:
		return domain.NewGitRevisionSource(input.RepositoryAlias, input.RequestedRevision)
	case domain.SourceModeLocalWorktree:
		return domain.NewLocalWorktreeSource(input.Path)
	default:
		return domain.Source{}, fmt.Errorf("%w: unknown source mode %q", domain.ErrInvalidSource, input.Mode)
	}
}

func parseRequestedLimits(input *limitsRequest) (domain.RequestedLimits, error) {
	if input == nil {
		return domain.RequestedLimits{}, nil
	}
	for name, value := range map[string]int64{
		"command_timeout_seconds":      input.CommandTimeoutSeconds,
		"idle_timeout_seconds":         input.IdleTimeoutSeconds,
		"session_max_lifetime_seconds": input.SessionMaxLifetimeSeconds,
		"output_bytes_per_command":     input.OutputBytesPerCommand,
	} {
		if value < 0 {
			return domain.RequestedLimits{}, fmt.Errorf("%w: %s must not be negative", domain.ErrInvalidRequestedLimits, name)
		}
	}
	return domain.RequestedLimits{
		CommandTimeout:        secondsDuration(input.CommandTimeoutSeconds),
		IdleTimeout:           secondsDuration(input.IdleTimeoutSeconds),
		SessionMaxLifetime:    secondsDuration(input.SessionMaxLifetimeSeconds),
		OutputBytesPerCommand: input.OutputBytesPerCommand,
	}, nil
}

func secondsDuration(seconds int64) time.Duration {
	if seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func parseIsolation(input *isolationRequest) domain.IsolationRequirements {
	if input == nil {
		return domain.IsolationRequirements{}
	}
	return domain.IsolationRequirements{
		FilesystemBoundary: input.FilesystemBoundary,
		CPUControl:         input.CPUControl,
		MemoryControl:      input.MemoryControl,
		PIDControl:         input.PIDControl,
		DiskControl:        input.DiskControl,
		NetworkControl:     input.NetworkControl,
		Mounts:             input.Mounts,
		Volumes:            input.Volumes,
		PrivilegeControl:   input.PrivilegeControl,
	}
}

func sessionResponseFromRecord(record store.SessionRecord, duplicate bool) sessionResponse {
	portable := record.Source.Portable()
	source := sourceResponse{
		Mode:              string(record.Source.Mode()),
		RepositoryAlias:   record.Source.RepositoryAlias(),
		RequestedRevision: record.Source.RequestedRevision(),
		Path:              record.Source.Path(),
		Portable:          &portable,
		ResolvedCommit:    record.ResolvedRevision,
	}
	authority := "remote"
	if record.Target.Kind() == domain.TargetKindLocal {
		authority = "local"
	}
	return sessionResponse{
		SessionID:         string(record.SessionID),
		SessionState:      string(record.State),
		ExecutionTarget:   targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()},
		Authority:         authority,
		Controller:        controllerRequest{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())},
		ObservedAt:        record.UpdatedAt.UTC(),
		Environment:       record.Environment,
		Source:            source,
		RuntimeGeneration: record.RuntimeGeneration,
		ResolvedRevision:  record.ResolvedRevision,
		Duplicate:         duplicate,
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writePrivateError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func privateStatusForError(err error) int {
	switch {
	case errors.Is(err, store.ErrSessionNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrCommandNotFound), errors.Is(err, store.ErrJobNotFound):
		return http.StatusNotFound
	case errors.Is(err, execution.ErrSessionController):
		return http.StatusForbidden
	case errors.Is(err, store.ErrIdempotencyConflict), errors.Is(err, store.ErrSessionExists):
		return http.StatusConflict
	case errors.Is(err, store.ErrJobExists):
		return http.StatusConflict
	case errors.Is(err, execution.ErrRuntimeUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, store.ErrCommandReplayGap):
		return http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, domain.ErrEnvironmentTargetMismatch), errors.Is(err, domain.ErrEnvironmentSourceMismatch), errors.Is(err, domain.ErrControllerMismatch), errors.Is(err, domain.ErrUnsupportedIsolationRequirement), errors.Is(err, domain.ErrLimitExceedsServiceCeiling), errors.Is(err, domain.ErrInvalidRequestedLimits), errors.Is(err, store.ErrInvalidJob), errors.Is(err, execution.ErrSessionNotReady), errors.Is(err, execution.ErrCommandNotReady), errors.Is(err, store.ErrCommandSessionState):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func decodePrivateJSON[T any](s *PrivateServer, response http.ResponseWriter, request *http.Request) (T, []byte, error) {
	var zero T
	if request.Body == nil {
		writePrivateError(response, http.StatusBadRequest, "request body is required")
		return zero, nil, errors.New("request body is required")
	}
	limited := http.MaxBytesReader(response, request.Body, s.maxBodyBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return zero, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		writePrivateError(response, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return zero, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writePrivateError(response, http.StatusBadRequest, "request body contains multiple JSON values")
		if err == nil {
			return zero, nil, errors.New("multiple JSON values")
		}
		return zero, nil, err
	}
	return value, body, nil
}

// newRuntimeGeneration returns an unpredictable process-generation token so
// a later runnerd process cannot be mistaken for the one that created a shell.
func newRuntimeGeneration() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "linux-" + hex.EncodeToString(bytes[:]), nil
}
