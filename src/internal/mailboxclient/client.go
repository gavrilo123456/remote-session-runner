// Package mailboxclient implements the file-only mailbox client. It has no
// dependency on the Runner API, CLI, dispatcher, or execution packages.
package mailboxclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxRequestBytes = 1 << 20
	maxEventLine    = 1 << 20
	fileMode        = 0o600
	directoryMode   = 0o700
)

var (
	ErrConfiguration  = errors.New("mailbox client configuration is invalid")
	ErrRequest        = errors.New("mailbox request file is invalid")
	ErrResponse       = errors.New("mailbox response file is invalid")
	ErrDiagnostic     = errors.New("mailbox ingress diagnostic file is invalid")
	ErrEvents         = errors.New("mailbox event file is invalid")
	ErrAcknowledgment = errors.New("mailbox acknowledgment is invalid")
	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Client struct {
	root string
}

// ResponseTarget is the resolved immutable target included in new-work
// mailbox responses. It is deliberately a data-only view of the server's
// existing response field; it cannot select or alter execution.
type ResponseTarget struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

// ResponseError is the structured terminal diagnostic carried by a mailbox
// response. It lets file-only clients distinguish an accepted request whose
// target result could not be verified from a normal completed command.
type ResponseError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Response struct {
	InboxID                  string          `json:"inbox_id,omitempty"`
	RequestID                string          `json:"request_id"`
	Operation                string          `json:"operation"`
	RequestState             string          `json:"request_state"`
	ResponseRevision         int64           `json:"response_revision"`
	JobID                    string          `json:"job_id,omitempty"`
	JobPhase                 string          `json:"job_phase,omitempty"`
	SessionID                string          `json:"session_id,omitempty"`
	CommandID                string          `json:"command_id,omitempty"`
	DeliveryState            string          `json:"delivery_state,omitempty"`
	CommandState             string          `json:"command_state,omitempty"`
	QueueBlockedReason       string          `json:"queue_blocked_reason,omitempty"`
	ExitCode                 *int            `json:"exit_code,omitempty"`
	Stdout                   string          `json:"stdout,omitempty"`
	Stderr                   string          `json:"stderr,omitempty"`
	FinalEventSequence       *int64          `json:"final_event_sequence,omitempty"`
	AvailableEventSequence   *int64          `json:"available_event_sequence,omitempty"`
	OutputComplete           *bool           `json:"output_complete,omitempty"`
	OutputTruncated          *bool           `json:"output_truncated,omitempty"`
	OutputUnavailableReason  string          `json:"output_unavailable_reason,omitempty"`
	EventsFile               string          `json:"events_file,omitempty"`
	TeardownOutcome          string          `json:"teardown_outcome,omitempty"`
	ExecutionSelectionSource string          `json:"execution_selection_source,omitempty"`
	ResolvedEnvironment      string          `json:"resolved_environment,omitempty"`
	ResolvedExecutionTarget  *ResponseTarget `json:"resolved_execution_target,omitempty"`
	Error                    *ResponseError  `json:"error,omitempty"`
}

// Diagnostic is the private, read-only result of a safe mailbox input that
// failed before normal exchange acceptance. It deliberately has no operation,
// target, idempotency, response, event, or acknowledgement fields.
type Diagnostic struct {
	InboxID            string                  `json:"inbox_id"`
	RequestID          string                  `json:"request_id"`
	DiagnosticRevision int64                   `json:"diagnostic_revision"`
	LifecyclePhase     string                  `json:"lifecycle_phase"`
	Accepted           bool                    `json:"accepted"`
	Executed           bool                    `json:"executed"`
	Code               string                  `json:"code"`
	Message            string                  `json:"message"`
	SchemaDetail       *DiagnosticSchemaDetail `json:"schema_detail,omitempty"`
	ObservedAt         string                  `json:"observed_at"`
}

// DiagnosticSchemaDetail is static v1 protocol guidance attached only to an
// invalid-request-schema diagnostic. It is never copied from a submitted
// script, target, idempotency key, or another request value.
type DiagnosticSchemaDetail struct {
	SchemaVersion     string
	JSONPointer       string
	Expected          string
	ReceivedType      string
	CanonicalRunField string
	CanonicalRunType  string
	MinimalValidRun   DiagnosticMinimalValidRun
}

type DiagnosticMinimalValidRun struct {
	RequestID      string
	IdempotencyKey string
	Operation      string
	Script         string
}

type Event struct {
	CommandID  string `json:"command_id"`
	Sequence   int64  `json:"sequence"`
	Type       string `json:"type"`
	Encoding   string `json:"encoding,omitempty"`
	Text       string `json:"text,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	ByteCount  int64  `json:"byte_count,omitempty"`
}

type Acknowledgment struct {
	RequestID              string `json:"request_id"`
	ResponseRevision       int64  `json:"response_revision"`
	AvailableEventSequence *int64 `json:"available_event_sequence,omitempty"`
}

func New(root string) (*Client, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrConfiguration
	}
	if err := validateOwnerDirectory(root); err != nil {
		return nil, fmt.Errorf("%w: mailbox root: %v", ErrConfiguration, err)
	}
	return &Client{root: root}, nil
}

// WriteRequest publishes immutable JSON first and an empty .ready marker
// last. The client only touches the mailbox filesystem.
func (c *Client) WriteRequest(requestID string, raw []byte) error {
	if c == nil || !validRequestID(requestID) || len(raw) == 0 || len(raw) > maxRequestBytes || !json.Valid(raw) {
		return ErrRequest
	}
	var identity struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil || identity.RequestID != requestID {
		return fmt.Errorf("%w: request_id does not match filename", ErrRequest)
	}
	inbox := filepath.Join(c.root, "inbox")
	if err := validateOwnerDirectory(inbox); err != nil {
		return fmt.Errorf("%w: inbox: %v", ErrRequest, err)
	}
	if err := writeExclusiveSynced(filepath.Join(inbox, requestID+".json"), raw); err != nil {
		return fmt.Errorf("%w: write request: %v", ErrRequest, err)
	}
	if err := writeExclusiveSynced(filepath.Join(inbox, requestID+".ready"), nil); err != nil {
		return fmt.Errorf("%w: publish marker: %v", ErrRequest, err)
	}
	return syncDirectory(inbox)
}

// WaitResponse polls only the outbox file and returns once the matching
// response revision is visible.
func (c *Client) WaitResponse(ctx context.Context, requestID string) (Response, error) {
	if c == nil || !validRequestID(requestID) {
		return Response{}, ErrResponse
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outbox := filepath.Join(c.root, "outbox")
	if err := validateOwnerDirectory(outbox); err != nil {
		return Response{}, fmt.Errorf("%w: outbox: %v", ErrResponse, err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := readOwnerFile(filepath.Join(outbox, requestID+".json"), maxRequestBytes)
		if err == nil {
			var response Response
			if decodeErr := json.Unmarshal(raw, &response); decodeErr != nil || response.RequestID != requestID || response.ResponseRevision < 1 {
				return Response{}, fmt.Errorf("%w: invalid request identity or revision", ErrResponse)
			}
			return response, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Response{}, fmt.Errorf("%w: read outbox: %v", ErrResponse, err)
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ReadDiagnostic reads one owner-only private ingress diagnostic. Diagnostics
// are generated only by Runner after safe marker/file admission; this client
// has no method to publish one.
func (c *Client) ReadDiagnostic(requestID string) (Diagnostic, error) {
	if c == nil || !validRequestID(requestID) {
		return Diagnostic{}, ErrDiagnostic
	}
	diagnostics := filepath.Join(c.root, "diagnostics")
	if err := validateOwnerDirectory(diagnostics); err != nil {
		return Diagnostic{}, fmt.Errorf("%w: diagnostics: %v", ErrDiagnostic, err)
	}
	raw, err := readExactOwnerFile(filepath.Join(diagnostics, requestID+".json"), maxRequestBytes)
	if err != nil {
		return Diagnostic{}, fmt.Errorf("%w: read diagnostics: %w", ErrDiagnostic, err)
	}
	diagnostic, err := decodeDiagnostic(raw)
	if err != nil || diagnostic.RequestID != requestID {
		return Diagnostic{}, ErrDiagnostic
	}
	return diagnostic, nil
}

// WaitDiagnostic polls the private diagnostic path until a matching frozen
// diagnostic becomes visible. It intentionally does not create an ACK because
// diagnostics have their own seven-day retention lifecycle.
func (c *Client) WaitDiagnostic(ctx context.Context, requestID string) (Diagnostic, error) {
	if c == nil || !validRequestID(requestID) {
		return Diagnostic{}, ErrDiagnostic
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		diagnostic, err := c.ReadDiagnostic(requestID)
		if err == nil {
			return diagnostic, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Diagnostic{}, err
		}
		select {
		case <-ctx.Done():
			return Diagnostic{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func decodeDiagnostic(raw []byte) (Diagnostic, error) {
	var wire struct {
		InboxID            *string `json:"inbox_id"`
		RequestID          *string `json:"request_id"`
		DiagnosticRevision *int64  `json:"diagnostic_revision"`
		LifecyclePhase     *string `json:"lifecycle_phase"`
		Accepted           *bool   `json:"accepted"`
		Executed           *bool   `json:"executed"`
		Code               *string `json:"code"`
		Message            *string `json:"message"`
		SchemaDetail       *struct {
			SchemaVersion     *string `json:"schema_version"`
			JSONPointer       *string `json:"json_pointer"`
			Expected          *string `json:"expected"`
			ReceivedType      *string `json:"received_type"`
			CanonicalRunField *string `json:"canonical_run_field"`
			CanonicalRunType  *string `json:"canonical_run_type"`
			MinimalValidRun   *struct {
				RequestID      *string `json:"request_id"`
				IdempotencyKey *string `json:"idempotency_key"`
				Operation      *string `json:"operation"`
				Script         *string `json:"script"`
			} `json:"minimal_valid_run"`
		} `json:"schema_detail"`
		ObservedAt *string `json:"observed_at"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Diagnostic{}, ErrDiagnostic
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Diagnostic{}, ErrDiagnostic
	}
	if wire.InboxID == nil || !validRequestID(*wire.InboxID) || wire.RequestID == nil || !validRequestID(*wire.RequestID) ||
		wire.DiagnosticRevision == nil || *wire.DiagnosticRevision != 1 || wire.LifecyclePhase == nil || *wire.LifecyclePhase != "ingress_validation" ||
		wire.Accepted == nil || *wire.Accepted || wire.Executed == nil || *wire.Executed || wire.Code == nil || wire.Message == nil ||
		wire.ObservedAt == nil || !validDiagnosticCodeMessage(*wire.Code, *wire.Message) {
		return Diagnostic{}, ErrDiagnostic
	}
	if observedAt, err := time.Parse(time.RFC3339Nano, *wire.ObservedAt); err != nil || observedAt.IsZero() {
		return Diagnostic{}, ErrDiagnostic
	}
	var schemaDetail *DiagnosticSchemaDetail
	if wire.SchemaDetail != nil {
		if *wire.Code != "invalid_request_schema" || wire.SchemaDetail.SchemaVersion == nil || wire.SchemaDetail.JSONPointer == nil ||
			wire.SchemaDetail.Expected == nil || wire.SchemaDetail.ReceivedType == nil || wire.SchemaDetail.CanonicalRunField == nil ||
			wire.SchemaDetail.CanonicalRunType == nil || wire.SchemaDetail.MinimalValidRun == nil || wire.SchemaDetail.MinimalValidRun.RequestID == nil ||
			wire.SchemaDetail.MinimalValidRun.IdempotencyKey == nil || wire.SchemaDetail.MinimalValidRun.Operation == nil || wire.SchemaDetail.MinimalValidRun.Script == nil {
			return Diagnostic{}, ErrDiagnostic
		}
		schemaDetail = &DiagnosticSchemaDetail{
			SchemaVersion: *wire.SchemaDetail.SchemaVersion, JSONPointer: *wire.SchemaDetail.JSONPointer,
			Expected: *wire.SchemaDetail.Expected, ReceivedType: *wire.SchemaDetail.ReceivedType,
			CanonicalRunField: *wire.SchemaDetail.CanonicalRunField, CanonicalRunType: *wire.SchemaDetail.CanonicalRunType,
			MinimalValidRun: DiagnosticMinimalValidRun{
				RequestID: *wire.SchemaDetail.MinimalValidRun.RequestID, IdempotencyKey: *wire.SchemaDetail.MinimalValidRun.IdempotencyKey,
				Operation: *wire.SchemaDetail.MinimalValidRun.Operation, Script: *wire.SchemaDetail.MinimalValidRun.Script,
			},
		}
		if !validDiagnosticSchemaDetail(schemaDetail) {
			return Diagnostic{}, ErrDiagnostic
		}
	}
	return Diagnostic{
		InboxID: *wire.InboxID, RequestID: *wire.RequestID, DiagnosticRevision: *wire.DiagnosticRevision,
		LifecyclePhase: *wire.LifecyclePhase, Accepted: *wire.Accepted, Executed: *wire.Executed,
		Code: *wire.Code, Message: *wire.Message, SchemaDetail: schemaDetail, ObservedAt: *wire.ObservedAt,
	}, nil
}

