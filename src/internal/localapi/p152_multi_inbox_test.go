package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

// TestP152TwoMailboxRootsKeepEventProjectionAndCleanupIsolated instantiates
// two explicit mailbox runtimes against one authority store. P154 owns their
// production composition; this P152 fixture proves the durable namespace is
// already sufficient to isolate same-name response and event artifacts.
func TestP152TwoMailboxRootsKeepEventProjectionAndCleanupIsolated(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	owner := p063Owner(t)
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{
		Authority:  authority,
		Owner:      owner,
		SocketPath: filepath.Join(root, "run", "local-api.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	commandID := p152CreateTerminalLocalCommand(t, ctx, authority, server, owner)
	defaultRuntime := p152NewMailboxRuntime(t, authority, server, owner, store.DefaultMailboxID, filepath.Join(root, "mailboxes", "default"))
	repositoryRuntime := p152NewMailboxRuntime(t, authority, server, owner, "repo-alpha", filepath.Join(root, "mailboxes", "repo-alpha"))

	const requestID = "req-p152-shared-event-projection"
	request := map[string]any{
		"request_id": requestID,
		"operation":  "get_command",
		"command_id": string(commandID),
	}
	p152ImportMailboxRequest(t, ctx, defaultRuntime, requestID, request)
	p152ImportMailboxRequest(t, ctx, repositoryRuntime, requestID, request)

	defaultResponse := readP095Response(t, defaultRuntime.outbox, requestID)
	repositoryResponse := readP095Response(t, repositoryRuntime.outbox, requestID)
	if defaultResponse.RequestState != "complete" || repositoryResponse.RequestState != "complete" ||
		defaultResponse.CommandID != string(commandID) || repositoryResponse.CommandID != string(commandID) ||
		defaultResponse.AvailableEventSequence == nil || repositoryResponse.AvailableEventSequence == nil ||
		*defaultResponse.AvailableEventSequence != *repositoryResponse.AvailableEventSequence {
		t.Fatalf("two-root command responses default=%+v repo=%+v", defaultResponse, repositoryResponse)
	}
	defaultEvents, defaultCursor, err := defaultRuntime.events.Read(commandID)
	if err != nil || defaultCursor != *defaultResponse.AvailableEventSequence || len(defaultEvents) == 0 {
		t.Fatalf("default event projection cursor=%d bytes=%d err=%v", defaultCursor, len(defaultEvents), err)
	}
	repositoryEvents, repositoryCursor, err := repositoryRuntime.events.Read(commandID)
	if err != nil || repositoryCursor != *repositoryResponse.AvailableEventSequence || string(repositoryEvents) != string(defaultEvents) {
		t.Fatalf("repository event projection cursor=%d bytes=%d err=%v", repositoryCursor, len(repositoryEvents), err)
	}

	defaultRef, err := store.NewMailboxExchangeRef(store.DefaultMailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	repositoryRef, err := store.NewMailboxExchangeRef("repo-alpha", requestID)
	if err != nil {
		t.Fatal(err)
	}
	defaultRecord, err := authority.GetMailboxExchangeInMailbox(ctx, defaultRef)
	if err != nil {
		t.Fatal(err)
	}
	repositoryRecord, err := authority.GetMailboxExchangeInMailbox(ctx, repositoryRef)
	if err != nil {
		t.Fatal(err)
	}
	if defaultRecord.ExchangeID == repositoryRecord.ExchangeID || defaultRecord.EventFileCommandID != string(commandID) || repositoryRecord.EventFileCommandID != string(commandID) {
		t.Fatalf("event references were not independently scoped: default=%+v repo=%+v", defaultRecord, repositoryRecord)
	}

	// Simulate a crash after one root loses its projections. Reconciliation for
	// the other root must not repair files outside its namespace.
	if err := repositoryRuntime.outbox.Remove(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := repositoryRuntime.events.Remove(ctx, commandID); err != nil {
		t.Fatal(err)
	}
	p152AssertMailboxArtifactMissing(t, repositoryRuntime.outbox, repositoryRuntime.events, requestID, commandID)
	if err := defaultRuntime.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	p152AssertMailboxArtifactMissing(t, repositoryRuntime.outbox, repositoryRuntime.events, requestID, commandID)
	if err := repositoryRuntime.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	p152AssertMailboxArtifactPresent(t, repositoryRuntime.outbox, repositoryRuntime.events, requestID, commandID)
	repositoryResponse = readP095Response(t, repositoryRuntime.outbox, requestID)

	defaultAcks, err := mailbox.NewAckImporter(mailbox.AckImporterOptions{
		MailboxID: store.DefaultMailboxID, Root: defaultRuntime.root, Authority: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	p152WriteAckPair(t, defaultAcks, requestID, defaultResponse.ResponseRevision, defaultResponse.AvailableEventSequence)
	ackResults, err := defaultAcks.Import(ctx)
	if err != nil || len(ackResults) != 1 || !ackResults[0].Durable || !ackResults[0].PairRemoved {
		t.Fatalf("default ACK import results=%+v err=%v", ackResults, err)
	}
	unchangedRepository, err := authority.GetMailboxExchangeInMailbox(ctx, repositoryRef)
	if err != nil || unchangedRepository.AcknowledgedAt != nil || unchangedRepository.ResponseCleanupAt == nil {
		t.Fatalf("default ACK altered repository receipt=%+v err=%v", unchangedRepository, err)
	}

	now = now.Add(store.MailboxAckedResponseLifetime + time.Hour)
	report, err := (mailbox.ArtifactCleaner{
		MailboxID: store.DefaultMailboxID, Authority: authority,
		Outbox: defaultRuntime.outbox, EventFiles: defaultRuntime.events,
	}).Run(ctx)
	if err != nil || report.ResponsesRemoved != 1 || report.EventFilesRemoved != 1 {
		t.Fatalf("default cleanup report=%+v err=%v", report, err)
	}
	p152AssertMailboxArtifactMissing(t, defaultRuntime.outbox, defaultRuntime.events, requestID, commandID)
	p152AssertMailboxArtifactPresent(t, repositoryRuntime.outbox, repositoryRuntime.events, requestID, commandID)

	repositoryAfterCleanup, err := authority.GetMailboxExchangeInMailbox(ctx, repositoryRef)
	if err != nil || repositoryAfterCleanup.AcknowledgedAt != nil || repositoryAfterCleanup.ResponseCleanupStartedAt != nil || repositoryAfterCleanup.ResponseFileRemovedAt != nil {
		t.Fatalf("default cleanup altered repository exchange=%+v err=%v", repositoryAfterCleanup, err)
	}
	if _, _, err := (mailbox.EventProjector{Authority: authority}).ProjectMailboxResponseThroughInMailbox(ctx, repositoryRef, commandID); err != nil {
		t.Fatalf("repository event projection stopped after default cleanup: %v", err)
	}
}

// TestP152TwoMailboxRootsScopeMutationExecutionIdentity exercises the real
// mailbox processor boundary: the same client key in two roots reaches the
// local-intent layer as two opaque, mailbox-scoped keys.
func TestP152TwoMailboxRootsScopeMutationExecutionIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	owner := p063Owner(t)
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{
		Authority:  authority,
		Owner:      owner,
		SocketPath: filepath.Join(root, "run", "local-api.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	defaultRuntime := p152NewMailboxRuntime(t, authority, server, owner, store.DefaultMailboxID, filepath.Join(root, "mailboxes", "default"))
	repositoryRuntime := p152NewMailboxRuntime(t, authority, server, owner, "repo-alpha", filepath.Join(root, "mailboxes", "repo-alpha"))
	const requestID = "req-p152-shared-mutation"
	const clientKey = "key-p152-shared-mutation"
	request := p100RunRequest(requestID, clientKey, "local", "mac-workstation", "mac-dev", "printf p152-scoped")
	p152ImportMailboxRequest(t, ctx, defaultRuntime, requestID, request)
	p152ImportMailboxRequest(t, ctx, repositoryRuntime, requestID, request)

	defaultRef, err := store.NewMailboxExchangeRef(store.DefaultMailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	repositoryRef, err := store.NewMailboxExchangeRef("repo-alpha", requestID)
	if err != nil {
		t.Fatal(err)
	}
	defaultRecord, err := authority.GetMailboxExchangeInMailbox(ctx, defaultRef)
	if err != nil {
		t.Fatal(err)
	}
	repositoryRecord, err := authority.GetMailboxExchangeInMailbox(ctx, repositoryRef)
	if err != nil {
		t.Fatal(err)
	}
	if defaultRecord.IdempotencyKey != clientKey || repositoryRecord.IdempotencyKey != clientKey ||
		defaultRecord.ExecutionIdempotencyKey == "" || repositoryRecord.ExecutionIdempotencyKey == "" ||
		defaultRecord.ExecutionIdempotencyKey == clientKey || repositoryRecord.ExecutionIdempotencyKey == clientKey ||
		defaultRecord.ExecutionIdempotencyKey == repositoryRecord.ExecutionIdempotencyKey {
		t.Fatalf("scoped mailbox records default=%+v repository=%+v", defaultRecord, repositoryRecord)
	}
	defaultIntent, err := authority.GetLocalIntentByIdempotency(ctx, "run", defaultRecord.ExecutionIdempotencyKey, owner)
	if err != nil {
		t.Fatal(err)
	}
	repositoryIntent, err := authority.GetLocalIntentByIdempotency(ctx, "run", repositoryRecord.ExecutionIdempotencyKey, owner)
	if err != nil {
		t.Fatal(err)
	}
	if defaultIntent.IdempotencyKey != defaultRecord.ExecutionIdempotencyKey ||
		repositoryIntent.IdempotencyKey != repositoryRecord.ExecutionIdempotencyKey ||
		defaultIntent.IntentID == repositoryIntent.IntentID || defaultIntent.JobID == repositoryIntent.JobID ||
		defaultIntent.CommandID == repositoryIntent.CommandID || defaultIntent.SessionID == repositoryIntent.SessionID {
		t.Fatalf("mailbox mutation intents were not isolated: default=%+v repository=%+v", defaultIntent, repositoryIntent)
	}
}

type p152MailboxRuntime struct {
	root      string
	importer  *mailbox.Importer
	outbox    *mailbox.Outbox
	events    *mailbox.EventFiles
	processor *mailbox.SessionProcessor
}

func p152NewMailboxRuntime(t *testing.T, authority *store.AuthorityStore, server *Server, owner domain.ControllerIdentity, mailboxID, root string) p152MailboxRuntime {
	t.Helper()
	importer, err := mailbox.New(mailbox.Options{MailboxID: mailboxID, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := mailbox.NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	events, err := mailbox.NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		MailboxID: mailboxID, Importer: importer, Authority: authority, Controller: owner,
		Operations: server, Outbox: outbox, EventFiles: events,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p152MailboxRuntime{root: root, importer: importer, outbox: outbox, events: events, processor: processor}
}

func p152ImportMailboxRequest(t *testing.T, ctx context.Context, runtime p152MailboxRuntime, requestID string, request any) {
	t.Helper()
	writeP094Request(t, runtime.importer, requestID, request)
	results, err := runtime.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("mailbox import %s results=%+v err=%v", requestID, results, err)
	}
}

func p152WriteAckPair(t *testing.T, importer *mailbox.AckImporter, requestID string, revision int64, cursor *int64) {
	t.Helper()
	value := map[string]any{"request_id": requestID, "response_revision": revision}
	if cursor != nil {
		value["available_event_sequence"] = *cursor
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		requestID + mailbox.RequestSuffix: raw,
		requestID + mailbox.ReadySuffix:   nil,
	} {
		path := filepath.Join(importer.AcksPath(), name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func p152CreateTerminalLocalCommand(t *testing.T, ctx context.Context, authority *store.AuthorityStore, server *Server, owner domain.ControllerIdentity) domain.CommandID {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	createKey := "key-p152-shared-events-create"
	createRaw, err := json.Marshal(map[string]any{
		"request_id": "req-p152-shared-events-create", "idempotency_key": createKey,
		"operation": "create_session", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source":           map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	create, err := server.CreateSessionIntent(ctx, mailbox.Request{
		MailboxID: store.DefaultMailboxID, RequestID: "req-p152-shared-events-create", IdempotencyKey: createKey,
		ExecutionIdempotencyKey: pMailboxTestExecutionKey(createKey), Operation: "create_session", RawJSON: createRaw,
	})
	if err != nil || create.SessionID == "" {
		t.Fatalf("create bootstrap intent=%+v err=%v", create, err)
	}
	sessionID, err := domain.NewSessionID(create.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err := authority.GetLocalIntentByResource(ctx, "create_session", create.SessionID, owner)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err = authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentDispatching, "p152-bootstrap-create-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentAccepted, "p152-bootstrap-create-accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateSession(ctx, store.SessionCreate{
		SessionID: sessionID, Target: target, Environment: "mac-dev", Controller: owner,
		Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, sessionID, domain.SessionStateReady, "p152-test-session-ready"); err != nil {
		t.Fatal(err)
	}
	submitKey := "key-p152-shared-events-submit"
	submitRaw, err := json.Marshal(map[string]any{
		"request_id": "req-p152-shared-events-submit", "idempotency_key": submitKey,
		"operation": "submit_command", "session_id": string(sessionID), "script": "printf p152", "timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := server.SubmitCommandIntent(ctx, mailbox.Request{
		MailboxID: store.DefaultMailboxID, RequestID: "req-p152-shared-events-submit", IdempotencyKey: submitKey,
		ExecutionIdempotencyKey: pMailboxTestExecutionKey(submitKey), Operation: "submit_command", SessionID: string(sessionID), RawJSON: submitRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	submitIntent, err := authority.GetLocalIntentByResource(ctx, "submit_command", submitted.CommandID, owner)
	if err != nil {
		t.Fatal(err)
	}
	submitIntent, err = authority.TransitionLocalIntent(ctx, submitIntent.IntentID, store.LocalIntentDispatching, "p152-bootstrap-submit-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, submitIntent.IntentID, store.LocalIntentAccepted, "p152-bootstrap-submit-accepted"); err != nil {
		t.Fatal(err)
	}
	command, _, err := authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: submitIntent.CommandID, SessionID: sessionID, IdempotencyKey: submitIntent.IdempotencyKey,
		RequestHash: submitIntent.RequestHash, Script: string(submitIntent.ScriptBytes), Timeout: 30 * time.Second, IntentOrdinal: *submitIntent.IntentOrdinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	output := []byte("P152_SHARED_EVENT_OK\n")
	if _, err := authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: output, ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: &exitCode, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	return command.CommandID
}

func p152AssertMailboxArtifactMissing(t *testing.T, outbox *mailbox.Outbox, events *mailbox.EventFiles, requestID string, commandID domain.CommandID) {
	t.Helper()
	responsePath, err := outbox.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	eventPath, err := events.Path(commandID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{responsePath, eventPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup left artifact %s: err=%v", path, err)
		}
	}
}

func p152AssertMailboxArtifactPresent(t *testing.T, outbox *mailbox.Outbox, events *mailbox.EventFiles, requestID string, commandID domain.CommandID) {
	t.Helper()
	responsePath, err := outbox.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	eventPath, err := events.Path(commandID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{responsePath, eventPath} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("expected live artifact %s: info=%v err=%v", path, info, err)
		}
	}
}
