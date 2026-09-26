package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	// ErrNilDatabase means a store was constructed without an opened database.
	ErrNilDatabase = errors.New("authority store database is nil")
	// ErrSessionNotFound means the requested session does not exist in this authority.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionExists means a new session reused an existing session ID.
	ErrSessionExists = errors.New("session already exists")
	// ErrInvalidSession means a session record failed the store boundary checks.
	ErrInvalidSession = errors.New("invalid session record")
	// ErrSessionLifecycle means the persisted lifecycle history is inconsistent.
	ErrSessionLifecycle = errors.New("session lifecycle history is inconsistent")
	// ErrIdempotencyKey means the create idempotency key is malformed.
	ErrIdempotencyKey = errors.New("invalid idempotency key")
	// ErrIdempotencyConflict means a retained key was reused for another request.
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	// ErrSessionCapacityExceeded means no host session reservation is available.
	ErrSessionCapacityExceeded = errors.New("session capacity exceeded")
	// ErrSessionReservationNotFound means no reservation exists for a session.
	ErrSessionReservationNotFound = errors.New("session capacity reservation not found")
)

const (
	// DefaultActiveSessionLimit is the selected PoC reservation ceiling per
	// authority host.
	DefaultActiveSessionLimit = 20
	// DefaultSessionIdempotencyRetention is the minimum retained create-key
	// window selected by the PoC design.
	DefaultSessionIdempotencyRetention = 90 * 24 * time.Hour
	createSessionOperation             = "create_session"
	reservationHostKey                 = "authority"
)

// SessionCreate contains the immutable and effective values recorded when an
// authority accepts a new session. The P013 acceptance boundary adds the
// idempotency record and capacity reservation before any runtime action.
type SessionCreate struct {
	SessionID         domain.SessionID
	Target            domain.ExecutionTarget
	Environment       string
	Controller        domain.ControllerIdentity
	Source            domain.Source
	ResolvedRevision  string
	RuntimeGeneration string
	Limits            domain.EffectiveSessionLimits
	Reason            string
}

// SessionRecord is the authoritative session snapshot persisted by this
// store. Target, environment, and controller identity are immutable for the
// lifetime of a session.
type SessionRecord struct {
	SessionID         domain.SessionID
	Target            domain.ExecutionTarget
	Environment       string
	Controller        domain.ControllerIdentity
	Source            domain.Source
	ResolvedRevision  string
	RuntimeGeneration string
	State             domain.SessionState
	Limits            domain.EffectiveSessionLimits
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ExpiresAt         time.Time
}

// SessionLifecycleRecord is one durable state transition. The first record
// has a nil PreviousState and records the authoritative creating acceptance.
type SessionLifecycleRecord struct {
	SessionID     domain.SessionID
	Sequence      int64
	PreviousState *domain.SessionState
	NewState      domain.SessionState
	Reason        string
	OccurredAt    time.Time
}

// AuthorityStore owns authoritative SQLite session records. Its clock is
// injectable so expiry and transaction fixtures do not depend on wall time.
type AuthorityStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewAuthorityStore wraps an opened P011 SQLite database with the P012 session
// methods and the production wall clock.
func NewAuthorityStore(db *sql.DB) (*AuthorityStore, error) {
	return NewAuthorityStoreWithClock(db, time.Now)
}

// NewAuthorityStoreWithClock constructs a store with a deterministic clock.
func NewAuthorityStoreWithClock(db *sql.DB, now func() time.Time) (*AuthorityStore, error) {
	if db == nil {
		return nil, ErrNilDatabase
	}
	if now == nil {
		return nil, fmt.Errorf("%w: clock is nil", ErrInvalidSession)
	}
	return &AuthorityStore{db: db, now: now}, nil
}

