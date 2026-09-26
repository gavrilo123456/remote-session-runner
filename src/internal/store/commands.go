package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	// ErrInvalidCommand means command acceptance input failed the store boundary.
	ErrInvalidCommand = errors.New("invalid command")
	// ErrCommandNotFound means no authoritative command exists for the ID.
	ErrCommandNotFound = errors.New("command not found")
	// ErrCommandSessionState means the parent session cannot accept a command.
	ErrCommandSessionState = errors.New("session cannot accept command")
	// ErrCommandPayloadCorrupt means durable script bytes no longer match their hash.
	ErrCommandPayloadCorrupt = errors.New("command script payload is corrupt")
	// ErrCommandEvent means a durable command event failed store validation.
	ErrCommandEvent = errors.New("invalid command event")
	// ErrCommandOrderCorrupt means authoritative ordinals are not contiguous.
	ErrCommandOrderCorrupt = errors.New("authoritative command order is corrupt")
	// ErrCommandNotEligible means no queued command can run for the session.
	ErrCommandNotEligible = errors.New("no eligible command")
	// ErrCommandTerminal means a terminal command cannot receive another event.
	ErrCommandTerminal = errors.New("command is already terminal")
	// ErrCommandReplayGap means a requested event range is not contiguous.
	ErrCommandReplayGap = errors.New("command event replay has a gap")
	// ErrCommandTransition means a command state/event transaction is invalid.
	ErrCommandTransition = errors.New("invalid command state transition")
)

const submitCommandOperation = "submit_command"

// CommandAcceptance is the authoritative command-acceptance input. P015 adds
// the reusable idempotency lookup; this phase still persists the canonical
// request hash with the command row.
type CommandAcceptance struct {
	CommandID            domain.CommandID
	SessionID            domain.SessionID
	RequestHash          domain.CanonicalHash
	IdempotencyKey       string
	IdempotencyRetention time.Duration
	Script               string
	Timeout              time.Duration
	IntentOrdinal        int64
}

