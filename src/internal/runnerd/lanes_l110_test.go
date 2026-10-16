package runnerd

import (
	"context"
	"net/http"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneadmission"
)

func TestLanesL110DirectAndPrivateLifecycleMutationsFailClosed(t *testing.T) {
	runtime := &p109Runtime{generation: "l110-lifecycle-generation"}
	service, authority := newP046Service(t, runtime)
	directHandler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")

	create := func(key string) domain.SessionID {
		t.Helper()
		response := p107Do(directHandler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), key)
		if response.Code != http.StatusAccepted {
			t.Fatalf("fixture session create status=%d body=%s", response.Code, response.Body.String())
		}
		var session directSessionAcceptance
		p107Decode(t, response, &session)
		return domain.SessionID(session.SessionID)
	}
	directSession := create("l110-direct-create")
	privateSession := create("l110-private-create")
	directCommand := p109AcceptQueuedCommand(t, service, directSession, controller, "cmd-l110-direct")
	privateCommand := p109AcceptQueuedCommand(t, service, privateSession, controller, "cmd-l110-private")
	if err := authority.ConfigureLaneAdmission(context.Background(), laneadmission.LaneDev, laneadmission.HostLinux); err != nil {
		t.Fatal(err)
	}

	directCancel := p107Do(directHandler, controller, true, http.MethodPost, "/v1/commands/"+string(directCommand)+"/cancel", nil, "l110-direct-cancel")
	if directCancel.Code != http.StatusLocked {
		t.Fatalf("direct cancel status=%d body=%s", directCancel.Code, directCancel.Body.String())
	}
	var directErr directAPIError
	p107Decode(t, directCancel, &directErr)
	if directErr.Code != "lane_admission_closed" {
		t.Fatalf("direct cancel error=%+v", directErr)
	}
	directClose := p107Do(directHandler, controller, true, http.MethodDelete, "/v1/sessions/"+string(directSession), nil, "l110-direct-close")
	if directClose.Code != http.StatusLocked {
		t.Fatalf("direct close status=%d body=%s", directClose.Code, directClose.Body.String())
	}

	privateServer, serveErr := p048StartServer(t, service)
	client := p046UnixClient(privateServer.SocketPath())
	defer p048CloseServer(t, privateServer, serveErr)
	privateCancelBody := []byte(`{"idempotency_key":"l110-private-cancel","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
	privateCancel := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/commands/"+string(privateCommand)+"/cancel", privateCancelBody)
	if privateCancel.StatusCode != http.StatusLocked {
		t.Fatalf("private cancel status=%d body=%s", privateCancel.StatusCode, p046ReadBody(t, privateCancel))
	}
	privateCloseBody := []byte(`{"idempotency_key":"l110-private-close","policy":"graceful","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
	privateClose := p046DoJSON(t, client, http.MethodDelete, "http://runnerd/internal/v1/sessions/"+string(privateSession), privateCloseBody)
	if privateClose.StatusCode != http.StatusLocked {
		t.Fatalf("private close status=%d body=%s", privateClose.StatusCode, p046ReadBody(t, privateClose))
	}

	for _, commandID := range []domain.CommandID{directCommand, privateCommand} {
		command, err := authority.GetCommand(context.Background(), commandID)
		if err != nil {
			t.Fatal(err)
		}
		if command.State != domain.CommandStateQueued {
			t.Errorf("rejected cancel changed command %s to %s", commandID, command.State)
		}
	}
	for _, sessionID := range []domain.SessionID{directSession, privateSession} {
		session, err := authority.GetSession(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if session.State != domain.SessionStateReady {
			t.Errorf("rejected close changed session %s to %s", sessionID, session.State)
		}
	}
}
