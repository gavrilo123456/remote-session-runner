package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
)

var ErrMailboxCleanupState = errors.New("mailbox cleanup state is invalid")

const (
	maxEventCleanupWorkItemsPerBatch = 4
	maxEventCleanupRefsPerWorkItem   = 32
)

// EnsureMailboxResponsePublishable prevents restart repair from recreating a
// terminal response after its cleanup deadline or cleanup claim.
func (s *AuthorityStore) EnsureMailboxResponsePublishable(ctx context.Context, requestID string) error {
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return err
	}
	return s.EnsureMailboxResponsePublishableInMailbox(ctx, ref)
}

// EnsureMailboxResponsePublishableInMailbox prevents recovery in one root
// from recreating an exchange owned by another mailbox.
func (s *AuthorityStore) EnsureMailboxResponsePublishableInMailbox(ctx context.Context, ref MailboxExchangeRef) error {
	record, err := s.GetMailboxExchangeInMailbox(ctx, ref)
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
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return err
	}
	return s.BindMailboxEventFileReferenceInMailbox(ctx, ref, commandID)
}

// BindMailboxEventFileReferenceInMailbox records the reference inside the
// physical mailbox namespace that will own the projected file.
func (s *AuthorityStore) BindMailboxEventFileReferenceInMailbox(ctx context.Context, ref MailboxExchangeRef, commandID domain.CommandID) error {
	validatedRef, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return err
	}
	validatedCommandID, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return fmt.Errorf("%w: command ID: %v", ErrMailboxResponseInvalid, err)
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		return struct{}{}, bindMailboxEventFileReferenceOnConnection(ctx, connection, validatedRef, validatedCommandID, now)
	})
	return err
}

func bindMailboxEventFileReferenceOnConnection(ctx context.Context, connection *sql.Conn, ref MailboxExchangeRef, commandID domain.CommandID, now time.Time) error {
	record, err := readMailboxExchangeOnConnection(ctx, connection, ref)
	if err != nil {
		return err
	}
	if record.State == MailboxExchangeAccepted || record.AvailableEventSequence == nil || *record.AvailableEventSequence < 1 || record.ResponseCleanupAt == nil {
		return fmt.Errorf("%w: terminal response has no event cursor or cleanup deadline", ErrMailboxResponseInvalid)
	}
	if record.ResponseCleanupStartedAt != nil || record.ResponseFileRemovedAt != nil || !now.Before(*record.ResponseCleanupAt) {
		return ErrMailboxResponseExpired
	}

	var localState, remoteState string
	localErr := connection.QueryRowContext(ctx, `SELECT state FROM exec_commands WHERE command_id = ?`, string(commandID)).Scan(&localState)
	remoteErr := connection.QueryRowContext(ctx, `SELECT command_state FROM local_remote_command_projections WHERE command_id = ?`, string(commandID)).Scan(&remoteState)
	localFound, remoteFound := localErr == nil, remoteErr == nil
	if localErr != nil && !errors.Is(localErr, sql.ErrNoRows) {
		return fmt.Errorf("read local event-file command: %w", localErr)
	}
	if remoteErr != nil && !errors.Is(remoteErr, sql.ErrNoRows) {
		return fmt.Errorf("read mirrored event-file command: %w", remoteErr)
	}
	if localFound == remoteFound {
		if localFound {
			return fmt.Errorf("%w: command ID is ambiguous across local and remote stores", ErrMailboxExchangeConflict)
		}
		return ErrCommandNotFound
	}
	remote := remoteFound
	if record.EventFileCommandID != "" && record.EventFileCommandID != string(commandID) {
		return ErrMailboxExchangeConflict
	}

	cleanupTable := "mailbox_event_file_cleanup"
	referenceTable := "mailbox_event_file_references"
	if remote {
		cleanupTable = "mailbox_remote_event_file_cleanup"
		referenceTable = "mailbox_remote_event_file_references"
	}
	var cleanupStarted, fileRemovedAt sql.NullString
	err = connection.QueryRowContext(ctx, `SELECT cleanup_started_at, file_removed_at FROM `+cleanupTable+` WHERE mailbox_id = ? AND command_id = ?`, record.MailboxID, string(commandID)).Scan(&cleanupStarted, &fileRemovedAt)
	if err == nil {
		if !fileRemovedAt.Valid {
			return ErrMailboxEventReferenceExpired
		}
		if _, err := connection.ExecContext(ctx, `DELETE FROM `+cleanupTable+` WHERE mailbox_id = ? AND command_id = ? AND file_removed_at IS NOT NULL`, record.MailboxID, string(commandID)); err != nil {
			return fmt.Errorf("reactivate cleaned event file: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read event-file cleanup state: %w", err)
	}

	var localReference, remoteReference sql.NullString
	if err := connection.QueryRowContext(ctx, `SELECT command_id FROM mailbox_event_file_references WHERE exchange_id = ?`, record.ExchangeID).Scan(&localReference); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read local event-file reference: %w", err)
	}
	if err := connection.QueryRowContext(ctx, `SELECT command_id FROM mailbox_remote_event_file_references WHERE exchange_id = ?`, record.ExchangeID).Scan(&remoteReference); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read remote event-file reference: %w", err)
	}
	if localReference.Valid || remoteReference.Valid {
		existing := localReference
		if remoteReference.Valid {
			if existing.Valid {
				return ErrMailboxExchangeConflict
			}
			existing = remoteReference
		}
		if existing.String != string(commandID) || remoteReference.Valid != remote {
			return ErrMailboxExchangeConflict
		}
		return resetMailboxEventCleanupWork(ctx, connection, record.MailboxID, commandID, remote)
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO `+referenceTable+` (exchange_id, command_id, created_at) VALUES (?, ?, ?)`, record.ExchangeID, string(commandID), formatStoredTime(now)); err != nil {
		return fmt.Errorf("record event-file reference: %w", err)
	}
	return resetMailboxEventCleanupWork(ctx, connection, record.MailboxID, commandID, remote)
}

func resetMailboxEventCleanupWork(ctx context.Context, connection *sql.Conn, mailboxID string, commandID domain.CommandID, remote bool) error {
	remoteValue := 0
	if remote {
		remoteValue = 1
	}
	_, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_event_cleanup_work (
    mailbox_id, command_id, is_remote, last_exchange_id, scanned_refs,
    scan_complete, all_terminal, latest_cleanup_at
) VALUES (?, ?, ?, '', 0, 0, 1, NULL)
ON CONFLICT(mailbox_id, command_id, is_remote) DO UPDATE SET
    last_exchange_id = '', scanned_refs = 0, scan_complete = 0,
    all_terminal = 1, latest_cleanup_at = NULL
`, mailboxID, string(commandID), remoteValue)
	if err != nil {
		return fmt.Errorf("queue mailbox event-file cleanup work: %w", err)
	}
	return nil
}

