package laneinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPRDReplacementServerCSRIsPrivateDurableAndIdempotent(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	root := filepath.Join(parent, "operations")
	operationID := "0123456789abcdef0123456789abcdef"
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	first, err := preparePRDReplacementServerCSRAt(root, operationID, now)
	if err != nil {
		t.Fatalf("prepare host-bound CSR: %v", err)
	}
	second, err := preparePRDReplacementServerCSRAt(root, operationID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("exact CSR retry: %v", err)
	}
	if first.OperationID != operationID || first.RequestSHA != digestBytes(first.RequestPEM) ||
		!bytes.Equal(first.RequestPEM, second.RequestPEM) || first.RequestSHA != second.RequestSHA ||
		bytes.Contains(first.RequestPEM, []byte("PRIVATE KEY")) {
		t.Fatal("CSR retry changed operation identity or exposed a private key")
	}
	operationDir := filepath.Join(root, "server-csr", operationID)
	for _, name := range []string{"server.key", "server.csr", "manifest.json"} {
		info, err := os.Lstat(filepath.Join(operationDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("CSR operation file %s has unsafe type/mode: info=%v err=%v", name, info, err)
		}
	}
	var stat unix.Stat_t
	if unix.Lstat(filepath.Join(operationDir, "server.key"), &stat) != nil || stat.Nlink != 1 || stat.Mode&0o077 != 0 {
		t.Fatal("host server key is linked or readable beyond its owner")
	}
	if _, _, err := loadPRDReplacementServerKeyAt(root, operationID, first.RequestSHA); err != nil {
		t.Fatalf("bootstrap cannot load the exact operation-bound CSR/key pair: %v", err)
	}
	if _, _, err := loadPRDReplacementServerKeyAt(root, operationID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("bootstrap loaded the server private key under a mismatched CSR digest")
	}
}

func TestPRDReplacementServerCSRRejectsPartialOperationDirectory(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	root := filepath.Join(parent, "operations")
	operationID := "abcdefabcdefabcdefabcdefabcdefab"
	partial := filepath.Join(root, "server-csr", operationID)
	if err := os.MkdirAll(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePRDReplacementServerCSRAt(root, operationID, time.Now().UTC()); err == nil {
		t.Fatal("partial CSR state was treated as absent and regenerated")
	}
}
