package store

import (
	"context"
	"database/sql"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

// RemoteStatusFailureCodeUnavailable records that a target accepted a
// one-off remote run, but read-only reconciliation could not establish a
// trustworthy terminal status. It deliberately contains no target error text.
const RemoteStatusFailureCodeUnavailable = "remote_status_unavailable"

// RecordRemoteStatusFailure preserves the first post-acceptance status failure
// for one remote run. Repeated reconciliation failures retain the original
// deadline anchor and do not create a lifecycle transition.
func (s *AuthorityStore) MarkAcceptedRemoteRunStatusFailure(ctx context.Context, id domain.IntentID, code string) (LocalIntentRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	if code != RemoteStatusFailureCodeUnavailable {
		return LocalIntentRecord{}, fmt.Errorf("%w: remote status failure code", ErrInvalidLocalIntent)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		intent, err := readLocalIntentOnConnection(ctx, connection, validatedID)
		if err != nil {
			return LocalIntentRecord{}, err
		}
		if intent.Operation != localIntentRunOperation || intent.Target.Kind() != domain.TargetKindRemote || intent.DeliveryState != LocalIntentAccepted {
			return LocalIntentRecord{}, fmt.Errorf("%w: remote status failure requires an accepted remote run", ErrInvalidLocalIntent)
		}
		if _, err := connection.ExecContext(ctx, `
INSERT INTO local_remote_status_failures(intent_id, first_observed_at, reason, reconciliation_attempts)
VALUES (?, ?, ?, 1)
ON CONFLICT(intent_id) DO UPDATE
SET reconciliation_attempts = reconciliation_attempts + 1
`, string(validatedID), formatStoredTime(now), code); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("record remote status failure: %w", err)
		}
		return readLocalIntentOnConnection(ctx, connection, validatedID)
	})
}

// ClearRemoteStatusFailure removes a prior marker after a later read-only
// reconciliation has returned a coherent status. It does not alter the
// accepted/reconciled delivery state or rerun a mutation.
func (s *AuthorityStore) ClearAcceptedRemoteRunStatusFailure(ctx context.Context, id domain.IntentID) (LocalIntentRecord, error) {
	validatedID, err := domain.NewIntentID(string(id))
	if err != nil {
		return LocalIntentRecord{}, fmt.Errorf("%w: intent ID: %v", ErrInvalidLocalIntent, err)
	}
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (LocalIntentRecord, error) {
		intent, err := readLocalIntentOnConnection(ctx, connection, validatedID)
		if err != nil {
			return LocalIntentRecord{}, err
		}
		if intent.Operation != localIntentRunOperation || intent.Target.Kind() != domain.TargetKindRemote || intent.DeliveryState != LocalIntentAccepted {
			return LocalIntentRecord{}, fmt.Errorf("%w: remote status failure requires an accepted remote run", ErrInvalidLocalIntent)
		}
		if _, err := connection.ExecContext(ctx, "DELETE FROM local_remote_status_failures WHERE intent_id = ?", string(validatedID)); err != nil {
			return LocalIntentRecord{}, fmt.Errorf("clear remote status failure: %w", err)
		}
		return readLocalIntentOnConnection(ctx, connection, validatedID)
	})
}
