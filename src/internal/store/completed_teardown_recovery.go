package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// ErrCompletedTeardownRecoveryNotReleasable means the exact selected Mac
// one-off does not prove a complete ordinary command result and lost session cleanup.
var ErrCompletedTeardownRecoveryNotReleasable = errors.New("completed teardown recovery is not releasable")

// CompletedTeardownRecoveryPair identifies a complete succeeded or ordinary
// nonzero-exit failed command and its lost ephemeral session. It is deliberately
// distinct from a lost-command pair.
type CompletedTeardownRecoveryPair struct {
	SessionID domain.SessionID
	CommandID domain.CommandID
}

// CompletedTeardownRecovery contains only metadata needed for exact runtime
// proof. No script, canonical request, or output payload is loaded.
type CompletedTeardownRecovery struct {
	Pair                CompletedTeardownRecoveryPair
	JobID               domain.JobID
	Session             SessionRecord
	Command             LostRuntimeRecoveryCommand
	CommandSlot         CommandSlotRecord
	SessionReservation  SessionReservation
	AlreadyRecovered    bool
	FinalizationPending bool
}

// CheckCompletedTeardownRecoveryBatch validates the complete idle recovery
// selection without changing capacity or touching its runtime.
func (s *AuthorityStore) CheckCompletedTeardownRecoveryBatch(ctx context.Context, pairs []CompletedTeardownRecoveryPair) ([]CompletedTeardownRecovery, error) {
	if s == nil || s.db == nil {
		return nil, ErrCompletedTeardownRecoveryNotReleasable
	}
	validated, err := validateCompletedTeardownRecoveryPairs(pairs)
	if err != nil {
		return nil, err
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]CompletedTeardownRecovery, error) {
		return checkCompletedTeardownRecoveryBatch(ctx, connection, validated)
	})
}

