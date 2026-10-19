package laneinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestCatalogEntrypointRefreshPreservesExactPriorFileAndRetriesSafely(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runnerd-entrypoint.sh")
	backup := filepath.Join(root, "runnerd-entrypoint.previous")
	previous := []byte("#!/bin/sh\nexec /legacy/runnerd\n")
	selected := []byte("#!/bin/sh\nexec /catalog/current/bin/runnerd\n")
	if err := os.WriteFile(path, previous, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	previousSHA := catalogArtifactSHA256(previous)
	selectedSHA := catalogArtifactSHA256(selected)

	if err := preserveCatalogArtifactBackup(path, backup, previousSHA); err != nil {
		t.Fatalf("preserve exact prior launcher: %v", err)
	}
	if err := installCatalogArtifactAtomically(path, previousSHA, selected, selectedSHA, 0o700); err != nil {
		t.Fatalf("install selected launcher: %v", err)
	}
	if err := preserveCatalogArtifactBackup(path, backup, previousSHA); err != nil {
		t.Fatalf("recover after replacement with the durable prior-file backup: %v", err)
	}
	if err := installCatalogArtifactAtomically(path, previousSHA, selected, selectedSHA, 0o700); err == nil {
		t.Fatal("retry with the stale prior digest unexpectedly replaced an already selected artifact")
	}
	if err := verifyCatalogArtifactFile(path, selectedSHA, uint32(os.Getuid()), 0o700); err != nil {
		t.Fatalf("selected launcher not verified after retry refusal: %v", err)
	}
	if err := verifyCatalogArtifactFile(backup, previousSHA, uint32(os.Getuid()), 0o600); err != nil {
		t.Fatalf("preserved previous launcher not verified: %v", err)
	}
	backupContents, err := os.ReadFile(backup)
	if err != nil || string(backupContents) != string(previous) {
		t.Fatalf("backup contents=%q err=%v, want original launcher", backupContents, err)
	}
}

func TestCatalogEntrypointRefreshRejectsChangedOrLinkedArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runnerd-entrypoint.sh")
	previous := []byte("#!/bin/sh\nexit 0\n")
	selected := []byte("#!/bin/sh\nexit 78\n")
	if err := os.WriteFile(path, previous, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installCatalogArtifactAtomically(path, strings.Repeat("0", 64), selected, catalogArtifactSHA256(selected), 0o700); err == nil {
		t.Fatal("artifact replacement accepted an unobserved previous digest")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", path); err != nil {
		t.Fatal(err)
	}
	if err := preserveCatalogArtifactBackup(path, filepath.Join(root, "backup"), catalogArtifactSHA256(previous)); err == nil {
		t.Fatal("backup accepted a symlink in place of the observed launcher")
	}
}

func TestCatalogEntrypointRefreshOperationRequiresCanonicalProtectedRecord(t *testing.T) {
	t.Setenv("TMPDIR", "/private/tmp")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	operationID := strings.Repeat("a", 32)
	record := CatalogArtifactRefreshOperation{
		FormatVersion: catalogArtifactRefreshFormatVersion, OperationID: operationID, CohortOperationID: operationID,
		HostID: "linux-host", Lane: config.ReleaseLaneDev, ReleaseID: "cat-20261019-f0e2843",
		ArtifactPath:   "/home/ubuntu/.local/share/remote-session-runner-dev/bin/runnerd-entrypoint.sh",
		PreviousSHA256: strings.Repeat("1", 64), SelectedSHA256: strings.Repeat("2", 64),
		BackupPath:   "/home/ubuntu/.local/share/remote-session-runner-dev/backups/entrypoint.bak",
		BackupSHA256: strings.Repeat("1", 64), DatabaseBackup: "/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-fixture",
		DatabaseBackupManifest: strings.Repeat("3", 64), Stage: "prepared", CreatedUTC: "2026-10-19T12:00:00Z",
	}
	if !validCatalogArtifactRefreshRecord(record, operationID) {
		t.Fatalf("test record unexpectedly invalid: %+v", record)
	}
	if err := writeCatalogArtifactRefreshOperation(root, record); err != nil {
		t.Fatalf("write canonical operation: %v", err)
	}
	read, err := readCatalogArtifactRefreshOperation(root, operationID)
	if err != nil || !catalogArtifactRefreshIdentityEqual(record, read) || read.Stage != "prepared" {
		t.Fatalf("read operation=%+v err=%v", read, err)
	}
	path := filepath.Join(root, operationID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "\"stage\": \"prepared\"", "\"stage\": \"interrupted\"", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCatalogArtifactRefreshOperation(root, operationID); err == nil {
		t.Fatal("noncanonical/tampered operation record was accepted")
	}
}
