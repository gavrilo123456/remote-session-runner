package sshbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestP053ForwardCancelAndCloseWithStableMutationIdentity(t *testing.T) {
	controller := p049Controller(t)
	var mu sync.Mutex
	var cancelBodies, closeBodies [][]byte
	var cancelPaths, closePaths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read mutation body: %v", err)
		}
		var input map[string]any
		if err := json.Unmarshal(body, &input); err != nil {
			t.Fatalf("decode mutation body: %v", err)
		}
		mapped, ok := input["controller"].(map[string]any)
		if !ok || mapped["controller_type"] != string(controller.Type()) || mapped["controller_id"] != string(controller.ID()) {
			t.Errorf("mapped controller = %#v", input["controller"])
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/internal/v1/commands/command-1/cancel":
			cancelBodies = append(cancelBodies, body)
			cancelPaths = append(cancelPaths, request.URL.Path)
			if input["command_id"] != "command-1" || input["idempotency_key"] != "cancel-key" || input["request_id"] != "cancel-exchange" {
				t.Errorf("cancel body = %#v", input)
			}
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte(`{"command_id":"command-1","command_state":"cancelled","output_complete":true}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/internal/v1/sessions/session-1":
			closeBodies = append(closeBodies, body)
			closePaths = append(closePaths, request.URL.Path)
			if input["session_id"] != "session-1" || input["idempotency_key"] != "close-key" || input["request_id"] != "close-exchange" || input["policy"] != "graceful" {
				t.Errorf("close body = %#v", input)
			}
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte(`{"session_id":"session-1","session_state":"closed"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cancel := RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "cancel-exchange", Operation: OperationCancelCommand, ResourceID: "command-1", IdempotencyKey: "cancel-key", Payload: json.RawMessage(`{"command_id":"command-1"}`)}
	for i := 0; i < 2; i++ {
		reply, err := forwarder.Handle(context.Background(), controller, cancel)
		if err != nil || reply.ResponseType != "result" || !strings.Contains(string(reply.Payload), `"command_state":"cancelled"`) {
			t.Fatalf("cancel reply = %+v, %v", reply, err)
		}
	}
	close := RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "close-exchange", Operation: OperationCloseSession, ResourceID: "session-1", IdempotencyKey: "close-key", Payload: json.RawMessage(`{"session_id":"session-1","policy":"graceful"}`)}
	for i := 0; i < 2; i++ {
		reply, err := forwarder.Handle(context.Background(), controller, close)
		if err != nil || reply.ResponseType != "result" || !strings.Contains(string(reply.Payload), `"session_state":"closed"`) {
			t.Fatalf("close reply = %+v, %v", reply, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cancelBodies) != 2 || string(cancelBodies[0]) != string(cancelBodies[1]) || len(cancelPaths) != 2 || cancelPaths[0] != cancelPaths[1] {
		t.Fatalf("cancel retry identity = %q, %q, %q", cancelBodies, cancelBodies, cancelPaths)
	}
	if len(closeBodies) != 2 || string(closeBodies[0]) != string(closeBodies[1]) || len(closePaths) != 2 || closePaths[0] != closePaths[1] {
		t.Fatalf("close retry identity = %q, %q, %q", closeBodies, closeBodies, closePaths)
	}
}

func TestP053CloseOmittedPolicyAndRejectsPayloadIdentityMismatch(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"session_id":"session-1","session_state":"closed"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	controller := p049Controller(t)
	reply, err := forwarder.Handle(context.Background(), controller, RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "close-default", Operation: OperationCloseSession, ResourceID: "session-1", IdempotencyKey: "key", Payload: json.RawMessage(`{"session_id":"session-1"}`)})
	if err != nil || reply.ResponseType != "result" {
		t.Fatalf("default-policy close = %+v, %v", reply, err)
	}
	var input map[string]any
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	if _, present := input["policy"]; present {
		t.Fatalf("omitted policy was synthesized by bridge: %#v", input)
	}
	for _, request := range []RequestFrame{
		{ProtocolVersion: ProtocolVersion, RequestID: "bad-cancel", Operation: OperationCancelCommand, ResourceID: "command-1", IdempotencyKey: "key", Payload: json.RawMessage(`{"command_id":"command-2"}`)},
		{ProtocolVersion: ProtocolVersion, RequestID: "bad-close", Operation: OperationCloseSession, ResourceID: "session-1", IdempotencyKey: "key", Payload: json.RawMessage(`{"session_id":"session-2"}`)},
	} {
		if _, err := forwarder.Handle(context.Background(), controller, request); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("identity mismatch error = %v", err)
		}
	}
}

func TestP053MapsCancelClosePrivateErrorsAndRejectsMalformedPayload(t *testing.T) {
	controller := p049Controller(t)
	for _, status := range []int{http.StatusForbidden, http.StatusConflict, http.StatusNotFound, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(`{"error":"mutation failed"}`))
		}))
		forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		reply, err := forwarder.Handle(context.Background(), controller, RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "negative", Operation: OperationCancelCommand, ResourceID: "command-1", IdempotencyKey: "key", Payload: json.RawMessage(`{"command_id":"command-1"}`)})
		server.Close()
		if err != nil || reply.ResponseType != "error" {
			t.Fatalf("private status %d reply = %+v, %v", status, reply, err)
		}
		var payload ErrorPayload
		if err := json.Unmarshal(reply.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		want := map[int]string{http.StatusForbidden: "controller_mismatch", http.StatusConflict: "idempotency_conflict", http.StatusNotFound: "resource_not_found", http.StatusServiceUnavailable: "runtime_unavailable"}[status]
		if payload.Code != want || (status == http.StatusServiceUnavailable) != payload.Retryable {
			t.Fatalf("status %d mapped payload = %+v", status, payload)
		}
	}
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []RequestFrame{
		{ProtocolVersion: ProtocolVersion, RequestID: "bad-cancel", Operation: OperationCancelCommand, ResourceID: "command-1", IdempotencyKey: "key", Payload: json.RawMessage(`{"command_id":"command-1","unexpected":true}`)},
		{ProtocolVersion: ProtocolVersion, RequestID: "bad-close", Operation: OperationCloseSession, ResourceID: "session-1", IdempotencyKey: "key", Payload: json.RawMessage(`{"session_id":"session-1","unexpected":true}`)},
	} {
		if _, err := forwarder.Handle(context.Background(), controller, request); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("malformed mutation error = %v", err)
		}
	}
}
