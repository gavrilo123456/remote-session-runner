package sshbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func p049Controller(t *testing.T) domain.ControllerIdentity {
	t.Helper()
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("bridge-client"))
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func p049Frame(operation Operation, requestID string, payload string) string {
	frame := RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, Operation: operation, Payload: json.RawMessage(payload)}
	switch operation {
	case OperationCreateOrResumeSession, OperationSubmitOrResumeCommand, OperationCancelCommand, OperationCloseSession, OperationRunOrResumeJob:
		frame.ResourceID = "resource-1"
		frame.IdempotencyKey = "key-1"
	}
	data, err := json.Marshal(frame)
	if err != nil {
		panic(err)
	}
	return string(data) + "\n"
}

func TestP049VersionedNDJSONHelloPingAndAuthenticatedMapping(t *testing.T) {
	controller := p049Controller(t)
	server, err := NewServer(ServerOptions{Controllers: KeyControllerMap{"SHA256:allowed": controller}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input := p049Frame(OperationHello, "hello-1", `{}`) + p049Frame(OperationPing, "ping-1", `{}`)
	if err := server.Serve(context.Background(), "SHA256:allowed", strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	decoder := NewDecoder(strings.NewReader(output.String()))
	first, err := decoder.DecodeReply()
	if err != nil {
		t.Fatal(err)
	}
	if first.ResponseType != "hello" || string(first.Payload) != `{"supported_protocol_version":1}` {
		t.Fatalf("hello reply = %+v", first)
	}
	second, err := decoder.DecodeReply()
	if err != nil {
		t.Fatal(err)
	}
	if second.ResponseType != "result" || second.RequestID != "ping-1" {
		t.Fatalf("ping reply = %+v", second)
	}
	got, err := server.ControllerForKey("SHA256:allowed")
	if err != nil || got.Type() != controller.Type() || got.ID() != controller.ID() {
		t.Fatalf("mapped controller = %v, %v", got, err)
	}
}

func TestP049HelloPingAreNotForwardedAndOtherOperationsWaitForLaterPhase(t *testing.T) {
	controller := p049Controller(t)
	called := false
	server, err := NewServer(ServerOptions{
		Controllers: KeyControllerMap{"allowed": controller},
		Handler: requestHandlerFunc(func(context.Context, domain.ControllerIdentity, RequestFrame) (ReplyFrame, error) {
			called = true
			return pingReply("unexpected"), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := server.Serve(context.Background(), "allowed", strings.NewReader(p049Frame(OperationHello, "h", `{}`)+p049Frame(OperationPing, "p", `{}`)), &output); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("hello/ping were forwarded")
	}

	server, err = NewServer(ServerOptions{Controllers: KeyControllerMap{"allowed": controller}})
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := server.Serve(context.Background(), "allowed", strings.NewReader(p049Frame(OperationGetSession, "g", `{"session_id":"s"}`)), &output); err != nil {
		t.Fatal(err)
	}
	var reply ReplyFrame
	if _, err := io.ReadAll(strings.NewReader(output.String())); err != nil {
		t.Fatal(err)
	}
	reply, err = decodeReplyBytes([]byte(strings.TrimSpace(output.String())))
	if err != nil {
		t.Fatal(err)
	}
	if reply.ResponseType != "error" || !strings.Contains(string(reply.Payload), "operation_unsupported") {
		t.Fatalf("unsupported operation reply = %+v", reply)
	}
}

func TestP049OversizeIsRejectedBeforeJSONDecodeIncludingNoNewline(t *testing.T) {
	limit := domain.MaxSerializedFrameBytes
	for _, raw := range [][]byte{
		append(bytes.Repeat([]byte{'{'}, limit+1), '\n'),
		bytes.Repeat([]byte{'{'}, limit+1),
	} {
		decoder := NewDecoder(bytes.NewReader(raw))
		_, err := decoder.Decode()
		if !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("oversized frame error = %v, want ErrFrameTooLarge", err)
		}
	}
}

func TestP049OversizeMalformedFrameNeverReachesJSONDecoder(t *testing.T) {
	raw := append([]byte("not-json"), bytes.Repeat([]byte{'{'}, domain.MaxSerializedFrameBytes)...)
	_, err := DecodeRequest(raw)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized malformed frame error = %v, want size error", err)
	}
}

func TestP049UnknownMajorAndForgedControllerFieldsFailClosed(t *testing.T) {
	controller := p049Controller(t)
	server, err := NewServer(ServerOptions{Controllers: KeyControllerMap{"allowed": controller}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`{"protocol_version":2,"request_id":"r","operation":"ping","payload":{}}`,
		`{"protocol_version":1,"request_id":"r","operation":"ping","payload":{},"controller_id":"forged"}`,
		`{"protocol_version":1,"request_id":"r","operation":"ping","payload":{"controller":{"type":"direct_mtls","id":"forged"}}}`,
	}
	for _, raw := range cases {
		if _, err := DecodeRequest([]byte(raw)); err == nil {
			t.Fatalf("forged/unknown frame accepted: %s", raw)
		}
	}
	if _, err := server.ControllerForKey("forged"); !errors.Is(err, ErrUnknownAuthenticatedKey) {
		t.Fatalf("unknown key error = %v", err)
	}
}

func TestP049AdditivePayloadFieldsRoundTripWithinBound(t *testing.T) {
	raw := []byte(p049Frame(OperationGetSession, "r", `{"session_id":"s","future_field":{"enabled":true}}`))
	request, err := NewDecoder(bytes.NewReader(raw)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(request.Payload, []byte("future_field")) {
		t.Fatalf("payload lost additive field: %s", request.Payload)
	}
}

func FuzzP049FrameDecoderNeverPanics(f *testing.F) {
	f.Add([]byte(p049Frame(OperationPing, "seed", `{}`)))
	f.Add([]byte("{"))
	f.Add(bytes.Repeat([]byte{'x'}, 1024))
	f.Fuzz(func(t *testing.T, raw []byte) {
		decoder := NewDecoder(bytes.NewReader(raw))
		for i := 0; i < 2; i++ {
			if _, err := decoder.Decode(); errors.Is(err, io.EOF) {
				return
			}
		}
	})
}

type requestHandlerFunc func(context.Context, domain.ControllerIdentity, RequestFrame) (ReplyFrame, error)

func (f requestHandlerFunc) Handle(ctx context.Context, controller domain.ControllerIdentity, request RequestFrame) (ReplyFrame, error) {
	return f(ctx, controller, request)
}

func decodeReplyBytes(raw []byte) (ReplyFrame, error) {
	var reply ReplyFrame
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return ReplyFrame{}, err
	}
	return reply, nil
}
