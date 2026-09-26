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

func TestP066CancelRecordsKeyedLocalIntentForBothTargets(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		body   string
		target string
	}{
		{name: "local", body: `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, target: "local"},
		{name: "queued remote", body: `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, target: "remote"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, authority, db, client := p063Server(t)
			sessionID := p064CreateSession(t, client, fixture.body, "p066-cancel-session-"+fixture.name)
			commandID := p066SubmitCommand(t, client, sessionID, "p066-cancel-command-"+fixture.name)
			request, err := http.NewRequest(http.MethodPost, "http://local/v1/commands/"+commandID+"/cancel", strings.NewReader(`{"command_id":"`+commandID+`","reason":"user_requested"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Idempotency-Key", "p066-cancel-key")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("cancel status = %d, body=%s", response.StatusCode, data)
			}
			var accepted commandAcceptance
			if err := json.Unmarshal(data, &accepted); err != nil {
				t.Fatal(err)
			}
			if accepted.ResourceID != commandID || accepted.CommandID != commandID || accepted.SessionID != sessionID || accepted.IntentID == "" || accepted.AcceptanceScope != "local_intent" || accepted.ExecutionTarget.Kind != fixture.target || accepted.KnownState.DeliveryState != "recorded" {
				t.Fatalf("cancel acceptance = %+v", accepted)
			}
			intent, err := authority.GetLocalIntentByResource(context.Background(), "cancel_command", commandID, p063Owner(t))
			if err != nil {
				t.Fatal(err)
			}
			if intent.CommandID != domain.CommandID(commandID) || intent.SessionID != domain.SessionID(sessionID) || intent.Target.Kind() != domain.TargetKind(fixture.target) || string(intent.ScriptBytes) != "" || !strings.Contains(string(intent.PayloadJSON), "user_requested") {
				t.Fatalf("cancel intent = %+v", intent)
			}
			var authoritySessions, authorityCommands int
			if err := db.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&authoritySessions); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM exec_commands").Scan(&authorityCommands); err != nil {
				t.Fatal(err)
			}
			if authoritySessions != 0 || authorityCommands != 0 {
				t.Fatalf("cancel ingress created authority state: sessions=%d commands=%d", authoritySessions, authorityCommands)
			}

			retry, err := http.NewRequest(http.MethodPost, "http://local/v1/commands/"+commandID+"/cancel", strings.NewReader(`{"reason":"user_requested"}`))
			if err != nil {
				t.Fatal(err)
			}
			retry.Header.Set("Idempotency-Key", "p066-cancel-key")
			retryResponse, err := client.Do(retry)
			if err != nil {
				t.Fatal(err)
			}
			retryData, _ := io.ReadAll(retryResponse.Body)
			retryResponse.Body.Close()
			var retryAccepted commandAcceptance
			if retryResponse.StatusCode != http.StatusAccepted || json.Unmarshal(retryData, &retryAccepted) != nil || retryAccepted.IntentID != accepted.IntentID {
				t.Fatalf("cancel retry status/body = %d/%s", retryResponse.StatusCode, retryData)
			}

			conflict, err := http.NewRequest(http.MethodPost, "http://local/v1/commands/"+commandID+"/cancel", strings.NewReader(`{"reason":"changed"}`))
			if err != nil {
				t.Fatal(err)
			}
			conflict.Header.Set("Idempotency-Key", "p066-cancel-key")
			conflictResponse, err := client.Do(conflict)
			if err != nil {
				t.Fatal(err)
			}
			defer conflictResponse.Body.Close()
			if conflictResponse.StatusCode != http.StatusConflict {
				body, _ := io.ReadAll(conflictResponse.Body)
				t.Fatalf("changed cancel status = %d, body=%s", conflictResponse.StatusCode, body)
			}
		})
	}
}

func TestP066CloseRecordsPolicyIntentAndChangedPolicyConflicts(t *testing.T) {
	_, authority, db, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`, "p066-close-session")

	request, err := http.NewRequest(http.MethodDelete, "http://local/v1/sessions/"+sessionID+"?mode=cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p066-close-key")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("close status = %d, body=%s", response.StatusCode, data)
	}
	var accepted closeAcceptance
	if err := json.Unmarshal(data, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ResourceID != sessionID || accepted.SessionID != sessionID || accepted.IntentID == "" || accepted.AcceptanceScope != "local_intent" || accepted.ExecutionTarget.Kind != "local" || accepted.KnownState.DeliveryState != "recorded" {
		t.Fatalf("close acceptance = %+v", accepted)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "close_session", sessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(intent.PayloadJSON), `"policy":"cancel"`) || intent.SessionID != domain.SessionID(sessionID) {
		t.Fatalf("close intent = %+v", intent)
	}
	var authoritySessions int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&authoritySessions); err != nil {
		t.Fatal(err)
	}
	if authoritySessions != 0 {
		t.Fatalf("close ingress created %d authority sessions", authoritySessions)
	}

	conflict, err := http.NewRequest(http.MethodDelete, "http://local/v1/sessions/"+sessionID+"?mode=drain", nil)
	if err != nil {
		t.Fatal(err)
	}
	conflict.Header.Set("Idempotency-Key", "p066-close-key")
	conflictResponse, err := client.Do(conflict)
	if err != nil {
		t.Fatal(err)
	}
	defer conflictResponse.Body.Close()
	if conflictResponse.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(conflictResponse.Body)
		t.Fatalf("changed close policy status = %d, body=%s", conflictResponse.StatusCode, body)
	}
}

