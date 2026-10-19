package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const (
	prdCatalogProofMaxAge      = 5 * time.Minute
	prdCatalogSelectorHeadroom = 64 * 1024 * 1024
)

// CatalogPRDMaintenance is the only selector mutation path for the existing
// unsuffixed PRD service. It holds the catalog lock before the established PRD
// lifecycle lock and never restores or edits the production database.
type CatalogPRDMaintenance struct {
	plan      PRDUpgradePlan
	service   *PRDServiceController
	layout    catalogSelectorLayout
	catalogFD int
	laneFD    int
	proof     prdCatalogProof
	backup    *PRDBackupReceipt
	closed    bool
}

type prdCatalogProof struct {
	observedAt          time.Time
	schema              int
	configurationSHA256 string
	selectorTarget      string
	bundleSHA256        string
	legacyBundleSHA256  string
	readSchemaMax       int
	work                PRDWorkInventory
	workSHA256          string
	availableBytes      uint64
	requiredBytes       uint64
}

// PRDCatalogSelectorHeadroomBytes is the bounded free-space reserve required
// after the consistent backup already exists and before an atomic PRD switch.
func PRDCatalogSelectorHeadroomBytes() uint64 { return prdCatalogSelectorHeadroom }

// ReadPRDCatalogSelectorCapacity reports actual post-backup free bytes on the
// existing PRD filesystem and the fixed atomic-switch reserve.
func ReadPRDCatalogSelectorCapacity(plan PRDUpgradePlan) (availableBytes, requiredBytes uint64, err error) {
	if validatePRDPlan(plan) != nil {
		return 0, 0, ErrInvalidPRDUpgradePlan
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(plan.Namespace.ServiceRoot, &filesystem); err != nil {
		return 0, 0, err
	}
	return uint64(filesystem.Bavail) * uint64(filesystem.Bsize), prdCatalogSelectorHeadroom, nil
}

// BeginCatalogPRDMaintenance requires a locally recorded whole-cohort hold,
// stops only unsuffixed PRD when necessary, then captures a fresh empty-work
// proof under the existing PRD lifecycle lock.
func BeginCatalogPRDMaintenance(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController, cohortOperationID string) (*CatalogPRDMaintenance, error) {
	if validatePRDPlan(plan) != nil || service == nil || !validCatalogOperationID(cohortOperationID) {
		return nil, ErrInvalidPRDUpgradePlan
	}
	catalogPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	if held, operationID, err := CatalogCohortAdmissionHeld(catalogPlan); err != nil || !held || operationID != cohortOperationID {
		return nil, ErrCatalogOperationInvalid
	}
	root, err := DefaultCatalogRootForPlan(catalogPlan)
	if err != nil {
		return nil, err
	}
	layout, err := catalogSelectorLayoutForPlan(catalogPlan, root)
	if err != nil {
		return nil, err
	}
	catalogFD, err := acquireCatalogRootPublicationLock(root)
	if err != nil {
		return nil, err
	}
	maintenance := &CatalogPRDMaintenance{plan: plan, service: service, layout: layout, catalogFD: catalogFD, laneFD: -1}
	fail := func(err error) (*CatalogPRDMaintenance, error) {
		_ = maintenance.Close()
		return nil, err
	}
	// Serialize the complete stop-and-switch transaction before inspecting or
	// stopping the selected service. Acquiring this lock after Stop can leave
	// PRD offline when another maintenance operation already owns it.
	maintenance.laneFD, err = acquirePRDUpgradeLock(plan)
	if err != nil {
		return fail(err)
	}
	preStopProof, err := readPRDCatalogProof(ctx, plan, layout)
	if err != nil {
		return fail(err)
	}
	if !preStopProof.work.Empty() {
		return fail(fmt.Errorf("%w: unsuffixed PRD work must be settled before stopping services: %+v", ErrPRDUpgradeGuard, preStopProof.work))
	}
	if held, operationID, err := CatalogCohortAdmissionHeld(catalogPlan); err != nil || !held || operationID != cohortOperationID {
		return fail(ErrCatalogOperationInvalid)
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return fail(err)
	}
	if active {
		if err := service.Stop(ctx, plan); err != nil {
			return fail(fmt.Errorf("stop only the selected unsuffixed PRD services: %w", err))
		}
	}
	active, err = service.Status(ctx, plan)
	if err != nil || active {
		return fail(errors.Join(ErrPRDUpgradeGuard, err))
	}
	if err := validatePRDConfig(plan); err != nil {
		return fail(err)
	}
	maintenance.proof, err = readPRDCatalogProof(ctx, plan, layout)
	if err != nil {
		return fail(err)
	}
	if !maintenance.proof.work.Empty() || maintenance.proof.schema != preStopProof.schema ||
		maintenance.proof.configurationSHA256 != preStopProof.configurationSHA256 || maintenance.proof.selectorTarget != preStopProof.selectorTarget ||
		maintenance.proof.bundleSHA256 != preStopProof.bundleSHA256 || maintenance.proof.legacyBundleSHA256 != preStopProof.legacyBundleSHA256 {
		return fail(ErrPRDUpgradeGuard)
	}
	return maintenance, nil
}

// Backup captures the exact host-local PRD binaries, launch/bridge artifacts,
// configuration, secrets, and a consistent online SQLite backup. It is bound
// to the target release bundle digest but does not change any live state.
func (m *CatalogPRDMaintenance) Backup(ctx context.Context, releaseID string) (PRDBackupReceipt, error) {
	if m == nil || m.closed || !validCatalogReleaseID(releaseID) || !prdCatalogProofFresh(m.proof) {
		return PRDBackupReceipt{}, ErrPRDUpgradeGuard
	}
	target, err := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, releaseID)
	if err != nil {
		return PRDBackupReceipt{}, err
	}
	if err := m.recheck(ctx); err != nil {
		return PRDBackupReceipt{}, err
	}
	receipt, err := CreatePRDCatalogBackup(ctx, m.plan, target.Identity.BundleSHA256)
	if err != nil {
		return PRDBackupReceipt{}, err
	}
	if receipt.Manifest.SchemaVersion != m.proof.schema || receipt.Manifest.DatabasePath != m.plan.Namespace.Database {
		return PRDBackupReceipt{}, ErrPRDUpgradeGuard
	}
	m.backup = &receipt
	return receipt, nil
}

