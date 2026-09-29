package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/testfixture"
)

func TestP139OnlineBackupCapturesCommittedWALAndRestoresSnapshot(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	sourcePath := filepath.Join(root.Path(), "state", "source.db")
	source, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecContext(ctx, "CREATE TABLE p139_backup_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecContext(ctx, "INSERT INTO p139_backup_probe(value) VALUES ('before-reader')"); err != nil {
		t.Fatal(err)
	}
	reader, err := source.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var baselineCount int
	if err := transaction.QueryRowContext(ctx, "SELECT count(*) FROM p139_backup_probe").Scan(&baselineCount); err != nil {
		t.Fatal(err)
	}
	if baselineCount != 1 {
		t.Fatalf("reader baseline rows=%d, want 1", baselineCount)
	}
	if _, err := source.ExecContext(ctx, "INSERT INTO p139_backup_probe(value) VALUES ('committed-in-wal')"); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := source.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		t.Fatal(err)
	}
	if logFrames <= checkpointedFrames {
		t.Fatalf("WAL checkpoint log=%d checkpointed=%d busy=%d; wanted committed frames retained in WAL", logFrames, checkpointedFrames, busy)
	}

	backupPath := filepath.Join(root.Path(), "backups", "source.snapshot.db")
	if err := CreateOnlineBackup(ctx, source, backupPath); err != nil {
		t.Fatalf("create online backup while a reader holds prior WAL snapshot: %v", err)
	}
	checkP139BackupProbe(t, backupPath, "committed-in-wal", true)
	backupInfo, err := os.Lstat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0o600 || !backupInfo.Mode().IsRegular() {
		t.Fatalf("backup metadata=%v err=%v; want regular owner-only file", backupInfo, err)
	}

	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	targetPath := filepath.Join(root.Path(), "state", "target.db")
	target, err := Open(ctx, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(ctx, "CREATE TABLE p139_backup_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(ctx, "INSERT INTO p139_backup_probe(value) VALUES ('later-target-state')"); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RestoreOnlineBackup(ctx, targetPath, backupPath); err != nil {
		t.Fatalf("restore online backup: %v", err)
	}
	checkP139BackupProbe(t, targetPath, "committed-in-wal", true)
	checkP139BackupProbe(t, targetPath, "later-target-state", false)
}

func TestP139OnlineBackupRejectsUnsafePathsAndPreservesExistingDestination(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := CreateOnlineBackup(ctx, database, "relative.db"); !errors.Is(err, ErrDatabasePath) {
		t.Fatalf("relative backup destination error=%v, want ErrDatabasePath", err)
	}
	existing := filepath.Join(root.Path(), "backups", "existing.db")
	if err := os.Mkdir(filepath.Dir(existing), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CreateOnlineBackup(ctx, database, existing); err == nil {
		t.Fatal("CreateOnlineBackup replaced an existing destination")
	}
	if content, err := os.ReadFile(existing); err != nil || string(content) != "preserve" {
		t.Fatalf("existing destination content=%q err=%v, want preserved", content, err)
	}
	link := filepath.Join(root.Path(), "backups", "link.db")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	if err := RestoreOnlineBackup(ctx, filepath.Join(root.Path(), "state", "source.db"), link); err == nil {
		t.Fatal("RestoreOnlineBackup accepted a symlink backup")
	}
}

func TestP139RestoreRejectsCorruptBackupBeforeChangingAuthority(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "authority.db")
	database, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "CREATE TABLE p139_backup_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO p139_backup_probe(value) VALUES ('authority-kept')"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(root.Path(), "backups", "corrupt.db")
	if err := os.Mkdir(filepath.Dir(backupPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreOnlineBackup(ctx, databasePath, backupPath); err == nil {
		t.Fatal("RestoreOnlineBackup accepted a corrupt snapshot")
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen authority after rejected corrupt backup: %v", err)
	}
	defer reopened.Close()
	var value string
	if err := reopened.QueryRowContext(ctx, "SELECT value FROM p139_backup_probe").Scan(&value); err != nil || value != "authority-kept" {
		t.Fatalf("authority after rejected backup value=%q err=%v", value, err)
	}
}

func checkP139BackupProbe(t *testing.T, path, value string, wantPresent bool) {
	t.Helper()
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open backup database %q: %v", filepath.Base(path), err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT count(*) FROM p139_backup_probe WHERE value = ?", value).Scan(&count); err != nil {
		t.Fatalf("query backup probe %q: %v", value, err)
	}
	want := 0
	if wantPresent {
		want = 1
	}
	if count != want {
		t.Fatalf("backup probe %q count=%d, want %d", value, count, want)
	}
}
