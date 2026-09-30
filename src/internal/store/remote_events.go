package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrRemoteEventBatch            = errors.New("remote event batch is invalid")
	ErrRemoteEventGap              = errors.New("remote event sequence has a gap")
	ErrRemoteEventConflict         = errors.New("remote event duplicate conflicts with stored bytes")
	ErrRemoteEventTerminal         = errors.New("remote event stream crosses a terminal boundary")
	ErrRemoteEventNotFound         = errors.New("remote event was not found")
	ErrRemoteEventRetentionExpired = errors.New("mirrored remote event output retention expired")
	ErrRemoteGapRecord             = errors.New("remote event gap record is invalid")
	ErrRemoteGapConflict           = errors.New("remote event gap record conflicts with stored confirmation")
	ErrRemoteGapNotFound           = errors.New("remote event gap record was not found")
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

// RemoteEventLifecycle is the semantically validated shape of one contiguous
// retained command-event prefix. A prefix may be nonterminal because a command
// is still running or because a later terminal suffix was unavailable.
//
// It is deliberately derived from the event history instead of from a remote
// status projection. Callers use it to prove that a projection cannot claim a
// fuller or differently terminated result than the immutable event evidence.
type RemoteEventLifecycle struct {
	Started         bool
	OutputTruncated bool
	TerminalState   *domain.CommandState
}

// RemoteEventPrefixSnapshot is the integrity-only view of the contiguous
// remote event rows retained by the Mac. It deliberately remains available to
// recovery validation after output retention expires: user-facing reads still
// hide expired output, but a later strict target status must not contradict
// immutable rows that were already mirrored locally.
type RemoteEventPrefixSnapshot struct {
	Cursor    int64
	Lifecycle RemoteEventLifecycle
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
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (RemoteEventGapRecord, error) {
		cursor, err := remoteEventCursorOnConnection(ctx, connection, validated.CommandID)
		if err != nil {
			return RemoteEventGapRecord{}, err
		}
		if cursor != validated.AvailableSequence {
			return RemoteEventGapRecord{}, fmt.Errorf("%w: command %s cursor %d differs from available sequence %d", ErrRemoteGapConflict, validated.CommandID, cursor, validated.AvailableSequence)
		}
		terminal, err := remoteEventPrefixHasTerminalOnConnection(ctx, connection, validated.CommandID, cursor)
		if err != nil {
			return RemoteEventGapRecord{}, err
		}
		if terminal {
			return RemoteEventGapRecord{}, fmt.Errorf("%w: command %s retained prefix already contains a terminal event", ErrRemoteGapConflict, validated.CommandID)
		}
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
		if err := normalizeStoredRemoteProjectionsForEventGap(ctx, connection, validated); err != nil {
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

func remoteEventCursorOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID) (int64, error) {
	var cursor int64
	err := connection.QueryRowContext(ctx, `SELECT last_sequence FROM local_remote_event_cursors WHERE command_id = ?`, string(commandID)).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read remote event cursor: %w", err)
	}
	return cursor, nil
}

// normalizeStoredRemoteProjectionsForEventGap preserves the locally proven
// output boundary. A later target read can confirm the terminal state, but it
// cannot turn an irrecoverable event gap into complete output.
func normalizeStoredRemoteProjectionsForEventGap(ctx context.Context, connection *sql.Conn, gap RemoteEventGapRecord) error {
	command, err := readRemoteCommandProjectionOnConnection(ctx, connection, gap.CommandID)
	if err == nil {
		if err := validateProjectionAgainstRemoteEventGap(command.State, command.FinalEventSequence, gap); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE local_remote_command_projections
SET output_complete = 0, output_unavailable_reason = 'remote_event_gap'
WHERE command_id = ?
`, string(gap.CommandID)); err != nil {
			return fmt.Errorf("normalize remote command event gap: %w", err)
		}
	} else if !errors.Is(err, ErrRemoteProjectionNotFound) {
		return err
	}

	rows, err := connection.QueryContext(ctx, `SELECT job_id FROM local_remote_job_projections WHERE command_id = ?`, string(gap.CommandID))
	if err != nil {
		return fmt.Errorf("list remote jobs for event gap: %w", err)
	}
	defer rows.Close()
	var jobIDs []domain.JobID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("scan remote job for event gap: %w", err)
		}
		jobID, err := domain.NewJobID(raw)
		if err != nil {
			return fmt.Errorf("read remote job for event gap: %w", err)
		}
		jobIDs = append(jobIDs, jobID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate remote jobs for event gap: %w", err)
	}
	for _, jobID := range jobIDs {
		job, err := readRemoteJobProjectionOnConnection(ctx, connection, jobID)
		if err != nil {
			return err
		}
		if job.CommandState == nil {
			return fmt.Errorf("%w: remote job %s has no command state", ErrRemoteGapConflict, jobID)
		}
		if err := validateProjectionAgainstRemoteEventGap(*job.CommandState, job.FinalEventSequence, gap); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE local_remote_job_projections
SET output_complete = 0, output_unavailable_reason = 'remote_event_gap'
WHERE job_id = ?
`, string(jobID)); err != nil {
			return fmt.Errorf("normalize remote job event gap: %w", err)
		}
	}
	return nil
}

