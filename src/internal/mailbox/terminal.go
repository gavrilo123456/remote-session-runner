package mailbox

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	ErrTerminalSnapshot            = errors.New("invalid terminal command snapshot")
	ErrTerminalSnapshotUnavailable = errors.New("terminal command output is unavailable")
)

// TerminalCommandSnapshot is the truthful terminal status and contiguous
// output prefix for one command. A zero available cursor means no event file
// is advertised (for example after retention expiry).
type TerminalCommandSnapshot struct {
	CommandID               domain.CommandID
	SessionID               domain.SessionID
	Ordinal                 int64
	State                   domain.CommandState
	ExitCode                *int
	StdoutPreview           string
	StderrPreview           string
	FinalEventSequence      int64
	AvailableEventSequence  int64
	OutputComplete          bool
	OutputTruncated         bool
	OutputUnavailableReason string
	EventsFile              string
	EventBytes              []byte
}

// TerminalSnapshot reads a terminal command and its currently retained event
// prefix. It never turns an unavailable or incomplete prefix into complete
// output.
func (p EventProjector) TerminalSnapshot(ctx context.Context, commandID domain.CommandID) (TerminalCommandSnapshot, error) {
	if p.Authority == nil {
		return TerminalCommandSnapshot{}, ErrEventProjectionConfiguration
	}
	command, events, err := p.Authority.GetCommandWithEvents(ctx, commandID)
	if err != nil {
		return TerminalCommandSnapshot{}, err
	}
	if !command.State.IsTerminal() {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: command state %q is not terminal", ErrTerminalSnapshot, command.State)
	}
	return RenderTerminalCommandSnapshot(command, events)
}

// RenderTerminalCommandSnapshot validates terminal metadata against a
// contiguous retained event prefix and returns the exact status that a
// get_command response may advertise.
func RenderTerminalCommandSnapshot(command store.CommandRecord, events []store.CommandEventRecord) (TerminalCommandSnapshot, error) {
	if command.CommandID == "" || command.Ordinal < 1 || !command.State.IsTerminal() || command.FinalEventSequence == nil || *command.FinalEventSequence < 1 {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: terminal identity or final sequence", ErrTerminalSnapshot)
	}
	finalSequence := *command.FinalEventSequence
	reason := command.OutputUnavailableReason
	if reason != "" && reason != "capture_boundary_unconfirmed" && reason != "remote_event_gap" && reason != "retention_expired" {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: unknown unavailable reason %q", ErrTerminalSnapshot, reason)
	}
	if !command.OutputComplete && reason == "" {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: incomplete output has no reason", ErrTerminalSnapshot)
	}
	if reason == "retention_expired" && len(events) != 0 {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: retention-expired output still has events", ErrTerminalSnapshot)
	}
	var data []byte
	available := int64(0)
	file := ""
	if len(events) > 0 {
		var err error
		data, err = RenderCommandEvents(command, events)
		if err != nil {
			return TerminalCommandSnapshot{}, err
		}
		available = events[len(events)-1].Sequence
		file, err = commandEventsFileReference(command.CommandID)
		if err != nil {
			return TerminalCommandSnapshot{}, err
		}
	}
	if reason == "retention_expired" && available != 0 {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: retention cursor must be zero", ErrTerminalSnapshot)
	}
	if reason == "remote_event_gap" {
		if available >= finalSequence {
			return TerminalCommandSnapshot{}, fmt.Errorf("%w: gap prefix passes final sequence", ErrTerminalSnapshot)
		}
	} else if reason != "retention_expired" && available != finalSequence {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: available sequence %d does not match final %d", ErrTerminalSnapshot, available, finalSequence)
	}
	if command.OutputComplete && reason != "" {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: complete output has unavailable reason %q", ErrTerminalSnapshot, reason)
	}
	if command.OutputComplete && available != finalSequence {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: complete output has incomplete event prefix", ErrTerminalSnapshot)
	}
	if reason == "capture_boundary_unconfirmed" && (available != finalSequence || len(events) == 0 || events[len(events)-1].Type != "command_lost") {
		return TerminalCommandSnapshot{}, fmt.Errorf("%w: capture boundary is not closed by command_lost", ErrTerminalSnapshot)
	}
	if reason != "remote_event_gap" && reason != "retention_expired" {
		if len(events) == 0 || events[len(events)-1].Sequence != finalSequence {
			return TerminalCommandSnapshot{}, fmt.Errorf("%w: final event is unavailable", ErrTerminalSnapshot)
		}
		expected, ok := terminalMailboxEventType(command.State)
		if !ok || events[len(events)-1].Type != expected {
			return TerminalCommandSnapshot{}, fmt.Errorf("%w: final event type does not match command state", ErrTerminalSnapshot)
		}
	}
	if reason == "retention_expired" {
		data = nil
		file = ""
	}
	stdoutPreview, stderrPreview := inlineOutputPreviews(events)
	if reason == "retention_expired" {
		stdoutPreview, stderrPreview = "", ""
	}
	var exitCode *int
	if command.ExitCode != nil {
		value := *command.ExitCode
		exitCode = &value
	}
	return TerminalCommandSnapshot{
		CommandID:               command.CommandID,
		SessionID:               command.SessionID,
		Ordinal:                 command.Ordinal,
		State:                   command.State,
		ExitCode:                exitCode,
		StdoutPreview:           stdoutPreview,
		StderrPreview:           stderrPreview,
		FinalEventSequence:      finalSequence,
		AvailableEventSequence:  available,
		OutputComplete:          command.OutputComplete,
		OutputTruncated:         command.OutputTruncated,
		OutputUnavailableReason: reason,
		EventsFile:              file,
		EventBytes:              append([]byte(nil), data...),
	}, nil
}

func terminalMailboxEventType(state domain.CommandState) (string, bool) {
	switch state {
	case domain.CommandStateSucceeded:
		return "command_succeeded", true
	case domain.CommandStateFailed:
		return "command_failed", true
	case domain.CommandStateCancelled:
		return "command_cancelled", true
	case domain.CommandStateTimedOut:
		return "command_timed_out", true
	case domain.CommandStateRejected:
		return "command_rejected", true
	case domain.CommandStateLost:
		return "command_lost", true
	default:
		return "", false
	}
}

// FullAnswer reports the detailed-design triple condition. It is deliberately
// independent of command state: a terminal shell failure can still have a
// complete, untruncated answer, while a lost or truncated result cannot.
func (s TerminalCommandSnapshot) FullAnswer(requestState string) bool {
	return requestState == "complete" && s.OutputComplete && !s.OutputTruncated && s.AvailableEventSequence > 0 && s.AvailableEventSequence == s.FinalEventSequence
}
