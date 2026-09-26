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

var (
	ErrInvalidJob        = errors.New("invalid job")
	ErrJobNotFound       = errors.New("job not found")
	ErrJobExists         = errors.New("job already exists")
	ErrJobPayloadCorrupt = errors.New("job payload is corrupt")
)

const runJobOperation = "run"

// JobPhase is the durable one-off coordinator checkpoint. P024 starts every
// accepted job before session creation; later phases advance it after each
// underlying authority transaction.
type JobPhase string

const (
	JobPhaseCreatingSession  JobPhase = "creating_session"
	JobPhaseAcceptingCommand JobPhase = "accepting_command"
	JobPhaseAwaitingCommand  JobPhase = "awaiting_command"
	JobPhaseClosingSession   JobPhase = "closing_session"
	JobPhaseComplete         JobPhase = "complete"
	JobPhaseFailed           JobPhase = "failed"
	JobPhaseLost             JobPhase = "lost"
)

func (p JobPhase) Valid() bool {
	switch p {
	case JobPhaseCreatingSession, JobPhaseAcceptingCommand, JobPhaseAwaitingCommand,
		JobPhaseClosingSession, JobPhaseComplete, JobPhaseFailed, JobPhaseLost:
		return true
	default:
		return false
	}
}

// JobTeardownState describes the one-off session teardown checkpoint.
type JobTeardownState string

const (
	JobTeardownPending    JobTeardownState = "pending"
	JobTeardownClosed     JobTeardownState = "closed"
	JobTeardownFailed     JobTeardownState = "failed"
	JobTeardownLost       JobTeardownState = "lost"
	JobTeardownNotCreated JobTeardownState = "not_created"
)

func (s JobTeardownState) Valid() bool {
	switch s {
	case JobTeardownPending, JobTeardownClosed, JobTeardownFailed, JobTeardownLost, JobTeardownNotCreated:
		return true
	default:
		return false
	}
}

// JobAcceptance is the immutable one-off request accepted before any runtime
// starts. CanonicalPayload is the exact canonical JSON used by RequestHash.
type JobAcceptance struct {
	JobID                domain.JobID
	SessionID            domain.SessionID
	CommandID            domain.CommandID
	Controller           domain.ControllerIdentity
	IdempotencyKey       string
	RequestHash          domain.CanonicalHash
	Environment          string
	Target               domain.ExecutionTarget
	Source               domain.Source
	Script               string
	CanonicalPayload     []byte
	IdempotencyRetention time.Duration
}

// JobRecord is the durable one-off resource snapshot. The command/session IDs
// remain stable even while the coordinator has not yet created those rows.
type JobRecord struct {
	JobID                   domain.JobID
	SessionID               domain.SessionID
	CommandID               domain.CommandID
	Controller              domain.ControllerIdentity
	Environment             string
	Target                  domain.ExecutionTarget
	Source                  domain.Source
	RequestHash             domain.CanonicalHash
	IdempotencyKey          string
	CanonicalPayload        []byte
	ScriptBytes             []byte
	ScriptSHA256            []byte
	Phase                   JobPhase
	CommandState            *domain.CommandState
	ExitCode                *int
	FinalEventSequence      *int64
	OutputTruncated         bool
	OutputComplete          bool
	OutputUnavailableReason string
	TeardownState           JobTeardownState
	TeardownReason          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// AcceptJob atomically binds one run key/hash to a stable job/session/command
// tuple and stores the complete canonical request before runtime work. A
// same-key/same-payload retry returns the original row without another job;
// changed payloads return ErrIdempotencyConflict.
func (s *AuthorityStore) AcceptJob(ctx context.Context, input JobAcceptance) (record JobRecord, duplicate bool, err error) {
	validated, err := validateJobAcceptance(input)
	if err != nil {
		return JobRecord{}, false, err
	}
	now := s.now().UTC()
	record, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (JobRecord, error) {
		existing, found, err := lookupIdempotencyOnConnection(ctx, connection, validated.Controller, runJobOperation, validated.IdempotencyKey, now)
		if err != nil {
			return JobRecord{}, err
		}
		if found {
			if domain.CompareIdempotency(existing.Hash, validated.RequestHash) == domain.IdempotencyConflict {
				return JobRecord{}, ErrIdempotencyConflict
			}
			existingID, err := domain.NewJobID(existing.ResourceID)
			if err != nil {
				return JobRecord{}, fmt.Errorf("read idempotent job ID: %w", err)
			}
			record, err := readJobOnConnection(ctx, connection, existingID)
			if err != nil {
				return JobRecord{}, fmt.Errorf("read idempotent job: %w", err)
			}
			duplicate = true
			return record, nil
		}
		var exists int
		if err := connection.QueryRowContext(ctx, "SELECT 1 FROM exec_jobs WHERE job_id = ?", string(validated.JobID)).Scan(&exists); err == nil {
			return JobRecord{}, ErrJobExists
		} else if !errors.Is(err, sql.ErrNoRows) {
			return JobRecord{}, fmt.Errorf("check job identity: %w", err)
		}
		if err := insertJobOnConnection(ctx, connection, validated, now); err != nil {
			return JobRecord{}, err
		}
		return readJobOnConnection(ctx, connection, validated.JobID)
	})
	if err != nil {
		return JobRecord{}, false, err
	}
	return record, duplicate, nil
}

