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

// CatalogRetainedInventoryDiagnostic identifies only an allowlisted failure
// boundary. It never carries the underlying error, SQL, paths or identity rows.
type CatalogRetainedInventoryDiagnostic struct {
	Stage               string `json:"failure_stage"`
	Category            string `json:"error_category"`
	ElapsedMilliseconds int64  `json:"elapsed_ms"`
}

type catalogRetainedInventoryError struct {
	diagnostic CatalogRetainedInventoryDiagnostic
}

func (e *catalogRetainedInventoryError) Error() string { return ErrCatalogRetainedInventory.Error() }
func (e *catalogRetainedInventoryError) Unwrap() error { return ErrCatalogRetainedInventory }

// CatalogRetainedInventoryDiagnostics returns safe metadata only for errors
// produced by this reader. Generic errors retain the existing unknown failure.
func CatalogRetainedInventoryDiagnostics(err error) (CatalogRetainedInventoryDiagnostic, bool) {
	var failure *catalogRetainedInventoryError
	if !errors.As(err, &failure) || failure == nil || failure.diagnostic.ElapsedMilliseconds < 0 ||
		!validRetainedInventoryStage(failure.diagnostic.Stage) || !validRetainedInventoryCategory(failure.diagnostic.Category) {
		return CatalogRetainedInventoryDiagnostic{}, false
	}
	return failure.diagnostic, true
}

func retainedInventoryFailure(stage string, started time.Time, cause error) error {
	category := "operation_failed"
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		category = "deadline_exceeded"
	case errors.Is(cause, context.Canceled):
		category = "cancelled"
	case errors.Is(cause, store.ErrRetainedWorkInventoryLimit):
		category = "limit_exceeded"
	case errors.Is(cause, store.ErrSchemaVersion), errors.Is(cause, store.ErrSchemaHistory):
		category = "schema_incompatible"
	case errors.Is(cause, ErrCatalogRetainedInventory):
		category = "guard_refused"
	}
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return &catalogRetainedInventoryError{CatalogRetainedInventoryDiagnostic{
		Stage: stage, Category: category, ElapsedMilliseconds: elapsed,
	}}
}

// Some SQLite/helpers return a driver-specific interruption error rather than
// wrap the operation context. Observe it before deferred cancel runs, so an
// expired operation is classified without mislabelling ordinary failures.
func retainedInventoryOperationError(ctx context.Context, cause error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return cause
}

func validRetainedInventoryStage(stage string) bool {
	switch stage {
	case "scope", "catalog_root", "layout", "authority", "selection", "configuration", "database_identity", "sidecar_identity",
		"service_status", "database_open", "database_binding", "inventory_read", "database_close", "database_revalidation",
		"configuration_revalidation", "selection_revalidation", "service_revalidation", "artifact_schema":
		return true
	default:
		return false
	}
}

func validRetainedInventoryCategory(category string) bool {
	switch category {
	case "guard_refused", "operation_failed", "deadline_exceeded", "cancelled", "limit_exceeded", "schema_incompatible":
		return true
	default:
		return false
	}
}

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
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryFailure("scope", time.Now(), ErrCatalogRetainedInventory)
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
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryFailure("scope", time.Now(), ErrCatalogRetainedInventory)
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
	started := time.Now()
	if validateCatalogPlan(plan) != nil || serviceStatus == nil || validateAuthority == nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryFailure("scope", started, ErrCatalogRetainedInventory)
	}
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryFailure("catalog_root", started, err)
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryFailure("layout", started, err)
	}
	return readCatalogRetainedWorkInventoryAt(ctx, layout, serviceStatus, validateAuthority, maxRows, store.ReadRetainedWorkInventory)
}

type retainedInventoryReader func(context.Context, *sql.DB, int) (store.RetainedWorkInventory, error)

