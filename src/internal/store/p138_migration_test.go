package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
)

func TestP138D14MigratesV22AuditRowsAndAddsCleanupFailureRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "authority.db")
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
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(schemaMigrationsSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(auditRecordsSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO runner_audit_records (
		id, principal_type, principal_id, ingress, action, outcome, reason_code, occurred_at
	) VALUES (7, 'direct_mtls', 'p138-existing', 'direct_mtls', 'create', 'allowed', '', '2026-09-29T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:22] {
		if _, err := legacy.Exec(`INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES(?, ?, ?, ?)`,
			migration.version, migration.name, migrationChecksum(migration), time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.Exec("PRAGMA user_version = 22"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("migrate v22 database: %v", err)
	}
	defer db.Close()
	if version := readUserVersion(t, db); version != 23 {
		t.Fatalf("migrated schema version=%d, want 23", version)
	}
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "p138-cleanup-auditor")
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := domain.NewSessionID("session-p138-migration-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := audit.NewRecord(principal, audit.IngressInternal, audit.ActionRuntimeCleanup, audit.OutcomeFailed)
	cleanup.SessionID = sessionID
	cleanup.ReasonCode = audit.ReasonRuntimeCleanupUnconfirmed
	if err := authority.RecordAudit(context.Background(), cleanup); err != nil {
		t.Fatalf("record runtime cleanup failure after migration: %v", err)
	}
	records, err := authority.ListAuditRecords(context.Background(), 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("migrated audit rows=%+v err=%v, want preserved and new records", records, err)
	}
	if records[0].ID != 7 || records[0].Principal.ID() != "p138-existing" {
		t.Fatalf("prior audit row was not preserved: %+v", records[0])
	}
	if records[1].Action != audit.ActionRuntimeCleanup || records[1].Outcome != audit.OutcomeFailed || records[1].ReasonCode != audit.ReasonRuntimeCleanupUnconfirmed || records[1].SessionID != sessionID {
		t.Fatalf("migrated cleanup audit row=%+v", records[1])
	}
	if _, err := db.Exec("UPDATE runner_audit_records SET action = 'close' WHERE id = 7"); err == nil {
		t.Fatal("audit append-only trigger was not restored by migration")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM runner_schema_migrations").Scan(&count); err != nil || count != 23 {
		t.Fatalf("migration ledger count=%d err=%v, want 23", count, err)
	}
}
