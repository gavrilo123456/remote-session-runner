package localapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

var errP099InjectedCrash = errors.New("p099 injected crash after local intent commit")

type p099Case struct {
	operation string
	requestID string
	key       string
	request   map[string]any
}

func p099PrepareCase(t *testing.T, h *p095Harness, operation string) p099Case {
	t.Helper()
	requestID := "req-p099-" + operation
	key := "key-p099-" + operation
	request := map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": operation,
	}
	switch operation {
	case "create_session":
		request["environment"] = "mac-dev"
		request["execution_target"] = map[string]string{"kind": string(domain.TargetKindLocal), "profile": "mac-workstation"}
		request["source"] = map[string]string{"mode": "empty"}
	case "submit_command":
		sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
		request["session_id"] = sessionID
		request["script"] = "echo p099-submit"
		request["timeout_seconds"] = 30
	case "cancel_command":
		sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
		commandID := h.submitQueuedCommand(t, "req-p099-setup-cancel", "key-p099-setup-cancel", sessionID)
		request["command_id"] = commandID
	case "close_session":
		sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
		request["session_id"] = sessionID
	default:
		t.Fatalf("unsupported P099 operation %q", operation)
	}
	return p099Case{operation: operation, requestID: requestID, key: key, request: request}
}

func p099NewProcessor(t *testing.T, h *p095Harness, operations mailbox.SessionOperations) *mailbox.SessionProcessor {
	t.Helper()
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: h.importer, Authority: h.authority, Controller: p063Owner(t), Operations: operations,
		Outbox: h.outbox, EventFiles: h.eventFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func TestP099RestartAfterMutationCommitBeforeResponsePublication(t *testing.T) {
	for _, operation := range []string{"create_session", "submit_command", "cancel_command", "close_session"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			h, _ := newP096Harness(t)
			request := p099PrepareCase(t, h, operation)
			writeP094Request(t, h.importer, request.requestID, request.request)

			operations := &p099Operations{delegate: h.server, crashAfter: operation, calls: make(map[string]int)}
			first := p099NewProcessor(t, h, operations)
			results, err := first.Import(ctx)
			if err != nil || len(results) != 1 || results[0].Durable || results[0].PairRemoved {
				t.Fatalf("interrupted import results=%+v err=%v, want an unremoved pair", results, err)
			}
			if _, err := h.outbox.Read(request.requestID); err == nil {
				t.Fatal("response was published despite injected stop before response commit")
			}
			receipt, err := h.authority.GetMailboxExchange(ctx, request.requestID)
			if err != nil || receipt.State != store.MailboxExchangeAccepted || len(receipt.ResponseBytes) != 0 {
				t.Fatalf("receipt at injected stop=%+v err=%v", receipt, err)
			}
			intentBefore := pMailboxIntentForRequest(t, h.authority, operation, request.requestID)
			if intentBefore.IntentID == "" {
				t.Fatalf("committed local intent=%+v", intentBefore)
			}
			p099AssertPairPresent(t, h.importer.InboxPath(), request.requestID)

			// A fresh processor models startup from the durable receipt and the
			// same idempotency key after the process stopped at the API boundary.
			restarted := p099NewProcessor(t, h, operations)
			results, err = restarted.Import(ctx)
			if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("restarted import results=%+v err=%v", results, err)
			}
			intentAfter := pMailboxIntentForRequest(t, h.authority, operation, request.requestID)
			if intentAfter.IntentID != intentBefore.IntentID {
				t.Fatalf("restart created a different intent: before=%+v after=%+v", intentBefore, intentAfter)
			}
			response := readP095Response(t, h.outbox, request.requestID)
			if response.RequestID != request.requestID || response.Operation != operation || response.RequestState == "rejected" || response.Error != nil {
				t.Fatalf("recovered response=%+v", response)
			}
			p099AssertPairAbsent(t, h.importer.InboxPath(), request.requestID)
			if operations.calls[operation] != 2 {
				t.Fatalf("operation call count=%d after retry, want one initial and one idempotent replay", operations.calls[operation])
			}
		})
	}
}

