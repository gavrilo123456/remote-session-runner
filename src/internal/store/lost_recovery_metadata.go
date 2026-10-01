package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// LostRuntimeRecoveryCommand is the minimal command metadata needed to prove
// a terminal lost-recovery boundary. It intentionally excludes immutable
// script bytes, request hashes, and output payloads.
type LostRuntimeRecoveryCommand struct {
	CommandID          domain.CommandID
	SessionID          domain.SessionID
	State              domain.CommandState
	OutputComplete     bool
	FinalEventSequence *int64
}

// CommandStateMetadata is an identity-only session command row used to reject
// a recovery when another command remains nonterminal.
type CommandStateMetadata struct {
	CommandID domain.CommandID
	State     domain.CommandState
}

// CommandEventBoundary is the durable tail metadata of one event stream. It
// intentionally excludes the event payload, which can contain command output.
type CommandEventBoundary struct {
	Sequence int64
	Type     string
}

// GetLostRuntimeRecoveryCommand reads only the fields used by the explicit
// lost-runtime operator recovery. It must not be used to execute a command.
func (s *AuthorityStore) GetLostRuntimeRecoveryCommand(ctx context.Context, id domain.CommandID) (LostRuntimeRecoveryCommand, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return LostRuntimeRecoveryCommand{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return LostRuntimeRecoveryCommand{}, fmt.Errorf("acquire lost-recovery command connection: %w", err)
	}
	defer connection.Close()
	return readLostRuntimeRecoveryCommandOnConnection(ctx, connection, validatedID)
}

func readLostRuntimeRecoveryCommandOnConnection(ctx context.Context, connection *sql.Conn, id domain.CommandID) (LostRuntimeRecoveryCommand, error) {
	var record LostRuntimeRecoveryCommand
	var commandID, sessionID, state string
	var outputComplete int
	var finalSequence sql.NullInt64
	if err := connection.QueryRowContext(ctx, `
SELECT command_id, session_id, state, output_complete, final_event_sequence
FROM exec_commands WHERE command_id = ?`, string(id)).Scan(&commandID, &sessionID, &state, &outputComplete, &finalSequence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LostRuntimeRecoveryCommand{}, ErrCommandNotFound
		}
		return LostRuntimeRecoveryCommand{}, fmt.Errorf("read lost-recovery command: %w", err)
	}
	validatedCommandID, err := domain.NewCommandID(commandID)
	if err != nil || validatedCommandID != id {
		return LostRuntimeRecoveryCommand{}, fmt.Errorf("%w: command ID", ErrCommandEvent)
	}
	validatedSessionID, err := domain.NewSessionID(sessionID)
	if err != nil {
		return LostRuntimeRecoveryCommand{}, fmt.Errorf("%w: session ID", ErrCommandEvent)
	}
	commandState := domain.CommandState(state)
	if !commandState.Valid() || (outputComplete != 0 && outputComplete != 1) {
		return LostRuntimeRecoveryCommand{}, fmt.Errorf("%w: command state metadata", ErrCommandEvent)
	}
	record = LostRuntimeRecoveryCommand{
		CommandID: validatedCommandID, SessionID: validatedSessionID, State: commandState, OutputComplete: outputComplete == 1,
	}
	if finalSequence.Valid {
		if finalSequence.Int64 < 1 {
			return LostRuntimeRecoveryCommand{}, fmt.Errorf("%w: final event sequence", ErrCommandEvent)
		}
		value := finalSequence.Int64
		record.FinalEventSequence = &value
	}
	return record, nil
}

// ListSessionCommandStates reads only identities and current states. It is
// sufficient for lost-recovery sibling safety without loading any script.
func (s *AuthorityStore) ListSessionCommandStates(ctx context.Context, id domain.SessionID) ([]CommandStateMetadata, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return nil, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire lost-recovery session command connection: %w", err)
	}
	defer connection.Close()
	var present int
	if err := connection.QueryRowContext(ctx, "SELECT 1 FROM exec_sessions WHERE session_id = ?", string(validatedID)).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("read lost-recovery session: %w", err)
	}
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, state FROM exec_commands
WHERE session_id = ? ORDER BY ordinal`, string(validatedID))
	if err != nil {
		return nil, fmt.Errorf("query lost-recovery session command states: %w", err)
	}
	defer rows.Close()
	commands := make([]CommandStateMetadata, 0)
	for rows.Next() {
		var commandID, state string
		if err := rows.Scan(&commandID, &state); err != nil {
			return nil, fmt.Errorf("scan lost-recovery session command state: %w", err)
		}
		validatedCommandID, err := domain.NewCommandID(commandID)
		if err != nil {
			return nil, fmt.Errorf("%w: command ID", ErrCommandEvent)
		}
		commandState := domain.CommandState(state)
		if !commandState.Valid() {
			return nil, fmt.Errorf("%w: command state", ErrCommandEvent)
		}
		commands = append(commands, CommandStateMetadata{CommandID: validatedCommandID, State: commandState})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lost-recovery session command states: %w", err)
	}
	return commands, nil
}

// GetCommandEventTail reads the terminal boundary without reading event
// payloads. It rejects a missing event stream.
func (s *AuthorityStore) GetCommandEventTail(ctx context.Context, id domain.CommandID) (CommandEventBoundary, error) {
	validatedID, err := domain.NewCommandID(string(id))
	if err != nil {
		return CommandEventBoundary{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return CommandEventBoundary{}, fmt.Errorf("acquire lost-recovery event connection: %w", err)
	}
	defer connection.Close()
	var sequence int64
	var eventType string
	if err := connection.QueryRowContext(ctx, `
SELECT sequence, event_type FROM exec_command_events
WHERE command_id = ?
ORDER BY sequence DESC
LIMIT 1`, string(validatedID)).Scan(&sequence, &eventType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandEventBoundary{}, ErrCommandEvent
		}
		return CommandEventBoundary{}, fmt.Errorf("read lost-recovery event tail: %w", err)
	}
	if sequence < 1 || !validCommandEventType(eventType) {
		return CommandEventBoundary{}, fmt.Errorf("%w: event tail", ErrCommandEvent)
	}
	return CommandEventBoundary{Sequence: sequence, Type: eventType}, nil
}
