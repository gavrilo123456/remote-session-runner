package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

// ErrLostRuntimeRecoveryNotReleasable means an operator recovery did not find
// one matching, fully retained lost command and session reservation.
var ErrLostRuntimeRecoveryNotReleasable = errors.New("lost runtime recovery is not releasable")

// LostRuntimeRecoveryPair identifies one lost command and its owning session.
// It is used only after the runtime has durably proven that exact process
// group has stopped.
type LostRuntimeRecoveryPair struct {
	SessionID domain.SessionID
	CommandID domain.CommandID
}

// ConfirmLostRuntimeRecovery atomically records the cleanup proof for one
// already-lost command and its session. Callers must durably establish the
// runtime process-group proof first while retaining its ownership marker. The
// transaction keeps the two capacity releases together so a storage failure
// cannot expose a partial recovered state.
func (s *AuthorityStore) ConfirmLostRuntimeRecovery(ctx context.Context, sessionID domain.SessionID, commandID domain.CommandID) error {
	return s.ConfirmLostRuntimeRecoveryBatch(ctx, []LostRuntimeRecoveryPair{{SessionID: sessionID, CommandID: commandID}})
}

// ConfirmLostRuntimeRecoveryBatch records paired capacity releases for an
// explicit, complete set of lost runtimes. Each selected pair is validated in
// one immediate transaction and the transaction refuses any live slot or
// session reservation outside that set. A process crash can therefore leave
// all pairs retained, or every selected pair released; it cannot expose a
// partially released set.
//
// A pair already released by a prior successful transaction is accepted so an
// operator can retry only post-release workspace finalization. Partially
// released pairs remain ineligible.
func (s *AuthorityStore) ConfirmLostRuntimeRecoveryBatch(ctx context.Context, pairs []LostRuntimeRecoveryPair) error {
	validated, err := validateLostRuntimeRecoveryPairs(pairs)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		unreleased := make([]LostRuntimeRecoveryPair, 0, len(validated))
		for _, pair := range validated {
			alreadyReleased, err := checkLostRuntimeRecoveryPair(ctx, connection, pair)
			if err != nil {
				return struct{}{}, err
			}
			if !alreadyReleased {
				unreleased = append(unreleased, pair)
			}
		}
		if len(unreleased) == 0 {
			return struct{}{}, nil
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
		if liveSlots != len(unreleased) || liveReservations != len(unreleased) {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}

		for _, pair := range unreleased {
			if err := releaseLostRuntimeRecoveryPair(ctx, connection, pair, now); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func validateLostRuntimeRecoveryPairs(pairs []LostRuntimeRecoveryPair) ([]LostRuntimeRecoveryPair, error) {
	if len(pairs) == 0 {
		return nil, ErrLostRuntimeRecoveryNotReleasable
	}
	validated := make([]LostRuntimeRecoveryPair, 0, len(pairs))
	seenSessions := make(map[domain.SessionID]struct{}, len(pairs))
	seenCommands := make(map[domain.CommandID]struct{}, len(pairs))
	for _, pair := range pairs {
		sessionID, err := domain.NewSessionID(string(pair.SessionID))
		if err != nil {
			return nil, err
		}
		commandID, err := domain.NewCommandID(string(pair.CommandID))
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenSessions[sessionID]; duplicate {
			return nil, ErrLostRuntimeRecoveryNotReleasable
		}
		if _, duplicate := seenCommands[commandID]; duplicate {
			return nil, ErrLostRuntimeRecoveryNotReleasable
		}
		seenSessions[sessionID] = struct{}{}
		seenCommands[commandID] = struct{}{}
		validated = append(validated, LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID})
	}
	return validated, nil
}

// checkLostRuntimeRecoveryPair returns whether this pair was already released.
// It intentionally accepts an already-released pair even if event retention
// has removed its old command_lost record.
func checkLostRuntimeRecoveryPair(ctx context.Context, connection *sql.Conn, pair LostRuntimeRecoveryPair) (bool, error) {
	var commandSessionID, commandState, sessionState string
	var outputComplete int
	var finalSequence sql.NullInt64
	var commandSlotHostKey, sessionReservationHostKey string
	var commandStop, commandReleased, sessionCleanup, sessionReleased sql.NullString
	err := connection.QueryRowContext(ctx, `
SELECT c.session_id, c.state, s.state, c.output_complete, c.final_event_sequence,
       slot.host_key, slot.stop_confirmed_at, slot.released_at,
       reservation.host_key, reservation.cleanup_confirmed_at, reservation.released_at
FROM exec_commands AS c
JOIN exec_sessions AS s ON s.session_id = c.session_id
JOIN exec_command_slots AS slot ON slot.command_id = c.command_id
JOIN exec_capacity_reservations AS reservation ON reservation.session_id = s.session_id
WHERE c.command_id = ?`, string(pair.CommandID)).Scan(
		&commandSessionID, &commandState, &sessionState, &outputComplete, &finalSequence,
		&commandSlotHostKey, &commandStop, &commandReleased,
		&sessionReservationHostKey, &sessionCleanup, &sessionReleased,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrLostRuntimeRecoveryNotReleasable
		}
		return false, fmt.Errorf("read lost runtime recovery state: %w", err)
	}
	if commandSessionID != string(pair.SessionID) || commandState != string(domain.CommandStateLost) || sessionState != string(domain.SessionStateLost) || commandSlotHostKey != schedulerHostKey || sessionReservationHostKey != reservationHostKey {
		return false, ErrLostRuntimeRecoveryNotReleasable
	}
	commandAlreadyReleased := commandStop.Valid && commandReleased.Valid
	sessionAlreadyReleased := sessionCleanup.Valid && sessionReleased.Valid
	if commandAlreadyReleased && sessionAlreadyReleased {
		return true, nil
	}
	if commandStop.Valid != commandReleased.Valid || sessionCleanup.Valid != sessionReleased.Valid || commandAlreadyReleased != sessionAlreadyReleased {
		return false, ErrLostRuntimeRecoveryNotReleasable
	}
	if outputComplete != 0 || !finalSequence.Valid {
		return false, ErrLostRuntimeRecoveryNotReleasable
	}
	var tailSequence int64
	var tailType string
	if err := connection.QueryRowContext(ctx, `
SELECT sequence, event_type
FROM exec_command_events
WHERE command_id = ?
ORDER BY sequence DESC
LIMIT 1`, string(pair.CommandID)).Scan(&tailSequence, &tailType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrLostRuntimeRecoveryNotReleasable
		}
		return false, fmt.Errorf("read lost runtime event boundary: %w", err)
	}
	if tailSequence != finalSequence.Int64 || tailType != "command_lost" {
		return false, ErrLostRuntimeRecoveryNotReleasable
	}
	return false, nil
}

