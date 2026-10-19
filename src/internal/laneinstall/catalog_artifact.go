package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

const catalogArtifactRefreshFormatVersion = 1

// CatalogArtifactRefreshOperation records the guarded replacement of the
// fixed Linux Dev/QAS launcher needed when a lane changes from its legacy
// release selector to its sealed catalog selector.
type CatalogArtifactRefreshOperation struct {
	FormatVersion          int                `json:"format_version"`
	OperationID            string             `json:"operation_id"`
	CohortOperationID      string             `json:"cohort_operation_id"`
	HostID                 string             `json:"host_id"`
	Lane                   config.ReleaseLane `json:"lane"`
	ReleaseID              string             `json:"release_id"`
	ArtifactPath           string             `json:"artifact_path"`
	PreviousSHA256         string             `json:"previous_sha256"`
	SelectedSHA256         string             `json:"selected_sha256"`
	BackupPath             string             `json:"backup_path"`
	BackupSHA256           string             `json:"backup_sha256"`
	DatabaseBackup         string             `json:"database_backup"`
	DatabaseBackupManifest string             `json:"database_backup_manifest_sha256"`
	Stage                  string             `json:"stage"`
	CreatedUTC             string             `json:"created_utc"`
}

// RefreshLinuxEntrypoint replaces only the fixed Dev/QAS Linux launcher after
// the complete four-host cohort has selected the target release and remains
// held. It requires a stopped service, fresh empty-work proof, a verified
// backup bound to the selected catalog release, and the caller's exact
// observation of the previous file hash.
func (m *CatalogLaneMaintenance) RefreshLinuxEntrypoint(ctx context.Context, cohortOperationID, expectedCurrentSHA256 string) (CatalogArtifactRefreshOperation, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh ||
		m.layout.hostID == "mac-control-plane" ||
		(m.layout.lane != config.ReleaseLaneDev && m.layout.lane != config.ReleaseLaneQAS) ||
		!validCatalogOperationID(cohortOperationID) || !bundleDigestPattern.MatchString(expectedCurrentSHA256) ||
		!simpleProofFresh(m.maintenance.proof, m.maintenance.plan) {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	receipt, err := ReadCatalogCohortReceipt(m.maintenance.plan, cohortOperationID)
	if err != nil || receipt.Stage != CatalogCohortSwitching || len(cohortSwitchedHosts(receipt)) != len(catalogCohortSwitchOrder) {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	selected, err := inspectCatalogSelector(m.layout)
	if err != nil || selected.Kind != "catalog" || selected.Release == nil || selected.Release.Identity.ReleaseID != receipt.ReleaseID {
		return CatalogArtifactRefreshOperation{}, ErrCatalogSelectorConflict
	}
	active, err := m.maintenance.service.Status(ctx, m.maintenance.plan)
	if err != nil || active {
		return CatalogArtifactRefreshOperation{}, ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundleForPlan(m.maintenance.plan)
	if err != nil || current != m.maintenance.proof.current {
		return CatalogArtifactRefreshOperation{}, ErrCatalogSelectorConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.maintenance.plan, current)
	if err != nil || proof.database != m.maintenance.proof.database || !simpleMetricsEmpty(proof.metrics) {
		return CatalogArtifactRefreshOperation{}, ErrSimpleLaneNotQuiescent
	}
	m.maintenance.proof = proof
	release, err := InspectCatalogRelease(m.layout.catalogRoot, m.layout.hostID, receipt.ReleaseID)
	if err != nil || release.Identity.ReleaseSHA256 != receipt.ReleaseSHA256 || release.Identity.BundleSHA256 != current {
		return CatalogArtifactRefreshOperation{}, ErrCatalogSelectorConflict
	}
	var artifact *Artifact
	for index := range m.maintenance.plan.Artifacts {
		candidate := &m.maintenance.plan.Artifacts[index]
		if candidate.Kind == ArtifactEntrypoint {
			artifact = candidate
			break
		}
	}
	if artifact == nil || artifact.InstallPath != m.maintenance.plan.Namespace.RunnerEntrypoint || artifact.Mode != 0o700 {
		return CatalogArtifactRefreshOperation{}, ErrSimpleArtifactConflict
	}
	// The cohort backup is taken before the selector changes and therefore
	// records the predecessor bundle. Refreshing the launcher happens after the
	// new selector is active, so capture a second consistent backup bound to the
	// selected catalog bundle. On retry, reuse the exact backup already bound to
	// the durable artifact-operation record.
	operationRoot := filepath.Join(m.maintenance.plan.Namespace.Staging, "catalog-artifact-operations")
	if err := ensureCatalogPrivateDirectory(operationRoot); err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	if existing, readErr := readCatalogArtifactRefreshOperation(operationRoot, cohortOperationID); readErr == nil {
		if existing.HostID != m.layout.hostID || existing.Lane != m.layout.lane || existing.ReleaseID != receipt.ReleaseID ||
			existing.ArtifactPath != artifact.InstallPath || existing.PreviousSHA256 != expectedCurrentSHA256 ||
			existing.SelectedSHA256 != catalogArtifactSHA256([]byte(artifact.Contents)) {
			return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
		}
		if err := m.UseVerifiedBackup(ctx, existing.DatabaseBackup, existing.DatabaseBackupManifest); err != nil {
			return CatalogArtifactRefreshOperation{}, err
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
	} else {
		return CatalogArtifactRefreshOperation{}, readErr
	}
	backup, err := ensureCurrentCatalogArtifactRefreshBackup(ctx, m.maintenance, m.maintenance.Backup)
	if err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	return refreshCatalogLinuxEntrypoint(m.maintenance.plan, receipt.ReleaseID, cohortOperationID,
		expectedCurrentSHA256, *artifact, backup)
}

func ensureCurrentCatalogArtifactRefreshBackup(ctx context.Context, maintenance *SimpleLaneMaintenance,
	capture func(context.Context) (SimpleLaneBackupReceipt, error)) (SimpleLaneBackupReceipt, error) {
	if maintenance == nil || maintenance.closed || maintenance.fresh || capture == nil ||
		!simpleProofFresh(maintenance.proof, maintenance.plan) {
		return SimpleLaneBackupReceipt{}, ErrSimpleBackupRequired
	}
	if maintenance.backup != nil && simpleBackupReceiptValid(maintenance.plan, *maintenance.backup, maintenance.proof) {
		return *maintenance.backup, nil
	}
	// A cohort backup may be bound to the pre-switch bundle. Keep it on disk,
	// but never reuse it for an artifact change against the selected bundle.
	maintenance.backup = nil
	backup, err := capture(ctx)
	if err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	if !simpleBackupReceiptValid(maintenance.plan, backup, maintenance.proof) {
		return SimpleLaneBackupReceipt{}, ErrSimpleBackupRequired
	}
	maintenance.backup = &backup
	return backup, nil
}

func refreshCatalogLinuxEntrypoint(plan Plan, releaseID, operationID, expectedCurrentSHA256 string, artifact Artifact, backup SimpleLaneBackupReceipt) (CatalogArtifactRefreshOperation, error) {
	if validateSimplePlan(plan) != nil || plan.Namespace.Host != config.HostKindLinux ||
		artifact.Kind != ArtifactEntrypoint || artifact.InstallPath != plan.Namespace.RunnerEntrypoint || artifact.Mode != 0o700 ||
		!validCatalogReleaseID(releaseID) || !validCatalogOperationID(operationID) ||
		!bundleDigestPattern.MatchString(expectedCurrentSHA256) || backup.hostID != plan.HostID ||
		backup.lane != plan.Lane || backup.databasePath != plan.Namespace.Database {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	selectedSHA256 := catalogArtifactSHA256([]byte(artifact.Contents))
	operationRoot := filepath.Join(plan.Namespace.Staging, "catalog-artifact-operations")
	if err := ensureCatalogPrivateDirectory(operationRoot); err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	backupPath := filepath.Join(plan.Namespace.Backups, ".catalog-artifact-"+operationID+"-runnerd-entrypoint.bak")
	operation := CatalogArtifactRefreshOperation{
		FormatVersion: catalogArtifactRefreshFormatVersion, OperationID: operationID, CohortOperationID: operationID,
		HostID: plan.HostID, Lane: plan.Lane, ReleaseID: releaseID, ArtifactPath: artifact.InstallPath,
		PreviousSHA256: expectedCurrentSHA256, SelectedSHA256: selectedSHA256, BackupPath: backupPath,
		BackupSHA256: expectedCurrentSHA256, DatabaseBackup: backup.directory, DatabaseBackupManifest: backup.manifestHash,
		Stage: "prepared", CreatedUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if existing, err := readCatalogArtifactRefreshOperation(operationRoot, operationID); err == nil {
		if !catalogArtifactRefreshIdentityEqual(existing, operation) {
			return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
		}
		operation = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return CatalogArtifactRefreshOperation{}, err
	}
	if err := preserveCatalogArtifactBackup(artifact.InstallPath, backupPath, expectedCurrentSHA256); err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	if operation.Stage == "complete" {
		if verifyCatalogArtifactFile(artifact.InstallPath, selectedSHA256, uint32(os.Getuid()), 0o700) != nil {
			return CatalogArtifactRefreshOperation{}, ErrSimpleArtifactConflict
		}
		return operation, nil
	}
	if operation.Stage != "prepared" {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	if err := writeCatalogArtifactRefreshOperation(operationRoot, operation); err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	if err := installCatalogArtifactAtomically(artifact.InstallPath, expectedCurrentSHA256, []byte(artifact.Contents), selectedSHA256, 0o700); err != nil {
		// A crash after the rename but before the completion record is recoverable
		// only when the installed bytes already match this operation exactly.
		if verifyCatalogArtifactFile(artifact.InstallPath, selectedSHA256, uint32(os.Getuid()), 0o700) != nil {
			return operation, err
		}
	}
	operation.Stage = "complete"
	if err := writeCatalogArtifactRefreshOperation(operationRoot, operation); err != nil {
		return operation, err
	}
	if err := verifyCatalogArtifactFile(artifact.InstallPath, selectedSHA256, uint32(os.Getuid()), 0o700); err != nil {
		return operation, ErrSimpleArtifactConflict
	}
	return operation, nil
}

func catalogArtifactRefreshIdentityEqual(left, right CatalogArtifactRefreshOperation) bool {
	return left.FormatVersion == right.FormatVersion && left.OperationID == right.OperationID &&
		left.CohortOperationID == right.CohortOperationID && left.HostID == right.HostID && left.Lane == right.Lane &&
		left.ReleaseID == right.ReleaseID && left.ArtifactPath == right.ArtifactPath &&
		left.PreviousSHA256 == right.PreviousSHA256 && left.SelectedSHA256 == right.SelectedSHA256 &&
		left.BackupPath == right.BackupPath && left.BackupSHA256 == right.BackupSHA256 &&
		left.DatabaseBackup == right.DatabaseBackup && left.DatabaseBackupManifest == right.DatabaseBackupManifest
}

func preserveCatalogArtifactBackup(source, backupPath, expectedSHA256 string) error {
	if !filepath.IsAbs(backupPath) || filepath.Clean(backupPath) != backupPath || !bundleDigestPattern.MatchString(expectedSHA256) {
		return ErrCatalogOperationInvalid
	}
	if data, err := readCatalogArtifactFile(backupPath, expectedSHA256, uint32(os.Getuid()), 0o600); err == nil {
		_ = data
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := readCatalogArtifactFile(source, expectedSHA256, uint32(os.Getuid()), 0o700)
	if err != nil {
		return ErrSimpleArtifactConflict
	}
	file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syncSimpleDirectory(filepath.Dir(backupPath)); err != nil {
		return err
	}
	_, err = readCatalogArtifactFile(backupPath, expectedSHA256, uint32(os.Getuid()), 0o600)
	return err
}

func installCatalogArtifactAtomically(path, expectedCurrentSHA256 string, content []byte, selectedSHA256 string, mode os.FileMode) error {
	if err := verifyCatalogArtifactFile(path, expectedCurrentSHA256, uint32(os.Getuid()), mode); err != nil {
		return ErrSimpleArtifactConflict
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".catalog-artifact-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
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
	if catalogArtifactSHA256(content) != selectedSHA256 {
		return ErrCatalogOperationInvalid
	}
	if err := verifyCatalogArtifactFile(path, expectedCurrentSHA256, uint32(os.Getuid()), mode); err != nil {
		return ErrSimpleArtifactConflict
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := syncSimpleDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return verifyCatalogArtifactFile(path, selectedSHA256, uint32(os.Getuid()), mode)
}

func verifyCatalogArtifactFile(path, expectedSHA256 string, uid uint32, mode os.FileMode) error {
	_, err := readCatalogArtifactFile(path, expectedSHA256, uid, mode)
	return err
}

func readCatalogArtifactFile(path, expectedSHA256 string, uid uint32, mode os.FileMode) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != mode.Perm() || !catalogFileHasSingleLink(info) {
		return nil, ErrSimpleArtifactConflict
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != mode.Perm() {
		return nil, ErrSimpleArtifactConflict
	}
	var ownership unix.Stat_t
	if unix.Fstat(fd, &ownership) != nil || ownership.Uid != uid || ownership.Nlink != 1 {
		return nil, ErrSimpleArtifactConflict
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil || catalogArtifactSHA256(data) != expectedSHA256 {
		return nil, ErrSimpleArtifactConflict
	}
	return data, nil
}

func readCatalogArtifactRefreshOperation(root, operationID string) (CatalogArtifactRefreshOperation, error) {
	if !validCatalogOperationID(operationID) || validateCatalogRootPath(root) != nil || validateExistingPrivateDirectory(root) != nil {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	path := filepath.Join(root, operationID+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<16 || !catalogFileHasSingleLink(info) {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogArtifactRefreshOperation{}, err
	}
	var operation CatalogArtifactRefreshOperation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&operation) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validCatalogArtifactRefreshRecord(operation, operationID) {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(operation, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return CatalogArtifactRefreshOperation{}, ErrCatalogOperationInvalid
	}
	return operation, nil
}

func validCatalogArtifactRefreshRecord(operation CatalogArtifactRefreshOperation, operationID string) bool {
	return operation.FormatVersion == catalogArtifactRefreshFormatVersion && operation.OperationID == operationID &&
		operation.CohortOperationID == operationID && containsCatalogHost(operation.HostID) &&
		(operation.Lane == config.ReleaseLaneDev || operation.Lane == config.ReleaseLaneQAS) &&
		operation.HostID != "mac-control-plane" && validCatalogReleaseID(operation.ReleaseID) &&
		filepath.IsAbs(operation.ArtifactPath) && filepath.Clean(operation.ArtifactPath) == operation.ArtifactPath &&
		bundleDigestPattern.MatchString(operation.PreviousSHA256) && bundleDigestPattern.MatchString(operation.SelectedSHA256) &&
		operation.BackupSHA256 == operation.PreviousSHA256 && filepath.IsAbs(operation.BackupPath) &&
		filepath.Clean(operation.BackupPath) == operation.BackupPath && filepath.IsAbs(operation.DatabaseBackup) &&
		filepath.Clean(operation.DatabaseBackup) == operation.DatabaseBackup &&
		bundleDigestPattern.MatchString(operation.DatabaseBackupManifest) &&
		(operation.Stage == "prepared" || operation.Stage == "complete") && validCohortTimestamp(operation.CreatedUTC)
}

func writeCatalogArtifactRefreshOperation(root string, operation CatalogArtifactRefreshOperation) error {
	if !validCatalogArtifactRefreshRecord(operation, operation.OperationID) {
		return ErrCatalogOperationInvalid
	}
	if err := ensureCatalogPrivateDirectory(root); err != nil {
		return err
	}
	data, err := json.MarshalIndent(operation, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(root, ".artifact-operation-*.tmp")
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
	if err := os.Rename(temporaryPath, filepath.Join(root, operation.OperationID+".json")); err != nil {
		return err
	}
	return syncSimpleDirectory(root)
}

func catalogArtifactSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
