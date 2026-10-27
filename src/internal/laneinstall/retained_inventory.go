package laneinstall

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/user"
	"reflect"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/buildinfo"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

// ErrCatalogRetainedInventory deliberately omits database errors, which can
// contain SQL or paths unrelated to the selected metadata authority.
var ErrCatalogRetainedInventory = errors.New("selected retained-work inventory is unavailable or incomplete")

// CatalogRetainedDatabaseIdentity binds the read cut to the selected private
// authority file. It is not a content hash: live SQLite files can change while
// a read transaction retains its coherent WAL snapshot.
type CatalogRetainedDatabaseIdentity struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// CatalogRetainedSelectedArtifact describes the verified selector, not an
// assertion about the executable mapped into an already-running process.
type CatalogRetainedSelectedArtifact struct {
	Kind           string `json:"kind"`
	SelectorTarget string `json:"selector_target"`
	ReleaseID      string `json:"release_id,omitempty"`
	BundleSHA256   string `json:"bundle_sha256"`
	SourceRevision string `json:"source_revision"`
	ReadSchemaMin  int    `json:"read_schema_min"`
	ReadSchemaMax  int    `json:"read_schema_max"`
	Toolchain      string `json:"toolchain"`
}

// CatalogRetainedWorkInventoryReport exports metadata from one SQLite read
// cut. Completeness concerns that durable authority only; it proves neither
// runtime cleanup nor recovery permission, and never authorizes resumption.
type CatalogRetainedWorkInventoryReport struct {
	FormatVersion           int                             `json:"format_version"`
	InventoryComplete       bool                            `json:"inventory_complete"`
	SafeToResume            string                          `json:"safe_to_resume"`
	RecoveryAuthorized      bool                            `json:"recovery_authorized"`
	HostID                  string                          `json:"host_id"`
	Lane                    config.ReleaseLane              `json:"lane"`
	ObservationStartedAt    string                          `json:"observation_started_at"`
	ObservationCompletedAt  string                          `json:"observation_completed_at"`
	ServiceActive           bool                            `json:"service_active"`
	DatabaseIdentity        CatalogRetainedDatabaseIdentity `json:"database_identity"`
	ConfigurationSHA256     string                          `json:"configuration_sha256"`
	DatabaseSchema          int                             `json:"database_schema"`
	SelectedArtifact        CatalogRetainedSelectedArtifact `json:"selected_artifact"`
	DiagnosticBuildRevision string                          `json:"diagnostic_build_revision"`
	WorkInventory           store.RetainedWorkInventory     `json:"work_inventory"`
}

