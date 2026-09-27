package mailbox

import (
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// CommandSnapshot is a frozen file-mailbox view. State is empty when only a
// local intent/delivery outcome is known and no target command state exists.
type CommandSnapshot struct {
	CommandID               domain.CommandID
	SessionID               domain.SessionID
	DeliveryState           string
	State                   domain.CommandState
	Ordinal                 int64
	ObservedAt              time.Time
	ExitCode                *int
	FinalEventSequence      *int64
	AvailableEventSequence  int64
	OutputComplete          bool
	OutputTruncated         bool
	OutputUnavailableReason string
	EventsFile              string
	EventBytes              []byte
}

// SnapshotCommand renders one local authority command and event prefix as a
// single frozen mailbox snapshot.
func SnapshotCommand(command store.CommandRecord, events []store.CommandEventRecord, observedAt time.Time) (CommandSnapshot, error) {
	if command.State.IsTerminal() {
		terminal, err := RenderTerminalCommandSnapshot(command, events)
		if err != nil {
			return CommandSnapshot{}, err
		}
		return commandSnapshotFromTerminal(terminal, observedAt), nil
	}
	active, err := RenderActiveCommandSnapshot(command, events, observedAt)
	if err != nil {
		return CommandSnapshot{}, err
	}
	return commandSnapshotFromActive(active), nil
}

// SnapshotRemoteCommand renders one Mac projection and its mirrored event
// prefix. The caller obtains both from one local SQLite snapshot and verifies
// that the projection belongs to the queued local intent first.
func SnapshotRemoteCommand(projection store.RemoteCommandProjection, events []store.RemoteEventRecord, observedAt time.Time) (CommandSnapshot, error) {
	command := store.CommandRecord{
		CommandID: projection.CommandID, SessionID: projection.SessionID, Ordinal: projection.Ordinal,
		State: projection.State, ExitCode: projection.ExitCode, FinalEventSequence: projection.FinalEventSequence,
		OutputComplete: projection.OutputComplete, OutputTruncated: projection.OutputTruncated,
		OutputUnavailableReason: projection.OutputUnavailableReason,
	}
	converted := make([]store.CommandEventRecord, len(events))
	for index, event := range events {
		converted[index] = store.CommandEventRecord{
			CommandID: event.CommandID, Sequence: event.Sequence, Type: event.Type,
			Payload: append([]byte(nil), event.Payload...), ByteCount: event.ByteCount, OccurredAt: event.OccurredAt,
		}
	}
	return SnapshotCommand(command, converted, observedAt)
}

func commandSnapshotFromActive(snapshot ActiveCommandSnapshot) CommandSnapshot {
	return CommandSnapshot{
		CommandID: snapshot.CommandID, SessionID: snapshot.SessionID, State: snapshot.State,
		Ordinal: snapshot.Ordinal, ObservedAt: snapshot.ObservedAt,
		AvailableEventSequence: snapshot.AvailableEventSequence, OutputComplete: false,
		OutputTruncated: snapshot.OutputTruncated, EventsFile: snapshot.EventsFile,
		EventBytes: append([]byte(nil), snapshot.EventBytes...),
	}
}

func commandSnapshotFromTerminal(snapshot TerminalCommandSnapshot, observedAt time.Time) CommandSnapshot {
	finalSequence := snapshot.FinalEventSequence
	return CommandSnapshot{
		CommandID: snapshot.CommandID, SessionID: snapshot.SessionID, State: snapshot.State,
		Ordinal: snapshot.Ordinal, ObservedAt: observedAt.UTC(), ExitCode: snapshot.ExitCode,
		FinalEventSequence: &finalSequence, AvailableEventSequence: snapshot.AvailableEventSequence,
		OutputComplete: snapshot.OutputComplete, OutputTruncated: snapshot.OutputTruncated,
		OutputUnavailableReason: snapshot.OutputUnavailableReason, EventsFile: snapshot.EventsFile,
		EventBytes: append([]byte(nil), snapshot.EventBytes...),
	}
}

// ValidateCommandSnapshot ensures a boundary implementation did not return a
// mismatched ID or a malformed authority state before serialization.
func ValidateCommandSnapshot(snapshot CommandSnapshot, commandID domain.CommandID) error {
	if snapshot.CommandID != commandID || snapshot.SessionID == "" || snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("%w: command snapshot identity or observation time", ErrEventProjection)
	}
	if snapshot.State != "" && !snapshot.State.Valid() {
		return fmt.Errorf("%w: command snapshot state %q", ErrEventProjection, snapshot.State)
	}
	return nil
}
