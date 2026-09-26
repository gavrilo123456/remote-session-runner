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
)

// SessionCreate contains the immutable and effective values recorded when an
// authority accepts a new session. Runtime startup and capacity reservation
// are later-phase responsibilities; P012 records the session in creating
// state before those actions occur.
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

// CreateSession atomically inserts the session and its initial creating
// lifecycle record. It does not start a runtime or reserve a host slot.
func (s *AuthorityStore) CreateSession(ctx context.Context, input SessionCreate) (SessionRecord, error) {
	validated, err := validateSessionCreate(input)
	if err != nil {
		return SessionRecord{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(validated.Limits.SessionMaxLifetime)
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (SessionRecord, error) {
		var existing int
		err := connection.QueryRowContext(ctx,
			"SELECT 1 FROM exec_sessions WHERE session_id = ?", string(validated.SessionID)).Scan(&existing)
		if err == nil {
			return SessionRecord{}, ErrSessionExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return SessionRecord{}, fmt.Errorf("check session identity: %w", err)
		}

		_, err = connection.ExecContext(ctx, `
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
		)
		if err != nil {
			return SessionRecord{}, fmt.Errorf("insert session: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_session_lifecycle (session_id, lifecycle_sequence, previous_state, new_state, reason, occurred_at)
VALUES (?, 1, NULL, ?, ?, ?)
`, string(validated.SessionID), string(domain.SessionStateCreating), validated.Reason, formatStoredTime(now)); err != nil {
			return SessionRecord{}, fmt.Errorf("insert initial session lifecycle: %w", err)
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
