package laneinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBUG028TrustFileAliasGuardAllowsExactSharingAndIndependentFiles(t *testing.T) {
	root := bug028PrivateTempDir(t)
	primary := filepath.Join(root, "poc-ca.pem")
	peer := filepath.Join(root, "independent-ca.pem")
	bug028Must(t, os.WriteFile(primary, []byte("unchanged primary trust"), 0o600))
	bug028Must(t, os.WriteFile(peer, []byte("independent peer trust"), 0o600))
	if err := validatePRDReplacementMacTrustFileAliases(primary, []string{primary, peer, peer}); err != nil {
		t.Fatalf("exact shared and independent CA paths refused: %v", err)
	}
	primaryInfo, err := os.Lstat(primary)
	bug028Must(t, err)
	peerInfo, err := os.Lstat(peer)
	bug028Must(t, err)
	if prdReplacementTrustPathsAlias(primary, peer, primaryInfo, peerInfo) {
		t.Fatal("different files were classified as aliases")
	}
	// Filesystems may return this same identity for two case-variant labels
	// even though its hard-link count is one. This comparison is portable.
	if !prdReplacementTrustPathsAlias(primary, filepath.Join(root, "POC-CA.PEM"), primaryInfo, primaryInfo) {
		t.Fatal("same single-link identity under different labels was not an alias")
	}
	if prdReplacementTrustPathsAlias(primary, primary, primaryInfo, primaryInfo) {
		t.Fatal("exact shared path must remain eligible for additive trust")
	}
}

func TestBUG028TrustFileAliasGuardRejectsActualCaseAlias(t *testing.T) {
	root := bug028PrivateTempDir(t)
	primary := filepath.Join(root, "poc-ca.pem")
	alias := filepath.Join(root, "POC-CA.PEM")
	before := []byte("unchanged case-insensitive filesystem trust")
	bug028Must(t, os.WriteFile(primary, before, 0o600))
	first, err := os.Lstat(primary)
	bug028Must(t, err)
	second, err := os.Lstat(alias)
	if os.IsNotExist(err) {
		t.Skip("temporary filesystem is case-sensitive")
	}
	bug028Must(t, err)
	if !os.SameFile(first, second) {
		t.Skip("case-variant temporary paths do not identify the same file")
	}
	if !catalogFileHasSingleLink(first) {
		t.Fatal("case-alias fixture must retain a single-link file")
	}
	if err := validatePRDReplacementMacTrustFileAliases(primary, []string{alias}); err == nil {
		t.Fatal("real case-insensitive alias was accepted as independent trust")
	}
	after, err := os.ReadFile(primary)
	bug028Must(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("alias rejection changed original trust bytes")
	}
}

func TestBUG028TrustFileAliasGuardAllowsDistinctCaseSensitiveFiles(t *testing.T) {
	root := bug028PrivateTempDir(t)
	primary := filepath.Join(root, "poc-ca.pem")
	peer := filepath.Join(root, "POC-CA.PEM")
	bug028Must(t, os.WriteFile(primary, []byte("primary trust"), 0o600))
	file, err := os.OpenFile(peer, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		t.Skip("temporary filesystem cannot represent independent case-variant names")
	}
	bug028Must(t, err)
	_, err = file.Write([]byte("separate peer trust"))
	bug028Must(t, err)
	bug028Must(t, file.Close())
	if err := validatePRDReplacementMacTrustFileAliases(primary, []string{peer}); err != nil {
		t.Fatalf("independent case-sensitive CA files were refused: %v", err)
	}
}

func TestBUG028TrustFileAliasGuardRejectsUnsafeMetadataAndRecheckedDrift(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "mode", "missing", "directory", "recheck-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := bug028PrivateTempDir(t)
			primary := filepath.Join(root, "primary.pem")
			peer := filepath.Join(root, "peer.pem")
			before := []byte("unchanged primary trust")
			bug028Must(t, os.WriteFile(primary, before, 0o600))
			if kind == "recheck-symlink" {
				bug028Must(t, os.WriteFile(peer, []byte("original independent peer"), 0o600))
				bug028Must(t, validatePRDReplacementMacTrustFileAliases(primary, []string{peer}))
				bug028Must(t, os.Remove(peer))
			}
			switch kind {
			case "symlink", "recheck-symlink":
				bug028Must(t, os.Symlink(primary, peer))
			case "hardlink":
				bug028Must(t, os.Link(primary, peer))
			case "mode":
				bug028Must(t, os.WriteFile(peer, []byte("unsafe peer mode"), 0o600))
				bug028Must(t, os.Chmod(peer, 0o644))
			case "directory":
				bug028Must(t, os.Mkdir(peer, 0o700))
			}
			if err := validatePRDReplacementMacTrustFileAliases(primary, []string{peer}); err == nil {
				t.Fatal("ambiguous, unsafe, missing or changed peer identity was accepted")
			}
			after, err := os.ReadFile(primary)
			bug028Must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("metadata refusal changed primary trust")
			}
		})
	}
}
