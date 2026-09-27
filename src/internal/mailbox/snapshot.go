package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var ErrActiveSnapshotTerminal = errors.New("active command snapshot requested for terminal command")

// ActiveCommandSnapshot is an as-of view of one nonterminal command. The
// rendered event bytes and cursor are retained together, so later events do
// not retroactively change the response represented by this value.
type ActiveCommandSnapshot struct {
	CommandID              domain.CommandID
	SessionID              domain.SessionID
	Ordinal                int64
	State                  domain.CommandState
	ObservedAt             time.Time
	AvailableEventSequence int64
	EventsFile             string
	OutputComplete         bool
	OutputTruncated        bool
	EventBytes             []byte
}

// ActiveSnapshot captures the currently available contiguous event prefix for
// a queued, running, or cancelling command. It intentionally does not claim
// terminal completeness; P087 adds those terminal/output rules.
func (p EventProjector) ActiveSnapshot(ctx context.Context, commandID domain.CommandID) (ActiveCommandSnapshot, error) {
	if p.Authority == nil {
		return ActiveCommandSnapshot{}, ErrEventProjectionConfiguration
	}
	validatedID, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return ActiveCommandSnapshot{}, err
	}
	command, events, err := p.Authority.GetCommandWithEvents(ctx, validatedID)
	if err != nil {
		return ActiveCommandSnapshot{}, err
	}
	if command.State.IsTerminal() {
		return ActiveCommandSnapshot{}, ErrActiveSnapshotTerminal
	}
	clock := p.Clock
	if clock == nil {
		clock = time.Now
	}
	return RenderActiveCommandSnapshot(command, events, clock())
}

// RenderActiveCommandSnapshot freezes the event prefix supplied by one store
// snapshot. An empty prefix is valid while a queued-remote mirror has not yet
// received its first event; it advertises cursor zero and no file reference.
func RenderActiveCommandSnapshot(command store.CommandRecord, events []store.CommandEventRecord, observedAt time.Time) (ActiveCommandSnapshot, error) {
	if command.CommandID == "" || command.SessionID == "" || command.Ordinal < 1 || command.State.IsTerminal() || observedAt.IsZero() {
		return ActiveCommandSnapshot{}, fmt.Errorf("%w: active command identity or state", ErrEventProjection)
	}
	var data []byte
	available := int64(0)
	file := ""
	if len(events) > 0 {
		var err error
		data, err = RenderCommandEvents(command, events)
		if err != nil {
			return ActiveCommandSnapshot{}, err
		}
		available = events[len(events)-1].Sequence
		file, err = commandEventsFileReference(command.CommandID)
		if err != nil {
			return ActiveCommandSnapshot{}, err
		}
	}
	return ActiveCommandSnapshot{
		CommandID: command.CommandID, SessionID: command.SessionID, Ordinal: command.Ordinal,
		State: command.State, ObservedAt: observedAt.UTC(), AvailableEventSequence: available,
		EventsFile: file, OutputComplete: false, OutputTruncated: command.OutputTruncated,
		EventBytes: append([]byte(nil), data...),
	}, nil
}

func commandEventsFileReference(commandID domain.CommandID) (string, error) {
	if _, ok := safeRequestID(string(commandID)); !ok {
		return "", fmt.Errorf("%w: command ID cannot be used as file reference", ErrEventProjection)
	}
	return "events/" + string(commandID) + ".ndjson", nil
}
