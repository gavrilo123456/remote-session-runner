package laneinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPRDReplacementMacRouteCatalogPreflightRequiresSelectedCompatibleEmptyCatalogState(t *testing.T) {
	valid := prdCatalogProof{
		selectorTarget:      "/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-fixture/mac-control-plane",
		bundleSHA256:        strings.Repeat("b", 64),
		configurationSHA256: strings.Repeat("c", 64),
		schema:              40,
		work:                PRDWorkInventory{},
	}
	valid.workSHA256 = valid.work.SHA256()
	selector := CatalogSelectorStatus{Kind: "catalog", Target: valid.selectorTarget,
		Release: &CatalogReleaseStatus{Identity: CatalogReleaseIdentity{BundleSHA256: valid.bundleSHA256}}}

	got, err := validatePRDReplacementMacRouteCatalogProof(true, valid, selector)
	if err != nil || !got.ServiceActive || got.BundleSHA256 != valid.bundleSHA256 ||
		got.ConfigSHA256 != valid.configurationSHA256 || got.SchemaVersion != 40 || !got.Work.Empty() {
		t.Fatalf("valid selected catalog PRD proof rejected or lost bindings: %+v err=%v", got, err)
	}

	tests := []struct {
		name   string
		mutate func(*prdCatalogProof, *CatalogSelectorStatus)
	}{
		{name: "unselected", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) { p.selectorTarget = "" }},
		{name: "legacy selector", mutate: func(_ *prdCatalogProof, s *CatalogSelectorStatus) { s.Kind = "legacy" }},
		{name: "missing release details", mutate: func(_ *prdCatalogProof, s *CatalogSelectorStatus) { s.Release = nil }},
		{name: "selector proof target mismatch", mutate: func(_ *prdCatalogProof, s *CatalogSelectorStatus) { s.Target += "-other" }},
		{name: "selector bundle mismatch", mutate: func(_ *prdCatalogProof, s *CatalogSelectorStatus) {
			s.Release.Identity.BundleSHA256 = strings.Repeat("e", 64)
		}},
		{name: "legacy bundle identity", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) { p.bundleSHA256 = "" }},
		{name: "malformed catalog digest", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) { p.bundleSHA256 = "not-a-digest" }},
		{name: "unknown schema", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) { p.schema = 0 }},
		{name: "inconsistent work digest", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) { p.workSHA256 = strings.Repeat("d", 64) }},
		{name: "nonempty work", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) {
			p.work.ActiveSessions = 1
			p.workSHA256 = p.work.SHA256()
		}},
		{name: "fresh-install marker on Mac", mutate: func(p *prdCatalogProof, _ *CatalogSelectorStatus) { p.freshInstall = &CatalogFreshInstallBaseline{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := valid
			selected := selector
			test.mutate(&proof, &selected)
			if _, err := validatePRDReplacementMacRouteCatalogProof(false, proof, selected); err == nil {
				t.Fatal("invalid catalog route proof was accepted")
			}
		})
	}
}

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
