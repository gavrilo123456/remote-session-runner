package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

type catalogSelectorFixture struct {
	layout        catalogSelectorLayout
	catalogRoot   string
	legacyTarget  string
	firstRelease  string
	secondRelease string
}

func TestCatalogPreflightComparesSelectorBundleIdentityNotSymlinkPayload(t *testing.T) {
	digest := strings.Repeat("a", 64)
	legacy := CatalogSelectorStatus{
		Kind: "legacy", Target: "releases/" + digest,
		Bundle: SimpleBundleManifest{BundleSHA256: digest},
	}
	if got := catalogSelectorStatusBundleSHA256(legacy); got != digest {
		t.Fatalf("legacy selector bundle digest=%q, want %q", got, digest)
	}
	catalog := CatalogSelectorStatus{
		Kind: "catalog", Target: "/home/ubuntu/projects-runtime/runner-releases/cat-20261019-fixture/linux-host",
		Release: &CatalogReleaseStatus{Identity: CatalogReleaseIdentity{BundleSHA256: digest}},
	}
	if got := catalogSelectorStatusBundleSHA256(catalog); got != digest {
		t.Fatalf("catalog selector bundle digest=%q, want %q", got, digest)
	}
	if got := catalogSelectorStatusBundleSHA256(CatalogSelectorStatus{Kind: "catalog"}); got != "" {
		t.Fatalf("malformed catalog selector bundle digest=%q, want empty", got)
	}
}

