package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

var (
	ErrRemoteProjectionInvalid  = errors.New("invalid remote projection")
	ErrRemoteProjectionNotFound = errors.New("remote projection not found")
	ErrRemoteProjectionConflict = errors.New("remote projection immutable identity conflicts")
)

// RemoteCapabilities is the effective capability snapshot reported by the
// remote authority. ServiceLimits is intentionally JSON-shaped so new limit
// fields can be preserved without changing the local projection schema.
type RemoteCapabilities struct {
	HostClass        string         `json:"host_class"`
	Isolation        string         `json:"isolation"`
	EffectiveAccount string         `json:"effective_account"`
	ServiceLimits    map[string]any `json:"service_limits"`
}

// RemoteSessionProjection is a read-only Mac view of a remote authoritative
// session snapshot. It never participates in local execution transitions.
type RemoteSessionProjection struct {
	SessionID         domain.SessionID
	Target            domain.ExecutionTarget
	Controller        domain.ControllerIdentity
	State             domain.SessionState
	Environment       string
	Source            domain.Source
	ResolvedRevision  string
	RuntimeGeneration string
	Capabilities      RemoteCapabilities
	ObservedAt        time.Time
	IsStale           bool
}

// RemoteCommandProjection is a read-only Mac view of a remote command.
type RemoteCommandProjection struct {
	CommandID               domain.CommandID
	SessionID               domain.SessionID
	Ordinal                 int64
	State                   domain.CommandState
	ExitCode                *int
	FinalEventSequence      *int64
	OutputComplete          bool
	OutputTruncated         bool
	OutputUnavailableReason string
	Target                  domain.ExecutionTarget
	Controller              domain.ControllerIdentity
	Environment             string
	Source                  domain.Source
	Capabilities            RemoteCapabilities
	ObservedAt              time.Time
	IsStale                 bool
}

type projectionSourceJSON struct {
	Mode              string `json:"mode"`
	RepositoryAlias   string `json:"repository_alias,omitempty"`
	RequestedRevision string `json:"requested_revision,omitempty"`
	ResolvedCommit    string `json:"resolved_commit,omitempty"`
	Path              string `json:"path,omitempty"`
	Portable          *bool  `json:"portable,omitempty"`
}

func (s *AuthorityStore) UpsertRemoteSessionProjection(ctx context.Context, input RemoteSessionProjection) (RemoteSessionProjection, error) {
	validated, sourceJSON, capabilitiesJSON, err := validateRemoteSessionProjection(input)
	if err != nil {
		return RemoteSessionProjection{}, err
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteSessionProjection, error) {
		stored, err := readRemoteSessionProjectionOnConnection(ctx, connection, validated.SessionID)
		if errors.Is(err, ErrRemoteProjectionNotFound) {
			_, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_session_projections (
 session_id, target_kind, target_profile, controller_type, controller_id,
 session_state, environment, source_json, capabilities_json,
 runtime_generation, resolved_revision, observed_at, is_stale
) VALUES (?, 'remote', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(validated.SessionID), validated.Target.Profile(), string(validated.Controller.Type()), string(validated.Controller.ID()),
				string(validated.State), validated.Environment, sourceJSON, capabilitiesJSON, validated.RuntimeGeneration,
				validated.ResolvedRevision, formatStoredTime(validated.ObservedAt), boolInt(validated.IsStale))
			if err != nil {
				return RemoteSessionProjection{}, fmt.Errorf("insert remote session projection: %w", err)
			}
			return validated, nil
		}
		if err != nil {
			return RemoteSessionProjection{}, err
		}
		if err := validateRemoteSessionProjectionIdentity(stored, validated); err != nil {
			return RemoteSessionProjection{}, err
		}
		if validated.ObservedAt.Before(stored.ObservedAt) {
			return stored, nil
		}
		_, err = connection.ExecContext(ctx, `
UPDATE local_remote_session_projections SET
 session_state = ?, runtime_generation = ?, resolved_revision = ?, observed_at = ?, is_stale = ?, capabilities_json = ?
WHERE session_id = ?
`, string(validated.State), validated.RuntimeGeneration, validated.ResolvedRevision, formatStoredTime(validated.ObservedAt), boolInt(validated.IsStale), capabilitiesJSON, string(validated.SessionID))
		if err != nil {
			return RemoteSessionProjection{}, fmt.Errorf("update remote session projection: %w", err)
		}
		return validated, nil
	})
}

