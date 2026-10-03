package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

const controlledRestartPlanKey = 1

var (
	// ErrControlledRestartPlanNotFound means no controlled-restart recovery
	// boundary is currently holding scheduler admission.
	ErrControlledRestartPlanNotFound = errors.New("controlled restart plan not found")
	// ErrControlledRestartPlanConflict means a different controlled-restart
	// plan is already durable. Callers must read or explicitly clear that plan
	// rather than replacing it.
	ErrControlledRestartPlanConflict = errors.New("controlled restart plan conflicts with existing plan")
	// ErrControlledRestartPlanCorrupt means durable plan identities or their
	// relationships failed structural validation. Scheduler admission fails
	// closed for this condition.
	ErrControlledRestartPlanCorrupt = errors.New("controlled restart plan is corrupt")
	// ErrControlledRestartPlanNotActive means a durable controlled-restart
	// plan has not yet received the replacement-runtime activation boundary.
	// Scheduler admission remains closed until activation succeeds.
	ErrControlledRestartPlanNotActive = errors.New("controlled restart plan is not active")
)

// ControlledRestartPlan is the narrow durable boundary for an intentional
// restart that preserves one untouched, empty-source queued one-off. It never
// contains scripts or canonical request payloads. LostPairs contains exactly
// DefaultRunningCommandLimit terminal-lost pairs while the plan is prepared.
type ControlledRestartPlan struct {
	JobID     domain.JobID
	SessionID domain.SessionID
	CommandID domain.CommandID
	// RuntimeGeneration is the exact durable generation of the queued
	// session that the replacement runtime must rehydrate before activation.
	RuntimeGeneration string
	LostPairs         []LostRuntimeRecoveryPair
	PreparedAt        time.Time
	Activated         bool
	ActivatedAt       *time.Time
}

// PrepareControlledRestartPlan atomically validates and records the only
// controlled-restart shape this PoC permits: all four configured command
// slots are retained by the supplied terminal-lost pairs, and exactly one
// unrelated empty-source one-off remains untouched in ready/awaiting/queued
// state. It deliberately reuses the queue-preserving lost-recovery boundary
// before adding the stricter exactly-one and empty-source requirements.
//
// Repeating a preparation for the same four pairs returns the existing
// prepared plan only after its full pre-activation shape is revalidated. An
// active plan is never reusable as a new preparation: its replacement runtime
// may already own the preserved shell. A different durable plan is never
// overwritten. Preparing a plan does not release capacity, start a runtime,
// or alter the queued job.
func (s *AuthorityStore) PrepareControlledRestartPlan(ctx context.Context, pairs []LostRuntimeRecoveryPair) (ControlledRestartPlan, error) {
	if s == nil || s.db == nil {
		return ControlledRestartPlan{}, ErrNilDatabase
	}
	validatedPairs, err := validateControlledRestartPlanPairs(pairs)
	if err != nil {
		return ControlledRestartPlan{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (ControlledRestartPlan, error) {
		existing, err := readControlledRestartPlanOnConnection(ctx, connection)
		switch {
		case err == nil:
			if existing.Activated {
				return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
			}
			if !sameControlledRestartLostPairs(existing.LostPairs, validatedPairs) {
				return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
			}
			unreleased, err := checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, connection, validatedPairs)
			if err != nil || len(unreleased) != DefaultRunningCommandLimit {
				return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
			}
			jobID, sessionID, commandID, runtimeGeneration, err := findExactEmptySourceQueuedOneOffForControlledRestartPlan(ctx, connection, validatedPairs)
			if err != nil || jobID != existing.JobID || sessionID != existing.SessionID || commandID != existing.CommandID || runtimeGeneration != existing.RuntimeGeneration {
				return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
			}
			return existing, nil
		case !errors.Is(err, ErrControlledRestartPlanNotFound):
			return ControlledRestartPlan{}, err
		}

		unreleased, err := checkLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, connection, validatedPairs)
		if err != nil {
			return ControlledRestartPlan{}, err
		}
		// The generic recovery check admits a retry after a prior release. A
		// restart plan must instead start from the exact fully retained shape.
		if len(unreleased) != DefaultRunningCommandLimit {
			return ControlledRestartPlan{}, fmt.Errorf("%w: controlled restart requires fully retained lost capacity", ErrLostRuntimeRecoveryNotReleasable)
		}

		jobID, sessionID, commandID, runtimeGeneration, err := findExactEmptySourceQueuedOneOffForControlledRestartPlan(ctx, connection, validatedPairs)
		if err != nil {
			return ControlledRestartPlan{}, err
		}
		plan := ControlledRestartPlan{
			JobID:             jobID,
			SessionID:         sessionID,
			CommandID:         commandID,
			RuntimeGeneration: runtimeGeneration,
			LostPairs:         append([]LostRuntimeRecoveryPair(nil), validatedPairs...),
			PreparedAt:        now,
		}
		if err := insertControlledRestartPlanOnConnection(ctx, connection, plan); err != nil {
			return ControlledRestartPlan{}, err
		}
		return cloneControlledRestartPlan(plan), nil
	})
}

