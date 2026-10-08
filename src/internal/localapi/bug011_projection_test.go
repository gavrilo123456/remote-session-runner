package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG011LocalJobStatusProjectsAndRetractsRetainedCapacity(t *testing.T) {
	ctx := context.Background()
	_, authority, _, client := p063Server(t)
	pairs := bug011RetainedLostPairs(t, authority, "status")
	accepted := bug011PostLocalRun(t, client, "status")
	intent, err := authority.GetLocalIntentByResource(ctx, "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	bug011SetAccepted(t, authority, intent)
	bug011MaterializeQueuedLocalRun(t, authority, intent)

	blocked, blockedRaw := bug011GetLocalJob(t, client, accepted.JobID)
	bug011AssertLocalJobStatus(t, blocked, accepted, domain.CommandStateQueued, store.QueueBlockedReasonLostCapacityRecoveryPending)
	bug011AssertNestedJSONFieldPresent(t, blockedRaw, "resource", "queue_blocked_reason")

	if err := authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	released, releasedRaw := bug011GetLocalJob(t, client, accepted.JobID)
	bug011AssertLocalJobStatus(t, released, accepted, domain.CommandStateQueued, "")
	bug011AssertNestedJSONFieldAbsent(t, releasedRaw, "resource", "queue_blocked_reason")

	started, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != intent.CommandID {
		t.Fatalf("start queued local one-off=%+v err=%v", started, err)
	}
	if _, err := authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAwaitingCommand, NextPhase: store.JobPhaseAwaitingCommand, Command: &started}); err != nil {
		t.Fatal(err)
	}
	running, runningRaw := bug011GetLocalJob(t, client, accepted.JobID)
	bug011AssertLocalJobStatus(t, running, accepted, domain.CommandStateRunning, "")
	bug011AssertNestedJSONFieldAbsent(t, runningRaw, "resource", "queue_blocked_reason")
}

func TestBUG011LocalMailboxRunProjectsAndRetractsRetainedCapacity(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID := "req-bug011-local-mailbox"
	p101Import(t, h, requestID, p100RunRequest(requestID, "key-bug011-local-mailbox", "local", "mac-workstation", "mac-dev", "printf bug011"))
	initial := p101ReadResponse(t, h, requestID)
	if initial.RequestState != string(store.MailboxExchangeAccepted) || initial.DeliveryState != string(store.LocalIntentRecorded) || initial.JobID == "" || initial.SessionID == "" || initial.CommandID == "" {
		t.Fatalf("initial local mailbox receipt=%+v", initial)
	}
	intent, err := h.authority.GetLocalIntentByResource(ctx, "run", initial.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	pairs := bug011RetainedLostPairs(t, h.authority, "mailbox")
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
	bug011MaterializeQueuedLocalRun(t, h.authority, intent)

	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	blocked, blockedRaw := bug011ReadMailboxResponse(t, h, requestID)
	bug011AssertMailboxActiveStatus(t, blocked, initial, domain.CommandStateQueued, store.QueueBlockedReasonLostCapacityRecoveryPending, initial.ResponseRevision+1)
	bug011AssertNoMailboxResultFields(t, blockedRaw)

	if err := h.authority.ConfirmLostRuntimeRecoveryBatchPreservingQueuedOneOffs(ctx, pairs); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	released, releasedRaw := bug011ReadMailboxResponse(t, h, requestID)
	bug011AssertMailboxActiveStatus(t, released, initial, domain.CommandStateQueued, "", blocked.ResponseRevision+1)
	bug011AssertJSONFieldAbsent(t, releasedRaw, "queue_blocked_reason")
	bug011AssertNoMailboxResultFields(t, releasedRaw)

	started, err := h.authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
	if err != nil || started.CommandID != intent.CommandID {
		t.Fatalf("start queued local mailbox one-off=%+v err=%v", started, err)
	}
	if _, err := h.authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAwaitingCommand, NextPhase: store.JobPhaseAwaitingCommand, Command: &started}); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	running, runningRaw := bug011ReadMailboxResponse(t, h, requestID)
	bug011AssertMailboxActiveStatus(t, running, initial, domain.CommandStateRunning, "", released.ResponseRevision+1)
	bug011AssertJSONFieldAbsent(t, runningRaw, "queue_blocked_reason")
	bug011AssertNoMailboxResultFields(t, runningRaw)
}

