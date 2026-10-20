package sshbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
)

func TestQueuedMaintenanceProbeForwardsOnlyFixedBoundedRequest(t *testing.T) {
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "mac-router")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != "/internal/v1/jobs" {
			t.Errorf("queued probe route = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get(laneinstall.CatalogMaintenanceProbeTokenHeader) != token {
			t.Errorf("maintenance token was not forwarded")
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read forwarded job: %v", readErr)
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal(body, &input); err != nil {
			t.Errorf("decode forwarded job: %v", err)
		}
		var environment, script string
		_ = json.Unmarshal(input["environment"], &environment)
		_ = json.Unmarshal(input["script"], &script)
		if environment != "linux-host-dev" || script != "printf 'catalog release maintenance probe\\n'" {
			t.Errorf("forwarded command changed: environment=%q script=%q", environment, script)
		}
		semanticBody, err := json.Marshal(catalogProbeSemanticRequest{
			Environment: "linux-host-dev", ExecutionTarget: bridgeTarget{Kind: "remote", Profile: "linux-host"},
			Source: struct {
				Mode string `json:"mode"`
			}{Mode: "empty"}, Script: "printf 'catalog release maintenance probe\\n'",
			Limits: map[string]int{"command_timeout_seconds": 5},
		})
		if err != nil {
			t.Errorf("encode expected queued probe digest body: %v", err)
		} else {
			digest := sha256.Sum256(semanticBody)
			if got, want := request.Header.Get(laneinstall.CatalogMaintenanceProbeDigestHeader), hex.EncodeToString(digest[:]); got != want {
				t.Errorf("semantic request digest=%s, want exact queued scope digest %s", got, want)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"job_id":"job-0123456789abcdef0123456789abcdef","session_id":"sess-0123456789abcdef0123456789abcdef","command_id":"cmd-0123456789abcdef0123456789abcdef","job_phase":"complete","command_state":"succeeded","exit_code":0,"output_complete":true,"teardown_state":"closed"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	frame := RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "catalog-probe-queued", Operation: OperationRunOrResumeJob,
		ResourceID: "job-0123456789abcdef0123456789abcdef", IdempotencyKey: "catalog-probe-op-linux-host-queued-ssh",
		MaintenanceProbeToken: token,
		Payload:               json.RawMessage(`{"environment":"linux-host-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf 'catalog release maintenance probe\\n'","limits":{"command_timeout_seconds":5}}`),
	}
	reply, err := forwarder.Handle(context.Background(), controller, frame)
	if err != nil || reply.ResponseType != "result" {
		t.Fatalf("queued maintenance probe reply=%+v err=%v", reply, err)
	}
	if calls != 1 {
		t.Fatalf("forwarded calls=%d, want one", calls)
	}

	frame.Payload = json.RawMessage(`{"environment":"linux-host-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"echo arbitrary","limits":{"command_timeout_seconds":5}}`)
	if _, err := forwarder.Handle(context.Background(), controller, frame); err == nil {
		t.Fatal("non-fixed command was accepted as a maintenance probe")
	}
	if calls != 1 {
		t.Fatalf("invalid probe reached runnerd; calls=%d", calls)
	}
}
