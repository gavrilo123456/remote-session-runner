package mailbox

import (
	"context"
	"crypto/sha256"
	"errors"

	"remote-session-runner/src/internal/store"
)

var ErrMailboxIngressDiagnosticFailure = errors.New("mailbox ingress diagnostic processing failed")

const ingressDiagnosticLifecyclePhase = "ingress_validation"

// IngressDiagnosticEvent is a deliberately small, safe log payload. It never
// carries parser text, raw mailbox bytes, scripts, operations, idempotency
// keys, execution selections, remote IDs, or filesystem paths.
type IngressDiagnosticEvent struct {
	MailboxID      string
	RequestID      string
	FailureClass   string
	LifecyclePhase string
}

type mailboxIngressDiagnosticFailure struct{ cause error }

func (e *mailboxIngressDiagnosticFailure) Error() string {
	return ErrMailboxIngressDiagnosticFailure.Error()
}

func (e *mailboxIngressDiagnosticFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *mailboxIngressDiagnosticFailure) Is(target error) bool {
	return target == ErrMailboxIngressDiagnosticFailure
}

func newMailboxIngressDiagnosticFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &mailboxIngressDiagnosticFailure{cause: cause}
}

// IsMailboxIngressDiagnosticFailure lets runner-local retain its operational
// error accounting while suppressing its generic reconciliation line in favor
// of the one fixed, correlation-safe ingress-diagnostic event.
func IsMailboxIngressDiagnosticFailure(err error) bool {
	return errors.Is(err, ErrMailboxIngressDiagnosticFailure)
}

// TakeIngressDiagnosticEvents drains safe, once-per-event telemetry after one
// Import cycle. It is safe for runner-local to call regardless of Import's
// error result and returns nil for a nil processor or an empty queue.
func (p *SessionProcessor) TakeIngressDiagnosticEvents() []IngressDiagnosticEvent {
	if p == nil {
		return nil
	}
	p.ingressDiagnosticEventsMu.Lock()
	defer p.ingressDiagnosticEventsMu.Unlock()
	if len(p.ingressDiagnosticEvents) == 0 {
		return nil
	}
	events := append([]IngressDiagnosticEvent(nil), p.ingressDiagnosticEvents...)
	p.ingressDiagnosticEvents = nil
	return events
}

func (p *SessionProcessor) appendIngressDiagnosticEvent(requestID, failureClass string) {
	if p == nil {
		return
	}
	if _, ok := safeMailboxID(p.mailboxID); !ok {
		return
	}
	if _, ok := safeRequestID(requestID); !ok || !safeIngressDiagnosticFailureClass(failureClass) {
		return
	}
	p.ingressDiagnosticEventsMu.Lock()
	defer p.ingressDiagnosticEventsMu.Unlock()
	// A mailbox cycle is bounded by filesystem work. Cap queued events anyway
	// so a hostile directory cannot turn an unavailable stderr consumer into an
	// unbounded in-memory queue.
	if len(p.ingressDiagnosticEvents) >= 256 {
		return
	}
	p.ingressDiagnosticEvents = append(p.ingressDiagnosticEvents, IngressDiagnosticEvent{
		MailboxID: p.mailboxID, RequestID: requestID, FailureClass: failureClass, LifecyclePhase: ingressDiagnosticLifecyclePhase,
	})
}

func safeIngressDiagnosticFailureClass(value string) bool {
	switch store.MailboxIngressDiagnosticCode(value) {
	case store.MailboxIngressDiagnosticMalformedJSON,
		store.MailboxIngressDiagnosticInvalidRequestSchema,
		store.MailboxIngressDiagnosticRequestIdentityMismatch,
		store.MailboxIngressDiagnosticInvalidScript,
		store.MailboxIngressDiagnosticRequestTooLarge,
		store.MailboxIngressDiagnosticRequestIDReusedAfterReject:
		return true
	case "recovery_failed":
		return true
	default:
		return false
	}
}

