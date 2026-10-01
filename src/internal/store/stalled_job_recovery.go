package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// ErrClosedCancelledJobSettlementNotEligible means a requested one-off job is
// not the narrow, already-terminal cancellation shape that offline recovery
// may checkpoint. The caller must leave it untouched for normal recovery.
var ErrClosedCancelledJobSettlementNotEligible = errors.New("closed cancelled one-off job settlement is not eligible")

// ClosedCancelledJobSettlement is an identity-only result from the offline
// job settlement boundary. It deliberately does not contain the canonical
// payload or script bytes.
type ClosedCancelledJobSettlement struct {
	JobID          domain.JobID
	SessionID      domain.SessionID
	CommandID      domain.CommandID
	AlreadySettled bool
}

// CheckClosedCancelledOneOffJobs validates the exact narrow terminal shape
// required for offline settlement without changing any job. It accepts a
// previously settled result so a recovery retry can resume a later lost-runtime
// step without reopening or re-running the job.
func (s *AuthorityStore) CheckClosedCancelledOneOffJobs(ctx context.Context, jobIDs []domain.JobID) ([]ClosedCancelledJobSettlement, error) {
	validated, err := validateClosedCancelledJobSettlementIDs(jobIDs)
	if err != nil {
		return nil, err
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]ClosedCancelledJobSettlement, error) {
		candidates := make([]closedCancelledJobSettlementCandidate, 0, len(validated))
		for _, jobID := range validated {
			candidate, err := readClosedCancelledJobSettlementCandidate(ctx, connection, jobID)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, candidate)
		}
		return closedCancelledJobSettlementResults(candidates), nil
	})
}

// CountActiveSessions returns the durable session states that can still own a
// runtime boundary. It is intentionally metadata-only so an offline recovery
// command can reject unexpected work without reading request payloads.
func (s *AuthorityStore) CountActiveSessions(ctx context.Context) (int, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire active-session count connection: %w", err)
	}
	defer connection.Close()
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_sessions
WHERE state IN (?, ?, ?, ?, ?)
`, string(domain.SessionStateRequested), string(domain.SessionStateCreating), string(domain.SessionStateReady), string(domain.SessionStateBusy), string(domain.SessionStateClosing)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active sessions: %w", err)
	}
	return count, nil
}

// CountRunningCommands returns command records that still describe an active
// process boundary. It is metadata-only for offline recovery preflight.
func (s *AuthorityStore) CountRunningCommands(ctx context.Context) (int, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire running-command count connection: %w", err)
	}
	defer connection.Close()
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_commands
WHERE state IN (?, ?)
`, string(domain.CommandStateRunning), string(domain.CommandStateCancelling)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count running commands: %w", err)
	}
	return count, nil
}

// ListNonterminalJobIDs returns only coordinator identifiers. It is used by
// offline recovery to prove that its explicit input covers all pending jobs
// without loading any canonical payload or script bytes.
func (s *AuthorityStore) ListNonterminalJobIDs(ctx context.Context) ([]domain.JobID, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire nonterminal-job ID connection: %w", err)
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx, `
SELECT job_id FROM exec_jobs
WHERE phase IN (?, ?, ?, ?)
ORDER BY created_at, job_id
`, string(JobPhaseCreatingSession), string(JobPhaseAcceptingCommand), string(JobPhaseAwaitingCommand), string(JobPhaseClosingSession))
	if err != nil {
		return nil, fmt.Errorf("query nonterminal job IDs: %w", err)
	}
	defer rows.Close()
	jobIDs := make([]domain.JobID, 0)
	for rows.Next() {
		var rawID string
		if err := rows.Scan(&rawID); err != nil {
			return nil, fmt.Errorf("scan nonterminal job ID: %w", err)
		}
		jobID, err := domain.NewJobID(rawID)
		if err != nil {
			return nil, fmt.Errorf("%w: job ID", ErrJobPayloadCorrupt)
		}
		jobIDs = append(jobIDs, jobID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nonterminal job IDs: %w", err)
	}
	return jobIDs, nil
}

