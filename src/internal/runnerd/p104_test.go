package runnerd

import (
	"net/http"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP104GetCommandIncludesRemoteProjectionContext(t *testing.T) {
	service, authority := newP046Service(t, &p046FakeRuntime{generation: "p104-fake-generation"})
	server, serveErr := p048StartServer(t, service)
	client := p046UnixClient(server.SocketPath())
	defer p048CloseServer(t, server, serveErr)

	createBody := []byte(`{"session_id":"p104-session","idempotency_key":"p104-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	created := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("P104 create status=%d body=%s", created.StatusCode, p046ReadBody(t, created))
	}
	_ = p046ReadBody(t, created)
	p048QueueCommand(t, authority, "p104-session", "p104-command", "p104-command-key")

	read := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/commands/p104-command?controller_type=queued_mac&controller_id=tomasz.walczuk", nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("P104 get-command status=%d body=%s", read.StatusCode, p046ReadBody(t, read))
	}
	var result commandReadResponse
	p046DecodeJSON(t, read, &result)
	if result.CommandID != "p104-command" || result.SessionID != "p104-session" || result.ExecutionTarget.Profile != "linux-host" || result.Authority != "remote" || result.Controller.Type != "queued_mac" || result.Environment != "linux-dev" || result.ObservedAt.IsZero() {
		t.Fatalf("P104 command read context=%+v", result)
	}
	if result.Capabilities.HostClass != "Ubuntu Linux host" || result.Capabilities.Isolation != "os-user" || result.Capabilities.EffectiveAccount != "ubuntu" || result.Capabilities.ServiceLimits["running_commands"] != float64(4) {
		t.Fatalf("P104 command read capabilities=%+v", result.Capabilities)
	}
	if result.Source.Mode != "empty" || result.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("P104 command read source/state=%+v", result)
	}
}
