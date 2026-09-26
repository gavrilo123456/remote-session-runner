package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP015CommandIdempotencyReplaysConflictsAndSurvivesReopen(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p015-command.db"
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p015-command", "key-p015-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	hash := p014Hash(t, `{"operation":"submit_command","session_id":"session-p015-command","script":"printf ok"}`)
	firstInput := CommandAcceptance{CommandID: "command-p015-first", SessionID: created.SessionID, IdempotencyKey: "key-p015-command", RequestHash: hash, Script: "printf ok", Timeout: time.Second}
	first, duplicate, err := store.AcceptCommand(context.Background(), firstInput)
	if err != nil || duplicate {
		t.Fatalf("first command = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	retry := firstInput
	retry.CommandID = "command-p015-different-client-id"
	replayed, duplicate, err := store.AcceptCommand(context.Background(), retry)
	if err != nil || !duplicate || replayed.CommandID != first.CommandID || replayed.Ordinal != first.Ordinal {
		t.Fatalf("same-key replay = %+v duplicate=%v err=%v", replayed, duplicate, err)
	}
	var commands, events int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_commands WHERE session_id = ?", string(created.SessionID)).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_command_events WHERE command_id = ?", string(first.CommandID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if commands != 1 || events != 1 {
		t.Fatalf("replay rows commands=%d events=%d", commands, events)
	}
	conflict := retry
	conflict.RequestHash = p014Hash(t, `{"operation":"submit_command","session_id":"session-p015-command","script":"printf changed"}`)
	conflict.Script = "printf changed"
	if _, _, err := store.AcceptCommand(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed same-key command error = %v", err)
	}
	lookup, found, err := store.LookupIdempotency(context.Background(), created.Controller, submitCommandOperation, "key-p015-command")
	if err != nil || !found || lookup.ResourceID != string(first.CommandID) || lookup.Hash.String() != hash.String() {
		t.Fatalf("lookup = %+v found=%v err=%v", lookup, found, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRetry := retry
	reopenedRetry.CommandID = "command-p015-after-reopen"
	reopenedCommand, duplicate, err := reopened.AcceptCommand(context.Background(), reopenedRetry)
	if err != nil || !duplicate || reopenedCommand.CommandID != first.CommandID {
		t.Fatalf("reopened replay = %+v duplicate=%v err=%v", reopenedCommand, duplicate, err)
	}
}

func TestP015GenericIdempotencyExpiresWithoutReleasingResource(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/generic.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 26, 19, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	hash := p014Hash(t, `{"operation":"submit_command","session_id":"s","script":"x"}`)
	first, duplicate, err := store.EnsureIdempotency(context.Background(), controller, "future_cancel", "key-generic", hash, "resource-1", time.Minute)
	if err != nil || duplicate || first.ResourceID != "resource-1" {
		t.Fatalf("first generic record = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	replay, duplicate, err := store.EnsureIdempotency(context.Background(), controller, "future_cancel", "key-generic", hash, "resource-2", time.Minute)
	if err != nil || !duplicate || replay.ResourceID != "resource-1" {
		t.Fatalf("generic replay = %+v duplicate=%v err=%v", replay, duplicate, err)
	}
	if _, _, err := store.EnsureIdempotency(context.Background(), controller, "future_cancel", "key-generic", p014Hash(t, `{"operation":"submit_command","session_id":"s","script":"changed"}`), "resource-3", time.Minute); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("generic conflict = %v", err)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if _, found, err := store.LookupIdempotency(context.Background(), controller, "future_cancel", "key-generic"); err != nil || found {
		t.Fatalf("expired generic lookup = found %v err %v", found, err)
	}
	reused, duplicate, err := store.EnsureIdempotency(context.Background(), controller, "future_cancel", "key-generic", hash, "resource-4", time.Minute)
	if err != nil || duplicate || reused.ResourceID != "resource-4" {
		t.Fatalf("reused generic key = %+v duplicate=%v err=%v", reused, duplicate, err)
	}
}

func TestP015ConcurrentSameKeyCreatesOneCommand(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/race.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p015-race", "key-p015-race-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	hash := p014Hash(t, `{"operation":"submit_command","session_id":"session-p015-race","script":"echo race"}`)
	const attempts = 12
	start := make(chan struct{})
	results := make(chan struct {
		command   CommandRecord
		duplicate bool
		err       error
	}, attempts)
	var wait sync.WaitGroup
	for i := 0; i < attempts; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			command, duplicate, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: domain.CommandID(fmt.Sprintf("command-p015-race-%02d", i)), SessionID: created.SessionID, IdempotencyKey: "key-p015-race-command", RequestHash: hash, Script: "echo race", Timeout: time.Second})
			results <- struct {
				command   CommandRecord
				duplicate bool
				err       error
			}{command, duplicate, err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var first CommandRecord
	accepted, duplicates := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.duplicate {
			duplicates++
		} else {
			accepted++
			first = result.command
		}
		if first.CommandID != "" && result.command.CommandID != first.CommandID {
			t.Fatalf("concurrent command IDs differ: %s vs %s", result.command.CommandID, first.CommandID)
		}
	}
	if accepted != 1 || duplicates != attempts-1 {
		t.Fatalf("accepted=%d duplicates=%d", accepted, duplicates)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_commands WHERE session_id = ?", string(created.SessionID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("command rows = %d, want 1", count)
	}
}