// CommandRecord is the authoritative command snapshot. ScriptBytes is the
// exact validated UTF-8 byte sequence committed at acceptance.
type CommandRecord struct {
	CommandID          domain.CommandID
	SessionID          domain.SessionID
	Ordinal            int64
	IntentOrdinal      *int64
	RequestHash        domain.CanonicalHash
	ScriptBytes        []byte
	ScriptSHA256       []byte
	State              domain.CommandState
	Timeout            time.Duration
	ExitCode           *int
	FinalEventSequence *int64
	OutputTruncated    bool
	OutputComplete     bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// CommandEventRecord is one durable event in a command's ordered stream.
type CommandEventRecord struct {
	CommandID  domain.CommandID
	Sequence   int64
	Type       string
	Payload    []byte
	ByteCount  int64
	OccurredAt time.Time
}

// CommandEventAppend is one raw event appended after command acceptance.
// Output payloads retain exact bytes and require a matching positive count.
type CommandEventAppend struct {
	CommandID domain.CommandID
	Type      string
	Payload   []byte
	ByteCount int64
}

// CommandTransition describes one atomic command state/event transition.
// Terminal transitions set the final event sequence and output completeness.
type CommandTransition struct {
	CommandID       domain.CommandID
	NextState       domain.CommandState
	ExitCode        *int
	OutputComplete  bool
	OutputTruncated bool
}

// AcceptCommand atomically allocates the next authoritative session ordinal,
// inserts the immutable queued command, and inserts event sequence 1. It does
// not call a scheduler or start a runtime.
func (s *AuthorityStore) AcceptCommand(ctx context.Context, input CommandAcceptance) (record CommandRecord, duplicate bool, err error) {
	validated, err := validateCommandAcceptance(input)
	if err != nil {
		return CommandRecord{}, false, err
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	now := s.now().UTC()
	returnRecord, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (CommandRecord, error) {
		var sessionState, controllerType, controllerID string
		if err := connection.QueryRowContext(ctx,
			"SELECT state, controller_type, controller_id FROM exec_sessions WHERE session_id = ?", string(validated.SessionID)).Scan(&sessionState, &controllerType, &controllerID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return CommandRecord{}, ErrSessionNotFound
			}
			return CommandRecord{}, fmt.Errorf("read command session: %w", err)
		}
		controllerIDValue, err := domain.NewControllerID(controllerID)
		if err != nil {
			return CommandRecord{}, fmt.Errorf("read command controller: %w", err)
		}
		controller, err := domain.NewControllerIdentity(domain.ControllerType(controllerType), controllerIDValue)
		if err != nil {
			return CommandRecord{}, fmt.Errorf("read command controller: %w", err)
		}
		existing, found, err := lookupIdempotencyOnConnection(ctx, connection, controller, submitCommandOperation, validated.IdempotencyKey, now)
		if err != nil {
			return CommandRecord{}, err
		}
		if found {
			if domain.CompareIdempotency(existing.Hash, validated.RequestHash) == domain.IdempotencyConflict {
				return CommandRecord{}, ErrIdempotencyConflict
			}
			existingID, err := domain.NewCommandID(existing.ResourceID)
			if err != nil {
				return CommandRecord{}, fmt.Errorf("read idempotent command ID: %w", err)
			}
			record, err := readCommandOnConnection(ctx, connection, existingID)
			if err != nil {
				return CommandRecord{}, fmt.Errorf("read idempotent command: %w", err)
			}
			duplicate = true
			return record, nil
		}
		if sessionState != string(domain.SessionStateReady) && sessionState != string(domain.SessionStateBusy) {
			return CommandRecord{}, fmt.Errorf("%w: current state %q", ErrCommandSessionState, sessionState)
		}

		ordinal, err := nextCommandOrdinalOnConnection(ctx, connection, validated.SessionID)
		if err != nil {
			return CommandRecord{}, err
		}
		scriptBytes := []byte(validated.Script)
		scriptHash := sha256.Sum256(scriptBytes)
		var intentOrdinal any
		if validated.IntentOrdinal != 0 {
			intentOrdinal = validated.IntentOrdinal
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_commands (
    command_id, session_id, ordinal, intent_ordinal,
    request_hash_version, request_hash, script_bytes, script_sha256,
    state, timeout_ns, output_truncated, output_complete, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
`, string(validated.CommandID), string(validated.SessionID), ordinal, intentOrdinal,
			validated.RequestHash.Version(), validated.RequestHash.SHA256(), scriptBytes, scriptHash[:],
			string(domain.CommandStateQueued), int64(validated.Timeout), formatStoredTime(now), formatStoredTime(now)); err != nil {
			return CommandRecord{}, fmt.Errorf("insert command: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_command_events (command_id, sequence, event_type, payload, byte_count, occurred_at)
VALUES (?, 1, 'command_queued', X'', 0, ?)
`, string(validated.CommandID), formatStoredTime(now)); err != nil {
			return CommandRecord{}, fmt.Errorf("insert queued command event: %w", err)
		}
		idempotencyInput, err := validateIdempotencyInput(controller, submitCommandOperation, validated.IdempotencyKey, validated.RequestHash, string(validated.CommandID), validated.IdempotencyRetention)
		if err != nil {
			return CommandRecord{}, err
		}
		idempotencyInput.CreatedAt, idempotencyInput.ExpiresAt = now, now.Add(validated.IdempotencyRetention)
		if err := recordIdempotencyOnConnection(ctx, connection, idempotencyInput); err != nil {
			return CommandRecord{}, err
		}
		return readCommandOnConnection(ctx, connection, validated.CommandID)
	})
	if err != nil {
		return CommandRecord{}, false, err
	}
	if !duplicate {
		s.publishCommandEvent(CommandEventRecord{
			CommandID:  returnRecord.CommandID,
			Sequence:   1,
			Type:       "command_queued",
			Payload:    []byte{},
			ByteCount:  0,
			OccurredAt: returnRecord.CreatedAt,
		})
	}
	return returnRecord, duplicate, nil
}

// AppendCommandEvent appends one non-lifecycle output event after the latest
// contiguous event. It shares the command write transaction and refuses any
// append after a terminal state.
func (s *AuthorityStore) AppendCommandEvent(ctx context.Context, input CommandEventAppend) (CommandEventRecord, error) {
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return CommandEventRecord{}, err
	}
	if !validCommandEventType(input.Type) || input.Type == "command_queued" || input.Type == "command_started" || isTerminalCommandEvent(input.Type) {
		return CommandEventRecord{}, fmt.Errorf("%w: event type %q", ErrCommandEvent, input.Type)
	}
	if input.Type == "stdout" || input.Type == "stderr" {
		if input.ByteCount <= 0 || int64(len(input.Payload)) != input.ByteCount {
			return CommandEventRecord{}, fmt.Errorf("%w: output byte count does not match payload", ErrCommandEvent)
		}
	} else if len(input.Payload) != 0 || input.ByteCount != 0 {
		return CommandEventRecord{}, fmt.Errorf("%w: non-output event carries bytes", ErrCommandEvent)
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	now := s.now().UTC()
	event, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (CommandEventRecord, error) {
		command, err := readCommandOnConnection(ctx, connection, commandID)
		if err != nil {
			return CommandEventRecord{}, err
		}
		if command.State.IsTerminal() {
			return CommandEventRecord{}, ErrCommandTerminal
		}
		if command.State != domain.CommandStateRunning && command.State != domain.CommandStateCancelling {
			return CommandEventRecord{}, fmt.Errorf("%w: output event while command is %q", ErrCommandTransition, command.State)
		}
		sequence, err := nextEventSequenceOnConnection(ctx, connection, commandID)
		if err != nil {
			return CommandEventRecord{}, err
		}
		if err := insertCommandEventOnConnection(ctx, connection, commandID, sequence, input.Type, input.Payload, input.ByteCount, now); err != nil {
			return CommandEventRecord{}, err
		}
		return CommandEventRecord{CommandID: commandID, Sequence: sequence, Type: input.Type, Payload: append([]byte(nil), input.Payload...), ByteCount: input.ByteCount, OccurredAt: now}, nil
	})
	if err != nil {
		return CommandEventRecord{}, err
	}
	s.publishCommandEvent(event)
	return event, nil
}

