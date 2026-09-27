package mailbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP087TerminalSnapshotDistinguishesCompleteTruncatedAndUnavailable(t *testing.T) {
	baseCommand := store.CommandRecord{CommandID: "command-p087", SessionID: "session-p087", Ordinal: 2, State: domain.CommandStateSucceeded, ExitCode: intPointerP085(0), FinalEventSequence: int64PointerP087(4), OutputComplete: true}
	events := []store.CommandEventRecord{
		{CommandID: baseCommand.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: time.Unix(1, 0).UTC()},
		{CommandID: baseCommand.CommandID, Sequence: 2, Type: "command_started", OccurredAt: time.Unix(2, 0).UTC()},
		{CommandID: baseCommand.CommandID, Sequence: 3, Type: "stdout", Payload: []byte("done\n"), ByteCount: 5, OccurredAt: time.Unix(3, 0).UTC()},
		{CommandID: baseCommand.CommandID, Sequence: 4, Type: "command_succeeded", OccurredAt: time.Unix(4, 0).UTC()},
	}
	complete, err := RenderTerminalCommandSnapshot(baseCommand, events)
	if err != nil {
		t.Fatal(err)
	}
	if complete.AvailableEventSequence != 4 || !complete.OutputComplete || complete.OutputTruncated || complete.OutputUnavailableReason != "" || complete.EventsFile != "events/command-p087.ndjson" || !complete.FullAnswer("complete") || complete.FullAnswer("accepted") {
		t.Fatalf("complete snapshot=%+v", complete)
	}

	truncatedCommand := baseCommand
	truncatedCommand.OutputTruncated = true
	truncated, err := RenderTerminalCommandSnapshot(truncatedCommand, events)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated.OutputComplete || !truncated.OutputTruncated || truncated.FullAnswer("complete") {
		t.Fatalf("truncated snapshot=%+v", truncated)
	}

	gappedCommand := baseCommand
	gappedCommand.OutputComplete = false
	gappedCommand.OutputUnavailableReason = "remote_event_gap"
	gapped, err := RenderTerminalCommandSnapshot(gappedCommand, events[:3])
	if err != nil {
		t.Fatal(err)
	}
	if gapped.AvailableEventSequence != 3 || gapped.FinalEventSequence != 4 || gapped.OutputComplete || gapped.OutputUnavailableReason != "remote_event_gap" || gapped.FullAnswer("complete") {
		t.Fatalf("gapped snapshot=%+v", gapped)
	}

	retainedExpired := baseCommand
	retainedExpired.OutputComplete = false
	retainedExpired.OutputUnavailableReason = "retention_expired"
	expired, err := RenderTerminalCommandSnapshot(retainedExpired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if expired.AvailableEventSequence != 0 || expired.EventsFile != "" || len(expired.EventBytes) != 0 || expired.FullAnswer("complete") {
		t.Fatalf("expired snapshot=%+v", expired)
	}
}

func TestP087CaptureBoundaryRequiresTerminalLossAndNoMissingOutputClaims(t *testing.T) {
	command := store.CommandRecord{CommandID: "command-p087-loss", SessionID: "session-p087", Ordinal: 1, State: domain.CommandStateLost, FinalEventSequence: int64PointerP087(4), OutputUnavailableReason: "capture_boundary_unconfirmed"}
	events := []store.CommandEventRecord{
		{CommandID: command.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: time.Unix(1, 0).UTC()},
		{CommandID: command.CommandID, Sequence: 2, Type: "command_started", OccurredAt: time.Unix(2, 0).UTC()},
		{CommandID: command.CommandID, Sequence: 3, Type: "stdout", Payload: []byte("prefix"), ByteCount: 6, OccurredAt: time.Unix(3, 0).UTC()},
		{CommandID: command.CommandID, Sequence: 4, Type: "command_lost", OccurredAt: time.Unix(4, 0).UTC()},
	}
	snapshot, err := RenderTerminalCommandSnapshot(command, events)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.OutputComplete || snapshot.OutputUnavailableReason != "capture_boundary_unconfirmed" || snapshot.FullAnswer("complete") {
		t.Fatalf("capture-loss snapshot=%+v", snapshot)
	}
	if _, err := RenderTerminalCommandSnapshot(command, events[:3]); !errors.Is(err, ErrTerminalSnapshot) {
		t.Fatalf("missing loss terminal error=%v", err)
	}
	invalid := command
	invalid.OutputUnavailableReason = ""
	if _, err := RenderTerminalCommandSnapshot(invalid, events); !errors.Is(err, ErrTerminalSnapshot) {
		t.Fatalf("unexplained incomplete error=%v", err)
	}
}

func TestP087TerminalProjectorRejectsActiveAndRetainsFinalMetadata(t *testing.T) {
	authority, command := p085CommandAuthority(t)
	projector := EventProjector{Authority: authority}
	if _, err := projector.TerminalSnapshot(context.Background(), command.CommandID); !errors.Is(err, ErrTerminalSnapshot) {
		t.Fatalf("active command error=%v", err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("ok"), ByteCount: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointerP085(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projector.TerminalSnapshot(context.Background(), command.CommandID)
	if err != nil || snapshot.FinalEventSequence != 4 || snapshot.AvailableEventSequence != 4 || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 || !snapshot.FullAnswer("complete") {
		t.Fatalf("terminal projector snapshot=%+v err=%v", snapshot, err)
	}
}

func int64PointerP087(value int64) *int64 { return &value }
