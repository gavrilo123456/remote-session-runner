package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrMailboxLifecycleEvidenceInvalid means that a compact mailbox lifecycle
// lookup could not form a safe, unambiguous answer. It deliberately carries no
// mailbox payload, response, idempotency key, or filesystem detail.
var ErrMailboxLifecycleEvidenceInvalid = errors.New("mailbox lifecycle evidence is invalid")

// MailboxLifecycleEvidence is the narrow, read-only durable evidence needed
// to classify an inbox artifact. It intentionally excludes canonical request
// bytes, responses, scripts, idempotency keys, and diagnostic payloads.
type MailboxLifecycleEvidence struct {
	ExchangeExists             bool
	ExchangeState              MailboxExchangeState
	ExchangeAcknowledged       bool
	DiagnosticExists           bool
	DiagnosticInputPairRemoved bool
}

const mailboxLifecycleEvidenceQueryLimit = 500

// LookupMailboxLifecycleEvidenceForRequestIDsInMailbox returns compact durable
// lifecycle evidence for request IDs in one mailbox namespace. It keeps every
// related read in one read-only SQLite snapshot and never loads exchange or
// diagnostic bodies. Missing IDs are returned with zero-valued evidence.
func (s *AuthorityStore) LookupMailboxLifecycleEvidenceForRequestIDsInMailbox(ctx context.Context, mailboxID string, requestIDs []string) (map[string]MailboxLifecycleEvidence, error) {
	if s == nil || s.db == nil {
		return nil, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateMailboxID(mailboxID); err != nil {
		return nil, ErrMailboxLifecycleEvidenceInvalid
	}
	unique := make([]string, 0, len(requestIDs))
	seen := make(map[string]struct{}, len(requestIDs))
	for _, requestID := range requestIDs {
		if _, err := NewMailboxExchangeRef(mailboxID, requestID); err != nil {
			return nil, ErrMailboxLifecycleEvidenceInvalid
		}
		if _, exists := seen[requestID]; exists {
			continue
		}
		seen[requestID] = struct{}{}
		unique = append(unique, requestID)
	}
	evidence := make(map[string]MailboxLifecycleEvidence, len(unique))
	for _, requestID := range unique {
		evidence[requestID] = MailboxLifecycleEvidence{}
	}
	if len(unique) == 0 {
		return evidence, nil
	}
	result, err := withReadTransaction(ctx, s.db, func(ctx context.Context, connection *sql.Conn) (map[string]MailboxLifecycleEvidence, error) {
		for offset := 0; offset < len(unique); offset += mailboxLifecycleEvidenceQueryLimit {
			end := offset + mailboxLifecycleEvidenceQueryLimit
			if end > len(unique) {
				end = len(unique)
			}
			if err := readMailboxLifecycleEvidenceChunk(ctx, connection, mailboxID, unique[offset:end], evidence); err != nil {
				return nil, err
			}
		}
		for requestID, item := range evidence {
			if item.ExchangeExists && item.DiagnosticExists {
				return nil, fmt.Errorf("%w: durable exchange and ingress diagnostic share request ID", ErrMailboxLifecycleEvidenceInvalid)
			}
			if item.ExchangeExists && !validMailboxLifecycleExchangeState(item.ExchangeState) {
				return nil, fmt.Errorf("%w: exchange state", ErrMailboxLifecycleEvidenceInvalid)
			}
			if item.ExchangeAcknowledged && !item.ExchangeExists {
				return nil, fmt.Errorf("%w: acknowledgement without exchange", ErrMailboxLifecycleEvidenceInvalid)
			}
			// Keep the compiler from permitting a map iteration change to hide
			// a malformed request ID after validation above.
			if _, ok := seen[requestID]; !ok {
				return nil, ErrMailboxLifecycleEvidenceInvalid
			}
		}
		return evidence, nil
	})
	if err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return nil, err
	}
	return result, nil
}

func readMailboxLifecycleEvidenceChunk(ctx context.Context, connection *sql.Conn, mailboxID string, requestIDs []string, evidence map[string]MailboxLifecycleEvidence) error {
	if len(requestIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(requestIDs)), ",")
	arguments := make([]any, 0, len(requestIDs)+1)
	arguments = append(arguments, mailboxID)
	for _, requestID := range requestIDs {
		arguments = append(arguments, requestID)
	}
	rows, err := connection.QueryContext(ctx, `
SELECT client_request_id, request_state, acknowledged_at IS NOT NULL
FROM mailbox_exchanges
WHERE mailbox_id = ? AND client_request_id IN (`+placeholders+`)
`, arguments...)
	if err != nil {
		return fmt.Errorf("read mailbox lifecycle exchanges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var requestID, state string
		var acknowledged int
		if err := rows.Scan(&requestID, &state, &acknowledged); err != nil {
			return fmt.Errorf("scan mailbox lifecycle exchange: %w", err)
		}
		item, exists := evidence[requestID]
		if !exists || item.ExchangeExists || acknowledged < 0 || acknowledged > 1 {
			return ErrMailboxLifecycleEvidenceInvalid
		}
		item.ExchangeExists = true
		item.ExchangeState = MailboxExchangeState(state)
		item.ExchangeAcknowledged = acknowledged == 1
		evidence[requestID] = item
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate mailbox lifecycle exchanges: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close mailbox lifecycle exchanges: %w", err)
	}

	rows, err = connection.QueryContext(ctx, `
SELECT client_request_id, input_pair_removed_at IS NOT NULL
FROM mailbox_ingress_diagnostics
WHERE mailbox_id = ? AND client_request_id IN (`+placeholders+`)
`, arguments...)
	if err != nil {
		return fmt.Errorf("read mailbox lifecycle diagnostics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var requestID string
		var inputPairRemoved int
		if err := rows.Scan(&requestID, &inputPairRemoved); err != nil {
			return fmt.Errorf("scan mailbox lifecycle diagnostic: %w", err)
		}
		item, exists := evidence[requestID]
		if !exists || item.DiagnosticExists || inputPairRemoved < 0 || inputPairRemoved > 1 {
			return ErrMailboxLifecycleEvidenceInvalid
		}
		item.DiagnosticExists = true
		item.DiagnosticInputPairRemoved = inputPairRemoved == 1
		evidence[requestID] = item
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate mailbox lifecycle diagnostics: %w", err)
	}
	return nil
}

func validMailboxLifecycleExchangeState(state MailboxExchangeState) bool {
	switch state {
	case MailboxExchangeAccepted, MailboxExchangeComplete, MailboxExchangeRejected, MailboxExchangeIndeterminate:
		return true
	default:
		return false
	}
}
