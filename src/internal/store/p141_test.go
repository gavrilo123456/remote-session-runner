package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP141F05SubscriberByteLimitReturnsResumableReplayPrefix(t *testing.T) {
	authority, command := newP018CommandStore(t, "p141-byte-overflow")
	if _, err := authority.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte("1234"), []byte("5678")} {
		if _, err := authority.AppendCommandEvent(context.Background(), CommandEventAppend{
			CommandID: command.CommandID, Type: "stdout", Payload: payload, ByteCount: int64(len(payload)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	completed, err := authority.TransitionCommand(context.Background(), CommandTransition{
		CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	subscription, err := authority.SubscribeCommandEventsWithByteLimit(context.Background(), command.CommandID, 0, 16, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if got := subscription.BufferedPayloadBytes(); got != 4 {
		t.Fatalf("subscriber buffered payload=%d, want the first four-byte output event only", got)
	}
	if got := subscription.LastSequence(); got != 3 {
		t.Fatalf("overflow resume cursor=%d, want sequence 3 before the rejected second output event", got)
	}
	var prefix []CommandEventRecord
	for event := range subscription.Events() {
		prefix = append(prefix, event)
		subscription.Acknowledge(event)
	}
	if len(prefix) != 3 || prefix[2].Sequence != 3 || subscription.BufferedPayloadBytes() != 0 {
		t.Fatalf("overflow prefix=%+v buffered=%d, want events through sequence 3 and zero bytes after acknowledgement", prefix, subscription.BufferedPayloadBytes())
	}
	if overflow, open := <-subscription.Errors(); !open || !errors.Is(overflow, ErrSubscriberOverflow) {
		t.Fatalf("overflow error=%v open=%t, want ErrSubscriberOverflow", overflow, open)
	}

	resumed, err := authority.SubscribeCommandEventsWithByteLimit(context.Background(), command.CommandID, subscription.LastSequence(), 16, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	var suffix []CommandEventRecord
	deadline := time.After(time.Second)
	for len(suffix) < 2 {
		select {
		case event, ok := <-resumed.Events():
			if !ok {
				t.Fatalf("resumed stream closed after %d events", len(suffix))
			}
			suffix = append(suffix, event)
			resumed.Acknowledge(event)
		case <-deadline:
			t.Fatal("timed out waiting for resumed event suffix")
		}
	}
	if suffix[0].Sequence != 4 || suffix[1].Sequence != 5 || suffix[1].Type != "command_succeeded" || completed.FinalEventSequence == nil || *completed.FinalEventSequence != 5 {
		t.Fatalf("resumed suffix=%+v terminal=%v, want output 4 then terminal 5", suffix, completed.FinalEventSequence)
	}
}

func TestP141F05SubscriberAcknowledgementReleasesBytesForLongStream(t *testing.T) {
	authority, command := newP018CommandStore(t, "p141-byte-ack")
	if _, err := authority.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	subscription, err := authority.SubscribeCommandEventsWithByteLimit(context.Background(), command.CommandID, 2, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	for index := 0; index < 20; index++ {
		payload := []byte("four")
		if _, err := authority.AppendCommandEvent(context.Background(), CommandEventAppend{
			CommandID: command.CommandID, Type: "stderr", Payload: payload, ByteCount: int64(len(payload)),
		}); err != nil {
			t.Fatalf("append output %d: %v", index, err)
		}
		select {
		case event := <-subscription.Events():
			if event.Sequence != int64(index+3) || string(event.Payload) != "four" {
				t.Fatalf("stream event %d=%+v", index, event)
			}
			subscription.Acknowledge(event)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for stream event %d", index)
		}
		if got := subscription.BufferedPayloadBytes(); got != 0 {
			t.Fatalf("buffered payload after event acknowledgement=%d, want zero", got)
		}
	}
	if got := subscription.LastSequence(); got != 22 {
		t.Fatalf("long stream cursor=%d, want 22 after 20 acknowledged outputs", got)
	}
	subscription.Close()
	if overflow, open := <-subscription.Errors(); open {
		t.Fatalf("long stream overflowed after acknowledgements: %v", overflow)
	}
}
