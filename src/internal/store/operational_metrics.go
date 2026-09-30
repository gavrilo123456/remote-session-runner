package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlitedriver "modernc.org/sqlite"
)

// OperationalMetrics is a bounded snapshot of durable operational state.
// It contains no resource identifiers, payloads, controller labels, or paths.
type OperationalMetrics struct {
	ActiveSessionSlots       int64
	ActiveCommandSlots       int64
	QueuedCommands           int64
	QueuedIntents            int64
	DispatchAttemptsTotal    int64
	ReconciliationAgeSeconds int64
	EventLagEvents           int64
	EventGapsTotal           int64
	OutputTruncationsTotal   int64
	StorageErrorsTotal       int64
	CleanupFailuresTotal     int64
	MailboxBacklog           int64
}

// ReadOperationalMetrics reads durable counters and gauges in one bounded
// SQLite statement. A missing/stale transport does not alter local durable
// queue counts; those remain visible until reconciled.
func (s *AuthorityStore) ReadOperationalMetrics(ctx context.Context) (OperationalMetrics, error) {
	if s == nil || s.db == nil {
		return OperationalMetrics{}, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result OperationalMetrics
	var oldestUncertain sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM exec_capacity_reservations WHERE cleanup_confirmed_at IS NULL),
  (SELECT COUNT(*) FROM exec_command_slots WHERE stop_confirmed_at IS NULL),
  (SELECT COUNT(*) FROM exec_commands WHERE state = 'queued'),
  (SELECT COUNT(*) FROM local_intents WHERE delivery_state IN ('recorded', 'dispatching', 'uncertain')),
  (SELECT COALESCE(SUM(attempt_count), 0) FROM local_intents),
  (SELECT MIN(updated_at) FROM local_intents WHERE delivery_state = 'uncertain'),
  (SELECT COALESCE(SUM(
      CASE WHEN p.final_event_sequence > COALESCE(c.last_sequence, 0)
           THEN p.final_event_sequence - COALESCE(c.last_sequence, 0) ELSE 0 END
    ), 0)
   FROM local_remote_command_projections p
   LEFT JOIN local_remote_event_cursors c ON c.command_id = p.command_id
   WHERE p.final_event_sequence IS NOT NULL AND p.output_unavailable_reason = ''),
  (SELECT COUNT(*) FROM local_remote_event_gaps),
  (SELECT
      (SELECT COUNT(*) FROM exec_command_events WHERE event_type = 'output_truncated') +
      (SELECT COUNT(*) FROM local_remote_events WHERE event_type = 'output_truncated')),
  (SELECT COUNT(*) FROM mailbox_exchanges WHERE request_state = 'accepted')
`).Scan(&result.ActiveSessionSlots, &result.ActiveCommandSlots, &result.QueuedCommands,
		&result.QueuedIntents, &result.DispatchAttemptsTotal, &oldestUncertain,
		&result.EventLagEvents, &result.EventGapsTotal, &result.OutputTruncationsTotal,
		&result.MailboxBacklog)
	if err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return OperationalMetrics{}, fmt.Errorf("read operational metrics: %w", err)
	}
	if oldestUncertain.Valid {
		updated, err := parseStoredTime(oldestUncertain.String)
		if err != nil {
			return OperationalMetrics{}, fmt.Errorf("decode oldest uncertain intent time: %w", err)
		}
		age := s.now().UTC().Sub(updated)
		if age > 0 {
			result.ReconciliationAgeSeconds = int64(age / 1e9)
		}
	}
	result.StorageErrorsTotal = s.storageErrors.Load()
	result.CleanupFailuresTotal = s.cleanupFailures.Load()
	return result, nil
}

// CountMailboxBacklogByInbox reports accepted durable exchanges for the named
// mailbox namespaces. Callers supply configured mailbox IDs, so the returned
// map has a stable, low-cardinality entry for every configured inbox, even
// when its durable backlog is zero. Ready marker files are intentionally not
// counted here; the Mac ingress owns those filesystem projections.
func (s *AuthorityStore) CountMailboxBacklogByInbox(ctx context.Context, mailboxIDs []string) (map[string]int64, error) {
	if s == nil || s.db == nil {
		return nil, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	counts := make(map[string]int64, len(mailboxIDs))
	if len(mailboxIDs) == 0 {
		return counts, nil
	}
	placeholders := make([]string, 0, len(mailboxIDs))
	arguments := make([]any, 0, len(mailboxIDs))
	for _, mailboxID := range mailboxIDs {
		if err := validateMailboxID(mailboxID); err != nil {
			return nil, err
		}
		if _, exists := counts[mailboxID]; exists {
			return nil, ErrMailboxExchangeInvalid
		}
		counts[mailboxID] = 0
		placeholders = append(placeholders, "?")
		arguments = append(arguments, mailboxID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT mailbox_id, COUNT(*)
FROM mailbox_exchanges
WHERE request_state = 'accepted' AND mailbox_id IN (`+strings.Join(placeholders, ",")+`)
GROUP BY mailbox_id`, arguments...)
	if err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return nil, fmt.Errorf("count mailbox backlog by inbox: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mailboxID string
		var count int64
		if err := rows.Scan(&mailboxID, &count); err != nil {
			return nil, fmt.Errorf("scan mailbox backlog by inbox: %w", err)
		}
		if _, exists := counts[mailboxID]; !exists {
			return nil, ErrMailboxExchangeInvalid
		}
		counts[mailboxID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailbox backlog by inbox: %w", err)
	}
	return counts, nil
}

// RecordCleanupFailure increments the process-local cleanup error counter.
func (s *AuthorityStore) RecordCleanupFailure() {
	if s != nil {
		s.cleanupFailures.Add(1)
	}
}

// IsSQLiteError identifies a database-engine failure after store methods have
// added their operation context. Validation, authorization, and capacity
// errors are not counted as storage failures.
func IsSQLiteError(err error) bool {
	var sqliteError *sqlitedriver.Error
	return errors.As(err, &sqliteError)
}
