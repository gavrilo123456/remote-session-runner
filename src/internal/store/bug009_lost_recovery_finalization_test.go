package store

import (
	"context"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/testfixture"
)

func TestBUG009MigrationLeavesHistoricalReleasedLostPairsOutsideNewWorkList(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug009-v31.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
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
