// Package runnerclient provides the shared HTTP/JSON v1 client used by the
// CLI. A Client is bound to one explicitly selected ingress at construction;
// resource IDs never select or change its endpoint.
package runnerclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const maxJSONBytes = 1 << 20

// EndpointKind identifies the explicitly configured ingress for a Client.
type EndpointKind string

const (
	EndpointUnixSocket EndpointKind = "unix_socket"
	EndpointHTTPS      EndpointKind = "https"
)

var (
	ErrConfiguration = errors.New("runner client configuration is invalid")
	ErrRequest       = errors.New("runner API request is invalid")
	ErrRequestLarge  = errors.New("runner API request exceeds the v1 size limit")
	ErrTransport     = errors.New("runner API transport failed")
	ErrProtocol      = errors.New("runner API response violates the v1 contract")
	ErrResponseLarge = errors.New("runner API response exceeds the client size limit")
	ErrResourceID    = errors.New("runner resource ID is invalid for a v1 path")
	ErrIdempotency   = errors.New("runner mutation idempotency key is invalid")
)

// HTTPSConfig contains all inputs needed for authenticated direct HTTPS.
// Roots and a client certificate are mandatory; the client never reads
// credentials from environment variables or falls back to another endpoint.
type HTTPSConfig struct {
	Endpoint          string
	RootCAs           *x509.CertPool
	ClientCertificate tls.Certificate
}

// Client is a reusable API client bound to exactly one ingress. Construct it
// with NewUnixSocketClient or NewHTTPSClient and share it for request use.
type Client struct {
	endpointURL string
	kind        EndpointKind
	httpClient  *http.Client
}

// NewUnixSocketClient binds a client to the given absolute Unix socket path.
func NewUnixSocketClient(socketPath string) (*Client, error) {
	if socketPath == "" || !filepath.IsAbs(socketPath) || strings.IndexByte(socketPath, 0) >= 0 {
		return nil, fmt.Errorf("%w: Unix socket path must be absolute", ErrConfiguration)
	}
	if filepath.Clean(socketPath) != socketPath {
		return nil, fmt.Errorf("%w: Unix socket path must be clean", ErrConfiguration)
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return newClient("http://runner.local", EndpointUnixSocket, transport), nil
}

// NewHTTPSClient binds a client to one HTTPS endpoint and requires explicit
// trust roots and a client certificate. TLS 1.3 is enforced as the minimum.
func NewHTTPSClient(config HTTPSConfig) (*Client, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint == nil || endpoint.Scheme != "https" || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") {
		return nil, fmt.Errorf("%w: HTTPS endpoint must be an origin URL without credentials or a path", ErrConfiguration)
	}
	if config.RootCAs == nil {
		return nil, fmt.Errorf("%w: HTTPS root CA pool is required", ErrConfiguration)
	}
	if len(config.ClientCertificate.Certificate) == 0 || config.ClientCertificate.PrivateKey == nil {
		return nil, fmt.Errorf("%w: HTTPS client certificate and private key are required", ErrConfiguration)
	}
	endpointURL := "https://" + endpoint.Host
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      config.RootCAs.Clone(),
		Certificates: []tls.Certificate{config.ClientCertificate},
	}
	transport := &http.Transport{
		Proxy:             nil,
		TLSClientConfig:   tlsConfig,
		ForceAttemptHTTP2: true,
	}
	return newClient(endpointURL, EndpointHTTPS, transport), nil
}