// ActivateControlledRestartPlan records the single admission boundary after a
// caller has successfully rebuilt the exact replacement runtime for plan's
// durable session generation. It rechecks the untouched queued one-off in the
// same immediate transaction and never activates a different plan. Repeating
// activation for the same already-active plan is idempotent.
func (s *AuthorityStore) ActivateControlledRestartPlan(ctx context.Context, plan ControlledRestartPlan) (ControlledRestartPlan, error) {
	if s == nil || s.db == nil {
		return ControlledRestartPlan{}, ErrNilDatabase
	}
	expected, err := validateControlledRestartPlan(plan)
	if err != nil {
		return ControlledRestartPlan{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (ControlledRestartPlan, error) {
		current, err := readControlledRestartPlanOnConnection(ctx, connection)
		if err != nil {
			return ControlledRestartPlan{}, err
		}
		if !sameControlledRestartPlanIdentity(current, expected) {
			return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
		}
		if current.Activated {
			return cloneControlledRestartPlan(current), nil
		}
		if expected.Activated || expected.ActivatedAt != nil {
			return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
		}

		jobID, commandID, err := requireSafeQueuedOneOffReservation(ctx, connection, current.SessionID)
		if err != nil || jobID != current.JobID || commandID != current.CommandID {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		runtimeGeneration, err := readControlledRestartQueuedOneOffRuntimeGeneration(ctx, connection, current.JobID, current.SessionID, current.CommandID)
		if err != nil || runtimeGeneration != current.RuntimeGeneration {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}

		now := s.now().UTC()
		result, err := connection.ExecContext(ctx, `
UPDATE exec_controlled_restart_plans
SET activation_state = 'active', activated_at = ?
WHERE plan_key = ?
  AND job_id = ?
  AND session_id = ?
  AND command_id = ?
  AND runtime_generation = ?
  AND prepared_at = ?
  AND activation_state = 'prepared'
  AND activated_at IS NULL`,
			formatStoredTime(now),
			controlledRestartPlanKey, string(current.JobID), string(current.SessionID), string(current.CommandID), current.RuntimeGeneration, formatStoredTime(current.PreparedAt))
		if err != nil {
			return ControlledRestartPlan{}, fmt.Errorf("activate controlled restart plan: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return ControlledRestartPlan{}, fmt.Errorf("read controlled restart activation result: %w", err)
		}
		if changed != 1 {
			return ControlledRestartPlan{}, ErrControlledRestartPlanConflict
		}
		return readControlledRestartPlanOnConnection(ctx, connection)
	})
}

// ReadControlledRestartPlan returns the current durable controlled-restart
// boundary. It validates only the plan's immutable identity relationships and
// its empty-source contract; command eligibility remains the scheduler's
// atomic claim decision.
func (s *AuthorityStore) ReadControlledRestartPlan(ctx context.Context) (ControlledRestartPlan, error) {
	if s == nil || s.db == nil {
		return ControlledRestartPlan{}, ErrNilDatabase
	}
	return withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (ControlledRestartPlan, error) {
		return readControlledRestartPlanOnConnection(ctx, connection)
	})
}

// ClearControlledRestartPlan removes exactly the supplied durable plan. It is
// for an explicit controlled-restart abort or completed cleanup; the
// scheduler uses its private consume helper only after atomically claiming the
// exact planned command.
func (s *AuthorityStore) ClearControlledRestartPlan(ctx context.Context, plan ControlledRestartPlan) error {
	if s == nil || s.db == nil {
		return ErrNilDatabase
	}
	expected, err := validateControlledRestartPlan(plan)
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		current, err := readControlledRestartPlanOnConnection(ctx, connection)
		if err != nil {
			return struct{}{}, err
		}
		if !sameControlledRestartPlan(current, expected) {
			return struct{}{}, ErrControlledRestartPlanConflict
		}
		if err := deleteControlledRestartPlanOnConnection(ctx, connection, current); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

// consumeControlledRestartPlanOnClaim is intentionally private. The
// scheduler calls it in the same immediate transaction after the exact plan
// command has been moved from queued to running and its session to busy. A
// delete failure rolls those state changes back, so a plan cannot disappear
// without its planned durable claim.
func consumeControlledRestartPlanOnClaim(ctx context.Context, connection *sql.Conn, plan ControlledRestartPlan) error {
	if !plan.Activated || plan.ActivatedAt == nil {
		return ErrControlledRestartPlanNotActive
	}
	return deleteControlledRestartPlanOnConnection(ctx, connection, plan)
}

func insertControlledRestartPlanOnConnection(ctx context.Context, connection *sql.Conn, plan ControlledRestartPlan) error {
	result, err := connection.ExecContext(ctx, `
INSERT INTO exec_controlled_restart_plans (
    plan_key, job_id, session_id, command_id, runtime_generation,
    prepared_at, activation_state, activated_at
) VALUES (?, ?, ?, ?, ?, ?, 'prepared', NULL)`,
		controlledRestartPlanKey, string(plan.JobID), string(plan.SessionID), string(plan.CommandID), plan.RuntimeGeneration, formatStoredTime(plan.PreparedAt))
	if err != nil {
		return fmt.Errorf("record controlled restart plan: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read controlled restart plan record result: %w", err)
	}
	if changed != 1 {
		return ErrControlledRestartPlanCorrupt
	}
	for index, pair := range plan.LostPairs {
		result, err := connection.ExecContext(ctx, `
INSERT INTO exec_controlled_restart_plan_lost_pairs (
    plan_key, ordinal, session_id, command_id
) VALUES (?, ?, ?, ?)`,
			controlledRestartPlanKey, index+1, string(pair.SessionID), string(pair.CommandID))
		if err != nil {
			return fmt.Errorf("record controlled restart lost pair: %w", err)
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read controlled restart lost-pair record result: %w", err)
		}
		if changed != 1 {
			return ErrControlledRestartPlanCorrupt
		}
	}
	return nil
}

func readControlledRestartPlanOnConnection(ctx context.Context, connection *sql.Conn) (ControlledRestartPlan, error) {
	var jobIDValue, sessionIDValue, commandIDValue, runtimeGeneration, preparedAtValue, activationState string
	var activatedAtValue sql.NullString
	err := connection.QueryRowContext(ctx, `
SELECT job_id, session_id, command_id, runtime_generation, prepared_at, activation_state, activated_at
FROM exec_controlled_restart_plans
WHERE plan_key = ?`, controlledRestartPlanKey).Scan(&jobIDValue, &sessionIDValue, &commandIDValue, &runtimeGeneration, &preparedAtValue, &activationState, &activatedAtValue)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ControlledRestartPlan{}, ErrControlledRestartPlanNotFound
		}
		return ControlledRestartPlan{}, fmt.Errorf("read controlled restart plan: %w", err)
	}
	jobID, err := domain.NewJobID(jobIDValue)
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	sessionID, err := domain.NewSessionID(sessionIDValue)
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	commandID, err := domain.NewCommandID(commandIDValue)
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	if runtimeGeneration == "" || len(runtimeGeneration) > 256 || strings.IndexByte(runtimeGeneration, 0) >= 0 {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	preparedAt, err := parseStoredTime(preparedAtValue)
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	activated := false
	var activatedAt *time.Time
	switch activationState {
	case "prepared":
		if activatedAtValue.Valid {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
	case "active":
		if !activatedAtValue.Valid {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		value, err := parseStoredTime(activatedAtValue.String)
		if err != nil {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		activated = true
		activatedAt = &value
	default:
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}

	actualRuntimeGeneration, err := readControlledRestartQueuedOneOffRuntimeGeneration(ctx, connection, jobID, sessionID, commandID)
	if err != nil {
		return ControlledRestartPlan{}, err
	}
	if actualRuntimeGeneration != runtimeGeneration {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}

	rows, err := connection.QueryContext(ctx, `
SELECT ordinal, session_id, command_id
FROM exec_controlled_restart_plan_lost_pairs
WHERE plan_key = ?
ORDER BY ordinal`, controlledRestartPlanKey)
	if err != nil {
		return ControlledRestartPlan{}, fmt.Errorf("read controlled restart lost pairs: %w", err)
	}
	defer rows.Close()
	pairs := make([]LostRuntimeRecoveryPair, 0, DefaultRunningCommandLimit)
	wantOrdinal := 1
	for rows.Next() {
		var ordinal int
		var lostSessionIDValue, lostCommandIDValue string
		if err := rows.Scan(&ordinal, &lostSessionIDValue, &lostCommandIDValue); err != nil {
			return ControlledRestartPlan{}, fmt.Errorf("scan controlled restart lost pair: %w", err)
		}
		if ordinal != wantOrdinal {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		wantOrdinal++
		lostSessionID, err := domain.NewSessionID(lostSessionIDValue)
		if err != nil {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		lostCommandID, err := domain.NewCommandID(lostCommandIDValue)
		if err != nil {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		if lostSessionID == sessionID || lostCommandID == commandID {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
		pairs = append(pairs, LostRuntimeRecoveryPair{SessionID: lostSessionID, CommandID: lostCommandID})
	}
	if err := rows.Err(); err != nil {
		return ControlledRestartPlan{}, fmt.Errorf("iterate controlled restart lost pairs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ControlledRestartPlan{}, fmt.Errorf("close controlled restart lost pairs: %w", err)
	}
	validatedPairs, err := validateControlledRestartPlanPairs(pairs)
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	for _, pair := range validatedPairs {
		if err := requireControlledRestartLostPairIdentity(ctx, connection, pair.SessionID, pair.CommandID); err != nil {
			return ControlledRestartPlan{}, err
		}
	}
	return ControlledRestartPlan{
		JobID:             jobID,
		SessionID:         sessionID,
		CommandID:         commandID,
		RuntimeGeneration: runtimeGeneration,
		LostPairs:         validatedPairs,
		PreparedAt:        preparedAt,
		Activated:         activated,
		ActivatedAt:       activatedAt,
	}, nil
}

func readControlledRestartQueuedOneOffRuntimeGeneration(ctx context.Context, connection *sql.Conn, jobID domain.JobID, sessionID domain.SessionID, commandID domain.CommandID) (string, error) {
	var jobSessionID, jobCommandID string
	var runtimeGeneration string
	var sessionSourceMode, sessionRepositoryAlias, sessionRequestedRevision, sessionPath string
	var jobSourceMode, jobRepositoryAlias, jobRequestedRevision, jobPath string
	err := connection.QueryRowContext(ctx, `
SELECT job.session_id, job.command_id,
       session.runtime_generation,
       session.source_mode, session.source_repository_alias, session.source_requested_revision, session.source_path,
       job.source_mode, job.source_repository_alias, job.source_requested_revision, job.source_path
FROM exec_jobs AS job
JOIN exec_sessions AS session ON session.session_id = job.session_id
WHERE job.job_id = ?`, string(jobID)).Scan(
		&jobSessionID, &jobCommandID,
		&runtimeGeneration,
		&sessionSourceMode, &sessionRepositoryAlias, &sessionRequestedRevision, &sessionPath,
		&jobSourceMode, &jobRepositoryAlias, &jobRequestedRevision, &jobPath,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrControlledRestartPlanCorrupt
		}
		return "", fmt.Errorf("read controlled restart queued one-off identity: %w", err)
	}
	if jobSessionID != string(sessionID) || jobCommandID != string(commandID) ||
		runtimeGeneration == "" || len(runtimeGeneration) > 256 || strings.IndexByte(runtimeGeneration, 0) >= 0 ||
		sessionSourceMode != string(domain.SourceModeEmpty) || sessionRepositoryAlias != "" || sessionRequestedRevision != "" || sessionPath != "" ||
		jobSourceMode != string(domain.SourceModeEmpty) || jobRepositoryAlias != "" || jobRequestedRevision != "" || jobPath != "" {
		return "", ErrControlledRestartPlanCorrupt
	}
	var commandSessionID string
	if err := connection.QueryRowContext(ctx, `SELECT session_id FROM exec_commands WHERE command_id = ?`, string(commandID)).Scan(&commandSessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrControlledRestartPlanCorrupt
		}
		return "", fmt.Errorf("read controlled restart queued command identity: %w", err)
	}
	if commandSessionID != string(sessionID) {
		return "", ErrControlledRestartPlanCorrupt
	}
	return runtimeGeneration, nil
}

func requireControlledRestartLostPairIdentity(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID, commandID domain.CommandID) error {
	var commandSessionID string
	if err := connection.QueryRowContext(ctx, `SELECT session_id FROM exec_commands WHERE command_id = ?`, string(commandID)).Scan(&commandSessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrControlledRestartPlanCorrupt
		}
		return fmt.Errorf("read controlled restart lost command identity: %w", err)
	}
	if commandSessionID != string(sessionID) {
		return ErrControlledRestartPlanCorrupt
	}
	return nil
}

func findExactEmptySourceQueuedOneOffForControlledRestartPlan(ctx context.Context, connection *sql.Conn, pairs []LostRuntimeRecoveryPair) (domain.JobID, domain.SessionID, domain.CommandID, string, error) {
	selected := make(map[domain.SessionID]struct{}, len(pairs))
	for _, pair := range pairs {
		selected[pair.SessionID] = struct{}{}
	}
	rows, err := connection.QueryContext(ctx, `
SELECT session_id, host_key
FROM exec_capacity_reservations
WHERE cleanup_confirmed_at IS NULL
ORDER BY session_id`)
	if err != nil {
		return "", "", "", "", fmt.Errorf("query controlled restart live reservations: %w", err)
	}
	defer rows.Close()
	var queuedSessionID domain.SessionID
	queuedCount := 0
	for rows.Next() {
		var sessionIDValue, hostKey string
		if err := rows.Scan(&sessionIDValue, &hostKey); err != nil {
			return "", "", "", "", fmt.Errorf("scan controlled restart live reservation: %w", err)
		}
		sessionID, err := domain.NewSessionID(sessionIDValue)
		if err != nil || hostKey != reservationHostKey {
			return "", "", "", "", ErrLostRuntimeRecoveryNotReleasable
		}
		if _, selected := selected[sessionID]; selected {
			continue
		}
		queuedCount++
		queuedSessionID = sessionID
	}
	if err := rows.Err(); err != nil {
		return "", "", "", "", fmt.Errorf("iterate controlled restart live reservations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", "", "", "", fmt.Errorf("close controlled restart live reservations: %w", err)
	}
	if queuedCount != 1 {
		return "", "", "", "", fmt.Errorf("%w: controlled restart requires exactly one queued one-off", ErrLostRuntimeRecoveryNotReleasable)
	}
	jobID, commandID, err := requireSafeQueuedOneOffReservation(ctx, connection, queuedSessionID)
	if err != nil {
		return "", "", "", "", err
	}
	runtimeGeneration, err := readControlledRestartQueuedOneOffRuntimeGeneration(ctx, connection, jobID, queuedSessionID, commandID)
	if err != nil {
		if errors.Is(err, ErrControlledRestartPlanCorrupt) {
			return "", "", "", "", fmt.Errorf("%w: controlled restart requires an empty-source queued one-off with a runtime generation", ErrLostRuntimeRecoveryNotReleasable)
		}
		return "", "", "", "", err
	}
	return jobID, queuedSessionID, commandID, runtimeGeneration, nil
}

func deleteControlledRestartPlanOnConnection(ctx context.Context, connection *sql.Conn, plan ControlledRestartPlan) error {
	var result sql.Result
	var err error
	if plan.Activated {
		if plan.ActivatedAt == nil {
			return ErrControlledRestartPlanCorrupt
		}
		result, err = connection.ExecContext(ctx, `
DELETE FROM exec_controlled_restart_plans
WHERE plan_key = ?
  AND job_id = ?
  AND session_id = ?
  AND command_id = ?
  AND runtime_generation = ?
  AND prepared_at = ?
	  AND activation_state = 'active'
	  AND activated_at = ?`,
			controlledRestartPlanKey, string(plan.JobID), string(plan.SessionID), string(plan.CommandID), plan.RuntimeGeneration, formatStoredTime(plan.PreparedAt), formatStoredTime(*plan.ActivatedAt))
	} else {
		result, err = connection.ExecContext(ctx, `
DELETE FROM exec_controlled_restart_plans
WHERE plan_key = ?
  AND job_id = ?
  AND session_id = ?
  AND command_id = ?
  AND runtime_generation = ?
  AND prepared_at = ?
  AND activation_state = 'prepared'
  AND activated_at IS NULL`,
			controlledRestartPlanKey, string(plan.JobID), string(plan.SessionID), string(plan.CommandID), plan.RuntimeGeneration, formatStoredTime(plan.PreparedAt))
	}
	if err != nil {
		return fmt.Errorf("delete controlled restart plan: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read controlled restart plan delete result: %w", err)
	}
	if changed != 1 {
		return ErrControlledRestartPlanNotFound
	}
	return nil
}

func validateControlledRestartPlan(plan ControlledRestartPlan) (ControlledRestartPlan, error) {
	jobID, err := domain.NewJobID(string(plan.JobID))
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	sessionID, err := domain.NewSessionID(string(plan.SessionID))
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	commandID, err := domain.NewCommandID(string(plan.CommandID))
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	pairs, err := validateControlledRestartPlanPairs(plan.LostPairs)
	if err != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	if plan.PreparedAt.IsZero() {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	if plan.RuntimeGeneration == "" || len(plan.RuntimeGeneration) > 256 || strings.IndexByte(plan.RuntimeGeneration, 0) >= 0 {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	if plan.Activated && plan.ActivatedAt == nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	if !plan.Activated && plan.ActivatedAt != nil {
		return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
	}
	for _, pair := range pairs {
		if pair.SessionID == sessionID || pair.CommandID == commandID {
			return ControlledRestartPlan{}, ErrControlledRestartPlanCorrupt
		}
	}
	validated := ControlledRestartPlan{
		JobID:             jobID,
		SessionID:         sessionID,
		CommandID:         commandID,
		RuntimeGeneration: plan.RuntimeGeneration,
		LostPairs:         pairs,
		PreparedAt:        plan.PreparedAt.UTC(),
		Activated:         plan.Activated,
	}
	if plan.ActivatedAt != nil {
		value := plan.ActivatedAt.UTC()
		validated.ActivatedAt = &value
	}
	return validated, nil
}

func validateControlledRestartPlanPairs(pairs []LostRuntimeRecoveryPair) ([]LostRuntimeRecoveryPair, error) {
	validated, err := validateLostRuntimeRecoveryPairs(pairs)
	if err != nil {
		return nil, err
	}
	if len(validated) != DefaultRunningCommandLimit {
		return nil, ErrLostRuntimeRecoveryNotReleasable
	}
	sort.Slice(validated, func(left, right int) bool {
		if validated[left].SessionID != validated[right].SessionID {
			return validated[left].SessionID < validated[right].SessionID
		}
		return validated[left].CommandID < validated[right].CommandID
	})
	return validated, nil
}

func sameControlledRestartPlan(left, right ControlledRestartPlan) bool {
	return left.JobID == right.JobID &&
		left.SessionID == right.SessionID &&
		left.CommandID == right.CommandID &&
		left.RuntimeGeneration == right.RuntimeGeneration &&
		left.PreparedAt.Equal(right.PreparedAt) &&
		left.Activated == right.Activated &&
		sameOptionalTime(left.ActivatedAt, right.ActivatedAt) &&
		sameControlledRestartLostPairs(left.LostPairs, right.LostPairs)
}

func sameControlledRestartPlanIdentity(left, right ControlledRestartPlan) bool {
	return left.JobID == right.JobID &&
		left.SessionID == right.SessionID &&
		left.CommandID == right.CommandID &&
		left.RuntimeGeneration == right.RuntimeGeneration &&
		left.PreparedAt.Equal(right.PreparedAt) &&
		sameControlledRestartLostPairs(left.LostPairs, right.LostPairs)
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameControlledRestartLostPairs(left, right []LostRuntimeRecoveryPair) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneControlledRestartPlan(plan ControlledRestartPlan) ControlledRestartPlan {
	plan.LostPairs = append([]LostRuntimeRecoveryPair(nil), plan.LostPairs...)
	if plan.ActivatedAt != nil {
		value := plan.ActivatedAt.UTC()
		plan.ActivatedAt = &value
	}
	return plan
}
