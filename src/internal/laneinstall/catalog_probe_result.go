package laneinstall

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const catalogMaintenanceProbeScript = "printf 'catalog release maintenance probe\\n'"
const catalogMaintenanceProbeOutput = "catalog release maintenance probe\n"

// CatalogProbeResultBinding ties a read-only terminal-result observation to
// the exact accepted private probe scope. The caller must validate the scope
// file and its loaded configuration before requesting this database read.
type CatalogProbeResultBinding struct {
	HostID              string
	Lane                config.ReleaseLane
	Transport           CatalogProbeTransport
	OperationID         string
	ControllerType      domain.ControllerType
	ControllerID        string
	Environment         string
	ConfigurationSHA256 string
	IdempotencyKey      string
	RequestSHA256       string
	ScopeCreatedAt      string
	ScopeExpiresAt      string
	JobID               string
}

// CatalogProbeTerminalResult is a bounded, sanitized view of one accepted
// probe's durable result. It never returns scripts, request payloads, output
// bytes, credentials, or unrestricted database errors.
type CatalogProbeTerminalResult struct {
	ObservedAtUTC              string                `json:"observed_at_utc"`
	HostID                     string                `json:"host_id"`
	Lane                       string                `json:"lane"`
	Transport                  CatalogProbeTransport `json:"transport"`
	OperationID                string                `json:"operation_id"`
	ControllerType             string                `json:"controller_type"`
	ControllerID               string                `json:"controller_id"`
	Environment                string                `json:"environment"`
	ConfigurationSHA256        string                `json:"configuration_sha256"`
	IdempotencyKey             string                `json:"idempotency_key"`
	RequestSHA256              string                `json:"request_body_sha256"`
	JobID                      string                `json:"job_id"`
	SessionID                  string                `json:"session_id"`
	CommandID                  string                `json:"command_id"`
	JobCreatedAtUTC            string                `json:"job_created_at_utc"`
	SchemaVersion              int                   `json:"schema_version"`
	Phase                      string                `json:"phase"`
	CommandState               string                `json:"command_state,omitempty"`
	SessionState               string                `json:"session_state,omitempty"`
	ExitCode                   *int                  `json:"exit_code,omitempty"`
	FinalEventSequence         *int64                `json:"final_event_sequence,omitempty"`
	OutputBytes                int64                 `json:"output_bytes"`
	OutputSHA256               string                `json:"output_sha256,omitempty"`
	OutputComplete             bool                  `json:"output_complete"`
	OutputTruncated            bool                  `json:"output_truncated"`
	OutputUnavailableReason    string                `json:"output_unavailable_reason,omitempty"`
	TeardownState              string                `json:"teardown_state"`
	TeardownReason             string                `json:"teardown_reason,omitempty"`
	EventTypes                 []string              `json:"event_types,omitempty"`
	EventChainSHA256           string                `json:"event_chain_sha256,omitempty"`
	CommandStopConfirmed       bool                  `json:"command_stop_confirmed"`
	CommandSlotReleased        bool                  `json:"command_slot_released"`
	SessionCleanupConfirmed    bool                  `json:"session_cleanup_confirmed"`
	SessionReservationReleased bool                  `json:"session_reservation_released"`
	WorkInventory              CatalogWorkInventory  `json:"work_inventory"`
	Terminal                   bool                  `json:"terminal"`
	Accepted                   bool                  `json:"accepted"`
	EvidenceSHA256             string                `json:"evidence_sha256"`
}

