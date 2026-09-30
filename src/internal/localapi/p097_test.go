package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP097M04TerminalMailboxRevisionAndBytesStayFrozen(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID := "req-p097-create-revision"
	createRequest := map[string]any{
		"request_id": requestID, "idempotency_key": "key-p097-create-revision", "operation": "create_session",
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source": map[string]string{"mode": "empty"},
	}
	p097Import(t, h, requestID, createRequest)
	first := readP095Response(t, h.outbox, requestID)
	if first.RequestState != "accepted" || first.ResponseRevision != 1 || first.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("first create revision=%+v", first)
	}
	sessionID := first.SessionID
	intent, err := h.authority.GetLocalIntentByResource(ctx, "create_session", sessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentDispatching, "p097-create-dispatching")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	dispatching := readP095Response(t, h.outbox, requestID)
	if dispatching.RequestState != "accepted" || dispatching.ResponseRevision != 2 || dispatching.DeliveryState != string(store.LocalIntentDispatching) {
		t.Fatalf("dispatching create revision=%+v", dispatching)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "p097-create-accepted"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	accepted := readP095Response(t, h.outbox, requestID)
	if accepted.RequestState != "accepted" || accepted.ResponseRevision != 3 || accepted.DeliveryState != string(store.LocalIntentAccepted) {
		t.Fatalf("accepted create revision=%+v", accepted)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: domain.SessionID(sessionID), Target: target, Environment: "mac-dev", Controller: p063Owner(t),
		Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, domain.SessionID(sessionID), domain.SessionStateReady, "p097-ready"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := readP095Response(t, h.outbox, requestID)
	terminalBytes, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.RequestState != "complete" || terminal.ResponseRevision != 4 || terminal.SessionState != string(domain.SessionStateReady) {
		t.Fatalf("terminal create revision=%+v", terminal)
	}
	receipt, err := h.authority.GetMailboxExchange(ctx, requestID)
	if err != nil || receipt.State != store.MailboxExchangeComplete || receipt.ResponseRevision != terminal.ResponseRevision || string(receipt.TerminalResponseBytes) != string(terminalBytes) {
		t.Fatalf("stored terminal receipt state=%s revision=%d bytes_match=%v err=%v", receipt.State, receipt.ResponseRevision, string(receipt.TerminalResponseBytes) == string(terminalBytes), err)
	}
	if _, err := h.authority.TransitionSession(ctx, domain.SessionID(sessionID), domain.SessionStateClosing, "p097-close-started"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, domain.SessionID(sessionID), domain.SessionStateClosed, "p097-close-complete"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	p097Import(t, h, requestID, createRequest)
	unchangedBytes, err := h.outbox.Read(requestID)
	if err != nil || string(unchangedBytes) != string(terminalBytes) {
		t.Fatalf("reused terminal request changed bytes: equal=%v err=%v", string(unchangedBytes) == string(terminalBytes), err)
	}
	unchanged, err := h.authority.GetMailboxExchange(ctx, requestID)
	if err != nil || unchanged.ResponseRevision != terminal.ResponseRevision || string(unchanged.TerminalResponseBytes) != string(terminalBytes) {
		t.Fatalf("terminal receipt mutated: revision=%d bytes_equal=%v err=%v", unchanged.ResponseRevision, string(unchanged.TerminalResponseBytes) == string(terminalBytes), err)
	}

	getID := "req-p097-get-closed-session"
	p097Import(t, h, getID, map[string]any{"request_id": getID, "operation": "get_session", "session_id": sessionID})
	changed := readP095Response(t, h.outbox, getID)
	if changed.RequestState != "complete" || changed.SessionID != sessionID || changed.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("new status request did not observe changed state: %+v", changed)
	}
}

func TestP097M09MailboxResponseAndEventFileMatrix(t *testing.T) {
	t.Run("tiny fully inline", func(t *testing.T) {
		h, _, commandID := p097StartLocalCommand(t)
		p097Append(t, h, commandID, "stdout", []byte("tiny 🍎\n"))
		p097FinishLocalCommand(t, h, commandID, false)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		response := p097GetCommand(t, h, "req-p097-tiny-get", commandID)
		p097AssertCommandResponse(t, h, response, commandID, p097Want{
			state: string(domain.CommandStateSucceeded), cursor: 4, final: 4, complete: true, fullAnswer: true,
			stdout: "tiny 🍎\n", fileCursor: 4,
		})
	})

	t.Run("normal complete stdout and stderr", func(t *testing.T) {
		h, _, commandID := p097StartLocalCommand(t)
		p097Append(t, h, commandID, "stdout", []byte("normal output\n"))
		p097Append(t, h, commandID, "stderr", []byte("diagnostic\n"))
		p097FinishLocalCommand(t, h, commandID, false)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		response := p097GetCommand(t, h, "req-p097-normal-get", commandID)
		p097AssertCommandResponse(t, h, response, commandID, p097Want{
			state: string(domain.CommandStateSucceeded), cursor: 5, final: 5, complete: true, fullAnswer: true,
			stdout: "normal output\n", stderr: "diagnostic\n", fileCursor: 5,
		})
	})

	t.Run("active cursor stays frozen", func(t *testing.T) {
		h, _, commandID := p097StartLocalCommand(t)
		p097Append(t, h, commandID, "stdout", []byte("active prefix"))
		firstID := "req-p097-active-get-first"
		first := p097GetCommand(t, h, firstID, commandID)
		firstBytes, err := h.outbox.Read(firstID)
		if err != nil {
			t.Fatal(err)
		}
		p097AssertCommandResponse(t, h, first, commandID, p097Want{
			state: string(domain.CommandStateRunning), cursor: 3, complete: false, fullAnswer: false,
			stdout: "active prefix", fileCursor: 3,
		})
		p097Append(t, h, commandID, "stdout", []byte(" later"))
		p097FinishLocalCommand(t, h, commandID, false)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		second := p097GetCommand(t, h, "req-p097-active-get-second", commandID)
		p097AssertCommandResponse(t, h, second, commandID, p097Want{
			state: string(domain.CommandStateSucceeded), cursor: 5, final: 5, complete: true, fullAnswer: true,
			stdout: "active prefix later", fileCursor: 5,
		})
		unchangedBytes, err := h.outbox.Read(firstID)
		if err != nil || string(unchangedBytes) != string(firstBytes) {
			t.Fatalf("later status changed frozen active response: equal=%v err=%v", string(unchangedBytes) == string(firstBytes), err)
		}
		stored, err := h.authority.GetMailboxExchange(context.Background(), firstID)
		if err != nil || stored.ResponseRevision != first.ResponseRevision || stored.AvailableEventSequence == nil || *stored.AvailableEventSequence != 3 {
			t.Fatalf("frozen active receipt=%+v err=%v", stored, err)
		}
		if eventBytes, fileCursor, err := h.eventFiles.Read(domain.CommandID(commandID)); err != nil || fileCursor != 5 || !p097HasContiguousPrefix(eventBytes, 3) {
			t.Fatalf("expanded event file cursor=%d preserves first prefix=%v err=%v", fileCursor, p097HasContiguousPrefix(eventBytes, 3), err)
		}
	})

	t.Run("output truncated", func(t *testing.T) {
		h, _, commandID := p097StartLocalCommand(t)
		large := strings.Repeat("x", 5000)
		p097Append(t, h, commandID, "stdout", []byte(large))
		p097Append(t, h, commandID, "output_truncated", nil)
		p097FinishLocalCommand(t, h, commandID, true)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		response := p097GetCommand(t, h, "req-p097-truncated-get", commandID)
		p097AssertCommandResponse(t, h, response, commandID, p097Want{
			state: string(domain.CommandStateSucceeded), cursor: 5, final: 5, complete: true, truncated: true,
			fullAnswer: false, stdout: strings.Repeat("x", 4096), fileCursor: 5,
		})
	})

	t.Run("capture boundary lost", func(t *testing.T) {
		h, _, commandID := p097RemoteTerminal(t, domain.CommandStateLost, "capture_boundary_unconfirmed")
		response := p097GetCommand(t, h, "req-p097-capture-lost-get", commandID)
		p097AssertCommandResponse(t, h, response, commandID, p097Want{
			state: string(domain.CommandStateLost), cursor: 4, final: 4, complete: false, truncated: false,
			fullAnswer: false, reason: "capture_boundary_unconfirmed", stdout: "captured prefix", fileCursor: 4,
			lastEventType: "command_lost",
		})
	})

	t.Run("irrecoverable remote gap", func(t *testing.T) {
		h, _, commandID := p097RemoteTerminal(t, domain.CommandStateSucceeded, "remote_event_gap")
		response := p097GetCommand(t, h, "req-p097-gap-get", commandID)
		p097AssertCommandResponse(t, h, response, commandID, p097Want{
			state: string(domain.CommandStateSucceeded), cursor: 3, final: 4, complete: false, truncated: false,
			fullAnswer: false, reason: "remote_event_gap", stdout: "available prefix", fileCursor: 3,
		})
	})

	t.Run("retention expired", func(t *testing.T) {
		h, now, commandID := p097StartLocalCommand(t)
		p097Append(t, h, commandID, "stdout", []byte("expired output"))
		p097FinishLocalCommand(t, h, commandID, false)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(store.DefaultOutputRetention + time.Minute)
		gc, err := h.authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
		if err != nil || gc.CommandsOutputExpired != 1 {
			t.Fatalf("output expiry GC=%+v err=%v", gc, err)
		}
		response := p097GetCommand(t, h, "req-p097-expired-get", commandID)
		p097AssertCommandResponse(t, h, response, commandID, p097Want{
			state: string(domain.CommandStateSucceeded), cursor: 0, final: 4, complete: false, truncated: false,
			fullAnswer: false, reason: "retention_expired", fileCursor: 0,
		})
	})
}

type p097Want struct {
	state         string
	cursor        int64
	final         int64
	complete      bool
	truncated     bool
	fullAnswer    bool
	reason        string
	stdout        string
	stderr        string
	fileCursor    int64
	lastEventType string
}

func p097Import(t *testing.T, h *p095Harness, requestID string, request map[string]any) {
	t.Helper()
	writeP094Request(t, h.importer, requestID, request)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("mailbox import %s results=%+v err=%v", requestID, results, err)
	}
}

