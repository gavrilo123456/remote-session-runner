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

// ListRetainedLostRuntimeRecoveryPairs returns the exact terminal-lost
// session/command pairs whose matching command-slot and session-capacity
// reservations are both still retained. It intentionally reads neither script
// bytes nor event payloads. Callers must still use the recovery transaction as
// the final concurrency and inventory boundary before releasing capacity.
func (s *AuthorityStore) ListRetainedLostRuntimeRecoveryPairs(ctx context.Context) ([]LostRuntimeRecoveryPair, error) {
	if s == nil || s.db == nil {
		return nil, ErrLostRuntimeRecoveryNotReleasable
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]LostRuntimeRecoveryPair, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT session.session_id, command.command_id
FROM exec_commands AS command
JOIN exec_sessions AS session ON session.session_id = command.session_id
JOIN exec_command_slots AS slot ON slot.command_id = command.command_id
JOIN exec_capacity_reservations AS reservation ON reservation.session_id = session.session_id
JOIN exec_command_events AS event ON event.command_id = command.command_id
    AND event.sequence = command.final_event_sequence
WHERE command.state = ?
  AND session.state = ?
  AND command.output_complete = 0
  AND command.final_event_sequence IS NOT NULL
  AND event.event_type = 'command_lost'
  AND slot.host_key = ?
  AND slot.stop_confirmed_at IS NULL
  AND slot.released_at IS NULL
  AND reservation.host_key = ?
  AND reservation.cleanup_confirmed_at IS NULL
  AND reservation.released_at IS NULL
ORDER BY slot.reserved_at, command.command_id`,
			string(domain.CommandStateLost), string(domain.SessionStateLost), schedulerHostKey, reservationHostKey)
		if err != nil {
			return nil, fmt.Errorf("query retained lost runtime recovery pairs: %w", err)
		}
		defer rows.Close()

		pairs := make([]LostRuntimeRecoveryPair, 0)
		for rows.Next() {
			var sessionIDValue, commandIDValue string
			if err := rows.Scan(&sessionIDValue, &commandIDValue); err != nil {
				return nil, fmt.Errorf("scan retained lost runtime recovery pair: %w", err)
			}
			sessionID, err := domain.NewSessionID(sessionIDValue)
			if err != nil {
				return nil, fmt.Errorf("%w: retained lost recovery session identity", ErrLostRuntimeRecoveryNotReleasable)
			}
			commandID, err := domain.NewCommandID(commandIDValue)
			if err != nil {
				return nil, fmt.Errorf("%w: retained lost recovery command identity", ErrLostRuntimeRecoveryNotReleasable)
			}
			pairs = append(pairs, LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate retained lost runtime recovery pairs: %w", err)
		}
		return pairs, nil
	})
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

// CheckLostRuntimeRecoveryBatchPreservingQueuedOneOffs verifies the narrow
// online-repair shape used when terminal lost runtimes have retained the
// complete configured command capacity, while one-off jobs are durably queued
// behind them.
//
// It accepts only selected, fully retained lost pairs and unrelated sessions
// that are exactly ready with one awaiting_command job and one never-started
// queued command. It performs no runtime action and no durable write. The
// confirmation method repeats these checks in one immediate transaction
// before releasing capacity, so this read-only preflight is never the sole
// authorization boundary.
func (s *AuthorityStore) CheckLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx context.Context, pairs []LostRuntimeRecoveryPair) error {
	validated, err := validateLostRuntimeRecoveryPairs(pairs)
	if err != nil {
		return err
	}
	_, err = withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		_, err := checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, connection, validated)
		return struct{}{}, err
	})
	return err
}

// ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs atomically releases
// an explicit set of fully retained terminal-lost pairs while preserving
// unrelated, already-queued one-off jobs. It is deliberately narrower than
// ConfirmLostRuntimeRecoveryBatch: every configured live command slot must
// belong to a selected pair and the configured command capacity must be fully
// occupied; no command may be running or cancelling; and every other live
// session reservation must prove the exact ready/awaiting/queued shape.
//
// Callers must first obtain a runtime cleanup proof for every selected pair.
// This store boundary never executes commands or changes the preserved job,
// session, command, idempotency, ordinal, or event history records.
func (s *AuthorityStore) ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx context.Context, pairs []LostRuntimeRecoveryPair) error {
	validated, err := validateLostRuntimeRecoveryPairs(pairs)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		unreleased, err := checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, connection, validated)
		if err != nil {
			return struct{}{}, err
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

// checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs is shared by the
// read-only preflight and the immediate confirmation transaction. It returns
// only pairs whose capacity is still retained. Fully released pairs remain
// valid for an idempotent finalization retry, matching the offline recovery
// behavior, but never authorize a release of unrelated capacity.
func checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx context.Context, connection *sql.Conn, pairs []LostRuntimeRecoveryPair) ([]LostRuntimeRecoveryPair, error) {
	unreleased := make([]LostRuntimeRecoveryPair, 0, len(pairs))
	selectedSessions := make(map[domain.SessionID]struct{}, len(pairs))
	selectedLiveCommands := make(map[domain.CommandID]struct{}, len(pairs))
	for _, pair := range pairs {
		selectedSessions[pair.SessionID] = struct{}{}
		alreadyReleased, err := checkLostRuntimeRecoveryPair(ctx, connection, pair)
		if err != nil {
			return nil, err
		}
		if err := requireLostRecoveryTerminalSessionBoundary(ctx, connection, pair); err != nil {
			return nil, err
		}
		if !alreadyReleased {
			unreleased = append(unreleased, pair)
			selectedLiveCommands[pair.CommandID] = struct{}{}
		}
	}
	// A previous attempt may have durably released every selected pair before
	// its runtime ownership-marker finalization failed. That retry performs no
	// capacity write, so it must not be blocked by work that a dispatcher could
	// have started after capacity became available. Each selected pair and its
	// terminal boundary were still validated above; mixed retained/released
	// pairs continue through the complete inventory boundary below.
	if len(unreleased) == 0 {
		return unreleased, nil
	}

	if err := requireNoRunningOrCancellingCommands(ctx, connection); err != nil {
		return nil, err
	}
	if err := requireFullyOccupiedLiveCommandSlotsSelected(ctx, connection, selectedLiveCommands); err != nil {
		return nil, err
	}
	if err := requireOnlySelectedOrQueuedOneOffReservations(ctx, connection, selectedSessions); err != nil {
		return nil, err
	}
	return unreleased, nil
}

func requireLostRecoveryTerminalSessionBoundary(ctx context.Context, connection *sql.Conn, pair LostRuntimeRecoveryPair) error {
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, state
FROM exec_commands
WHERE session_id = ?
ORDER BY ordinal`, string(pair.SessionID))
	if err != nil {
		return fmt.Errorf("read lost-recovery session commands: %w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var commandIDValue, stateValue string
		if err := rows.Scan(&commandIDValue, &stateValue); err != nil {
			return fmt.Errorf("scan lost-recovery session command: %w", err)
		}
		commandID, err := domain.NewCommandID(commandIDValue)
		if err != nil {
			return fmt.Errorf("%w: session command identity", ErrLostRuntimeRecoveryNotReleasable)
		}
		state := domain.CommandState(stateValue)
		if !state.Valid() {
			return fmt.Errorf("%w: session command state", ErrLostRuntimeRecoveryNotReleasable)
		}
		if commandID == pair.CommandID {
			found = true
			continue
		}
		if !state.IsTerminal() {
			return fmt.Errorf("%w: selected session has nonterminal command", ErrLostRuntimeRecoveryNotReleasable)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate lost-recovery session commands: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: selected command is absent from session", ErrLostRuntimeRecoveryNotReleasable)
	}
	return nil
}

func requireNoRunningOrCancellingCommands(ctx context.Context, connection *sql.Conn) error {
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_commands
WHERE state IN (?, ?)`, string(domain.CommandStateRunning), string(domain.CommandStateCancelling)).Scan(&count); err != nil {
		return fmt.Errorf("count active command boundaries: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("%w: active command boundary", ErrLostRuntimeRecoveryNotReleasable)
	}
	return nil
}

func requireFullyOccupiedLiveCommandSlotsSelected(ctx context.Context, connection *sql.Conn, selected map[domain.CommandID]struct{}) error {
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, host_key
FROM exec_command_slots
WHERE stop_confirmed_at IS NULL`)
	if err != nil {
		return fmt.Errorf("query live command slots: %w", err)
	}
	defer rows.Close()
	liveSlots := 0
	for rows.Next() {
		var commandIDValue, hostKey string
		if err := rows.Scan(&commandIDValue, &hostKey); err != nil {
			return fmt.Errorf("scan live command slot: %w", err)
		}
		commandID, err := domain.NewCommandID(commandIDValue)
		if err != nil || hostKey != schedulerHostKey {
			return fmt.Errorf("%w: live command slot host or identity", ErrLostRuntimeRecoveryNotReleasable)
		}
		if _, ok := selected[commandID]; !ok {
			return fmt.Errorf("%w: unselected live command slot", ErrLostRuntimeRecoveryNotReleasable)
		}
		liveSlots++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate live command slots: %w", err)
	}
	// A retry after an earlier durable paired release has no live slots and no
	// retained selected pair. It may proceed only to idempotent finalization;
	// any half-released pair was rejected by checkLostRuntimeRecoveryPair above.
	if liveSlots == 0 && len(selected) == 0 {
		return nil
	}
	if liveSlots != DefaultRunningCommandLimit || len(selected) != DefaultRunningCommandLimit {
		return fmt.Errorf("%w: live command slots=%d selected lost pairs=%d, want full configured capacity %d", ErrLostRuntimeRecoveryNotReleasable, liveSlots, len(selected), DefaultRunningCommandLimit)
	}
	return nil
}

