package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

// ErrCommandlessLostRuntimeRecoveryNotReleasable means the selected session
// does not prove the narrow state in which a one-off job was lost before an
// exec_commands row was persisted. It deliberately does not authorize a
// synthetic command or a manual reservation release.
var ErrCommandlessLostRuntimeRecoveryNotReleasable = errors.New("commandless lost runtime recovery is not releasable")

// CommandlessLostRuntimeRecovery identifies one terminal lost session whose
// one-off job retained a planned command ID but never created a command row.
// The Session snapshot is metadata only and is sufficient for the existing
// runtime ownership proof; no canonical request payload or script is read.
type CommandlessLostRuntimeRecovery struct {
	JobID               domain.JobID
	SessionID           domain.SessionID
	CommandID           domain.CommandID
	Session             SessionRecord
	SessionReservation  SessionReservation
	AlreadyRecovered    bool
	FinalizationPending bool
}

// CheckCommandlessLostRuntimeRecoveryBatch reads an explicit set of
// commandless terminal-lost sessions without changing capacity. It is the
// pre-runtime-proof half of the offline recovery boundary.
func (s *AuthorityStore) CheckCommandlessLostRuntimeRecoveryBatch(ctx context.Context, sessions []domain.SessionID) ([]CommandlessLostRuntimeRecovery, error) {
	validated, err := validateCommandlessLostRuntimeRecoverySessions(sessions)
	if err != nil {
		return nil, err
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]CommandlessLostRuntimeRecovery, error) {
		return readCommandlessLostRuntimeRecoveryBatch(ctx, connection, validated)
	})
}

// ConfirmCommandlessLostRuntimeRecoveryBatch records reservation releases for
// an explicit complete set of commandless lost sessions. Each release creates
// a session-keyed finalization row in the same transaction. That row keeps
// a crash after release but before marker removal recoverable on next startup.
//
// This method validates only its selected session-shaped records. Callers that
// recover a mixed command-pair and commandless set must use
// ConfirmLostRuntimeRecoverySet so the complete capacity inventory is checked
// in one transaction.
func (s *AuthorityStore) ConfirmCommandlessLostRuntimeRecoveryBatch(ctx context.Context, sessions []domain.SessionID) error {
	return s.ConfirmLostRuntimeRecoverySet(ctx, nil, sessions)
}

