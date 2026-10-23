package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	ErrCatalogPRDRecovery     = errors.New("sealed-candidate PRD retained-work recovery refused")
	prdRecoveryPairIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)
	prdRecoverySummaryPattern = regexp.MustCompile(`recovered_lost_pairs=([0-9]+) already_recovered_lost_pairs=([0-9]+)`)
)

// CatalogPRDRecoveryRequest binds one guarded offline recovery to a sealed
// release and a fresh, consistent pre-recovery backup. LostPairs must be the
// complete retained lost-pair set for this host.
type CatalogPRDRecoveryRequest struct {
	ReleaseID                 string
	ExpectedReleaseSHA256     string
	ExpectedBundleSHA256      string
	BackupDirectory           string
	ExpectedBackupManifestSHA string
	LostPairs                 []string
}

// CatalogPRDRecoveryReport is bounded evidence for an offline recovery. It
// deliberately contains identities and digests, never command payloads or
// process output.
type CatalogPRDRecoveryReport struct {
	HostID                    string               `json:"host_id"`
	ReleaseID                 string               `json:"release_id"`
	ReleaseSHA256             string               `json:"release_sha256"`
	BundleSHA256              string               `json:"bundle_sha256"`
	RecoveryComponent         string               `json:"recovery_component"`
	BackupDirectory           string               `json:"backup_directory"`
	BackupManifestSHA256      string               `json:"backup_manifest_sha256"`
	SchemaBefore              int                  `json:"schema_before"`
	SchemaAfter               int                  `json:"schema_after"`
	WorkBefore                CatalogWorkInventory `json:"work_before"`
	WorkAfter                 CatalogWorkInventory `json:"work_after"`
	RecoveredLostPairs        int                  `json:"recovered_lost_pairs"`
	RecoveryOutputSHA256      string               `json:"recovery_output_sha256"`
	ServiceRemainsStopped     bool                 `json:"service_remains_stopped"`
	SelectorRemainsUnselected bool                 `json:"selector_remains_unselected"`
}

// CatalogPRDRecoveryExecutor runs only the recovery executable selected from
// the verified sealed release. Production uses execCatalogPRDRecoveryExecutor;
// tests can supply a bounded fake without adding a production fixture switch.
type CatalogPRDRecoveryExecutor interface {
	Run(context.Context, string, []string) ([]byte, error)
}

type execCatalogPRDRecoveryExecutor struct{}

func (execCatalogPRDRecoveryExecutor) Run(ctx context.Context, executable string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	return command.CombinedOutput()
}

