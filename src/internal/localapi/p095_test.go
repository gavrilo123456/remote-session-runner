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
)

func TestP095MailboxSubmitSeparatesShellExitFromUncertainDelivery(t *testing.T) {
	ctx := context.Background()
	h := newP095Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)

	writeP094Request(t, h.importer, "req-p095-shell-exit", map[string]any{
		"request_id": "req-p095-shell-exit", "idempotency_key": "key-p095-shell-exit", "operation": "submit_command",
		"session_id": sessionID, "script": "printf 'shell failed\\n'; exit 7", "timeout_seconds": 30,
	})
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("local submit import results=%+v err=%v", results, err)
	}
	accepted := readP095Response(t, h.outbox, "req-p095-shell-exit")
	if accepted.RequestState != "accepted" || accepted.CommandID == "" || accepted.SessionID != sessionID || accepted.DeliveryState != string(store.LocalIntentRecorded) || accepted.CommandState != "" || accepted.ExitCode != nil {
		t.Fatalf("submit acknowledgement invented target state: %+v", accepted)
	}

	intent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentDispatching, "p095-test-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "p095-test-accepted"); err != nil {
		t.Fatal(err)
	}
	ordinal := int64(1)
	command, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, RequestHash: intent.RequestHash,
		IdempotencyKey: intent.IdempotencyKey, Script: string(intent.ScriptBytes), Timeout: 30 * time.Second, IntentOrdinal: ordinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	output := []byte("shell failed\n")
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: output, ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 7
	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateFailed, ExitCode: &exitCode, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	completed := readP095Response(t, h.outbox, "req-p095-shell-exit")
	if completed.RequestState != "complete" || completed.CommandState != string(domain.CommandStateFailed) || completed.ExitCode == nil || *completed.ExitCode != 7 || completed.Error != nil {
		t.Fatalf("non-zero shell exit was not reported as a completed failed command: %+v", completed)
	}
	if completed.FinalEventSequence == nil || completed.AvailableEventSequence == nil || *completed.FinalEventSequence != 4 || *completed.AvailableEventSequence != 4 || completed.OutputComplete == nil || !*completed.OutputComplete || completed.EventsFile != "events/"+accepted.CommandID+".ndjson" {
		t.Fatalf("terminal command output metadata = %+v", completed)
	}
	eventBytes, cursor, err := h.eventFiles.Read(domain.CommandID(accepted.CommandID))
	if err != nil || cursor != 4 || len(eventBytes) == 0 {
		t.Fatalf("terminal event file cursor=%d bytes=%d err=%v", cursor, len(eventBytes), err)
	}

	remoteSessionID := h.createSession(t, domain.TargetKindRemote, "linux-host", "linux-dev", false)
	writeP094Request(t, h.importer, "req-p095-uncertain-submit", map[string]any{
		"request_id": "req-p095-uncertain-submit", "idempotency_key": "key-p095-uncertain-submit", "operation": "submit_command",
		"session_id": remoteSessionID, "script": "echo remote", "timeout_seconds": 30,
	})
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("remote submit import results=%+v err=%v", results, err)
	}
	remoteAccepted := readP095Response(t, h.outbox, "req-p095-uncertain-submit")
	remoteIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", remoteAccepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	remoteIntent, err = h.authority.TransitionLocalIntent(ctx, remoteIntent.IntentID, store.LocalIntentDispatching, "p095-test-send")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, remoteIntent.IntentID, store.LocalIntentUncertain, "p095-test-response-lost"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	uncertain := readP095Response(t, h.outbox, "req-p095-uncertain-submit")
	if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.CommandState != "" || uncertain.ExitCode != nil || uncertain.Error != nil || uncertain.AvailableEventSequence != nil {
		t.Fatalf("uncertain transport was converted into command failure or rejection: %+v", uncertain)
	}
	writeP094Request(t, h.importer, "req-p095-uncertain-get", map[string]any{
		"request_id": "req-p095-uncertain-get", "operation": "get_command", "command_id": remoteAccepted.CommandID,
	})
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("uncertain get_command import results=%+v err=%v", results, err)
	}
	uncertainRead := readP095Response(t, h.outbox, "req-p095-uncertain-get")
	if uncertainRead.RequestState != "complete" || uncertainRead.DeliveryState != string(store.LocalIntentUncertain) || uncertainRead.CommandState != "" || uncertainRead.Error != nil || uncertainRead.AvailableEventSequence != nil {
		t.Fatalf("get_command invented remote state for uncertain delivery: %+v", uncertainRead)
	}

	writeP094Request(t, h.importer, "req-p095-not-delivered-submit", map[string]any{
		"request_id": "req-p095-not-delivered-submit", "idempotency_key": "key-p095-not-delivered-submit", "operation": "submit_command",
		"session_id": remoteSessionID, "script": "echo never sent", "timeout_seconds": 30,
	})
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("not-delivered submit import results=%+v err=%v", results, err)
	}
	notDeliveredAccepted := readP095Response(t, h.outbox, "req-p095-not-delivered-submit")
	notDeliveredIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", notDeliveredAccepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	notDeliveredIntent, err = h.authority.TransitionLocalIntent(ctx, notDeliveredIntent.IntentID, store.LocalIntentDispatching, "p095-test-not-delivered-send")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, notDeliveredIntent.IntentID, store.LocalIntentNotDelivered, "p095-test-proven-not-delivered"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	rejected := readP095Response(t, h.outbox, "req-p095-not-delivered-submit")
	if rejected.RequestState != "rejected" || rejected.DeliveryState != string(store.LocalIntentNotDelivered) || rejected.CommandState != "" || rejected.Error == nil {
		t.Fatalf("proven non-delivery response = %+v", rejected)
	}
	writeP094Request(t, h.importer, "req-p095-not-delivered-get", map[string]any{
		"request_id": "req-p095-not-delivered-get", "operation": "get_command", "command_id": notDeliveredAccepted.CommandID,
	})
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("not-delivered get_command import results=%+v err=%v", results, err)
	}
	notDeliveredRead := readP095Response(t, h.outbox, "req-p095-not-delivered-get")
	if notDeliveredRead.RequestState != "complete" || notDeliveredRead.DeliveryState != string(store.LocalIntentNotDelivered) || notDeliveredRead.CommandState != "" || notDeliveredRead.AvailableEventSequence != nil || notDeliveredRead.EventsFile != "" {
		t.Fatalf("get_command fabricated target state after proven non-delivery: %+v", notDeliveredRead)
	}
}

