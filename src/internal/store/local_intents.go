package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

// Local intent delivery states describe Mac-local persistence and delivery.
// They are never authoritative target execution states.
type LocalIntentDeliveryState string

const (
	LocalIntentRecorded     LocalIntentDeliveryState = "recorded"
	LocalIntentDispatching  LocalIntentDeliveryState = "dispatching"
	LocalIntentUncertain    LocalIntentDeliveryState = "uncertain"
	LocalIntentAccepted     LocalIntentDeliveryState = "accepted"
	LocalIntentReconciled   LocalIntentDeliveryState = "reconciled"
	LocalIntentNotDelivered LocalIntentDeliveryState = "not_delivered"
)

var (
	ErrInvalidLocalIntent        = errors.New("invalid local intent")
	ErrLocalIntentExists         = errors.New("local intent already exists")
	ErrLocalIntentNotFound       = errors.New("local intent not found")
	ErrLocalIntentPayloadCorrupt = errors.New("local intent payload is corrupt")
	ErrLocalIntentTransition     = errors.New("invalid local intent transition")
	ErrLocalIdempotencyCorrupt   = errors.New("local idempotency record is corrupt")
	ErrNoEligibleLocalIntent     = errors.New("no eligible local intent")
	ErrLocalIntentLeaseHeld      = errors.New("local intent lease is held")
	ErrLocalIntentLeaseLost      = errors.New("local intent lease is lost")
	ErrInvalidLocalIntentLease   = errors.New("invalid local intent lease")
)

// LocalIntentCreate is the complete immutable request recorded by Mac local
// ingress. The request payload and script bytes are copied into SQLite in the
// same transaction as the intent identity and its initial lifecycle row.
type LocalIntentCreate struct {
	IntentID             domain.IntentID
	Operation            string
	ResourceID           string
	SessionID            domain.SessionID
	CommandID            domain.CommandID
	JobID                domain.JobID
	Target               domain.ExecutionTarget
	Environment          string
	Controller           domain.ControllerIdentity
	Source               domain.Source
	RequestHash          domain.CanonicalHash
	IdempotencyKey       string
	IdempotencyRetention time.Duration
	PayloadJSON          []byte
	ScriptBytes          []byte
	IntentOrdinal        *int64
	DeliveryState        LocalIntentDeliveryState
	Reason               string
	LeaseOwner           string
	LeaseExpiresAt       *time.Time
	AttemptCount         int
}

