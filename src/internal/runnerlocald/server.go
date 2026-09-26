// Package runnerlocald contains the Mac-local executor's owner-only private
// API. It accepts only a committed local intent ID and immutable hash/ordinal;
// request payload and script bytes are reloaded from SQLite.
package runnerlocald

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

const DefaultPrivateRequestBytes int64 = 1 << 20

var (
	ErrPrivateAPIConfiguration = errors.New("private locald API configuration is invalid")
	ErrPrivateSocketPath       = errors.New("private locald socket path is invalid")
	ErrIntentRequest           = errors.New("invalid local intent acceptance request")
	ErrIntentHashMismatch      = errors.New("local intent request hash mismatch")
	ErrIntentOrdinalMismatch   = errors.New("local intent ordinal mismatch")
	ErrIntentTargetMismatch    = errors.New("local intent target is not local")
)

type PrivateServerOptions struct {
	Authority    *store.AuthorityStore
	Service      *execution.Service
	SocketPath   string
	MaxBodyBytes int64
}

type PrivateServer struct {
	authority    *store.AuthorityStore
	service      *execution.Service
	socketPath   string
	maxBodyBytes int64
	httpServer   *http.Server
	listener     net.Listener
	closed       bool
}

func NewPrivateServer(options PrivateServerOptions) (*PrivateServer, error) {
	if options.Authority == nil || options.Service == nil {
		return nil, ErrPrivateAPIConfiguration
	}
	if err := validatePrivateSocketPath(options.SocketPath); err != nil {
		return nil, err
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = DefaultPrivateRequestBytes
	}
	return &PrivateServer{authority: options.Authority, service: options.Service, socketPath: options.SocketPath, maxBodyBytes: options.MaxBodyBytes, httpServer: &http.Server{}}, nil
}

func (s *PrivateServer) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.socketPath
}

func (s *PrivateServer) Listen() error {
	if s == nil {
		return ErrPrivateAPIConfiguration
	}
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

func (s *PrivateServer) Serve() error {
	if s == nil || s.listener == nil {
		return ErrPrivateSocketPath
	}
	err := s.httpServer.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *PrivateServer) Close(ctx context.Context) error {
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

type acceptIntentRequest struct {
	IntentID            string `json:"intent_id"`
	RequestHash         string `json:"request_hash,omitempty"`
	ExpectedRequestHash string `json:"expected_request_hash,omitempty"`
	IntentOrdinal       *int64 `json:"intent_ordinal,omitempty"`
}

type intentAcceptanceResponse struct {
	IntentID        string         `json:"intent_id"`
	Operation       string         `json:"operation"`
	ResourceID      string         `json:"resource_id"`
	AcceptanceScope string         `json:"acceptance_scope"`
	Target          targetResponse `json:"execution_target"`
	SessionState    string         `json:"session_state,omitempty"`
	CommandState    string         `json:"command_state,omitempty"`
	JobPhase        string         `json:"job_phase,omitempty"`
	Duplicate       bool           `json:"duplicate,omitempty"`
	ObservedAt      time.Time      `json:"observed_at"`
}

type targetResponse struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

func (s *PrivateServer) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/internal/v1/accept-intent" || request.Method != http.MethodPost {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "resource_not_found", "message": "private locald endpoint not found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, s.maxBodyBytes+1))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_request", "message": "request body could not be read"})
		return
	}
	if int64(len(body)) > s.maxBodyBytes || domain.ValidateSerializedRequest(body) != nil {
		writeJSON(response, http.StatusRequestEntityTooLarge, map[string]string{"code": "invalid_request", "message": "request body exceeds the configured limit"})
		return
	}
	var input acceptIntentRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_request", "message": "malformed or unsupported intent request"})
		return
	}
	intentID, err := domain.NewIntentID(input.IntentID)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_request", "message": "intent_id is required"})
		return
	}
	expectedText := input.RequestHash
	if expectedText == "" {
		expectedText = input.ExpectedRequestHash
	}
	expected, err := parseCanonicalHash(expectedText)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_request", "message": "request_hash is required"})
		return
	}
	intent, err := s.authority.GetLocalIntent(request.Context(), intentID)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, store.ErrLocalIntentPayloadCorrupt) {
			status = http.StatusConflict
		}
		writeJSON(response, status, map[string]string{"code": "resource_not_found", "message": "committed local intent is unavailable"})
		return
	}
	if domain.CompareIdempotency(intent.RequestHash, expected) == domain.IdempotencyConflict {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "intent_hash_mismatch", "message": ErrIntentHashMismatch.Error()})
		return
	}
	if intent.Operation == "submit_command" {
		if input.IntentOrdinal == nil || intent.IntentOrdinal == nil || *input.IntentOrdinal != *intent.IntentOrdinal {
			writeJSON(response, http.StatusConflict, map[string]string{"code": "intent_ordinal_mismatch", "message": ErrIntentOrdinalMismatch.Error()})
			return
		}
	} else if input.IntentOrdinal != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "intent_ordinal_mismatch", "message": ErrIntentOrdinalMismatch.Error()})
		return
	}
	if intent.Target.Kind() != domain.TargetKindLocal {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "environment_target_mismatch", "message": ErrIntentTargetMismatch.Error()})
		return
	}
	accepted, err := s.acceptIntent(request.Context(), intent)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"code": "intent_acceptance_failed", "message": sanitizeError(err)})
		return
	}
	writeJSON(response, http.StatusAccepted, accepted)
}

