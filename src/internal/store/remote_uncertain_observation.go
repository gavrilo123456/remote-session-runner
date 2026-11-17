package store

import (
	"context"
	"database/sql"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// ListRecoverableRemoteIntentsAfter selects uncertain remote runs whose
// delivery lease expired. Abandoned dispatching attempts need separate recovery.
// It never releases a lease, changes an outcome, or makes work dispatchable.
func (s *AuthorityStore) ListRecoverableRemoteIntentsAfter(ctx context.Context, limit int, after *RemoteIntentCursor) ([]LocalIntentRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, fmt.Errorf("%w: uncertain remote limit exceeds 1000", ErrInvalidLocalIntent)
	}
	arguments := []any{formatStoredTime(s.now().UTC())}
	query := `SELECT intent_id FROM local_intents
WHERE operation = 'run' AND target_kind = 'remote' AND delivery_state = 'uncertain'
  AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?`
	if after != nil {
		if after.CreatedAt.IsZero() {
			return nil, fmt.Errorf("%w: uncertain remote cursor time", ErrInvalidLocalIntent)
		}
		id, err := domain.NewIntentID(string(after.IntentID))
		if err != nil {
			return nil, fmt.Errorf("%w: uncertain remote cursor identity", ErrInvalidLocalIntent)
		}
		query += ` AND (created_at > ? OR (created_at = ? AND intent_id > ?))`
		created := formatStoredTime(after.CreatedAt.UTC())
		arguments = append(arguments, created, created, string(id))
	}
	query += ` ORDER BY created_at, intent_id LIMIT ?`
	arguments = append(arguments, limit)
	return withStoreReadTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]LocalIntentRecord, error) {
		rows, err := connection.QueryContext(ctx, query, arguments...)
		if err != nil {
			return nil, fmt.Errorf("list recoverable remote intents: %w", err)
		}
		defer rows.Close()
		return readLocalIntentRows(ctx, connection, rows)
	})
}

// AcceptObservedRemoteIntent commits a target-found observation only while
// the same expired attempt is still current. A renewal or concurrent change
// invalidates the observation instead of overwriting newer lifecycle state.
// This is an acceptance observation, never terminal execution/cleanup proof.
func (s *AuthorityStore) AcceptObservedRemoteIntent(ctx context.Context, observed LocalIntentRecord) (LocalIntentRecord, error) {
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		current, err := readLocalIntentOnConnection(ctx, connection, observed.IntentID)
		if err != nil {
			return LocalIntentRecord{}, err
		}
		now := s.now().UTC()
		if current.Operation != localIntentRunOperation || current.Target.Kind() != domain.TargetKindRemote ||
			current.DeliveryState != LocalIntentUncertain ||
			current.LeaseExpiresAt == nil || now.Before(*current.LeaseExpiresAt) ||
			current.DeliveryState != observed.DeliveryState || current.AttemptCount != observed.AttemptCount ||
			current.LeaseOwner != observed.LeaseOwner || observed.LeaseExpiresAt == nil ||
			!current.LeaseExpiresAt.Equal(*observed.LeaseExpiresAt) || !current.UpdatedAt.Equal(observed.UpdatedAt) ||
			current.RequestHash != observed.RequestHash {
			return LocalIntentRecord{}, ErrLocalIntentLeaseLost
		}
		const reason = "remote_observed_target_found"
		if _, err := connection.ExecContext(ctx, `UPDATE local_intents SET delivery_state = 'accepted', reason = ?, updated_at = ? WHERE intent_id = ?`, reason, formatStoredTime(now), string(current.IntentID)); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("accept observed remote intent: %w", err)
		}
		if err := appendLocalIntentLifecycleOnConnection(ctx, connection, current.IntentID, LocalIntentAccepted, reason, now); err != nil {
			return LocalIntentRecord{}, err
		}
		return readLocalIntentOnConnection(ctx, connection, current.IntentID)
	})
}
