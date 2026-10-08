package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

const (
	// DefaultOutputRetention is the normal authority retention for command
	// event payloads after a command reaches a terminal state.
	DefaultOutputRetention = 30 * 24 * time.Hour
	// DefaultMetadataRetention is the minimum retention for terminal authority
	// metadata and mutation keys.
	DefaultMetadataRetention = 90 * 24 * time.Hour
)

var ErrInvalidGarbageCollection = errors.New("invalid garbage-collection retention")

// GarbageCollectionOptions controls one deterministic authority cleanup pass.
// Zero values select the PoC's 30-day output and 90-day metadata boundaries.
type GarbageCollectionOptions struct {
	OutputRetention   time.Duration
	MetadataRetention time.Duration
}

// GarbageCollectionReport records rows and payloads removed by one pass.
// Live session reservations and live command slots deliberately do not appear
// in the deletion counts: they pin their parent records beyond retention.
type GarbageCollectionReport struct {
	IdempotencyRecordsDeleted        int
	CommandsOutputExpired            int
	CommandEventsDeleted             int
	RemoteCommandsOutputExpired      int
	RemoteCommandEventsDeleted       int
	JobsDeleted                      int
	CommandsDeleted                  int
	SessionsDeleted                  int
	MailboxIngressDiagnosticsDeleted int
}

