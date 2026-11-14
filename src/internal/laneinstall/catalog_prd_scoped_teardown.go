package laneinstall

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/buildinfo"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

var ErrCatalogPRDScopedTeardown = errors.New("qualified operator scoped completed-teardown recovery refused")

type scopedTeardownBoundaryError struct {
	stage, category string
	apply           bool
	cause           error
}

func (e *scopedTeardownBoundaryError) Error() string {
	message := fmt.Sprintf("%s: failure_stage=%s failure_category=%s", ErrCatalogPRDScopedTeardown, e.stage, e.category)
	if e.apply {
		message += "; recovery incomplete; inspect exact target state"
	}
	return message
}

func (e *scopedTeardownBoundaryError) Unwrap() []error {
	return []error{ErrCatalogPRDScopedTeardown, e.cause}
}

func scopedTeardownFailure(stage string, cause error, apply bool) error {
	category := "guard_refused"
	switch {
	case errors.Is(cause, store.ErrRetainedWorkInventoryLimit):
		category = "inventory_limit"
	case errors.Is(cause, context.DeadlineExceeded):
		category = "deadline_exceeded"
	case errors.Is(cause, context.Canceled):
		category = "cancelled"
	}
	return &scopedTeardownBoundaryError{stage: stage, category: category, apply: apply, cause: cause}
}

// CatalogPRDScopedTeardownRequest selects one completed command, never a lost
// command or an implicit set. Apply binds the read-only plan's preservation
// digests and a separately verified fresh catalog backup.
type CatalogPRDScopedTeardownRequest struct {
	ReleaseID                            string
	ExpectedReleaseSHA256                string
	ExpectedBundleSHA256                 string
	ExpectedConfigurationSHA256          string
	TeardownPair                         string
	BackupDirectory                      string
	ExpectedBackupManifestSHA            string
	ExpectedPreservedInventorySHA256     string
	ExpectedPreservedOwnershipSHA256     string
	ExpectedTargetOwnershipSHA256        string
	RuntimeArchiveDirectory              string
	ExpectedRuntimeArchiveManifestSHA256 string
}

type CatalogPRDScopedTeardownReport struct {
	FormatVersion             int                  `json:"format_version"`
	Mode                      string               `json:"mode"`
	Applied                   bool                 `json:"applied"`
	HostID                    string               `json:"host_id"`
	SessionID                 string               `json:"session_id"`
	CommandID                 string               `json:"command_id"`
	ReleaseID                 string               `json:"release_id"`
	ReleaseSHA256             string               `json:"release_sha256"`
	BundleSHA256              string               `json:"bundle_sha256"`
	ConfigurationSHA256       string               `json:"configuration_sha256"`
	OperatorBuildRevision     string               `json:"operator_build_revision"`
	PreservedInventorySHA256  string               `json:"preserved_inventory_sha256"`
	PreservedOwnershipSHA256  string               `json:"preserved_ownership_sha256"`
	TargetOwnershipSHA256     string               `json:"target_ownership_sha256"`
	PreservedSessionIDs       []domain.SessionID   `json:"preserved_session_ids"`
	TargetReservationReleased bool                 `json:"target_reservation_released"`
	FinalizationPending       bool                 `json:"finalization_pending"`
	AlreadyRecovered          bool                 `json:"already_recovered"`
	RuntimeExitVerified       bool                 `json:"runtime_exit_verified"`
	ServiceActive             bool                 `json:"service_active"`
	ServiceRemainsStopped     bool                 `json:"service_remains_stopped"`
	SelectorRemainsUnchanged  bool                 `json:"selector_remains_unchanged"`
	SchemaBefore              int                  `json:"schema_before"`
	SchemaAfter               int                  `json:"schema_after"`
	WorkBefore                CatalogWorkInventory `json:"work_before"`
	WorkAfter                 CatalogWorkInventory `json:"work_after"`
	ArchiveDirectory          string               `json:"archive_directory,omitempty"`
	ArchiveManifestSHA256     string               `json:"archive_manifest_sha256,omitempty"`
	SelectedExecutorInvoked   bool                 `json:"selected_executor_invoked"`
	SafeToResume              string               `json:"safe_to_resume"`
}