func validateProjectionAgainstRemoteEventGap(state domain.CommandState, final *int64, gap RemoteEventGapRecord) error {
	if !state.IsTerminal() || state != gap.TerminalState || final == nil || *final != gap.FinalSequence {
		return fmt.Errorf("%w: projection does not match terminal event gap", ErrRemoteGapConflict)
	}
	return nil
}

func normalizeIncomingRemoteCommandProjectionForEventGap(ctx context.Context, connection *sql.Conn, input RemoteCommandProjection) (RemoteCommandProjection, error) {
	gap, err := readRemoteEventGapOnConnection(ctx, connection, input.CommandID)
	if errors.Is(err, ErrRemoteGapNotFound) {
		return input, nil
	}
	if err != nil {
		return RemoteCommandProjection{}, err
	}
	if err := validateProjectionAgainstRemoteEventGap(input.State, input.FinalEventSequence, gap); err != nil {
		return RemoteCommandProjection{}, err
	}
	input.OutputComplete = false
	input.OutputUnavailableReason = "remote_event_gap"
	return input, nil
}

func normalizeIncomingRemoteJobProjectionForEventGap(ctx context.Context, connection *sql.Conn, input RemoteJobProjection) (RemoteJobProjection, error) {
	gap, err := readRemoteEventGapOnConnection(ctx, connection, input.CommandID)
	if errors.Is(err, ErrRemoteGapNotFound) {
		return input, nil
	}
	if err != nil {
		return RemoteJobProjection{}, err
	}
	if input.CommandState == nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: remote job has no command state", ErrRemoteGapConflict)
	}
	if err := validateProjectionAgainstRemoteEventGap(*input.CommandState, input.FinalEventSequence, gap); err != nil {
		return RemoteJobProjection{}, err
	}
	input.OutputComplete = false
	input.OutputUnavailableReason = "remote_event_gap"
	return input, nil
}

