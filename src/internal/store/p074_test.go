package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP074RemoteEventMirrorDeduplicatesAndRequiresContiguousSequences(t *testing.T) {
	authority := p074Store(t)
	commandID := domain.CommandID("command-p074-mirror")
	when := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	queued := RemoteEventRecord{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when}
	started := RemoteEventRecord{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)}
	first := RemoteEventRecord{CommandID: commandID, Sequence: 3, Type: "stdout", Payload: []byte("one"), ByteCount: 3, OccurredAt: when.Add(2 * time.Second)}
	second := RemoteEventRecord{CommandID: commandID, Sequence: 4, Type: "command_succeeded", OccurredAt: when.Add(3 * time.Second)}
	result, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{queued, started, first, second})
	if err != nil || result.LastSequence != 4 || result.Mirrored != 4 || result.Duplicates != 0 {
		t.Fatalf("initial mirror = %+v, %v", result, err)
	}
	duplicate, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{queued, started, first, second})
	if err != nil || duplicate.LastSequence != 4 || duplicate.Mirrored != 0 || duplicate.Duplicates != 4 {
		t.Fatalf("duplicate mirror = %+v, %v", duplicate, err)
	}
	conflict := first
	conflict.Payload = []byte("two")
	conflict.ByteCount = 3
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{conflict}); !errors.Is(err, ErrRemoteEventConflict) {
		t.Fatalf("conflicting duplicate error = %v", err)
	}
	gap := second
	gap.Sequence = 6
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{gap}); !errors.Is(err, ErrRemoteEventGap) {
		t.Fatalf("gap error = %v", err)
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 4 {
		t.Fatalf("cursor after rejected batches = %d, %v", cursor, err)
	}
	third := second
	third.Sequence = 5
	third.OccurredAt = when.Add(4 * time.Second)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{third}); !errors.Is(err, ErrRemoteEventTerminal) {
		t.Fatalf("append after terminal error = %v", err)
	}
	events, err := authority.ListRemoteEvents(context.Background(), commandID, 0)
	if err != nil || len(events) != 4 || events[3].Sequence != 4 || string(events[2].Payload) != "one" {
		t.Fatalf("mirrored events = %+v, %v", events, err)
	}
}