// LocalIntentRecord is a durable local-intent snapshot. PayloadJSON and
// ScriptBytes are exact copies of the accepted request data.
type LocalIntentRecord struct {
	LocalIntentCreate
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LocalIntentLifecycleRecord is one immutable local intent lifecycle entry.
type LocalIntentLifecycleRecord struct {
	IntentID      domain.IntentID
	Sequence      int64
	PreviousState *LocalIntentDeliveryState
	NewState      LocalIntentDeliveryState
	Reason        string
	OccurredAt    time.Time
}

const (
	localIntentCreateSessionOperation = "create_session"
	localIntentSubmitCommandOperation = "submit_command"
	localIntentCancelCommandOperation = "cancel_command"
	localIntentCloseSessionOperation  = "close_session"
	localIntentRunOperation           = "run"
)

// CreateLocalIntent durably records one Mac-local request and its initial
// lifecycle event. It does not dispatch to a target or claim target
// acceptance. A retained same-key retry returns the original intent.
func (s *AuthorityStore) CreateLocalIntent(ctx context.Context, input LocalIntentCreate) (LocalIntentRecord, error) {
	record, _, err := s.AcceptLocalIntent(ctx, input)
	return record, err
}

// AcceptLocalIntent records or resumes a Mac-local request in one immediate
// transaction. duplicate is true only when an unexpired key/hash binding
// returned an already committed intent.
func (s *AuthorityStore) AcceptLocalIntent(ctx context.Context, input LocalIntentCreate) (record LocalIntentRecord, duplicate bool, err error) {
	validated, err := validateLocalIntentCreate(input)
	if err != nil {
		return LocalIntentRecord{}, false, err
	}
	now := s.now().UTC()
	result, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		existing, found, lookupErr := lookupLocalIdempotencyOnConnection(ctx, connection, validated.Controller, validated.Operation, validated.IdempotencyKey, now)
		if lookupErr != nil {
			return LocalIntentRecord{}, lookupErr
		}
		if found {
			if domain.CompareIdempotency(existing.Hash, validated.RequestHash) == domain.IdempotencyConflict {
				return LocalIntentRecord{}, ErrIdempotencyConflict
			}
			existingID, err := domain.NewIntentID(existing.IntentID)
			if err != nil {
				return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrLocalIdempotencyCorrupt, err)
			}
			record, err := readLocalIntentOnConnection(ctx, connection, existingID)
			if err != nil {
				return LocalIntentRecord{}, fmt.Errorf("read idempotent local intent: %w", err)
			}
			duplicate = true
			return record, nil
		}
		if validated.Operation == localIntentSubmitCommandOperation && validated.IntentOrdinal == nil {
			ordinal, err := nextLocalIntentOrdinalOnConnection(ctx, connection, validated.SessionID)
			if err != nil {
				return LocalIntentRecord{}, err
			}
			validated.IntentOrdinal = &ordinal
		}
		if err := insertLocalIntentOnConnection(ctx, connection, validated, now); err != nil {
			return LocalIntentRecord{}, err
		}
		initialState := LocalIntentDeliveryState(validated.DeliveryState)
		if validated.Operation == localIntentCreateSessionOperation {
			initialState = LocalIntentDeliveryState("requested")
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO local_intent_lifecycle (intent_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, 1, NULL, ?, ?, ?)
`, string(validated.IntentID), string(initialState), validated.Reason, formatStoredTime(now)); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("insert local intent lifecycle: %w", err)
		}
		if err := insertLocalIdempotencyOnConnection(ctx, connection, validated, now); err != nil {
			return LocalIntentRecord{}, err
		}
		return readLocalIntentOnConnection(ctx, connection, validated.IntentID)
	})
	if err != nil {
		return LocalIntentRecord{}, false, err
	}
	return result, duplicate, nil
}

// GetLocalIntent reloads an intent and validates all immutable bytes before
// returning it. A corrupt or truncated payload is never returned as usable.
func (s *AuthorityStore) GetLocalIntent(ctx context.Context, id domain.IntentID) (LocalIntentRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		return readLocalIntentOnConnection(ctx, connection, validatedID)
	})
}

// ValidateLocalIntentPayload verifies the persisted immutable payload without
// exposing it to a caller that only needs a durability check.
func (s *AuthorityStore) ValidateLocalIntentPayload(ctx context.Context, id domain.IntentID) error {
	_, err := s.GetLocalIntent(ctx, id)
	return err
}

// ListLocalIntentLifecycle returns the immutable lifecycle history in order.
func (s *AuthorityStore) ListLocalIntentLifecycle(ctx context.Context, id domain.IntentID) ([]LocalIntentLifecycleRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return nil, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]LocalIntentLifecycleRecord, error) {
		if _, err := readLocalIntentOnConnection(ctx, connection, validatedID); err != nil {
			return nil, err
		}
		rows, err := connection.QueryContext(ctx, `
SELECT lifecycle_sequence, previous_state, new_state, reason, occurred_at
FROM local_intent_lifecycle WHERE intent_id = ? ORDER BY lifecycle_sequence
`, string(validatedID))
		if err != nil {
			return nil, fmt.Errorf("read local intent lifecycle: %w", err)
		}
		defer rows.Close()
		result := make([]LocalIntentLifecycleRecord, 0)
		wantSequence := int64(1)
		for rows.Next() {
			var sequence int64
			var previous sql.NullString
			var next, reason, occurred string
			if err := rows.Scan(&sequence, &previous, &next, &reason, &occurred); err != nil {
				return nil, fmt.Errorf("scan local intent lifecycle: %w", err)
			}
			if sequence != wantSequence || !validLocalIntentLifecycleState(next) {
				return nil, fmt.Errorf("%w: lifecycle sequence/state", ErrLocalIntentPayloadCorrupt)
			}
			occurredAt, err := parseStoredTime(occurred)
			if err != nil {
				return nil, fmt.Errorf("%w: lifecycle timestamp: %v", ErrLocalIntentPayloadCorrupt, err)
			}
			var previousState *LocalIntentDeliveryState
			if previous.Valid {
				state := LocalIntentDeliveryState(previous.String)
				if !validLocalIntentLifecycleState(previous.String) {
					return nil, fmt.Errorf("%w: lifecycle previous state", ErrLocalIntentPayloadCorrupt)
				}
				previousState = &state
			}
			result = append(result, LocalIntentLifecycleRecord{IntentID: validatedID, Sequence: sequence, PreviousState: previousState, NewState: LocalIntentDeliveryState(next), Reason: reason, OccurredAt: occurredAt})
			wantSequence++
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate local intent lifecycle: %w", err)
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("%w: lifecycle missing", ErrLocalIntentPayloadCorrupt)
		}
		return result, nil
	})
}

// TransitionLocalIntent records a delivery-state change without changing any
// immutable request fields. The operation is transactional and idempotent for
// a repeated state.
func (s *AuthorityStore) TransitionLocalIntent(ctx context.Context, id domain.IntentID, next LocalIntentDeliveryState, reason string) (LocalIntentRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	if !validLocalIntentDeliveryState(string(next)) {
		return LocalIntentRecord{}, fmt.Errorf("%w: unknown next state %q", ErrLocalIntentTransition, next)
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		current, err := readLocalIntentOnConnection(ctx, connection, validatedID)
		if err != nil {
			return LocalIntentRecord{}, err
		}
		if current.DeliveryState == next {
			return current, nil
		}
		if !localIntentStateTransitionAllowed(current.DeliveryState, next) {
			return LocalIntentRecord{}, fmt.Errorf("%w: %s -> %s", ErrLocalIntentTransition, current.DeliveryState, next)
		}
		now := s.now().UTC()
		if _, err := connection.ExecContext(ctx, `
UPDATE local_intents SET delivery_state = ?, reason = ?, updated_at = ? WHERE intent_id = ?
`, string(next), reason, formatStoredTime(now), string(validatedID)); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("update local intent delivery state: %w", err)
		}
		var sequence int64
		if err := connection.QueryRowContext(ctx, `SELECT COALESCE(MAX(lifecycle_sequence), 0) + 1 FROM local_intent_lifecycle WHERE intent_id = ?`, string(validatedID)).Scan(&sequence); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("allocate local intent lifecycle sequence: %w", err)
		}
		var previous string
		if err := connection.QueryRowContext(ctx, `SELECT new_state FROM local_intent_lifecycle WHERE intent_id = ? ORDER BY lifecycle_sequence DESC LIMIT 1`, string(validatedID)).Scan(&previous); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("read local intent lifecycle predecessor: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO local_intent_lifecycle (intent_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)
`, string(validatedID), sequence, previous, string(next), reason, formatStoredTime(now)); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("insert local intent transition: %w", err)
		}
		return readLocalIntentOnConnection(ctx, connection, validatedID)
	})
}