// TransitionCommand atomically validates a D-01 command transition, appends
// its lifecycle/terminal event, and updates terminal metadata. The internal
// cancelling state is persisted without a separate v1 wire event; its later
// terminal transition appends the event that closes the command stream.
func (s *AuthorityStore) TransitionCommand(ctx context.Context, input CommandTransition) (CommandRecord, error) {
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return CommandRecord{}, err
	}
	if !input.NextState.Valid() {
		return CommandRecord{}, fmt.Errorf("%w: invalid next state %q", ErrCommandTransition, input.NextState)
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	now := s.now().UTC()
	var publishedEvent *CommandEventRecord
	record, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (CommandRecord, error) {
		var currentValue string
		if err := connection.QueryRowContext(ctx, "SELECT state FROM exec_commands WHERE command_id = ?", string(commandID)).Scan(&currentValue); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return CommandRecord{}, ErrCommandNotFound
			}
			return CommandRecord{}, fmt.Errorf("read command state: %w", err)
		}
		current := domain.CommandState(currentValue)
		if err := domain.ValidateCommandTransition(current, input.NextState); err != nil {
			return CommandRecord{}, fmt.Errorf("%w: %v", ErrCommandTransition, err)
		}
		if input.NextState == domain.CommandStateCancelling {
			if _, err := connection.ExecContext(ctx, "UPDATE exec_commands SET state = ?, updated_at = ? WHERE command_id = ?", string(input.NextState), formatStoredTime(now), string(commandID)); err != nil {
				return CommandRecord{}, fmt.Errorf("persist cancelling state: %w", err)
			}
			return readCommandOnConnection(ctx, connection, commandID)
		}
		eventType, ok := commandEventTypeForState(input.NextState)
		if !ok {
			return CommandRecord{}, fmt.Errorf("%w: no event type for state %q", ErrCommandTransition, input.NextState)
		}
		sequence, err := nextEventSequenceOnConnection(ctx, connection, commandID)
		if err != nil {
			return CommandRecord{}, err
		}
		if err := insertCommandEventOnConnection(ctx, connection, commandID, sequence, eventType, nil, 0, now); err != nil {
			return CommandRecord{}, err
		}
		event := CommandEventRecord{CommandID: commandID, Sequence: sequence, Type: eventType, Payload: []byte{}, ByteCount: 0, OccurredAt: now}
		publishedEvent = &event
		if input.NextState.IsTerminal() {
			var exitCode any
			if input.ExitCode != nil {
				exitCode = *input.ExitCode
			}
			if _, err := connection.ExecContext(ctx, `
UPDATE exec_commands
SET state = ?, exit_code = ?, final_event_sequence = ?, output_complete = ?, output_truncated = ?, updated_at = ?
WHERE command_id = ?
`, string(input.NextState), exitCode, sequence, boolToSQLite(input.OutputComplete), boolToSQLite(input.OutputTruncated), formatStoredTime(now), string(commandID)); err != nil {
				return CommandRecord{}, fmt.Errorf("persist terminal command: %w", err)
			}
		} else {
			if _, err := connection.ExecContext(ctx, "UPDATE exec_commands SET state = ?, updated_at = ? WHERE command_id = ?", string(input.NextState), formatStoredTime(now), string(commandID)); err != nil {
				return CommandRecord{}, fmt.Errorf("persist command state: %w", err)
			}
		}
		return readCommandOnConnection(ctx, connection, commandID)
	})
	if err != nil {
		return CommandRecord{}, err
	}
	if publishedEvent != nil {
		s.publishCommandEvent(*publishedEvent)
	}
	return record, nil
}