func p097StartLocalCommand(t *testing.T) (*p095Harness, *time.Time, string) {
	t.Helper()
	h, now := newP096Harness(t)
	sessionID := h.createSession(t, domain.TargetKindLocal, "mac-workstation", "mac-dev", true)
	commandID := h.submitQueuedCommand(t, "req-p097-submit", "key-p097-submit", sessionID)
	if _, err := h.authority.TransitionCommand(context.Background(), store.CommandTransition{
		CommandID: domain.CommandID(commandID), NextState: domain.CommandStateRunning,
	}); err != nil {
		t.Fatal(err)
	}
	return h, now, commandID
}

func p097Append(t *testing.T, h *p095Harness, commandID, eventType string, payload []byte) {
	t.Helper()
	if _, err := h.authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
		CommandID: domain.CommandID(commandID), Type: eventType, Payload: payload, ByteCount: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}
}

func p097FinishLocalCommand(t *testing.T, h *p095Harness, commandID string, truncated bool) {
	t.Helper()
	exitCode := 0
	if _, err := h.authority.TransitionCommand(context.Background(), store.CommandTransition{
		CommandID: domain.CommandID(commandID), NextState: domain.CommandStateSucceeded,
		ExitCode: &exitCode, OutputComplete: true, OutputTruncated: truncated,
	}); err != nil {
		t.Fatal(err)
	}
}