func TestP099RestartRebuildsStoredResponseWithoutRepeatingMutation(t *testing.T) {
	for _, operation := range []string{"create_session", "submit_command", "cancel_command", "close_session"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			h, _ := newP096Harness(t)
			request := p099PrepareCase(t, h, operation)
			writeP094Request(t, h.importer, request.requestID, request.request)

			operations := &p099Operations{delegate: h.server, calls: make(map[string]int)}
			first := p099NewProcessor(t, h, operations)
			outboxDir := filepath.Dir(mustP099OutboxPath(t, h.outbox, request.requestID))
			savedOutboxDir := outboxDir + ".p099-saved"
			if err := os.Rename(outboxDir, savedOutboxDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outboxDir, []byte("blocked"), 0o600); err != nil {
				_ = os.Rename(savedOutboxDir, outboxDir)
				t.Fatal(err)
			}
			results, err := first.Import(ctx)
			if err == nil || len(results) != 1 || results[0].Durable || results[0].PairRemoved {
				_ = os.Remove(outboxDir)
				_ = os.Rename(savedOutboxDir, outboxDir)
				t.Fatalf("projection interruption results=%+v err=%v, want a reported projection failure and unremoved pair", results, err)
			}
			if err := os.Remove(outboxDir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(savedOutboxDir, outboxDir); err != nil {
				t.Fatal(err)
			}
			if operations.calls[operation] != 1 {
				t.Fatalf("mutation calls before restart=%d, want 1", operations.calls[operation])
			}
			receipt, err := h.authority.GetMailboxExchange(ctx, request.requestID)
			if err != nil || len(receipt.ResponseBytes) == 0 {
				t.Fatalf("stored response at projection interruption=%+v err=%v", receipt, err)
			}
			p099AssertPairPresent(t, h.importer.InboxPath(), request.requestID)

			// The duplicate receipt takes the stored-response projection path;
			// the mutation adapter must not be called again.
			restarted := p099NewProcessor(t, h, operations)
			results, err = restarted.Import(ctx)
			if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("restarted projection results=%+v err=%v", results, err)
			}
			if operations.calls[operation] != 1 {
				t.Fatalf("restart repeated committed %s mutation; calls=%d", operation, operations.calls[operation])
			}
			projected, err := h.outbox.Read(request.requestID)
			if err != nil || !bytes.Equal(projected, receipt.ResponseBytes) {
				t.Fatalf("rebuilt response differs from durable bytes: equal=%v err=%v", bytes.Equal(projected, receipt.ResponseBytes), err)
			}
			response := readP095Response(t, h.outbox, request.requestID)
			if response.RequestID != request.requestID || response.Operation != operation || response.RequestState == "rejected" || response.Error != nil {
				t.Fatalf("rebuilt response=%+v", response)
			}
			p099AssertPairAbsent(t, h.importer.InboxPath(), request.requestID)
		})
	}
}

func mustP099OutboxPath(t *testing.T, outbox *mailbox.Outbox, requestID string) string {
	t.Helper()
	path, err := outbox.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func p099AssertPairPresent(t *testing.T, inbox, requestID string) {
	t.Helper()
	for _, suffix := range []string{mailbox.RequestSuffix, mailbox.ReadySuffix} {
		if _, err := os.Stat(filepath.Join(inbox, requestID+suffix)); err != nil {
			t.Fatalf("expected inbox pair member %s to remain: %v", suffix, err)
		}
	}
}

func p099AssertPairAbsent(t *testing.T, inbox, requestID string) {
	t.Helper()
	for _, suffix := range []string{mailbox.RequestSuffix, mailbox.ReadySuffix} {
		if _, err := os.Stat(filepath.Join(inbox, requestID+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inbox pair member %s remains or stat failed: %v", suffix, err)
		}
	}
}

type p099Operations struct {
	delegate   mailbox.SessionOperations
	crashAfter string
	calls      map[string]int
}

func (o *p099Operations) SessionController() domain.ControllerIdentity {
	return o.delegate.SessionController()
}

func (o *p099Operations) afterCommit(operation string, err error) error {
	o.calls[operation]++
	if err == nil && o.crashAfter == operation {
		o.crashAfter = ""
		return errP099InjectedCrash
	}
	return err
}

func (o *p099Operations) CreateSessionIntent(ctx context.Context, request mailbox.Request) (mailbox.SessionIntent, error) {
	intent, err := o.delegate.CreateSessionIntent(ctx, request)
	return intent, o.afterCommit("create_session", err)
}

func (o *p099Operations) GetSession(ctx context.Context, sessionID string) (mailbox.SessionSnapshot, error) {
	return o.delegate.GetSession(ctx, sessionID)
}

func (o *p099Operations) SubmitCommandIntent(ctx context.Context, request mailbox.Request) (mailbox.CommandIntent, error) {
	intent, err := o.delegate.SubmitCommandIntent(ctx, request)
	return intent, o.afterCommit("submit_command", err)
}

func (o *p099Operations) GetCommandSnapshot(ctx context.Context, commandID string) (mailbox.CommandSnapshot, error) {
	return o.delegate.GetCommandSnapshot(ctx, commandID)
}

func (o *p099Operations) CancelCommandIntent(ctx context.Context, request mailbox.Request) (mailbox.CommandIntent, error) {
	intent, err := o.delegate.CancelCommandIntent(ctx, request)
	return intent, o.afterCommit("cancel_command", err)
}

func (o *p099Operations) GetCancelCommandSnapshot(ctx context.Context, commandID, key string) (mailbox.CancelCommandSnapshot, error) {
	return o.delegate.GetCancelCommandSnapshot(ctx, commandID, key)
}

func (o *p099Operations) CloseSessionIntent(ctx context.Context, request mailbox.Request) (mailbox.SessionIntent, error) {
	intent, err := o.delegate.CloseSessionIntent(ctx, request)
	return intent, o.afterCommit("close_session", err)
}

func (o *p099Operations) GetCloseSessionSnapshot(ctx context.Context, sessionID, key string) (mailbox.CloseSessionSnapshot, error) {
	return o.delegate.GetCloseSessionSnapshot(ctx, sessionID, key)
}

func (o *p099Operations) RunJobIntent(ctx context.Context, request mailbox.Request) (mailbox.RunIntent, error) {
	intent, err := o.delegate.RunJobIntent(ctx, request)
	return intent, o.afterCommit("run", err)
}

func (o *p099Operations) GetRunSnapshot(ctx context.Context, jobID string) (mailbox.RunSnapshot, error) {
	return o.delegate.GetRunSnapshot(ctx, jobID)
}