// GetJob reads and validates one durable job row, including its canonical
// request and immutable exact script bytes.
func (s *AuthorityStore) GetJob(ctx context.Context, id domain.JobID) (JobRecord, error) {
	validatedID, err := domain.NewJobID(string(id))
	if err != nil {
		return JobRecord{}, err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return JobRecord{}, fmt.Errorf("acquire job connection: %w", err)
	}
	defer connection.Close()
	return readJobOnConnection(ctx, connection, validatedID)
}

func validateJobAcceptance(input JobAcceptance) (JobAcceptance, error) {
	jobID, err := domain.NewJobID(string(input.JobID))
	if err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: job ID: %v", ErrInvalidJob, err)
	}
	sessionID, err := domain.NewSessionID(string(input.SessionID))
	if err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: session ID: %v", ErrInvalidJob, err)
	}
	commandID, err := domain.NewCommandID(string(input.CommandID))
	if err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: command ID: %v", ErrInvalidJob, err)
	}
	controller, err := validateController(input.Controller)
	if err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: controller: %v", ErrInvalidJob, err)
	}
	target, err := domain.NewExecutionTarget(input.Target.Kind(), input.Target.Profile())
	if err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: target: %v", ErrInvalidJob, err)
	}
	source, err := normalizeSource(input.Source)
	if err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: source: %v", ErrInvalidJob, err)
	}
	if strings.TrimSpace(input.Environment) == "" {
		return JobAcceptance{}, fmt.Errorf("%w: environment is empty", ErrInvalidJob)
	}
	if err := domain.ValidateScriptUTF8(input.Script); err != nil {
		return JobAcceptance{}, fmt.Errorf("%w: script: %v", ErrInvalidJob, err)
	}
	if _, _, err := validateIdempotencyOperationKey(runJobOperation, input.IdempotencyKey); err != nil {
		return JobAcceptance{}, err
	}
	retention := input.IdempotencyRetention
	if retention == 0 {
		retention = DefaultSessionIdempotencyRetention
	}
	if retention < 0 {
		return JobAcceptance{}, fmt.Errorf("%w: idempotency retention must not be negative", ErrInvalidJob)
	}
	payload, hash, err := validateRunPayload(input.CanonicalPayload, input.Script, input.Environment, target, source, input.RequestHash)
	if err != nil {
		return JobAcceptance{}, err
	}
	return JobAcceptance{JobID: jobID, SessionID: sessionID, CommandID: commandID, Controller: controller,
		IdempotencyKey: input.IdempotencyKey, RequestHash: hash, Environment: strings.TrimSpace(input.Environment),
		Target: target, Source: source, Script: input.Script, CanonicalPayload: payload, IdempotencyRetention: retention}, nil
}

