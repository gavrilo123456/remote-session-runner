package runnerlocald

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestP061PrivateReadAndEventsUseStableOwnerIDs(t *testing.T) {
	authority, service := newP060Service(t)
	socketPath := p060SocketPath(t)
	server, serveErr := p060StartServer(t, authority, service, socketPath)
	defer func() {
		if err := server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
	}()

	session := p061CreateSession(t, service, "session-p061-read")
	command := p061SubmitCommand(t, service, session.SessionID, "command-p061-read", 1, "printf p061")
	client := p060UnixClient(socketPath)

	sessionResponse := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/sessions/"+string(session.SessionID), nil)
	if sessionResponse.StatusCode != http.StatusOK {
		t.Fatalf("session read status = %d body=%s", sessionResponse.StatusCode, p060ReadBody(t, sessionResponse))
	}
	var sessionView localSessionResponse
	p060DecodeJSON(t, sessionResponse, &sessionView)
	if sessionView.SessionID != string(session.SessionID) || sessionView.SessionState != string(domain.SessionStateReady) || sessionView.Authority != "local" {
		t.Fatalf("session view = %+v", sessionView)
	}

	commandResponse := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/commands/"+string(command.CommandID), nil)
	if commandResponse.StatusCode != http.StatusOK {
		t.Fatalf("command read status = %d body=%s", commandResponse.StatusCode, p060ReadBody(t, commandResponse))
	}
	var commandView localCommandResponse
	p060DecodeJSON(t, commandResponse, &commandView)
	if commandView.CommandID != string(command.CommandID) || commandView.SessionID != string(session.SessionID) || commandView.CommandState != string(domain.CommandStateSucceeded) {
		t.Fatalf("command view = %+v", commandView)
	}

	eventsResponse := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/commands/"+string(command.CommandID)+"/events?after=0", nil)
	if eventsResponse.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d body=%s", eventsResponse.StatusCode, p060ReadBody(t, eventsResponse))
	}
	eventsBody := p060ReadBody(t, eventsResponse)
	lines := nonEmptyLines(eventsBody)
	if len(lines) < 4 {
		t.Fatalf("events = %q, want queued/start/output/terminal", eventsBody)
	}
	var outputEvent localCommandEventResponse
	for _, line := range lines {
		var event localCommandEventResponse
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "stdout" {
			outputEvent = event
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(outputEvent.DataBase64)
	if err != nil || string(decoded) != "p060-output\n" {
		t.Fatalf("stdout event = %+v decoded=%q err=%v", outputEvent, decoded, err)
	}

	followResponse := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/commands/"+string(command.CommandID)+"/events?after=0&follow=true", nil)
	if followResponse.StatusCode != http.StatusOK {
		t.Fatalf("follow status = %d body=%s", followResponse.StatusCode, p060ReadBody(t, followResponse))
	}
	if got := len(nonEmptyLines(p060ReadBody(t, followResponse))); got < 4 {
		t.Fatalf("follow event count = %d, want at least 4", got)
	}

	controllerQuery := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/sessions/"+string(session.SessionID)+"?controller_type=direct_mtls&controller_id=attacker", nil)
	if controllerQuery.StatusCode != http.StatusBadRequest {
		t.Fatalf("controller query status = %d body=%s", controllerQuery.StatusCode, p060ReadBody(t, controllerQuery))
	}

	directController, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "attacker")
	if err != nil {
		t.Fatal(err)
	}
	directTarget, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	directHash, err := localMutationHash("create_session", map[string]string{"environment": "mac-dev", "session_id": "session-p061-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptSessionCreate(context.Background(), store.SessionCreateAcceptance{SessionCreate: store.SessionCreate{SessionID: "session-p061-owner", Target: directTarget, Environment: "mac-dev", Controller: directController, Source: domain.NewEmptySource(), Limits: domain.EffectiveSessionLimits{CommandTimeout: 30 * time.Minute, IdleTimeout: 30 * time.Minute, SessionMaxLifetime: 4 * time.Hour, OutputBytesPerCommand: 100 << 20}}, IdempotencyKey: "owner-negative", RequestHash: directHash}); err != nil {
		t.Fatal(err)
	}
	wrongOwner := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/sessions/session-p061-owner", nil)
	if wrongOwner.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong owner status = %d body=%s", wrongOwner.StatusCode, p060ReadBody(t, wrongOwner))
	}
}

