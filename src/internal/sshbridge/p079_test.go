package sshbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestP079RunPreservesDurableJobOutcomeOnPrivateTeardownError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/internal/v1/jobs" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"job_id":"job-p079","session_id":"sess-p079","command_id":"cmd-p079","job_phase":"lost","command_state":"succeeded","output_complete":true,"output_truncated":false,"teardown_state":"lost","teardown_reason":"runtime_cleanup_unconfirmed"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	frame := RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "p079-run", Operation: OperationRunOrResumeJob, ResourceID: "job-p079", IdempotencyKey: "p079-key", Payload: json.RawMessage(`{"session_id":"sess-p079","command_id":"cmd-p079","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"echo p079"}`)}
	reply, err := forwarder.Handle(context.Background(), p049Controller(t), frame)
	if err != nil || reply.ResponseType != "result" || !strings.Contains(string(reply.Payload), `"teardown_state":"lost"`) || !strings.Contains(string(reply.Payload), `"command_state":"succeeded"`) {
		t.Fatalf("durable job outcome = %+v err=%v", reply, err)
	}
}
