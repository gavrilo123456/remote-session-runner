package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"syscall"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const (
	// ReadySuffix is published last by a mailbox client. A JSON draft without
	// this marker is never imported.
	ReadySuffix = ".ready"
	// RequestSuffix is the immutable JSON request paired with ReadySuffix.
	RequestSuffix = ".json"
	// MailboxDirectoryMode is the owner-only mode for the mailbox root/inbox.
	MailboxDirectoryMode os.FileMode = 0o700
	// MailboxFileMode is the exact private mode for native-client pairs and
	// every Runner-produced outbox/event projection.
	MailboxFileMode os.FileMode = 0o600
	// MailboxWorkspaceIngressFileMode is the exact mode accepted for a direct
	// workspace-produced request or ACK pair inside an owner-only mailbox tree.
	// It is never used for Runner-produced output or events.
	MailboxWorkspaceIngressFileMode os.FileMode = 0o644
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
	// mailboxIngressBeforeOpenHook is a package-private deterministic race seam
	// for tests. Production leaves it nil. It runs only after the initial
	// Lstat and before the O_NOFOLLOW descriptor open.
	mailboxIngressBeforeOpenHook func()
	// mailboxIngressAfterRequestReadHook is a package-private deterministic
	// race seam for tests. Production leaves it nil. It runs after a safe
	// request descriptor has been read and before the ready marker is
	// revalidated for dispatch.
	mailboxIngressAfterRequestReadHook func()
	// mailboxIngressBeforeDiagnosticCleanupStageHook is a package-private
	// deterministic race seam for tests. Production leaves it nil. It runs
	// after a malformed pair was revalidated and before either member is moved
	// into its durable-cleanup staging path.
	mailboxIngressBeforeDiagnosticCleanupStageHook func()
	// mailboxIngressAfterDiagnosticMarkerStageHook is a package-private
	// deterministic race seam for tests. Production leaves it nil. It runs
	// after the old marker was safely moved out of its publishable path and
	// before cleanup considers the paired request.
	mailboxIngressAfterDiagnosticMarkerStageHook func()
)

//go:embed schemas/v1/request.schema.json
var requestSchemaFS embed.FS

