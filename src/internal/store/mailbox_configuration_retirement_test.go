package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestMailboxConfigurationRetirementMigrationPreservesHistoricalRegistryAndIsImmutable(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "mailbox-retirement-migration.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	legacy := MailboxConfiguration{ID: "legacy", Root: filepath.Join(root.Path(), "mailboxes", "legacy")}
	replacement := MailboxConfiguration{ID: "replacement", Root: filepath.Join(root.Path(), "mailboxes", "replacement")}
	if err := authority.RegisterConfiguredMailboxSet(ctx, []MailboxConfiguration{legacy, replacement}, nil); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	downgradeMailboxRetirementSchemaTo35(t, path)
	migrated, err := OpenExistingOfflineMaintenanceMigrating(ctx, path)
	if err != nil {
		t.Fatalf("migrate schema 35 authority: %v", err)
	}
	defer migrated.Close()

	var registeredID, registeredRoot string
	if err := migrated.QueryRowContext(ctx, `
SELECT mailbox_id, mailbox_root
FROM mailbox_configuration_registry
WHERE mailbox_id = ?`, legacy.ID).Scan(&registeredID, &registeredRoot); err != nil {
		t.Fatal(err)
	}
	if registeredID != legacy.ID || registeredRoot != legacy.Root {
		t.Fatalf("historical registry row=%q/%q, want %q/%q", registeredID, registeredRoot, legacy.ID, legacy.Root)
	}
	if _, err := migrated.ExecContext(ctx, `UPDATE mailbox_configuration_registry SET mailbox_root = ? WHERE mailbox_id = ?`, legacy.Root+"-moved", legacy.ID); err == nil {
		t.Fatal("registry update succeeded after retirement migration")
	}
	if _, err := migrated.ExecContext(ctx, `DELETE FROM mailbox_configuration_registry WHERE mailbox_id = ?`, legacy.ID); err == nil {
		t.Fatal("registry delete succeeded after retirement migration")
	}
	if _, err := migrated.ExecContext(ctx, `
INSERT INTO mailbox_configuration_retirements (mailbox_id, mailbox_root, retired_at)
VALUES (?, ?, ?)`, legacy.ID, replacement.Root, formatStoredTime(time.Now())); err == nil {
		t.Fatal("mismatched mailbox retirement pair satisfied composite foreign key")
	}
	if _, err := migrated.ExecContext(ctx, `
INSERT INTO mailbox_configuration_retirements (mailbox_id, mailbox_root, retired_at)
VALUES (?, ?, ?)`, legacy.ID, legacy.Root, formatStoredTime(time.Now())); err != nil {
		t.Fatalf("insert exact historical retirement: %v", err)
	}
	if _, err := migrated.ExecContext(ctx, `UPDATE mailbox_configuration_retirements SET retired_at = ? WHERE mailbox_id = ?`, formatStoredTime(time.Now().Add(time.Second)), legacy.ID); err == nil {
		t.Fatal("retirement update succeeded")
	}
	if _, err := migrated.ExecContext(ctx, `DELETE FROM mailbox_configuration_retirements WHERE mailbox_id = ?`, legacy.ID); err == nil {
		t.Fatal("retirement delete succeeded")
	}
}

