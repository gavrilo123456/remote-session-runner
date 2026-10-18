package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestSimpleQueuedRunCommandReadsResolveByCommandIdentity(t *testing.T) {
	h := newP095Harness(t)
	body, err := json.Marshal(map[string]any{
		"environment":      "linux-host-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
		"script":           "printf simple-run-read",
		"timeout_seconds":  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, failure := h.server.acceptCreateJobIntent(context.Background(), "key-simple-run-command-read", body, nil)
	if failure != nil || accepted.CommandID == "" {
		t.Fatalf("create queued run intent: acceptance=%+v failure=%+v", accepted, failure)
	}

	commandResponse := httptest.NewRecorder()
	h.server.serveHTTP(commandResponse, httptest.NewRequest("GET", "http://local/v1/commands/"+accepted.CommandID, nil))
	if commandResponse.Code != 200 {
		t.Fatalf("GET queued run command status=%d body=%s, want its durable local intent", commandResponse.Code, commandResponse.Body.String())
	}

	eventsResponse := httptest.NewRecorder()
	h.server.serveHTTP(eventsResponse, httptest.NewRequest("GET", "http://local/v1/commands/"+accepted.CommandID+"/events?after=0", nil))
	if eventsResponse.Code != 409 {
		t.Fatalf("queued run events status=%d body=%s, want events_unavailable until remote acceptance", eventsResponse.Code, eventsResponse.Body.String())
	}
	var failureBody struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(eventsResponse.Body.Bytes(), &failureBody); err != nil || failureBody.Code != "events_unavailable" {
		t.Fatalf("queued run event boundary=%s err=%v, want events_unavailable", eventsResponse.Body.String(), err)
	}
}

func TestSimpleAcceptedLocalRunCommandReadsUseExactRunIdentity(t *testing.T) {
	h := newP095Harness(t)
	body := []byte(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":"printf local-run-output","timeout_seconds":5}`)
	accepted, failure := h.server.acceptCreateJobIntent(context.Background(), "key-simple-local-run-command-read", body, nil)
	if failure != nil || accepted.JobID == "" || accepted.CommandID == "" {
		t.Fatalf("create local run intent: acceptance=%+v failure=%+v", accepted, failure)
	}
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentDispatching, "simple-local-run-command-read-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentAccepted, "simple-local-run-command-read-accepted"); err != nil {
		t.Fatal(err)
	}
	p101CompleteLocalRun(t, h, intent, "local-run-output\n")

	command, err := h.authority.GetCommand(context.Background(), intent.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if domain.CompareIdempotency(command.RequestHash, intent.RequestHash) == domain.IdempotencySamePayload {
		t.Fatal("fixture must distinguish the run request hash from its command request hash")
	}
	if !localCommandRequestHashMatchesIntent(intent, command) {
		t.Fatal("run command identity should not compare its distinct submit-command hash with the enclosing run hash")
	}
	submitIntent := intent
	submitIntent.Operation = "submit_command"
	if localCommandRequestHashMatchesIntent(submitIntent, command) {
		t.Fatal("ordinary submit command identity must continue to require a matching request hash")
	}

	jobSnapshot, err := h.server.GetRunSnapshot(context.Background(), string(intent.JobID))
	if err != nil || jobSnapshot.Command == nil || jobSnapshot.Command.State != domain.CommandStateSucceeded || !jobSnapshot.Command.OutputComplete {
		t.Fatalf("local run job snapshot=%+v err=%v", jobSnapshot, err)
	}

	commandRead := httptest.NewRecorder()
	h.server.serveHTTP(commandRead, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+string(intent.CommandID), nil))
	if commandRead.Code != http.StatusOK || !strings.Contains(commandRead.Body.String(), `"view":"authority"`) || !strings.Contains(commandRead.Body.String(), `"command_state":"succeeded"`) {
		t.Fatalf("GET local run command status=%d body=%s", commandRead.Code, commandRead.Body.String())
	}
	eventsRead := httptest.NewRecorder()
	h.server.serveHTTP(eventsRead, httptest.NewRequest(http.MethodGet, "http://local/v1/commands/"+string(intent.CommandID)+"/events?after=0", nil))
	if eventsRead.Code != http.StatusOK || !strings.Contains(eventsRead.Body.String(), `"type":"stdout"`) || !strings.Contains(eventsRead.Body.String(), `"type":"command_succeeded"`) {
		t.Fatalf("GET local run command events status=%d body=%s", eventsRead.Code, eventsRead.Body.String())
	}

	commandSnapshot, err := h.server.GetCommandSnapshot(context.Background(), string(intent.CommandID))
	if err != nil || commandSnapshot.State != domain.CommandStateSucceeded || !commandSnapshot.OutputComplete || !strings.Contains(string(commandSnapshot.EventBytes), "local-run-output") {
		t.Fatalf("local run mailbox command snapshot=%+v err=%v", commandSnapshot, err)
	}

	if _, err := h.db.ExecContext(context.Background(), `UPDATE exec_commands SET script_bytes=? WHERE command_id=?`, []byte("printf tampered"), string(intent.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.server.GetCommandSnapshot(context.Background(), string(intent.CommandID)); err == nil {
		t.Fatal("mailbox command snapshot accepted a script identity mismatch")
	}
	for _, path := range []string{"/v1/commands/" + string(intent.CommandID), "/v1/commands/" + string(intent.CommandID) + "/events?after=0"} {
		response := httptest.NewRecorder()
		h.server.serveHTTP(response, httptest.NewRequest(http.MethodGet, "http://local"+path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("mismatched command identity path=%s status=%d body=%s, want fail-closed 503", path, response.Code, response.Body.String())
		}
	}
}
