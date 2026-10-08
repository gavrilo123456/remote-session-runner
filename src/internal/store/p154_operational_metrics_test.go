package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestP154MailboxBacklogByInboxKeepsConfiguredNamespacesSeparate(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), filepath.Join(root.Path(), "state", "p154-metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, mailbox, state string
	}{
		{"mbx-p154-default-accepted", DefaultMailboxID, "accepted"},
		{"mbx-p154-default-complete", DefaultMailboxID, "complete"},
		{"mbx-p154-analytics-accepted-a", "analytics", "accepted"},
		{"mbx-p154-analytics-accepted-b", "analytics", "accepted"},
		{"mbx-p154-unconfigured", "retired", "accepted"},
	} {
		if _, err := db.ExecContext(context.Background(), `
INSERT INTO mailbox_exchanges (
  exchange_id, mailbox_id, client_request_id, operation, controller_type,
  controller_id, client_idempotency_key, execution_idempotency_key,
  canonical_hash_version, canonical_hash, canonical_payload, resource_id,
  request_state, created_at, updated_at
) VALUES (?, ?, ?, 'get_session', 'local_user', 'tomasz.walczuk', '', '', 1,
          zeroblob(32), X'7b7d', '', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			row.id, row.mailbox, "req-"+row.id, row.state); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := authority.CountMailboxBacklogByInbox(context.Background(), []string{DefaultMailboxID, "analytics"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{DefaultMailboxID: 1, "analytics": 2}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("per-inbox backlog = %#v, want %#v", counts, want)
	}
	if _, err := authority.CountMailboxBacklogByInbox(context.Background(), []string{DefaultMailboxID, DefaultMailboxID}); err == nil {
		t.Fatal("duplicate configured mailbox ID was accepted")
	}
}

func TestP154ConfiguredMailboxSetRejectsRemovedInboxWithLiveWork(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	db, err := Open(ctx, filepath.Join(root.Path(), "state", "p154-mailbox-set.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.September, 30, 13, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, mailboxID, state string, cleanupAt, acknowledgedAt, cleanupStartedAt, fileRemovedAt any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
INSERT INTO mailbox_exchanges (
  exchange_id, mailbox_id, client_request_id, operation, controller_type,
  controller_id, client_idempotency_key, execution_idempotency_key,
  canonical_hash_version, canonical_hash, canonical_payload, resource_id,
  request_state, response_cleanup_at, acknowledged_at,
  response_cleanup_started_at, response_file_removed_at, created_at, updated_at
) VALUES (?, ?, ?, 'get_session', 'local_user', 'tomasz.walczuk', '', '', 1,
          zeroblob(32), X'7b7d', '', ?, ?, ?, ?, ?, ?, ?)`,
			id, mailboxID, "req-"+id, state, cleanupAt, acknowledgedAt, cleanupStartedAt, fileRemovedAt,
			formatStoredTime(now), formatStoredTime(now)); err != nil {
			t.Fatal(err)
		}
	}

	insert("mbx-p154-default-live", DefaultMailboxID, "accepted", nil, nil, nil, nil)
	insert("mbx-p154-retired-accepted", "retired", "accepted", nil, nil, nil, nil)
	defaultMailbox := MailboxConfiguration{ID: DefaultMailboxID, Root: p154LegacyDefaultMailboxRoot}
	retiredMailbox := MailboxConfiguration{ID: "retired", Root: filepath.Join(root.Path(), "mailboxes", "retired")}
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{defaultMailbox}, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("removed inbox with accepted work error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, filepath.Join(root.Path(), "state", "p154-mailbox-set.db"), []MailboxConfiguration{defaultMailbox}, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("read-only removed inbox validation error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{defaultMailbox, retiredMailbox}, nil); err != nil {
		t.Fatalf("configured accepted inbox error=%v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE mailbox_exchanges SET request_state = 'complete' WHERE exchange_id = 'mbx-p154-retired-accepted'`); err != nil {
		t.Fatal(err)
	}
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{defaultMailbox}, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("removed inbox with live unacknowledged response error=%v, want %v", err, ErrMailboxConfigurationPending)
	}

	past := formatStoredTime(now.Add(-time.Second))
	if _, err := db.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_cleanup_at = ? WHERE exchange_id = 'mbx-p154-retired-accepted'`, past); err != nil {
		t.Fatal(err)
	}
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{defaultMailbox}, nil); err != nil {
		t.Fatalf("expired unacknowledged response blocks removal: %v", err)
	}
	if err := authority.ValidateConfiguredMailboxSet(ctx, nil, nil); !errors.Is(err, ErrMailboxExchangeInvalid) {
		t.Fatalf("empty configured set error=%v, want %v", err, ErrMailboxExchangeInvalid)
	}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, filepath.Join(root.Path(), "state", "missing.db"), []MailboxConfiguration{defaultMailbox}, nil); err != nil {
		t.Fatalf("first-install missing database validation error=%v", err)
	}
	orphanedSidecar := filepath.Join(root.Path(), "state", "orphaned.db-wal")
	if err := os.WriteFile(orphanedSidecar, []byte("orphaned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, filepath.Join(root.Path(), "state", "orphaned.db"), []MailboxConfiguration{defaultMailbox}, nil); !errors.Is(err, ErrDatabasePath) {
		t.Fatalf("orphaned SQLite sidecar validation error=%v, want %v", err, ErrDatabasePath)
	}
}

const p154LegacyDefaultMailboxRoot = "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"

func TestP154MailboxRegistryPreventsRemovalOrRelocationWithoutAnExchange(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "p154-mailbox-registry.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	baseline := []MailboxConfiguration{{ID: DefaultMailboxID, Root: p154LegacyDefaultMailboxRoot}}
	analytics := MailboxConfiguration{ID: "analytics", Root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics"}
	configured := append(append([]MailboxConfiguration(nil), baseline...), analytics)

	if err := authority.RegisterConfiguredMailboxSet(ctx, configured, baseline); err != nil {
		t.Fatalf("register initial configured inboxes: %v", err)
	}
	if err := authority.ValidateConfiguredMailboxSet(ctx, baseline, baseline); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("remove configured inbox with no exchange error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	movedAnalytics := append(append([]MailboxConfiguration(nil), baseline...), MailboxConfiguration{ID: "analytics", Root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics-moved"})
	if err := authority.ValidateConfiguredMailboxSet(ctx, movedAnalytics, baseline); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("relocate configured inbox with no exchange error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, baseline, baseline); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("read-only registry removal error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
}

func TestP154PreMigrationReadOnlyValidationKeepsLegacyDefaultIngress(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "p154-pre-migration.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE mailbox_configuration_registry`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE local_remote_status_failures`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	// Model the P153 migration ledger used by read-only mailbox preflight. P164
	// adds a malformed-ingress ledger, BUG-007 adds unrelated job/audit
	// provenance columns, and B009-P3 adds a remote-projection status column;
	// remove ledger entries later than P153 so migration history remains
	// internally consistent for this legacy-only check.
	for _, statement := range []string{
		`DROP TABLE exec_commandless_lost_runtime_recovery_finalizations`,
		`DROP TABLE exec_controlled_restart_plan_lost_pairs`,
		`DROP TABLE exec_controlled_restart_plans`,
		`DROP TABLE exec_lost_runtime_recovery_finalizations`,
		`DROP TRIGGER mailbox_exchanges_reject_ingress_diagnostic_identity`,
		`DROP TRIGGER mailbox_ingress_diagnostics_reject_exchange_identity`,
		`DROP TRIGGER mailbox_ingress_diagnostics_monotonic_lifecycle`,
		`DROP TRIGGER mailbox_ingress_diagnostics_frozen_core`,
		`DROP TABLE mailbox_ingress_diagnostics`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM runner_schema_migrations WHERE version IN (27, 28, 29, 30, 31, 32, 33, 34, 35)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 26`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	baseline := []MailboxConfiguration{{ID: DefaultMailboxID, Root: p154LegacyDefaultMailboxRoot}}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, baseline, baseline); err != nil {
		t.Fatalf("P153-schema legacy default preflight error=%v", err)
	}
	movedDefault := []MailboxConfiguration{{ID: DefaultMailboxID, Root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/default"}}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, movedDefault, baseline); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("P153-schema default relocation error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
}

// TestP155ReadOnlyV24MailboxPreflightAndActivationMigration models the
// currently installed V1 authority. The post-quiescence preflight must make no
// change to its schema, while the existing activation path later migrates and
// records the complete V2 candidate only after the installer no-rollback
// boundary.
func TestP155ReadOnlyV24MailboxPreflightAndActivationMigration(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	stateDirectory := filepath.Join(root.Path(), "state")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDirectory, "local.db")
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
	p152BuildActualV24Database(t, ctx, legacy)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	baseline := []MailboxConfiguration{{ID: DefaultMailboxID, Root: p154LegacyDefaultMailboxRoot}}
	analytics := MailboxConfiguration{ID: "analytics", Root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics"}
	candidate := append(append([]MailboxConfiguration(nil), baseline...), analytics)
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, candidate, baseline); err != nil {
		t.Fatalf("V24 candidate preflight error=%v", err)
	}
	p155AssertV24ReadOnlyPreflight(t, ctx, path)

	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, []MailboxConfiguration{analytics}, baseline); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("V24 missing-default preflight error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, candidate, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("V24 missing-baseline preflight error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	movedDefault := append([]MailboxConfiguration{{ID: DefaultMailboxID, Root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/default"}}, analytics)
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, movedDefault, baseline); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("V24 moved-default preflight error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	p155AssertV24ReadOnlyPreflight(t, ctx, path)

	// This models the existing --activate-mailbox-set path after the installer
	// has disabled V1 recovery: Open applies migrations, then registration
	// validates the current schema and records the candidate roots.
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("activate V24 candidate database: %v", err)
	}
	authority, err := NewAuthorityStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := authority.RegisterConfiguredMailboxSet(ctx, candidate, baseline); err != nil {
		_ = db.Close()
		t.Fatalf("register migrated candidate roots: %v", err)
	}
	if version := readUserVersion(t, db); version != CurrentSchemaVersion {
		_ = db.Close()
		t.Fatalf("activated schema version=%d, want %d", version, CurrentSchemaVersion)
	}
	rows, err := db.QueryContext(ctx, `SELECT mailbox_id, mailbox_root FROM mailbox_configuration_registry ORDER BY mailbox_id`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	registered := make(map[string]string)
	for rows.Next() {
		var id, mailboxRoot string
		if err := rows.Scan(&id, &mailboxRoot); err != nil {
			_ = rows.Close()
			_ = db.Close()
			t.Fatal(err)
		}
		registered[id] = mailboxRoot
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	wantRegistered := map[string]string{DefaultMailboxID: p154LegacyDefaultMailboxRoot, analytics.ID: analytics.Root}
	if !reflect.DeepEqual(registered, wantRegistered) {
		t.Fatalf("registered migrated roots=%#v, want %#v", registered, wantRegistered)
	}
	if err := ValidateConfiguredMailboxSetAtPath(ctx, path, candidate, baseline); err != nil {
		t.Fatalf("migrated candidate retained preflight error=%v", err)
	}
}

func p155AssertV24ReadOnlyPreflight(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqlitePathURI(path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var version, ledger, registry, namespaced int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runner_schema_migrations`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'mailbox_configuration_registry')`).Scan(&registry); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('mailbox_exchanges') WHERE name = 'mailbox_id')`).Scan(&namespaced); err != nil {
		t.Fatal(err)
	}
	if version != legacySingleMailboxSchemaVersion || ledger != legacySingleMailboxSchemaVersion || registry != 0 || namespaced != 0 {
		t.Fatalf("read-only V24 preflight changed schema: version=%d ledger=%d registry=%d mailbox_id=%d", version, ledger, registry, namespaced)
	}
}