// SessionCreateAcceptance contains the authoritative create request and the
// stable idempotency data supplied by its controller. MaxActiveSessions and
// IdempotencyRetention use the selected PoC defaults when zero.
type SessionCreateAcceptance struct {
	SessionCreate        SessionCreate
	IdempotencyKey       string
	RequestHash          domain.CanonicalHash
	MaxActiveSessions    int
	IdempotencyRetention time.Duration
}

// SessionReservation is the durable host-capacity reservation for a session.
// A reservation remains live until both cleanup and release timestamps are
// recorded by a later confirmed-teardown transaction.
type SessionReservation struct {
	SessionID          domain.SessionID
	HostKey            string
	ReservedAt         time.Time
	CleanupConfirmedAt *time.Time
	ReleasedAt         *time.Time
}

// AcceptSessionCreate atomically accepts a new session, its create idempotency
// record, lifecycle sequence 1, and one host-capacity reservation. A duplicate
// same-key/same-hash request returns the original session with duplicate=true;
// a changed hash conflicts. No runtime is started by this store method.
func (s *AuthorityStore) AcceptSessionCreate(ctx context.Context, input SessionCreateAcceptance) (record SessionRecord, duplicate bool, err error) {
	validated, err := validateSessionAcceptance(input)
	if err != nil {
		return SessionRecord{}, false, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(validated.IdempotencyRetention)
	record, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (SessionRecord, error) {
		if existing, found, err := findSessionIdempotency(ctx, connection, validated.SessionCreate.Controller, validated.IdempotencyKey, now); err != nil {
			return SessionRecord{}, err
		} else if found {
			if domain.CompareIdempotency(existing.Hash, validated.RequestHash) == domain.IdempotencyConflict {
				return SessionRecord{}, ErrIdempotencyConflict
			}
			if _, err := readReservationOnConnection(ctx, connection, existing.ResourceID); err != nil {
				return SessionRecord{}, fmt.Errorf("read idempotent session reservation: %w", err)
			}
			existingRecord, err := readSessionOnConnection(ctx, connection, existing.ResourceID)
			if err != nil {
				return SessionRecord{}, fmt.Errorf("read idempotent session: %w", err)
			}
			duplicate = true
			return existingRecord, nil
		}

		if err := ensureSessionCapacity(ctx, connection, validated.MaxActiveSessions); err != nil {
			return SessionRecord{}, err
		}
		if err := insertSessionAcceptance(ctx, connection, validated.SessionCreate, validated.IdempotencyKey, validated.RequestHash, expiresAt, now); err != nil {
			return SessionRecord{}, err
		}
		return readSessionOnConnection(ctx, connection, validated.SessionCreate.SessionID)
	})
	if err != nil {
		return SessionRecord{}, false, err
	}
	return record, duplicate, nil
}

// CreateSession atomically inserts the session, lifecycle sequence 1, and a
// host-capacity reservation. It is retained as the P012 fixture-compatible
// non-idempotent constructor; production-shaped acceptance uses
// AcceptSessionCreate above.
func (s *AuthorityStore) CreateSession(ctx context.Context, input SessionCreate) (SessionRecord, error) {
	validated, err := validateSessionCreate(input)
	if err != nil {
		return SessionRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (SessionRecord, error) {
		if err := ensureSessionCapacity(ctx, connection, DefaultActiveSessionLimit); err != nil {
			return SessionRecord{}, err
		}
		if err := insertSessionAcceptance(ctx, connection, validated, "", domain.CanonicalHash{}, time.Time{}, now); err != nil {
			return SessionRecord{}, err
		}
		return readSessionOnConnection(ctx, connection, validated.SessionID)
	})
}

// GetSession returns the current authoritative snapshot for one session.
func (s *AuthorityStore) GetSession(ctx context.Context, id domain.SessionID) (SessionRecord, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return SessionRecord{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("acquire session connection: %w", err)
	}
	defer connection.Close()
	return readSessionOnConnection(ctx, connection, validatedID)
}