func bug011PostLocalRun(t *testing.T, client *http.Client, suffix string) jobAcceptance {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":"printf bug011"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "key-bug011-local-status-"+suffix)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read local run acceptance read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("local run acceptance status=%d body=%s", response.StatusCode, raw)
	}
	var accepted jobAcceptance
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" || accepted.ExecutionTarget.Kind != string(domain.TargetKindLocal) {
		t.Fatalf("local run acceptance=%+v", accepted)
	}
	return accepted
}

func bug011SetAccepted(t *testing.T, authority *store.AuthorityStore, intent store.LocalIntentRecord) {
	t.Helper()
	ctx := context.Background()
	current, err := authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentDispatching, "bug011-dispatching")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, current.IntentID, store.LocalIntentAccepted, "bug011-accepted"); err != nil {
		t.Fatal(err)
	}
}

func bug011MaterializeQueuedLocalRun(t *testing.T, authority *store.AuthorityStore, intent store.LocalIntentRecord) {
	t.Helper()
	ctx := context.Background()
	if _, duplicate, err := authority.AcceptJob(ctx, store.JobAcceptance{
		JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID, Controller: intent.Controller,
		IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash, Environment: intent.Environment,
		Target: intent.Target, Source: intent.Source, Script: string(intent.ScriptBytes), CanonicalPayload: intent.PayloadJSON,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}); err != nil || duplicate {
		t.Fatalf("accept local queued one-off duplicate=%v err=%v", duplicate, err)
	}
	if _, err := authority.CreateSession(ctx, store.SessionCreate{
		SessionID: intent.SessionID, Target: intent.Target, Environment: intent.Environment, Controller: intent.Controller,
		Source: intent.Source, Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, intent.SessionID, domain.SessionStateReady, "bug011-local-ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseAcceptingCommand}); err != nil {
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
	command, duplicate, err := authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, IdempotencyKey: string(intent.IdempotencyKey) + "-command",
		RequestHash: commandHash, Script: string(intent.ScriptBytes), Timeout: 30 * time.Second, IntentOrdinal: 1,
	})
	if err != nil || duplicate {
		t.Fatalf("accept local queued one-off command=%+v duplicate=%v err=%v", command, duplicate, err)
	}
	if _, err := authority.CheckpointJob(ctx, intent.JobID, store.JobCheckpoint{ExpectedPhase: store.JobPhaseAcceptingCommand, NextPhase: store.JobPhaseAwaitingCommand, Command: &command}); err != nil {
		t.Fatal(err)
	}
}

func bug011RetainedLostPairs(t *testing.T, authority *store.AuthorityStore, suffix string) []store.LostRuntimeRecoveryPair {
	t.Helper()
	ctx := context.Background()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller := p063Owner(t)
	pairs := make([]store.LostRuntimeRecoveryPair, 0, store.DefaultRunningCommandLimit)
	for index := 0; index < store.DefaultRunningCommandLimit; index++ {
		sessionID := domain.SessionID(fmt.Sprintf("sess-bug011-retained-%s-%d", suffix, index))
		commandID := domain.CommandID(fmt.Sprintf("cmd-bug011-retained-%s-%d", suffix, index))
		if _, err := authority.CreateSession(ctx, store.SessionCreate{
			SessionID: sessionID, Target: target, Environment: "mac-dev", Controller: controller,
			Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.CompleteSessionCreation(ctx, sessionID, domain.SessionStateReady, "generation-"+string(sessionID), "", "bug011-retained-ready"); err != nil {
			t.Fatal(err)
		}
		hash, err := domain.HashMutationRequestJSON("submit_command", []byte(fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":"printf retained"}`, sessionID)), domain.CanonicalizationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		command, duplicate, err := authority.AcceptCommand(ctx, store.CommandAcceptance{
			CommandID: commandID, SessionID: sessionID, IdempotencyKey: fmt.Sprintf("key-bug011-retained-%s-%d", suffix, index),
			RequestHash: hash, Script: "printf retained", Timeout: 30 * time.Second,
		})
		if err != nil || duplicate {
			t.Fatalf("accept retained command=%+v duplicate=%v err=%v", command, duplicate, err)
		}
		started, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit)
		if err != nil || started.CommandID != commandID {
			t.Fatalf("start retained command=%+v err=%v", started, err)
		}
		if _, err := authority.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: commandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateLost, "bug011-retained-lost", false); err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, store.LostRuntimeRecoveryPair{SessionID: sessionID, CommandID: commandID})
	}
	return pairs
}

