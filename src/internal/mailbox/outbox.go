package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	ErrOutboxConfiguration = errors.New("mailbox outbox configuration is invalid")
	ErrOutboxPath          = errors.New("mailbox outbox path is invalid")
	ErrOutboxResponse      = errors.New("mailbox outbox response is invalid")
)

// Outbox atomically publishes owner-only response JSON files.
type Outbox struct{ root string }

func NewOutbox(root string) (*Outbox, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return nil, ErrOutboxConfiguration
	}
	if err := ensureOwnerDirectory(root); err != nil {
		return nil, err
	}
	outbox := filepath.Join(root, "outbox")
	if err := ensureOwnerDirectory(outbox); err != nil {
		return nil, err
	}
	return &Outbox{root: outbox}, nil
}

func (o *Outbox) Path(requestID string) (string, error) {
	if o == nil || o.root == "" {
		return "", ErrOutboxConfiguration
	}
	if _, ok := safeRequestID(requestID); !ok {
		return "", fmt.Errorf("%w: request ID", ErrOutboxPath)
	}
	return filepath.Join(o.root, requestID+".json"), nil
}

// Replace publishes bytes through a synced same-directory temporary file and
// atomic rename. Readers see the old complete file or the new complete file.
func (o *Outbox) Replace(ctx context.Context, requestID string, response []byte) error {
	path, err := o.Path(requestID)
	if err != nil {
		return err
	}
	if err := domain.ValidateSerializedRequest(response); err != nil {
		return fmt.Errorf("%w: %v", ErrOutboxResponse, err)
	}
	if !json.Valid(response) {
		return fmt.Errorf("%w: response JSON is invalid", ErrOutboxResponse)
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(o.root, ".response-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temporary: %v", ErrOutboxPath, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(MailboxFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: temporary mode: %v", ErrOutboxPath, err)
	}
	if _, err := temporary.Write(response); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: write temporary: %v", ErrOutboxResponse, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: sync temporary: %v", ErrOutboxPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close temporary: %v", ErrOutboxPath, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("%w: replace response: %v", ErrOutboxPath, err)
	}
	if err := syncDirectory(o.root); err != nil {
		return err
	}
	return nil
}

func (o *Outbox) Read(requestID string) ([]byte, error) {
	path, err := o.Path(requestID)
	if err != nil {
		return nil, err
	}
	if err := validateMailboxFile(path, false); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(domain.MaxSerializedRequestBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > domain.MaxSerializedRequestBytes || !json.Valid(data) {
		return nil, ErrOutboxResponse
	}
	return data, nil
}

// Remove durably removes one safe owner-only response file. A missing file is
// treated as an already-completed unlink after a crash.
func (o *Outbox) Remove(ctx context.Context, requestID string) error {
	path, err := o.Path(requestID)
	if err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return removeOwnedMailboxFile(path, o.root)
}

// Projector republishes the durable SQLite response snapshot after a crash.
type Projector struct {
	Authority  *store.AuthorityStore
	Outbox     *Outbox
	EventFiles *EventFiles
}

func (p Projector) Publish(ctx context.Context, requestID string) error {
	if p.Authority == nil || p.Outbox == nil {
		return ErrOutboxConfiguration
	}
	record, err := p.Authority.GetMailboxExchange(ctx, requestID)
	if err != nil {
		return err
	}
	if len(record.ResponseBytes) == 0 {
		return fmt.Errorf("%w: response revision is not published", ErrOutboxResponse)
	}
	if err := p.Authority.EnsureMailboxResponsePublishable(ctx, requestID); err != nil {
		return err
	}
	return p.Outbox.Replace(ctx, requestID, record.ResponseBytes)
}

// PublishCommand repairs/publishes the event prefix before replacing the
// response file. Repeating it after a crash is safe: the event image and
// response bytes are both regenerated from durable records, and the response
// advertises only its stored frozen cursor.
func (p Projector) PublishCommand(ctx context.Context, requestID string, commandID domain.CommandID) error {
	if p.Authority == nil || p.Outbox == nil || p.EventFiles == nil {
		return ErrOutboxConfiguration
	}
	record, err := p.Authority.GetMailboxExchange(ctx, requestID)
	if err != nil {
		return err
	}
	if len(record.ResponseBytes) == 0 {
		return fmt.Errorf("%w: response revision is not published", ErrOutboxResponse)
	}
	if record.AvailableEventSequence == nil || *record.AvailableEventSequence < 1 {
		return fmt.Errorf("%w: response has no event cursor", ErrOutboxResponse)
	}
	var response struct {
		CommandID  string `json:"command_id"`
		EventsFile string `json:"events_file"`
	}
	if err := json.Unmarshal(record.ResponseBytes, &response); err != nil {
		return fmt.Errorf("%w: response JSON: %v", ErrOutboxResponse, err)
	}
	expectedReference, err := commandEventsFileReference(commandID)
	if err != nil || response.CommandID != string(commandID) || response.EventsFile != expectedReference {
		return fmt.Errorf("%w: event-file reference does not match command", ErrOutboxResponse)
	}
	if err := p.Authority.EnsureMailboxResponsePublishable(ctx, requestID); err != nil {
		return err
	}
	if err := p.Authority.BindMailboxEventFileReference(ctx, requestID, commandID); err != nil {
		return err
	}
	eventProjector := EventProjector{Authority: p.Authority}
	eventBytes, cursor, err := eventProjector.ProjectThrough(ctx, commandID, *record.AvailableEventSequence)
	if err != nil {
		return err
	}
	if cursor != *record.AvailableEventSequence {
		return fmt.Errorf("%w: projected cursor %d differs from response cursor %d", ErrOutboxResponse, cursor, *record.AvailableEventSequence)
	}
	if err := p.EventFiles.Replace(ctx, commandID, eventBytes); err != nil {
		return err
	}
	return p.Outbox.Replace(ctx, requestID, record.ResponseBytes)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open directory for sync: %v", ErrOutboxPath, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("%w: sync directory: %v", ErrOutboxPath, err)
	}
	return nil
}
