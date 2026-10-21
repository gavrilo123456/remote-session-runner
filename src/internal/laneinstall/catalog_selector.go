package laneinstall

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

var (
	ErrCatalogSelectorConflict  = errors.New("catalog selector changed from its expected predecessor")
	ErrCatalogOperationInvalid  = errors.New("invalid or inconsistent catalog selector operation")
	ErrCatalogRollbackUnsafe    = errors.New("catalog binary rollback is incompatible with current data or configuration")
	ErrCatalogInsufficientSpace = errors.New("insufficient free space for the catalog selector operation")
)

const catalogOperationFormatVersion = 1

type catalogOperationStage string

const (
	catalogOperationPrepared        catalogOperationStage = "prepared"
	catalogOperationSwitched        catalogOperationStage = "switched"
	catalogOperationComplete        catalogOperationStage = "complete"
	catalogOperationRollbackPrepare catalogOperationStage = "rollback_prepared"
	catalogOperationRolledBack      catalogOperationStage = "rolled_back"
	catalogSelectorAfterPrepared    string                = "prepared_record_durable"
	catalogSelectorAfterSwitch      string                = "selector_switched_before_record"
)

// CatalogSelectorStatus reports the validated selector target without
// changing the selected lane. Target is the exact symlink payload; catalog
// releases additionally include their sealed identity.
type CatalogSelectorStatus struct {
	HostID  string                `json:"host_id"`
	Lane    config.ReleaseLane    `json:"lane"`
	Target  string                `json:"target,omitempty"`
	Kind    string                `json:"kind"`
	Bundle  SimpleBundleManifest  `json:"bundle_manifest,omitempty"`
	Release *CatalogReleaseStatus `json:"catalog_release,omitempty"`
}

// CatalogSelectorOperation is the durable, host-local record used to
// reconcile a selector switch after interruption. Its fields are exported for
// evidence/status serialization; callers cannot fabricate a valid operation
// because every effect re-reads and validates the protected record.
type CatalogSelectorOperation struct {
	FormatVersion                int                    `json:"format_version"`
	OperationID                  string                 `json:"operation_id"`
	CohortOperationID            string                 `json:"cohort_operation_id,omitempty"`
	CohortEvidenceSHA256         string                 `json:"cohort_evidence_sha256,omitempty"`
	HostID                       string                 `json:"host_id"`
	Lane                         config.ReleaseLane     `json:"lane"`
	Release                      CatalogReleaseIdentity `json:"release_identity"`
	PreviousTarget               string                 `json:"previous_target,omitempty"`
	NextTarget                   string                 `json:"next_target"`
	ConfigurationSHA256          string                 `json:"configuration_sha256"`
	DatabaseSchemaAtSwitch       int                    `json:"database_schema_at_switch"`
	BackupDirectory              string                 `json:"backup_directory"`
	BackupManifestSHA256         string                 `json:"backup_manifest_sha256"`
	RollbackBackupDirectory      string                 `json:"rollback_backup_directory,omitempty"`
	RollbackBackupManifestSHA256 string                 `json:"rollback_backup_manifest_sha256,omitempty"`
	Stage                        catalogOperationStage  `json:"stage"`
	CreatedUTC                   string                 `json:"created_utc"`
}

type catalogSelectorLayout struct {
	serviceRoot   string
	catalogRoot   string
	operationRoot string
	backupRoot    string
	lifecycleLock string
	configFile    string
	databasePath  string
	hostID        string
	lane          config.ReleaseLane
}

type catalogBackupReference struct {
	Directory            string
	ManifestSHA256       string
	CohortOperationID    string
	CohortEvidenceSHA256 string
}

// CatalogLaneMaintenance holds the catalog lock before the existing selected-
// lane lifecycle lock. It requires the same stopped, empty restart inventory
// and verified backup as other lane updates. CAT-03 adds the four-host cohort
// hold required before any real selector change is scheduled.
type CatalogLaneMaintenance struct {
	layout      catalogSelectorLayout
	maintenance *SimpleLaneMaintenance
	catalogFD   int
	closed      bool
}

func catalogSelectorLayoutForPlan(plan Plan, catalogRoot string) (catalogSelectorLayout, error) {
	if validateCatalogPlan(plan) != nil || !filepath.IsAbs(catalogRoot) || filepath.Clean(catalogRoot) != catalogRoot {
		return catalogSelectorLayout{}, ErrInvalidCatalogRelease
	}
	expectedRoot, err := DefaultCatalogRootForPlan(plan)
	if err != nil || catalogRoot != expectedRoot {
		return catalogSelectorLayout{}, ErrInvalidCatalogRelease
	}
	return catalogSelectorLayout{
		serviceRoot: plan.Namespace.ServiceRoot, catalogRoot: catalogRoot,
		operationRoot: filepath.Join(plan.Namespace.Staging, "catalog-operations"),
		backupRoot:    plan.Namespace.Backups, lifecycleLock: plan.Namespace.LifecycleLock,
		configFile: plan.Namespace.ConfigFile, databasePath: plan.Namespace.Database,
		hostID: plan.HostID, lane: plan.Lane,
	}, nil
}

// DefaultCatalogRootForPlan derives the fixed per-account catalog directory.
// It does not create the directory or mutate any lane state.
func DefaultCatalogRootForPlan(plan Plan) (string, error) {
	if validateCatalogPlan(plan) != nil {
		return "", ErrInvalidCatalogRelease
	}
	home := filepath.Join("/home", plan.Namespace.Account)
	if plan.Namespace.Host == config.HostKindMac {
		home = filepath.Join("/Users", plan.Namespace.Account)
	}
	return filepath.Join(home, "projects-runtime", "runner-releases"), nil
}

// BeginCatalogLaneMaintenance acquires the shared catalog lock first, then
// uses the existing supported service stop and lane quiescence procedure.
// The caller must capture a verified backup before selecting or rolling back.
func BeginCatalogLaneMaintenance(ctx context.Context, plan Plan, service SimpleServiceProbe) (*CatalogLaneMaintenance, error) {
	if validateSimplePlan(plan) != nil {
		return nil, ErrInvalidCatalogRelease
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return nil, err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return nil, err
	}
	catalogFD, err := acquireCatalogRootPublicationLock(root)
	if err != nil {
		return nil, err
	}
	maintenance, err := BeginSimpleLaneMaintenance(ctx, plan, service)
	if err != nil {
		releaseCatalogRootPublicationLock(catalogFD)
		return nil, err
	}
	if maintenance.Fresh() {
		_ = maintenance.Close()
		releaseCatalogRootPublicationLock(catalogFD)
		return nil, ErrSimpleBackupRequired
	}
	return &CatalogLaneMaintenance{layout: layout, maintenance: maintenance, catalogFD: catalogFD}, nil
}

func (m *CatalogLaneMaintenance) Backup(ctx context.Context) (SimpleLaneBackupReceipt, error) {
	if m == nil || m.closed || m.maintenance == nil {
		return SimpleLaneBackupReceipt{}, ErrSimpleBackupRequired
	}
	return m.maintenance.Backup(ctx)
}

type CatalogCohortHostPreflightReport struct {
	Preflight     CatalogCohortHostPreflight `json:"preflight"`
	WorkInventory CatalogWorkInventory       `json:"work_inventory"`
}

// CatalogWorkInventoryReport records the read-only lane, selector and
// restart-guard measurements captured before a selected-service stop.
type CatalogWorkInventoryReport struct {
	HostID              string               `json:"host_id"`
	Lane                config.ReleaseLane   `json:"lane"`
	ObservedAt          string               `json:"observed_at"`
	ServiceActive       bool                 `json:"service_active"`
	SelectorTarget      string               `json:"selector_target"`
	BundleSHA256        string               `json:"bundle_sha256"`
	ConfigurationSHA256 string               `json:"configuration_sha256"`
	DatabaseSchema      int                  `json:"database_schema"`
	WorkInventory       CatalogWorkInventory `json:"work_inventory"`
	WorkInventoryEmpty  bool                 `json:"work_inventory_empty"`
}

