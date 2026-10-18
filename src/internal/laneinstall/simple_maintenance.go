package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

var (
	ErrSimpleLaneNotQuiescent = errors.New("selected lane is active or has retained work")
	ErrSimpleLanePreflight    = errors.New("selected lane state cannot be proven safe for maintenance")
	ErrSimpleLaneLockHeld     = errors.New("selected lane lifecycle lock is held")
	ErrSimpleBackupRequired   = errors.New("existing lane update requires a verified config and database backup")
)

const simpleProofMaxAge = 5 * time.Minute

type SimpleServiceProbe interface {
	Status(context.Context, Plan) (bool, error)
}

// SimpleQuiescenceProof is produced only after the selected service is stopped,
// its complete V3 config and SQLite authority are read successfully, and all
// durable restart-preflight counters are zero.
type SimpleQuiescenceProof struct {
	hostID       string
	lane         config.ReleaseLane
	databasePath string
	observedAt   time.Time
	metrics      store.RestartPreflightMetrics
	database     int
	current      string
}

func (p SimpleQuiescenceProof) Metrics() store.RestartPreflightMetrics { return p.metrics }
func (p SimpleQuiescenceProof) DatabaseSchema() int                    { return p.database }

type SimpleLaneBackupReceipt struct {
	hostID       string
	lane         config.ReleaseLane
	databasePath string
	activeBundle string
	database     int
	directory    string
	manifestHash string
}

func (r SimpleLaneBackupReceipt) Directory() string { return r.directory }

// SimpleLaneMaintenance holds the selected lane's existing lifecycle flock
// from the stopped-service check through backup and bundle activation. Runner
// startup uses the same lane lock, preventing an activation from racing a
// service start. Closing the lease always releases the lock.
type SimpleLaneMaintenance struct {
	plan    Plan
	service SimpleServiceProbe
	lockFD  int
	proof   SimpleQuiescenceProof
	fresh   bool
	backup  *SimpleLaneBackupReceipt
	closed  bool
}

// Fresh reports whether this lease targets a first install with no existing
// lane database, configuration, or active bundle.
func (m *SimpleLaneMaintenance) Fresh() bool {
	return m != nil && !m.closed && m.fresh
}

// BeginSimpleLaneMaintenance refuses to inspect or change another lane. A
// first install is accepted only when both its config and authority database
// are absent and no bundle is active. Existing state must be a valid V3 lane
// config with a readable, empty restart inventory.
func BeginSimpleLaneMaintenance(ctx context.Context, plan Plan, service SimpleServiceProbe) (*SimpleLaneMaintenance, error) {
	if validateSimplePlan(plan) != nil || service == nil {
		return nil, ErrSimpleLanePreflight
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("read selected lane service state before maintenance: %w", err)
	}
	if active {
		stopper, ok := service.(interface {
			Stop(context.Context, Plan) error
		})
		if !ok {
			return nil, ErrSimpleLaneNotQuiescent
		}
		if err := stopper.Stop(ctx, plan); err != nil {
			return nil, fmt.Errorf("gracefully stop selected lane and prove its terminal inventory: %w", err)
		}
	}
	lockFD, err := acquireSimpleLaneLock(plan)
	if err != nil {
		return nil, err
	}
	maintenance := &SimpleLaneMaintenance{plan: plan, service: service, lockFD: lockFD}
	fail := func(err error) (*SimpleLaneMaintenance, error) {
		_ = maintenance.Close()
		return nil, err
	}
	active, err = service.Status(ctx, plan)
	if err != nil {
		return fail(fmt.Errorf("read selected lane service state: %w", err))
	}
	if active {
		return fail(ErrSimpleLaneNotQuiescent)
	}
	current, err := simpleCurrentBundle(plan.Namespace.ServiceRoot)
	if err != nil {
		return fail(err)
	}
	configExists, err := simplePathExists(plan.Namespace.ConfigFile)
	if err != nil {
		return fail(err)
	}
	databaseExists, err := simplePathExists(plan.Namespace.Database)
	if err != nil {
		return fail(err)
	}
	marker, markerExists, err := readSimpleInstallMarker(plan.Namespace)
	if err != nil {
		return fail(err)
	}
	if current == "" && !configExists && !databaseExists {
		maintenance.fresh = true
		return maintenance, nil
	}
	if !databaseExists && markerExists && marker.HostID == plan.HostID && marker.Lane == plan.Lane {
		maintenance.fresh = true
		return maintenance, nil
	}
	if !configExists || !databaseExists {
		return fail(ErrSimpleLanePreflight)
	}
	proof, err := readSimpleLaneQuiescence(ctx, plan, current)
	if err != nil {
		return fail(err)
	}
	maintenance.proof = proof
	return maintenance, nil
}