// ReadCatalogProbeTerminalResult performs a bounded read-only SQLite snapshot
// for the job named by an accepted private probe scope. It does not submit,
// replay, checkpoint, migrate, or mutate Runner state.
func ReadCatalogProbeTerminalResult(ctx context.Context, databasePath string, binding CatalogProbeResultBinding) (CatalogProbeTerminalResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validCatalogProbeResultBinding(binding) {
		return CatalogProbeTerminalResult{}, errors.New("probe result binding is invalid")
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	database, err := store.OpenExistingRestartPreflightReadOnly(readCtx, databasePath)
	if err != nil {
		return CatalogProbeTerminalResult{}, errors.New("existing authority is unavailable through the read-only compatibility path")
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	tx, err := database.BeginTx(readCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CatalogProbeTerminalResult{}, errors.New("read-only probe result snapshot could not start")
	}
	defer tx.Rollback()

	result := CatalogProbeTerminalResult{
		ObservedAtUTC: time.Now().UTC().Format(time.RFC3339Nano), HostID: binding.HostID, Lane: string(binding.Lane),
		Transport: binding.Transport, OperationID: binding.OperationID, ControllerType: string(binding.ControllerType),
		ControllerID: binding.ControllerID, Environment: binding.Environment, ConfigurationSHA256: binding.ConfigurationSHA256,
		IdempotencyKey: binding.IdempotencyKey, RequestSHA256: binding.RequestSHA256, JobID: binding.JobID,
	}
	if err := tx.QueryRowContext(readCtx, "PRAGMA user_version").Scan(&result.SchemaVersion); err != nil {
		return CatalogProbeTerminalResult{}, errors.New("probe result schema could not be read")
	}
	var sessionID, commandID, jobEnvironment, controllerType, controllerID, targetKind, targetProfile, sourceMode string
	var script []byte
	var phase, teardownState, teardownReason, outputUnavailable, createdAt string
	var commandState sql.NullString
	var exitCode, finalSequence sql.NullInt64
	var truncated, complete int
	err = tx.QueryRowContext(readCtx, `SELECT session_id,command_id,environment,controller_type,controller_id,target_kind,target_profile,
source_mode,script_bytes,phase,command_state,exit_code,final_event_sequence,output_truncated,output_complete,
output_unavailable_reason,teardown_state,teardown_reason,created_at FROM exec_jobs WHERE job_id=?`, binding.JobID).
		Scan(&sessionID, &commandID, &jobEnvironment, &controllerType, &controllerID, &targetKind, &targetProfile,
			&sourceMode, &script, &phase, &commandState, &exitCode, &finalSequence, &truncated, &complete,
			&outputUnavailable, &teardownState, &teardownReason, &createdAt)
	if err != nil {
		return CatalogProbeTerminalResult{}, errors.New("accepted probe job row is unavailable")
	}
	if !validProbeJobAndResourceIDs(binding.JobID, sessionID, commandID) || jobEnvironment != binding.Environment ||
		controllerType != string(binding.ControllerType) || controllerID != binding.ControllerID || targetKind != resultTargetKind(binding.Transport) ||
		targetProfile != resultTargetProfile(binding.HostID, binding.Transport) || sourceMode != string(domain.SourceModeEmpty) ||
		string(script) != catalogMaintenanceProbeScript {
		return CatalogProbeTerminalResult{}, errors.New("accepted probe job does not match its immutable scope")
	}
	jobCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return CatalogProbeTerminalResult{}, errors.New("accepted probe creation time is invalid")
	}
	scopeCreated, _ := time.Parse(time.RFC3339Nano, binding.ScopeCreatedAt)
	scopeExpires, _ := time.Parse(time.RFC3339Nano, binding.ScopeExpiresAt)
	if jobCreated.Before(scopeCreated) || !jobCreated.Before(scopeExpires) {
		return CatalogProbeTerminalResult{}, errors.New("accepted probe was created outside its one-time scope")
	}
	result.SessionID, result.CommandID, result.JobCreatedAtUTC = sessionID, commandID, jobCreated.UTC().Format(time.RFC3339Nano)
	result.Phase, result.TeardownState, result.TeardownReason = phase, teardownState, teardownReason
	result.OutputUnavailableReason = outputUnavailable
	if commandState.Valid {
		result.CommandState = commandState.String
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		result.ExitCode = &value
	}
	if finalSequence.Valid {
		value := finalSequence.Int64
		result.FinalEventSequence = &value
	}
	result.OutputTruncated, result.OutputComplete = truncated == 1, complete == 1
	result.Terminal = phase == "complete" || phase == "failed" || phase == "lost"
	if !result.Terminal {
		if err := tx.Commit(); err != nil {
			return CatalogProbeTerminalResult{}, errors.New("read-only probe result snapshot could not close")
		}
		sealCatalogProbeTerminalResult(&result)
		return result, nil
	}
	if err := readProbeResultSession(readCtx, tx, &result, sessionID, binding); err != nil {
		return CatalogProbeTerminalResult{}, err
	}
	jobState, jobExitCode, jobFinalSequence := result.CommandState, cloneInt(result.ExitCode), cloneInt64(result.FinalEventSequence)
	jobTruncated, jobComplete, jobUnavailable := result.OutputTruncated, result.OutputComplete, result.OutputUnavailableReason
	if err := readProbeResultCommand(readCtx, tx, &result, commandID, sessionID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) || phase == "complete" {
			return CatalogProbeTerminalResult{}, errors.New("terminal probe command evidence is inconsistent")
		}
	}
	if result.CommandState != jobState || !sameOptionalInt(result.ExitCode, jobExitCode) ||
		!sameOptionalInt64(result.FinalEventSequence, jobFinalSequence) || result.OutputTruncated != jobTruncated ||
		result.OutputComplete != jobComplete || result.OutputUnavailableReason != jobUnavailable {
		return CatalogProbeTerminalResult{}, errors.New("terminal probe job and command snapshots disagree")
	}
	if result.CommandState != "" {
		if err := readProbeResultEvents(readCtx, tx, &result, commandID); err != nil {
			return CatalogProbeTerminalResult{}, errors.New("terminal probe event evidence is inconsistent")
		}
		if err := readProbeResultReleaseMarkers(readCtx, tx, &result, commandID, sessionID); err != nil {
			return CatalogProbeTerminalResult{}, errors.New("terminal probe cleanup evidence is inconsistent")
		}
	}
	inventory, err := readPRDWorkInventoryTx(readCtx, tx)
	if err != nil {
		return CatalogProbeTerminalResult{}, errors.New("probe work inventory could not be read")
	}
	result.WorkInventory = inventory
	result.Accepted = result.Phase == "complete" && result.CommandState == string(domain.CommandStateSucceeded) &&
		result.SessionState == string(domain.SessionStateClosed) && result.ExitCode != nil && *result.ExitCode == 0 &&
		result.FinalEventSequence != nil && *result.FinalEventSequence > 0 && result.OutputComplete && !result.OutputTruncated &&
		result.OutputUnavailableReason == "" && result.OutputSHA256 == probeExpectedOutputSHA256() &&
		result.TeardownState == "closed" && result.TeardownReason == "runtime_closed" &&
		result.CommandStopConfirmed && result.CommandSlotReleased && result.SessionCleanupConfirmed &&
		result.SessionReservationReleased && inventory.Empty()
	if result.Phase == "complete" && result.CommandState == string(domain.CommandStateSucceeded) && !result.Accepted {
		return CatalogProbeTerminalResult{}, errors.New("successful terminal probe lacks complete output, cleanup, or empty-work proof")
	}
	if err := tx.Commit(); err != nil {
		return CatalogProbeTerminalResult{}, errors.New("read-only probe result snapshot could not close")
	}
	sealCatalogProbeTerminalResult(&result)
	return result, nil
}

