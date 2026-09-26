package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

const (
	// DefaultRunningCommandLimit is the selected four-live-command host limit.
	DefaultRunningCommandLimit = 4
	schedulerHostKey           = "authority"
)

var (
	// ErrCommandSlotsFull means all durable host command slots are live.
	ErrCommandSlotsFull = errors.New("running command slots full")
	// ErrCommandSlotNotFound means a command has no durable slot reservation.
	ErrCommandSlotNotFound = errors.New("command slot not found")
	// ErrCommandSlotNotReleasable means stop confirmation arrived before a
	// terminal command state was recorded.
	ErrCommandSlotNotReleasable = errors.New("command slot is not releasable")
	// ErrCommandSlotCorrupt means a durable slot record failed validation.
	ErrCommandSlotCorrupt = errors.New("command slot record is corrupt")
)

// CommandSlotRecord is the durable host-capacity reservation for one command.
// A slot remains live until both stop confirmation and release are recorded.
type CommandSlotRecord struct {
	CommandID       domain.CommandID
	HostKey         string
	ReservedAt      time.Time
	StopConfirmedAt *time.Time
	ReleasedAt      *time.Time
}

// StartNextEligibleCommand atomically chooses the oldest eligible queued
// command, reserves one live host slot, moves its session to busy, appends
// command_started, and sets the command running. No runtime is started here;
// the caller starts it only after this durable transaction commits.
func (s *AuthorityStore) StartNextEligibleCommand(ctx context.Context, maxSlots int) (CommandRecord, error) {
	if maxSlots == 0 {
		maxSlots = DefaultRunningCommandLimit
	}
	if maxSlots < 1 {
		return CommandRecord{}, fmt.Errorf("%w: max slots must be positive", ErrCommandSlotsFull)
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	now := s.now().UTC()
	var publishedEvent *CommandEventRecord
	record, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (CommandRecord, error) {
		var liveSlots int
		if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_command_slots
WHERE host_key = ? AND stop_confirmed_at IS NULL
`, schedulerHostKey).Scan(&liveSlots); err != nil {
			return CommandRecord{}, fmt.Errorf("count live command slots: %w", err)
		}
		if liveSlots >= maxSlots {
			return CommandRecord{}, ErrCommandSlotsFull
		}

		rows, err := connection.QueryContext(ctx, `
SELECT c.command_id, c.session_id
FROM exec_commands c
JOIN exec_sessions s ON s.session_id = c.session_id
WHERE c.state = ? AND s.state = ?
ORDER BY c.created_at, c.session_id, c.ordinal
`, string(domain.CommandStateQueued), string(domain.SessionStateReady))
		if err != nil {
			return CommandRecord{}, fmt.Errorf("query scheduler candidates: %w", err)
		}
		var candidates []schedulerCandidate
		for rows.Next() {
			var commandIDValue, sessionIDValue string
			if err := rows.Scan(&commandIDValue, &sessionIDValue); err != nil {
				_ = rows.Close()
				return CommandRecord{}, fmt.Errorf("scan scheduler candidate: %w", err)
			}
			candidates = append(candidates, schedulerCandidate{commandID: commandIDValue, sessionID: sessionIDValue})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return CommandRecord{}, fmt.Errorf("iterate scheduler candidates: %w", err)
		}
		if err := rows.Close(); err != nil {
			return CommandRecord{}, fmt.Errorf("close scheduler candidates: %w", err)
		}
		for _, candidateRow := range candidates {
			commandID, err := domain.NewCommandID(candidateRow.commandID)
			if err != nil {
				return CommandRecord{}, fmt.Errorf("%w: command ID: %v", ErrCommandOrderCorrupt, err)
			}
			sessionID, err := domain.NewSessionID(candidateRow.sessionID)
			if err != nil {
				return CommandRecord{}, fmt.Errorf("%w: session ID: %v", ErrCommandOrderCorrupt, err)
			}
			candidate, err := nextEligibleCommandOnConnection(ctx, connection, sessionID)
			if err != nil {
				if errors.Is(err, ErrCommandNotEligible) {
					continue
				}
				return CommandRecord{}, err
			}
			if candidate.CommandID != commandID {
				continue
			}
			if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_command_slots (command_id, host_key, reserved_at)
VALUES (?, ?, ?)
`, string(commandID), schedulerHostKey, formatStoredTime(now)); err != nil {
				return CommandRecord{}, fmt.Errorf("reserve command slot: %w", err)
			}
			sequence, err := nextEventSequenceOnConnection(ctx, connection, commandID)
			if err != nil {
				return CommandRecord{}, err
			}
			if err := insertCommandEventOnConnection(ctx, connection, commandID, sequence, "command_started", nil, 0, now); err != nil {
				return CommandRecord{}, err
			}
			if _, err := connection.ExecContext(ctx, `
UPDATE exec_commands SET state = ?, updated_at = ?
WHERE command_id = ? AND state = ?
`, string(domain.CommandStateRunning), formatStoredTime(now), string(commandID), string(domain.CommandStateQueued)); err != nil {
				return CommandRecord{}, fmt.Errorf("start command: %w", err)
			}
			if err := transitionSessionOnConnection(ctx, connection, sessionID, domain.SessionStateBusy, "command_started", now); err != nil {
				return CommandRecord{}, err
			}
			event := CommandEventRecord{CommandID: commandID, Sequence: sequence, Type: "command_started", Payload: []byte{}, ByteCount: 0, OccurredAt: now}
			publishedEvent = &event
			return readCommandOnConnection(ctx, connection, commandID)
		}
		return CommandRecord{}, ErrCommandNotEligible
	})
	if err != nil {
		return CommandRecord{}, err
	}
	if publishedEvent != nil {
		s.publishCommandEvent(*publishedEvent)
	}
	return record, nil
}

