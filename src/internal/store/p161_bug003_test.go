package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP161RemoteStatusFailureAttemptCountPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "p161-attempts.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := &p019Clock{value: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	authority, err := NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	controller := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	input := p151RemoteRunIntent(t, "intent-p161-attempts", "job-p161-attempts", "session-p161-attempts", "command-p161-attempts", "key-p161-attempts", controller)
	if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentDispatching, "dispatching"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentAccepted, "target_accepted"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}

	first, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, input.IntentID, RemoteStatusFailureCodeUnavailable)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if first.RemoteStatusFailureAttempts != 1 || first.RemoteStatusFailureAt == nil || first.RemoteStatusFailureCode != RemoteStatusFailureCodeUnavailable || first.DeliveryState != LocalIntentAccepted {
		_ = database.Close()
		t.Fatalf("first durable status failure marker=%+v, want count=1 on accepted remote run", first)
	}
	firstObserved := *first.RemoteStatusFailureAt

	clock.Advance(time.Hour)
	second, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, input.IntentID, RemoteStatusFailureCodeUnavailable)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if second.RemoteStatusFailureAttempts != 2 || second.RemoteStatusFailureAt == nil || !second.RemoteStatusFailureAt.Equal(firstObserved) || second.DeliveryState != LocalIntentAccepted {
		_ = database.Close()
		t.Fatalf("repeated durable status failure marker=%+v, want original deadline anchor and count=2", second)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err = NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := authority.GetLocalIntent(ctx, input.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.RemoteStatusFailureAttempts != 2 || restarted.RemoteStatusFailureAt == nil || !restarted.RemoteStatusFailureAt.Equal(firstObserved) || restarted.RemoteStatusFailureCode != RemoteStatusFailureCodeUnavailable || restarted.DeliveryState != LocalIntentAccepted {
		t.Fatalf("restarted durable status failure marker=%+v, want original marker and count=2", restarted)
	}
}

func TestP161Migration29BackfillsExistingRemoteStatusFailureAttemptCount(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "p161-v28.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetMaxOpenConns(1)
	for _, migration := range migrations[:28] {
		if _, err := legacy.ExecContext(ctx, migration.sql); err != nil {
			_ = legacy.Close()
			t.Fatalf("apply v%d migration: %v", migration.version, err)
		}
	}
	when := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	whenText := formatStoredTime(when)
	for _, migration := range migrations[:28] {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, migration.version, migration.name, migrationChecksum(migration), whenText); err != nil {
			_ = legacy.Close()
			t.Fatalf("record v%d migration: %v", migration.version, err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `PRAGMA user_version = 28`); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}

	controller := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	input := p151RemoteRunIntent(t, "intent-p161-migration", "job-p161-migration", "session-p161-migration", "command-p161-migration", "key-p161-migration", controller)
	p161InsertAcceptedRemoteRunV28(t, ctx, legacy, input, when)
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO local_remote_status_failures(intent_id, first_observed_at, reason)
VALUES (?, ?, ?)
`, string(input.IntentID), whenText, RemoteStatusFailureCodeUnavailable); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert v28 remote-status marker: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v28 BUG-003 database: %v", err)
	}
	defer database.Close()
	if version := readUserVersion(t, database); version != CurrentSchemaVersion {
		t.Fatalf("schema version=%d, want %d", version, CurrentSchemaVersion)
	}
	var migrationName string
	if err := database.QueryRowContext(ctx, `SELECT name FROM runner_schema_migrations WHERE version = 29`).Scan(&migrationName); err != nil || migrationName != "bug003_reconciliation_attempts" {
		t.Fatalf("migration 29 ledger name=%q err=%v", migrationName, err)
	}
	authority, err := NewAuthorityStoreWithClock(database, func() time.Time { return when })
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := authority.GetLocalIntent(ctx, input.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.RemoteStatusFailureAttempts != 1 || migrated.RemoteStatusFailureAt == nil || !migrated.RemoteStatusFailureAt.Equal(when) || migrated.RemoteStatusFailureCode != RemoteStatusFailureCodeUnavailable || migrated.DeliveryState != LocalIntentAccepted {
		t.Fatalf("migrated v28 status failure marker=%+v, want preserved marker with count=1", migrated)
	}
}

func p161InsertAcceptedRemoteRunV28(t *testing.T, ctx context.Context, database *sql.DB, input LocalIntentCreate, when time.Time) {
	t.Helper()
	scriptHash := sha256.Sum256(input.ScriptBytes)
	if _, err := database.ExecContext(ctx, `
INSERT INTO local_intents (
 intent_id, operation, resource_id, session_id, command_id, job_id,
 target_kind, target_profile, environment, controller_type, controller_id,
 source_mode, source_repository_alias, source_requested_revision, source_path,
 request_hash_version, request_hash, idempotency_key, payload_json,
 script_bytes, script_sha256, delivery_state, reason, lease_owner,
 attempt_count, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'accepted', '', '', 0, ?, ?)
`, string(input.IntentID), input.Operation, input.ResourceID, string(input.SessionID), string(input.CommandID), string(input.JobID),
		string(input.Target.Kind()), input.Target.Profile(), input.Environment, string(input.Controller.Type()), string(input.Controller.ID()),
		string(input.Source.Mode()), input.Source.RepositoryAlias(), input.Source.RequestedRevision(), input.Source.Path(),
		input.RequestHash.Version(), input.RequestHash.SHA256(), input.IdempotencyKey, input.PayloadJSON,
		input.ScriptBytes, scriptHash[:], formatStoredTime(when), formatStoredTime(when)); err != nil {
		t.Fatalf("insert accepted v28 remote run: %v", err)
	}
}