// ConfirmLostRuntimeRecoverySet atomically releases a complete explicit set
// of ordinary lost command pairs and commandless lost sessions. Every selected
// runtime must already have a durable runtime-cleanup proof. The transaction
// rejects any live slot or session reservation outside the selected set.
func (s *AuthorityStore) ConfirmLostRuntimeRecoverySet(ctx context.Context, pairs []LostRuntimeRecoveryPair, sessions []domain.SessionID) error {
	validatedPairs, err := validateLostRuntimeRecoveryPairsAllowEmpty(pairs)
	if err != nil {
		return err
	}
	validatedSessions, err := validateCommandlessLostRuntimeRecoverySessionsAllowEmpty(sessions)
	if err != nil {
		return err
	}
	if len(validatedPairs) == 0 && len(validatedSessions) == 0 {
		return ErrLostRuntimeRecoveryNotReleasable
	}
	seenSessions := make(map[domain.SessionID]struct{}, len(validatedPairs)+len(validatedSessions))
	for _, pair := range validatedPairs {
		seenSessions[pair.SessionID] = struct{}{}
	}
	for _, sessionID := range validatedSessions {
		if _, exists := seenSessions[sessionID]; exists {
			return ErrCommandlessLostRuntimeRecoveryNotReleasable
		}
		seenSessions[sessionID] = struct{}{}
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		unreleasedPairs := make([]LostRuntimeRecoveryPair, 0, len(validatedPairs))
		for _, pair := range validatedPairs {
			alreadyReleased, checkErr := checkLostRuntimeRecoveryPair(ctx, connection, pair)
			if checkErr != nil {
				return struct{}{}, checkErr
			}
			if !alreadyReleased {
				unreleasedPairs = append(unreleasedPairs, pair)
			}
		}
		commandless, readErr := readCommandlessLostRuntimeRecoveryBatch(ctx, connection, validatedSessions)
		if readErr != nil {
			return struct{}{}, readErr
		}
		unreleasedSessions := make([]CommandlessLostRuntimeRecovery, 0, len(commandless))
		for _, recovery := range commandless {
			if !recovery.AlreadyRecovered {
				unreleasedSessions = append(unreleasedSessions, recovery)
			}
		}
		if len(unreleasedPairs) == 0 && len(unreleasedSessions) == 0 {
			return struct{}{}, nil
		}
		var liveSlots, liveReservations int
		if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_command_slots
WHERE host_key = ? AND stop_confirmed_at IS NULL AND released_at IS NULL`, schedulerHostKey).Scan(&liveSlots); err != nil {
			return struct{}{}, fmt.Errorf("count live mixed-recovery command slots: %w", err)
		}
		if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_capacity_reservations
WHERE host_key = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`, reservationHostKey).Scan(&liveReservations); err != nil {
			return struct{}{}, fmt.Errorf("count live mixed-recovery session reservations: %w", err)
		}
		if liveSlots != len(unreleasedPairs) || liveReservations != len(unreleasedPairs)+len(unreleasedSessions) {
			return struct{}{}, ErrLostRuntimeRecoveryNotReleasable
		}
		for _, pair := range unreleasedPairs {
			if err := releaseLostRuntimeRecoveryPair(ctx, connection, pair, now); err != nil {
				return struct{}{}, err
			}
		}
		for _, recovery := range unreleasedSessions {
			if err := releaseCommandlessLostRuntimeRecovery(ctx, connection, recovery, now); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// ListPendingCommandlessLostRuntimeRecoveryFinalizations returns only durable
// release records that still need owner-marker/workspace finalization. It is
// called before startup ownership audit so an interrupted recovery remains
// retryable without confusing a valid retained marker for an orphan.
func (s *AuthorityStore) ListPendingCommandlessLostRuntimeRecoveryFinalizations(ctx context.Context) ([]CommandlessLostRuntimeRecovery, error) {
	if s == nil || s.db == nil {
		return nil, ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]CommandlessLostRuntimeRecovery, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT session_id
FROM exec_commandless_lost_runtime_recovery_finalizations
ORDER BY capacity_released_at, session_id`)
		if err != nil {
			return nil, fmt.Errorf("query pending commandless lost runtime recovery finalizations: %w", err)
		}
		ids := make([]domain.SessionID, 0)
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan pending commandless lost runtime recovery finalization: %w", err)
			}
			id, err := domain.NewSessionID(raw)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("%w: pending finalization session identity", ErrCommandlessLostRuntimeRecoveryNotReleasable)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate pending commandless lost runtime recovery finalizations: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close pending commandless lost runtime recovery finalizations: %w", err)
		}
		recoveries, err := readCommandlessLostRuntimeRecoveryBatch(ctx, connection, ids)
		if err != nil {
			return nil, err
		}
		for _, recovery := range recoveries {
			if !recovery.AlreadyRecovered || !recovery.FinalizationPending {
				return nil, ErrCommandlessLostRuntimeRecoveryNotReleasable
			}
		}
		return recoveries, nil
	})
}

// CompleteCommandlessLostRuntimeRecoveryFinalization removes the durable work
// item after runtime marker/workspace finalization succeeds. Retrying after a
// successful completion is harmless only when the exact released candidate
// still proves its terminal commandless shape.
func (s *AuthorityStore) CompleteCommandlessLostRuntimeRecoveryFinalization(ctx context.Context, sessionID domain.SessionID) error {
	validated, err := domain.NewSessionID(string(sessionID))
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		recoveries, readErr := readCommandlessLostRuntimeRecoveryBatch(ctx, connection, []domain.SessionID{validated})
		if readErr != nil || len(recoveries) != 1 || !recoveries[0].AlreadyRecovered {
			if readErr != nil {
				return struct{}{}, readErr
			}
			return struct{}{}, ErrCommandlessLostRuntimeRecoveryNotReleasable
		}
		if !recoveries[0].FinalizationPending {
			return struct{}{}, nil
		}
		result, deleteErr := connection.ExecContext(ctx, `
DELETE FROM exec_commandless_lost_runtime_recovery_finalizations
WHERE session_id = ? AND job_id = ?`, string(validated), string(recoveries[0].JobID))
		if deleteErr != nil {
			return struct{}{}, fmt.Errorf("complete commandless lost runtime recovery finalization: %w", deleteErr)
		}
		changed, changedErr := result.RowsAffected()
		if changedErr != nil {
			return struct{}{}, fmt.Errorf("read commandless lost runtime finalization completion result: %w", changedErr)
		}
		if changed != 1 {
			return struct{}{}, ErrCommandlessLostRuntimeRecoveryNotReleasable
		}
		return struct{}{}, nil
	})
	return err
}

func readCommandlessLostRuntimeRecoveryBatch(ctx context.Context, connection *sql.Conn, sessions []domain.SessionID) ([]CommandlessLostRuntimeRecovery, error) {
	recoveries := make([]CommandlessLostRuntimeRecovery, 0, len(sessions))
	for _, sessionID := range sessions {
		recovery, err := readCommandlessLostRuntimeRecovery(ctx, connection, sessionID)
		if err != nil {
			return nil, err
		}
		recoveries = append(recoveries, recovery)
	}
	return recoveries, nil
}

func readCommandlessLostRuntimeRecovery(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID) (CommandlessLostRuntimeRecovery, error) {
	var recovery CommandlessLostRuntimeRecovery
	var rawJobID, rawSessionID, rawCommandID, phase, teardownState, teardownReason string
	var commandState sql.NullString
	var exitCode, finalSequence sql.NullInt64
	var outputTruncated, outputComplete int
	var outputUnavailable string
	var reservationHost string
	var cleanupConfirmed, released sql.NullString
	var jobsForSession, commandsForSession, slotsForJob, finalizations int
	err := connection.QueryRowContext(ctx, `
SELECT job.job_id, job.session_id, job.command_id, job.phase,
       job.command_state, job.exit_code, job.final_event_sequence,
       job.output_truncated, job.output_complete, job.output_unavailable_reason,
       job.teardown_state, job.teardown_reason,
       reservation.host_key, reservation.cleanup_confirmed_at, reservation.released_at,
       (SELECT COUNT(*) FROM exec_jobs AS candidate WHERE candidate.session_id = session.session_id),
       (SELECT COUNT(*) FROM exec_commands AS command WHERE command.session_id = session.session_id),
       (SELECT COUNT(*) FROM exec_command_slots AS slot WHERE slot.command_id = job.command_id),
       (SELECT COUNT(*) FROM exec_commandless_lost_runtime_recovery_finalizations AS finalization WHERE finalization.session_id = session.session_id AND finalization.job_id = job.job_id)
FROM exec_sessions AS session
JOIN exec_capacity_reservations AS reservation ON reservation.session_id = session.session_id
JOIN exec_jobs AS job ON job.session_id = session.session_id
WHERE session.session_id = ?`, string(sessionID)).Scan(
		&rawJobID, &rawSessionID, &rawCommandID, &phase,
		&commandState, &exitCode, &finalSequence,
		&outputTruncated, &outputComplete, &outputUnavailable,
		&teardownState, &teardownReason,
		&reservationHost, &cleanupConfirmed, &released,
		&jobsForSession,
		&commandsForSession, &slotsForJob, &finalizations,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return recovery, ErrCommandlessLostRuntimeRecoveryNotReleasable
		}
		return recovery, fmt.Errorf("read commandless lost runtime recovery state: %w", err)
	}
	jobID, err := domain.NewJobID(rawJobID)
	if err != nil {
		return recovery, fmt.Errorf("%w: job identity", ErrCommandlessLostRuntimeRecoveryNotReleasable)
	}
	storedSessionID, err := domain.NewSessionID(rawSessionID)
	if err != nil || storedSessionID != sessionID {
		return recovery, fmt.Errorf("%w: session identity", ErrCommandlessLostRuntimeRecoveryNotReleasable)
	}
	commandID, err := domain.NewCommandID(rawCommandID)
	if err != nil {
		return recovery, fmt.Errorf("%w: planned command identity", ErrCommandlessLostRuntimeRecoveryNotReleasable)
	}
	session, err := readSessionOnConnection(ctx, connection, sessionID)
	if err != nil {
		return recovery, err
	}
	if phase != string(JobPhaseLost) || commandState.Valid || exitCode.Valid || finalSequence.Valid || outputTruncated != 0 || outputComplete != 0 || outputUnavailable != "" ||
		teardownState != string(JobTeardownPending) || teardownReason != "" || session.State != domain.SessionStateLost || session.RuntimeGeneration == "" ||
		reservationHost != reservationHostKey || jobsForSession != 1 || commandsForSession != 0 || slotsForJob != 0 || finalizations > 1 {
		return recovery, ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	reservation, err := readReservationOnConnection(ctx, connection, sessionID)
	if err != nil {
		return recovery, err
	}
	alreadyReleased := cleanupConfirmed.Valid && released.Valid
	if cleanupConfirmed.Valid != released.Valid {
		return recovery, ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	if !alreadyReleased && finalizations != 0 {
		return recovery, ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	return CommandlessLostRuntimeRecovery{
		JobID:               jobID,
		SessionID:           sessionID,
		CommandID:           commandID,
		Session:             session,
		SessionReservation:  reservation,
		AlreadyRecovered:    alreadyReleased,
		FinalizationPending: finalizations == 1,
	}, nil
}

func releaseCommandlessLostRuntimeRecovery(ctx context.Context, connection *sql.Conn, recovery CommandlessLostRuntimeRecovery, now time.Time) error {
	result, err := connection.ExecContext(ctx, `
UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND host_key = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`,
		formatStoredTime(now), formatStoredTime(now), string(recovery.SessionID), reservationHostKey)
	if err != nil {
		return fmt.Errorf("release commandless lost runtime session reservation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read commandless lost runtime session reservation update result: %w", err)
	}
	if changed != 1 {
		return ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	result, err = connection.ExecContext(ctx, `
INSERT INTO exec_commandless_lost_runtime_recovery_finalizations (
    session_id, job_id, capacity_released_at
) VALUES (?, ?, ?)`, string(recovery.SessionID), string(recovery.JobID), formatStoredTime(now))
	if err != nil {
		return fmt.Errorf("record commandless lost runtime recovery finalization: %w", err)
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read commandless lost runtime finalization record result: %w", err)
	}
	if changed != 1 {
		return ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	return nil
}

func validateCommandlessLostRuntimeRecoverySessions(sessions []domain.SessionID) ([]domain.SessionID, error) {
	validated, err := validateCommandlessLostRuntimeRecoverySessionsAllowEmpty(sessions)
	if err != nil {
		return nil, err
	}
	if len(validated) == 0 {
		return nil, ErrCommandlessLostRuntimeRecoveryNotReleasable
	}
	return validated, nil
}

func validateCommandlessLostRuntimeRecoverySessionsAllowEmpty(sessions []domain.SessionID) ([]domain.SessionID, error) {
	validated := make([]domain.SessionID, 0, len(sessions))
	seen := make(map[domain.SessionID]struct{}, len(sessions))
	for _, raw := range sessions {
		id, err := domain.NewSessionID(string(raw))
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, ErrCommandlessLostRuntimeRecoveryNotReleasable
		}
		seen[id] = struct{}{}
		validated = append(validated, id)
	}
	return validated, nil
}

func validateLostRuntimeRecoveryPairsAllowEmpty(pairs []LostRuntimeRecoveryPair) ([]LostRuntimeRecoveryPair, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	return validateLostRuntimeRecoveryPairs(pairs)
}