// ReadCatalogWorkInventory returns a read-only snapshot of the selected
// Dev/QAS authority and service state. It includes resumable jobs and does not
// require a maintenance stop; the guarded stop repeats the check under its
// lifecycle lock before any selector effect.
func ReadCatalogWorkInventory(ctx context.Context, plan Plan, service SimpleServiceProbe) (CatalogWorkInventoryReport, error) {
	if validateSimplePlan(plan) != nil || service == nil {
		return CatalogWorkInventoryReport{}, ErrSimpleLanePreflight
	}
	if _, err := loadSimpleLaneAuthority(plan); err != nil {
		return CatalogWorkInventoryReport{}, err
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return CatalogWorkInventoryReport{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return CatalogWorkInventoryReport{}, err
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return CatalogWorkInventoryReport{}, err
	}
	configurationSHA256, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return CatalogWorkInventoryReport{}, err
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return CatalogWorkInventoryReport{}, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return CatalogWorkInventoryReport{}, ErrSimpleLanePreflight
	}
	defer database.Close()
	schema, workInventory, err := readPRDWorkSnapshot(ctx, database)
	if err != nil || schema <= 0 {
		return CatalogWorkInventoryReport{}, ErrSimpleLanePreflight
	}
	bundleSHA256 := status.Bundle.BundleSHA256
	if status.Release != nil {
		bundleSHA256 = status.Release.Identity.BundleSHA256
	}
	return CatalogWorkInventoryReport{
		HostID: plan.HostID, Lane: plan.Lane, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ServiceActive: active, SelectorTarget: status.Target, BundleSHA256: bundleSHA256,
		ConfigurationSHA256: configurationSHA256, DatabaseSchema: schema,
		WorkInventory: workInventory, WorkInventoryEmpty: workInventory.Empty(),
	}, nil
}