// CollectGarbage expires output payloads at the 30-day boundary, removes
// expired idempotency bindings after retaining a short-lived key fingerprint
// warning marker, and removes terminal metadata only after the 90-day
// boundary. Unconfirmed session reservations and command slots pin all
// related parent metadata, so GC can never free residual runtime capacity.
func (s *AuthorityStore) CollectGarbage(ctx context.Context, options GarbageCollectionOptions) (GarbageCollectionReport, error) {
	if s == nil || s.db == nil {
		return GarbageCollectionReport{}, ErrNilDatabase
	}
	outputRetention := options.OutputRetention
	if outputRetention == 0 {
		outputRetention = DefaultOutputRetention
	}
	metadataRetention := options.MetadataRetention
	if metadataRetention == 0 {
		metadataRetention = DefaultMetadataRetention
	}
	if outputRetention < 0 || metadataRetention < 0 {
		return GarbageCollectionReport{}, ErrInvalidGarbageCollection
	}
	now := s.now().UTC()
	outputCutoff := now.Add(-outputRetention)
	metadataCutoff := now.Add(-metadataRetention)
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (GarbageCollectionReport, error) {
		var report GarbageCollectionReport
		rows, err := connection.QueryContext(ctx, `
SELECT controller_type, controller_id, operation, idempotency_key, expires_at
FROM exec_idempotency WHERE expires_at <= ?
`, formatStoredTime(now))
		if err != nil {
			return report, fmt.Errorf("read expired idempotency keys: %w", err)
		}
		type expiredIdempotencyKey struct {
			controllerType string
			controllerID   string
			operation      string
			key            string
			expiresAt      string
		}
		var expiredKeys []expiredIdempotencyKey
		for rows.Next() {
			var key expiredIdempotencyKey
			if err := rows.Scan(&key.controllerType, &key.controllerID, &key.operation, &key.key, &key.expiresAt); err != nil {
				rows.Close()
				return report, fmt.Errorf("scan expired idempotency key: %w", err)
			}
			expiredKeys = append(expiredKeys, key)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return report, fmt.Errorf("read expired idempotency keys: %w", err)
		}
		if err := rows.Close(); err != nil {
			return report, fmt.Errorf("close expired idempotency key rows: %w", err)
		}
		for _, expired := range expiredKeys {
			controllerID, err := domain.NewControllerID(expired.controllerID)
			if err != nil {
				return report, fmt.Errorf("validate expired idempotency controller ID: %w", err)
			}
			controller, err := domain.NewControllerIdentity(domain.ControllerType(expired.controllerType), controllerID)
			if err != nil {
				return report, fmt.Errorf("validate expired idempotency controller: %w", err)
			}
			expiresAt, err := parseStoredTime(expired.expiresAt)
			if err != nil {
				return report, fmt.Errorf("parse expired idempotency expiry: %w", err)
			}
			if err := recordIdempotencyExpiryWarningOnConnection(ctx, connection, controller, expired.operation, expired.key, expiresAt, now); err != nil {
				return report, err
			}
		}
		result, err := connection.ExecContext(ctx, "DELETE FROM exec_idempotency WHERE expires_at <= ?", formatStoredTime(now))
		if err != nil {
			return report, fmt.Errorf("garbage-collect idempotency: %w", err)
		}
		if report.IdempotencyRecordsDeleted, err = rowsAffected(result); err != nil {
			return report, err
		}
		warningRows, err := connection.QueryContext(ctx, `
SELECT controller_type, controller_id, operation, key_fingerprint, warning_until
FROM exec_idempotency_expiry_warnings
`)
		if err != nil {
			return report, fmt.Errorf("read idempotency expiry warnings: %w", err)
		}
		type expiredWarning struct {
			controllerType string
			controllerID   string
			operation      string
			fingerprint    []byte
			warningUntil   string
		}
		var expiredWarnings []expiredWarning
		for warningRows.Next() {
			var warning expiredWarning
			if err := warningRows.Scan(&warning.controllerType, &warning.controllerID, &warning.operation, &warning.fingerprint, &warning.warningUntil); err != nil {
				warningRows.Close()
				return report, fmt.Errorf("scan idempotency expiry warning: %w", err)
			}
			expiredWarnings = append(expiredWarnings, warning)
		}
		if err := warningRows.Err(); err != nil {
			warningRows.Close()
			return report, fmt.Errorf("read idempotency expiry warnings: %w", err)
		}
		if err := warningRows.Close(); err != nil {
			return report, fmt.Errorf("close idempotency expiry warning rows: %w", err)
		}
		for _, warning := range expiredWarnings {
			warningUntil, err := parseStoredTime(warning.warningUntil)
			if err != nil {
				return report, fmt.Errorf("parse idempotency expiry warning: %w", err)
			}
			if now.Before(warningUntil) {
				continue
			}
			if _, err := connection.ExecContext(ctx, `
DELETE FROM exec_idempotency_expiry_warnings
WHERE controller_type = ? AND controller_id = ? AND operation = ? AND key_fingerprint = ?
`, warning.controllerType, warning.controllerID, warning.operation, warning.fingerprint); err != nil {
				return report, fmt.Errorf("garbage-collect idempotency expiry warning: %w", err)
			}
		}

		result, err = connection.ExecContext(ctx, `
UPDATE exec_commands
SET output_complete = 0, output_unavailable_reason = 'retention_expired'
WHERE state IN (?, ?, ?, ?, ?, ?)
  AND updated_at <= ?
  AND output_unavailable_reason = ''
`, string(domain.CommandStateSucceeded), string(domain.CommandStateFailed), string(domain.CommandStateCancelled),
			string(domain.CommandStateTimedOut), string(domain.CommandStateRejected), string(domain.CommandStateLost), formatStoredTime(outputCutoff))
		if err != nil {
			return report, fmt.Errorf("expire command output: %w", err)
		}
		if report.CommandsOutputExpired, err = rowsAffected(result); err != nil {
			return report, err
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE exec_jobs
SET output_complete = 0, output_unavailable_reason = 'retention_expired'
WHERE command_id IN (
    SELECT command_id FROM exec_commands WHERE output_unavailable_reason = 'retention_expired'
)
  AND output_unavailable_reason = ''
`); err != nil {
			return report, fmt.Errorf("expire job output: %w", err)
		}
		result, err = connection.ExecContext(ctx, `
UPDATE local_remote_command_projections
SET output_complete = 0, output_unavailable_reason = 'retention_expired'
WHERE command_state IN (?, ?, ?, ?, ?, ?)
  AND output_unavailable_reason != 'retention_expired'
  AND (
      EXISTS (
          SELECT 1 FROM local_remote_events e
          WHERE e.command_id = local_remote_command_projections.command_id
            AND e.sequence = local_remote_command_projections.final_event_sequence
            AND e.event_type IN ('command_succeeded', 'command_failed', 'command_cancelled', 'command_timed_out', 'command_rejected', 'command_lost')
            AND e.occurred_at <= ?
      )
      OR EXISTS (
          SELECT 1 FROM local_remote_event_gaps g
          WHERE g.command_id = local_remote_command_projections.command_id
            AND g.confirmed_at <= ?
      )
  )
`, string(domain.CommandStateSucceeded), string(domain.CommandStateFailed), string(domain.CommandStateCancelled),
			string(domain.CommandStateTimedOut), string(domain.CommandStateRejected), string(domain.CommandStateLost), formatStoredTime(outputCutoff), formatStoredTime(outputCutoff))
		if err != nil {
			return report, fmt.Errorf("expire mirrored remote output: %w", err)
		}
		if report.RemoteCommandsOutputExpired, err = rowsAffected(result); err != nil {
			return report, err
		}
		if _, err := connection.ExecContext(ctx, `
UPDATE local_remote_job_projections
SET output_complete = 0, output_unavailable_reason = 'retention_expired'
WHERE command_id IN (
    SELECT command_id FROM local_remote_command_projections WHERE output_unavailable_reason = 'retention_expired'
)
  AND output_unavailable_reason = ''
`); err != nil {
			return report, fmt.Errorf("expire mirrored remote job output: %w", err)
		}
		result, err = connection.ExecContext(ctx, `
DELETE FROM exec_command_events
WHERE command_id IN (
    SELECT command_id FROM exec_commands WHERE output_unavailable_reason = 'retention_expired'
)
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_event_file_references r
      JOIN mailbox_exchanges e ON e.exchange_id = r.exchange_id
      WHERE r.command_id = exec_command_events.command_id
        AND e.response_file_removed_at IS NULL
        AND (e.response_cleanup_at IS NULL OR e.response_cleanup_at > ?)
  )
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_remote_event_file_references r
      JOIN mailbox_exchanges e ON e.exchange_id = r.exchange_id
      WHERE r.command_id = exec_command_events.command_id
        AND e.response_file_removed_at IS NULL
        AND (e.response_cleanup_at IS NULL OR e.response_cleanup_at > ?)
  )
`, formatStoredTime(now), formatStoredTime(now))
		if err != nil {
			return report, fmt.Errorf("delete expired command events: %w", err)
		}
		if report.CommandEventsDeleted, err = rowsAffected(result); err != nil {
			return report, err
		}
		result, err = connection.ExecContext(ctx, `
DELETE FROM local_remote_events
WHERE command_id IN (
    SELECT command_id FROM local_remote_command_projections WHERE output_unavailable_reason = 'retention_expired'
)
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_event_file_references r
      JOIN mailbox_exchanges e ON e.exchange_id = r.exchange_id
      WHERE r.command_id = local_remote_events.command_id
        AND e.response_file_removed_at IS NULL
        AND (e.response_cleanup_at IS NULL OR e.response_cleanup_at > ?)
  )
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_remote_event_file_references r
      JOIN mailbox_exchanges e ON e.exchange_id = r.exchange_id
      WHERE r.command_id = local_remote_events.command_id
        AND e.response_file_removed_at IS NULL
        AND (e.response_cleanup_at IS NULL OR e.response_cleanup_at > ?)
  )