// MailboxResponseCommandEvents reads the immutable event prefix advertised by
// one still-live mailbox response. It may read payloads retained only for that
// response after ordinary output expiry; ordinary command reads remain
// retention-filtered.
func (s *AuthorityStore) MailboxResponseCommandEvents(ctx context.Context, requestID string, commandID domain.CommandID) (CommandRecord, []CommandEventRecord, error) {
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return CommandRecord{}, nil, err
	}
	return s.MailboxResponseCommandEventsInMailbox(ctx, ref, commandID)
}

// MailboxResponseCommandEventsInMailbox reads only a response pinned by the
// supplied mailbox exchange.
func (s *AuthorityStore) MailboxResponseCommandEventsInMailbox(ctx context.Context, ref MailboxExchangeRef, commandID domain.CommandID) (CommandRecord, []CommandEventRecord, error) {
	validatedRef, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return CommandRecord{}, nil, err
	}
	validatedCommandID, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return CommandRecord{}, nil, fmt.Errorf("%w: command ID: %v", ErrMailboxResponseInvalid, err)
	}
	now := s.now().UTC()
	type commandEvents struct {
		command CommandRecord
		events  []CommandEventRecord
	}
	result, err := withStoreReadTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (commandEvents, error) {
		exchange, err := readMailboxExchangeOnConnection(ctx, connection, validatedRef)
		if err != nil {
			return commandEvents{}, err
		}
		if exchange.State == MailboxExchangeAccepted || exchange.AvailableEventSequence == nil || *exchange.AvailableEventSequence < 1 || exchange.EventFileCommandID != string(validatedCommandID) {
			return commandEvents{}, ErrMailboxResponseInvalid
		}
		if exchange.ResponseCleanupAt == nil || exchange.ResponseCleanupStartedAt != nil || exchange.ResponseFileRemovedAt != nil || !now.Before(*exchange.ResponseCleanupAt) {
			return commandEvents{}, ErrMailboxResponseExpired
		}
		var localReference, remoteReference sql.NullString
		if err := connection.QueryRowContext(ctx, `SELECT command_id FROM mailbox_event_file_references WHERE exchange_id = ?`, exchange.ExchangeID).Scan(&localReference); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return commandEvents{}, fmt.Errorf("read local mailbox event reference: %w", err)
		}
		if err := connection.QueryRowContext(ctx, `SELECT command_id FROM mailbox_remote_event_file_references WHERE exchange_id = ?`, exchange.ExchangeID).Scan(&remoteReference); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return commandEvents{}, fmt.Errorf("read mirrored mailbox event reference: %w", err)
		}
		if localReference.Valid == remoteReference.Valid {
			return commandEvents{}, ErrMailboxResponseInvalid
		}
		through := *exchange.AvailableEventSequence
		var command CommandRecord
		var events []CommandEventRecord
		if localReference.Valid {
			if localReference.String != string(validatedCommandID) {
				return commandEvents{}, ErrMailboxResponseInvalid
			}
			command, err = readCommandOnConnection(ctx, connection, validatedCommandID)
			if err != nil {
				return commandEvents{}, err
			}
			events, err = readCommandEventsOnConnection(ctx, connection, validatedCommandID, -1)
		} else {
			if remoteReference.String != string(validatedCommandID) {
				return commandEvents{}, ErrMailboxResponseInvalid
			}
			projection, projectionErr := readRemoteCommandProjectionOnConnection(ctx, connection, validatedCommandID)
			if projectionErr != nil {
				return commandEvents{}, projectionErr
			}
			command = CommandRecord{
				CommandID: projection.CommandID, SessionID: projection.SessionID,
				Ordinal: projection.Ordinal, State: projection.State,
				ExitCode: projection.ExitCode, FinalEventSequence: projection.FinalEventSequence,
				OutputComplete: projection.OutputComplete, OutputTruncated: projection.OutputTruncated,
				OutputUnavailableReason: projection.OutputUnavailableReason,
			}
			events, err = readMirroredCommandEventsOnConnection(ctx, connection, validatedCommandID, through)
		}
		if err != nil {
			return commandEvents{}, err
		}
		if int64(len(events)) < through {
			return commandEvents{}, fmt.Errorf("%w: pinned event prefix ends at %d, response advertises %d", ErrCommandReplayGap, len(events), through)
		}
		return commandEvents{command: command, events: events[:through]}, nil
	})
	return result.command, result.events, err
}

