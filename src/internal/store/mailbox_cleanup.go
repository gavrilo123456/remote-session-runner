package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
)

var ErrMailboxCleanupState = errors.New("mailbox cleanup state is invalid")

// EnsureMailboxResponsePublishable prevents restart repair from recreating a
// terminal response after its cleanup deadline or cleanup claim.
func (s *AuthorityStore) EnsureMailboxResponsePublishable(ctx context.Context, requestID string) error {
	record, err := s.GetMailboxExchange(ctx, requestID)
	if err != nil {
		return err
	}
	if record.ResponseCleanupStartedAt != nil || record.ResponseFileRemovedAt != nil ||
		(record.ResponseCleanupAt != nil && !s.now().UTC().Before(*record.ResponseCleanupAt)) {
		return ErrMailboxResponseExpired
	}
	return nil
}

// BindMailboxEventFileReference durably records that one response uses a
// command's shared event file. References are immutable and one command file
// cannot be rebound after cleanup has started.
func (s *AuthorityStore) BindMailboxEventFileReference(ctx context.Context, requestID string, commandID domain.CommandID) error {
	if err := validateMailboxRequestID(requestID); err != nil {
		return err
	}
	validatedCommandID, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return fmt.Errorf("%w: command ID: %v", ErrMailboxResponseInvalid, err)
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		record, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
		if err != nil {
			return struct{}{}, err
		}
		if record.AvailableEventSequence == nil || *record.AvailableEventSequence < 1 {
			return struct{}{}, fmt.Errorf("%w: response has no event cursor", ErrMailboxResponseInvalid)
		}
		if record.ResponseCleanupStartedAt != nil || record.ResponseFileRemovedAt != nil ||
			(record.ResponseCleanupAt != nil && !now.Before(*record.ResponseCleanupAt)) {
			return struct{}{}, ErrMailboxResponseExpired
		}
		var commandState string
		if err := connection.QueryRowContext(ctx, `SELECT state FROM exec_commands WHERE command_id = ?`, string(validatedCommandID)).Scan(&commandState); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, ErrCommandNotFound
			}
			return struct{}{}, fmt.Errorf("read event-file command: %w", err)
		}
		var cleanupStarted, fileRemovedAt sql.NullString
		err = connection.QueryRowContext(ctx, `SELECT cleanup_started_at, file_removed_at FROM mailbox_event_file_cleanup WHERE command_id = ?`, string(validatedCommandID)).Scan(&cleanupStarted, &fileRemovedAt)
		if err == nil {
			if !fileRemovedAt.Valid {
				return struct{}{}, ErrMailboxEventReferenceExpired
			}
			if _, err := connection.ExecContext(ctx, `DELETE FROM mailbox_event_file_cleanup WHERE command_id = ? AND file_removed_at IS NOT NULL`, string(validatedCommandID)); err != nil {
				return struct{}{}, fmt.Errorf("reactivate cleaned event file: %w", err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, fmt.Errorf("read event-file cleanup state: %w", err)
		}
		var existing string
		err = connection.QueryRowContext(ctx, `SELECT command_id FROM mailbox_event_file_references WHERE request_id = ?`, requestID).Scan(&existing)
		if err == nil {
			if existing != string(validatedCommandID) {
				return struct{}{}, ErrMailboxExchangeConflict
			}
			return struct{}{}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, fmt.Errorf("read event-file reference: %w", err)
		}
		if _, err := connection.ExecContext(ctx, `INSERT INTO mailbox_event_file_references (request_id, command_id, created_at) VALUES (?, ?, ?)`, requestID, string(validatedCommandID), formatStoredTime(now)); err != nil {
			return struct{}{}, fmt.Errorf("record event-file reference: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

// ClaimMailboxResponsesForCleanup claims expired terminal response files. A
// claim remains visible after a crash so the next cleanup pass retries the
// idempotent unlink instead of allowing a projector to recreate the file.
func (s *AuthorityStore) ClaimMailboxResponsesForCleanup(ctx context.Context) ([]string, error) {
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]string, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT request_id FROM mailbox_exchanges
WHERE request_state IN ('complete', 'rejected', 'indeterminate') AND response_revision > 0
  AND response_file_removed_at IS NULL
ORDER BY request_id
`)
		if err != nil {
			return nil, fmt.Errorf("list terminal mailbox responses for cleanup: %w", err)
		}
		var requestIDs []string
		for rows.Next() {
			var requestID string
			if err := rows.Scan(&requestID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan mailbox response cleanup ID: %w", err)
			}
			requestIDs = append(requestIDs, requestID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read mailbox response cleanup IDs: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close mailbox response cleanup IDs: %w", err)
		}
		var claimed []string
		for _, requestID := range requestIDs {
			record, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
			if err != nil {
				return nil, err
			}
			if record.ResponseCleanupAt == nil || record.ResponseCleanupAt.After(now) {
				continue
			}
			if _, err := connection.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_cleanup_started_at = COALESCE(response_cleanup_started_at, ?) WHERE request_id = ? AND response_file_removed_at IS NULL`, formatStoredTime(now), requestID); err != nil {
				return nil, fmt.Errorf("claim mailbox response cleanup: %w", err)
			}
			claimed = append(claimed, requestID)
		}
		return claimed, nil
	})
}