// ReadCatalogRetainedWorkInventory reads the selected Dev/QAS authority while
// services and retained holds may remain present. It has no mutation path.
func ReadCatalogRetainedWorkInventory(ctx context.Context, plan Plan, service SimpleServiceProbe, maxRows int) (CatalogRetainedWorkInventoryReport, error) {
	if validateSimplePlan(plan) != nil || service == nil || !retainedInventoryAccountAndPlatform(plan.HostID, plan.Namespace.Account) {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	validate := func() error {
		_, err := loadSimpleLaneAuthority(plan)
		return err
	}
	return readCatalogRetainedWorkInventoryForPlan(ctx, plan, func(ctx context.Context) (bool, error) {
		return service.Status(ctx, plan)
	}, validate, maxRows)
}

// ReadCatalogPRDRetainedWorkInventory uses the existing unsuffixed authority
// without entering PRD preflight, backup, installation or recovery commands.
func ReadCatalogPRDRetainedWorkInventory(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, maxRows int) (CatalogRetainedWorkInventoryReport, error) {
	if validatePRDPlan(plan) != nil || service == nil || !retainedInventoryAccountAndPlatform(plan.HostID, plan.Namespace.Account) {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	catalogPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	return readCatalogRetainedWorkInventoryForPlan(ctx, catalogPlan, func(ctx context.Context) (bool, error) {
		return service.Status(ctx, plan)
	}, func() error { return validatePRDConfig(plan) }, maxRows)
}

func retainedInventoryAccountAndPlatform(hostID, account string) bool {
	if os.Getuid() <= 0 || os.Getuid() != os.Geteuid() || !CatalogHostMatchesPlatform(hostID, runtime.GOOS, runtime.GOARCH) {
		return false
	}
	current, err := user.Current()
	if err != nil || current.Username != account {
		return false
	}
	uid, err := strconv.Atoi(current.Uid)
	return err == nil && uid == os.Getuid()
}

func readCatalogRetainedWorkInventoryForPlan(ctx context.Context, plan Plan, serviceStatus func(context.Context) (bool, error), validateAuthority func() error, maxRows int) (CatalogRetainedWorkInventoryReport, error) {
	if validateCatalogPlan(plan) != nil || serviceStatus == nil || validateAuthority == nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	return readCatalogRetainedWorkInventoryAt(ctx, layout, serviceStatus, validateAuthority, maxRows, store.ReadRetainedWorkInventory)
}

type retainedInventoryReader func(context.Context, *sql.DB, int) (store.RetainedWorkInventory, error)

// The injectable reader is limited to this private fixture seam. Public
// commands always use the store's schema-40, single-cut metadata reader.
func readCatalogRetainedWorkInventoryAt(ctx context.Context, layout catalogSelectorLayout, serviceStatus func(context.Context) (bool, error), validateAuthority func() error, maxRows int, readInventory retainedInventoryReader) (CatalogRetainedWorkInventoryReport, error) {
	if validateCatalogSelectorLayout(layout) != nil || serviceStatus == nil || validateAuthority == nil || readInventory == nil ||
		maxRows < 0 || maxRows > store.MaxRetainedWorkInventoryRows {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if validateAuthority() != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	selected, err := inspectCatalogSelector(layout)
	if err != nil || selected.Kind == "unselected" {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	configurationSHA, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	identity, err := retainedInventoryDatabaseIdentity(layout.databasePath)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	// SQLite may create WAL shared-memory files even with mode=ro when they
	// are absent. This diagnostic accepts only an already-open WAL authority
	// with its complete private sidecar pair; it never uses immutable=1, which
	// would hide committed WAL work and invalidate the inventory.
	sidecars, err := retainedInventorySidecarIdentities(layout.databasePath)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	active, err := serviceStatus(ctx)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	database, err := store.OpenExistingCurrentReadOnly(ctx, layout.databasePath)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	openedIdentity, err := retainedInventoryDatabaseIdentity(layout.databasePath)
	openedSidecars, sidecarErr := retainedInventorySidecarIdentities(layout.databasePath)
	if err != nil || openedIdentity != identity || sidecarErr != nil || openedSidecars != sidecars {
		_ = database.Close()
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	inventory, readErr := readInventory(ctx, database, maxRows)
	closeErr := database.Close()
	if readErr != nil || closeErr != nil || !inventory.Complete || inventory.SchemaVersion != 40 || ctx.Err() != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	finalIdentity, err := retainedInventoryDatabaseIdentity(layout.databasePath)
	finalSidecars, sidecarErr := retainedInventorySidecarIdentities(layout.databasePath)
	if err != nil || finalIdentity != identity || sidecarErr != nil || finalSidecars != sidecars {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	finalConfigurationSHA, err := hashCatalogConfiguration(layout.configFile)
	if err != nil || finalConfigurationSHA != configurationSHA || validateAuthority() != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	finalSelection, err := inspectCatalogSelector(layout)
	if err != nil || !reflect.DeepEqual(selected, finalSelection) {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	finalActive, err := serviceStatus(ctx)
	if err != nil || active != finalActive || ctx.Err() != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	artifact := CatalogRetainedSelectedArtifact{
		Kind: selected.Kind, SelectorTarget: selected.Target, BundleSHA256: selected.Bundle.BundleSHA256,
		SourceRevision: selected.Bundle.SourceRevision, ReadSchemaMin: 1,
		ReadSchemaMax: selected.Bundle.DatabaseSchemaMax, Toolchain: selected.Bundle.Toolchain,
	}
	if selected.Release != nil {
		artifact.ReleaseID = selected.Release.Identity.ReleaseID
		artifact.BundleSHA256 = selected.Release.Identity.BundleSHA256
		artifact.SourceRevision = selected.Release.SourceRevision
		artifact.ReadSchemaMin = selected.Release.Database.ReadSchemaMin
		artifact.ReadSchemaMax = selected.Release.Database.ReadSchemaMax
		artifact.Toolchain = selected.Release.Toolchain
	}
	if inventory.SchemaVersion < artifact.ReadSchemaMin || inventory.SchemaVersion > artifact.ReadSchemaMax {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	return CatalogRetainedWorkInventoryReport{
		FormatVersion: 1, InventoryComplete: true, SafeToResume: "not_established", RecoveryAuthorized: false,
		HostID: layout.hostID, Lane: layout.lane, ObservationStartedAt: startedAt,
		ObservationCompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ServiceActive: active,
		DatabaseIdentity: identity, ConfigurationSHA256: configurationSHA, DatabaseSchema: inventory.SchemaVersion,
		SelectedArtifact: artifact, DiagnosticBuildRevision: buildinfo.Revision(), WorkInventory: inventory,
	}, nil
}

func retainedInventoryDatabaseIdentity(path string) (CatalogRetainedDatabaseIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !catalogFileHasSingleLink(info) {
		return CatalogRetainedDatabaseIdentity{}, ErrCatalogRetainedInventory
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return CatalogRetainedDatabaseIdentity{}, ErrCatalogRetainedInventory
	}
	return CatalogRetainedDatabaseIdentity{Path: path, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

func retainedInventorySidecarIdentities(path string) ([2]CatalogRetainedDatabaseIdentity, error) {
	var identities [2]CatalogRetainedDatabaseIdentity
	for index, suffix := range []string{"-wal", "-shm"} {
		identity, err := retainedInventoryDatabaseIdentity(path + suffix)
		if err != nil {
			return [2]CatalogRetainedDatabaseIdentity{}, ErrCatalogRetainedInventory
		}
		identities[index] = identity
	}
	return identities, nil
}
