package mailbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/store"
)

var errRequestConflictPending = errors.New("mailbox conflicting publication cleanup remains unproven")

// requestConflictBinding fingerprints only immutable authority fields. Current
// responses, ACKs and execution state may advance independently while a rejected
// publication is being preserved; none of those fields authorize its cleanup.
func requestConflictBinding(record store.MailboxExchangeRecord) string {
	raw, _ := json.Marshal(struct {
		ExchangeID, MailboxID, RequestID, Operation, ControllerType, ControllerID, IdempotencyKey string
		CreatedAt                                                                                 string
		CanonicalPayload                                                                          []byte
	}{record.ExchangeID, record.MailboxID, record.RequestID, record.Operation,
		string(record.Controller.Type()), string(record.Controller.ID()), record.IdempotencyKey,
		record.CreatedAt.UTC().Format(time.RFC3339Nano), record.CanonicalPayload})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// processRequestConflict runs only after the store has refused a different
// binding under an existing mailbox-scoped ID. It cannot invoke operations or
// write the original exchange, its response, events, ACK or capacity records.
func (p *SessionProcessor) processRequestConflict(ctx context.Context, request Request, candidate ingressDiagnosticCandidate) (ingressHandlingResult, error) {
	if p == nil || p.conflicts == nil || request.MailboxID != p.mailboxID ||
		request.RequestID != candidate.RequestID || sha256.Sum256(request.RawJSON) != candidate.RequestSHA256 {
		return ingressHandlingResult{}, ErrSessionProcessorConfiguration
	}
	ref, err := p.exchangeRef(request.RequestID)
	if err != nil {
		return ingressHandlingResult{}, err
	}
	original, err := p.authority.GetMailboxExchangeInMailbox(ctx, ref)
	if err != nil || original.Controller != p.controller {
		return ingressHandlingResult{}, errors.Join(errRequestConflictPending, err)
	}
	state, matches, err := p.importer.revalidateIngressDiagnosticPair(request.RequestID, candidate.RequestSHA256)
	if err != nil || !matches || !state.MarkerPresent || !state.RequestPresent {
		return ingressHandlingResult{}, errors.Join(errRequestConflictPending, err)
	}
	record := requestConflictRecord{
		FormatVersion: 1, MailboxID: p.mailboxID, RequestID: request.RequestID,
		InputSHA256:           hex.EncodeToString(candidate.RequestSHA256[:]),
		OriginalBindingSHA256: requestConflictBinding(original),
		Code:                  requestConflictCode, Message: requestConflictMessage,
	}
	if err := p.conflicts.Preserve(ctx, record, request.RawJSON); err != nil {
		return ingressHandlingResult{}, errors.Join(errRequestConflictPending, err)
	}
	key := "request_conflict/" + ingressDiagnosticEventKey(request.RequestID, candidate.RequestSHA256)
	if _, logged := p.ingressDiagnosticReuseFailures[key]; !logged && len(p.ingressDiagnosticReuseFailures) < 1024 {
		p.ingressDiagnosticReuseFailures[key] = struct{}{}
		p.appendIngressDiagnosticEvent(request.RequestID, requestConflictCode)
	}
	outcome := ingressHandlingResult{Durable: true}
	removed, err := p.completeRequestConflict(ctx, record)
	outcome.PairRemoved = removed
	return outcome, err
}

func (p *SessionProcessor) completeRequestConflict(ctx context.Context, record requestConflictRecord) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if record.MailboxID != p.mailboxID {
		return false, ErrMailboxOwnership
	}
	if err := p.importer.checkAuthorization(record.RequestID, ""); err != nil {
		return false, err
	}
	ref, err := p.exchangeRef(record.RequestID)
	if err != nil {
		return false, err
	}
	original, err := p.authority.GetMailboxExchangeInMailbox(ctx, ref)
	if err != nil || original.Controller != p.controller || requestConflictBinding(original) != record.OriginalBindingSHA256 {
		return false, errors.Join(errRequestConflictPending, err)
	}
	fingerprint, err := hex.DecodeString(record.InputSHA256)
	if err != nil || len(fingerprint) != sha256.Size {
		return false, errRequestConflictPending
	}
	var want [sha256.Size]byte
	copy(want[:], fingerprint)
	// Pending preservation is durable before cleanup begins. Existing safe
	// staging recovery never touches a different fingerprint or a republished
	// source name, and it runs before generic unmarked-draft collection.
	if err := p.importer.cleanupRecordedIngressDiagnosticStages(record.RequestID, want); err != nil {
		return false, errors.Join(errRequestConflictPending, err)
	}
	_, removed, err := p.importer.removeRevalidatedIngressDiagnosticPair(record.RequestID, want)
	if err != nil || !removed {
		return false, errors.Join(errRequestConflictPending, err)
	}
	if err := p.conflicts.Finish(ctx, record); err != nil {
		return false, errors.Join(errRequestConflictPending, err)
	}
	return true, nil
}

func (p *SessionProcessor) recoverRequestConflicts(ctx context.Context) error {
	if p == nil || p.conflicts == nil {
		return ErrSessionProcessorConfiguration
	}
	records, err := p.conflicts.Pending(ctx, 64)
	if err != nil {
		return errors.Join(errRequestConflictPending, err)
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if _, err := p.completeRequestConflict(ctx, record); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
