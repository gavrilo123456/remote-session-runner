package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// MixedRuntimeRecoverySet is a metadata-only view of one complete idle
// inventory containing both terminal lost commands and completed commands
// whose session teardown was lost. It never includes scripts or event payloads.
type MixedRuntimeRecoverySet struct {
	Lost     []MixedLostRuntimeRecovery
	Teardown []CompletedTeardownRecovery
}

// MixedLostRuntimeRecovery contains only identity and cleanup metadata needed
// to reconcile one already-terminal lost runtime without replaying its command.
type MixedLostRuntimeRecovery struct {
	Pair                LostRuntimeRecoveryPair
	Session             SessionRecord
	Command             LostRuntimeRecoveryCommand
	CommandSlot         CommandSlotRecord
	SessionReservation  SessionReservation
	AlreadyRecovered    bool
	FinalizationPending bool
}

// CheckMixedRuntimeRecoverySet validates the entire live-work inventory and
// exact selected identities in one read snapshot. The caller must still obtain
// runtime cleanup proof and call ConfirmMixedRuntimeRecoverySet, which repeats
// the same guards inside its release transaction.
func (s *AuthorityStore) CheckMixedRuntimeRecoverySet(ctx context.Context, lost []LostRuntimeRecoveryPair, teardown []CompletedTeardownRecoveryPair) (MixedRuntimeRecoverySet, error) {
	if s == nil || s.db == nil {
		return MixedRuntimeRecoverySet{}, ErrLostRuntimeRecoveryNotReleasable
	}
	validatedLost, validatedTeardown, err := validateMixedRuntimeRecoverySelection(lost, teardown)
	if err != nil {
		return MixedRuntimeRecoverySet{}, err
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MixedRuntimeRecoverySet, error) {
		return readMixedRuntimeRecoverySet(ctx, connection, validatedLost, validatedTeardown)
	})
}

