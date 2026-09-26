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
	IdempotencyRecordsDeleted int
	CommandsOutputExpired     int
	CommandEventsDeleted      int
	JobsDeleted               int
	CommandsDeleted           int
	SessionsDeleted           int
}

// CollectGarbage expires output payloads at the 30-day boundary, removes
// expired idempotency bindings, and removes terminal metadata only after the
// 90-day boundary. Unconfirmed session reservations and command slots pin all
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
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (GarbageCollectionReport, error) {
		var report GarbageCollectionReport
		result, err := connection.ExecContext(ctx, "DELETE FROM exec_idempotency WHERE expires_at <= ?", formatStoredTime(now))
		if err != nil {
			return report, fmt.Errorf("garbage-collect idempotency: %w", err)
		}
		if report.IdempotencyRecordsDeleted, err = rowsAffected(result); err != nil {
			return report, err
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
DELETE FROM exec_command_events
WHERE command_id IN (
    SELECT command_id FROM exec_commands WHERE output_unavailable_reason = 'retention_expired'
)
`)
		if err != nil {
			return report, fmt.Errorf("delete expired command events: %w", err)
		}
		if report.CommandEventsDeleted, err = rowsAffected(result); err != nil {
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