// The injectable reader is limited to this private fixture seam. Public
// commands always use the store's schema-40, single-cut metadata reader.
func readCatalogRetainedWorkInventoryAt(ctx context.Context, layout catalogSelectorLayout, serviceStatus func(context.Context) (bool, error), validateAuthority func() error, maxRows int, readInventory retainedInventoryReader) (report CatalogRetainedWorkInventoryReport, resultErr error) {
	started := time.Now()
	stage := "layout"
	defer func() {
		if resultErr != nil {
			// Discard partial rows and raw causes at this boundary. The safe
			// classification is computed before the raw cause is dropped.
			report = CatalogRetainedWorkInventoryReport{}
			resultErr = retainedInventoryFailure(stage, started, resultErr)
		}
	}()
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
	stage = "authority"
	if err := validateAuthority(); err != nil {
		return CatalogRetainedWorkInventoryReport{}, err
	}
	stage = "selection"
	selected, err := inspectCatalogSelector(layout)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, err
	}
	if selected.Kind == "unselected" {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	stage = "configuration"
	configurationSHA, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, err
	}
	stage = "database_identity"
	identity, err := retainedInventoryDatabaseIdentity(layout.databasePath)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	// SQLite may create WAL shared-memory files even with mode=ro when they
	// are absent. This diagnostic accepts only an already-open WAL authority
	// with its complete private sidecar pair; it never uses immutable=1, which
	// would hide committed WAL work and invalidate the inventory.
	stage = "sidecar_identity"
	sidecars, err := retainedInventorySidecarIdentities(layout.databasePath)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	stage = "service_status"
	active, err := serviceStatus(ctx)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryOperationError(ctx, err)
	}
	stage = "database_open"
	database, err := store.OpenExistingCurrentReadOnly(ctx, layout.databasePath)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryOperationError(ctx, err)
	}
	stage = "database_binding"
	openedIdentity, err := retainedInventoryDatabaseIdentity(layout.databasePath)
	openedSidecars, sidecarErr := retainedInventorySidecarIdentities(layout.databasePath)
	if err != nil || openedIdentity != identity || sidecarErr != nil || openedSidecars != sidecars {
		_ = database.Close()
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	stage = "inventory_read"
	inventory, readErr := readInventory(ctx, database, maxRows)
	closeErr := database.Close()
	if readErr != nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryOperationError(ctx, readErr)
	}
	if closeErr != nil {
		stage = "database_close"
		return CatalogRetainedWorkInventoryReport{}, closeErr
	}
	if !inventory.Complete {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	if inventory.SchemaVersion != 40 {
		return CatalogRetainedWorkInventoryReport{}, store.ErrSchemaVersion
	}
	if ctx.Err() != nil {
		return CatalogRetainedWorkInventoryReport{}, ctx.Err()
	}
	stage = "database_revalidation"
	finalIdentity, err := retainedInventoryDatabaseIdentity(layout.databasePath)
	finalSidecars, sidecarErr := retainedInventorySidecarIdentities(layout.databasePath)
	if err != nil || finalIdentity != identity || sidecarErr != nil || finalSidecars != sidecars {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	stage = "configuration_revalidation"
	finalConfigurationSHA, err := hashCatalogConfiguration(layout.configFile)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, err
	}
	if finalConfigurationSHA != configurationSHA {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	if err := validateAuthority(); err != nil {
		return CatalogRetainedWorkInventoryReport{}, err
	}
	stage = "selection_revalidation"
	finalSelection, err := inspectCatalogSelector(layout)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, err
	}
	if !reflect.DeepEqual(selected, finalSelection) {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	stage = "service_revalidation"
	finalActive, err := serviceStatus(ctx)
	if err != nil {
		return CatalogRetainedWorkInventoryReport{}, retainedInventoryOperationError(ctx, err)
	}
	if active != finalActive {
		return CatalogRetainedWorkInventoryReport{}, ErrCatalogRetainedInventory
	}
	if ctx.Err() != nil {
		return CatalogRetainedWorkInventoryReport{}, ctx.Err()
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
	stage = "artifact_schema"
	if inventory.SchemaVersion < artifact.ReadSchemaMin || inventory.SchemaVersion > artifact.ReadSchemaMax {
		return CatalogRetainedWorkInventoryReport{}, store.ErrSchemaVersion
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