// SettleClosedCancelledOneOffJobs atomically marks explicitly selected jobs
// complete only after their durable child session and command prove that a
// queued command was cancelled before it started. It reads no canonical
// payload or script bytes and never calls an execution or scheduler path.
//
// The intentionally narrow two-event history is the proof that the command
// never crossed a command execution boundary. Any broader history must be
// handled by ordinary runner reconciliation instead of this operator repair.
func (s *AuthorityStore) SettleClosedCancelledOneOffJobs(ctx context.Context, jobIDs []domain.JobID) ([]ClosedCancelledJobSettlement, error) {
	validated, err := validateClosedCancelledJobSettlementIDs(jobIDs)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]ClosedCancelledJobSettlement, error) {
		candidates := make([]closedCancelledJobSettlementCandidate, 0, len(validated))
		for _, jobID := range validated {
			candidate, err := readClosedCancelledJobSettlementCandidate(ctx, connection, jobID)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, candidate)
		}

		results := closedCancelledJobSettlementResults(candidates)
		for _, candidate := range candidates {
			if !candidate.alreadySettled {
				update, err := connection.ExecContext(ctx, `
UPDATE exec_jobs
SET phase = ?, command_state = ?, exit_code = NULL, final_event_sequence = ?,
    output_truncated = 0, output_complete = 1, output_unavailable_reason = '',
    teardown_state = ?, teardown_reason = ?, updated_at = ?
WHERE job_id = ? AND phase = ?`,
					string(JobPhaseComplete), string(domain.CommandStateCancelled), candidate.finalEventSequence,
					string(JobTeardownClosed), "runtime_closed", formatStoredTime(now),
					string(candidate.jobID), string(JobPhaseAwaitingCommand))
				if err != nil {
					return nil, fmt.Errorf("settle closed cancelled job: %w", err)
				}
				changed, err := update.RowsAffected()
				if err != nil {
					return nil, fmt.Errorf("read closed cancelled job settlement result: %w", err)
				}
				if changed != 1 {
					return nil, fmt.Errorf("%w: job update changed %d rows", ErrClosedCancelledJobSettlementNotEligible, changed)
				}
			}
		}
		return results, nil
	})
}

func closedCancelledJobSettlementResults(candidates []closedCancelledJobSettlementCandidate) []ClosedCancelledJobSettlement {
	results := make([]ClosedCancelledJobSettlement, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, ClosedCancelledJobSettlement{
			JobID:          candidate.jobID,
			SessionID:      candidate.sessionID,
			CommandID:      candidate.commandID,
			AlreadySettled: candidate.alreadySettled,
		})
	}
	return results
}

func validateClosedCancelledJobSettlementIDs(jobIDs []domain.JobID) ([]domain.JobID, error) {
	if len(jobIDs) == 0 {
		return nil, ErrClosedCancelledJobSettlementNotEligible
	}
	validated := make([]domain.JobID, 0, len(jobIDs))
	seen := make(map[domain.JobID]struct{}, len(jobIDs))
	for _, jobID := range jobIDs {
		value, err := domain.NewJobID(string(jobID))
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, ErrClosedCancelledJobSettlementNotEligible
		}
		seen[value] = struct{}{}
		validated = append(validated, value)
	}
	return validated, nil
}

type closedCancelledJobSettlementCandidate struct {
	jobID              domain.JobID
	sessionID          domain.SessionID
	commandID          domain.CommandID
	finalEventSequence int64
	alreadySettled     bool
}

