package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestBUG009MigrationLeavesHistoricalReleasedLostPairsOutsideNewWorkList(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug009-v31.db")
	pBUG009BuildV32Database(t, ctx, path)
	database, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	authority, err := NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	// The current scheduler consults the controlled-restart plan table before
	// it claims a command. This fixture intentionally begins at the v32
	// boundary to create historical lost state, so add only that mechanical
	// table for the current scheduler and remove it again before modelling the
	// actual v31 upgrade below. It is never recorded in the migration ledger.
	if _, err := database.ExecContext(ctx, controlledRestartPlanSQL); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	sessionID, commandID := pStalledRecoveryLostPair(t, authority, "bug009-v31")
	if err := authority.ConfirmLostRuntimeRecovery(ctx, sessionID, commandID); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 1 {
		_ = database.Close()
		t.Fatalf("new-schema pending finalizations=%+v err=%v, want one", pending, err)
	}

	// Model a database that reached a released terminal-lost state before this
	// migration existed. The new work list is intentionally only for the new
	// two-stage Linux recovery transaction, so activation must not reinterpret
	// old ordinary-reconciliation records as pending finalization work.
	if _, err := database.ExecContext(ctx, `DROP TABLE exec_controlled_restart_plan_lost_pairs`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE exec_controlled_restart_plans`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE exec_lost_runtime_recovery_finalizations`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM runner_schema_migrations WHERE version = 32`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 31`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	migratedAuthority, err := NewAuthorityStore(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := migratedAuthority.ListPendingLostRuntimeRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("historical released pair entered finalization work list=%+v err=%v", pending, err)
	}
}

// pBUG009BuildV32Database constructs the actual migration-32 boundary before
// it seeds an old-style released pair. Opening a current database and deleting
// ledger rows would leave future schema artifacts behind and cannot prove an
// upgrade behaves correctly.
func pBUG009BuildV32Database(t *testing.T, ctx context.Context, path string) {
	t.Helper()
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
	database, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	for _, migration := range migrations[:32] {
		if _, err := database.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply v%d migration: %v", migration.version, err)
		}
	}
	when := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	for _, migration := range migrations[:32] {
		if _, err := database.ExecContext(ctx, `INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, migration.version, migration.name, migrationChecksum(migration), formatStoredTime(when)); err != nil {
			t.Fatalf("record v%d migration: %v", migration.version, err)
		}
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 32`); err != nil {
		t.Fatal(err)
	}
}
