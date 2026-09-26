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
	first := RemoteEventRecord{CommandID: commandID, Sequence: 1, Type: "stdout", Payload: []byte("one"), ByteCount: 3, OccurredAt: when}
	second := RemoteEventRecord{CommandID: commandID, Sequence: 2, Type: "command_succeeded", OccurredAt: when.Add(time.Second)}
	result, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{first, second})
	if err != nil || result.LastSequence != 2 || result.Mirrored != 2 || result.Duplicates != 0 {
		t.Fatalf("initial mirror = %+v, %v", result, err)
	}
	duplicate, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{first, second})
	if err != nil || duplicate.LastSequence != 2 || duplicate.Mirrored != 0 || duplicate.Duplicates != 2 {
		t.Fatalf("duplicate mirror = %+v, %v", duplicate, err)
	}
	conflict := first
	conflict.Payload = []byte("two")
	conflict.ByteCount = 3
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{conflict}); !errors.Is(err, ErrRemoteEventConflict) {
		t.Fatalf("conflicting duplicate error = %v", err)
	}
	gap := second
	gap.Sequence = 4
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{gap}); !errors.Is(err, ErrRemoteEventGap) {
		t.Fatalf("gap error = %v", err)
	}
	cursor, err := authority.GetRemoteEventCursor(context.Background(), commandID)
	if err != nil || cursor != 2 {
		t.Fatalf("cursor after rejected batches = %d, %v", cursor, err)
	}
	third := second
	third.Sequence = 3
	third.OccurredAt = when.Add(2 * time.Second)
	if _, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{third}); err != nil {
		t.Fatal(err)
	}
	events, err := authority.ListRemoteEvents(context.Background(), commandID, 0)
	if err != nil || len(events) != 3 || events[2].Sequence != 3 || string(events[0].Payload) != "one" {
		t.Fatalf("mirrored events = %+v, %v", events, err)
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
	_, err := authority.MirrorRemoteEvents(context.Background(), []RemoteEventRecord{{CommandID: commandID, Sequence: 2, Type: "command_succeeded", OccurredAt: when.Add(time.Second)}})
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
