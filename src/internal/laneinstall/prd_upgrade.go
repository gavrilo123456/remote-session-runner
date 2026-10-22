package laneinstall

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const prdUpgradeBackupFormat = 1

var (
	ErrInvalidPRDUpgradePlan = errors.New("invalid fixed PRD upgrade plan")
	ErrPRDUpgradeGuard       = errors.New("PRD upgrade guard refused the operation")
	ErrPRDCatalogManaged     = errors.New("legacy PRD installer cannot modify a catalog-managed service")
	prdDigestPattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// PRDUpgradePlan is deliberately separate from Plan: the Dev/QAS plan
// validator remains closed to PRD and the production layout reuses only its
// existing unsuffixed service root and artifacts.
type PRDUpgradePlan struct {
	HostID    string
	Namespace config.LaneNamespace
}

// BuildPRDUpgradePlan returns one canonical unsuffixed service namespace.
func BuildPRDUpgradePlan(hostID string) (PRDUpgradePlan, error) {
	var host config.HostKind
	profile := ""
	switch hostID {
	case "mac-control-plane":
		host = config.HostKindMac
	case "linux-host", "sandbox-host", "factory-sandbox":
		host = config.HostKindLinux
		profile = hostID
	default:
		return PRDUpgradePlan{}, ErrInvalidPRDUpgradePlan
	}
	namespace, err := config.DeriveSimpleLaneNamespace(host, config.ReleaseLanePRD, profile)
	if err != nil || namespace.Lane != config.ReleaseLanePRD || namespace.Account == "" || namespace.ServiceRoot == "" || namespace.Database == "" || namespace.Binaries == "" {
		return PRDUpgradePlan{}, ErrInvalidPRDUpgradePlan
	}
	return PRDUpgradePlan{HostID: hostID, Namespace: namespace}, nil
}

// PRDServiceProbe must identify only the canonical unsuffixed service for the
// selected plan. The installer never stops or starts a service itself.
type PRDServiceProbe interface {
	Status(context.Context, PRDUpgradePlan) (bool, error)
}

type PRDBackupFile struct {
	TargetPath string `json:"target_path"`
	BackupPath string `json:"backup_path"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	Mode       uint32 `json:"mode"`
}

type PRDBackupManifest struct {
	FormatVersion      int             `json:"format_version"`
	HostID             string          `json:"host_id"`
	CreatedAt          string          `json:"created_at"`
	BundleSHA256       string          `json:"bundle_sha256"`
	SchemaVersion      int             `json:"schema_version"`
	DatabasePath       string          `json:"database_path"`
	DatabaseBackupPath string          `json:"database_backup_path"`
	DatabaseSHA256     string          `json:"database_sha256"`
	Files              []PRDBackupFile `json:"files"`
}

type PRDBackupReceipt struct {
	Directory string
	Manifest  PRDBackupManifest
}

// PRDBackupManifestSHA256 returns the digest of the exact canonical manifest
// inside a verified host-local PRD backup directory.
func PRDBackupManifestSHA256(directory string) string { return backupManifestSHA256(directory) }

type PRDPreflight struct {
	HostID             string            `json:"host_id"`
	ServiceActive      bool              `json:"service_active"`
	SchemaVersion      int               `json:"schema_version"`
	ConfigSHA256       string            `json:"config_sha256"`
	BinarySHA256       map[string]string `json:"binary_sha256"`
	LegacyBundleSHA256 string            `json:"legacy_bundle_sha256,omitempty"`
	AvailableBytes     uint64            `json:"available_bytes,omitempty"`
	RequiredBytes      uint64            `json:"required_bytes,omitempty"`
	MissingComponents  []string          `json:"missing_candidate_components,omitempty"`
	Work               PRDWorkInventory  `json:"work_inventory"`
}

// ReadPRDPreflight reports service, schema, artifact and durable-work facts
// without opening the database through store.Open or changing any file.
func ReadPRDPreflight(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe) (PRDPreflight, error) {
	if validatePRDPlan(plan) != nil || service == nil {
		return PRDPreflight{}, ErrInvalidPRDUpgradePlan
	}
	if err := refuseLegacyPRDMutationForCatalog(plan); err != nil {
		return PRDPreflight{}, err
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return PRDPreflight{}, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return PRDPreflight{}, err
	}
	schema, work, inventoryErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil || schema <= 0 {
		return PRDPreflight{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	configSHA, err := hashPRDRegularFile(plan.Namespace.ConfigFile)
	if err != nil {
		return PRDPreflight{}, fmt.Errorf("hash existing PRD configuration: %w", err)
	}
	binaries := make(map[string]string)
	entries, err := os.ReadDir(plan.Namespace.Binaries)
	if err != nil {
		return PRDPreflight{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return PRDPreflight{}, fmt.Errorf("%w: unexpected PRD binary-root entry", ErrPRDUpgradeGuard)
		}
		path := filepath.Join(plan.Namespace.Binaries, entry.Name())
		digest, err := hashPRDRegularFile(path)
		if err != nil {
			return PRDPreflight{}, fmt.Errorf("hash existing PRD binary %s: %w", entry.Name(), err)
		}
		binaries[entry.Name()] = digest
	}
	var missing []string
	for _, name := range simpleHostComponents[plan.HostID] {
		if _, exists := binaries[name]; !exists {
			missing = append(missing, name)
		}
	}
	preflight := PRDPreflight{HostID: plan.HostID, ServiceActive: active, SchemaVersion: schema, ConfigSHA256: configSHA,
		BinarySHA256: binaries, MissingComponents: missing, Work: work}
	if len(missing) == 0 {
		preflight.LegacyBundleSHA256, err = PRDLegacyBundleDigest(preflight)
		if err != nil {
			return PRDPreflight{}, err
		}
	}
	preflight.AvailableBytes, preflight.RequiredBytes, err = measurePRDBackupCapacity(plan, plan.Namespace.Database)
	if err != nil {
		return PRDPreflight{}, err
	}
	return preflight, nil
}

func hashPRDRegularFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrPRDUpgradeGuard
	}
	return fileSHA256(path)
}

// PRDLegacyBundleDigest identifies the exact sorted file/hash inventory in an
// existing unsuffixed PRD bin directory. It is deliberately only an identity:
// it does not infer source revision or schema compatibility for legacy bytes.
func PRDLegacyBundleDigest(preflight PRDPreflight) (string, error) {
	if !containsCatalogHost(preflight.HostID) || len(preflight.BinarySHA256) == 0 || len(preflight.MissingComponents) != 0 {
		return "", ErrPRDUpgradeGuard
	}
	for name, digest := range preflight.BinarySHA256 {
		if filepath.Base(name) != name || name == "." || name == ".." || !prdDigestPattern.MatchString(digest) {
			return "", ErrPRDUpgradeGuard
		}
	}
	canonical, err := json.Marshal(struct {
		HostID string            `json:"host_id"`
		Files  map[string]string `json:"files"`
	}{preflight.HostID, preflight.BinarySHA256})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// CreatePRDBackup captures the old unsuffixed binaries, configuration,
// secrets, service definitions, and a consistent online SQLite snapshot. It
// can run before admissions are closed because every read remains read-only.
func CreatePRDBackup(ctx context.Context, plan PRDUpgradePlan, bundleSHA string) (PRDBackupReceipt, error) {
	return createPRDBackup(ctx, plan, bundleSHA, false)
}

// CreatePRDCatalogBackup captures the same consistent database and host-local
// recovery material for a selector-aware PRD transition. It is separate from
// the legacy backup command so the ordinary PRD installer remains fail-closed
// after catalog adoption.
func CreatePRDCatalogBackup(ctx context.Context, plan PRDUpgradePlan, bundleSHA string) (PRDBackupReceipt, error) {
	return createPRDBackup(ctx, plan, bundleSHA, true)
}

func createPRDBackup(ctx context.Context, plan PRDUpgradePlan, bundleSHA string, catalogManaged bool) (PRDBackupReceipt, error) {
	if validatePRDPlan(plan) != nil || !prdDigestPattern.MatchString(bundleSHA) {
		return PRDBackupReceipt{}, ErrInvalidPRDUpgradePlan
	}
	if !catalogManaged {
		if err := refuseLegacyPRDMutationForCatalog(plan); err != nil {
			return PRDBackupReceipt{}, err
		}
	}
	if err := validatePRDConfig(plan); err != nil {
		return PRDBackupReceipt{}, err
	}
	if catalogManaged {
		if err := ensurePRDCatalogOperatingBuffer(plan, plan.Namespace.Database); err != nil {
			return PRDBackupReceipt{}, err
		}
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return PRDBackupReceipt{}, fmt.Errorf("open existing PRD authority read-only for backup: %w", err)
	}
	defer database.Close()
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return PRDBackupReceipt{}, fmt.Errorf("read PRD schema before backup: %w", err)
	}
	if err := ensurePRDBackupCapacity(plan, plan.Namespace.Database); err != nil {
		return PRDBackupReceipt{}, err
	}
	for _, path := range []string{plan.Namespace.ServiceRoot, plan.Namespace.Binaries, filepath.Join(plan.Namespace.ServiceRoot, "config"), filepath.Join(plan.Namespace.ServiceRoot, "secrets")} {
		if err := ensurePRDPrivateDirectory(path); err != nil {
			return PRDBackupReceipt{}, err
		}
	}
	if err := ensurePRDPrivateDirectory(plan.Namespace.Backups); err != nil {
		return PRDBackupReceipt{}, err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	prefix := "prd-upgrade-"
	if catalogManaged {
		prefix = "prd-catalog-"
	}
	directory := filepath.Join(plan.Namespace.Backups, prefix+stamp)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return PRDBackupReceipt{}, fmt.Errorf("create unique private PRD backup directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(directory)
		}
	}()
	manifest := PRDBackupManifest{
		FormatVersion: prdUpgradeBackupFormat, HostID: plan.HostID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), BundleSHA256: bundleSHA,
		SchemaVersion: schema, DatabasePath: plan.Namespace.Database,
	}
	for _, source := range []string{plan.Namespace.Binaries, filepath.Join(plan.Namespace.ServiceRoot, "config"), filepath.Join(plan.Namespace.ServiceRoot, "secrets")} {
		files, copyErr := snapshotDirectory(source, plan.Namespace.ServiceRoot, directory)
		if copyErr != nil {
			return PRDBackupReceipt{}, copyErr
		}
		manifest.Files = append(manifest.Files, files...)
	}
	for _, servicePath := range prdServiceArtifactPaths(plan) {
		backupRelative := filepath.Join("service", filepath.Base(servicePath))
		file, copyErr := snapshotFile(servicePath, backupRelative, directory)
		if copyErr != nil {
			return PRDBackupReceipt{}, copyErr
		}
		manifest.Files = append(manifest.Files, file)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].TargetPath < manifest.Files[j].TargetPath })
	manifest.DatabaseBackupPath = filepath.Join(directory, "state.db.online")
	if err := store.CreateExistingOnlineBackup(ctx, database, manifest.DatabaseBackupPath); err != nil {
		return PRDBackupReceipt{}, fmt.Errorf("create consistent existing-PRD SQLite online backup: %w", err)
	}
	manifest.DatabaseSHA256, err = fileSHA256(manifest.DatabaseBackupPath)
	if err != nil {
		return PRDBackupReceipt{}, err
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return PRDBackupReceipt{}, err
	}
	if err := writePrivateSynced(filepath.Join(directory, "manifest.json"), manifestBytes, 0o600); err != nil {
		return PRDBackupReceipt{}, err
	}
	if err := syncDirectory(directory); err != nil {
		return PRDBackupReceipt{}, err
	}
	cleanup = false
	return PRDBackupReceipt{Directory: directory, Manifest: manifest}, nil
}

// InstallPRDBundle atomically replaces only the selected host's bundle
// components in the existing bin directory. It allows retained terminal
// reservations so the newly installed guarded recovery utility can dispose of
// them; active sessions, running/queued commands and resumable jobs still
// block installation. It does not migrate the DB or restart the service.
func InstallPRDBundle(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, bundle SimpleBundle, expectedSHA, backupDirectory string) ([]PRDBackupFile, error) {
	if validatePRDPlan(plan) != nil || service == nil || bundle.Manifest.HostID != plan.HostID || ValidateSimpleBundle(bundle) != nil || bundle.Manifest.BundleSHA256 != expectedSHA || !prdDigestPattern.MatchString(expectedSHA) {
		return nil, ErrInvalidPRDUpgradePlan
	}
	if err := refuseLegacyPRDMutationForCatalog(plan); err != nil {
		return nil, err
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("inspect unsuffixed PRD service before install: %w", err)
	}
	if active {
		return nil, fmt.Errorf("%w: unsuffixed PRD service must be stopped", ErrPRDUpgradeGuard)
	}
	lockFD, err := acquirePRDUpgradeLock(plan)
	if err != nil {
		return nil, err
	}
	defer releasePRDUpgradeLock(lockFD)
	active, err = service.Status(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("recheck unsuffixed PRD service before install: %w", err)
	}
	if active {
		return nil, fmt.Errorf("%w: unsuffixed PRD service must remain stopped", ErrPRDUpgradeGuard)
	}
	if err := validatePRDConfig(plan); err != nil {
		return nil, err
	}
	manifest, err := loadPRDBackupManifest(plan, backupDirectory)
	if err != nil || manifest.BundleSHA256 != expectedSHA {
		return nil, fmt.Errorf("%w: matching PRD backup is unavailable", ErrPRDUpgradeGuard)
	}
	if err := verifyPRDBackupTargets(manifest, true); err != nil {
		return nil, err
	}
	if err := verifyNoNewPRDWork(ctx, plan.Namespace.Database, manifest.DatabaseBackupPath); err != nil {
		return nil, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	inventory, err := readPRDWorkInventory(ctx, database)
	if err != nil {
		return nil, err
	}
	if !inventory.InstallableForGuardedRecovery() {
		return nil, fmt.Errorf("%w: active or resumable work blocks candidate installation: %+v", ErrPRDUpgradeGuard, inventory)
	}
	if err := database.Close(); err != nil {
		return nil, err
	}
	installed, err := installPRDBundleFiles(plan.Namespace.Binaries, bundle, manifest)
	if err != nil {
		return nil, errors.Join(err, restorePRDBinarySnapshot(plan.Namespace.Binaries, plan.HostID, manifest))
	}
	return installed, nil
}

// RollbackPRD restores the exact pre-upgrade DB schema and original binaries.
// It refuses to restore if any session/job/command/local-intent identity was
// added after the backup, preventing loss of accepted work.
func RollbackPRD(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, backupDirectory string) error {
	if validatePRDPlan(plan) != nil || service == nil {
		return ErrInvalidPRDUpgradePlan
	}
	if err := refuseLegacyPRDMutationForCatalog(plan); err != nil {
		return err
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return fmt.Errorf("inspect unsuffixed PRD service before rollback: %w", err)
	}
	if active {
		return fmt.Errorf("%w: unsuffixed PRD service must be stopped before rollback", ErrPRDUpgradeGuard)
	}
	lockFD, err := acquirePRDUpgradeLock(plan)
	if err != nil {
		return err
	}
	defer releasePRDUpgradeLock(lockFD)
	active, err = service.Status(ctx, plan)
	if err != nil {
		return fmt.Errorf("recheck unsuffixed PRD service before rollback: %w", err)
	}
	if active {
		return fmt.Errorf("%w: unsuffixed PRD service must remain stopped before rollback", ErrPRDUpgradeGuard)
	}
	manifest, err := loadPRDBackupManifest(plan, backupDirectory)
	if err != nil {
		return err
	}
	if err := validatePRDConfig(plan); err != nil {
		return err
	}
	if err := verifyPRDBackupTargets(manifest, false); err != nil {
		return err
	}
	if err := verifyNoNewPRDWork(ctx, plan.Namespace.Database, manifest.DatabaseBackupPath); err != nil {
		return err
	}
	if err := store.RestoreExistingOnlineBackup(ctx, plan.Namespace.Database, manifest.DatabaseBackupPath); err != nil {
		return fmt.Errorf("restore exact PRD database snapshot: %w", err)
	}
	return restorePRDBinarySnapshot(plan.Namespace.Binaries, plan.HostID, manifest)
}

// CatalogWorkInventory is the canonical full work snapshot used by PRD and
// catalog cohort receipts. Keep this representation stable: preflight,
// receipt creation, selector checks and recovery all hash these exact fields.
type CatalogWorkInventory struct {
	ActiveSessions               int64 `json:"active_sessions"`
	ActiveCommandSlots           int64 `json:"active_command_slots"`
	ActiveSessionReservations    int64 `json:"active_session_reservations"`
	QueuedCommands               int64 `json:"queued_commands"`
	RunningCommands              int64 `json:"running_commands"`
	ResumableJobs                int64 `json:"resumable_jobs"`
	PendingRecoveryFinalizations int64 `json:"pending_recovery_finalizations"`
}

// PRDWorkInventory remains an alias for existing PRD maintenance APIs while
// the catalog uses the shared canonical type.
type PRDWorkInventory = CatalogWorkInventory

// SHA256 returns the canonical digest shared by preflight, cohort receipts,
// selector guards and recovery. Struct field order and JSON tags are part of
// this durable evidence contract.
func (i CatalogWorkInventory) SHA256() string {
	encoded, _ := json.Marshal(i)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (i CatalogWorkInventory) Empty() bool {
	return i.ActiveSessions == 0 && i.ActiveCommandSlots == 0 && i.ActiveSessionReservations == 0 &&
		i.QueuedCommands == 0 && i.RunningCommands == 0 && i.ResumableJobs == 0 && i.PendingRecoveryFinalizations == 0
}

func (i CatalogWorkInventory) InstallableForGuardedRecovery() bool {
	return i.ActiveSessions == 0 && i.QueuedCommands == 0 && i.RunningCommands == 0 && i.ResumableJobs == 0
}

// MigratePRDDatabase applies the embedded forward-only migrations to one
// already-existing unsuffixed authority. It refuses a running PRD service or
// any remaining execution, reservation, queued command, or recovery marker.
func MigratePRDDatabase(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe) (int, PRDWorkInventory, error) {
	if validatePRDPlan(plan) != nil || service == nil {
		return 0, PRDWorkInventory{}, ErrInvalidPRDUpgradePlan
	}
	if err := refuseLegacyPRDMutationForCatalog(plan); err != nil {
		return 0, PRDWorkInventory{}, err
	}
	if err := validatePRDConfig(plan); err != nil {
		return 0, PRDWorkInventory{}, err
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return 0, PRDWorkInventory{}, fmt.Errorf("inspect unsuffixed PRD service before migration: %w", err)
	}
	if active {
		return 0, PRDWorkInventory{}, fmt.Errorf("%w: unsuffixed PRD service must be stopped before migration", ErrPRDUpgradeGuard)
	}
	lockFD, err := acquirePRDUpgradeLock(plan)
	if err != nil {
		return 0, PRDWorkInventory{}, err
	}
	defer releasePRDUpgradeLock(lockFD)
	active, err = service.Status(ctx, plan)
	if err != nil {
		return 0, PRDWorkInventory{}, fmt.Errorf("recheck unsuffixed PRD service before migration: %w", err)
	}
	if active {
		return 0, PRDWorkInventory{}, fmt.Errorf("%w: unsuffixed PRD service must remain stopped before migration", ErrPRDUpgradeGuard)
	}
	before, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return 0, PRDWorkInventory{}, err
	}
	inventory, inventoryErr := readPRDWorkInventory(ctx, before)
	closeErr := before.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil {
		return 0, PRDWorkInventory{}, err
	}
	if !inventory.Empty() {
		return 0, inventory, fmt.Errorf("%w: pre-migration work inventory is not empty: %+v", ErrPRDUpgradeGuard, inventory)
	}
	database, err := store.OpenExistingOfflineMaintenanceMigrating(ctx, plan.Namespace.Database)
	if err != nil {
		return 0, inventory, fmt.Errorf("migrate existing PRD authority in place: %w", err)
	}
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		_ = database.Close()
		return 0, inventory, err
	}
	post, err := readPRDWorkInventory(ctx, database)
	closeErr = database.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return schema, inventory, err
	}
	if schema != store.CurrentSchemaVersion || !post.Empty() {
		return schema, post, fmt.Errorf("%w: migrated schema=%d inventory=%+v", ErrPRDUpgradeGuard, schema, post)
	}
	return schema, post, nil
}

// refuseLegacyPRDMutationForCatalog prevents the pre-catalog PRD commands
// from backing up only the obsolete bin directory or writing binaries there
// after a selector migration. Catalog migration and recovery use their own
// selector-aware procedures.
func refuseLegacyPRDMutationForCatalog(plan PRDUpgradePlan) error {
	return refuseLegacyPRDMutationAt(filepath.Join(plan.Namespace.ServiceRoot, "current"))
}

func refuseLegacyPRDMutationAt(selector string) error {
	if _, err := os.Lstat(selector); err == nil {
		return ErrPRDCatalogManaged
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: cannot inspect the PRD catalog selector", ErrPRDUpgradeGuard)
	}
	return nil
}

type catalogSQLiteQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readPRDWorkInventory(ctx context.Context, database *sql.DB) (PRDWorkInventory, error) {
	_, inventory, err := readPRDWorkSnapshot(ctx, database)
	return inventory, err
}

// readPRDWorkSnapshot observes the schema and every work category inside one
// read-only SQLite transaction, so all receipt hashes describe one coherent
// database snapshot rather than adjacent count queries.
func readPRDWorkSnapshot(ctx context.Context, database *sql.DB) (int, CatalogWorkInventory, error) {
	if database == nil {
		return 0, CatalogWorkInventory{}, store.ErrNilDatabase
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, CatalogWorkInventory{}, err
	}
	var schema int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema <= 0 {
		_ = tx.Rollback()
		return 0, CatalogWorkInventory{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	inventory, err := readPRDWorkInventoryTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return 0, CatalogWorkInventory{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, CatalogWorkInventory{}, err
	}
	return schema, inventory, nil
}

func readPRDWorkInventoryTx(ctx context.Context, database catalogSQLiteQueryRower) (CatalogWorkInventory, error) {
	var inventory PRDWorkInventory
	queries := []struct {
		table string
		sql   string
		dest  *int64
	}{
		{"exec_sessions", `SELECT COUNT(*) FROM exec_sessions WHERE state NOT IN ('closed','expired','failed','lost')`, &inventory.ActiveSessions},
		{"exec_command_slots", `SELECT COUNT(*) FROM exec_command_slots WHERE stop_confirmed_at IS NULL OR released_at IS NULL`, &inventory.ActiveCommandSlots},
		{"exec_capacity_reservations", `SELECT COUNT(*) FROM exec_capacity_reservations WHERE cleanup_confirmed_at IS NULL OR released_at IS NULL`, &inventory.ActiveSessionReservations},
		{"exec_commands", `SELECT COUNT(*) FROM exec_commands WHERE state='queued'`, &inventory.QueuedCommands},
		{"exec_commands", `SELECT COUNT(*) FROM exec_commands WHERE state IN ('running','cancelling')`, &inventory.RunningCommands},
		{"exec_jobs", `SELECT COUNT(*) FROM exec_jobs WHERE phase IN ('creating_session','accepting_command','awaiting_command','closing_session')`, &inventory.ResumableJobs},
	}
	for _, query := range queries {
		exists, err := sqliteTableExists(ctx, database, query.table)
		if err != nil {
			return inventory, err
		}
		if !exists {
			continue
		}
		if err := database.QueryRowContext(ctx, query.sql).Scan(query.dest); err != nil {
			return inventory, fmt.Errorf("read PRD work inventory table %s: %w", query.table, err)
		}
	}
	for _, table := range []string{"exec_lost_runtime_recovery_finalizations", "exec_commandless_lost_runtime_recovery_finalizations", "exec_completed_teardown_recovery_finalizations"} {
		exists, err := sqliteTableExists(ctx, database, table)
		if err != nil {
			return inventory, err
		}
		if !exists {
			continue
		}
		var count int64
		if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return inventory, fmt.Errorf("read PRD recovery finalization table %s: %w", table, err)
		}
		inventory.PendingRecoveryFinalizations += count
	}
	return inventory, nil
}

func sqliteTableExists(ctx context.Context, database catalogSQLiteQueryRower, table string) (bool, error) {
	if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
		return false, ErrPRDUpgradeGuard
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func acquirePRDUpgradeLock(plan PRDUpgradePlan) (int, error) {
	lockPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	return acquireSimpleLaneLock(lockPlan)
}

func releasePRDUpgradeLock(fd int) {
	_ = unix.Flock(fd, unix.LOCK_UN)
	_ = unix.Close(fd)
}

func validatePRDPlan(plan PRDUpgradePlan) error {
	expected, err := BuildPRDUpgradePlan(plan.HostID)
	if err != nil || expected.Namespace != plan.Namespace {
		return ErrInvalidPRDUpgradePlan
	}
	current, err := user.Current()
	if err != nil || current.Username != plan.Namespace.Account {
		return ErrInvalidPRDUpgradePlan
	}
	want := simpleHostPlatforms[plan.HostID]
	if runtime.GOOS != want.goos || runtime.GOARCH != want.goarch {
		return ErrInvalidPRDUpgradePlan
	}
	return nil
}

func validatePRDConfig(plan PRDUpgradePlan) error {
	loaded, err := config.LoadFile(plan.Namespace.ConfigFile)
	if err != nil || loaded.ReleaseLane() != config.ReleaseLanePRD || loaded.Kind() != plan.Namespace.Host {
		return fmt.Errorf("%w: installed unsuffixed PRD configuration is not valid", ErrPRDUpgradeGuard)
	}
	if plan.Namespace.Host == config.HostKindMac {
		settings, ok := loaded.MacSettings()
		if !ok || settings.ServiceRoot != plan.Namespace.ServiceRoot || settings.Database != plan.Namespace.Database || settings.Account != plan.Namespace.Account {
			return ErrPRDUpgradeGuard
		}
		return nil
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || settings.ServiceRoot != plan.Namespace.ServiceRoot || settings.Database != plan.Namespace.Database || settings.Account != plan.Namespace.Account ||
		settings.DirectHTTPSBind != plan.Namespace.DirectHTTPSBind || settings.DirectPublicEndpoint != plan.Namespace.DirectEndpointURL {
		return ErrPRDUpgradeGuard
	}
	return nil
}

func prdServiceArtifactPaths(plan PRDUpgradePlan) []string {
	if plan.Namespace.Host == config.HostKindMac {
		return []string{plan.Namespace.LocalAgentPlist, plan.Namespace.LocalDAgentPlist}
	}
	return []string{filepath.Join("/etc/systemd/system", plan.Namespace.RunnerUnit)}
}

func ensurePRDBackupCapacity(plan PRDUpgradePlan, databasePath string) error {
	available, required, err := measurePRDBackupCapacity(plan, databasePath)
	if err != nil {
		return err
	}
	if available < required {
		return fmt.Errorf("%w: available=%d required=%d bytes for backup and rollback headroom", ErrPRDUpgradeGuard, available, required)
	}
	return nil
}

// ensurePRDCatalogOperatingBuffer keeps the owner-selected 0.5 GB operating buffer
// separate from the 64 MiB selector reserve. The existing backup estimate
// already includes the consistent DB copy, current binaries/config/secrets,
// and the selector reserve. Catalog releases are already sealed in place, so
// selector activation creates no second executable staging copy.
func ensurePRDCatalogOperatingBuffer(plan PRDUpgradePlan, databasePath string) error {
	available, backupAndSelectorRequired, err := measurePRDBackupCapacity(plan, databasePath)
	if err != nil {
		return err
	}
	required, err := prdCatalogPreBackupRequiredBytes(backupAndSelectorRequired)
	if err != nil {
		return fmt.Errorf("%w: catalog pre-backup capacity calculation overflow", ErrPRDUpgradeGuard)
	}
	if available < required {
		return fmt.Errorf("%w: catalog pre-backup free space is insufficient: available=%d required=%d (backup and selector reserve=%d, operating buffer=%d)",
			ErrPRDUpgradeGuard, available, required, backupAndSelectorRequired, prdCatalogOperatingBufferBytes)
	}
	return nil
}

// GB is decimal here: the owner's 0.5 GB policy is 500,000,000 bytes.
const prdCatalogOperatingBufferBytes uint64 = 500_000_000

func prdCatalogPreBackupRequiredBytes(backupAndSelectorRequired uint64) (uint64, error) {
	if backupAndSelectorRequired > ^uint64(0)-prdCatalogOperatingBufferBytes {
		return 0, ErrPRDUpgradeGuard
	}
	return backupAndSelectorRequired + prdCatalogOperatingBufferBytes, nil
}

func measurePRDBackupCapacity(plan PRDUpgradePlan, databasePath string) (uint64, uint64, error) {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(plan.Namespace.ServiceRoot, &filesystem); err != nil {
		return 0, 0, fmt.Errorf("inspect existing PRD filesystem capacity: %w", err)
	}
	databaseBytes, err := prdExistingFileSize(databasePath, false)
	if err != nil {
		return 0, 0, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecarBytes, err := prdExistingFileSize(databasePath+suffix, true)
		if err != nil {
			return 0, 0, err
		}
		databaseBytes += sidecarBytes
	}
	copyBytes, err := estimatePRDFileBackupBytes(plan)
	if err != nil {
		return 0, 0, err
	}
	available := uint64(filesystem.Bavail) * uint64(filesystem.Bsize)
	required := databaseBytes*2 + copyBytes + 64*1024*1024
	return available, required, nil
}

func prdExistingFileSize(path string, optional bool) (uint64, error) {
	info, err := os.Lstat(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 {
		return 0, fmt.Errorf("%w: PRD database or SQLite sidecar has an invalid file type", ErrPRDUpgradeGuard)
	}
	return uint64(info.Size()), nil
}

func estimatePRDFileBackupBytes(plan PRDUpgradePlan) (uint64, error) {
	var total uint64
	for _, root := range []string{plan.Namespace.Binaries, filepath.Join(plan.Namespace.ServiceRoot, "config"), filepath.Join(plan.Namespace.ServiceRoot, "secrets")} {
		_, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrPRDUpgradeGuard
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return ErrPRDUpgradeGuard
			}
			total += uint64(info.Size())
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("estimate existing PRD backup payload: %w", err)
		}
	}
	for _, path := range prdServiceArtifactPaths(plan) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("%w: PRD service definition is unavailable for backup", ErrPRDUpgradeGuard)
		}
		total += uint64(info.Size())
	}
	return total, nil
}

func ensurePRDPrivateDirectory(path string) error {
	var info unix.Stat_t
	if err := unix.Lstat(path, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != uint32(os.Getuid()) || info.Mode&0o777 != 0o700 {
		return fmt.Errorf("%w: PRD backup root must exist as an owner-only directory", ErrPRDUpgradeGuard)
	}
	return nil
}

func snapshotDirectory(source, serviceRoot, backupRoot string) ([]PRDBackupFile, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: unexpected PRD directory %s", ErrPRDUpgradeGuard, source)
	}
	var files []PRDBackupFile
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic link in PRD backup input", ErrPRDUpgradeGuard)
		}
		if entry.IsDir() {
			var directory unix.Stat_t
			if err := unix.Lstat(path, &directory); err != nil || directory.Mode&unix.S_IFMT != unix.S_IFDIR || directory.Uid != uint32(os.Getuid()) || directory.Mode&0o077 != 0 {
				return fmt.Errorf("%w: PRD backup directory ownership or mode is unsafe", ErrPRDUpgradeGuard)
			}
			return nil
		}
		relative, err := filepath.Rel(serviceRoot, path)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrPRDUpgradeGuard
		}
		files = append(files, PRDBackupFile{})
		files[len(files)-1], err = snapshotFile(path, relative, backupRoot)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot existing PRD directory: %w", err)
	}
	return files, nil
}

func snapshotFile(source, relative, backupRoot string) (PRDBackupFile, error) {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return PRDBackupFile{}, fmt.Errorf("%w: PRD backup input must be a regular file", ErrPRDUpgradeGuard)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return PRDBackupFile{}, err
	}
	destination := filepath.Join(backupRoot, relative)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return PRDBackupFile{}, err
	}
	if err := writePrivateSynced(destination, data, info.Mode().Perm()); err != nil {
		return PRDBackupFile{}, err
	}
	digest := sha256.Sum256(data)
	return PRDBackupFile{TargetPath: source, BackupPath: relative, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Mode: uint32(info.Mode().Perm())}, nil
}

func writePrivateSynced(path string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func loadPRDBackupManifest(plan PRDUpgradePlan, directory string) (PRDBackupManifest, error) {
	var manifest PRDBackupManifest
	clean, err := filepath.Abs(directory)
	if err != nil || filepath.Clean(clean) != clean || !pathContained(plan.Namespace.Backups, clean) {
		return manifest, ErrPRDUpgradeGuard
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return manifest, ErrPRDUpgradeGuard
	}
	manifestPath := filepath.Join(clean, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 || manifestInfo.Mode().Perm() != 0o600 {
		return manifest, ErrPRDUpgradeGuard
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil || len(data) == 0 || len(data) > 1<<20 {
		return manifest, ErrPRDUpgradeGuard
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.FormatVersion != prdUpgradeBackupFormat || manifest.HostID != plan.HostID || !prdDigestPattern.MatchString(manifest.BundleSHA256) || !prdDigestPattern.MatchString(manifest.DatabaseSHA256) {
		return PRDBackupManifest{}, ErrPRDUpgradeGuard
	}
	if manifest.DatabasePath != plan.Namespace.Database || !pathContained(clean, manifest.DatabaseBackupPath) || filepath.Base(manifest.DatabaseBackupPath) != "state.db.online" {
		return PRDBackupManifest{}, ErrPRDUpgradeGuard
	}
	seenTargets := make(map[string]struct{}, len(manifest.Files))
	seenBackups := make(map[string]struct{}, len(manifest.Files))
	for _, file := range manifest.Files {
		if !validPRDBackupFileEntry(plan, clean, file, seenTargets, seenBackups) {
			return PRDBackupManifest{}, ErrPRDUpgradeGuard
		}
	}
	databaseInfo, err := os.Lstat(manifest.DatabaseBackupPath)
	if err != nil || !databaseInfo.Mode().IsRegular() || databaseInfo.Mode()&os.ModeSymlink != 0 || databaseInfo.Mode().Perm() != 0o600 {
		return PRDBackupManifest{}, ErrPRDUpgradeGuard
	}
	digest, err := fileSHA256(manifest.DatabaseBackupPath)
	if err != nil || digest != manifest.DatabaseSHA256 {
		return PRDBackupManifest{}, ErrPRDUpgradeGuard
	}
	for _, file := range manifest.Files {
		backupPath := filepath.Join(clean, file.BackupPath)
		if err := validatePRDBackupFile(clean, backupPath, file); err != nil {
			return PRDBackupManifest{}, err
		}
	}
	return manifest, nil
}

func validatePRDBackupFile(root, path string, file PRDBackupFile) error {
	if !pathContained(root, path) {
		return ErrPRDUpgradeGuard
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return ErrPRDUpgradeGuard
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return ErrPRDUpgradeGuard
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != file.Bytes || uint32(info.Mode().Perm()) != file.Mode {
		return ErrPRDUpgradeGuard
	}
	digest, err := fileSHA256(path)
	if err != nil || digest != file.SHA256 {
		return ErrPRDUpgradeGuard
	}
	return nil
}

func validPRDBackupFileEntry(plan PRDUpgradePlan, backupRoot string, file PRDBackupFile, seenTargets, seenBackups map[string]struct{}) bool {
	target, targetErr := filepath.Abs(file.TargetPath)
	backupRelative := filepath.Clean(file.BackupPath)
	backup := filepath.Join(backupRoot, backupRelative)
	if targetErr != nil || filepath.Clean(target) != file.TargetPath || file.BackupPath == "" || filepath.IsAbs(file.BackupPath) || backupRelative != file.BackupPath ||
		backupRelative == ".." || strings.HasPrefix(backupRelative, ".."+string(filepath.Separator)) || !pathContained(backupRoot, backup) ||
		!prdDigestPattern.MatchString(file.SHA256) || file.Bytes < 0 || file.Mode&^uint32(0o777) != 0 {
		return false
	}
	if _, exists := seenTargets[target]; exists {
		return false
	}
	if _, exists := seenBackups[backupRelative]; exists {
		return false
	}
	serviceRelative, err := filepath.Rel(plan.Namespace.ServiceRoot, target)
	serviceTarget := err == nil && !filepath.IsAbs(serviceRelative) && serviceRelative != ".." && !strings.HasPrefix(serviceRelative, ".."+string(filepath.Separator))
	if serviceTarget {
		first := strings.Split(serviceRelative, string(filepath.Separator))[0]
		if first != "bin" && first != "config" && first != "secrets" || backupRelative != serviceRelative {
			return false
		}
	} else {
		allowed := false
		for _, servicePath := range prdServiceArtifactPaths(plan) {
			if target == servicePath && backupRelative == filepath.Join("service", filepath.Base(servicePath)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	seenTargets[target] = struct{}{}
	seenBackups[backupRelative] = struct{}{}
	return true
}

func verifyPRDBackupTargets(manifest PRDBackupManifest, includeBinaries bool) error {
	for _, file := range manifest.Files {
		if !includeBinaries && strings.HasPrefix(filepath.Clean(file.BackupPath), "bin"+string(filepath.Separator)) {
			continue
		}
		info, err := os.Lstat(file.TargetPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || uint32(info.Mode().Perm()) != file.Mode {
			return fmt.Errorf("%w: original PRD configuration/service artifact unavailable", ErrPRDUpgradeGuard)
		}
		digest, err := fileSHA256(file.TargetPath)
		if err != nil || digest != file.SHA256 || info.Size() != file.Bytes {
			return fmt.Errorf("%w: PRD configuration or service definition changed after backup", ErrPRDUpgradeGuard)
		}
	}
	return nil
}

func pathContained(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func verifyNoNewPRDWork(ctx context.Context, livePath, backupPath string) error {
	live, err := store.OpenExistingRestartPreflightReadOnly(ctx, livePath)
	if err != nil {
		return err
	}
	backup, err := store.OpenExistingRestartPreflightReadOnly(ctx, backupPath)
	if err != nil {
		_ = live.Close()
		return err
	}
	defer live.Close()
	defer backup.Close()
	for _, item := range []struct{ table, column string }{{"exec_sessions", "session_id"}, {"exec_commands", "command_id"}, {"exec_jobs", "job_id"}, {"local_intents", "intent_id"}} {
		liveIDs, liveErr := readIdentityColumn(ctx, live, item.table, item.column)
		backupIDs, backupErr := readIdentityColumn(ctx, backup, item.table, item.column)
		if liveErr != nil || backupErr != nil || !equalPRDStrings(liveIDs, backupIDs) {
			return fmt.Errorf("%w: PRD work identities changed since the consistent backup", ErrPRDUpgradeGuard)
		}
	}
	return nil
}

func readIdentityColumn(ctx context.Context, database interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table, column string) ([]string, error) {
	if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) || !regexp.MustCompile(`^[a-z_]+$`).MatchString(column) {
		return nil, ErrPRDUpgradeGuard
	}
	var exists int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return []string{}, nil
	}
	rows, err := database.QueryContext(ctx, "SELECT "+column+" FROM "+table+" ORDER BY "+column)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func equalPRDStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func installPRDBundleFiles(binaryRoot string, bundle SimpleBundle, backup PRDBackupManifest) ([]PRDBackupFile, error) {
	var info unix.Stat_t
	if err := unix.Lstat(binaryRoot, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != uint32(os.Getuid()) || info.Mode&0o777 != 0o700 {
		return nil, ErrPRDUpgradeGuard
	}
	installed := make([]PRDBackupFile, 0, len(bundle.Components))
	for name, contents := range bundle.Components {
		if !simpleBundleComponentAllowed(bundle.Manifest.HostID, name) {
			return installed, ErrInvalidSimpleBundle
		}
		target := filepath.Join(binaryRoot, name)
		if filepath.Dir(target) != binaryRoot {
			return installed, ErrInvalidSimpleBundle
		}
		if file, statErr := os.Lstat(target); statErr == nil && (!file.Mode().IsRegular() || file.Mode()&os.ModeSymlink != 0) {
			return installed, ErrPRDUpgradeGuard
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return installed, statErr
		}
		temporary, err := os.CreateTemp(binaryRoot, ".runner-prd-upgrade-*")
		if err != nil {
			return installed, err
		}
		temporaryPath := temporary.Name()
		if err := temporary.Chmod(0o700); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return installed, err
		}
		if _, err := temporary.Write(contents); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return installed, err
		}
		if err := temporary.Sync(); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return installed, err
		}
		if err := temporary.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return installed, err
		}
		if err := os.Rename(temporaryPath, target); err != nil {
			_ = os.Remove(temporaryPath)
			return installed, err
		}
		digest := sha256.Sum256(contents)
		installed = append(installed, PRDBackupFile{TargetPath: target, BackupPath: name, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(contents)), Mode: 0o700})
	}
	if err := syncDirectory(binaryRoot); err != nil {
		return installed, err
	}
	return installed, nil
}

func simpleBundleComponentAllowed(hostID, name string) bool {
	for _, expected := range simpleHostComponents[hostID] {
		if expected == name {
			return true
		}
	}
	return false
}

func restorePRDBinarySnapshot(binaryRoot string, hostID string, manifest PRDBackupManifest) error {
	var binaryDirectory unix.Stat_t
	if err := unix.Lstat(binaryRoot, &binaryDirectory); err != nil || binaryDirectory.Mode&unix.S_IFMT != unix.S_IFDIR || binaryDirectory.Uid != uint32(os.Getuid()) || binaryDirectory.Mode&0o777 != 0o700 {
		return ErrPRDUpgradeGuard
	}
	wanted := make(map[string]PRDBackupFile)
	for _, file := range manifest.Files {
		if filepath.Dir(file.TargetPath) == binaryRoot {
			wanted[filepath.Base(file.TargetPath)] = file
		}
	}
	entries, err := os.ReadDir(binaryRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, keep := wanted[entry.Name()]; !keep && simpleBundleComponentAllowed(hostID, entry.Name()) {
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return ErrPRDUpgradeGuard
			}
			if err := os.Remove(filepath.Join(binaryRoot, entry.Name())); err != nil {
				return err
			}
		}
	}
	for name, file := range wanted {
		backupPath := filepath.Join(filepath.Dir(manifest.DatabaseBackupPath), file.BackupPath)
		data, err := os.ReadFile(backupPath)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return ErrPRDUpgradeGuard
		}
		temporary, err := os.CreateTemp(binaryRoot, ".runner-prd-rollback-*")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		if err := temporary.Chmod(os.FileMode(file.Mode)); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		if _, err := temporary.Write(data); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Sync(); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := temporary.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
		if err := os.Rename(temporaryPath, filepath.Join(binaryRoot, name)); err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
	}
	return syncDirectory(binaryRoot)
}
