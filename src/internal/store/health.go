package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrHealthProbe = errors.New("SQLite health write probe failed")

type RemoteIntentBacklog struct {
	Pending   int64
	Uncertain int64
}

// CheckWritable commits a small timestamp-only row for the named component.
// This confirms that the configured authority can perform the same SQLite
// write/commit path required before acknowledging accepted work.
func (s *AuthorityStore) CheckWritable(ctx context.Context, component string) error {
	if s == nil || s.db == nil {
		return ErrHealthProbe
	}
	component = strings.TrimSpace(component)
	if component == "" || len(component) > 80 || strings.IndexByte(component, 0) >= 0 {
		return fmt.Errorf("%w: invalid component name", ErrHealthProbe)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runner_health_probes(component, checked_at) VALUES(?, ?)
ON CONFLICT(component) DO UPDATE SET checked_at = excluded.checked_at
`, component, formatStoredTime(s.now()))
	if err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return fmt.Errorf("%w: %w", ErrHealthProbe, err)
	}
	return nil
}

// CountRemoteIntentBacklog returns safe operational counts for the Mac Router
// without exposing payloads, IDs, scripts, or controller data.
func (s *AuthorityStore) CountRemoteIntentBacklog(ctx context.Context) (RemoteIntentBacklog, error) {
	if s == nil || s.db == nil {
		return RemoteIntentBacklog{}, ErrHealthProbe
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result RemoteIntentBacklog
	err := s.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN delivery_state IN ('recorded', 'dispatching') THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN delivery_state = 'uncertain' THEN 1 ELSE 0 END), 0)
		FROM local_intents
WHERE target_kind = 'remote'
`).Scan(&result.Pending, &result.Uncertain)
	if err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return RemoteIntentBacklog{}, fmt.Errorf("read Router backlog: %w", err)
	}
	return result, nil
}