func TestP095GetCommandFreezesResponseAndPreservesLaterEventFile(t *testing.T) {
	ctx := context.Background()
	h := newP095Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
	commandID := h.submitQueuedCommand(t, "req-p095-active-submit", "key-p095-active-submit", sessionID)

	request := map[string]any{"request_id": "req-p095-active-first", "operation": "get_command", "command_id": commandID}
	writeP094Request(t, h.importer, "req-p095-active-first", request)
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("first get_command import results=%+v err=%v", results, err)
	}
	firstBytes, err := h.outbox.Read("req-p095-active-first")
	if err != nil {
		t.Fatal(err)
	}
	first := readP095Response(t, h.outbox, "req-p095-active-first")
	if first.Operation != "get_command" || first.RequestState != "complete" || first.CommandState != string(domain.CommandStateQueued) || first.AvailableEventSequence == nil || *first.AvailableEventSequence != 1 || first.OutputComplete == nil || *first.OutputComplete || first.OutputUnavailableReason != "" || first.ObservedAt == "" {
		t.Fatalf("active as-of response = %+v", first)
	}
	firstEventBytes, firstCursor, err := h.eventFiles.Read(domain.CommandID(commandID))
	if err != nil || firstCursor != 1 || len(firstEventBytes) == 0 {
		t.Fatalf("first event snapshot cursor=%d bytes=%d err=%v", firstCursor, len(firstEventBytes), err)
	}

	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: domain.CommandID(commandID), NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	output := []byte("later output")
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: domain.CommandID(commandID), Type: "stdout", Payload: output, ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	writeP094Request(t, h.importer, "req-p095-active-later", map[string]any{
		"request_id": "req-p095-active-later", "operation": "get_command", "command_id": commandID,
	})
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("later get_command import results=%+v err=%v", results, err)
	}
	laterEventBytes, laterCursor, err := h.eventFiles.Read(domain.CommandID(commandID))
	if err != nil || laterCursor != 3 || len(laterEventBytes) <= len(firstEventBytes) {
		t.Fatalf("later event snapshot cursor=%d bytes=%d err=%v", laterCursor, len(laterEventBytes), err)
	}

	// Reimporting the original request replays its durable response and must not
	// shrink the shared event file or alter the frozen observed time/cursor.
	writeP094Request(t, h.importer, "req-p095-active-first", request)
	results, err = h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("duplicate get_command import results=%+v err=%v", results, err)
	}
	replayedBytes, err := h.outbox.Read("req-p095-active-first")
	if err != nil || string(replayedBytes) != string(firstBytes) {
		t.Fatalf("replayed response changed frozen bytes: err=%v", err)
	}
	finalEventBytes, finalCursor, err := h.eventFiles.Read(domain.CommandID(commandID))
	if err != nil || finalCursor != laterCursor || string(finalEventBytes) != string(laterEventBytes) {
		t.Fatalf("old snapshot replay changed shared event file: cursor=%d err=%v", finalCursor, err)
	}
}