func newClient(endpointURL string, kind EndpointKind, transport *http.Transport) *Client {
	return &Client{
		endpointURL: endpointURL,
		kind:        kind,
		httpClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// EndpointKind reports the ingress permanently selected for this client.
func (c *Client) EndpointKind() EndpointKind {
	if c == nil {
		return ""
	}
	return c.kind
}

// CloseIdleConnections releases pooled connections owned by this client.
func (c *Client) CloseIdleConnections() {
	if c != nil && c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
}

func (c *Client) do(ctx context.Context, method, requestPath string, body any, idempotencyKey string, accept string) (*http.Response, error) {
	if c == nil || c.httpClient == nil || c.endpointURL == "" {
		return nil, ErrConfiguration
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: request context is nil", ErrConfiguration)
	}
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%w: encode request: %v", ErrRequest, err)
		}
		if len(encoded) > maxJSONBytes {
			return nil, fmt.Errorf("%w: request exceeds %d bytes", ErrRequestLarge, maxJSONBytes)
		}
		bodyReader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpointURL+requestPath, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrConfiguration, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if idempotencyKey != "" {
		if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 || strings.IndexByte(idempotencyKey, 0) >= 0 {
			return nil, ErrIdempotency
		}
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: %w", ErrTransport, method, requestPath, err)
	}
	return response, nil
}

func (c *Client) decodeJSON(ctx context.Context, method, requestPath string, requestBody any, key string, wantStatus int, result any) error {
	response, err := c.do(ctx, method, requestPath, requestBody, key, "application/json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxJSONBytes)
	if err != nil {
		if errors.Is(err, ErrResponseLarge) {
			return fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		return fmt.Errorf("%w: read response body: %w", ErrTransport, err)
	}
	if response.StatusCode != wantStatus {
		return decodeAPIError(response.StatusCode, body)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("%w: empty JSON response", ErrProtocol)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("%w: decode HTTP %d JSON: %v", ErrProtocol, response.StatusCode, err)
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrResponseLarge, limit)
	}
	return contents, nil
}

func decodeAPIError(status int, body []byte) error {
	apiError := &APIError{StatusCode: status, Code: "http_error", Message: http.StatusText(status)}
	if len(body) > 0 {
		var envelope struct {
			Code       string          `json:"code"`
			Message    string          `json:"message"`
			Retryable  bool            `json:"retryable"`
			ResourceID string          `json:"resource_id"`
			Details    json.RawMessage `json:"details"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if envelope.Code != "" {
				apiError.Code = envelope.Code
			}
			if envelope.Message != "" {
				apiError.Message = envelope.Message
			}
			apiError.Retryable = envelope.Retryable
			apiError.ResourceID = envelope.ResourceID
			if len(envelope.Details) != 0 && string(envelope.Details) != "null" {
				apiError.Details = append(json.RawMessage(nil), envelope.Details...)
			}
		} else {
			apiError.Details = append(json.RawMessage(nil), body...)
		}
	}
	return apiError
}

func validateResourceID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") || strings.IndexByte(id, 0) >= 0 {
		return fmt.Errorf("%w: IDs must be nonempty single path segments", ErrResourceID)
	}
	return nil
}

func resourcePath(prefix, id string) (string, error) {
	if err := validateResourceID(id); err != nil {
		return "", err
	}
	return prefix + url.PathEscape(id), nil
}

func mutationPath(path, key string) (string, error) {
	if strings.TrimSpace(key) == "" || len(key) > 256 || strings.IndexByte(key, 0) >= 0 {
		return "", ErrIdempotency
	}
	return path, nil
}

// APIError is a structured non-success HTTP response from a Runner API.
// Details retains the original JSON object so callers can inspect versioned
// fields such as event-history completeness and earliest available sequence.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Retryable  bool
	ResourceID string
	Details    json.RawMessage
}

func (e *APIError) Error() string {
	if e == nil {
		return "runner API error"
	}
	return fmt.Sprintf("runner API HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// Source identifies the source mode and optional repository/revision/path
// metadata on a resource snapshot.
type Source struct {
	Mode              string `json:"mode,omitempty"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	ResolvedCommit    string `json:"resolved_commit,omitempty"`
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

// Target is the API representation of an immutable execution target.
type Target struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

// Controller is the API representation of the ingress identity that owns a
// resource. Local and direct APIs use the same field names.
type Controller struct {
	Type string `json:"controller_type,omitempty"`
	ID   string `json:"controller_id,omitempty"`
}

// Capabilities reports selected host facts without claiming host isolation.
type Capabilities struct {
	HostClass        string         `json:"host_class,omitempty"`
	Isolation        string         `json:"isolation,omitempty"`
	EffectiveAccount string         `json:"effective_account,omitempty"`
	ServiceLimits    map[string]any `json:"service_limits,omitempty"`
}

// KnownState carries only state known at acceptance time. Empty fields mean
// that this ingress accepted an intent but has no authoritative state yet.
type KnownState struct {
	DeliveryState string `json:"delivery_state,omitempty"`
	SessionState  string `json:"session_state,omitempty"`
	CommandState  string `json:"command_state,omitempty"`
}

// Acceptance is returned after a mutation has been durably accepted.
type Acceptance struct {
	ResourceID         string     `json:"resource_id"`
	SessionID          string     `json:"session_id,omitempty"`
	CommandID          string     `json:"command_id,omitempty"`
	JobID              string     `json:"job_id,omitempty"`
	IntentID           string     `json:"intent_id,omitempty"`
	AcceptanceScope    string     `json:"acceptance_scope"`
	ExecutionTarget    Target     `json:"execution_target"`
	KnownState         KnownState `json:"known_state"`
	IdempotencyWarning string     `json:"idempotency_warning,omitempty"`
}

const IdempotencyWarningDeduplicationNotGuaranteed = "deduplication_not_guaranteed"

// Snapshot wraps an as-of resource view returned by either ingress.
type Snapshot[T any] struct {
	View     string `json:"view"`
	IsStale  bool   `json:"is_stale"`
	Resource T      `json:"resource"`
}

// SessionResource is the typed common superset of local intent, local or
// remote projection, and target-authority session snapshots.
type SessionResource struct {
	SessionID         string       `json:"session_id"`
	SessionState      string       `json:"session_state,omitempty"`
	DeliveryState     string       `json:"delivery_state,omitempty"`
	Reason            string       `json:"reason,omitempty"`
	ExecutionTarget   Target       `json:"execution_target"`
	Authority         string       `json:"authority,omitempty"`
	Controller        Controller   `json:"controller"`
	ObservedAt        time.Time    `json:"observed_at"`
	Environment       string       `json:"environment,omitempty"`
	Source            Source       `json:"source,omitempty"`
	Capabilities      Capabilities `json:"capabilities,omitempty"`
	RuntimeGeneration string       `json:"runtime_generation,omitempty"`
	ResolvedRevision  string       `json:"resolved_revision,omitempty"`
	IsStale           bool         `json:"is_stale,omitempty"`
}

// CommandResource is the typed common superset of command intent and
// authority/projection snapshots.
type CommandResource struct {
	CommandID               string       `json:"command_id"`
	SessionID               string       `json:"session_id"`
	Ordinal                 int64        `json:"ordinal,omitempty"`
	CommandState            string       `json:"command_state,omitempty"`
	DeliveryState           string       `json:"delivery_state,omitempty"`
	Reason                  string       `json:"reason,omitempty"`
	ExitCode                *int         `json:"exit_code,omitempty"`
	FinalEventSequence      *int64       `json:"final_event_sequence,omitempty"`
	OutputComplete          bool         `json:"output_complete,omitempty"`
	OutputTruncated         bool         `json:"output_truncated,omitempty"`
	OutputUnavailableReason string       `json:"output_unavailable_reason,omitempty"`
	ExecutionTarget         Target       `json:"execution_target"`
	Authority               string       `json:"authority,omitempty"`
	Controller              Controller   `json:"controller"`
	ObservedAt              time.Time    `json:"observed_at"`
	Environment             string       `json:"environment,omitempty"`
	Source                  Source       `json:"source,omitempty"`
	Capabilities            Capabilities `json:"capabilities,omitempty"`
	IsStale                 bool         `json:"is_stale,omitempty"`
}

// JobResource carries stable job/session/command IDs and the state fields
// present in both queued local-intent and direct-authority responses.
type JobResource struct {
	JobID                   string       `json:"job_id"`
	SessionID               string       `json:"session_id"`
	CommandID               string       `json:"command_id"`
	Phase                   string       `json:"phase,omitempty"`
	JobPhase                string       `json:"job_phase,omitempty"`
	CommandState            *string      `json:"command_state,omitempty"`
	ExitCode                *int         `json:"exit_code,omitempty"`
	FinalEventSequence      *int64       `json:"final_event_sequence,omitempty"`
	OutputComplete          bool         `json:"output_complete,omitempty"`
	OutputTruncated         bool         `json:"output_truncated,omitempty"`
	OutputUnavailableReason string       `json:"output_unavailable_reason,omitempty"`
	TeardownState           string       `json:"teardown_state,omitempty"`
	TeardownReason          string       `json:"teardown_reason,omitempty"`
	DeliveryState           string       `json:"delivery_state,omitempty"`
	Reason                  string       `json:"reason,omitempty"`
	ExecutionTarget         Target       `json:"execution_target"`
	Authority               string       `json:"authority,omitempty"`
	Controller              Controller   `json:"controller"`
	ObservedAt              time.Time    `json:"observed_at"`
	Environment             string       `json:"environment,omitempty"`
	Source                  Source       `json:"source,omitempty"`
	Capabilities            Capabilities `json:"capabilities,omitempty"`
	IsStale                 bool         `json:"is_stale,omitempty"`
}

// EffectivePhase normalizes the one local/HTTPS spelling difference.
func (r JobResource) EffectivePhase() string {
	if r.Phase != "" {
		return r.Phase
	}
	return r.JobPhase
}

// CreateSessionRequest names a target once; commands submitted later inherit
// this immutable selection. Optional source/limits/policy values are JSON
// objects so the client can preserve the versioned API schema.
type CreateSessionRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget Target          `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

// SubmitCommandRequest submits a UTF-8 script to the target already selected
// for the session.
type SubmitCommandRequest struct {
	Script         string `json:"script"`
	TimeoutSeconds *int64 `json:"timeout_seconds,omitempty"`
}

// RunJobRequest asks an ingress to perform the one-off create/execute/teardown
// lifecycle while retaining stable job/session/command identifiers.
type RunJobRequest struct {
	Environment     string          `json:"environment"`
	ExecutionTarget Target          `json:"execution_target"`
	Source          json.RawMessage `json:"source,omitempty"`
	Script          string          `json:"script"`
	TimeoutSeconds  *int64          `json:"timeout_seconds,omitempty"`
	Limits          json.RawMessage `json:"limits,omitempty"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

// EventHistoryDetails contains the stable common event-gap details.
type EventHistoryDetails struct {
	OutputComplete          *bool  `json:"output_complete,omitempty"`
	OutputUnavailableReason string `json:"output_unavailable_reason,omitempty"`
	EarliestAvailable       *int64 `json:"earliest_available_sequence,omitempty"`
}

func decodeEventHistoryDetails(raw json.RawMessage) EventHistoryDetails {
	var details EventHistoryDetails
	if len(raw) != 0 {
		_ = json.Unmarshal(raw, &details)
	}
	return details
}
