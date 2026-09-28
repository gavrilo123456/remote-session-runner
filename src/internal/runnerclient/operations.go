package runnerclient

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CreateSession accepts a session request on this client's selected ingress.
func (c *Client) CreateSession(ctx context.Context, request CreateSessionRequest, idempotencyKey string) (Acceptance, error) {
	path, err := mutationPath("/v1/sessions", idempotencyKey)
	if err != nil {
		return Acceptance{}, err
	}
	var accepted Acceptance
	if err := c.decodeJSON(ctx, http.MethodPost, path, request, idempotencyKey, http.StatusAccepted, &accepted); err != nil {
		return Acceptance{}, err
	}
	if accepted.SessionID == "" || accepted.ResourceID != accepted.SessionID {
		return Acceptance{}, fmt.Errorf("%w: session acceptance must return matching stable IDs", ErrProtocol)
	}
	return accepted, nil
}

// GetSession reads one as-of session snapshot from this client's selected
// ingress. The ID is used only in the resource path, never to select ingress.
func (c *Client) GetSession(ctx context.Context, sessionID string) (Snapshot[SessionResource], error) {
	path, err := resourcePath("/v1/sessions/", sessionID)
	if err != nil {
		return Snapshot[SessionResource]{}, err
	}
	var result Snapshot[SessionResource]
	if err := c.decodeJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, &result); err != nil {
		return Snapshot[SessionResource]{}, err
	}
	if result.Resource.SessionID != sessionID {
		return Snapshot[SessionResource]{}, fmt.Errorf("%w: session response ID differs from request", ErrProtocol)
	}
	return result, nil
}

// SubmitCommand accepts a command for the immutable target of sessionID.
func (c *Client) SubmitCommand(ctx context.Context, sessionID string, request SubmitCommandRequest, idempotencyKey string) (Acceptance, error) {
	path, err := resourcePath("/v1/sessions/", sessionID)
	if err != nil {
		return Acceptance{}, err
	}
	path, err = mutationPath(path+"/commands", idempotencyKey)
	if err != nil {
		return Acceptance{}, err
	}
	var accepted Acceptance
	if err := c.decodeJSON(ctx, http.MethodPost, path, request, idempotencyKey, http.StatusAccepted, &accepted); err != nil {
		return Acceptance{}, err
	}
	if accepted.CommandID == "" || accepted.SessionID != sessionID || accepted.ResourceID != accepted.CommandID {
		return Acceptance{}, fmt.Errorf("%w: command acceptance IDs do not match the request", ErrProtocol)
	}
	return accepted, nil
}

// GetCommand reads one as-of command snapshot from this client's selected
// ingress without changing that ingress based on the command ID.
func (c *Client) GetCommand(ctx context.Context, commandID string) (Snapshot[CommandResource], error) {
	path, err := resourcePath("/v1/commands/", commandID)
	if err != nil {
		return Snapshot[CommandResource]{}, err
	}
	var result Snapshot[CommandResource]
	if err := c.decodeJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, &result); err != nil {
		return Snapshot[CommandResource]{}, err
	}
	if result.Resource.CommandID != commandID {
		return Snapshot[CommandResource]{}, fmt.Errorf("%w: command response ID differs from request", ErrProtocol)
	}
	return result, nil
}

// CancelCommand asks the selected ingress to cancel one command. The empty
// body is accepted by both v1 adapters and the mutation is keyed explicitly.
func (c *Client) CancelCommand(ctx context.Context, commandID, idempotencyKey string) (Acceptance, error) {
	path, err := resourcePath("/v1/commands/", commandID)
	if err != nil {
		return Acceptance{}, err
	}
	path, err = mutationPath(path+"/cancel", idempotencyKey)
	if err != nil {
		return Acceptance{}, err
	}
	var accepted Acceptance
	if err := c.decodeJSON(ctx, http.MethodPost, path, nil, idempotencyKey, http.StatusAccepted, &accepted); err != nil {
		return Acceptance{}, err
	}
	if accepted.CommandID != commandID || accepted.ResourceID != commandID {
		return Acceptance{}, fmt.Errorf("%w: cancel acceptance does not identify the requested command", ErrProtocol)
	}
	return accepted, nil
}