type schedulerCandidate struct {
	commandID string
	sessionID string
}

// CountLiveCommandSlots counts host reservations whose stop boundary is not
// confirmed. Released slots are never counted again.
func (s *AuthorityStore) CountLiveCommandSlots(ctx context.Context) (int, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire command slot connection: %w", err)
	}
	defer connection.Close()
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_command_slots
WHERE host_key = ? AND stop_confirmed_at IS NULL
`, schedulerHostKey).Scan(&count); err != nil {
		return 0, fmt.Errorf("count live command slots: %w", err)
	}
	return count, nil
}

// GetCommandSlot returns one durable command slot reservation.
func (s *AuthorityStore) GetCommandSlot(ctx context.Context, id domain.CommandID) (CommandSlotRecord, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return CommandSlotRecord{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return CommandSlotRecord{}, fmt.Errorf("acquire command slot connection: %w", err)
	}
	defer connection.Close()
	return readCommandSlotOnConnection(ctx, connection, validatedID)
}

// ConfirmCommandSlotRelease records a proven stop/teardown boundary. It is
// idempotent, but refuses to release a slot while its command is nonterminal.
func (s *AuthorityStore) ConfirmCommandSlotRelease(ctx context.Context, id domain.CommandID) error {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		var state string
		if err := connection.QueryRowContext(ctx, "SELECT state FROM exec_commands WHERE command_id = ?", string(validatedID)).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrCommandNotFound
			}
			return struct{}{}, fmt.Errorf("read command for slot release: %w", err)
		}
		if !domain.CommandState(state).IsTerminal() {
			return struct{}{}, ErrCommandSlotNotReleasable
		}
		result, err := connection.ExecContext(ctx, `