// ClaimLocalIntent claims one eligible recorded intent for owner and assigns
// a bounded lease. The claim, state transition, attempt increment, and
// lifecycle row commit together.
func (s *AuthorityStore) ClaimLocalIntent(ctx context.Context, id domain.IntentID, owner string, leaseDuration time.Duration) (LocalIntentRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	if err := validateLeaseRequest(owner, leaseDuration); err != nil {
		return LocalIntentRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		return claimLocalIntentOnConnection(ctx, connection, validatedID, owner, leaseDuration, now)
	})
}

// ClaimNextLocalIntent claims the earliest eligible recorded intent. Earlier
// unsettled command intents in a session keep later ordinals blocked.
func (s *AuthorityStore) ClaimNextLocalIntent(ctx context.Context, owner string, leaseDuration time.Duration) (LocalIntentRecord, error) {
	if err := validateLeaseRequest(owner, leaseDuration); err != nil {
		return LocalIntentRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		id, err := findNextEligibleLocalIntentOnConnection(ctx, connection, now)
		if err != nil {
			return LocalIntentRecord{}, err
		}
		return claimLocalIntentOnConnection(ctx, connection, id, owner, leaseDuration, now)
	})
}

// RenewLocalIntentLease extends an unexpired lease owned by owner. Lease
// renewal does not create a lifecycle state entry because the delivery state
// and immutable request remain unchanged.
func (s *AuthorityStore) RenewLocalIntentLease(ctx context.Context, id domain.IntentID, owner string, leaseDuration time.Duration) (LocalIntentRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	if err := validateLeaseRequest(owner, leaseDuration); err != nil {
		return LocalIntentRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		current, err := readLocalIntentOnConnection(ctx, connection, validatedID)
		if err != nil {
			return LocalIntentRecord{}, err
		}
		if current.DeliveryState != LocalIntentDispatching && current.DeliveryState != LocalIntentUncertain {
			return LocalIntentRecord{}, ErrLocalIntentLeaseLost
		}
		if current.LeaseOwner != owner || current.LeaseExpiresAt == nil || !now.Before(*current.LeaseExpiresAt) {
			return LocalIntentRecord{}, ErrLocalIntentLeaseLost
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE local_intents SET lease_expires_at = ?, updated_at = ? WHERE intent_id = ? AND lease_owner = ?
`, formatStoredTime(now.Add(leaseDuration)), formatStoredTime(now), string(validatedID), owner); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("renew local intent lease: %w", err)
		}
		return readLocalIntentOnConnection(ctx, connection, validatedID)
	})
}

// ListEligibleLocalIntents returns recorded intents that can be claimed now,
// ordered by session intent ordinal and then creation identity.
func (s *AuthorityStore) ListEligibleLocalIntents(ctx context.Context, limit int) ([]LocalIntentRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, fmt.Errorf("%w: eligibility limit exceeds 1000", ErrInvalidLocalIntent)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]LocalIntentRecord, error) {
		rows, err := connection.QueryContext(ctx, eligibleLocalIntentQuery+" LIMIT ?", formatStoredTime(now), limit)
		if err != nil {
			return nil, fmt.Errorf("list eligible local intents: %w", err)
		}
		defer rows.Close()
		return readLocalIntentRows(ctx, connection, rows)
	})
}

// ListRecoverableLocalIntents returns intents whose dispatch/reconciliation
// lease has expired. It reports candidates for recovery and never silently
// changes an uncertain dispatch back to recorded.
func (s *AuthorityStore) ListRecoverableLocalIntents(ctx context.Context, limit int) ([]LocalIntentRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, fmt.Errorf("%w: recovery limit exceeds 1000", ErrInvalidLocalIntent)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]LocalIntentRecord, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT intent_id FROM local_intents
WHERE delivery_state IN ('dispatching', 'uncertain')
  AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
ORDER BY lease_expires_at, created_at, intent_id
LIMIT ?
`, formatStoredTime(now), limit)
		if err != nil {
			return nil, fmt.Errorf("list recoverable local intents: %w", err)
		}
		defer rows.Close()
		return readLocalIntentRows(ctx, connection, rows)
	})
}

const eligibleLocalIntentWhere = `
WHERE li.delivery_state = 'recorded'
  AND (li.lease_expires_at IS NULL OR li.lease_expires_at <= ?)
  AND (
      li.operation <> 'submit_command'
      OR li.intent_ordinal IS NULL
      OR NOT EXISTS (
          SELECT 1 FROM local_intents AS prior
          WHERE prior.session_id = li.session_id
            AND prior.operation = 'submit_command'
            AND prior.intent_ordinal IS NOT NULL
            AND prior.intent_ordinal < li.intent_ordinal
            AND prior.delivery_state NOT IN ('reconciled', 'not_delivered')
      )
  )`

const eligibleLocalIntentQuery = `SELECT li.intent_id FROM local_intents AS li` + eligibleLocalIntentWhere + `
ORDER BY (li.intent_ordinal IS NULL), li.intent_ordinal, li.created_at, li.intent_id`

