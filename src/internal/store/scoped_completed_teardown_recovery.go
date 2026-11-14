package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"time"
	"unicode/utf8"

	"remote-session-runner/src/internal/domain"
)

// ScopedCompletedTeardownRecoveryPlan is input to a separately guarded,
// stopped-Mac operator procedure, not authorization to release capacity. The
// in-memory records must not be serialized as a public report.
type ScopedCompletedTeardownRecoveryPlan struct {
	Recovery                 CompletedTeardownRecovery `json:"-"`
	PreservedInventorySHA256 string                    `json:"preserved_inventory_sha256"`
	PreservedSessionIDs      []domain.SessionID        `json:"preserved_session_ids"`
	Sessions                 []SessionRecord           `json:"-"`
}

// ReadScopedCompletedTeardownRecoveryPlan retains all other fully classified
// terminal holds. It never invokes runtime code, reads stored scripts/output,
// migrates schema or changes durable state.
func (s *AuthorityStore) ReadScopedCompletedTeardownRecoveryPlan(ctx context.Context, pair CompletedTeardownRecoveryPair) (ScopedCompletedTeardownRecoveryPlan, error) {
	if s == nil || s.db == nil {
		return ScopedCompletedTeardownRecoveryPlan{}, ErrCompletedTeardownRecoveryNotReleasable
	}
	if _, err := validateCompletedTeardownRecoveryPairs([]CompletedTeardownRecoveryPair{pair}); err != nil {
		return ScopedCompletedTeardownRecoveryPlan{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, scopedCompletedTeardownTimeout)
	defer cancel()
	return withStoreReadTransaction(ctx, s, func(ctx context.Context, conn *sql.Conn) (ScopedCompletedTeardownRecoveryPlan, error) {
		return readScopedCompletedTeardownRecoveryPlan(ctx, conn, pair)
	})
}

// ConfirmScopedCompletedTeardownRecovery is valid only after the operator has
// proved both Mac services stopped under their lifecycle lock, bound the
// selected release/configuration/backup, audited all ownership records, and
// durably proved the selected root absent and group empty without signalling.
// Its immediate transaction repeats eligibility and the complete preservation
// digest, updates only the exact reservation, and records finalization work.
// The ordinary whole-set recovery APIs retain their existing stricter guards.
func (s *AuthorityStore) ConfirmScopedCompletedTeardownRecovery(ctx context.Context, pair CompletedTeardownRecoveryPair, expectedPreservedSHA string) error {
	if s == nil || s.db == nil || !validScopedPreservationSHA(expectedPreservedSHA) {
		return ErrCompletedTeardownRecoveryNotReleasable
	}
	if _, err := validateCompletedTeardownRecoveryPairs([]CompletedTeardownRecoveryPair{pair}); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, scopedCompletedTeardownTimeout)
	defer cancel()
	now := s.now().UTC()
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, conn *sql.Conn) (struct{}, error) {
		plan, err := readScopedCompletedTeardownRecoveryPlan(ctx, conn, pair)
		if err != nil {
			return struct{}{}, err
		}
		if plan.PreservedInventorySHA256 != expectedPreservedSHA {
			return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
		}
		if plan.Recovery.AlreadyRecovered {
			return struct{}{}, nil // Pending and fully finalized repeats never release again.
		}
		beforeChanges, err := scopedConnectionChanges(ctx, conn)
		if err != nil {
			return struct{}{}, err
		}
		result, err := conn.ExecContext(ctx, `UPDATE exec_capacity_reservations
SET cleanup_confirmed_at = ?, released_at = ?
WHERE session_id = ? AND host_key = ? AND cleanup_confirmed_at IS NULL AND released_at IS NULL`,
			formatStoredTime(now), formatStoredTime(now), string(pair.SessionID), reservationHostKey)
		if err != nil {
			return struct{}{}, fmt.Errorf("release scoped completed-teardown reservation: %w", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO exec_completed_teardown_recovery_finalizations
(session_id,command_id,job_id,capacity_released_at) VALUES (?,?,?,?)`,
			string(pair.SessionID), string(pair.CommandID), string(plan.Recovery.JobID), formatStoredTime(now)); err != nil {
			return struct{}{}, fmt.Errorf("record scoped completed-teardown finalization: %w", err)
		}
		if afterChanges, err := scopedConnectionChanges(ctx, conn); err != nil || afterChanges-beforeChanges != 2 {
			return struct{}{}, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
		}
		// A trigger or future modification must not alter protected metadata.
		after, err := readScopedCompletedTeardownRecoveryPlan(ctx, conn, pair)
		if err != nil || after.PreservedInventorySHA256 != expectedPreservedSHA || !after.Recovery.AlreadyRecovered || !after.Recovery.FinalizationPending {
			return struct{}{}, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
		}
		return struct{}{}, nil
	})
	return err
}

// CompleteScopedCompletedTeardownRecoveryFinalization deletes only the exact
// post-release work item after the operator has finalized the selected owner
// marker/workspace. It preserves the same baseline and refuses collateral
// trigger writes even when those affect payloads excluded from metadata reads.
func (s *AuthorityStore) CompleteScopedCompletedTeardownRecoveryFinalization(ctx context.Context, pair CompletedTeardownRecoveryPair, expectedPreservedSHA string) error {
	if s == nil || s.db == nil || !validScopedPreservationSHA(expectedPreservedSHA) {
		return ErrCompletedTeardownRecoveryNotReleasable
	}
	if _, err := validateCompletedTeardownRecoveryPairs([]CompletedTeardownRecoveryPair{pair}); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, scopedCompletedTeardownTimeout)
	defer cancel()
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, conn *sql.Conn) (struct{}, error) {
		plan, err := readScopedCompletedTeardownRecoveryPlan(ctx, conn, pair)
		if err != nil {
			return struct{}{}, err
		}
		if plan.PreservedInventorySHA256 != expectedPreservedSHA || !plan.Recovery.AlreadyRecovered {
			return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
		}
		if !plan.Recovery.FinalizationPending {
			return struct{}{}, nil
		}
		beforeChanges, err := scopedConnectionChanges(ctx, conn)
		if err != nil {
			return struct{}{}, err
		}
		result, err := conn.ExecContext(ctx, `DELETE FROM exec_completed_teardown_recovery_finalizations
WHERE session_id=? AND command_id=? AND job_id=?`, string(pair.SessionID), string(pair.CommandID), string(plan.Recovery.JobID))
		if err != nil {
			return struct{}{}, fmt.Errorf("complete scoped teardown finalization: %w", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return struct{}{}, ErrCompletedTeardownRecoveryNotReleasable
		}
		if afterChanges, err := scopedConnectionChanges(ctx, conn); err != nil || afterChanges-beforeChanges != 1 {
			return struct{}{}, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
		}
		after, err := readScopedCompletedTeardownRecoveryPlan(ctx, conn, pair)
		if err != nil || after.PreservedInventorySHA256 != expectedPreservedSHA || !after.Recovery.AlreadyRecovered || after.Recovery.FinalizationPending {
			return struct{}{}, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
		}
		return struct{}{}, nil
	})
	return err
}

func scopedConnectionChanges(ctx context.Context, conn *sql.Conn) (int64, error) {
	var count int64
	err := conn.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&count)
	return count, err
}

func validScopedPreservationSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func readScopedCompletedTeardownRecoveryPlan(ctx context.Context, conn *sql.Conn, pair CompletedTeardownRecoveryPair) (ScopedCompletedTeardownRecoveryPlan, error) {
	var plan ScopedCompletedTeardownRecoveryPlan
	if err := verifyCurrentSchemaConnection(ctx, conn); err != nil {
		return plan, err
	}
	if CurrentSchemaVersion != 40 {
		return plan, ErrSchemaVersion
	}
	recovery, err := readCompletedTeardownRecovery(ctx, conn, pair)
	if err != nil {
		return plan, err
	}
	if recovery.Session.Target.Kind() != domain.TargetKindLocal || recovery.Session.Target.Profile() != "mac-workstation" ||
		recovery.Session.Controller.Type() != domain.ControllerTypeLocalUser || recovery.Session.Source.Mode() != domain.SourceModeEmpty {
		return plan, ErrCompletedTeardownRecoveryNotReleasable
	}
	if recovery.FinalizationPending {
		var releasedAt string
		if err := conn.QueryRowContext(ctx, `SELECT capacity_released_at FROM exec_completed_teardown_recovery_finalizations WHERE session_id=? AND command_id=?`, string(pair.SessionID), string(pair.CommandID)).Scan(&releasedAt); err != nil || recovery.SessionReservation.ReleasedAt == nil || releasedAt != formatStoredTime(*recovery.SessionReservation.ReleasedAt) {
			return plan, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
		}
	}
	var unsafe int
	if err := conn.QueryRowContext(ctx, `SELECT
(SELECT COUNT(*) FROM exec_sessions WHERE state NOT IN ('closed','expired','failed','lost')) +
(SELECT COUNT(*) FROM exec_commands WHERE state NOT IN ('succeeded','failed','cancelled','timed_out','rejected','lost')) +
(SELECT COUNT(*) FROM exec_jobs WHERE phase NOT IN ('complete','failed','lost')) +
(SELECT COUNT(*) FROM local_intents WHERE target_kind='local' AND delivery_state IN ('recorded','dispatching','uncertain')) +
(SELECT COUNT(*) FROM exec_lost_runtime_recovery_finalizations) +
(SELECT COUNT(*) FROM exec_commandless_lost_runtime_recovery_finalizations) +
(SELECT COUNT(*) FROM exec_completed_teardown_recovery_finalizations WHERE session_id<>? OR command_id<>?)`,
		string(pair.SessionID), string(pair.CommandID)).Scan(&unsafe); err != nil {
		return plan, fmt.Errorf("read scoped completed-teardown idle inventory: %w", err)
	}
	if unsafe != 0 {
		return plan, ErrCompletedTeardownRecoveryNotReleasable
	}
	// A terminal lost peer may be an interactive session without a job. When
	// a job does exist for a retained reservation, its identity must agree with
	// the actual session rather than merely sharing a session ID.
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_jobs j
JOIN exec_capacity_reservations r ON r.session_id=j.session_id
LEFT JOIN exec_sessions s ON s.session_id=j.session_id
WHERE (r.cleanup_confirmed_at IS NULL OR r.released_at IS NULL) AND
(s.session_id IS NULL OR j.controller_type<>s.controller_type OR j.controller_id<>s.controller_id
 OR j.target_kind<>s.target_kind OR j.target_profile<>s.target_profile OR j.environment<>s.environment
 OR j.source_mode<>s.source_mode OR j.source_repository_alias<>s.source_repository_alias
 OR j.source_requested_revision<>s.source_requested_revision OR j.source_path<>s.source_path)`).Scan(&unsafe); err != nil {
		return plan, fmt.Errorf("read scoped retained identity consistency: %w", err)
	}
	if unsafe != 0 {
		return plan, ErrCompletedTeardownRecoveryNotReleasable
	}
	inventory, err := readRetainedWorkInventorySnapshot(ctx, conn, MaxRetainedWorkInventoryRows)
	if err != nil {
		return plan, err
	}
	bySession := make(map[string]RetainedWorkPair, len(inventory.RetainedPairs))
	for _, held := range inventory.RetainedPairs {
		if _, duplicate := bySession[held.SessionID]; duplicate || !held.IdentityConsistent || !held.SessionReservationHeld || held.SessionState != "lost" ||
			(held.Classification != "lost_pair" && held.Classification != "completed_teardown" && held.Classification != "commandless_lost") {
			return plan, ErrCompletedTeardownRecoveryNotReleasable
		}
		switch held.Classification {
		case "lost_pair":
			already, err := checkLostRuntimeRecoveryPair(ctx, conn, LostRuntimeRecoveryPair{SessionID: domain.SessionID(held.SessionID), CommandID: domain.CommandID(held.CommandID)})
			if err != nil || already {
				return plan, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
			}
		case "completed_teardown":
			other, err := readCompletedTeardownRecovery(ctx, conn, CompletedTeardownRecoveryPair{SessionID: domain.SessionID(held.SessionID), CommandID: domain.CommandID(held.CommandID)})
			if err != nil || other.AlreadyRecovered {
				return plan, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
			}
		case "commandless_lost":
			other, err := readCommandlessLostRuntimeRecovery(ctx, conn, domain.SessionID(held.SessionID))
			if err != nil || other.AlreadyRecovered {
				return plan, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
			}
		}
		bySession[held.SessionID] = held
	}
	all := map[domain.SessionID]struct{}{pair.SessionID: {}}
	for _, reservation := range inventory.SessionReservations {
		if !reservation.SessionPresent {
			return plan, ErrCompletedTeardownRecoveryNotReleasable
		}
		held, ok := bySession[reservation.SessionID]
		if !ok || held.SessionID != reservation.SessionID || reservation.HostKey != reservationHostKey || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
			return plan, ErrCompletedTeardownRecoveryNotReleasable
		}
		all[domain.SessionID(reservation.SessionID)] = struct{}{}
	}
	for _, slot := range inventory.CommandSlots {
		held, ok := bySession[slot.SessionID]
		if !ok || !slot.CommandPresent || held.Classification != "lost_pair" || held.CommandID != slot.CommandID || slot.HostKey != schedulerHostKey || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
			return plan, ErrCompletedTeardownRecoveryNotReleasable
		}
	}
	ids := make([]domain.SessionID, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	plan.Recovery = recovery
	for _, id := range ids {
		session, err := readSessionOnConnection(ctx, conn, id)
		if err != nil || session.Target.Kind() != domain.TargetKindLocal || session.Target.Profile() != "mac-workstation" || session.Controller.Type() != domain.ControllerTypeLocalUser {
			return ScopedCompletedTeardownRecoveryPlan{}, errors.Join(ErrCompletedTeardownRecoveryNotReleasable, err)
		}
		plan.Sessions = append(plan.Sessions, session)
		if id != pair.SessionID {
			plan.PreservedSessionIDs = append(plan.PreservedSessionIDs, id)
		}
	}
	plan.PreservedInventorySHA256, err = scopedCompletedTeardownMetadataDigest(ctx, conn, pair)
	if err != nil {
		return ScopedCompletedTeardownRecoveryPlan{}, err
	}
	return plan, nil
}

