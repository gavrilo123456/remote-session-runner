package releaseflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceTestReceiptRequiresCanonicalOwnerOnlyEvidence(t *testing.T) {
	receipt := fixtureSourceTestReceipts("0123456789abcdef0123456789abcdef01234567")[0]
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, ref, err := LoadSourceTestReceiptFile(path)
	if err != nil || !reflect.DeepEqual(loaded, receipt) || ref != digest(append(data, '\n')) {
		t.Fatalf("canonical receipt load=%+v ref=%s err=%v", loaded, ref, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSourceTestReceiptFile(path); err == nil {
		t.Fatal("group/world-readable source-test receipt was accepted")
	}
}

func TestSourceTestReceiptRejectsFailedGateAndPlatformMismatch(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	receipt := fixtureSourceTestReceipts(commit)[0]
	receipt.Gates["vet"] = "failed"
	if validSourceTestReceipt(receipt) {
		t.Fatal("failed vet receipt was accepted")
	}
	receipt = fixtureSourceTestReceipts(commit)[0]
	bundle := Bundle{HostID: receipt.HostID, GOOS: receipt.GOOS, GOARCH: receipt.GOARCH, Toolchain: receipt.Toolchain, SourceRevision: commit}
	settings := HostBuildSettings{HostID: receipt.HostID, GOOS: receipt.GOOS, GOARCH: receipt.GOARCH, ToolchainBinarySHA: receipt.ToolchainBinarySHA256}
	if err := verifyQualificationReceiptForBuild(receipt, receipt.HostID, bundle, settings); err != nil {
		t.Fatalf("matching source-test receipt rejected: %v", err)
	}
	bundle.GOARCH = "mismatch"
	if err := verifyQualificationReceiptForBuild(receipt, receipt.HostID, bundle, settings); err == nil {
		t.Fatal("receipt for a different native platform was accepted")
	}
}

func TestPersistQualificationReceiptsBindsFourHostsToOneCommit(t *testing.T) {
	store := testStore(t)
	commit := "0123456789abcdef0123456789abcdef01234567"
	receipts := fixtureSourceTestReceipts(commit)
	refs, err := persistQualificationReceipts(store, receipts, commit)
	if err != nil || len(refs) != len(HostOrder) {
		t.Fatalf("persist qualification receipts: refs=%v err=%v", refs, err)
	}
	for i, host := range HostOrder {
		if !validQualificationReference(refs[i], host) {
			t.Fatalf("receipt ref[%d]=%q is not bound to %s", i, refs[i], host)
		}
		receipt, err := store.LoadSourceTestReceipt(refDigest(refs[i], host))
		if err != nil || receipt.SourceCommit != commit || receipt.HostID != host {
			t.Fatalf("load %s source receipt: %+v err=%v", host, receipt, err)
		}
	}
	duplicate := append([]SourceTestReceipt(nil), receipts...)
	duplicate[3] = duplicate[0]
	if _, err := persistQualificationReceipts(store, duplicate, commit); err == nil {
		t.Fatal("duplicate host qualification receipts were accepted")
	}
	wrongCommit := append([]SourceTestReceipt(nil), receipts...)
	wrongCommit[0].SourceCommit = "1123456789abcdef0123456789abcdef01234567"
	if _, err := persistQualificationReceipts(store, wrongCommit, commit); err == nil {
		t.Fatal("receipt from another source commit was accepted")
	}
}

func refDigest(ref, host string) string {
	return strings.TrimPrefix(ref, host+":sha256:")
}

func TestSourceTestReceiptTimestampMustBeParseable(t *testing.T) {
	receipt := fixtureSourceTestReceipts("0123456789abcdef0123456789abcdef01234567")[0]
	receipt.TestedAtUTC = time.Now().Format("not-a-timestamp")
	if validSourceTestReceipt(receipt) {
		t.Fatal("invalid timestamp was accepted")
	}
}