// RecoverCatalogPRDLostPairs invokes the sealed host bundle's built-in
// offline recovery command without rebuilding or installing binaries. The
// service must already be stopped; the candidate independently enforces its
// lifecycle/process ownership guards before changing SQLite state.
func RecoverCatalogPRDLostPairs(ctx context.Context, plan PRDUpgradePlan, service PRDServiceProbe,
	request CatalogPRDRecoveryRequest, executor CatalogPRDRecoveryExecutor) (CatalogPRDRecoveryReport, error) {
	if ctx == nil || validatePRDPlan(plan) != nil || service == nil ||
		!validCatalogReleaseID(request.ReleaseID) || !prdDigestPattern.MatchString(request.ExpectedReleaseSHA256) ||
		!prdDigestPattern.MatchString(request.ExpectedBundleSHA256) ||
		!prdDigestPattern.MatchString(request.ExpectedBackupManifestSHA) ||
		filepath.Clean(request.BackupDirectory) != request.BackupDirectory || !filepath.IsAbs(request.BackupDirectory) {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	if executor == nil {
		executor = execCatalogPRDRecoveryExecutor{}
	}
	pairs, err := validatePRDRecoveryLostPairs(request.LostPairs)
	if err != nil {
		return CatalogPRDRecoveryReport{}, err
	}

	catalogPlan := Plan{HostID: plan.HostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace}
	if held, _, holdErr := CatalogCohortAdmissionHeld(catalogPlan); holdErr != nil || held {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, holdErr)
	}
	root, err := DefaultCatalogRootForPlan(catalogPlan)
	if err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	publicationFD, err := acquireCatalogRootPublicationLock(root)
	if err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	defer releaseCatalogRootPublicationLock(publicationFD)

	status, err := InspectCatalogRelease(root, plan.HostID, request.ReleaseID)
	if err != nil || status.Identity.ReleaseSHA256 != request.ExpectedReleaseSHA256 || status.Identity.BundleSHA256 != request.ExpectedBundleSHA256 {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	if status.Database.ReadSchemaMin <= 0 || status.Database.ReadSchemaMax < status.Database.ReadSchemaMin {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	bundlePath := filepath.Join(root, request.ReleaseID, plan.HostID)
	bundle, err := readSimpleBundleAtWithModes(bundlePath, 0o500, 0o400, 0o500, 0o500)
	if err != nil || bundle.Manifest.BundleSHA256 != request.ExpectedBundleSHA256 || ValidateSimpleBundle(bundle) != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	component, ok := CatalogPRDRecoveryCommandComponent(plan.HostID)
	if !ok {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	componentBytes, ok := bundle.Components[component]
	if !ok || len(componentBytes) == 0 {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	componentHash := sha256.Sum256(componentBytes)
	var declaredHash string
	for _, item := range bundle.Manifest.Components {
		if item.Name == component {
			declaredHash = item.SHA256
			break
		}
	}
	if declaredHash == "" || hex.EncodeToString(componentHash[:]) != declaredHash {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	executable := filepath.Join(bundlePath, "bin", component)

	active, err := service.Status(ctx, plan)
	if err != nil || active {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	if err := validatePRDConfig(plan); err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	legacySelector, err := InspectCatalogSelectorForPlan(catalogPlan, root)
	if err != nil || legacySelector.Kind != "unselected" || legacySelector.Target != "" {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	configSHA, err := hashPRDRegularFile(plan.Namespace.ConfigFile)
	if err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}

	backup, err := loadPRDBackupManifest(plan, request.BackupDirectory)
	if err != nil || backup.BundleSHA256 != request.ExpectedBundleSHA256 ||
		backupManifestSHA256(request.BackupDirectory) != request.ExpectedBackupManifestSHA {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, backup.CreatedAt)
	if err != nil || time.Since(createdAt) < 0 || time.Since(createdAt) > prdCatalogProofMaxAge {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	if err := verifyPRDBackupTargets(backup, true); err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	if err := verifyNoNewPRDWork(ctx, plan.Namespace.Database, backup.DatabaseBackupPath); err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	schemaBefore, workBefore, snapshotErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(snapshotErr, closeErr); err != nil || schemaBefore != backup.SchemaVersion ||
		!catalogPRDRecoverySchemaIsCurrent(schemaBefore, status.Database.ReadSchemaMin,
			status.Database.ReadSchemaMax, bundle.Manifest.DatabaseSchemaMax) ||
		!workBefore.InstallableForGuardedRecovery() || workBefore.PendingRecoveryFinalizations != 0 ||
		workBefore.ActiveCommandSlots != int64(len(pairs)) || workBefore.ActiveSessionReservations != int64(len(pairs)) {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	available, selectorReserve, err := ReadPRDCatalogSelectorCapacity(plan)
	postBackupRequired := prdCatalogOperatingBufferBytes + selectorReserve
	if err != nil || selectorReserve != prdCatalogSelectorHeadroom || available < postBackupRequired {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogInsufficientSpace, err)
	}
	if status.Identity.HostID != plan.HostID || status.Database.ReadSchemaMax != bundle.Manifest.DatabaseSchemaMax {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}

	args, err := buildCatalogPRDRecoveryCommand(plan.HostID, plan.Namespace.ConfigFile, pairs)
	if err != nil {
		return CatalogPRDRecoveryReport{}, err
	}
	output, runErr := executor.Run(ctx, executable, args)
	if runErr != nil {
		return CatalogPRDRecoveryReport{}, ErrCatalogPRDRecovery
	}
	recovered, err := parsePRDRecoverySummary(output, len(pairs))
	if err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}

	activeAfter, activeErr := service.Status(ctx, plan)
	if activeErr != nil || activeAfter {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, activeErr)
	}
	selectorAfter, selectorErr := InspectCatalogSelectorForPlan(catalogPlan, root)
	if selectorErr != nil || selectorAfter.Kind != "unselected" || selectorAfter.Target != "" {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, selectorErr)
	}
	configSHAAfter, configErr := hashPRDRegularFile(plan.Namespace.ConfigFile)
	if configErr != nil || configSHAAfter != configSHA {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, configErr)
	}
	if err := verifyPRDBackupTargets(backup, true); err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	statusAfter, statusErr := InspectCatalogRelease(root, plan.HostID, request.ReleaseID)
	if statusErr != nil || statusAfter.Identity != status.Identity {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, statusErr)
	}
	database, err = store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	schemaAfter, workAfter, snapshotErr := readPRDWorkSnapshot(ctx, database)
	closeErr = database.Close()
	if err := errors.Join(snapshotErr, closeErr); err != nil || schemaAfter != schemaBefore || !workAfter.Empty() {
		return CatalogPRDRecoveryReport{}, errors.Join(ErrCatalogPRDRecovery, err)
	}
	outputHash := sha256.Sum256(output)
	return CatalogPRDRecoveryReport{
		HostID: plan.HostID, ReleaseID: status.Identity.ReleaseID, ReleaseSHA256: status.Identity.ReleaseSHA256,
		BundleSHA256: status.Identity.BundleSHA256, RecoveryComponent: component,
		BackupDirectory: request.BackupDirectory, BackupManifestSHA256: request.ExpectedBackupManifestSHA,
		SchemaBefore: schemaBefore, SchemaAfter: schemaAfter, WorkBefore: workBefore, WorkAfter: workAfter,
		RecoveredLostPairs: recovered, RecoveryOutputSHA256: hex.EncodeToString(outputHash[:]),
		ServiceRemainsStopped: true, SelectorRemainsUnselected: true,
	}, nil
}

func catalogPRDRecoverySchemaIsCurrent(schema, readMin, readMax, target int) bool {
	return schema > 0 && readMin > 0 && readMax >= readMin && schema == readMax && schema == target
}

func validatePRDRecoveryLostPairs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, ErrCatalogPRDRecovery
	}
	seenSessions := make(map[string]struct{}, len(values))
	seenCommands := make(map[string]struct{}, len(values))
	validated := make([]string, 0, len(values))
	for _, value := range values {
		sessionValue, commandValue, found := strings.Cut(value, ":")
		if !found || strings.Contains(commandValue, ":") || !prdRecoveryPairIDPattern.MatchString(sessionValue) || !prdRecoveryPairIDPattern.MatchString(commandValue) {
			return nil, ErrCatalogPRDRecovery
		}
		sessionID, sessionErr := domain.NewSessionID(sessionValue)
		commandID, commandErr := domain.NewCommandID(commandValue)
		if sessionErr != nil || commandErr != nil {
			return nil, ErrCatalogPRDRecovery
		}
		if _, duplicate := seenSessions[string(sessionID)]; duplicate {
			return nil, ErrCatalogPRDRecovery
		}
		if _, duplicate := seenCommands[string(commandID)]; duplicate {
			return nil, ErrCatalogPRDRecovery
		}
		seenSessions[string(sessionID)] = struct{}{}
		seenCommands[string(commandID)] = struct{}{}
		validated = append(validated, string(sessionID)+":"+string(commandID))
	}
	return validated, nil
}