func readClosedCancelledJobSettlementCandidate(ctx context.Context, connection *sql.Conn, jobID domain.JobID) (closedCancelledJobSettlementCandidate, error) {
	var candidate closedCancelledJobSettlementCandidate
	var storedJobID, jobSessionID, jobCommandID, jobPhase, jobTeardownState, jobTeardownReason string
	var jobCommandState sql.NullString
	var jobExitCode, jobFinalSequence sql.NullInt64
	var jobOutputTruncated, jobOutputComplete int
	var jobOutputUnavailable string
	var sessionState string
	var commandSessionID, commandState string
	var commandExitCode, commandFinalSequence sql.NullInt64
	var commandOutputTruncated, commandOutputComplete int
	var reservationHost string
	var reservationCleanup, reservationReleased sql.NullString
	var slotCommandID, slotHost sql.NullString
	var slotStop, slotReleased sql.NullString
	err := connection.QueryRowContext(ctx, `
SELECT j.job_id, j.session_id, j.command_id, j.phase,
       j.command_state, j.exit_code, j.final_event_sequence,
       j.output_truncated, j.output_complete, j.output_unavailable_reason,
       j.teardown_state, j.teardown_reason,
       s.state,
       c.session_id, c.state, c.exit_code, c.final_event_sequence,
       c.output_truncated, c.output_complete,
       reservation.host_key, reservation.cleanup_confirmed_at, reservation.released_at,
       slot.command_id, slot.host_key, slot.stop_confirmed_at, slot.released_at
FROM exec_jobs AS j
JOIN exec_sessions AS s ON s.session_id = j.session_id
JOIN exec_commands AS c ON c.command_id = j.command_id
JOIN exec_capacity_reservations AS reservation ON reservation.session_id = j.session_id
LEFT JOIN exec_command_slots AS slot ON slot.command_id = c.command_id
WHERE j.job_id = ?`, string(jobID)).Scan(
		&storedJobID, &jobSessionID, &jobCommandID, &jobPhase,
		&jobCommandState, &jobExitCode, &jobFinalSequence,
		&jobOutputTruncated, &jobOutputComplete, &jobOutputUnavailable,
		&jobTeardownState, &jobTeardownReason,
		&sessionState,
		&commandSessionID, &commandState, &commandExitCode, &commandFinalSequence,
		&commandOutputTruncated, &commandOutputComplete,
		&reservationHost, &reservationCleanup, &reservationReleased,
		&slotCommandID, &slotHost, &slotStop, &slotReleased,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, ErrJobNotFound
		}
		return candidate, fmt.Errorf("read closed cancelled job settlement state: %w", err)
	}
	validatedJobID, err := domain.NewJobID(storedJobID)
	if err != nil || validatedJobID != jobID {
		return candidate, fmt.Errorf("%w: job identity", ErrClosedCancelledJobSettlementNotEligible)
	}
	sessionID, err := domain.NewSessionID(jobSessionID)
	if err != nil {
		return candidate, fmt.Errorf("%w: session identity", ErrClosedCancelledJobSettlementNotEligible)
	}
	commandID, err := domain.NewCommandID(jobCommandID)
	if err != nil {
		return candidate, fmt.Errorf("%w: command identity", ErrClosedCancelledJobSettlementNotEligible)
	}
	if commandSessionID != jobSessionID || sessionState != string(domain.SessionStateClosed) || commandState != string(domain.CommandStateCancelled) {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	if commandExitCode.Valid || !commandFinalSequence.Valid || commandFinalSequence.Int64 != 2 || commandOutputTruncated != 0 || commandOutputComplete != 1 {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	if reservationHost != reservationHostKey || !reservationCleanup.Valid || !reservationReleased.Valid {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	if slotCommandID.Valid && (slotCommandID.String != jobCommandID || slotHost.String != schedulerHostKey || slotStop.Valid != slotReleased.Valid || !slotStop.Valid) {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	var otherNonterminal int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_commands
WHERE session_id = ? AND command_id != ? AND state IN (?, ?, ?)
`, jobSessionID, jobCommandID, string(domain.CommandStateQueued), string(domain.CommandStateRunning), string(domain.CommandStateCancelling)).Scan(&otherNonterminal); err != nil {
		return candidate, fmt.Errorf("count closed cancelled job sibling commands: %w", err)
	}
	if otherNonterminal != 0 {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	if err := requireExactlyQueuedThenCancelledEvents(ctx, connection, commandID); err != nil {
		return candidate, err
	}

	alreadySettled := jobPhase == string(JobPhaseComplete)
	if !alreadySettled && (jobPhase != string(JobPhaseAwaitingCommand) || jobTeardownState != string(JobTeardownPending)) {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	if alreadySettled && (!jobCommandState.Valid || jobCommandState.String != string(domain.CommandStateCancelled) || jobExitCode.Valid || !jobFinalSequence.Valid || jobFinalSequence.Int64 != commandFinalSequence.Int64 || jobOutputTruncated != 0 || jobOutputComplete != 1 || jobOutputUnavailable != "" || jobTeardownState != string(JobTeardownClosed) || jobTeardownReason != "runtime_closed") {
		return candidate, ErrClosedCancelledJobSettlementNotEligible
	}
	candidate = closedCancelledJobSettlementCandidate{
		jobID: jobID, sessionID: sessionID, commandID: commandID,
		finalEventSequence: commandFinalSequence.Int64, alreadySettled: alreadySettled,
	}
	return candidate, nil
}

func requireExactlyQueuedThenCancelledEvents(ctx context.Context, connection *sql.Conn, commandID domain.CommandID) error {
	rows, err := connection.QueryContext(ctx, `
SELECT sequence, event_type FROM exec_command_events
WHERE command_id = ?
ORDER BY sequence`, string(commandID))
	if err != nil {
		return fmt.Errorf("read closed cancelled job event boundary: %w", err)
	}
	defer rows.Close()
	expected := []string{"command_queued", "command_cancelled"}
	index := 0
	for rows.Next() {
		var sequence int64
		var eventType string
		if err := rows.Scan(&sequence, &eventType); err != nil {
			return fmt.Errorf("scan closed cancelled job event boundary: %w", err)
		}
		if index >= len(expected) || sequence != int64(index+1) || eventType != expected[index] {
			return ErrClosedCancelledJobSettlementNotEligible
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate closed cancelled job event boundary: %w", err)
	}
	if index != len(expected) {
		return ErrClosedCancelledJobSettlementNotEligible
	}
	return nil
}
