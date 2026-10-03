package store

import (
	"context"
	"fmt"
)

// RestartPreflightMetrics is the small durable inventory that must be empty
// before an executor restart can be considered safe. It deliberately exposes
// counts only: callers do not need resource IDs, scripts, process details, or
// mailbox payloads to decide whether to refuse a restart.
type RestartPreflightMetrics struct {
	ActiveSessionSlots int64
	ActiveCommandSlots int64
	QueuedCommands     int64
	ResumableJobs      int64
}

// ReadRestartPreflightMetrics reads every durable condition through which a
// restarted queue worker could resume or start local execution. In addition to
// reservations, slots, and queued commands, a creating/accepting/awaiting or
// closing one-off job is unsafe because startup recovery can advance it even
// before a command row exists. The method is read-only and makes no authority
// change.
func (s *AuthorityStore) ReadRestartPreflightMetrics(ctx context.Context) (RestartPreflightMetrics, error) {
	if s == nil || s.db == nil {
		return RestartPreflightMetrics{}, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result RestartPreflightMetrics
	err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM exec_capacity_reservations WHERE cleanup_confirmed_at IS NULL),
  (SELECT COUNT(*) FROM exec_command_slots WHERE stop_confirmed_at IS NULL),
  (SELECT COUNT(*) FROM exec_commands WHERE state = 'queued'),
  (SELECT COUNT(*) FROM exec_jobs WHERE phase IN ('creating_session', 'accepting_command', 'awaiting_command', 'closing_session'))
`).Scan(&result.ActiveSessionSlots, &result.ActiveCommandSlots, &result.QueuedCommands, &result.ResumableJobs)
	if err != nil {
		return RestartPreflightMetrics{}, fmt.Errorf("read restart preflight metrics: %w", err)
	}
	return result, nil
}