func TestP095GetCommandDeniesDirectCreatedAuthorityCommand(t *testing.T) {
	ctx := context.Background()
	h := newP095Harness(t)
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "p095-direct-controller")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("sess-p095-direct")
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: sessionID, Target: target, Environment: "linux-dev", Controller: controller,
		Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, sessionID, domain.SessionStateReady, "p095-direct-ready"); err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", []byte(`{"operation":"submit_command","session_id":"sess-p095-direct","script":"echo direct"}`), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	command, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: "command-p095-direct", SessionID: sessionID, IdempotencyKey: "key-p095-direct-command",
		RequestHash: hash, Script: "echo direct", Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeP094Request(t, h.importer, "req-p095-direct-get", map[string]any{
		"request_id": "req-p095-direct-get", "operation": "get_command", "command_id": string(command.CommandID),
	})
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("direct command read import results=%+v err=%v", results, err)
	}
	denied := readP095Response(t, h.outbox, "req-p095-direct-get")
	if denied.RequestState != "rejected" || denied.Error == nil || denied.Error.Code != "resource_not_found" || denied.CommandState != "" || denied.AvailableEventSequence != nil {
		t.Fatalf("direct-created command was exposed through Mac ingress: %+v", denied)
	}
}

type p095Harness struct {
	server     *Server
	authority  *store.AuthorityStore
	importer   *mailbox.Importer
	outbox     *mailbox.Outbox
	eventFiles *mailbox.EventFiles
	processor  *mailbox.SessionProcessor
}

func newP095Harness(t *testing.T) *p095Harness {
	t.Helper()
	server, authority, _, _ := p063Server(t)
	root := filepath.Join(t.TempDir(), "mailbox")
	importer, err := mailbox.NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := mailbox.NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := mailbox.NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: importer, Authority: authority, Controller: p063Owner(t), Operations: server,
		Outbox: outbox, EventFiles: eventFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &p095Harness{server: server, authority: authority, importer: importer, outbox: outbox, eventFiles: eventFiles, processor: processor}
}