func (p *SessionProcessor) processIngressDiagnostic(ctx context.Context, candidate ingressDiagnosticCandidate) (ingressHandlingResult, error) {
	if p == nil || p.authority == nil || p.importer == nil || p.diagnostics == nil {
		return ingressHandlingResult{}, newMailboxIngressDiagnosticFailure(ErrSessionProcessorConfiguration)
	}
	ref, err := store.NewMailboxIngressDiagnosticRef(p.mailboxID, candidate.RequestID)
	if err != nil {
		return ingressHandlingResult{}, newMailboxIngressDiagnosticFailure(err)
	}
	record, disposition, err := p.authority.RecordMailboxIngressDiagnosticWithSchemaDetailInMailbox(ctx, ref, candidate.RequestSHA256, candidate.Code, candidate.SchemaDetail)
	if err != nil {
		return ingressHandlingResult{}, newMailboxIngressDiagnosticFailure(err)
	}
	outcome := ingressHandlingResult{Durable: true}
	switch disposition {
	case store.MailboxIngressDiagnosticCreated:
		p.appendIngressDiagnosticEvent(candidate.RequestID, string(record.Code))
	case store.MailboxIngressDiagnosticRequestIDReused:
		p.appendIngressDiagnosticReuseOnce(candidate)
		if err := p.ensureIngressDiagnosticProjected(ctx, record); err != nil {
			return outcome, newMailboxIngressDiagnosticFailure(err)
		}
		removed, err := p.removeRetainedIngressDiagnosticPair(candidate)
		if err != nil {
			return outcome, newMailboxIngressDiagnosticFailure(err)
		}
		outcome.PairRemoved = removed
		if removed {
			p.clearIngressDiagnosticReuse(candidate)
		}
		return outcome, nil
	case store.MailboxIngressDiagnosticSameFingerprint:
		// The initial durable record already owns its frozen diagnostic and
		// input pair. Continue its recovery path without another log line.
	default:
		return outcome, newMailboxIngressDiagnosticFailure(store.ErrMailboxIngressDiagnosticInvalid)
	}
	if err := p.ensureIngressDiagnosticProjected(ctx, record); err != nil {
		return outcome, newMailboxIngressDiagnosticFailure(err)
	}
	removed, err := p.completeRecordedIngressDiagnosticCleanup(ctx, record)
	if err != nil {
		return outcome, newMailboxIngressDiagnosticFailure(err)
	}
	outcome.PairRemoved = removed
	return outcome, nil
}

// processRetainedIngressDiagnostic intercepts a syntactically valid request
// before the ordinary receipt handler. A request ID that belongs to a retained
// malformed-input ledger row is never allowed to become accepted work.
func (p *SessionProcessor) processRetainedIngressDiagnostic(ctx context.Context, _ Request, candidate ingressDiagnosticCandidate) (bool, ingressHandlingResult, error) {
	if p == nil || p.authority == nil || p.diagnostics == nil {
		return false, ingressHandlingResult{}, newMailboxIngressDiagnosticFailure(ErrSessionProcessorConfiguration)
	}
	ref, err := store.NewMailboxIngressDiagnosticRef(p.mailboxID, candidate.RequestID)
	if err != nil {
		return false, ingressHandlingResult{}, newMailboxIngressDiagnosticFailure(err)
	}
	record, err := p.authority.GetMailboxIngressDiagnosticInMailbox(ctx, ref)
	if errors.Is(err, store.ErrMailboxIngressDiagnosticNotFound) {
		return false, ingressHandlingResult{}, nil
	}
	if err != nil {
		return false, ingressHandlingResult{}, newMailboxIngressDiagnosticFailure(err)
	}
	outcome := ingressHandlingResult{Durable: true}
	p.appendIngressDiagnosticReuseOnce(candidate)
	if err := p.ensureIngressDiagnosticProjected(ctx, record); err != nil {
		return true, outcome, newMailboxIngressDiagnosticFailure(err)
	}
	removed, err := p.removeRetainedIngressDiagnosticPair(candidate)
	if err != nil {
		return true, outcome, newMailboxIngressDiagnosticFailure(err)
	}
	outcome.PairRemoved = removed
	if removed {
		p.clearIngressDiagnosticReuse(candidate)
	}
	return true, outcome, nil
}