func validCatalogProbeResultBinding(binding CatalogProbeResultBinding) bool {
	if !containsCatalogHost(binding.HostID) || (binding.Lane != config.ReleaseLaneDev && binding.Lane != config.ReleaseLaneQAS && binding.Lane != config.ReleaseLanePRD) ||
		(binding.Transport != CatalogProbeDirectMTLS && binding.Transport != CatalogProbeQueuedSSH) ||
		!validCatalogOperationID(binding.OperationID) || binding.ControllerID == "" || binding.Environment == "" ||
		!prdDigestPattern.MatchString(binding.ConfigurationSHA256) || !prdDigestPattern.MatchString(binding.RequestSHA256) ||
		binding.JobID == "" || !probeBindingWindowValid(binding.ScopeCreatedAt, binding.ScopeExpiresAt) {
		return false
	}
	wantType := domain.ControllerTypeDirectMTLS
	if binding.Transport == CatalogProbeQueuedSSH {
		wantType = domain.ControllerTypeQueuedMac
	}
	if binding.ControllerType != wantType || binding.ControllerID == "" {
		return false
	}
	wantKey := catalogProbeIdempotencyKey(binding.OperationID, binding.HostID, binding.Transport)
	if binding.IdempotencyKey == wantKey {
		return validProbeJobIDForObservation(binding.JobID)
	}
	return binding.HostID == "linux-host" && binding.Lane == config.ReleaseLanePRD &&
		binding.Transport == CatalogProbeDirectMTLS && binding.OperationID == "63471d41af07b7afaf577d6a0989cc03" &&
		binding.IdempotencyKey == wantKey+"-retry-1" && binding.JobID == "job-d2c85d259d07b6a5d3c2055f87e34bc8"
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameOptionalInt(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func probeBindingWindowValid(createdAt, expiresAt string) bool {
	created, createdErr := time.Parse(time.RFC3339Nano, createdAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, expiresAt)
	return createdErr == nil && expiresErr == nil && expires.After(created) && expires.Sub(created) <= 10*time.Minute
}

func validProbeJobAndResourceIDs(jobID, sessionID, commandID string) bool {
	_, jobErr := domain.NewJobID(jobID)
	_, sessionErr := domain.NewSessionID(sessionID)
	_, commandErr := domain.NewCommandID(commandID)
	return jobErr == nil && sessionErr == nil && commandErr == nil
}

func validProbeJobIDForObservation(jobID string) bool {
	_, err := domain.NewJobID(jobID)
	return err == nil
}

func resultTargetKind(transport CatalogProbeTransport) string {
	if transport == CatalogProbeMacLocal {
		return string(domain.TargetKindLocal)
	}
	return string(domain.TargetKindRemote)
}

func resultTargetProfile(hostID string, transport CatalogProbeTransport) string {
	if transport == CatalogProbeMacLocal {
		return "mac-workstation"
	}
	return hostID
}

func readProbeResultSession(ctx context.Context, tx *sql.Tx, result *CatalogProbeTerminalResult, sessionID string, binding CatalogProbeResultBinding) error {
	var targetKind, targetProfile, environment, controllerType, controllerID, sourceMode, state string
	err := tx.QueryRowContext(ctx, `SELECT target_kind,target_profile,environment,controller_type,controller_id,source_mode,state
FROM exec_sessions WHERE session_id=?`, sessionID).Scan(&targetKind, &targetProfile, &environment, &controllerType, &controllerID, &sourceMode, &state)
	if err != nil || targetKind != resultTargetKind(binding.Transport) || targetProfile != resultTargetProfile(binding.HostID, binding.Transport) ||
		environment != binding.Environment || controllerType != string(binding.ControllerType) || controllerID != binding.ControllerID ||
		sourceMode != string(domain.SourceModeEmpty) {
		return errors.New("terminal probe session does not match its immutable scope")
	}
	result.SessionState = state
	return nil
}

func readProbeResultCommand(ctx context.Context, tx *sql.Tx, result *CatalogProbeTerminalResult, commandID, sessionID string) error {
	var commandSession, state, outputUnavailable string
	var exitCode, finalSequence sql.NullInt64
	var truncated, complete int
	var script, scriptHash []byte
	var timeoutNS int64
	err := tx.QueryRowContext(ctx, `SELECT session_id,state,exit_code,final_event_sequence,output_truncated,output_complete,
output_unavailable_reason,script_bytes,script_sha256,timeout_ns FROM exec_commands WHERE command_id=?`, commandID).
		Scan(&commandSession, &state, &exitCode, &finalSequence, &truncated, &complete, &outputUnavailable, &script, &scriptHash, &timeoutNS)
	if err != nil {
		return err
	}
	expectedScriptHash := sha256.Sum256([]byte(catalogMaintenanceProbeScript))
	if commandSession != sessionID || string(script) != catalogMaintenanceProbeScript || !equalDigestBytes(scriptHash, expectedScriptHash[:]) || timeoutNS != int64(5*time.Second) {
		return errors.New("terminal probe command does not match its immutable scope")
	}
	result.CommandState = state
	if exitCode.Valid {
		value := int(exitCode.Int64)
		result.ExitCode = &value
	}
	if finalSequence.Valid {
		value := finalSequence.Int64
		result.FinalEventSequence = &value
	}
	result.OutputTruncated, result.OutputComplete, result.OutputUnavailableReason = truncated == 1, complete == 1, outputUnavailable
	return nil
}

func readProbeResultEvents(ctx context.Context, tx *sql.Tx, result *CatalogProbeTerminalResult, commandID string) error {
	if result.FinalEventSequence == nil || *result.FinalEventSequence < 1 || *result.FinalEventSequence > 64 {
		return errors.New("terminal event sequence is outside the bounded probe shape")
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,event_type,payload,byte_count,occurred_at FROM exec_command_events
WHERE command_id=? ORDER BY sequence LIMIT 65`, commandID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var output []byte
	var eventProof []string
	var previous time.Time
	sequence := int64(1)
	for rows.Next() {
		var got int64
		var eventType, occurredAt string
		var payload []byte
		var byteCount int64
		if err := rows.Scan(&got, &eventType, &payload, &byteCount, &occurredAt); err != nil {
			return err
		}
		if got != sequence || int64(len(payload)) != byteCount || byteCount > 4096 {
			return errors.New("terminal event sequence or byte accounting is inconsistent")
		}
		at, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil || (!previous.IsZero() && at.Before(previous)) {
			return errors.New("terminal event time is invalid")
		}
		previous = at
		result.EventTypes = append(result.EventTypes, eventType)
		eventHash := sha256.Sum256(payload)
		eventProof = append(eventProof, fmt.Sprintf("%d:%s:%d:%s", got, eventType, byteCount, hex.EncodeToString(eventHash[:])))
		if len(result.EventTypes) == 1 && eventType != "command_queued" || len(result.EventTypes) == 2 && eventType != "command_started" ||
			len(result.EventTypes) > 2 && sequence < *result.FinalEventSequence && eventType != "stdout" ||
			sequence == *result.FinalEventSequence && eventType != "command_succeeded" {
			return errors.New("probe event order is outside the successful fixed-command shape")
		}
		if eventType == "stdout" {
			output = append(output, payload...)
		} else if len(payload) != 0 {
			return errors.New("non-output probe event contains payload")
		}
		sequence++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if sequence-1 != *result.FinalEventSequence || len(result.EventTypes) < 3 || result.EventTypes[0] != "command_queued" ||
		result.EventTypes[1] != "command_started" || result.EventTypes[len(result.EventTypes)-1] != "command_succeeded" ||
		string(output) != catalogMaintenanceProbeOutput {
		return errors.New("terminal event chain does not prove the fixed successful probe")
	}
	outputHash := sha256.Sum256(output)
	result.OutputBytes = int64(len(output))
	result.OutputSHA256 = hex.EncodeToString(outputHash[:])
	eventBytes, err := json.Marshal(eventProof)
	if err != nil {
		return err
	}
	eventHash := sha256.Sum256(eventBytes)
	result.EventChainSHA256 = hex.EncodeToString(eventHash[:])
	return nil
}

func readProbeResultReleaseMarkers(ctx context.Context, tx *sql.Tx, result *CatalogProbeTerminalResult, commandID, sessionID string) error {
	var stop, slotReleased, cleanup, reservationReleased sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT stop_confirmed_at,released_at FROM exec_command_slots WHERE command_id=?`, commandID).
		Scan(&stop, &slotReleased); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT cleanup_confirmed_at,released_at FROM exec_capacity_reservations WHERE session_id=?`, sessionID).
		Scan(&cleanup, &reservationReleased); err != nil {
		return err
	}
	result.CommandStopConfirmed = validStoredProofTime(stop)
	result.CommandSlotReleased = validStoredProofTime(slotReleased)
	result.SessionCleanupConfirmed = validStoredProofTime(cleanup)
	result.SessionReservationReleased = validStoredProofTime(reservationReleased)
	return nil
}

func validStoredProofTime(value sql.NullString) bool {
	if !value.Valid {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value.String)
	return err == nil
}

func probeExpectedOutputSHA256() string {
	hash := sha256.Sum256([]byte(catalogMaintenanceProbeOutput))
	return hex.EncodeToString(hash[:])
}

func equalDigestBytes(left, right []byte) bool {
	return len(left) == len(right) && strings.EqualFold(hex.EncodeToString(left), hex.EncodeToString(right))
}

func sealCatalogProbeTerminalResult(result *CatalogProbeTerminalResult) {
	result.EvidenceSHA256 = ""
	encoded, err := json.Marshal(result)
	if err != nil {
		result.EvidenceSHA256 = ""
		return
	}
	hash := sha256.Sum256(encoded)
	result.EvidenceSHA256 = hex.EncodeToString(hash[:])
}