func readMirroredCommandEventsOnConnection(ctx context.Context, connection *sql.Conn, commandID domain.CommandID, through int64) ([]CommandEventRecord, error) {
	rows, err := connection.QueryContext(ctx, `
SELECT sequence, event_type, payload, byte_count, occurred_at
FROM local_remote_events WHERE command_id = ? ORDER BY sequence LIMIT ?
`, string(commandID), through)
	if err != nil {
		return nil, fmt.Errorf("read pinned mirrored events: %w", err)
	}
	defer rows.Close()
	events := make([]CommandEventRecord, 0, through)
	expected := int64(1)
	for rows.Next() {
		remoteEvent, err := scanRemoteEvent(rows, commandID)
		if err != nil {
			return nil, err
		}
		if remoteEvent.Sequence != expected {
			return nil, fmt.Errorf("%w: pinned mirror expected sequence %d, got %d", ErrRemoteEventGap, expected, remoteEvent.Sequence)
		}
		events = append(events, CommandEventRecord{
			CommandID: commandID, Sequence: remoteEvent.Sequence, Type: remoteEvent.Type,
			Payload: append([]byte(nil), remoteEvent.Payload...), ByteCount: remoteEvent.ByteCount,
			OccurredAt: remoteEvent.OccurredAt,
		})
		expected++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pinned mirrored events: %w", err)
	}
	return events, nil
}

// ClaimMailboxResponsesForCleanup claims expired terminal response files. A
// claim remains visible after a crash so the next cleanup pass retries the
// idempotent unlink instead of allowing a projector to recreate the file.
func (s *AuthorityStore) ClaimMailboxResponsesForCleanup(ctx context.Context) ([]string, error) {
	refs, err := s.ClaimMailboxResponsesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil {
		return nil, err
	}
	requestIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		requestIDs = append(requestIDs, ref.ClientRequestID)
	}
	return requestIDs, nil
}

