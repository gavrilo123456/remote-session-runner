package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP124AcceptedLocalCommandReadReturnsAuthorityStatus(t *testing.T) {
	ctx := context.Background()
	_, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p124-authority-session")
	script := "printf 'P124 authority output\\n'; exit 7"
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"`+script+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p124-authority-command")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	if response.StatusCode != http.StatusAccepted {
		response.Body.Close()
		t.Fatalf("submit status = %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if accepted.CommandID == "" {
		t.Fatal("submit response has no command ID")
	}

	createIntent, err := authority.GetLocalIntentByResource(ctx, "create_session", sessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentDispatching, "p124-test-session-dispatch"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentAccepted, "p124-test-session-accepted"); err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CreateSession(ctx, store.SessionCreate{
		SessionID: domain.SessionID(sessionID), Target: target, Environment: createIntent.Environment,
		Controller: createIntent.Controller, Source: createIntent.Source, Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, domain.SessionID(sessionID), domain.SessionStateReady, "p124-test-session-ready"); err != nil {
		t.Fatal(err)
	}

	commandIntent, err := authority.GetLocalIntentByResource(ctx, "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if commandIntent.IntentOrdinal == nil {
		t.Fatal("command intent has no ordinal")
	}
	if _, err := authority.TransitionLocalIntent(ctx, commandIntent.IntentID, store.LocalIntentDispatching, "p124-test-command-dispatch"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, commandIntent.IntentID, store.LocalIntentAccepted, "p124-test-command-accepted"); err != nil {
		t.Fatal(err)
	}
	command, _, err := authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: commandIntent.CommandID, SessionID: commandIntent.SessionID, RequestHash: commandIntent.RequestHash,
		IdempotencyKey: commandIntent.IdempotencyKey, Script: string(commandIntent.ScriptBytes), Timeout: 30 * time.Second,
		IntentOrdinal: *commandIntent.IntentOrdinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	output := []byte("P124 authority output\n")
	if _, err := authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: output, ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 7
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateFailed, ExitCode: &exitCode, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}

	readResponse, err := client.Get("http://local/v1/commands/" + accepted.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	defer readResponse.Body.Close()
	var read struct {
		View     string                    `json:"view"`
		IsStale  bool                      `json:"is_stale"`
		Resource commandProjectionResource `json:"resource"`
	}
	if err := json.NewDecoder(readResponse.Body).Decode(&read); err != nil {
		t.Fatal(err)
	}
	if readResponse.StatusCode != http.StatusOK || read.View != "authority" || read.IsStale ||
		read.Resource.CommandID != accepted.CommandID || read.Resource.SessionID != sessionID ||
		read.Resource.CommandState != string(domain.CommandStateFailed) || read.Resource.ExitCode == nil || *read.Resource.ExitCode != 7 ||
		read.Resource.FinalEventSequence == nil || *read.Resource.FinalEventSequence != 4 || !read.Resource.OutputComplete ||
		read.Resource.Authority != "local" || read.Resource.ExecutionTarget.Kind != string(domain.TargetKindLocal) ||
		read.Resource.Controller.ID != "tomasz.walczuk" {
		t.Fatalf("status=%d authority command read=%+v", readResponse.StatusCode, read)
	}
}
