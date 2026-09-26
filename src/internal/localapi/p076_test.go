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

func TestP076RemoteProjectionReadsExposeAuthorityAndStaleness(t *testing.T) {
	_, authority, _, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, "p076-api-session")
	sessionIntent, err := authority.GetLocalIntentByResource(context.Background(), "create_session", sessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), sessionIntent.IntentID, store.LocalIntentDispatching, "p076-dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), sessionIntent.IntentID, store.LocalIntentAccepted, "p076-accepted"); err != nil {
		t.Fatal(err)
	}
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if _, err := authority.UpsertRemoteSessionProjection(context.Background(), store.RemoteSessionProjection{SessionID: sessionIntent.SessionID, Target: target, Controller: sessionIntent.Controller, State: domain.SessionStateReady, Environment: sessionIntent.Environment, Source: sessionIntent.Source, Capabilities: p076APICapabilities(), ObservedAt: time.Date(2026, 9, 27, 13, 20, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := authority.MarkRemoteSessionProjectionStale(context.Background(), sessionIntent.SessionID); err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://local/v1/sessions/" + sessionID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var read struct {
		View     string `json:"view"`
		IsStale  bool   `json:"is_stale"`
		Resource struct {
			SessionState string `json:"session_state"`
			Authority    string `json:"authority"`
			Capabilities struct {
				EffectiveAccount string `json:"effective_account"`
			} `json:"capabilities"`
			IsStale bool `json:"is_stale"`
		} `json:"resource"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(data, &read) != nil || read.View != "projection" || !read.IsStale || !read.Resource.IsStale || read.Resource.SessionState != "ready" || read.Resource.Authority != "remote" || read.Resource.Capabilities.EffectiveAccount != "ubuntu" {
		t.Fatalf("session projection response status=%d body=%s decoded=%+v", response.StatusCode, data, read)
	}

	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"echo p076"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p076-api-command")
	acceptedResponse, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	acceptedData, _ := io.ReadAll(acceptedResponse.Body)
	acceptedResponse.Body.Close()
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	if acceptedResponse.StatusCode != http.StatusAccepted || json.Unmarshal(acceptedData, &accepted) != nil || accepted.CommandID == "" {
		t.Fatalf("command acceptance status=%d body=%s", acceptedResponse.StatusCode, acceptedData)
	}
	commandIntent, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), commandIntent.IntentID, store.LocalIntentDispatching, "p076-dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), commandIntent.IntentID, store.LocalIntentAccepted, "p076-accepted"); err != nil {
		t.Fatal(err)
	}
	finalSequence := int64(2)
	if _, err := authority.UpsertRemoteCommandProjection(context.Background(), store.RemoteCommandProjection{CommandID: commandIntent.CommandID, SessionID: commandIntent.SessionID, Ordinal: *commandIntent.IntentOrdinal, State: domain.CommandStateSucceeded, FinalEventSequence: &finalSequence, OutputComplete: true, Target: target, Controller: commandIntent.Controller, Environment: commandIntent.Environment, Source: commandIntent.Source, Capabilities: p076APICapabilities(), ObservedAt: time.Date(2026, 9, 27, 13, 21, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	commandResponse, err := client.Get("http://local/v1/commands/" + accepted.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	commandData, _ := io.ReadAll(commandResponse.Body)
	commandResponse.Body.Close()
	var commandReadValue struct {
		View     string `json:"view"`
		Resource struct {
			CommandState   string `json:"command_state"`
			Authority      string `json:"authority"`
			OutputComplete bool   `json:"output_complete"`
		} `json:"resource"`
	}
	if commandResponse.StatusCode != http.StatusOK || json.Unmarshal(commandData, &commandReadValue) != nil || commandReadValue.View != "projection" || commandReadValue.Resource.CommandState != "succeeded" || commandReadValue.Resource.Authority != "remote" || !commandReadValue.Resource.OutputComplete {
		t.Fatalf("command projection response status=%d body=%s decoded=%+v", commandResponse.StatusCode, commandData, commandReadValue)
	}
}

func p076APICapabilities() store.RemoteCapabilities {
	return store.RemoteCapabilities{HostClass: "linux-host", Isolation: string(domain.IsolationOSUser), EffectiveAccount: "ubuntu", ServiceLimits: map[string]any{"running_commands_per_host": 4}}
}
