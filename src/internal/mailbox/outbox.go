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
	if err := ctx.Err(); err != nil {
		return err
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

// Projector republishes the durable SQLite response snapshot after a crash.
type Projector struct {
	Authority *store.AuthorityStore
	Outbox    *Outbox
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
