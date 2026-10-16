package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	return receiptCanonicalWithSelection(request, nil)
}

// receiptCanonicalWithSelection adds the already-resolved effective target to
// a new-work receipt only when the mailbox request omitted it. The canonical
// payload therefore binds defaulted work to the original target before its
// durable exchange is accepted.
func receiptCanonicalWithSelection(request Request, selection *store.MailboxExecutionSelection) ([]byte, domain.CanonicalHash, error) {
	switch request.Operation {
	case "create_session", "submit_command", "cancel_command", "close_session", "run":
		payload := request.RawJSON
		if request.Operation == "run" {
			if normalized, normalizeErr := NormalizeRunTimeout(payload); normalizeErr == nil {
				payload = normalized
			}
		}
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
		options, err := mailboxReceiptCanonicalizationOptions(request.Operation, selection)
		if err != nil {
			return nil, domain.CanonicalHash{}, err
		}
		canonical, err := domain.CanonicalizeMutationRequestJSON(request.Operation, payload, options)
		if err != nil {
			return nil, domain.CanonicalHash{}, err
		}
		hash, err := domain.HashMutationRequestJSON(request.Operation, payload, options)
		return canonical, hash, err
	default:
		digest := sha256.Sum256(request.RawJSON)
		hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
		return append([]byte(nil), request.RawJSON...), hash, err
	}
}

// NormalizeRunTimeout maps the public timeout_seconds alias into
// limits.command_timeout_seconds. Alias-only and equivalent explicit-limit
// run requests then have the same durable idempotency payload and downstream
// request shape. Callers that need to return a validation error should surface
// the returned error; receipt hashing can retain the original request when
// normalization fails so the operation handler can publish a terminal reject.
func NormalizeRunTimeout(raw []byte) ([]byte, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(raw, &request); err != nil || request == nil {
		return nil, fmt.Errorf("run request must be a JSON object")
	}
	timeoutRaw, present := request["timeout_seconds"]
	if !present {
		return append([]byte(nil), raw...), nil
	}
	trimmedTimeout := bytes.TrimSpace(timeoutRaw)
	var seconds int64
	if len(trimmedTimeout) == 0 || bytes.Equal(trimmedTimeout, []byte("null")) || json.Unmarshal(trimmedTimeout, &seconds) != nil || seconds <= 0 {
		return nil, fmt.Errorf("timeout_seconds must be a positive integer")
	}
	const maxDurationSeconds int64 = (1<<63 - 1) / int64(time.Second)
	if seconds > maxDurationSeconds {
		return nil, fmt.Errorf("timeout_seconds exceeds the supported duration")
	}

	limits := make(map[string]json.RawMessage)
	if rawLimits, ok := request["limits"]; ok {
		if err := json.Unmarshal(rawLimits, &limits); err != nil || limits == nil {
			return nil, fmt.Errorf("limits must be a JSON object")
		}
	}
	if explicit, ok := limits["command_timeout_seconds"]; ok {
		trimmed := bytes.TrimSpace(explicit)
		var explicitSeconds int64
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(trimmed, &explicitSeconds) != nil || explicitSeconds < 0 || explicitSeconds > maxDurationSeconds {
			return nil, fmt.Errorf("limits.command_timeout_seconds must be a supported nonnegative integer")
		}
		if explicitSeconds > 0 && explicitSeconds != seconds {
			return nil, fmt.Errorf("timeout_seconds conflicts with limits.command_timeout_seconds")
		}
	}
	value, _ := json.Marshal(seconds)
	limits["command_timeout_seconds"] = value
	limitsJSON, err := json.Marshal(limits)
	if err != nil {
		return nil, fmt.Errorf("encode normalized limits: %w", err)
	}
	delete(request, "timeout_seconds")
	request["limits"] = limitsJSON
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode normalized run request: %w", err)
	}
	return normalized, nil
}

func mailboxReceiptCanonicalizationOptions(operation string, selection *store.MailboxExecutionSelection) (domain.CanonicalizationOptions, error) {
	if selection == nil || (operation != "create_session" && operation != "run") {
		return domain.CanonicalizationOptions{}, nil
	}
	environment, err := json.Marshal(selection.Environment)
	if err != nil {
		return domain.CanonicalizationOptions{}, fmt.Errorf("encode mailbox selection environment: %w", err)
	}
	target, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	}{Kind: string(selection.Target.Kind()), Profile: selection.Target.Profile()})
	if err != nil {
		return domain.CanonicalizationOptions{}, fmt.Errorf("encode mailbox selection target: %w", err)
	}
	return domain.CanonicalizationOptions{Defaults: map[string]json.RawMessage{
		"environment":      environment,
		"execution_target": target,
	}}, nil
}