// CountLiveSessionReservations returns reservations whose cleanup has not
// been durably confirmed. It is the admission count used by create
// acceptance, and therefore includes creating, failed, or lost sessions until
// a later cleanup confirmation releases them.
func (s *AuthorityStore) CountLiveSessionReservations(ctx context.Context) (int, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire reservation connection: %w", err)
	}
	defer connection.Close()
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_capacity_reservations
WHERE host_key = ? AND cleanup_confirmed_at IS NULL
`, reservationHostKey).Scan(&count); err != nil {
		return 0, fmt.Errorf("count live session reservations: %w", err)
	}
	return count, nil
}

// GetSessionReservation reads the durable reservation for one session.
func (s *AuthorityStore) GetSessionReservation(ctx context.Context, id domain.SessionID) (SessionReservation, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return SessionReservation{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return SessionReservation{}, fmt.Errorf("acquire reservation connection: %w", err)
	}
	defer connection.Close()
	return readReservationOnConnection(ctx, connection, validatedID)
}

// ConfirmSessionCleanup records the only event that releases a session slot.
// It is idempotent so a reconciler retry cannot double-release capacity.
func (s *AuthorityStore) ConfirmSessionCleanup(ctx context.Context, id domain.SessionID) error {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		result, err := connection.ExecContext(ctx, `
UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND cleanup_confirmed_at IS NULL
`, formatStoredTime(now), formatStoredTime(now), string(validatedID))
		if err != nil {
			return struct{}{}, fmt.Errorf("confirm session cleanup: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return struct{}{}, fmt.Errorf("read cleanup result: %w", err)
		}
		if changed > 0 {
			return struct{}{}, nil
		}
		var ignored string
		if err := connection.QueryRowContext(ctx,
			"SELECT session_id FROM exec_capacity_reservations WHERE session_id = ?", string(validatedID)).Scan(&ignored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrSessionReservationNotFound
			}
			return struct{}{}, fmt.Errorf("check session reservation: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

// TransitionSession atomically validates a D-01 edge, updates the current
// state, and appends the corresponding lifecycle record. Target/profile,
// environment, and controller identity are never accepted as update input.
func (s *AuthorityStore) TransitionSession(ctx context.Context, id domain.SessionID, next domain.SessionState, reason string) (SessionRecord, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return SessionRecord{}, err
	}
	if !next.Valid() {
		return SessionRecord{}, fmt.Errorf("%w: %q", domain.ErrInvalidSessionState, next)
	}
	reason, err = validateLifecycleReason(reason)
	if err != nil {
		return SessionRecord{}, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (SessionRecord, error) {
		var currentValue string
		if err := connection.QueryRowContext(ctx,
			"SELECT state FROM exec_sessions WHERE session_id = ?", string(validatedID)).Scan(&currentValue); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return SessionRecord{}, ErrSessionNotFound
			}
			return SessionRecord{}, fmt.Errorf("read current session state: %w", err)
		}
		current := domain.SessionState(currentValue)
		if err := domain.ValidateSessionTransition(current, next); err != nil {
			return SessionRecord{}, err
		}
		var sequence int64
		if err := connection.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(lifecycle_sequence), 0) + 1 FROM exec_session_lifecycle WHERE session_id = ?",
			string(validatedID)).Scan(&sequence); err != nil {
			return SessionRecord{}, fmt.Errorf("read session lifecycle sequence: %w", err)
		}
		if _, err := connection.ExecContext(ctx,
			"UPDATE exec_sessions SET state = ?, updated_at = ? WHERE session_id = ? AND state = ?",
			string(next), formatStoredTime(now), string(validatedID), currentValue); err != nil {
			return SessionRecord{}, fmt.Errorf("update session state: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_session_lifecycle (session_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, ?, ?, ?, ?, ?)
`, string(validatedID), sequence, currentValue, string(next), reason, formatStoredTime(now)); err != nil {
			return SessionRecord{}, fmt.Errorf("insert session lifecycle: %w", err)
		}
		return readSessionOnConnection(ctx, connection, validatedID)
	})
}

