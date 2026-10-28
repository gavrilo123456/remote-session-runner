package laneinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestReduceReplacementKnownHostsProducesPinnedCurrentEntry(t *testing.T) {
	retiredHost := "129.151.232.40"
	targetHost := config.SimpleReplacementLinuxPublicIP
	retired, err := parseEd25519PublicKey(l202PublicKey("known-host-refresh-retired"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := parseEd25519PublicKey(l202PublicKey("known-host-refresh-target"))
	if err != nil {
		t.Fatal(err)
	}
	retiredLine := retiredHost + " ssh-ed25519 " + retired.keyData
	currentLine := targetHost + " ssh-ed25519 " + target.keyData
	original := []byte("# verified predecessor and replacement pins\n" + retiredLine + "\n[" + retiredHost + "]:22 ssh-ed25519 " + retired.keyData + "\n" + currentLine + "\n")
	fingerprint := lowerHexFingerprint(target.blob)
	active, err := reduceReplacementKnownHosts(original, retiredHost, targetHost, fingerprint, true)
	if err != nil {
		t.Fatalf("reduce verified predecessor/current file: %v", err)
	}
	if string(active) != currentLine+"\n" {
		t.Fatalf("active host-key file = %q, want only the exact current endpoint pin", active)
	}
	if retry, err := reduceReplacementKnownHosts(active, retiredHost, targetHost, fingerprint, true); err != nil || !bytes.Equal(retry, active) {
		t.Fatalf("replacement-only retry changed output: equal=%v err=%v", bytes.Equal(retry, active), err)
	}
}

func TestPreserveKnownHostsRefreshBackupRetainsExactPriorBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backupDirectory := filepath.Join(root, "lane-backup")
	if err := os.Mkdir(backupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("retired and replacement pins\n")
	path, err := preserveKnownHostsRefreshBackup(backupDirectory, original, digestBytes(original))
	if err != nil {
		t.Fatalf("preserve known_hosts beside immutable backup: %v", err)
	}
	if path != backupDirectory+".known_hosts.before" {
		t.Fatalf("backup path=%q, want a sibling of the immutable backup directory", path)
	}
	verified, err := readOwnedSimpleRegularFile(path, 64*1024)
	if err != nil || !bytes.Equal(verified, original) {
		t.Fatalf("preserved bytes=%q err=%v", verified, err)
	}
}

func TestReduceReplacementKnownHostsRejectsUnsafeOrMismatchedEvidence(t *testing.T) {
	retiredHost := "129.151.232.40"
	targetHost := config.SimpleReplacementLinuxPublicIP
	retired, _ := parseEd25519PublicKey(l202PublicKey("known-host-refresh-retired-reject"))
	target, _ := parseEd25519PublicKey(l202PublicKey("known-host-refresh-target-reject"))
	other, _ := parseEd25519PublicKey(l202PublicKey("known-host-refresh-other-reject"))
	fingerprint := lowerHexFingerprint(target.blob)
	retiredLine := retiredHost + " ssh-ed25519 " + retired.keyData + "\n"
	targetLine := targetHost + " ssh-ed25519 " + target.keyData + "\n"
	cases := []struct {
		name, contents string
	}{
		{name: "target pin missing", contents: retiredLine},
		{name: "target pin mismatch", contents: retiredLine + targetHost + " ssh-ed25519 " + other.keyData + "\n"},
		{name: "duplicate target", contents: retiredLine + targetLine + targetLine},
		{name: "unknown host", contents: retiredLine + targetLine + "sandbox.example ssh-ed25519 " + other.keyData + "\n"},
		{name: "wildcard host", contents: retiredLine + targetLine + "*.example ssh-ed25519 " + other.keyData + "\n"},
		{name: "hashed host", contents: retiredLine + targetLine + "|1|opaque ssh-ed25519 " + other.keyData + "\n"},
		{name: "comma-separated hosts", contents: retiredLine + strings.Replace(targetLine, targetHost, targetHost+",other.example", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reduceReplacementKnownHosts([]byte(tc.contents), retiredHost, targetHost, fingerprint, false); err == nil {
				t.Fatal("unsafe or mismatched host-key evidence was accepted")
			}
		})
	}
}

func TestAppendReplacementKnownHostPreservesOldAndAddsVerifiedKeyOnce(t *testing.T) {
	oldHost := "129.151.232.40"
	newHost := config.SimpleReplacementLinuxPublicIP
	oldKey := l202PublicKey("catalog-rebind-old-host")
	newKey := l202PublicKey("catalog-rebind-new-host")
	oldParsed, err := parseEd25519PublicKey(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	newParsed, err := parseEd25519PublicKey(newKey)
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint := lowerHexFingerprint(oldParsed.blob)
	newFingerprint := lowerHexFingerprint(newParsed.blob)
	original := []byte("# existing selected-profile pin\n" + oldHost + " " + oldKey + "\n[" + oldHost + "]:22 " + oldKey + "\n")
	replacementLine := newHost + " ssh-ed25519 " + newParsed.keyData + "\n"
	updated, err := appendReplacementKnownHost(original, oldHost, oldFingerprint, newHost, newFingerprint, replacementLine)
	if err != nil {
		t.Fatalf("append replacement host key: %v", err)
	}
	if !bytes.HasPrefix(updated, original) || strings.Count(string(updated), replacementLine) != 1 {
		t.Fatalf("old known_hosts evidence was not preserved or new key is not unique: %q", updated)
	}
	retried, err := appendReplacementKnownHost(updated, oldHost, oldFingerprint, newHost, newFingerprint, replacementLine)
	if err != nil || !bytes.Equal(retried, updated) {
		t.Fatalf("idempotent retry changed known_hosts: equal=%v err=%v", bytes.Equal(retried, updated), err)
	}
}

func TestAppendReplacementKnownHostRejectsAmbiguousOrMismatchedPins(t *testing.T) {
	oldHost := "129.151.232.40"
	newHost := config.SimpleReplacementLinuxPublicIP
	oldKey := l202PublicKey("catalog-rebind-old-host-reject")
	newKey := l202PublicKey("catalog-rebind-new-host-reject")
	otherKey := l202PublicKey("catalog-rebind-other-host-reject")
	oldParsed, _ := parseEd25519PublicKey(oldKey)
	newParsed, _ := parseEd25519PublicKey(newKey)
	otherParsed, _ := parseEd25519PublicKey(otherKey)
	oldFingerprint := lowerHexFingerprint(oldParsed.blob)
	newFingerprint := lowerHexFingerprint(newParsed.blob)
	oldEntry := oldHost + " " + oldKey + "\n"
	newEntry := newHost + " ssh-ed25519 " + newParsed.keyData + "\n"
	cases := []struct {
		name, existing, newFingerprint, replacementLine string
	}{
		{name: "old key mismatch", existing: oldHost + " " + otherKey + "\n", newFingerprint: newFingerprint, replacementLine: newEntry},
		{name: "replacement key mismatch", existing: oldEntry, newFingerprint: newFingerprint, replacementLine: newHost + " ssh-ed25519 " + otherParsed.keyData + "\n"},
		{name: "preexisting replacement conflict", existing: oldEntry + newHost + " ssh-ed25519 " + otherParsed.keyData + "\n", newFingerprint: newFingerprint, replacementLine: newEntry},
		{name: "unrelated host entry", existing: oldEntry + "sandbox.example ssh-ed25519 " + otherParsed.keyData + "\n", newFingerprint: newFingerprint, replacementLine: newEntry},
		{name: "missing old pin", existing: "# no host pins\n", newFingerprint: newFingerprint, replacementLine: newEntry},
		{name: "hashed host entry", existing: "|1|opaque ssh-ed25519 " + oldParsed.keyData + "\n", newFingerprint: newFingerprint, replacementLine: newEntry},
		{name: "malformed replacement line", existing: oldEntry, newFingerprint: newFingerprint, replacementLine: newHost + " ssh-rsa invalid\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := appendReplacementKnownHost([]byte(tc.existing), oldHost, oldFingerprint, newHost, tc.newFingerprint, tc.replacementLine); err == nil {
				t.Fatal("ambiguous or mismatched known_hosts evidence was accepted")
			}
		})
	}
}

func TestReplaceOwnedSimpleConfigRequiresExactPrivatePredecessor(t *testing.T) {
	root := resolvedConfigTestDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.yaml")
	old := []byte("version: old\n")
	next := []byte("version: next\n")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(old)
	oldSHA := hex.EncodeToString(digest[:])
	if err := replaceOwnedSimpleConfig(path, oldSHA, next); err != nil {
		t.Fatalf("replace exact predecessor: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(next) {
		t.Fatalf("installed config=%q err=%v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("installed config mode/type=%v err=%v", info, err)
	}
	if err := replaceOwnedSimpleConfig(path, oldSHA, []byte("version: stale\n")); err == nil {
		t.Fatal("stale expected predecessor was accepted")
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != string(next) {
		t.Fatalf("stale replacement changed current config=%q err=%v", got, err)
	}
}

func TestReplaceOwnedSimpleConfigRejectsSymlinkTarget(t *testing.T) {
	root := resolvedConfigTestDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(root, "actual.yaml")
	link := filepath.Join(root, "config.yaml")
	old := []byte("version: old\n")
	if err := os.WriteFile(actual, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(old)
	if err := replaceOwnedSimpleConfig(link, hex.EncodeToString(digest[:]), []byte("version: next\n")); err == nil {
		t.Fatal("symlink target was accepted")
	}
	got, err := os.ReadFile(actual)
	if err != nil || string(got) != string(old) {
		t.Fatalf("symlink target changed=%q err=%v", got, err)
	}
}

func resolvedConfigTestDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
