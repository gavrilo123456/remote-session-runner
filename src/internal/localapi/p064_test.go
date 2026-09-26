package localapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP064SubmitReadCommandInheritsSessionTargetAndController(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		session string
		target  string
	}{
		{name: "local", session: `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, target: "local"},
		{name: "queued remote", session: `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, target: "remote"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, authority, db, client := p063Server(t)
			sessionID := p064CreateSession(t, client, fixture.session, "p064-session-"+fixture.name)
			body := `{"script":"printf p064\n","timeout_seconds":7}`
			request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Idempotency-Key", "p064-command-"+fixture.name)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			responseBytes, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("submit status = %d, body = %s", response.StatusCode, responseBytes)
			}
			var accepted struct {
				ResourceID      string `json:"resource_id"`
				CommandID       string `json:"command_id"`
				SessionID       string `json:"session_id"`
				AcceptanceScope string `json:"acceptance_scope"`
				ExecutionTarget struct {
					Kind string `json:"kind"`
				} `json:"execution_target"`
				KnownState struct {
					DeliveryState string `json:"delivery_state"`
				} `json:"known_state"`
			}
			if err := json.Unmarshal(responseBytes, &accepted); err != nil {
				t.Fatal(err)
			}
			if accepted.ResourceID == "" || accepted.ResourceID != accepted.CommandID || accepted.SessionID != sessionID || accepted.AcceptanceScope != "local_intent" || accepted.ExecutionTarget.Kind != fixture.target || accepted.KnownState.DeliveryState != "recorded" {
				t.Fatalf("acceptance = %+v", accepted)
			}
			record, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, p063Owner(t))
			if err != nil {
				t.Fatal(err)
			}
			if record.Target.Kind() != domain.TargetKind(fixture.target) || string(record.SessionID) != sessionID || string(record.ScriptBytes) != "printf p064\n" || record.IntentOrdinal == nil || *record.IntentOrdinal != 1 {
				t.Fatalf("command intent = %+v", record)
			}
			getResponse, err := client.Get("http://local/v1/commands/" + accepted.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			getBytes, _ := io.ReadAll(getResponse.Body)
			getResponse.Body.Close()
			if getResponse.StatusCode != http.StatusOK {
				t.Fatalf("read status = %d, body = %s", getResponse.StatusCode, getBytes)
			}
			var read struct {
				View     string `json:"view"`
				IsStale  bool   `json:"is_stale"`
				Resource struct {
					CommandID     string `json:"command_id"`
					SessionID     string `json:"session_id"`
					DeliveryState string `json:"delivery_state"`
					Target        struct {
						Kind string `json:"kind"`
					} `json:"execution_target"`
				} `json:"resource"`
			}
			if err := json.Unmarshal(getBytes, &read); err != nil {
				t.Fatal(err)
			}
			if read.View != "local_intent" || read.IsStale || read.Resource.CommandID != accepted.CommandID || read.Resource.SessionID != sessionID || read.Resource.DeliveryState != "recorded" || read.Resource.Target.Kind != fixture.target {
				t.Fatalf("read = %+v", read)
			}
			var authorityCommands int
			if err := db.QueryRow("SELECT COUNT(*) FROM exec_commands").Scan(&authorityCommands); err != nil {
				t.Fatal(err)
			}
			if authorityCommands != 0 {
				t.Fatalf("local API created %d authoritative commands", authorityCommands)
			}
			duplicateRequest, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			duplicateRequest.Header.Set("Idempotency-Key", "p064-command-"+fixture.name)
			duplicateResponse, err := client.Do(duplicateRequest)
			if err != nil {
				t.Fatal(err)
			}
			duplicateBytes, _ := io.ReadAll(duplicateResponse.Body)
			duplicateResponse.Body.Close()
			if duplicateResponse.StatusCode != http.StatusAccepted || !strings.Contains(string(duplicateBytes), accepted.CommandID) {
				t.Fatalf("duplicate status = %d, body = %s", duplicateResponse.StatusCode, duplicateBytes)
			}
			forgedRequest, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"different","execution_target":{"kind":"local","profile":"mac-workstation"}}`))
			if err != nil {
				t.Fatal(err)
			}
			forgedRequest.Header.Set("Idempotency-Key", "p064-forged")
			forgedResponse, err := client.Do(forgedRequest)
			if err != nil {
				t.Fatal(err)
			}
			defer forgedResponse.Body.Close()
			if forgedResponse.StatusCode != http.StatusBadRequest {
				data, _ := io.ReadAll(forgedResponse.Body)
				t.Fatalf("forged target status = %d, body = %s", forgedResponse.StatusCode, data)
			}
		})
	}
}

func TestP064OversizeScriptLeavesNoCommandIntent(t *testing.T) {
	_, _, db, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p064-oversize-session")
	body := `{"script":"` + strings.Repeat("x", domain.MaxScriptUTF8Bytes+1) + `"}`
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p064-oversize-command")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("oversize status = %d, body = %s", response.StatusCode, data)
	}
	var intents int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents WHERE operation = 'submit_command'").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("oversize script inserted %d command intents", intents)
	}
}

func TestP064DirectCreatedSessionIsNotVisibleToLocalCommandIngress(t *testing.T) {
	_, authority, db, client := p063Server(t)
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	limits := domain.DefaultServiceLimits()
	directSession := domain.SessionID("direct-p064-session")
	if _, err := authority.CreateSession(context.Background(), store.SessionCreate{
		SessionID: directSession, Target: target, Environment: "linux-dev", Controller: controller, Source: domain.NewEmptySource(),
		Limits: domain.EffectiveSessionLimits{CommandTimeout: limits.CommandTimeout, IdleTimeout: limits.IdleTimeout, SessionMaxLifetime: limits.SessionMaxLifetime, OutputBytesPerCommand: limits.OutputBytesPerCommand},
	}); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+string(directSession)+"/commands", strings.NewReader(`{"script":"echo denied"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p064-direct-denied")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("direct-created command status = %d, body = %s", response.StatusCode, data)
	}
	var intents int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents WHERE operation = 'submit_command'").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("direct-created command inserted %d local intents", intents)
	}
}

func p064CreateSession(t *testing.T, client *http.Client, body, key string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("session setup status = %d, body = %s", response.StatusCode, data)
	}
	var accepted struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &accepted); err != nil || accepted.SessionID == "" {
		t.Fatalf("session setup response = %s", data)
	}
	return accepted.SessionID
}