func TestP074RemoteEventMirrorRejectsTerminalBeforeLaterEventInSameBatch(t *testing.T) {
	authority := p074Store(t)
	commandID := domain.CommandID("command-p074-terminal-batch")
	when := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	batch := []RemoteEventRecord{
		{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
		{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
		{CommandID: commandID, Sequence: 3, Type: "command_succeeded", OccurredAt: when.Add(2 * time.Second)},
		{CommandID: commandID, Sequence: 4, Type: "stdout", Payload: []byte("late"), ByteCount: 4, OccurredAt: when.Add(3 * time.Second)},
	}
	if _, err := authority.MirrorRemoteEvents(context.Background(), batch); !errors.Is(err, ErrRemoteEventTerminal) {
		t.Fatalf("terminal-in-batch error = %v", err)
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 0 {
		t.Fatalf("cursor after terminal batch rejection = %d, %v", cursor, err)
	}
}

func TestP074RemoteEventMirrorRollsBackEventAndCursorTogether(t *testing.T) {
	authority := p074Store(t)
	commandID := domain.CommandID("command-p074-atomic")
	when := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when}}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.db.Exec(`CREATE TRIGGER p074_fail_cursor BEFORE UPDATE OF last_sequence ON local_remote_event_cursors BEGIN SELECT RAISE(ABORT, 'injected cursor commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	defer authority.db.Exec(`DROP TRIGGER p074_fail_cursor`)
	_, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)}})
	if err == nil {
		t.Fatal("cursor failure unexpectedly committed")
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 1 {
		t.Fatalf("cursor after rollback = %d, %v", cursor, err)
	}
	events, err := authority.ListRemoteEvents(context.Background(), commandID, 0)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("events after rollback = %+v, %v", events, err)
	}
}

func TestP074RemoteEventMirrorEnforcesLifecycleGrammar(t *testing.T) {
	when := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		events []RemoteEventRecord
		wantOK bool
	}{
		{
			name: "output-before-start",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-output-before-start", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-output-before-start", Sequence: 2, Type: "stdout", Payload: []byte("no"), ByteCount: 2, OccurredAt: when.Add(time.Second)},
			},
		},
		{
			name: "succeeded-without-start",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-success-without-start", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-success-without-start", Sequence: 2, Type: "command_succeeded", OccurredAt: when.Add(time.Second)},
			},
		},
		{
			name: "duplicate-start",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-duplicate-start", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-duplicate-start", Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
				{CommandID: "command-p074-duplicate-start", Sequence: 3, Type: "command_started", OccurredAt: when.Add(2 * time.Second)},
			},
		},
		{
			name: "duplicate-truncation",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-duplicate-truncation", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-duplicate-truncation", Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
				{CommandID: "command-p074-duplicate-truncation", Sequence: 3, Type: "output_truncated", OccurredAt: when.Add(2 * time.Second)},
				{CommandID: "command-p074-duplicate-truncation", Sequence: 4, Type: "output_truncated", OccurredAt: when.Add(3 * time.Second)},
			},
		},
		{
			name: "rejected-after-start",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-rejected-after-start", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-rejected-after-start", Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
				{CommandID: "command-p074-rejected-after-start", Sequence: 3, Type: "command_rejected", OccurredAt: when.Add(2 * time.Second)},
			},
		},
		{
			name: "cancelled-before-start-is-valid",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-cancelled", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-cancelled", Sequence: 2, Type: "command_cancelled", OccurredAt: when.Add(time.Second)},
			},
			wantOK: true,
		},
		{
			name: "rejected-before-start-is-valid",
			events: []RemoteEventRecord{
				{CommandID: "command-p074-rejected", Sequence: 1, Type: "command_queued", OccurredAt: when},
				{CommandID: "command-p074-rejected", Sequence: 2, Type: "command_rejected", OccurredAt: when.Add(time.Second)},
			},
			wantOK: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := p074Store(t)
			_, err := authority.MirrorRemoteEvents(context.Background(), test.events)
			if test.wantOK {
				if err != nil {
					t.Fatalf("valid lifecycle rejected: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrRemoteEventBatch) {
				t.Fatalf("invalid lifecycle error=%v, want ErrRemoteEventBatch", err)
			}
		})
	}
}

func TestP074RemoteEventGapRejectsLegacyPrefixAfterTerminal(t *testing.T) {
	authority := p074Store(t)
	commandID := domain.CommandID("command-p074-legacy-terminal")
	when := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	legacy := []RemoteEventRecord{
		{CommandID: commandID, Sequence: 1, Type: "command_queued", OccurredAt: when},
		{CommandID: commandID, Sequence: 2, Type: "command_started", OccurredAt: when.Add(time.Second)},
		{CommandID: commandID, Sequence: 3, Type: "command_succeeded", OccurredAt: when.Add(2 * time.Second)},
		{CommandID: commandID, Sequence: 4, Type: "stdout", Payload: []byte("late"), ByteCount: 4, OccurredAt: when.Add(3 * time.Second)},
	}
	for _, event := range legacy {
		payload := event.Payload
		if payload == nil {
			payload = []byte{}
		}
		if _, err := authority.db.Exec(`INSERT INTO local_remote_events (command_id, sequence, event_type, payload, byte_count, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`, string(event.CommandID), event.Sequence, event.Type, payload, event.ByteCount, formatStoredTime(event.OccurredAt)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := authority.db.Exec(`INSERT INTO local_remote_event_cursors (command_id, last_sequence, updated_at) VALUES (?, ?, ?)`, string(commandID), 4, formatStoredTime(when.Add(4*time.Second))); err != nil {
		t.Fatal(err)
	}
	_, err := authority.RecordRemoteEventGap(context.Background(), RemoteEventGapRecord{
		CommandID: commandID, MissingFrom: 5, MissingTo: 5, AvailableSequence: 4, FinalSequence: 5,
		TerminalState: domain.CommandStateSucceeded, OutputUnavailableReason: "remote_event_gap", ConfirmedAt: when.Add(5 * time.Second),
	})
	if !errors.Is(err, ErrRemoteEventTerminal) {
		t.Fatalf("legacy terminal prefix gap error=%v, want ErrRemoteEventTerminal", err)
	}
}

func p074Store(t *testing.T) *AuthorityStore {
	t.Helper()
	db, err := Open(context.Background(), testfixture.New(t).Path()+"/state/p074.db")
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
