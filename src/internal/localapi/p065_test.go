package localapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP065LocalAuthorityEventReplayUsesPublicNDJSONShape(t *testing.T) {
	_, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p065-replay-session")
	commandID := p065CreateAuthoritativeCommand(t, client, authority, sessionID, "p065-replay-command", "printf p065")

	response, err := client.Get("http://local/v1/commands/" + commandID + "/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/x-ndjson") {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("replay status/content-type = %d/%q, body=%s", response.StatusCode, response.Header.Get("Content-Type"), data)
	}
	events := p065ReadEvents(t, response.Body)
	wantTypes := []string{"command_queued", "command_started", "stdout", "stderr", "command_succeeded"}
	if len(events) != len(wantTypes) {
		t.Fatalf("replayed events = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for index, event := range events {
		if event["command_id"] != commandID || int(event["sequence"].(float64)) != index+1 || event["type"] != wantTypes[index] || event["timestamp"] == "" {
			t.Fatalf("event %d = %#v", index, event)
		}
		if _, hasOccurredAt := event["occurred_at"]; hasOccurredAt {
			t.Fatalf("public event exposed private occurred_at: %#v", event)
		}
	}
	if events[0]["ordinal"] != float64(1) {
		t.Fatalf("queued event ordinal = %#v", events[0]["ordinal"])
	}
	if events[2]["encoding"] != "base64" || events[2]["data_base64"] != base64.StdEncoding.EncodeToString([]byte("stdout\n")) || events[2]["byte_count"] != float64(7) {
		t.Fatalf("stdout event = %#v", events[2])
	}
	if _, ok := events[1]["byte_count"]; ok {
		t.Fatalf("lifecycle event carries byte_count: %#v", events[1])
	}

	resumed, err := client.Get("http://local/v1/commands/" + commandID + "/events?after=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Body.Close()
	resumedEvents := p065ReadEvents(t, resumed.Body)
	if len(resumedEvents) != 3 || int(resumedEvents[0]["sequence"].(float64)) != 3 || int(resumedEvents[2]["sequence"].(float64)) != 5 {
		t.Fatalf("resumed events = %#v", resumedEvents)
	}
}

func TestP065LocalAuthorityEventFollowReplaysAndHandsOffToLive(t *testing.T) {
	_, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p065-follow-session")
	commandID := p065CreateAuthoritativeRunningCommand(t, client, authority, sessionID, "p065-follow-command", "printf follow")

	request, err := http.NewRequest(http.MethodGet, "http://local/v1/commands/"+commandID+"/events?after=1&follow=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request = request.WithContext(requestContext)
	responses := make(chan *http.Response, 1)
	errors := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(request)
		if requestErr != nil {
			errors <- requestErr
			return
		}
		responses <- response
	}()

	// The follow request is registered before this append in normal operation;
	// the short wait also permits the Unix HTTP handler to finish its durable
	// replay setup while keeping the test independent of private server state.
	time.Sleep(50 * time.Millisecond)
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: domain.CommandID(commandID), Type: "stdout", Payload: []byte("live\n"), ByteCount: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(context.Background(), store.CommandTransition{CommandID: domain.CommandID(commandID), NextState: domain.CommandStateSucceeded, ExitCode: p065IntPointer(0), OutputComplete: true}, domain.SessionStateReady, "p065 complete", false); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-errors:
		t.Fatal(err)
	case response := <-responses:
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("follow status = %d, body=%s", response.StatusCode, data)
		}
		events := p065ReadEvents(t, response.Body)
		if len(events) != 3 || int(events[0]["sequence"].(float64)) != 2 || int(events[2]["sequence"].(float64)) != 4 || events[2]["type"] != "command_succeeded" {
			t.Fatalf("follow events = %#v", events)
		}
	case <-requestContext.Done():
		t.Fatal("timed out waiting for follow response")
	}
}

func TestP065RemoteIntentHasNoLocalAuthorityEventStream(t *testing.T) {
	_, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, "p065-remote-session")
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"echo remote"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p065-remote-command")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("remote submit status = %d, body=%s", response.StatusCode, data)
	}
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(data, &accepted); err != nil || accepted.CommandID == "" {
		t.Fatalf("remote submit response = %s", data)
	}
	events, err := client.Get("http://local/v1/commands/" + accepted.CommandID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	if events.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(events.Body)
		t.Fatalf("remote events status = %d, body=%s", events.StatusCode, body)
	}
	_ = authority
}

func p065CreateAuthoritativeCommand(t *testing.T, client *http.Client, authority *store.AuthorityStore, sessionID, key, script string) string {
	t.Helper()
	commandID := p065CreateAuthoritativeRunningCommand(t, client, authority, sessionID, key, script)
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: domain.CommandID(commandID), Type: "stdout", Payload: []byte("stdout\n"), ByteCount: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: domain.CommandID(commandID), Type: "stderr", Payload: []byte("stderr\n"), ByteCount: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(context.Background(), store.CommandTransition{CommandID: domain.CommandID(commandID), NextState: domain.CommandStateSucceeded, ExitCode: p065IntPointer(0), OutputComplete: true}, domain.SessionStateReady, "p065 complete", false); err != nil {
		t.Fatal(err)
	}
	return commandID
}

func p065CreateAuthoritativeRunningCommand(t *testing.T, client *http.Client, authority *store.AuthorityStore, sessionID, key, script string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"`+script+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", key+"-intent")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("command intent status = %d, body=%s", response.StatusCode, data)
	}
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(data, &accepted); err != nil || accepted.CommandID == "" {
		t.Fatalf("command intent response = %s", data)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	target := intent.Target
	limits := domain.DefaultServiceLimits()
	if _, err := authority.CreateSession(context.Background(), store.SessionCreate{SessionID: intent.SessionID, Target: target, Environment: intent.Environment, Controller: intent.Controller, Source: intent.Source, Limits: domain.EffectiveSessionLimits{CommandTimeout: limits.CommandTimeout, IdleTimeout: limits.IdleTimeout, SessionMaxLifetime: limits.SessionMaxLifetime, OutputBytesPerCommand: limits.OutputBytesPerCommand}}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), intent.SessionID, domain.SessionStateReady, "p065 ready"); err != nil {
		t.Fatal(err)
	}
	var ordinal int64
	if intent.IntentOrdinal != nil {
		ordinal = *intent.IntentOrdinal
	}
	command, _, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{CommandID: intent.CommandID, SessionID: intent.SessionID, RequestHash: intent.RequestHash, IdempotencyKey: key + "-authority", Script: script, Timeout: 7 * time.Second, IntentOrdinal: ordinal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	return string(command.CommandID)
}

func p065ReadEvents(t *testing.T, reader io.Reader) []map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	var events []map[string]any
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func p065IntPointer(value int) *int { return &value }