func findNextEligibleLocalIntentOnConnection(ctx context.Context, connection *sql.Conn, now time.Time) (domain.IntentID, error) {
	var value string
	if err := connection.QueryRowContext(ctx, eligibleLocalIntentQuery+" LIMIT 1", formatStoredTime(now)).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoEligibleLocalIntent
		}
		return "", fmt.Errorf("find eligible local intent: %w", err)
	}
	id, err := domain.NewIntentID(value)
	if err != nil {
		return "", fmt.Errorf("%w: eligible intent ID: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	return id, nil
}

func claimLocalIntentOnConnection(ctx context.Context, connection *sql.Conn, id domain.IntentID, owner string, leaseDuration time.Duration, now time.Time) (LocalIntentRecord, error) {
	current, err := readLocalIntentOnConnection(ctx, connection, id)
	if err != nil {
		return LocalIntentRecord{}, err
	}
	if current.DeliveryState != LocalIntentRecorded {
		return LocalIntentRecord{}, ErrNoEligibleLocalIntent
	}
	if current.LeaseExpiresAt != nil && now.Before(*current.LeaseExpiresAt) {
		return LocalIntentRecord{}, ErrLocalIntentLeaseHeld
	}
	if !localIntentIsEligibleOnConnection(ctx, connection, id, now) {
		return LocalIntentRecord{}, ErrNoEligibleLocalIntent
	}
	if _, err := connection.ExecContext(ctx, `
UPDATE local_intents
SET delivery_state = 'dispatching', lease_owner = ?, lease_expires_at = ?, attempt_count = attempt_count + 1,
    reason = 'lease_claimed', updated_at = ?
WHERE intent_id = ? AND delivery_state = 'recorded'
`, owner, formatStoredTime(now.Add(leaseDuration)), formatStoredTime(now), string(id)); err != nil {
		return LocalIntentRecord{}, fmt.Errorf("claim local intent: %w", err)
	}
	if err := appendLocalIntentLifecycleOnConnection(ctx, connection, id, LocalIntentDispatching, "lease_claimed", now); err != nil {
		return LocalIntentRecord{}, err
	}
	return readLocalIntentOnConnection(ctx, connection, id)
}

func localIntentIsEligibleOnConnection(ctx context.Context, connection *sql.Conn, id domain.IntentID, now time.Time) bool {
	var value string
	err := connection.QueryRowContext(ctx, `SELECT li.intent_id FROM local_intents AS li`+eligibleLocalIntentWhere+` AND li.intent_id = ? LIMIT 1`, formatStoredTime(now), string(id)).Scan(&value)
	return err == nil && value == string(id)
}

func appendLocalIntentLifecycleOnConnection(ctx context.Context, connection *sql.Conn, id domain.IntentID, next LocalIntentDeliveryState, reason string, now time.Time) error {
	var sequence int64
	if err := connection.QueryRowContext(ctx, `SELECT COALESCE(MAX(lifecycle_sequence), 0) + 1 FROM local_intent_lifecycle WHERE intent_id = ?`, string(id)).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate local intent lifecycle sequence: %w", err)
	}
	var previous string
	if err := connection.QueryRowContext(ctx, `SELECT new_state FROM local_intent_lifecycle WHERE intent_id = ? ORDER BY lifecycle_sequence DESC LIMIT 1`, string(id)).Scan(&previous); err != nil {
		return fmt.Errorf("read local intent lifecycle predecessor: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO local_intent_lifecycle (intent_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)
`, string(id), sequence, previous, string(next), reason, formatStoredTime(now)); err != nil {
		return fmt.Errorf("insert local intent lifecycle: %w", err)
	}
	return nil
}

func readLocalIntentRows(ctx context.Context, connection *sql.Conn, rows *sql.Rows) ([]LocalIntentRecord, error) {
	ids := make([]domain.IntentID, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan local intent candidate: %w", err)
		}
		id, err := domain.NewIntentID(value)
		if err != nil {
			return nil, fmt.Errorf("%w: candidate ID: %v", ErrLocalIntentPayloadCorrupt, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local intent candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close local intent candidates: %w", err)
	}
	result := make([]LocalIntentRecord, 0, len(ids))
	for _, id := range ids {
		record, err := readLocalIntentOnConnection(ctx, connection, id)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func nextLocalIntentOrdinalOnConnection(ctx context.Context, connection *sql.Conn, sessionID domain.SessionID) (int64, error) {
	var maximum sql.NullInt64
	if err := connection.QueryRowContext(ctx, `SELECT MAX(intent_ordinal) FROM local_intents WHERE operation = 'submit_command' AND session_id = ?`, string(sessionID)).Scan(&maximum); err != nil {
		return 0, fmt.Errorf("read local intent ordinal: %w", err)
	}
	if !maximum.Valid {
		return 1, nil
	}
	if maximum.Int64 == int64(^uint64(0)>>1) {
		return 0, fmt.Errorf("%w: intent ordinal exhausted", ErrInvalidLocalIntent)
	}
	return maximum.Int64 + 1, nil
}

func validateLeaseRequest(owner string, leaseDuration time.Duration) error {
	if owner == "" || len(owner) > 256 || strings.IndexByte(owner, 0) >= 0 {
		return fmt.Errorf("%w: owner must be 1..256 bytes and contain no NUL", ErrInvalidLocalIntentLease)
	}
	if leaseDuration <= 0 {
		return fmt.Errorf("%w: duration must be positive", ErrInvalidLocalIntentLease)
	}
	return nil
}

func validateLocalIntentCreate(input LocalIntentCreate) (LocalIntentCreate, error) {
	intentID, err := domain.NewIntentID(string(input.IntentID))
	if err != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	if !validLocalIntentOperation(input.Operation) {
		return LocalIntentCreate{}, fmt.Errorf("%w: operation %q", ErrInvalidLocalIntent, input.Operation)
	}
	if input.ResourceID == "" || len(input.ResourceID) > 256 || strings.IndexByte(input.ResourceID, 0) >= 0 {
		return LocalIntentCreate{}, fmt.Errorf("%w: resource ID", ErrInvalidLocalIntent)
	}
	validated := input
	validated.IntentID = intentID
	validated.Environment = strings.TrimSpace(input.Environment)
	if validated.Environment == "" {
		return LocalIntentCreate{}, fmt.Errorf("%w: environment is empty", ErrInvalidLocalIntent)
	}
	validated.Target, err = domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: target: %v", ErrInvalidLocalIntent, err)
	}
	validated.Controller, err = validateController(input.Controller)
	if err != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: controller: %v", ErrInvalidLocalIntent, err)
	}
	validated.Source, err = normalizeSource(input.Source)
	if err != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: source: %v", ErrInvalidLocalIntent, err)
	}
	if _, _, err := validateIdempotencyOperationKey(input.Operation, input.IdempotencyKey); err != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: idempotency: %v", ErrInvalidLocalIntent, err)
	}
	retention := input.IdempotencyRetention
	if retention == 0 {
		retention = DefaultSessionIdempotencyRetention
	}
	if retention < 0 {
		return LocalIntentCreate{}, fmt.Errorf("%w: idempotency retention must not be negative", ErrInvalidLocalIntent)
	}
	validated.IdempotencyRetention = retention
	if input.AttemptCount < 0 {
		return LocalIntentCreate{}, fmt.Errorf("%w: negative attempt count", ErrInvalidLocalIntent)
	}
	if input.IntentOrdinal != nil && *input.IntentOrdinal <= 0 {
		return LocalIntentCreate{}, fmt.Errorf("%w: intent ordinal must be positive", ErrInvalidLocalIntent)
	}
	if input.Operation != localIntentSubmitCommandOperation && input.IntentOrdinal != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: intent ordinal only applies to submit_command", ErrInvalidLocalIntent)
	}
	if err := validateLocalIntentIdentityFields(validated); err != nil {
		return LocalIntentCreate{}, err
	}
	if err := domain.ValidateScriptUTF8(string(input.ScriptBytes)); err != nil {
		return LocalIntentCreate{}, fmt.Errorf("%w: script: %v", ErrInvalidLocalIntent, err)
	}
	canonical, hash, err := validateLocalIntentPayload(input.Operation, input.PayloadJSON, input.ScriptBytes, validated.Environment, validated.Target, validated.Source, input.RequestHash)
	if err != nil {
		return LocalIntentCreate{}, err
	}
	validated.PayloadJSON = append([]byte{}, canonical...)
	validated.RequestHash = hash
	validated.ScriptBytes = make([]byte, len(input.ScriptBytes))
	copy(validated.ScriptBytes, input.ScriptBytes)
	validated.DeliveryState = input.DeliveryState
	if validated.DeliveryState == "" {
		validated.DeliveryState = LocalIntentRecorded
	}
	if !validLocalIntentDeliveryState(string(validated.DeliveryState)) {
		return LocalIntentCreate{}, fmt.Errorf("%w: delivery state %q", ErrInvalidLocalIntent, validated.DeliveryState)
	}
	validated.Reason = input.Reason
	validated.LeaseOwner = input.LeaseOwner
	if strings.IndexByte(validated.LeaseOwner, 0) >= 0 || len(validated.LeaseOwner) > 256 {
		return LocalIntentCreate{}, fmt.Errorf("%w: lease owner", ErrInvalidLocalIntent)
	}
	if input.LeaseExpiresAt != nil {
		value := input.LeaseExpiresAt.UTC()
		validated.LeaseExpiresAt = &value
	}
	return validated, nil
}

func validateLocalIntentIdentityFields(input LocalIntentCreate) error {
	check := func(name, value string, required bool) error {
		if value == "" && !required {
			return nil
		}
		if value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidLocalIntent, name)
		}
		return nil
	}
	sessionRequired := input.Operation == localIntentCreateSessionOperation || input.Operation == localIntentSubmitCommandOperation || input.Operation == localIntentCloseSessionOperation
	if err := check("session ID", string(input.SessionID), sessionRequired); err != nil {
		return err
	}
	if input.SessionID != "" {
		if _, err := domain.NewSessionID(string(input.SessionID)); err != nil {
			return fmt.Errorf("%w: session ID: %v", ErrInvalidLocalIntent, err)
		}
	}
	commandRequired := input.Operation == localIntentSubmitCommandOperation || input.Operation == localIntentCancelCommandOperation
	if err := check("command ID", string(input.CommandID), commandRequired); err != nil {
		return err
	}
	if input.CommandID != "" {
		if _, err := domain.NewCommandID(string(input.CommandID)); err != nil {
			return fmt.Errorf("%w: command ID: %v", ErrInvalidLocalIntent, err)
		}
	}
	jobRequired := input.Operation == localIntentRunOperation
	if err := check("job ID", string(input.JobID), jobRequired); err != nil {
		return err
	}
	if input.JobID != "" {
		if _, err := domain.NewJobID(string(input.JobID)); err != nil {
			return fmt.Errorf("%w: job ID: %v", ErrInvalidLocalIntent, err)
		}
	}
	if input.Operation == localIntentCreateSessionOperation && input.ResourceID != string(input.SessionID) {
		return fmt.Errorf("%w: create_session resource must equal session ID", ErrInvalidLocalIntent)
	}
	if input.Operation == localIntentSubmitCommandOperation && input.ResourceID != string(input.CommandID) {
		return fmt.Errorf("%w: submit_command resource must equal command ID", ErrInvalidLocalIntent)
	}
	if input.Operation == localIntentCancelCommandOperation && input.ResourceID != string(input.CommandID) {
		return fmt.Errorf("%w: cancel_command resource must equal command ID", ErrInvalidLocalIntent)
	}
	if input.Operation == localIntentCloseSessionOperation && input.ResourceID != string(input.SessionID) {
		return fmt.Errorf("%w: close_session resource must equal session ID", ErrInvalidLocalIntent)
	}
	if input.Operation == localIntentRunOperation && input.ResourceID != string(input.JobID) {
		return fmt.Errorf("%w: run resource must equal job ID", ErrInvalidLocalIntent)
	}
	return nil
}

func validateLocalIntentPayload(operation string, payload []byte, script []byte, environment string, target domain.ExecutionTarget, source domain.Source, requestHash domain.CanonicalHash) ([]byte, domain.CanonicalHash, error) {
	if len(payload) == 0 {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: payload is empty", ErrInvalidLocalIntent)
	}
	if err := domain.ValidateSerializedRequest(payload); err != nil {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: payload: %v", ErrInvalidLocalIntent, err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON(operation, payload, domain.CanonicalizationOptions{})
	if err != nil {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: payload canonicalization: %v", ErrInvalidLocalIntent, err)
	}
	if !bytes.Equal(canonical, payload) {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: payload is not canonical", ErrInvalidLocalIntent)
	}
	hash, err := domain.HashMutationRequestJSON(operation, payload, domain.CanonicalizationOptions{})
	if err != nil || domain.CompareIdempotency(hash, requestHash) == domain.IdempotencyConflict {
		if err == nil {
			err = errors.New("request hash does not match payload")
		}
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: %v", ErrInvalidLocalIntent, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: payload object: %v", ErrInvalidLocalIntent, err)
	}
	if value, ok := object["script"]; ok {
		var payloadScript string
		if err := json.Unmarshal(value, &payloadScript); err != nil || !bytes.Equal([]byte(payloadScript), script) {
			return nil, domain.CanonicalHash{}, fmt.Errorf("%w: script differs from payload", ErrInvalidLocalIntent)
		}
	} else if len(script) != 0 {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: script missing from payload", ErrInvalidLocalIntent)
	}
	if value, ok := object["environment"]; ok {
		var payloadEnvironment string
		if err := json.Unmarshal(value, &payloadEnvironment); err != nil || payloadEnvironment != environment {
			return nil, domain.CanonicalHash{}, fmt.Errorf("%w: environment differs from payload", ErrInvalidLocalIntent)
		}
	}
	if value, ok := object["execution_target"]; ok {
		var payloadTarget struct{ Kind, Profile string }
		if err := json.Unmarshal(value, &payloadTarget); err != nil || payloadTarget.Kind != string(target.Kind()) || payloadTarget.Profile != target.Profile() {
			return nil, domain.CanonicalHash{}, fmt.Errorf("%w: target differs from payload", ErrInvalidLocalIntent)
		}
	}
	if value, ok := object["source"]; ok {
		var payloadSource struct {
			Mode              string `json:"mode"`
			RepositoryAlias   string `json:"repository_alias"`
			RequestedRevision string `json:"requested_revision"`
			Path              string `json:"path"`
		}
		if err := json.Unmarshal(value, &payloadSource); err != nil || payloadSource.Mode != string(source.Mode()) || payloadSource.RepositoryAlias != source.RepositoryAlias() || payloadSource.RequestedRevision != source.RequestedRevision() || payloadSource.Path != source.Path() {
			return nil, domain.CanonicalHash{}, fmt.Errorf("%w: source differs from payload", ErrInvalidLocalIntent)
		}
	}
	return append([]byte(nil), payload...), hash, nil
}

func insertLocalIntentOnConnection(ctx context.Context, connection *sql.Conn, input LocalIntentCreate, now time.Time) error {
	scriptHash := sha256.Sum256(input.ScriptBytes)
	var ordinal any
	if input.IntentOrdinal != nil {
		ordinal = *input.IntentOrdinal
	}
	var leaseExpires any
	if input.LeaseExpiresAt != nil {
		leaseExpires = formatStoredTime(*input.LeaseExpiresAt)
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO local_intents (
 intent_id, operation, resource_id, session_id, command_id, job_id,
 target_kind, target_profile, environment, controller_type, controller_id,
 source_mode, source_repository_alias, source_requested_revision, source_path,
 request_hash_version, request_hash, idempotency_key, payload_json,
 script_bytes, script_sha256, intent_ordinal, delivery_state, reason,
 lease_owner, lease_expires_at, attempt_count, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(input.IntentID), input.Operation, input.ResourceID, string(input.SessionID), string(input.CommandID), string(input.JobID),
		string(input.Target.Kind()), input.Target.Profile(), input.Environment, string(input.Controller.Type()), string(input.Controller.ID()),
		string(input.Source.Mode()), input.Source.RepositoryAlias(), input.Source.RequestedRevision(), input.Source.Path(),
		input.RequestHash.Version(), input.RequestHash.SHA256(), input.IdempotencyKey, input.PayloadJSON,
		input.ScriptBytes, scriptHash[:], ordinal, string(input.DeliveryState), input.Reason,
		input.LeaseOwner, leaseExpires, input.AttemptCount, formatStoredTime(now), formatStoredTime(now)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrLocalIntentExists
		}
		return fmt.Errorf("insert local intent: %w", err)
	}
	return nil
}

type localIdempotencyBinding struct {
	Controller domain.ControllerIdentity
	Operation  string
	Key        string
	Hash       domain.CanonicalHash
	IntentID   string
	ResourceID string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

func insertLocalIdempotencyOnConnection(ctx context.Context, connection *sql.Conn, input LocalIntentCreate, now time.Time) error {
	if input.IdempotencyRetention <= 0 {
		return fmt.Errorf("%w: idempotency retention must be positive", ErrInvalidLocalIntent)
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO local_idempotency (
 controller_type, controller_id, operation, idempotency_key,
 request_hash_version, request_hash, intent_id, resource_id, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(input.Controller.Type()), string(input.Controller.ID()), input.Operation, input.IdempotencyKey,
		input.RequestHash.Version(), input.RequestHash.SHA256(), string(input.IntentID), input.ResourceID,
		formatStoredTime(now), formatStoredTime(now.Add(input.IdempotencyRetention))); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrIdempotencyConflict
		}
		return fmt.Errorf("insert local idempotency: %w", err)
	}
	return nil
}

func lookupLocalIdempotencyOnConnection(ctx context.Context, connection *sql.Conn, controller domain.ControllerIdentity, operation, key string, now time.Time) (localIdempotencyBinding, bool, error) {
	var binding localIdempotencyBinding
	var hashVersion int
	var controllerTypeText, controllerID, operationText, keyText, intentID, resourceID, createdAt, expiresAt string
	var digestBytes []byte
	if err := connection.QueryRowContext(ctx, `
SELECT controller_type, controller_id, operation, idempotency_key,
       request_hash_version, request_hash, intent_id, resource_id, created_at, expires_at
FROM local_idempotency
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
`, string(controller.Type()), string(controller.ID()), operation, key).Scan(&controllerTypeText, &controllerID, &operationText, &keyText, &hashVersion, &digestBytes, &intentID, &resourceID, &createdAt, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return localIdempotencyBinding{}, false, nil
		}
		return localIdempotencyBinding{}, false, fmt.Errorf("lookup local idempotency: %w", err)
	}
	binding.Controller, _ = domain.NewControllerIdentity(domain.ControllerType(controllerTypeText), domain.ControllerID(controllerID))
	binding.Operation, binding.Key, binding.IntentID, binding.ResourceID = operationText, keyText, intentID, resourceID
	requestHash, err := domain.NewCanonicalHash(uint16(hashVersion), digestBytes)
	if err != nil {
		return localIdempotencyBinding{}, false, fmt.Errorf("%w: request hash: %v", ErrLocalIdempotencyCorrupt, err)
	}
	binding.Hash = requestHash
	binding.CreatedAt, err = parseStoredTime(createdAt)
	if err != nil {
		return localIdempotencyBinding{}, false, fmt.Errorf("%w: created_at: %v", ErrLocalIdempotencyCorrupt, err)
	}
	binding.ExpiresAt, err = parseStoredTime(expiresAt)
	if err != nil {
		return localIdempotencyBinding{}, false, fmt.Errorf("%w: expires_at: %v", ErrLocalIdempotencyCorrupt, err)
	}
	if binding.IntentID == "" || binding.ResourceID == "" || !validLocalIntentOperation(binding.Operation) || binding.Controller.Type() == "" || binding.Controller.ID() == "" {
		return localIdempotencyBinding{}, false, fmt.Errorf("%w: identity fields", ErrLocalIdempotencyCorrupt)
	}
	if !now.Before(binding.ExpiresAt) {
		if _, err := connection.ExecContext(ctx, `
DELETE FROM local_idempotency
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
`, string(controller.Type()), string(controller.ID()), operation, key); err != nil {
			return localIdempotencyBinding{}, false, fmt.Errorf("expire local idempotency: %w", err)
		}
		return localIdempotencyBinding{}, false, nil
	}
	return binding, true, nil
}

func readLocalIntentOnConnection(ctx context.Context, connection *sql.Conn, id domain.IntentID) (LocalIntentRecord, error) {
	var record LocalIntentRecord
	var intentID, operation, resourceID, sessionID, commandID, jobID string
	var targetKind, targetProfile, environment, controllerType, controllerID string
	var sourceMode, repositoryAlias, requestedRevision, sourcePath string
	var requestHashVersion int
	var requestHash, payload, scriptBytes, scriptHash []byte
	var idempotencyKey, deliveryState, reason, leaseOwner string
	var leaseExpires sql.NullString
	var ordinal sql.NullInt64
	var attemptCount int
	var createdAt, updatedAt string
	err := connection.QueryRowContext(ctx, `
SELECT intent_id, operation, resource_id, session_id, command_id, job_id,
 target_kind, target_profile, environment, controller_type, controller_id,
 source_mode, source_repository_alias, source_requested_revision, source_path,
 request_hash_version, request_hash, idempotency_key, payload_json,
 script_bytes, script_sha256, intent_ordinal, delivery_state, reason,
 lease_owner, lease_expires_at, attempt_count, created_at, updated_at
FROM local_intents WHERE intent_id = ?
`, string(id)).Scan(&intentID, &operation, &resourceID, &sessionID, &commandID, &jobID,
		&targetKind, &targetProfile, &environment, &controllerType, &controllerID,
		&sourceMode, &repositoryAlias, &requestedRevision, &sourcePath,
		&requestHashVersion, &requestHash, &idempotencyKey, &payload,
		&scriptBytes, &scriptHash, &ordinal, &deliveryState, &reason,
		&leaseOwner, &leaseExpires, &attemptCount, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalIntentRecord{}, ErrLocalIntentNotFound
	}
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("read local intent: %w", err)
	}
	validatedID, err := domain.NewIntentID(intentID)
	if err != nil || validatedID != id {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID", ErrLocalIntentPayloadCorrupt)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: target: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerType(controllerType), domain.ControllerID(controllerID))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: controller: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	source, err := decodeSource(domain.SourceMode(sourceMode), repositoryAlias, requestedRevision, sourcePath)
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: source: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	hash, err := domain.NewCanonicalHash(uint16(requestHashVersion), requestHash)
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: request hash: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	if !validLocalIntentOperation(operation) || !validLocalIntentDeliveryState(deliveryState) || attemptCount < 0 {
		return LocalIntentRecord{}, fmt.Errorf("%w: state metadata", ErrLocalIntentPayloadCorrupt)
	}
	var intentOrdinal *int64
	if ordinal.Valid {
		value := ordinal.Int64
		if value <= 0 {
			return LocalIntentRecord{}, fmt.Errorf("%w: intent ordinal", ErrLocalIntentPayloadCorrupt)
		}
		intentOrdinal = &value
	}
	created, err := parseStoredTime(createdAt)
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: created_at: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	updated, err := parseStoredTime(updatedAt)
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: updated_at: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	var leaseTime *time.Time
	if leaseExpires.Valid {
		parsed, err := parseStoredTime(leaseExpires.String)
		if err != nil {
			return LocalIntentRecord{}, fmt.Errorf("%w: lease expiry: %v", ErrLocalIntentPayloadCorrupt, err)
		}
		leaseTime = &parsed
	}
	canonical, computedHash, err := validateLocalIntentPayload(operation, payload, scriptBytes, environment, target, source, hash)
	if err != nil || !bytes.Equal(canonical, payload) || domain.CompareIdempotency(computedHash, hash) == domain.IdempotencyConflict {
		if err == nil {
			err = errors.New("immutable payload hash mismatch")
		}
		return LocalIntentRecord{}, fmt.Errorf("%w: %v", ErrLocalIntentPayloadCorrupt, err)
	}
	computedScriptHash := sha256.Sum256(scriptBytes)
	if !bytes.Equal(computedScriptHash[:], scriptHash) {
		return LocalIntentRecord{}, fmt.Errorf("%w: script hash mismatch", ErrLocalIntentPayloadCorrupt)
	}
	record = LocalIntentRecord{LocalIntentCreate: LocalIntentCreate{IntentID: validatedID, Operation: operation, ResourceID: resourceID, Target: target, Environment: environment, Controller: controller, Source: source, RequestHash: hash, IdempotencyKey: idempotencyKey, PayloadJSON: append([]byte(nil), payload...), ScriptBytes: append([]byte(nil), scriptBytes...), IntentOrdinal: intentOrdinal, DeliveryState: LocalIntentDeliveryState(deliveryState), Reason: reason, LeaseOwner: leaseOwner, LeaseExpiresAt: leaseTime, AttemptCount: attemptCount}, CreatedAt: created, UpdatedAt: updated}
	if sessionID != "" {
		record.SessionID, err = domain.NewSessionID(sessionID)
		if err != nil {
			return LocalIntentRecord{}, fmt.Errorf("%w: session ID", ErrLocalIntentPayloadCorrupt)
		}
	}
	if commandID != "" {
		record.CommandID, err = domain.NewCommandID(commandID)
		if err != nil {
			return LocalIntentRecord{}, fmt.Errorf("%w: command ID", ErrLocalIntentPayloadCorrupt)
		}
	}
	if jobID != "" {
		record.JobID, err = domain.NewJobID(jobID)
		if err != nil {
			return LocalIntentRecord{}, fmt.Errorf("%w: job ID", ErrLocalIntentPayloadCorrupt)
		}
	}
	return record, nil
}

func validLocalIntentOperation(operation string) bool {
	switch operation {
	case localIntentCreateSessionOperation, localIntentSubmitCommandOperation, localIntentCancelCommandOperation, localIntentCloseSessionOperation, localIntentRunOperation:
		return true
	default:
		return false
	}
}

func validLocalIntentDeliveryState(state string) bool {
	switch LocalIntentDeliveryState(state) {
	case LocalIntentRecorded, LocalIntentDispatching, LocalIntentUncertain, LocalIntentAccepted, LocalIntentReconciled, LocalIntentNotDelivered:
		return true
	default:
		return false
	}
}

func validLocalIntentLifecycleState(state string) bool {
	return state == "requested" || validLocalIntentDeliveryState(state)
}

func localIntentStateTransitionAllowed(current, next LocalIntentDeliveryState) bool {
	switch current {
	case LocalIntentRecorded:
		return next == LocalIntentDispatching || next == LocalIntentNotDelivered
	case LocalIntentDispatching:
		return next == LocalIntentUncertain || next == LocalIntentAccepted || next == LocalIntentNotDelivered
	case LocalIntentUncertain:
		return next == LocalIntentDispatching || next == LocalIntentAccepted || next == LocalIntentReconciled || next == LocalIntentNotDelivered
	case LocalIntentAccepted:
		return next == LocalIntentReconciled
	case LocalIntentReconciled, LocalIntentNotDelivered:
		return false
	default:
		return false
	}
}