func (m *SimpleLaneMaintenance) Backup(ctx context.Context) (SimpleLaneBackupReceipt, error) {
	if m == nil || m.closed || m.fresh || m.proof.hostID != m.plan.HostID || !simpleProofFresh(m.proof, m.plan) {
		return SimpleLaneBackupReceipt{}, ErrSimpleBackupRequired
	}
	if m.backup != nil {
		return *m.backup, nil
	}
	settings, err := loadSimpleLaneAuthority(m.plan)
	if err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, m.plan.Namespace.Database)
	if err != nil {
		return SimpleLaneBackupReceipt{}, ErrSimpleLanePreflight
	}
	defer database.Close()
	receipt, err := createSimpleLaneBackup(ctx, m.plan, settings, database, m.proof)
	if err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	m.backup = &receipt
	return receipt, nil
}

// InstallBundle installs only the validated staged bundle for this host. An
// existing lane requires both the stopped/empty proof and a verified complete
// config plus SQLite snapshot. It never edits the service unit or restarts it.
func (m *SimpleLaneMaintenance) InstallBundle(ctx context.Context, sourcePath string) (SimpleActivation, error) {
	if m == nil || m.closed || validateSimplePlan(m.plan) != nil {
		return SimpleActivation{}, ErrSimpleLanePreflight
	}
	active, err := m.service.Status(ctx, m.plan)
	if err != nil || active {
		return SimpleActivation{}, ErrSimpleLaneNotQuiescent
	}
	if !m.fresh {
		current, currentErr := simpleCurrentBundle(m.plan.Namespace.ServiceRoot)
		if currentErr != nil || current != m.proof.current {
			return SimpleActivation{}, ErrSimpleActivationConflict
		}
		proof, proofErr := readSimpleLaneQuiescence(ctx, m.plan, current)
		if proofErr != nil || proof.database != m.proof.database {
			return SimpleActivation{}, ErrSimpleLaneNotQuiescent
		}
		m.proof = proof
	}
	bundle, err := readSimpleBundleAt(sourcePath)
	if err != nil || bundle.Manifest.HostID != m.plan.HostID ||
		filepath.Clean(sourcePath) != filepath.Join(m.plan.Namespace.Staging, "bundles", bundle.Manifest.BundleSHA256) {
		return SimpleActivation{}, ErrInvalidSimpleBundle
	}
	if !m.fresh {
		if !simpleProofFresh(m.proof, m.plan) || m.backup == nil || !simpleBackupReceiptValid(m.plan, *m.backup, m.proof) {
			return SimpleActivation{}, ErrSimpleBackupRequired
		}
	}
	return installSimpleBundleAt(m.plan.Namespace.ServiceRoot, m.plan.HostID, m.plan.Lane, bundle)
}