func requireOnlySelectedOrQueuedOneOffReservations(ctx context.Context, connection *sql.Conn, selected map[domain.SessionID]struct{}) error {
	rows, err := connection.QueryContext(ctx, `
SELECT session_id, host_key
FROM exec_capacity_reservations
WHERE cleanup_confirmed_at IS NULL`)
	if err != nil {
		return fmt.Errorf("query live session reservations: %w", err)
	}
	defer rows.Close()
	type liveReservation struct {
		sessionID domain.SessionID
		hostKey   string
	}
	liveReservations := make([]liveReservation, 0)
	for rows.Next() {
		var sessionIDValue, hostKey string
		if err := rows.Scan(&sessionIDValue, &hostKey); err != nil {
			return fmt.Errorf("scan live session reservation: %w", err)
		}
		sessionID, err := domain.NewSessionID(sessionIDValue)
		if err != nil {
			return fmt.Errorf("%w: live session reservation host or identity", ErrLostRuntimeRecoveryNotReleasable)
		}
		liveReservations = append(liveReservations, liveReservation{sessionID: sessionID, hostKey: hostKey})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate live session reservations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close live session reservations: %w", err)
	}

	safeQueuedJobs := make(map[domain.JobID]struct{})
	safeQueuedCommands := make(map[domain.CommandID]domain.SessionID)
	for _, reservation := range liveReservations {
		if reservation.hostKey != reservationHostKey {
			return fmt.Errorf("%w: live session reservation host or identity", ErrLostRuntimeRecoveryNotReleasable)
		}
		if _, ok := selected[reservation.sessionID]; ok {
			continue
		}
		jobID, commandID, err := requireSafeQueuedOneOffReservation(ctx, connection, reservation.sessionID)
		if err != nil {
			return err
		}
		safeQueuedJobs[jobID] = struct{}{}
		safeQueuedCommands[commandID] = reservation.sessionID
	}
	if err := requireOnlyPreservedSchedulerEligibleQueuedCommands(ctx, connection, safeQueuedCommands); err != nil {
		return err
	}

	jobRows, err := connection.QueryContext(ctx, `
SELECT job_id, phase
FROM exec_jobs
WHERE phase IN (?, ?, ?, ?)
ORDER BY created_at, job_id`,
		string(JobPhaseCreatingSession), string(JobPhaseAcceptingCommand), string(JobPhaseAwaitingCommand), string(JobPhaseClosingSession))
	if err != nil {
		return fmt.Errorf("query nonterminal jobs during lost recovery: %w", err)
	}
	defer jobRows.Close()
	for jobRows.Next() {
		var jobIDValue, phase string
		if err := jobRows.Scan(&jobIDValue, &phase); err != nil {
			return fmt.Errorf("scan nonterminal job during lost recovery: %w", err)
		}
		jobID, err := domain.NewJobID(jobIDValue)
		if err != nil || phase != string(JobPhaseAwaitingCommand) {
			return fmt.Errorf("%w: nonterminal job is not a preserved queued one-off", ErrLostRuntimeRecoveryNotReleasable)
		}
		if _, ok := safeQueuedJobs[jobID]; !ok {
			return fmt.Errorf("%w: nonterminal job lacks a preserved queued reservation", ErrLostRuntimeRecoveryNotReleasable)
		}
	}
	if err := jobRows.Err(); err != nil {
		return fmt.Errorf("iterate nonterminal jobs during lost recovery: %w", err)
	}
	return nil
}

