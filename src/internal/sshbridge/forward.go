package sshbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

const defaultPrivateResponseBytes int64 = domain.MaxSerializedRequestBytes

const bridgeMaxOutputChunkBytes = 16 << 10

const maxBridgeEventLineBytes = domain.MaxSerializedFrameBytes

var (
	ErrForwarderConfiguration = errors.New("SSH bridge forwarder configuration is invalid")
	ErrPrivateResponse        = errors.New("private runnerd response is invalid")
)

// ForwarderOptions configures the bridge's HTTP/JSON adapter to the private
// runnerd Unix API. The bridge never starts a process or opens a database.
type ForwarderOptions struct {
	Client           *http.Client
	BaseURL          string
	MaxResponseBytes int64
}

// RunnerdForwarder forwards the bridge operations implemented through P054.
type RunnerdForwarder struct {
	client           *http.Client
	baseURL          string
	maxResponseBytes int64
}

// NewRunnerdForwarder validates and constructs a private runnerd forwarder.
func NewRunnerdForwarder(options ForwarderOptions) (*RunnerdForwarder, error) {
	if options.Client == nil {
		return nil, ErrForwarderConfiguration
	}
	if options.BaseURL == "" {
		options.BaseURL = "http://runnerd"
	}
	parsed, err := url.Parse(options.BaseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("%w: BaseURL must be an HTTP origin", ErrForwarderConfiguration)
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = defaultPrivateResponseBytes
	}
	if options.MaxResponseBytes > defaultPrivateResponseBytes {
		return nil, fmt.Errorf("%w: response limit exceeds %d bytes", ErrForwarderConfiguration, defaultPrivateResponseBytes)
	}
	return &RunnerdForwarder{client: options.Client, baseURL: strings.TrimRight(options.BaseURL, "/"), maxResponseBytes: options.MaxResponseBytes}, nil
}

// NewUnixSocketHTTPClient creates an HTTP client whose only network dial is to
// the owner-restricted runnerd Unix socket.
func NewUnixSocketHTTPClient(socketPath string) (*http.Client, error) {
	if socketPath == "" || !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return nil, fmt.Errorf("%w: invalid Unix socket path", ErrForwarderConfiguration)
	}
	transport := &http.Transport{}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}
	return &http.Client{Transport: transport}, nil
}

// NewUnixSocketForwarder combines the owner-only Unix client with the
// runnerd's private HTTP origin.
func NewUnixSocketForwarder(socketPath string) (*RunnerdForwarder, error) {
	client, err := NewUnixSocketHTTPClient(socketPath)
	if err != nil {
		return nil, err
	}
	return NewRunnerdForwarder(ForwarderOptions{Client: client})
}

// Handle implements RequestHandler for non-streaming bridge operations through
// P054. Event streaming is exposed through StreamRequestHandler.
func (f *RunnerdForwarder) Handle(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	if f == nil || f.client == nil {
		return ReplyFrame{}, ErrForwarderConfiguration
	}
	switch request.Operation {
	case OperationCreateOrResumeSession:
		return f.createSession(ctx, controller, request)
	case OperationGetSession:
		return f.getSession(ctx, controller, request)
	case OperationSubmitOrResumeCommand:
		return f.submitCommand(ctx, controller, request)
	case OperationGetCommand:
		return f.getCommand(ctx, controller, request)
	case OperationRunOrResumeJob:
		return f.runJob(ctx, controller, request)
	case OperationGetJob:
		return f.getJob(ctx, controller, request)
	case OperationCancelCommand:
		return f.cancelCommand(ctx, controller, request)
	case OperationCloseSession:
		return f.closeSession(ctx, controller, request)
	case OperationStreamCommandEvents:
		return ReplyFrame{}, fmt.Errorf("%w: %s requires streaming dispatch", ErrOperationUnsupported, request.Operation)
	default:
		return ReplyFrame{}, fmt.Errorf("%w: %s", ErrOperationUnsupported, request.Operation)
	}
}

type bridgeTarget struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

type bridgeController struct {
	Type string `json:"controller_type"`
	ID   string `json:"controller_id"`
}

