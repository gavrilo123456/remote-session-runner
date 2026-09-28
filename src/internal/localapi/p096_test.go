package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP096D05M08MailboxRetryConflictAndOutputExpiry(t *testing.T) {
	ctx := context.Background()
	h, now := newP096Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
	commandID := h.submitQueuedCommand(t, "req-p096-original", "key-p096-original", sessionID)
	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: domain.CommandID(commandID), NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	output := []byte("retained output\n")
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: domain.CommandID(commandID), Type: "stdout", Payload: output, ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: domain.CommandID(commandID), NextState: domain.CommandStateSucceeded, ExitCode: &exitCode, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	originalBytes, err := h.outbox.Read("req-p096-original")
	if err != nil {
		t.Fatal(err)
	}
	original := readP095Response(t, h.outbox, "req-p096-original")
	if original.RequestState != "complete" || original.CommandID != commandID || original.OutputComplete == nil || !*original.OutputComplete {
		t.Fatalf("initial terminal response=%+v", original)
	}

	*now = now.Add(store.DefaultOutputRetention + time.Minute)
	gc, err := h.authority.CollectGarbage(ctx, store.GarbageCollectionOptions{})
	if err != nil || gc.CommandsOutputExpired != 1 {
		t.Fatalf("output expiry GC=%+v err=%v", gc, err)
	}
	writeP096RawRequest(t, h.importer, "req-p096-retry", []byte("{\n  \"script\": \"echo queued\", \"session_id\": \""+sessionID+"\", \"operation\": \"submit_command\", \"timeout_seconds\": 30, \"idempotency_key\": \"key-p096-original\", \"request_id\": \"req-p096-retry\"\n}"))
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("same-key retry results=%+v err=%v", results, err)
	}
	retry := readP095Response(t, h.outbox, "req-p096-retry")
	if retry.CommandID != commandID || retry.RequestState != "complete" || retry.CommandState != string(domain.CommandStateSucceeded) || retry.OutputComplete == nil || *retry.OutputComplete || retry.OutputUnavailableReason != "retention_expired" || retry.AvailableEventSequence == nil || *retry.AvailableEventSequence != 0 || retry.EventsFile != "" {
		t.Fatalf("same-key retry did not return retained metadata after output expiry: %+v", retry)
	}

	writeP094Request(t, h.importer, "req-p096-original", map[string]any{
		"request_id": "req-p096-original", "idempotency_key": "key-p096-original", "operation": "submit_command",
		"session_id": sessionID, "script": "echo queued", "timeout_seconds": 30,
	})
	replayResults, err := h.processor.Import(ctx)
	if err != nil || len(replayResults) != 1 || replayResults[0].Status != mailbox.ResultRejected || !replayResults[0].Durable || !replayResults[0].PairRemoved {
		t.Fatalf("expired original request replay=%+v err=%v", replayResults, err)
	}
	replayedBytes, err := h.outbox.Read("req-p096-original")
	if err != nil || string(replayedBytes) != string(originalBytes) {
		t.Fatalf("expired original request replay changed its response: equal=%v err=%v", string(replayedBytes) == string(originalBytes), err)
	}

	writeP094Request(t, h.importer, "req-p096-changed", map[string]any{
		"request_id": "req-p096-changed", "idempotency_key": "key-p096-original", "operation": "submit_command",
		"session_id": sessionID, "script": "echo changed", "timeout_seconds": 30,
	})
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("changed-payload retry results=%+v err=%v", results, err)
	}
	conflict := readP095Response(t, h.outbox, "req-p096-changed")
	if conflict.RequestState != "rejected" || conflict.Error == nil || conflict.Error.Code != "idempotency_conflict" || conflict.CommandID != "" {
		t.Fatalf("changed payload did not reject only in its new outbox: %+v", conflict)
	}
	unchangedBytes, err := h.outbox.Read("req-p096-original")
	if err != nil || string(unchangedBytes) != string(originalBytes) {
		t.Fatalf("original response was overwritten: equal=%v err=%v", string(unchangedBytes) == string(originalBytes), err)
	}

	writeP094Request(t, h.importer, "req-p096-original", map[string]any{
		"request_id": "req-p096-original", "idempotency_key": "key-p096-original", "operation": "submit_command",
		"session_id": sessionID, "script": "echo changed", "timeout_seconds": 30,
	})
	reuseResults, err := h.processor.Import(ctx)
	if err != nil || len(reuseResults) != 1 || reuseResults[0].Durable || reuseResults[0].PairRemoved {
		t.Fatalf("reused request ID results=%+v err=%v, want it rejected without replacing the response", reuseResults, err)
	}
	unchangedBytes, err = h.outbox.Read("req-p096-original")
	if err != nil || string(unchangedBytes) != string(originalBytes) {
		t.Fatalf("reused request ID changed original terminal response: equal=%v err=%v", string(unchangedBytes) == string(originalBytes), err)
	}
}

