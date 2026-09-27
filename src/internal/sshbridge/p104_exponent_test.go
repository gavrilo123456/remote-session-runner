package sshbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestP104ForwardSubmitAcceptsCanonicalExponentTimeout(t *testing.T) {
	var forwardedTimeout json.Number
	forwardedRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwardedRequests++
		var input struct {
			TimeoutSeconds json.Number `json:"timeout_seconds"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil {
			t.Errorf("decode private submit request: %v", err)
		}
		forwardedTimeout = input.TimeoutSeconds
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"command_id":"command-1"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	controller := p049Controller(t)
	request := RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "p104-exponent-timeout",
		Operation: OperationSubmitOrResumeCommand, ResourceID: "command-1", IdempotencyKey: "key-1",
		Payload: json.RawMessage(`{"session_id":"session-1","intent_ordinal":1,"script":"printf hi","timeout_seconds":3e1}`),
	}
	reply, err := forwarder.Handle(context.Background(), controller, request)
	if err != nil || reply.ResponseType != "result" {
		t.Fatalf("exponent timeout reply=%+v err=%v", reply, err)
	}
	if forwardedTimeout != json.Number("30") || forwardedRequests != 1 {
		t.Fatalf("forwarded timeout=%q requests=%d, want 30 and one request", forwardedTimeout, forwardedRequests)
	}

	for _, timeout := range []string{`1.5`, `1e100`, `"30"`} {
		request.RequestID = "p104-invalid-timeout"
		request.Payload = json.RawMessage(`{"session_id":"session-1","intent_ordinal":1,"script":"printf hi","timeout_seconds":` + timeout + `}`)
		if _, err := forwarder.Handle(context.Background(), controller, request); err == nil || !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("timeout %s error=%v, want ErrInvalidFrame", timeout, err)
		}
	}
	if forwardedRequests != 1 {
		t.Fatalf("invalid timeout values reached private API: requests=%d", forwardedRequests)
	}
}