type bridgeCreatePayload struct {
	Environment     string          `json:"environment"`
	ExecutionTarget bridgeTarget    `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Isolation       json.RawMessage `json:"isolation,omitempty"`
}

type bridgeGetSessionPayload struct {
	SessionID string `json:"session_id"`
}

type bridgeSubmitCommandPayload struct {
	SessionID      string `json:"session_id"`
	IntentOrdinal  int64  `json:"intent_ordinal"`
	Script         string `json:"script"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`
}

type bridgeGetCommandPayload struct {
	CommandID string `json:"command_id"`
}

type bridgeRunJobPayload struct {
	SessionID       string          `json:"session_id,omitempty"`
	CommandID       string          `json:"command_id,omitempty"`
	Environment     string          `json:"environment"`
	ExecutionTarget bridgeTarget    `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Script          string          `json:"script"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Isolation       json.RawMessage `json:"isolation,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

type bridgeGetJobPayload struct {
	JobID string `json:"job_id"`
}

type bridgeStreamEventsPayload struct {
	CommandID     string `json:"command_id"`
	AfterSequence int64  `json:"after_sequence"`
	Follow        bool   `json:"follow,omitempty"`
}

type bridgeCancelCommandPayload struct {
	CommandID string `json:"command_id"`
}

type bridgeCloseSessionPayload struct {
	SessionID string `json:"session_id"`
	Policy    string `json:"policy,omitempty"`
}

type privateCreateSessionRequest struct {
	SessionID       string           `json:"session_id"`
	IdempotencyKey  string           `json:"idempotency_key"`
	RequestID       string           `json:"request_id,omitempty"`
	Environment     string           `json:"environment"`
	ExecutionTarget bridgeTarget     `json:"execution_target"`
	Controller      bridgeController `json:"controller"`
	Source          json.RawMessage  `json:"source,omitempty"`
	Limits          json.RawMessage  `json:"limits,omitempty"`
	Isolation       json.RawMessage  `json:"isolation,omitempty"`
}

type privateSubmitCommandRequest struct {
	CommandID      string           `json:"command_id"`
	SessionID      string           `json:"session_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	RequestID      string           `json:"request_id,omitempty"`
	Controller     bridgeController `json:"controller"`
	Script         string           `json:"script"`
	TimeoutSeconds int64            `json:"timeout_seconds,omitempty"`
	IntentOrdinal  int64            `json:"intent_ordinal,omitempty"`
}

type privateRunJobRequest struct {
	JobID           string           `json:"job_id"`
	SessionID       string           `json:"session_id"`
	CommandID       string           `json:"command_id"`
	IdempotencyKey  string           `json:"idempotency_key"`
	RequestID       string           `json:"request_id,omitempty"`
	Environment     string           `json:"environment"`
	ExecutionTarget bridgeTarget     `json:"execution_target"`
	Controller      bridgeController `json:"controller"`
	Source          json.RawMessage  `json:"source,omitempty"`
	Script          string           `json:"script"`
	Limits          json.RawMessage  `json:"limits,omitempty"`
	Isolation       json.RawMessage  `json:"isolation,omitempty"`
	Policy          json.RawMessage  `json:"policy,omitempty"`
}

type privateCancelCommandRequest struct {
	CommandID      string           `json:"command_id,omitempty"`
	IdempotencyKey string           `json:"idempotency_key"`
	RequestID      string           `json:"request_id,omitempty"`
	Controller     bridgeController `json:"controller"`
}

type privateCloseSessionRequest struct {
	SessionID      string           `json:"session_id,omitempty"`
	IdempotencyKey string           `json:"idempotency_key"`
	RequestID      string           `json:"request_id,omitempty"`
	Controller     bridgeController `json:"controller"`
	Policy         string           `json:"policy,omitempty"`
}

func (f *RunnerdForwarder) createSession(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeCreatePayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.Environment) == "" || strings.TrimSpace(payload.ExecutionTarget.Kind) == "" || strings.TrimSpace(payload.ExecutionTarget.Profile) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: create payload requires environment and execution_target", ErrInvalidFrame)
	}
	body, err := json.Marshal(privateCreateSessionRequest{
		SessionID: request.ResourceID, IdempotencyKey: request.IdempotencyKey, RequestID: request.RequestID,
		Environment: payload.Environment, ExecutionTarget: payload.ExecutionTarget,
		Controller: bridgeController{Type: string(controller.Type()), ID: string(controller.ID())},
		Source:     payload.Source, Limits: payload.Limits, Isolation: payload.Isolation,
	})
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: create request: %v", ErrInvalidFrame, err)
	}
	return f.doJSON(ctx, request.RequestID, http.MethodPost, "/internal/v1/sessions", nil, body)
}

func (f *RunnerdForwarder) getSession(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeGetSessionPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: get_session requires session_id", ErrInvalidFrame)
	}
	query := url.Values{}
	query.Set("controller_type", string(controller.Type()))
	query.Set("controller_id", string(controller.ID()))
	return f.doJSON(ctx, request.RequestID, http.MethodGet, "/internal/v1/sessions/"+url.PathEscape(payload.SessionID), query, nil)
}

func (f *RunnerdForwarder) submitCommand(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeSubmitCommandPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.SessionID) == "" || payload.IntentOrdinal < 1 {
		return ReplyFrame{}, fmt.Errorf("%w: submit payload requires session_id and positive intent_ordinal", ErrInvalidFrame)
	}
	if err := domain.ValidateScriptUTF8(payload.Script); err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	body, err := json.Marshal(privateSubmitCommandRequest{
		CommandID: request.ResourceID, SessionID: payload.SessionID, IdempotencyKey: request.IdempotencyKey,
		RequestID: request.RequestID, Controller: bridgeController{Type: string(controller.Type()), ID: string(controller.ID())},
		Script: payload.Script, TimeoutSeconds: payload.TimeoutSeconds, IntentOrdinal: payload.IntentOrdinal,
	})
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: submit request: %v", ErrInvalidFrame, err)
	}
	return f.doJSON(ctx, request.RequestID, http.MethodPost, "/internal/v1/sessions/"+url.PathEscape(payload.SessionID)+"/commands", nil, body)
}

func (f *RunnerdForwarder) getCommand(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeGetCommandPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.CommandID) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: get_command requires command_id", ErrInvalidFrame)
	}
	query := url.Values{}
	query.Set("controller_type", string(controller.Type()))
	query.Set("controller_id", string(controller.ID()))
	return f.doJSON(ctx, request.RequestID, http.MethodGet, "/internal/v1/commands/"+url.PathEscape(payload.CommandID), query, nil)
}

func (f *RunnerdForwarder) runJob(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeRunJobPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.Environment) == "" || strings.TrimSpace(payload.ExecutionTarget.Kind) == "" || strings.TrimSpace(payload.ExecutionTarget.Profile) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: run payload requires environment and execution_target", ErrInvalidFrame)
	}
	if err := domain.ValidateScriptUTF8(payload.Script); err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	// The bridge frame's resource_id is the stable job identity. Older/minimal
	// frames did not carry the two child IDs, so derive them deterministically
	// from that job ID; retries can never allocate replacement resources.
	sessionID := payload.SessionID
	if strings.TrimSpace(sessionID) == "" {
		sessionID = request.ResourceID + "-session"
	}
	commandID := payload.CommandID
	if strings.TrimSpace(commandID) == "" {
		commandID = request.ResourceID + "-command"
	}
	body, err := json.Marshal(privateRunJobRequest{
		JobID: request.ResourceID, SessionID: sessionID, CommandID: commandID,
		IdempotencyKey: request.IdempotencyKey, RequestID: request.RequestID,
		Environment: payload.Environment, ExecutionTarget: payload.ExecutionTarget,
		Controller: bridgeController{Type: string(controller.Type()), ID: string(controller.ID())},
		Source:     payload.Source, Script: payload.Script, Limits: payload.Limits,
		Isolation: payload.Isolation, Policy: payload.Policy,
	})
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: run request: %v", ErrInvalidFrame, err)
	}
	return f.doJSON(ctx, request.RequestID, http.MethodPost, "/internal/v1/jobs", nil, body)
}

func (f *RunnerdForwarder) getJob(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeGetJobPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.JobID) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: get_job requires job_id", ErrInvalidFrame)
	}
	query := url.Values{}
	query.Set("controller_type", string(controller.Type()))
	query.Set("controller_id", string(controller.ID()))
	return f.doJSON(ctx, request.RequestID, http.MethodGet, "/internal/v1/jobs/"+url.PathEscape(payload.JobID), query, nil)
}

func (f *RunnerdForwarder) cancelCommand(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeCancelCommandPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.CommandID) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: cancel_command requires command_id", ErrInvalidFrame)
	}
	if payload.CommandID != request.ResourceID {
		return ReplyFrame{}, fmt.Errorf("%w: cancel command resource_id and command_id differ", ErrInvalidFrame)
	}
	body, err := json.Marshal(privateCancelCommandRequest{
		CommandID: payload.CommandID, IdempotencyKey: request.IdempotencyKey, RequestID: request.RequestID,
		Controller: bridgeController{Type: string(controller.Type()), ID: string(controller.ID())},
	})
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: cancel request: %v", ErrInvalidFrame, err)
	}
	return f.doJSON(ctx, request.RequestID, http.MethodPost, "/internal/v1/commands/"+url.PathEscape(payload.CommandID)+"/cancel", nil, body)
}

func (f *RunnerdForwarder) closeSession(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	payload, err := decodeBridgePayload[bridgeCloseSessionPayload](request.Payload)
	if err != nil {
		return ReplyFrame{}, err
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		return ReplyFrame{}, fmt.Errorf("%w: close_session requires session_id", ErrInvalidFrame)
	}
	if payload.SessionID != request.ResourceID {
		return ReplyFrame{}, fmt.Errorf("%w: close session resource_id and session_id differ", ErrInvalidFrame)
	}
	body, err := json.Marshal(privateCloseSessionRequest{
		SessionID: payload.SessionID, IdempotencyKey: request.IdempotencyKey, RequestID: request.RequestID,
		Controller: bridgeController{Type: string(controller.Type()), ID: string(controller.ID())}, Policy: payload.Policy,
	})
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: close request: %v", ErrInvalidFrame, err)
	}
	return f.doJSON(ctx, request.RequestID, http.MethodDelete, "/internal/v1/sessions/"+url.PathEscape(payload.SessionID), nil, body)
}

type privateCommandEvent struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	ByteCount  int64     `json:"byte_count"`
	DataBase64 string    `json:"data_base64,omitempty"`
}

// Stream forwards private runnerd event NDJSON as bounded bridge event frames.
// Reconnects use the supplied cursor and never allocate or mutate resources.
func (f *RunnerdForwarder) Stream(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame, send func(ReplyFrame) error) error {
	if f == nil || f.client == nil || send == nil {
		return ErrForwarderConfiguration
	}
	payload, err := decodeBridgePayload[bridgeStreamEventsPayload](request.Payload)
	if err != nil {
		return err
	}
	if strings.TrimSpace(payload.CommandID) == "" || payload.AfterSequence < 0 {
		return fmt.Errorf("%w: stream requires command_id and non-negative after_sequence", ErrInvalidFrame)
	}
	query := url.Values{}
	query.Set("controller_type", string(controller.Type()))
	query.Set("controller_id", string(controller.ID()))
	query.Set("after", strconv.FormatInt(payload.AfterSequence, 10))
	if payload.Follow {
		query.Set("follow", "true")
	}
	target := f.baseURL + "/internal/v1/commands/" + url.PathEscape(payload.CommandID) + "/events?" + query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%w: private event request: %v", ErrPrivateResponse, err)
	}
	response, err := f.client.Do(httpRequest)
	if err != nil {
		return send(transportUncertainReply(request.RequestID, err))
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return send(privateErrorReply(request.RequestID, response.StatusCode, readPrivateErrorBody(response.Body, f.maxResponseBytes)))
	}
	reader := bufio.NewReader(response.Body)
	lastSequence := payload.AfterSequence
	for {
		line, readErr := readBoundedLine(reader, maxBridgeEventLineBytes)
		if len(line) != 0 {
			event, decodeErr := decodePrivateEvent(line)
			if decodeErr != nil {
				return decodeErr
			}
			if event.CommandID != payload.CommandID || event.Sequence != lastSequence+1 {
				return send(eventHistoryUnavailableReply(request.RequestID, payload.CommandID, lastSequence, "private event sequence is not contiguous"))
			}
			frame, frameErr := bridgeEventReply(request.RequestID, event)
			if frameErr != nil {
				return frameErr
			}
			if err := send(frame); err != nil {
				return err
			}
			lastSequence = event.Sequence
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("%w: event stream read: %v", ErrPrivateResponse, readErr)
		}
	}
	return send(streamEndReply(request.RequestID, lastSequence))
}

func decodePrivateEvent(raw []byte) (privateCommandEvent, error) {
	var event privateCommandEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return privateCommandEvent{}, fmt.Errorf("%w: event JSON: %v", ErrPrivateResponse, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return privateCommandEvent{}, fmt.Errorf("%w: event trailing JSON", ErrPrivateResponse)
	}
	if strings.TrimSpace(event.CommandID) == "" || event.Sequence < 1 || strings.TrimSpace(event.Type) == "" || event.OccurredAt.IsZero() {
		return privateCommandEvent{}, fmt.Errorf("%w: event envelope is incomplete", ErrPrivateResponse)
	}
	if event.ByteCount < 0 {
		return privateCommandEvent{}, fmt.Errorf("%w: event byte count is negative", ErrPrivateResponse)
	}
	if event.Type == "stdout" || event.Type == "stderr" {
		if event.DataBase64 == "" {
			return privateCommandEvent{}, fmt.Errorf("%w: output event has no data", ErrPrivateResponse)
		}
		data, err := base64.StdEncoding.DecodeString(event.DataBase64)
		if err != nil || len(data) > bridgeMaxOutputChunkBytes || int64(len(data)) != event.ByteCount {
			return privateCommandEvent{}, fmt.Errorf("%w: output event exceeds chunk or byte-count bounds", ErrPrivateResponse)
		}
	} else if event.DataBase64 != "" || event.ByteCount != 0 {
		return privateCommandEvent{}, fmt.Errorf("%w: non-output event carries output bytes", ErrPrivateResponse)
	}
	return event, nil
}

func bridgeEventReply(requestID string, event privateCommandEvent) (ReplyFrame, error) {
	payload := map[string]any{
		"command_id": event.CommandID, "sequence": event.Sequence, "type": event.Type,
		"timestamp": event.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	if event.Type == "stdout" || event.Type == "stderr" {
		payload["encoding"] = "base64"
		payload["data_base64"] = event.DataBase64
		payload["byte_count"] = event.ByteCount
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: event reply: %v", ErrPrivateResponse, err)
	}
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "event", Payload: raw}, nil
}

func streamEndReply(requestID string, lastSequence int64) ReplyFrame {
	payload, _ := json.Marshal(map[string]int64{"last_sequence": lastSequence})
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "stream_end", Payload: payload}
}

func eventHistoryUnavailableReply(requestID, resourceID string, lastSequence int64, message string) ReplyFrame {
	payload, _ := json.Marshal(ErrorPayload{Code: "event_history_unavailable", Message: message, Retryable: false, ResourceID: resourceID, Details: map[string]any{"last_sequence": lastSequence}})
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "error", Payload: payload}
}

func readPrivateErrorBody(body io.Reader, limit int64) []byte {
	if limit <= 0 {
		limit = defaultPrivateResponseBytes
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil
	}
	return data
}

func readBoundedLine(reader *bufio.Reader, limit int64) ([]byte, error) {
	line := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) != 0 {
			if int64(len(line)+len(part)) > limit+1 {
				return nil, fmt.Errorf("%w: event line exceeds %d bytes", ErrPrivateResponse, limit)
			}
			line = append(line, part...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, io.EOF
		}
		if err != nil {
			return nil, err
		}
		return line[:len(line)-1], nil
	}
}

func decodeBridgePayload[T any](raw json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("%w: payload: %v", ErrInvalidFrame, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return value, fmt.Errorf("%w: payload contains multiple JSON values", ErrInvalidFrame)
		}
		return value, fmt.Errorf("%w: payload trailing JSON: %v", ErrInvalidFrame, err)
	}
	return value, nil
}

func (f *RunnerdForwarder) doJSON(ctx context.Context, requestID, method, route string, query url.Values, body []byte) (ReplyFrame, error) {
	target := f.baseURL + route
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: build request: %v", ErrForwarderConfiguration, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := f.client.Do(req)
	if err != nil {
		return transportUncertainReply(requestID, err), nil
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, f.maxResponseBytes+1))
	if err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: read response: %v", ErrPrivateResponse, err)
	}
	if int64(len(responseBody)) > f.maxResponseBytes {
		return ReplyFrame{}, fmt.Errorf("%w: response exceeds %d bytes", ErrPrivateResponse, f.maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return privateErrorReply(requestID, response.StatusCode, responseBody), nil
	}
	if err := validateJSONObject(responseBody); err != nil {
		return ReplyFrame{}, fmt.Errorf("%w: %v", ErrPrivateResponse, err)
	}
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "result", Payload: json.RawMessage(responseBody)}, nil
}

func validateJSONObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil || value == nil {
		return fmt.Errorf("response must be a JSON object: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func transportUncertainReply(requestID string, err error) ReplyFrame {
	payload, _ := json.Marshal(ErrorPayload{Code: "transport_uncertain", Message: "private runnerd transport outcome is unknown", Retryable: true, Details: map[string]any{"cause": err.Error()}})
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "error", Payload: payload}
}

func privateErrorReply(requestID string, status int, raw []byte) ReplyFrame {
	code := "invalid_request"
	retryable := false
	switch status {
	case http.StatusNotFound:
		code = "resource_not_found"
	case http.StatusForbidden:
		code = "controller_mismatch"
	case http.StatusConflict:
		code = "idempotency_conflict"
	case http.StatusServiceUnavailable:
		code = "runtime_unavailable"
		retryable = true
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		code = "transport_uncertain"
		retryable = true
	case http.StatusGone, http.StatusRequestedRangeNotSatisfiable:
		code = "event_history_unavailable"
	}
	message := fmt.Sprintf("runnerd private API returned HTTP %d", status)
	var private map[string]string
	if json.Unmarshal(raw, &private) == nil && strings.TrimSpace(private["error"]) != "" {
		message = private["error"]
	}
	payload, _ := json.Marshal(ErrorPayload{Code: code, Message: message, Retryable: retryable})
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "error", Payload: payload}
}
