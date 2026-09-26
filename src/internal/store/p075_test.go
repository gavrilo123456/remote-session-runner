package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP075RemoteEventGapPersistsTerminalConfirmationWithoutCursorJump(t *testing.T) {
	authority := p075Store(t)
	commandID := domain.CommandID("command-p075-gap")
	when := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when}}); err != nil {
		t.Fatal(err)
	}
	input := RemoteEventGapRecord{
		CommandID: commandID, MissingFrom: 2, MissingTo: 4, AvailableSequence: 1,
		FinalSequence: 4, TerminalState: domain.CommandStateSucceeded,
		OutputComplete: false, OutputUnavailableReason: "remote_event_gap", ConfirmedAt: when.Add(time.Second),
	}
	recorded, err := authority.RecordRemoteEventGap(context.Background(), input)
	if err != nil || recorded.MissingFrom != 2 || recorded.MissingTo != 4 || recorded.FinalSequence != 4 {
		t.Fatalf("recorded gap = %+v, %v", recorded, err)
	}
	duplicate, err := authority.RecordRemoteEventGap(context.Background(), input)
	if err != nil || duplicate.MissingFrom != input.MissingFrom {
		t.Fatalf("idempotent gap = %+v, %v", duplicate, err)
	}
	conflict := input
	conflict.TerminalState = domain.CommandStateFailed
	if _, err := authority.RecordRemoteEventGap(context.Background(), conflict); !errors.Is(err, ErrRemoteGapConflict) {
		t.Fatalf("conflicting gap error = %v", err)
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 1 {
		t.Fatalf("gap advanced cursor = %d, %v", cursor, err)
	}
	read, err := authority.GetRemoteEventGap(context.Background(), commandID)
	if err != nil || read.TerminalState != domain.CommandStateSucceeded || read.OutputComplete || read.OutputUnavailableReason != "remote_event_gap" {
		t.Fatalf("read gap = %+v, %v", read, err)
	}
}

func TestP075RemoteEventGapRejectsUnknownOrNonTerminalConfirmation(t *testing.T) {
	authority := p075Store(t)
	base := RemoteEventGapRecord{CommandID: domain.CommandID("command-p075-invalid"), MissingFrom: 1, MissingTo: 2, AvailableSequence: 0, FinalSequence: 2, TerminalState: domain.CommandStateRunning, OutputComplete: false, OutputUnavailableReason: "remote_event_gap", ConfirmedAt: time.Now().UTC()}
	if _, err := authority.RecordRemoteEventGap(context.Background(), base); !errors.Is(err, ErrRemoteGapRecord) {
		t.Fatalf("non-terminal gap error = %v", err)
	}
	base.TerminalState = domain.CommandStateSucceeded
	base.AvailableSequence = 1
	base.MissingFrom = 3
	if _, err := authority.RecordRemoteEventGap(context.Background(), base); !errors.Is(err, ErrRemoteGapRecord) {
		t.Fatalf("non-contiguous gap error = %v", err)
	}
}

func p075Store(t *testing.T) *AuthorityStore {
	t.Helper()
	db, err := Open(context.Background(), testfixture.New(t).Path()+"/state/p075.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}
