package mailbox

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	ErrAckImporterConfiguration = errors.New("mailbox ACK importer configuration is invalid")
	ErrMailboxAckSchema         = errors.New("mailbox ACK schema is invalid")
)

//go:embed schemas/v1/ack.schema.json
var ackSchemaFS embed.FS

// AckImporterOptions configures safe ACK-file consumption. Clock is shared
// with draft cleanup so tests can advance the 24-hour cutoff without waiting.
type AckImporterOptions struct {
	MailboxID string
	Root      string
	Authority *store.AuthorityStore
	Clock     func() time.Time
}

// AckImporter records exact ACKs through the durable P089 store transaction
// before deleting either file in the ACK pair.
type AckImporter struct {
	mailboxID string
	root      string
	inbox     string
	acks      string
	authority *store.AuthorityStore
	schema    *jsonschema.Schema
	clock     func() time.Time
}

// NewAckImporter creates the owner-only mailbox directories and validates the
// embedded ACK contract before accepting files.
func NewAckImporter(options AckImporterOptions) (*AckImporter, error) {
	if options.MailboxID == "" {
		options.MailboxID = "default"
	}
	if _, ok := safeMailboxID(options.MailboxID); !ok {
		return nil, ErrAckImporterConfiguration
	}
	if strings.TrimSpace(options.Root) == "" || !filepath.IsAbs(options.Root) || filepath.Clean(options.Root) != options.Root || strings.IndexByte(options.Root, 0) >= 0 || options.Authority == nil {
		return nil, ErrAckImporterConfiguration
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
	schema, err := compileAckSchema()
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &AckImporter{mailboxID: options.MailboxID, root: options.Root, inbox: inbox, acks: acks, authority: options.Authority, schema: schema, clock: clock}, nil
}

// MailboxID returns the trusted namespace configured for this ACK root.
func (i *AckImporter) MailboxID() string {
	if i == nil {
		return ""
	}
	return i.mailboxID
}

// AcksPath returns the configured ACK directory.
func (i *AckImporter) AcksPath() string {
	if i == nil {
		return ""
	}
	return i.acks
}

// Import first collects expired unmarked drafts, then consumes each safe
// marker-last ACK. A mismatched or malformed ACK remains on disk because no
// durable acknowledgement was recorded.
func (i *AckImporter) Import(ctx context.Context) ([]Result, error) {
	if i == nil || i.schema == nil || i.authority == nil || i.acks == "" {
		return nil, ErrAckImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cutoff := i.clock().Add(-UnmarkedDraftLifetime)
	for _, directory := range []string{i.inbox, i.acks} {
		if err := ensureOwnerDirectory(directory); err != nil {
			return nil, err
		}
		if _, err := cleanupUnmarkedDraftDirectory(ctx, directory, cutoff); err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(i.acks)
	if err != nil {
		return nil, fmt.Errorf("%w: read ACK directory: %v", ErrMailboxPath, err)
	}
	results := make([]Result, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ReadySuffix) {
			continue
		}
		result, importErr := i.importMarker(ctx, name)
		if importErr != nil {
			return results, importErr
		}
		if result.Status != "" {
			results = append(results, result)
		}
	}
	return results, nil
}

func (i *AckImporter) importMarker(ctx context.Context, markerName string) (Result, error) {
	result := Result{Filename: markerName, MarkerPath: filepath.Join(i.acks, markerName), Status: ResultRejected}
	requestID, ok := safeRequestID(strings.TrimSuffix(markerName, ReadySuffix))
	if !ok {
		result.Reason = "unsafe ACK marker filename"
		return result, nil
	}
	result.RequestID = requestID
	result.RequestPath = filepath.Join(i.acks, requestID+RequestSuffix)
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
		result.Reason = "ACK marker disappeared before import"
		return result, nil
	}
	if markerInfo.Size() != 0 {
		result.Reason = "ready marker must be empty"
		return result, nil
	}
	requestInfo, err := os.Stat(result.RequestPath)
	if err != nil {
		result.Reason = "ACK file disappeared before import"
		return result, nil
	}
	if requestInfo.Size() > int64(domain.MaxSerializedRequestBytes) {
		result.Reason = ErrMailboxRequestTooLarge.Error()
		return result, nil
	}
	raw, err := readBounded(result.RequestPath, domain.MaxSerializedRequestBytes)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	if err := domain.ValidateSerializedRequest(raw); err != nil {
		result.Reason = fmt.Sprintf("%v: %v", ErrMailboxRequestTooLarge, err)
		return result, nil
	}
	value, err := decodeOneJSON(raw)
	if err != nil {
		result.Reason = fmt.Sprintf("%v: malformed JSON: %v", ErrMailboxAckSchema, err)
		return result, nil
	}
	if err := i.schema.Validate(value); err != nil {
		result.Reason = fmt.Sprintf("%v: %v", ErrMailboxAckSchema, err)
		return result, nil
	}
	var wire struct {
		RequestID              string `json:"request_id"`
		ResponseRevision       int64  `json:"response_revision"`
		AvailableEventSequence *int64 `json:"available_event_sequence"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		result.Reason = fmt.Sprintf("%v: decode ACK: %v", ErrMailboxAckSchema, err)
		return result, nil
	}
	if wire.RequestID != requestID {
		result.Reason = "ACK request_id does not match marker filename"
		return result, nil
	}
	if _, ok := safeRequestID(wire.RequestID); !ok {
		result.Reason = "ACK request_id is not a safe basename"
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	ref, refErr := store.NewMailboxExchangeRef(i.mailboxID, wire.RequestID)
	if refErr != nil {
		result.Reason = refErr.Error()
		return result, nil
	}
	record, err := i.authority.AcknowledgeMailboxExchangeInMailbox(ctx, ref, store.MailboxAcknowledgement{
		MailboxID: i.mailboxID, RequestID: wire.RequestID, ResponseRevision: wire.ResponseRevision,
		AvailableEventSequence: wire.AvailableEventSequence,
	})
	if err != nil {
		if errors.Is(err, store.ErrMailboxAckInvalid) || errors.Is(err, store.ErrMailboxAckConflict) || errors.Is(err, store.ErrMailboxExchangeNotFound) {
			result.Reason = err.Error()
			return result, nil
		}
		return result, fmt.Errorf("record durable mailbox ACK: %w", err)
	}
	if record.AcknowledgedAt == nil {
		return result, fmt.Errorf("record durable mailbox ACK: store returned no receipt")
	}
	result.Status = ResultAccepted
	result.Durable = true
	if err := removeMailboxPair(i.acks, requestID); err != nil {
		return result, err
	}
	result.PairRemoved = true
	return result, nil
}

func compileAckSchema() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const resource = "https://remote-session-runner.invalid/src/internal/mailbox/schemas/v1/ack.schema.json"
	data, err := fs.ReadFile(ackSchemaFS, "schemas/v1/ack.schema.json")
	if err != nil {
		return nil, fmt.Errorf("%w: read ACK schema: %v", ErrMailboxAckSchema, err)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode ACK schema: %v", ErrMailboxAckSchema, err)
	}
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, fmt.Errorf("%w: register ACK schema: %v", ErrMailboxAckSchema, err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("%w: compile ACK schema: %v", ErrMailboxAckSchema, err)
	}
	return schema, nil
}