func TestRetireConfiguredMailboxSetAllowsOneSafeExplicitRetirementAndPreventsRevival(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "mailbox-retirement.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	clock := time.Date(2026, time.October, 15, 10, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(database, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	legacy := MailboxConfiguration{ID: "legacy", Root: filepath.Join(root.Path(), "mailboxes", "legacy")}
	replacement := MailboxConfiguration{ID: "replacement", Root: filepath.Join(root.Path(), "mailboxes", "replacement")}
	if err := authority.RegisterConfiguredMailboxSet(ctx, []MailboxConfiguration{legacy}, nil); err != nil {
		t.Fatalf("register legacy mailbox: %v", err)
	}

	retirement := MailboxRetirement{ID: legacy.ID}
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("ordinary legacy omission error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if _, err := authority.ValidateConfiguredMailboxRetirement(ctx, []MailboxConfiguration{{ID: "reused-root", Root: legacy.Root}}, nil, retirement); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("retirement candidate reusing legacy root error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	resolved, err := authority.ValidateConfiguredMailboxRetirement(ctx, []MailboxConfiguration{replacement}, nil, retirement)
	if err != nil {
		t.Fatalf("validate safe retirement: %v", err)
	}
	if resolved != legacy {
		t.Fatalf("resolved retirement=%+v, want %+v", resolved, legacy)
	}
	retired, err := authority.RetireConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil, retirement)
	if err != nil {
		t.Fatalf("retire configured mailbox set: %v", err)
	}
	if retired != legacy {
		t.Fatalf("retired identity=%+v, want %+v", retired, legacy)
	}
	var registryCount, retirementCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_configuration_registry WHERE mailbox_id = ? AND mailbox_root = ?`, legacy.ID, legacy.Root).Scan(&registryCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_configuration_retirements WHERE mailbox_id = ? AND mailbox_root = ?`, legacy.ID, legacy.Root).Scan(&retirementCount); err != nil {
		t.Fatal(err)
	}
	if registryCount != 1 || retirementCount != 1 {
		t.Fatalf("historical registry=%d retirement=%d, want both 1", registryCount, retirementCount)
	}
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil); err != nil {
		t.Fatalf("post-retirement active configuration rejected: %v", err)
	}
	for name, candidate := range map[string][]MailboxConfiguration{
		"former ID and root":       {legacy, replacement},
		"former root under new ID": {{ID: "reused-root", Root: legacy.Root}, replacement},
	} {
		t.Run(name, func(t *testing.T) {
			if err := authority.ValidateConfiguredMailboxSet(ctx, candidate, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
				t.Fatalf("reuse validation error=%v, want %v", err, ErrMailboxConfigurationPending)
			}
			if err := authority.RegisterConfiguredMailboxSet(ctx, candidate, nil); !errors.Is(err, ErrMailboxConfigurationPending) {
				t.Fatalf("reuse registration error=%v, want %v", err, ErrMailboxConfigurationPending)
			}
		})
	}
	retried, err := authority.RetireConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil, retirement)
	if err != nil {
		t.Fatalf("exact retirement retry: %v", err)
	}
	if retried != legacy {
		t.Fatalf("retry identity=%+v, want %+v", retried, legacy)
	}
	if _, err := authority.ValidateConfiguredMailboxRetirement(ctx, []MailboxConfiguration{replacement}, nil, MailboxRetirement{ID: "unknown"}); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("unknown retirement error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if _, err := authority.ValidateConfiguredMailboxRetirement(ctx, []MailboxConfiguration{legacy, replacement}, nil, retirement); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("retirement candidate retaining former identity error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
}

func TestRetireConfiguredMailboxSetRejectsLiveOrUnacknowledgedWorkButAllowsAcknowledgedHistory(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "mailbox-retirement-work.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	clock := time.Date(2026, time.October, 15, 11, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(database, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	legacy := MailboxConfiguration{ID: "legacy", Root: filepath.Join(root.Path(), "mailboxes", "legacy")}
	replacement := MailboxConfiguration{ID: "replacement", Root: filepath.Join(root.Path(), "mailboxes", "replacement")}
	if err := authority.RegisterConfiguredMailboxSet(ctx, []MailboxConfiguration{legacy}, nil); err != nil {
		t.Fatal(err)
	}
	insertRetirementMailboxExchange(t, ctx, database, clock, "retirement-live", legacy.ID, "accepted", nil)
	retirement := MailboxRetirement{ID: legacy.ID}
	if _, err := authority.RetireConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil, retirement); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("accepted work retirement error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	assertMailboxRetirementCount(t, ctx, database, 0)

	if _, err := database.ExecContext(ctx, `UPDATE mailbox_exchanges SET request_state = 'complete' WHERE exchange_id = 'retirement-live'`); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ValidateConfiguredMailboxRetirement(ctx, []MailboxConfiguration{replacement}, nil, retirement); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("unacknowledged terminal retirement error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_exchanges SET acknowledged_at = ? WHERE exchange_id = 'retirement-live'`, formatStoredTime(clock)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RetireConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil, retirement); err != nil {
		t.Fatalf("acknowledged terminal history retirement: %v", err)
	}
	assertMailboxRetirementCount(t, ctx, database, 1)
}

