package sshbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"remote-session-runner/src/internal/domain"
)

const defaultPrivateResponseBytes int64 = domain.MaxSerializedRequestBytes

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

// RunnerdForwarder forwards the P050 session subset to runnerd.
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

// Handle implements RequestHandler for the P050 create/read session subset.
func (f *RunnerdForwarder) Handle(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	if f == nil || f.client == nil {
		return ReplyFrame{}, ErrForwarderConfiguration
	}
	switch request.Operation {
	case OperationCreateOrResumeSession:
		return f.createSession(ctx, controller, request)
	case OperationGetSession:
		return f.getSession(ctx, controller, request)
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
	}
	message := fmt.Sprintf("runnerd private API returned HTTP %d", status)
	var private map[string]string
	if json.Unmarshal(raw, &private) == nil && strings.TrimSpace(private["error"]) != "" {
		message = private["error"]
	}
	payload, _ := json.Marshal(ErrorPayload{Code: code, Message: message, Retryable: retryable})
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "error", Payload: payload}
}