// CompleteRunningCommand appends one terminal event, updates terminal output
// metadata, transitions the owning busy session, and optionally confirms and
// releases the command slot in one SQLite transaction. A normal succeeded or
// failed completion releases its slot; a lost completion deliberately retains
// capacity until a later runtime inspection proves the stop boundary.
func (s *AuthorityStore) CompleteRunningCommand(ctx context.Context, input CommandTransition, nextSessionState domain.SessionState, sessionReason string, releaseSlot bool) (CommandRecord, error) {
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return CommandRecord{}, err
	}
	if !input.NextState.IsTerminal() {
		return CommandRecord{}, fmt.Errorf("%w: completion state %q is not terminal", ErrCommandTransition, input.NextState)
	}
	if nextSessionState != domain.SessionStateReady && nextSessionState != domain.SessionStateClosing && nextSessionState != domain.SessionStateExpired && nextSessionState != domain.SessionStateLost {
		return CommandRecord{}, fmt.Errorf("%w: completion session state %q is invalid", ErrCommandTransition, nextSessionState)
	}
	if _, err := validateLifecycleReason(sessionReason); err != nil {
		return CommandRecord{}, err
	}
	s.commandEventsMu.Lock()
	defer s.commandEventsMu.Unlock()
	now := s.now().UTC()
	var publishedEvent *CommandEventRecord
	record, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (CommandRecord, error) {
		command, err := readCommandOnConnection(ctx, connection, commandID)
		if err != nil {
			return CommandRecord{}, err
		}
		if command.State != domain.CommandStateRunning && command.State != domain.CommandStateCancelling {
			return CommandRecord{}, fmt.Errorf("%w: command is %q", ErrCommandTransition, command.State)
		}
		if err := domain.ValidateCommandTransition(command.State, input.NextState); err != nil {
			return CommandRecord{}, fmt.Errorf("%w: %v", ErrCommandTransition, err)
		}
		sequence, err := nextEventSequenceOnConnection(ctx, connection, commandID)
		if err != nil {
			return CommandRecord{}, err
		}
		eventType, ok := commandEventTypeForState(input.NextState)
		if !ok {
			return CommandRecord{}, fmt.Errorf("%w: no event type for state %q", ErrCommandTransition, input.NextState)
		}
		if err := insertCommandEventOnConnection(ctx, connection, commandID, sequence, eventType, nil, 0, now); err != nil {
			return CommandRecord{}, err
		}
		var exitCode any
		if input.ExitCode != nil {
			exitCode = *input.ExitCode
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE exec_commands
SET state = ?, exit_code = ?, final_event_sequence = ?, output_complete = ?, output_truncated = ?, updated_at = ?
WHERE command_id = ?
`, string(input.NextState), exitCode, sequence, boolToSQLite(input.OutputComplete), boolToSQLite(input.OutputTruncated), formatStoredTime(now), string(commandID)); err != nil {
			return CommandRecord{}, fmt.Errorf("persist completed command: %w", err)
		}
		var sessionState string
		if err := connection.QueryRowContext(ctx, "SELECT state FROM exec_sessions WHERE session_id = ?", string(command.SessionID)).Scan(&sessionState); err != nil {
			return CommandRecord{}, fmt.Errorf("read completed command session: %w", err)
		}
		if domain.SessionState(sessionState) != nextSessionState {
			if err := transitionSessionOnConnection(ctx, connection, command.SessionID, nextSessionState, sessionReason, now); err != nil {
				return CommandRecord{}, err
			}
		}
		if releaseSlot {
			result, err := connection.ExecContext(ctx, `
UPDATE exec_command_slots
SET stop_confirmed_at = ?, released_at = ?
WHERE command_id = ? AND stop_confirmed_at IS NULL
`, formatStoredTime(now), formatStoredTime(now), string(commandID))
			if err != nil {
				return CommandRecord{}, fmt.Errorf("release completed command slot: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return CommandRecord{}, fmt.Errorf("read completed slot release: %w", err)
			}
			if changed == 0 {
				var ignored string
				if err := connection.QueryRowContext(ctx, "SELECT command_id FROM exec_command_slots WHERE command_id = ?", string(commandID)).Scan(&ignored); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return CommandRecord{}, ErrCommandSlotNotFound
					}
					return CommandRecord{}, fmt.Errorf("check completed command slot: %w", err)
				}
				return CommandRecord{}, ErrCommandSlotNotReleasable
			}
		}
		event := CommandEventRecord{CommandID: commandID, Sequence: sequence, Type: eventType, Payload: []byte{}, ByteCount: 0, OccurredAt: now}
		publishedEvent = &event
		return readCommandOnConnection(ctx, connection, commandID)
	})
	if err != nil {
		return CommandRecord{}, err
	}
	if publishedEvent != nil {
		s.publishCommandEvent(*publishedEvent)
	}
	return record, nil
}

// ReplayCommandEvents returns a contiguous event range after afterSequence.
// A caller may request a cursor beyond the current tail and receive an empty
// range; any missing sequence inside the advertised range is an error.
func (s *AuthorityStore) ReplayCommandEvents(ctx context.Context, id domain.CommandID, afterSequence int64) ([]CommandEventRecord, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return nil, err
	}
	if afterSequence < 0 {
		return nil, fmt.Errorf("%w: negative cursor", ErrCommandReplayGap)
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire replay connection: %w", err)
	}
	defer connection.Close()
	if _, err := readCommandOnConnection(ctx, connection, validatedID); err != nil {
		return nil, err
	}
	return readCommandEventsOnConnection(ctx, connection, validatedID, afterSequence)
}

// NextEligibleCommand returns the oldest queued command that may be started
// for a ready/busy session. Earlier terminal commands are skipped; an earlier
// queued, running, or cancelling command blocks later ordinals. The read also
// verifies that authoritative ordinals start at one and have no gaps.
func (s *AuthorityStore) NextEligibleCommand(ctx context.Context, id domain.SessionID) (CommandRecord, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return CommandRecord{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return CommandRecord{}, fmt.Errorf("acquire eligibility connection: %w", err)
	}
	defer connection.Close()
	var sessionState string
	if err := connection.QueryRowContext(ctx, "SELECT state FROM exec_sessions WHERE session_id = ?", string(validatedID)).Scan(&sessionState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandRecord{}, ErrSessionNotFound
		}
		return CommandRecord{}, fmt.Errorf("read eligibility session: %w", err)
	}
	if sessionState != string(domain.SessionStateReady) && sessionState != string(domain.SessionStateBusy) {
		return CommandRecord{}, fmt.Errorf("%w: current session state %q", ErrCommandNotEligible, sessionState)
	}
	commands, err := readCommandOrderOnConnection(ctx, connection, validatedID)
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

type commandOrderRow struct {
	CommandID domain.CommandID
	Ordinal   int64
	State     domain.CommandState
}

func readCommandOrderOnConnection(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID) ([]commandOrderRow, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, ordinal, state
FROM exec_commands WHERE session_id = ? ORDER BY ordinal
`, string(sessionID))
	if err != nil {
		return nil, fmt.Errorf("read command order: %w", err)
	}
	defer rows.Close()
	var result []commandOrderRow
	expected := int64(1)
	for rows.Next() {
		var commandID string
		var row commandOrderRow
		var state string
		if err := rows.Scan(&commandID, &row.Ordinal, &state); err != nil {
			return nil, fmt.Errorf("scan command order: %w", err)
		}
		row.CommandID, err = domain.NewCommandID(commandID)
		if err != nil || row.Ordinal != expected {
			return nil, fmt.Errorf("%w: expected ordinal %d, got %d for %q", ErrCommandOrderCorrupt, expected, row.Ordinal, commandID)
		}
		row.State = domain.CommandState(state)
		if !row.State.Valid() {
			return nil, fmt.Errorf("%w: command %q has invalid state %q", ErrCommandOrderCorrupt, commandID, state)
		}
		result = append(result, row)
		expected++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command order: %w", err)
	}
	return result, nil
}

