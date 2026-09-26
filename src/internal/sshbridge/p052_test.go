package sshbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP052ForwardRunReadJobAndStableRetry(t *testing.T) {
	controller := p049Controller(t)
	var mu sync.Mutex
	var runBodies [][]byte
	var jobQueries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/internal/v1/jobs" {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read run body: %v", err)
			}
			mu.Lock()
			runBodies = append(runBodies, body)
			mu.Unlock()
			var input map[string]any
			if err := json.Unmarshal(body, &input); err != nil {
				t.Fatalf("decode run body: %v", err)
			}
			if input["job_id"] != "job-1" || input["session_id"] != "job-session-1" || input["command_id"] != "job-command-1" || input["idempotency_key"] != "run-key" || input["request_id"] != "run-exchange-1" || input["environment"] != "linux-dev" || input["script"] != "printf job" {
				t.Errorf("stable run identity/script = %#v", input)
			}
			target, ok := input["execution_target"].(map[string]any)
			if !ok || target["kind"] != "remote" || target["profile"] != "linux-host" {
				t.Errorf("run target = %#v", input["execution_target"])
			}
			controllerBody, ok := input["controller"].(map[string]any)
			if !ok || controllerBody["controller_type"] != string(controller.Type()) || controllerBody["controller_id"] != string(controller.ID()) {
				t.Errorf("run controller = %#v", input["controller"])
			}
			if source, ok := input["source"].(map[string]any); !ok || source["mode"] != "empty" {
				t.Errorf("run source = %#v", input["source"])
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte(`{"job_id":"job-1","session_id":"job-session-1","command_id":"job-command-1","job_phase":"complete","command_state":"failed","exit_code":7,"output_complete":true,"output_truncated":false,"teardown_state":"closed"}`))
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/internal/v1/jobs/job-1" {
			mu.Lock()
			jobQueries = append(jobQueries, request.URL.Query())
			mu.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"job_id":"job-1","session_id":"job-session-1","command_id":"job-command-1","job_phase":"complete","command_state":"failed","exit_code":7,"output_complete":true,"output_truncated":false,"teardown_state":"closed"}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	run := RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "run-exchange-1", Operation: OperationRunOrResumeJob,
		ResourceID: "job-1", IdempotencyKey: "run-key",
		Payload: json.RawMessage(`{"session_id":"job-session-1","command_id":"job-command-1","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf job"}`),
	}
	first, err := forwarder.Handle(context.Background(), controller, run)
	if err != nil {
		t.Fatal(err)
	}
	second, err := forwarder.Handle(context.Background(), controller, run)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResponseType != "result" || second.ResponseType != "result" || !strings.Contains(string(first.Payload), `"command_state":"failed"`) || !strings.Contains(string(first.Payload), `"teardown_state":"closed"`) {
		t.Fatalf("run replies = %+v, %+v", first, second)
	}
	mu.Lock()
	if len(runBodies) != 2 || string(runBodies[0]) != string(runBodies[1]) {
		t.Fatalf("run retry bodies = %q and %q", runBodies[0], runBodies[1])
	}
	mu.Unlock()

	read, err := forwarder.Handle(context.Background(), controller, RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "job-read-1", Operation: OperationGetJob,
		Payload: json.RawMessage(`{"job_id":"job-1"}`),
	})
	if err != nil || read.ResponseType != "result" || !strings.Contains(string(read.Payload), `"job_id":"job-1"`) {
		t.Fatalf("read job reply = %+v, %v", read, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(jobQueries) != 1 || jobQueries[0].Get("controller_type") != string(controller.Type()) || jobQueries[0].Get("controller_id") != string(controller.ID()) {
		t.Fatalf("job controller query = %#v", jobQueries)
	}
}

func TestP052MinimalRunDerivesStableChildIDs(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read run body: %v", err)
		}
		bodies = append(bodies, body)
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"job_id":"job-min","session_id":"job-min-session","command_id":"job-min-command","job_phase":"accepted","teardown_state":"pending"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "minimal", Operation: OperationRunOrResumeJob, ResourceID: "job-min", IdempotencyKey: "key-min", Payload: json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"pwd"}`)}
	for i := 0; i < 2; i++ {
		if _, err := forwarder.Handle(context.Background(), p049Controller(t), request); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("derived run identities changed: %q and %q", bodies[0], bodies[1])
	}
	var body map[string]any
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["job_id"] != "job-min" || body["session_id"] != "job-min-session" || body["command_id"] != "job-min-command" {
		t.Fatalf("derived IDs = %#v", body)
	}
}

func TestP052RunRejectsOversizeScriptAndMalformedJobPayload(t *testing.T) {
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = forwarder.Handle(context.Background(), p049Controller(t), RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "oversize-run", Operation: OperationRunOrResumeJob,
		ResourceID: "job-1", IdempotencyKey: "key-1",
		Payload: json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"` + strings.Repeat("x", domain.MaxScriptUTF8Bytes+1) + `"}`),
	})
	if !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("oversize run script error = %v", err)
	}
	_, err = forwarder.Handle(context.Background(), p049Controller(t), RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "bad-job", Operation: OperationGetJob,
		Payload: json.RawMessage(`{"job_id":"job-1","unexpected":true}`),
	})
	if !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("malformed get-job payload error = %v", err)
	}
}
