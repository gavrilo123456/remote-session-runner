// Package runnerlocald contains the Mac-local executor's owner-only private
// API. It accepts only a committed local intent ID and immutable hash/ordinal;
// request payload and script bytes are reloaded from SQLite.
package runnerlocald

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	Owner        domain.ControllerIdentity
	SocketPath   string
	MaxBodyBytes int64
}

type PrivateServer struct {
	authority    *store.AuthorityStore
	service      *execution.Service
	owner        domain.ControllerIdentity
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
	owner := options.Owner
	if owner.Type() == "" && owner.ID() == "" {
		var err error
		owner, err = domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
		if err != nil {
			return nil, ErrPrivateAPIConfiguration
		}
	}
	if owner.Type() != domain.ControllerTypeLocalUser || owner.ID() == "" {
		return nil, fmt.Errorf("%w: owner must be a local_user controller", ErrPrivateAPIConfiguration)
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = DefaultPrivateRequestBytes
	}
	return &PrivateServer{authority: options.Authority, service: options.Service, owner: owner, socketPath: options.SocketPath, maxBodyBytes: options.MaxBodyBytes, httpServer: &http.Server{}}, nil
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
	if localSessionPathPrefix(request.URL.Path) != "" {
		if request.Method == http.MethodDelete {
			s.handleCloseSession(response, request)
			return
		}
		if request.Method == http.MethodGet {
			s.handleGetSession(response, request)
			return
		}
	}
	if localCommandPathPrefix(request.URL.Path) != "" {
		prefix := localCommandPathPrefix(request.URL.Path)
		remainder := strings.TrimPrefix(request.URL.Path, prefix+"/")
		parts := strings.Split(remainder, "/")
		if len(parts) == 2 && parts[1] == "events" && request.Method == http.MethodGet {
			s.handleCommandEvents(response, request, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "cancel" && request.Method == http.MethodPost {
			s.handleCancelCommand(response, request, parts[0])
			return
		}
		if len(parts) == 1 && request.Method == http.MethodGet {
			s.handleGetCommand(response, request, parts[0])
			return
		}
	}
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
	if !sameController(intent.Controller, s.owner) {
		return intentAcceptanceResponse{}, fmt.Errorf("%w: locald owner controller mismatch", execution.ErrSessionController)
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

type localSessionResponse struct {
	SessionID         string         `json:"session_id"`
	SessionState      string         `json:"session_state"`
	ExecutionTarget   targetResponse `json:"execution_target"`
	Authority         string         `json:"authority"`
	Controller        ownerResponse  `json:"controller"`
	ObservedAt        time.Time      `json:"observed_at"`
	Environment       string         `json:"environment"`
	Source            localSource    `json:"source"`
	RuntimeGeneration string         `json:"runtime_generation,omitempty"`
	ResolvedRevision  string         `json:"resolved_revision,omitempty"`
	Duplicate         bool           `json:"duplicate,omitempty"`
}

type ownerResponse struct {
	Type string `json:"controller_type"`
	ID   string `json:"controller_id"`
}

type localSource struct {
	Mode              string `json:"mode"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	ResolvedCommit    string `json:"resolved_commit,omitempty"`
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

type localCommandResponse struct {
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

type localCommandEventResponse struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	ByteCount  int64     `json:"byte_count"`
	DataBase64 string    `json:"data_base64,omitempty"`
}

type localCancelRequest struct {
	CommandID      string `json:"command_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type localCloseRequest struct {
	SessionID      string `json:"session_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
	Policy         string `json:"policy,omitempty"`
}

func (s *PrivateServer) handleGetSession(response http.ResponseWriter, request *http.Request) {
	if err := rejectControllerQuery(request); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	idText, err := localPathID(request.URL.Path, localSessionPathPrefix(request.URL.Path))
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	sessionID, err := domain.NewSessionID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	record, err := s.service.GetSession(request.Context(), sessionID, s.owner)
	if err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	if record.Target.Kind() != domain.TargetKindLocal {
		writePrivateError(response, http.StatusForbidden, ErrIntentTargetMismatch.Error())
		return
	}
	writeJSON(response, http.StatusOK, localSessionResponseFromRecord(record, false))
}

func (s *PrivateServer) handleGetCommand(response http.ResponseWriter, request *http.Request, rawID string) {
	if err := rejectControllerQuery(request); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writePrivateError(response, http.StatusBadRequest, "invalid command path")
		return
	}
	commandID, err := domain.NewCommandID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	command, err := s.service.GetCommand(request.Context(), commandID, s.owner)
	if err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	if err := s.ensureLocalCommand(request.Context(), command.SessionID); err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	writeJSON(response, http.StatusOK, localCommandResponseFromRecord(command, false))
}

func (s *PrivateServer) handleCommandEvents(response http.ResponseWriter, request *http.Request, rawID string) {
	if err := rejectControllerQuery(request); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writePrivateError(response, http.StatusBadRequest, "invalid command path")
		return
	}
	commandID, err := domain.NewCommandID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	after, err := parseEventCursor(request.URL.Query().Get("after"))
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	command, err := s.service.GetCommand(request.Context(), commandID, s.owner)
	if err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	if err := s.ensureLocalCommand(request.Context(), command.SessionID); err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	follow := request.URL.Query().Get("follow") == "true"
	if !follow {
		events, replayErr := s.service.ReplayCommandEvents(request.Context(), commandID, s.owner, after)
		if replayErr != nil {
			writePrivateError(response, localPrivateStatusForError(replayErr), replayErr.Error())
			return
		}
		response.Header().Set("Content-Type", "application/x-ndjson")
		response.WriteHeader(http.StatusOK)
		for _, event := range events {
			if err := writeLocalEvent(response, event); err != nil {
				return
			}
		}
		return
	}
	subscription, err := s.service.SubscribeCommandEvents(request.Context(), commandID, s.owner, after, 256)
	if err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	defer subscription.Close()
	response.Header().Set("Content-Type", "application/x-ndjson")
	response.WriteHeader(http.StatusOK)
	flusher, _ := response.(http.Flusher)
	for {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				return
			}
			if err := writeLocalEvent(response, event); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			if isTerminalCommandEvent(event.Type) {
				return
			}
		case <-subscription.Errors():
			return
		case <-request.Context().Done():
			return
		}
	}
}

func (s *PrivateServer) handleCancelCommand(response http.ResponseWriter, request *http.Request, rawID string) {
	if err := rejectControllerQuery(request); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	idText, err := url.PathUnescape(rawID)
	if err != nil || idText == "" || strings.Contains(idText, "/") {
		writePrivateError(response, http.StatusBadRequest, "invalid command path")
		return
	}
	commandID, err := domain.NewCommandID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	input, _, err := decodeLocalJSON[localCancelRequest](s, response, request)
	if err != nil {
		return
	}
	if input.CommandID != "" && input.CommandID != idText {
		writePrivateError(response, http.StatusBadRequest, "command path and body command_id differ")
		return
	}
	if input.IdempotencyKey == "" {
		writePrivateError(response, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	command, err := s.service.GetCommand(request.Context(), commandID, s.owner)
	if err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	if err := s.ensureLocalCommand(request.Context(), command.SessionID); err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	hash, err := localMutationHash("cancel_command", map[string]string{"command_id": idText})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, serviceErr := s.service.CancelCommand(request.Context(), execution.CancelCommandRequest{CommandID: commandID, Controller: s.owner, IdempotencyKey: input.IdempotencyKey, RequestHash: hash, IdempotencyRetention: store.DefaultSessionIdempotencyRetention})
	if serviceErr != nil {
		status := localPrivateStatusForError(serviceErr)
		if result.Command.CommandID != "" {
			writeJSON(response, status, localCommandResponseFromRecord(result.Command, result.Duplicate))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, localCommandResponseFromRecord(result.Command, result.Duplicate))
}

func (s *PrivateServer) handleCloseSession(response http.ResponseWriter, request *http.Request) {
	if err := rejectControllerQuery(request); err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	idText, err := localPathID(request.URL.Path, localSessionPathPrefix(request.URL.Path))
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	sessionID, err := domain.NewSessionID(idText)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	input, _, err := decodeLocalJSON[localCloseRequest](s, response, request)
	if err != nil {
		return
	}
	if input.SessionID != "" && input.SessionID != idText {
		writePrivateError(response, http.StatusBadRequest, "session path and body session_id differ")
		return
	}
	if input.IdempotencyKey == "" {
		writePrivateError(response, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	policy := input.Policy
	if policy == "" {
		policy = "graceful"
	}
	session, err := s.service.GetSession(request.Context(), sessionID, s.owner)
	if err != nil {
		writePrivateError(response, localPrivateStatusForError(err), err.Error())
		return
	}
	if session.Target.Kind() != domain.TargetKindLocal {
		writePrivateError(response, http.StatusForbidden, ErrIntentTargetMismatch.Error())
		return
	}
	hash, err := localMutationHash("close_session", map[string]string{"session_id": idText, "policy": policy})
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, serviceErr := s.service.CloseSession(request.Context(), execution.CloseSessionRequest{SessionID: sessionID, Controller: s.owner, IdempotencyKey: input.IdempotencyKey, RequestHash: hash, Policy: policy, IdempotencyRetention: store.DefaultSessionIdempotencyRetention})
	if serviceErr != nil {
		status := localPrivateStatusForError(serviceErr)
		if result.Session.SessionID != "" {
			writeJSON(response, status, localSessionResponseFromRecord(result.Session, result.Duplicate))
			return
		}
		writePrivateError(response, status, serviceErr.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, localSessionResponseFromRecord(result.Session, result.Duplicate))
}

func (s *PrivateServer) ensureLocalCommand(ctx context.Context, sessionID domain.SessionID) error {
	session, err := s.service.GetSession(ctx, sessionID, s.owner)
	if err != nil {
		return err
	}
	if session.Target.Kind() != domain.TargetKindLocal {
		return ErrIntentTargetMismatch
	}
	return nil
}

func localSessionPathPrefix(path string) string {
	for _, prefix := range []string{"/internal/v1/sessions", "/v1/sessions"} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

func localCommandPathPrefix(path string) string {
	for _, prefix := range []string{"/internal/v1/commands", "/v1/commands"} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

func localPathID(path, prefix string) (string, error) {
	if prefix == "" {
		return "", errors.New("invalid resource path")
	}
	raw := strings.TrimPrefix(path, prefix+"/")
	id, err := url.PathUnescape(raw)
	if err != nil || id == "" || strings.Contains(id, "/") {
		return "", errors.New("invalid resource path")
	}
	return id, nil
}

func localSessionResponseFromRecord(record store.SessionRecord, duplicate bool) localSessionResponse {
	portable := record.Source.Portable()
	authority := "remote"
	if record.Target.Kind() == domain.TargetKindLocal {
		authority = "local"
	}
	return localSessionResponse{SessionID: string(record.SessionID), SessionState: string(record.State), ExecutionTarget: targetResponse{Kind: string(record.Target.Kind()), Profile: record.Target.Profile()}, Authority: authority, Controller: ownerResponse{Type: string(record.Controller.Type()), ID: string(record.Controller.ID())}, ObservedAt: record.UpdatedAt.UTC(), Environment: record.Environment, Source: localSource{Mode: string(record.Source.Mode()), RepositoryAlias: record.Source.RepositoryAlias(), RequestedRevision: record.Source.RequestedRevision(), ResolvedCommit: record.ResolvedRevision, Path: record.Source.Path(), Portable: &portable}, RuntimeGeneration: record.RuntimeGeneration, ResolvedRevision: record.ResolvedRevision, Duplicate: duplicate}
}

func localCommandResponseFromRecord(record store.CommandRecord, duplicate bool) localCommandResponse {
	sum := sha256.Sum256(record.ScriptBytes)
	return localCommandResponse{CommandID: string(record.CommandID), SessionID: string(record.SessionID), Ordinal: record.Ordinal, CommandState: string(record.State), ExitCode: record.ExitCode, FinalEventSequence: record.FinalEventSequence, OutputComplete: record.OutputComplete, OutputTruncated: record.OutputTruncated, OutputUnavailableReason: record.OutputUnavailableReason, ScriptByteCount: len(record.ScriptBytes), ScriptSHA256: hex.EncodeToString(sum[:]), Duplicate: duplicate}
}

func writeLocalEvent(response http.ResponseWriter, event store.CommandEventRecord) error {
	value := localCommandEventResponse{CommandID: string(event.CommandID), Sequence: event.Sequence, Type: event.Type, OccurredAt: event.OccurredAt.UTC(), ByteCount: event.ByteCount}
	if event.Type == "stdout" || event.Type == "stderr" {
		value.DataBase64 = base64.StdEncoding.EncodeToString(event.Payload)
	}
	return json.NewEncoder(response).Encode(value)
}

func localMutationHash(operation string, fields map[string]string) (domain.CanonicalHash, error) {
	payload := make(map[string]string, len(fields)+1)
	payload["operation"] = operation
	for key, value := range fields {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.CanonicalHash{}, err
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON(operation, raw, domain.CanonicalizationOptions{})
	if err != nil {
		return domain.CanonicalHash{}, err
	}
	return domain.HashMutationRequestJSON(operation, canonical, domain.CanonicalizationOptions{})
}

func parseEventCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("invalid event cursor")
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

func localPrivateStatusForError(err error) int {
	switch {
	case errors.Is(err, store.ErrSessionNotFound), errors.Is(err, store.ErrCommandNotFound):
		return http.StatusNotFound
	case errors.Is(err, execution.ErrSessionController):
		return http.StatusForbidden
	case errors.Is(err, ErrIntentTargetMismatch):
		return http.StatusForbidden
	case errors.Is(err, store.ErrIdempotencyConflict), errors.Is(err, store.ErrSessionExists):
		return http.StatusConflict
	case errors.Is(err, execution.ErrRuntimeUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, store.ErrCommandReplayGap):
		return http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, domain.ErrEnvironmentTargetMismatch), errors.Is(err, domain.ErrEnvironmentSourceMismatch), errors.Is(err, domain.ErrControllerMismatch), errors.Is(err, domain.ErrUnsupportedIsolationRequirement), errors.Is(err, domain.ErrLimitExceedsServiceCeiling), errors.Is(err, domain.ErrInvalidRequestedLimits), errors.Is(err, execution.ErrSessionNotReady), errors.Is(err, execution.ErrCommandNotReady), errors.Is(err, store.ErrCommandSessionState):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writePrivateError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func decodeLocalJSON[T any](s *PrivateServer, response http.ResponseWriter, request *http.Request) (T, []byte, error) {
	var zero T
	if request.Body == nil {
		writePrivateError(response, http.StatusBadRequest, "request body is required")
		return zero, nil, errors.New("request body is required")
	}
	limited := http.MaxBytesReader(response, request.Body, s.maxBodyBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		writePrivateError(response, http.StatusBadRequest, "request body could not be read")
		return zero, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		writePrivateError(response, http.StatusBadRequest, "malformed or unsupported request JSON")
		return zero, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writePrivateError(response, http.StatusBadRequest, "request body contains multiple JSON values")
		return zero, nil, errors.New("multiple JSON values")
	}
	return value, body, nil
}

func sameController(left, right domain.ControllerIdentity) bool {
	return left.Type() == right.Type() && left.ID() == right.ID()
}

func rejectControllerQuery(request *http.Request) error {
	if request.URL.Query().Get("controller_type") != "" || request.URL.Query().Get("controller_id") != "" {
		return errors.New("controller is fixed by the owner-only local socket")
	}
	return nil
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
