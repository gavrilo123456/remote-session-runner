package laneinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPRDReplacementMacRouteBackupValidationAllowsJournaledLiveChanges(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backup")
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []struct{ target, backup, contents string }{
		{filepath.Join(root, "config", "mac.yaml"), filepath.Join("config", "mac.yaml"), "original config"},
		{filepath.Join(root, "secrets", "server-ca.pem"), filepath.Join("secrets", "server-ca.pem"), "original CA"},
		{filepath.Join(root, "ssh", "known_hosts"), filepath.Join("secrets", "known_hosts"), "original known hosts"},
	}
	expected := make(map[string]string, len(paths))
	files := make([]PRDBackupFile, 0, len(paths))
	for _, entry := range paths {
		relative := entry.backup
		backupPath := filepath.Join(backupDir, relative)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
			t.Fatal(err)
		}
		contents := []byte(entry.contents)
		if err := os.WriteFile(backupPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := digestBytes(contents)
		expected[entry.target] = digest
		files = append(files, PRDBackupFile{TargetPath: entry.target, BackupPath: relative,
			SHA256: digest, Bytes: int64(len(contents)), Mode: 0o600})
	}
	databaseBackup := filepath.Join(backupDir, "state.db.online")
	if err := os.WriteFile(databaseBackup, []byte("consistent sqlite snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := PRDBackupManifest{DatabaseBackupPath: databaseBackup, DatabaseSHA256: digestFileSHA(databaseBackup), Files: files}
	if err := verifyPRDReplacementMacRouteBackupFiles(manifest, expected); err != nil {
		t.Fatalf("valid immutable route backup rejected: %v", err)
	}
	changed := map[string]string{}
	for target, hash := range expected {
		changed[target] = hash
	}
	changed[filepath.Join(root, "config", "mac.yaml")] = digestBytes([]byte("unapproved config"))
	if err := verifyPRDReplacementMacRouteBackupFiles(manifest, changed); err == nil {
		t.Fatal("backup did not bind the original Mac configuration hash")
	}
	if err := os.WriteFile(filepath.Join(backupDir, files[0].BackupPath), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPRDReplacementMacRouteBackupFiles(manifest, expected); err == nil {
		t.Fatal("tampered backup copy was accepted")
	}
}

func TestApplyPRDReplacementMacRouteFileSupportsExactRetryAndRejectsDrift(t *testing.T) {
	tempRoot := os.TempDir()
	if runtime.GOOS == "darwin" {
		tempRoot = "/private/tmp"
	}
	root, err := os.MkdirTemp(tempRoot, "runner-prd-route-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "route.yaml")
	before := []byte("old route\n")
	after := []byte("new route\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPRDReplacementMacRouteFile(path, digestBytes(before), digestBytes(after), after, 4096); err != nil {
		t.Fatalf("apply approved route bytes: %v", err)
	}
	if err := applyPRDReplacementMacRouteFile(path, digestBytes(before), digestBytes(after), after, 4096); err != nil {
		t.Fatalf("exact interrupted retry must be idempotent: %v", err)
	}
	if err := os.WriteFile(path, []byte("unexpected route\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPRDReplacementMacRouteFile(path, digestBytes(before), digestBytes(after), after, 4096); err == nil {
		t.Fatal("unexpected configuration drift was overwritten")
	}
}
