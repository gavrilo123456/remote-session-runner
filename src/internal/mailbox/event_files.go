package mailbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrEventFileConfiguration = errors.New("mailbox event-file configuration is invalid")
	ErrEventFilePath          = errors.New("mailbox event-file path is invalid")
	ErrEventFileInvalid       = errors.New("mailbox event file is invalid")
	ErrEventFileIncomplete    = errors.New("mailbox event file has an incomplete trailing line")
)

// EventFiles owns the synced per-command NDJSON projection directory.
type EventFiles struct{ root string }

func NewEventFiles(root string) (*EventFiles, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return nil, ErrEventFileConfiguration
	}
	if err := ensureOwnerDirectory(root); err != nil {
		return nil, err
	}
	events := filepath.Join(root, "events")
	if err := ensureOwnerDirectory(events); err != nil {
		return nil, err
	}
	return &EventFiles{root: events}, nil
}

func (f *EventFiles) Path(commandID domain.CommandID) (string, error) {
	if f == nil || f.root == "" {
		return "", ErrEventFileConfiguration
	}
	path, err := commandEventsFileReference(commandID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrEventFilePath, err)
	}
	return filepath.Join(f.root, filepath.Base(path)), nil
}

// Replace writes a complete event-file image through a synced temporary file
// and same-directory atomic rename. A reader sees an old complete file or a
// new complete file, never a partial append.
func (f *EventFiles) Replace(ctx context.Context, commandID domain.CommandID, data []byte) error {
	path, err := f.Path(commandID)
	if err != nil {
		return err
	}
	if _, err := validateProjectedEventFile(commandID, data); err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(f.root, ".events-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temporary: %v", ErrEventFilePath, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(MailboxFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: temporary mode: %v", ErrEventFilePath, err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: write temporary: %v", ErrEventFileInvalid, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: sync temporary: %v", ErrEventFilePath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close temporary: %v", ErrEventFilePath, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("%w: replace event file: %v", ErrEventFilePath, err)
	}
	return syncDirectory(f.root)
}

// Read validates that an event file is newline-terminated and contiguous.
func (f *EventFiles) Read(commandID domain.CommandID) ([]byte, int64, error) {
	path, err := f.Path(commandID)
	if err != nil {
		return nil, 0, err
	}
	if err := validateMailboxFile(path, false); err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, 0, err
	}
	cursor, err := validateProjectedEventFile(commandID, data)
	if err != nil {
		return nil, 0, err
	}
	return data, cursor, nil
}

// Remove durably removes one safe owner-only command event file. A missing
// file is treated as an already-completed unlink after a crash.
func (f *EventFiles) Remove(ctx context.Context, commandID domain.CommandID) error {
	path, err := f.Path(commandID)
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

// Publish renders durable events and atomically replaces the event file. It
// is safe to repeat after a crash before response publication.
func (p EventProjector) Publish(ctx context.Context, files *EventFiles, commandID domain.CommandID) (int64, error) {
	if files == nil {
		return 0, ErrEventFileConfiguration
	}
	data, cursor, err := p.Project(ctx, commandID)
	if err != nil {
		return 0, err
	}
	if err := files.Replace(ctx, commandID, data); err != nil {
		return 0, err
	}
	return cursor, nil
}

func validateProjectedEventFile(commandID domain.CommandID, data []byte) (int64, error) {
	if len(data) == 0 || !bytes.HasSuffix(data, []byte{'\n'}) {
		return 0, ErrEventFileIncomplete
	}
	lines := bytes.Split(data, []byte{'\n'})
	var expected int64 = 1
	for index, line := range lines[:len(lines)-1] {
		if len(line) == 0 {
			return 0, fmt.Errorf("%w: empty line %d", ErrEventFileInvalid, index+1)
		}
		var event mailboxEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return 0, fmt.Errorf("%w: line %d JSON: %v", ErrEventFileInvalid, index+1, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return 0, fmt.Errorf("%w: line %d has multiple JSON values", ErrEventFileInvalid, index+1)
		}
		if event.CommandID != commandID || event.Sequence != expected || event.Type == "" || event.Timestamp == "" {
			return 0, fmt.Errorf("%w: line %d identity/sequence", ErrEventFileInvalid, index+1)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
			return 0, fmt.Errorf("%w: line %d timestamp: %v", ErrEventFileInvalid, index+1, err)
		}
		if event.Type == "stdout" || event.Type == "stderr" {
			if event.ByteCount <= 0 || event.ByteCount > mailboxEventChunkBytes {
				return 0, fmt.Errorf("%w: line %d byte count", ErrEventFileInvalid, index+1)
			}
			var raw []byte
			switch event.Encoding {
			case "utf8":
				if !utf8.ValidString(event.Text) || int64(len([]byte(event.Text))) != event.ByteCount {
					return 0, fmt.Errorf("%w: line %d UTF-8 payload", ErrEventFileInvalid, index+1)
				}
				raw = []byte(event.Text)
			case "base64":
				decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
				if err != nil || int64(len(decoded)) != event.ByteCount {
					return 0, fmt.Errorf("%w: line %d base64 payload", ErrEventFileInvalid, index+1)
				}
				raw = decoded
			default:
				return 0, fmt.Errorf("%w: line %d output encoding", ErrEventFileInvalid, index+1)
			}
			if len(raw) == 0 {
				return 0, fmt.Errorf("%w: line %d empty output", ErrEventFileInvalid, index+1)
			}
		} else if event.Encoding != "" || event.Text != "" || event.DataBase64 != "" || event.ByteCount != 0 {
			return 0, fmt.Errorf("%w: line %d non-output payload", ErrEventFileInvalid, index+1)
		}
		expected++
	}
	return expected - 1, nil
}