func validDiagnosticSchemaDetail(detail *DiagnosticSchemaDetail) bool {
	if detail == nil || detail.SchemaVersion != "v1" || detail.CanonicalRunField != "script" || detail.CanonicalRunType != "string" ||
		detail.MinimalValidRun.RequestID != "<new-request-id>" || detail.MinimalValidRun.IdempotencyKey != "<new-idempotency-key>" ||
		detail.MinimalValidRun.Operation != "run" || detail.MinimalValidRun.Script != "<shell script>" {
		return false
	}
	switch detail.JSONPointer {
	case "/script":
		if detail.Expected != "required string" && detail.Expected != "string" {
			return false
		}
	case "/command", "/argv", "/cwd":
		if detail.Expected != "unsupported field; use script" {
			return false
		}
	case "":
		if detail.Expected != "documented v1 run fields" {
			return false
		}
	default:
		return false
	}
	switch detail.ReceivedType {
	case "missing", "null", "boolean", "number", "string", "array", "object":
		return true
	default:
		return false
	}
}

func validDiagnosticCodeMessage(code, message string) bool {
	switch code {
	case "malformed_json":
		return message == "request is not valid JSON"
	case "invalid_request_schema":
		return message == "request does not satisfy the mailbox request format"
	case "request_identity_mismatch":
		return message == "request ID does not match the marker filename"
	case "invalid_script":
		return message == "request script is invalid"
	case "request_too_large":
		return message == "request exceeds the mailbox size limit"
	default:
		return false
	}
}

