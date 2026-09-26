package mailbox

import (
	"context"
	"crypto/sha256"
	"fmt"

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
	handler    Handler
}

// ReceiptProcessorOptions configures the P082 receipt boundary.
type ReceiptProcessorOptions struct {
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
	return &ReceiptProcessor{importer: options.Importer, authority: options.Authority, controller: controller, handler: options.Handler}, nil
}

// Import records each validated request before calling Handler. A terminal
// exchange is replayed without another callback. An accepted receipt can be
// resumed after a crash; external side effects still require their own
// idempotent target boundary and are not claimed exactly once here.
func (p *ReceiptProcessor) Import(ctx context.Context) ([]Result, error) {
	if p == nil || p.importer == nil || p.authority == nil {
		return nil, ErrImporterConfiguration
	}
	return p.importer.importWithHandler(ctx, p.process)
}

func (p *ReceiptProcessor) process(ctx context.Context, request Request) error {
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		return err
	}
	record, duplicate, err := p.authority.AcceptMailboxExchange(ctx, store.MailboxExchangeCreate{
		RequestID: request.RequestID, Operation: request.Operation, Controller: p.controller,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
	if err != nil {
		return err
	}
	if duplicate && record.State != store.MailboxExchangeAccepted {
		return nil
	}
	if p.handler == nil {
		return nil
	}
	if err := p.handler(ctx, request); err != nil {
		_, completeErr := p.authority.CompleteMailboxExchange(ctx, request.RequestID, store.MailboxExchangeRejected)
		if completeErr != nil {
			return fmt.Errorf("%w; receipt rejection: %v", err, completeErr)
		}
		return err
	}
	_, err = p.authority.CompleteMailboxExchange(ctx, request.RequestID, store.MailboxExchangeComplete)
	return err
}

func receiptCanonical(request Request) ([]byte, domain.CanonicalHash, error) {
	switch request.Operation {
	case "create_session", "submit_command", "cancel_command", "close_session", "run":
		canonical, err := domain.CanonicalizeMutationRequestJSON(request.Operation, request.RawJSON, domain.CanonicalizationOptions{})
		if err != nil {
			return nil, domain.CanonicalHash{}, err
		}
		hash, err := domain.HashMutationRequestJSON(request.Operation, request.RawJSON, domain.CanonicalizationOptions{})
		return canonical, hash, err
	default:
		digest := sha256.Sum256(request.RawJSON)
		hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
		return append([]byte(nil), request.RawJSON...), hash, err
	}
}