func (p *SessionProcessor) ensureIngressDiagnosticProjected(ctx context.Context, record store.MailboxIngressDiagnosticRecord) error {
	if p == nil || p.authority == nil || p.diagnostics == nil || record.MailboxID != p.mailboxID {
		return ErrSessionProcessorConfiguration
	}
	if record.DiagnosticCleanupStartedAt != nil || record.DiagnosticFileRemovedAt != nil {
		return nil
	}
	if p.now == nil || !p.now().UTC().Before(record.DiagnosticCleanupAt) {
		return nil
	}
	current, err := p.diagnostics.Matches(record.RequestID, record.DiagnosticBytes)
	if err != nil {
		return err
	}
	if !current {
		if err := p.diagnostics.Replace(ctx, record.RequestID, record.DiagnosticBytes); err != nil {
			return err
		}
	}
	if record.ProjectedAt != nil {
		return nil
	}
	_, err = p.authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, store.MailboxIngressDiagnosticRef{MailboxID: record.MailboxID, ClientRequestID: record.RequestID})
	if errors.Is(err, store.ErrMailboxIngressDiagnosticExpired) {
		// A concurrent cleanup claim owns expiry. It is never safe to revive an
		// artifact after that point, and there is no normal mailbox work to run.
		return nil
	}
	return err
}

// completeRecordedIngressDiagnosticCleanup advances only the original pair's
// fingerprint-bound ledger lifecycle. It is used for the first malformed
// publication and restart recovery, never for a later retained-ID reuse.
func (p *SessionProcessor) completeRecordedIngressDiagnosticCleanup(ctx context.Context, record store.MailboxIngressDiagnosticRecord) (bool, error) {
	if p == nil || p.importer == nil || p.authority == nil || record.MailboxID != p.mailboxID {
		return false, ErrSessionProcessorConfiguration
	}
	if record.InputPairRemovedAt != nil {
		return true, nil
	}
	if record.InputCleanupStartedAt != nil {
		if err := p.importer.cleanupRecordedIngressDiagnosticStages(record.RequestID, record.RequestSHA256); err != nil {
			return false, err
		}
	}
	state, matches, err := p.importer.revalidateIngressDiagnosticPair(record.RequestID, record.RequestSHA256)
	if err != nil || !matches {
		return false, err
	}
	if state.MarkerPresent && !state.RequestPresent {
		return false, nil
	}
	if !state.MarkerPresent && !state.RequestPresent {
		if record.InputCleanupStartedAt == nil {
			return false, nil
		}
		_, err := p.authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, store.MailboxIngressDiagnosticRef{MailboxID: record.MailboxID, ClientRequestID: record.RequestID}, record.RequestSHA256)
		return err == nil, err
	}
	if !state.MarkerPresent && record.InputCleanupStartedAt == nil {
		// A pre-existing unmarked draft is not evidence that this processor
		// removed the marker, so leave it for normal draft cleanup/review.
		return false, nil
	}
	updated, err := p.authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, store.MailboxIngressDiagnosticRef{MailboxID: record.MailboxID, ClientRequestID: record.RequestID}, record.RequestSHA256)
	if err != nil {
		return false, err
	}
	_, removed, err := p.importer.removeRevalidatedIngressDiagnosticPair(record.RequestID, record.RequestSHA256)
	if err != nil || !removed {
		return false, err
	}
	_, err = p.authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, store.MailboxIngressDiagnosticRef{MailboxID: updated.MailboxID, ClientRequestID: updated.RequestID}, record.RequestSHA256)
	return err == nil, err
}

