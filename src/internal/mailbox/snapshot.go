package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"remote-session-runner/src/internal/domain"
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
	events, err := p.Authority.ListCommandEvents(ctx, validatedID)
	if err != nil {
		return ActiveCommandSnapshot{}, err
	}
	command, err := p.Authority.GetCommand(ctx, validatedID)
	if err != nil {
		return ActiveCommandSnapshot{}, err
	}
	if command.State.IsTerminal() {
		return ActiveCommandSnapshot{}, ErrActiveSnapshotTerminal
	}
	if len(events) == 0 {
		return ActiveCommandSnapshot{}, fmt.Errorf("%w: active command has no retained events", ErrEventProjection)
	}
	data, err := RenderCommandEvents(command, events)
	if err != nil {
		return ActiveCommandSnapshot{}, err
	}
	file, err := commandEventsFileReference(validatedID)
	if err != nil {
		return ActiveCommandSnapshot{}, err
	}
	clock := p.Clock
	if clock == nil {
		clock = time.Now
	}
	return ActiveCommandSnapshot{
		CommandID:              command.CommandID,
		SessionID:              command.SessionID,
		Ordinal:                command.Ordinal,
		State:                  command.State,
		ObservedAt:             clock().UTC(),
		AvailableEventSequence: events[len(events)-1].Sequence,
		EventsFile:             file,
		OutputComplete:         false,
		OutputTruncated:        command.OutputTruncated,
		EventBytes:             append([]byte(nil), data...),
	}, nil
}

func commandEventsFileReference(commandID domain.CommandID) (string, error) {
	if _, ok := safeRequestID(string(commandID)); !ok {
		return "", fmt.Errorf("%w: command ID cannot be used as file reference", ErrEventProjection)
	}
	return "events/" + string(commandID) + ".ndjson", nil
}