// Rollback restores the previous immutable executable bundle. The selected
// service must be stopped again, the complete authority inventory empty, and a
// fresh backup captured before the pointer can move backward. When the prior
// bundle supports schema 39 and the selected database is exactly schema 40,
// the additive BUG-025 cleanup-work metadata is removed in one guarded
// migration transaction; application rows and configuration are preserved.
func (m *SimpleLaneMaintenance) Rollback(ctx context.Context, activation SimpleActivation) error {
	if m == nil || m.closed || m.fresh || activation.hostID != m.plan.HostID || activation.lane != m.plan.Lane {
		return ErrSimpleRollbackUnavailable
	}
	active, err := m.service.Status(ctx, m.plan)
	if err != nil || active {
		return ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundle(m.plan.Namespace.ServiceRoot)
	if err != nil || current != activation.activated {
		return ErrSimpleActivationConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.plan, current)
	if err != nil {
		return err
	}
	m.proof = proof
	// Never reuse the pre-install snapshot as the rollback preflight backup.
	// Keep it on disk and create a second fresh snapshot of the current state.
	m.backup = nil
	backup, err := m.Backup(ctx)
	if err != nil || !simpleBackupReceiptValid(m.plan, backup, m.proof) {
		return ErrSimpleBackupRequired
	}
	return rollbackSimpleActivation(ctx, m.plan, activation, proof.database)
}

// RollbackFromBackup reconstructs the previous immutable bundle from the
// verified pre-install backup manifest, so a process restart does not lose
// the rollback identity. It preserves the current SQLite authority and uses
// only the supported schema-40-to-39 removal when required by the prior binary.
func (m *SimpleLaneMaintenance) RollbackFromBackup(ctx context.Context, backupDirectory string) error {
	if m == nil || m.closed || m.fresh || validateSimplePlan(m.plan) != nil {
		return ErrSimpleRollbackUnavailable
	}
	active, err := m.service.Status(ctx, m.plan)
	if err != nil || active {
		return ErrSimpleLaneNotQuiescent
	}
	current, err := simpleCurrentBundle(m.plan.Namespace.ServiceRoot)
	if err != nil || current == "" {
		return ErrSimpleRollbackUnavailable
	}
	proof, err := readSimpleLaneQuiescence(ctx, m.plan, current)
	if err != nil {
		return err
	}
	manifest, err := readSimpleLaneBackupManifest(m.plan, backupDirectory)
	if err != nil || manifest.ActiveBundle == "" || manifest.ActiveBundle == current {
		return ErrSimpleRollbackUnavailable
	}
	if !bundleDigestPattern.MatchString(manifest.ActiveBundle) {
		return ErrSimpleRollbackUnavailable
	}
	m.proof = proof
	m.backup = nil
	backup, err := m.Backup(ctx)
	if err != nil || !simpleBackupReceiptValid(m.plan, backup, proof) {
		return ErrSimpleBackupRequired
	}
	activation := SimpleActivation{hostID: m.plan.HostID, lane: m.plan.Lane, previous: manifest.ActiveBundle, activated: current}
	return rollbackSimpleActivation(ctx, m.plan, activation, proof.database)
}

func rollbackSimpleActivation(ctx context.Context, plan Plan, activation SimpleActivation, schema int) error {
	if validateSimplePlan(plan) != nil || activation.hostID != plan.HostID || activation.lane != plan.Lane ||
		!bundleDigestPattern.MatchString(activation.previous) || !bundleDigestPattern.MatchString(activation.activated) || schema <= 0 {
		return ErrSimpleRollbackUnavailable
	}
	return rollbackSimpleActivationAt(ctx, plan.Namespace.ServiceRoot, plan.Namespace.Database, plan.HostID, plan.Lane, activation, schema)
}

// rollbackSimpleActivationAt contains the filesystem/database transaction for
// a previously validated plan. The public maintenance paths validate the
// canonical host/lane namespace before calling it; tests use a private
// temporary namespace so they never touch an installed service tree.
func rollbackSimpleActivationAt(ctx context.Context, serviceRoot, databasePath, hostID string, lane config.ReleaseLane, activation SimpleActivation, schema int) error {
	if filepath.Clean(serviceRoot) != serviceRoot || !filepath.IsAbs(serviceRoot) ||
		filepath.Clean(databasePath) != databasePath || !filepath.IsAbs(databasePath) ||
		activation.hostID != hostID || activation.lane != lane ||
		!bundleDigestPattern.MatchString(activation.previous) || !bundleDigestPattern.MatchString(activation.activated) || schema <= 0 {
		return ErrSimpleRollbackUnavailable
	}
	previousPath := filepath.Join(serviceRoot, "releases", activation.previous)
	previous, err := readSimpleBundleAt(previousPath)
	if err != nil || previous.Manifest.HostID != hostID {
		return ErrSimpleRollbackUnavailable
	}
	if schema > previous.Manifest.DatabaseSchemaMax {
		if schema != store.CurrentSchemaVersion || previous.Manifest.DatabaseSchemaMax != 39 {
			return ErrSimpleRollbackUnavailable
		}
		if err := store.RollbackBoundedEventCleanupSchema40To39(ctx, databasePath); err != nil {
			return fmt.Errorf("remove only the additive BUG-025 schema before bundle rollback: %w", err)
		}
		database, err := store.OpenExistingRestartPreflightReadOnly(ctx, databasePath)
		if err != nil {
			return fmt.Errorf("verify authority after BUG-025 schema rollback: %w", err)
		}
		if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
			_ = database.Close()
			return fmt.Errorf("read authority schema after BUG-025 rollback: %w", err)
		}
		if err := database.Close(); err != nil {
			return fmt.Errorf("close authority after BUG-025 schema rollback: %w", err)
		}
	}
	if schema > previous.Manifest.DatabaseSchemaMax {
		return ErrSimpleRollbackUnavailable
	}
	return rollbackSimpleBundleAt(serviceRoot, hostID, lane, activation, schema)
}