// ReadEventsThroughCursor reads only the frozen prefix advertised by a
// response and validates each event's command ID, sequence, encoding, and
// byte count.
func (c *Client) ReadEventsThroughCursor(response Response) ([]Event, error) {
	if c == nil {
		return nil, ErrConfiguration
	}
	if response.AvailableEventSequence == nil {
		if response.EventsFile != "" {
			return nil, ErrEvents
		}
		return nil, nil
	}
	cursor := *response.AvailableEventSequence
	if cursor < 0 || !validRequestID(response.CommandID) {
		return nil, ErrEvents
	}
	if cursor == 0 {
		if response.EventsFile != "" {
			return nil, ErrEvents
		}
		return []Event{}, nil
	}
	if !validEventReference(response.EventsFile, response.CommandID) {
		return nil, ErrEvents
	}
	eventsDir := filepath.Join(c.root, "events")
	if err := validateOwnerDirectory(eventsDir); err != nil {
		return nil, fmt.Errorf("%w: events directory: %v", ErrEvents, err)
	}
	path := filepath.Join(c.root, filepath.FromSlash(response.EventsFile))
	file, err := openOwnerFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open event file: %v", ErrEvents, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxEventLine)
	events := make([]Event, 0, min(cursor, 256))
	for expected := int64(1); expected <= cursor; expected++ {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("%w: scan event file: %v", ErrEvents, err)
			}
			return nil, fmt.Errorf("%w: event cursor %d is incomplete at %d", ErrEvents, cursor, expected)
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.CommandID != response.CommandID || event.Sequence != expected || event.Type == "" {
			return nil, fmt.Errorf("%w: invalid event at sequence %d", ErrEvents, expected)
		}
		if err := validateEventPayload(event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

// WriteAcknowledgment echoes the response revision and available event cursor
// exactly, then publishes its marker last.
func (c *Client) WriteAcknowledgment(requestID string, response Response) error {
	if c == nil || !validRequestID(requestID) || response.RequestID != requestID || response.ResponseRevision < 1 {
		return ErrAcknowledgment
	}
	if response.AvailableEventSequence != nil && *response.AvailableEventSequence < 0 {
		return ErrAcknowledgment
	}
	acks := filepath.Join(c.root, "acks")
	if err := validateOwnerDirectory(acks); err != nil {
		return fmt.Errorf("%w: ACK directory: %v", ErrAcknowledgment, err)
	}
	ack := Acknowledgment{
		RequestID: requestID, ResponseRevision: response.ResponseRevision,
		AvailableEventSequence: cloneInt64(response.AvailableEventSequence),
	}
	raw, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrAcknowledgment, err)
	}
	if err := writeExclusiveSynced(filepath.Join(acks, requestID+".json"), raw); err != nil {
		return fmt.Errorf("%w: write: %v", ErrAcknowledgment, err)
	}
	if err := writeExclusiveSynced(filepath.Join(acks, requestID+".ready"), nil); err != nil {
		return fmt.Errorf("%w: publish marker: %v", ErrAcknowledgment, err)
	}
	return syncDirectory(acks)
}