// ConfirmMixedRuntimeRecoverySet atomically releases a complete explicit set
// of terminal lost command slots/session reservations and completed-teardown
// session reservations. It is called only after every unreleased selected
// runtime has a durable cleanup proof. Command outcomes, outputs, and event
// histories are never changed.
func (s *AuthorityStore) ConfirmMixedRuntimeRecoverySet(ctx context.Context, lost []LostRuntimeRecoveryPair, teardown []CompletedTeardownRecoveryPair) error {
	if s == nil || s.db == nil {
		return ErrLostRuntimeRecoveryNotReleasable
	}
	validatedLost, validatedTeardown, err := validateMixedRuntimeRecoverySelection(lost, teardown)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		set, err := readMixedRuntimeRecoverySet(ctx, connection, validatedLost, validatedTeardown)
		if err != nil {
			return struct{}{}, err
		}
		for _, recovery := range set.Lost {
			if recovery.AlreadyRecovered {
				continue
			}
			if err := releaseLostRuntimeRecoveryPair(ctx, connection, recovery.Pair, now); err != nil {
				return struct{}{}, err
			}
		}
		for _, recovery := range set.Teardown {
			if recovery.AlreadyRecovered {
				continue
			}
			result, err := connection.ExecContext(ctx, `
UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND host_key = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`,
				formatStoredTime(now), formatStoredTime(now), string(recovery.Pair.SessionID), reservationHostKey)
			if err != nil {
				return struct{}{}, fmt.Errorf("release mixed completed-teardown session reservation: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
			}
			if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_completed_teardown_recovery_finalizations (session_id, command_id, job_id, capacity_released_at)
VALUES (?, ?, ?, ?)`, string(recovery.Pair.SessionID), string(recovery.Pair.CommandID), string(recovery.JobID), formatStoredTime(now)); err != nil {
				return struct{}{}, fmt.Errorf("record mixed completed-teardown finalization: %w", err)
			}
		}
		return struct{}{}, nil
	})
	return err
}

func validateMixedRuntimeRecoverySelection(lost []LostRuntimeRecoveryPair, teardown []CompletedTeardownRecoveryPair) ([]LostRuntimeRecoveryPair, []CompletedTeardownRecoveryPair, error) {
	if len(lost) == 0 && len(teardown) == 0 {
		return nil, nil, ErrLostRuntimeRecoveryNotReleasable
	}
	var validatedLost []LostRuntimeRecoveryPair
	var err error
	if len(lost) != 0 {
		validatedLost, err = validateLostRuntimeRecoveryPairs(lost)
		if err != nil {
			return nil, nil, err
		}
	}
	var validatedTeardown []CompletedTeardownRecoveryPair
	if len(teardown) != 0 {
		validatedTeardown, err = validateCompletedTeardownRecoveryPairs(teardown)
		if err != nil {
			return nil, nil, err
		}
	}
	sessions := make(map[domain.SessionID]struct{}, len(validatedLost)+len(validatedTeardown))
	commands := make(map[domain.CommandID]struct{}, len(validatedLost)+len(validatedTeardown))
	for _, pair := range validatedLost {
		sessions[pair.SessionID] = struct{}{}
		commands[pair.CommandID] = struct{}{}
	}
	for _, pair := range validatedTeardown {
		if _, exists := sessions[pair.SessionID]; exists {
			return nil, nil, ErrLostRuntimeRecoveryNotReleasable
		}
		if _, exists := commands[pair.CommandID]; exists {
			return nil, nil, ErrLostRuntimeRecoveryNotReleasable
		}
		sessions[pair.SessionID] = struct{}{}
		commands[pair.CommandID] = struct{}{}
	}
	return validatedLost, validatedTeardown, nil
}

func readMixedRuntimeRecoverySet(ctx context.Context, connection *sql.Conn, lost []LostRuntimeRecoveryPair, teardown []CompletedTeardownRecoveryPair) (MixedRuntimeRecoverySet, error) {
	set := MixedRuntimeRecoverySet{
		Lost:     make([]MixedLostRuntimeRecovery, 0, len(lost)),
		Teardown: make([]CompletedTeardownRecovery, 0, len(teardown)),
	}
	selectedLost := make(map[domain.CommandID]LostRuntimeRecoveryPair, len(lost))
	selectedTeardown := make(map[domain.SessionID]CompletedTeardownRecoveryPair, len(teardown))
	selectedSessions := make(map[domain.SessionID]struct{}, len(lost)+len(teardown))
	for _, pair := range lost {
		selectedLost[pair.CommandID] = pair
		selectedSessions[pair.SessionID] = struct{}{}
		alreadyReleased, err := checkLostRuntimeRecoveryPair(ctx, connection, pair)
		if err != nil {
			return set, err
		}
		recovery := MixedLostRuntimeRecovery{Pair: pair, AlreadyRecovered: alreadyReleased}
		recovery.Session, err = readSessionOnConnection(ctx, connection, pair.SessionID)
		if err != nil {
			return set, err
		}
		recovery.Command, err = readLostRuntimeRecoveryCommandOnConnection(ctx, connection, pair.CommandID)
		if err != nil {
			return set, err
		}
		recovery.CommandSlot, err = readCommandSlotOnConnection(ctx, connection, pair.CommandID)
		if err != nil {
			return set, err
		}
		recovery.SessionReservation, err = readReservationOnConnection(ctx, connection, pair.SessionID)
		if err != nil {
			return set, err
		}
		var pending int
		if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_lost_runtime_recovery_finalizations WHERE session_id = ? AND command_id = ?`, string(pair.SessionID), string(pair.CommandID)).Scan(&pending); err != nil {
			return set, fmt.Errorf("read mixed lost-runtime finalization state: %w", err)
		}
		recovery.FinalizationPending = pending == 1
		if pending > 1 || (pending == 1 && !alreadyReleased) {
			return set, ErrLostRuntimeRecoveryNotReleasable
		}
		set.Lost = append(set.Lost, recovery)
	}
	for _, pair := range teardown {
		selectedTeardown[pair.SessionID] = pair
		selectedSessions[pair.SessionID] = struct{}{}
		recovery, err := readCompletedTeardownRecovery(ctx, connection, pair)
		if err != nil {
			return set, err
		}
		set.Teardown = append(set.Teardown, recovery)
	}

	var unsafe int
	if err := connection.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM exec_sessions WHERE state NOT IN ('closed','expired','failed','lost')) +
 (SELECT COUNT(*) FROM exec_commands WHERE state NOT IN ('succeeded','failed','cancelled','timed_out','rejected','lost')) +
 (SELECT COUNT(*) FROM exec_jobs WHERE phase NOT IN ('complete','failed','lost'))`).Scan(&unsafe); err != nil {
		return set, fmt.Errorf("read mixed recovery active-work inventory: %w", err)
	}
	if unsafe != 0 {
		return set, ErrLostRuntimeRecoveryNotReleasable
	}

	var liveSlots int
	rows, err := connection.QueryContext(ctx, `SELECT command_id, host_key, stop_confirmed_at, released_at FROM exec_command_slots WHERE stop_confirmed_at IS NULL OR released_at IS NULL`)
	if err != nil {
		return set, fmt.Errorf("read mixed recovery command-slot inventory: %w", err)
	}
	for rows.Next() {
		var commandIDValue, hostKey string
		var stop, released sql.NullString
		if err := rows.Scan(&commandIDValue, &hostKey, &stop, &released); err != nil {
			_ = rows.Close()
			return set, err
		}
		commandID, identityErr := domain.NewCommandID(commandIDValue)
		pair, selected := selectedLost[commandID]
		if identityErr != nil || !selected || hostKey != schedulerHostKey || stop.Valid || released.Valid || pair.CommandID != commandID {
			_ = rows.Close()
			return set, ErrLostRuntimeRecoveryNotReleasable
		}
		liveSlots++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return set, err
	}
	if err := rows.Close(); err != nil {
		return set, err
	}
	wantSlots := 0
	for _, recovery := range set.Lost {
		if !recovery.AlreadyRecovered {
			wantSlots++
		}
	}
	if liveSlots != wantSlots {
		return set, ErrLostRuntimeRecoveryNotReleasable
	}

	var liveReservations int
	rows, err = connection.QueryContext(ctx, `SELECT session_id, host_key, cleanup_confirmed_at, released_at FROM exec_capacity_reservations WHERE cleanup_confirmed_at IS NULL OR released_at IS NULL`)
	if err != nil {
		return set, fmt.Errorf("read mixed recovery session-reservation inventory: %w", err)
	}
	for rows.Next() {
		var raw, hostKey string
		var cleanup, released sql.NullString
		if err := rows.Scan(&raw, &hostKey, &cleanup, &released); err != nil {
			_ = rows.Close()
			return set, err
		}
		sessionID, identityErr := domain.NewSessionID(raw)
		_, selected := selectedSessions[sessionID]
		if identityErr != nil || !selected || hostKey != reservationHostKey || cleanup.Valid || released.Valid {
			_ = rows.Close()
			return set, ErrLostRuntimeRecoveryNotReleasable
		}
		liveReservations++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return set, err
	}
	if err := rows.Close(); err != nil {
		return set, err
	}
	wantReservations := 0
	for _, recovery := range set.Lost {
		if !recovery.AlreadyRecovered {
			wantReservations++
		}
	}
	for _, recovery := range set.Teardown {
		if !recovery.AlreadyRecovered {
			wantReservations++
		}
	}
	if liveReservations != wantReservations {
		return set, ErrLostRuntimeRecoveryNotReleasable
	}

	if err := requireSelectedMixedFinalizations(ctx, connection, selectedLost, selectedTeardown); err != nil {
		return set, err
	}
	return set, nil
}

func requireSelectedMixedFinalizations(ctx context.Context, connection *sql.Conn, lost map[domain.CommandID]LostRuntimeRecoveryPair, teardown map[domain.SessionID]CompletedTeardownRecoveryPair) error {
	for _, query := range []struct {
		sql      string
		validate func(string, string) bool
	}{
		{`SELECT session_id, command_id FROM exec_lost_runtime_recovery_finalizations`, func(session, command string) bool {
			pair, ok := lost[domain.CommandID(command)]
			return ok && string(pair.SessionID) == session
		}},
		{`SELECT session_id, command_id FROM exec_completed_teardown_recovery_finalizations`, func(session, command string) bool {
			pair, ok := teardown[domain.SessionID(session)]
			return ok && string(pair.CommandID) == command
		}},
	} {
		rows, err := connection.QueryContext(ctx, query.sql)
		if err != nil {
			return fmt.Errorf("read mixed recovery pending finalizations: %w", err)
		}
		for rows.Next() {
			var sessionID, commandID string
			if err := rows.Scan(&sessionID, &commandID); err != nil {
				_ = rows.Close()
				return err
			}
			if !query.validate(sessionID, commandID) {
				_ = rows.Close()
				return ErrLostRuntimeRecoveryNotReleasable
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	var commandless int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_commandless_lost_runtime_recovery_finalizations`).Scan(&commandless); err != nil {
		return fmt.Errorf("read commandless recovery finalization inventory: %w", err)
	}
	if commandless != 0 {
		return errors.Join(ErrLostRuntimeRecoveryNotReleasable, ErrCommandlessLostRuntimeRecoveryNotReleasable)
	}
	return nil
}