func (m *SimpleLaneMaintenance) Close() error {
	if m == nil || m.closed {
		return nil
	}
	m.closed = true
	if m.lockFD < 0 {
		return nil
	}
	unlockErr := unix.Flock(m.lockFD, unix.LOCK_UN)
	closeErr := unix.Close(m.lockFD)
	m.lockFD = -1
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func readSimpleLaneQuiescence(ctx context.Context, plan Plan, current string) (SimpleQuiescenceProof, error) {
	_, err := loadSimpleLaneAuthority(plan)
	if err != nil {
		return SimpleQuiescenceProof{}, err
	}
	return readSimpleLaneDatabaseQuiescence(ctx, plan, current)
}

func readSimpleLaneDatabaseQuiescence(ctx context.Context, plan Plan, current string) (SimpleQuiescenceProof, error) {
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return SimpleQuiescenceProof{}, ErrSimpleLanePreflight
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return SimpleQuiescenceProof{}, ErrSimpleLanePreflight
	}
	metrics, err := authority.ReadRestartPreflightMetrics(ctx)
	if err != nil || !simpleMetricsEmpty(metrics) {
		return SimpleQuiescenceProof{metrics: metrics}, ErrSimpleLaneNotQuiescent
	}
	var schema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema <= 0 {
		return SimpleQuiescenceProof{}, ErrSimpleLanePreflight
	}
	return SimpleQuiescenceProof{
		hostID: plan.HostID, lane: plan.Lane, databasePath: plan.Namespace.Database,
		observedAt: time.Now().UTC(), metrics: metrics, database: schema, current: current,
	}, nil
}

