package localapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p100Response struct {
	RequestID              string `json:"request_id"`
	Operation              string `json:"operation"`
	RequestState           string `json:"request_state"`
	ResponseRevision       int64  `json:"response_revision"`
	JobID                  string `json:"job_id"`
	JobPhase               string `json:"job_phase"`
	SessionID              string `json:"session_id"`
	CommandID              string `json:"command_id"`
	DeliveryState          string `json:"delivery_state"`
	CommandState           string `json:"command_state"`
	ExitCode               *int   `json:"exit_code"`
	Stdout                 string `json:"stdout"`
	FinalEventSequence     *int64 `json:"final_event_sequence"`
	AvailableEventSequence *int64 `json:"available_event_sequence"`
	OutputComplete         *bool  `json:"output_complete"`
	OutputTruncated        *bool  `json:"output_truncated"`
	EventsFile             string `json:"events_file"`
	TeardownOutcome        string `json:"teardown_outcome"`
	Error                  *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func readP100Response(t *testing.T, h *p095Harness, requestID string) p100Response {
	t.Helper()
	data, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var response p100Response
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func p100RunRequest(requestID, key, kind, profile, environment, script string) map[string]any {
	return map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "run",
		"environment":      environment,
		"execution_target": map[string]string{"kind": kind, "profile": profile},
		"source":           map[string]string{"mode": "empty"},
		"script":           script, "timeout_seconds": 30,
	}
}

func TestP100RunKeepsStableIDsForLocalAndQueuedRemoteIntents(t *testing.T) {
	for _, fixture := range []struct {
		name, kind, profile, environment string
	}{
		{name: "local", kind: "local", profile: "mac-workstation", environment: "mac-dev"},
		{name: "queued remote", kind: "remote", profile: "linux-host", environment: "linux-dev"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			h, _ := newP096Harness(t)
			requestID, key := "req-p100-"+fixture.kind, "key-p100-"+fixture.kind
			request := p100RunRequest(requestID, key, fixture.kind, fixture.profile, fixture.environment, "printf p100")
			writeP094Request(t, h.importer, requestID, request)
			results, err := h.processor.Import(ctx)
			if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("run import results=%+v err=%v", results, err)
			}
			first := readP100Response(t, h, requestID)
			if first.RequestID != requestID || first.Operation != "run" || first.RequestState != "accepted" || first.JobID == "" || first.SessionID == "" || first.CommandID == "" || first.DeliveryState != string(store.LocalIntentRecorded) || first.JobPhase != "" || first.CommandState != "" || first.TeardownOutcome != "" {
				t.Fatalf("initial run response invented target state: %+v", first)
			}
			intent, err := h.authority.GetLocalIntentByResource(ctx, "run", first.JobID, p063Owner(t))
			if err != nil || intent.JobID != domain.JobID(first.JobID) || string(intent.SessionID) != first.SessionID || string(intent.CommandID) != first.CommandID || intent.DeliveryState != store.LocalIntentRecorded {
				t.Fatalf("run intent=%+v err=%v", intent, err)
			}
			if _, err := h.authority.GetJob(ctx, intent.JobID); err == nil {
				t.Fatal("file run created authority job state before target acceptance")
			}

			retryID := requestID + "-retry"
			retry := p100RunRequest(retryID, key, fixture.kind, fixture.profile, fixture.environment, "printf p100")
			writeP094Request(t, h.importer, retryID, retry)
			results, err = h.processor.Import(ctx)
			if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("run retry results=%+v err=%v", results, err)
			}
			replayed := readP100Response(t, h, retryID)
			if replayed.RequestID != retryID || replayed.RequestState != "accepted" || replayed.JobID != first.JobID || replayed.SessionID != first.SessionID || replayed.CommandID != first.CommandID {
				t.Fatalf("same-key run retry changed stable IDs: first=%+v retry=%+v", first, replayed)
			}
			replayedIntent := pMailboxIntentForRequest(t, h.authority, "run", retryID)
			if replayedIntent.IntentID != intent.IntentID {
				t.Fatalf("same-key retry created another run intent: original=%+v replay=%+v", intent, replayedIntent)
			}
		})
	}
}