// ConfirmCompletedTeardownRecoveryBatch must be called only after the caller
// durably proves exact runtime cleanup while retaining its ownership marker.
// It rechecks the complete idle inventory and atomically releases only session
// reservations, recording finalization work in the same transaction.
func (s *AuthorityStore) ConfirmCompletedTeardownRecoveryBatch(ctx context.Context, pairs []CompletedTeardownRecoveryPair) error {
	if s == nil || s.db == nil {
		return ErrCompletedTeardownRecoveryNotReleasable
	}
	validated, err := validateCompletedTeardownRecoveryPairs(pairs)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		recoveries, err := checkCompletedTeardownRecoveryBatch(ctx, connection, validated)
		if err != nil {
			return struct{}{}, err
		}
		for _, recovery := range recoveries {
			if recovery.AlreadyRecovered {
				continue
			}
			result, err := connection.ExecContext(ctx, `
UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND host_key = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`,
				formatStoredTime(now), formatStoredTime(now), string(recovery.Pair.SessionID), reservationHostKey)
			if err != nil {
				return struct{}{}, fmt.Errorf("release completed-teardown session reservation: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
			}
			if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_completed_teardown_recovery_finalizations (session_id, command_id, job_id, capacity_released_at)
VALUES (?, ?, ?, ?)`, string(recovery.Pair.SessionID), string(recovery.Pair.CommandID), string(recovery.JobID), formatStoredTime(now)); err != nil {
				return struct{}{}, fmt.Errorf("record completed-teardown finalization: %w", err)
			}
		}
		return struct{}{}, nil
	})
	return err
}

// ListPendingCompletedTeardownRecoveryFinalizations validates exact released
// records without requiring unrelated work to be idle. Startup uses this
// finalization-only path before auditing retained runtime ownership.
func (s *AuthorityStore) ListPendingCompletedTeardownRecoveryFinalizations(ctx context.Context) ([]CompletedTeardownRecovery, error) {
	if s == nil || s.db == nil {
		return nil, ErrCompletedTeardownRecoveryNotReleasable
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]CompletedTeardownRecovery, error) {
		pairs, err := pendingCompletedTeardownPairs(ctx, connection)
		if err != nil {
			return nil, err
		}
		results := make([]CompletedTeardownRecovery, 0, len(pairs))
		for _, pair := range pairs {
			result, err := readCompletedTeardownRecovery(ctx, connection, pair)
			if err != nil {
				return nil, err
			}
			if !result.AlreadyRecovered || !result.FinalizationPending {
				return nil, ErrCompletedTeardownRecoveryNotReleasable
			}
			results = append(results, result)
		}
		return results, nil
	})
}

// CompleteCompletedTeardownRecoveryFinalization removes only the exact work
// item after marker/workspace finalization. A repeat is a no-op when the same
// successful-command/lost-session boundary is still fully released.
func (s *AuthorityStore) CompleteCompletedTeardownRecoveryFinalization(ctx context.Context, pair CompletedTeardownRecoveryPair) error {
	if s == nil || s.db == nil {
		return ErrCompletedTeardownRecoveryNotReleasable
	}
	validated, err := validateCompletedTeardownRecoveryPairs([]CompletedTeardownRecoveryPair{pair})
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		recovery, err := readCompletedTeardownRecovery(ctx, connection, validated[0])
		if err != nil {
			return struct{}{}, err
		}
		if !recovery.AlreadyRecovered {
			return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
		}
		if !recovery.FinalizationPending {
			return struct{}{}, nil
		}
		result, err := connection.ExecContext(ctx, `
DELETE FROM exec_completed_teardown_recovery_finalizations
WHERE session_id = ? AND command_id = ? AND job_id = ?`, string(pair.SessionID), string(pair.CommandID), string(recovery.JobID))
		if err != nil {
			return struct{}{}, fmt.Errorf("complete completed-teardown finalization: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
		}
		return struct{}{}, nil
	})
	return err
}

func validateCompletedTeardownRecoveryPairs(pairs []CompletedTeardownRecoveryPair) ([]CompletedTeardownRecoveryPair, error) {
	if len(pairs) == 0 {
		return nil, ErrCompletedTeardownRecoveryNotReleasable
	}
	validated := append([]CompletedTeardownRecoveryPair(nil), pairs...)
	sessions, commands := map[domain.SessionID]bool{}, map[domain.CommandID]bool{}
	for _, pair := range validated {
		if _, err := domain.NewSessionID(string(pair.SessionID)); err != nil {
			return nil, ErrCompletedTeardownRecoveryNotReleasable
		}
		if _, err := domain.NewCommandID(string(pair.CommandID)); err != nil {
			return nil, ErrCompletedTeardownRecoveryNotReleasable
		}
		if sessions[pair.SessionID] || commands[pair.CommandID] {
			return nil, ErrCompletedTeardownRecoveryNotReleasable
		}
		sessions[pair.SessionID], commands[pair.CommandID] = true, true
	}
	return validated, nil
}

func checkCompletedTeardownRecoveryBatch(ctx context.Context, connection *sql.Conn, pairs []CompletedTeardownRecoveryPair) ([]CompletedTeardownRecovery, error) {
	results := make([]CompletedTeardownRecovery, 0, len(pairs))
	selected := map[domain.SessionID]bool{}
	for _, pair := range pairs {
		recovery, err := readCompletedTeardownRecovery(ctx, connection, pair)
		if err != nil {
			return nil, err
		}
		results = append(results, recovery)
		selected[pair.SessionID] = true
	}
	var unsafe int
	if err := connection.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM exec_sessions WHERE state NOT IN ('closed','expired','failed','lost')) +
 (SELECT COUNT(*) FROM exec_commands WHERE state NOT IN ('succeeded','failed','cancelled','timed_out','rejected','lost')) +
 (SELECT COUNT(*) FROM exec_jobs WHERE phase NOT IN ('complete','failed','lost')) +
 (SELECT COUNT(*) FROM exec_command_slots WHERE stop_confirmed_at IS NULL OR released_at IS NULL) +
 (SELECT COUNT(*) FROM exec_lost_runtime_recovery_finalizations) +
 (SELECT COUNT(*) FROM exec_commandless_lost_runtime_recovery_finalizations)`).Scan(&unsafe); err != nil {
		return nil, fmt.Errorf("read completed-teardown idle inventory: %w", err)
	}
	if unsafe != 0 {
		return nil, ErrCompletedTeardownRecoveryNotReleasable
	}
	rows, err := connection.QueryContext(ctx, `
SELECT session_id FROM exec_capacity_reservations
WHERE cleanup_confirmed_at IS NULL OR released_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("read completed-teardown reservation inventory: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !selected[domain.SessionID(id)] {
			_ = rows.Close()
			return nil, ErrCompletedTeardownRecoveryNotReleasable
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	pending, err := pendingCompletedTeardownPairs(ctx, connection)
	if err != nil {
		return nil, err
	}
	for _, pair := range pending {
		if !selected[pair.SessionID] {
			return nil, ErrCompletedTeardownRecoveryNotReleasable
		}
	}
	return results, nil
}

func pendingCompletedTeardownPairs(ctx context.Context, connection *sql.Conn) ([]CompletedTeardownRecoveryPair, error) {
	rows, err := connection.QueryContext(ctx, `SELECT session_id, command_id
FROM exec_completed_teardown_recovery_finalizations ORDER BY capacity_released_at, session_id`)
	if err != nil {
		return nil, fmt.Errorf("read completed-teardown pending finalizations: %w", err)
	}
	defer rows.Close()
	pairs := make([]CompletedTeardownRecoveryPair, 0)
	for rows.Next() {
		var pair CompletedTeardownRecoveryPair
		if err := rows.Scan(&pair.SessionID, &pair.CommandID); err != nil {
			return nil, err
		}
		if _, err := validateCompletedTeardownRecoveryPairs([]CompletedTeardownRecoveryPair{pair}); err != nil {
			return nil, err
		}
		pairs = append(pairs, pair)
	}
	return pairs, rows.Err()
}

func readCompletedTeardownRecovery(ctx context.Context, connection *sql.Conn, pair CompletedTeardownRecoveryPair) (CompletedTeardownRecovery, error) {
	result := CompletedTeardownRecovery{Pair: pair}
	var jobID string
	var finalizations, matchingFinalizations int
	// SQL compares immutable ownership/selection/source fields directly and
	// selects only metadata; it never loads script_bytes or payload_json.
	if err := connection.QueryRowContext(ctx, `
SELECT j.job_id,
 (SELECT COUNT(*) FROM exec_completed_teardown_recovery_finalizations f WHERE f.session_id = s.session_id OR f.command_id = c.command_id OR f.job_id = j.job_id),
 (SELECT COUNT(*) FROM exec_completed_teardown_recovery_finalizations f WHERE f.session_id = s.session_id AND f.command_id = c.command_id AND f.job_id = j.job_id)
FROM exec_sessions s
JOIN exec_commands c ON c.session_id = s.session_id
JOIN exec_jobs j ON j.session_id = s.session_id AND j.command_id = c.command_id
WHERE s.session_id = ? AND c.command_id = ?
 AND s.state = 'lost' AND length(s.runtime_generation) > 0
 AND s.target_kind = 'local' AND s.controller_type = 'local_user'
 AND j.controller_type = s.controller_type AND j.controller_id = s.controller_id
 AND j.target_kind = s.target_kind AND j.target_profile = s.target_profile AND j.environment = s.environment
 AND j.source_mode = s.source_mode AND j.source_repository_alias = s.source_repository_alias
 AND j.source_requested_revision = s.source_requested_revision AND j.source_path = s.source_path
 AND j.phase = 'lost' AND j.teardown_state = 'lost' AND j.teardown_reason = 'runtime_cleanup_unconfirmed'
 AND ((c.state = 'succeeded' AND c.exit_code = 0) OR (c.state = 'failed' AND c.exit_code IS NOT NULL AND c.exit_code <> 0))
 AND c.output_complete = 1 AND c.output_truncated = 0 AND c.output_unavailable_reason = ''
 AND j.command_state = c.state AND j.exit_code = c.exit_code AND j.final_event_sequence = c.final_event_sequence
 AND j.output_complete = c.output_complete AND j.output_truncated = c.output_truncated AND j.output_unavailable_reason = c.output_unavailable_reason
 AND c.final_event_sequence > 0
 AND (SELECT COUNT(*) FROM exec_commands other WHERE other.session_id = s.session_id) = 1
 AND (SELECT COUNT(*) FROM exec_jobs other WHERE other.session_id = s.session_id) = 1
 AND (SELECT MAX(sequence) FROM exec_command_events e WHERE e.command_id = c.command_id) = c.final_event_sequence
 AND (SELECT COUNT(*) FROM exec_command_events e WHERE e.command_id = c.command_id) = c.final_event_sequence
 AND (SELECT event_type FROM exec_command_events e WHERE e.command_id = c.command_id AND e.sequence = c.final_event_sequence) = CASE c.state WHEN 'succeeded' THEN 'command_succeeded' WHEN 'failed' THEN 'command_failed' END`,
		string(pair.SessionID), string(pair.CommandID)).Scan(&jobID, &finalizations, &matchingFinalizations); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrCompletedTeardownRecoveryNotReleasable
		}
		return result, fmt.Errorf("read completed-teardown recovery metadata: %w", err)
	}
	var err error
	result.JobID, err = domain.NewJobID(jobID)
	if err != nil || finalizations != matchingFinalizations || matchingFinalizations > 1 {
		return result, ErrCompletedTeardownRecoveryNotReleasable
	}
	result.Session, err = readSessionOnConnection(ctx, connection, pair.SessionID)
	if err != nil {
		return result, err
	}
	result.Command, err = readLostRuntimeRecoveryCommandOnConnection(ctx, connection, pair.CommandID)
	if err != nil {
		return result, err
	}
	result.CommandSlot, err = readCommandSlotOnConnection(ctx, connection, pair.CommandID)
	if err != nil || result.CommandSlot.HostKey != schedulerHostKey || result.CommandSlot.StopConfirmedAt == nil || result.CommandSlot.ReleasedAt == nil {
		return result, ErrCompletedTeardownRecoveryNotReleasable
	}
	result.SessionReservation, err = readReservationOnConnection(ctx, connection, pair.SessionID)
	if err != nil || result.SessionReservation.HostKey != reservationHostKey ||
		(result.SessionReservation.CleanupConfirmedAt == nil) != (result.SessionReservation.ReleasedAt == nil) {
		return result, ErrCompletedTeardownRecoveryNotReleasable
	}
	result.AlreadyRecovered = result.SessionReservation.CleanupConfirmedAt != nil
	result.FinalizationPending = matchingFinalizations == 1
	if result.FinalizationPending && !result.AlreadyRecovered {
		return result, ErrCompletedTeardownRecoveryNotReleasable
	}
	return result, nil
}