func bug011GetLocalJob(t *testing.T, client *http.Client, jobID string) (jobAuthorityRead, []byte) {
	t.Helper()
	response, err := client.Get("http://local/v1/jobs/" + jobID)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read local job status read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("local job status=%d body=%s", response.StatusCode, raw)
	}
	var result jobAuthorityRead
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result, raw
}

func bug011AssertLocalJobStatus(t *testing.T, got jobAuthorityRead, accepted jobAcceptance, state domain.CommandState, reason string) {
	t.Helper()
	if got.View != "authority" || got.IsStale || got.Resource.JobID != accepted.JobID || got.Resource.SessionID != accepted.SessionID || got.Resource.CommandID != accepted.CommandID ||
		got.Resource.JobPhase != string(store.JobPhaseAwaitingCommand) || got.Resource.CommandState == nil || *got.Resource.CommandState != string(state) || got.Resource.QueueBlockedReason != reason {
		t.Fatalf("local job status=%+v, want state=%s reason=%q", got, state, reason)
	}
}

func bug011ReadMailboxResponse(t *testing.T, h *p095Harness, requestID string) (p101Response, []byte) {
	t.Helper()
	raw, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var response p101Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response, raw
}

func bug011AssertMailboxActiveStatus(t *testing.T, got, initial p101Response, state domain.CommandState, reason string, revision int64) {
	t.Helper()
	if got.RequestState != string(store.MailboxExchangeAccepted) || got.ResponseRevision != revision ||
		got.JobID != initial.JobID || got.SessionID != initial.SessionID || got.CommandID != initial.CommandID ||
		got.DeliveryState != string(store.LocalIntentAccepted) || got.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		got.CommandState != string(state) || got.QueueBlockedReason != reason {
		t.Fatalf("active local mailbox response=%+v, want state=%s reason=%q revision=%d", got, state, reason, revision)
	}
}

func bug011AssertJSONFieldPresent(t *testing.T, raw []byte, name string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if _, present := object[name]; !present {
		t.Fatalf("JSON response omitted %q: %s", name, raw)
	}
}

func bug011AssertJSONFieldAbsent(t *testing.T, raw []byte, name string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if _, present := object[name]; present {
		t.Fatalf("JSON response retained %q: %s", name, raw)
	}
}

func bug011AssertNestedJSONFieldPresent(t *testing.T, raw []byte, parent, name string) {
	t.Helper()
	object := bug011JSONObject(t, raw)
	child, present := object[parent]
	if !present {
		t.Fatalf("JSON response omitted parent %q: %s", parent, raw)
	}
	bug011AssertJSONFieldPresent(t, child, name)
}

func bug011AssertNestedJSONFieldAbsent(t *testing.T, raw []byte, parent, name string) {
	t.Helper()
	object := bug011JSONObject(t, raw)
	child, present := object[parent]
	if !present {
		t.Fatalf("JSON response omitted parent %q: %s", parent, raw)
	}
	bug011AssertJSONFieldAbsent(t, child, name)
}

func bug011JSONObject(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func bug011AssertNoMailboxResultFields(t *testing.T, raw []byte) {
	t.Helper()
	for _, name := range []string{
		"observed_at", "teardown_outcome", "exit_code", "stdout", "stderr", "final_event_sequence",
		"available_event_sequence", "output_complete", "output_truncated", "output_unavailable_reason", "events_file", "error",
	} {
		bug011AssertJSONFieldAbsent(t, raw, name)
	}
}