// UseVerifiedBackup attaches the exact PRD backup bound in the four-host
// receipt. It revalidates the manifest, configuration, schema, selector and
// empty-work proof before allowing a selector operation.
func (m *CatalogPRDMaintenance) UseVerifiedBackup(ctx context.Context, directory, expectedManifestSHA256 string) error {
	if m == nil || m.closed || !prdCatalogProofFresh(m.proof) || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		!bundleDigestPattern.MatchString(expectedManifestSHA256) {
		return ErrSimpleBackupRequired
	}
	if err := m.recheck(ctx); err != nil {
		return err
	}
	manifest, err := loadPRDBackupManifest(m.plan, directory)
	if err != nil || manifest.SchemaVersion != m.proof.schema || manifest.DatabasePath != m.plan.Namespace.Database ||
		backupManifestSHA256(directory) != expectedManifestSHA256 || manifest.BundleSHA256 == "" {
		return ErrSimpleBackupRequired
	}
	if err := verifyPRDBackupTargets(manifest, true); err != nil {
		return err
	}
	m.backup = &PRDBackupReceipt{Directory: directory, Manifest: manifest}
	return nil
}

// Select switches the PRD selector only after the exact host preflight and
// backup in the whole-cohort receipt agree with fresh local observations. On
// first adoption, catalog-aware service artifacts are installed after the
// durable prepared operation is written and before the selector is switched.
func (m *CatalogPRDMaintenance) Select(ctx context.Context, releaseID, expectedTarget, cohortOperationID, switchEvidenceSHA256 string) (CatalogSelectorOperation, error) {
	if m == nil || m.closed || m.backup == nil || !prdCatalogProofFresh(m.proof) || !validCatalogOperationID(cohortOperationID) ||
		!bundleDigestPattern.MatchString(switchEvidenceSHA256) {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	if err := m.recheck(ctx); err != nil {
		return CatalogSelectorOperation{}, err
	}
	cohortPlan := Plan{HostID: m.plan.HostID, Lane: config.ReleaseLanePRD, Namespace: m.plan.Namespace}
	receipt, err := ReadCatalogCohortReceipt(cohortPlan, cohortOperationID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	current, err := inspectCatalogSelector(m.layout)
	if err != nil || current.Target != expectedTarget || expectedTarget != m.proof.selectorTarget {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	target, err := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, releaseID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	if m.backup.Manifest.BundleSHA256 != target.Identity.BundleSHA256 || m.backup.Manifest.SchemaVersion != m.proof.schema {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	if err := validateCatalogCohortHostMaySwitch(receipt, m.layout, current.Target, target.Identity, m.proof.schema,
		m.backup.Directory, backupManifestSHA256(m.backup.Directory), time.Now().UTC()); err != nil {
		return CatalogSelectorOperation{}, err
	}
	index := cohortHostIndex(receipt, m.plan.HostID)
	previousMaxMatches := index >= 0 && receipt.Hosts[index].PreviousReadSchemaMax == m.proof.readSchemaMax
	if index >= 0 && m.proof.selectorTarget == "" {
		previousMaxMatches = receipt.Hosts[index].PreviousReadSchemaMax >= m.proof.schema
	}
	if index < 0 || receipt.Hosts[index].PreviousBundleSHA256 != m.proof.previousBundleSHA256() || !previousMaxMatches || receipt.Hosts[index].WorkInventorySHA256 != m.proof.workSHA256 ||
		!receipt.Hosts[index].WorkInventoryEmpty || receipt.Hosts[index].RequiredBytes != m.proof.requiredBytes ||
		m.proof.availableBytes < receipt.Hosts[index].RequiredBytes {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	backupRef := catalogBackupReference{Directory: m.backup.Directory, ManifestSHA256: backupManifestSHA256(m.backup.Directory),
		CohortOperationID: cohortOperationID, CohortEvidenceSHA256: switchEvidenceSHA256}
	operation, err := selectCatalogReleaseLocked(m.layout, releaseID, expectedTarget, m.proof.schema, backupRef, func(stage string) error {
		if stage != catalogSelectorAfterPrepared {
			return nil
		}
		return installPRDCatalogServiceArtifacts(ctx, m.plan, m.service, m.backup.Manifest)
	})
	if err != nil {
		return operation, err
	}
	if err := recordCatalogCohortSwitch(cohortPlan, operation, false); err != nil {
		return operation, err
	}
	return operation, nil
}

// Reconcile applies the durable prepared operation to the actual selector.
// It repairs only the launch artifacts named by the immutable PRD backup and
// never starts the service or touches database contents.
func (m *CatalogPRDMaintenance) Reconcile(ctx context.Context, operationID string) (CatalogSelectorOperation, error) {
	if m == nil || m.closed || !validCatalogOperationID(operationID) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	if err := m.recheck(ctx); err != nil {
		return CatalogSelectorOperation{}, err
	}
	operation, err := readCatalogSelectorOperation(m.layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(m.layout, operation) != nil || operation.CohortOperationID == "" {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	manifest, err := loadPRDBackupManifest(m.plan, operation.BackupDirectory)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	actual, err := inspectCatalogSelector(m.layout)
	if err != nil || (actual.Target != operation.PreviousTarget && actual.Target != operation.NextTarget) {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	if operation.Stage == catalogOperationRollbackPrepare && operation.PreviousTarget == "" {
		if err := restorePRDLegacyServiceArtifacts(ctx, m.plan, m.service, manifest); err != nil {
			return operation, err
		}
	} else if operation.Stage == catalogOperationPrepared || operation.Stage == catalogOperationSwitched || operation.Stage == catalogOperationComplete {
		if err := installPRDCatalogServiceArtifacts(ctx, m.plan, m.service, manifest); err != nil {
			return operation, err
		}
	}
	completed, err := reconcileCatalogSelectorLocked(m.layout, operationID)
	if err != nil {
		return completed, err
	}
	if completed.Stage == catalogOperationRolledBack {
		err = recordCatalogCohortRollback(Plan{HostID: m.plan.HostID, Lane: config.ReleaseLanePRD, Namespace: m.plan.Namespace}, completed)
	} else {
		err = recordCatalogCohortSwitch(Plan{HostID: m.plan.HostID, Lane: config.ReleaseLanePRD, Namespace: m.plan.Namespace}, completed, true)
	}
	return completed, err
}

// Rollback moves only the selector and, for first adoption, restores the
// backed-up legacy launch artifacts. The current database is preserved; a
// first-adoption rollback additionally requires the cohort's measured legacy
// schema compatibility claim to cover the current schema.
func (m *CatalogPRDMaintenance) Rollback(ctx context.Context, operationID string) (CatalogSelectorOperation, error) {
	if m == nil || m.closed || !validCatalogOperationID(operationID) {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	if err := m.recheck(ctx); err != nil {
		return CatalogSelectorOperation{}, err
	}
	operation, err := readCatalogSelectorOperation(m.layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(m.layout, operation) != nil || operation.CohortOperationID == "" || operation.Stage != catalogOperationComplete {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	cohortPlan := Plan{HostID: m.plan.HostID, Lane: config.ReleaseLanePRD, Namespace: m.plan.Namespace}
	receipt, err := ReadCatalogCohortReceipt(cohortPlan, operation.CohortOperationID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	current, err := inspectCatalogSelector(m.layout)
	if err != nil || validateCatalogCohortHostMayRollback(receipt, m.layout, operation, current.Target) != nil {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	if current.Target != operation.NextTarget {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	rollbackBackup, err := CreatePRDCatalogBackup(ctx, m.plan, operation.Release.BundleSHA256)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	backupRef := catalogBackupReference{Directory: rollbackBackup.Directory, ManifestSHA256: backupManifestSHA256(rollbackBackup.Directory),
		CohortOperationID: operation.CohortOperationID}
	if operation.PreviousTarget != "" {
		rolledBack, err := rollbackCatalogSelectorLocked(m.layout, operationID, m.proof.schema, backupRef, nil)
		if err != nil {
			return rolledBack, err
		}
		if err := recordCatalogCohortRollback(cohortPlan, rolledBack); err != nil {
			return rolledBack, err
		}
		return rolledBack, nil
	}
	index := cohortHostIndex(receipt, m.plan.HostID)
	legacy, err := readPRDLegacyInventory(m.plan)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	legacyDigest, err := PRDLegacyBundleDigest(legacy)
	if err != nil || index < 0 || receipt.Hosts[index].PreviousBundleSHA256 != legacyDigest ||
		receipt.Hosts[index].PreviousReadSchemaMax < m.proof.schema {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	manifest, err := loadPRDBackupManifest(m.plan, operation.BackupDirectory)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	rolledBack, err := rollbackPRDCatalogSelectorToLegacy(m.layout, operation, m.proof.schema, backupRef, func() error {
		return restorePRDLegacyServiceArtifacts(ctx, m.plan, m.service, manifest)
	})
	if err != nil {
		return rolledBack, err
	}
	if err := recordCatalogCohortRollback(cohortPlan, rolledBack); err != nil {
		return rolledBack, err
	}
	return rolledBack, nil
}

func (m *CatalogPRDMaintenance) recheck(ctx context.Context) error {
	if m == nil || m.closed || !prdCatalogProofFresh(m.proof) {
		return ErrPRDUpgradeGuard
	}
	active, err := m.service.Status(ctx, m.plan)
	if err != nil || active {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	proof, err := readPRDCatalogProof(ctx, m.plan, m.layout)
	if err != nil {
		return err
	}
	if !proof.work.Empty() || proof.schema != m.proof.schema || proof.configurationSHA256 != m.proof.configurationSHA256 ||
		proof.selectorTarget != m.proof.selectorTarget || proof.bundleSHA256 != m.proof.bundleSHA256 || proof.legacyBundleSHA256 != m.proof.legacyBundleSHA256 {
		return ErrPRDUpgradeGuard
	}
	m.proof = proof
	return nil
}

func (m *CatalogPRDMaintenance) Close() error {
	if m == nil || m.closed {
		return nil
	}
	m.closed = true
	if m.laneFD >= 0 {
		releasePRDUpgradeLock(m.laneFD)
		m.laneFD = -1
	}
	releaseCatalogRootPublicationLock(m.catalogFD)
	m.catalogFD = -1
	return nil
}

func readPRDCatalogProof(ctx context.Context, plan PRDUpgradePlan, layout catalogSelectorLayout) (prdCatalogProof, error) {
	if validatePRDPlan(plan) != nil || validateCatalogSelectorLayout(layout) != nil || layout.lane != config.ReleaseLanePRD ||
		layout.hostID != plan.HostID || layout.databasePath != plan.Namespace.Database {
		return prdCatalogProof{}, ErrPRDUpgradeGuard
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return prdCatalogProof{}, err
	}
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		_ = database.Close()
		return prdCatalogProof{}, err
	}
	work, inventoryErr := readPRDWorkInventory(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil || schema <= 0 {
		return prdCatalogProof{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	configurationSHA256, err := hashPRDRegularFile(plan.Namespace.ConfigFile)
	if err != nil {
		return prdCatalogProof{}, err
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return prdCatalogProof{}, err
	}
	proof := prdCatalogProof{observedAt: time.Now().UTC(), schema: schema, configurationSHA256: configurationSHA256,
		selectorTarget: status.Target, work: work}
	workBytes, err := json.Marshal(work)
	if err != nil {
		return prdCatalogProof{}, err
	}
	workDigest := sha256.Sum256(workBytes)
	proof.workSHA256 = hex.EncodeToString(workDigest[:])
	if status.Kind == "catalog" && status.Release != nil {
		if !catalogDatabaseSupportsSchema(resolvedCatalogSelection{kind: "catalog", release: status.Release}, schema) {
			return prdCatalogProof{}, ErrCatalogRollbackUnsafe
		}
		proof.bundleSHA256 = status.Bundle.BundleSHA256
		proof.readSchemaMax = status.Release.Database.ReadSchemaMax
	} else if status.Kind == "unselected" {
		preflight, err := readPRDLegacyInventory(plan)
		if err != nil {
			return prdCatalogProof{}, err
		}
		proof.legacyBundleSHA256, err = PRDLegacyBundleDigest(preflight)
		if err != nil {
			return prdCatalogProof{}, err
		}
		proof.readSchemaMax = 0 // Set only from tested cohort evidence.
	} else {
		return prdCatalogProof{}, ErrInvalidCatalogRelease
	}
	proof.availableBytes, proof.requiredBytes, err = measurePRDBackupCapacity(plan, plan.Namespace.Database)
	if err != nil {
		return prdCatalogProof{}, err
	}
	// The full database/config/binary snapshot budget is checked by
	// CreatePRDCatalogBackup before writing it. The cohort receipt records the
	// post-backup free space, so selector activation reserves only its bounded
	// atomic-file and journal headroom here.
	proof.requiredBytes = prdCatalogSelectorHeadroom
	return proof, nil
}

func readPRDLegacyInventory(plan PRDUpgradePlan) (PRDPreflight, error) {
	entries, err := os.ReadDir(plan.Namespace.Binaries)
	if err != nil {
		return PRDPreflight{}, err
	}
	preflight := PRDPreflight{HostID: plan.HostID, BinarySHA256: make(map[string]string)}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return PRDPreflight{}, ErrPRDUpgradeGuard
		}
		name := entry.Name()
		digest, err := hashPRDRegularFile(filepath.Join(plan.Namespace.Binaries, name))
		if err != nil {
			return PRDPreflight{}, err
		}
		preflight.BinarySHA256[name] = digest
	}
	for _, required := range simpleHostComponents[plan.HostID] {
		if _, ok := preflight.BinarySHA256[required]; !ok {
			preflight.MissingComponents = append(preflight.MissingComponents, required)
		}
	}
	return preflight, nil
}

func (p prdCatalogProof) previousBundleSHA256() string {
	if p.selectorTarget == "" {
		return p.legacyBundleSHA256
	}
	return p.bundleSHA256
}

func prdCatalogProofFresh(proof prdCatalogProof) bool {
	return !proof.observedAt.IsZero() && time.Since(proof.observedAt) >= 0 && time.Since(proof.observedAt) <= prdCatalogProofMaxAge &&
		proof.schema > 0 && bundleDigestPattern.MatchString(proof.configurationSHA256) && bundleDigestPattern.MatchString(proof.workSHA256) &&
		(proof.bundleSHA256 == "" || bundleDigestPattern.MatchString(proof.bundleSHA256)) &&
		(proof.legacyBundleSHA256 == "" || bundleDigestPattern.MatchString(proof.legacyBundleSHA256))
}

func installPRDCatalogServiceArtifacts(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController, backup PRDBackupManifest) error {
	if validatePRDPlan(plan) != nil || service == nil || backup.HostID != plan.HostID {
		return ErrPRDUpgradeGuard
	}
	artifacts, err := BuildPRDCatalogServiceArtifacts(plan.HostID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if verifyPRDCatalogArtifact(artifact) == nil {
			continue
		}
		original, ok := findPRDBackupFile(backup, artifact.InstallPath)
		if !ok {
			return ErrPRDUpgradeGuard
		}
		if err := replacePRDCatalogArtifact(ctx, service, artifact, original.SHA256, original.Mode); err != nil {
			return err
		}
	}
	if plan.Namespace.Host == config.HostKindLinux {
		return service.base.reloadSystemd(ctx)
	}
	return nil
}

func restorePRDLegacyServiceArtifacts(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController, backup PRDBackupManifest) error {
	if validatePRDPlan(plan) != nil || service == nil || backup.HostID != plan.HostID {
		return ErrPRDUpgradeGuard
	}
	artifacts, err := BuildPRDCatalogServiceArtifacts(plan.HostID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		file, ok := findPRDBackupFile(backup, artifact.InstallPath)
		if !ok {
			return ErrPRDUpgradeGuard
		}
		contents, err := os.ReadFile(filepath.Join(filepath.Dir(backup.DatabaseBackupPath), file.BackupPath))
		if err != nil {
			return err
		}
		legacy := artifact
		legacy.Mode = file.Mode
		legacy.Contents = string(contents)
		if verifyPRDArtifactContents(legacy) == nil {
			continue
		}
		if err := replacePRDCatalogArtifact(ctx, service, legacy, sha256HexString([]byte(artifact.Contents)), artifact.Mode); err != nil {
			return err
		}
	}
	if plan.Namespace.Host == config.HostKindLinux {
		return service.base.reloadSystemd(ctx)
	}
	return nil
}

func findPRDBackupFile(backup PRDBackupManifest, target string) (PRDBackupFile, bool) {
	for _, file := range backup.Files {
		if file.TargetPath == target {
			return file, true
		}
	}
	return PRDBackupFile{}, false
}

func verifyPRDCatalogArtifact(artifact Artifact) error {
	return verifyPRDArtifactContents(artifact)
}

func verifyPRDArtifactContents(artifact Artifact) error {
	info, err := os.Lstat(artifact.InstallPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != os.FileMode(artifact.Mode) {
		return ErrSimpleArtifactConflict
	}
	if artifact.Owner == "root" {
		var stat unix.Stat_t
		if unix.Lstat(artifact.InstallPath, &stat) != nil || stat.Uid != 0 || (artifact.Group != "" && stat.Gid != 0) {
			return ErrSimpleArtifactConflict
		}
	} else if artifact.Owner != "" {
		current, err := user.Current()
		group, groupErr := user.LookupGroupId(strconv.Itoa(os.Getgid()))
		var stat unix.Stat_t
		if err != nil || groupErr != nil || artifact.Owner != current.Username || artifact.Group != group.Name ||
			unix.Lstat(artifact.InstallPath, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) {
			return ErrSimpleArtifactConflict
		}
	} else if statOwner(artifact.InstallPath) != uint32(os.Getuid()) {
		return ErrSimpleArtifactConflict
	}
	contents, err := os.ReadFile(artifact.InstallPath)
	if err != nil || !bytes.Equal(contents, []byte(artifact.Contents)) {
		return ErrSimpleArtifactConflict
	}
	return nil
}

func statOwner(path string) uint32 {
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil {
		return ^uint32(0)
	}
	return stat.Uid
}

func replacePRDCatalogArtifact(ctx context.Context, service *PRDServiceController, artifact Artifact, expectedOldSHA string, expectedOldMode uint32) error {
	if service == nil || !filepath.IsAbs(artifact.InstallPath) || filepath.Clean(artifact.InstallPath) != artifact.InstallPath ||
		!bundleDigestPattern.MatchString(expectedOldSHA) || expectedOldMode == 0 || artifact.Mode == 0 {
		return fmt.Errorf("%w: invalid replacement input", ErrSimpleArtifactConflict)
	}
	if err := verifyPRDArtifactContents(artifact); err == nil {
		return nil
	}
	if artifact.Owner == "root" {
		if artifact.Kind != ArtifactSystemdUnit || artifact.Mode != 0o644 || artifact.Group != "root" ||
			filepath.Dir(artifact.InstallPath) != "/etc/systemd/system" || filepath.Base(artifact.InstallPath) != "runnerd.service" {
			return fmt.Errorf("%w: unsupported root-owned service artifact", ErrSimpleArtifactConflict)
		}
		staged, err := os.CreateTemp("", "runner-prd-catalog-unit-*")
		if err != nil {
			return err
		}
		stagedPath := staged.Name()
		defer os.Remove(stagedPath)
		if err := staged.Chmod(0o600); err != nil {
			_ = staged.Close()
			return err
		}
		if _, err := staged.WriteString(artifact.Contents); err != nil {
			_ = staged.Close()
			return err
		}
		if err := staged.Sync(); err != nil {
			_ = staged.Close()
			return err
		}
		if err := staged.Close(); err != nil {
			return err
		}
		newSHA := sha256HexString([]byte(artifact.Contents))
		result, err := service.base.runner.Run(ctx, "/usr/bin/sudo", "-n", "/bin/sh", "-c", atomicPRDSystemdReplaceScript, "runner-lane", artifact.InstallPath, stagedPath, expectedOldSHA, newSHA)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("replace exact catalog PRD systemd unit failed (exit %d)", result.ExitCode)
		}
		return verifyPRDArtifactContents(artifact)
	}
	if artifact.Owner != "" {
		current, err := user.Current()
		group, groupErr := user.LookupGroupId(strconv.Itoa(os.Getgid()))
		if err != nil || groupErr != nil || artifact.Owner != current.Username || artifact.Group != group.Name || artifact.Kind != ArtifactSSHWrapper {
			return fmt.Errorf("%w: unsupported current-account artifact identity", ErrSimpleArtifactConflict)
		}
	}
	var target unix.Stat_t
	if unix.Lstat(artifact.InstallPath, &target) != nil || target.Mode&unix.S_IFMT != unix.S_IFREG || target.Uid != uint32(os.Getuid()) ||
		!catalogArtifactGroupMatches(artifact.Group, target.Gid, uint32(os.Getgid())) || uint32(target.Mode&0o777) != expectedOldMode || !catalogFileHasSingleLinkFromStat(target) {
		return fmt.Errorf("%w: existing target identity, mode, type, or link count differs", ErrSimpleArtifactConflict)
	}
	oldSHA, err := fileSHA256(artifact.InstallPath)
	if err != nil || oldSHA != expectedOldSHA {
		return fmt.Errorf("%w: existing target digest differs", ErrSimpleArtifactConflict)
	}
	parent := filepath.Dir(artifact.InstallPath)
	var parentStat unix.Stat_t
	if err := unix.Lstat(parent, &parentStat); err != nil {
		return fmt.Errorf("%w: replacement directory cannot be inspected", ErrSimpleArtifactConflict)
	}
	if parentStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%w: replacement parent is not a directory", ErrSimpleArtifactConflict)
	}
	if parentStat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%w: replacement directory has an unexpected owner", ErrSimpleArtifactConflict)
	}
	if !catalogArtifactGroupMatches(artifact.Group, parentStat.Gid, uint32(os.Getgid())) {
		return fmt.Errorf("%w: replacement directory has an unexpected group", ErrSimpleArtifactConflict)
	}
	if parentStat.Mode&0o022 != 0 {
		return fmt.Errorf("%w: replacement directory is group- or world-writable (mode %04o)", ErrSimpleArtifactConflict, parentStat.Mode&0o777)
	}
	temporary, err := os.CreateTemp(parent, ".runner-prd-catalog-artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(os.FileMode(artifact.Mode)); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(artifact.Contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	currentSHA, err := fileSHA256(artifact.InstallPath)
	if err != nil || currentSHA != expectedOldSHA {
		return fmt.Errorf("%w: target changed during replacement staging", ErrSimpleArtifactConflict)
	}
	if err := os.Rename(temporaryPath, artifact.InstallPath); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	if err := verifyPRDArtifactContents(artifact); err != nil {
		return fmt.Errorf("verify installed catalog artifact: %w", err)
	}
	return nil
}

func catalogFileHasSingleLinkFromStat(info unix.Stat_t) bool { return info.Nlink == 1 }

func catalogArtifactGroupMatches(expectedGroup string, actualGroup, currentGroup uint32) bool {
	return expectedGroup == "" || actualGroup == currentGroup
}

func sha256HexString(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

const atomicPRDSystemdReplaceScript = `set -eu
target=$1
source=$2
old_sha=$3
new_sha=$4
directory=${target%/*}
[ -f "$target" ] && [ ! -L "$target" ] && [ "$(stat -c '%U:%a' "$target")" = root:644 ] || exit 74
current=$(sha256sum -- "$target" | cut -d ' ' -f 1)
if [ "$current" != "$old_sha" ] && [ "$current" != "$new_sha" ]; then
	printf 'PRD systemd artifact changed since its verified backup\n' >&2
	exit 73
fi
temporary=$(mktemp "$directory/.runner-prd-catalog-unit.XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
install -o root -g root -m 0644 -- "$source" "$temporary"
[ "$(sha256sum -- "$temporary" | cut -d ' ' -f 1)" = "$new_sha" ]
if [ "$current" != "$new_sha" ]; then
	mv -f -- "$temporary" "$target"
	chmod 0644 -- "$target"
	chown root:root -- "$target"
	sync -f "$directory"
fi
`

func rollbackPRDCatalogSelectorToLegacy(layout catalogSelectorLayout, operation CatalogSelectorOperation, schema int, backup catalogBackupReference, restore func() error) (CatalogSelectorOperation, error) {
	if validateCatalogTransitionLayout(layout) != nil || layout.lane != config.ReleaseLanePRD || operation.PreviousTarget != "" ||
		operation.Stage != catalogOperationComplete || operation.DatabaseSchemaAtSwitch <= 0 || schema <= 0 || !validCatalogOperationID(operation.OperationID) {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Target != operation.NextTarget {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	configurationSHA256, err := hashCatalogConfiguration(layout.configFile)
	if err != nil || configurationSHA256 != operation.ConfigurationSHA256 {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	if err := validateCatalogBackupReference(layout, backup, operation.NextTarget, schema); err != nil {
		return CatalogSelectorOperation{}, err
	}
	operation.Stage = catalogOperationRollbackPrepare
	operation.RollbackBackupDirectory = backup.Directory
	operation.RollbackBackupManifestSHA256 = backup.ManifestSHA256
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return operation, err
	}
	if err := restore(); err != nil {
		return operation, err
	}
	if err := switchCatalogSelector(layout, operation.NextTarget, ""); err != nil {
		return operation, err
	}
	operation.Stage = catalogOperationRolledBack
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return operation, err
	}
	return operation, nil
}
