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

// RemoteJobProjection is a read-only Mac view of a remote one-off job.
type RemoteJobProjection struct {
	JobID                   domain.JobID
	SessionID               domain.SessionID
	CommandID               domain.CommandID
	Phase                   JobPhase
	CommandState            *domain.CommandState
	ExitCode                *int
	FinalEventSequence      *int64
	OutputComplete          bool
	OutputTruncated         bool
	OutputUnavailableReason string
	TeardownState           JobTeardownState
	TeardownReason          string
	Target                  domain.ExecutionTarget
	Controller              domain.ControllerIdentity
	Environment             string
	Source                  domain.Source
	Capabilities            RemoteCapabilities
	ObservedAt              time.Time
	IsStale                 bool
}

func (s *AuthorityStore) UpsertRemoteJobProjection(ctx context.Context, input RemoteJobProjection) (RemoteJobProjection, error) {
	validated, sourceJSON, capabilitiesJSON, err := validateRemoteJobProjection(input)
	if err != nil {
		return RemoteJobProjection{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (RemoteJobProjection, error) {
		stored, err := readRemoteJobProjectionOnConnection(ctx, connection, validated.JobID)
		if errors.Is(err, ErrRemoteProjectionNotFound) {
			_, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_job_projections (
 job_id, session_id, command_id, job_phase, command_state, exit_code, final_event_sequence,
 output_complete, output_truncated, output_unavailable_reason, teardown_state, teardown_reason,
 target_kind, target_profile, controller_type, controller_id, environment, source_json,
 capabilities_json, observed_at, is_stale
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'remote', ?, ?, ?, ?, ?, ?, ?, ?)
`, string(validated.JobID), string(validated.SessionID), string(validated.CommandID), string(validated.Phase), nullableCommandState(validated.CommandState), nullableInt(validated.ExitCode), nullableInt64(validated.FinalEventSequence),
				boolInt(validated.OutputComplete), boolInt(validated.OutputTruncated), validated.OutputUnavailableReason, string(validated.TeardownState), validated.TeardownReason,
				validated.Target.Profile(), string(validated.Controller.Type()), string(validated.Controller.ID()), validated.Environment, sourceJSON, capabilitiesJSON, formatStoredTime(validated.ObservedAt), boolInt(validated.IsStale))
			if err != nil {
				return RemoteJobProjection{}, fmt.Errorf("insert remote job projection: %w", err)
			}
			return validated, nil
		}
		if err != nil {
			return RemoteJobProjection{}, err
		}
		if err := validateRemoteJobProjectionIdentity(stored, validated); err != nil {
			return RemoteJobProjection{}, err
		}
		if validated.ObservedAt.Before(stored.ObservedAt) {
			return stored, nil
		}
		_, err = connection.ExecContext(ctx, `
UPDATE local_remote_job_projections SET job_phase = ?, command_state = ?, exit_code = ?, final_event_sequence = ?,
 output_complete = ?, output_truncated = ?, output_unavailable_reason = ?, teardown_state = ?, teardown_reason = ?,
 capabilities_json = ?, observed_at = ?, is_stale = ? WHERE job_id = ?
`, string(validated.Phase), nullableCommandState(validated.CommandState), nullableInt(validated.ExitCode), nullableInt64(validated.FinalEventSequence), boolInt(validated.OutputComplete), boolInt(validated.OutputTruncated), validated.OutputUnavailableReason, string(validated.TeardownState), validated.TeardownReason, capabilitiesJSON, formatStoredTime(validated.ObservedAt), boolInt(validated.IsStale), string(validated.JobID))
		if err != nil {
			return RemoteJobProjection{}, fmt.Errorf("update remote job projection: %w", err)
		}
		return validated, nil
	})
}

func (s *AuthorityStore) GetRemoteJobProjection(ctx context.Context, id domain.JobID) (RemoteJobProjection, error) {
	validated, err := domain.NewJobID(string(id))
	if err != nil {
		return RemoteJobProjection{}, err
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (RemoteJobProjection, error) {
		return readRemoteJobProjectionOnConnection(ctx, connection, validated)
	})
}

func (s *AuthorityStore) MarkRemoteJobProjectionStale(ctx context.Context, id domain.JobID) error {
	validated, err := domain.NewJobID(string(id))
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		result, err := connection.ExecContext(ctx, `UPDATE local_remote_job_projections SET is_stale = 1 WHERE job_id = ?`, string(validated))
		if err != nil {
			return struct{}{}, fmt.Errorf("mark remote job projection stale: %w", err)
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return struct{}{}, ErrRemoteProjectionNotFound
		}
		return struct{}{}, nil
	})
	return err
}

func validateRemoteJobProjection(input RemoteJobProjection) (RemoteJobProjection, string, string, error) {
	jobID, err := domain.NewJobID(string(input.JobID))
	if err != nil {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: job ID: %v", ErrRemoteProjectionInvalid, err)
	}
	sessionID, err := domain.NewSessionID(string(input.SessionID))
	if err != nil {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: session ID: %v", ErrRemoteProjectionInvalid, err)
	}
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: command ID: %v", ErrRemoteProjectionInvalid, err)
	}
	target, err := domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil || target.Kind() != domain.TargetKindRemote {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: remote target", ErrRemoteProjectionInvalid)
	}
	controller, err := domain.NewControllerIdentity(input.Controller.Type(), input.Controller.ID())
	if err != nil {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: controller: %v", ErrRemoteProjectionInvalid, err)
	}
	if !input.Phase.Valid() || !input.TeardownState.Valid() || strings.TrimSpace(input.Environment) == "" || input.ObservedAt.IsZero() {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: job fields", ErrRemoteProjectionInvalid)
	}
	if input.CommandState != nil && !input.CommandState.Valid() {
		return RemoteJobProjection{}, "", "", fmt.Errorf("%w: command state", ErrRemoteProjectionInvalid)
	}
	sourceJSON, err := marshalProjectionSource(input.Source, "")
	if err != nil {
		return RemoteJobProjection{}, "", "", err
	}
	capabilitiesJSON, err := marshalRemoteCapabilities(input.Capabilities)
	if err != nil {
		return RemoteJobProjection{}, "", "", err
	}
	input.JobID, input.SessionID, input.CommandID, input.Target, input.Controller = jobID, sessionID, commandID, target, controller
	input.ObservedAt = input.ObservedAt.UTC()
	input.Capabilities = cloneRemoteCapabilities(input.Capabilities, capabilitiesJSON)
	return input, sourceJSON, capabilitiesJSON, nil
}

func validateRemoteJobProjectionIdentity(stored, incoming RemoteJobProjection) error {
	if stored.JobID != incoming.JobID || stored.SessionID != incoming.SessionID || stored.CommandID != incoming.CommandID || stored.Target != incoming.Target || stored.Controller != incoming.Controller || stored.Environment != incoming.Environment || !sourcesEqual(stored.Source, incoming.Source) {
		return fmt.Errorf("%w: job %s", ErrRemoteProjectionConflict, incoming.JobID)
	}
	return nil
}

func readRemoteJobProjectionOnConnection(ctx context.Context, connection *sql.Conn, id domain.JobID) (RemoteJobProjection, error) {
	var result RemoteJobProjection
	var sessionID, commandID, phase, targetKind, targetProfile, controllerType, controllerID, environment, sourceJSON, capabilitiesJSON, observed, teardownState, teardownReason, outputReason string
	var outputComplete, outputTruncated, stale int64
	var exitCode, finalSequence sql.NullInt64
	var commandStateSQL sql.NullString
	err := connection.QueryRowContext(ctx, `
SELECT session_id, command_id, job_phase, command_state, exit_code, final_event_sequence,
 output_complete, output_truncated, output_unavailable_reason, teardown_state, teardown_reason,
 target_kind, target_profile, controller_type, controller_id, environment, source_json,
 capabilities_json, observed_at, is_stale
FROM local_remote_job_projections WHERE job_id = ?
`, string(id)).Scan(&sessionID, &commandID, &phase, &commandStateSQL, &exitCode, &finalSequence, &outputComplete, &outputTruncated, &outputReason, &teardownState, &teardownReason, &targetKind, &targetProfile, &controllerType, &controllerID, &environment, &sourceJSON, &capabilitiesJSON, &observed, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteJobProjection{}, ErrRemoteProjectionNotFound
	}
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("read remote job projection: %w", err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: target: %v", ErrRemoteProjectionInvalid, err)
	}
	session, err := domain.NewSessionID(sessionID)
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: session ID: %v", ErrRemoteProjectionInvalid, err)
	}
	command, err := domain.NewCommandID(commandID)
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: command ID: %v", ErrRemoteProjectionInvalid, err)
	}
	controllerIDValue, err := domain.NewControllerID(controllerID)
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: controller ID: %v", ErrRemoteProjectionInvalid, err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerType(controllerType), controllerIDValue)
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: controller: %v", ErrRemoteProjectionInvalid, err)
	}
	source, err := unmarshalProjectionSource(sourceJSON)
	if err != nil {
		return RemoteJobProjection{}, err
	}
	var capabilities RemoteCapabilities
	if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: capabilities: %v", ErrRemoteProjectionInvalid, err)
	}
	observedAt, err := parseStoredTime(observed)
	if err != nil {
		return RemoteJobProjection{}, fmt.Errorf("%w: observed_at: %v", ErrRemoteProjectionInvalid, err)
	}
	if commandStateSQL.Valid {
		state := domain.CommandState(commandStateSQL.String)
		result.CommandState = &state
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		result.ExitCode = &value
	}
	if finalSequence.Valid {
		value := finalSequence.Int64
		result.FinalEventSequence = &value
	}
	result = RemoteJobProjection{JobID: id, SessionID: session, CommandID: command, Phase: JobPhase(phase), CommandState: result.CommandState, ExitCode: result.ExitCode, FinalEventSequence: result.FinalEventSequence, OutputComplete: outputComplete != 0, OutputTruncated: outputTruncated != 0, OutputUnavailableReason: outputReason, TeardownState: JobTeardownState(teardownState), TeardownReason: teardownReason, Target: target, Controller: controller, Environment: environment, Source: source, Capabilities: capabilities, ObservedAt: observedAt, IsStale: stale != 0}
	if _, _, _, err := validateRemoteJobProjection(result); err != nil {
		return RemoteJobProjection{}, err
	}
	return result, nil
}

func nullableCommandState(value *domain.CommandState) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