func TestP066CloseBodyPolicyAndForgedResourceAreValidatedBeforeInsert(t *testing.T) {
	_, authority, db, client := p063Server(t)
	sessionID := p064CreateSession(t, client, `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, "p066-body-session")
	unknownField, err := http.NewRequest(http.MethodDelete, "http://local/v1/sessions/"+sessionID, strings.NewReader(`{"script":"forged"}`))
	if err != nil {
		t.Fatal(err)
	}
	unknownField.Header.Set("Idempotency-Key", "p066-body-invalid")
	invalidResponse, err := client.Do(unknownField)
	if err != nil {
		t.Fatal(err)
	}
	if invalidResponse.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(invalidResponse.Body)
		invalidResponse.Body.Close()
		t.Fatalf("forged close status = %d, body=%s", invalidResponse.StatusCode, body)
	}
	invalidResponse.Body.Close()
	valid, err := http.NewRequest(http.MethodDelete, "http://local/v1/sessions/"+sessionID, strings.NewReader(`{"session_id":"`+sessionID+`","policy":"drain"}`))
	if err != nil {
		t.Fatal(err)
	}
	valid.Header.Set("Idempotency-Key", "p066-body-valid")
	validResponse, err := client.Do(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer validResponse.Body.Close()
	if validResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(validResponse.Body)
		t.Fatalf("body close status = %d, body=%s", validResponse.StatusCode, body)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "close_session", sessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(intent.PayloadJSON), `"policy":"drain"`) || intent.Target.Kind() != domain.TargetKindRemote {
		t.Fatalf("body close intent = %+v", intent)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents WHERE operation = 'close_session'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("close intent count = %d, want 1", count)
	}
}

func TestP066DirectCreatedResourceIsNotVisibleToCancelOrCloseIngress(t *testing.T) {
	_, authority, db, client := p063Server(t)
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller := p063Owner(t)
	limits := domain.DefaultServiceLimits()
	if _, err := authority.CreateSession(context.Background(), store.SessionCreate{SessionID: "p066-direct-session", Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), Limits: domain.EffectiveSessionLimits{CommandTimeout: limits.CommandTimeout, IdleTimeout: limits.IdleTimeout, SessionMaxLifetime: limits.SessionMaxLifetime, OutputBytesPerCommand: limits.OutputBytesPerCommand}}); err != nil {
		t.Fatal(err)
	}
	closeRequest, err := http.NewRequest(http.MethodDelete, "http://local/v1/sessions/p066-direct-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	closeRequest.Header.Set("Idempotency-Key", "p066-direct-close")
	closeResponse, err := client.Do(closeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if closeResponse.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(closeResponse.Body)
		closeResponse.Body.Close()
		t.Fatalf("direct close status = %d, body=%s", closeResponse.StatusCode, body)
	}
	closeResponse.Body.Close()
	var intents int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("direct resource created %d local intents", intents)
	}
}

func p066SubmitCommand(t *testing.T, client *http.Client, sessionID, key string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions/"+sessionID+"/commands", strings.NewReader(`{"script":"printf p066"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("submit setup status = %d, body=%s", response.StatusCode, data)
	}
	var accepted struct {
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(data, &accepted); err != nil || accepted.CommandID == "" {
		t.Fatalf("submit setup response = %s", data)
	}
	return accepted.CommandID
}