func validRequestID(value string) bool {
	return requestIDPattern.MatchString(value)
}

func validEventReference(reference, commandID string) bool {
	return reference == "events/"+commandID+".ndjson" && filepath.Clean(filepath.FromSlash(reference)) == filepath.FromSlash(reference)
}

func validateEventPayload(event Event) error {
	switch event.Type {
	case "stdout", "stderr":
		switch event.Encoding {
		case "utf8":
			if !utf8.ValidString(event.Text) || int64(len(event.Text)) != event.ByteCount || event.DataBase64 != "" {
				return fmt.Errorf("%w: invalid UTF-8 output at sequence %d", ErrEvents, event.Sequence)
			}
		case "base64":
			decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil || int64(len(decoded)) != event.ByteCount || event.Text != "" {
				return fmt.Errorf("%w: invalid base64 output at sequence %d", ErrEvents, event.Sequence)
			}
		default:
			return fmt.Errorf("%w: unsupported output encoding at sequence %d", ErrEvents, event.Sequence)
		}
	default:
		if event.Encoding != "" || event.Text != "" || event.DataBase64 != "" || event.ByteCount != 0 {
			return fmt.Errorf("%w: non-output event carries bytes at sequence %d", ErrEvents, event.Sequence)
		}
	}
	return nil
}

func validateOwnerDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory must be owner-only")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("directory owner differs from current account")
	}
	return nil
}

func openOwnerFile(path string) (*os.File, error) {
	return openPrivateFile(path, false)
}

// openExactOwnerFile accepts only Runner-produced private artifacts. Unlike
// ordinary owner-only mailbox input, the diagnostic contract requires exact
// 0600 permissions so a caller never treats a differently-modeled file as a
// frozen Runner projection.
func openExactOwnerFile(path string) (*os.File, error) {
	return openPrivateFile(path, true)
}

func openPrivateFile(path string, requireExactMode bool) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || (requireExactMode && before.Mode().Perm() != fileMode) {
		return nil, fmt.Errorf("file must be regular and owner-only")
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("file owner differs from current account")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 || (requireExactMode && after.Mode().Perm() != fileMode) {
		_ = file.Close()
		return nil, fmt.Errorf("file changed while opening")
	}
	return file, nil
}

func readOwnerFile(path string, limit int) ([]byte, error) {
	file, err := openOwnerFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("file exceeds byte limit")
	}
	return data, nil
}

func readExactOwnerFile(path string, limit int) ([]byte, error) {
	file, err := openExactOwnerFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("file exceeds byte limit")
	}
	return data, nil
}

func writeExclusiveSynced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(fileMode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
