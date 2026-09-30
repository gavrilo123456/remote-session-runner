package dispatcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

func TestP074RemoteEventMirrorStreamsDeduplicatesAndAdvancesCursor(t *testing.T) {
	authority := p068Authority(t)
	commandID := domain.CommandID("command-p074-stream")
	intent := p068SubmitIntent(t, "intent-p074-stream", "session-p074-stream", string(commandID), domain.TargetKindRemote, "printf stream")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p074StreamCaller{replies: []sshbridge.ReplyFrame{
		p074EventReply("events/command-p074-stream/0", commandID, 1, "command_queued", nil),
		p074EventReply("events/command-p074-stream/0", commandID, 1, "command_queued", nil),
		p074EventReply("events/command-p074-stream/0", commandID, 2, "command_started", nil),
		p074EventReply("events/command-p074-stream/0", commandID, 3, "stdout", []byte("one")),
		p074EventReply("events/command-p074-stream/0", commandID, 4, "command_succeeded", nil),
		p074EndReply("events/command-p074-stream/0", 4),
	}}
	driver, err := NewRemoteDriver(authority, caller, "router-p074", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.MirrorCommandEvents(context.Background(), commandID, controller)
	if err != nil || result.LastSequence != 4 || result.Mirrored != 4 || result.Duplicates != 1 {
		t.Fatalf("stream mirror = %+v, %v", result, err)
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 4 {
		t.Fatalf("stream cursor = %d, %v", cursor, err)
	}
	events, err := authority.ListRemoteEvents(context.Background(), commandID, 0)
	if err != nil || len(events) != 4 || events[0].Sequence != 1 || events[1].Sequence != 2 || events[2].Sequence != 3 || events[3].Sequence != 4 {
		t.Fatalf("stream events = %+v, %v", events, err)
	}
	if len(caller.requests) != 1 || caller.requests[0].Operation != sshbridge.OperationStreamCommandEvents || caller.requests[0].ResourceID != "" || caller.requests[0].IdempotencyKey != "" {
		t.Fatalf("stream request = %+v", caller.requests)
	}
	requestData, err := json.Marshal(caller.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sshbridge.DecodeRequest(requestData); err != nil {
		t.Fatalf("stream request violates frozen bridge protocol: %v", err)
	}
}

func TestP074RemoteEventMirrorRejectsGapWithoutAdvancingCursor(t *testing.T) {
	authority := p068Authority(t)
	commandID := domain.CommandID("command-p074-gap")
	intent := p068SubmitIntent(t, "intent-p074-gap", "session-p074-gap", string(commandID), domain.TargetKindRemote, "printf gap")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	requestID := "events/command-p074-gap/0"
	caller := &p074StreamCaller{replies: []sshbridge.ReplyFrame{
		p074EventReply(requestID, commandID, 2, "command_succeeded", nil),
	}}
	driver, err := NewRemoteDriver(authority, caller, "router-p074", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	controller := domain.ControllerIdentity{}
	controller, err = domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.MirrorCommandEvents(context.Background(), commandID, controller); !errors.Is(err, store.ErrRemoteEventGap) {
		t.Fatalf("gap mirror error = %v", err)
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 0 {
		t.Fatalf("gap cursor = %d, %v", cursor, err)
	}
}

type p074StreamCaller struct {
	mu       sync.Mutex
	requests []sshbridge.RequestFrame
	replies  []sshbridge.ReplyFrame
}

func (c *p074StreamCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	return p069AcceptedReply(frame), nil
}

func (c *p074StreamCaller) Stream(_ context.Context, request sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	replies := append([]sshbridge.ReplyFrame(nil), c.replies...)
	c.mu.Unlock()
	for _, reply := range replies {
		if err := receive(reply); err != nil {
			return err
		}
	}
	return nil
}

func p074EventReply(requestID string, commandID domain.CommandID, sequence int64, eventType string, data []byte) sshbridge.ReplyFrame {
	payload := map[string]any{"command_id": string(commandID), "sequence": sequence, "type": eventType, "timestamp": "2026-09-27T10:00:00Z"}
	if len(data) != 0 {
		payload["encoding"] = "base64"
		payload["data_base64"] = base64.StdEncoding.EncodeToString(data)
		payload["byte_count"] = len(data)
	}
	raw, _ := json.Marshal(payload)
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, ResponseType: "event", Payload: raw}
}

func p074EndReply(requestID string, sequence int64) sshbridge.ReplyFrame {
	raw, _ := json.Marshal(map[string]int64{"last_sequence": sequence})
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, ResponseType: "stream_end", Payload: raw}
}