func validateRunPayload(raw []byte, script, environment string, target domain.ExecutionTarget, source domain.Source, requestHash domain.CanonicalHash) ([]byte, domain.CanonicalHash, error) {
	if err := domain.ValidateSerializedRequest(raw); err != nil || len(raw) == 0 {
		if err == nil {
			err = ErrInvalidJob
		}
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: canonical payload: %v", ErrInvalidJob, err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON(runJobOperation, raw, domain.CanonicalizationOptions{})
	if err != nil {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: canonical payload: %v", ErrInvalidJob, err)
	}
	if !bytes.Equal(canonical, raw) {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: payload is not canonical", ErrInvalidJob)
	}
	hash, err := domain.HashMutationRequestJSON(runJobOperation, raw, domain.CanonicalizationOptions{})
	if err != nil || domain.CompareIdempotency(hash, requestHash) == domain.IdempotencyConflict {
		if err == nil {
			err = fmt.Errorf("request hash does not match canonical payload")
		}
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: %v", ErrInvalidJob, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: canonical payload object: %v", ErrInvalidJob, err)
	}
	var payloadScript string
	if value, ok := object["script"]; !ok || json.Unmarshal(value, &payloadScript) != nil || payloadScript != script {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: script differs from canonical payload", ErrInvalidJob)
	}
	var payloadEnvironment string
	if value, ok := object["environment"]; !ok || json.Unmarshal(value, &payloadEnvironment) != nil || payloadEnvironment != environment {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: environment differs from canonical payload", ErrInvalidJob)
	}
	var payloadTarget struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	}
	value, ok := object["execution_target"]
	if !ok || json.Unmarshal(value, &payloadTarget) != nil || payloadTarget.Kind != string(target.Kind()) || payloadTarget.Profile != target.Profile() {
		return nil, domain.CanonicalHash{}, fmt.Errorf("%w: target differs from canonical payload", ErrInvalidJob)
	}
	if value, ok := object["source"]; ok {
		var payloadSource struct {
			Mode              string `json:"mode"`
			RepositoryAlias   string `json:"repository_alias"`
			RequestedRevision string `json:"requested_revision"`
			Path              string `json:"path"`
		}
		if json.Unmarshal(value, &payloadSource) != nil || payloadSource.Mode != string(source.Mode()) || payloadSource.RepositoryAlias != source.RepositoryAlias() || payloadSource.RequestedRevision != source.RequestedRevision() || payloadSource.Path != source.Path() {
			return nil, domain.CanonicalHash{}, fmt.Errorf("%w: source differs from canonical payload", ErrInvalidJob)
		}
	}
	return append([]byte(nil), canonical...), hash, nil
}