// ValidateCatalogPRDScopedTeardownRequest is pure. It is also used before any
// disposable CLI fixture seam, so a fixture cannot waive wire bindings.
func ValidateCatalogPRDScopedTeardownRequest(request CatalogPRDScopedTeardownRequest, apply bool) error {
	if !validCatalogReleaseID(request.ReleaseID) || !prdDigestPattern.MatchString(request.ExpectedReleaseSHA256) ||
		!prdDigestPattern.MatchString(request.ExpectedBundleSHA256) {
		return ErrCatalogPRDScopedTeardown
	}
	if _, err := scopedTeardownPair(request.TeardownPair); err != nil {
		return err
	}
	for _, value := range []string{request.ExpectedConfigurationSHA256, request.ExpectedPreservedInventorySHA256, request.ExpectedPreservedOwnershipSHA256, request.ExpectedTargetOwnershipSHA256} {
		if (apply || value != "") && !prdDigestPattern.MatchString(value) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	if apply || request.BackupDirectory != "" || request.ExpectedBackupManifestSHA != "" {
		if !filepath.IsAbs(request.BackupDirectory) || filepath.Clean(request.BackupDirectory) != request.BackupDirectory ||
			!prdDigestPattern.MatchString(request.ExpectedBackupManifestSHA) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	if request.RuntimeArchiveDirectory != "" || request.ExpectedRuntimeArchiveManifestSHA256 != "" {
		if !filepath.IsAbs(request.RuntimeArchiveDirectory) || filepath.Clean(request.RuntimeArchiveDirectory) != request.RuntimeArchiveDirectory ||
			!prdDigestPattern.MatchString(request.ExpectedRuntimeArchiveManifestSHA256) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	return nil
}

func scopedTeardownPair(value string) (store.CompletedTeardownRecoveryPair, error) {
	validated, err := validatePRDRecoveryLostPairs([]string{value})
	if err != nil || len(validated) != 1 {
		return store.CompletedTeardownRecoveryPair{}, ErrCatalogPRDScopedTeardown
	}
	session, command, _ := strings.Cut(validated[0], ":")
	return store.CompletedTeardownRecoveryPair{SessionID: domain.SessionID(session), CommandID: domain.CommandID(command)}, nil
}

type scopedCatalogBinding struct {
	root          string
	selector      CatalogSelectorStatus
	configuration string
	database      CatalogRetainedDatabaseIdentity
	active        bool
}

func readScopedCatalogBinding(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, request CatalogPRDScopedTeardownRequest) (scopedCatalogBinding, error) {
	if plan.HostID != "mac-control-plane" || validatePRDPlan(plan) != nil || service == nil || validatePRDConfig(plan) != nil {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	catalog := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	if held, _, err := CatalogCohortAdmissionHeld(catalog); err != nil || held {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	root, err := DefaultCatalogRootForPlan(catalog)
	if err != nil {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	selector, err := InspectCatalogSelectorForPlan(catalog, root)
	if err != nil || selector.Kind != "catalog" || selector.Release == nil {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	identity := selector.Release.Identity
	if identity.HostID != plan.HostID || identity.ReleaseID != request.ReleaseID || identity.ReleaseSHA256 != request.ExpectedReleaseSHA256 ||
		identity.BundleSHA256 != request.ExpectedBundleSHA256 || selector.Release.Database.ReadSchemaMin > 40 || selector.Release.Database.ReadSchemaMax < 40 {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	configuration, err := hashPRDRegularFile(plan.Namespace.ConfigFile)
	if err != nil || (request.ExpectedConfigurationSHA256 != "" && request.ExpectedConfigurationSHA256 != configuration) {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	database, err := retainedInventoryDatabaseIdentity(plan.Namespace.Database)
	if err != nil {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return scopedCatalogBinding{}, ErrCatalogPRDScopedTeardown
	}
	return scopedCatalogBinding{root: root, selector: selector, configuration: configuration, database: database, active: active}, nil
}

func sameScopedCatalogBinding(before, after scopedCatalogBinding) bool {
	return before.root == after.root && before.database == after.database && before.configuration == after.configuration &&
		before.active == after.active && sameCatalogPRDRecoverySelector(before.selector, after.selector)
}

func scopedRuntimeSessions(plan store.ScopedCompletedTeardownRecoveryPlan) []hostruntime.MacScopedTeardownSession {
	result := make([]hostruntime.MacScopedTeardownSession, 0, len(plan.Sessions)+1)
	found := false
	for _, session := range plan.Sessions {
		if session.SessionID == plan.Recovery.Pair.SessionID {
			found = true
		}
		result = append(result, hostruntime.MacScopedTeardownSession{SessionID: string(session.SessionID), Generation: session.RuntimeGeneration,
			SourceMode: string(session.Source.Mode()), SourcePath: session.Source.Path()})
	}
	if !found {
		session := plan.Recovery.Session
		result = append(result, hostruntime.MacScopedTeardownSession{SessionID: string(session.SessionID), Generation: session.RuntimeGeneration,
			SourceMode: string(session.Source.Mode()), SourcePath: session.Source.Path()})
	}
	return result
}

func scopedReport(binding scopedCatalogBinding, plan store.ScopedCompletedTeardownRecoveryPlan, ownership hostruntime.MacScopedTeardownOwnershipSnapshot, work CatalogWorkInventory) CatalogPRDScopedTeardownReport {
	identity := binding.selector.Release.Identity
	return CatalogPRDScopedTeardownReport{FormatVersion: 1, Mode: "operator_scoped_completed_teardown_v1", HostID: "mac-control-plane",
		SessionID: string(plan.Recovery.Pair.SessionID), CommandID: string(plan.Recovery.Pair.CommandID),
		ReleaseID: identity.ReleaseID, ReleaseSHA256: identity.ReleaseSHA256, BundleSHA256: identity.BundleSHA256,
		ConfigurationSHA256: binding.configuration, OperatorBuildRevision: buildinfo.Revision(),
		PreservedInventorySHA256: plan.PreservedInventorySHA256, PreservedOwnershipSHA256: ownership.PreservedSHA256,
		TargetOwnershipSHA256: ownership.TargetIdentitySHA256,
		PreservedSessionIDs:   plan.PreservedSessionIDs, TargetReservationReleased: plan.Recovery.AlreadyRecovered,
		FinalizationPending: plan.Recovery.FinalizationPending, AlreadyRecovered: plan.Recovery.AlreadyRecovered,
		ServiceActive: binding.active, SchemaBefore: 40, SchemaAfter: 40, WorkBefore: work, WorkAfter: work,
		SafeToResume: "not_established"}
}

func readScopedStorePlan(ctx context.Context, database *sql.DB, pair store.CompletedTeardownRecoveryPair) (*store.AuthorityStore, store.ScopedCompletedTeardownRecoveryPlan, CatalogWorkInventory, error) {
	schema, work, err := readPRDWorkSnapshot(ctx, database)
	if err != nil || schema != 40 {
		return nil, store.ScopedCompletedTeardownRecoveryPlan{}, CatalogWorkInventory{}, ErrCatalogPRDScopedTeardown
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		return nil, store.ScopedCompletedTeardownRecoveryPlan{}, CatalogWorkInventory{}, ErrCatalogPRDScopedTeardown
	}
	plan, err := authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, pair)
	if err != nil {
		return nil, store.ScopedCompletedTeardownRecoveryPlan{}, CatalogWorkInventory{}, errors.Join(ErrCatalogPRDScopedTeardown, err)
	}
	return authority, plan, work, nil
}

// InspectCatalogPRDScopedTeardown never creates a database or WAL sidecar,
// takes a lifecycle lock, repairs permissions, or signals a PID. Its native
// process observation is read-only and establishes applicability before stop.
func InspectCatalogPRDScopedTeardown(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, request CatalogPRDScopedTeardownRequest) (result CatalogPRDScopedTeardownReport, resultErr error) {
	stage := "bindings"
	defer func() {
		if resultErr != nil {
			resultErr = scopedTeardownFailure(stage, resultErr, false)
		}
	}()
	if ctx == nil || ValidateCatalogPRDScopedTeardownRequest(request, false) != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	before, err := readScopedCatalogBinding(ctx, plan, service, request)
	if err != nil || validateScopedRuntimeArchiveInput(plan, request) != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	// mode=ro can create missing WAL coordination files on some SQLite builds.
	// Refuse that condition instead of repairing a stopped authority for a plan.
	sidecars, err := retainedInventorySidecarIdentities(plan.Namespace.Database)
	if err != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	database, err := store.OpenExistingCurrentReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	pair, _ := scopedTeardownPair(request.TeardownPair)
	stage = "inventory"
	_, inventory, work, readErr := readScopedStorePlan(ctx, database, pair)
	closeErr := database.Close()
	if readErr != nil || closeErr != nil {
		return CatalogPRDScopedTeardownReport{}, errors.Join(ErrCatalogPRDScopedTeardown, readErr, closeErr)
	}
	stage = "peer_ownership"
	ownership, err := hostruntime.ReadMacScopedTeardownOwnership(ctx, plan.Namespace.Workspaces, string(pair.SessionID), scopedRuntimeSessions(inventory))
	if err != nil || (request.ExpectedPreservedInventorySHA256 != "" && request.ExpectedPreservedInventorySHA256 != inventory.PreservedInventorySHA256) ||
		(request.ExpectedPreservedOwnershipSHA256 != "" && request.ExpectedPreservedOwnershipSHA256 != ownership.PreservedSHA256) {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	stage = "target_binding"
	if request.ExpectedTargetOwnershipSHA256 != "" && validateScopedTeardownTargetBinding(plan, request, inventory, ownership) != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	stage = "target_exit"
	if !inventory.Recovery.AlreadyRecovered && (ownership.CheckFinalization(ctx) != nil || ownership.CheckExited(ctx) != nil) {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	stage = "postflight"
	after, err := readScopedCatalogBinding(ctx, plan, service, request)
	finalSidecars, sidecarErr := retainedInventorySidecarIdentities(plan.Namespace.Database)
	if err != nil || !sameScopedCatalogBinding(before, after) || sidecarErr != nil || sidecars != finalSidecars || ctx.Err() != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	report := scopedReport(before, inventory, ownership, work)
	report.RuntimeExitVerified = !inventory.Recovery.AlreadyRecovered || ownership.Target.LostRecoveryCleanupConfirmedAt != "" ||
		(request.ExpectedTargetOwnershipSHA256 != "" && validateScopedTeardownTargetBinding(plan, request, inventory, ownership) == nil)
	if request.BackupDirectory != "" || request.RuntimeArchiveDirectory != "" {
		report.ArchiveDirectory = scopedTeardownArchiveDirectory(request, string(pair.SessionID))
		report.ArchiveManifestSHA256 = request.ExpectedRuntimeArchiveManifestSHA256
	}
	if report.TargetOwnershipSHA256 == "" {
		report.TargetOwnershipSHA256 = request.ExpectedTargetOwnershipSHA256
	}
	return report, nil
}

// RecoverCatalogPRDScopedTeardown is a separate qualified operator boundary.
// It never invokes/replaces the selected service, migrates its database, calls
// normal startup reconciliation, or sends a process signal.
func RecoverCatalogPRDScopedTeardown(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, request CatalogPRDScopedTeardownRequest) (CatalogPRDScopedTeardownReport, error) {
	return recoverCatalogPRDScopedTeardownWith(ctx, plan, service, request, scopedTeardownGuards{
		readBinding: readScopedCatalogBinding, requireStopped: requireScopedTeardownServicesStopped,
		validateBackup: validateScopedTeardownBackup, attested: buildinfo.Revision() != "unattested",
	})
}

// These private dependencies allow disposable composition fixtures to replace
// only host-bound observations. Database, runtime ownership/proof, locks,
// archives, and target-only store transactions remain the production paths.
type scopedTeardownGuards struct {
	readBinding    func(context.Context, PRDUpgradePlan, PRDServiceProbe, CatalogPRDScopedTeardownRequest) (scopedCatalogBinding, error)
	requireStopped func(context.Context, PRDUpgradePlan) error
	validateBackup func(context.Context, PRDUpgradePlan, CatalogPRDScopedTeardownRequest) error
	attested       bool
	checkpoint     func(string) error
}

func recoverCatalogPRDScopedTeardownWith(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe, request CatalogPRDScopedTeardownRequest, guards scopedTeardownGuards) (result CatalogPRDScopedTeardownReport, resultErr error) {
	stage := "bindings"
	defer func() {
		if resultErr != nil {
			resultErr = scopedTeardownFailure(stage, resultErr, true)
		}
	}()
	if ctx == nil || ValidateCatalogPRDScopedTeardownRequest(request, true) != nil || !guards.attested || guards.readBinding == nil || guards.requireStopped == nil || guards.validateBackup == nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	before, err := guards.readBinding(ctx, plan, service, request)
	if err != nil || before.active {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	stage = "stopped_guard"
	catalogFD, err := acquireCatalogRootPublicationLock(before.root)
	if err != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	defer releaseCatalogRootPublicationLock(catalogFD)
	lifecycleFD, err := acquireScopedTeardownLifecycleLock(plan.Namespace.LifecycleLock)
	if err != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	defer releasePRDUpgradeLock(lifecycleFD)
	if err := guards.requireStopped(ctx, plan); err != nil {
		return CatalogPRDScopedTeardownReport{}, err
	}
	stage = "backup"
	locked, err := guards.readBinding(ctx, plan, service, request)
	if err != nil || !sameScopedCatalogBinding(before, locked) || guards.validateBackup(ctx, plan, request) != nil || validateScopedRuntimeArchiveInput(plan, request) != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	stage = "inventory"
	database, err := store.OpenExistingCurrent(ctx, plan.Namespace.Database)
	if err != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	defer database.Close()
	pair, _ := scopedTeardownPair(request.TeardownPair)
	authority, inventory, work, err := readScopedStorePlan(ctx, database, pair)
	if err != nil || inventory.PreservedInventorySHA256 != request.ExpectedPreservedInventorySHA256 {
		return CatalogPRDScopedTeardownReport{}, errors.Join(ErrCatalogPRDScopedTeardown, err)
	}
	stage = "peer_ownership"
	ownership, err := hostruntime.ReadMacScopedTeardownOwnership(ctx, plan.Namespace.Workspaces, string(pair.SessionID), scopedRuntimeSessions(inventory))
	if err != nil || ownership.PreservedSHA256 != request.ExpectedPreservedOwnershipSHA256 {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	stage = "target_binding"
	if validateScopedTeardownTargetBinding(plan, request, inventory, ownership) != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	report := scopedReport(before, inventory, ownership, work)
	report.TargetOwnershipSHA256 = request.ExpectedTargetOwnershipSHA256
	report.RuntimeExitVerified = inventory.Recovery.AlreadyRecovered
	if request.RuntimeArchiveDirectory != "" {
		report.ArchiveDirectory, report.ArchiveManifestSHA256 = request.RuntimeArchiveDirectory, request.ExpectedRuntimeArchiveManifestSHA256
	}
	if inventory.Recovery.AlreadyRecovered && !inventory.Recovery.FinalizationPending {
		if ownership.TargetPresent {
			stage = "finalize_preflight"
			return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
		}
	} else {
		if ownership.TargetPresent {
			if ownership.CheckFinalization(ctx) != nil {
				return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
			}
			stage = "archive"
			archive, err := archiveScopedTeardownMarker(plan, request, ownership)
			if err != nil {
				return CatalogPRDScopedTeardownReport{}, err
			}
			report.ArchiveDirectory = archive
			manifestSHA, err := archiveScopedTeardownWorkspace(ctx, archive, ownership.Target, request.ExpectedTargetOwnershipSHA256)
			if err != nil {
				return CatalogPRDScopedTeardownReport{}, err
			}
			report.ArchiveManifestSHA256 = manifestSHA
			archiveBinding := request
			archiveBinding.RuntimeArchiveDirectory = archive
			archiveBinding.ExpectedRuntimeArchiveManifestSHA256 = manifestSHA
			if validateScopedRuntimeArchiveInput(plan, archiveBinding) != nil {
				return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
			}
			if !inventory.Recovery.AlreadyRecovered {
				stage = "target_exit"
				if _, err := ownership.ConfirmExited(ctx); err != nil {
					return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
				}
				report.RuntimeExitVerified = true
				if guards.checkpoint != nil && guards.checkpoint("after_proof") != nil {
					return report, ErrCatalogPRDScopedTeardown
				}
				// The no-signal proof may wait while a final descendant exits.
				// Preserve the original archive, and refuse release if any bytes
				// changed between its capture and the established exit boundary.
				stage = "archive"
				verifiedSHA, verifyErr := archiveScopedTeardownWorkspace(ctx, archive, ownership.Target, request.ExpectedTargetOwnershipSHA256)
				if verifyErr != nil || verifiedSHA != manifestSHA {
					return report, ErrCatalogPRDScopedTeardown
				}
			}
		} else if !inventory.Recovery.AlreadyRecovered {
			return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
		}
		if !inventory.Recovery.AlreadyRecovered {
			stage = "release"
			if ownership.CheckFinalization(ctx) != nil {
				return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
			}
			if err := authority.ConfirmScopedCompletedTeardownRecovery(ctx, pair, request.ExpectedPreservedInventorySHA256); err != nil {
				return CatalogPRDScopedTeardownReport{}, errors.Join(ErrCatalogPRDScopedTeardown, err)
			}
			if guards.checkpoint != nil && guards.checkpoint("after_release") != nil {
				return report, ErrCatalogPRDScopedTeardown
			}
		}
		stage = "finalize"
		if err := ownership.Finalize(ctx); err != nil {
			return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
		}
		if guards.checkpoint != nil && guards.checkpoint("after_runtime_finalization") != nil {
			return report, ErrCatalogPRDScopedTeardown
		}
		if err := authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, pair, request.ExpectedPreservedInventorySHA256); err != nil {
			return CatalogPRDScopedTeardownReport{}, errors.Join(ErrCatalogPRDScopedTeardown, err)
		}
	}
	stage = "postflight"
	_, afterPlan, afterWork, err := readScopedStorePlan(ctx, database, pair)
	if err != nil || !afterPlan.Recovery.AlreadyRecovered || afterPlan.Recovery.FinalizationPending ||
		afterPlan.PreservedInventorySHA256 != inventory.PreservedInventorySHA256 || !scopedTeardownWorkDelta(work, afterWork, inventory.Recovery.AlreadyRecovered) {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	finalOwnership, err := hostruntime.ReadMacScopedTeardownOwnership(ctx, plan.Namespace.Workspaces, string(pair.SessionID), scopedRuntimeSessions(afterPlan))
	if err != nil || finalOwnership.TargetPresent || finalOwnership.PreservedSHA256 != ownership.PreservedSHA256 || guards.requireStopped(ctx, plan) != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	after, err := guards.readBinding(ctx, plan, service, request)
	if err != nil || !sameScopedCatalogBinding(before, after) || ctx.Err() != nil {
		return CatalogPRDScopedTeardownReport{}, ErrCatalogPRDScopedTeardown
	}
	report.Applied, report.TargetReservationReleased, report.FinalizationPending = true, true, false
	report.ServiceRemainsStopped, report.SelectorRemainsUnchanged = true, true
	report.WorkAfter = afterWork
	return report, nil
}

func scopedTeardownWorkDelta(before, after CatalogWorkInventory, alreadyRecovered bool) bool {
	if before.ActiveSessions != 0 || before.RunningCommands != 0 || before.QueuedCommands != 0 || before.ResumableJobs != 0 ||
		after.ActiveSessions != 0 || after.RunningCommands != 0 || after.QueuedCommands != 0 || after.ResumableJobs != 0 || after.PendingRecoveryFinalizations != 0 {
		return false
	}
	wanted := before.ActiveSessionReservations
	if !alreadyRecovered {
		wanted--
	}
	return wanted >= 0 && after.ActiveSessionReservations == wanted && before.ActiveCommandSlots == after.ActiveCommandSlots
}

func validateScopedTeardownBackup(ctx context.Context, plan PRDUpgradePlan, request CatalogPRDScopedTeardownRequest) error {
	backup, err := loadPRDBackupManifest(plan, request.BackupDirectory)
	if err != nil || backup.SchemaVersion != 40 || backup.BundleSHA256 != request.ExpectedBundleSHA256 ||
		backupManifestSHA256(request.BackupDirectory) != request.ExpectedBackupManifestSHA {
		return ErrCatalogPRDScopedTeardown
	}
	created, err := time.Parse(time.RFC3339Nano, backup.CreatedAt)
	if err != nil || time.Since(created) < 0 || time.Since(created) > prdCatalogProofMaxAge || verifyPRDBackupTargets(backup, true) != nil ||
		verifyNoNewPRDWork(ctx, plan.Namespace.Database, backup.DatabaseBackupPath) != nil {
		return ErrCatalogPRDScopedTeardown
	}
	available, reserve, err := ReadPRDCatalogSelectorCapacity(plan)
	if err != nil || reserve != prdCatalogSelectorHeadroom || available < prdCatalogOperatingBufferBytes+reserve {
		return ErrCatalogPRDScopedTeardown
	}
	return nil
}

func acquireScopedTeardownLifecycleLock(path string) (int, error) {
	var directory unix.Stat_t
	if unix.Lstat(filepath.Dir(path), &directory) != nil || directory.Mode&unix.S_IFMT != unix.S_IFDIR || directory.Uid != uint32(os.Getuid()) || directory.Mode&0o077 != 0 {
		return -1, ErrCatalogPRDScopedTeardown
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, ErrCatalogPRDScopedTeardown
	}
	var opened, named unix.Stat_t
	if unix.Fstat(fd, &opened) != nil || unix.Lstat(path, &named) != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG ||
		opened.Mode&0o777 != 0o600 || opened.Uid != uint32(os.Getuid()) || opened.Nlink != 1 || opened.Dev != named.Dev || opened.Ino != named.Ino {
		_ = unix.Close(fd)
		return -1, ErrCatalogPRDScopedTeardown
	}
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		_ = unix.Close(fd)
		return -1, ErrCatalogPRDScopedTeardown
	}
	return fd, nil
}

func requireScopedTeardownServicesStopped(ctx context.Context, plan PRDUpgradePlan) error {
	return requireScopedTeardownServicesStoppedWith(plan, func(target string) (bool, error) {
		command := exec.CommandContext(ctx, "launchctl", "print", target)
		if err := command.Run(); err != nil {
			var exited *exec.ExitError
			if errors.As(err, &exited) && exited.ExitCode() == 113 {
				return false, nil
			}
			return false, ErrCatalogPRDScopedTeardown
		}
		return true, nil
	}, os.Lstat)
}

func requireScopedTeardownServicesStoppedWith(plan PRDUpgradePlan, loaded func(string) (bool, error), lstat func(string) (os.FileInfo, error)) error {
	for _, label := range []string{"com.remote-session-runner.local", "com.remote-session-runner.locald"} {
		present, err := loaded("gui/" + strconv.Itoa(os.Getuid()) + "/" + label)
		if err != nil || present {
			return ErrCatalogPRDScopedTeardown
		}
	}
	for _, socket := range []string{plan.Namespace.APISocket, plan.Namespace.LocalDSocket} {
		if socket == "" || !filepath.IsAbs(socket) {
			return ErrCatalogPRDScopedTeardown
		}
		if _, err := lstat(socket); !errors.Is(err, os.ErrNotExist) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	return nil
}