func loadSimpleLaneAuthority(plan Plan) (string, error) {
	loaded, err := config.LoadFile(plan.Namespace.ConfigFile)
	if err != nil || !simpleLoadedConfigMatches(plan, loaded) {
		return "", ErrSimpleLanePreflight
	}
	if plan.Namespace.Host == config.HostKindMac {
		settings, ok := loaded.MacSettings()
		if !ok || settings.Account != plan.Namespace.Account || settings.ServiceRoot != plan.Namespace.ServiceRoot || settings.Database != plan.Namespace.Database {
			return "", ErrSimpleLanePreflight
		}
		return settings.Database, nil
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || settings.Account != plan.Namespace.Account || settings.ServiceRoot != plan.Namespace.ServiceRoot || settings.Database != plan.Namespace.Database || settings.PrivateSocket != plan.Namespace.PrivateSocket {
		return "", ErrSimpleLanePreflight
	}
	return settings.Database, nil
}

func simpleMetricsEmpty(metrics store.RestartPreflightMetrics) bool {
	return metrics.ActiveSessionSlots == 0 && metrics.ActiveCommandSlots == 0 && metrics.QueuedCommands == 0 && metrics.ResumableJobs == 0
}

func simpleProofFresh(proof SimpleQuiescenceProof, plan Plan) bool {
	age := time.Since(proof.observedAt)
	return proof.hostID == plan.HostID && proof.lane == plan.Lane && proof.databasePath == plan.Namespace.Database &&
		proof.database > 0 && simpleMetricsEmpty(proof.metrics) && age >= 0 && age <= simpleProofMaxAge
}

type simpleBackupManifest struct {
	FormatVersion  int                `json:"format_version"`
	HostID         string             `json:"host_id"`
	Lane           config.ReleaseLane `json:"lane"`
	DatabasePath   string             `json:"database_path"`
	ActiveBundle   string             `json:"active_bundle_sha256"`
	DatabaseSchema int                `json:"database_schema"`
	ConfigSHA256   string             `json:"config_sha256"`
	DatabaseSHA256 string             `json:"database_sha256"`
	CreatedAt      time.Time          `json:"created_at"`
}

func createSimpleLaneBackup(ctx context.Context, plan Plan, databasePath string, database *sql.DB, proof SimpleQuiescenceProof) (SimpleLaneBackupReceipt, error) {
	if !simpleProofFresh(proof, plan) || databasePath != plan.Namespace.Database || database == nil {
		return SimpleLaneBackupReceipt{}, ErrSimpleBackupRequired
	}
	if err := ensureSimpleDirectoryPath(plan.Namespace.Backups); err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	temporary, err := os.MkdirTemp(plan.Namespace.Backups, ".simple-backup-*")
	if err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	configBytes, err := os.ReadFile(plan.Namespace.ConfigFile)
	if err != nil || len(configBytes) == 0 {
		return SimpleLaneBackupReceipt{}, ErrSimpleLanePreflight
	}
	configPath := filepath.Join(temporary, "config.yaml")
	databaseCopyPath := filepath.Join(temporary, "authority.sqlite")
	if err := writePrivateFile(configPath, configBytes, 0o600); err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	var sourceSchema int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&sourceSchema); err != nil || sourceSchema != proof.database {
		return SimpleLaneBackupReceipt{}, ErrSimpleLanePreflight
	}
	// This backup is taken before the candidate starts and applies its schema
	// migrations. Validate it as an existing supported authority rather than
	// requiring the current executable schema, then prove the snapshot retained
	// exactly the source schema recorded in the quiescence proof.
	if err := store.CreateExistingOnlineBackup(ctx, database, databaseCopyPath); err != nil {
		return SimpleLaneBackupReceipt{}, fmt.Errorf("create consistent lane SQLite backup: %w", err)
	}
	backup, err := store.OpenExistingRestartPreflightReadOnly(ctx, databaseCopyPath)
	if err != nil {
		return SimpleLaneBackupReceipt{}, fmt.Errorf("verify consistent lane SQLite backup: %w", err)
	}
	var backupSchema int
	backupSchemaErr := backup.QueryRowContext(ctx, "PRAGMA user_version").Scan(&backupSchema)
	closeBackupErr := backup.Close()
	if backupSchemaErr != nil || closeBackupErr != nil || backupSchema != sourceSchema {
		return SimpleLaneBackupReceipt{}, ErrSimpleLanePreflight
	}
	configHash := sha256.Sum256(configBytes)
	databaseBytes, err := os.ReadFile(databaseCopyPath)
	if err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	databaseHash := sha256.Sum256(databaseBytes)
	manifest := simpleBackupManifest{
		FormatVersion: 1, HostID: plan.HostID, Lane: plan.Lane, DatabasePath: plan.Namespace.Database,
		ActiveBundle: proof.current, DatabaseSchema: proof.database,
		ConfigSHA256: hex.EncodeToString(configHash[:]), DatabaseSHA256: hex.EncodeToString(databaseHash[:]), CreatedAt: time.Now().UTC(),
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writePrivateFile(filepath.Join(temporary, "manifest.json"), manifestBytes, 0o600); err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	if err := syncSimpleDirectory(temporary); err != nil {
		return SimpleLaneBackupReceipt{}, err
	}
	manifestHash := sha256.Sum256(manifestBytes)
	receipt := SimpleLaneBackupReceipt{
		hostID: plan.HostID, lane: plan.Lane, databasePath: plan.Namespace.Database,
		activeBundle: proof.current, database: proof.database, directory: temporary,
		manifestHash: hex.EncodeToString(manifestHash[:]),
	}
	keep = true
	return receipt, nil
}

func simpleBackupReceiptValid(plan Plan, receipt SimpleLaneBackupReceipt, proof SimpleQuiescenceProof) bool {
	if receipt.hostID != plan.HostID || receipt.lane != plan.Lane || receipt.databasePath != plan.Namespace.Database ||
		receipt.activeBundle != proof.current || receipt.database != proof.database || !simpleProofFresh(proof, plan) ||
		filepath.Dir(receipt.directory) != plan.Namespace.Backups {
		return false
	}
	info, err := os.Lstat(receipt.directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	manifestPath := filepath.Join(receipt.directory, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || manifestInfo.Mode().Perm() != 0o600 {
		return false
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return false
	}
	manifestHash := sha256.Sum256(manifestBytes)
	if hex.EncodeToString(manifestHash[:]) != receipt.manifestHash {
		return false
	}
	var manifest simpleBackupManifest
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.FormatVersion != 1 || manifest.HostID != plan.HostID ||
		manifest.Lane != plan.Lane || manifest.DatabasePath != plan.Namespace.Database || manifest.ActiveBundle != proof.current || manifest.DatabaseSchema != proof.database {
		return false
	}
	for name, want := range map[string]string{"config.yaml": manifest.ConfigSHA256, "authority.sqlite": manifest.DatabaseSHA256} {
		path := filepath.Join(receipt.directory, name)
		fileInfo, err := os.Lstat(path)
		if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 {
			return false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != want {
			return false
		}
	}
	return true
}

func readSimpleLaneBackupManifest(plan Plan, directory string) (simpleBackupManifest, error) {
	if filepath.Clean(directory) != directory || !filepath.IsAbs(directory) || filepath.Dir(directory) != plan.Namespace.Backups ||
		!strings.HasPrefix(filepath.Base(directory), ".simple-backup-") {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	var directoryStat unix.Stat_t
	if err := unix.Lstat(directory, &directoryStat); err != nil || directoryStat.Uid != uint32(os.Getuid()) {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != 0o600 {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	var manifestStat unix.Stat_t
	if err := unix.Lstat(manifestPath, &manifestStat); err != nil || manifestStat.Uid != uint32(os.Getuid()) {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest simpleBackupManifest
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		manifest.FormatVersion != 1 || manifest.HostID != plan.HostID || manifest.Lane != plan.Lane ||
		manifest.DatabasePath != plan.Namespace.Database || manifest.DatabaseSchema <= 0 || manifest.CreatedAt.IsZero() {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	if manifest.ActiveBundle != "" && !bundleDigestPattern.MatchString(manifest.ActiveBundle) {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	for name, expected := range map[string]string{"config.yaml": manifest.ConfigSHA256, "authority.sqlite": manifest.DatabaseSHA256} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
		}
		var fileStat unix.Stat_t
		if err := unix.Lstat(path, &fileStat); err != nil || fileStat.Uid != uint32(os.Getuid()) {
			return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != expected {
			return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
		}
	}
	backupDatabase := filepath.Join(directory, "authority.sqlite")
	database, err := store.OpenExistingRestartPreflightReadOnly(context.Background(), backupDatabase)
	if err != nil {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	defer database.Close()
	var schema int
	if err := database.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != manifest.DatabaseSchema {
		return simpleBackupManifest{}, ErrSimpleRollbackUnavailable
	}
	return manifest, nil
}

func simplePathExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, ErrSimpleLanePreflight
	}
	return true, nil
}

func acquireSimpleLaneLock(plan Plan) (int, error) {
	if err := ensureSimpleDirectoryPath(filepath.Dir(plan.Namespace.LifecycleLock)); err != nil {
		return -1, err
	}
	uid := uint32(os.Getuid())
	var lockDirectory unix.Stat_t
	if err := unix.Lstat(filepath.Dir(plan.Namespace.LifecycleLock), &lockDirectory); err != nil ||
		lockDirectory.Mode&unix.S_IFMT != unix.S_IFDIR || lockDirectory.Uid != uid || lockDirectory.Mode&0o077 != 0 {
		return -1, ErrSimpleLanePreflight
	}
	fd, err := unix.Open(plan.Namespace.LifecycleLock, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return -1, fmt.Errorf("open selected lane lifecycle lock: %w", err)
	}
	closeFD := func() { _ = unix.Close(fd) }
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		closeFD()
		return -1, err
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Uid != uid || opened.Mode&0o777 != 0o600 {
		closeFD()
		return -1, ErrSimpleLanePreflight
	}
	var named unix.Stat_t
	if err := unix.Lstat(plan.Namespace.LifecycleLock, &named); err != nil || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Dev != opened.Dev || named.Ino != opened.Ino {
		closeFD()
		return -1, ErrSimpleLanePreflight
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeFD()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return -1, ErrSimpleLaneLockHeld
		}
		return -1, err
	}
	return fd, nil
}