// ListSessionLifecycle returns lifecycle records in durable sequence order.
func (s *AuthorityStore) ListSessionLifecycle(ctx context.Context, id domain.SessionID) ([]SessionLifecycleRecord, error) {
	validatedID, err := domain.NewSessionID(string(id))
	if err != nil {
		return nil, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire lifecycle connection: %w", err)
	}
	defer connection.Close()
	if _, err := readSessionOnConnection(ctx, connection, validatedID); err != nil {
		return nil, err
	}
	rows, err := connection.QueryContext(ctx, `
SELECT lifecycle_sequence, previous_state, new_state, reason, occurred_at
FROM exec_session_lifecycle WHERE session_id = ? ORDER BY lifecycle_sequence
`, string(validatedID))
	if err != nil {
		return nil, fmt.Errorf("query session lifecycle: %w", err)
	}
	defer rows.Close()
	var records []SessionLifecycleRecord
	for rows.Next() {
		var record SessionLifecycleRecord
		var previous sql.NullString
		var next, occurredAt string
		if err := rows.Scan(&record.Sequence, &previous, &next, &record.Reason, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan session lifecycle: %w", err)
		}
		record.SessionID = validatedID
		record.NewState = domain.SessionState(next)
		if !record.NewState.Valid() {
			return nil, fmt.Errorf("%w: invalid new state %q", ErrSessionLifecycle, next)
		}
		if previous.Valid {
			state := domain.SessionState(previous.String)
			if !state.Valid() {
				return nil, fmt.Errorf("%w: invalid previous state %q", ErrSessionLifecycle, previous.String)
			}
			record.PreviousState = &state
		}
		record.OccurredAt, err = parseStoredTime(occurredAt)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSessionLifecycle, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session lifecycle: %w", err)
	}
	if len(records) == 0 {
		return nil, ErrSessionLifecycle
	}
	return records, nil
}

