package localapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p101Target struct {
	name, kind, profile, environment string
}

func p101Targets() []p101Target {
	return []p101Target{
		{name: "local", kind: string(domain.TargetKindLocal), profile: "mac-workstation", environment: "mac-dev"},
		{name: "queued-remote", kind: string(domain.TargetKindRemote), profile: "linux-host", environment: "linux-dev"},
	}
}

type p101Response struct {
	RequestID               string `json:"request_id"`
	Operation               string `json:"operation"`
	RequestState            string `json:"request_state"`
	ResponseRevision        int64  `json:"response_revision"`
	SessionID               string `json:"session_id"`
	SessionState            string `json:"session_state"`
	CommandID               string `json:"command_id"`
	CommandState            string `json:"command_state"`
	JobID                   string `json:"job_id"`
	JobPhase                string `json:"job_phase"`
	DeliveryState           string `json:"delivery_state"`
	ExitCode                *int   `json:"exit_code"`
	Stdout                  string `json:"stdout"`
	FinalEventSequence      *int64 `json:"final_event_sequence"`
	AvailableEventSequence  *int64 `json:"available_event_sequence"`
	OutputComplete          *bool  `json:"output_complete"`
	OutputTruncated         *bool  `json:"output_truncated"`
	EventsFile              string `json:"events_file"`
	TeardownOutcome         string `json:"teardown_outcome"`
	OutputUnavailableReason string `json:"output_unavailable_reason"`
	Error                   *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func TestP101M10SevenOperationMatrixLocalAndQueuedRemote(t *testing.T) {
	for _, target := range p101Targets() {
		t.Run(target.name, func(t *testing.T) {
			h, _ := newP096Harness(t)
			ctx := context.Background()

			sessionID := p101CreateSession(t, h, target, "matrix")
			p101Import(t, h, "req-p101-"+target.name+"-get-session", map[string]any{
				"request_id": "req-p101-" + target.name + "-get-session", "operation": "get_session", "session_id": sessionID,
			})
			gotSession := p101ReadResponse(t, h, "req-p101-"+target.name+"-get-session")
			if gotSession.RequestState != "complete" || gotSession.SessionID != sessionID || gotSession.SessionState != string(domain.SessionStateReady) {
				t.Fatalf("get_session response=%+v", gotSession)
			}

			commandID := p101Submit(t, h, target, sessionID, "terminal", true)
			p101Import(t, h, "req-p101-"+target.name+"-get-command", map[string]any{
				"request_id": "req-p101-" + target.name + "-get-command", "operation": "get_command", "command_id": commandID,
			})
			gotCommand := p101ReadResponse(t, h, "req-p101-"+target.name+"-get-command")
			if gotCommand.RequestState != "complete" || gotCommand.CommandID != commandID || gotCommand.SessionID != sessionID ||
				gotCommand.CommandState != string(domain.CommandStateSucceeded) || gotCommand.OutputComplete == nil || !*gotCommand.OutputComplete ||
				gotCommand.AvailableEventSequence == nil || gotCommand.EventsFile != "events/"+commandID+".ndjson" {
				t.Fatalf("get_command response=%+v", gotCommand)
			}

			cancelCommandID := p101Submit(t, h, target, sessionID, "cancel", false)
			cancelRequestID := "req-p101-" + target.name + "-cancel"
			p101Import(t, h, cancelRequestID, map[string]any{
				"request_id": cancelRequestID, "idempotency_key": "key-p101-" + target.name + "-cancel",
				"operation": "cancel_command", "command_id": cancelCommandID,
			})
			cancelIntent, err := h.authority.GetLocalIntentByIdempotency(ctx, "cancel_command", "key-p101-"+target.name+"-cancel", p063Owner(t))
			if err != nil {
				t.Fatal(err)
			}
			if target.kind == string(domain.TargetKindRemote) {
				p101SetIntentDelivery(t, h, cancelIntent, store.LocalIntentUncertain)
				if err := h.processor.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
				uncertain := p101ReadResponse(t, h, cancelRequestID)
				if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.CommandState != "" || uncertain.Error != nil {
					t.Fatalf("uncertain cancel response=%+v", uncertain)
				}
			}
			p101SetIntentDelivery(t, h, cancelIntent, store.LocalIntentAccepted)
			if target.kind == string(domain.TargetKindLocal) {
				if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: domain.CommandID(cancelCommandID), NextState: domain.CommandStateCancelled, OutputComplete: true}); err != nil {
					t.Fatal(err)
				}
			} else {
				submitIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", cancelCommandID, p063Owner(t))
				if err != nil {
					t.Fatal(err)
				}
				p101ProjectRemoteCommand(t, h, cancelCommandID, sessionID, submitIntent, domain.CommandStateCancelled, "", 2, time.Date(2026, 9, 27, 12, 2, 0, 0, time.UTC))
			}
			if err := h.processor.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			cancelled := p101ReadResponse(t, h, cancelRequestID)
			if cancelled.RequestState != "complete" || cancelled.CommandID != cancelCommandID || cancelled.SessionID != sessionID ||
				cancelled.DeliveryState != string(store.LocalIntentAccepted) || cancelled.CommandState != "" || cancelled.Error != nil {
				t.Fatalf("cancel terminal boundary response=%+v", cancelled)
			}

			closeRequestID := "req-p101-" + target.name + "-close"
			p101Import(t, h, closeRequestID, map[string]any{
				"request_id": closeRequestID, "idempotency_key": "key-p101-" + target.name + "-close",
				"operation": "close_session", "session_id": sessionID,
			})
			closeIntent, err := h.authority.GetLocalIntentByIdempotency(ctx, "close_session", "key-p101-"+target.name+"-close", p063Owner(t))
			if err != nil {
				t.Fatal(err)
			}
			if target.kind == string(domain.TargetKindRemote) {
				p101SetIntentDelivery(t, h, closeIntent, store.LocalIntentUncertain)
				if err := h.processor.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
				uncertain := p101ReadResponse(t, h, closeRequestID)
				if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.SessionState != "" || uncertain.Error != nil {
					t.Fatalf("uncertain close response=%+v", uncertain)
				}
			}
			p101SetIntentDelivery(t, h, closeIntent, store.LocalIntentAccepted)
			p101CloseTarget(t, h, target, sessionID)
			if err := h.processor.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			closed := p101ReadResponse(t, h, closeRequestID)
			if closed.RequestState != "complete" || closed.SessionID != sessionID || closed.SessionState != string(domain.SessionStateClosed) || closed.TeardownOutcome != "closed" {
				t.Fatalf("close terminal boundary response=%+v", closed)
			}

			p101RunAndComplete(t, h, target, "matrix-run")
		})
	}
}

func TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary(t *testing.T) {
	t.Run("cancel before queued remote delivery", func(t *testing.T) {
		h, _ := newP096Harness(t)
		sessionID := h.createSession(t, domain.TargetKindRemote, "linux-host", "linux-dev", false)
		submitID := "req-p101-cancel-undelivered-submit"
		p101Import(t, h, submitID, map[string]any{
			"request_id": submitID, "idempotency_key": "key-p101-cancel-undelivered-submit", "operation": "submit_command",
			"session_id": sessionID, "script": "printf never-delivered", "timeout_seconds": 30,
		})
		submitted := p101ReadResponse(t, h, submitID)
		cancelID := "req-p101-cancel-undelivered-cancel"
		p101Import(t, h, cancelID, map[string]any{
			"request_id": cancelID, "idempotency_key": "key-p101-cancel-undelivered-cancel", "operation": "cancel_command", "command_id": submitted.CommandID,
		})
		submitIntent, err := h.authority.GetLocalIntentByResource(context.Background(), "submit_command", submitted.CommandID, p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		cancelIntent, err := h.authority.GetLocalIntentByIdempotency(context.Background(), "cancel_command", "key-p101-cancel-undelivered-cancel", p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		p101SetIntentDelivery(t, h, submitIntent, store.LocalIntentNotDelivered)
		p101SetIntentDelivery(t, h, cancelIntent, store.LocalIntentNotDelivered)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		submitResult, cancelResult := p101ReadResponse(t, h, submitID), p101ReadResponse(t, h, cancelID)
		if submitResult.RequestState != "rejected" || submitResult.DeliveryState != string(store.LocalIntentNotDelivered) || submitResult.CommandState != "" || submitResult.AvailableEventSequence != nil {
			t.Fatalf("never-delivered submit response=%+v", submitResult)
		}
		if cancelResult.RequestState != "complete" || cancelResult.DeliveryState != string(store.LocalIntentNotDelivered) || cancelResult.CommandState != "" || cancelResult.Error != nil {
			t.Fatalf("cancel before delivery response=%+v", cancelResult)
		}
	})

	t.Run("close before queued remote creation delivery", func(t *testing.T) {
		h, _ := newP096Harness(t)
		createID := "req-p101-close-undelivered-create"
		p101Import(t, h, createID, p101CreateRequest(createID, "key-p101-close-undelivered-create", p101Targets()[1]))
		created := p101ReadResponse(t, h, createID)
		closeID := "req-p101-close-undelivered-close"
		p101Import(t, h, closeID, map[string]any{
			"request_id": closeID, "idempotency_key": "key-p101-close-undelivered-close", "operation": "close_session", "session_id": created.SessionID,
		})
		createIntent, err := h.authority.GetLocalIntentByResource(context.Background(), "create_session", created.SessionID, p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		closeIntent, err := h.authority.GetLocalIntentByIdempotency(context.Background(), "close_session", "key-p101-close-undelivered-close", p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		p101SetIntentDelivery(t, h, createIntent, store.LocalIntentNotDelivered)
		p101SetIntentDelivery(t, h, closeIntent, store.LocalIntentNotDelivered)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		createResult, closeResult := p101ReadResponse(t, h, createID), p101ReadResponse(t, h, closeID)
		if createResult.RequestState != "rejected" || createResult.DeliveryState != string(store.LocalIntentNotDelivered) || createResult.SessionState != "" {
			t.Fatalf("never-delivered create response=%+v", createResult)
		}
		if closeResult.RequestState != "complete" || closeResult.DeliveryState != string(store.LocalIntentNotDelivered) || closeResult.TeardownOutcome != "not_created" || closeResult.SessionState != "" {
			t.Fatalf("close before creation delivery response=%+v", closeResult)
		}
	})

	t.Run("one-off run never delivered", func(t *testing.T) {
		h, _ := newP096Harness(t)
		runID := "req-p101-run-undelivered"
		p101Import(t, h, runID, p100RunRequest(runID, "key-p101-run-undelivered", "remote", "linux-host", "linux-dev", "printf never"))
		accepted := p101ReadResponse(t, h, runID)
		intent, err := h.authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
		if err != nil {
			t.Fatal(err)
		}
		p101SetIntentDelivery(t, h, intent, store.LocalIntentNotDelivered)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		result := p101ReadResponse(t, h, runID)
		if result.RequestState != "rejected" || result.DeliveryState != string(store.LocalIntentNotDelivered) || result.JobID != accepted.JobID || result.SessionID != accepted.SessionID || result.CommandID != accepted.CommandID || result.JobPhase != "" || result.CommandState != "" || result.TeardownOutcome != "" || result.AvailableEventSequence != nil || result.Error == nil {
			t.Fatalf("never-delivered run response=%+v", result)
		}
		if _, err := h.authority.GetJob(context.Background(), domain.JobID(accepted.JobID)); err == nil {
			t.Fatal("never-delivered run created an authoritative job")
		}
	})
}

func TestP101M10MailboxCannotAccessDirectCreatedResources(t *testing.T) {
	h, _ := newP096Harness(t)
	ctx := context.Background()
	directController, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "p101-direct-client")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("sess-p101-direct")
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: sessionID, Target: target, Environment: "linux-dev", Controller: directController,
		Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, sessionID, domain.SessionStateReady, "p101-direct-ready"); err != nil {
		t.Fatal(err)
	}
	directCommandID := domain.CommandID("cmd-p101-direct")
	hash, err := domain.HashMutationRequestJSON("submit_command", []byte(`{"operation":"submit_command","session_id":"sess-p101-direct","script":"printf direct"}`), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: directCommandID, SessionID: sessionID, IdempotencyKey: "key-p101-direct-command",
		RequestHash: hash, Script: "printf direct", Timeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, operation string
		request         map[string]any
	}{
		{name: "get_session", operation: "get_session", request: map[string]any{"session_id": string(sessionID)}},
		{name: "submit_command", operation: "submit_command", request: map[string]any{"session_id": string(sessionID), "script": "printf forbidden", "idempotency_key": "key-p101-direct-submit", "timeout_seconds": 30}},
		{name: "get_command", operation: "get_command", request: map[string]any{"command_id": string(directCommandID)}},
		{name: "cancel_command", operation: "cancel_command", request: map[string]any{"command_id": string(directCommandID), "idempotency_key": "key-p101-direct-cancel"}},
		{name: "close_session", operation: "close_session", request: map[string]any{"session_id": string(sessionID), "idempotency_key": "key-p101-direct-close"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			requestID := "req-p101-direct-" + test.name
			request := map[string]any{"request_id": requestID, "operation": test.operation}
			for key, value := range test.request {
				request[key] = value
			}
			p101Import(t, h, requestID, request)
			response := p101ReadResponse(t, h, requestID)
			if response.RequestState != "rejected" || response.Error == nil || response.Error.Code != "resource_not_found" || response.SessionState != "" || response.CommandState != "" {
				t.Fatalf("direct-created resource response=%+v", response)
			}
		})
	}
	for _, operationAndKey := range [][2]string{{"submit_command", "key-p101-direct-submit"}, {"cancel_command", "key-p101-direct-cancel"}, {"close_session", "key-p101-direct-close"}} {
		if _, err := h.authority.GetLocalIntentByIdempotency(ctx, operationAndKey[0], operationAndKey[1], p063Owner(t)); err == nil {
			t.Fatalf("denied direct-created %s wrote a Mac intent", operationAndKey[0])
		}
	}
}

func TestP101RunRetryDoesNotCreateOrStartAnotherCommand(t *testing.T) {
	h, _ := newP096Harness(t)
	requestID := "req-p101-run-once"
	key := "key-p101-run-once"
	request := p100RunRequest(requestID, key, "local", "mac-workstation", "mac-dev", "printf once")
	p101Import(t, h, requestID, request)
	accepted := p101ReadResponse(t, h, requestID)
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
	p101CompleteLocalRun(t, h, intent, "once\n")
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := p101ReadResponse(t, h, requestID)
	if first.RequestState != "complete" || first.CommandState != string(domain.CommandStateSucceeded) || first.TeardownOutcome != "closed" || first.AvailableEventSequence == nil {
		t.Fatalf("first terminal run response=%+v", first)
	}

	retryID := requestID + "-retry"
	retry := p100RunRequest(retryID, key, "local", "mac-workstation", "mac-dev", "printf once")
	p101Import(t, h, retryID, retry)
	second := p101ReadResponse(t, h, retryID)
	if second.RequestState != "complete" || second.JobID != first.JobID || second.SessionID != first.SessionID || second.CommandID != first.CommandID || second.CommandState != first.CommandState || second.TeardownOutcome != first.TeardownOutcome || second.AvailableEventSequence == nil || *second.AvailableEventSequence != *first.AvailableEventSequence {
		t.Fatalf("run retry did not replay original terminal result: first=%+v retry=%+v", first, second)
	}
	replayedIntent, err := h.authority.GetLocalIntentByIdempotency(context.Background(), "run", key, p063Owner(t))
	if err != nil || replayedIntent.IntentID != intent.IntentID || replayedIntent.JobID != intent.JobID || replayedIntent.CommandID != intent.CommandID {
		t.Fatalf("run retry created a second durable intent: first=%+v retry=%+v err=%v", intent, replayedIntent, err)
	}
	job, err := h.authority.GetJob(context.Background(), intent.JobID)
	if err != nil || job.JobID != intent.JobID || job.CommandID != intent.CommandID || job.CommandState == nil || *job.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("run retry changed authority job: job=%+v err=%v", job, err)
	}
	command, events, err := h.authority.GetCommandWithEvents(context.Background(), intent.CommandID)
	if err != nil || command.State != domain.CommandStateSucceeded {
		t.Fatalf("run command after retry=%+v err=%v", command, err)
	}
	starts := 0
	for _, event := range events {
		if event.Type == "command_started" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("one-off retry recorded %d command starts, want exactly one", starts)
	}
}

func p101CreateSession(t *testing.T, h *p095Harness, target p101Target, suffix string) string {
	t.Helper()
	requestID := "req-p101-" + target.name + "-create-" + suffix
	p101Import(t, h, requestID, p101CreateRequest(requestID, "key-p101-"+target.name+"-create-"+suffix, target))
	accepted := p101ReadResponse(t, h, requestID)
	if accepted.RequestState != "accepted" || accepted.SessionID == "" || accepted.DeliveryState != string(store.LocalIntentRecorded) || accepted.SessionState != "" {
		t.Fatalf("create acknowledgement invented authority state: %+v", accepted)
	}
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "create_session", accepted.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if target.kind == string(domain.TargetKindRemote) {
		p101SetIntentDelivery(t, h, intent, store.LocalIntentUncertain)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		uncertain := p101ReadResponse(t, h, requestID)
		if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.SessionState != "" {
			t.Fatalf("uncertain create response=%+v", uncertain)
		}
	}
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
	if target.kind == string(domain.TargetKindLocal) {
		if _, err := h.authority.CreateSession(context.Background(), store.SessionCreate{
			SessionID: intent.SessionID, Target: intent.Target, Environment: intent.Environment,
			Controller: intent.Controller, Source: intent.Source, Limits: p094SessionLimits(),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.authority.TransitionSession(context.Background(), intent.SessionID, domain.SessionStateReady, "p101-matrix-ready"); err != nil {
			t.Fatal(err)
		}
	} else {
		p101UpsertRemoteSession(t, h, intent, domain.SessionStateReady, time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := p101ReadResponse(t, h, requestID)
	if ready.RequestState != "complete" || ready.SessionState != string(domain.SessionStateReady) || ready.SessionID != accepted.SessionID {
		t.Fatalf("create terminal boundary response=%+v", ready)
	}
	return accepted.SessionID
}

func p101CreateRequest(requestID, key string, target p101Target) map[string]any {
	return map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "create_session",
		"environment":      target.environment,
		"execution_target": map[string]string{"kind": target.kind, "profile": target.profile},
		"source":           map[string]string{"mode": "empty"},
	}
}

func p101Submit(t *testing.T, h *p095Harness, target p101Target, sessionID, suffix string, finish bool) string {
	t.Helper()
	requestID := "req-p101-" + target.name + "-submit-" + suffix
	p101Import(t, h, requestID, map[string]any{
		"request_id": requestID, "idempotency_key": "key-p101-" + target.name + "-submit-" + suffix,
		"operation": "submit_command", "session_id": sessionID, "script": "printf p101-" + suffix, "timeout_seconds": 30,
	})
	accepted := p101ReadResponse(t, h, requestID)
	if accepted.RequestState != "accepted" || accepted.CommandID == "" || accepted.SessionID != sessionID || accepted.DeliveryState != string(store.LocalIntentRecorded) || accepted.CommandState != "" {
		t.Fatalf("submit acknowledgement invented authority state: %+v", accepted)
	}
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if target.kind == string(domain.TargetKindRemote) {
		p101SetIntentDelivery(t, h, intent, store.LocalIntentUncertain)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		uncertain := p101ReadResponse(t, h, requestID)
		if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.CommandState != "" {
			t.Fatalf("uncertain submit response=%+v", uncertain)
		}
	}
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
	if target.kind == string(domain.TargetKindLocal) {
		p101AcceptLocalCommand(t, h, intent)
		if finish {
			p101FinishLocalCommand(t, h, intent.CommandID, "p101-"+suffix+"\n")
		}
	} else if finish {
		p101ProjectRemoteCommand(t, h, accepted.CommandID, sessionID, intent, domain.CommandStateSucceeded, "p101-"+suffix+"\n", 4, time.Date(2026, 9, 27, 12, 1, 0, 0, time.UTC))
	} else {
		p101ProjectRemoteCommand(t, h, accepted.CommandID, sessionID, intent, domain.CommandStateQueued, "", 1, time.Date(2026, 9, 27, 12, 1, 0, 0, time.UTC))
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return accepted.CommandID
}

func p101AcceptLocalCommand(t *testing.T, h *p095Harness, intent store.LocalIntentRecord) {
	t.Helper()
	if intent.IntentOrdinal == nil {
		t.Fatal("accepted local command intent has no ordinal")
	}
	_, _, err := h.authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, IdempotencyKey: intent.IdempotencyKey,
		RequestHash: intent.RequestHash, Script: string(intent.ScriptBytes), Timeout: 30 * time.Second,
		IntentOrdinal: *intent.IntentOrdinal,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func p101FinishLocalCommand(t *testing.T, h *p095Harness, commandID domain.CommandID, output string) {
	t.Helper()
	ctx := context.Background()
	started, err := h.authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != commandID {
		t.Fatalf("start command=%+v err=%v", started, err)
	}
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: commandID, Type: "stdout", Payload: []byte(output), ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	if _, err := h.authority.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID: commandID, NextState: domain.CommandStateSucceeded, ExitCode: &exitCode, OutputComplete: true,
	}, domain.SessionStateReady, "p101-command-complete", true); err != nil {
		t.Fatal(err)
	}
}

func p101ProjectRemoteCommand(t *testing.T, h *p095Harness, commandID, sessionID string, intent store.LocalIntentRecord, state domain.CommandState, output string, eventCount int64, when time.Time) {
	t.Helper()
	ctx := context.Background()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, intent.Target.Profile())
	if err != nil {
		t.Fatal(err)
	}
	command := domain.CommandID(commandID)
	events := []store.RemoteEventRecord{{CommandID: command, Sequence: 1, Type: "command_queued", OccurredAt: when}}
	finalSequence := int64(1)
	var exitCode *int
	outputComplete := false
	switch state {
	case domain.CommandStateQueued:
	case domain.CommandStateCancelled:
		events = append(events, store.RemoteEventRecord{CommandID: command, Sequence: 2, Type: "command_cancelled", OccurredAt: when.Add(time.Second)})
		finalSequence, outputComplete = 2, true
	case domain.CommandStateSucceeded:
		code := 0
		exitCode = &code
		text := []byte(output)
		events = append(events,
			store.RemoteEventRecord{CommandID: command, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
			store.RemoteEventRecord{CommandID: command, Sequence: 3, Type: "stdout", Payload: text, ByteCount: int64(len(text)), OccurredAt: when.Add(2 * time.Second)},
			store.RemoteEventRecord{CommandID: command, Sequence: 4, Type: "command_succeeded", OccurredAt: when.Add(3 * time.Second)},
		)
		finalSequence, outputComplete = 4, true
	default:
		t.Fatalf("unsupported P101 remote command state %q", state)
	}
	if int64(len(events)) != eventCount {
		t.Fatalf("test fixture event count=%d, want %d", len(events), eventCount)
	}
	newEvents := events
	if state == domain.CommandStateCancelled {
		// The queued intent already published sequence 1. Append only the
		// target's new terminal cancellation event so its immutable queue event
		// retains its original timestamp and bytes.
		newEvents = events[1:]
	}
	if _, err := h.authority.MirrorRemoteEvents(ctx, newEvents); err != nil {
		t.Fatal(err)
	}
	projection := store.RemoteCommandProjection{
		CommandID: command, SessionID: domain.SessionID(sessionID), Ordinal: p101IntentOrdinal(intent), State: state,
		Target: target, Controller: intent.Controller, Environment: intent.Environment, Source: intent.Source,
		Capabilities: p076APICapabilities(), ObservedAt: when,
	}
	if state.IsTerminal() {
		projection.ExitCode = exitCode
		projection.FinalEventSequence = &finalSequence
		projection.OutputComplete = outputComplete
	}
	if _, err := h.authority.UpsertRemoteCommandProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
}

func p101IntentOrdinal(intent store.LocalIntentRecord) int64 {
	if intent.IntentOrdinal == nil {
		return 1
	}
	return *intent.IntentOrdinal
}

func p101RunAndComplete(t *testing.T, h *p095Harness, target p101Target, suffix string) {
	t.Helper()
	requestID := "req-p101-" + target.name + "-run-" + suffix
	p101Import(t, h, requestID, p100RunRequest(requestID, "key-p101-"+target.name+"-run-"+suffix, target.kind, target.profile, target.environment, "printf p101-run"))
	accepted := p101ReadResponse(t, h, requestID)
	if accepted.RequestState != "accepted" || accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" || accepted.JobPhase != "" || accepted.CommandState != "" {
		t.Fatalf("run acknowledgement invented authority state: %+v", accepted)
	}
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if target.kind == string(domain.TargetKindRemote) {
		p101SetIntentDelivery(t, h, intent, store.LocalIntentUncertain)
		if err := h.processor.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		uncertain := p101ReadResponse(t, h, requestID)
		if uncertain.RequestState != "accepted" || uncertain.DeliveryState != string(store.LocalIntentUncertain) || uncertain.JobPhase != "" || uncertain.CommandState != "" || uncertain.TeardownOutcome != "" {
			t.Fatalf("uncertain run response=%+v", uncertain)
		}
	}
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
	if target.kind == string(domain.TargetKindLocal) {
		p101CompleteLocalRun(t, h, intent, "p101-run\n")
	} else {
		p101CompleteRemoteRun(t, h, intent, "p101-run\n")
	}
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	complete := p101ReadResponse(t, h, requestID)
	if complete.RequestState != "complete" || complete.JobPhase != string(store.JobPhaseComplete) || complete.CommandState != string(domain.CommandStateSucceeded) ||
		complete.JobID != accepted.JobID || complete.SessionID != accepted.SessionID || complete.CommandID != accepted.CommandID || complete.TeardownOutcome != "closed" ||
		complete.OutputComplete == nil || !*complete.OutputComplete || complete.AvailableEventSequence == nil || complete.FinalEventSequence == nil || *complete.AvailableEventSequence != *complete.FinalEventSequence || complete.EventsFile != "events/"+accepted.CommandID+".ndjson" {
		t.Fatalf("run terminal command/teardown response=%+v", complete)
	}
}

func p101CompleteLocalRun(t *testing.T, h *p095Harness, intent store.LocalIntentRecord, output string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := h.authority.AcceptJob(ctx, store.JobAcceptance{
		JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID, Controller: intent.Controller,
		IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash, Environment: intent.Environment,
		Target: intent.Target, Source: intent.Source, Script: string(intent.ScriptBytes), CanonicalPayload: intent.PayloadJSON,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: intent.SessionID, Target: intent.Target, Environment: intent.Environment, Controller: intent.Controller,
		Source: intent.Source, Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateReady, "p101-run-ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseAcceptingCommand}); err != nil {
		t.Fatal(err)
	}
	commandBody, err := json.Marshal(map[string]any{"session_id": string(intent.SessionID), "script": string(intent.ScriptBytes), "timeout_seconds": 30})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", commandBody, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	commandHash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, IdempotencyKey: string(intent.IdempotencyKey) + "-command",
		RequestHash: commandHash, Script: string(intent.ScriptBytes), Timeout: 30 * time.Second, IntentOrdinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAcceptingCommand, NextPhase: store.JobPhaseAwaitingCommand}); err != nil {
		t.Fatal(err)
	}
	started, err := h.authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != intent.CommandID {
		t.Fatalf("start one-off command=%+v err=%v", started, err)
	}
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: intent.CommandID, Type: "stdout", Payload: []byte(output), ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	completed, err := h.authority.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID: intent.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: &exitCode, OutputComplete: true,
	}, domain.SessionStateReady, "p101-run-command-complete", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAwaitingCommand, NextPhase: store.JobPhaseClosingSession, Command: &completed}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateClosing, "p101-run-closing"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateClosed, "p101-run-closed"); err != nil {
		t.Fatal(err)
	}
	teardown := store.JobTeardownClosed
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseClosingSession, NextPhase: store.JobPhaseComplete,
		Command: &completed, TeardownState: &teardown,
	}); err != nil {
		t.Fatal(err)
	}
}