UPDATE exec_command_slots
SET stop_confirmed_at = ?, released_at = ?
WHERE command_id = ? AND stop_confirmed_at IS NULL
`, formatStoredTime(now), formatStoredTime(now), string(validatedID))
		if err != nil {
			return struct{}{}, fmt.Errorf("release command slot: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return struct{}{}, fmt.Errorf("read command slot release result: %w", err)
		}
		if changed > 0 {
			return struct{}{}, nil
		}
		var ignored string
		if err := connection.QueryRowContext(ctx, "SELECT command_id FROM exec_command_slots WHERE command_id = ?", string(validatedID)).Scan(&ignored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrCommandSlotNotFound
			}
			return struct{}{}, fmt.Errorf("check command slot release: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

func nextEligibleCommandOnConnection(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID) (CommandRecord, error) {
	var sessionState string
	if err := connection.QueryRowContext(ctx, "SELECT state FROM exec_sessions WHERE session_id = ?", string(sessionID)).Scan(&sessionState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandRecord{}, ErrSessionNotFound
		}
		return CommandRecord{}, fmt.Errorf("read scheduler session: %w", err)
	}
	if sessionState != string(domain.SessionStateReady) {
		return CommandRecord{}, ErrCommandNotEligible
	}
	commands, err := readCommandOrderOnConnection(ctx, connection, sessionID)
	if err != nil {
		return CommandRecord{}, err
	}
	for _, command := range commands {
		switch command.State {
		case domain.CommandStateSucceeded, domain.CommandStateFailed, domain.CommandStateCancelled, domain.CommandStateTimedOut, domain.CommandStateRejected, domain.CommandStateLost:
			continue
		case domain.CommandStateQueued:
			return readCommandOnConnection(ctx, connection, command.CommandID)
		case domain.CommandStateRunning, domain.CommandStateCancelling:
			return CommandRecord{}, ErrCommandNotEligible
		default:
			return CommandRecord{}, fmt.Errorf("%w: command %q has state %q", ErrCommandOrderCorrupt, command.CommandID, command.State)
		}
	}
	return CommandRecord{}, ErrCommandNotEligible
}

func transitionSessionOnConnection(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID, next domain.SessionState, reason string, now time.Time) error {
	var currentValue string
	if err := connection.QueryRowContext(ctx, "SELECT state FROM exec_sessions WHERE session_id = ?", string(sessionID)).Scan(&currentValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("read scheduler session state: %w", err)
	}
	current := domain.SessionState(currentValue)
	if err := domain.ValidateSessionTransition(current, next); err != nil {
		return err
	}
	if _, err := validateLifecycleReason(reason); err != nil {
		return err
	}
	var sequence int64
	if err := connection.QueryRowContext(ctx, "SELECT COALESCE(MAX(lifecycle_sequence), 0) + 1 FROM exec_session_lifecycle WHERE session_id = ?", string(sessionID)).Scan(&sequence); err != nil {
		return fmt.Errorf("read scheduler lifecycle sequence: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `
UPDATE exec_sessions SET state = ?, updated_at = ?
WHERE session_id = ? AND state = ?
`, string(next), formatStoredTime(now), string(sessionID), currentValue); err != nil {
		return fmt.Errorf("update scheduler session state: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_session_lifecycle (session_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)
`, string(sessionID), sequence, currentValue, string(next), reason, formatStoredTime(now)); err != nil {
		return fmt.Errorf("insert scheduler lifecycle: %w", err)
	}
	return nil
}

func readCommandSlotOnConnection(ctx context.Context, connection *sql.Conn, id domain.CommandID) (CommandSlotRecord, error) {
	var record CommandSlotRecord
	var commandID, hostKey, reservedAt string
	var stopConfirmedAt, releasedAt sql.NullString
	if err := connection.QueryRowContext(ctx, `
SELECT command_id, host_key, reserved_at, stop_confirmed_at, released_at
FROM exec_command_slots WHERE command_id = ?
`, string(id)).Scan(&commandID, &hostKey, &reservedAt, &stopConfirmedAt, &releasedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandSlotRecord{}, ErrCommandSlotNotFound
		}
		return CommandSlotRecord{}, fmt.Errorf("read command slot: %w", err)
	}
	validatedID, err := domain.NewCommandID(commandID)
	if err != nil || validatedID != id || hostKey == "" {
		return CommandSlotRecord{}, fmt.Errorf("%w: command=%q host=%q", ErrCommandSlotCorrupt, commandID, hostKey)
	}
	record.CommandID, record.HostKey = id, hostKey
	if record.ReservedAt, err = parseStoredTime(reservedAt); err != nil {
		return CommandSlotRecord{}, fmt.Errorf("%w: reserved_at: %v", ErrCommandSlotCorrupt, err)
	}
	if stopConfirmedAt.Valid {
		value, err := parseStoredTime(stopConfirmedAt.String)
		if err != nil {
			return CommandSlotRecord{}, fmt.Errorf("%w: stop_confirmed_at: %v", ErrCommandSlotCorrupt, err)
		}
		record.StopConfirmedAt = &value
	}
	if releasedAt.Valid {
		value, err := parseStoredTime(releasedAt.String)
		if err != nil {
			return CommandSlotRecord{}, fmt.Errorf("%w: released_at: %v", ErrCommandSlotCorrupt, err)
		}
		record.ReleasedAt = &value
	}
	if (record.StopConfirmedAt == nil) != (record.ReleasedAt == nil) {
		return CommandSlotRecord{}, fmt.Errorf("%w: release timestamps must be paired", ErrCommandSlotCorrupt)
	}
	return record, nil
}
