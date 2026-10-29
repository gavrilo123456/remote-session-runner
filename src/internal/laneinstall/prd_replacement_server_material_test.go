package laneinstall

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPRDReplacementServerMaterialIsDurablePrivateAndExactRetry(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	root := filepath.Join(parent, "operations")
	operationID := "0123456789abcdef0123456789abcdef"
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	request, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	first, err := preparePRDReplacementServerMaterialAt(root, operationID, request.RequestPEM, now)
	if err != nil {
		t.Fatalf("prepare signed material: %v", err)
	}
	if first.OperationID != operationID || first.RequestSHA256 != digestBytes(request.RequestPEM) ||
		first.CertificateSHA != digestBytes(first.CertificatePEM) || first.CASHA != digestBytes(first.CAPEM) ||
		bytes.Contains(first.CertificatePEM, []byte("PRIVATE KEY")) || bytes.Contains(first.CAPEM, []byte("PRIVATE KEY")) {
		t.Fatal("public issuance report has incorrect bindings or contains private material")
	}
	if err := ValidatePRDReplacementServerMaterial(first.CertificatePEM, request.PrivateKeyPEM, first.CAPEM, now); err != nil {
		t.Fatalf("issued certificate does not match replacement private key: %v", err)
	}
	second, err := preparePRDReplacementServerMaterialAt(root, operationID, request.RequestPEM, now.Add(time.Second))
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if !bytes.Equal(first.CertificatePEM, second.CertificatePEM) || !bytes.Equal(first.CAPEM, second.CAPEM) ||
		first.CertificateSHA != second.CertificateSHA || first.CASHA != second.CASHA {
		t.Fatal("exact retry changed the issued material")
	}
	other, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparePRDReplacementServerMaterialAt(root, operationID, other.RequestPEM, now.Add(2*time.Second)); err == nil {
		t.Fatal("conflicting CSR was accepted under an existing operation")
	}
	operationDir := filepath.Join(root, "server-material", operationID)
	for name, wantMode := range map[string]os.FileMode{"server-ca.key": 0o600, "server.pem": 0o600, "server-ca.pem": 0o600, "server.csr": 0o600, "manifest.json": 0o600} {
		info, err := os.Lstat(filepath.Join(operationDir, name))
		if err != nil || info.Mode().Perm() != wantMode || !info.Mode().IsRegular() {
			t.Fatalf("material file %s has unsafe type/mode: info=%v err=%v", name, info, err)
		}
	}
	var manifest prdReplacementServerMaterialManifest
	manifestBytes, err := os.ReadFile(filepath.Join(operationDir, "manifest.json"))
	if err != nil || json.Unmarshal(manifestBytes, &manifest) != nil || manifest.OperationID != operationID ||
		manifest.RequestSHA256 != first.RequestSHA256 || manifest.CertificateSHA != first.CertificateSHA || manifest.CASHA != first.CASHA {
		t.Fatal("durable material journal does not bind the exact operation inputs and outputs")
	}
	var st unix.Stat_t
	if unix.Lstat(filepath.Join(operationDir, "server-ca.key"), &st) != nil || st.Nlink != 1 || st.Mode&0o077 != 0 {
		t.Fatal("persisted CA private key is linked or accessible beyond its owner")
	}
}

func TestPRDReplacementServerMaterialRejectsPartialOrCorruptRetry(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("make task-owned temp parent private: %v", err)
	}
	root := filepath.Join(parent, "operations")
	operationID := "abcdefabcdefabcdefabcdefabcdefab"
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	request, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, "server-material", operationID)
	if err := os.MkdirAll(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePRDReplacementServerMaterialAt(root, operationID, request.RequestPEM, now); err == nil {
		t.Fatal("partial existing operation directory was treated as absent")
	}
}