func (s *PrivateServer) acceptIntent(ctx context.Context, intent store.LocalIntentRecord) (intentAcceptanceResponse, error) {
	if intent.Controller.Type() != domain.ControllerTypeLocalUser {
		return intentAcceptanceResponse{}, fmt.Errorf("%w: locald requires local_user controller", ErrIntentTargetMismatch)
	}
	base := intentAcceptanceResponse{IntentID: string(intent.IntentID), Operation: intent.Operation, ResourceID: intent.ResourceID, AcceptanceScope: "target_authority", Target: targetResponse{Kind: string(intent.Target.Kind()), Profile: intent.Target.Profile()}, ObservedAt: time.Now().UTC()}
	payload, err := decodeIntentPayload(intent.PayloadJSON)
	if err != nil {
		return intentAcceptanceResponse{}, err
	}
	switch intent.Operation {
	case "create_session":
		result, err := s.service.CreateSession(ctx, execution.CreateSessionRequest{SessionID: intent.SessionID, IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash, Environment: intent.Environment, Target: intent.Target, Controller: intent.Controller, Source: intent.Source, RequestedLimits: payload.RequestedLimits, Isolation: payload.Isolation})
		if err != nil {
			return intentAcceptanceResponse{}, err
		}
		base.SessionState = string(result.Session.State)
		base.Duplicate = result.Duplicate
	case "submit_command":
		timeout := payload.Timeout
		result, err := s.service.SubmitCommand(ctx, execution.SubmitCommandRequest{CommandID: intent.CommandID, SessionID: intent.SessionID, Controller: intent.Controller, IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash, Script: string(intent.ScriptBytes), Timeout: timeout, IntentOrdinal: dereferenceOrdinal(intent.IntentOrdinal)})
		if err != nil {
			return intentAcceptanceResponse{}, err
		}
		base.CommandState = string(result.Command.State)
		base.Duplicate = result.Duplicate
	case "cancel_command":
		result, err := s.service.CancelCommand(ctx, execution.CancelCommandRequest{CommandID: intent.CommandID, Controller: intent.Controller, IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash})
		if err != nil {
			return intentAcceptanceResponse{}, err
		}
		base.CommandState = string(result.Command.State)
		base.Duplicate = result.Duplicate
	case "close_session":
		result, err := s.service.CloseSession(ctx, execution.CloseSessionRequest{SessionID: intent.SessionID, Controller: intent.Controller, IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash, Policy: payload.Policy})
		if err != nil {
			return intentAcceptanceResponse{}, err
		}
		base.SessionState = string(result.Session.State)
		base.Duplicate = result.Duplicate
	default:
		return intentAcceptanceResponse{}, fmt.Errorf("%w: operation %q is not wired", ErrIntentRequest, intent.Operation)
	}
	return base, nil
}