func nextCommandOrdinalOnConnection(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID) (int64, error) {
	commands, err := readCommandOrderOnConnection(ctx, connection, sessionID)
	if err != nil {
		return 0, err
	}
	return int64(len(commands) + 1), nil
}

// GetCommand returns an authoritative command and verifies its durable script
// bytes before exposing them to a caller.
func (s *AuthorityStore) GetCommand(ctx context.Context, id domain.CommandID) (CommandRecord, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return CommandRecord{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return CommandRecord{}, fmt.Errorf("acquire command connection: %w", err)
	}
	defer connection.Close()
	return readCommandOnConnection(ctx, connection, validatedID)
}

// ListSessionCommands returns authoritative commands in ordinal order. It is
// used by close orchestration to cancel queued work before teardown.
func (s *AuthorityStore) ListSessionCommands(ctx context.Context, id domain.SessionID) ([]CommandRecord, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return nil, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire session command connection: %w", err)
	}
	defer connection.Close()
	if _, err := readSessionOnConnection(ctx, connection, validatedID); err != nil {
		return nil, err
	}
	rows, err := connection.QueryContext(ctx, `
SELECT command_id FROM exec_commands WHERE session_id = ? ORDER BY ordinal
`, string(validatedID))
	if err != nil {
		return nil, fmt.Errorf("query session commands: %w", err)
	}
	defer rows.Close()
	var commandIDs []domain.CommandID
	for rows.Next() {
		var commandID string
		if err := rows.Scan(&commandID); err != nil {
			return nil, fmt.Errorf("scan session command: %w", err)
		}
		validatedCommandID, err := domain.NewCommandID(commandID)
		if err != nil {
			return nil, fmt.Errorf("invalid session command ID: %w", err)
		}
		commandIDs = append(commandIDs, validatedCommandID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session commands: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close session commands: %w", err)
	}
	commands := make([]CommandRecord, 0, len(commandIDs))
	for _, commandID := range commandIDs {
		command, err := readCommandOnConnection(ctx, connection, commandID)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

// ListCommandEvents returns the durable event prefix in sequence order.
func (s *AuthorityStore) ListCommandEvents(ctx context.Context, id domain.CommandID) ([]CommandEventRecord, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return nil, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire command event connection: %w", err)
	}
	defer connection.Close()
	if _, err := readCommandOnConnection(ctx, connection, validatedID); err != nil {
		return nil, err
	}
	return readCommandEventsOnConnection(ctx, connection, validatedID, -1)
}

func readCommandEventsOnConnection(ctx context.Context, connection *sql.Conn, expectedCommandID domain.CommandID, afterSequence int64) ([]CommandEventRecord, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, sequence, event_type, payload, byte_count, occurred_at
FROM exec_command_events WHERE command_id = ? AND sequence > ? ORDER BY sequence
`, string(expectedCommandID), afterSequence)
	if err != nil {
		return nil, fmt.Errorf("query command events: %w", err)
	}
	defer rows.Close()
	var events []CommandEventRecord
	expected := afterSequence + 1
	if afterSequence < 0 {
		expected = 1
	}
	for rows.Next() {
		var event CommandEventRecord
		var storedCommandID, eventType, occurredAt string
		var payload []byte
		if err := rows.Scan(&storedCommandID, &event.Sequence, &eventType, &payload, &event.ByteCount, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan command event: %w", err)
		}
		if storedCommandID != string(expectedCommandID) || !validCommandEventType(eventType) || event.Sequence != expected {
			return nil, fmt.Errorf("%w: expected sequence %d, got command=%q sequence=%d type=%q", ErrCommandReplayGap, expected, storedCommandID, event.Sequence, eventType)
		}
		if err := validateStoredCommandEvent(eventType, event.Sequence, payload, event.ByteCount); err != nil {
			return nil, err
		}
		event.CommandID = expectedCommandID
		event.Type = eventType
		event.Payload = append([]byte(nil), payload...)
		event.OccurredAt, err = parseStoredTime(occurredAt)
		if err != nil {
			return nil, fmt.Errorf("%w: event timestamp: %v", ErrCommandEvent, err)
		}
		events = append(events, event)
		expected++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command events: %w", err)
	}
	if len(events) == 0 && afterSequence < 0 {
		return nil, fmt.Errorf("%w: no events", ErrCommandEvent)
	}
	return events, nil
}

func nextEventSequenceOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID) (int64, error) {
	events, err := readCommandEventsOnConnection(ctx, connection, commandID, -1)
	if err != nil {
		return 0, err
	}
	return int64(len(events) + 1), nil
}

func insertCommandEventOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID, sequence int64, eventType string, payload []byte, byteCount int64, occurredAt time.Time) error {
	if err := validateStoredCommandEvent(eventType, sequence, payload, byteCount); err != nil {
		return err
	}
	if payload == nil {
		payload = []byte{}
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_command_events (command_id, sequence, event_type, payload, byte_count, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)
`, string(commandID), sequence, eventType, payload, byteCount, formatStoredTime(occurredAt)); err != nil {
		return fmt.Errorf("insert command event: %w", err)
	}
	return nil
}