func p097GetCommand(t *testing.T, h *p095Harness, requestID, commandID string) p095Response {
	t.Helper()
	p097Import(t, h, requestID, map[string]any{"request_id": requestID, "operation": "get_command", "command_id": commandID})
	return readP095Response(t, h.outbox, requestID)
}

func p097RemoteTerminal(t *testing.T, hState domain.CommandState, reason string) (*p095Harness, string, string) {
	return p097RemoteTerminalWithProof(t, hState, reason, true)
}

// p097RemoteTerminalWithProof prepares a remote terminal projection. A false
// proof models the legacy reconciled state written before P149; callers that
// need a terminal file response must construct the historical receipt
// explicitly because current mailbox reads intentionally keep it hidden.
func p097RemoteTerminalWithProof(t *testing.T, hState domain.CommandState, reason string, terminalProof bool) (*p095Harness, string, string) {
	t.Helper()
	h, _ := newP096Harness(t)
	sessionID := h.createSession(t, domain.TargetKindRemote, "linux-host", "linux-dev", false)
	requestID := "req-p097-remote-submit-" + reason
	p097Import(t, h, requestID, map[string]any{
		"request_id": requestID, "idempotency_key": "key-p097-remote-" + reason, "operation": "submit_command",
		"session_id": sessionID, "script": "echo remote", "timeout_seconds": 30,
	})
	accepted := readP095Response(t, h.outbox, requestID)
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentDispatching, "p097-remote-dispatching")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentAccepted, "p097-remote-accepted"); err != nil {
		t.Fatal(err)
	}
	ordinal := int64(1)
	if intent.IntentOrdinal != nil {
		ordinal = *intent.IntentOrdinal
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	final := int64(4)
	exitCode := 0
	projection := store.RemoteCommandProjection{
		CommandID: intent.CommandID, SessionID: intent.SessionID, Ordinal: ordinal, State: hState,
		FinalEventSequence: &final, OutputComplete: false, OutputUnavailableReason: reason,
		Target: target, Controller: intent.Controller, Environment: intent.Environment,
		Source: intent.Source, Capabilities: p076APICapabilities(), ObservedAt: when.Add(10 * time.Second),
	}
	if hState == domain.CommandStateSucceeded {
		projection.ExitCode = &exitCode
	}
	if _, err := h.authority.UpsertRemoteCommandProjection(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	outputText := "available prefix"
	events := []store.RemoteEventRecord{
		{CommandID: intent.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
		{CommandID: intent.CommandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
		{CommandID: intent.CommandID, Sequence: 3, Type: "stdout", Payload: []byte(outputText), ByteCount: int64(len(outputText)), OccurredAt: when.Add(2 * time.Second)},
	}
	if reason == "capture_boundary_unconfirmed" {
		outputText = "captured prefix"
		events[2].Payload = []byte(outputText)
		events[2].ByteCount = int64(len(outputText))
		events = append(events, store.RemoteEventRecord{CommandID: intent.CommandID, Sequence: 4, Type: "command_lost", OccurredAt: when.Add(3 * time.Second)})
	}
	if reason != "retention_expired" {
		if _, err := h.authority.MirrorRemoteEvents(context.Background(), events); err != nil {
			t.Fatal(err)
		}
	}
	if reason == "remote_event_gap" {
		if _, err := h.authority.RecordRemoteEventGap(context.Background(), store.RemoteEventGapRecord{
			CommandID: intent.CommandID, MissingFrom: 4, MissingTo: 4, AvailableSequence: 3, FinalSequence: final,
			TerminalState: hState, OutputComplete: false, OutputUnavailableReason: reason, ConfirmedAt: when.Add(5 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if terminalProof {
		// This fixture represents an already verified target terminal result. The
		// mailbox gate intentionally hides remote terminal projections until this
		// durable reconciliation boundary has been crossed.
		if _, err := h.authority.MarkRemoteIntentTerminalProof(context.Background(), intent.IntentID, "p097-remote-terminal-reconciled"); err != nil {
			t.Fatal(err)
		}
	} else if _, err := h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentReconciled, "p097-legacy-reconciled"); err != nil {
		t.Fatal(err)
	}
	return h, sessionID, string(intent.CommandID)
}

func p097AssertCommandResponse(t *testing.T, h *p095Harness, response p095Response, commandID string, want p097Want) {
	t.Helper()
	if response.Operation != "get_command" || response.RequestState != "complete" || response.CommandID != commandID || response.CommandState != want.state {
		t.Fatalf("command response identity/state=%+v", response)
	}
	if response.AvailableEventSequence == nil || *response.AvailableEventSequence != want.cursor || response.OutputComplete == nil || *response.OutputComplete != want.complete || response.OutputTruncated == nil || *response.OutputTruncated != want.truncated || response.OutputUnavailableReason != want.reason {
		t.Fatalf("command response completeness=%+v want cursor=%d complete=%v truncated=%v reason=%q", response, want.cursor, want.complete, want.truncated, want.reason)
	}
	if want.final == 0 {
		if response.FinalEventSequence != nil {
			t.Fatalf("active response has final cursor %d", *response.FinalEventSequence)
		}
	} else if response.FinalEventSequence == nil || *response.FinalEventSequence != want.final {
		t.Fatalf("final cursor=%v, want %d", response.FinalEventSequence, want.final)
	}
	if response.Stdout != want.stdout || response.Stderr != want.stderr || len(response.Stdout) > 4096 || len(response.Stderr) > 4096 {
		t.Fatalf("inline previews stdout=%q stderr=%q", response.Stdout, response.Stderr)
	}
	fullAnswer := response.RequestState == "complete" && response.OutputComplete != nil && *response.OutputComplete &&
		response.OutputTruncated != nil && !*response.OutputTruncated && response.AvailableEventSequence != nil &&
		*response.AvailableEventSequence > 0 && response.FinalEventSequence != nil && *response.AvailableEventSequence == *response.FinalEventSequence
	if fullAnswer != want.fullAnswer {
		t.Fatalf("full-answer triple=%v, want %v: %+v", fullAnswer, want.fullAnswer, response)
	}
	if want.cursor == 0 {
		if response.EventsFile != "" || want.fileCursor != 0 {
			t.Fatalf("zero-cursor response advertises output file %q", response.EventsFile)
		}
		return
	}
	if response.EventsFile != "events/"+commandID+".ndjson" {
		t.Fatalf("available cursor %d has event file %q", want.cursor, response.EventsFile)
	}
	data, fileCursor, err := h.eventFiles.Read(domain.CommandID(commandID))
	if err != nil || fileCursor != want.fileCursor || fileCursor < want.cursor || !p097HasContiguousPrefix(data, want.cursor) {
		t.Fatalf("event file cursor=%d data_prefix=%v err=%v, want file cursor %d through %d", fileCursor, p097HasContiguousPrefix(data, want.cursor), err, want.fileCursor, want.cursor)
	}
	if want.lastEventType != "" && p097LastEventType(data, want.cursor) != want.lastEventType {
		t.Fatalf("last advertised event=%q, want %q", p097LastEventType(data, want.cursor), want.lastEventType)
	}
	if want.reason == "capture_boundary_unconfirmed" && (want.complete || want.truncated || want.cursor != want.final) {
		t.Fatalf("contiguous command_lost boundary was treated as complete: %+v", response)
	}
	if want.reason == "remote_event_gap" && (want.complete || want.truncated || want.cursor >= want.final) {
		t.Fatalf("remote gap was mislabeled or filled: %+v", response)
	}
}

func p097HasContiguousPrefix(data []byte, through int64) bool {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var sequence int64
	for scanner.Scan() && sequence < through {
		var event struct {
			Sequence int64 `json:"sequence"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Sequence != sequence+1 {
			return false
		}
		sequence = event.Sequence
	}
	return sequence == through && scanner.Err() == nil
}

func p097LastEventType(data []byte, through int64) string {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var eventType string
	var sequence int64
	for scanner.Scan() && sequence < through {
		var event struct {
			Sequence int64  `json:"sequence"`
			Type     string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Sequence != sequence+1 {
			return ""
		}
		sequence = event.Sequence
		eventType = event.Type
	}
	return eventType
}
