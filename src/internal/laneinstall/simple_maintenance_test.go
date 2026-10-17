package laneinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestSimpleLaneDatabasePreflightIsReadOnlyAndRejectsRetainedReservation(t *testing.T) {
	ctx := context.Background()
	root := simpleTestTempDir(t)
	plan := simpleTestPlanAt(t, root)
	database, err := store.Open(ctx, plan.Namespace.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(plan.Namespace.Database)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := readSimpleLaneDatabaseQuiescence(ctx, plan, "")
	if err != nil || !simpleProofFresh(proof, plan) {
		t.Fatalf("empty authority proof=%+v err=%v", proof, err)
	}
	if !simpleMetricsEmpty(proof.Metrics()) || proof.DatabaseSchema() != store.CurrentSchemaVersion {
		t.Fatalf("unexpected clean restart inventory/schema: %+v/%d", proof.Metrics(), proof.DatabaseSchema())
	}
	after, err := os.ReadFile(plan.Namespace.Database)
	if err != nil || string(after) != string(before) {
		t.Fatalf("read-only preflight changed authority bytes: err=%v", err)
	}

	database, err = store.Open(ctx, plan.Namespace.Database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.ExecContext(ctx, `
INSERT INTO exec_sessions (
 session_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,state,
 command_timeout_ns,idle_timeout_ns,session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at
) VALUES ('held-session','remote','linux-host','linux-host-dev','direct_mtls','test-identity','empty','ready',
 1000000000,1000000000,60000000000,1024,'now','now','later');
INSERT INTO exec_capacity_reservations (session_id,host_key,reserved_at) VALUES ('held-session','test-host','now');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSimpleLaneDatabaseQuiescence(ctx, plan, ""); !errors.Is(err, ErrSimpleLaneNotQuiescent) {
		t.Fatalf("retained reservation preflight error=%v, want ErrSimpleLaneNotQuiescent", err)
	}
}

func TestSimpleLaneBackupIsConsistentAndReceiptDetectsTampering(t *testing.T) {
	ctx := context.Background()
	root := simpleTestTempDir(t)
	plan := simpleTestPlanAt(t, root)
	if err := os.MkdirAll(plan.Namespace.Backups, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Namespace.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Namespace.ConfigFile, []byte("isolated lane config fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, plan.Namespace.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const active = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	proof := SimpleQuiescenceProof{
		hostID: plan.HostID, lane: plan.Lane, databasePath: plan.Namespace.Database,
		observedAt: time.Now().UTC(), database: store.CurrentSchemaVersion, current: active,
	}
	receipt, err := createSimpleLaneBackup(ctx, plan, plan.Namespace.Database, database, proof)
	if err != nil {
		t.Fatal(err)
	}
	if !simpleBackupReceiptValid(plan, receipt, proof) {
		t.Fatal("new consistent backup did not validate against its private receipt")
	}
	manifest, err := readSimpleLaneBackupManifest(plan, receipt.Directory())
	if err != nil || manifest.ActiveBundle != active || manifest.DatabaseSchema != store.CurrentSchemaVersion {
		t.Fatalf("persistent backup manifest=%+v err=%v", manifest, err)
	}
	backupDatabase := filepath.Join(receipt.Directory(), "authority.sqlite")
	backup, err := store.OpenExistingRestartPreflightReadOnly(ctx, backupDatabase)
	if err != nil {
		t.Fatalf("open SQLite online snapshot: %v", err)
	}
	var version int
	if err := backup.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != store.CurrentSchemaVersion {
		t.Fatalf("snapshot schema=%d err=%v", version, err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receipt.Directory(), "config.yaml"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if simpleBackupReceiptValid(plan, receipt, proof) {
		t.Fatal("tampered config backup still validated")
	}
	if _, err := readSimpleLaneBackupManifest(plan, receipt.Directory()); !errors.Is(err, ErrSimpleRollbackUnavailable) {
		t.Fatalf("rollback accepted tampered persistent backup: %v", err)
	}
}

func TestSimpleLaneLifecycleLockIsExclusiveAndRetryable(t *testing.T) {
	root := simpleTestTempDir(t)
	plan := simpleTestPlanAt(t, root)
	first, err := acquireSimpleLaneLock(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireSimpleLaneLock(plan); !errors.Is(err, ErrSimpleLaneLockHeld) {
		t.Fatalf("second selected-lane lock error=%v, want ErrSimpleLaneLockHeld", err)
	}
	if err := unix.Flock(first, unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(first); err != nil {
		t.Fatal(err)
	}
	second, err := acquireSimpleLaneLock(plan)
	if err != nil {
		t.Fatalf("lifecycle lock could not be retried after release: %v", err)
	}
	if err := unix.Flock(second, unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(second); err != nil {
		t.Fatal(err)
	}
}

func simpleTestPlanAt(t *testing.T, root string) Plan {
	t.Helper()
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	plan.Namespace.ServiceRoot = root
	plan.Namespace.Database = filepath.Join(root, "state", "remote.db")
	plan.Namespace.ConfigFile = filepath.Join(root, "config", "linux.yaml")
	plan.Namespace.Backups = filepath.Join(root, "backups")
	plan.Namespace.LifecycleLock = filepath.Join(root, "run", "runnerd-lifecycle.lock")
	return plan
}
