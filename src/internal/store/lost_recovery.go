package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// ErrLostRuntimeRecoveryNotReleasable means an operator recovery did not find
// one matching, fully retained lost command and session reservation.
var ErrLostRuntimeRecoveryNotReleasable = errors.New("lost runtime recovery is not releasable")

// ConfirmLostRuntimeRecovery atomically records the cleanup proof for one
// already-lost command and its session. Callers must durably establish the
// runtime process-group proof first while retaining its ownership marker. The
// transaction keeps the two capacity releases together so a storage failure
// cannot expose a partial recovered state.
func (s *AuthorityStore) ConfirmLostRuntimeRecovery(ctx context.Context, sessionID domain.SessionID, commandID domain.CommandID) error {
	validatedSessionID, err := domain.NewSessionID(string(sessionID))
	if err != nil {
		return err
	}
	validatedCommandID, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		var commandSessionID, commandState, sessionState string
		var outputComplete int
		var finalSequence sql.NullInt64
		var commandStop, commandReleased, sessionCleanup, sessionReleased sql.NullString
		err := connection.QueryRowContext(ctx, `
SELECT c.session_id, c.state, s.state, c.output_complete, c.final_event_sequence,
       slot.stop_confirmed_at, slot.released_at,
       reservation.cleanup_confirmed_at, reservation.released_at
FROM exec_commands AS c
JOIN exec_sessions AS s ON s.session_id = c.session_id
JOIN exec_command_slots AS slot ON slot.command_id = c.command_id
JOIN exec_capacity_reservations AS reservation ON reservation.session_id = s.session_id
WHERE c.command_id = ?`, string(validatedCommandID)).Scan(
			&commandSessionID, &commandState, &sessionState, &outputComplete, &finalSequence,
			&commandStop, &commandReleased, &sessionCleanup, &sessionReleased,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
			}
			return struct{}{}, fmt.Errorf("read lost runtime recovery state: %w", err)
		}
		if commandSessionID != string(validatedSessionID) || commandState != string(domain.CommandStateLost) || sessionState != string(domain.SessionStateLost) {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}
		commandAlreadyReleased := commandStop.Valid && commandReleased.Valid
		sessionAlreadyReleased := sessionCleanup.Valid && sessionReleased.Valid
		if commandAlreadyReleased && sessionAlreadyReleased {
			return struct{}{}, nil
		}
		if commandStop.Valid != commandReleased.Valid || sessionCleanup.Valid != sessionReleased.Valid || commandAlreadyReleased != sessionAlreadyReleased {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}
		if outputComplete != 0 || !finalSequence.Valid {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}
		var tailSequence int64
		var tailType string
		if err := connection.QueryRowContext(ctx, `
SELECT sequence, event_type
FROM exec_command_events
WHERE command_id = ?
ORDER BY sequence DESC
LIMIT 1`, string(validatedCommandID)).Scan(&tailSequence, &tailType); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
			}
			return struct{}{}, fmt.Errorf("read lost runtime event boundary: %w", err)
		}
		if tailSequence != finalSequence.Int64 || tailType != "command_lost" {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}
		var liveSlots, liveReservations int
		if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_command_slots
WHERE host_key = ? AND stop_confirmed_at IS NULL`, schedulerHostKey).Scan(&liveSlots); err != nil {
			return struct{}{}, fmt.Errorf("count live lost-recovery command slots: %w", err)
		}
		if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_capacity_reservations
WHERE host_key = ? AND cleanup_confirmed_at IS NULL`, reservationHostKey).Scan(&liveReservations); err != nil {
			return struct{}{}, fmt.Errorf("count live lost-recovery session reservations: %w", err)
		}
		if liveSlots != 1 || liveReservations != 1 {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}
		commandUpdate, err := connection.ExecContext(ctx, `
UPDATE exec_command_slots
SET stop_confirmed_at = ?, released_at = ?
WHERE command_id = ? AND stop_confirmed_at IS NULL AND released_at IS NULL`,
			formatStoredTime(now), formatStoredTime(now), string(validatedCommandID))
		if err != nil {
			return struct{}{}, fmt.Errorf("release lost recovery command slot: %w", err)
		}
		changed, err := commandUpdate.RowsAffected()
		if err != nil {
			return struct{}{}, fmt.Errorf("read lost recovery command slot result: %w", err)
		}
		if changed != 1 {
			return struct{}{}, fmt.Errorf("%w: command slot update changed %d rows", ErrLostRuntimeRecoveryNotReleasable, changed)
		}
		var confirmedCommandStop, confirmedCommandRelease sql.NullString
		if err := connection.QueryRowContext(ctx, `
SELECT stop_confirmed_at, released_at
FROM exec_command_slots
WHERE command_id = ?`, string(validatedCommandID)).Scan(&confirmedCommandStop, &confirmedCommandRelease); err != nil {
			return struct{}{}, fmt.Errorf("read confirmed lost-recovery command slot: %w", err)
		}
		if !confirmedCommandStop.Valid || !confirmedCommandRelease.Valid {
			return struct{}{}, fmt.Errorf("%w: command slot update did not persist both timestamps", ErrLostRuntimeRecoveryNotReleasable)
		}
		sessionUpdate, err := connection.ExecContext(ctx, `
UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`,
			formatStoredTime(now), formatStoredTime(now), string(validatedSessionID))
		if err != nil {
			return struct{}{}, fmt.Errorf("release lost recovery session reservation: %w", err)
		}
		changed, err = sessionUpdate.RowsAffected()
		if err != nil {
			return struct{}{}, fmt.Errorf("read lost recovery session reservation result: %w", err)
		}
		if changed != 1 {
			return struct{}{}, fmt.Errorf("%w: session reservation update changed %d rows", ErrLostRuntimeRecoveryNotReleasable, changed)
		}
		var confirmedSessionCleanup, confirmedSessionRelease sql.NullString
		if err := connection.QueryRowContext(ctx, `
SELECT cleanup_confirmed_at, released_at
FROM exec_capacity_reservations
WHERE session_id = ?`, string(validatedSessionID)).Scan(&confirmedSessionCleanup, &confirmedSessionRelease); err != nil {
			return struct{}{}, fmt.Errorf("read confirmed lost-recovery session reservation: %w", err)
		}
		if !confirmedSessionCleanup.Valid || !confirmedSessionRelease.Valid {
			return struct{}{}, fmt.Errorf("%w: session reservation update did not persist both timestamps", ErrLostRuntimeRecoveryNotReleasable)
		}
		return struct{}{}, nil
	})
	return err
}
