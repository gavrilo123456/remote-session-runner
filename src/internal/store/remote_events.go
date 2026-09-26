package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrRemoteEventBatch    = errors.New("remote event batch is invalid")
	ErrRemoteEventGap      = errors.New("remote event sequence has a gap")
	ErrRemoteEventConflict = errors.New("remote event duplicate conflicts with stored bytes")
	ErrRemoteEventNotFound = errors.New("remote event was not found")
)

// RemoteEventRecord is one immutable event mirrored from the remote command
// authority. It is keyed by the remote (command_id, sequence) pair.
type RemoteEventRecord struct {
	CommandID  domain.CommandID
	Sequence   int64
	Type       string
	Payload    []byte
	ByteCount  int64
	OccurredAt time.Time
}

// RemoteEventMirrorResult reports the cursor after one atomic mirror batch.
// Duplicate events are acknowledged without rewriting their stored bytes.
type RemoteEventMirrorResult struct {
	CommandID    domain.CommandID
	LastSequence int64
	Mirrored     int
	Duplicates   int
}

// MirrorRemoteEvents atomically inserts a contiguous event batch and advances
// its cursor in the same SQLite transaction. A replayed identical event is a
// deduplicated success; a conflicting duplicate or sequence gap rolls back the
// complete batch and leaves the prior cursor intact.
func (s *AuthorityStore) MirrorRemoteEvents(ctx context.Context, events []RemoteEventRecord) (RemoteEventMirrorResult, error) {
	if s == nil || s.db == nil {
		return RemoteEventMirrorResult{}, ErrNilDatabase
	}
	if len(events) == 0 {
		return RemoteEventMirrorResult{}, ErrRemoteEventBatch
	}
	validated, err := validateRemoteEventBatch(events)
	if err != nil {
		return RemoteEventMirrorResult{}, err
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteEventMirrorResult, error) {
		var result RemoteEventMirrorResult
		result.CommandID = validated[0].CommandID
		var last int64
		err := connection.QueryRowContext(ctx, `SELECT last_sequence FROM local_remote_event_cursors WHERE command_id = ?`, string(result.CommandID)).Scan(&last)
		if errors.Is(err, sql.ErrNoRows) {
			last = 0
		} else if err != nil {
			return result, fmt.Errorf("read remote event cursor: %w", err)
		}
		result.LastSequence = last
		for _, event := range validated {
			if event.CommandID != result.CommandID {
				return result, fmt.Errorf("%w: mixed command IDs", ErrRemoteEventBatch)
			}
			if event.Sequence <= last {
				stored, readErr := readRemoteEventOnConnection(ctx, connection, event.CommandID, event.Sequence)
				if errors.Is(readErr, ErrRemoteEventNotFound) {
					return result, fmt.Errorf("%w: cursor points past sequence %d", ErrRemoteEventGap, event.Sequence)
				}
				if readErr != nil {
					return result, readErr
				}
				if !sameRemoteEvent(stored, event) {
					return result, fmt.Errorf("%w: command %s sequence %d", ErrRemoteEventConflict, event.CommandID, event.Sequence)
				}
				result.Duplicates++
				continue
			}
			if event.Sequence != last+1 {
				return result, fmt.Errorf("%w: got %d after %d", ErrRemoteEventGap, event.Sequence, last)
			}
			if _, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_events (command_id, sequence, event_type, payload, byte_count, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)
`, string(event.CommandID), event.Sequence, event.Type, event.Payload, event.ByteCount, formatStoredTime(event.OccurredAt)); err != nil {
				return result, fmt.Errorf("insert remote event: %w", err)
			}
			last = event.Sequence
			result.LastSequence = last
			result.Mirrored++
		}
		if result.Mirrored > 0 {
			if _, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_event_cursors (command_id, last_sequence, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(command_id) DO UPDATE SET last_sequence = excluded.last_sequence, updated_at = excluded.updated_at
`, string(result.CommandID), last, formatStoredTime(now)); err != nil {
				return result, fmt.Errorf("advance remote event cursor: %w", err)
			}
		}
		return result, nil
	})
}

// GetRemoteEventCursor returns the highest contiguous mirrored sequence. A
// command with no mirrored events has cursor zero.
func (s *AuthorityStore) GetRemoteEventCursor(ctx context.Context, commandID domain.CommandID) (int64, error) {
	validated, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return 0, err
	}
	var cursor int64
	_, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		err := connection.QueryRowContext(ctx, `SELECT last_sequence FROM local_remote_event_cursors WHERE command_id = ?`, string(validated)).Scan(&cursor)
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, nil
		}
		if err != nil {
			return struct{}{}, fmt.Errorf("read remote event cursor: %w", err)
		}
		return struct{}{}, nil
	})
	return cursor, err
}