func TestValidateConfiguredMailboxRetirementAtPathIsReadOnlyAndSupportsSchema35(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "mailbox-retirement-readonly.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	legacy := MailboxConfiguration{ID: "legacy", Root: filepath.Join(root.Path(), "mailboxes", "legacy")}
	replacement := MailboxConfiguration{ID: "replacement", Root: filepath.Join(root.Path(), "mailboxes", "replacement")}
	if err := authority.RegisterConfiguredMailboxSet(ctx, []MailboxConfiguration{legacy}, nil); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	downgradeMailboxRetirementSchemaTo35(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ValidateConfiguredMailboxRetirementAtPath(ctx, path, []MailboxConfiguration{replacement}, nil, MailboxRetirement{ID: legacy.ID})
	if err != nil {
		t.Fatalf("schema-35 read-only retirement validation: %v", err)
	}
	if resolved != legacy {
		t.Fatalf("read-only resolved identity=%+v, want %+v", resolved, legacy)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("read-only retirement validation changed database bytes")
	}
	if _, err := ValidateConfiguredMailboxRetirementAtPath(ctx, bug011V24AuthorityPath(t), []MailboxConfiguration{replacement}, nil, MailboxRetirement{ID: legacy.ID}); !errors.Is(err, ErrMailboxConfigurationPending) {
		t.Fatalf("schema-24 retirement preflight error=%v, want %v", err, ErrMailboxConfigurationPending)
	}
}

func TestRetireConfiguredMailboxSetRollsBackTombstoneWhenRegistrationFails(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "mailbox-retirement-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	legacy := MailboxConfiguration{ID: "legacy", Root: filepath.Join(root.Path(), "mailboxes", "legacy")}
	replacement := MailboxConfiguration{ID: "replacement", Root: filepath.Join(root.Path(), "mailboxes", "replacement")}
	if err := authority.RegisterConfiguredMailboxSet(ctx, []MailboxConfiguration{legacy}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
CREATE TRIGGER mailbox_retirement_test_fail_registration
BEFORE INSERT ON mailbox_configuration_registry
WHEN NEW.mailbox_id = 'replacement'
BEGIN
    SELECT RAISE(ABORT, 'test registration failure');
END`); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RetireConfiguredMailboxSet(ctx, []MailboxConfiguration{replacement}, nil, MailboxRetirement{ID: legacy.ID}); err == nil {
		t.Fatal("retirement succeeded despite registration failure")
	}
	assertMailboxRetirementCount(t, ctx, database, 0)
	if err := authority.ValidateConfiguredMailboxSet(ctx, []MailboxConfiguration{legacy}, nil); err != nil {
		t.Fatalf("failed retirement changed active registry: %v", err)
	}
}

func downgradeMailboxRetirementSchemaTo35(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE mailbox_configuration_retirements`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP INDEX ux_mailbox_configuration_registry_id_root`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE exec_completed_teardown_recovery_finalizations`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE lane_ingress_admission`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM runner_schema_migrations WHERE version > 35`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = 35`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func insertRetirementMailboxExchange(t *testing.T, ctx context.Context, database *sql.DB, now time.Time, exchangeID, mailboxID, state string, acknowledgedAt any) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
INSERT INTO mailbox_exchanges (
  exchange_id, mailbox_id, client_request_id, operation, controller_type,
  controller_id, client_idempotency_key, execution_idempotency_key,
  canonical_hash_version, canonical_hash, canonical_payload, resource_id,
  request_state, acknowledged_at, created_at, updated_at
) VALUES (?, ?, ?, 'get_session', 'local_user', 'retirement-test', '', '', 1,
          zeroblob(32), X'7b7d', '', ?, ?, ?, ?)`,
		exchangeID, mailboxID, "request-"+exchangeID, state, acknowledgedAt,
		formatStoredTime(now), formatStoredTime(now)); err != nil {
		t.Fatal(err)
	}
}

func assertMailboxRetirementCount(t *testing.T, ctx context.Context, database *sql.DB, want int) {
	t.Helper()
	var count int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_configuration_retirements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("mailbox retirement rows=%d, want %d", count, want)
	}
}