func TestP096D05ExpiredCreateKeyWarnsInFileResponse(t *testing.T) {
	h, now := newP096Harness(t)
	request := func(requestID string) map[string]any {
		return map[string]any{
			"request_id": requestID, "idempotency_key": "key-p096-expired-create", "operation": "create_session",
			"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
			"source": map[string]string{"mode": "empty"},
		}
	}
	writeP094Request(t, h.importer, "req-p096-create-first", request("req-p096-create-first"))
	if _, err := h.processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstRaw, err := h.outbox.Read("req-p096-create-first")
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(firstRaw, &first); err != nil || first.SessionID == "" {
		t.Fatalf("first response=%s err=%v", firstRaw, err)
	}
	*now = now.Add(store.DefaultSessionIdempotencyRetention)
	writeP094Request(t, h.importer, "req-p096-create-expired", request("req-p096-create-expired"))
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("expired-key create results=%+v err=%v", results, err)
	}
	responseRaw, err := h.outbox.Read("req-p096-create-expired")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		RequestState       string `json:"request_state"`
		SessionID          string `json:"session_id"`
		IdempotencyWarning string `json:"idempotency_warning"`
	}
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestState != "accepted" || response.SessionID == "" || response.SessionID == first.SessionID || response.IdempotencyWarning != "deduplication_not_guaranteed" {
		t.Fatalf("expired-key response=%s", responseRaw)
	}
}

func newP096Harness(t *testing.T) (*p095Harness, *time.Time) {
	t.Helper()
	ctx := context.Background()
	root := testfixture.New(t)
	db, err := store.Open(ctx, filepath.Join(root.Path(), "state", "p096-localapi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	authority, err := store.NewAuthorityStoreWithClock(db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "rsr-p096-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "local-api.sock")
	server, err := NewServer(ServerOptions{Authority: authority, Owner: p063Owner(t), SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	go func() { _ = server.Serve() }()
	mailboxRoot := filepath.Join(root.Path(), "mailbox")
	importer, err := mailbox.NewImporter(mailboxRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := mailbox.NewOutbox(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := mailbox.NewEventFiles(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: importer, Authority: authority, Controller: p063Owner(t), Operations: server,
		Outbox: outbox, EventFiles: eventFiles, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &p095Harness{server: server, authority: authority, importer: importer, outbox: outbox, eventFiles: eventFiles, processor: processor}, &clock
}

func writeP096RawRequest(t *testing.T, importer *mailbox.Importer, requestID string, raw []byte) {
	t.Helper()
	for _, file := range []struct {
		path string
		data []byte
	}{
		{path: filepath.Join(importer.InboxPath(), requestID+mailbox.RequestSuffix), data: raw},
		{path: filepath.Join(importer.InboxPath(), requestID+mailbox.ReadySuffix)},
	} {
		output, err := os.OpenFile(file.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mailbox.MailboxFileMode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write(file.data); err != nil {
			_ = output.Close()
			t.Fatal(err)
		}
		if err := output.Sync(); err != nil {
			_ = output.Close()
			t.Fatal(err)
		}
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