func TestP061PrivateCancelCloseAndForgedPayloadRules(t *testing.T) {
	authority, service := newP060Service(t)
	socketPath := p060SocketPath(t)
	server, serveErr := p060StartServer(t, authority, service, socketPath)
	defer func() {
		if err := server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
	}()

	session := p061CreateSession(t, service, "session-p061-mutate")
	command := p061SubmitCommand(t, service, session.SessionID, "command-p061-mutate", 1, "printf mutate")
	client := p060UnixClient(socketPath)
	commandPath := "http://locald/internal/v1/commands/" + string(command.CommandID)

	forged := p060DoJSON(t, client, http.MethodPost, commandPath+"/cancel", []byte(`{"idempotency_key":"cancel-p061","script":"forged"}`))
	if forged.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged cancel status = %d body=%s", forged.StatusCode, p060ReadBody(t, forged))
	}
	cancelBody := []byte(`{"idempotency_key":"cancel-p061"}`)
	cancel := p060DoJSON(t, client, http.MethodPost, commandPath+"/cancel", cancelBody)
	if cancel.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d body=%s", cancel.StatusCode, p060ReadBody(t, cancel))
	}
	duplicate := p060DoJSON(t, client, http.MethodPost, commandPath+"/cancel", cancelBody)
	if duplicate.StatusCode != http.StatusAccepted {
		t.Fatalf("duplicate cancel status = %d body=%s", duplicate.StatusCode, p060ReadBody(t, duplicate))
	}
	var duplicateView localCommandResponse
	p060DecodeJSON(t, duplicate, &duplicateView)
	if !duplicateView.Duplicate {
		t.Fatalf("duplicate cancel response = %+v", duplicateView)
	}

	closeBody := []byte(`{"idempotency_key":"close-p061","policy":"graceful"}`)
	closed := p060DoJSON(t, client, http.MethodDelete, "http://locald/internal/v1/sessions/"+string(session.SessionID), closeBody)
	if closed.StatusCode != http.StatusAccepted {
		t.Fatalf("close status = %d body=%s", closed.StatusCode, p060ReadBody(t, closed))
	}
	var closedView localSessionResponse
	p060DecodeJSON(t, closed, &closedView)
	if closedView.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("closed response = %+v", closedView)
	}
	changedPolicy := p060DoJSON(t, client, http.MethodDelete, "http://locald/internal/v1/sessions/"+string(session.SessionID), []byte(`{"idempotency_key":"close-p061","policy":"drain"}`))
	if changedPolicy.StatusCode != http.StatusConflict {
		t.Fatalf("changed close policy status = %d body=%s", changedPolicy.StatusCode, p060ReadBody(t, changedPolicy))
	}

	missingID := p060DoJSON(t, client, http.MethodGet, "http://locald/internal/v1/commands/missing-command", nil)
	if missingID.StatusCode != http.StatusNotFound {
		t.Fatalf("missing command status = %d body=%s", missingID.StatusCode, p060ReadBody(t, missingID))
	}
	unknownClose := p060DoJSON(t, client, http.MethodDelete, "http://locald/internal/v1/sessions/"+string(session.SessionID), []byte(`{"idempotency_key":"close-unknown","script":"forged"}`))
	if unknownClose.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged close status = %d body=%s", unknownClose.StatusCode, p060ReadBody(t, unknownClose))
	}
}

func p061CreateSession(t *testing.T, service *execution.Service, id string) store.SessionRecord {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"operation":"create_session","session_id":%q}`, id))
	hash, err := domain.HashMutationRequestJSON("create_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateSession(context.Background(), execution.CreateSessionRequest{SessionID: domain.SessionID(id), IdempotencyKey: "create-" + id, RequestHash: hash, Environment: "mac-dev", Target: target, Controller: controller, Source: domain.NewEmptySource()})
	if err != nil {
		t.Fatal(err)
	}
	return result.Session
}

func p061SubmitCommand(t *testing.T, service *execution.Service, sessionID domain.SessionID, id string, ordinal int64, script string) store.CommandRecord {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"command_id":%q,"script":%q}`, sessionID, id, script))
	hash, err := domain.HashMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitCommand(context.Background(), execution.SubmitCommandRequest{CommandID: domain.CommandID(id), SessionID: sessionID, Controller: controller, IdempotencyKey: "submit-" + id, RequestHash: hash, Script: script, IntentOrdinal: ordinal})
	if err != nil {
		t.Fatal(err)
	}
	return result.Command
}

func nonEmptyLines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