func p101CompleteRemoteRun(t *testing.T, h *p095Harness, intent store.LocalIntentRecord, output string) {
	t.Helper()
	ctx := context.Background()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, intent.Target.Profile())
	if err != nil {
		t.Fatal(err)
	}
	command := intent.CommandID
	when := time.Date(2026, 9, 27, 12, 3, 0, 0, time.UTC)
	text := []byte(output)
	events := []store.RemoteEventRecord{
		{CommandID: command, Sequence: 1, Type: "command_queued", OccurredAt: when},
		{CommandID: command, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
		{CommandID: command, Sequence: 3, Type: "stdout", Payload: text, ByteCount: int64(len(text)), OccurredAt: when.Add(2 * time.Second)},
		{CommandID: command, Sequence: 4, Type: "command_succeeded", OccurredAt: when.Add(3 * time.Second)},
	}
	if _, err := h.authority.MirrorRemoteEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	exitCode, finalSequence := 0, int64(4)
	state := domain.CommandStateSucceeded
	if _, err := h.authority.UpsertRemoteCommandProjection(ctx, store.RemoteCommandProjection{
		CommandID: command, SessionID: intent.SessionID, Ordinal: p101IntentOrdinal(intent), State: state,
		ExitCode: &exitCode, FinalEventSequence: &finalSequence, OutputComplete: true,
		Target: target, Controller: intent.Controller, Environment: intent.Environment, Source: intent.Source,
		Capabilities: p076APICapabilities(), ObservedAt: when.Add(4 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	teardown := store.JobTeardownClosed
	phase := store.JobPhaseComplete
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, store.RemoteJobProjection{
		JobID: intent.JobID, SessionID: intent.SessionID, CommandID: command, Phase: phase, CommandState: &state,
		ExitCode: &exitCode, FinalEventSequence: &finalSequence, OutputComplete: true, TeardownState: teardown,
		Target: target, Controller: intent.Controller, Environment: intent.Environment, Source: intent.Source,
		Capabilities: p076APICapabilities(), ObservedAt: when.Add(5 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

func p101UpsertRemoteSession(t *testing.T, h *p095Harness, intent store.LocalIntentRecord, state domain.SessionState, observedAt time.Time) {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, intent.Target.Profile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.UpsertRemoteSessionProjection(context.Background(), store.RemoteSessionProjection{
		SessionID: intent.SessionID, Target: target, Controller: intent.Controller, State: state,
		Environment: intent.Environment, Source: intent.Source, Capabilities: p076APICapabilities(), ObservedAt: observedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func p101CloseTarget(t *testing.T, h *p095Harness, target p101Target, sessionID string) {
	t.Helper()
	if target.kind == string(domain.TargetKindLocal) {
		if _, err := h.authority.TransitionSession(context.Background(), domain.SessionID(sessionID), domain.SessionStateClosing, "p101-close-started"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.authority.TransitionSession(context.Background(), domain.SessionID(sessionID), domain.SessionStateClosed, "p101-close-complete"); err != nil {
			t.Fatal(err)
		}
		return
	}
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "create_session", sessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 27, 12, 4, 0, 0, time.UTC)
	p101UpsertRemoteSession(t, h, intent, domain.SessionStateClosing, base)
	p101UpsertRemoteSession(t, h, intent, domain.SessionStateClosed, base.Add(time.Second))
}

func p101SetIntentDelivery(t *testing.T, h *p095Harness, intent store.LocalIntentRecord, next store.LocalIntentDeliveryState) {
	t.Helper()
	ctx := context.Background()
	current, err := h.authority.GetLocalIntentByIdempotency(ctx, intent.Operation, string(intent.IdempotencyKey), p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState == next {
		return
	}
	if current.DeliveryState == store.LocalIntentRecorded && next != store.LocalIntentDispatching {
		current, err = h.authority.TransitionLocalIntent(ctx, current.IntentID, store.LocalIntentDispatching, "p101-dispatching")
		if err != nil {
			t.Fatal(err)
		}
	}
	if current.DeliveryState != next {
		if _, err := h.authority.TransitionLocalIntent(ctx, current.IntentID, next, "p101-"+string(next)); err != nil {
			t.Fatal(err)
		}
	}
}

func p101Import(t *testing.T, h *p095Harness, requestID string, request map[string]any) {
	t.Helper()
	writeP094Request(t, h.importer, requestID, request)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("P101 mailbox import %s results=%+v err=%v", requestID, results, err)
	}
}

func p101ReadResponse(t *testing.T, h *p095Harness, requestID string) p101Response {
	t.Helper()
	data, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var response p101Response
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