// removeRetainedIngressDiagnosticPair removes a later safe pair that cannot
// use the original ledger fingerprint. The durable rejection already reserves
// its ID; this cleanup does not alter that original record's lifecycle.
func (p *SessionProcessor) removeRetainedIngressDiagnosticPair(candidate ingressDiagnosticCandidate) (bool, error) {
	if p == nil || p.importer == nil {
		return false, ErrSessionProcessorConfiguration
	}
	state, matches, err := p.importer.revalidateIngressDiagnosticPair(candidate.RequestID, candidate.RequestSHA256)
	if err != nil || !matches || !state.MarkerPresent || !state.RequestPresent {
		return false, err
	}
	_, removed, err := p.importer.removeRevalidatedIngressDiagnosticPair(candidate.RequestID, candidate.RequestSHA256)
	return removed, err
}

// recoverIngressDiagnostics runs before ordinary unmarked-draft cleanup. It
// restores only frozen active private artifacts and completes only an original
// pair whose current bounded bytes still match its ledger fingerprint.
func (p *SessionProcessor) recoverIngressDiagnostics(ctx context.Context) error {
	if p == nil || p.authority == nil || p.importer == nil || p.diagnostics == nil {
		return ErrSessionProcessorConfiguration
	}
	records, err := p.authority.ListRecoverableMailboxIngressDiagnosticsInMailbox(ctx, p.mailboxID, 64)
	if err != nil {
		return newMailboxIngressDiagnosticFailure(err)
	}
	var result error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := p.ensureIngressDiagnosticProjected(ctx, record); err != nil {
			p.appendIngressDiagnosticRecoveryFailureOnce(record.RequestID)
			result = errors.Join(result, newMailboxIngressDiagnosticFailure(err))
			continue
		}
		removed, err := p.completeRecordedIngressDiagnosticCleanup(ctx, record)
		if err != nil {
			p.appendIngressDiagnosticRecoveryFailureOnce(record.RequestID)
			result = errors.Join(result, newMailboxIngressDiagnosticFailure(err))
			continue
		}
		if removed {
			p.clearIngressDiagnosticRecoveryFailure(record.RequestID)
		}
	}
	return result
}

func (p *SessionProcessor) appendIngressDiagnosticReuseOnce(candidate ingressDiagnosticCandidate) {
	if p == nil {
		return
	}
	key := ingressDiagnosticEventKey(candidate.RequestID, candidate.RequestSHA256)
	if _, exists := p.ingressDiagnosticReuseFailures[key]; exists {
		return
	}
	if len(p.ingressDiagnosticReuseFailures) >= 1024 {
		return
	}
	p.ingressDiagnosticReuseFailures[key] = struct{}{}
	p.appendIngressDiagnosticEvent(candidate.RequestID, string(store.MailboxIngressDiagnosticRequestIDReusedAfterReject))
}

func (p *SessionProcessor) clearIngressDiagnosticReuse(candidate ingressDiagnosticCandidate) {
	if p == nil {
		return
	}
	delete(p.ingressDiagnosticReuseFailures, ingressDiagnosticEventKey(candidate.RequestID, candidate.RequestSHA256))
}

func (p *SessionProcessor) appendIngressDiagnosticRecoveryFailureOnce(requestID string) {
	if p == nil {
		return
	}
	if _, exists := p.ingressDiagnosticRecoveryFailures[requestID]; exists {
		return
	}
	if len(p.ingressDiagnosticRecoveryFailures) >= 1024 {
		return
	}
	p.ingressDiagnosticRecoveryFailures[requestID] = struct{}{}
	p.appendIngressDiagnosticEvent(requestID, "recovery_failed")
}

func (p *SessionProcessor) clearIngressDiagnosticRecoveryFailure(requestID string) {
	if p != nil {
		delete(p.ingressDiagnosticRecoveryFailures, requestID)
	}
}

func ingressDiagnosticEventKey(requestID string, fingerprint [sha256.Size]byte) string {
	return requestID + "\x00" + string(fingerprint[:])
}
