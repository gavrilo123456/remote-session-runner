package mailbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// ReceiptProcessor persists a mailbox exchange before invoking its optional
// handler. A restart can therefore recover the request ID/key/hash binding
// without treating the same file as a new mutation.
type ReceiptProcessor struct {
	importer   *Importer
	authority  *store.AuthorityStore
	controller domain.ControllerIdentity
	mailboxID  string
	handler    Handler
}

// ReceiptProcessorOptions configures the P082 receipt boundary.
type ReceiptProcessorOptions struct {
	MailboxID  string
	Importer   *Importer
	Authority  *store.AuthorityStore
	Controller domain.ControllerIdentity
	Handler    Handler
}

// NewReceiptProcessor constructs the durable receipt adapter over a P081
// importer and the Mac authority store.
func NewReceiptProcessor(options ReceiptProcessorOptions) (*ReceiptProcessor, error) {
	if options.Importer == nil || options.Authority == nil {
		return nil, fmt.Errorf("%w: receipt processor dependencies", ErrImporterConfiguration)
	}
	controller, err := domain.NewControllerIdentity(options.Controller.Type(), options.Controller.ID())
	if err != nil {
		return nil, fmt.Errorf("%w: controller: %v", ErrImporterConfiguration, err)
	}
	mailboxID := options.MailboxID
	if mailboxID == "" {
		mailboxID = options.Importer.MailboxID()
	}
	if _, ok := safeMailboxID(mailboxID); !ok || mailboxID != options.Importer.MailboxID() {
		return nil, fmt.Errorf("%w: mailbox ID", ErrImporterConfiguration)
	}
	return &ReceiptProcessor{importer: options.Importer, authority: options.Authority, controller: controller, mailboxID: mailboxID, handler: options.Handler}, nil
}

// Import records each validated request before calling Handler. A terminal
// exchange is replayed without another callback. An accepted receipt can be
// resumed after a crash; external side effects still require their own
// idempotent target boundary and are not claimed exactly once here.
func (p *ReceiptProcessor) Import(ctx context.Context) ([]Result, error) {
	if p == nil || p.importer == nil || p.authority == nil {
		return nil, ErrImporterConfiguration
	}
	return p.importer.importWithRecorder(ctx, p.process)
}

func (p *ReceiptProcessor) process(ctx context.Context, request Request) (bool, error) {
	ref, err := store.NewMailboxExchangeRef(p.mailboxID, request.RequestID)
	if err != nil || request.MailboxID != p.mailboxID {
		return false, ErrImporterConfiguration
	}
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return false, err
	}
	record, duplicate, err := p.authority.AcceptMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: p.mailboxID, RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
	if err != nil {
		return false, err
	}
	if duplicate && record.State != store.MailboxExchangeAccepted {
		return true, nil
	}
	if p.handler == nil {
		// The receipt is durable, but the request remains available so a later
		// processor with its operation handler can resume it.
		return false, nil
	}
	if err := p.handler(ctx, request); err != nil {
		_, completeErr := p.authority.CompleteMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeRejected)
		if completeErr != nil {
			return false, fmt.Errorf("%w; receipt rejection: %v", err, completeErr)
		}
		return true, err
	}
	_, err = p.authority.CompleteMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeComplete)
	return err == nil, err
}

func receiptCanonical(request Request) ([]byte, domain.CanonicalHash, error) {
	switch request.Operation {
	case "create_session", "submit_command", "cancel_command", "close_session", "run":
		payload := request.RawJSON
		if request.Operation == "close_session" {
			policy := strings.TrimSpace(request.ClosePolicy)
			if policy == "" {
				policy = "cancel"
			}
			encoded, err := json.Marshal(struct {
				Operation string `json:"operation"`
				SessionID string `json:"session_id"`
				Policy    string `json:"policy"`
			}{Operation: request.Operation, SessionID: request.SessionID, Policy: policy})
			if err != nil {
				return nil, domain.CanonicalHash{}, fmt.Errorf("encode close-session receipt: %w", err)
			}
			payload = encoded
		}
		canonical, err := domain.CanonicalizeMutationRequestJSON(request.Operation, payload, domain.CanonicalizationOptions{})
		if err != nil {
			return nil, domain.CanonicalHash{}, err
		}
		hash, err := domain.HashMutationRequestJSON(request.Operation, payload, domain.CanonicalizationOptions{})
		return canonical, hash, err
	default:
		digest := sha256.Sum256(request.RawJSON)
		hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
		return append([]byte(nil), request.RawJSON...), hash, err
	}
}
