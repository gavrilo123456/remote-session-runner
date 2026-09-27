package mailbox

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"remote-session-runner/src/internal/domain"
)

const (
	// ReadySuffix is published last by a mailbox client. A JSON draft without
	// this marker is never imported.
	ReadySuffix = ".ready"
	// RequestSuffix is the immutable JSON request paired with ReadySuffix.
	RequestSuffix = ".json"
	// MailboxDirectoryMode is the owner-only mode for the mailbox root/inbox.
	MailboxDirectoryMode os.FileMode = 0o700
	// MailboxFileMode is the owner-only mode for request and marker files.
	MailboxFileMode os.FileMode = 0o600
)

var (
	ErrImporterConfiguration  = errors.New("mailbox importer configuration is invalid")
	ErrMailboxPath            = errors.New("mailbox path is invalid")
	ErrMailboxInput           = errors.New("mailbox input is invalid")
	ErrMailboxSchema          = errors.New("mailbox request schema is invalid")
	ErrMailboxRequestTooLarge = errors.New("mailbox request exceeds byte limit")
	ErrMailboxScriptTooLarge  = errors.New("mailbox script exceeds byte limit")

	// Request IDs are also filename stems. Restricting them to a bounded safe
	// basename prevents traversal, separators, control characters, and names
	// that could be confused with a marker or temporary file.
	mailboxRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

//go:embed schemas/v1/request.schema.json
var requestSchemaFS embed.FS

// Request is one validated mailbox request. RawJSON is an immutable copy of
// the bounded bytes read from inbox and is retained for the later durable
// receipt/import phases.
type Request struct {
	RequestID      string
	IdempotencyKey string
	Operation      string
	Environment    string
	SessionID      string
	CommandID      string
	Script         string
	RawJSON        []byte
}

// ResultStatus describes whether a marked request passed importer validation.
// A rejected result is never sent to the handler.
type ResultStatus string

const (
	ResultAccepted ResultStatus = "accepted"
	ResultRejected ResultStatus = "rejected"
)

// Result is one marker encountered by Import. Rejection reasons are safe
// diagnostic text; raw request/script bytes are never included.
type Result struct {
	Filename    string
	RequestPath string
	MarkerPath  string
	RequestID   string
	Status      ResultStatus
	Reason      string
	Request     *Request
	Durable     bool
	PairRemoved bool
}

// Handler receives only fully validated requests. It is deliberately an
// injected boundary: P081 does not execute mutations or write responses.
type Handler func(context.Context, Request) error

type durableHandler func(context.Context, Request) (bool, error)

// Options configures an owner-only mailbox importer rooted at Root. Inbox and
// ACK directories are created below Root when absent. Handler is optional for
// validation-only use; Clock controls the 24-hour draft cutoff.
type Options struct {
	Root    string
	Handler Handler
	Clock   func() time.Time
}

// Importer implements marker-last mailbox discovery and validation.
type Importer struct {
	root    string
	inbox   string
	acks    string
	handler Handler
	schema  *jsonschema.Schema
	clock   func() time.Time
}

// NewImporter creates an importer for root. It creates missing root/inbox
// directories with owner-only permissions and rejects existing unsafe modes.
func NewImporter(root string, handler Handler) (*Importer, error) {
	return New(Options{Root: root, Handler: handler})
}

// New is the options-based constructor for the mailbox importer.
func New(options Options) (*Importer, error) {
	if strings.TrimSpace(options.Root) == "" || !filepath.IsAbs(options.Root) || filepath.Clean(options.Root) != options.Root || strings.IndexByte(options.Root, 0) >= 0 {
		return nil, fmt.Errorf("%w: root must be an absolute clean path", ErrImporterConfiguration)
	}
	if err := ensureOwnerDirectory(options.Root); err != nil {
		return nil, err
	}
	inbox := filepath.Join(options.Root, "inbox")
	if err := ensureOwnerDirectory(inbox); err != nil {
		return nil, err
	}
	acks := filepath.Join(options.Root, "acks")
	if err := ensureOwnerDirectory(acks); err != nil {
		return nil, err
	}
	schema, err := compileRequestSchema()
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Importer{root: options.Root, inbox: inbox, acks: acks, handler: options.Handler, schema: schema, clock: clock}, nil
}

// InboxPath returns the configured inbox directory.
func (i *Importer) InboxPath() string {
	if i == nil {
		return ""
	}
	return i.inbox
}

// Import first collects expired unmarked drafts, then scans the inbox once.
// Only safe regular request/marker pairs with a valid schema and bounded
// script reach Handler. A malformed marked input produces a rejected Result
// and does not stop other markers from being examined.
func (i *Importer) Import(ctx context.Context) ([]Result, error) {
	return i.importWithHandler(ctx, i.handler)
}

func (i *Importer) importWithHandler(ctx context.Context, handler Handler) ([]Result, error) {
	var recorder durableHandler
	if handler != nil {
		recorder = func(ctx context.Context, request Request) (bool, error) {
			return false, handler(ctx, request)
		}
	}
	return i.importWithRecorder(ctx, recorder)
}

func (i *Importer) importWithRecorder(ctx context.Context, handler durableHandler) ([]Result, error) {
	if i == nil || i.schema == nil || i.inbox == "" {
		return nil, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := i.CleanupUnmarkedDrafts(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(i.inbox)
	if err != nil {
		return nil, fmt.Errorf("%w: read inbox: %v", ErrMailboxPath, err)
	}
	results := make([]Result, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ReadySuffix) {
			// Draft JSON, temporary files, and unrelated names are inert until
			// their marker is safely published.
			continue
		}
		result, importErr := i.importMarker(ctx, name, handler)
		if importErr != nil {
			return results, importErr
		}
		if result.Status != "" {
			results = append(results, result)
		}
	}
	return results, nil
}

func (i *Importer) importMarker(ctx context.Context, markerName string, handler durableHandler) (Result, error) {
	result := Result{Filename: markerName, MarkerPath: filepath.Join(i.inbox, markerName), Status: ResultRejected}
	requestID, ok := safeRequestID(strings.TrimSuffix(markerName, ReadySuffix))
	if !ok {
		result.Reason = "unsafe request marker filename"
		return result, nil
	}
	result.RequestID = requestID
	requestName := requestID + RequestSuffix
	result.RequestPath = filepath.Join(i.inbox, requestName)
	if err := validateMailboxFile(result.MarkerPath, true); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	if err := validateMailboxFile(result.RequestPath, false); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	markerInfo, err := os.Stat(result.MarkerPath)
	if err != nil {
		result.Reason = "marker disappeared before import"
		return result, nil
	}
	if markerInfo.Size() != 0 {
		result.Reason = "ready marker must be empty"
		return result, nil
	}
	requestInfo, err := os.Stat(result.RequestPath)
	if err != nil {
		result.Reason = "request disappeared before import"
		return result, nil
	}
	if requestInfo.Size() > int64(domain.MaxSerializedRequestBytes) {
		result.Reason = ErrMailboxRequestTooLarge.Error()
		return result, nil
	}
	requestBytes, err := readBounded(result.RequestPath, domain.MaxSerializedRequestBytes)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	request, err := i.validateRequest(requestID, requestBytes)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	result.Status = ResultAccepted
	result.Request = &request
	if handler != nil {
		durable, err := handler(ctx, request)
		result.Durable = durable
		if err != nil {
			result.Status = ResultRejected
			result.Reason = err.Error()
			result.Request = nil
		}
		if durable {
			if err := removeMailboxPair(i.inbox, requestID); err != nil {
				return result, err
			}
			result.PairRemoved = true
		}
	}
	return result, nil
}

func (i *Importer) validateRequest(filenameID string, raw []byte) (Request, error) {
	if err := domain.ValidateSerializedRequest(raw); err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrMailboxRequestTooLarge, err)
	}
	value, err := decodeOneJSON(raw)
	if err != nil {
		return Request{}, fmt.Errorf("%w: malformed JSON: %v", ErrMailboxSchema, err)
	}
	if err := i.schema.Validate(value); err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrMailboxSchema, err)
	}
	var wire struct {
		RequestID      string `json:"request_id"`
		IdempotencyKey string `json:"idempotency_key"`
		Operation      string `json:"operation"`
		Environment    string `json:"environment"`
		SessionID      string `json:"session_id"`
		CommandID      string `json:"command_id"`
		Script         string `json:"script"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Request{}, fmt.Errorf("%w: decode request: %v", ErrMailboxSchema, err)
	}
	if wire.RequestID != filenameID {
		return Request{}, fmt.Errorf("%w: request_id does not match marker filename", ErrMailboxInput)
	}
	if !mailboxRequestIDPattern.MatchString(wire.RequestID) {
		return Request{}, fmt.Errorf("%w: request_id is not a safe basename", ErrMailboxInput)
	}
	if wire.Script != "" || wire.Operation == "submit_command" || wire.Operation == "run" {
		if err := domain.ValidateScriptUTF8(wire.Script); err != nil {
			return Request{}, fmt.Errorf("%w: %v", ErrMailboxScriptTooLarge, err)
		}
	}
	return Request{RequestID: wire.RequestID, IdempotencyKey: wire.IdempotencyKey, Operation: wire.Operation, Environment: wire.Environment, SessionID: wire.SessionID, CommandID: wire.CommandID, Script: wire.Script, RawJSON: append([]byte(nil), raw...)}, nil
}

func compileRequestSchema() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const resource = "https://remote-session-runner.invalid/src/internal/mailbox/schemas/v1/request.schema.json"
	data, err := fs.ReadFile(requestSchemaFS, "schemas/v1/request.schema.json")
	if err != nil {
		return nil, fmt.Errorf("%w: read request schema: %v", ErrMailboxSchema, err)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode request schema: %v", ErrMailboxSchema, err)
	}
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, fmt.Errorf("%w: register request schema: %v", ErrMailboxSchema, err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("%w: compile request schema: %v", ErrMailboxSchema, err)
	}
	return schema, nil
}

func decodeOneJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func safeRequestID(value string) (string, bool) {
	return value, mailboxRequestIDPattern.MatchString(value)
}

func ensureOwnerDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, MailboxDirectoryMode); err != nil {
			return fmt.Errorf("%w: create %s: %v", ErrMailboxPath, path, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrMailboxPath, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s must be an owner-only directory", ErrMailboxPath, path)
	}
	return nil
}

func validateMailboxFile(path string, marker bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if marker {
			return errors.New("ready marker has no regular request pair")
		}
		return errors.New("request file is missing")
	}
	if err != nil {
		return fmt.Errorf("inspect mailbox file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("mailbox symlink is rejected")
	}
	if !info.Mode().IsRegular() {
		return errors.New("mailbox input must be a regular file")
	}
	if info.Mode().Perm() != MailboxFileMode {
		return fmt.Errorf("mailbox file mode is %04o, want 0600", info.Mode().Perm())
	}
	return nil
}

func readBounded(path string, maximum int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read mailbox request: %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, fmt.Errorf("read mailbox request: %v", err)
	}
	if len(data) > maximum {
		return nil, ErrMailboxRequestTooLarge
	}
	return data, nil
}