// MarkMailboxResponseFileRemoved completes the durable half of response-file
// cleanup. Repeating the marker after an unlink is harmless.
func (s *AuthorityStore) MarkMailboxResponseFileRemoved(ctx context.Context, requestID string) error {
	now := s.now().UTC()
	_, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		result, err := connection.ExecContext(ctx, `UPDATE mailbox_exchanges
SET response_file_removed_at = COALESCE(response_file_removed_at, ?)
WHERE request_id = ? AND response_cleanup_started_at IS NOT NULL`, formatStoredTime(now), requestID)
		if err != nil {
			return struct{}{}, fmt.Errorf("mark mailbox response file removed: %w", err)
		}
		count, err := rowsAffected(result)
		if err != nil {
			return struct{}{}, err
		}
		if count == 0 {
			var exists int
			if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges WHERE request_id = ? AND response_file_removed_at IS NOT NULL`, requestID).Scan(&exists); err != nil {
				return struct{}{}, fmt.Errorf("verify mailbox response cleanup: %w", err)
			}
			if exists == 0 {
				return struct{}{}, ErrMailboxCleanupState
			}
		}
		return struct{}{}, nil
	})
	return err
}

// ClaimMailboxEventFilesForCleanup claims only terminal command files for
// which every durable response reference has reached its own cleanup deadline.
func (s *AuthorityStore) ClaimMailboxEventFilesForCleanup(ctx context.Context) ([]domain.CommandID, error) {
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) ([]domain.CommandID, error) {
		rows, err := connection.QueryContext(ctx, `SELECT DISTINCT command_id FROM mailbox_event_file_references ORDER BY command_id`)
		if err != nil {
			return nil, fmt.Errorf("list shared mailbox event files: %w", err)
		}
		var commandIDs []string
		for rows.Next() {
			var commandID string
			if err := rows.Scan(&commandID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan shared mailbox event file: %w", err)
			}
			commandIDs = append(commandIDs, commandID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read shared mailbox event files: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close shared mailbox event files: %w", err)
		}
		var claimed []domain.CommandID
		for _, commandID := range commandIDs {
			var commandState string
			if err := connection.QueryRowContext(ctx, `SELECT state FROM exec_commands WHERE command_id = ?`, commandID).Scan(&commandState); err != nil {
				return nil, fmt.Errorf("read mailbox event command: %w", err)
			}
			if !mailboxCommandStateTerminal(commandState) {
				continue
			}
			refs, err := connection.QueryContext(ctx, `SELECT request_id FROM mailbox_event_file_references WHERE command_id = ? ORDER BY request_id`, commandID)
			if err != nil {
				return nil, fmt.Errorf("list mailbox event references: %w", err)
			}
			var requestIDs []string
			for refs.Next() {
				var requestID string
				if err := refs.Scan(&requestID); err != nil {
					_ = refs.Close()
					return nil, fmt.Errorf("scan mailbox event reference: %w", err)
				}
				requestIDs = append(requestIDs, requestID)
			}
			if err := refs.Err(); err != nil {
				_ = refs.Close()
				return nil, fmt.Errorf("read mailbox event references: %w", err)
			}
			if err := refs.Close(); err != nil {
				return nil, fmt.Errorf("close mailbox event references: %w", err)
			}
			eligible := len(requestIDs) > 0
			for _, requestID := range requestIDs {
				record, err := readMailboxExchangeOnConnection(ctx, connection, requestID)
				if err != nil {
					return nil, err
				}
				if record.State != MailboxExchangeComplete && record.State != MailboxExchangeRejected && record.State != MailboxExchangeIndeterminate {
					eligible = false
					break
				}
				if record.ResponseCleanupAt == nil || record.ResponseCleanupAt.After(now) {
					eligible = false
					break
				}
			}
			if !eligible {
				continue
			}
			if _, err := connection.ExecContext(ctx, `INSERT OR IGNORE INTO mailbox_event_file_cleanup (command_id, cleanup_started_at) VALUES (?, ?)`, commandID, formatStoredTime(now)); err != nil {
				return nil, fmt.Errorf("claim mailbox event-file cleanup: %w", err)
			}
			var removedAt sql.NullString
			if err := connection.QueryRowContext(ctx, `SELECT file_removed_at FROM mailbox_event_file_cleanup WHERE command_id = ?`, commandID).Scan(&removedAt); err != nil {
				return nil, fmt.Errorf("read mailbox event-file cleanup claim: %w", err)
			}
			if !removedAt.Valid {
				claimed = append(claimed, domain.CommandID(commandID))
			}
		}
		return claimed, nil
	})
}

// MarkMailboxEventFileRemoved completes a claimed shared-event unlink.
func (s *AuthorityStore) MarkMailboxEventFileRemoved(ctx context.Context, commandID domain.CommandID) error {
	now := s.now().UTC()
	_, err := withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		result, err := connection.ExecContext(ctx, `UPDATE mailbox_event_file_cleanup
SET file_removed_at = COALESCE(file_removed_at, ?)
WHERE command_id = ?`, formatStoredTime(now), string(commandID))
		if err != nil {
			return struct{}{}, fmt.Errorf("mark mailbox event file removed: %w", err)
		}
		count, err := rowsAffected(result)
		if err != nil {
			return struct{}{}, err
		}
		if count == 0 {
			return struct{}{}, ErrMailboxCleanupState
		}
		return struct{}{}, nil
	})
	return err
}

func mailboxCommandStateTerminal(state string) bool {
	switch state {
	case string(domain.CommandStateSucceeded), string(domain.CommandStateFailed), string(domain.CommandStateCancelled), string(domain.CommandStateTimedOut), string(domain.CommandStateRejected), string(domain.CommandStateLost):
		return true
	default:
		return false
	}
}
