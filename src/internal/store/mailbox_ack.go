package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrMailboxAckInvalid  = errors.New("invalid mailbox acknowledgement")
	ErrMailboxAckConflict = errors.New("mailbox acknowledgement does not match terminal response")
)

// MailboxAcknowledgement acknowledges exactly one immutable terminal response
// revision and its advertised event cursor. A nil cursor means the response
// advertised no event file; a pointer to zero acknowledges an expired prefix.
type MailboxAcknowledgement struct {
	MailboxID              string
	RequestID              string
	ResponseRevision       int64
	AvailableEventSequence *int64
}

// AcknowledgeMailboxExchange records a matching ACK durably. Exact duplicate
// ACKs are idempotent; wrong revision/cursor or preterminal ACKs never record
// receipt. An incomplete-output ACK acknowledges the available prefix and its
// warning only.
func (s *AuthorityStore) AcknowledgeMailboxExchange(ctx context.Context, ack MailboxAcknowledgement) (MailboxExchangeRecord, error) {
	mailboxID := ack.MailboxID
	if mailboxID == "" {
		mailboxID = DefaultMailboxID
	}
	ref, err := NewMailboxExchangeRef(mailboxID, ack.RequestID)
	if err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: request ID: %v", ErrMailboxAckInvalid, err)
	}
	return s.AcknowledgeMailboxExchangeInMailbox(ctx, ref, ack)
}

// AcknowledgeMailboxExchangeInMailbox records an ACK only for the exchange in
// the supplied mailbox namespace.
func (s *AuthorityStore) AcknowledgeMailboxExchangeInMailbox(ctx context.Context, ref MailboxExchangeRef, ack MailboxAcknowledgement) (MailboxExchangeRecord, error) {
	validated, err := validateMailboxExchangeRef(ref)
	if err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: request ID: %v", ErrMailboxAckInvalid, err)
	}
	if (ack.MailboxID != "" && ack.MailboxID != validated.MailboxID) ||
		(ack.RequestID != "" && ack.RequestID != validated.ClientRequestID) {
		return MailboxExchangeRecord{}, ErrMailboxAckInvalid
	}
	if ack.ResponseRevision < 1 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: response revision", ErrMailboxAckInvalid)
	}
	if ack.AvailableEventSequence != nil && *ack.AvailableEventSequence < 0 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: available event sequence", ErrMailboxAckInvalid)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		record, err := readMailboxExchangeOnConnection(ctx, connection, validated)
		if err != nil {
			return MailboxExchangeRecord{}, err
		}
		if record.State != MailboxExchangeComplete && record.State != MailboxExchangeRejected && record.State != MailboxExchangeIndeterminate || record.ResponseRevision < 1 || len(record.TerminalResponseBytes) == 0 {
			return MailboxExchangeRecord{}, ErrMailboxAckConflict
		}
		if record.ResponseRevision != ack.ResponseRevision || !sameCursor(record.AvailableEventSequence, ack.AvailableEventSequence) {
			return MailboxExchangeRecord{}, ErrMailboxAckConflict
		}
		if record.AcknowledgedAt != nil {
			return record, nil
		}
		cleanupAt := record.ResponseCleanupAt
		if cleanupAt == nil {
			fallback := record.UpdatedAt.Add(MailboxUnackedResponseLifetime)
			cleanupAt = &fallback
		}
		ackedDeadline := now.Add(MailboxAckedResponseLifetime)
		if record.ResponseCleanupStartedAt == nil && now.Before(*cleanupAt) && ackedDeadline.Before(*cleanupAt) {
			cleanupAt = &ackedDeadline
		}
		if _, err := connection.ExecContext(ctx, `UPDATE mailbox_exchanges
SET acknowledged_at = ?, response_cleanup_at = ?
WHERE exchange_id = ? AND acknowledged_at IS NULL`, formatStoredTime(now), formatStoredTime(*cleanupAt), record.ExchangeID); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("record mailbox acknowledgement: %w", err)
		}
		return readMailboxExchangeOnConnection(ctx, connection, validated)
	})
}