func insertJobOnConnection(ctx context.Context, connection *sql.Conn, input JobAcceptance, now time.Time) error {
	scriptBytes := []byte(input.Script)
	scriptHash := sha256.Sum256(scriptBytes)
	if _, err := connection.ExecContext(ctx, `
INSERT INTO exec_jobs (
    job_id, session_id, command_id, controller_type, controller_id,
    target_kind, target_profile, environment,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    request_hash_version, request_hash, idempotency_key, payload_json,
    script_bytes, script_sha256, phase, teardown_state, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(input.JobID), string(input.SessionID), string(input.CommandID), string(input.Controller.Type()), string(input.Controller.ID()),
		string(input.Target.Kind()), input.Target.Profile(), input.Environment,
		string(input.Source.Mode()), input.Source.RepositoryAlias(), input.Source.RequestedRevision(), input.Source.Path(),
		input.RequestHash.Version(), input.RequestHash.SHA256(), input.IdempotencyKey, input.CanonicalPayload,
		scriptBytes, scriptHash[:], string(JobPhaseCreatingSession), string(JobTeardownPending), formatStoredTime(now), formatStoredTime(now)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrJobExists
		}
		return fmt.Errorf("insert job: %w", err)
	}
	idempotency, err := validateIdempotencyInput(input.Controller, runJobOperation, input.IdempotencyKey, input.RequestHash, string(input.JobID), input.IdempotencyRetention)
	if err != nil {
		return err
	}
	idempotency.CreatedAt, idempotency.ExpiresAt = now, now.Add(input.IdempotencyRetention)
	if err := recordIdempotencyOnConnection(ctx, connection, idempotency); err != nil {
		return err
	}
	return nil
}

func readJobOnConnection(ctx context.Context, connection *sql.Conn, id domain.JobID) (JobRecord, error) {
	var record JobRecord
	var jobID, sessionID, commandID, controllerType, controllerID string
	var targetKind, targetProfile, environment string
	var sourceMode, repositoryAlias, requestedRevision, sourcePath string
	var requestHashVersion int
	var requestHash, payload, scriptBytes, scriptHash []byte
	var idempotencyKey, phase, teardownState, outputUnavailableReason, teardownReason string
	var commandState sql.NullString
	var exitCode sql.NullInt64
	var finalSequence sql.NullInt64
	var outputTruncated, outputComplete int64
	var createdAt, updatedAt string
	if err := connection.QueryRowContext(ctx, `
SELECT job_id, session_id, command_id, controller_type, controller_id,
       target_kind, target_profile, environment,
       source_mode, source_repository_alias, source_requested_revision, source_path,
       request_hash_version, request_hash, idempotency_key, payload_json,
       script_bytes, script_sha256, phase, command_state, exit_code, final_event_sequence,
       output_truncated, output_complete, output_unavailable_reason,
       teardown_state, teardown_reason, created_at, updated_at
FROM exec_jobs WHERE job_id = ?
`, string(id)).Scan(&jobID, &sessionID, &commandID, &controllerType, &controllerID,
		&targetKind, &targetProfile, &environment, &sourceMode, &repositoryAlias, &requestedRevision, &sourcePath,
		&requestHashVersion, &requestHash, &idempotencyKey, &payload, &scriptBytes, &scriptHash, &phase, &commandState,
		&exitCode, &finalSequence, &outputTruncated, &outputComplete, &outputUnavailableReason, &teardownState, &teardownReason, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobRecord{}, ErrJobNotFound
		}
		return JobRecord{}, fmt.Errorf("read job: %w", err)
	}
	validatedID, err := domain.NewJobID(jobID)
	if err != nil || validatedID != id {
		return JobRecord{}, fmt.Errorf("%w: job ID", ErrJobPayloadCorrupt)
	}
	record.SessionID, err = domain.NewSessionID(sessionID)
	if err != nil {
		return JobRecord{}, fmt.Errorf("%w: session ID", ErrJobPayloadCorrupt)
	}
	record.CommandID, err = domain.NewCommandID(commandID)
	if err != nil {
		return JobRecord{}, fmt.Errorf("%w: command ID", ErrJobPayloadCorrupt)
	}
	record.Controller, err = domain.NewControllerIdentity(domain.ControllerType(controllerType), domain.ControllerID(controllerID))
	if err != nil {
		return JobRecord{}, fmt.Errorf("%w: controller", ErrJobPayloadCorrupt)
	}
	record.Target, err = domain.NewExecutionTarget(domain.TargetKind(targetKind), targetProfile)
	if err != nil {
		return JobRecord{}, fmt.Errorf("%w: target", ErrJobPayloadCorrupt)
	}
	record.Source, err = decodeSource(domain.SourceMode(sourceMode), repositoryAlias, requestedRevision, sourcePath)
	if err != nil {
		return JobRecord{}, fmt.Errorf("%w: source: %v", ErrJobPayloadCorrupt, err)
	}
	record.RequestHash, err = domain.NewCanonicalHash(uint16(requestHashVersion), requestHash)
	if err != nil {
		return JobRecord{}, fmt.Errorf("%w: request hash", ErrJobPayloadCorrupt)
	}
	if err := domain.ValidateScriptUTF8(string(scriptBytes)); err != nil {
		return JobRecord{}, fmt.Errorf("%w: script: %v", ErrJobPayloadCorrupt, err)
	}
	computedScriptHash := sha256.Sum256(scriptBytes)
	if !bytes.Equal(scriptHash, computedScriptHash[:]) {
		return JobRecord{}, fmt.Errorf("%w: script hash mismatch", ErrJobPayloadCorrupt)
	}
	if _, _, err := validateIdempotencyOperationKey(runJobOperation, idempotencyKey); err != nil {
		return JobRecord{}, fmt.Errorf("%w: idempotency key: %v", ErrJobPayloadCorrupt, err)
	}
	if _, _, err := validateRunPayload(payload, string(scriptBytes), environment, record.Target, record.Source, record.RequestHash); err != nil {
		return JobRecord{}, fmt.Errorf("%w: %v", ErrJobPayloadCorrupt, err)
	}
	record.JobID, record.Environment, record.IdempotencyKey = validatedID, environment, idempotencyKey
	record.CanonicalPayload, record.ScriptBytes, record.ScriptSHA256 = append([]byte(nil), payload...), append([]byte(nil), scriptBytes...), append([]byte(nil), scriptHash...)
	record.Phase = JobPhase(phase)
	if !record.Phase.Valid() || !JobTeardownState(teardownState).Valid() || outputTruncated < 0 || outputTruncated > 1 || outputComplete < 0 || outputComplete > 1 {
		return JobRecord{}, fmt.Errorf("%w: job state metadata", ErrJobPayloadCorrupt)
	}
	record.TeardownState, record.TeardownReason = JobTeardownState(teardownState), teardownReason
	record.OutputTruncated, record.OutputComplete, record.OutputUnavailableReason = outputTruncated == 1, outputComplete == 1, outputUnavailableReason
	if commandState.Valid {
		state := domain.CommandState(commandState.String)
		if !state.Valid() {
			return JobRecord{}, fmt.Errorf("%w: command state", ErrJobPayloadCorrupt)
		}
		record.CommandState = &state
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		record.ExitCode = &value
	}
	if finalSequence.Valid {
		value := finalSequence.Int64
		record.FinalEventSequence = &value
	}
	if record.CreatedAt, err = parseStoredTime(createdAt); err != nil {
		return JobRecord{}, fmt.Errorf("%w: created_at", ErrJobPayloadCorrupt)
	}
	if record.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
		return JobRecord{}, fmt.Errorf("%w: updated_at", ErrJobPayloadCorrupt)
	}
	return record, nil
}
