package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP018I03SubscriberRegisterReplayLiveHandoffDedupe(t *testing.T) {
	store, command := newP018CommandStore(t, "p018-handoff")
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}

	const outputEvents = 32
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; i < outputEvents; i++ {
			payload := []byte{byte(i), 0xff, byte(i + 1)}
			if _, err := store.AppendCommandEvent(context.Background(), CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: payload, ByteCount: int64(len(payload))}); err != nil {
				t.Errorf("append output %d: %v", i, err)
				return
			}
		}
	}()
	subscription, err := store.SubscribeCommandEvents(context.Background(), command.CommandID, 1, outputEvents+4)
	if err != nil {
		t.Fatal(err)
	}
	writers.Wait()
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}

	wantLast := int64(outputEvents + 3)
	seen := make(map[int64]struct{}, wantLast-1)
	for len(seen) < int(wantLast-1) {
		select {
		case event, ok := <-subscription.Events():
			if !ok {
				t.Fatalf("subscriber closed before sequence %d", wantLast)
			}
			if event.Sequence < 2 || event.Sequence > wantLast {
				t.Fatalf("unexpected event sequence %d", event.Sequence)
			}
			if _, duplicate := seen[event.Sequence]; duplicate {
				t.Fatalf("duplicate event sequence %d", event.Sequence)
			}
			seen[event.Sequence] = struct{}{}
		case subscriberErr, ok := <-subscription.Errors():
			if ok {
				t.Fatalf("subscriber failed during replay/live handoff: %v", subscriberErr)
			}
			t.Fatal("subscriber error channel closed before all events")
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out after %d/%d events", len(seen), wantLast-1)
		}
	}
	if subscription.LastSequence() != wantLast {
		t.Fatalf("last sequence = %d, want %d", subscription.LastSequence(), wantLast)
	}
	subscription.Close()
}

func TestP018I03SubscriberOverflowReturnsResumableCursor(t *testing.T) {
	store, command := newP018CommandStore(t, "p018-overflow")
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		payload := []byte{byte(i + 1)}
		if _, err := store.AppendCommandEvent(context.Background(), CommandEventAppend{CommandID: command.CommandID, Type: "stderr", Payload: payload, ByteCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}

	subscription, err := store.SubscribeCommandEvents(context.Background(), command.CommandID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	var replayed []int64
	for event := range subscription.Events() {
		replayed = append(replayed, event.Sequence)
	}
	if len(replayed) != 2 || replayed[0] != 1 || replayed[1] != 2 {
		t.Fatalf("overflow prefix = %v, want [1 2]", replayed)
	}
	var overflow error
	select {
	case overflow = <-subscription.Errors():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for overflow")
	}
	if !errors.Is(overflow, ErrSubscriberOverflow) {
		t.Fatalf("overflow error = %v", overflow)
	}
	if subscription.LastSequence() != 2 {
		t.Fatalf("overflow cursor = %d, want 2", subscription.LastSequence())
	}

	resumed, err := store.SubscribeCommandEvents(context.Background(), command.CommandID, subscription.LastSequence(), 8)
	if err != nil {
		t.Fatal(err)
	}
	var resumedSequences []int64
	for i := 0; i < 3; i++ {
		select {
		case event := <-resumed.Events():
			resumedSequences = append(resumedSequences, event.Sequence)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for resumed event %d", i+1)
		}
	}
	resumed.Close()
	if len(resumedSequences) != 3 || resumedSequences[0] != 3 || resumedSequences[1] != 4 || resumedSequences[2] != 5 {
		t.Fatalf("resumed events = %v, want [3 4 5]", resumedSequences)
	}
}

func newP018CommandStore(t *testing.T, suffix string) (*AuthorityStore, CommandRecord) {
	t.Helper()
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/"+suffix+".db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-"+suffix, "key-"+suffix+"-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{
		CommandID:      domain.CommandID("command-" + suffix),
		SessionID:      created.SessionID,
		IdempotencyKey: "key-" + suffix + "-command",
		RequestHash:    p014Hash(t, `{"operation":"submit_command","session_id":"`+string(created.SessionID)+`","script":"echo subscriber"}`),
		Script:         "echo subscriber",
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, command
}
