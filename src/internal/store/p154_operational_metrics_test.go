package store

import (
	"context"
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
	if _, err := db.ExecContext(ctx, `DELETE FROM runner_schema_migrations WHERE version = 27`); err != nil {
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