// These explicit schema-40 projections contain identity/result/capacity
// metadata only. Existing script/request/response digests bind content without
// loading scripts, canonical payloads or command output. Event payload bytes
// are deliberately excluded; the operator separately preserves projected file
// hashes. The version identifies this exact canonical projection contract.
func scopedCompletedTeardownMetadataDigest(ctx context.Context, conn *sql.Conn, pair CompletedTeardownRecoveryPair) (string, error) {
	queries := []struct {
		name, sql string
		args      []any
	}{
		{"sessions", `SELECT session_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,source_repository_alias,source_requested_revision,source_path,source_resolved_revision,runtime_generation,state,command_timeout_ns,idle_timeout_ns,session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at FROM exec_sessions ORDER BY session_id`, nil},
		{"commands", `SELECT command_id,session_id,ordinal,intent_ordinal,request_hash_version,hex(request_hash),hex(script_sha256),state,timeout_ns,exit_code,final_event_sequence,output_truncated,output_complete,output_unavailable_reason,created_at,updated_at FROM exec_commands ORDER BY command_id`, nil},
		{"jobs", `SELECT job_id,session_id,command_id,controller_type,controller_id,target_kind,target_profile,environment,source_mode,source_repository_alias,source_requested_revision,source_path,request_hash_version,hex(request_hash),idempotency_key,hex(script_sha256),phase,command_state,exit_code,final_event_sequence,output_truncated,output_complete,output_unavailable_reason,teardown_state,teardown_reason,ingress,created_at,updated_at FROM exec_jobs ORDER BY job_id`, nil},
		{"session_lifecycle", `SELECT session_id,lifecycle_sequence,previous_state,new_state,reason,occurred_at FROM exec_session_lifecycle ORDER BY session_id,lifecycle_sequence`, nil},
		{"events", `SELECT command_id,sequence,event_type,byte_count,occurred_at FROM exec_command_events ORDER BY command_id,sequence`, nil},
		{"reservations", `SELECT session_id,host_key,reserved_at,CASE WHEN session_id=? THEN NULL ELSE cleanup_confirmed_at END,CASE WHEN session_id=? THEN NULL ELSE released_at END FROM exec_capacity_reservations ORDER BY session_id`, []any{string(pair.SessionID), string(pair.SessionID)}},
		{"command_slots", `SELECT command_id,host_key,reserved_at,stop_confirmed_at,released_at FROM exec_command_slots ORDER BY command_id`, nil},
		{"completed_finalizations", `SELECT session_id,command_id,job_id,capacity_released_at FROM exec_completed_teardown_recovery_finalizations WHERE NOT(session_id=? AND command_id=?) ORDER BY session_id,command_id`, []any{string(pair.SessionID), string(pair.CommandID)}},
		{"intents", `SELECT intent_id,operation,resource_id,session_id,command_id,job_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,source_repository_alias,source_requested_revision,source_path,request_hash_version,hex(request_hash),idempotency_key,hex(script_sha256),intent_ordinal,delivery_state,reason,lease_owner,lease_expires_at,attempt_count,remote_terminal_proof_version,created_at,updated_at FROM local_intents ORDER BY intent_id`, nil},
		{"intent_lifecycle", `SELECT intent_id,lifecycle_sequence,previous_state,new_state,reason,occurred_at FROM local_intent_lifecycle ORDER BY intent_id,lifecycle_sequence`, nil},
		{"mailboxes", `SELECT exchange_id,mailbox_id,client_request_id,operation,controller_type,controller_id,client_idempotency_key,execution_idempotency_key,canonical_hash_version,hex(canonical_hash),resource_id,request_state,response_revision,hex(terminal_response_sha256),available_event_sequence,created_at,updated_at,hex(response_sha256),acknowledged_at,response_cleanup_at,response_cleanup_started_at,response_file_removed_at,idempotency_key_expires_at,idempotency_binding_active,deduplication_warning,resolved_execution_context,resolved_environment,resolved_target_kind,resolved_target_profile,execution_selection_source,execution_selection_state,repository_alias,repository_aliases_json FROM mailbox_exchanges ORDER BY exchange_id`, nil},
		{"remote_sessions", `SELECT session_id,target_kind,target_profile,controller_type,controller_id,session_state,environment,source_json,capabilities_json,runtime_generation,resolved_revision,observed_at,is_stale FROM local_remote_session_projections ORDER BY session_id`, nil},
		{"remote_commands", `SELECT command_id,session_id,ordinal,command_state,exit_code,final_event_sequence,output_complete,output_truncated,output_unavailable_reason,target_kind,target_profile,controller_type,controller_id,environment,source_json,capabilities_json,observed_at,is_stale FROM local_remote_command_projections ORDER BY command_id`, nil},
		{"remote_jobs", `SELECT job_id,session_id,command_id,job_phase,command_state,exit_code,final_event_sequence,output_complete,output_truncated,output_unavailable_reason,teardown_state,teardown_reason,target_kind,target_profile,controller_type,controller_id,environment,source_json,capabilities_json,queue_blocked_reason,observed_at,is_stale FROM local_remote_job_projections ORDER BY job_id`, nil},
		{"remote_events", `SELECT command_id,sequence,event_type,byte_count,occurred_at FROM local_remote_events ORDER BY command_id,sequence`, nil},
		{"remote_cursors", `SELECT command_id,last_sequence,updated_at FROM local_remote_event_cursors ORDER BY command_id`, nil},
		{"remote_gaps", `SELECT command_id,missing_from,missing_to,available_sequence,final_sequence,terminal_state,output_complete,output_unavailable_reason,confirmed_at FROM local_remote_event_gaps ORDER BY command_id`, nil},
		{"remote_status_failures", `SELECT intent_id,first_observed_at,reason,reconciliation_attempts FROM local_remote_status_failures ORDER BY intent_id`, nil},
		{"exec_idempotency", `SELECT controller_type,controller_id,operation,idempotency_key,canonical_hash_version,hex(canonical_hash),resource_id,created_at,expires_at,deduplication_warning FROM exec_idempotency ORDER BY controller_type,controller_id,operation,idempotency_key`, nil},
		{"local_idempotency", `SELECT controller_type,controller_id,operation,idempotency_key,request_hash_version,hex(request_hash),intent_id,resource_id,created_at,expires_at FROM local_idempotency ORDER BY controller_type,controller_id,operation,idempotency_key`, nil},
		{"idempotency_warnings", `SELECT controller_type,controller_id,operation,hex(key_fingerprint),warning_until FROM exec_idempotency_expiry_warnings ORDER BY controller_type,controller_id,operation,key_fingerprint`, nil},
		{"lane_admission", `SELECT singleton,hex(state_sha256),updated_at FROM lane_ingress_admission ORDER BY singleton`, nil},
		{"lane_budgets", `SELECT lane,max_reserved_bytes,policy_digest,updated_at FROM lane_capacity_budgets ORDER BY lane`, nil},
		{"lane_reservations", `SELECT lane,reservation_id,category,reserved_bytes,created_at FROM lane_capacity_reservations ORDER BY lane,reservation_id,category`, nil},
	}
	hash := sha256.New()
	encoder := json.NewEncoder(&scopedMetadataDigestWriter{Hash: hash, remaining: scopedMetadataMaxBytes})
	if err := encoder.Encode([]string{"scoped-completed-teardown-metadata-v1", string(pair.SessionID), string(pair.CommandID)}); err != nil {
		return "", err
	}
	remainingRows := scopedMetadataMaxRows
	for _, query := range queries {
		if err := encoder.Encode(query.name); err != nil {
			return "", err
		}
		rows, err := conn.QueryContext(ctx, query.sql, query.args...)
		if err != nil {
			return "", fmt.Errorf("read scoped metadata %s: %w", query.name, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return "", err
		}
		for rows.Next() {
			remainingRows--
			if remainingRows < 0 {
				_ = rows.Close()
				return "", ErrRetainedWorkInventoryLimit
			}
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for index := range values {
				dest[index] = &values[index]
			}
			if err := rows.Scan(dest...); err != nil {
				_ = rows.Close()
				return "", err
			}
			// Every projected BLOB is explicitly hex-encoded by SQL. Refuse
			// unexpected SQLite storage classes and invalid text rather than
			// let JSON coerce them to a colliding canonical representation.
			for _, value := range values {
				valid := false
				switch typed := value.(type) {
				case nil, int64:
					valid = true
				case string:
					valid = utf8.ValidString(typed)
				}
				if !valid {
					_ = rows.Close()
					return "", ErrCompletedTeardownRecoveryNotReleasable
				}
			}
			if err := encoder.Encode(values); err != nil {
				_ = rows.Close()
				return "", err
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Keep the planned metadata read/release bounded even when callers omit a
// deadline. This does not extend the operator's separate authorization window.
const (
	scopedCompletedTeardownTimeout = 30 * time.Second
	// The current paused authority has 366,192 historical metadata rows and
	// about 97 MB in this projection. Hash it as a bounded stream; do not load
	// stored scripts/output or discard history to fit a small fixture budget.
	scopedMetadataMaxRows  = 1_000_000
	scopedMetadataMaxBytes = 256 << 20
)

type scopedMetadataDigestWriter struct {
	hash.Hash
	remaining int
}

func (w *scopedMetadataDigestWriter) Write(value []byte) (int, error) {
	if len(value) > w.remaining {
		return 0, ErrRetainedWorkInventoryLimit
	}
	w.remaining -= len(value)
	return w.Hash.Write(value)
}
