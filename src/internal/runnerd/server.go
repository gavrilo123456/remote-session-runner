// Package runnerd contains the remote worker's private control-plane wiring.
// The socket in this package is intentionally owner-only; public HTTPS and
// the SSH bridge are later ingress adapters over the same execution service.
package runnerd

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
	if isSessionCollectionPath(request.URL.Path) {
		if request.Method != http.MethodPost {
			writePrivateError(response, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleCreateSession(response, request)
		return
	}
	if sessionPathPrefix(request.URL.Path) != "" {
		if request.Method != http.MethodGet {
			writePrivateError(response, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleGetSession(response, request)
		return
	}
	writePrivateError(response, http.StatusNotFound, "route not found")
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
	case errors.Is(err, execution.ErrSessionController):
		return http.StatusForbidden
	case errors.Is(err, store.ErrIdempotencyConflict), errors.Is(err, store.ErrSessionExists):
		return http.StatusConflict
	case errors.Is(err, execution.ErrRuntimeUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, domain.ErrEnvironmentTargetMismatch), errors.Is(err, domain.ErrEnvironmentSourceMismatch), errors.Is(err, domain.ErrControllerMismatch), errors.Is(err, domain.ErrUnsupportedIsolationRequirement), errors.Is(err, domain.ErrLimitExceedsServiceCeiling), errors.Is(err, domain.ErrInvalidRequestedLimits):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
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