func newCatalogSelectorFixture(t *testing.T) catalogSelectorFixture {
	t.Helper()
	root := catalogTestTempDir(t)
	serviceRoot := filepath.Join(root, "lane")
	catalogRoot := filepath.Join(root, "runner-releases")
	for _, path := range []string{
		serviceRoot, filepath.Join(serviceRoot, "run"), filepath.Join(serviceRoot, "staging"),
		filepath.Join(serviceRoot, "config"), filepath.Join(serviceRoot, "releases"),
		filepath.Join(serviceRoot, "backups"), filepath.Join(serviceRoot, "state"), catalogRoot,
	} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	configFile := filepath.Join(serviceRoot, "config", "linux.yaml")
	if err := os.WriteFile(configFile, []byte("fixture: lane-config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewSimpleBundle("linux-host", "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("linux-host", "selector-legacy"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleBundleAt(filepath.Join(serviceRoot, "releases", legacy.Manifest.BundleSHA256), legacy); err != nil {
		t.Fatal(err)
	}
	legacyTarget := filepath.Join("releases", legacy.Manifest.BundleSHA256)
	if err := os.Symlink(legacyTarget, filepath.Join(serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}

	firstBundles := catalogFixtureBundles(t, "selector-release-one")
	firstManifest, err := NewCatalogReleaseManifest("v2.0.0", firstBundles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishCatalogRelease(catalogRoot, "linux-host", firstManifest, firstBundles["linux-host"]); err != nil {
		t.Fatal(err)
	}
	secondBundles := catalogFixtureBundles(t, "selector-release-two")
	secondManifest, err := NewCatalogReleaseManifest("v2.0.1", secondBundles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishCatalogRelease(catalogRoot, "linux-host", secondManifest, secondBundles["linux-host"]); err != nil {
		t.Fatal(err)
	}
	return catalogSelectorFixture{
		layout: catalogSelectorLayout{
			serviceRoot: serviceRoot, catalogRoot: catalogRoot,
			operationRoot: filepath.Join(serviceRoot, "staging", "catalog-operations"),
			backupRoot:    filepath.Join(serviceRoot, "backups"), databasePath: filepath.Join(serviceRoot, "state", "remote.db"),
			lifecycleLock: filepath.Join(serviceRoot, "run", "runnerd-lifecycle.lock"),
			configFile:    configFile, hostID: "linux-host", lane: config.ReleaseLaneDev,
		},
		catalogRoot: catalogRoot, legacyTarget: legacyTarget,
		firstRelease: firstManifest.ReleaseID, secondRelease: secondManifest.ReleaseID,
	}
}

func fixtureCatalogBackup(t *testing.T, layout catalogSelectorLayout, activeTarget string, schema int) catalogBackupReference {
	t.Helper()
	operationID, err := newCatalogOperationID()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(layout.backupRoot, ".simple-backup-"+operationID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	configBytes, err := os.ReadFile(layout.configFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(directory, "config.yaml"), configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), layout.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	backupDatabasePath := filepath.Join(directory, "authority.sqlite")
	if err := store.CreateExistingOnlineBackup(context.Background(), database, backupDatabasePath); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(backupDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	activeBundle := ""
	if activeTarget != "" {
		selection, err := resolveCatalogSelectorTarget(layout, activeTarget)
		if err != nil {
			t.Fatal(err)
		}
		activeBundle = selection.bundle.Manifest.BundleSHA256
	}
	configDigest := sha256Hex(configBytes)
	databaseDigest := sha256Hex(databaseBytes)
	manifest := simpleBackupManifest{
		FormatVersion: 1, HostID: layout.hostID, Lane: layout.lane, DatabasePath: layout.databasePath,
		ActiveBundle: activeBundle, DatabaseSchema: schema, ConfigSHA256: configDigest,
		DatabaseSHA256: databaseDigest, CreatedAt: time.Now().UTC(),
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writePrivateFile(filepath.Join(directory, "manifest.json"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return catalogBackupReference{Directory: directory, ManifestSHA256: sha256Hex(manifestBytes)}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestCatalogSelectorSwitchReconcilesInterruptedAtomicReplacement(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	beforeBackup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	beforeSwitch, err := selectCatalogReleaseAt(fixture.layout, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, beforeBackup, func(stage string) error {
		if stage == catalogSelectorAfterPrepared {
			return errors.New("prepared record durable; selector not yet switched")
		}
		return nil
	})
	if beforeSwitch.Stage != catalogOperationPrepared || err == nil {
		t.Fatalf("pre-switch interruption operation=%+v err=%v", beforeSwitch, err)
	}
	finalized, err := reconcileCatalogSelectorOperation(fixture.layout, beforeSwitch.OperationID)
	if err != nil || finalized.Stage != catalogOperationComplete {
		t.Fatalf("pre-switch recovery operation=%+v err=%v", finalized, err)
	}
	firstTarget := filepath.Join(fixture.catalogRoot, fixture.firstRelease, "linux-host")
	afterBackup := fixtureCatalogBackup(t, fixture.layout, firstTarget, store.CurrentSchemaVersion)
	interrupted := errors.New("simulated process interruption")
	operation, err := selectCatalogReleaseAt(fixture.layout, fixture.secondRelease, firstTarget, store.CurrentSchemaVersion, afterBackup, func(stage string) error {
		if stage == catalogSelectorAfterSwitch {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) || operation.Stage != catalogOperationPrepared {
		t.Fatalf("switch interruption operation=%+v err=%v", operation, err)
	}
	status, err := inspectCatalogSelector(fixture.layout)
	if err != nil || status.Kind != "catalog" || status.Release == nil || status.Release.Identity.ReleaseID != fixture.secondRelease {
		t.Fatalf("actual selector after interruption=%+v err=%v", status, err)
	}
	reconciled, err := reconcileCatalogSelectorOperation(fixture.layout, operation.OperationID)
	if err != nil || reconciled.Stage != catalogOperationComplete {
		t.Fatalf("interrupted selector finalization=%+v err=%v", reconciled, err)
	}
	if _, err := reconcileCatalogSelectorOperation(fixture.layout, operation.OperationID); err != nil {
		t.Fatalf("idempotent finalization retry failed: %v", err)
	}
}

func TestCatalogSelectorConcurrentSwitchesUseExpectedPredecessorAndPreservePeerLane(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	qasRoot := filepath.Join(filepath.Dir(fixture.layout.serviceRoot), "qas")
	for _, path := range []string{qasRoot, filepath.Join(qasRoot, "run"), filepath.Join(qasRoot, "staging"), filepath.Join(qasRoot, "config"), filepath.Join(qasRoot, "releases"), filepath.Join(qasRoot, "backups"), filepath.Join(qasRoot, "state")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	qasConfig := filepath.Join(qasRoot, "config", "linux.yaml")
	if err := os.WriteFile(qasConfig, []byte("fixture: qas\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	qas := catalogSelectorLayout{
		serviceRoot: qasRoot, catalogRoot: fixture.catalogRoot,
		operationRoot: filepath.Join(qasRoot, "staging", "catalog-operations"),
		backupRoot:    filepath.Join(qasRoot, "backups"), databasePath: filepath.Join(qasRoot, "state", "remote.db"),
		lifecycleLock: filepath.Join(qasRoot, "run", "runnerd-lifecycle.lock"),
		configFile:    qasConfig, hostID: "linux-host", lane: config.ReleaseLaneQAS,
	}
	qasLegacy, err := NewSimpleBundle("linux-host", "1123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("linux-host", "qas-legacy"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSimpleBundleAt(filepath.Join(qas.serviceRoot, "releases", qasLegacy.Manifest.BundleSHA256), qasLegacy); err != nil {
		t.Fatal(err)
	}
	qasLegacyTarget := filepath.Join("releases", qasLegacy.Manifest.BundleSHA256)
	if err := os.Symlink(qasLegacyTarget, filepath.Join(qas.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}

	concurrentBackup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, releaseID := range []string{fixture.firstRelease, fixture.secondRelease} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := selectCatalogReleaseAt(fixture.layout, id, fixture.legacyTarget, store.CurrentSchemaVersion, concurrentBackup, nil)
			results <- err
		}(releaseID)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCatalogSelectorConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent selector result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent selection successes=%d conflicts=%d, want one each", successes, conflicts)
	}
	qasStatus, err := inspectCatalogSelector(qas)
	if err != nil || qasStatus.Target != qasLegacyTarget {
		t.Fatalf("Dev switch changed peer QAS selector: %+v err=%v", qasStatus, err)
	}
}

func TestCatalogSelectorRollbackRequiresCompatibleSchemaAndUnchangedConfiguration(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	firstBackup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	first, err := selectCatalogReleaseAt(fixture.layout, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, firstBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondBackup := fixtureCatalogBackup(t, fixture.layout, first.NextTarget, store.CurrentSchemaVersion)
	second, err := selectCatalogReleaseAt(fixture.layout, fixture.secondRelease, first.NextTarget, store.CurrentSchemaVersion, secondBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	rollbackBackup := fixtureCatalogBackup(t, fixture.layout, second.NextTarget, store.CurrentSchemaVersion)
	if _, err := rollbackCatalogSelectorAt(fixture.layout, second.OperationID, store.CurrentSchemaVersion+1, rollbackBackup, nil); !errors.Is(err, ErrCatalogRollbackUnsafe) {
		t.Fatalf("rollback accepted a schema outside the previous release range: %v", err)
	}
	if _, err := rollbackCatalogSelectorAt(fixture.layout, second.OperationID, store.CurrentSchemaVersion, rollbackBackup, nil); err != nil {
		t.Fatalf("compatible binary-only rollback failed: %v", err)
	}
	status, err := inspectCatalogSelector(fixture.layout)
	if err != nil || status.Release == nil || status.Release.Identity.ReleaseID != fixture.firstRelease {
		t.Fatalf("compatible rollback selected wrong release: %+v err=%v", status, err)
	}
}

func TestCatalogSelectorRollbackFinalizationCanBeReconciled(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	firstBackup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	first, err := selectCatalogReleaseAt(fixture.layout, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, firstBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondBackup := fixtureCatalogBackup(t, fixture.layout, first.NextTarget, store.CurrentSchemaVersion)
	second, err := selectCatalogReleaseAt(fixture.layout, fixture.secondRelease, first.NextTarget, store.CurrentSchemaVersion, secondBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("simulated rollback journal interruption")
	rollbackBackup := fixtureCatalogBackup(t, fixture.layout, second.NextTarget, store.CurrentSchemaVersion)
	operation, err := rollbackCatalogSelectorAt(fixture.layout, second.OperationID, store.CurrentSchemaVersion, rollbackBackup, func(stage string) error {
		if stage == catalogSelectorAfterSwitch {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) || operation.Stage != catalogOperationRollbackPrepare {
		t.Fatalf("rollback interruption operation=%+v err=%v", operation, err)
	}
	finalized, err := reconcileCatalogSelectorOperation(fixture.layout, second.OperationID)
	if err != nil || finalized.Stage != catalogOperationRolledBack {
		t.Fatalf("rollback finalization retry=%+v err=%v", finalized, err)
	}
}

func TestCatalogSelectorRollbackRejectsChangedConfigurationAndJournalTampering(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	firstBackup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	first, err := selectCatalogReleaseAt(fixture.layout, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, firstBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondBackup := fixtureCatalogBackup(t, fixture.layout, first.NextTarget, store.CurrentSchemaVersion)
	second, err := selectCatalogReleaseAt(fixture.layout, fixture.secondRelease, first.NextTarget, store.CurrentSchemaVersion, secondBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.layout.configFile, []byte("fixture: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rollbackBackup := fixtureCatalogBackup(t, fixture.layout, second.NextTarget, store.CurrentSchemaVersion)
	if _, err := rollbackCatalogSelectorAt(fixture.layout, second.OperationID, store.CurrentSchemaVersion, rollbackBackup, nil); !errors.Is(err, ErrCatalogRollbackUnsafe) {
		t.Fatalf("rollback accepted changed configuration: %v", err)
	}

	other := newCatalogSelectorFixture(t)
	otherBackup := fixtureCatalogBackup(t, other.layout, other.legacyTarget, store.CurrentSchemaVersion)
	operation, err := selectCatalogReleaseAt(other.layout, other.firstRelease, other.legacyTarget, store.CurrentSchemaVersion, otherBackup, func(stage string) error {
		if stage == catalogSelectorAfterPrepared {
			return errors.New("leave prepared record")
		}
		return nil
	})
	if err == nil {
		t.Fatal("interruption hook did not leave a prepared record")
	}
	path := filepath.Join(other.layout.operationRoot, operation.OperationID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tampered CatalogSelectorOperation
	if err := json.Unmarshal(data, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.NextTarget = filepath.Join(other.catalogRoot, "foreign", "linux-host")
	data, err = json.MarshalIndent(tampered, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCatalogSelectorOperation(other.layout, operation.OperationID); !errors.Is(err, ErrCatalogOperationInvalid) {
		t.Fatalf("tampered operation record was accepted: %v", err)
	}

	backupCase := newCatalogSelectorFixture(t)
	backup := fixtureCatalogBackup(t, backupCase.layout, backupCase.legacyTarget, store.CurrentSchemaVersion)
	prepared, err := selectCatalogReleaseAt(backupCase.layout, backupCase.firstRelease, backupCase.legacyTarget, store.CurrentSchemaVersion, backup, func(stage string) error {
		if stage == catalogSelectorAfterPrepared {
			return errors.New("leave operation for backup integrity check")
		}
		return nil
	})
	if err == nil {
		t.Fatal("selector operation did not stop at prepared state")
	}
	if err := os.WriteFile(filepath.Join(backup.Directory, "authority.sqlite"), []byte("tampered backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCatalogSelectorOperation(backupCase.layout, prepared.OperationID); !errors.Is(err, ErrCatalogOperationInvalid) {
		t.Fatalf("operation with a modified backup was reconciled: %v", err)
	}
	current, err := inspectCatalogSelector(backupCase.layout)
	if err != nil || current.Target != backupCase.legacyTarget {
		t.Fatalf("unsafe recovery changed the selector: %+v err=%v", current, err)
	}
}

func TestCatalogSelectorRejectsWrongHostAndProductionLane(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	wrongLane := fixture.layout
	wrongLane.lane = config.ReleaseLanePRD
	if _, err := selectCatalogReleaseAt(wrongLane, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, catalogBackupReference{}, nil); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("Dev selector primitive accepted PRD lane: %v", err)
	}
	wrongHost := fixture.layout
	wrongHost.hostID = "sandbox-host"
	if _, err := inspectCatalogSelector(wrongHost); !errors.Is(err, ErrInvalidSimpleBundle) {
		t.Fatalf("selector accepted a bundle for another host: %v", err)
	}
}

func TestCatalogExecutablePathMustMatchExactSelectedComponent(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	selectedBundle := filepath.Join(fixture.catalogRoot, fixture.firstRelease, "linux-host")
	selectedRunner := filepath.Join(selectedBundle, "bin", "runnerd")
	if err := validateCatalogExecutablePath(selectedRunner, selectedRunner); err != nil {
		t.Fatalf("selected catalog component path rejected: %v", err)
	}
	otherComponent := filepath.Join(selectedBundle, "bin", "runner-ssh-bridge")
	if err := validateCatalogExecutablePath(selectedRunner, otherComponent); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("catalog accepted a different component as runnerd: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "runnerd")
	if err := os.Symlink(otherComponent, alias); err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogExecutablePath(selectedRunner, alias); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("catalog accepted an executable alias outside the selected component: %v", err)
	}
}

func TestCatalogLegacyAdoptionCopiesVerifiedSelectedBundleWithoutLinksOrSwitching(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	destination := filepath.Join(filepath.Dir(fixture.layout.serviceRoot), "adopted-legacy-bundle")
	manifest, err := copySelectedLegacyBundle(fixture.layout, destination)
	if err != nil || manifest.HostID != "linux-host" || manifest.BundleSHA256 != filepath.Base(fixture.legacyTarget) {
		t.Fatalf("legacy adoption manifest=%+v err=%v", manifest, err)
	}
	loaded, err := LoadSimpleBundleDirectory(destination)
	if err != nil || loaded.Manifest.BundleSHA256 != manifest.BundleSHA256 {
		t.Fatalf("copied legacy bundle failed verification: bundle=%+v err=%v", loaded.Manifest, err)
	}
	sourceInfo, sourceErr := os.Stat(filepath.Join(fixture.layout.serviceRoot, fixture.legacyTarget, "bin", "runnerd"))
	destinationInfo, destinationErr := os.Stat(filepath.Join(destination, "bin", "runnerd"))
	if sourceErr != nil || destinationErr != nil || os.SameFile(sourceInfo, destinationInfo) {
		t.Fatalf("adoption hard-linked source or could not inspect copy: source=%v destination=%v errors=%v/%v", sourceInfo, destinationInfo, sourceErr, destinationErr)
	}
	status, err := inspectCatalogSelector(fixture.layout)
	if err != nil || status.Target != fixture.legacyTarget || status.Kind != "legacy" {
		t.Fatalf("adoption changed installed selector: status=%+v err=%v", status, err)
	}
	if _, err := copySelectedLegacyBundle(fixture.layout, destination); err != nil {
		t.Fatalf("exact idempotent adoption copy failed: %v", err)
	}
	if _, err := copySelectedLegacyBundle(fixture.layout, filepath.Join(fixture.layout.serviceRoot, "staging", "unsafe")); !errors.Is(err, ErrInvalidCatalogRelease) {
		t.Fatalf("adoption wrote inside mutable lane state: %v", err)
	}
	backup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	if _, err := selectCatalogReleaseAt(fixture.layout, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, backup, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := copySelectedLegacyBundle(fixture.layout, filepath.Join(filepath.Dir(fixture.layout.serviceRoot), "adopt-after-switch")); !errors.Is(err, ErrCatalogLegacyBundleUnavailable) {
		t.Fatalf("catalog-selected lane was accepted as a legacy source: %v", err)
	}
}

func fixtureCatalogCohortSwitch(t *testing.T, fixture catalogSelectorFixture, releaseID, previousTarget, cohortID string) CatalogSelectorOperation {
	t.Helper()
	backup := fixtureCatalogBackup(t, fixture.layout, previousTarget, store.CurrentSchemaVersion)
	backup.CohortOperationID = cohortID
	backup.CohortEvidenceSHA256 = strings.Repeat("e", 64)
	operation, err := selectCatalogReleaseAt(fixture.layout, releaseID, previousTarget, store.CurrentSchemaVersion, backup, nil)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestCatalogSelectorDiscoveryIgnoresRetiredUnrelatedPayloads(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	oldCohort, currentCohort := strings.Repeat("1", 32), strings.Repeat("2", 32)
	old := fixtureCatalogCohortSwitch(t, fixture, fixture.firstRelease, fixture.legacyTarget, oldCohort)
	rollbackBackup := fixtureCatalogBackup(t, fixture.layout, old.NextTarget, store.CurrentSchemaVersion)
	old, err := rollbackCatalogSelectorAt(fixture.layout, old.OperationID, store.CurrentSchemaVersion, rollbackBackup, nil)
	if err != nil {
		t.Fatal(err)
	}
	current := fixtureCatalogCohortSwitch(t, fixture, fixture.secondRelease, fixture.legacyTarget, currentCohort)
	oldPath := filepath.Join(fixture.layout.operationRoot, old.OperationID+".json")
	oldBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}

	// Retire only the old rollout's disposable payloads. Its canonical journal
	// remains present, while the current rollout retains its own full backup.
	retired := filepath.Join(fixture.catalogRoot, fixture.firstRelease)
	if err := filepath.Walk(retired, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{retired, old.BackupDirectory, old.RollbackBackupDirectory} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	operations, err := findCatalogSelectorOperationsAt(fixture.layout, currentCohort)
	if err != nil || len(operations) != 1 || operations[0].OperationID != current.OperationID {
		t.Fatalf("unrelated retired history blocked discovery: operations=%+v err=%v", operations, err)
	}
	if after, err := os.ReadFile(oldPath); err != nil || string(after) != string(oldBytes) {
		t.Fatalf("discovery changed preserved historical bytes: err=%v", err)
	}
	if selected, err := os.Readlink(filepath.Join(fixture.layout.serviceRoot, "current")); err != nil || selected != current.NextTarget {
		t.Fatalf("discovery changed the selector: target=%q err=%v", selected, err)
	}

	// A cohort binding never exempts a matching journal from physical checks.
	// Retagging this incomplete history cannot make it an accepted operation.
	old.CohortOperationID = currentCohort
	if err := writeCatalogSelectorOperation(fixture.layout.operationRoot, old); err != nil {
		t.Fatal(err)
	}
	if _, err := findCatalogSelectorOperationsAt(fixture.layout, currentCohort); !errors.Is(err, ErrCatalogOperationInvalid) {
		t.Fatalf("matching journal with missing release/backup was accepted: %v", err)
	}
}

func TestCatalogSelectorDiscoveryScopesMissingPredecessorToItsCohort(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	oldCohort, currentCohort := strings.Repeat("1", 32), strings.Repeat("2", 32)
	old := fixtureCatalogCohortSwitch(t, fixture, fixture.firstRelease, fixture.legacyTarget, oldCohort)
	current := fixtureCatalogCohortSwitch(t, fixture, fixture.secondRelease, old.NextTarget, currentCohort)
	if err := os.RemoveAll(filepath.Join(fixture.layout.serviceRoot, fixture.legacyTarget)); err != nil {
		t.Fatal(err)
	}
	operations, err := findCatalogSelectorOperationsAt(fixture.layout, currentCohort)
	if err != nil || len(operations) != 1 || operations[0].OperationID != current.OperationID {
		t.Fatalf("unrelated missing predecessor blocked discovery: operations=%+v err=%v", operations, err)
	}
	if _, err := findCatalogSelectorOperationsAt(fixture.layout, oldCohort); !errors.Is(err, ErrCatalogOperationInvalid) {
		t.Fatalf("matching journal with missing predecessor was accepted: %v", err)
	}
}

func TestCatalogSelectorDiscoveryRejectsUnsafeUnrelatedJournals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, catalogSelectorFixture, *CatalogSelectorOperation, string)
	}{
		{"wrong_lane", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			operation.Lane = config.ReleaseLaneQAS
		}},
		{"next_target_identity_mismatch", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			operation.NextTarget = filepath.Join(fixture.catalogRoot, fixture.secondRelease, fixture.layout.hostID)
		}},
		{"escaping_previous_target", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			operation.PreviousTarget = filepath.Join("..", "foreign-lane", "releases", strings.Repeat("a", 64))
		}},
		{"foreign_backup_root", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			operation.BackupDirectory = filepath.Join(t.TempDir(), ".simple-backup-"+strings.Repeat("a", 32))
		}},
		{"unknown_json_field", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.TrimSuffix(string(data), "}\n") + ",\n  \"unknown_field\": true\n}\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"noncanonical_json", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString(" "); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"public_mode", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "preserved.json")
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, fixture catalogSelectorFixture, operation *CatalogSelectorOperation, path string) {
			if err := os.Link(path, filepath.Join(t.TempDir(), "second-link.json")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCatalogSelectorFixture(t)
			currentCohort := strings.Repeat("2", 32)
			current := fixtureCatalogCohortSwitch(t, fixture, fixture.firstRelease, fixture.legacyTarget, currentCohort)
			old := current
			old.OperationID = strings.Repeat("1", 32)
			old.CohortOperationID = strings.Repeat("3", 32)
			if err := writeCatalogSelectorOperation(fixture.layout.operationRoot, old); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.layout.operationRoot, old.OperationID+".json")
			test.mutate(t, fixture, &old, path)
			if test.name == "wrong_lane" || test.name == "next_target_identity_mismatch" || test.name == "escaping_previous_target" || test.name == "foreign_backup_root" {
				if err := writeCatalogSelectorOperation(fixture.layout.operationRoot, old); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := findCatalogSelectorOperationsAt(fixture.layout, currentCohort); !errors.Is(err, ErrCatalogOperationInvalid) {
				t.Fatalf("unsafe unrelated journal was ignored: %v", err)
			}
		})
	}
}

func TestCatalogSelectorDiscoveryPreservesMatchingJournalAmbiguity(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	cohort := strings.Repeat("2", 32)
	first := fixtureCatalogCohortSwitch(t, fixture, fixture.firstRelease, fixture.legacyTarget, cohort)
	second := fixtureCatalogCohortSwitch(t, fixture, fixture.secondRelease, first.NextTarget, cohort)
	operations, err := findCatalogSelectorOperationsAt(fixture.layout, cohort)
	if err != nil || len(operations) != 2 || operations[0].OperationID != first.OperationID || operations[1].OperationID != second.OperationID {
		t.Fatalf("discovery concealed ambiguous matching journals: operations=%+v err=%v", operations, err)
	}
}

func TestCatalogSelectorDiscoveryRequiresBackupOnlyForMatchingCohort(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	cohort := strings.Repeat("2", 32)
	operation := fixtureCatalogCohortSwitch(t, fixture, fixture.firstRelease, fixture.legacyTarget, cohort)
	if err := os.RemoveAll(operation.BackupDirectory); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{operation.PreviousTarget, operation.NextTarget} {
		if _, err := resolveCatalogSelectorTarget(fixture.layout, target); err != nil {
			t.Fatalf("release payload unexpectedly unavailable: target=%q err=%v", target, err)
		}
	}
	operations, err := findCatalogSelectorOperationsAt(fixture.layout, strings.Repeat("3", 32))
	if err != nil || len(operations) != 0 {
		t.Fatalf("unrelated missing backup blocked discovery: operations=%+v err=%v", operations, err)
	}
	if _, err := findCatalogSelectorOperationsAt(fixture.layout, cohort); !errors.Is(err, ErrCatalogOperationInvalid) {
		t.Fatalf("matching journal with missing backup was accepted: %v", err)
	}
}