// Request is one validated mailbox request. RawJSON is an immutable copy of
// the bounded bytes read from inbox and is retained for the later durable
// receipt/import phases.
type Request struct {
	// MailboxID and ExecutionIdempotencyKey are trusted runtime fields. Neither
	// is accepted from mailbox JSON or rendered in a response.
	MailboxID               string
	RequestID               string
	IdempotencyKey          string
	ExecutionIdempotencyKey string
	Operation               string
	Environment             string
	// EnvironmentPresent and ExecutionTargetPresent retain whether the
	// corresponding create_session or run request member appeared in the
	// mailbox JSON. P153 needs this distinction because omitting both selects
	// the inbox default, while supplying only one is a terminal request error.
	EnvironmentPresent     bool
	ExecutionTargetPresent bool
	ExecutionTarget        domain.ExecutionTarget
	// RepositoryAlias is optional policy and audit metadata for new-work
	// requests. It never selects a source checkout or execution target.
	RepositoryAlias string
	// ExecutionSelection is trusted processor state. It is not decoded from
	// mailbox JSON; the session processor populates it only from a resolved
	// and durably recorded selection.
	ExecutionSelection *store.MailboxExecutionSelection
	SessionID          string
	CommandID          string
	ClosePolicy        string
	Script             string
	RawJSON            []byte
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

// ingressDiagnosticCandidate is constructed only after every filesystem
// safety gate has passed. It deliberately carries a fingerprint rather than
// raw bytes so the durable malformed-input path cannot retain a request,
// script, operation, or idempotency key.
type ingressDiagnosticCandidate struct {
	RequestID     string
	RequestSHA256 [sha256.Size]byte
	Code          store.MailboxIngressDiagnosticCode
}

// ingressHandlingResult tells the importer whether the mailbox processor made
// a durable decision and performed its own fingerprint-checked pair cleanup.
// It is separate from durableHandler because a retained malformed identity is
// not a normal accepted mailbox exchange.
type ingressHandlingResult struct {
	Durable     bool
	PairRemoved bool
}

type ingressDiagnosticHandler func(context.Context, ingressDiagnosticCandidate) (ingressHandlingResult, error)
type retainedIngressHandler func(context.Context, Request, ingressDiagnosticCandidate) (handled bool, outcome ingressHandlingResult, err error)

type mailboxRequestValidationError struct {
	code store.MailboxIngressDiagnosticCode
	err  error
}

func (e *mailboxRequestValidationError) Error() string {
	if e == nil || e.err == nil {
		return ErrMailboxInput.Error()
	}
	return e.err.Error()
}

func (e *mailboxRequestValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// Options configures an owner-only mailbox importer rooted at Root. Inbox and
// ACK directories are created below Root when absent. Handler is optional for
// validation-only use; Clock controls the 24-hour draft cutoff.
type Options struct {
	MailboxID string
	Root      string
	Handler   Handler
	Clock     func() time.Time
}

// Importer implements marker-last mailbox discovery and validation.
type Importer struct {
	mailboxID string
	root      string
	inbox     string
	acks      string
	handler   Handler
	schema    *jsonschema.Schema
	clock     func() time.Time
}

// NewImporter creates an importer for root. It creates missing root/inbox
// directories with owner-only permissions and rejects existing unsafe modes.
func NewImporter(root string, handler Handler) (*Importer, error) {
	return New(Options{MailboxID: "default", Root: root, Handler: handler})
}

// New is the options-based constructor for the mailbox importer.
func New(options Options) (*Importer, error) {
	if options.MailboxID == "" {
		options.MailboxID = "default"
	}
	if _, ok := safeMailboxID(options.MailboxID); !ok {
		return nil, fmt.Errorf("%w: mailbox ID", ErrImporterConfiguration)
	}
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
	return &Importer{mailboxID: options.MailboxID, root: options.Root, inbox: inbox, acks: acks, handler: options.Handler, schema: schema, clock: clock}, nil
}

// MailboxID returns the trusted namespace configured for this filesystem root.
func (i *Importer) MailboxID() string {
	if i == nil {
		return ""
	}
	return i.mailboxID
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
	return i.importWithIngressHandlers(ctx, handler, nil, nil)
}

// importWithIngressHandlers keeps the ordinary accepted-request boundary
// unchanged while allowing SessionProcessor to handle only safely classified
// malformed input and retained rejected identities. Direct importer users keep
// the original validation-only behavior through importWithRecorder.
func (i *Importer) importWithIngressHandlers(ctx context.Context, handler durableHandler, diagnosticHandler ingressDiagnosticHandler, retainedHandler retainedIngressHandler) ([]Result, error) {
	return i.importWithIngressHandlersAfterRecovery(ctx, handler, diagnosticHandler, retainedHandler, true)
}

// importWithIngressHandlersAfterRecovery permits SessionProcessor to scan
// safely marked work after diagnostic recovery failed without letting ordinary
// draft cleanup erase an unmarked JSON remnant from that interrupted recovery.
// Direct importer users always retain the historical cleanup-first behavior.
func (i *Importer) importWithIngressHandlersAfterRecovery(ctx context.Context, handler durableHandler, diagnosticHandler ingressDiagnosticHandler, retainedHandler retainedIngressHandler, cleanupUnmarkedDrafts bool) ([]Result, error) {
	if i == nil || i.schema == nil || i.inbox == "" {
		return nil, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cleanupUnmarkedDrafts {
		if _, err := i.CleanupUnmarkedDrafts(ctx); err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(i.inbox)
	if err != nil {
		return nil, fmt.Errorf("%w: read inbox: %v", ErrMailboxPath, err)
	}
	results := make([]Result, 0)
	var importErrors error
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
		result, importErr := i.importMarkerWithIngressHandlers(ctx, name, handler, diagnosticHandler, retainedHandler)
		if importErr != nil {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			// A handler may have retained a valid marker for retry. Do not let
			// that one record prevent a later, independent ready pair from being
			// inspected in this cycle.
			importErrors = errors.Join(importErrors, importErr)
		}
		if result.Status != "" {
			results = append(results, result)
		}
	}
	return results, importErrors
}

func (i *Importer) importMarker(ctx context.Context, markerName string, handler durableHandler) (Result, error) {
	return i.importMarkerWithIngressHandlers(ctx, markerName, handler, nil, nil)
}

func (i *Importer) importMarkerWithIngressHandlers(ctx context.Context, markerName string, handler durableHandler, diagnosticHandler ingressDiagnosticHandler, retainedHandler retainedIngressHandler) (Result, error) {
	result := Result{Filename: markerName, MarkerPath: filepath.Join(i.inbox, markerName), Status: ResultRejected}
	requestID, ok := safeRequestID(strings.TrimSuffix(markerName, ReadySuffix))
	if !ok {
		result.Reason = "unsafe request marker filename"
		return result, nil
	}
	result.RequestID = requestID
	requestName := requestID + RequestSuffix
	result.RequestPath = filepath.Join(i.inbox, requestName)
	if err := validateMailboxIngressFile(result.MarkerPath, true); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	if err := validateMailboxIngressFile(result.RequestPath, false); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	if err := validateMailboxReadyMarkerEmpty(result.MarkerPath); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	requestBytes, tooLarge, err := readMailboxIngressBounded(result.RequestPath, domain.MaxSerializedRequestBytes)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	// Marker-last publication is the dispatch authority. Recheck it after
	// reading the request so a marker replaced with a non-empty or unsafe file
	// during intake remains inert and cannot create a diagnostic or work item.
	if err := validateMailboxReadyMarkerEmpty(result.MarkerPath); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	if tooLarge {
		return i.handleIngressDiagnosticCandidate(ctx, result, ingressDiagnosticCandidate{
			RequestID: requestID, RequestSHA256: sha256.Sum256(requestBytes), Code: store.MailboxIngressDiagnosticRequestTooLarge,
		}, diagnosticHandler)
	}
	request, err := i.validateRequest(requestID, requestBytes)
	if err != nil {
		if code, ok := mailboxIngressDiagnosticCodeForError(err); ok {
			return i.handleIngressDiagnosticCandidate(ctx, result, ingressDiagnosticCandidate{
				RequestID: requestID, RequestSHA256: sha256.Sum256(requestBytes), Code: code,
			}, diagnosticHandler)
		}
		result.Reason = err.Error()
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	result.Status = ResultAccepted
	result.Request = &request
	candidate := ingressDiagnosticCandidate{RequestID: requestID, RequestSHA256: sha256.Sum256(requestBytes)}
	if retainedHandler != nil {
		handled, outcome, err := retainedHandler(ctx, request, candidate)
		if err != nil {
			result.Status = ResultRejected
			result.Reason = "mailbox retained ingress diagnostic handling failed"
			result.Request = nil
			result.Durable = outcome.Durable
			result.PairRemoved = outcome.PairRemoved
			return result, err
		}
		if handled {
			result.Status = ResultRejected
			result.Reason = "request ID is retained for a previous rejected request"
			result.Request = nil
			result.Durable = outcome.Durable
			result.PairRemoved = outcome.PairRemoved
			return result, nil
		}
	}
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

func (i *Importer) handleIngressDiagnosticCandidate(ctx context.Context, result Result, candidate ingressDiagnosticCandidate, handler ingressDiagnosticHandler) (Result, error) {
	result.Reason = mailboxIngressDiagnosticResultReason(candidate.Code)
	if handler == nil {
		return result, nil
	}
	outcome, err := handler(ctx, candidate)
	result.Durable = outcome.Durable
	result.PairRemoved = outcome.PairRemoved
	if err != nil {
		// A structured runtime log may report only the fixed failure class. Do
		// not put a parser, filesystem, or request-derived error into Result.
		result.Reason = "mailbox ingress diagnostic handling failed"
		return result, err
	}
	return result, nil
}

func mailboxIngressDiagnosticResultReason(code store.MailboxIngressDiagnosticCode) string {
	switch code {
	case store.MailboxIngressDiagnosticMalformedJSON:
		return "mailbox request is malformed JSON"
	case store.MailboxIngressDiagnosticInvalidRequestSchema:
		return ErrMailboxSchema.Error()
	case store.MailboxIngressDiagnosticRequestIdentityMismatch:
		return "mailbox request ID does not match marker filename"
	case store.MailboxIngressDiagnosticInvalidScript:
		return ErrMailboxScriptTooLarge.Error()
	case store.MailboxIngressDiagnosticRequestTooLarge:
		return ErrMailboxRequestTooLarge.Error()
	default:
		return ErrMailboxInput.Error()
	}
}

func (i *Importer) validateRequest(filenameID string, raw []byte) (Request, error) {
	if err := domain.ValidateSerializedRequest(raw); err != nil {
		return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticRequestTooLarge, err: ErrMailboxRequestTooLarge}
	}
	value, err := decodeOneJSON(raw)
	if err != nil {
		return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticMalformedJSON, err: fmt.Errorf("%w: malformed JSON", ErrMailboxSchema)}
	}
	if err := i.schema.Validate(value); err != nil {
		return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticInvalidRequestSchema, err: ErrMailboxSchema}
	}
	var wire struct {
		RequestID       string  `json:"request_id"`
		IdempotencyKey  string  `json:"idempotency_key"`
		Operation       string  `json:"operation"`
		Environment     *string `json:"environment"`
		ExecutionTarget *struct {
			Kind    domain.TargetKind `json:"kind"`
			Profile string            `json:"profile"`
		} `json:"execution_target"`
		RepositoryAlias string          `json:"repository_alias"`
		SessionID       string          `json:"session_id"`
		CommandID       string          `json:"command_id"`
		Script          string          `json:"script"`
		ClosePolicy     json.RawMessage `json:"close_policy"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticInvalidRequestSchema, err: ErrMailboxSchema}
	}
	if wire.RequestID != filenameID {
		return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticRequestIdentityMismatch, err: fmt.Errorf("%w: request_id does not match marker filename", ErrMailboxInput)}
	}
	if !mailboxRequestIDPattern.MatchString(wire.RequestID) {
		return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticInvalidRequestSchema, err: fmt.Errorf("%w: request_id is not a safe basename", ErrMailboxInput)}
	}
	if wire.Script != "" || wire.Operation == "submit_command" || wire.Operation == "run" {
		if err := domain.ValidateScriptUTF8(wire.Script); err != nil {
			return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticInvalidScript, err: ErrMailboxScriptTooLarge}
		}
	}
	var target domain.ExecutionTarget
	if wire.ExecutionTarget != nil {
		target, err = domain.NewExecutionTarget(wire.ExecutionTarget.Kind, wire.ExecutionTarget.Profile)
		if err != nil {
			return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticInvalidRequestSchema, err: ErrMailboxSchema}
		}
	}
	environment := ""
	if wire.Environment != nil {
		environment = *wire.Environment
	}
	closePolicy := ""
	if wire.Operation == "close_session" {
		closePolicy, err = parseClosePolicy(wire.ClosePolicy)
		if err != nil {
			return Request{}, &mailboxRequestValidationError{code: store.MailboxIngressDiagnosticInvalidRequestSchema, err: ErrMailboxSchema}
		}
	}
	return Request{
		MailboxID:              i.mailboxID,
		RequestID:              wire.RequestID,
		IdempotencyKey:         wire.IdempotencyKey,
		Operation:              wire.Operation,
		Environment:            environment,
		EnvironmentPresent:     wire.Environment != nil,
		ExecutionTarget:        target,
		ExecutionTargetPresent: wire.ExecutionTarget != nil,
		RepositoryAlias:        wire.RepositoryAlias,
		SessionID:              wire.SessionID,
		CommandID:              wire.CommandID,
		ClosePolicy:            closePolicy,
		Script:                 wire.Script,
		RawJSON:                append([]byte(nil), raw...),
	}, nil
}

func mailboxIngressDiagnosticCodeForError(err error) (store.MailboxIngressDiagnosticCode, bool) {
	var validation *mailboxRequestValidationError
	if errors.As(err, &validation) && validation != nil {
		return validation.code, true
	}
	return "", false
}

func parseClosePolicy(raw json.RawMessage) (string, error) {
	policy := "cancel"
	if len(raw) == 0 {
		return policy, nil
	}
	var input struct {
		Policy string `json:"policy"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", errors.New("close_policy contains multiple JSON values")
		}
		return "", err
	}
	policy = strings.TrimSpace(input.Policy)
	if policy == "" {
		policy = "cancel"
	}
	if len(policy) > 64 || strings.IndexByte(policy, 0) >= 0 {
		return "", errors.New("policy is invalid")
	}
	return policy, nil
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

func safeMailboxID(value string) (string, bool) {
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
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !mailboxFileOwnedByCurrentUser(info) || info.Mode().Perm() != MailboxDirectoryMode {
		return fmt.Errorf("%w: %s must be an owner-only directory", ErrMailboxPath, path)
	}
	return nil
}

func mailboxFileOwnedByCurrentUser(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ownerOK := info.Sys().(*syscall.Stat_t)
	return ownerOK && int(stat.Uid) == os.Geteuid()
}

func validateMailboxIngressFile(path string, marker bool) error {
	_, err := inspectMailboxIngressFile(path, marker)
	return err
}

func validateMailboxReadyMarkerEmpty(path string) error {
	info, err := inspectMailboxIngressFile(path, true)
	if err != nil {
		return errors.New("ready marker changed before import")
	}
	if info.Size() != 0 {
		return errors.New("ready marker must be empty")
	}
	return nil
}

// inspectMailboxIngressFile opens a selected mailbox input without following
// a replacement symlink and proves the opened descriptor is the same regular,
// selected-user-owned inode examined by Lstat. The caller can safely use the
// returned metadata only for that descriptor's immutable-at-open identity.
func inspectMailboxIngressFile(path string, marker bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if marker {
			return nil, errors.New("ready marker has no regular request pair")
		}
		return nil, errors.New("request file is missing")
	}
	if err != nil {
		return nil, fmt.Errorf("inspect mailbox file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("mailbox symlink is rejected")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("mailbox input must be a regular file")
	}
	if !mailboxFileOwnedByCurrentUser(info) {
		return nil, errors.New("mailbox input must be owned by the selected user")
	}
	if !isMailboxIngressFileMode(info.Mode().Perm()) {
		return nil, fmt.Errorf("mailbox ingress file mode is %04o, want 0600 or 0644", info.Mode().Perm())
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("mailbox input changed while opening")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !safeIngressFileInfo(opened) || !os.SameFile(info, opened) {
		return nil, errors.New("mailbox input changed while opening")
	}
	return opened, nil
}

func validateMailboxPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("mailbox file is missing")
	}
	if err != nil {
		return fmt.Errorf("inspect mailbox file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("mailbox symlink is rejected")
	}
	if !info.Mode().IsRegular() {
		return errors.New("mailbox file must be a regular file")
	}
	if info.Mode().Perm() != MailboxFileMode {
		return fmt.Errorf("mailbox private file mode is %04o, want 0600", info.Mode().Perm())
	}
	return nil
}

func isMailboxIngressFileMode(mode os.FileMode) bool {
	return mode == MailboxFileMode || mode == MailboxWorkspaceIngressFileMode
}

func readBounded(path string, maximum int) ([]byte, error) {
	file, err := openMailboxIngressFile(path)
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

// readMailboxIngressBounded retains at most one byte beyond the configured
// ingress limit. That bounded image is sufficient to fingerprint an oversized
// safe input without retaining its full content, and lets a later cleanup
// prove it is removing the same published file.
func readMailboxIngressBounded(path string, maximum int) ([]byte, bool, error) {
	data, tooLarge, _, err := readMailboxIngressBoundedWithInfo(path, maximum)
	return data, tooLarge, err
}

// readMailboxIngressBoundedWithInfo retains at most one byte beyond the
// configured ingress limit and returns the descriptor-backed identity used for
// that read. Cleanup staging uses the identity to avoid unlinking a later
// publisher replacement selected by pathname after revalidation.
func readMailboxIngressBoundedWithInfo(path string, maximum int) ([]byte, bool, os.FileInfo, error) {
	file, err := openMailboxIngressFile(path)
	if err != nil {
		return nil, false, nil, fmt.Errorf("read mailbox request: %v", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !safeIngressFileInfo(info) {
		return nil, false, nil, errors.New("read mailbox request: mailbox input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, false, nil, fmt.Errorf("read mailbox request: %v", err)
	}
	if hook := mailboxIngressAfterRequestReadHook; hook != nil {
		mailboxIngressAfterRequestReadHook = nil
		hook()
	}
	return data, len(data) > maximum, info, nil
}

func openMailboxIngressFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read mailbox request: %v", err)
	}
	if !safeIngressFileInfo(info) {
		return nil, errors.New("read mailbox request: mailbox input changed while opening")
	}
	if hook := mailboxIngressBeforeOpenHook; hook != nil {
		mailboxIngressBeforeOpenHook = nil
		hook()
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("read mailbox request: mailbox input changed while opening")
	}
	file := os.NewFile(uintptr(fd), path)
	opened, err := file.Stat()
	if err != nil || !safeIngressFileInfo(opened) || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, errors.New("read mailbox request: mailbox input changed while opening")
	}
	return file, nil
}

type mailboxIngressPairState struct {
	MarkerPresent  bool
	RequestPresent bool
	markerInfo     os.FileInfo
	requestInfo    os.FileInfo
}

func (i *Importer) ingressDiagnosticCleanupPaths(requestID string) (string, string) {
	prefix := "." + requestID + ".ingress-diagnostic"
	return filepath.Join(i.inbox, prefix+".ready.cleanup"), filepath.Join(i.inbox, prefix+".json.cleanup")
}

// revalidateIngressDiagnosticPair proves that the input pair remains in a
// safe state and that any remaining request image has the exact bounded
// fingerprint recorded during intake. A false result means a publisher or
// filesystem race changed the pair; it is deliberately inert rather than a
// reason to remove a newer request.
func (i *Importer) revalidateIngressDiagnosticPair(requestID string, want [sha256.Size]byte) (mailboxIngressPairState, bool, error) {
	if i == nil || i.inbox == "" {
		return mailboxIngressPairState{}, false, ErrImporterConfiguration
	}
	if _, ok := safeRequestID(requestID); !ok {
		return mailboxIngressPairState{}, false, ErrMailboxPath
	}
	if err := ensureOwnerDirectory(i.inbox); err != nil {
		return mailboxIngressPairState{}, false, err
	}
	state := mailboxIngressPairState{}
	markerPath := filepath.Join(i.inbox, requestID+ReadySuffix)
	markerInitial, err := os.Lstat(markerPath)
	if err == nil {
		state.MarkerPresent = true
		if !safeIngressFileInfo(markerInitial) {
			return state, false, nil
		}
		markerInfo, inspectErr := inspectMailboxIngressFile(markerPath, true)
		if inspectErr != nil {
			return state, false, nil
		}
		if markerInfo.Size() != 0 {
			return state, false, nil
		}
		state.markerInfo = markerInfo
	} else if !errors.Is(err, os.ErrNotExist) {
		return state, false, fmt.Errorf("%w: inspect mailbox diagnostic marker", ErrMailboxPath)
	}
	requestPath := filepath.Join(i.inbox, requestID+RequestSuffix)
	requestInfo, err := os.Lstat(requestPath)
	if err == nil {
		state.RequestPresent = true
		if !safeIngressFileInfo(requestInfo) {
			return state, false, nil
		}
		data, _, requestInfo, err := readMailboxIngressBoundedWithInfo(requestPath, domain.MaxSerializedRequestBytes)
		if err != nil {
			return state, false, err
		}
		if sha256.Sum256(data) != want {
			return state, false, nil
		}
		state.requestInfo = requestInfo
	} else if !errors.Is(err, os.ErrNotExist) {
		return state, false, fmt.Errorf("%w: inspect mailbox diagnostic request", ErrMailboxPath)
	}
	// The marker is dispatch authority, including during marker-first cleanup.
	// Reinspect it after the request descriptor has been read so a replacement
	// to a non-empty or unsafe marker cannot be treated as the original
	// publication and removed by diagnostic cleanup.
	if state.MarkerPresent {
		finalMarker, markerErr := os.Lstat(markerPath)
		switch {
		case errors.Is(markerErr, os.ErrNotExist):
			state.MarkerPresent = false
			state.markerInfo = nil
		case markerErr != nil:
			return state, false, fmt.Errorf("%w: recheck mailbox diagnostic marker", ErrMailboxPath)
		case !safeIngressFileInfo(finalMarker):
			return state, false, nil
		default:
			markerInfo, inspectErr := inspectMailboxIngressFile(markerPath, true)
			if inspectErr != nil || markerInfo.Size() != 0 {
				return state, false, nil
			}
			state.markerInfo = markerInfo
		}
	}
	return state, true, nil
}

// removeRevalidatedIngressDiagnosticPair removes only input that still matches
// a previously recorded bounded fingerprint. It writes no normal receipt and
// leaves a changed or unsafe replacement untouched. The marker is removed
// first and each unlink is synced before the next one.
func (i *Importer) removeRevalidatedIngressDiagnosticPair(requestID string, want [sha256.Size]byte) (mailboxIngressPairState, bool, error) {
	state, matched, err := i.revalidateIngressDiagnosticPair(requestID, want)
	if err != nil || !matched {
		return state, matched, err
	}
	if state.MarkerPresent && !state.RequestPresent {
		// A marker without its paired bytes cannot be bound to the ledger
		// fingerprint. Leave it inert for explicit operator review.
		return state, false, nil
	}
	if hook := mailboxIngressBeforeDiagnosticCleanupStageHook; hook != nil {
		mailboxIngressBeforeDiagnosticCleanupStageHook = nil
		hook()
	}

	markerPath := filepath.Join(i.inbox, requestID+ReadySuffix)
	requestPath := filepath.Join(i.inbox, requestID+RequestSuffix)
	markerStage, requestStage := i.ingressDiagnosticCleanupPaths(requestID)
	if state.MarkerPresent {
		staged, err := i.stageRevalidatedIngressFile(markerPath, markerStage, state.markerInfo)
		if err != nil || !staged {
			return state, false, err
		}
		if hook := mailboxIngressAfterDiagnosticMarkerStageHook; hook != nil {
			mailboxIngressAfterDiagnosticMarkerStageHook = nil
			hook()
		}
	}
	// Once the old marker is staged, it must never be restored. A publisher may
	// be writing a replacement JSON before its own marker. Restoring the old
	// marker would make that new JSON dispatchable before the publisher declares
	// it complete.
	if present, err := ingressPathPresent(markerPath); err != nil || present {
		if discardErr := i.discardIngressDiagnosticStage(markerStage, true, want); discardErr != nil && err == nil {
			err = discardErr
		}
		return state, false, err
	}
	if state.RequestPresent {
		staged, err := i.stageRevalidatedIngressFile(requestPath, requestStage, state.requestInfo)
		if err != nil || !staged {
			if discardErr := i.discardIngressDiagnosticStage(markerStage, true, want); discardErr != nil && err == nil {
				err = discardErr
			}
			return state, false, err
		}
	}
	// A replacement can appear after either old member is staged. Keep every
	// current source pathname untouched and discard only the known old staging
	// entries. The next scan will see the publisher's own marker-last pair.
	markerPresent, err := ingressPathPresent(markerPath)
	if err != nil {
		return state, false, err
	}
	requestPresent, err := ingressPathPresent(requestPath)
	if err != nil {
		return state, false, err
	}
	if markerPresent || requestPresent {
		if err := i.discardIngressDiagnosticStages(markerStage, requestStage, want); err != nil {
			return state, false, err
		}
		return state, false, nil
	}
	if err := i.discardIngressDiagnosticStages(markerStage, requestStage, want); err != nil {
		return state, false, err
	}
	return mailboxIngressPairState{}, true, nil
}

// cleanupRecordedIngressDiagnosticStages completes cleanup after a stop that
// occurred while a known old pair was staged out of its public inbox names.
// Staging names are deliberately not publishable request or marker names. The
// marker is removed only when empty; the request is removed only when its
// bounded image still matches the durable fingerprint.
func (i *Importer) cleanupRecordedIngressDiagnosticStages(requestID string, want [sha256.Size]byte) error {
	if i == nil || i.inbox == "" {
		return ErrImporterConfiguration
	}
	if _, ok := safeRequestID(requestID); !ok {
		return ErrMailboxPath
	}
	markerStage, requestStage := i.ingressDiagnosticCleanupPaths(requestID)
	return i.discardIngressDiagnosticStages(markerStage, requestStage, want)
}

func (i *Importer) discardIngressDiagnosticStages(markerStage, requestStage string, want [sha256.Size]byte) error {
	if err := i.discardIngressDiagnosticStage(markerStage, true, want); err != nil {
		return err
	}
	return i.discardIngressDiagnosticStage(requestStage, false, want)
}

// stageRevalidatedIngressFile moves one selected source name aside before it
// is removed. If a publisher replaced the pathname after revalidation, the
// mismatched entry is restored with an exclusive hard-link so a newer source
// pathname is never overwritten. The caller sees false and leaves current
// publishable files for a later scan.
func (i *Importer) stageRevalidatedIngressFile(sourcePath, stagePath string, want os.FileInfo) (bool, error) {
	if want == nil {
		return false, ErrMailboxPath
	}
	if _, err := os.Lstat(stagePath); err == nil {
		return false, fmt.Errorf("%w: ingress diagnostic staging path already exists", ErrMailboxPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("%w: inspect ingress diagnostic staging path", ErrMailboxPath)
	}
	if err := os.Rename(sourcePath, stagePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%w: stage malformed request input", ErrMailboxPath)
	}
	staged, err := os.Lstat(stagePath)
	if err != nil {
		return false, fmt.Errorf("%w: inspect staged malformed request input", ErrMailboxPath)
	}
	if !safeIngressFileInfo(staged) {
		// Do not link or reopen a replacement symlink, special file, foreign
		// inode, or unsafe mode. Keep it hidden and inert for operator review.
		return false, fmt.Errorf("%w: staged mailbox input became unsafe", ErrMailboxPath)
	}
	if os.SameFile(want, staged) {
		if err := syncMailboxDirectory(i.inbox); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := restoreIngressDiagnosticReplacement(stagePath, sourcePath); err != nil {
		return false, err
	}
	if err := syncMailboxDirectory(i.inbox); err != nil {
		return false, err
	}
	return false, nil
}

// restoreIngressDiagnosticReplacement restores a source that was replaced
// after revalidation without ever overwriting a pathname created by a newer
// publisher. A hard link is created only while the original source name is
// absent; EEXIST proves a newer pathname already won the race.
func restoreIngressDiagnosticReplacement(stagePath, sourcePath string) error {
	if err := os.Link(stagePath, sourcePath); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: restore replaced mailbox input", ErrMailboxPath)
	}
	if err := os.Remove(stagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove replaced mailbox staging input", ErrMailboxPath)
	}
	return nil
}

// discardIngressDiagnosticStage removes one hidden stage file. It is only
// called after input-cleanup intent is durable. Hidden marker stages must be
// safe and empty; hidden request stages must still match the ledger's bounded
// fingerprint so a newer request is never discarded after a restart.
func (i *Importer) discardIngressDiagnosticStage(path string, marker bool, want [sha256.Size]byte) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect ingress diagnostic staging input", ErrMailboxPath)
	}
	if !safeIngressFileInfo(info) {
		return fmt.Errorf("%w: unsafe ingress diagnostic staging input", ErrMailboxPath)
	}
	if marker {
		markerInfo, err := inspectMailboxIngressFile(path, true)
		if err != nil || markerInfo.Size() != 0 {
			return fmt.Errorf("%w: invalid ingress diagnostic staging marker", ErrMailboxPath)
		}
	} else {
		data, _, _, err := readMailboxIngressBoundedWithInfo(path, domain.MaxSerializedRequestBytes)
		if err != nil {
			return err
		}
		if sha256.Sum256(data) != want {
			return fmt.Errorf("%w: changed ingress diagnostic staging request", ErrMailboxPath)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove ingress diagnostic staging input", ErrMailboxPath)
	}
	return syncMailboxDirectory(i.inbox)
}

func ingressPathPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("%w: inspect mailbox input during ingress cleanup", ErrMailboxPath)
}