type decodedIntentPayload struct {
	TimeoutSeconds            int64  `json:"timeout_seconds,omitempty"`
	Policy                    string `json:"policy,omitempty"`
	CommandTimeoutSeconds     int64  `json:"command_timeout_seconds,omitempty"`
	IdleTimeoutSeconds        int64  `json:"idle_timeout_seconds,omitempty"`
	SessionMaxLifetimeSeconds int64  `json:"session_max_lifetime_seconds,omitempty"`
	OutputBytesPerCommand     int64  `json:"output_bytes_per_command,omitempty"`
	IsolationFlags            struct {
		FilesystemBoundary bool `json:"filesystem_boundary,omitempty"`
		CPUControl         bool `json:"cpu_control,omitempty"`
		MemoryControl      bool `json:"memory_control,omitempty"`
		PIDControl         bool `json:"pid_control,omitempty"`
		DiskControl        bool `json:"disk_control,omitempty"`
		NetworkControl     bool `json:"network_control,omitempty"`
		Mounts             bool `json:"mounts,omitempty"`
		Volumes            bool `json:"volumes,omitempty"`
		PrivilegeControl   bool `json:"privilege_control,omitempty"`
	} `json:"isolation,omitempty"`
	Timeout         time.Duration
	RequestedLimits domain.RequestedLimits
	Isolation       domain.IsolationRequirements
}

func decodeIntentPayload(raw []byte) (decodedIntentPayload, error) {
	var payload decodedIntentPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return decodedIntentPayload{}, fmt.Errorf("%w: payload: %v", ErrIntentRequest, err)
	}
	if payload.TimeoutSeconds > 0 {
		payload.Timeout = time.Duration(payload.TimeoutSeconds) * time.Second
	} else if payload.CommandTimeoutSeconds > 0 {
		payload.Timeout = time.Duration(payload.CommandTimeoutSeconds) * time.Second
	}
	payload.RequestedLimits = domain.RequestedLimits{CommandTimeout: time.Duration(payload.CommandTimeoutSeconds) * time.Second, IdleTimeout: time.Duration(payload.IdleTimeoutSeconds) * time.Second, SessionMaxLifetime: time.Duration(payload.SessionMaxLifetimeSeconds) * time.Second, OutputBytesPerCommand: payload.OutputBytesPerCommand}
	payload.Isolation = domain.IsolationRequirements{FilesystemBoundary: payload.IsolationFlags.FilesystemBoundary, CPUControl: payload.IsolationFlags.CPUControl, MemoryControl: payload.IsolationFlags.MemoryControl, PIDControl: payload.IsolationFlags.PIDControl, DiskControl: payload.IsolationFlags.DiskControl, NetworkControl: payload.IsolationFlags.NetworkControl, Mounts: payload.IsolationFlags.Mounts, Volumes: payload.IsolationFlags.Volumes, PrivilegeControl: payload.IsolationFlags.PrivilegeControl}
	return payload, nil
}

func dereferenceOrdinal(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func parseCanonicalHash(value string) (domain.CanonicalHash, error) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "v") {
		return domain.CanonicalHash{}, ErrIntentRequest
	}
	var version uint64
	if _, err := fmt.Sscanf(parts[0], "v%d", &version); err != nil || version == 0 || version > 65535 {
		return domain.CanonicalHash{}, ErrIntentRequest
	}
	digest, err := hex.DecodeString(parts[1])
	if err != nil || len(digest) != sha256.Size {
		return domain.CanonicalHash{}, ErrIntentRequest
	}
	return domain.NewCanonicalHash(uint16(version), digest)
}

func validatePrivateSocketPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%w: path must be absolute and clean", ErrPrivateSocketPath)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || !parentInfo.IsDir() {
		return fmt.Errorf("%w: parent directory unavailable", ErrPrivateSocketPath)
	}
	if parentInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: parent directory must exclude group and other access", ErrPrivateSocketPath)
	}
	stat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: parent directory owner mismatch", ErrPrivateSocketPath)
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
}