func TestP100RunPublishesCommandAndTeardownTogether(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID, key := "req-p100-terminal", "key-p100-terminal"
	writeP094Request(t, h.importer, requestID, p100RunRequest(requestID, key, "local", "mac-workstation", "mac-dev", "printf p100-output"))
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("run import results=%+v err=%v", results, err)
	}
	accepted := readP100Response(t, h, requestID)
	if accepted.RequestState != "accepted" || accepted.JobID == "" || accepted.JobPhase != "" || accepted.CommandState != "" || accepted.TeardownOutcome != "" {
		t.Fatalf("initial run response=%+v", accepted)
	}
	intent, err := h.authority.GetLocalIntentByResource(ctx, "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentDispatching, "p100-test-dispatching"); err != nil {
		t.Fatal(err)
	}
	intent, err = h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "p100-test-accepted")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authority.AcceptJob(ctx, store.JobAcceptance{
		JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID,
		Controller: intent.Controller, IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash,
		Environment: intent.Environment, Target: target, Source: intent.Source, Script: string(intent.ScriptBytes),
		CanonicalPayload: intent.PayloadJSON, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: intent.SessionID, Target: target, Environment: intent.Environment, Controller: intent.Controller,
		Source: intent.Source, Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateReady, "p100-test-ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseAcceptingCommand}); err != nil {
		t.Fatal(err)
	}
	commandPayload, err := json.Marshal(map[string]any{"session_id": string(intent.SessionID), "script": string(intent.ScriptBytes), "timeout_seconds": 30})
	if err != nil {
		t.Fatal(err)
	}
	canonicalCommand, err := domain.CanonicalizeMutationRequestJSON("submit_command", commandPayload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	commandHash, err := domain.HashMutationRequestJSON("submit_command", canonicalCommand, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, IdempotencyKey: key + "-command",
		RequestHash: commandHash, Script: string(intent.ScriptBytes), Timeout: 30 * time.Second, IntentOrdinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAcceptingCommand, NextPhase: store.JobPhaseAwaitingCommand}); err != nil {
		t.Fatal(err)
	}
	started, err := h.authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != intent.CommandID {
		t.Fatalf("start run command=%+v err=%v", started, err)
	}
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: intent.CommandID, Type: "stdout", Payload: []byte("p100-output"), ByteCount: int64(len("p100-output"))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	completed, err := h.authority.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID: intent.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: &exitCode, OutputComplete: true,
	}, domain.SessionStateReady, "p100-test-command-complete", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseAwaitingCommand, NextPhase: store.JobPhaseClosingSession, Command: &completed,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateClosing, "p100-test-closing"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateClosed, "p100-test-closed"); err != nil {
		t.Fatal(err)
	}
	closed := store.JobTeardownClosed
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseClosingSession, NextPhase: store.JobPhaseComplete,
		Command: &completed, TeardownState: &closed,
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	response := readP100Response(t, h, requestID)
	if response.RequestState != "complete" || response.JobPhase != string(store.JobPhaseComplete) || response.CommandState != string(domain.CommandStateSucceeded) || response.TeardownOutcome != "closed" || response.JobID != string(intent.JobID) || response.SessionID != string(intent.SessionID) || response.CommandID != string(intent.CommandID) || response.AvailableEventSequence == nil || response.FinalEventSequence == nil || *response.AvailableEventSequence != *response.FinalEventSequence || response.OutputComplete == nil || !*response.OutputComplete || response.ExitCode == nil || *response.ExitCode != 0 || response.Stdout != "p100-output" {
		t.Fatalf("terminal run response omitted command/teardown result: %+v", response)
	}
	eventBytes, eventCursor, err := h.eventFiles.Read(intent.CommandID)
	if err != nil || eventCursor != *response.AvailableEventSequence || string(eventBytes) == "" {
		t.Fatalf("terminal run events cursor=%d response=%v bytes=%d err=%v", eventCursor, response.AvailableEventSequence, len(eventBytes), err)
	}
	if _, err := h.authority.GetJob(ctx, intent.JobID); err != nil {
		t.Fatal(err)
	}
}

func TestP100ProvenNeverDeliveredRunRemainsIntentOutcome(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID, key := "req-p100-not-delivered", "key-p100-not-delivered"
	writeP094Request(t, h.importer, requestID, p100RunRequest(requestID, key, "local", "mac-workstation", "mac-dev", "printf never"))
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("run import results=%+v err=%v", results, err)
	}
	accepted := readP100Response(t, h, requestID)
	intent, err := h.authority.GetLocalIntentByResource(ctx, "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentNotDelivered, "p100-test-proven-not-delivered"); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	response := readP100Response(t, h, requestID)
	if response.RequestState != "rejected" || response.DeliveryState != string(store.LocalIntentNotDelivered) || response.JobID != string(intent.JobID) || response.SessionID != string(intent.SessionID) || response.CommandID != string(intent.CommandID) || response.JobPhase != "" || response.CommandState != "" || response.TeardownOutcome != "" || response.FinalEventSequence != nil || response.AvailableEventSequence != nil || response.Error == nil {
		t.Fatalf("proven non-delivery fabricated authority outcome: %+v", response)
	}
	if _, err := h.authority.GetJob(ctx, intent.JobID); err == nil {
		t.Fatal("proven never-delivered run created authority job state")
	}
}