// ListRemoteEvents returns mirrored events strictly after a cursor and checks
// that the stored range remains contiguous.
func (s *AuthorityStore) ListRemoteEvents(ctx context.Context, commandID domain.CommandID, afterSequence int64) ([]RemoteEventRecord, error) {
	validated, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return nil, err
	}
	if afterSequence < 0 {
		return nil, fmt.Errorf("%w: negative cursor", ErrRemoteEventBatch)
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]RemoteEventRecord, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT sequence, event_type, payload, byte_count, occurred_at
FROM local_remote_events WHERE command_id = ? AND sequence > ? ORDER BY sequence
`, string(validated), afterSequence)
		if err != nil {
			return nil, fmt.Errorf("list remote events: %w", err)
		}
		defer rows.Close()
		result := make([]RemoteEventRecord, 0)
		want := afterSequence + 1
		for rows.Next() {
			event, err := scanRemoteEvent(rows, validated)
			if err != nil {
				return nil, err
			}
			if event.Sequence != want {
				return nil, fmt.Errorf("%w: got %d after %d", ErrRemoteEventGap, event.Sequence, want-1)
			}
			result = append(result, event)
			want++
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate remote events: %w", err)
		}
		return result, nil
	})
}

func validateRemoteEventBatch(events []RemoteEventRecord) ([]RemoteEventRecord, error) {
	result := make([]RemoteEventRecord, len(events))
	for index, input := range events {
		commandID, err := domain.NewCommandID(string(input.CommandID))
		if err != nil {
			return nil, fmt.Errorf("%w: command ID: %v", ErrRemoteEventBatch, err)
		}
		if input.Sequence <= 0 || strings.TrimSpace(input.Type) == "" || strings.IndexByte(input.Type, 0) >= 0 || len(input.Type) > 256 || input.OccurredAt.IsZero() {
			return nil, fmt.Errorf("%w: event envelope", ErrRemoteEventBatch)
		}
		if input.ByteCount < 0 {
			return nil, fmt.Errorf("%w: negative byte count", ErrRemoteEventBatch)
		}
		if input.Type == "stdout" || input.Type == "stderr" {
			if input.ByteCount <= 0 || int64(len(input.Payload)) != input.ByteCount || len(input.Payload) > 16<<10 {
				return nil, fmt.Errorf("%w: output byte count or size", ErrRemoteEventBatch)
			}
		} else if len(input.Payload) != 0 || input.ByteCount != 0 {
			return nil, fmt.Errorf("%w: non-output payload", ErrRemoteEventBatch)
		}
		result[index] = RemoteEventRecord{CommandID: commandID, Sequence: input.Sequence, Type: input.Type, Payload: append([]byte{}, input.Payload...), ByteCount: input.ByteCount, OccurredAt: input.OccurredAt.UTC()}
	}
	return result, nil
}

func readRemoteEventOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID, sequence int64) (RemoteEventRecord, error) {
	row := connection.QueryRowContext(ctx, `SELECT event_type, payload, byte_count, occurred_at FROM local_remote_events WHERE command_id = ? AND sequence = ?`, string(commandID), sequence)
	var event RemoteEventRecord
	var payload []byte
	var occurred string
	if err := row.Scan(&event.Type, &payload, &event.ByteCount, &occurred); errors.Is(err, sql.ErrNoRows) {
		return RemoteEventRecord{}, ErrRemoteEventNotFound
	} else if err != nil {
		return RemoteEventRecord{}, fmt.Errorf("read remote event: %w", err)
	}
	event.CommandID, event.Sequence, event.Payload = commandID, sequence, append([]byte(nil), payload...)
	event.OccurredAt, _ = parseStoredTime(occurred)
	return event, nil
}

type remoteEventScanner interface{ Scan(...any) error }

func scanRemoteEvent(scanner remoteEventScanner, commandID domain.CommandID) (RemoteEventRecord, error) {
	var event RemoteEventRecord
	var payload []byte
	var occurred string
	if err := scanner.Scan(&event.Sequence, &event.Type, &payload, &event.ByteCount, &occurred); err != nil {
		return RemoteEventRecord{}, fmt.Errorf("scan remote event: %w", err)
	}
	event.CommandID = commandID
	event.Payload = append([]byte(nil), payload...)
	parsed, err := parseStoredTime(occurred)
	if err != nil {
		return RemoteEventRecord{}, err
	}
	event.OccurredAt = parsed
	return event, nil
}

func sameRemoteEvent(left, right RemoteEventRecord) bool {
	return left.CommandID == right.CommandID && left.Sequence == right.Sequence && left.Type == right.Type && left.ByteCount == right.ByteCount && left.OccurredAt.Equal(right.OccurredAt) && bytes.Equal(left.Payload, right.Payload)
}