func (h *p095Harness) createSession(t *testing.T, kind domain.TargetKind, profile, environment string, ready bool) string {
	t.Helper()
	requestID := "req-p095-setup-" + profile
	key := "key-p095-setup-" + profile
	raw, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "create_session", "environment": environment,
		"execution_target": map[string]string{"kind": string(kind), "profile": profile}, "source": map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := h.server.CreateSessionIntent(context.Background(), mailbox.Request{
		RequestID: requestID, IdempotencyKey: key, Operation: "create_session", RawJSON: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		return intent.SessionID
	}
	createIntent, err := h.authority.GetLocalIntentByResource(context.Background(), "create_session", intent.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err = h.authority.TransitionLocalIntent(context.Background(), createIntent.IntentID, store.LocalIntentDispatching, "p095-test-create-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(context.Background(), createIntent.IntentID, store.LocalIntentAccepted, "p095-test-create-accepted"); err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CreateSession(context.Background(), store.SessionCreate{
		SessionID: domain.SessionID(intent.SessionID), Target: target, Environment: environment,
		Controller: p063Owner(t), Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(context.Background(), domain.SessionID(intent.SessionID), domain.SessionStateReady, "p095-test-ready"); err != nil {
		t.Fatal(err)
	}
	return intent.SessionID
}

func (h *p095Harness) submitQueuedCommand(t *testing.T, requestID, key, sessionID string) string {
	t.Helper()
	writeP094Request(t, h.importer, requestID, map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "submit_command",
		"session_id": sessionID, "script": "echo queued", "timeout_seconds": 30,
	})
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("submit import results=%+v err=%v", results, err)
	}
	accepted := readP095Response(t, h.outbox, requestID)
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentDispatching, "p095-test-command-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentAccepted, "p095-test-command-accepted"); err != nil {
		t.Fatal(err)
	}
	ordinal := int64(1)
	if _, _, err := h.authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, RequestHash: intent.RequestHash,
		IdempotencyKey: intent.IdempotencyKey, Script: string(intent.ScriptBytes), Timeout: 30 * time.Second, IntentOrdinal: ordinal,
	}); err != nil {
		t.Fatal(err)
	}
	return accepted.CommandID
}

type p095Response struct {
	RequestID               string `json:"request_id"`
	Operation               string `json:"operation"`
	RequestState            string `json:"request_state"`
	ResponseRevision        int64  `json:"response_revision"`
	IdempotencyWarning      string `json:"idempotency_warning"`
	CommandID               string `json:"command_id"`
	SessionID               string `json:"session_id"`
	SessionState            string `json:"session_state"`
	DeliveryState           string `json:"delivery_state"`
	CommandState            string `json:"command_state"`
	ObservedAt              string `json:"observed_at"`
	ExitCode                *int   `json:"exit_code"`
	Stdout                  string `json:"stdout"`
	Stderr                  string `json:"stderr"`
	FinalEventSequence      *int64 `json:"final_event_sequence"`
	AvailableEventSequence  *int64 `json:"available_event_sequence"`
	OutputComplete          *bool  `json:"output_complete"`
	OutputTruncated         *bool  `json:"output_truncated"`
	OutputUnavailableReason string `json:"output_unavailable_reason"`
	EventsFile              string `json:"events_file"`
	TeardownOutcome         string `json:"teardown_outcome"`
	Error                   *struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func readP095Response(t *testing.T, outbox *mailbox.Outbox, requestID string) p095Response {
	t.Helper()
	data, err := outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var response p095Response
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestP095DirectCommandRequestDoesNotRequireTargetStateForQueuedRemote(t *testing.T) {
	h := newP095Harness(t)
	sessionID := h.createSession(t, domain.TargetKindRemote, "linux-host", "linux-dev", false)
	writeP094Request(t, h.importer, "req-p095-queued-remote", map[string]any{
		"request_id": "req-p095-queued-remote", "idempotency_key": "key-p095-queued-remote", "operation": "submit_command",
		"session_id": sessionID, "script": "echo queued remotely", "timeout_seconds": 30,
	})
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("queued remote submit import results=%+v err=%v", results, err)
	}
	accepted := readP095Response(t, h.outbox, "req-p095-queued-remote")
	if accepted.RequestState != "accepted" || accepted.DeliveryState != string(store.LocalIntentRecorded) || accepted.CommandState != "" || accepted.ExitCode != nil {
		t.Fatalf("queued remote submit invented target state: %+v", accepted)
	}
}

func TestP095OutputEventFileIsOwnerOnly(t *testing.T) {
	h := newP095Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
	commandID := h.submitQueuedCommand(t, "req-p095-event-mode-submit", "key-p095-event-mode", sessionID)
	writeP094Request(t, h.importer, "req-p095-event-mode-get", map[string]any{"request_id": "req-p095-event-mode-get", "operation": "get_command", "command_id": commandID})
	if _, err := h.processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	path, err := h.eventFiles.Path(domain.CommandID(commandID))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mailbox.MailboxFileMode.Perm() {
		t.Fatalf("event file mode=%#o want %#o", info.Mode().Perm(), mailbox.MailboxFileMode.Perm())
	}
	if filepath.Base(path) == "" {
		t.Fatal("event file path has no basename")
	}
}
