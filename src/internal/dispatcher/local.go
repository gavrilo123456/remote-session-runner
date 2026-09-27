package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const (
	operationCreateSession = "create_session"
	operationSubmitCommand = "submit_command"
	operationCancelCommand = "cancel_command"
	operationCloseSession  = "close_session"
)

var (
	ErrDriverConfiguration       = errors.New("local dispatcher configuration is invalid")
	ErrNoLocalDispatchWork       = errors.New("no eligible local dispatch work")
	ErrUnsupportedLocalOperation = errors.New("local operation is not supported by runner-locald")
	ErrLocaldRejected            = errors.New("runner-locald rejected local intent")
	ErrLocaldTransport           = errors.New("runner-locald transport failed")
	ErrLocaldResponse            = errors.New("runner-locald returned an invalid response")
)

// AcceptIntentRequest is the complete identity-only request sent to locald.
// PayloadJSON and ScriptBytes are deliberately absent: locald reloads both
// from the committed intent in the shared local SQLite store.
type AcceptIntentRequest struct {
	IntentID      domain.IntentID
	RequestHash   domain.CanonicalHash
	IntentOrdinal *int64
}

// IntentAcceptance is the target-authority acknowledgement returned by locald.
type IntentAcceptance struct {
	IntentID        string           `json:"intent_id"`
	Operation       string           `json:"operation"`
	ResourceID      string           `json:"resource_id"`
	AcceptanceScope string           `json:"acceptance_scope"`
	ExecutionTarget TargetAcceptance `json:"execution_target"`
	SessionState    string           `json:"session_state,omitempty"`
	CommandState    string           `json:"command_state,omitempty"`
	JobPhase        string           `json:"job_phase,omitempty"`
	Duplicate       bool             `json:"duplicate,omitempty"`
	ObservedAt      time.Time        `json:"observed_at"`
}