func parsePRDRecoverySummary(output []byte, expected int) (recovered int, err error) {
	matches := prdRecoverySummaryPattern.FindSubmatch(output)
	if len(matches) != 3 || expected <= 0 {
		return 0, ErrCatalogPRDRecovery
	}
	recovered, err = strconv.Atoi(string(matches[1]))
	if err != nil {
		return 0, ErrCatalogPRDRecovery
	}
	already, parseErr := strconv.Atoi(string(matches[2]))
	if parseErr != nil || recovered != expected || already != 0 {
		return 0, ErrCatalogPRDRecovery
	}
	return recovered, nil
}

// CatalogPRDRecoveryCommandComponent returns the only allowed recovery
// executable for a canonical host. It exists so command tests assert the
// platform split without creating a production-path fixture override.
func CatalogPRDRecoveryCommandComponent(hostID string) (string, bool) {
	switch hostID {
	case "mac-control-plane":
		return "runner-locald", true
	case "linux-host", "sandbox-host", "factory-sandbox":
		return "runnerd", true
	default:
		return "", false
	}
}

func buildCatalogPRDRecoveryCommand(hostID, configPath string, pairs []string) ([]string, error) {
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return nil, ErrCatalogPRDRecovery
	}
	validated, err := validatePRDRecoveryLostPairs(pairs)
	if err != nil {
		return nil, err
	}
	if _, ok := CatalogPRDRecoveryCommandComponent(hostID); !ok {
		return nil, ErrCatalogPRDRecovery
	}
	args := []string{"recover-stalled", "--config", configPath, "--apply"}
	for _, pair := range validated {
		args = append(args, "--lost-pair", pair)
	}
	return args, nil
}