// GetRemoteEventGap returns a previously confirmed irrecoverable gap.
func (s *AuthorityStore) GetRemoteEventGap(ctx context.Context, commandID domain.CommandID) (RemoteEventGapRecord, error) {
	validated, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return RemoteEventGapRecord{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (RemoteEventGapRecord, error) {
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
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (RemoteEventMirrorResult, error) {
		var result RemoteEventMirrorResult
		result.CommandID = validated[0].CommandID
		if _, err := readRemoteEventGapOnConnection(ctx, connection, result.CommandID); err == nil {
			return result, fmt.Errorf("%w: command %s has an irrecoverable event gap", ErrRemoteGapConflict, result.CommandID)
		} else if !errors.Is(err, ErrRemoteGapNotFound) {
			return result, err
		}
		last, err := remoteEventCursorOnConnection(ctx, connection, result.CommandID)
		if err != nil {
			return result, err
		}
		result.LastSequence = last
		prefix, lifecycle, err := remoteEventPrefixOnConnection(ctx, connection, result.CommandID, last)
		if err != nil {
			return result, err
		}
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
			if lifecycle.TerminalState != nil {
				return result, fmt.Errorf("%w: command %s event sequence %d follows terminal prefix", ErrRemoteEventTerminal, event.CommandID, event.Sequence)
			}
			candidate := append(append([]RemoteEventRecord(nil), prefix...), event)
			candidateLifecycle, inspectErr := InspectRemoteEventLifecycle(candidate)
			if inspectErr != nil {
				return result, inspectErr
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
			prefix, lifecycle = candidate, candidateLifecycle
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

// remoteEventPrefixHasTerminalOnConnection proves that the persisted prefix
// is contiguous and that any terminal event is its final member. It returns
// true only for a valid terminal-closed prefix. Legacy or corrupted prefixes
// with a terminal event followed by another event are rejected rather than
// being extended or converted into a retention gap.
func remoteEventPrefixHasTerminalOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID, cursor int64) (bool, error) {
	_, lifecycle, err := remoteEventPrefixOnConnection(ctx, connection, commandID, cursor)
	if err != nil {
		return false, err
	}
	return lifecycle.TerminalState != nil, nil
}

// remoteEventPrefixOnConnection returns exactly the contiguous retained prefix
// owned by cursor and rejects both storage corruption and legacy event streams
// that violate the command lifecycle grammar.
func remoteEventPrefixOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID, cursor int64) ([]RemoteEventRecord, RemoteEventLifecycle, error) {
	events, err := readRemoteEventsOnConnection(ctx, connection, commandID, 0)
	if err != nil {
		return nil, RemoteEventLifecycle{}, err
	}
	if int64(len(events)) != cursor || (cursor > 0 && (len(events) == 0 || events[len(events)-1].Sequence != cursor)) {
		return nil, RemoteEventLifecycle{}, fmt.Errorf("%w: command %s cursor %d does not match retained prefix", ErrRemoteEventGap, commandID, cursor)
	}
	lifecycle, err := InspectRemoteEventLifecycle(events)
	if err != nil {
		return nil, RemoteEventLifecycle{}, err
	}
	return events, lifecycle, nil
}

// GetRemoteEventCursor returns the highest contiguous mirrored sequence. A
// command with no mirrored events has cursor zero.
func (s *AuthorityStore) GetRemoteEventCursor(ctx context.Context, commandID domain.CommandID) (int64, error) {
	validated, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return 0, err
	}
	var cursor int64
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
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

// InspectRemoteEventPrefix validates the raw durable event prefix, including
// when the command projection marks user-visible output as retention-expired.
// It is for reconciliation integrity checks only; callers that serve output
// must use ListRemoteEvents or GetRemoteCommandWithEvents, which preserve the
// retention boundary.
func (s *AuthorityStore) InspectRemoteEventPrefix(ctx context.Context, commandID domain.CommandID) (RemoteEventPrefixSnapshot, error) {
	validated, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return RemoteEventPrefixSnapshot{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (RemoteEventPrefixSnapshot, error) {
		cursor, err := remoteEventCursorOnConnection(ctx, connection, validated)
		if err != nil {
			return RemoteEventPrefixSnapshot{}, err
		}
		_, lifecycle, err := remoteEventPrefixOnConnection(ctx, connection, validated, cursor)
		if err != nil {
			return RemoteEventPrefixSnapshot{}, err
		}
		return RemoteEventPrefixSnapshot{Cursor: cursor, Lifecycle: lifecycle}, nil
	})
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
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]RemoteEventRecord, error) {
		var outputUnavailable string
		projectionErr := connection.QueryRowContext(ctx, `SELECT output_unavailable_reason FROM local_remote_command_projections WHERE command_id = ?`, string(validated)).Scan(&outputUnavailable)
		if projectionErr != nil && !errors.Is(projectionErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("read mirrored event retention state: %w", projectionErr)
		}
		if outputUnavailable == "retention_expired" {
			return nil, ErrRemoteEventRetentionExpired
		}
		return readRemoteEventsOnConnection(ctx, connection, validated, afterSequence)
	})
}

func readRemoteEventsOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID, afterSequence int64) ([]RemoteEventRecord, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT sequence, event_type, payload, byte_count, occurred_at
FROM local_remote_events WHERE command_id = ? AND sequence > ? ORDER BY sequence
`, string(commandID), afterSequence)
	if err != nil {
		return nil, fmt.Errorf("list remote events: %w", err)
	}
	defer rows.Close()
	result := make([]RemoteEventRecord, 0)
	want := afterSequence + 1
	for rows.Next() {
		event, err := scanRemoteEvent(rows, commandID)
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
}

func validateRemoteEventBatch(events []RemoteEventRecord) ([]RemoteEventRecord, error) {
	result := make([]RemoteEventRecord, len(events))
	for index, input := range events {
		validated, err := validateRemoteEventRecord(input)
		if err != nil {
			return nil, err
		}
		result[index] = validated
	}
	return result, nil
}

func validateRemoteEventRecord(input RemoteEventRecord) (RemoteEventRecord, error) {
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return RemoteEventRecord{}, fmt.Errorf("%w: command ID: %v", ErrRemoteEventBatch, err)
	}
	if input.Sequence <= 0 || !validRemoteEventType(input.Type) || input.OccurredAt.IsZero() {
		return RemoteEventRecord{}, fmt.Errorf("%w: event envelope", ErrRemoteEventBatch)
	}
	if input.ByteCount < 0 {
		return RemoteEventRecord{}, fmt.Errorf("%w: negative byte count", ErrRemoteEventBatch)
	}
	if input.Type == "stdout" || input.Type == "stderr" {
		if input.ByteCount <= 0 || int64(len(input.Payload)) != input.ByteCount || len(input.Payload) > 16<<10 {
			return RemoteEventRecord{}, fmt.Errorf("%w: output byte count or size", ErrRemoteEventBatch)
		}
	} else if len(input.Payload) != 0 || input.ByteCount != 0 {
		return RemoteEventRecord{}, fmt.Errorf("%w: non-output payload", ErrRemoteEventBatch)
	}
	return RemoteEventRecord{CommandID: commandID, Sequence: input.Sequence, Type: input.Type, Payload: append([]byte{}, input.Payload...), ByteCount: input.ByteCount, OccurredAt: input.OccurredAt.UTC()}, nil
}

// InspectRemoteEventLifecycle validates the complete ordered event prefix for
// one command. It accepts an unfinished prefix, but never accepts an output
// event before command_started, a terminal state that could not occur from the
// retained lifecycle, or events after a terminal boundary.
func InspectRemoteEventLifecycle(events []RemoteEventRecord) (RemoteEventLifecycle, error) {
	var result RemoteEventLifecycle
	if len(events) == 0 {
		return result, nil
	}
	var commandID domain.CommandID
	for index, input := range events {
		event, err := validateRemoteEventRecord(input)
		if err != nil {
			return RemoteEventLifecycle{}, err
		}
		wantSequence := int64(index + 1)
		if event.Sequence != wantSequence {
			return RemoteEventLifecycle{}, fmt.Errorf("%w: command event sequence %d follows %d", ErrRemoteEventGap, event.Sequence, wantSequence-1)
		}
		if index == 0 {
			commandID = event.CommandID
			if event.Type != "command_queued" {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: sequence one must be command_queued", ErrRemoteEventBatch)
			}
		} else {
			if event.CommandID != commandID {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: mixed command IDs", ErrRemoteEventBatch)
			}
			if event.Type == "command_queued" {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command_queued must be sequence one", ErrRemoteEventBatch)
			}
		}
		if result.TerminalState != nil {
			return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s event sequence %d follows terminal event", ErrRemoteEventTerminal, commandID, event.Sequence)
		}
		switch event.Type {
		case "command_queued":
			// The sequence-one requirement above is the entire queued rule.
		case "command_started":
			if result.Started {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s started more than once", ErrRemoteEventBatch, commandID)
			}
			result.Started = true
		case "stdout", "stderr":
			if !result.Started {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s emitted output before start", ErrRemoteEventBatch, commandID)
			}
		case "output_truncated":
			if !result.Started {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s truncated output before start", ErrRemoteEventBatch, commandID)
			}
			if result.OutputTruncated {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s reported output truncation more than once", ErrRemoteEventBatch, commandID)
			}
			result.OutputTruncated = true
		default:
			state, terminal := remoteTerminalStateForEventType(event.Type)
			if !terminal {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: unexpected remote event type %q", ErrRemoteEventBatch, event.Type)
			}
			// D-01 permits rejection only from queued. Once command_started is
			// retained, a later rejection would contradict the authoritative
			// command-state transition table and must not become terminal proof.
			if state == domain.CommandStateRejected && result.Started {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s was rejected after command_started", ErrRemoteEventBatch, commandID)
			}
			if terminalStateRequiresStart(state) && !result.Started {
				return RemoteEventLifecycle{}, fmt.Errorf("%w: command %s terminal state %q requires command_started", ErrRemoteEventBatch, commandID, state)
			}
			result.TerminalState = &state
		}
	}
	return result, nil
}

func validRemoteEventType(value string) bool {
	switch value {
	case "command_queued", "command_started", "stdout", "stderr", "output_truncated", "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func isRemoteTerminalEventType(value string) bool {
	_, ok := remoteTerminalStateForEventType(value)
	return ok
}

func remoteTerminalStateForEventType(value string) (domain.CommandState, bool) {
	switch value {
	case "command_succeeded":
		return domain.CommandStateSucceeded, true
	case "command_failed":
		return domain.CommandStateFailed, true
	case "command_cancelled":
		return domain.CommandStateCancelled, true
	case "command_timed_out":
		return domain.CommandStateTimedOut, true
	case "command_rejected":
		return domain.CommandStateRejected, true
	case "command_lost":
		return domain.CommandStateLost, true
	default:
		return "", false
	}
}

func terminalStateRequiresStart(state domain.CommandState) bool {
	switch state {
	case domain.CommandStateSucceeded, domain.CommandStateFailed, domain.CommandStateTimedOut, domain.CommandStateLost:
		return true
	case domain.CommandStateCancelled, domain.CommandStateRejected:
		return false
	default:
		return true
	}
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