// ClaimMailboxResponsesForCleanupInMailbox claims terminal response files in
// exactly one physical mailbox root.
func (s *AuthorityStore) ClaimMailboxResponsesForCleanupInMailbox(ctx context.Context, mailboxID string) ([]MailboxExchangeRef, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]MailboxExchangeRef, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT client_request_id FROM mailbox_exchanges
WHERE mailbox_id = ? AND request_state IN ('complete', 'rejected', 'indeterminate') AND response_revision > 0
  AND response_file_removed_at IS NULL
ORDER BY exchange_id
`, mailboxID)
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
		var claimed []MailboxExchangeRef
		for _, requestID := range requestIDs {
			ref := MailboxExchangeRef{MailboxID: mailboxID, ClientRequestID: requestID}
			record, err := readMailboxExchangeOnConnection(ctx, connection, ref)
			if err != nil {
				return nil, err
			}
			if record.ResponseCleanupAt == nil || record.ResponseCleanupAt.After(now) {
				continue
			}
			if _, err := connection.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_cleanup_started_at = COALESCE(response_cleanup_started_at, ?) WHERE exchange_id = ? AND response_file_removed_at IS NULL`, formatStoredTime(now), record.ExchangeID); err != nil {
				return nil, fmt.Errorf("claim mailbox response cleanup: %w", err)
			}
			claimed = append(claimed, ref)
		}
		return claimed, nil
	})
}

// MarkMailboxResponseFileRemoved completes the durable half of response-file
// cleanup. Repeating the marker after an unlink is harmless.
func (s *AuthorityStore) MarkMailboxResponseFileRemoved(ctx context.Context, requestID string) error {
	ref, err := defaultMailboxExchangeRef(requestID)
	if err != nil {
		return err
	}
	return s.MarkMailboxResponseFileRemovedInMailbox(ctx, ref)
}

