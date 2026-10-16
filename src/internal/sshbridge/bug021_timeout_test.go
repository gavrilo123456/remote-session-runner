package sshbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBUG021ForwardRunNormalizesCanonicalIntegerLimits(t *testing.T) {
	var forwarded struct {
		Limits map[string]int64 `json:"limits"`
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodPost || request.URL.Path != "/internal/v1/jobs" {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&forwarded); err != nil {
			t.Errorf("decode private run request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"job_id":"job-bug021","session_id":"sess-bug021","command_id":"cmd-bug021","job_phase":"accepted","teardown_state":"pending"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	frame := RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "req-bug021-forward", Operation: OperationRunOrResumeJob,
		ResourceID: "job-bug021", IdempotencyKey: "key-bug021",
		Payload: json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"printf bounded","limits":{"command_timeout_seconds":5e0}}`),
	}
	reply, err := forwarder.Handle(context.Background(), p049Controller(t), frame)
	if err != nil || reply.ResponseType != "result" {
		t.Fatalf("run reply=%+v err=%v", reply, err)
	}
	if requests != 1 || forwarded.Limits["command_timeout_seconds"] != 5 {
		t.Fatalf("forwarded limits=%v requests=%d; want integer timeout 5 and one request", forwarded.Limits, requests)
	}
	for _, invalid := range []string{`1.5`, `-1`, `"5"`} {
		frame.RequestID = "req-bug021-invalid-limit"
		frame.Payload = json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"printf invalid","limits":{"command_timeout_seconds":` + invalid + `}}`)
		if _, err := forwarder.Handle(context.Background(), p049Controller(t), frame); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("invalid timeout %s error=%v, want ErrInvalidFrame", invalid, err)
		}
	}
	if requests != 1 {
		t.Fatalf("invalid run limits reached private API: requests=%d", requests)
	}
}