func validateSessionCreate(input SessionCreate) (SessionCreate, error) {
	id, err := domain.NewSessionID(string(input.SessionID))
	if err != nil {
		return SessionCreate{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	target, err := domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil {
		return SessionCreate{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	controllerID, err := domain.NewControllerID(string(input.Controller.ID()))
	if err != nil {
		return SessionCreate{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	controller, err := domain.NewControllerIdentity(input.Controller.Type(), controllerID)
	if err != nil {
		return SessionCreate{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	source, err := normalizeSource(input.Source)
	if err != nil {
		return SessionCreate{}, err
	}
	if strings.TrimSpace(input.Environment) == "" {
		return SessionCreate{}, fmt.Errorf("%w: environment is empty", ErrInvalidSession)
	}
	if input.ResolvedRevision != "" && source.Mode() != domain.SourceModeGitRevision {
		return SessionCreate{}, fmt.Errorf("%w: resolved revision requires git_revision source", ErrInvalidSession)
	}
	if len(input.RuntimeGeneration) > 256 || strings.IndexByte(input.RuntimeGeneration, 0) >= 0 {
		return SessionCreate{}, fmt.Errorf("%w: runtime generation is invalid", ErrInvalidSession)
	}
	if input.Limits.CommandTimeout <= 0 || input.Limits.IdleTimeout <= 0 || input.Limits.SessionMaxLifetime <= 0 || input.Limits.OutputBytesPerCommand <= 0 {
		return SessionCreate{}, fmt.Errorf("%w: effective session limits must be positive", ErrInvalidSession)
	}
	reason, err := validateLifecycleReason(input.Reason)
	if err != nil {
		if input.Reason == "" {
			reason = "session_created"
		} else {
			return SessionCreate{}, err
		}
	}
	return SessionCreate{
		SessionID:         id,
		Target:            target,
		Environment:       strings.TrimSpace(input.Environment),
		Controller:        controller,
		Source:            source,
		ResolvedRevision:  input.ResolvedRevision,
		RuntimeGeneration: input.RuntimeGeneration,
		Limits:            input.Limits,
		Reason:            reason,
	}, nil
}

func validateSessionAcceptance(input SessionCreateAcceptance) (SessionCreateAcceptance, error) {
	session, err := validateSessionCreate(input.SessionCreate)
	if err != nil {
		return SessionCreateAcceptance{}, err
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 256 || strings.IndexByte(input.IdempotencyKey, 0) >= 0 {
		return SessionCreateAcceptance{}, fmt.Errorf("%w: key must be 1..256 bytes and contain no NUL", ErrIdempotencyKey)
	}
	hash, err := domain.NewCanonicalHash(input.RequestHash.Version(), input.RequestHash.SHA256())
	if err != nil {
		return SessionCreateAcceptance{}, fmt.Errorf("%w: request hash: %v", ErrInvalidSession, err)
	}
	maxActive := input.MaxActiveSessions
	if maxActive == 0 {
		maxActive = DefaultActiveSessionLimit
	}
	if maxActive < 1 {
		return SessionCreateAcceptance{}, fmt.Errorf("%w: maximum active sessions must be positive", ErrInvalidSession)
	}
	retention := input.IdempotencyRetention
	if retention == 0 {
		retention = DefaultSessionIdempotencyRetention
	}
	if retention < 0 {
		return SessionCreateAcceptance{}, fmt.Errorf("%w: idempotency retention must not be negative", ErrInvalidSession)
	}
	return SessionCreateAcceptance{
		SessionCreate:        session,
		IdempotencyKey:       input.IdempotencyKey,
		RequestHash:          hash,
		MaxActiveSessions:    maxActive,
		IdempotencyRetention: retention,
	}, nil
}

type sessionIdempotencyRecord struct {
	Hash       domain.CanonicalHash
	ResourceID domain.SessionID
}

func findSessionIdempotency(ctx context.Context, connection *sql.Conn, controller domain.ControllerIdentity, key string, now time.Time) (sessionIdempotencyRecord, bool, error) {
	var version int
	var digest []byte
	var resourceID, expiresAt string
	err := connection.QueryRowContext(ctx, `
SELECT canonical_hash_version, canonical_hash, resource_id, expires_at
FROM exec_idempotency
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
`, string(controller.Type()), string(controller.ID()), createSessionOperation, key).Scan(&version, &digest, &resourceID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionIdempotencyRecord{}, false, nil
	}
	if err != nil {
		return sessionIdempotencyRecord{}, false, fmt.Errorf("lookup session idempotency: %w", err)
	}
	expiry, err := parseStoredTime(expiresAt)
	if err != nil {
		return sessionIdempotencyRecord{}, false, fmt.Errorf("%w: idempotency expiry: %v", ErrSessionLifecycle, err)
	}
	if !now.Before(expiry) {
		if _, err := connection.ExecContext(ctx, `
DELETE FROM exec_idempotency
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND idempotency_key = ?
`, string(controller.Type()), string(controller.ID()), createSessionOperation, key); err != nil {
			return sessionIdempotencyRecord{}, false, fmt.Errorf("expire session idempotency: %w", err)
		}
		return sessionIdempotencyRecord{}, false, nil
	}
	hash, err := domain.NewCanonicalHash(uint16(version), digest)
	if err != nil {
		return sessionIdempotencyRecord{}, false, fmt.Errorf("%w: stored idempotency hash: %v", ErrSessionLifecycle, err)
	}
	validatedResourceID, err := domain.NewSessionID(resourceID)
	if err != nil {
		return sessionIdempotencyRecord{}, false, fmt.Errorf("%w: stored idempotency resource: %v", ErrSessionLifecycle, err)
	}
	return sessionIdempotencyRecord{Hash: hash, ResourceID: validatedResourceID}, true, nil
}

func ensureSessionCapacity(ctx context.Context, connection *sql.Conn, maxActive int) error {
	var count int
	if err := connection.QueryRowContext(ctx, `
SELECT COUNT(*) FROM exec_capacity_reservations
WHERE host_key = ? AND cleanup_confirmed_at IS NULL
`, reservationHostKey).Scan(&count); err != nil {
		return fmt.Errorf("count session capacity: %w", err)
	}
	if count >= maxActive {
		return fmt.Errorf("%w: %d live reservations, limit %d", ErrSessionCapacityExceeded, count, maxActive)
	}
	return nil
}

func insertSessionAcceptance(ctx context.Context, connection *sql.Conn, validated SessionCreate, idempotencyKey string, requestHash domain.CanonicalHash, idempotencyExpiresAt, now time.Time) error {
	var existing int
	err := connection.QueryRowContext(ctx,
		"SELECT 1 FROM exec_sessions WHERE session_id = ?", string(validated.SessionID)).Scan(&existing)
	if err == nil {
		return ErrSessionExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check session identity: %w", err)
	}

	expiresAt := now.Add(validated.Limits.SessionMaxLifetime)
	if _, err = connection.ExecContext(ctx, `
INSERT INTO exec_sessions (
    session_id, target_kind, target_profile, environment,
    controller_type, controller_id,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    source_resolved_revision, runtime_generation, state,
    command_timeout_ns, idle_timeout_ns, session_max_lifetime_ns, output_bytes_per_command,
    created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		string(validated.SessionID), string(validated.Target.Kind()), validated.Target.Profile(), validated.Environment,
		string(validated.Controller.Type()), string(validated.Controller.ID()),
		string(validated.Source.Mode()), validated.Source.RepositoryAlias(), validated.Source.RequestedRevision(), validated.Source.Path(),
		validated.ResolvedRevision, validated.RuntimeGeneration, string(domain.SessionStateCreating),
		int64(validated.Limits.CommandTimeout), int64(validated.Limits.IdleTimeout), int64(validated.Limits.SessionMaxLifetime), validated.Limits.OutputBytesPerCommand,
		formatStoredTime(now), formatStoredTime(now), formatStoredTime(expiresAt),
	); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_session_lifecycle (session_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, 1, NULL, ?, ?, ?)
`, string(validated.SessionID), string(domain.SessionStateCreating), validated.Reason, formatStoredTime(now)); err != nil {
		return fmt.Errorf("insert initial session lifecycle: %w", err)
	}
	if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_capacity_reservations (session_id, host_key, reserved_at)
VALUES (?, ?, ?)
`, string(validated.SessionID), reservationHostKey, formatStoredTime(now)); err != nil {
		return fmt.Errorf("insert session capacity reservation: %w", err)
	}
	if idempotencyKey != "" {
		if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_idempotency (
    controller_type, controller_id, operation, idempotency_key,
    canonical_hash_version, canonical_hash, resource_id, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(validated.Controller.Type()), string(validated.Controller.ID()), createSessionOperation, idempotencyKey,
			requestHash.Version(), requestHash.SHA256(), string(validated.SessionID), formatStoredTime(now), formatStoredTime(idempotencyExpiresAt)); err != nil {
			return fmt.Errorf("insert session idempotency: %w", err)
		}
	}
	return nil
}

func readReservationOnConnection(ctx context.Context, connection *sql.Conn, id domain.SessionID) (SessionReservation, error) {
	var reservation SessionReservation
	var sessionID, hostKey, reservedAt string
	var cleanupConfirmedAt, releasedAt sql.NullString
	if err := connection.QueryRowContext(ctx, `
SELECT session_id, host_key, reserved_at, cleanup_confirmed_at, released_at
FROM exec_capacity_reservations WHERE session_id = ?
`, string(id)).Scan(&sessionID, &hostKey, &reservedAt, &cleanupConfirmedAt, &releasedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionReservation{}, ErrSessionReservationNotFound
		}
		return SessionReservation{}, fmt.Errorf("read session reservation: %w", err)
	}
	validatedID, err := domain.NewSessionID(sessionID)
	if err != nil {
		return SessionReservation{}, fmt.Errorf("%w: reservation session ID: %v", ErrSessionLifecycle, err)
	}
	reservation.SessionID = validatedID
	reservation.HostKey = hostKey
	if reservation.ReservedAt, err = parseStoredTime(reservedAt); err != nil {
		return SessionReservation{}, fmt.Errorf("%w: reservation time: %v", ErrSessionLifecycle, err)
	}
	if cleanupConfirmedAt.Valid {
		value, err := parseStoredTime(cleanupConfirmedAt.String)
		if err != nil {
			return SessionReservation{}, fmt.Errorf("%w: cleanup time: %v", ErrSessionLifecycle, err)
		}
		reservation.CleanupConfirmedAt = &value
	}
	if releasedAt.Valid {
		value, err := parseStoredTime(releasedAt.String)
		if err != nil {
			return SessionReservation{}, fmt.Errorf("%w: release time: %v", ErrSessionLifecycle, err)
		}
		reservation.ReleasedAt = &value
	}
	return reservation, nil
}

func normalizeSource(source domain.Source) (domain.Source, error) {
	switch source.Mode() {
	case domain.SourceModeEmpty:
		if source.RepositoryAlias() != "" || source.RequestedRevision() != "" || source.Path() != "" {
			return domain.Source{}, fmt.Errorf("%w: empty source has extra fields", ErrInvalidSession)
		}
		return domain.NewEmptySource(), nil
	case domain.SourceModeGitRevision:
		value, err := domain.NewGitRevisionSource(source.RepositoryAlias(), source.RequestedRevision())
		if err != nil {
			return domain.Source{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
		}
		return value, nil
	case domain.SourceModeLocalWorktree:
		if source.RepositoryAlias() != "" || source.RequestedRevision() != "" || !filepath.IsAbs(source.Path()) {
			return domain.Source{}, fmt.Errorf("%w: invalid local_worktree source", ErrInvalidSession)
		}
		value, err := domain.NewLocalWorktreeSource(source.Path())
		if err != nil {
			return domain.Source{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
		}
		return value, nil
	default:
		return domain.Source{}, fmt.Errorf("%w: unknown source mode %q", ErrInvalidSession, source.Mode())
	}
}

func validateLifecycleReason(reason string) (string, error) {
	if reason == "" {
		return "", ErrInvalidSession
	}
	if len(reason) > 512 || strings.IndexByte(reason, 0) >= 0 {
		return "", fmt.Errorf("%w: lifecycle reason is invalid", ErrInvalidSession)
	}
	return reason, nil
}

func readSessionOnConnection(ctx context.Context, connection *sql.Conn, id domain.SessionID) (SessionRecord, error) {
	var record SessionRecord
	var err error
	var sessionID, targetKind, targetProfile, environment, controllerType, controllerID string
	var sourceMode, repositoryAlias, requestedRevision, sourcePath, resolvedRevision, runtimeGeneration string
	var state string
	var commandTimeout, idleTimeout, maxLifetime, outputBytes int64
	var createdAt, updatedAt, expiresAt string
	if err := connection.QueryRowContext(ctx, `
SELECT session_id, target_kind, target_profile, environment, controller_type, controller_id,
       source_mode, source_repository_alias, source_requested_revision, source_path,
       source_resolved_revision, runtime_generation, state,
       command_timeout_ns, idle_timeout_ns, session_max_lifetime_ns, output_bytes_per_command,
       created_at, updated_at, expires_at
	FROM exec_sessions WHERE session_id = ?
`, string(id)).Scan(
		&sessionID, &targetKind, &targetProfile, &environment, &controllerType, &controllerID,
		&sourceMode, &repositoryAlias, &requestedRevision, &sourcePath,
		&resolvedRevision, &runtimeGeneration, &state,
		&commandTimeout, &idleTimeout, &maxLifetime, &outputBytes,
		&createdAt, &updatedAt, &expiresAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionRecord{}, ErrSessionNotFound
		}
		return SessionRecord{}, fmt.Errorf("read session: %w", err)
	}
	record.SessionID, err = domain.NewSessionID(sessionID)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("%w: session ID: %v", ErrSessionLifecycle, err)
	}
	record.Target, err = domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("%w: target: %v", ErrSessionLifecycle, err)
	}
	controllerIDValue, err := domain.NewControllerID(controllerID)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("%w: controller ID: %v", ErrSessionLifecycle, err)
	}
	record.Controller, err = domain.NewControllerIdentity(domain.ControllerType(controllerType), controllerIDValue)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("%w: controller: %v", ErrSessionLifecycle, err)
	}
	record.Environment = environment
	record.Source, err = decodeSource(domain.SourceMode(sourceMode), repositoryAlias, requestedRevision, sourcePath)
	if err != nil {
		return SessionRecord{}, err
	}
	record.ResolvedRevision = resolvedRevision
	record.RuntimeGeneration = runtimeGeneration
	record.State = domain.SessionState(state)
	if !record.State.Valid() {
		return SessionRecord{}, fmt.Errorf("%w: invalid state %q", ErrSessionLifecycle, state)
	}
	record.Limits = domain.EffectiveSessionLimits{
		CommandTimeout:        time.Duration(commandTimeout),
		IdleTimeout:           time.Duration(idleTimeout),
		SessionMaxLifetime:    time.Duration(maxLifetime),
		OutputBytesPerCommand: outputBytes,
	}
	if record.Limits.CommandTimeout <= 0 || record.Limits.IdleTimeout <= 0 || record.Limits.SessionMaxLifetime <= 0 || record.Limits.OutputBytesPerCommand <= 0 {
		return SessionRecord{}, fmt.Errorf("%w: non-positive effective limits", ErrSessionLifecycle)
	}
	if record.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return SessionRecord{}, fmt.Errorf("%w: created_at: %v", ErrSessionLifecycle, err)
	}
	if record.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
		return SessionRecord{}, fmt.Errorf("%w: updated_at: %v", ErrSessionLifecycle, err)
	}
	if record.ExpiresAt, err = parseStoredTime(expiresAt); err != nil {
		return SessionRecord{}, fmt.Errorf("%w: expires_at: %v", ErrSessionLifecycle, err)
	}
	return record, nil
}

func decodeSource(mode domain.SourceMode, repositoryAlias, requestedRevision, path string) (domain.Source, error) {
	switch mode {
	case domain.SourceModeEmpty:
		return normalizeSource(domain.NewEmptySource())
	case domain.SourceModeGitRevision:
		source, err := domain.NewGitRevisionSource(repositoryAlias, requestedRevision)
		if err != nil {
			return domain.Source{}, fmt.Errorf("%w: %v", ErrSessionLifecycle, err)
		}
		return source, nil
	case domain.SourceModeLocalWorktree:
		source, err := domain.NewLocalWorktreeSource(path)
		if err != nil {
			return domain.Source{}, fmt.Errorf("%w: %v", ErrSessionLifecycle, err)
		}
		return source, nil
	default:
		return domain.Source{}, fmt.Errorf("%w: source mode %q", ErrSessionLifecycle, mode)
	}
}

func formatStoredTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return time.Time{}, fmt.Errorf("invalid UTC timestamp %q", value)
	}
	return parsed.UTC(), nil
}

func withImmediateTransaction[T any](ctx context.Context, db *sql.DB, fn func(context.Context, *sql.Conn) (T, error)) (result T, err error) {
	connection, err := db.Conn(ctx)
	if err != nil {
		return result, fmt.Errorf("acquire SQLite transaction connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return result, fmt.Errorf("begin SQLite transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	result, err = fn(ctx, connection)
	if err != nil {
		return result, err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return result, fmt.Errorf("commit SQLite transaction: %w", err)
	}
	committed = true
	return result, nil
}