func (s *AuthorityStore) GetRemoteSessionProjection(ctx context.Context, id domain.SessionID) (RemoteSessionProjection, error) {
	validated, err := domain.NewSessionID(string(id))
	if err != nil {
		return RemoteSessionProjection{}, err
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteSessionProjection, error) {
		return readRemoteSessionProjectionOnConnection(ctx, connection, validated)
	})
}

// MarkRemoteSessionProjectionStale marks a projection stale without changing
// the last authoritative snapshot or its observed_at value.
func (s *AuthorityStore) MarkRemoteSessionProjectionStale(ctx context.Context, id domain.SessionID) error {
	validated, err := domain.NewSessionID(string(id))
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		result, err := connection.ExecContext(ctx, `UPDATE local_remote_session_projections SET is_stale = 1 WHERE session_id = ?`, string(validated))
		if err != nil {
			return struct{}{}, fmt.Errorf("mark remote session projection stale: %w", err)
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return struct{}{}, ErrRemoteProjectionNotFound
		}
		return struct{}{}, nil
	})
	return err
}

func (s *AuthorityStore) UpsertRemoteCommandProjection(ctx context.Context, input RemoteCommandProjection) (RemoteCommandProjection, error) {
	validated, sourceJSON, capabilitiesJSON, err := validateRemoteCommandProjection(input)
	if err != nil {
		return RemoteCommandProjection{}, err
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteCommandProjection, error) {
		stored, err := readRemoteCommandProjectionOnConnection(ctx, connection, validated.CommandID)
		if errors.Is(err, ErrRemoteProjectionNotFound) {
			_, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_command_projections (
 command_id, session_id, ordinal, command_state, exit_code, final_event_sequence,
 output_complete, output_truncated, output_unavailable_reason, target_kind, target_profile,
 controller_type, controller_id, environment, source_json, capabilities_json, observed_at, is_stale
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'remote', ?, ?, ?, ?, ?, ?, ?, ?)
`, string(validated.CommandID), string(validated.SessionID), validated.Ordinal, string(validated.State), nullableInt(validated.ExitCode), nullableInt64(validated.FinalEventSequence),
				boolInt(validated.OutputComplete), boolInt(validated.OutputTruncated), validated.OutputUnavailableReason, validated.Target.Profile(),
				string(validated.Controller.Type()), string(validated.Controller.ID()), validated.Environment, sourceJSON, capabilitiesJSON, formatStoredTime(validated.ObservedAt), boolInt(validated.IsStale))
			if err != nil {
				return RemoteCommandProjection{}, fmt.Errorf("insert remote command projection: %w", err)
			}
			return validated, nil
		}
		if err != nil {
			return RemoteCommandProjection{}, err
		}
		if err := validateRemoteCommandProjectionIdentity(stored, validated); err != nil {
			return RemoteCommandProjection{}, err
		}
		if validated.ObservedAt.Before(stored.ObservedAt) {
			return stored, nil
		}
		if stored.OutputUnavailableReason == "retention_expired" {
			validated.OutputComplete = false
			validated.OutputUnavailableReason = "retention_expired"
		}
		_, err = connection.ExecContext(ctx, `
UPDATE local_remote_command_projections SET
 command_state = ?, exit_code = ?, final_event_sequence = ?, output_complete = ?, output_truncated = ?,
 output_unavailable_reason = ?, observed_at = ?, is_stale = ?, capabilities_json = ?
WHERE command_id = ?
`, string(validated.State), nullableInt(validated.ExitCode), nullableInt64(validated.FinalEventSequence), boolInt(validated.OutputComplete), boolInt(validated.OutputTruncated),
			validated.OutputUnavailableReason, formatStoredTime(validated.ObservedAt), boolInt(validated.IsStale), capabilitiesJSON, string(validated.CommandID))
		if err != nil {
			return RemoteCommandProjection{}, fmt.Errorf("update remote command projection: %w", err)
		}
		return validated, nil
	})
}

func (s *AuthorityStore) GetRemoteCommandProjection(ctx context.Context, id domain.CommandID) (RemoteCommandProjection, error) {
	validated, err := domain.NewCommandID(string(id))
	if err != nil {
		return RemoteCommandProjection{}, err
	}
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (RemoteCommandProjection, error) {
		return readRemoteCommandProjectionOnConnection(ctx, connection, validated)
	})
}

// MarkRemoteCommandProjectionStale marks a command view stale while retaining
// its last authoritative fields and observation timestamp.
func (s *AuthorityStore) MarkRemoteCommandProjectionStale(ctx context.Context, id domain.CommandID) error {
	validated, err := domain.NewCommandID(string(id))
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		result, err := connection.ExecContext(ctx, `UPDATE local_remote_command_projections SET is_stale = 1 WHERE command_id = ?`, string(validated))
		if err != nil {
			return struct{}{}, fmt.Errorf("mark remote command projection stale: %w", err)
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return struct{}{}, ErrRemoteProjectionNotFound
		}
		return struct{}{}, nil
	})
	return err
}

func validateRemoteSessionProjection(input RemoteSessionProjection) (RemoteSessionProjection, string, string, error) {
	id, err := domain.NewSessionID(string(input.SessionID))
	if err != nil {
		return RemoteSessionProjection{}, "", "", fmt.Errorf("%w: session ID: %v", ErrRemoteProjectionInvalid, err)
	}
	target, err := domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil || target.Kind() != domain.TargetKindRemote {
		return RemoteSessionProjection{}, "", "", fmt.Errorf("%w: remote target", ErrRemoteProjectionInvalid)
	}
	controller, err := domain.NewControllerIdentity(input.Controller.Type(), input.Controller.ID())
	if err != nil {
		return RemoteSessionProjection{}, "", "", fmt.Errorf("%w: controller: %v", ErrRemoteProjectionInvalid, err)
	}
	if !input.State.Valid() || strings.TrimSpace(input.Environment) == "" || input.ObservedAt.IsZero() {
		return RemoteSessionProjection{}, "", "", fmt.Errorf("%w: session fields", ErrRemoteProjectionInvalid)
	}
	sourceJSON, err := marshalProjectionSource(input.Source, input.ResolvedRevision)
	if err != nil {
		return RemoteSessionProjection{}, "", "", err
	}
	capabilitiesJSON, err := marshalRemoteCapabilities(input.Capabilities)
	if err != nil {
		return RemoteSessionProjection{}, "", "", err
	}
	input.SessionID, input.Target, input.Controller = id, target, controller
	input.ObservedAt = input.ObservedAt.UTC()
	input.Capabilities = cloneRemoteCapabilities(input.Capabilities, capabilitiesJSON)
	return input, sourceJSON, capabilitiesJSON, nil
}

func validateRemoteCommandProjection(input RemoteCommandProjection) (RemoteCommandProjection, string, string, error) {
	id, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: command ID: %v", ErrRemoteProjectionInvalid, err)
	}
	sessionID, err := domain.NewSessionID(string(input.SessionID))
	if err != nil {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: session ID: %v", ErrRemoteProjectionInvalid, err)
	}
	target, err := domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil || target.Kind() != domain.TargetKindRemote {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: remote target", ErrRemoteProjectionInvalid)
	}
	controller, err := domain.NewControllerIdentity(input.Controller.Type(), input.Controller.ID())
	if err != nil {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: controller: %v", ErrRemoteProjectionInvalid, err)
	}
	if input.Ordinal <= 0 || !input.State.Valid() || strings.TrimSpace(input.Environment) == "" || input.ObservedAt.IsZero() {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: command fields", ErrRemoteProjectionInvalid)
	}
	if input.State.IsTerminal() && (input.FinalEventSequence == nil || *input.FinalEventSequence <= 0) {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: terminal command requires final event sequence", ErrRemoteProjectionInvalid)
	}
	if !input.OutputComplete && input.State.IsTerminal() && strings.TrimSpace(input.OutputUnavailableReason) == "" {
		return RemoteCommandProjection{}, "", "", fmt.Errorf("%w: incomplete terminal output requires reason", ErrRemoteProjectionInvalid)
	}
	sourceJSON, err := marshalProjectionSource(input.Source, "")
	if err != nil {
		return RemoteCommandProjection{}, "", "", err
	}
	capabilitiesJSON, err := marshalRemoteCapabilities(input.Capabilities)
	if err != nil {
		return RemoteCommandProjection{}, "", "", err
	}
	input.CommandID, input.SessionID, input.Target, input.Controller = id, sessionID, target, controller
	input.ObservedAt = input.ObservedAt.UTC()
	input.Capabilities = cloneRemoteCapabilities(input.Capabilities, capabilitiesJSON)
	return input, sourceJSON, capabilitiesJSON, nil
}

func marshalProjectionSource(source domain.Source, resolvedRevision string) (string, error) {
	if source.Mode() == "" {
		source = domain.NewEmptySource()
	}
	if source.Mode() == domain.SourceModeLocalWorktree {
		return "", fmt.Errorf("%w: remote projection cannot use local_worktree source", ErrRemoteProjectionInvalid)
	}
	payload, err := json.Marshal(projectionSourceJSON{Mode: string(source.Mode()), RepositoryAlias: source.RepositoryAlias(), RequestedRevision: source.RequestedRevision(), ResolvedCommit: resolvedRevision, Path: source.Path()})
	if err != nil {
		return "", fmt.Errorf("%w: source: %v", ErrRemoteProjectionInvalid, err)
	}
	return string(payload), nil
}

func marshalRemoteCapabilities(capabilities RemoteCapabilities) (string, error) {
	if strings.TrimSpace(capabilities.HostClass) == "" || capabilities.Isolation != string(domain.IsolationOSUser) || strings.TrimSpace(capabilities.EffectiveAccount) == "" || capabilities.ServiceLimits == nil {
		return "", fmt.Errorf("%w: capabilities", ErrRemoteProjectionInvalid)
	}
	payload, err := json.Marshal(capabilities)
	if err != nil {
		return "", fmt.Errorf("%w: capabilities: %v", ErrRemoteProjectionInvalid, err)
	}
	return string(payload), nil
}

func cloneRemoteCapabilities(capabilities RemoteCapabilities, raw string) RemoteCapabilities {
	var cloned RemoteCapabilities
	if json.Unmarshal([]byte(raw), &cloned) == nil {
		return cloned
	}
	return capabilities
}

func validateRemoteSessionProjectionIdentity(stored, incoming RemoteSessionProjection) error {
	if stored.SessionID != incoming.SessionID || stored.Target != incoming.Target || stored.Controller != incoming.Controller || stored.Environment != incoming.Environment || !sourcesEqual(stored.Source, incoming.Source) {
		return fmt.Errorf("%w: session %s", ErrRemoteProjectionConflict, incoming.SessionID)
	}
	return nil
}

func validateRemoteCommandProjectionIdentity(stored, incoming RemoteCommandProjection) error {
	if stored.CommandID != incoming.CommandID || stored.SessionID != incoming.SessionID || stored.Ordinal != incoming.Ordinal || stored.Target != incoming.Target || stored.Controller != incoming.Controller || stored.Environment != incoming.Environment || !sourcesEqual(stored.Source, incoming.Source) {
		return fmt.Errorf("%w: command %s", ErrRemoteProjectionConflict, incoming.CommandID)
	}
	return nil
}

func sourcesEqual(left, right domain.Source) bool {
	l, _ := marshalProjectionSource(left, "")
	r, _ := marshalProjectionSource(right, "")
	return l == r
}

func readRemoteSessionProjectionOnConnection(ctx context.Context, connection *sql.Conn, id domain.SessionID) (RemoteSessionProjection, error) {
	var result RemoteSessionProjection
	var targetKind, targetProfile, controllerType, controllerID, state, environment, sourceJSON, capabilitiesJSON, runtimeGeneration, resolvedRevision, observed string
	var stale int64
	err := connection.QueryRowContext(ctx, `
SELECT target_kind, target_profile, controller_type, controller_id, session_state,
       environment, source_json, capabilities_json, runtime_generation, resolved_revision, observed_at, is_stale
FROM local_remote_session_projections WHERE session_id = ?
`, string(id)).Scan(&targetKind, &targetProfile, &controllerType, &controllerID, &state, &environment, &sourceJSON, &capabilitiesJSON, &runtimeGeneration, &resolvedRevision, &observed, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteSessionProjection{}, ErrRemoteProjectionNotFound
	}
	if err != nil {
		return RemoteSessionProjection{}, fmt.Errorf("read remote session projection: %w", err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return RemoteSessionProjection{}, fmt.Errorf("%w: target: %v", ErrRemoteProjectionInvalid, err)
	}
	controllerIDValue, err := domain.NewControllerID(controllerID)
	if err != nil {
		return RemoteSessionProjection{}, fmt.Errorf("%w: controller ID: %v", ErrRemoteProjectionInvalid, err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerType(controllerType), controllerIDValue)
	if err != nil {
		return RemoteSessionProjection{}, fmt.Errorf("%w: controller: %v", ErrRemoteProjectionInvalid, err)
	}
	source, err := unmarshalProjectionSource(sourceJSON)
	if err != nil {
		return RemoteSessionProjection{}, err
	}
	var capabilities RemoteCapabilities
	if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil {
		return RemoteSessionProjection{}, fmt.Errorf("%w: capabilities: %v", ErrRemoteProjectionInvalid, err)
	}
	observedAt, err := parseStoredTime(observed)
	if err != nil {
		return RemoteSessionProjection{}, fmt.Errorf("%w: observed_at: %v", ErrRemoteProjectionInvalid, err)
	}
	result = RemoteSessionProjection{SessionID: id, Target: target, Controller: controller, State: domain.SessionState(state), Environment: environment, Source: source, ResolvedRevision: resolvedRevision, RuntimeGeneration: runtimeGeneration, Capabilities: capabilities, ObservedAt: observedAt, IsStale: stale != 0}
	if _, _, _, err := validateRemoteSessionProjection(result); err != nil {
		return RemoteSessionProjection{}, err
	}
	return result, nil
}

func readRemoteCommandProjectionOnConnection(ctx context.Context, connection *sql.Conn, id domain.CommandID) (RemoteCommandProjection, error) {
	var result RemoteCommandProjection
	var commandID, sessionID, targetKind, targetProfile, controllerType, controllerID, state, environment, sourceJSON, capabilitiesJSON, observed, reason string
	var ordinal, outputComplete, outputTruncated, stale int64
	var exitCode, finalSequence sql.NullInt64
	err := connection.QueryRowContext(ctx, `
SELECT session_id, ordinal, command_state, exit_code, final_event_sequence, output_complete,
       output_truncated, output_unavailable_reason, target_kind, target_profile,
       controller_type, controller_id, environment, source_json, capabilities_json, observed_at, is_stale
FROM local_remote_command_projections WHERE command_id = ?
`, string(id)).Scan(&sessionID, &ordinal, &state, &exitCode, &finalSequence, &outputComplete, &outputTruncated, &reason, &targetKind, &targetProfile, &controllerType, &controllerID, &environment, &sourceJSON, &capabilitiesJSON, &observed, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteCommandProjection{}, ErrRemoteProjectionNotFound
	}
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("read remote command projection: %w", err)
	}
	commandID = string(id)
	command, err := domain.NewCommandID(commandID)
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: command ID: %v", ErrRemoteProjectionInvalid, err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: target: %v", ErrRemoteProjectionInvalid, err)
	}
	session, err := domain.NewSessionID(sessionID)
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: session ID: %v", ErrRemoteProjectionInvalid, err)
	}
	controllerIDValue, err := domain.NewControllerID(controllerID)
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: controller ID: %v", ErrRemoteProjectionInvalid, err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerType(controllerType), controllerIDValue)
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: controller: %v", ErrRemoteProjectionInvalid, err)
	}
	source, err := unmarshalProjectionSource(sourceJSON)
	if err != nil {
		return RemoteCommandProjection{}, err
	}
	var capabilities RemoteCapabilities
	if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: capabilities: %v", ErrRemoteProjectionInvalid, err)
	}
	observedAt, err := parseStoredTime(observed)
	if err != nil {
		return RemoteCommandProjection{}, fmt.Errorf("%w: observed_at: %v", ErrRemoteProjectionInvalid, err)
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		result.ExitCode = &value
	}
	if finalSequence.Valid {
		value := finalSequence.Int64
		result.FinalEventSequence = &value
	}
	result = RemoteCommandProjection{CommandID: command, SessionID: session, Ordinal: ordinal, State: domain.CommandState(state), ExitCode: result.ExitCode, FinalEventSequence: result.FinalEventSequence, OutputComplete: outputComplete != 0, OutputTruncated: outputTruncated != 0, OutputUnavailableReason: reason, Target: target, Controller: controller, Environment: environment, Source: source, Capabilities: capabilities, ObservedAt: observedAt, IsStale: stale != 0}
	if _, _, _, err := validateRemoteCommandProjection(result); err != nil {
		return RemoteCommandProjection{}, err
	}
	return result, nil
}

func unmarshalProjectionSource(raw string) (domain.Source, error) {
	var input projectionSourceJSON
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return domain.Source{}, fmt.Errorf("%w: source: %v", ErrRemoteProjectionInvalid, err)
	}
	switch domain.SourceMode(input.Mode) {
	case "", domain.SourceModeEmpty:
		return domain.NewEmptySource(), nil
	case domain.SourceModeGitRevision:
		return domain.NewGitRevisionSource(input.RepositoryAlias, input.RequestedRevision)
	default:
		return domain.Source{}, fmt.Errorf("%w: source mode %q", ErrRemoteProjectionInvalid, input.Mode)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
