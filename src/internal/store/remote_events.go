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
	ErrRemoteGapRecord     = errors.New("remote event gap record is invalid")
	ErrRemoteGapConflict   = errors.New("remote event gap record conflicts with stored confirmation")
	ErrRemoteGapNotFound   = errors.New("remote event gap record was not found")
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
	Gap          *RemoteEventGapRecord
}

// RemoteEventGapRecord is the durable exception to a contiguous mirror. It
// records the highest available prefix and the independently confirmed
// terminal range without advancing the event cursor or inventing missing
// events.
type RemoteEventGapRecord struct {
	CommandID               domain.CommandID
	MissingFrom             int64
	MissingTo               int64
	AvailableSequence       int64
	FinalSequence           int64
	TerminalState           domain.CommandState
	OutputComplete          bool
	OutputUnavailableReason string
	ConfirmedAt             time.Time
}

// RecordRemoteEventGap durably records one independently confirmed terminal
// command whose remote event range cannot be recovered. It is idempotent for
// the exact same confirmation and never advances the contiguous event cursor.
func (s *AuthorityStore) RecordRemoteEventGap(ctx context.Context, input RemoteEventGapRecord) (RemoteEventGapRecord, error) {
	if s == nil || s.db == nil {
		return RemoteEventGapRecord{}, ErrNilDatabase
	}
	validated, err := validateRemoteEventGap(input)
	if err != nil {
		return RemoteEventGapRecord{}, err
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteEventGapRecord, error) {
		stored, err := readRemoteEventGapOnConnection(ctx, connection, validated.CommandID)
		if err == nil {
			if sameRemoteEventGap(stored, validated) {
				return stored, nil
			}
			return RemoteEventGapRecord{}, fmt.Errorf("%w: command %s", ErrRemoteGapConflict, validated.CommandID)
		}
		if !errors.Is(err, ErrRemoteGapNotFound) {
			return RemoteEventGapRecord{}, err
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_event_gaps (
 command_id, missing_from, missing_to, available_sequence, final_sequence,
 terminal_state, output_complete, output_unavailable_reason, confirmed_at
) VALUES (?, ?, ?, ?, ?, ?, 0, 'remote_event_gap', ?)
`, string(validated.CommandID), validated.MissingFrom, validated.MissingTo, validated.AvailableSequence,
			validated.FinalSequence, string(validated.TerminalState), formatStoredTime(validated.ConfirmedAt)); err != nil {
			return RemoteEventGapRecord{}, fmt.Errorf("insert remote event gap: %w", err)
		}
		return validated, nil
	})
}

// GetRemoteEventGap returns a previously confirmed irrecoverable gap.
func (s *AuthorityStore) GetRemoteEventGap(ctx context.Context, commandID domain.CommandID) (RemoteEventGapRecord, error) {
	validated, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return RemoteEventGapRecord{}, err
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteEventGapRecord, error) {
		return readRemoteEventGapOnConnection(ctx, connection, validated)
	})
}

func validateRemoteEventGap(input RemoteEventGapRecord) (RemoteEventGapRecord, error) {
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return RemoteEventGapRecord{}, fmt.Errorf("%w: command ID: %v", ErrRemoteGapRecord, err)
	}
	if input.MissingFrom <= 0 || input.MissingTo < input.MissingFrom || input.AvailableSequence < 0 || input.AvailableSequence != input.MissingFrom-1 || input.FinalSequence < input.MissingTo {
		return RemoteEventGapRecord{}, fmt.Errorf("%w: sequence range", ErrRemoteGapRecord)
	}
	if !input.TerminalState.Valid() || !input.TerminalState.IsTerminal() {
		return RemoteEventGapRecord{}, fmt.Errorf("%w: terminal state %q", ErrRemoteGapRecord, input.TerminalState)
	}
	if input.OutputComplete || input.OutputUnavailableReason != "remote_event_gap" {
		return RemoteEventGapRecord{}, fmt.Errorf("%w: incomplete output reason", ErrRemoteGapRecord)
	}
	if input.ConfirmedAt.IsZero() {
		return RemoteEventGapRecord{}, fmt.Errorf("%w: confirmation timestamp", ErrRemoteGapRecord)
	}
	input.CommandID = commandID
	input.ConfirmedAt = input.ConfirmedAt.UTC()
	return input, nil
}

func readRemoteEventGapOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID) (RemoteEventGapRecord, error) {
	var record RemoteEventGapRecord
	var state, reason, confirmed string
	var outputComplete int64
	err := connection.QueryRowContext(ctx, `
SELECT missing_from, missing_to, available_sequence, final_sequence,
       terminal_state, output_complete, output_unavailable_reason, confirmed_at
FROM local_remote_event_gaps WHERE command_id = ?
`, string(commandID)).Scan(&record.MissingFrom, &record.MissingTo, &record.AvailableSequence,
		&record.FinalSequence, &state, &outputComplete, &reason, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteEventGapRecord{}, ErrRemoteGapNotFound
	}
	if err != nil {
		return RemoteEventGapRecord{}, fmt.Errorf("read remote event gap: %w", err)
	}
	record.CommandID = commandID
	record.TerminalState = domain.CommandState(state)
	record.OutputComplete = outputComplete != 0
	record.OutputUnavailableReason = reason
	record.ConfirmedAt, err = parseStoredTime(confirmed)
	if err != nil {
		return RemoteEventGapRecord{}, fmt.Errorf("%w: confirmation timestamp: %v", ErrRemoteGapRecord, err)
	}
	validated, err := validateRemoteEventGap(record)
	if err != nil {
		return RemoteEventGapRecord{}, err
	}
	return validated, nil
}

func sameRemoteEventGap(left, right RemoteEventGapRecord) bool {
	return left.CommandID == right.CommandID && left.MissingFrom == right.MissingFrom && left.MissingTo == right.MissingTo && left.AvailableSequence == right.AvailableSequence && left.FinalSequence == right.FinalSequence && left.TerminalState == right.TerminalState && !left.OutputComplete && !right.OutputComplete && left.OutputUnavailableReason == right.OutputUnavailableReason
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