// CloseSession requests a close under the given policy. An empty policy uses
// the documented v1 default, graceful, and is sent explicitly for parity
// across both ingress adapters.
func (c *Client) CloseSession(ctx context.Context, sessionID, policy, idempotencyKey string) (Acceptance, error) {
	path, err := resourcePath("/v1/sessions/", sessionID)
	if err != nil {
		return Acceptance{}, err
	}
	path, err = mutationPath(path, idempotencyKey)
	if err != nil {
		return Acceptance{}, err
	}
	if strings.TrimSpace(policy) == "" {
		policy = "graceful"
	}
	var accepted Acceptance
	if err := c.decodeJSON(ctx, http.MethodDelete, path, struct {
		Policy string `json:"policy"`
	}{Policy: policy}, idempotencyKey, http.StatusAccepted, &accepted); err != nil {
		return Acceptance{}, err
	}
	if accepted.SessionID != sessionID || accepted.ResourceID != sessionID {
		return Acceptance{}, fmt.Errorf("%w: close acceptance does not identify the requested session", ErrProtocol)
	}
	return accepted, nil
}

// Run accepts one one-off job on this client's selected ingress.
func (c *Client) Run(ctx context.Context, request RunJobRequest, idempotencyKey string) (Acceptance, error) {
	path, err := mutationPath("/v1/jobs", idempotencyKey)
	if err != nil {
		return Acceptance{}, err
	}
	var accepted Acceptance
	if err := c.decodeJSON(ctx, http.MethodPost, path, request, idempotencyKey, http.StatusAccepted, &accepted); err != nil {
		return Acceptance{}, err
	}
	if accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" || accepted.ResourceID != accepted.JobID {
		return Acceptance{}, fmt.Errorf("%w: job acceptance is missing stable resource IDs", ErrProtocol)
	}
	return accepted, nil
}

// GetJob reads one as-of one-off job snapshot from this client's ingress.
func (c *Client) GetJob(ctx context.Context, jobID string) (Snapshot[JobResource], error) {
	path, err := resourcePath("/v1/jobs/", jobID)
	if err != nil {
		return Snapshot[JobResource]{}, err
	}
	var result Snapshot[JobResource]
	if err := c.decodeJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, &result); err != nil {
		return Snapshot[JobResource]{}, err
	}
	if result.Resource.JobID != jobID {
		return Snapshot[JobResource]{}, fmt.Errorf("%w: job response ID differs from request", ErrProtocol)
	}
	return result, nil
}

// StreamCommandEvents opens a replay or follow stream after a contiguous
// command-event cursor. Cursor advancement occurs only after a complete,
// validated NDJSON frame is read. The stream is single-reader; callers close
// it when they stop consuming or cancel the context used to open it.
func (c *Client) StreamCommandEvents(ctx context.Context, commandID string, after int64, follow bool) (*EventStream, error) {
	if after < 0 {
		return nil, fmt.Errorf("%w: event cursor cannot be negative", ErrConfiguration)
	}
	path, err := resourcePath("/v1/commands/", commandID)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("after", fmt.Sprintf("%d", after))
	query.Set("follow", fmt.Sprintf("%t", follow))
	path += "/events?" + query.Encode()
	response, err := c.do(ctx, http.MethodGet, path, nil, "", "application/x-ndjson")
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		body, readErr := readBounded(response.Body, maxJSONBytes)
		if readErr != nil {
			if errors.Is(readErr, ErrResponseLarge) {
				return nil, fmt.Errorf("%w: %v", ErrProtocol, readErr)
			}
			return nil, fmt.Errorf("%w: read API error body: %w", ErrTransport, readErr)
		}
		return nil, decodeAPIError(response.StatusCode, body)
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/x-ndjson" {
		response.Body.Close()
		return nil, fmt.Errorf("%w: command event response content type is %q", ErrProtocol, response.Header.Get("Content-Type"))
	}
	stream := &EventStream{
		body:      response.Body,
		response:  response,
		commandID: commandID,
		cursor:    after,
	}
	stream.scanner = bufio.NewScanner(response.Body)
	stream.scanner.Buffer(make([]byte, 4096), maxJSONBytes)
	return stream, nil
}

// EventStream is a cursor-aware reader for the v1 NDJSON command-event
// interface. It is not safe for concurrent Next calls.
type EventStream struct {
	body      io.ReadCloser
	response  *http.Response
	scanner   *bufio.Scanner
	commandID string
	cursor    int64
	closed    bool
	failed    error
}

// Cursor returns the last event sequence successfully delivered to the
// caller, or the cursor supplied to StreamCommandEvents if none was delivered.
func (s *EventStream) Cursor() int64 {
	if s == nil {
		return 0
	}
	return s.cursor
}

