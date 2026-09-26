package sshbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func p054Event(commandID string, sequence int64, eventType string, data []byte) string {
	event := map[string]any{
		"command_id":  commandID,
		"sequence":    sequence,
		"type":        eventType,
		"occurred_at": time.Date(2026, 9, 26, 12, 0, int(sequence), 0, time.UTC),
		"byte_count":  len(data),
	}
	if len(data) != 0 {
		event["data_base64"] = base64.StdEncoding.EncodeToString(data)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(raw) + "\n"
}

func TestP054ForwardReplayFollowAndReconnectWithBoundedBase64Events(t *testing.T) {
	controller := p049Controller(t)
	var mu sync.Mutex
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		queries = append(queries, request.URL.Query())
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/x-ndjson")
		if request.URL.Query().Get("after") == "2" {
			_, _ = io.WriteString(writer, p054Event("command-1", 3, "command_succeeded", nil))
			return
		}
		_, _ = io.WriteString(writer,
			p054Event("command-1", 1, "command_queued", nil)+
				p054Event("command-1", 2, "stdout", []byte{0x00, 0xff, 0x41})+
				p054Event("command-1", 3, "command_succeeded", nil))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	collect := func(request RequestFrame) []ReplyFrame {
		var replies []ReplyFrame
		if err := forwarder.Stream(context.Background(), controller, request, func(reply ReplyFrame) error {
			replies = append(replies, reply)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return replies
	}
	replies := collect(RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "events-1", Operation: OperationStreamCommandEvents, Payload: json.RawMessage(`{"command_id":"command-1","after_sequence":0,"follow":true}`)})
	if len(replies) != 4 || replies[0].ResponseType != "event" || replies[3].ResponseType != "stream_end" {
		t.Fatalf("event stream replies = %+v", replies)
	}
	var output map[string]any
	if err := json.Unmarshal(replies[1].Payload, &output); err != nil {
		t.Fatal(err)
	}
	if output["encoding"] != "base64" || output["data_base64"] != base64.StdEncoding.EncodeToString([]byte{0, 255, 65}) || output["byte_count"] != float64(3) {
		t.Fatalf("output event = %#v", output)
	}
	var end map[string]any
	if err := json.Unmarshal(replies[3].Payload, &end); err != nil {
		t.Fatal(err)
	}
	if end["last_sequence"] != float64(3) {
		t.Fatalf("stream end = %#v", end)
	}
	reconnect := collect(RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "events-2", Operation: OperationStreamCommandEvents, Payload: json.RawMessage(`{"command_id":"command-1","after_sequence":2}`)})
	if len(reconnect) != 2 || reconnect[0].ResponseType != "event" || reconnect[1].ResponseType != "stream_end" {
		t.Fatalf("reconnect replies = %+v", reconnect)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || queries[0].Get("controller_type") != string(controller.Type()) || queries[0].Get("controller_id") != string(controller.ID()) || queries[0].Get("after") != "0" || queries[0].Get("follow") != "true" || queries[1].Get("after") != "2" || queries[1].Get("follow") != "" {
		t.Fatalf("event queries = %#v", queries)
	}
}

func TestP054BridgeServerWritesEventFramesAndStreamEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, p054Event("command-1", 1, "command_succeeded", nil))
	}))
	defer server.Close()
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewServer(ServerOptions{Controllers: KeyControllerMap{"allowed": p049Controller(t)}, Handler: forwarder})
	if err != nil {
		t.Fatal(err)
	}
	input := RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "server-events", Operation: OperationStreamCommandEvents, Payload: json.RawMessage(`{"command_id":"command-1","after_sequence":0}`)}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := bridge.Serve(context.Background(), "allowed", strings.NewReader(string(raw)+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	decoder := NewDecoder(strings.NewReader(output.String()))
	first, err := decoder.DecodeReply()
	if err != nil || first.ResponseType != "event" {
		t.Fatalf("first event reply = %+v, %v", first, err)
	}
	last, err := decoder.DecodeReply()
	if err != nil || last.ResponseType != "stream_end" || last.RequestID != input.RequestID {
		t.Fatalf("stream end reply = %+v, %v", last, err)
	}
}

func TestP054MapsCursorErrorsAndRejectsOversizedOrGappedEvents(t *testing.T) {
	controller := p049Controller(t)
	statusServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		_, _ = io.WriteString(writer, `{"error":"event history unavailable"}`)
	}))
	forwarder, err := NewRunnerdForwarder(ForwarderOptions{Client: statusServer.Client(), BaseURL: statusServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	var replies []ReplyFrame
	if err := forwarder.Stream(context.Background(), controller, RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "cursor-error", Operation: OperationStreamCommandEvents, Payload: json.RawMessage(`{"command_id":"command-1","after_sequence":9}`)}, func(reply ReplyFrame) error {
		replies = append(replies, reply)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].ResponseType != "error" {
		t.Fatalf("cursor error replies = %+v", replies)
	}
	var errorPayload ErrorPayload
	if err := json.Unmarshal(replies[0].Payload, &errorPayload); err != nil {
		t.Fatal(err)
	}
	if errorPayload.Code != "event_history_unavailable" || errorPayload.Retryable {
		t.Fatalf("cursor error payload = %+v", errorPayload)
	}
	statusServer.Close()

	oversized := make([]byte, bridgeMaxOutputChunkBytes+1)
	overflowServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, p054Event("command-1", 1, "stdout", oversized))
	}))
	defer overflowServer.Close()
	forwarder, err = NewRunnerdForwarder(ForwarderOptions{Client: overflowServer.Client(), BaseURL: overflowServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := forwarder.Stream(context.Background(), controller, RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "overflow", Operation: OperationStreamCommandEvents, Payload: json.RawMessage(`{"command_id":"command-1","after_sequence":0}`)}, func(ReplyFrame) error { return nil }); !errors.Is(err, ErrPrivateResponse) {
		t.Fatalf("oversized event error = %v", err)
	}

	gapServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, p054Event("command-1", 2, "command_succeeded", nil))
	}))
	defer gapServer.Close()
	forwarder, err = NewRunnerdForwarder(ForwarderOptions{Client: gapServer.Client(), BaseURL: gapServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	replies = nil
	if err := forwarder.Stream(context.Background(), controller, RequestFrame{ProtocolVersion: ProtocolVersion, RequestID: "gap", Operation: OperationStreamCommandEvents, Payload: json.RawMessage(`{"command_id":"command-1","after_sequence":0}`)}, func(reply ReplyFrame) error {
		replies = append(replies, reply)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].ResponseType != "error" {
		t.Fatalf("gap replies = %+v", replies)
	}
	if !strings.Contains(string(replies[0].Payload), "event_history_unavailable") {
		t.Fatalf("gap payload = %s", replies[0].Payload)
	}
}
