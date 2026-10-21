package laneinstall

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestPRDLegacyBundleDigestBindsHostAndEveryInstalledFile(t *testing.T) {
	preflight := PRDPreflight{HostID: "linux-host", BinarySHA256: map[string]string{
		"runnerd": strings.Repeat("a", 64), "runner-log-capture": strings.Repeat("b", 64),
	}}
	first, err := PRDLegacyBundleDigest(preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflight.BinarySHA256["runnerd"] = strings.Repeat("c", 64)
	second, err := PRDLegacyBundleDigest(preflight)
	if err != nil || first == second {
		t.Fatalf("legacy identity did not change with binary bytes: first=%s second=%s err=%v", first, second, err)
	}
	preflight.HostID = "sandbox-host"
	third, err := PRDLegacyBundleDigest(preflight)
	if err != nil || second == third {
		t.Fatalf("legacy identity did not change with host identity: second=%s third=%s err=%v", second, third, err)
	}
	preflight.BinarySHA256["../runnerd"] = strings.Repeat("d", 64)
	if _, err := PRDLegacyBundleDigest(preflight); err == nil {
		t.Fatal("legacy identity accepted a path-bearing component name")
	}
}

func TestPRDCatalogArtifactGroupOwnershipIsCheckedOnlyWhenDeclared(t *testing.T) {
	if !catalogArtifactGroupMatches("", 2002, 1001) {
		t.Fatal("an unspecified artifact group rejected an otherwise owner-verified group")
	}
	if catalogArtifactGroupMatches("runner", 2002, 1001) {
		t.Fatal("an explicitly declared artifact group accepted a different group")
	}
	if !catalogArtifactGroupMatches("runner", 1001, 1001) {
		t.Fatal("an explicitly declared current-account group was rejected")
	}
}

func TestPRDSelectorMutationRequiresGuardedMaintenanceAndSupportsUnselectedState(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	fixture.layout.lane = config.ReleaseLanePRD
	if err := validateCatalogMutationLayout(fixture.layout); err == nil {
		t.Fatal("generic Dev/QAS selector mutation accepted PRD")
	}
	if err := validateCatalogTransitionLayout(fixture.layout); err != nil {
		t.Fatalf("guarded PRD transition layout rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	status, err := inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "unselected" || status.Target != "" {
		t.Fatalf("initial PRD selector status=%+v err=%v", status, err)
	}
	target := filepath.Join(fixture.catalogRoot, fixture.firstRelease, fixture.layout.hostID)
	if err := switchCatalogSelector(fixture.layout, "", target); err != nil {
		t.Fatalf("select initial catalog release: %v", err)
	}
	if err := switchCatalogSelector(fixture.layout, target, ""); err != nil {
		t.Fatalf("return selector to preserved legacy layout: %v", err)
	}
	status, err = inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "unselected" || status.Target != "" {
		t.Fatalf("restored legacy selector status=%+v err=%v", status, err)
	}
}

func TestPRDReconciliationRecognizesExactUnselectedLegacyIdentity(t *testing.T) {
	now := time.Now().UTC()
	legacySHA := strings.Repeat("a", 64)
	configurationSHA := strings.Repeat("b", 64)
	host := CatalogCohortHostPreflight{
		HostID: "mac-control-plane", PreviousBundleSHA256: legacySHA,
		PreviousReadSchemaMax: store.CurrentSchemaVersion, ConfigurationSHA256: configurationSHA, SchemaVersion: store.CurrentSchemaVersion,
		LegacyRollback: validLegacyRollbackEvidence(legacySHA, configurationSHA, store.CurrentSchemaVersion, now),
	}
	status := CatalogSelectorStatus{Kind: "unselected"}
	if !catalogCohortPreviousSelectorMatches(config.ReleaseLanePRD, host, status) {
		t.Fatal("an exact evidence-bound legacy PRD identity was rejected after restoring the unselected launcher")
	}
	status.Kind = "catalog"
	if catalogCohortPreviousSelectorMatches(config.ReleaseLanePRD, host, status) {
		t.Fatal("unselected legacy receipt accepted a catalog selector")
	}
	status = CatalogSelectorStatus{Kind: "unselected"}
	host.LegacyRollback.ConfigurationSHA256 = strings.Repeat("c", 64)
	if catalogCohortPreviousSelectorMatches(config.ReleaseLanePRD, host, status) {
		t.Fatal("legacy rollback identity accepted evidence bound to another configuration")
	}
}

func TestPRDHostFenceCanRetryAfterStopBeforeReceiptWrite(t *testing.T) {
	if !validPRDCohortFenceServiceState(true, false) {
		t.Fatal("fence retry refused the selected service already stopped before receipt persistence")
	}
	if !validPRDCohortFenceServiceState(true, true) || !validPRDCohortFenceServiceState(false, false) {
		t.Fatal("fence service state accepted/rejected a valid preflight state incorrectly")
	}
	if validPRDCohortFenceServiceState(false, true) {
		t.Fatal("fence retry accepted a service started after a stopped preflight")
	}
}

func TestPRDCatalogArtifactReplacementIsExpectedHashBound(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "entrypoint.sh")
	old := []byte("#!/bin/sh\nexec /old/bin/runnerd\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactEntrypoint, InstallPath: path, Mode: 0o700, Contents: "#!/bin/sh\nexec /new/current/bin/runnerd\n"}
	service := &PRDServiceController{}
	if err := replacePRDCatalogArtifact(context.Background(), service, artifact, sha256HexString(old), 0o700); err != nil {
		t.Fatalf("replace backed-up entrypoint: %v", err)
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		t.Fatalf("replacement did not match exact catalog artifact: %v", err)
	}
	conflict := []byte("#!/bin/sh\nexec /unexpected/runnerd\n")
	if err := os.WriteFile(path, conflict, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replacePRDCatalogArtifact(context.Background(), service, artifact, sha256HexString(old), 0o700); err == nil {
		t.Fatal("artifact replacement overwrote a file that differed from both expected identities")
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(conflict) {
		t.Fatalf("conflicting artifact was changed: %q err=%v", actual, err)
	}
}

func TestPRDCatalogReplacementSupportsOnlyCurrentAccountOwnedQueuedWrapper(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	root := catalogPRDArtifactFixtureDir(t)
	path := filepath.Join(root, "runner-ssh-bridge-forced.sh")
	old := []byte("#!/bin/sh\nexec /old/bin/runner-ssh-bridge\n")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Kind: ArtifactSSHWrapper, InstallPath: path, Mode: 0o700, Owner: current.Username, Group: group.Name,
		Contents: "#!/bin/sh\nexec /new/current/bin/runner-ssh-bridge\n"}
	if err := replacePRDCatalogArtifact(context.Background(), &PRDServiceController{}, artifact, sha256HexString(old), 0o700); err != nil {
		t.Fatalf("replace current-account queued wrapper: %v", err)
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		t.Fatalf("installed queued wrapper does not match expected owner, group, mode, and bytes: %v", err)
	}
	foreign := artifact
	foreign.Owner = "nobody"
	if err := replacePRDCatalogArtifact(context.Background(), &PRDServiceController{}, foreign, sha256HexString([]byte(artifact.Contents)), 0o700); !errors.Is(err, ErrSimpleArtifactConflict) {
		t.Fatalf("replacement did not retain the conflict category for a foreign artifact owner: %v", err)
	}
}

func TestPRDLegacyRollbackRestoresLauncherAndQueuedWrapperWithoutChangingDatabase(t *testing.T) {
	root := catalogPRDArtifactFixtureDir(t)
	backupRoot := filepath.Join(root, "backup")
	if err := os.Mkdir(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceRoot := filepath.Join(root, "service")
	if err := os.Mkdir(serviceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	productionDatabase := filepath.Join(root, "production-local.db")
	if err := os.WriteFile(productionDatabase, []byte("preserved production database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	databaseBefore, err := fileSHA256(productionDatabase)
	if err != nil {
		t.Fatal(err)
	}

	definitions := []struct {
		name    string
		kind    ArtifactKind
		legacy  []byte
		catalog string
	}{
		{name: "runner-entrypoint.sh", kind: ArtifactEntrypoint, legacy: []byte("#!/bin/sh\nexec /legacy/bin/runnerd\n"), catalog: "#!/bin/sh\nexec /catalog/current/bin/runnerd\n"},
		{name: "runner-ssh-bridge-forced.sh", kind: ArtifactSSHWrapper, legacy: []byte("#!/bin/sh\nexec /legacy/bin/runner-ssh-bridge\n"), catalog: "#!/bin/sh\nexec /catalog/current/bin/runner-ssh-bridge\n"},
	}
	manifest := PRDBackupManifest{FormatVersion: prdUpgradeBackupFormat, HostID: "linux-host", SchemaVersion: 40,
		DatabasePath: productionDatabase, DatabaseBackupPath: filepath.Join(backupRoot, "database.sqlite")}
	if err := os.WriteFile(manifest.DatabaseBackupPath, []byte("private snapshot placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	var artifacts []Artifact
	for _, definition := range definitions {
		installPath := filepath.Join(serviceRoot, definition.name)
		catalogBytes := []byte(definition.catalog)
		if err := os.WriteFile(installPath, catalogBytes, 0o700); err != nil {
			t.Fatal(err)
		}
		backupPath := filepath.Join(backupRoot, "service", definition.name)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(backupPath, definition.legacy, 0o700); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, PRDBackupFile{
			TargetPath: installPath, BackupPath: filepath.Join("service", definition.name),
			SHA256: sha256HexString(definition.legacy), Bytes: int64(len(definition.legacy)), Mode: 0o700,
		})
		artifacts = append(artifacts, Artifact{Kind: definition.kind, InstallPath: installPath, Mode: 0o700, Contents: definition.catalog})
	}
	if err := restorePRDServiceArtifactsFromBackup(context.Background(), &PRDServiceController{}, artifacts, manifest); err != nil {
		t.Fatalf("restore exact legacy launcher and queued wrapper: %v", err)
	}
	for index, definition := range definitions {
		actual, err := os.ReadFile(artifacts[index].InstallPath)
		if err != nil || string(actual) != string(definition.legacy) {
			t.Fatalf("restored %s=%q err=%v", definition.name, actual, err)
		}
		info, err := os.Stat(artifacts[index].InstallPath)
		if err != nil {
			t.Fatalf("stat restored %s: %v", definition.name, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("restored %s mode=%v", definition.name, info.Mode().Perm())
		}
	}
	databaseAfter, err := fileSHA256(productionDatabase)
	if err != nil || databaseAfter != databaseBefore {
		t.Fatalf("rollback artifact restore changed production database: before=%s after=%s err=%v", databaseBefore, databaseAfter, err)
	}
}

func catalogPRDArtifactFixtureDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	return root
}
