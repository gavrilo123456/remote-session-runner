package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
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
	freshInstall        *CatalogFreshInstallBaseline
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

// ReadPRDCatalogCohortHostPreflight captures an online, read-only PRD work and
// identity baseline, takes a consistent SQLite online backup, then confirms
// the same work/selector/configuration state still holds. It does not stop or
// mutate the service; the later ordered host-fence action does that only after
// the prepared cohort receipt is synchronized.
func ReadPRDCatalogCohortHostPreflight(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe,
	releaseID string, requiredBytes uint64, legacyCompatibility *CatalogLegacyRollbackCompatibility) (CatalogCohortHostPreflightReport, error) {
	return readPRDCatalogCohortHostPreflight(ctx, plan, service, releaseID, requiredBytes, legacyCompatibility, "")
}

// ReadPRDCatalogReplacementHostPreflight captures the same consistent PRD
// backup and work/configuration/schema proof as an ordinary preflight while
// permitting only the exact fully switched verifying predecessor to remain
// as the current admission hold. The predecessor's selected release must be
// the current rollback baseline on this host.
func ReadPRDCatalogReplacementHostPreflight(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe,
	releaseID string, requiredBytes uint64, predecessorOperationID string) (CatalogCohortHostPreflightReport, error) {
	if !validCatalogOperationID(predecessorOperationID) {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	return readPRDCatalogCohortHostPreflight(ctx, plan, service, releaseID, requiredBytes, nil, predecessorOperationID)
}

func readPRDCatalogCohortHostPreflight(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe,
	releaseID string, requiredBytes uint64, legacyCompatibility *CatalogLegacyRollbackCompatibility,
	predecessorOperationID string) (CatalogCohortHostPreflightReport, error) {
	if validatePRDPlan(plan) != nil || service == nil || !validCatalogReleaseID(releaseID) ||
		requiredBytes != prdCatalogSelectorHeadroom {
		return CatalogCohortHostPreflightReport{}, ErrPRDUpgradeGuard
	}
	cohortPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	var predecessor CatalogCohortReceipt
	if predecessorOperationID == "" {
		if held, _, err := CatalogCohortAdmissionHeld(cohortPlan); err != nil || held {
			return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
		}
	} else {
		var err error
		predecessor, err = ReadCatalogCohortReceipt(cohortPlan, predecessorOperationID)
		if err != nil || predecessor.Stage != CatalogCohortVerifying ||
			!catalogCohortReceiptSupersessionEligible(predecessor) ||
			CatalogCohortAdmissionHoldMatches(cohortPlan, predecessorOperationID) != nil {
			return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
		}
	}
	root, err := DefaultCatalogRootForPlan(cohortPlan)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(cohortPlan, root)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	catalogFD, err := acquirePRDCatalogOnlinePreflightLock(root)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	defer releaseCatalogRootPublicationLock(catalogFD)
	// This is an online, read-only preflight. The running PRD service owns the
	// lane lifecycle lock for its lifetime, so acquiring it here would make the
	// documented online backup impossible while the service is active. The
	// catalog publication lock serializes catalog operations; fresh service,
	// work, selector, configuration, schema, and backup proofs are checked
	// before and after the online snapshot. The later held-cohort fence acquires
	// the lifecycle lock before stopping or changing the selected service.
	active, err := service.Status(ctx, plan)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	if predecessorOperationID != "" && active {
		return CatalogCohortHostPreflightReport{}, ErrSimpleLaneNotQuiescent
	}
	before, err := readPRDCatalogProof(ctx, plan, layout)
	if err != nil || !before.work.Empty() {
		return CatalogCohortHostPreflightReport{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	if predecessorOperationID != "" {
		index := cohortHostIndex(predecessor, plan.HostID)
		if index < 0 || validatePRDCatalogReplacementBaseline(layout, predecessor, predecessor.Hosts[index], before) != nil {
			return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
		}
	}
	target, err := InspectCatalogRelease(root, plan.HostID, releaseID)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	if !catalogDatabaseSupportsSchema(resolvedCatalogSelection{kind: "catalog", release: &target}, before.schema) {
		return CatalogCohortHostPreflightReport{}, ErrCatalogRollbackUnsafe
	}
	if err := validatePRDConfig(plan); err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	previousReadSchemaMax := before.readSchemaMax
	var compatibility *CatalogLegacyRollbackCompatibility
	if before.freshInstall != nil {
		if legacyCompatibility != nil {
			return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
		}
		previousReadSchemaMax = 0
	} else if before.selectorTarget == "" {
		if legacyCompatibility == nil || ValidateCatalogLegacyRollbackCompatibility(*legacyCompatibility,
			before.legacyBundleSHA256, before.configurationSHA256, before.schema) != nil {
			return CatalogCohortHostPreflightReport{}, ErrCatalogRollbackUnsafe
		}
		previousReadSchemaMax = legacyCompatibility.ReadSchemaMax
		copyEvidence := *legacyCompatibility
		copyEvidence.SemanticChecks = append([]string(nil), legacyCompatibility.SemanticChecks...)
		compatibility = &copyEvidence
	} else if legacyCompatibility != nil {
		return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
	}
	backup, err := CreatePRDCatalogBackup(ctx, plan, target.Identity.BundleSHA256)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	if backup.Manifest.SchemaVersion != before.schema || backup.Manifest.DatabaseSHA256 == "" ||
		verifyPRDBackupWorkSnapshot(ctx, plan, backup.Manifest, before.workSHA256, before.schema) != nil {
		return CatalogCohortHostPreflightReport{}, ErrSimpleBackupRequired
	}
	after, err := readPRDCatalogProof(ctx, plan, layout)
	if err != nil || !after.work.Empty() || after.workSHA256 != before.workSHA256 || after.schema != before.schema ||
		after.configurationSHA256 != before.configurationSHA256 || after.selectorTarget != before.selectorTarget ||
		after.bundleSHA256 != before.bundleSHA256 || after.legacyBundleSHA256 != before.legacyBundleSHA256 ||
		freshInstallEvidenceSHA(after.freshInstall) != freshInstallEvidenceSHA(before.freshInstall) {
		return CatalogCohortHostPreflightReport{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	activeAfter, err := service.Status(ctx, plan)
	if err != nil || activeAfter != active {
		return CatalogCohortHostPreflightReport{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	if predecessorOperationID != "" {
		index := cohortHostIndex(predecessor, plan.HostID)
		if index < 0 || validatePRDCatalogReplacementBaseline(layout, predecessor, predecessor.Hosts[index], after) != nil {
			return CatalogCohortHostPreflightReport{}, ErrCatalogOperationInvalid
		}
	}
	available, selectorRequired, err := ReadPRDCatalogSelectorCapacity(plan)
	if err != nil || selectorRequired != requiredBytes || available < requiredBytes {
		return CatalogCohortHostPreflightReport{}, errors.Join(ErrCatalogInsufficientSpace, err)
	}
	return CatalogCohortHostPreflightReport{
		Preflight: CatalogCohortHostPreflight{
			HostID: plan.HostID, ServiceActiveAtPreflight: active, PreviousTarget: after.selectorTarget,
			PreviousBundleSHA256: after.previousBundleSHA256(), PreviousReadSchemaMax: previousReadSchemaMax,
			LegacyRollback: compatibility, FreshInstall: after.freshInstall, Target: target.Identity, ConfigurationSHA256: after.configurationSHA256,
			SchemaVersion: after.schema, WorkInventorySHA256: after.workSHA256, WorkInventoryEmpty: after.work.Empty(),
			PreflightAt: time.Now().UTC().Format(time.RFC3339Nano), BackupDirectory: backup.Directory,
			BackupManifestSHA256: backupManifestSHA256(backup.Directory), AvailableBytes: available, RequiredBytes: requiredBytes,
		},
		WorkInventory: after.work,
	}, nil
}

// acquirePRDCatalogOnlinePreflightLock serializes the online PRD snapshot with
// catalog publication without contending with the active service's lifecycle
// lock. Stop/switch operations use the full catalog-plus-lifecycle lock pair.
func acquirePRDCatalogOnlinePreflightLock(catalogRoot string) (int, error) {
	return acquireCatalogRootPublicationLock(catalogRoot)
}

func validatePRDCatalogReplacementBaseline(layout catalogSelectorLayout, predecessor CatalogCohortReceipt,
	oldHost CatalogCohortHostPreflight, proof prdCatalogProof) error {
	if predecessor.Lane != config.ReleaseLanePRD || predecessor.Stage != CatalogCohortVerifying ||
		!catalogCohortReceiptSupersessionEligible(predecessor) || !cohortHasSwitched(predecessor, layout.hostID) ||
		oldHost.HostID != layout.hostID || proof.work.Empty() == false ||
		proof.workSHA256 != oldHost.WorkInventorySHA256 || proof.schema != oldHost.SchemaVersion ||
		proof.configurationSHA256 != oldHost.ConfigurationSHA256 ||
		proof.selectorTarget != filepath.Join(layout.catalogRoot, predecessor.ReleaseID, layout.hostID) ||
		proof.bundleSHA256 != oldHost.Target.BundleSHA256 {
		return ErrCatalogOperationInvalid
	}
	return nil
}

// FenceCatalogPRDHost performs the supported first-adoption stop after a
// prepared PRD receipt has been synchronized. It verifies the receipt's exact
// online baseline and backup, stops only this unsuffixed service, and returns
// hashable evidence for the ordered host-fence transition.
func FenceCatalogPRDHost(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController,
	receipt CatalogCohortReceipt) (CatalogCohortHostFence, error) {
	return fenceCatalogPRDHost(ctx, plan, service, receipt, "")
}

// FenceCatalogPRDReplacementHost records the already-stopped host fence for
// a prepared successor while the exact verifying predecessor continues to
// hold admissions. It does not stop or start a service.
func FenceCatalogPRDReplacementHost(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController,
	receipt CatalogCohortReceipt, predecessorOperationID string) (CatalogCohortHostFence, error) {
	if !validCatalogOperationID(predecessorOperationID) {
		return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
	}
	return fenceCatalogPRDHost(ctx, plan, service, receipt, predecessorOperationID)
}

type catalogPRDCohortFenceService interface {
	Status(context.Context, PRDUpgradePlan) (bool, error)
	Stop(context.Context, PRDUpgradePlan) error
}

func fenceCatalogPRDHost(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController,
	receipt CatalogCohortReceipt, predecessorOperationID string) (CatalogCohortHostFence, error) {
	if validatePRDPlan(plan) != nil || service == nil || ValidateCatalogCohortReceipt(receipt) != nil ||
		receipt.Lane != config.ReleaseLanePRD || receipt.Stage != CatalogCohortPrepared ||
		!containsString(catalogCohortSwitchOrder, plan.HostID) {
		return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
	}
	cohortPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	var predecessor CatalogCohortReceipt
	if predecessorOperationID == "" {
		if held, _, err := CatalogCohortAdmissionHeld(cohortPlan); err != nil || held {
			return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
		}
	} else {
		var err error
		predecessor, err = ReadCatalogCohortReceipt(cohortPlan, predecessorOperationID)
		if err != nil || predecessor.Stage != CatalogCohortVerifying ||
			!catalogCohortReceiptSupersessionEligible(predecessor) ||
			CatalogCohortAdmissionHoldMatches(cohortPlan, predecessorOperationID) != nil {
			return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
		}
	}
	index := cohortHostIndex(receipt, plan.HostID)
	if index < 0 {
		return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
	}
	host := receipt.Hosts[index]
	root, err := DefaultCatalogRootForPlan(Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace})
	if err != nil {
		return CatalogCohortHostFence{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}, root)
	if err != nil {
		return CatalogCohortHostFence{}, err
	}
	catalogFD, err := acquireCatalogRootPublicationLock(root)
	if err != nil {
		return CatalogCohortHostFence{}, err
	}
	defer releaseCatalogRootPublicationLock(catalogFD)
	active, err := service.Status(ctx, plan)
	if err != nil || !validPRDCohortFenceServiceState(host.ServiceActiveAtPreflight, active) {
		return CatalogCohortHostFence{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	proof, err := readPRDCatalogProof(ctx, plan, layout)
	if err != nil || !proof.work.Empty() || proof.workSHA256 != host.WorkInventorySHA256 || proof.schema != host.SchemaVersion ||
		proof.configurationSHA256 != host.ConfigurationSHA256 || proof.selectorTarget != host.PreviousTarget ||
		proof.previousBundleSHA256() != host.PreviousBundleSHA256 {
		return CatalogCohortHostFence{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	if predecessorOperationID != "" {
		if active || host.ServiceActiveAtPreflight || host.LegacyRollback != nil {
			return CatalogCohortHostFence{}, ErrSimpleLaneNotQuiescent
		}
		oldIndex := cohortHostIndex(predecessor, plan.HostID)
		if oldIndex < 0 || validatePRDCatalogReplacementBaseline(layout, predecessor, predecessor.Hosts[oldIndex], proof) != nil ||
			validatePRDPreflightBackup(ctx, plan, host) != nil {
			return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
		}
	}
	if host.FreshInstall != nil && (proof.freshInstall == nil || proof.freshInstall.EvidenceSHA256 != host.FreshInstall.EvidenceSHA256) {
		return CatalogCohortHostFence{}, ErrCatalogOperationInvalid
	}
	if err := validatePRDPreflightBackup(ctx, plan, host); err != nil {
		return CatalogCohortHostFence{}, err
	}
	// PRD keeps the selected lifecycle flock for the service lifetime. Check
	// and validate its online state first, stop only this lane, then acquire the
	// flock and recheck status. A restart in that gap prevents the fence from
	// being recorded instead of letting an active service race maintenance.
	laneFD, err := stopPRDServiceAndAcquireFenceLock(ctx, plan, host.ServiceActiveAtPreflight, service, acquirePRDUpgradeLock)
	if err != nil {
		return CatalogCohortHostFence{}, err
	}
	defer releasePRDUpgradeLock(laneFD)
	post, err := readPRDCatalogProof(ctx, plan, layout)
	if err != nil || !post.work.Empty() || post.workSHA256 != host.WorkInventorySHA256 || post.schema != host.SchemaVersion ||
		post.configurationSHA256 != host.ConfigurationSHA256 || post.selectorTarget != host.PreviousTarget ||
		post.previousBundleSHA256() != host.PreviousBundleSHA256 ||
		freshInstallEvidenceSHA(post.freshInstall) != freshInstallEvidenceSHA(host.FreshInstall) {
		return CatalogCohortHostFence{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	return CatalogCohortHostFence{
		HostID: plan.HostID, ServiceWasActive: host.ServiceActiveAtPreflight, ServiceStopped: true, WorkInventorySHA256: post.workSHA256,
		ConfigurationSHA256: post.configurationSHA256, SelectorTarget: post.selectorTarget,
		PreviousBundleSHA256: post.previousBundleSHA256(), FreshInstallEvidenceSHA256: freshInstallEvidenceSHA(post.freshInstall), DatabaseSchema: post.schema,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func validPRDCohortFenceServiceState(activeAtPreflight, currentlyActive bool) bool {
	// A crash after Stop but before the fence receipt transition is safely
	// retryable. The subsequent proof still requires the exact empty inventory,
	// configuration, selector, binary identity, and backup captured earlier.
	return !currentlyActive || activeAtPreflight
}

func stopPRDServiceAndAcquireFenceLock(ctx context.Context, plan PRDUpgradePlan, activeAtPreflight bool,
	service catalogPRDCohortFenceService, acquire func(PRDUpgradePlan) (int, error)) (int, error) {
	if service == nil || acquire == nil {
		return -1, ErrPRDUpgradeGuard
	}
	active, err := service.Status(ctx, plan)
	if err != nil || !validPRDCohortFenceServiceState(activeAtPreflight, active) {
		return -1, errors.Join(ErrPRDUpgradeGuard, err)
	}
	if active {
		if err := service.Stop(ctx, plan); err != nil {
			return -1, fmt.Errorf("stop selected unsuffixed PRD service for initial cohort fence: %w", err)
		}
	}
	activeAfterStop, err := service.Status(ctx, plan)
	if err != nil || activeAfterStop {
		return -1, errors.Join(ErrPRDUpgradeGuard, err)
	}
	laneFD, err := acquire(plan)
	if err != nil {
		return -1, err
	}
	activeWithLock, err := service.Status(ctx, plan)
	if err != nil || activeWithLock {
		releasePRDUpgradeLock(laneFD)
		return -1, errors.Join(ErrPRDUpgradeGuard, err)
	}
	return laneFD, nil
}

func validatePRDPreflightBackup(ctx context.Context, plan PRDUpgradePlan, preflight CatalogCohortHostPreflight) error {
	manifest, err := loadPRDBackupManifest(plan, preflight.BackupDirectory)
	if err != nil || manifest.SchemaVersion != preflight.SchemaVersion || manifest.DatabasePath != plan.Namespace.Database ||
		manifest.BundleSHA256 != preflight.Target.BundleSHA256 || backupManifestSHA256(preflight.BackupDirectory) != preflight.BackupManifestSHA256 {
		return ErrSimpleBackupRequired
	}
	if err := verifyPRDBackupTargets(manifest, true); err != nil {
		return err
	}
	return verifyPRDBackupWorkSnapshot(ctx, plan, manifest, preflight.WorkInventorySHA256, preflight.SchemaVersion)
}

// verifyPRDBackupWorkSnapshot reads the private online-backup database with
// the compatible read-only connection and proves that its schema and complete
// normalized work inventory match the preflight receipt. It never opens the
// live PRD database through store.Open or starts a worker.
func verifyPRDBackupWorkSnapshot(ctx context.Context, plan PRDUpgradePlan, manifest PRDBackupManifest,
	expectedWorkSHA256 string, expectedSchema int) error {
	if validatePRDPlan(plan) != nil || !filepath.IsAbs(manifest.DatabaseBackupPath) ||
		filepath.Clean(manifest.DatabaseBackupPath) != manifest.DatabaseBackupPath ||
		!bundleDigestPattern.MatchString(expectedWorkSHA256) || expectedSchema <= 0 ||
		manifest.SchemaVersion != expectedSchema {
		return ErrSimpleBackupRequired
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, manifest.DatabaseBackupPath)
	if err != nil {
		return errors.Join(ErrSimpleBackupRequired, err)
	}
	schema, inventory, readErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return errors.Join(ErrSimpleBackupRequired, err)
	}
	if schema != expectedSchema || inventory.SHA256() != expectedWorkSHA256 {
		return ErrSimpleBackupRequired
	}
	return nil
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

// AcquireCatalogPRDProbeDispositionLocks serializes the one supported,
// operation-specific disposition of an accepted Linux PRD maintenance probe.
// The caller must perform its exact scope, inventory, config, runtime-marker,
// and database checks while the returned locks remain held. It never stops or
// starts services and does not release execution capacity.
func AcquireCatalogPRDProbeDispositionLocks(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController,
	cohortOperationID string) (func(), error) {
	if ctx == nil || validatePRDPlan(plan) != nil || plan.HostID != "linux-host" || service == nil ||
		cohortOperationID != "63471d41af07b7afaf577d6a0989cc03" {
		return nil, ErrCatalogPRDRecovery
	}
	catalogPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	if err := CatalogCohortAdmissionHoldMatches(catalogPlan, cohortOperationID); err != nil {
		return nil, errors.Join(ErrCatalogPRDRecovery, err)
	}
	root, err := DefaultCatalogRootForPlan(catalogPlan)
	if err != nil {
		return nil, errors.Join(ErrCatalogPRDRecovery, err)
	}
	catalogFD, err := acquireCatalogRootPublicationLock(root)
	if err != nil {
		return nil, err
	}
	laneFD, err := acquirePRDUpgradeLock(plan)
	if err != nil {
		releaseCatalogRootPublicationLock(catalogFD)
		return nil, err
	}
	closeLocks := func() {
		releasePRDUpgradeLock(laneFD)
		releaseCatalogRootPublicationLock(catalogFD)
	}
	active, err := service.Status(ctx, plan)
	if err != nil || active {
		closeLocks()
		return nil, errors.Join(ErrCatalogPRDRecovery, err)
	}
	if err := CatalogCohortAdmissionHoldMatches(catalogPlan, cohortOperationID); err != nil {
		closeLocks()
		return nil, errors.Join(ErrCatalogPRDRecovery, err)
	}
	return closeLocks, nil
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

// CohortHostPreflight revalidates the initial online PRD report after the
// selected service has been stopped under its synchronized cohort hold. It
// returns fresh evidence for the ordered preflight transition without
// changing the original work-inventory hash or backup identity.
func (m *CatalogPRDMaintenance) CohortHostPreflight(ctx context.Context, expected CatalogCohortHostPreflight) (CatalogCohortHostPreflightReport, error) {
	if m == nil || m.closed || m.backup == nil || expected.HostID != m.plan.HostID ||
		!prdCatalogProofFresh(m.proof) || !expected.WorkInventoryEmpty ||
		!bundleDigestPattern.MatchString(expected.WorkInventorySHA256) {
		return CatalogCohortHostPreflightReport{}, ErrSimpleBackupRequired
	}
	if err := m.recheck(ctx); err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	proof := m.proof
	if proof.workSHA256 != expected.WorkInventorySHA256 || proof.schema != expected.SchemaVersion ||
		proof.configurationSHA256 != expected.ConfigurationSHA256 || proof.selectorTarget != expected.PreviousTarget ||
		proof.previousBundleSHA256() != expected.PreviousBundleSHA256 {
		return CatalogCohortHostPreflightReport{}, ErrPRDUpgradeGuard
	}
	target, err := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, expected.Target.ReleaseID)
	if err != nil || target.Identity != expected.Target {
		return CatalogCohortHostPreflightReport{}, ErrInvalidCatalogRelease
	}
	if err := validatePRDPreflightBackup(ctx, m.plan, expected); err != nil ||
		m.backup.Directory != expected.BackupDirectory || backupManifestSHA256(m.backup.Directory) != expected.BackupManifestSHA256 {
		return CatalogCohortHostPreflightReport{}, errors.Join(ErrSimpleBackupRequired, err)
	}
	available, required, err := ReadPRDCatalogSelectorCapacity(m.plan)
	if err != nil || required != expected.RequiredBytes || available < required {
		return CatalogCohortHostPreflightReport{}, errors.Join(ErrCatalogInsufficientSpace, err)
	}
	proof.availableBytes, proof.requiredBytes = available, required
	return CatalogCohortHostPreflightReport{
		Preflight: CatalogCohortHostPreflight{
			HostID: expected.HostID, ServiceActiveAtPreflight: expected.ServiceActiveAtPreflight,
			PreviousTarget: expected.PreviousTarget, PreviousBundleSHA256: expected.PreviousBundleSHA256,
			PreviousReadSchemaMax: expected.PreviousReadSchemaMax, LegacyRollback: expected.LegacyRollback, FreshInstall: expected.FreshInstall,
			Target: expected.Target, ConfigurationSHA256: proof.configurationSHA256, SchemaVersion: proof.schema,
			WorkInventorySHA256: proof.workSHA256, WorkInventoryEmpty: proof.work.Empty(),
			PreflightAt: time.Now().UTC().Format(time.RFC3339Nano), BackupDirectory: m.backup.Directory,
			BackupManifestSHA256: backupManifestSHA256(m.backup.Directory), AvailableBytes: available, RequiredBytes: required,
		},
		WorkInventory: proof.work,
	}, nil
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
	if !cohortEvidenceFresh(receipt, time.Now().UTC().Format(time.RFC3339Nano)) {
		required, requiredErr := catalogCohortContinuationRequiredBytes(prdCatalogSelectorHeadroom)
		if requiredErr != nil || m.proof.availableBytes < required {
			return CatalogSelectorOperation{}, errors.Join(ErrCatalogInsufficientSpace, requiredErr)
		}
	}
	index := cohortHostIndex(receipt, m.plan.HostID)
	previousMaxMatches := index >= 0 && receipt.Hosts[index].PreviousReadSchemaMax == m.proof.readSchemaMax
	if index >= 0 && receipt.Hosts[index].FreshInstall != nil {
		previousMaxMatches = m.proof.freshInstall != nil &&
			receipt.Hosts[index].FreshInstall.EvidenceSHA256 == m.proof.freshInstall.EvidenceSHA256 &&
			receipt.Hosts[index].PreviousReadSchemaMax == 0 && receipt.Hosts[index].PreviousBundleSHA256 == ""
	} else if index >= 0 && m.proof.selectorTarget == "" {
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
	cohortPlan := Plan{HostID: m.plan.HostID, Lane: config.ReleaseLanePRD, Namespace: m.plan.Namespace}
	cohortReceipt, err := ReadCatalogCohortReceipt(cohortPlan, operation.CohortOperationID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	cohortHost := cohortHostIndex(cohortReceipt, m.plan.HostID)
	freshPredecessor := cohortHost >= 0 && cohortReceipt.Hosts[cohortHost].FreshInstall != nil
	if operation.Stage == catalogOperationRollbackPrepare && operation.PreviousTarget == "" {
		if freshPredecessor {
			baseline, valid, baselineErr := currentFreshInstallBaseline(m.plan)
			if baselineErr != nil || !valid || baseline.EvidenceSHA256 != cohortReceipt.Hosts[cohortHost].FreshInstall.EvidenceSHA256 {
				return operation, errors.Join(ErrCatalogRollbackUnsafe, baselineErr)
			}
			// A fresh replacement has no previous launcher to restore. Keep the
			// installed configuration and history, but leave the selector absent
			// and the service stopped under the cohort's rollback hold.
		} else if err := restorePRDLegacyServiceArtifacts(ctx, m.plan, m.service, manifest); err != nil {
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
	if err != nil || validateCatalogOperation(m.layout, operation) != nil || operation.CohortOperationID == "" ||
		(operation.Stage != catalogOperationComplete && operation.Stage != catalogOperationRolledBack) {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	cohortPlan := Plan{HostID: m.plan.HostID, Lane: config.ReleaseLanePRD, Namespace: m.plan.Namespace}
	receipt, err := ReadCatalogCohortReceipt(cohortPlan, operation.CohortOperationID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	current, err := inspectCatalogSelector(m.layout)
	if err != nil {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	if operation.Stage == catalogOperationRolledBack {
		// A prior attempt may have atomically restored the selector and written
		// its host rollback transition, then been interrupted while synchronizing
		// the cohort receipt. Revalidate the exact journal, selector, and receipt
		// evidence instead of attempting that selector mutation a second time.
		if validatePRDCatalogRollbackRetryState(m.layout, receipt, operation, current, m.proof.schema,
			m.proof.configurationSHA256) != nil {
			return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
		}
		return operation, nil
	}
	if validateCatalogCohortHostMayRollback(receipt, m.layout, operation, current.Target) != nil {
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
	if index >= 0 && receipt.Hosts[index].FreshInstall != nil {
		baseline, valid, baselineErr := currentFreshInstallBaseline(m.plan)
		if baselineErr != nil || !valid || baseline.EvidenceSHA256 != receipt.Hosts[index].FreshInstall.EvidenceSHA256 ||
			operation.PreviousTarget != "" || receipt.Hosts[index].PreviousBundleSHA256 != "" || receipt.Hosts[index].LegacyRollback != nil {
			return CatalogSelectorOperation{}, errors.Join(ErrCatalogRollbackUnsafe, baselineErr)
		}
		rolledBack, err := rollbackPRDCatalogSelectorToLegacy(m.layout, operation, m.proof.schema, backupRef, func() error {
			// Fresh-install rollback deliberately leaves artifacts, config, and
			// database in place for evidence/retry. It never restores old state.
			return nil
		})
		if err != nil {
			return rolledBack, err
		}
		if err := recordCatalogCohortRollback(cohortPlan, rolledBack); err != nil {
			return rolledBack, err
		}
		return rolledBack, nil
	}
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
		proof.selectorTarget != m.proof.selectorTarget || proof.bundleSHA256 != m.proof.bundleSHA256 || proof.legacyBundleSHA256 != m.proof.legacyBundleSHA256 ||
		freshInstallEvidenceSHA(proof.freshInstall) != freshInstallEvidenceSHA(m.proof.freshInstall) {
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
	schema, work, inventoryErr := readPRDWorkSnapshot(ctx, database)
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
	proof.workSHA256 = work.SHA256()
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
		baseline, isFreshInstall, baselineErr := freshInstallBaselineForProof(plan, proof)
		if baselineErr != nil {
			return prdCatalogProof{}, errors.Join(ErrPRDUpgradeGuard, baselineErr)
		}
		if isFreshInstall {
			if validateFreshPRDStagedArtifactInventory(plan.HostID, preflight.BinarySHA256) != nil ||
				validateFreshPRDStagedArtifactsOnDisk(plan) != nil {
				return prdCatalogProof{}, ErrPRDUpgradeGuard
			}
			proof.freshInstall = &baseline
		} else if len(preflight.MissingComponents) != 0 {
			return prdCatalogProof{}, ErrPRDUpgradeGuard
		} else {
			proof.legacyBundleSHA256, err = PRDLegacyBundleDigest(preflight)
			if err != nil {
				return prdCatalogProof{}, err
			}
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

func freshInstallBaselineForProof(plan PRDUpgradePlan, proof prdCatalogProof) (CatalogFreshInstallBaseline, bool, error) {
	if validatePRDPlan(plan) != nil {
		return CatalogFreshInstallBaseline{}, false, ErrCatalogOperationInvalid
	}
	if plan.HostID != "linux-host" {
		return CatalogFreshInstallBaseline{}, false, nil
	}
	if _, err := os.Lstat(catalogFreshInstallBaselinePath(plan)); errors.Is(err, os.ErrNotExist) {
		return CatalogFreshInstallBaseline{}, false, nil
	} else if err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	evidence, err := readCatalogFreshInstallBaseline(plan)
	if err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	host := CatalogCohortHostPreflight{HostID: plan.HostID, ConfigurationSHA256: proof.configurationSHA256,
		SchemaVersion: proof.schema, WorkInventorySHA256: proof.workSHA256, WorkInventoryEmpty: proof.work.Empty()}
	if err := validateCatalogFreshInstallBaseline(evidence, host); err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	return evidence, true, nil
}

// validateFreshPRDStagedArtifacts permits only the exact launcher files that
// the catalog selector transaction may have created before an interrupted
// first switch. The immutable service bundle itself lives in the release
// catalog and is not accepted from the legacy bin directory.
func validateFreshPRDStagedArtifactInventory(hostID string, observed map[string]string) error {
	if hostID != "linux-host" {
		return ErrPRDUpgradeGuard
	}
	artifacts, err := BuildPRDCatalogServiceArtifacts(hostID)
	if err != nil {
		return err
	}
	expected := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Kind == ArtifactEntrypoint || artifact.Kind == ArtifactSSHWrapper {
			expected[filepath.Base(artifact.InstallPath)] = sha256HexString([]byte(artifact.Contents))
		}
	}
	if len(observed) > len(expected) {
		return ErrPRDUpgradeGuard
	}
	for name, digest := range observed {
		if want, ok := expected[name]; !ok || digest != want {
			return ErrPRDUpgradeGuard
		}
	}
	return nil
}

func validateFreshPRDStagedArtifactsOnDisk(plan PRDUpgradePlan) error {
	if plan.HostID != "linux-host" || validatePRDPlan(plan) != nil {
		return ErrPRDUpgradeGuard
	}
	artifacts, err := BuildPRDCatalogServiceArtifacts(plan.HostID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if _, statErr := os.Lstat(artifact.InstallPath); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil || verifyPRDCatalogArtifact(artifact) != nil {
			return ErrPRDUpgradeGuard
		}
	}
	return nil
}

func freshInstallEvidenceSHA(evidence *CatalogFreshInstallBaseline) string {
	if evidence == nil {
		return ""
	}
	return evidence.EvidenceSHA256
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

const catalogFreshInstallBaselineFile = "catalog-prd-fresh-install.json"

func catalogFreshInstallBaselinePath(plan PRDUpgradePlan) string {
	return filepath.Join(plan.Namespace.Staging, catalogFreshInstallBaselineFile)
}

func readCatalogFreshInstallBaseline(plan PRDUpgradePlan) (CatalogFreshInstallBaseline, error) {
	if plan.HostID != "linux-host" || validatePRDPlan(plan) != nil {
		return CatalogFreshInstallBaseline{}, ErrCatalogOperationInvalid
	}
	path := catalogFreshInstallBaselinePath(plan)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > 1<<20 {
		return CatalogFreshInstallBaseline{}, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || !catalogFileHasSingleLinkFromStat(stat) {
		return CatalogFreshInstallBaseline{}, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogFreshInstallBaseline{}, ErrCatalogOperationInvalid
	}
	var evidence CatalogFreshInstallBaseline
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return CatalogFreshInstallBaseline{}, ErrCatalogOperationInvalid
	}
	if err := validateCatalogFreshInstallBaseline(evidence, CatalogCohortHostPreflight{
		HostID: "linux-host", ConfigurationSHA256: evidence.ConfigurationSHA256, SchemaVersion: evidence.DatabaseSchema,
		WorkInventorySHA256: evidence.WorkInventorySHA256, WorkInventoryEmpty: true,
	}); err != nil {
		return CatalogFreshInstallBaseline{}, err
	}
	canonical, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return CatalogFreshInstallBaseline{}, ErrCatalogOperationInvalid
	}
	return evidence, nil
}

// currentFreshInstallBaseline binds the durable absence observation to the
// current replacement config, schema, and one coherent empty-work read. It is
// intentionally read-only and never creates or migrates the database.
func currentFreshInstallBaseline(plan PRDUpgradePlan) (CatalogFreshInstallBaseline, bool, error) {
	if plan.HostID != "linux-host" || validatePRDPlan(plan) != nil {
		return CatalogFreshInstallBaseline{}, false, ErrCatalogOperationInvalid
	}
	path := catalogFreshInstallBaselinePath(plan)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return CatalogFreshInstallBaseline{}, false, nil
	} else if err != nil {
		return CatalogFreshInstallBaseline{}, false, ErrCatalogOperationInvalid
	}
	evidence, err := readCatalogFreshInstallBaseline(plan)
	if err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	configurationSHA256, err := hashPRDRegularFile(plan.Namespace.ConfigFile)
	if err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	schema, work, inventoryErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	host := CatalogCohortHostPreflight{HostID: plan.HostID, ConfigurationSHA256: configurationSHA256,
		SchemaVersion: schema, WorkInventorySHA256: work.SHA256(), WorkInventoryEmpty: work.Empty()}
	if err := validateCatalogFreshInstallBaseline(evidence, host); err != nil {
		return CatalogFreshInstallBaseline{}, false, err
	}
	return evidence, true, nil
}

// ReadCatalogFreshInstallBaseline reports the verified first-install
// predecessor for the only supported replacement worker. It is read-only and
// does not create a namespace, open a database for migration, or infer
// freshness from an unselected selector alone.
func ReadCatalogFreshInstallBaseline(hostID string) (CatalogFreshInstallBaselineReport, error) {
	if hostID != "linux-host" {
		return CatalogFreshInstallBaselineReport{}, ErrCatalogOperationInvalid
	}
	plan, err := BuildPRDUpgradePlan(hostID)
	if err != nil {
		return CatalogFreshInstallBaselineReport{}, err
	}
	evidence, present, err := currentFreshInstallBaseline(plan)
	if err != nil {
		return CatalogFreshInstallBaselineReport{}, err
	}
	report := CatalogFreshInstallBaselineReport{HostID: hostID, Present: present}
	if present {
		report.Baseline = &evidence
	}
	return report, nil
}

func writeCatalogFreshInstallBaseline(plan PRDUpgradePlan, evidence CatalogFreshInstallBaseline) error {
	if plan.HostID != "linux-host" || validatePRDPlan(plan) != nil ||
		validateCatalogFreshInstallBaseline(evidence, CatalogCohortHostPreflight{HostID: "linux-host",
			ConfigurationSHA256: evidence.ConfigurationSHA256, SchemaVersion: evidence.DatabaseSchema,
			WorkInventorySHA256: evidence.WorkInventorySHA256, WorkInventoryEmpty: true}) != nil {
		return ErrCatalogOperationInvalid
	}
	if err := ensurePRDPrivateDirectory(plan.Namespace.Staging); err != nil {
		return err
	}
	path := catalogFreshInstallBaselinePath(plan)
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if bytes.Equal(existing, encoded) {
			return nil
		}
		return ErrCatalogOperationInvalid
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := writePrivateSynced(path, encoded, 0o600); err != nil {
		return err
	}
	return syncDirectory(plan.Namespace.Staging)
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
	freshInstall := false
	var err error
	if plan.HostID == "linux-host" {
		_, freshInstall, err = currentFreshInstallBaseline(plan)
		if err != nil {
			return err
		}
	}
	artifacts, err := BuildPRDCatalogServiceArtifacts(plan.HostID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if verifyPRDCatalogArtifact(artifact) == nil {
			if freshInstall && artifact.Kind == ArtifactSystemdUnit {
				if err := createFreshPRDCatalogArtifact(ctx, plan, service, artifact); err != nil {
					return err
				}
			}
			continue
		}
		original, ok := findPRDBackupFile(backup, artifact.InstallPath)
		if !ok {
			if !freshInstall {
				return ErrPRDUpgradeGuard
			}
			if err := createFreshPRDCatalogArtifact(ctx, plan, service, artifact); err != nil {
				return err
			}
			continue
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

func createFreshPRDCatalogArtifact(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController, artifact Artifact) error {
	if plan.HostID != "linux-host" || service == nil || !freshPRDCatalogArtifactAllowed(plan, artifact) {
		return ErrPRDUpgradeGuard
	}
	if _, fresh, err := currentFreshInstallBaseline(plan); err != nil || !fresh {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	if _, err := os.Lstat(artifact.InstallPath); err == nil {
		if artifact.Owner != "root" || verifyPRDCatalogArtifact(artifact) != nil {
			return ErrSimpleArtifactConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if artifact.Owner == "root" {
		if err := ensurePRDPrivateDirectory(plan.Namespace.Staging); err != nil {
			return err
		}
		staged, err := os.CreateTemp(plan.Namespace.Staging, "runner-prd-catalog-unit-*")
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
		result, err := service.base.runner.Run(ctx, "/usr/bin/sudo", "-n", "/bin/sh", "-c",
			atomicPRDSystemdCreateScript, "runner-lane", artifact.InstallPath, stagedPath, newSHA)
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("create exact fresh PRD systemd unit failed (exit %d)", result.ExitCode)
		}
	} else {
		if err := createFreshPRDCatalogUserArtifact(artifact); err != nil {
			return err
		}
	}
	if err := verifyPRDCatalogArtifact(artifact); err != nil {
		return errors.Join(ErrSimpleArtifactConflict, err)
	}
	return nil
}

// createFreshPRDCatalogUserArtifact is the shared owner-managed creation path
// used for the replacement entrypoint and forced-command wrapper. Keeping the
// ownership and parent checks here lets native Linux tests exercise the exact
// write branch with a disposable directory; the public caller still validates
// the artifact against the canonical PRD namespace before reaching it.
func createFreshPRDCatalogUserArtifact(artifact Artifact) error {
	current, err := user.Current()
	group, groupErr := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil || groupErr != nil || artifact.Owner != current.Username || artifact.Group != group.Name {
		return ErrSimpleArtifactConflict
	}
	parent := filepath.Dir(artifact.InstallPath)
	var parentInfo unix.Stat_t
	if unix.Lstat(parent, &parentInfo) != nil || parentInfo.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parentInfo.Uid != uint32(os.Getuid()) || parentInfo.Gid != uint32(os.Getgid()) || parentInfo.Mode&0o777 != 0o700 {
		return ErrSimpleArtifactConflict
	}
	if err := writePrivateSynced(artifact.InstallPath, []byte(artifact.Contents), os.FileMode(artifact.Mode)); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	return verifyPRDCatalogArtifact(artifact)
}

func freshPRDCatalogArtifactAllowed(plan PRDUpgradePlan, artifact Artifact) bool {
	if plan.HostID != "linux-host" {
		return false
	}
	switch artifact.Kind {
	case ArtifactEntrypoint:
		return artifact.InstallPath == plan.Namespace.RunnerEntrypoint && artifact.Mode == 0o700 &&
			artifact.Owner == config.LinuxAccount && artifact.Group == config.LinuxAccount
	case ArtifactSSHWrapper:
		return artifact.InstallPath == filepath.Join(plan.Namespace.Binaries, "runner-ssh-bridge-forced.sh") &&
			artifact.Mode == 0o700 && artifact.Owner == config.LinuxAccount && artifact.Group == config.LinuxAccount
	case ArtifactSystemdUnit:
		return artifact.InstallPath == filepath.Join("/etc/systemd/system", plan.Namespace.RunnerUnit) &&
			artifact.Mode == 0o644 && artifact.Owner == "root" && artifact.Group == "root"
	default:
		return false
	}
}

func restorePRDLegacyServiceArtifacts(ctx context.Context, plan PRDUpgradePlan, service *PRDServiceController, backup PRDBackupManifest) error {
	if validatePRDPlan(plan) != nil || service == nil || backup.HostID != plan.HostID {
		return ErrPRDUpgradeGuard
	}
	artifacts, err := BuildPRDCatalogServiceArtifacts(plan.HostID)
	if err != nil {
		return err
	}
	if err := restorePRDServiceArtifactsFromBackup(ctx, service, artifacts, backup); err != nil {
		return err
	}
	if plan.Namespace.Host == config.HostKindLinux {
		return service.base.reloadSystemd(ctx)
	}
	return nil
}

func restorePRDServiceArtifactsFromBackup(ctx context.Context, service *PRDServiceController, artifacts []Artifact,
	backup PRDBackupManifest) error {
	if service == nil || !filepath.IsAbs(backup.DatabaseBackupPath) || filepath.Clean(backup.DatabaseBackupPath) != backup.DatabaseBackupPath {
		return ErrPRDUpgradeGuard
	}
	backupRoot := filepath.Dir(backup.DatabaseBackupPath)
	for _, artifact := range artifacts {
		file, ok := findPRDBackupFile(backup, artifact.InstallPath)
		backupPath := filepath.Join(backupRoot, file.BackupPath)
		if !ok || !pathContained(backupRoot, backupPath) || filepath.Clean(backupPath) != backupPath {
			return ErrPRDUpgradeGuard
		}
		contents, err := os.ReadFile(backupPath)
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

const atomicPRDSystemdCreateScriptTemplate = `set -eu
target=$1
source=$2
new_sha=$3
directory=${target%/*}
[ "$target" = @TARGET@ ] && [ "$directory" = @DIRECTORY@ ] || exit 74
[ -f "$source" ] && [ ! -L "$source" ] && [ "$(stat -c '%U:%a' "$source")" = @SOURCE_OWNER@:600 ] || exit 74
[ "$(sha256sum -- "$source" | cut -d ' ' -f 1)" = "$new_sha" ] || exit 74
if [ -e "$target" ] || [ -L "$target" ]; then
	[ -f "$target" ] && [ ! -L "$target" ] && [ "$(stat -c '%U:%a' "$target")" = @TARGET_OWNER@:644 ] &&
		[ "$(sha256sum -- "$target" | cut -d ' ' -f 1)" = "$new_sha" ] || exit 73
	for stale in "$directory"/.runner-prd-catalog-unit-"$new_sha".*; do
		[ -e "$stale" ] || continue
		if [ -f "$stale" ] && [ ! -L "$stale" ] && [ "$(stat -c '%U:%a' "$stale")" = @TARGET_OWNER@:644 ] &&
			[ "$(sha256sum -- "$stale" | cut -d ' ' -f 1)" = "$new_sha" ]; then
			rm -f -- "$stale"
		fi
	done
	sync -f "$directory"
	exit 0
fi
for stale in "$directory"/.runner-prd-catalog-unit-"$new_sha".*; do
	[ -e "$stale" ] || continue
	if [ -f "$stale" ] && [ ! -L "$stale" ] && [ "$(stat -c '%U:%a' "$stale")" = @TARGET_OWNER@:644 ] &&
		[ "$(sha256sum -- "$stale" | cut -d ' ' -f 1)" = "$new_sha" ]; then
		rm -f -- "$stale"
	fi
done
temporary=$(mktemp "$directory/.runner-prd-catalog-unit-$new_sha.XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
install -o @TARGET_OWNER@ -g @TARGET_GROUP@ -m 0644 -- "$source" "$temporary"
[ "$(sha256sum -- "$temporary" | cut -d ' ' -f 1)" = "$new_sha" ] || exit 74
ln -- "$temporary" "$target"
rm -f -- "$temporary"
sync -f "$directory"
`

var atomicPRDSystemdCreateScript = renderAtomicPRDSystemdCreateScript(
	"/etc/systemd/system/runnerd.service", "ubuntu", "root", "root")

func renderAtomicPRDSystemdCreateScript(targetPath, sourceOwner, targetOwner, targetGroup string) string {
	directory := filepath.Dir(targetPath)
	return strings.NewReplacer(
		"@TARGET@", shellQuote(targetPath),
		"@DIRECTORY@", shellQuote(directory),
		"@SOURCE_OWNER@", shellQuote(sourceOwner),
		"@TARGET_OWNER@", shellQuote(targetOwner),
		"@TARGET_GROUP@", shellQuote(targetGroup),
	).Replace(atomicPRDSystemdCreateScriptTemplate)
}

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