// LocaldRejectionError preserves the private API's structured rejection for
// internal diagnosis. Error intentionally omits Message so callers do not
// accidentally copy executor details into general logs.
type LocaldRejectionError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *LocaldRejectionError) Error() string {
	if e == nil {
		return ErrLocaldRejected.Error()
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: status %d code %s", ErrLocaldRejected, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("%s: status %d", ErrLocaldRejected, e.StatusCode)
}

func (e *LocaldRejectionError) Unwrap() error { return ErrLocaldRejected }

// TargetAcceptance identifies the target that acknowledged an intent.
type TargetAcceptance struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

// IntentAcceptor is implemented by the owner-only locald client and by
// hermetic test doubles. Implementations must use only identity fields from
// AcceptIntentRequest.
type IntentAcceptor interface {
	AcceptIntent(context.Context, AcceptIntentRequest) (IntentAcceptance, error)
}

// LocalDriver claims and dispatches local intents to runner-locald.
type LocalDriver struct {
	authority     *store.AuthorityStore
	acceptor      IntentAcceptor
	owner         string
	leaseDuration time.Duration
}

// NewLocalDriver constructs a lease-owning local driver.
func NewLocalDriver(authority *store.AuthorityStore, acceptor IntentAcceptor, owner string, leaseDuration time.Duration) (*LocalDriver, error) {
	if authority == nil || acceptor == nil || owner == "" || len(owner) > 256 || strings.IndexByte(owner, 0) >= 0 || leaseDuration <= 0 {
		return nil, ErrDriverConfiguration
	}
	return &LocalDriver{authority: authority, acceptor: acceptor, owner: owner, leaseDuration: leaseDuration}, nil
}

// DispatchNext selects the earliest eligible supported local intent, claims it
// under the driver's lease, and sends only its identity/hash to locald.
func (d *LocalDriver) DispatchNext(ctx context.Context) (store.LocalIntentRecord, IntentAcceptance, error) {
	if d == nil || d.authority == nil || d.acceptor == nil {
		return store.LocalIntentRecord{}, IntentAcceptance{}, ErrDriverConfiguration
	}
	candidates, err := d.authority.ListEligibleLocalIntents(ctx, 1000)
	if err != nil {
		return store.LocalIntentRecord{}, IntentAcceptance{}, err
	}
	for _, candidate := range candidates {
		if candidate.Target.Kind() != domain.TargetKindLocal || !supportedLocalOperation(candidate.Operation) {
			continue
		}
		return d.dispatchClaimed(ctx, candidate.IntentID)
	}
	return store.LocalIntentRecord{}, IntentAcceptance{}, ErrNoLocalDispatchWork
}

// DispatchIntent claims and dispatches one known local intent by stable ID.
// The intent is reloaded before claiming so immutable hash and target data are
// always taken from SQLite rather than caller-supplied payload.
func (d *LocalDriver) DispatchIntent(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, IntentAcceptance, error) {
	if d == nil || d.authority == nil || d.acceptor == nil {
		return store.LocalIntentRecord{}, IntentAcceptance{}, ErrDriverConfiguration
	}
	intent, err := d.authority.GetLocalIntent(ctx, id)
	if err != nil {
		return store.LocalIntentRecord{}, IntentAcceptance{}, err
	}
	if intent.Target.Kind() != domain.TargetKindLocal {
		return store.LocalIntentRecord{}, IntentAcceptance{}, fmt.Errorf("%w: target %s", ErrUnsupportedLocalOperation, intent.Target.Kind())
	}
	if !supportedLocalOperation(intent.Operation) {
		return store.LocalIntentRecord{}, IntentAcceptance{}, fmt.Errorf("%w: %s", ErrUnsupportedLocalOperation, intent.Operation)
	}
	return d.dispatchClaimed(ctx, intent.IntentID)
}

func (d *LocalDriver) dispatchClaimed(ctx context.Context, id domain.IntentID) (store.LocalIntentRecord, IntentAcceptance, error) {
	claimed, err := d.authority.ClaimLocalIntent(ctx, id, d.owner, d.leaseDuration)
	if err != nil {
		return store.LocalIntentRecord{}, IntentAcceptance{}, err
	}
	request := AcceptIntentRequest{IntentID: claimed.IntentID, RequestHash: claimed.RequestHash, IntentOrdinal: claimed.IntentOrdinal}
	if claimed.Operation != operationSubmitCommand {
		request.IntentOrdinal = nil
	}
	acceptance, err := d.acceptor.AcceptIntent(ctx, request)
	if err != nil {
		next := store.LocalIntentUncertain
		reason := "locald_acceptance_uncertain"
		if errors.Is(err, ErrLocaldRejected) {
			next = store.LocalIntentNotDelivered
			reason = "locald_rejected"
		}
		// A transport or malformed reply cannot prove that locald did not
		// accept the idempotent intent, so retain the conservative uncertain state.
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, next, reason)
		if transitionErr != nil {
			return store.LocalIntentRecord{}, IntentAcceptance{}, fmt.Errorf("%w; transition uncertainty: %v", err, transitionErr)
		}
		return claimed, IntentAcceptance{}, err
	}
	if err := validateAcceptance(claimed, acceptance); err != nil {
		_, transitionErr := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, store.LocalIntentUncertain, "locald_response_uncertain")
		if transitionErr != nil {
			return store.LocalIntentRecord{}, IntentAcceptance{}, fmt.Errorf("%w; transition uncertainty: %v", err, transitionErr)
		}
		return claimed, IntentAcceptance{}, err
	}
	accepted, err := d.authority.TransitionLocalIntent(ctx, claimed.IntentID, store.LocalIntentAccepted, "locald_target_accepted")
	if err != nil {
		return store.LocalIntentRecord{}, IntentAcceptance{}, err
	}
	return accepted, acceptance, nil
}

func supportedLocalOperation(operation string) bool {
	switch operation {
	case operationCreateSession, operationSubmitCommand, operationCancelCommand, operationCloseSession, "run":
		return true
	default:
		return false
	}
}

func validateAcceptance(intent store.LocalIntentRecord, acceptance IntentAcceptance) error {
	if acceptance.IntentID != string(intent.IntentID) || acceptance.Operation != intent.Operation || acceptance.ResourceID != intent.ResourceID {
		return fmt.Errorf("%w: identity mismatch", ErrLocaldResponse)
	}
	if acceptance.AcceptanceScope != "target_authority" || acceptance.ExecutionTarget.Kind != string(domain.TargetKindLocal) {
		return fmt.Errorf("%w: target acceptance required", ErrLocaldResponse)
	}
	return nil
}

