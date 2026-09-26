package sshbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP050ForwardCreateReadSessionAndStableRetry(t *testing.T) {
	controller := p049Controller(t)
	var mu sync.Mutex
	var createBodies [][]byte
	var readQueries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/internal/v1/sessions" {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read create body: %v", err)
			}
			mu.Lock()
			createBodies = append(createBodies, body)
			mu.Unlock()
			var input map[string]any
			if err := json.Unmarshal(body, &input); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if input["session_id"] != "session-1" || input["idempotency_key"] != "create-key" {
				t.Errorf("stable create identity = %#v", input)
			}
			controllerBody, ok := input["controller"].(map[string]any)
			if !ok || controllerBody["controller_type"] != string(controller.Type()) || controllerBody["controller_id"] != string(controller.ID()) {
				t.Errorf("mapped controller = %#v", input["controller"])
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte(`{"session_id":"session-1","session_state":"ready","execution_target":{"kind":"remote","profile":"linux-host"}}`))
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/internal/v1/sessions/session-1" {
			mu.Lock()
			readQueries = append(readQueries, request.URL.Query())
			mu.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"session_id":"session-1","session_state":"ready"}`))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	create := RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "exchange-1", Operation: OperationCreateOrResumeSession,
		ResourceID: "session-1", IdempotencyKey: "create-key",
		Payload: json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"}}`),
	}
	first, err := forwarder.Handle(context.Background(), controller, create)
	if err != nil {
		t.Fatal(err)
	}
	second, err := forwarder.Handle(context.Background(), controller, create)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResponseType != "result" || second.ResponseType != "result" || string(first.Payload) != string(second.Payload) {
		t.Fatalf("create replies = %+v, %+v", first, second)
	}
	mu.Lock()
	if len(createBodies) != 2 || string(createBodies[0]) != string(createBodies[1]) {
		t.Fatalf("retry bodies = %q and %q", createBodies[0], createBodies[1])
	}
	mu.Unlock()

	read := RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "exchange-2", Operation: OperationGetSession,
		Payload: json.RawMessage(`{"session_id":"session-1"}`),
	}
	readReply, err := forwarder.Handle(context.Background(), controller, read)
	if err != nil {
		t.Fatal(err)
	}
	if readReply.ResponseType != "result" || !strings.Contains(string(readReply.Payload), `"session_state":"ready"`) {
		t.Fatalf("read reply = %+v", readReply)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(readQueries) != 1 || readQueries[0].Get("controller_type") != string(controller.Type()) || readQueries[0].Get("controller_id") != string(controller.ID()) {
		t.Fatalf("read controller query = %#v", readQueries)
	}
}

func TestP050ForwardMapsPrivateErrorsAndTransportUncertainty(t *testing.T) {
	controller := p049Controller(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":"session controller mismatch"}`))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := forwarder.Handle(context.Background(), controller, RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "read-1", Operation: OperationGetSession,
		Payload: json.RawMessage(`{"session_id":"session-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var errorPayload ErrorPayload
	if err := json.Unmarshal(reply.Payload, &errorPayload); err != nil {
		t.Fatal(err)
	}
	if reply.ResponseType != "error" || errorPayload.Code != "controller_mismatch" || errorPayload.Retryable {
		t.Fatalf("private error reply = %+v", reply)
	}

	transportError := errors.New("connection reset")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, transportError })}
	forwarder, err = NewRunnerdForwarder(ForwarderOptions{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	reply, err = forwarder.Handle(context.Background(), controller, RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "read-2", Operation: OperationGetSession,
		Payload: json.RawMessage(`{"session_id":"session-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ResponseType != "error" || !strings.Contains(string(reply.Payload), "transport_uncertain") {
		t.Fatalf("transport reply = %+v", reply)
	}
}

func TestP050ForwarderRejectsMalformedPayloadAndUnsafeConfiguration(t *testing.T) {
	if _, err := NewRunnerdForwarder(ForwarderOptions{Client: http.DefaultClient, BaseURL: "https://runnerd"}); !errors.Is(err, ErrForwarderConfiguration) {
		t.Fatalf("HTTPS base URL error = %v", err)
	}
	if _, err := NewUnixSocketHTTPClient("relative.sock"); !errors.Is(err, ErrForwarderConfiguration) {
		t.Fatalf("relative socket error = %v", err)
	}
	if _, err := NewRunnerdForwarder(ForwarderOptions{Client: http.DefaultClient, MaxResponseBytes: domain.MaxSerializedRequestBytes + 1}); !errors.Is(err, ErrForwarderConfiguration) {
		t.Fatalf("oversized response limit error = %v", err)
	}
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = forwarder.Handle(context.Background(), p049Controller(t), RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "bad", Operation: OperationCreateOrResumeSession,
		ResourceID: "session-1", IdempotencyKey: "key-1", Payload: json.RawMessage(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"unexpected":true}`),
	})
	if !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("malformed payload error = %v", err)
	}
}

func TestP050UnixSocketClientForwardsOnlyToPrivateSocket(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "r50-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	socketPath := tempDir + "/runnerd.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/v1/sessions/session-1" || request.URL.Query().Get("controller_type") == "" {
			t.Errorf("unexpected private request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		_, _ = writer.Write([]byte(`{"session_id":"session-1","session_state":"ready"}`))
	})}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("private test server: %v", err)
		}
	})
	client, err := NewUnixSocketHTTPClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := forwarder.Handle(context.Background(), p049Controller(t), RequestFrame{
		ProtocolVersion: ProtocolVersion, RequestID: "unix-read", Operation: OperationGetSession,
		Payload: json.RawMessage(`{"session_id":"session-1"}`),
	})
	if err != nil || reply.ResponseType != "result" {
		t.Fatalf("Unix private reply = %+v, %v", reply, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
