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

// AcceptCommand atomically allocates the next authoritative session ordinal,
// inserts the immutable queued command, and inserts event sequence 1. It does
// not call a scheduler or start a runtime.
func (s *AuthorityStore) AcceptCommand(ctx context.Context, input CommandAcceptance) (record CommandRecord, duplicate bool, err error) {
	validated, err := validateCommandAcceptance(input)
	if err != nil {
		return CommandRecord{}, false, err
	}
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
	return returnRecord, duplicate, nil
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
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, sequence, event_type, payload, byte_count, occurred_at
FROM exec_command_events WHERE command_id = ? ORDER BY sequence
`, string(validatedID))
	if err != nil {
		return nil, fmt.Errorf("query command events: %w", err)
	}
	defer rows.Close()
	var events []CommandEventRecord
	for rows.Next() {
		var event CommandEventRecord
		var commandID, eventType, occurredAt string
		var payload []byte
		if err := rows.Scan(&commandID, &event.Sequence, &eventType, &payload, &event.ByteCount, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan command event: %w", err)
		}
		if commandID != string(validatedID) || !validCommandEventType(eventType) || event.Sequence < 1 || event.ByteCount < 0 {
			return nil, fmt.Errorf("%w: command=%q sequence=%d type=%q", ErrCommandEvent, commandID, event.Sequence, eventType)
		}
		event.CommandID = validatedID
		event.Type = eventType
		event.Payload = append([]byte(nil), payload...)
		event.OccurredAt, err = parseStoredTime(occurredAt)
		if err != nil {
			return nil, fmt.Errorf("%w: event timestamp: %v", ErrCommandEvent, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command events: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: no events", ErrCommandEvent)
	}
	return events, nil
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
