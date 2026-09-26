package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestP058LocalIdempotencySameKeyHashReturnsOriginalAcrossRestart(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p058-retry.db"
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := p057SubmitIntent(t, "intent-p058-original", "session-p058-retry", "command-p058-retry", "key-p058-retry", 1, "printf 'same bytes\\n'")
	first, duplicate, err := store.AcceptLocalIntent(context.Background(), input)
	if err != nil || duplicate {
		t.Fatalf("first accept = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	retry, duplicate, err := store.AcceptLocalIntent(context.Background(), input)
	if err != nil || !duplicate || retry.IntentID != first.IntentID || retry.CreatedAt != first.CreatedAt {
		t.Fatalf("same-key retry = %+v duplicate=%v err=%v", retry, duplicate, err)
	}
	var intentCount, bindingCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents").Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM local_idempotency").Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 1 || bindingCount != 1 {
		t.Fatalf("same-key retry counts intents=%d bindings=%d, want 1/1", intentCount, bindingCount)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return now.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRetry, duplicate, err := reopened.AcceptLocalIntent(context.Background(), input)
	if err != nil || !duplicate || reopenedRetry.IntentID != first.IntentID || string(reopenedRetry.ScriptBytes) != string(first.ScriptBytes) {
		t.Fatalf("post-restart same-key retry = %+v duplicate=%v err=%v", reopenedRetry, duplicate, err)
	}
}

func TestP058LocalIdempotencyChangedHashConflictsBeforeSecondIntent(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p058-conflict.db"
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first := p057SubmitIntent(t, "intent-p058-conflict-1", "session-p058-conflict", "command-p058-conflict-1", "key-p058-conflict", 1, "echo first")
	if _, err := store.CreateLocalIntent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStore(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	changed := p057SubmitIntent(t, "intent-p058-conflict-2", "session-p058-conflict", "command-p058-conflict-2", first.IdempotencyKey, 2, "echo changed")
	if _, duplicate, err := reopened.AcceptLocalIntent(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) || duplicate {
		t.Fatalf("changed same-key accept = duplicate %v err %v, want conflict", duplicate, err)
	}
	var intentCount, bindingCount int
	if err := reopenedDB.QueryRow("SELECT COUNT(*) FROM local_intents").Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if err := reopenedDB.QueryRow("SELECT COUNT(*) FROM local_idempotency").Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 1 || bindingCount != 1 {
		t.Fatalf("conflict inserted intents=%d bindings=%d, want 1/1", intentCount, bindingCount)
	}
}

func TestP058LocalIdempotencyExpiryAllowsNewBinding(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p058-expiry.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first := p057SubmitIntent(t, "intent-p058-expiry-1", "session-p058-expiry", "command-p058-expiry-1", "key-p058-expiry", 1, "echo first")
	first.IdempotencyRetention = time.Hour
	created, err := store.CreateLocalIntent(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Nanosecond)
	second := p057SubmitIntent(t, "intent-p058-expiry-2", "session-p058-expiry", "command-p058-expiry-2", first.IdempotencyKey, 2, "echo second")
	second.IdempotencyRetention = time.Hour
	replaced, duplicate, err := store.AcceptLocalIntent(context.Background(), second)
	if err != nil || duplicate || replaced.IntentID != second.IntentID {
		t.Fatalf("expired-key accept = %+v duplicate=%v err=%v", replaced, duplicate, err)
	}
	if replaced.IntentID == created.IntentID {
		t.Fatal("expired key returned original intent")
	}
	var bindingCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_idempotency WHERE idempotency_key = ?", first.IdempotencyKey).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 {
		t.Fatalf("expired key binding count = %d, want 1", bindingCount)
	}
}

func TestP058LocalIdempotencyMigrationAndForeignKey(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p058-schema.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want current version %d", version, CurrentSchemaVersion)
	}
	if _, err := db.Exec(`INSERT INTO local_idempotency(controller_type, controller_id, operation, idempotency_key, request_hash_version, request_hash, intent_id, resource_id, created_at, expires_at) VALUES ('queued_mac', 'missing', 'submit_command', 'key', 1, zeroblob(32), 'missing-intent', 'command', '2026-09-27T00:00:00Z', '2026-09-28T00:00:00Z')`); err == nil {
		t.Fatal("local idempotency accepted an orphan intent")
	}
}
