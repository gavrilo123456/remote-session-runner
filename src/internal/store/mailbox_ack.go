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
	RequestID              string
	ResponseRevision       int64
	AvailableEventSequence *int64
}

// AcknowledgeMailboxExchange records a matching ACK durably. Exact duplicate
// ACKs are idempotent; wrong revision/cursor or preterminal ACKs never record
// receipt. An incomplete-output ACK acknowledges the available prefix and its
// warning only.
func (s *AuthorityStore) AcknowledgeMailboxExchange(ctx context.Context, ack MailboxAcknowledgement) (MailboxExchangeRecord, error) {
	if err := validateMailboxRequestID(ack.RequestID); err != nil {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: request ID: %v", ErrMailboxAckInvalid, err)
	}
	if ack.ResponseRevision < 1 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: response revision", ErrMailboxAckInvalid)
	}
	if ack.AvailableEventSequence != nil && *ack.AvailableEventSequence < 0 {
		return MailboxExchangeRecord{}, fmt.Errorf("%w: available event sequence", ErrMailboxAckInvalid)
	}
	now := s.now().UTC()
	return withImmediateTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (MailboxExchangeRecord, error) {
		record, err := readMailboxExchangeOnConnection(ctx, connection, ack.RequestID)
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
		if _, err := connection.ExecContext(ctx, `UPDATE mailbox_exchanges SET acknowledged_at = ? WHERE request_id = ? AND acknowledged_at IS NULL`, formatStoredTime(now), ack.RequestID); err != nil {
			return MailboxExchangeRecord{}, fmt.Errorf("record mailbox acknowledgement: %w", err)
		}
		return readMailboxExchangeOnConnection(ctx, connection, ack.RequestID)
	})
}
