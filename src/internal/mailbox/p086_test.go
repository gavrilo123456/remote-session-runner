package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP086ActiveSnapshotFreezesCursorObservedAtAndFileReference(t *testing.T) {
	authority, command := p085CommandAuthority(t)
	firstObserved := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	projector := EventProjector{Authority: authority, Clock: func() time.Time { return firstObserved }}
	first, err := projector.ActiveSnapshot(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != domain.CommandStateQueued || first.AvailableEventSequence != 1 || first.EventsFile != "events/command-p085.ndjson" || !first.ObservedAt.Equal(firstObserved) || first.OutputComplete || first.OutputTruncated || len(first.EventBytes) == 0 {
		t.Fatalf("initial active snapshot=%+v", first)
	}
	firstBytes := append([]byte(nil), first.EventBytes...)

	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("later\n"), ByteCount: 6}); err != nil {
		t.Fatal(err)
	}
	secondObserved := firstObserved.Add(time.Second)
	projector.Clock = func() time.Time { return secondObserved }
	second, err := projector.ActiveSnapshot(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != domain.CommandStateRunning || second.AvailableEventSequence != 3 || !second.ObservedAt.Equal(secondObserved) || bytes.Equal(first.EventBytes, second.EventBytes) {
		t.Fatalf("second active snapshot=%+v", second)
	}
	if !bytes.Equal(first.EventBytes, firstBytes) || first.AvailableEventSequence != 1 || !first.ObservedAt.Equal(firstObserved) {
		t.Fatalf("first snapshot mutated: %+v", first)
	}

	response := map[string]any{
		"request_id": "req-p086", "operation": "get_command", "request_state": "complete", "response_revision": 1,
		"command_id": string(first.CommandID), "command_state": string(first.State), "observed_at": first.ObservedAt.Format(time.RFC3339Nano),
		"available_event_sequence": first.AvailableEventSequence, "output_complete": false, "output_truncated": false, "events_file": first.EventsFile,
	}
	if err := p004MailboxSchemas(t)["response"].Validate(response); err != nil {
		t.Fatalf("active response schema rejected observed_at snapshot: %v", err)
	}

	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointerP085(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.ActiveSnapshot(context.Background(), command.CommandID); !errors.Is(err, ErrActiveSnapshotTerminal) {
		t.Fatalf("terminal snapshot error=%v, want ErrActiveSnapshotTerminal", err)
	}
}

func TestP086RejectsUnsafeCommandFileReference(t *testing.T) {
	command := store.CommandRecord{CommandID: domain.CommandID("../escape"), Ordinal: 1}
	event := store.CommandEventRecord{CommandID: command.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: time.Unix(1, 0).UTC()}
	data, err := RenderCommandEvents(command, []store.CommandEventRecord{event})
	if err != nil || !json.Valid(bytes.TrimSpace(data)) {
		t.Fatalf("render fixture data=%q err=%v", data, err)
	}
	if _, err := commandEventsFileReference(command.CommandID); !errors.Is(err, ErrEventProjection) {
		t.Fatalf("unsafe file reference error=%v", err)
	}
}