// MarkMailboxResponseFileRemovedInMailbox marks only the response file that
// was removed from the caller's mailbox root.
func (s *AuthorityStore) MarkMailboxResponseFileRemovedInMailbox(ctx context.Context, ref MailboxExchangeRef) error {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		record, err := readMailboxExchangeOnConnection(ctx, connection, validated)
		if err != nil {
			return struct{}{}, err
		}
		result, err := connection.ExecContext(ctx, `UPDATE mailbox_exchanges
SET response_file_removed_at = COALESCE(response_file_removed_at, ?)
WHERE exchange_id = ? AND response_cleanup_started_at IS NOT NULL`, formatStoredTime(now), record.ExchangeID)
		if err != nil {
			return struct{}{}, fmt.Errorf("mark mailbox response file removed: %w", err)
		}
		count, err := rowsAffected(result)
		if err != nil {
			return struct{}{}, err
		}
		if count == 0 {
			var exists int
			if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges WHERE exchange_id = ? AND response_file_removed_at IS NOT NULL`, record.ExchangeID).Scan(&exists); err != nil {
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
	refs, err := s.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil {
		return nil, err
	}
	commandIDs := make([]domain.CommandID, 0, len(refs))
	for _, ref := range refs {
		commandIDs = append(commandIDs, ref.CommandID)
	}
	return commandIDs, nil
}

// ClaimMailboxEventFilesForCleanupInMailbox claims at most a bounded batch of
// event files after every response reference for each command has reached its
// deadline. Durable keyset progress avoids rescanning completed history and
// eventually advances beyond large or not-yet-due command groups.
func (s *AuthorityStore) ClaimMailboxEventFilesForCleanupInMailbox(ctx context.Context, mailboxID string) ([]MailboxEventRef, error) {
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) ([]MailboxEventRef, error) {
		work, err := nextMailboxEventCleanupWork(ctx, connection, mailboxID)
		if err != nil {
			return nil, err
		}
		claimed := make([]MailboxEventRef, 0, len(work))
		for _, item := range work {
			if err := saveMailboxEventCleanupCursor(ctx, connection, mailboxID, item.commandID, item.remote); err != nil {
				return nil, err
			}
			ready, err := advanceMailboxEventCleanupWork(ctx, connection, mailboxID, item, now)
			if err != nil {
				return nil, err
			}
			if !ready {
				continue
			}
			cleanupTable := "mailbox_event_file_cleanup"
			if item.remote {
				cleanupTable = "mailbox_remote_event_file_cleanup"
			}
			if _, err := connection.ExecContext(ctx, `INSERT OR IGNORE INTO `+cleanupTable+` (mailbox_id, command_id, cleanup_started_at) VALUES (?, ?, ?)`, mailboxID, item.commandID, formatStoredTime(now)); err != nil {
				return nil, fmt.Errorf("claim mailbox event-file cleanup: %w", err)
			}
			var removedAt sql.NullString
			if err := connection.QueryRowContext(ctx, `SELECT file_removed_at FROM `+cleanupTable+` WHERE mailbox_id = ? AND command_id = ?`, mailboxID, item.commandID).Scan(&removedAt); err != nil {
				return nil, fmt.Errorf("read mailbox event-file cleanup claim: %w", err)
			}
			if !removedAt.Valid {
				claimed = append(claimed, MailboxEventRef{MailboxID: mailboxID, CommandID: domain.CommandID(item.commandID)})
			}
		}
		return claimed, nil
	})
}

type mailboxEventCleanupWork struct {
	commandID string
	remote    bool
}

func nextMailboxEventCleanupWork(ctx context.Context, connection *sql.Conn, mailboxID string) ([]mailboxEventCleanupWork, error) {
	var lastCommand string
	var lastRemote int
	err := connection.QueryRowContext(ctx, `SELECT last_command_id, last_is_remote FROM mailbox_event_cleanup_work_cursor WHERE mailbox_id = ?`, mailboxID).Scan(&lastCommand, &lastRemote)
	if errors.Is(err, sql.ErrNoRows) {
		lastCommand, lastRemote = "", -1
	} else if err != nil {
		return nil, fmt.Errorf("read mailbox event cleanup cursor: %w", err)
	}
	selectAfter := func() ([]mailboxEventCleanupWork, error) {
		rows, err := connection.QueryContext(ctx, `
SELECT command_id, is_remote
FROM mailbox_event_cleanup_work
WHERE mailbox_id = ? AND (command_id > ? OR (command_id = ? AND is_remote > ?))
ORDER BY command_id, is_remote LIMIT ?
`, mailboxID, lastCommand, lastCommand, lastRemote, maxEventCleanupWorkItemsPerBatch)
		if err != nil {
			return nil, fmt.Errorf("list next mailbox event cleanup work: %w", err)
		}
		defer rows.Close()
		var work []mailboxEventCleanupWork
		for rows.Next() {
			var item mailboxEventCleanupWork
			var remote int
			if err := rows.Scan(&item.commandID, &remote); err != nil {
				return nil, fmt.Errorf("scan mailbox event cleanup work: %w", err)
			}
			item.remote = remote == 1
			work = append(work, item)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate mailbox event cleanup work: %w", err)
		}
		return work, nil
	}
	work, err := selectAfter()
	if err != nil || len(work) == maxEventCleanupWorkItemsPerBatch || (lastCommand == "" && lastRemote == -1) {
		return work, err
	}
	if len(work) == 0 {
		rows, err := connection.QueryContext(ctx, `
SELECT command_id, is_remote
FROM mailbox_event_cleanup_work
WHERE mailbox_id = ?
ORDER BY command_id, is_remote LIMIT ?
`, mailboxID, maxEventCleanupWorkItemsPerBatch)
		if err != nil {
			return nil, fmt.Errorf("restart mailbox event cleanup cursor: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item mailboxEventCleanupWork
			var remote int
			if err := rows.Scan(&item.commandID, &remote); err != nil {
				return nil, fmt.Errorf("scan restarted mailbox event cleanup work: %w", err)
			}
			item.remote = remote == 1
			work = append(work, item)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate restarted mailbox event cleanup work: %w", err)
		}
		return work, nil
	}
	remaining := maxEventCleanupWorkItemsPerBatch - len(work)
	rows, err := connection.QueryContext(ctx, `
SELECT command_id, is_remote
FROM mailbox_event_cleanup_work
WHERE mailbox_id = ? AND (command_id < ? OR (command_id = ? AND is_remote < ?))
ORDER BY command_id, is_remote LIMIT ?
`, mailboxID, lastCommand, lastCommand, lastRemote, remaining)
	if err != nil {
		return nil, fmt.Errorf("wrap mailbox event cleanup cursor: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item mailboxEventCleanupWork
		var remote int
		if err := rows.Scan(&item.commandID, &remote); err != nil {
			return nil, fmt.Errorf("scan wrapped mailbox event cleanup work: %w", err)
		}
		item.remote = remote == 1
		work = append(work, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wrapped mailbox event cleanup work: %w", err)
	}
	return work, nil
}

func saveMailboxEventCleanupCursor(ctx context.Context, connection *sql.Conn, mailboxID, commandID string, remote bool) error {
	remoteValue := 0
	if remote {
		remoteValue = 1
	}
	_, err := connection.ExecContext(ctx, `
INSERT INTO mailbox_event_cleanup_work_cursor (mailbox_id, last_command_id, last_is_remote)
VALUES (?, ?, ?)
ON CONFLICT(mailbox_id) DO UPDATE SET last_command_id = excluded.last_command_id,
    last_is_remote = excluded.last_is_remote
`, mailboxID, commandID, remoteValue)
	if err != nil {
		return fmt.Errorf("advance mailbox event cleanup cursor: %w", err)
	}
	return nil
}

func advanceMailboxEventCleanupWork(ctx context.Context, connection *sql.Conn, mailboxID string, item mailboxEventCleanupWork, now time.Time) (bool, error) {
	commandTerminal, err := mailboxEventCommandTerminal(ctx, connection, item.commandID, item.remote)
	if err != nil {
		return false, err
	}
	if !commandTerminal {
		return false, nil
	}
	var lastExchange string
	var scanned int
	var scanComplete, allTerminal int
	var latestStored sql.NullString
	remoteValue := 0
	if item.remote {
		remoteValue = 1
	}
	err = connection.QueryRowContext(ctx, `
SELECT last_exchange_id, scanned_refs, scan_complete, all_terminal, latest_cleanup_at
FROM mailbox_event_cleanup_work
WHERE mailbox_id = ? AND command_id = ? AND is_remote = ?
`, mailboxID, item.commandID, remoteValue).Scan(&lastExchange, &scanned, &scanComplete, &allTerminal, &latestStored)
	if err != nil {
		return false, fmt.Errorf("read mailbox event cleanup progress: %w", err)
	}
	latest := time.Time{}
	if latestStored.Valid {
		latest, err = parseStoredTime(latestStored.String)
		if err != nil {
			return false, fmt.Errorf("%w: event cleanup deadline: %v", ErrMailboxCleanupState, err)
		}
	}
	if scanComplete == 1 {
		if scanned == 0 {
			return false, fmt.Errorf("%w: event cleanup work has no references", ErrMailboxCleanupState)
		}
		if allTerminal != 1 || latest.IsZero() || now.Before(latest) {
			return false, nil
		}
		return true, nil
	}

	table := "mailbox_event_file_references"
	if item.remote {
		table = "mailbox_remote_event_file_references"
	}
	rows, err := connection.QueryContext(ctx, `
SELECT refs.exchange_id
FROM `+table+` AS refs
JOIN mailbox_exchanges AS exchanges ON exchanges.exchange_id = refs.exchange_id
WHERE refs.command_id = ? AND exchanges.mailbox_id = ? AND refs.exchange_id > ?
ORDER BY refs.exchange_id LIMIT ?
`, item.commandID, mailboxID, lastExchange, maxEventCleanupRefsPerWorkItem)
	if err != nil {
		return false, fmt.Errorf("page mailbox event cleanup references: %w", err)
	}
	var exchangeIDs []string
	for rows.Next() {
		var exchangeID string
		if err := rows.Scan(&exchangeID); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan mailbox event cleanup reference: %w", err)
		}
		exchangeIDs = append(exchangeIDs, exchangeID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("iterate mailbox event cleanup references: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close mailbox event cleanup references: %w", err)
	}
	complete := len(exchangeIDs) < maxEventCleanupRefsPerWorkItem
	for _, exchangeID := range exchangeIDs {
		metadata, err := readMailboxEventCleanupMetadata(ctx, connection, mailboxID, exchangeID, item.commandID, item.remote)
		if err != nil {
			return false, err
		}
		if !mailboxExchangeStateTerminal(metadata.state) {
			allTerminal = 0
			continue
		}
		if latest.IsZero() || metadata.cleanupAt.After(latest) {
			latest = metadata.cleanupAt
		}
	}
	if len(exchangeIDs) > 0 {
		lastExchange = exchangeIDs[len(exchangeIDs)-1]
	}
	scanned += len(exchangeIDs)
	if _, err := connection.ExecContext(ctx, `
UPDATE mailbox_event_cleanup_work
SET last_exchange_id = ?, scanned_refs = ?, scan_complete = ?, all_terminal = ?, latest_cleanup_at = ?
WHERE mailbox_id = ? AND command_id = ? AND is_remote = ?
`, lastExchange, scanned, boolInt(complete), allTerminal, nullableStoredTime(latest), mailboxID, item.commandID, remoteValue); err != nil {
		return false, fmt.Errorf("save mailbox event cleanup progress: %w", err)
	}
	if !complete || scanned == 0 || allTerminal != 1 || latest.IsZero() || now.Before(latest) {
		return false, nil
	}
	return true, nil
}

type mailboxEventCleanupMetadata struct {
	state     MailboxExchangeState
	cleanupAt time.Time
}

func readMailboxEventCleanupMetadata(ctx context.Context, connection *sql.Conn, mailboxID, exchangeID, commandID string, remote bool) (mailboxEventCleanupMetadata, error) {
	var storedMailboxID, state, updatedAt string
	var responseRevision int
	var acknowledgedAt, cleanupAt, localEventFileCommandID, remoteEventFileCommandID sql.NullString
	var availableSequence sql.NullInt64
	err := connection.QueryRowContext(ctx, `
SELECT mailbox_id, request_state, response_revision, updated_at,
       acknowledged_at, response_cleanup_at, available_event_sequence,
       (SELECT command_id FROM mailbox_event_file_references WHERE exchange_id = mailbox_exchanges.exchange_id),
       (SELECT command_id FROM mailbox_remote_event_file_references WHERE exchange_id = mailbox_exchanges.exchange_id)
FROM mailbox_exchanges WHERE exchange_id = ?
`, exchangeID).Scan(&storedMailboxID, &state, &responseRevision, &updatedAt,
		&acknowledgedAt, &cleanupAt, &availableSequence, &localEventFileCommandID, &remoteEventFileCommandID)
	if err != nil {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("read mailbox event cleanup metadata: %w", err)
	}
	if storedMailboxID != mailboxID || localEventFileCommandID.Valid == remoteEventFileCommandID.Valid ||
		remoteEventFileCommandID.Valid != remote || responseRevision < 0 {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: event cleanup exchange identity", ErrMailboxCleanupState)
	}
	if localEventFileCommandID.Valid && localEventFileCommandID.String != commandID || remoteEventFileCommandID.Valid && remoteEventFileCommandID.String != commandID {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: event cleanup command identity", ErrMailboxCleanupState)
	}
	updated, err := parseStoredTime(updatedAt)
	if err != nil {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: event cleanup updated_at: %v", ErrMailboxCleanupState, err)
	}
	var acknowledged *time.Time
	if acknowledgedAt.Valid {
		value, err := parseStoredTime(acknowledgedAt.String)
		if err != nil {
			return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: event cleanup acknowledged_at: %v", ErrMailboxCleanupState, err)
		}
		acknowledged = &value
	}
	if availableSequence.Valid && availableSequence.Int64 < 0 {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: negative event cursor", ErrMailboxCleanupState)
	}
	if !availableSequence.Valid || availableSequence.Int64 < 1 {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: event-file reference has no positive event cursor", ErrMailboxCleanupState)
	}
	if !mailboxExchangeStateTerminal(MailboxExchangeState(state)) {
		return mailboxEventCleanupMetadata{state: MailboxExchangeState(state)}, nil
	}
	var deadline time.Time
	if cleanupAt.Valid {
		deadline, err = parseStoredTime(cleanupAt.String)
		if err != nil {
			return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: event cleanup response deadline: %v", ErrMailboxCleanupState, err)
		}
	} else if responseRevision > 0 {
		deadline = mailboxResponseCleanupDeadline(updated, acknowledged)
	} else {
		return mailboxEventCleanupMetadata{}, fmt.Errorf("%w: terminal exchange has no response cleanup deadline", ErrMailboxCleanupState)
	}
	return mailboxEventCleanupMetadata{state: MailboxExchangeState(state), cleanupAt: deadline}, nil
}

func mailboxExchangeStateTerminal(state MailboxExchangeState) bool {
	return state == MailboxExchangeComplete || state == MailboxExchangeRejected || state == MailboxExchangeIndeterminate
}

func mailboxEventCommandTerminal(ctx context.Context, connection *sql.Conn, commandID string, remote bool) (bool, error) {
	var localState, remoteState string
	localErr := connection.QueryRowContext(ctx, `SELECT state FROM exec_commands WHERE command_id = ?`, commandID).Scan(&localState)
	remoteErr := connection.QueryRowContext(ctx, `SELECT command_state FROM local_remote_command_projections WHERE command_id = ?`, commandID).Scan(&remoteState)
	localFound, remoteFound := localErr == nil, remoteErr == nil
	if localErr != nil && !errors.Is(localErr, sql.ErrNoRows) {
		return false, fmt.Errorf("read local event-file command state: %w", localErr)
	}
	if remoteErr != nil && !errors.Is(remoteErr, sql.ErrNoRows) {
		return false, fmt.Errorf("read mirrored event-file command state: %w", remoteErr)
	}
	if localFound == remoteFound || remoteFound != remote {
		return false, fmt.Errorf("%w: event-file command ownership is missing or ambiguous", ErrMailboxCleanupState)
	}
	if remote {
		return mailboxCommandStateTerminal(remoteState), nil
	}
	return mailboxCommandStateTerminal(localState), nil
}

func nullableStoredTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatStoredTime(value)
}

// MarkMailboxEventFileRemoved completes a claimed shared-event unlink.
func (s *AuthorityStore) MarkMailboxEventFileRemoved(ctx context.Context, commandID domain.CommandID) error {
	return s.MarkMailboxEventFileRemovedInMailbox(ctx, MailboxEventRef{MailboxID: DefaultMailboxID, CommandID: commandID})
}

// MarkMailboxEventFileRemovedInMailbox records an unlink only for that
// mailbox's physical event projection.
func (s *AuthorityStore) MarkMailboxEventFileRemovedInMailbox(ctx context.Context, ref MailboxEventRef) error {
	if err := validateMailboxID(ref.MailboxID); err != nil {
		return err
	}
	if _, err := domain.NewCommandID(string(ref.CommandID)); err != nil {
		return fmt.Errorf("%w: command ID: %v", ErrMailboxCleanupState, err)
	}
	now := s.now().UTC()
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		for index, table := range []string{"mailbox_event_file_cleanup", "mailbox_remote_event_file_cleanup"} {
			result, err := connection.ExecContext(ctx, `UPDATE `+table+`
SET file_removed_at = COALESCE(file_removed_at, ?)
WHERE mailbox_id = ? AND command_id = ?`, formatStoredTime(now), ref.MailboxID, string(ref.CommandID))
			if err != nil {
				return struct{}{}, fmt.Errorf("mark mailbox event file removed: %w", err)
			}
			count, err := rowsAffected(result)
			if err != nil {
				return struct{}{}, err
			}
			if count > 0 {
				if err := deleteMailboxEventCleanupWork(ctx, connection, ref.MailboxID, ref.CommandID, index == 1); err != nil {
					return struct{}{}, err
				}
				return struct{}{}, nil
			}
			var exists int
			if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE mailbox_id = ? AND command_id = ? AND file_removed_at IS NOT NULL`, ref.MailboxID, string(ref.CommandID)).Scan(&exists); err != nil {
				return struct{}{}, fmt.Errorf("verify mailbox event cleanup: %w", err)
			}
			if exists > 0 {
				if err := deleteMailboxEventCleanupWork(ctx, connection, ref.MailboxID, ref.CommandID, index == 1); err != nil {
					return struct{}{}, err
				}
				return struct{}{}, nil
			}
		}
		return struct{}{}, ErrMailboxCleanupState
	})
	return err
}

func deleteMailboxEventCleanupWork(ctx context.Context, connection *sql.Conn, mailboxID string, commandID domain.CommandID, remote bool) error {
	remoteValue := 0
	if remote {
		remoteValue = 1
	}
	if _, err := connection.ExecContext(ctx, `DELETE FROM mailbox_event_cleanup_work WHERE mailbox_id = ? AND command_id = ? AND is_remote = ?`, mailboxID, string(commandID), remoteValue); err != nil {
		return fmt.Errorf("complete mailbox event cleanup work: %w", err)
	}
	return nil
}

func mailboxCommandStateTerminal(state string) bool {
	switch state {
	case string(domain.CommandStateSucceeded), string(domain.CommandStateFailed), string(domain.CommandStateCancelled), string(domain.CommandStateTimedOut), string(domain.CommandStateRejected), string(domain.CommandStateLost):
		return true
	default:
		return false
	}
}