`, formatStoredTime(now), formatStoredTime(now))
		if err != nil {
			return report, fmt.Errorf("delete expired mirrored remote events: %w", err)
		}
		if report.RemoteCommandEventsDeleted, err = rowsAffected(result); err != nil {
			return report, err
		}

		jobIDs, err := gcJobIDs(ctx, connection, metadataCutoff)
		if err != nil {
			return report, err
		}
		for _, id := range jobIDs {
			result, err := connection.ExecContext(ctx, "DELETE FROM exec_jobs WHERE job_id = ?", id)
			if err != nil {
				return report, fmt.Errorf("delete job metadata %q: %w", id, err)
			}
			changed, err := rowsAffected(result)
			if err != nil {
				return report, err
			}
			report.JobsDeleted += changed
		}

		commandIDs, err := gcCommandIDs(ctx, connection, metadataCutoff)
		if err != nil {
			return report, err
		}
		for _, id := range commandIDs {
			if _, err := connection.ExecContext(ctx, "DELETE FROM exec_command_slots WHERE command_id = ?", id); err != nil {
				return report, fmt.Errorf("delete command slot metadata %q: %w", id, err)
			}
			if _, err := connection.ExecContext(ctx, "DELETE FROM exec_command_events WHERE command_id = ?", id); err != nil {
				return report, fmt.Errorf("delete command events %q: %w", id, err)
			}
			result, err := connection.ExecContext(ctx, "DELETE FROM exec_commands WHERE command_id = ?", id)
			if err != nil {
				return report, fmt.Errorf("delete command metadata %q: %w", id, err)
			}
			changed, err := rowsAffected(result)
			if err != nil {
				return report, err
			}
			report.CommandsDeleted += changed
		}

		sessionIDs, err := gcSessionIDs(ctx, connection, metadataCutoff)
		if err != nil {
			return report, err
		}
		for _, id := range sessionIDs {
			if _, err := connection.ExecContext(ctx, "DELETE FROM exec_capacity_reservations WHERE session_id = ?", id); err != nil {
				return report, fmt.Errorf("delete session reservation metadata %q: %w", id, err)
			}
			if _, err := connection.ExecContext(ctx, "DELETE FROM exec_session_lifecycle WHERE session_id = ?", id); err != nil {
				return report, fmt.Errorf("delete session lifecycle metadata %q: %w", id, err)
			}
			result, err := connection.ExecContext(ctx, "DELETE FROM exec_sessions WHERE session_id = ?", id)
			if err != nil {
				return report, fmt.Errorf("delete session metadata %q: %w", id, err)
			}
			changed, err := rowsAffected(result)
			if err != nil {
				return report, err
			}
			report.SessionsDeleted += changed
		}

		// A malformed-input record reserves its request ID for the normal
		// metadata window. It is eligible only after both the safe input pair
		// and its private diagnostic projection have been durably removed.
		result, err = connection.ExecContext(ctx, `