// Next returns the next validated event. io.EOF indicates a cleanly ended
// replay or a follow stream that ended at its resumable cursor.
func (s *EventStream) Next() (Event, error) {
	if s == nil {
		return Event{}, io.EOF
	}
	if s.failed != nil {
		return Event{}, s.failed
	}
	if s.closed || s.scanner == nil {
		return Event{}, io.EOF
	}
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				return Event{}, s.fail(fmt.Errorf("%w: event frame exceeds %d bytes", ErrProtocol, maxJSONBytes))
			}
			return Event{}, s.fail(fmt.Errorf("%w: read event stream: %w", ErrTransport, err))
		}
		if err := s.validateLastSequence(); err != nil {
			return Event{}, s.fail(err)
		}
		s.Close()
		return Event{}, io.EOF
	}
	var event Event
	if err := json.Unmarshal(s.scanner.Bytes(), &event); err != nil {
		return Event{}, s.fail(fmt.Errorf("%w: decode event frame: %v", ErrProtocol, err))
	}
	if err := event.validate(s.commandID, s.cursor); err != nil {
		return Event{}, s.fail(err)
	}
	s.cursor = event.Sequence
	return event, nil
}

func (s *EventStream) fail(err error) error {
	s.failed = err
	_ = s.Close()
	return err
}

func (s *EventStream) validateLastSequence() error {
	if s.response == nil {
		return nil
	}
	last := s.response.Trailer.Get("X-Runner-Last-Sequence")
	if last == "" {
		last = s.response.Header.Get("X-Runner-Last-Sequence")
	}
	if last == "" {
		return fmt.Errorf("%w: event response omitted X-Runner-Last-Sequence", ErrProtocol)
	}
	sequence, err := strconv.ParseInt(last, 10, 64)
	if err != nil || sequence < 0 || sequence != s.cursor {
		return fmt.Errorf("%w: server event cursor %q differs from last delivered sequence %d", ErrProtocol, last, s.cursor)
	}
	return nil
}

// Close closes the underlying HTTP response body.
func (s *EventStream) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.body == nil {
		return nil
	}
	return s.body.Close()
}

// Event is one validated v1 command event. Data contains decoded stdout or
// stderr bytes; DataBase64 preserves the exact wire value.
type Event struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Ordinal    int64     `json:"ordinal,omitempty"`
	Encoding   string    `json:"encoding,omitempty"`
	DataBase64 string    `json:"data_base64,omitempty"`
	ByteCount  int64     `json:"byte_count,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Data       []byte    `json:"-"`
}

func (e *Event) validate(commandID string, cursor int64) error {
	if e.CommandID != commandID || e.Sequence < 1 || cursor == int64(^uint64(0)>>1) || e.Sequence != cursor+1 || e.Type == "" || e.Timestamp.IsZero() {
		return fmt.Errorf("%w: event identity, timestamp, or sequence does not continue command %q after %d", ErrProtocol, commandID, cursor)
	}
	e.Timestamp = e.Timestamp.UTC()
	switch e.Type {
	case "command_queued", "command_started", "output_truncated", "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		if e.Encoding != "" || e.DataBase64 != "" || e.ByteCount != 0 {
			return fmt.Errorf("%w: lifecycle event %q contains output fields", ErrProtocol, e.Type)
		}
		if e.Type == "command_queued" && (e.Sequence != 1 || e.Ordinal < 1) {
			return fmt.Errorf("%w: queued event must be first and carry its command ordinal", ErrProtocol)
		}
	case "stdout", "stderr":
		if e.Encoding != "base64" || e.DataBase64 == "" || e.ByteCount < 1 {
			return fmt.Errorf("%w: output event has incomplete base64 payload fields", ErrProtocol)
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(e.DataBase64)
		if err != nil || int64(len(decoded)) != e.ByteCount {
			return fmt.Errorf("%w: output event base64 or byte_count is invalid", ErrProtocol)
		}
		e.Data = decoded
	default:
		return fmt.Errorf("%w: unknown command event type %q", ErrProtocol, e.Type)
	}
	return nil
}

// EventHistoryDetails returns typed completeness/cursor details for a 410
// event_history_unavailable API error.
func (e *APIError) EventHistoryDetails() EventHistoryDetails {
	if e == nil || e.Code != "event_history_unavailable" {
		return EventHistoryDetails{}
	}
	return decodeEventHistoryDetails(e.Details)
}