func releaseLostRuntimeRecoveryPair(ctx context.Context, connection *sql.Conn, pair LostRuntimeRecoveryPair, now time.Time) error {
	commandUpdate, err := connection.ExecContext(ctx, `
UPDATE exec_command_slots
SET stop_confirmed_at = ?, released_at = ?
WHERE command_id = ? AND host_key = ? AND stop_confirmed_at IS NULL AND released_at IS NULL`,
		formatStoredTime(now), formatStoredTime(now), string(pair.CommandID), schedulerHostKey)
	if err != nil {
		return fmt.Errorf("release lost recovery command slot: %w", err)
	}
	changed, err := commandUpdate.RowsAffected()
	if err != nil {
		return fmt.Errorf("read lost recovery command slot result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("%w: command slot update changed %d rows", ErrLostRuntimeRecoveryNotReleasable, changed)
	}
	var confirmedCommandStop, confirmedCommandRelease sql.NullString
	if err := connection.QueryRowContext(ctx, `
SELECT stop_confirmed_at, released_at
FROM exec_command_slots
WHERE command_id = ?`, string(pair.CommandID)).Scan(&confirmedCommandStop, &confirmedCommandRelease); err != nil {
		return fmt.Errorf("read confirmed lost-recovery command slot: %w", err)
	}
	if !confirmedCommandStop.Valid || !confirmedCommandRelease.Valid {
		return fmt.Errorf("%w: command slot update did not persist both timestamps", ErrLostRuntimeRecoveryNotReleasable)
	}
	sessionUpdate, err := connection.ExecContext(ctx, `
UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND host_key = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`,
		formatStoredTime(now), formatStoredTime(now), string(pair.SessionID), reservationHostKey)
	if err != nil {
		return fmt.Errorf("release lost recovery session reservation: %w", err)
	}
	changed, err = sessionUpdate.RowsAffected()
	if err != nil {
		return fmt.Errorf("read lost recovery session reservation result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("%w: session reservation update changed %d rows", ErrLostRuntimeRecoveryNotReleasable, changed)
	}
	var confirmedSessionCleanup, confirmedSessionRelease sql.NullString
	if err := connection.QueryRowContext(ctx, `
SELECT cleanup_confirmed_at, released_at
FROM exec_capacity_reservations
WHERE session_id = ?`, string(pair.SessionID)).Scan(&confirmedSessionCleanup, &confirmedSessionRelease); err != nil {
		return fmt.Errorf("read confirmed lost-recovery session reservation: %w", err)
	}
	if !confirmedSessionCleanup.Valid || !confirmedSessionRelease.Valid {
		return fmt.Errorf("%w: session reservation update did not persist both timestamps", ErrLostRuntimeRecoveryNotReleasable)
	}
	return nil
}