// CohortHostPreflight returns the fresh, host-local evidence required by a
// catalog cohort receipt. The selected service must already be stopped by the
// maintenance lease, the complete restart inventory must remain empty, and a
// verified online backup must have been captured through Backup.
func (m *CatalogLaneMaintenance) CohortHostPreflight(ctx context.Context, releaseID string, requiredBytes uint64) (CatalogCohortHostPreflightReport, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh ||
		!validCatalogReleaseID(releaseID) || requiredBytes == 0 || m.maintenance.backup == nil {
		return CatalogCohortHostPreflightReport{}, ErrSimpleBackupRequired
	}
	active, err := m.maintenance.service.Status(ctx, m.maintenance.plan)
	if err != nil || active {
		return CatalogCohortHostPreflightReport{}, ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(m.maintenance.plan)
	if err != nil || current != m.maintenance.proof.current {
		return CatalogCohortHostPreflightReport{}, ErrCatalogSelectorConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.maintenance.plan, current)
	if err != nil || proof.database != m.maintenance.proof.database || !simpleMetricsEmpty(proof.metrics) {
		return CatalogCohortHostPreflightReport{}, ErrSimpleLaneNotQuiescent
	}
	if !simpleBackupReceiptValid(m.maintenance.plan, *m.maintenance.backup, proof) {
		return CatalogCohortHostPreflightReport{}, ErrSimpleBackupRequired
	}
	status, err := inspectCatalogSelector(m.layout)
	if err != nil || catalogSelectorStatusBundleSHA256(status) != current {
		return CatalogCohortHostPreflightReport{}, ErrCatalogSelectorConflict
	}
	target, err := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, releaseID)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	configurationSHA256, err := hashCatalogConfiguration(m.layout.configFile)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, m.maintenance.plan.Namespace.Database)
	if err != nil {
		return CatalogCohortHostPreflightReport{}, ErrSimpleLanePreflight
	}
	workInventory, inventoryErr := readPRDWorkInventory(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil || !workInventory.Empty() {
		return CatalogCohortHostPreflightReport{}, ErrSimpleLaneNotQuiescent
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(m.layout.backupRoot, &stat); err != nil || stat.Bsize < 0 || stat.Bavail < 0 {
		return CatalogCohortHostPreflightReport{}, ErrSimpleLanePreflight
	}
	availableBytes := uint64(stat.Bavail) * uint64(stat.Bsize)
	if availableBytes < requiredBytes {
		return CatalogCohortHostPreflightReport{}, ErrCatalogInsufficientSpace
	}
	previousBundleSHA256 := ""
	previousReadSchemaMax := 0
	if status.Release != nil {
		previousBundleSHA256 = status.Release.Identity.BundleSHA256
		previousReadSchemaMax = status.Release.Database.ReadSchemaMax
	} else {
		previousBundleSHA256 = status.Bundle.BundleSHA256
		previousReadSchemaMax = status.Bundle.DatabaseSchemaMax
	}
	m.maintenance.proof = proof
	return CatalogCohortHostPreflightReport{
		Preflight: CatalogCohortHostPreflight{
			HostID: m.layout.hostID, PreviousTarget: status.Target,
			PreviousBundleSHA256: previousBundleSHA256, PreviousReadSchemaMax: previousReadSchemaMax,
			Target: target.Identity, ConfigurationSHA256: configurationSHA256,
			SchemaVersion: proof.database, WorkInventorySHA256: workInventory.SHA256(),
			WorkInventoryEmpty: true, PreflightAt: time.Now().UTC().Format(time.RFC3339Nano),
			BackupDirectory: m.maintenance.backup.directory, BackupManifestSHA256: m.maintenance.backup.manifestHash,
			AvailableBytes: availableBytes, RequiredBytes: requiredBytes,
		},
		WorkInventory: workInventory,
	}, nil
}

// UseVerifiedBackup attaches the exact host-local backup already bound by a
// cohort preflight. This lets operators collect four backups before freezing
// the shared receipt without silently creating a different, unbound backup
// during selector mutation.
func (m *CatalogLaneMaintenance) UseVerifiedBackup(ctx context.Context, directory, expectedManifestSHA256 string) error {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh ||
		!simpleProofFresh(m.maintenance.proof, m.maintenance.plan) || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		!bundleDigestPattern.MatchString(expectedManifestSHA256) {
		return ErrSimpleBackupRequired
	}
	active, err := m.maintenance.service.Status(ctx, m.maintenance.plan)
	if err != nil || active {
		return ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(m.maintenance.plan)
	if err != nil || current != m.maintenance.proof.current {
		return ErrCatalogSelectorConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.maintenance.plan, current)
	if err != nil || proof.database != m.maintenance.proof.database {
		return ErrSimpleLaneNotQuiescent
	}
	configurationSHA256, configErr := hashCatalogConfiguration(m.layout.configFile)
	manifest, err := readSimpleLaneBackupManifest(m.maintenance.plan, directory)
	if err != nil || backupManifestSHA256(directory) != expectedManifestSHA256 ||
		manifest.HostID != m.maintenance.plan.HostID || manifest.Lane != m.maintenance.plan.Lane ||
		manifest.DatabasePath != m.maintenance.plan.Namespace.Database || manifest.ActiveBundle != current ||
		manifest.DatabaseSchema != proof.database || configErr != nil || manifest.ConfigSHA256 != configurationSHA256 {
		return ErrSimpleBackupRequired
	}
	receipt := &SimpleLaneBackupReceipt{
		hostID: manifest.HostID, lane: manifest.Lane, databasePath: manifest.DatabasePath,
		activeBundle: manifest.ActiveBundle, database: manifest.DatabaseSchema,
		directory: directory, manifestHash: expectedManifestSHA256,
	}
	if !simpleBackupReceiptValid(m.maintenance.plan, *receipt, proof) {
		return ErrSimpleBackupRequired
	}
	m.maintenance.proof = proof
	m.maintenance.backup = receipt
	return nil
}

// Select validates the exact predecessor and sealed local-host release,
// checks the current database schema against the candidate read contract, and
// atomically changes only this lane's selector. It leaves the service stopped
// so the CAT-03 cohort coordinator can keep the whole lane held.
func (m *CatalogLaneMaintenance) Select(ctx context.Context, releaseID, expectedTarget, cohortOperationID, switchEvidenceSHA256 string) (CatalogSelectorOperation, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh ||
		!simpleProofFresh(m.maintenance.proof, m.maintenance.plan) || m.maintenance.backup == nil ||
		!validCatalogOperationID(cohortOperationID) || !bundleDigestPattern.MatchString(switchEvidenceSHA256) {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	active, err := m.maintenance.service.Status(ctx, m.maintenance.plan)
	if err != nil || active {
		return CatalogSelectorOperation{}, ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(m.maintenance.plan)
	if err != nil || current != m.maintenance.proof.current {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.maintenance.plan, current)
	if err != nil || proof.database != m.maintenance.proof.database {
		return CatalogSelectorOperation{}, ErrSimpleLaneNotQuiescent
	}
	m.maintenance.proof = proof
	if !simpleBackupReceiptValid(m.maintenance.plan, *m.maintenance.backup, proof) {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	receipt, err := ReadCatalogCohortReceipt(m.maintenance.plan, cohortOperationID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	if err := CatalogCohortAdmissionHoldMatches(m.maintenance.plan, cohortOperationID); err != nil {
		return CatalogSelectorOperation{}, err
	}
	currentStatus, err := inspectCatalogSelector(m.layout)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	target, err := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, releaseID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	if err := validateCatalogCohortHostMaySwitch(receipt, m.layout, currentStatus.Target, target.Identity, proof.database,
		m.maintenance.backup.directory, m.maintenance.backup.manifestHash, time.Now().UTC()); err != nil {
		return CatalogSelectorOperation{}, err
	}
	backupRef := catalogBackupReference{Directory: m.maintenance.backup.directory, ManifestSHA256: m.maintenance.backup.manifestHash,
		CohortOperationID: cohortOperationID, CohortEvidenceSHA256: switchEvidenceSHA256}
	operation, err := selectCatalogReleaseLocked(m.layout, releaseID, expectedTarget, proof.database, backupRef, nil)
	if err != nil {
		return operation, err
	}
	if err := recordCatalogCohortSwitch(m.maintenance.plan, operation, false); err != nil {
		return operation, err
	}
	_ = receipt
	return operation, nil
}

// Reconcile completes only the recorded selector transition after confirming
// the service is stopped and its restart inventory is still empty.
func (m *CatalogLaneMaintenance) Reconcile(ctx context.Context, operationID string) (CatalogSelectorOperation, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh || !simpleProofFresh(m.maintenance.proof, m.maintenance.plan) {
		return CatalogSelectorOperation{}, ErrSimpleLanePreflight
	}
	active, err := m.maintenance.service.Status(ctx, m.maintenance.plan)
	if err != nil || active {
		return CatalogSelectorOperation{}, ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(m.maintenance.plan)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.maintenance.plan, current)
	if err != nil || proof.database != m.maintenance.proof.database {
		return CatalogSelectorOperation{}, ErrSimpleLaneNotQuiescent
	}
	m.maintenance.proof = proof
	backup, err := m.maintenance.Backup(ctx)
	if err != nil {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	if !simpleBackupReceiptValid(m.maintenance.plan, backup, proof) {
		m.maintenance.backup = nil
		backup, err = m.maintenance.Backup(ctx)
		if err != nil || !simpleBackupReceiptValid(m.maintenance.plan, backup, proof) {
			return CatalogSelectorOperation{}, ErrSimpleBackupRequired
		}
	}
	operation, err := readCatalogSelectorOperation(m.layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(m.layout, operation) != nil {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	if _, err := readSimpleLaneBackupManifest(m.maintenance.plan, operation.BackupDirectory); err != nil || backupManifestSHA256(operation.BackupDirectory) != operation.BackupManifestSHA256 {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	if operation.RollbackBackupDirectory != "" {
		if _, err := readSimpleLaneBackupManifest(m.maintenance.plan, operation.RollbackBackupDirectory); err != nil || backupManifestSHA256(operation.RollbackBackupDirectory) != operation.RollbackBackupManifestSHA256 {
			return CatalogSelectorOperation{}, ErrSimpleBackupRequired
		}
	}
	if operation.Stage == catalogOperationRollbackPrepare {
		previous, resolveErr := resolveCatalogSelectorTarget(m.layout, operation.PreviousTarget)
		if resolveErr != nil || !catalogDatabaseSupportsSchema(previous, proof.database) {
			return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
		}
	} else {
		release, inspectErr := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, operation.Release.ReleaseID)
		if inspectErr != nil || proof.database < release.Database.ReadSchemaMin || proof.database > release.Database.ReadSchemaMax {
			return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
		}
	}
	completed, err := reconcileCatalogSelectorLocked(m.layout, operationID)
	if err != nil {
		return completed, err
	}
	if completed.CohortOperationID != "" {
		if completed.Stage == catalogOperationRolledBack {
			err = recordCatalogCohortRollback(m.maintenance.plan, completed)
		} else {
			err = recordCatalogCohortSwitch(m.maintenance.plan, completed, true)
		}
		if err != nil {
			return completed, err
		}
	}
	return completed, nil
}

// Rollback selects the recorded predecessor only when the current database is
// inside that release's read range and the lane configuration still has the
// exact digest captured at promotion. It preserves current database contents.
func (m *CatalogLaneMaintenance) Rollback(ctx context.Context, operationID string) (CatalogSelectorOperation, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh || !simpleProofFresh(m.maintenance.proof, m.maintenance.plan) {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	operation, err := readCatalogSelectorOperation(m.layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(m.layout, operation) != nil || operation.CohortOperationID == "" {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	receipt, err := ReadCatalogCohortReceipt(m.maintenance.plan, operation.CohortOperationID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	currentStatus, err := inspectCatalogSelector(m.layout)
	if err != nil || validateCatalogCohortHostMayRollback(receipt, m.layout, operation, currentStatus.Target) != nil {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	active, err := m.maintenance.service.Status(ctx, m.maintenance.plan)
	if err != nil || active {
		return CatalogSelectorOperation{}, ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(m.maintenance.plan)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.maintenance.plan, current)
	if err != nil || proof.database != m.maintenance.proof.database {
		return CatalogSelectorOperation{}, ErrSimpleLaneNotQuiescent
	}
	m.maintenance.proof = proof
	m.maintenance.backup = nil
	backup, err := m.maintenance.Backup(ctx)
	if err != nil || !simpleBackupReceiptValid(m.maintenance.plan, backup, proof) {
		return CatalogSelectorOperation{}, ErrSimpleBackupRequired
	}
	backupRef := catalogBackupReference{Directory: backup.directory, ManifestSHA256: backup.manifestHash,
		CohortOperationID: operation.CohortOperationID}
	rolledBack, err := rollbackCatalogSelectorLocked(m.layout, operationID, proof.database, backupRef, nil)
	if err != nil {
		return rolledBack, err
	}
	if err := recordCatalogCohortRollback(m.maintenance.plan, rolledBack); err != nil {
		return rolledBack, err
	}
	return rolledBack, nil
}

// Close releases the lane lock before the catalog lock, preserving the global
// catalog-then-lane acquisition order.
func (m *CatalogLaneMaintenance) Close() error {
	if m == nil || m.closed {
		return nil
	}
	m.closed = true
	var maintenanceErr error
	if m.maintenance != nil {
		maintenanceErr = m.maintenance.Close()
	}
	releaseCatalogRootPublicationLock(m.catalogFD)
	return maintenanceErr
}

func selectedSimpleBundleForPlan(plan Plan) (string, SimpleBundle, error) {
	catalogRoot, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return "", SimpleBundle{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, catalogRoot)
	if err != nil {
		return "", SimpleBundle{}, err
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil {
		return "", SimpleBundle{}, err
	}
	if status.Kind == "unselected" {
		return "", SimpleBundle{}, nil
	}
	selection, err := resolveCatalogSelectorTarget(layout, status.Target)
	if err != nil {
		return "", SimpleBundle{}, err
	}
	return status.Target, selection.bundle, nil
}

func simpleCurrentBundleForPlan(plan Plan) (string, error) {
	_, bundle, err := selectedSimpleBundleForPlan(plan)
	if err != nil || bundle.Manifest.BundleSHA256 == "" {
		return "", err
	}
	return bundle.Manifest.BundleSHA256, nil
}

func catalogSelectorStatusBundleSHA256(status CatalogSelectorStatus) string {
	switch status.Kind {
	case "legacy":
		return status.Bundle.BundleSHA256
	case "catalog":
		if status.Release != nil {
			return status.Release.Identity.BundleSHA256
		}
	}
	return ""
}

// InspectCatalogSelectorForPlan validates either the supported legacy
// releases/<digest> selector or a canonical catalog selector. It is
// read-only, so it can be used by status paths while a lane is active.
func InspectCatalogSelectorForPlan(plan Plan, catalogRoot string) (CatalogSelectorStatus, error) {
	layout, err := catalogSelectorLayoutForPlan(plan, catalogRoot)
	if err != nil {
		return CatalogSelectorStatus{}, err
	}
	return inspectCatalogSelector(layout)
}

// InspectCatalogSelectorOperationForPlan returns a validated durable selector
// operation record for CLI-level cohort binding. It has no side effects.
func InspectCatalogSelectorOperationForPlan(plan Plan, operationID string) (CatalogSelectorOperation, error) {
	if validateCatalogPlan(plan) != nil || !validCatalogOperationID(operationID) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	operation, err := readCatalogSelectorOperation(layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(layout, operation) != nil {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	return operation, nil
}

// BuildCatalogPlan derives the fixed host/lane namespace used to inspect a
// catalog selector. PRD is accepted for read-only inspection and startup
// identity validation; its live maintenance continues through the separate
// guarded PRD procedure.
func BuildCatalogPlan(hostID string, lane config.ReleaseLane) (Plan, error) {
	if lane == config.ReleaseLaneDev || lane == config.ReleaseLaneQAS {
		return BuildSimplePlan(hostID, lane)
	}
	if lane != config.ReleaseLanePRD {
		return Plan{}, ErrInvalidCatalogRelease
	}
	prd, err := BuildPRDUpgradePlan(hostID)
	if err != nil {
		return Plan{}, err
	}
	return Plan{HostID: hostID, Lane: config.ReleaseLanePRD, Namespace: prd.Namespace}, nil
}

// ValidateSelectedCatalogExecutableForPlan verifies that the running service
// binary is the exact component selected by this lane's sealed catalog bundle.
// Legacy selectors remain supported without changing their startup behavior.
func ValidateSelectedCatalogExecutableForPlan(plan Plan, component, executablePath string) error {
	if validateCatalogPlan(plan) != nil || component == "" || filepath.Base(component) != component ||
		!filepath.IsAbs(executablePath) || filepath.Clean(executablePath) != executablePath {
		return ErrInvalidCatalogRelease
	}
	wanted := false
	for _, name := range simpleHostComponents[plan.HostID] {
		if name == component {
			wanted = true
			break
		}
	}
	if !wanted {
		return ErrInvalidCatalogRelease
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return err
	}
	status, err := InspectCatalogSelectorForPlan(plan, root)
	if err != nil {
		return err
	}
	if status.Kind != "catalog" {
		return nil
	}
	return validateCatalogExecutablePath(filepath.Join(status.Target, "bin", component), executablePath)
}

// ValidateConfiguredCatalogExecutable binds a service's validated V3 lane
// contract to its executable before the service opens writable runtime state.
// Legacy PRD configurations and operations without lane profiles are unchanged.
func ValidateConfiguredCatalogExecutable(configuration config.Config, component, executablePath string) error {
	profiles, configured := configuration.LaneProfiles()
	if !configured {
		plan, selected, err := CatalogPlanFromConfiguration(configuration)
		if err != nil {
			return err
		}
		if !selected {
			return nil
		}
		return ValidateSelectedCatalogExecutableForPlan(plan, component, executablePath)
	}
	hostID := ""
	switch profiles.Host() {
	case config.HostKindMac:
		hostID = "mac-control-plane"
	case config.HostKindLinux:
		hostID = profiles.RemoteProfile()
	default:
		return ErrInvalidCatalogRelease
	}
	plan, err := BuildCatalogPlan(hostID, profiles.Lane())
	if err != nil {
		return err
	}
	return ValidateSelectedCatalogExecutableForPlan(plan, component, executablePath)
}

// CatalogPlanFromConfiguration derives a lane identity for installed services.
// SIMPLE Dev/QAS configurations carry the host/profile explicitly. Existing
// unsuffixed PRD configuration stays byte-for-byte unchanged; once its
// selector targets the canonical catalog, the host identity is recovered
// from that validated selector and checked against the configured service
// root and local platform.
func CatalogPlanFromConfiguration(configuration config.Config) (Plan, bool, error) {
	if profiles, configured := configuration.LaneProfiles(); configured {
		hostID := ""
		switch profiles.Host() {
		case config.HostKindMac:
			hostID = "mac-control-plane"
		case config.HostKindLinux:
			hostID = profiles.RemoteProfile()
		default:
			return Plan{}, false, ErrInvalidCatalogRelease
		}
		plan, err := BuildCatalogPlan(hostID, profiles.Lane())
		return plan, err == nil, err
	}
	if configuration.ReleaseLane() != config.ReleaseLanePRD {
		return Plan{}, false, nil
	}
	serviceRoot := ""
	if settings, ok := configuration.MacSettings(); ok {
		serviceRoot = settings.ServiceRoot
	} else if settings, ok := configuration.LinuxSettings(); ok {
		serviceRoot = settings.ServiceRoot
	}
	if serviceRoot == "" || configuration.ReleaseLane() != config.ReleaseLanePRD {
		return Plan{}, false, nil
	}
	hostID, err := configuredPRDHostID(configuration)
	if err != nil {
		return Plan{}, false, err
	}
	plan, err := BuildCatalogPlan(hostID, config.ReleaseLanePRD)
	if err != nil || plan.Namespace.ServiceRoot != serviceRoot {
		return Plan{}, false, ErrInvalidCatalogRelease
	}
	currentPath := filepath.Join(serviceRoot, "current")
	if _, err := os.Lstat(currentPath); errors.Is(err, os.ErrNotExist) {
		held, _, holdErr := CatalogCohortAdmissionHeld(plan)
		if holdErr != nil {
			return Plan{}, false, ErrInvalidCatalogRelease
		}
		return plan, held, nil
	} else if err != nil {
		return Plan{}, false, ErrInvalidCatalogRelease
	}
	target, err := os.Readlink(currentPath)
	if err != nil || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return Plan{}, false, ErrInvalidCatalogRelease
	}
	if !containsCatalogHost(hostID) || !CatalogHostMatchesPlatform(hostID, runtime.GOOS, runtime.GOARCH) || filepath.Base(target) != hostID {
		return Plan{}, false, ErrInvalidCatalogRelease
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return Plan{}, false, err
	}
	status, err := InspectCatalogSelectorForPlan(plan, root)
	if err != nil || status.Kind != "catalog" || status.Target != target {
		return Plan{}, false, ErrInvalidCatalogRelease
	}
	return plan, true, nil
}

func configuredPRDHostID(configuration config.Config) (string, error) {
	if _, ok := configuration.MacSettings(); ok && configuration.Kind() == config.HostKindMac {
		if !CatalogHostMatchesPlatform("mac-control-plane", runtime.GOOS, runtime.GOARCH) {
			return "", ErrInvalidCatalogRelease
		}
		return "mac-control-plane", nil
	}
	settings, ok := configuration.LinuxSettings()
	if !ok || configuration.Kind() != config.HostKindLinux {
		return "", ErrInvalidCatalogRelease
	}
	matched := ""
	for _, hostID := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		plan, err := BuildPRDUpgradePlan(hostID)
		if err != nil {
			continue
		}
		if settings.ServiceRoot == plan.Namespace.ServiceRoot && settings.Database == plan.Namespace.Database &&
			settings.DirectHTTPSBind == plan.Namespace.DirectHTTPSBind && settings.DirectPublicEndpoint == plan.Namespace.DirectEndpointURL {
			if matched != "" {
				return "", ErrInvalidCatalogRelease
			}
			matched = hostID
		}
	}
	if matched == "" || !CatalogHostMatchesPlatform(matched, runtime.GOOS, runtime.GOARCH) {
		return "", ErrInvalidCatalogRelease
	}
	return matched, nil
}

func validateCatalogExecutablePath(selectedPath, executablePath string) error {
	selected, err := filepath.EvalSymlinks(selectedPath)
	if err != nil {
		return ErrInvalidCatalogRelease
	}
	actual, err := filepath.EvalSymlinks(executablePath)
	if err != nil || actual != selected {
		return ErrInvalidCatalogRelease
	}
	return nil
}

func inspectCatalogSelector(layout catalogSelectorLayout) (CatalogSelectorStatus, error) {
	if err := validateCatalogSelectorLayout(layout); err != nil {
		return CatalogSelectorStatus{}, err
	}
	if err := validateCatalogRootPath(layout.serviceRoot); err != nil {
		return CatalogSelectorStatus{}, err
	}
	if err := validateExistingPrivateDirectory(layout.serviceRoot); err != nil {
		return CatalogSelectorStatus{}, err
	}
	status := CatalogSelectorStatus{HostID: layout.hostID, Lane: layout.lane, Kind: "unselected"}
	current := filepath.Join(layout.serviceRoot, "current")
	info, err := os.Lstat(current)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	var linkStat unix.Stat_t
	if err != nil || info.Mode()&os.ModeSymlink == 0 || unix.Lstat(current, &linkStat) != nil || linkStat.Uid != uint32(os.Getuid()) {
		return CatalogSelectorStatus{}, ErrInvalidSimpleBundle
	}
	target, err := os.Readlink(current)
	if err != nil || filepath.Clean(target) != target || target == "" {
		return CatalogSelectorStatus{}, ErrInvalidSimpleBundle
	}
	selected, err := resolveCatalogSelectorTarget(layout, target)
	if err != nil {
		return CatalogSelectorStatus{}, err
	}
	status.Target = target
	status.Kind = selected.kind
	status.Bundle = selected.bundle.Manifest
	status.Release = selected.release
	return status, nil
}

type resolvedCatalogSelection struct {
	kind    string
	bundle  SimpleBundle
	release *CatalogReleaseStatus
}

func resolveCatalogSelectorTarget(layout catalogSelectorLayout, target string) (resolvedCatalogSelection, error) {
	if filepath.IsAbs(target) {
		parts := strings.Split(filepath.Clean(target), string(filepath.Separator))
		if len(parts) < 4 || filepath.Base(target) != layout.hostID {
			return resolvedCatalogSelection{}, ErrInvalidCatalogRelease
		}
		releaseID := filepath.Base(filepath.Dir(target))
		want := filepath.Join(layout.catalogRoot, releaseID, layout.hostID)
		if target != want || !validCatalogReleaseID(releaseID) {
			return resolvedCatalogSelection{}, ErrInvalidCatalogRelease
		}
		status, err := InspectCatalogRelease(layout.catalogRoot, layout.hostID, releaseID)
		if err != nil {
			return resolvedCatalogSelection{}, err
		}
		bundlePath := filepath.Join(layout.catalogRoot, releaseID, layout.hostID)
		bundle, err := readSimpleBundleAtWithModes(bundlePath, 0o500, 0o400, 0o500, 0o500)
		if err != nil || bundle.Manifest.BundleSHA256 != status.Identity.BundleSHA256 {
			return resolvedCatalogSelection{}, ErrInvalidCatalogRelease
		}
		return resolvedCatalogSelection{kind: "catalog", bundle: bundle, release: &status}, nil
	}
	if filepath.Dir(target) != "releases" || !bundleDigestPattern.MatchString(filepath.Base(target)) {
		return resolvedCatalogSelection{}, ErrInvalidSimpleBundle
	}
	bundle, err := readSimpleBundleAt(filepath.Join(layout.serviceRoot, target))
	if err != nil || bundle.Manifest.HostID != layout.hostID || bundle.Manifest.BundleSHA256 != filepath.Base(target) {
		return resolvedCatalogSelection{}, ErrInvalidSimpleBundle
	}
	return resolvedCatalogSelection{kind: "legacy", bundle: bundle}, nil
}

func validateCatalogSelectorLayout(layout catalogSelectorLayout) error {
	if !containsCatalogHost(layout.hostID) || (layout.lane != config.ReleaseLaneDev && layout.lane != config.ReleaseLaneQAS && layout.lane != config.ReleaseLanePRD) {
		return ErrInvalidCatalogRelease
	}
	for _, path := range []string{layout.serviceRoot, layout.catalogRoot, layout.operationRoot, layout.backupRoot, layout.lifecycleLock, layout.configFile, layout.databasePath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return ErrInvalidCatalogRelease
		}
	}
	if filepath.Dir(layout.operationRoot) != filepath.Join(layout.serviceRoot, "staging") ||
		filepath.Dir(layout.backupRoot) != layout.serviceRoot || filepath.Dir(layout.lifecycleLock) != filepath.Join(layout.serviceRoot, "run") ||
		filepath.Dir(layout.configFile) != filepath.Join(layout.serviceRoot, "config") || filepath.Dir(layout.databasePath) != filepath.Join(layout.serviceRoot, "state") {
		return ErrInvalidCatalogRelease
	}
	return nil
}

func validateCatalogPlan(plan Plan) error {
	if plan.Lane == config.ReleaseLaneDev || plan.Lane == config.ReleaseLaneQAS {
		return validateSimplePlan(plan)
	}
	if plan.Lane != config.ReleaseLanePRD {
		return ErrInvalidCatalogRelease
	}
	expected, err := BuildCatalogPlan(plan.HostID, config.ReleaseLanePRD)
	if err != nil || expected.Namespace != plan.Namespace {
		return ErrInvalidCatalogRelease
	}
	return nil
}

func validateCatalogMutationLayout(layout catalogSelectorLayout) error {
	if layout.lane != config.ReleaseLaneDev && layout.lane != config.ReleaseLaneQAS {
		return ErrInvalidCatalogRelease
	}
	return validateCatalogSelectorLayout(layout)
}

// validateCatalogTransitionLayout is used only after the caller has entered
// the appropriate lane-specific maintenance lease. The public Dev/QAS
// mutation entry points continue to use validateCatalogMutationLayout; PRD
// transitions are reached only through CatalogPRDMaintenance.
func validateCatalogTransitionLayout(layout catalogSelectorLayout) error {
	if layout.lane != config.ReleaseLaneDev && layout.lane != config.ReleaseLaneQAS && layout.lane != config.ReleaseLanePRD {
		return ErrInvalidCatalogRelease
	}
	return validateCatalogSelectorLayout(layout)
}

// selectCatalogReleaseAt performs a local selector transaction. Callers that
// activate a real lane must first hold the whole-lane cohort receipt added by
// CAT-03; this primitive is intentionally not exposed as a standalone live
// CLI operation in CAT-02.
func selectCatalogReleaseAt(layout catalogSelectorLayout, releaseID, expectedTarget string, databaseSchema int, backup catalogBackupReference, interruption func(string) error) (CatalogSelectorOperation, error) {
	if validateCatalogMutationLayout(layout) != nil || !validCatalogReleaseID(releaseID) || databaseSchema <= 0 {
		return CatalogSelectorOperation{}, ErrInvalidCatalogRelease
	}
	locks, err := acquireCatalogSelectorLocks(layout)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	defer releaseCatalogSelectorLocks(locks)
	return selectCatalogReleaseLocked(layout, releaseID, expectedTarget, databaseSchema, backup, interruption)
}

func selectCatalogReleaseLocked(layout catalogSelectorLayout, releaseID, expectedTarget string, databaseSchema int, backup catalogBackupReference, interruption func(string) error) (CatalogSelectorOperation, error) {
	if validateCatalogTransitionLayout(layout) != nil || !validCatalogReleaseID(releaseID) || databaseSchema <= 0 {
		return CatalogSelectorOperation{}, ErrInvalidCatalogRelease
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Target != expectedTarget {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	if current.Kind == "catalog" && current.Release != nil && current.Release.Identity.ReleaseID == releaseID {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	target, err := InspectCatalogRelease(layout.catalogRoot, layout.hostID, releaseID)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	if databaseSchema < target.Database.ReadSchemaMin || databaseSchema > target.Database.ReadSchemaMax {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	if err := validateCatalogBackupReference(layout, backup, current.Target, databaseSchema); err != nil {
		return CatalogSelectorOperation{}, err
	}
	configurationHash, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	operationID, err := newCatalogOperationID()
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	nextTarget := filepath.Join(layout.catalogRoot, releaseID, layout.hostID)
	operation := CatalogSelectorOperation{
		FormatVersion: catalogOperationFormatVersion, OperationID: operationID,
		CohortOperationID:    backup.CohortOperationID,
		CohortEvidenceSHA256: backup.CohortEvidenceSHA256,
		HostID:               layout.hostID, Lane: layout.lane, Release: target.Identity,
		PreviousTarget: current.Target, NextTarget: nextTarget,
		ConfigurationSHA256: configurationHash, DatabaseSchemaAtSwitch: databaseSchema,
		BackupDirectory: backup.Directory, BackupManifestSHA256: backup.ManifestSHA256,
		Stage: catalogOperationPrepared, CreatedUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return CatalogSelectorOperation{}, err
	}
	if interruption != nil {
		if err := interruption(catalogSelectorAfterPrepared); err != nil {
			return operation, err
		}
	}
	if err := switchCatalogSelector(layout, operation.PreviousTarget, operation.NextTarget); err != nil {
		return operation, err
	}
	if interruption != nil {
		if err := interruption(catalogSelectorAfterSwitch); err != nil {
			return operation, err
		}
	}
	operation.Stage = catalogOperationSwitched
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return operation, err
	}
	operation.Stage = catalogOperationComplete
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return operation, err
	}
	return operation, nil
}

// reconcileCatalogSelectorOperation uses the durable operation record and the
// actual current link as authority after interruption. It never guesses when
// the selector points to an identity outside the recorded predecessor/target.
func reconcileCatalogSelectorOperation(layout catalogSelectorLayout, operationID string) (CatalogSelectorOperation, error) {
	if validateCatalogMutationLayout(layout) != nil || !validCatalogOperationID(operationID) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	locks, err := acquireCatalogSelectorLocks(layout)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	defer releaseCatalogSelectorLocks(locks)
	return reconcileCatalogSelectorLocked(layout, operationID)
}

func reconcileCatalogSelectorLocked(layout catalogSelectorLayout, operationID string) (CatalogSelectorOperation, error) {
	if validateCatalogTransitionLayout(layout) != nil || !validCatalogOperationID(operationID) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	operation, err := readCatalogSelectorOperation(layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(layout, operation) != nil {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	configurationHash, err := hashCatalogConfiguration(layout.configFile)
	if err != nil || configurationHash != operation.ConfigurationSHA256 {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	switch operation.Stage {
	case catalogOperationPrepared:
		if current.Target == operation.PreviousTarget {
			if _, err := InspectCatalogRelease(layout.catalogRoot, layout.hostID, operation.Release.ReleaseID); err != nil {
				return CatalogSelectorOperation{}, err
			}
			if err := switchCatalogSelector(layout, operation.PreviousTarget, operation.NextTarget); err != nil {
				return CatalogSelectorOperation{}, err
			}
		} else if current.Target != operation.NextTarget {
			return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
		}
		operation.Stage = catalogOperationSwitched
		if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
			return CatalogSelectorOperation{}, err
		}
		fallthrough
	case catalogOperationSwitched:
		current, err = inspectCatalogSelector(layout)
		if err != nil || current.Target != operation.NextTarget {
			return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
		}
		operation.Stage = catalogOperationComplete
		if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
			return CatalogSelectorOperation{}, err
		}
	case catalogOperationRollbackPrepare:
		if current.Target == operation.NextTarget {
			if err := switchCatalogSelector(layout, operation.NextTarget, operation.PreviousTarget); err != nil {
				return CatalogSelectorOperation{}, err
			}
		} else if current.Target != operation.PreviousTarget {
			return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
		}
		operation.Stage = catalogOperationRolledBack
		if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
			return CatalogSelectorOperation{}, err
		}
	case catalogOperationComplete, catalogOperationRolledBack:
		want := operation.NextTarget
		if operation.Stage == catalogOperationRolledBack {
			want = operation.PreviousTarget
		}
		if current.Target != want {
			return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
		}
	default:
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	return operation, nil
}

func rollbackCatalogSelectorAt(layout catalogSelectorLayout, operationID string, databaseSchema int, backup catalogBackupReference, interruption func(string) error) (CatalogSelectorOperation, error) {
	if validateCatalogMutationLayout(layout) != nil || !validCatalogOperationID(operationID) || databaseSchema <= 0 {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	locks, err := acquireCatalogSelectorLocks(layout)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	defer releaseCatalogSelectorLocks(locks)
	return rollbackCatalogSelectorLocked(layout, operationID, databaseSchema, backup, interruption)
}

func rollbackCatalogSelectorLocked(layout catalogSelectorLayout, operationID string, databaseSchema int, backup catalogBackupReference, interruption func(string) error) (CatalogSelectorOperation, error) {
	if validateCatalogTransitionLayout(layout) != nil || !validCatalogOperationID(operationID) || databaseSchema <= 0 {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	operation, err := readCatalogSelectorOperation(layout.operationRoot, operationID)
	if err != nil || validateCatalogOperation(layout, operation) != nil || operation.Stage != catalogOperationComplete || operation.PreviousTarget == "" {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Target != operation.NextTarget {
		return CatalogSelectorOperation{}, ErrCatalogSelectorConflict
	}
	configurationHash, err := hashCatalogConfiguration(layout.configFile)
	if err != nil || configurationHash != operation.ConfigurationSHA256 {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	previous, err := resolveCatalogSelectorTarget(layout, operation.PreviousTarget)
	if err != nil || !catalogDatabaseSupportsSchema(previous, databaseSchema) {
		return CatalogSelectorOperation{}, ErrCatalogRollbackUnsafe
	}
	if err := validateCatalogBackupReference(layout, backup, operation.NextTarget, databaseSchema); err != nil {
		return CatalogSelectorOperation{}, err
	}
	operation.Stage = catalogOperationRollbackPrepare
	operation.RollbackBackupDirectory = backup.Directory
	operation.RollbackBackupManifestSHA256 = backup.ManifestSHA256
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return CatalogSelectorOperation{}, err
	}
	if err := switchCatalogSelector(layout, operation.NextTarget, operation.PreviousTarget); err != nil {
		return operation, err
	}
	if interruption != nil {
		if err := interruption(catalogSelectorAfterSwitch); err != nil {
			return operation, err
		}
	}
	operation.Stage = catalogOperationRolledBack
	if err := writeCatalogSelectorOperation(layout.operationRoot, operation); err != nil {
		return operation, err
	}
	return operation, nil
}

func catalogDatabaseSupportsSchema(selection resolvedCatalogSelection, schema int) bool {
	if selection.kind == "catalog" && selection.release != nil {
		compatibility := selection.release.Database
		return compatibility.Rollback == catalogRollbackPolicy && schema >= compatibility.ReadSchemaMin && schema <= compatibility.ReadSchemaMax
	}
	return schema > 0 && schema <= selection.bundle.Manifest.DatabaseSchemaMax
}

func switchCatalogSelector(layout catalogSelectorLayout, expected, next string) error {
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Target != expected {
		return ErrCatalogSelectorConflict
	}
	if next != "" {
		if _, err := resolveCatalogSelectorTarget(layout, next); err != nil {
			return err
		}
	}
	currentPath := filepath.Join(layout.serviceRoot, "current")
	if next == "" {
		if err := os.Remove(currentPath); err != nil {
			return err
		}
		return syncSimpleDirectory(layout.serviceRoot)
	}
	temporary, err := os.MkdirTemp(layout.serviceRoot, ".catalog-selector-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	link := filepath.Join(temporary, "current")
	if err := os.Symlink(next, link); err != nil {
		return err
	}
	current, err = inspectCatalogSelector(layout)
	if err != nil || current.Target != expected {
		return ErrCatalogSelectorConflict
	}
	if err := os.Rename(link, currentPath); err != nil {
		return fmt.Errorf("atomically switch catalog lane selector: %w", err)
	}
	if err := syncSimpleDirectory(layout.serviceRoot); err != nil {
		return err
	}
	current, err = inspectCatalogSelector(layout)
	if err != nil || current.Target != next {
		return ErrCatalogOperationInvalid
	}
	return nil
}

type catalogSelectorLocks struct {
	catalogFD int
	laneFD    int
}

func acquireCatalogSelectorLocks(layout catalogSelectorLayout) (catalogSelectorLocks, error) {
	if err := validateCatalogSelectorLayout(layout); err != nil {
		return catalogSelectorLocks{}, err
	}
	if err := validateCatalogRootPath(layout.catalogRoot); err != nil {
		return catalogSelectorLocks{}, err
	}
	if err := validateCatalogRootPath(layout.serviceRoot); err != nil {
		return catalogSelectorLocks{}, err
	}
	if err := validateExistingPrivateDirectory(layout.serviceRoot); err != nil {
		return catalogSelectorLocks{}, err
	}
	if err := validateCatalogRootPath(filepath.Dir(layout.lifecycleLock)); err != nil {
		return catalogSelectorLocks{}, err
	}
	if err := validateExistingPrivateDirectory(filepath.Dir(layout.lifecycleLock)); err != nil {
		return catalogSelectorLocks{}, err
	}
	uid := uint32(os.Getuid())
	var rootInfo unix.Stat_t
	if err := unix.Lstat(layout.catalogRoot, &rootInfo); err != nil || rootInfo.Mode&unix.S_IFMT != unix.S_IFDIR || rootInfo.Uid != uid || rootInfo.Mode&0o777 != 0o700 {
		return catalogSelectorLocks{}, ErrInvalidCatalogRelease
	}
	catalogFD, err := openCatalogLock(filepath.Join(layout.catalogRoot, ".catalog.lock"), uid)
	if err != nil {
		return catalogSelectorLocks{}, err
	}
	locks := catalogSelectorLocks{catalogFD: catalogFD, laneFD: -1}
	laneFD, err := openCatalogLock(layout.lifecycleLock, uid)
	if err != nil {
		releaseCatalogSelectorLocks(locks)
		return catalogSelectorLocks{}, err
	}
	locks.laneFD = laneFD
	return locks, nil
}

func acquireCatalogRootPublicationLock(catalogRoot string) (int, error) {
	if !filepath.IsAbs(catalogRoot) || filepath.Clean(catalogRoot) != catalogRoot || validateCatalogRootPath(catalogRoot) != nil {
		return -1, ErrInvalidCatalogRelease
	}
	var rootInfo unix.Stat_t
	if err := unix.Lstat(catalogRoot, &rootInfo); err != nil || rootInfo.Mode&unix.S_IFMT != unix.S_IFDIR || rootInfo.Uid != uint32(os.Getuid()) || rootInfo.Mode&0o777 != 0o700 {
		return -1, ErrInvalidCatalogRelease
	}
	return openCatalogLock(filepath.Join(catalogRoot, ".catalog.lock"), uint32(os.Getuid()))
}

func releaseCatalogRootPublicationLock(fd int) {
	if fd >= 0 {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}
}

func openCatalogLock(path string, uid uint32) (int, error) {
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return -1, statErr
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return -1, err
	}
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uid || info.Mode&0o777 != 0o600 {
		_ = unix.Close(fd)
		return -1, ErrInvalidCatalogRelease
	}
	var named unix.Stat_t
	if err := unix.Lstat(path, &named); err != nil || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Dev != info.Dev || named.Ino != info.Ino {
		_ = unix.Close(fd)
		return -1, ErrInvalidCatalogRelease
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if created {
		if err := syncSimpleDirectory(filepath.Dir(path)); err != nil {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

func releaseCatalogSelectorLocks(locks catalogSelectorLocks) {
	if locks.laneFD >= 0 {
		_ = unix.Flock(locks.laneFD, unix.LOCK_UN)
		_ = unix.Close(locks.laneFD)
	}
	if locks.catalogFD >= 0 {
		_ = unix.Flock(locks.catalogFD, unix.LOCK_UN)
		_ = unix.Close(locks.catalogFD)
	}
}

func writeCatalogSelectorOperation(root string, operation CatalogSelectorOperation) error {
	if validateCatalogOperationFields(operation) != nil {
		return ErrCatalogOperationInvalid
	}
	if err := ensureCatalogPrivateDirectory(root); err != nil {
		return err
	}
	path := filepath.Join(root, operation.OperationID+".json")
	if info, err := os.Lstat(path); err == nil {
		var stat unix.Stat_t
		if unix.Lstat(path, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Mode&0o777 != 0o600 || !catalogFileHasSingleLink(info) {
			return ErrCatalogOperationInvalid
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(operation, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(root, ".operation-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncSimpleDirectory(root)
}

func readCatalogSelectorOperation(root, operationID string) (CatalogSelectorOperation, error) {
	if !validCatalogOperationID(operationID) || validateCatalogRootPath(root) != nil || validateExistingPrivateDirectory(root) != nil {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	path := filepath.Join(root, operationID+".json")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<16 || !catalogFileHasSingleLink(info) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogSelectorOperation{}, err
	}
	var operation CatalogSelectorOperation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&operation) != nil || decoder.Decode(&struct{}{}) != io.EOF || operation.OperationID != operationID || validateCatalogOperationFields(operation) != nil {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(operation, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return CatalogSelectorOperation{}, ErrCatalogOperationInvalid
	}
	return operation, nil
}

func validateCatalogOperation(layout catalogSelectorLayout, operation CatalogSelectorOperation) error {
	if validateCatalogOperationFields(operation) != nil || operation.HostID != layout.hostID || operation.Lane != layout.lane ||
		operation.NextTarget != filepath.Join(layout.catalogRoot, operation.Release.ReleaseID, layout.hostID) {
		return ErrCatalogOperationInvalid
	}
	if operation.PreviousTarget != "" {
		if _, err := resolveCatalogSelectorTarget(layout, operation.PreviousTarget); err != nil {
			return ErrCatalogOperationInvalid
		}
	}
	status, err := InspectCatalogRelease(layout.catalogRoot, layout.hostID, operation.Release.ReleaseID)
	if err != nil || status.Identity != operation.Release || operation.DatabaseSchemaAtSwitch < status.Database.ReadSchemaMin || operation.DatabaseSchemaAtSwitch > status.Database.ReadSchemaMax {
		return ErrCatalogOperationInvalid
	}
	if validateCatalogBackupReference(layout, catalogBackupReference{Directory: operation.BackupDirectory, ManifestSHA256: operation.BackupManifestSHA256, CohortOperationID: operation.CohortOperationID}, operation.PreviousTarget, operation.DatabaseSchemaAtSwitch) != nil {
		return ErrCatalogOperationInvalid
	}
	if layout.lane == config.ReleaseLanePRD {
		plan, err := BuildPRDUpgradePlan(layout.hostID)
		manifest, loadErr := loadPRDBackupManifest(plan, operation.BackupDirectory)
		if err != nil || loadErr != nil || manifest.BundleSHA256 != operation.Release.BundleSHA256 {
			return ErrCatalogOperationInvalid
		}
	}
	rollbackRef := catalogBackupReference{Directory: operation.RollbackBackupDirectory, ManifestSHA256: operation.RollbackBackupManifestSHA256}
	if operation.RollbackBackupDirectory != "" || operation.RollbackBackupManifestSHA256 != "" {
		if validateCatalogBackupReference(layout, rollbackRef, operation.NextTarget, operation.DatabaseSchemaAtSwitch) != nil {
			return ErrCatalogOperationInvalid
		}
		if layout.lane == config.ReleaseLanePRD {
			plan, err := BuildPRDUpgradePlan(layout.hostID)
			manifest, loadErr := loadPRDBackupManifest(plan, operation.RollbackBackupDirectory)
			if err != nil || loadErr != nil || manifest.BundleSHA256 != operation.Release.BundleSHA256 {
				return ErrCatalogOperationInvalid
			}
		}
	}
	return nil
}

func validateCatalogOperationFields(operation CatalogSelectorOperation) error {
	if operation.FormatVersion != catalogOperationFormatVersion || !validCatalogOperationID(operation.OperationID) ||
		(operation.CohortOperationID != "" && (!validCatalogOperationID(operation.CohortOperationID) || !bundleDigestPattern.MatchString(operation.CohortEvidenceSHA256))) ||
		(operation.CohortOperationID == "" && operation.CohortEvidenceSHA256 != "") || !containsCatalogHost(operation.HostID) ||
		(operation.Lane != config.ReleaseLaneDev && operation.Lane != config.ReleaseLaneQAS && operation.Lane != config.ReleaseLanePRD) || !validCatalogReleaseID(operation.Release.ReleaseID) ||
		operation.Release.HostID != operation.HostID || !bundleDigestPattern.MatchString(operation.Release.ReleaseSHA256) ||
		!bundleDigestPattern.MatchString(operation.Release.BundleSHA256) || !bundleDigestPattern.MatchString(operation.ConfigurationSHA256) ||
		operation.DatabaseSchemaAtSwitch <= 0 || !filepath.IsAbs(operation.NextTarget) || filepath.Clean(operation.NextTarget) != operation.NextTarget {
		return ErrCatalogOperationInvalid
	}
	switch operation.Stage {
	case catalogOperationPrepared, catalogOperationSwitched, catalogOperationComplete, catalogOperationRollbackPrepare, catalogOperationRolledBack:
	default:
		return ErrCatalogOperationInvalid
	}
	if operation.PreviousTarget != "" && filepath.Clean(operation.PreviousTarget) != operation.PreviousTarget {
		return ErrCatalogOperationInvalid
	}
	if !filepath.IsAbs(operation.BackupDirectory) || filepath.Clean(operation.BackupDirectory) != operation.BackupDirectory ||
		!bundleDigestPattern.MatchString(operation.BackupManifestSHA256) ||
		(operation.RollbackBackupDirectory == "") != (operation.RollbackBackupManifestSHA256 == "") ||
		(operation.RollbackBackupDirectory != "" && (!filepath.IsAbs(operation.RollbackBackupDirectory) || filepath.Clean(operation.RollbackBackupDirectory) != operation.RollbackBackupDirectory || !bundleDigestPattern.MatchString(operation.RollbackBackupManifestSHA256))) {
		return ErrCatalogOperationInvalid
	}
	if (operation.Stage == catalogOperationRollbackPrepare || operation.Stage == catalogOperationRolledBack) && operation.RollbackBackupDirectory == "" {
		return ErrCatalogOperationInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, operation.CreatedUTC); err != nil {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func validateCatalogBackupReference(layout catalogSelectorLayout, reference catalogBackupReference, activeTarget string, schema int) error {
	if layout.lane == config.ReleaseLanePRD {
		plan, err := BuildPRDUpgradePlan(layout.hostID)
		if err != nil || plan.Namespace.Backups != layout.backupRoot || plan.Namespace.Database != layout.databasePath {
			return catalogBackupFailure("PRD backup plan is invalid")
		}
		if !filepath.IsAbs(reference.Directory) || filepath.Clean(reference.Directory) != reference.Directory ||
			filepath.Dir(reference.Directory) != layout.backupRoot ||
			(!strings.HasPrefix(filepath.Base(reference.Directory), "prd-upgrade-") && !strings.HasPrefix(filepath.Base(reference.Directory), "prd-catalog-")) ||
			!bundleDigestPattern.MatchString(reference.ManifestSHA256) || schema <= 0 ||
			validateCatalogRootPath(layout.backupRoot) != nil || validateExistingPrivateDirectory(layout.backupRoot) != nil {
			return catalogBackupFailure("PRD backup reference is outside its protected root")
		}
		manifest, err := loadPRDBackupManifest(plan, reference.Directory)
		if err != nil || manifest.SchemaVersion != schema || manifest.DatabasePath != layout.databasePath ||
			backupManifestSHA256(reference.Directory) != reference.ManifestSHA256 {
			return catalogBackupFailure("PRD backup manifest does not match host, database, schema, or digest")
		}
		return nil
	}
	if !filepath.IsAbs(reference.Directory) || filepath.Clean(reference.Directory) != reference.Directory || filepath.Dir(reference.Directory) != layout.backupRoot ||
		!strings.HasPrefix(filepath.Base(reference.Directory), ".simple-backup-") || !bundleDigestPattern.MatchString(reference.ManifestSHA256) || schema <= 0 {
		return catalogBackupFailure("reference path is not inside the selected lane backup root")
	}
	if validateCatalogRootPath(layout.backupRoot) != nil || validateExistingPrivateDirectory(layout.backupRoot) != nil || validateExistingPrivateDirectory(reference.Directory) != nil {
		return catalogBackupFailure("directory ownership or permissions are invalid")
	}
	entries, err := os.ReadDir(reference.Directory)
	if err != nil {
		return catalogBackupFailure("directory could not be read")
	}
	coreEntries := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			return catalogBackupFailure("directory contains a symlink")
		}
		switch name {
		case "manifest.json", "config.yaml", "authority.sqlite":
			coreEntries[name] = true
		case "authority.sqlite-wal", "authority.sqlite-shm", "authority.sqlite-journal":
			path := filepath.Join(reference.Directory, name)
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !catalogFileHasSingleLink(info) {
				return catalogBackupFailure("SQLite sidecar is unsafe")
			}
			var stat unix.Stat_t
			if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
				return catalogBackupFailure("SQLite sidecar owner is invalid")
			}
		default:
			return catalogBackupFailure("directory contains an unexpected entry")
		}
	}
	if len(coreEntries) != 3 {
		return catalogBackupFailure("manifest or snapshot file is missing")
	}
	manifestPath := filepath.Join(reference.Directory, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != 0o600 || !catalogFileHasSingleLink(manifestInfo) {
		return catalogBackupFailure("manifest file is unsafe")
	}
	var manifestStat unix.Stat_t
	if unix.Lstat(manifestPath, &manifestStat) != nil || manifestStat.Uid != uint32(os.Getuid()) {
		return catalogBackupFailure("manifest owner is invalid")
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return catalogBackupFailure("manifest could not be read")
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(manifestDigest[:]) != reference.ManifestSHA256 {
		return catalogBackupFailure("manifest digest does not match")
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest simpleBackupManifest
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || manifest.FormatVersion != 1 ||
		manifest.HostID != layout.hostID || manifest.Lane != layout.lane || manifest.DatabasePath != layout.databasePath ||
		manifest.DatabaseSchema != schema || manifest.CreatedAt.IsZero() {
		return catalogBackupFailure("manifest does not match the selected host, lane, database, or schema")
	}
	expectedBundle := ""
	if activeTarget != "" {
		selection, err := resolveCatalogSelectorTarget(layout, activeTarget)
		if err != nil {
			return catalogBackupFailure("active bundle identity is invalid")
		}
		expectedBundle = selection.bundle.Manifest.BundleSHA256
	}
	if manifest.ActiveBundle != expectedBundle {
		return catalogBackupFailure("backup manifest active bundle does not match")
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(manifestBytes, append(canonical, '\n')) {
		return catalogBackupFailure("manifest encoding is not canonical")
	}
	for name, expected := range map[string]string{"config.yaml": manifest.ConfigSHA256, "authority.sqlite": manifest.DatabaseSHA256} {
		path := filepath.Join(reference.Directory, name)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !catalogFileHasSingleLink(info) {
			return catalogBackupFailure("snapshot file is unsafe")
		}
		var fileStat unix.Stat_t
		if unix.Lstat(path, &fileStat) != nil || fileStat.Uid != uint32(os.Getuid()) {
			return catalogBackupFailure("snapshot file owner is invalid")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return catalogBackupFailure("snapshot file could not be read")
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != expected {
			return catalogBackupFailure("snapshot file digest does not match")
		}
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(context.Background(), filepath.Join(reference.Directory, "authority.sqlite"))
	if err != nil {
		return fmt.Errorf("open backup SQLite snapshot: %w", ErrSimpleBackupRequired)
	}
	var backupSchema int
	schemaErr := database.QueryRow("PRAGMA user_version").Scan(&backupSchema)
	closeErr := database.Close()
	if schemaErr != nil || closeErr != nil || backupSchema != manifest.DatabaseSchema {
		return fmt.Errorf("backup SQLite schema does not match its manifest: %w", ErrSimpleBackupRequired)
	}
	return nil
}

func catalogBackupFailure(reason string) error {
	return fmt.Errorf("catalog backup %s: %w", reason, ErrSimpleBackupRequired)
}

func backupManifestSHA256(directory string) string {
	path := filepath.Join(directory, "manifest.json")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !catalogFileHasSingleLink(info) {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ensureCatalogPrivateDirectory(path string) error {
	if err := ensureSimpleDirectoryPath(path); err != nil {
		return err
	}
	return validateExistingPrivateDirectory(path)
}

func validateExistingPrivateDirectory(path string) error {
	var info unix.Stat_t
	if err := unix.Lstat(path, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != uint32(os.Getuid()) || info.Mode&0o777 != 0o700 {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func hashCatalogConfiguration(path string) (string, error) {
	if validateCatalogRootPath(filepath.Dir(path)) != nil {
		return "", ErrCatalogRollbackUnsafe
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 || !catalogFileHasSingleLink(info) {
		return "", ErrCatalogRollbackUnsafe
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return "", ErrCatalogRollbackUnsafe
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func newCatalogOperationID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validCatalogOperationID(operationID string) bool {
	if len(operationID) != 32 {
		return false
	}
	_, err := hex.DecodeString(operationID)
	return err == nil && strings.ToLower(operationID) == operationID
}

// CatalogSelectorPlanOperation returns the filesystem layout for a validated
// existing-host Dev/QAS plan. Kept small so CAT-03 can add its cohort hold
// receipt without changing the selector journal format.
