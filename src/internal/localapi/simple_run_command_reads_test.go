package localapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
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
