package localapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP077MacAPIReadsMirroredRemoteEventsAndRemoteJobProjection(t *testing.T) {
	_, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, "p077-api-session")
	commandID := p077APICommand(t, client, sessionID)
	intent, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", commandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentDispatching, "p077-dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentAccepted, "p077-accepted"); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 27, 14, 20, 0, 0, time.UTC)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []store.RemoteEventRecord{{CommandID: intent.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: when}, {CommandID: intent.CommandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)}, {CommandID: intent.CommandID, Sequence: 3, Type: "stdout", Payload: []byte("remote\n"), ByteCount: 7, OccurredAt: when.Add(2 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	beforeProjection, err := client.Get("http://local/v1/commands/" + commandID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	beforeProjectionData, _ := io.ReadAll(beforeProjection.Body)
	beforeProjection.Body.Close()
	if beforeProjection.StatusCode != http.StatusConflict || !strings.Contains(string(beforeProjectionData), `"code":"events_unavailable"`) || strings.Contains(string(beforeProjectionData), `"type":"command_queued"`) {
		t.Fatalf("pre-projection event response status=%d body=%s", beforeProjection.StatusCode, beforeProjectionData)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	if intent.IntentOrdinal == nil {
		t.Fatal("accepted remote command intent has no ordinal")
	}
	if _, err := authority.UpsertRemoteCommandProjection(context.Background(), store.RemoteCommandProjection{
		CommandID: intent.CommandID, SessionID: intent.SessionID, Ordinal: *intent.IntentOrdinal, State: domain.CommandStateRunning,
		Target: target, Controller: intent.Controller, Environment: intent.Environment, Source: intent.Source,
		Capabilities: p076APICapabilities(), ObservedAt: when,
	}); err != nil {
		t.Fatal(err)
	}
	replay, err := client.Get("http://local/v1/commands/" + commandID + "/events?after=1")
	if err != nil {
		t.Fatal(err)
	}
	replayData, _ := io.ReadAll(replay.Body)
	replay.Body.Close()
	if replay.StatusCode != http.StatusOK || !strings.Contains(string(replayData), `"sequence":2`) || strings.Contains(string(replayData), `"sequence":1`) {
		t.Fatalf("mirrored replay status=%d body=%s", replay.StatusCode, replayData)
	}

	type followResult struct {
		response *http.Response
		err      error
	}
	followDone := make(chan followResult, 1)
	go func() {
		response, err := client.Get("http://local/v1/commands/" + commandID + "/events?after=3&follow=true")
		followDone <- followResult{response: response, err: err}
	}()
	time.Sleep(60 * time.Millisecond)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []store.RemoteEventRecord{{CommandID: intent.CommandID, Sequence: 4, Type: "command_succeeded", OccurredAt: when.Add(3 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-followDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		data, _ := io.ReadAll(result.response.Body)
		result.response.Body.Close()
		if result.response.StatusCode != http.StatusOK || strings.Contains(string(data), `"sequence":4`) || strings.Contains(string(data), `command_succeeded`) {
			t.Fatalf("unreconciled mirrored follow exposed terminal event status=%d body=%s", result.response.StatusCode, data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unreconciled mirrored follow did not close before terminal event")
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentReconciled, "p077-legacy-terminal-event-reconciled"); err != nil {
		t.Fatal(err)
	}
	legacyReplay, err := client.Get("http://local/v1/commands/" + commandID + "/events?after=3")
	if err != nil {
		t.Fatal(err)
	}
	legacyReplayData, _ := io.ReadAll(legacyReplay.Body)
	legacyReplay.Body.Close()
	if legacyReplay.StatusCode != http.StatusConflict || strings.Contains(string(legacyReplayData), `"sequence":4`) {
		t.Fatalf("legacy reconciled terminal replay status=%d body=%s", legacyReplay.StatusCode, legacyReplayData)
	}
	if _, err := authority.MarkRemoteIntentTerminalProof(context.Background(), intent.IntentID, "p077-terminal-event-proof-reconciled"); err != nil {
		t.Fatal(err)
	}
	terminalReplay, err := client.Get("http://local/v1/commands/" + commandID + "/events?after=3")
	if err != nil {
		t.Fatal(err)
	}
	terminalReplayData, _ := io.ReadAll(terminalReplay.Body)
	terminalReplay.Body.Close()
	if terminalReplay.StatusCode != http.StatusOK || !strings.Contains(string(terminalReplayData), `"sequence":4`) || !strings.Contains(string(terminalReplayData), `command_succeeded`) {
		t.Fatalf("reconciled terminal replay status=%d body=%s", terminalReplay.StatusCode, terminalReplayData)
	}

	jobRequest, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"echo job"}`))
	if err != nil {
		t.Fatal(err)
	}
	jobRequest.Header.Set("Idempotency-Key", "p077-api-job")
	jobResponse, err := client.Do(jobRequest)
	if err != nil {
		t.Fatal(err)
	}
	jobData, _ := io.ReadAll(jobResponse.Body)
	jobResponse.Body.Close()
	var accepted struct {
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		CommandID string `json:"command_id"`
	}
	if jobResponse.StatusCode != http.StatusAccepted || json.Unmarshal(jobData, &accepted) != nil || accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" {
		t.Fatalf("job acceptance status=%d body=%s", jobResponse.StatusCode, jobData)
	}
	jobIntent, err := authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), jobIntent.IntentID, store.LocalIntentDispatching, "p077-dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), jobIntent.IntentID, store.LocalIntentAccepted, "p077-accepted"); err != nil {
		t.Fatal(err)
	}
	jobState := domain.CommandStateSucceeded
	if _, err := authority.UpsertRemoteJobProjection(context.Background(), store.RemoteJobProjection{JobID: jobIntent.JobID, SessionID: jobIntent.SessionID, CommandID: jobIntent.CommandID, Phase: store.JobPhaseComplete, CommandState: &jobState, OutputComplete: true, TeardownState: store.JobTeardownClosed, Target: target, Controller: jobIntent.Controller, Environment: jobIntent.Environment, Source: jobIntent.Source, Capabilities: p076APICapabilities(), ObservedAt: when.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	unreconciledJob, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	unreconciledJobData, _ := io.ReadAll(unreconciledJob.Body)
	unreconciledJob.Body.Close()
	if unreconciledJob.StatusCode != http.StatusOK || !strings.Contains(string(unreconciledJobData), `"view":"local_intent"`) || strings.Contains(string(unreconciledJobData), `"command_state":"succeeded"`) {
		t.Fatalf("unreconciled job response status=%d body=%s", unreconciledJob.StatusCode, unreconciledJobData)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), jobIntent.IntentID, store.LocalIntentReconciled, "p077-legacy-job-reconciled"); err != nil {
		t.Fatal(err)
	}
	legacyJob, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	legacyJobData, _ := io.ReadAll(legacyJob.Body)
	legacyJob.Body.Close()
	if legacyJob.StatusCode != http.StatusOK || !strings.Contains(string(legacyJobData), `"view":"local_intent"`) || strings.Contains(string(legacyJobData), `"command_state":"succeeded"`) {
		t.Fatalf("legacy reconciled job response status=%d body=%s", legacyJob.StatusCode, legacyJobData)
	}
	if _, err := authority.MarkRemoteIntentTerminalProof(context.Background(), jobIntent.IntentID, "p077-job-read-reconciled"); err != nil {
		t.Fatal(err)
	}
	readJob, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	readJobData, _ := io.ReadAll(readJob.Body)
	readJob.Body.Close()
	if readJob.StatusCode != http.StatusOK || !strings.Contains(string(readJobData), `"view":"projection"`) || !strings.Contains(string(readJobData), `"authority":"remote"`) || !strings.Contains(string(readJobData), `"command_state":"succeeded"`) {
		t.Fatalf("job projection status=%d body=%s", readJob.StatusCode, readJobData)
	}
}

func p077APICommand(t *testing.T, client *http.Client, sessionID string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"echo mirror"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p077-api-command")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	if response.StatusCode != http.StatusAccepted || json.Unmarshal(data, &accepted) != nil || accepted.CommandID == "" {
		t.Fatalf("command acceptance status=%d body=%s", response.StatusCode, data)
	}
	return accepted.CommandID
}