DELETE FROM mailbox_ingress_diagnostics
WHERE observed_at <= ?
  AND input_pair_removed_at IS NOT NULL
  AND diagnostic_file_removed_at IS NOT NULL
`, formatStoredTime(metadataCutoff))
		if err != nil {
			return report, fmt.Errorf("delete mailbox ingress diagnostic metadata: %w", err)
		}
		if report.MailboxIngressDiagnosticsDeleted, err = rowsAffected(result); err != nil {
			return report, err
		}
		return report, nil
	})
}

// GarbageCollect is the descriptive alias used by operational callers.
func (s *AuthorityStore) GarbageCollect(ctx context.Context, options GarbageCollectionOptions) (GarbageCollectionReport, error) {
	return s.CollectGarbage(ctx, options)
}

func rowsAffected(result sql.Result) (int, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read garbage-collection row count: %w", err)
	}
	return int(changed), nil
}

func gcJobIDs(ctx context.Context, connection *sql.Conn, cutoff time.Time) ([]string, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT j.job_id
FROM exec_jobs j
WHERE j.phase IN (?, ?, ?)
  AND j.updated_at <= ?
  AND NOT EXISTS (
      SELECT 1 FROM exec_capacity_reservations r
      WHERE r.session_id = j.session_id AND r.cleanup_confirmed_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM exec_command_slots cs
      WHERE cs.command_id = j.command_id AND cs.stop_confirmed_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM exec_commandless_lost_runtime_recovery_finalizations f
      WHERE f.job_id = j.job_id
  )
`, string(JobPhaseComplete), string(JobPhaseFailed), string(JobPhaseLost), formatStoredTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("select job metadata for GC: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan job metadata for GC: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job metadata for GC: %w", err)
	}
	return ids, nil
}

func gcCommandIDs(ctx context.Context, connection *sql.Conn, cutoff time.Time) ([]string, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT c.command_id
FROM exec_commands c
JOIN exec_sessions s ON s.session_id = c.session_id
WHERE c.state IN (?, ?, ?, ?, ?, ?)
  AND c.updated_at <= ?
  AND s.state IN (?, ?, ?, ?)
  AND s.updated_at <= ?
  AND NOT EXISTS (
      SELECT 1 FROM exec_command_slots cs
      WHERE cs.command_id = c.command_id AND cs.stop_confirmed_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM exec_jobs j WHERE j.command_id = c.command_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM exec_lost_runtime_recovery_finalizations f
      WHERE f.command_id = c.command_id
  )
`, string(domain.CommandStateSucceeded), string(domain.CommandStateFailed), string(domain.CommandStateCancelled),
		string(domain.CommandStateTimedOut), string(domain.CommandStateRejected), string(domain.CommandStateLost), formatStoredTime(cutoff),
		string(domain.SessionStateClosed), string(domain.SessionStateExpired), string(domain.SessionStateFailed), string(domain.SessionStateLost), formatStoredTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("select command metadata for GC: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan command metadata for GC: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command metadata for GC: %w", err)
	}
	return ids, nil
}

func gcSessionIDs(ctx context.Context, connection *sql.Conn, cutoff time.Time) ([]string, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT s.session_id
FROM exec_sessions s
JOIN exec_capacity_reservations r ON r.session_id = s.session_id
WHERE s.state IN (?, ?, ?, ?)
  AND s.updated_at <= ?
  AND r.cleanup_confirmed_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM exec_commands c WHERE c.session_id = s.session_id)
  AND NOT EXISTS (SELECT 1 FROM exec_jobs j WHERE j.session_id = s.session_id)
  AND NOT EXISTS (
      SELECT 1 FROM exec_lost_runtime_recovery_finalizations f
      WHERE f.session_id = s.session_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM exec_commandless_lost_runtime_recovery_finalizations f
      WHERE f.session_id = s.session_id
  )
`, string(domain.SessionStateClosed), string(domain.SessionStateExpired), string(domain.SessionStateFailed), string(domain.SessionStateLost), formatStoredTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("select session metadata for GC: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan session metadata for GC: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session metadata for GC: %w", err)
	}
	return ids, nil
}
