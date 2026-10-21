package laneinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

type cohortRollbackSyncHostFixture struct {
	selector    catalogSelectorFixture
	backup      catalogBackupReference
	operationID string
}

func TestCatalogCohortRollbackReconciliationSyncsEveryHostAfterEachRollback(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("a", 64)
	root := catalogTestTempDir(t)
	catalogRootsRoot := filepath.Join(root, "runner-releases")
	if err := os.Mkdir(catalogRootsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bundles := catalogFixtureBundles(t, "rollback-sync-release")
	manifest, err := NewCatalogReleaseManifest("v2.0.0-rollback-sync", bundles)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		catalogRoot := filepath.Join(catalogRootsRoot, hostID)
		if err := os.Mkdir(catalogRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PublishCatalogRelease(catalogRoot, hostID, manifest, bundles[hostID]); err != nil {
			t.Fatalf("publish fixture release for %s: %v", hostID, err)
		}
	}

	hosts := make(map[string]cohortRollbackSyncHostFixture, len(catalogCohortSwitchOrder))
	receipt := validCatalogCohortFixture(now)
	receipt.ReleaseID = manifest.ReleaseID
	receipt.ReleaseSHA256 = ""
	for _, hostID := range catalogCohortSwitchOrder {
		fixture, identity := newCohortRollbackSyncHostFixture(t, root, filepath.Join(catalogRootsRoot, hostID), hostID, manifest.ReleaseID)
		backup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
		hosts[hostID] = cohortRollbackSyncHostFixture{selector: fixture, backup: backup}

		index := cohortHostIndex(receipt, hostID)
		if index < 0 {
			t.Fatalf("receipt missing host %s", hostID)
		}
		host := &receipt.Hosts[index]
		host.Target = identity
		if receipt.ReleaseSHA256 == "" {
			receipt.ReleaseSHA256 = identity.ReleaseSHA256
		}
		host.PreviousTarget = fixture.legacyTarget
		host.PreviousBundleSHA256 = filepath.Base(fixture.legacyTarget)
		host.ConfigurationSHA256, err = hashCatalogConfiguration(fixture.layout.configFile)
		if err != nil {
			t.Fatal(err)
		}
		host.SchemaVersion = store.CurrentSchemaVersion
		host.BackupDirectory = backup.Directory
		host.BackupManifestSHA256 = backup.ManifestSHA256
		host.PreflightAt = now.Format(time.RFC3339Nano)
	}
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("four-host fixture receipt invalid: %v", err)
	}

	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", proof, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	linuxOperation := switchCohortFixtureHost(t, hosts["linux-host"], receipt, proof)
	linuxFixture := hosts["linux-host"]
	linuxFixture.operationID = linuxOperation.OperationID
	hosts["linux-host"] = linuxFixture
	linuxDigest, err := catalogSelectorOperationDigest(hosts["linux-host"].selector.layout, linuxOperation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", "linux-host", linuxDigest, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", "sandbox-host", proof, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	sandboxOperation := switchCohortFixtureHost(t, hosts["sandbox-host"], receipt, proof)
	sandboxFixture := hosts["sandbox-host"]
	sandboxFixture.operationID = sandboxOperation.OperationID
	hosts["sandbox-host"] = sandboxFixture
	sandboxDigest, err := catalogSelectorOperationDigest(hosts["sandbox-host"].selector.layout, sandboxOperation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	switchingCopy, err := AdvanceCatalogCohortReceipt(receipt, "switch", "sandbox-host", sandboxDigest, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	blockedCopy, err := AdvanceCatalogCohortReceipt(receipt, "block", "", proof, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{blockedCopy, switchingCopy}, now.Add(6*time.Second))
	if err != nil || reconciled.Stage != CatalogCohortBlocked {
		t.Fatalf("reconcile divergent switched histories stage=%q err=%v", reconciled.Stage, err)
	}
	if !catalogCohortReceiptHasHistoricalBranch(reconciled, blockedCopy.Transitions) ||
		!catalogCohortReceiptHasHistoricalBranch(reconciled, switchingCopy.Transitions) {
		t.Fatal("reconciliation discarded one of the blocked/switching histories")
	}

	// Start from different durable copies to prove that every host follows the
	// supported local-state validation and synchronization path.
	for _, hostID := range catalogCohortSwitchOrder {
		local := blockedCopy
		if hostID == "sandbox-host" || hostID == "factory-sandbox" {
			local = switchingCopy
		}
		if _, err := writeCatalogCohortReceiptAt(cohortFixtureReceiptRoot(hosts[hostID]), local); err != nil {
			t.Fatalf("write initial %s receipt: %v", hostID, err)
		}
	}
	syncCohortRollbackReceiptToAllHosts(t, hosts, reconciled)

	reconciled, err = AdvanceCatalogCohortReceipt(reconciled, "rollback-start", "", proof, now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	syncCohortRollbackReceiptToAllHosts(t, hosts, reconciled)

	// Reverse the actual selector journal and synchronize before proceeding to
	// the next host, matching the supported cohort recovery order.
	rollbackCohortFixtureHost(t, hosts["sandbox-host"], &reconciled, now.Add(8*time.Second))
	syncCohortRollbackReceiptToAllHosts(t, hosts, reconciled)
	for _, hostID := range catalogCohortSwitchOrder {
		if err := validateCohortRollbackFixtureLocalState(hosts[hostID], reconciled); err != nil {
			t.Fatalf("post-Sandbox synchronization rejected %s: %v", hostID, err)
		}
	}

	rollbackCohortFixtureHost(t, hosts["linux-host"], &reconciled, now.Add(9*time.Second))
	syncCohortRollbackReceiptToAllHosts(t, hosts, reconciled)
	reconciled, err = AdvanceCatalogCohortReceipt(reconciled, "rollback-complete", "", proof, now.Add(10*time.Second))
	if err != nil || reconciled.Stage != CatalogCohortRolledBack {
		t.Fatalf("rollback completion stage=%q err=%v", reconciled.Stage, err)
	}
	syncCohortRollbackReceiptToAllHosts(t, hosts, reconciled)
	for _, hostID := range catalogCohortSwitchOrder {
		if err := validateCohortRollbackFixtureLocalState(hosts[hostID], reconciled); err != nil {
			t.Fatalf("final synchronized rollback rejected %s: %v", hostID, err)
		}
		stored, err := readCatalogCohortReceipt(filepath.Join(cohortFixtureReceiptRoot(hosts[hostID]), reconciled.OperationID+".json"))
		if err != nil || stored.Stage != CatalogCohortRolledBack || !catalogCohortReceiptHasHistoricalBranch(stored, switchingCopy.Transitions) {
			t.Fatalf("%s does not retain synchronized rollback and original switching history: stage=%q err=%v", hostID, stored.Stage, err)
		}
	}

	sandbox := hosts["sandbox-host"]
	sandboxOperation, err = readCatalogSelectorOperation(sandbox.selector.layout.operationRoot, sandbox.operationID)
	if err != nil {
		t.Fatal(err)
	}
	completedInsteadOfRolledBack := sandboxOperation
	completedInsteadOfRolledBack.Stage = catalogOperationComplete
	if err := writeCatalogSelectorOperation(sandbox.selector.layout.operationRoot, completedInsteadOfRolledBack); err != nil {
		t.Fatal(err)
	}
	if err := validateCohortRollbackFixtureLocalState(sandbox, reconciled); err == nil {
		t.Fatal("a switched journal was accepted for a receipt that records rollback")
	}
	if err := writeCatalogSelectorOperation(sandbox.selector.layout.operationRoot, sandboxOperation); err != nil {
		t.Fatal(err)
	}
	status, err := inspectCatalogSelector(sandbox.selector.layout)
	if err != nil {
		t.Fatal(err)
	}
	status.Target = sandboxOperation.NextTarget
	configHash, err := hashCatalogConfiguration(sandbox.selector.layout.configFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogCohortHistoryLocalSelectorState(sandbox.selector.layout, reconciled, status,
		store.CurrentSchemaVersion, configHash); err == nil {
		t.Fatal("a selector pointing at the new release was accepted after rollback")
	}
	status, err = inspectCatalogSelector(sandbox.selector.layout)
	if err != nil {
		t.Fatal(err)
	}
	wrongPrevious := sandboxOperation
	wrongPrevious.PreviousTarget = sandboxOperation.NextTarget
	if err := validateCatalogCohortReconciledSelectorEvidence(sandbox.selector.layout, reconciled, status,
		store.CurrentSchemaVersion, configHash, []CatalogSelectorOperation{wrongPrevious}); err == nil {
		t.Fatal("a rollback journal whose PreviousTarget disagrees with the recorded prior selector was accepted")
	}
	badRollbackEvidence := reconciled
	badRollbackEvidence.Transitions = append([]CatalogCohortTransition(nil), reconciled.Transitions...)
	for index := range badRollbackEvidence.Transitions {
		if badRollbackEvidence.Transitions[index].Action == "rollback-host" && badRollbackEvidence.Transitions[index].HostID == "sandbox-host" {
			badRollbackEvidence.Transitions[index].EvidenceSHA256 = proof
			break
		}
	}
	if err := validateCohortRollbackFixtureLocalState(sandbox, badRollbackEvidence); err == nil {
		t.Fatal("a receipt with a mismatched rollback-operation digest was accepted")
	}
}

func newCohortRollbackSyncHostFixture(t *testing.T, root, catalogRoot, hostID, releaseID string) (catalogSelectorFixture, CatalogReleaseIdentity) {
	t.Helper()
	serviceRoot := filepath.Join(root, hostID)
	for _, path := range []string{
		serviceRoot, filepath.Join(serviceRoot, "run"), filepath.Join(serviceRoot, "staging"),
		filepath.Join(serviceRoot, "config"), filepath.Join(serviceRoot, "releases"),
		filepath.Join(serviceRoot, "backups"), filepath.Join(serviceRoot, "state"),
	} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("mkdir fixture %s: %v", path, err)
		}
	}
	configFile := filepath.Join(serviceRoot, "config", hostID+".yaml")
	if err := writePrivateFile(configFile, []byte("fixture: "+hostID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewSimpleBundle(hostID, "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents(hostID, "rollback-sync-legacy"))
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
	lane := config.ReleaseLaneDev
	layout := catalogSelectorLayout{
		serviceRoot: serviceRoot, catalogRoot: catalogRoot,
		operationRoot: filepath.Join(serviceRoot, "staging", "catalog-operations"),
		backupRoot:    filepath.Join(serviceRoot, "backups"), databasePath: filepath.Join(serviceRoot, "state", "remote.db"),
		lifecycleLock: filepath.Join(serviceRoot, "run", "runnerd-lifecycle.lock"), configFile: configFile,
		hostID: hostID, lane: lane,
	}
	identity, err := InspectCatalogRelease(catalogRoot, hostID, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	return catalogSelectorFixture{layout: layout, catalogRoot: catalogRoot, legacyTarget: legacyTarget, firstRelease: releaseID}, identity.Identity
}

func switchCohortFixtureHost(t *testing.T, fixture cohortRollbackSyncHostFixture, receipt CatalogCohortReceipt, proof string) CatalogSelectorOperation {
	t.Helper()
	backup := fixture.backup
	backup.CohortOperationID = receipt.OperationID
	backup.CohortEvidenceSHA256 = proof
	operation, err := selectCatalogReleaseAt(fixture.selector.layout, receipt.ReleaseID,
		fixture.selector.legacyTarget, store.CurrentSchemaVersion, backup, nil)
	if err != nil || operation.Stage != catalogOperationComplete {
		t.Fatalf("switch fixture host %s: stage=%q err=%v", fixture.selector.layout.hostID, operation.Stage, err)
	}
	return operation
}

func rollbackCohortFixtureHost(t *testing.T, fixture cohortRollbackSyncHostFixture, receipt *CatalogCohortReceipt, at time.Time) {
	t.Helper()
	layout := fixture.selector.layout
	active := filepath.Join(layout.catalogRoot, receipt.ReleaseID, layout.hostID)
	backup := fixtureCatalogBackup(t, layout, active, store.CurrentSchemaVersion)
	operation, err := rollbackCatalogSelectorAt(layout, fixture.operationID,
		store.CurrentSchemaVersion, backup, nil)
	if err != nil || operation.Stage != catalogOperationRolledBack {
		t.Fatalf("rollback fixture host %s: stage=%q err=%v", layout.hostID, operation.Stage, err)
	}
	digest, err := catalogSelectorOperationDigest(layout, operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := AdvanceCatalogCohortReceipt(*receipt, "rollback-host", layout.hostID, digest, at)
	if err != nil {
		t.Fatal(err)
	}
	*receipt = updated
}

func syncCohortRollbackReceiptToAllHosts(t *testing.T, hosts map[string]cohortRollbackSyncHostFixture, receipt CatalogCohortReceipt) {
	t.Helper()
	for _, hostID := range catalogCohortSwitchOrder {
		fixture := hosts[hostID]
		if _, err := syncCatalogCohortReceiptAt(cohortFixtureReceiptRoot(fixture), receipt, func() error {
			return validateCohortRollbackFixtureLocalState(fixture, receipt)
		}); err != nil {
			t.Fatalf("synchronize %s receipt at %s: %v", hostID, receipt.Stage, err)
		}
		stored, err := readCatalogCohortReceipt(filepath.Join(cohortFixtureReceiptRoot(fixture), receipt.OperationID+".json"))
		if err != nil || stored.Stage != receipt.Stage || lastCohortTransitionDigest(stored.Transitions) != lastCohortTransitionDigest(receipt.Transitions) {
			t.Fatalf("%s receipt did not synchronize at %s: stored=%q err=%v", hostID, receipt.Stage, stored.Stage, err)
		}
	}
}

func validateCohortRollbackFixtureLocalState(fixture cohortRollbackSyncHostFixture, receipt CatalogCohortReceipt) error {
	status, err := inspectCatalogSelector(fixture.selector.layout)
	if err != nil {
		return err
	}
	configurationSHA256, err := hashCatalogConfiguration(fixture.selector.layout.configFile)
	if err != nil {
		return err
	}
	return validateCatalogCohortHistoryLocalSelectorState(fixture.selector.layout, receipt, status,
		store.CurrentSchemaVersion, configurationSHA256)
}

func cohortFixtureReceiptRoot(fixture cohortRollbackSyncHostFixture) string {
	return filepath.Join(fixture.selector.layout.serviceRoot, "staging", "catalog-cohorts")
}