func validateStoredCommandEvent(eventType string, sequence int64, payload []byte, byteCount int64) error {
	if !validCommandEventType(eventType) || sequence < 1 || byteCount < 0 {
		return fmt.Errorf("%w: sequence=%d type=%q", ErrCommandEvent, sequence, eventType)
	}
	if sequence == 1 && eventType != "command_queued" {
		return fmt.Errorf("%w: sequence one must be command_queued", ErrCommandEvent)
	}
	if sequence > 1 && eventType == "command_queued" {
		return fmt.Errorf("%w: command_queued must be sequence one", ErrCommandEvent)
	}
	if eventType == "stdout" || eventType == "stderr" {
		if byteCount <= 0 || int64(len(payload)) != byteCount {
			return fmt.Errorf("%w: output byte count does not match payload", ErrCommandEvent)
		}
	} else if len(payload) != 0 || byteCount != 0 {
		return fmt.Errorf("%w: non-output event carries bytes", ErrCommandEvent)
	}
	return nil
}

func isTerminalCommandEvent(eventType string) bool {
	switch eventType {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func commandEventTypeForState(state domain.CommandState) (string, bool) {
	switch state {
	case domain.CommandStateRunning:
		return "command_started", true
	case domain.CommandStateSucceeded:
		return "command_succeeded", true
	case domain.CommandStateFailed:
		return "command_failed", true
	case domain.CommandStateCancelled:
		return "command_cancelled", true
	case domain.CommandStateTimedOut:
		return "command_timed_out", true
	case domain.CommandStateRejected:
		return "command_rejected", true
	case domain.CommandStateLost:
		return "command_lost", true
	default:
		return "", false
	}
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateCommandAcceptance(input CommandAcceptance) (CommandAcceptance, error) {
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return CommandAcceptance{}, fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	sessionID, err := domain.NewSessionID(string(input.SessionID))
	if err != nil {
		return CommandAcceptance{}, fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	hash, err := domain.NewCanonicalHash(input.RequestHash.Version(), input.RequestHash.SHA256())
	if err != nil {
		return CommandAcceptance{}, fmt.Errorf("%w: request hash: %v", ErrInvalidCommand, err)
	}
	if err := domain.ValidateScriptUTF8(input.Script); err != nil {
		return CommandAcceptance{}, fmt.Errorf("%w: %w", ErrInvalidCommand, err)
	}
	if _, _, err := validateIdempotencyOperationKey(submitCommandOperation, input.IdempotencyKey); err != nil {
		return CommandAcceptance{}, err
	}
	retention := input.IdempotencyRetention
	if retention == 0 {
		retention = DefaultSessionIdempotencyRetention
	}
	if retention < 0 {
		return CommandAcceptance{}, fmt.Errorf("%w: idempotency retention must not be negative", ErrInvalidCommand)
	}
	if input.Timeout <= 0 {
		return CommandAcceptance{}, fmt.Errorf("%w: timeout must be positive", ErrInvalidCommand)
	}
	if input.IntentOrdinal < 0 {
		return CommandAcceptance{}, fmt.Errorf("%w: intent ordinal must not be negative", ErrInvalidCommand)
	}
	return CommandAcceptance{CommandID: commandID, SessionID: sessionID, RequestHash: hash, IdempotencyKey: input.IdempotencyKey, IdempotencyRetention: retention, Script: input.Script, Timeout: input.Timeout, IntentOrdinal: input.IntentOrdinal}, nil
}

func readCommandOnConnection(ctx context.Context, connection *sql.Conn, id domain.CommandID) (CommandRecord, error) {
	var record CommandRecord
	var commandID, sessionID, state, createdAt, updatedAt string
	var intentOrdinal sql.NullInt64
	var requestHashVersion int
	var requestHash, scriptBytes, scriptHash []byte
	var ordinal, timeoutNS, outputTruncated, outputComplete int64
	var finalSequence sql.NullInt64
	var exitCode sql.NullInt64
	if err := connection.QueryRowContext(ctx, `
SELECT command_id, session_id, ordinal, intent_ordinal,
       request_hash_version, request_hash, script_bytes, script_sha256,
       state, timeout_ns, exit_code, final_event_sequence,
       output_truncated, output_complete, created_at, updated_at
FROM exec_commands WHERE command_id = ?
`, string(id)).Scan(&commandID, &sessionID, &ordinal, &intentOrdinal,
		&requestHashVersion, &requestHash, &scriptBytes, &scriptHash,
		&state, &timeoutNS, &exitCode, &finalSequence,
		&outputTruncated, &outputComplete, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandRecord{}, ErrCommandNotFound
		}
		return CommandRecord{}, fmt.Errorf("read command: %w", err)
	}
	validatedID, err := domain.NewCommandID(commandID)
	if err != nil {
		return CommandRecord{}, fmt.Errorf("%w: command ID: %v", ErrCommandPayloadCorrupt, err)
	}
	validatedSession, err := domain.NewSessionID(sessionID)
	if err != nil {
		return CommandRecord{}, fmt.Errorf("%w: session ID: %v", ErrCommandPayloadCorrupt, err)
	}
	if validatedID != id || ordinal < 1 || !domain.CommandState(state).Valid() || timeoutNS <= 0 || (outputTruncated != 0 && outputTruncated != 1) || (outputComplete != 0 && outputComplete != 1) {
		return CommandRecord{}, fmt.Errorf("%w: command metadata", ErrCommandPayloadCorrupt)
	}
	hash, err := domain.NewCanonicalHash(uint16(requestHashVersion), requestHash)
	if err != nil {
		return CommandRecord{}, fmt.Errorf("%w: request hash: %v", ErrCommandPayloadCorrupt, err)
	}
	if err := domain.ValidateScriptUTF8(string(scriptBytes)); err != nil {
		return CommandRecord{}, fmt.Errorf("%w: script validation: %v", ErrCommandPayloadCorrupt, err)
	}
	computedScriptHash := sha256.Sum256(scriptBytes)
	if len(scriptHash) != sha256.Size || !bytes.Equal(scriptHash, computedScriptHash[:]) {
		return CommandRecord{}, fmt.Errorf("%w: script hash mismatch", ErrCommandPayloadCorrupt)
	}
	record.CommandID, record.SessionID, record.Ordinal = validatedID, validatedSession, ordinal
	if intentOrdinal.Valid {
		if intentOrdinal.Int64 < 1 {
			return CommandRecord{}, fmt.Errorf("%w: intent ordinal", ErrCommandPayloadCorrupt)
		}
		value := intentOrdinal.Int64
		record.IntentOrdinal = &value
	}
	record.RequestHash = hash
	record.ScriptBytes = append([]byte(nil), scriptBytes...)
	record.ScriptSHA256 = append([]byte(nil), scriptHash...)
	record.State = domain.CommandState(state)
	record.Timeout = time.Duration(timeoutNS)
	if exitCode.Valid {
		value := int(exitCode.Int64)
		record.ExitCode = &value
	}
	if finalSequence.Valid {
		value := finalSequence.Int64
		record.FinalEventSequence = &value
	}
	record.OutputTruncated, record.OutputComplete = outputTruncated == 1, outputComplete == 1
	if record.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return CommandRecord{}, fmt.Errorf("%w: created_at: %v", ErrCommandPayloadCorrupt, err)
	}
	if record.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
		return CommandRecord{}, fmt.Errorf("%w: updated_at: %v", ErrCommandPayloadCorrupt, err)
	}
	return record, nil
}

func validCommandEventType(value string) bool {
	switch value {
	case "command_queued", "command_started", "stdout", "stderr", "output_truncated", "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}
