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
	"strings"
	"syscall"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrDiagnosticConfiguration = errors.New("mailbox diagnostic configuration is invalid")
	ErrDiagnosticPath          = errors.New("mailbox diagnostic path is invalid")
	ErrDiagnosticInvalid       = errors.New("mailbox diagnostic is invalid")

	// diagnosticBeforeOpenHook is a package-private deterministic race seam
	// for tests. Production leaves it nil. It runs after the private artifact
	// passes Lstat checks and before its O_NOFOLLOW descriptor open.
	diagnosticBeforeOpenHook func()
)

//go:embed schemas/v1/diagnostic.schema.json
var diagnosticSchemaFS embed.FS

// DiagnosticFiles owns private ingress-validation diagnostic projections. A
// diagnostic is deliberately separate from an accepted-exchange response: it
// has no operation, response revision, event cursor, or acknowledgement.
type DiagnosticFiles struct {
	root   string
	schema *jsonschema.Schema
}

// NewDiagnosticFiles creates the owner-only private diagnostic directory
// beneath one mailbox root.
func NewDiagnosticFiles(root string) (*DiagnosticFiles, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return nil, ErrDiagnosticConfiguration
	}
	if err := ensureOwnerDirectory(root); err != nil {
		return nil, err
	}
	diagnostics := filepath.Join(root, "diagnostics")
	if err := ensureOwnerDirectory(diagnostics); err != nil {
		return nil, err
	}
	schema, err := compileDiagnosticSchema()
	if err != nil {
		return nil, err
	}
	return &DiagnosticFiles{root: diagnostics, schema: schema}, nil
}

// Path returns the sole private artifact path for one trusted request ID.
func (f *DiagnosticFiles) Path(requestID string) (string, error) {
	if f == nil || f.root == "" {
		return "", ErrDiagnosticConfiguration
	}
	if _, ok := safeRequestID(requestID); !ok {
		return "", fmt.Errorf("%w: request ID", ErrDiagnosticPath)
	}
	return filepath.Join(f.root, requestID+RequestSuffix), nil
}

// Replace atomically publishes one already-frozen, schema-valid diagnostic at
// exact mode 0600. It never follows or replaces an unsafe existing artifact.
func (f *DiagnosticFiles) Replace(ctx context.Context, requestID string, data []byte) error {
	path, err := f.Path(requestID)
	if err != nil {
		return err
	}
	if err := f.validate(requestID, data); err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if info, err := os.Lstat(path); err == nil {
		if !safePrivateMailboxFileInfo(info) {
			return ErrDiagnosticPath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect existing diagnostic", ErrDiagnosticPath)
	}

	temporary, err := os.CreateTemp(f.root, ".diagnostic-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temporary", ErrDiagnosticPath)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(MailboxFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: temporary mode", ErrDiagnosticPath)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: write temporary", ErrDiagnosticInvalid)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: sync temporary", ErrDiagnosticPath)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close temporary", ErrDiagnosticPath)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("%w: replace diagnostic", ErrDiagnosticPath)
	}
	return syncDirectory(f.root)
}

// Read returns a complete, private, schema-valid diagnostic. It intentionally
// gives callers no access to partially written or unsafe filesystem entries.
func (f *DiagnosticFiles) Read(requestID string) ([]byte, error) {
	path, err := f.Path(requestID)
	if err != nil {
		return nil, err
	}
	file, err := openDiagnosticPrivateFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(domain.MaxSerializedRequestBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read diagnostic", ErrDiagnosticPath)
	}
	if err := f.validate(requestID, data); err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

// openDiagnosticPrivateFile rejects a replacement symlink and proves that the
// opened descriptor is the same safe private inode observed by Lstat. A
// diagnostic projection is never allowed to read a file selected after its
// safety check.
func openDiagnosticPrivateFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: diagnostic is missing", ErrDiagnosticPath)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: inspect diagnostic", ErrDiagnosticPath)
	}
	if !safePrivateMailboxFileInfo(info) {
		return nil, ErrDiagnosticPath
	}
	if hook := diagnosticBeforeOpenHook; hook != nil {
		diagnosticBeforeOpenHook = nil
		hook()
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrDiagnosticPath
	}
	file := os.NewFile(uintptr(fd), path)
	opened, err := file.Stat()
	if err != nil || !safePrivateMailboxFileInfo(opened) || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, ErrDiagnosticPath
	}
	return file, nil
}

// Matches reports whether the current private artifact is exactly the frozen
// ledger image. A missing artifact is a recoverable false result; an unsafe or
// malformed artifact is an error and is never overwritten blindly.
func (f *DiagnosticFiles) Matches(requestID string, want []byte) (bool, error) {
	path, err := f.Path(requestID)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("%w: inspect diagnostic", ErrDiagnosticPath)
	}
	got, err := f.Read(requestID)
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, want), nil
}

// Remove durably removes one safe private diagnostic. A missing diagnostic is
// an already-completed unlink after a process stop.
func (f *DiagnosticFiles) Remove(ctx context.Context, requestID string) error {
	path, err := f.Path(requestID)
	if err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return removeOwnedMailboxFile(path, f.root)
}

func (f *DiagnosticFiles) validate(requestID string, data []byte) error {
	if f == nil || f.schema == nil {
		return ErrDiagnosticConfiguration
	}
	if err := domain.ValidateSerializedRequest(data); err != nil || !json.Valid(data) {
		return ErrDiagnosticInvalid
	}
	value, err := decodeOneJSON(data)
	if err != nil {
		return ErrDiagnosticInvalid
	}
	if err := f.schema.Validate(value); err != nil {
		return ErrDiagnosticInvalid
	}
	var wire struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.RequestID != requestID {
		return ErrDiagnosticInvalid
	}
	return nil
}

func compileDiagnosticSchema() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const resource = "https://remote-session-runner.invalid/src/internal/mailbox/schemas/v1/diagnostic.schema.json"
	data, err := fs.ReadFile(diagnosticSchemaFS, "schemas/v1/diagnostic.schema.json")
	if err != nil {
		return nil, fmt.Errorf("%w: read schema", ErrDiagnosticConfiguration)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode schema", ErrDiagnosticConfiguration)
	}
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, fmt.Errorf("%w: register schema", ErrDiagnosticConfiguration)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("%w: compile schema", ErrDiagnosticConfiguration)
	}
	return schema, nil
}