// LocaldClient calls locald's owner-only Unix API. It never sends the
// immutable request payload or script bytes.
type LocaldClient struct {
	client       *http.Client
	baseURL      string
	maxBodyBytes int64
}

// NewLocaldClient creates an identity-only client for an absolute Unix socket.
func NewLocaldClient(socketPath string) (*LocaldClient, error) {
	if socketPath == "" || !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return nil, fmt.Errorf("%w: invalid locald socket path", ErrDriverConfiguration)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	return NewLocaldClientWithHTTP(&http.Client{Transport: transport})
}

// NewLocaldClientWithHTTP constructs a locald client around an injected HTTP
// client, primarily for bounded protocol tests.
func NewLocaldClientWithHTTP(client *http.Client) (*LocaldClient, error) {
	if client == nil {
		return nil, ErrDriverConfiguration
	}
	return &LocaldClient{client: client, baseURL: "http://locald", maxBodyBytes: domain.MaxSerializedRequestBytes}, nil
}

func (c *LocaldClient) AcceptIntent(ctx context.Context, input AcceptIntentRequest) (IntentAcceptance, error) {
	if c == nil || c.client == nil {
		return IntentAcceptance{}, ErrDriverConfiguration
	}
	if _, err := domain.NewIntentID(string(input.IntentID)); err != nil {
		return IntentAcceptance{}, fmt.Errorf("%w: intent ID", ErrDriverConfiguration)
	}
	if input.RequestHash.Version() == 0 {
		return IntentAcceptance{}, fmt.Errorf("%w: request hash", ErrDriverConfiguration)
	}
	body := struct {
		IntentID      string `json:"intent_id"`
		RequestHash   string `json:"request_hash"`
		IntentOrdinal *int64 `json:"intent_ordinal,omitempty"`
	}{IntentID: string(input.IntentID), RequestHash: input.RequestHash.String(), IntentOrdinal: input.IntentOrdinal}
	serialized, err := json.Marshal(body)
	if err != nil || len(serialized) > int(c.maxBodyBytes) {
		return IntentAcceptance{}, fmt.Errorf("%w: request body", ErrDriverConfiguration)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/v1/accept-intent", bytes.NewReader(serialized))
	if err != nil {
		return IntentAcceptance{}, fmt.Errorf("%w: request: %v", ErrLocaldRejected, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return IntentAcceptance{}, fmt.Errorf("%w: %v", ErrLocaldTransport, err)
	}
	defer response.Body.Close()
	if response.Body == nil {
		return IntentAcceptance{}, fmt.Errorf("%w: empty response body", ErrLocaldResponse)
	}
	reply, err := io.ReadAll(io.LimitReader(response.Body, c.maxBodyBytes+1))
	if err != nil || int64(len(reply)) > c.maxBodyBytes {
		return IntentAcceptance{}, fmt.Errorf("%w: bounded response", ErrLocaldResponse)
	}
	if response.StatusCode != http.StatusAccepted {
		var envelope struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(reply, &envelope); err == nil {
			return IntentAcceptance{}, &LocaldRejectionError{StatusCode: response.StatusCode, Code: safeLocaldErrorCode(envelope.Code), Message: envelope.Message}
		}
		return IntentAcceptance{}, fmt.Errorf("%w: status %d", ErrLocaldRejected, response.StatusCode)
	}
	var acceptance IntentAcceptance
	decoder := json.NewDecoder(bytes.NewReader(reply))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&acceptance); err != nil {
		return IntentAcceptance{}, fmt.Errorf("%w: decode: %v", ErrLocaldResponse, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return IntentAcceptance{}, fmt.Errorf("%w: trailing response data", ErrLocaldResponse)
	}
	return acceptance, nil
}

func safeLocaldErrorCode(code string) string {
	if code == "" || len(code) > 64 {
		return ""
	}
	for _, char := range code {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return ""
		}
	}
	return code
}
