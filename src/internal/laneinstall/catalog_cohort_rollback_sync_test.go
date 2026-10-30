package laneinstall

import (
	"encoding/json"
	"errors"
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

func TestPRDCohortRollbackReconciliationSynchronizesLegacySelectedState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("a", 64)
	root := catalogTestTempDir(t)
	catalogRootsRoot := filepath.Join(root, "runner-releases")
	if err := os.Mkdir(catalogRootsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bundles := catalogFixtureBundles(t, "prd-rollback-sync-release")
	manifest, err := NewCatalogReleaseManifest("v2.0.0-prd-rollback-sync", bundles)
	if err != nil {
		t.Fatal(err)
	}
	hosts := make(map[string]cohortRollbackSyncHostFixture, len(catalogCohortSwitchOrder))
	receipt := validCatalogCohortFixture(now)
	receipt.Lane = config.ReleaseLanePRD
	receipt.ReleaseID = manifest.ReleaseID
	receipt.ReleaseSHA256 = ""
	receipt.FenceProofs = []CatalogCohortFenceProof{{
		Scope: "mac-project-publishers", Control: "fixture-publisher-pause", Owner: "fixture-owner",
		Paused: true, NoNewAccepted: true, EvidenceSHA256: proof, VerifiedAt: now.Format(time.RFC3339Nano),
	}}
	for _, hostID := range catalogCohortSwitchOrder {
		catalogRoot := filepath.Join(catalogRootsRoot, hostID)
		if err := os.Mkdir(catalogRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		identity, err := PublishCatalogRelease(catalogRoot, hostID, manifest, bundles[hostID])
		if err != nil {
			t.Fatal(err)
		}
		fixture, _ := newCohortRollbackSyncHostFixture(t, root, catalogRoot, hostID, manifest.ReleaseID)
		fixture.layout.lane = config.ReleaseLanePRD
		backup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
		hosts[hostID] = cohortRollbackSyncHostFixture{selector: fixture, backup: backup}
		index := cohortHostIndex(receipt, hostID)
		host := &receipt.Hosts[index]
		host.Target = identity
		if receipt.ReleaseSHA256 == "" {
			receipt.ReleaseSHA256 = identity.ReleaseSHA256
		}
		host.ServiceActiveAtPreflight = false
		host.PreviousTarget = fixture.legacyTarget
		host.PreviousBundleSHA256 = filepath.Base(fixture.legacyTarget)
		host.PreviousReadSchemaMax = store.CurrentSchemaVersion
		host.LegacyRollback = nil
		host.ConfigurationSHA256, err = hashCatalogConfiguration(fixture.layout.configFile)
		if err != nil {
			t.Fatal(err)
		}
		host.SchemaVersion = store.CurrentSchemaVersion
		host.WorkInventorySHA256 = (CatalogWorkInventory{}).SHA256()
		host.BackupDirectory = backup.Directory
		host.BackupManifestSHA256 = backup.ManifestSHA256
		host.PreflightAt = now.Format(time.RFC3339Nano)
	}
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("PRD receipt fixture invalid: %v", err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		host := receipt.Hosts[cohortHostIndex(receipt, hostID)]
		receipt, err = AdvanceCatalogCohortHostFence(receipt, CatalogCohortHostFence{
			HostID: hostID, ServiceWasActive: false, ServiceStopped: true,
			WorkInventorySHA256: host.WorkInventorySHA256, ConfigurationSHA256: host.ConfigurationSHA256,
			SelectorTarget: host.PreviousTarget, PreviousBundleSHA256: host.PreviousBundleSHA256,
			DatabaseSchema: host.SchemaVersion, ObservedAt: now.Format(time.RFC3339Nano),
		}, now)
		if err != nil {
			t.Fatalf("record PRD host fence %s: %v", hostID, err)
		}
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatalf("record PRD hold after host fences: %v", err)
	}
	for index, hostID := range []string{"linux-host", "sandbox-host"} {
		preflightAt := now.Add(time.Duration(index*2+1) * time.Second)
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", hostID, proof, preflightAt)
		if err != nil {
			t.Fatalf("record PRD preflight for %s: %v", hostID, err)
		}
		fixture := hosts[hostID]
		operation := switchPRDCohortFixtureHost(t, fixture, receipt, proof)
		fixture.operationID = operation.OperationID
		hosts[hostID] = fixture
		digest, digestErr := catalogSelectorOperationDigest(fixture.selector.layout, operation.OperationID)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", hostID, digest, preflightAt.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	switching := receipt
	blocked, err := AdvanceCatalogCohortReceipt(receipt, "block", "", proof, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{switching, blocked}, now.Add(6*time.Second))
	if err != nil || reconciled.Stage != CatalogCohortBlocked || !catalogCohortStageHoldsAdmissions(reconciled.Stage) {
		t.Fatalf("reconcile PRD receipt stage=%q err=%v", reconciled.Stage, err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		fixture := hosts[hostID]
		if _, err := syncCatalogCohortReceiptAt(cohortFixtureReceiptRoot(fixture), reconciled, func() error {
			return validatePRDCohortRollbackFixtureLocalState(fixture, reconciled)
		}); err != nil {
			t.Fatalf("synchronize PRD reconciliation to %s: %v", hostID, err)
		}
	}
	reconciled, err = AdvanceCatalogCohortReceipt(reconciled, "rollback-start", "", proof, now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	syncPRDCohortRollbackReceiptToAllHosts(t, hosts, reconciled)
	for _, hostID := range []string{"sandbox-host", "linux-host"} {
		fixture := hosts[hostID]
		operation := rollbackPRDCohortFixtureHost(t, fixture)
		digest, digestErr := catalogSelectorOperationDigest(fixture.selector.layout, operation.OperationID)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		reconciled, err = AdvanceCatalogCohortReceipt(reconciled, "rollback-host", hostID, digest, now.Add(8*time.Second))
		if err != nil {
			t.Fatalf("record PRD rollback %s: %v", hostID, err)
		}
		hosts[hostID] = fixture
		status, statusErr := inspectCatalogSelector(fixture.selector.layout)
		configurationSHA256, configErr := hashCatalogConfiguration(fixture.selector.layout.configFile)
		if statusErr != nil || configErr != nil {
			t.Fatalf("inspect completed PRD rollback %s: status=%v config=%v", hostID, statusErr, configErr)
		}
		if err := validatePRDCatalogRollbackRetryStateUsing(fixture.selector.layout, reconciled, operation, status,
			store.CurrentSchemaVersion, configurationSHA256, func(_ catalogSelectorLayout, candidate CatalogSelectorOperation) error {
				return validateCatalogOperationFields(candidate)
			}); err != nil {
			t.Fatalf("accept exact already-applied PRD rollback for safe retry %s: %v", hostID, err)
		}
		wrongSelector := status
		wrongSelector.Target = operation.NextTarget
		if err := validatePRDCatalogRollbackRetryStateUsing(fixture.selector.layout, reconciled, operation, wrongSelector,
			store.CurrentSchemaVersion, configurationSHA256, func(_ catalogSelectorLayout, candidate CatalogSelectorOperation) error {
				return validateCatalogOperationFields(candidate)
			}); !errors.Is(err, ErrCatalogRollbackUnsafe) {
			t.Fatalf("accept retry with a changed selector for %s: %v", hostID, err)
		}
		syncPRDCohortRollbackReceiptToAllHosts(t, hosts, reconciled)
	}
	reconciled, err = AdvanceCatalogCohortReceipt(reconciled, "rollback-complete", "", proof, now.Add(10*time.Second))
	if err != nil || reconciled.Stage != CatalogCohortRolledBack {
		t.Fatalf("complete PRD rollback stage=%q err=%v", reconciled.Stage, err)
	}
	syncPRDCohortRollbackReceiptToAllHosts(t, hosts, reconciled)
	if !catalogCohortReceiptSupersessionEligible(reconciled) {
		t.Fatal("fully rolled-back PRD receipt is not eligible for guarded supersession")
	}
	validateFixtureOperation := func(layout catalogSelectorLayout, operation CatalogSelectorOperation) error {
		if validateCatalogOperationFields(operation) != nil || operation.Lane != config.ReleaseLanePRD || operation.HostID != layout.hostID {
			return ErrCatalogOperationInvalid
		}
		return nil
	}
	for _, hostID := range catalogCohortSwitchOrder {
		fixture := hosts[hostID]
		index := cohortHostIndex(reconciled, hostID)
		if index < 0 {
			t.Fatalf("rolled-back receipt missing host %s", hostID)
		}
		host := reconciled.Hosts[index]
		status, statusErr := inspectCatalogSelector(fixture.selector.layout)
		if statusErr != nil {
			t.Fatalf("inspect rolled-back selector %s: %v", hostID, statusErr)
		}
		baseline := prdCatalogProof{
			schema: host.SchemaVersion, configurationSHA256: host.ConfigurationSHA256,
			selectorTarget: status.Target, bundleSHA256: catalogSelectorStatusBundleSHA256(status),
			work: CatalogWorkInventory{}, workSHA256: host.WorkInventorySHA256,
		}
		if status.Kind == "unselected" {
			baseline.legacyBundleSHA256 = host.PreviousBundleSHA256
			baseline.freshInstall = host.FreshInstall
		}
		if err := validatePRDCatalogReplacementBaselineUsing(fixture.selector.layout, reconciled, host,
			baseline, status, validateFixtureOperation); err != nil {
			t.Fatalf("accept synchronized rolled-back predecessor as replacement baseline on %s: %v", hostID, err)
		}
		// Exercise the supported receipt synchronization path with the stricter
		// replacement-baseline validation. The receipt is already synchronized;
		// this proves an exact retry remains safe before any successor switch.
		if _, err := syncCatalogCohortReceiptAt(cohortFixtureReceiptRoot(fixture), reconciled, func() error {
			if err := validatePRDCohortRollbackFixtureLocalState(fixture, reconciled); err != nil {
				return err
			}
			current, err := inspectCatalogSelector(fixture.selector.layout)
			if err != nil {
				return err
			}
			return validatePRDCatalogReplacementBaselineUsing(fixture.selector.layout, reconciled, host,
				baseline, current, validateFixtureOperation)
		}); err != nil {
			t.Fatalf("retry synchronization with rolled-back replacement baseline on %s: %v", hostID, err)
		}
		wrongSelector := status
		wrongSelector.Target = filepath.Join(fixture.selector.layout.catalogRoot, reconciled.ReleaseID, hostID)
		if err := validatePRDCatalogReplacementBaselineUsing(fixture.selector.layout, reconciled, host,
			baseline, wrongSelector, validateFixtureOperation); !errors.Is(err, ErrCatalogOperationInvalid) {
			t.Fatalf("accept changed selector for rolled-back predecessor on %s: %v", hostID, err)
		}
	}
	rollbackInProgress, err := AdvanceCatalogCohortReceipt(blocked, "rollback-start", "", proof, now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for index, hostID := range []string{"sandbox-host", "linux-host"} {
		fixture := hosts[hostID]
		digest, digestErr := catalogSelectorOperationDigest(fixture.selector.layout, fixture.operationID)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		rollbackInProgress, err = AdvanceCatalogCohortReceipt(rollbackInProgress, "rollback-host", hostID, digest,
			now.Add(time.Duration(index+8)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	rolledBackBranch, err := AdvanceCatalogCohortReceipt(rollbackInProgress, "rollback-complete", "", proof, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rolledBackHistory, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{rollbackInProgress, rolledBackBranch}, now.Add(11*time.Second))
	if err != nil || rolledBackHistory.Stage != CatalogCohortBlocked ||
		blockedCohortResumeStage(rolledBackHistory.Transitions) != CatalogCohortRolledBack ||
		!catalogCohortStageHoldsAdmissions(rolledBackHistory.Stage) {
		t.Fatalf("reconcile PRD rolled-back histories stage=%q resume=%q err=%v", rolledBackHistory.Stage,
			blockedCohortResumeStage(rolledBackHistory.Transitions), err)
	}
	if err := ValidateCatalogCohortReceipt(rolledBackHistory); err != nil {
		t.Fatalf("reconciled PRD rollback history is invalid: %v", err)
	}
}

func switchPRDCohortFixtureHost(t *testing.T, fixture cohortRollbackSyncHostFixture, receipt CatalogCohortReceipt,
	evidenceSHA256 string) CatalogSelectorOperation {
	t.Helper()
	layout := fixture.selector.layout
	target := filepath.Join(layout.catalogRoot, receipt.ReleaseID, layout.hostID)
	operationID, err := newCatalogOperationID()
	if err != nil {
		t.Fatal(err)
	}
	operation := CatalogSelectorOperation{
		FormatVersion: catalogOperationFormatVersion, OperationID: operationID,
		CohortOperationID: receipt.OperationID, CohortEvidenceSHA256: evidenceSHA256,
		HostID: layout.hostID, Lane: config.ReleaseLanePRD, Release: receipt.Hosts[cohortHostIndex(receipt, layout.hostID)].Target,
		PreviousTarget: fixture.selector.legacyTarget, NextTarget: target,
		ConfigurationSHA256:    receipt.Hosts[cohortHostIndex(receipt, layout.hostID)].ConfigurationSHA256,
		DatabaseSchemaAtSwitch: store.CurrentSchemaVersion, BackupDirectory: fixture.backup.Directory,
		BackupManifestSHA256: fixture.backup.ManifestSHA256, Stage: catalogOperationPrepared,
		CreatedUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		t.Fatal(err)
	}
	if err := switchCatalogSelector(layout, fixture.selector.legacyTarget, target); err != nil {
		t.Fatal(err)
	}
	operation.Stage = catalogOperationComplete
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		t.Fatal(err)
	}
	return operation
}

func rollbackPRDCohortFixtureHost(t *testing.T, fixture cohortRollbackSyncHostFixture) CatalogSelectorOperation {
	t.Helper()
	layout := fixture.selector.layout
	operation, err := readFixtureCatalogOperation(layout.operationRoot, fixture.operationID)
	if err != nil {
		t.Fatal(err)
	}
	active := operation.NextTarget
	backup := fixtureCatalogBackup(t, layout, active, store.CurrentSchemaVersion)
	operation.Stage = catalogOperationRollbackPrepare
	operation.RollbackBackupDirectory = backup.Directory
	operation.RollbackBackupManifestSHA256 = backup.ManifestSHA256
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		t.Fatal(err)
	}
	if err := switchCatalogSelector(layout, operation.NextTarget, operation.PreviousTarget); err != nil {
		t.Fatal(err)
	}
	operation.Stage = catalogOperationRolledBack
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		t.Fatal(err)
	}
	return operation
}

func readFixtureCatalogOperation(root, operationID string) (CatalogSelectorOperation, error) {
	data, err := os.ReadFile(filepath.Join(root, operationID+".json"))
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	var operation CatalogSelectorOperation
	if err := json.Unmarshal(data, &operation); err != nil || validateCatalogOperationFields(operation) != nil {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	return operation, nil
}

func validatePRDCohortRollbackFixtureLocalState(fixture cohortRollbackSyncHostFixture, receipt CatalogCohortReceipt) error {
	layout := fixture.selector.layout
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return err
	}
	configurationSHA256, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return err
	}
	var matching []CatalogSelectorOperation
	if fixture.operationID != "" {
		operation, err := readFixtureCatalogOperation(layout.operationRoot, fixture.operationID)
		if err != nil {
			return err
		}
		matching = append(matching, operation)
	}
	return validateCatalogCohortReconciledSelectorEvidenceUsing(layout, receipt, status,
		store.CurrentSchemaVersion, configurationSHA256, matching, func(layout catalogSelectorLayout, operation CatalogSelectorOperation) error {
			if validateCatalogOperationFields(operation) != nil || operation.Lane != config.ReleaseLanePRD || operation.HostID != layout.hostID {
				return ErrCatalogOperationInvalid
			}
			return nil
		})
}

func syncPRDCohortRollbackReceiptToAllHosts(t *testing.T, hosts map[string]cohortRollbackSyncHostFixture, receipt CatalogCohortReceipt) {
	t.Helper()
	for _, hostID := range catalogCohortSwitchOrder {
		fixture := hosts[hostID]
		if _, err := syncCatalogCohortReceiptAt(cohortFixtureReceiptRoot(fixture), receipt, func() error {
			return validatePRDCohortRollbackFixtureLocalState(fixture, receipt)
		}); err != nil {
			t.Fatalf("synchronize PRD %s receipt at %s: %v", hostID, receipt.Stage, err)
		}
		stored, err := readCatalogCohortReceipt(filepath.Join(cohortFixtureReceiptRoot(fixture), receipt.OperationID+".json"))
		if err != nil || stored.Stage != receipt.Stage || lastCohortTransitionDigest(stored.Transitions) != lastCohortTransitionDigest(receipt.Transitions) {
			t.Fatalf("PRD %s receipt did not synchronize at %s: stored=%q err=%v", hostID, receipt.Stage, stored.Stage, err)
		}
	}
}

func TestCatalogCohortVerificationReconciliationSynchronizesEveryHost(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("a", 64)
	root := catalogTestTempDir(t)
	catalogRootsRoot := filepath.Join(root, "runner-releases")
	if err := os.Mkdir(catalogRootsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bundles := catalogFixtureBundles(t, "verify-reconcile-release")
	manifest, err := NewCatalogReleaseManifest("v2.0.0-verify-reconcile", bundles)
	if err != nil {
		t.Fatal(err)
	}
	hosts := make(map[string]cohortRollbackSyncHostFixture, len(catalogCohortSwitchOrder))
	receipt := validCatalogCohortFixture(now)
	receipt.ReleaseID = manifest.ReleaseID
	receipt.ReleaseSHA256 = ""
	for _, hostID := range catalogCohortSwitchOrder {
		catalogRoot := filepath.Join(catalogRootsRoot, hostID)
		if err := os.Mkdir(catalogRoot, 0o700); err != nil {
			t.Fatalf("mkdir catalog root for %s: %v", hostID, err)
		}
		if _, err := PublishCatalogRelease(catalogRoot, hostID, manifest, bundles[hostID]); err != nil {
			t.Fatalf("publish fixture release for %s: %v", hostID, err)
		}
		fixture, identity := newCohortRollbackSyncHostFixture(t, root, catalogRoot, hostID, manifest.ReleaseID)
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
	for index, hostID := range catalogCohortSwitchOrder {
		at := now.Add(time.Duration(index*2+1) * time.Second)
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", hostID, proof, at)
		if err != nil {
			t.Fatalf("preflight %s: %v", hostID, err)
		}
		operation := switchCohortFixtureHost(t, hosts[hostID], receipt, proof)
		fixture := hosts[hostID]
		fixture.operationID = operation.OperationID
		hosts[hostID] = fixture
		digest, digestErr := catalogSelectorOperationDigest(fixture.selector.layout, operation.OperationID)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", hostID, digest, at.Add(time.Second))
		if err != nil {
			t.Fatalf("switch %s: %v", hostID, err)
		}
	}
	verifyStart, err := AdvanceCatalogCohortReceipt(receipt, "verify-start", "", proof, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	branches := make([]CatalogCohortReceipt, 0, len(catalogCohortSwitchOrder))
	for index, hostID := range catalogCohortSwitchOrder {
		evidence := strings.Repeat(string(rune('a'+index)), 64)
		branch, branchErr := AdvanceCatalogCohortReceipt(verifyStart, "verify", hostID, evidence, now.Add(time.Duration(11+index)*time.Second))
		if branchErr != nil {
			t.Fatalf("verify %s: %v", hostID, branchErr)
		}
		branches = append(branches, branch)
		if _, writeErr := writeCatalogCohortReceiptAt(cohortFixtureReceiptRoot(hosts[hostID]), branch); writeErr != nil {
			t.Fatalf("write divergent %s verification receipt: %v", hostID, writeErr)
		}
	}
	reconciled, err := ReconcileCatalogCohortReceiptHistories(branches, now.Add(20*time.Second))
	if err != nil || reconciled.Stage != CatalogCohortBlocked || blockedCohortResumeStage(reconciled.Transitions) != CatalogCohortVerifying {
		t.Fatalf("verification reconciliation stage=%q resume=%q err=%v", reconciled.Stage,
			blockedCohortResumeStage(reconciled.Transitions), err)
	}
	for _, branch := range branches {
		if !catalogCohortReceiptHasHistoricalBranch(reconciled, branch.Transitions) {
			t.Fatal("reconciliation discarded a host verification history")
		}
	}
	syncCohortRollbackReceiptToAllHosts(t, hosts, reconciled)

	resumed, err := AdvanceCatalogCohortReceipt(reconciled, "resume", "", proof, now.Add(21*time.Second))
	if err != nil || resumed.Stage != CatalogCohortVerifying {
		t.Fatalf("verification resume stage=%q err=%v", resumed.Stage, err)
	}
	syncCohortRollbackReceiptToAllHosts(t, hosts, resumed)
	completed, err := AdvanceCatalogCohortReceipt(resumed, "complete", "", proof, now.Add(22*time.Second))
	if err != nil || completed.Stage != CatalogCohortComplete {
		t.Fatalf("verification completion stage=%q err=%v", completed.Stage, err)
	}
	syncCohortRollbackReceiptToAllHosts(t, hosts, completed)
	for _, hostID := range catalogCohortSwitchOrder {
		stored, readErr := readCatalogCohortReceipt(filepath.Join(cohortFixtureReceiptRoot(hosts[hostID]), completed.OperationID+".json"))
		if readErr != nil || lastCohortTransitionDigest(stored.Transitions) != lastCohortTransitionDigest(completed.Transitions) || stored.Stage != CatalogCohortComplete {
			t.Fatalf("%s did not receive the completed reconciled history: stage=%q err=%v", hostID, stored.Stage, readErr)
		}
	}

	conflicting := append([]CatalogCohortReceipt(nil), branches...)
	conflictingCopy, err := AdvanceCatalogCohortReceipt(verifyStart, "verify", "linux-host", strings.Repeat("f", 64), now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	conflicting = append(conflicting, conflictingCopy)
	if _, err := ReconcileCatalogCohortReceiptHistories(conflicting, now.Add(31*time.Second)); err == nil {
		t.Fatal("conflicting verification evidence for one host was reconciled")
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