// requireOnlyPreservedSchedulerEligibleQueuedCommands closes the gap between
// reservation accounting and scheduler eligibility: the scheduler selects a
// queued command on a ready session without consulting the reservation row.
// A released reservation must therefore never make an unrelated queued
// command invisible to this recovery boundary.
func requireOnlyPreservedSchedulerEligibleQueuedCommands(ctx context.Context, connection *sql.Conn, safe map[domain.CommandID]domain.SessionID) error {
	rows, err := connection.QueryContext(ctx, `
SELECT command.command_id, command.session_id
FROM exec_commands AS command
JOIN exec_sessions AS session ON session.session_id = command.session_id
WHERE command.state = ? AND session.state = ?
ORDER BY command.created_at, command.session_id, command.ordinal`, string(domain.CommandStateQueued), string(domain.SessionStateReady))
	if err != nil {
		return fmt.Errorf("query scheduler-eligible queued commands: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var commandIDValue, sessionIDValue string
		if err := rows.Scan(&commandIDValue, &sessionIDValue); err != nil {
			return fmt.Errorf("scan scheduler-eligible queued command: %w", err)
		}
		commandID, err := domain.NewCommandID(commandIDValue)
		if err != nil {
			return fmt.Errorf("%w: scheduler-eligible command identity", ErrLostRuntimeRecoveryNotReleasable)
		}
		sessionID, err := domain.NewSessionID(sessionIDValue)
		if err != nil {
			return fmt.Errorf("%w: scheduler-eligible session identity", ErrLostRuntimeRecoveryNotReleasable)
		}
		if expectedSessionID, ok := safe[commandID]; !ok || expectedSessionID != sessionID {
			return fmt.Errorf("%w: scheduler-eligible queued command is not a preserved one-off", ErrLostRuntimeRecoveryNotReleasable)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate scheduler-eligible queued commands: %w", err)
	}
	return nil
}

// requireSafeQueuedOneOffReservation accepts only an awaiting one-off that
// has not crossed a command execution boundary. It intentionally reads no
// canonical payload, script bytes, or event payload.
func requireSafeQueuedOneOffReservation(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID) (domain.JobID, domain.CommandID, error) {
	var reservationHost, sessionState string
	var jobIDValue, jobSessionID, jobCommandID, jobPhase, jobTeardownState, jobTeardownReason string
	var jobCommandState sql.NullString
	var jobExitCode, jobFinalSequence sql.NullInt64
	var jobOutputTruncated, jobOutputComplete int
	var jobOutputUnavailable string
	var commandSessionID, commandState string
	var commandExitCode, commandFinalSequence sql.NullInt64
	var commandOutputTruncated, commandOutputComplete int
	var commandOutputUnavailable string
	var sessionCommandCount, commandSlotCount int
	err := connection.QueryRowContext(ctx, `
SELECT reservation.host_key, session.state,
       job.job_id, job.session_id, job.command_id, job.phase,
       job.command_state, job.exit_code, job.final_event_sequence,
       job.output_truncated, job.output_complete, job.output_unavailable_reason,
       job.teardown_state, job.teardown_reason,
       command.session_id, command.state, command.exit_code, command.final_event_sequence,
       command.output_truncated, command.output_complete, command.output_unavailable_reason,
       (SELECT COUNT(*) FROM exec_commands WHERE session_id = session.session_id),
       (SELECT COUNT(*) FROM exec_command_slots WHERE command_id = command.command_id)
FROM exec_capacity_reservations AS reservation
JOIN exec_sessions AS session ON session.session_id = reservation.session_id
JOIN exec_jobs AS job ON job.session_id = session.session_id
JOIN exec_commands AS command ON command.command_id = job.command_id
WHERE reservation.session_id = ?`, string(sessionID)).Scan(
		&reservationHost, &sessionState,
		&jobIDValue, &jobSessionID, &jobCommandID, &jobPhase,
		&jobCommandState, &jobExitCode, &jobFinalSequence,
		&jobOutputTruncated, &jobOutputComplete, &jobOutputUnavailable,
		&jobTeardownState, &jobTeardownReason,
		&commandSessionID, &commandState, &commandExitCode, &commandFinalSequence,
		&commandOutputTruncated, &commandOutputComplete, &commandOutputUnavailable,
		&sessionCommandCount, &commandSlotCount,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("%w: reservation is not a queued one-off", ErrLostRuntimeRecoveryNotReleasable)
		}
		return "", "", fmt.Errorf("read preserved queued one-off state: %w", err)
	}
	jobID, err := domain.NewJobID(jobIDValue)
	if err != nil || reservationHost != reservationHostKey || jobSessionID != string(sessionID) || commandSessionID != string(sessionID) ||
		sessionState != string(domain.SessionStateReady) || jobPhase != string(JobPhaseAwaitingCommand) ||
		!jobCommandState.Valid || jobCommandState.String != string(domain.CommandStateQueued) ||
		commandState != string(domain.CommandStateQueued) || jobExitCode.Valid || jobFinalSequence.Valid ||
		jobOutputTruncated != 0 || jobOutputComplete != 0 || jobOutputUnavailable != "" ||
		jobTeardownState != string(JobTeardownPending) || jobTeardownReason != "" ||
		commandExitCode.Valid || commandFinalSequence.Valid || commandOutputTruncated != 0 ||
		commandOutputComplete != 0 || commandOutputUnavailable != "" ||
		sessionCommandCount != 1 || commandSlotCount != 0 {
		return "", "", fmt.Errorf("%w: reservation is not an untouched queued one-off", ErrLostRuntimeRecoveryNotReleasable)
	}
	commandID, err := domain.NewCommandID(jobCommandID)
	if err != nil {
		return "", "", fmt.Errorf("%w: queued one-off command identity", ErrLostRuntimeRecoveryNotReleasable)
	}
	if err := requireExactlyQueuedEvent(ctx, connection, commandID); err != nil {
		return "", "", err
	}
	return jobID, commandID, nil
}

func requireExactlyQueuedEvent(ctx context.Context, connection *sql.Conn, commandID domain.CommandID) error {
	rows, err := connection.QueryContext(ctx, `
SELECT sequence, event_type
FROM exec_command_events
WHERE command_id = ?
ORDER BY sequence`, string(commandID))
	if err != nil {
		return fmt.Errorf("read queued one-off event boundary: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var sequence int64
		var eventType string
		if err := rows.Scan(&sequence, &eventType); err != nil {
			return fmt.Errorf("scan queued one-off event boundary: %w", err)
		}
		if sequence != 1 || eventType != "command_queued" || count != 0 {
			return fmt.Errorf("%w: queued one-off crossed command event boundary", ErrLostRuntimeRecoveryNotReleasable)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate queued one-off event boundary: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: queued one-off has no exact queued event", ErrLostRuntimeRecoveryNotReleasable)
	}
	return nil
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
