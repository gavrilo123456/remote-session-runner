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
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
)

const (
	prdBootstrapRootModeOperationID = "02bfdc603d2ea3360ecd32501fe347d1"
	prdBootstrapRootModeBefore      = 0o775
	prdBootstrapRootModeAfter       = 0o700
)

// CatalogPRDFreshInstallObservation binds the prepared replacement namespace
// to its durable fresh-install baseline and current stopped/unselected state.
// It is read-only and is accepted only for the exact replacement operation.
type CatalogPRDFreshInstallObservation struct {
	FormatVersion             int                          `json:"format_version"`
	HostID                    string                       `json:"host_id"`
	OperationID               string                       `json:"operation_id"`
	Present                   bool                         `json:"present"`
	Baseline                  *CatalogFreshInstallBaseline `json:"baseline,omitempty"`
	BootstrapReleaseID        string                       `json:"bootstrap_release_id"`
	BootstrapReleaseSHA256    string                       `json:"bootstrap_release_sha256"`
	BootstrapBundleSHA256     string                       `json:"bootstrap_bundle_sha256"`
	BootstrapJournalSHA256    string                       `json:"bootstrap_journal_sha256"`
	ServiceRoot               string                       `json:"service_root"`
	RootDevice                uint64                       `json:"root_device"`
	RootInode                 uint64                       `json:"root_inode"`
	RootUID                   uint32                       `json:"root_uid"`
	RootGID                   uint32                       `json:"root_gid"`
	RootMode                  uint32                       `json:"root_mode"`
	CurrentSelectorAbsent     bool                         `json:"current_selector_absent"`
	UnitLoadState             string                       `json:"unit_load_state"`
	UnitFileState             string                       `json:"unit_file_state"`
	UnitActiveState           string                       `json:"unit_active_state"`
	UnitSubState              string                       `json:"unit_sub_state"`
	UnitMainPID               int                          `json:"unit_main_pid"`
	UnitControlPID            int                          `json:"unit_control_pid"`
	UnitControlGroup          string                       `json:"unit_control_group"`
	UnitFragmentPath          string                       `json:"unit_fragment_path"`
	ServiceStopped            bool                         `json:"service_stopped"`
	ListenerPort              int                          `json:"listener_port"`
	ListenerAbsent            bool                         `json:"listener_absent"`
	ProcessScanComplete       bool                         `json:"process_scan_complete"`
	UnexpectedRunnerProcesses int                          `json:"unexpected_runner_processes"`
	ObservedAtUTC             string                       `json:"observed_at_utc"`
	EvidenceSHA256            string                       `json:"evidence_sha256"`
}

// ValidateCatalogPRDFreshInstallObservation rejects partial, stale-shaped,
// selected, active, or internally inconsistent observations.
func ValidateCatalogPRDFreshInstallObservation(report CatalogPRDFreshInstallObservation,
	hostID, operationID string) error {
	if report.FormatVersion != 1 || report.HostID != hostID || hostID != "linux-host" ||
		report.OperationID != operationID || operationID != prdBootstrapRootModeOperationID || !report.Present ||
		report.Baseline == nil || !validCatalogReleaseID(report.BootstrapReleaseID) ||
		!bundleDigestPattern.MatchString(report.BootstrapReleaseSHA256) || !bundleDigestPattern.MatchString(report.BootstrapBundleSHA256) ||
		!bundleDigestPattern.MatchString(report.BootstrapJournalSHA256) || report.ServiceRoot != filepath.Join("/home", config.LinuxAccount, ".local", "share", "remote-session-runner") ||
		report.RootUID == 0 || report.RootGID == 0 || report.RootDevice == 0 || report.RootInode == 0 ||
		(report.RootMode != prdBootstrapRootModeBefore && report.RootMode != prdBootstrapRootModeAfter) ||
		!report.CurrentSelectorAbsent || report.UnitLoadState != "not-found" ||
		(report.UnitFileState != "" && report.UnitFileState != "not-found") || report.UnitActiveState != "inactive" ||
		report.UnitSubState != "dead" || report.UnitMainPID != 0 || report.UnitControlPID != 0 ||
		report.UnitControlGroup != "" || report.UnitFragmentPath != "" || !report.ServiceStopped ||
		report.ListenerPort <= 0 || !report.ListenerAbsent || !report.ProcessScanComplete ||
		report.UnexpectedRunnerProcesses != 0 || !validCohortTimestamp(report.ObservedAtUTC) ||
		report.EvidenceSHA256 == "" || report.EvidenceSHA256 != catalogPRDFreshInstallObservationSHA256(report) {
		return ErrCatalogOperationInvalid
	}
	if err := validateCatalogFreshInstallBaseline(*report.Baseline, CatalogCohortHostPreflight{
		HostID: hostID, ConfigurationSHA256: report.Baseline.ConfigurationSHA256,
		SchemaVersion: report.Baseline.DatabaseSchema, WorkInventorySHA256: report.Baseline.WorkInventorySHA256,
		WorkInventoryEmpty: true,
	}); err != nil || report.Baseline.OperationID != operationID {
		return errors.Join(ErrCatalogOperationInvalid, err)
	}
	observedAt, err := time.Parse(time.RFC3339Nano, report.ObservedAtUTC)
	if err != nil || observedAt.After(time.Now().UTC().Add(10*time.Second)) || time.Since(observedAt) < 0 || time.Since(observedAt) > 2*time.Minute {
		return ErrCatalogOperationInvalid
	}
	return nil
}

// ReadCatalogPRDFreshInstallObservation is the read-only path for the exact
// bootstrapped replacement namespace before catalog selector inspection can
// run. It proves the current selector is absent, the baseline still matches,
// and the unsuffixed unit/listener/process state is stopped and stable.
func ReadCatalogPRDFreshInstallObservation(ctx context.Context, hostID, operationID string) (CatalogPRDFreshInstallObservation, error) {
	var report CatalogPRDFreshInstallObservation
	if hostID != "linux-host" || operationID != prdBootstrapRootModeOperationID || !replacementLinuxLocalIdentityMatches() {
		return report, ErrCatalogOperationInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	plan, err := BuildPRDUpgradePlan(hostID)
	if err != nil {
		return report, err
	}
	bootstrapRoot, err := PRDReplacementBootstrapJournalRoot()
	if err != nil {
		return report, err
	}
	bootstrapJournalPath := filepath.Join(bootstrapRoot, operationID+".json")
	bootstrapJournalA, exists, err := readBootstrapJournal(bootstrapJournalPath)
	if err != nil || !exists || bootstrapJournalA.OperationID != operationID ||
		!equalPRDStrings(bootstrapJournalA.CompletedSteps, prdReplacementBootstrapSteps) ||
		bootstrapJournalA.Release.HostID != hostID || !validCatalogReleaseID(bootstrapJournalA.Release.ReleaseID) ||
		!bundleDigestPattern.MatchString(bootstrapJournalA.Release.ReleaseSHA256) ||
		!bundleDigestPattern.MatchString(bootstrapJournalA.Release.BundleSHA256) {
		return report, errors.Join(ErrCatalogOperationInvalid, err)
	}
	bootstrapPlan := PRDReplacementBootstrapPlan{OperationID: bootstrapJournalA.OperationID,
		Absence: bootstrapJournalA.Absence, Namespace: plan.Namespace}
	if validatePRDReplacementBootstrapDirectories(bootstrapPlan) != nil || bootstrapJournalA.DatabaseSchema <= 0 ||
		!bundleDigestPattern.MatchString(bootstrapJournalA.WorkInventorySHA256) {
		return report, ErrCatalogOperationInvalid
	}
	catalogRoot, err := DefaultCatalogRootForPlan(Plan{HostID: hostID, Lane: config.ReleaseLanePRD, Namespace: plan.Namespace})
	if err != nil {
		return report, err
	}
	sealedRelease, err := InspectCatalogRelease(catalogRoot, hostID, bootstrapJournalA.Release.ReleaseID)
	if err != nil || sealedRelease.Identity != bootstrapJournalA.Release {
		return report, errors.Join(ErrInvalidCatalogRelease, err)
	}
	bootstrapJournalBytes, err := os.ReadFile(bootstrapJournalPath)
	if err != nil {
		return report, err
	}
	bootstrapJournalSHA := digestBytes(bootstrapJournalBytes)
	baselineA, present, err := currentFreshInstallBaseline(plan)
	if err != nil || !present || baselineA.OperationID != operationID {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	if baselineA.ConfigurationSHA256 != bootstrapJournalA.ConfigurationSHA256 || baselineA.DatabaseSchema != bootstrapJournalA.DatabaseSchema ||
		baselineA.WorkInventorySHA256 != bootstrapJournalA.WorkInventorySHA256 {
		return report, ErrPRDUpgradeGuard
	}
	rootA, err := statFreshInstallServiceRoot(plan.Namespace.ServiceRoot)
	if err != nil || (rootA.Mode != prdBootstrapRootModeBefore && rootA.Mode != prdBootstrapRootModeAfter) {
		return report, errors.Join(ErrCatalogOperationInvalid, err)
	}
	if err := requireFreshInstallSelectorAbsent(plan.Namespace.ServiceRoot); err != nil {
		return report, err
	}
	probe := NewCatalogPRDAbsenceProbe()
	unitA, err := probe.UnitState(ctx, plan.Namespace.RunnerUnit)
	if err != nil || !freshInstallUnitStopped(unitA) {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	listenerA, err := probe.HasTCPListener(ctx, plan.Namespace.DirectPort)
	if err != nil || listenerA {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	processA, err := probe.HasUnexpectedRunnerProcess(ctx, plan.Namespace.RunnerUnit)
	if err != nil || processA {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	baselineB, present, err := currentFreshInstallBaseline(plan)
	if err != nil || !present || !samePRDReplacementBootstrapBaseline(baselineA, baselineB) {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	bootstrapJournalB, found, err := readBootstrapJournal(bootstrapJournalPath)
	if err != nil || !found || bootstrapJournalB.OperationID != bootstrapJournalA.OperationID ||
		bootstrapJournalB.RequestSHA256 != bootstrapJournalA.RequestSHA256 ||
		bootstrapJournalB.ApprovalSHA256 != bootstrapJournalA.ApprovalSHA256 ||
		bootstrapJournalB.InputSHA256 != bootstrapJournalA.InputSHA256 ||
		bootstrapJournalB.Release != bootstrapJournalA.Release ||
		!equalPRDStrings(bootstrapJournalB.CompletedSteps, bootstrapJournalA.CompletedSteps) {
		return report, errors.Join(ErrCatalogOperationInvalid, err)
	}
	bootstrapJournalBytesB, err := os.ReadFile(bootstrapJournalPath)
	if err != nil || digestBytes(bootstrapJournalBytesB) != bootstrapJournalSHA {
		return report, errors.Join(ErrCatalogOperationInvalid, err)
	}
	unitB, err := probe.UnitState(ctx, plan.Namespace.RunnerUnit)
	if err != nil || unitB != unitA || !freshInstallUnitStopped(unitB) {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	listenerB, err := probe.HasTCPListener(ctx, plan.Namespace.DirectPort)
	if err != nil || listenerB || listenerA != listenerB {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	processB, err := probe.HasUnexpectedRunnerProcess(ctx, plan.Namespace.RunnerUnit)
	if err != nil || processB || processA != processB {
		return report, errors.Join(ErrPRDUpgradeGuard, err)
	}
	rootB, err := statFreshInstallServiceRoot(plan.Namespace.ServiceRoot)
	if err != nil || rootB != rootA {
		return report, errors.Join(ErrCatalogOperationInvalid, err)
	}
	if err := requireFreshInstallSelectorAbsent(plan.Namespace.ServiceRoot); err != nil {
		return report, err
	}
	report = CatalogPRDFreshInstallObservation{
		FormatVersion: 1, HostID: hostID, OperationID: operationID, Present: true, Baseline: &baselineB,
		BootstrapReleaseID: bootstrapJournalA.Release.ReleaseID, BootstrapReleaseSHA256: bootstrapJournalA.Release.ReleaseSHA256,
		BootstrapBundleSHA256: bootstrapJournalA.Release.BundleSHA256, BootstrapJournalSHA256: bootstrapJournalSHA,
		ServiceRoot: plan.Namespace.ServiceRoot, RootDevice: rootB.Device, RootInode: rootB.Inode,
		RootUID: rootB.UID, RootGID: rootB.GID, RootMode: rootB.Mode, CurrentSelectorAbsent: true,
		UnitLoadState: unitB.LoadState, UnitFileState: unitB.FileState, UnitActiveState: unitB.ActiveState,
		UnitSubState: unitB.SubState, UnitMainPID: unitB.MainPID, UnitControlPID: unitB.ControlPID,
		UnitControlGroup: unitB.ControlGroup, UnitFragmentPath: unitB.FragmentPath, ServiceStopped: true,
		ListenerPort: plan.Namespace.DirectPort, ListenerAbsent: true, ProcessScanComplete: true,
		UnexpectedRunnerProcesses: 0, ObservedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	report.EvidenceSHA256 = catalogPRDFreshInstallObservationSHA256(report)
	if err := ValidateCatalogPRDFreshInstallObservation(report, hostID, operationID); err != nil {
		return CatalogPRDFreshInstallObservation{}, err
	}
	return report, nil
}

type freshInstallRootStat struct {
	Device uint64
	Inode  uint64
	UID    uint32
	GID    uint32
	Mode   uint32
}

func statFreshInstallServiceRoot(path string) (freshInstallRootStat, error) {
	expected := filepath.Join("/home", config.LinuxAccount, ".local", "share", "remote-session-runner")
	var result freshInstallRootStat
	if path != expected || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return result, ErrCatalogOperationInvalid
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 1 ||
		stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) {
		return result, ErrCatalogOperationInvalid
	}
	return freshInstallRootStat{Device: uint64(stat.Dev), Inode: stat.Ino, UID: stat.Uid, GID: stat.Gid,
		Mode: uint32(stat.Mode & 0o7777)}, nil
}

func requireFreshInstallSelectorAbsent(serviceRoot string) error {
	path := filepath.Join(serviceRoot, "current")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrCatalogOperationInvalid
	}
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.Join(ErrCatalogOperationInvalid, err)
}

func freshInstallUnitStopped(state CatalogPRDUnitState) bool {
	return state.LoadState == "not-found" && (state.FileState == "" || state.FileState == "not-found") &&
		state.ActiveState == "inactive" && state.SubState == "dead" && state.MainPID == 0 && state.ControlPID == 0 &&
		state.ControlGroup == "" && state.FragmentPath == ""
}

func catalogPRDFreshInstallObservationSHA256(report CatalogPRDFreshInstallObservation) string {
	report.ObservedAtUTC = ""
	report.EvidenceSHA256 = ""
	data, err := json.Marshal(report)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PRDBootstrapRootModeCorrectionAuthorization is passed only by the verified
// Mac controller after it validates the current operation's continuation.
type PRDBootstrapRootModeCorrectionAuthorization struct {
	OperationID        string `json:"operation_id"`
	RequestSHA256      string `json:"request_sha256"`
	ApprovalSHA256     string `json:"approval_sha256"`
	ReleaseID          string `json:"release_id"`
	ReleaseSHA256      string `json:"release_sha256"`
	ContinuationSHA256 string `json:"continuation_sha256"`
}

type PRDBootstrapRootModeCorrectionResult struct {
	OperationID       string `json:"operation_id"`
	ServiceRoot       string `json:"service_root"`
	PreviousMode      uint32 `json:"previous_mode"`
	CurrentMode       uint32 `json:"current_mode"`
	IntentSHA256      string `json:"intent_sha256"`
	CompletionSHA256  string `json:"completion_sha256"`
	ObservationSHA256 string `json:"observation_sha256"`
}

type prdBootstrapRootModeCorrectionRecord struct {
	FormatVersion           int    `json:"format_version"`
	Stage                   string `json:"stage"`
	OperationID             string `json:"operation_id"`
	RequestSHA256           string `json:"request_sha256"`
	ApprovalSHA256          string `json:"approval_sha256"`
	ReleaseID               string `json:"release_id"`
	ReleaseSHA256           string `json:"release_sha256"`
	ContinuationSHA256      string `json:"continuation_sha256"`
	ServiceRoot             string `json:"service_root"`
	RootDevice              uint64 `json:"root_device"`
	RootInode               uint64 `json:"root_inode"`
	RootUID                 uint32 `json:"root_uid"`
	RootGID                 uint32 `json:"root_gid"`
	PreviousMode            uint32 `json:"previous_mode"`
	TargetMode              uint32 `json:"target_mode"`
	FreshInstallEvidenceSHA string `json:"fresh_install_evidence_sha256"`
	BootstrapJournalSHA256  string `json:"bootstrap_journal_sha256"`
	ConfigurationSHA256     string `json:"configuration_sha256"`
	SchemaVersion           int    `json:"schema_version"`
	WorkInventorySHA256     string `json:"work_inventory_sha256"`
	IntentSHA256            string `json:"intent_sha256,omitempty"`
	RecordedAtUTC           string `json:"recorded_at_utc"`
	EvidenceSHA256          string `json:"evidence_sha256"`
}

// CorrectPRDReplacementBootstrapServiceRootMode performs the one operation-
// bound permission correction required by the completed replacement bootstrap.
// It changes only the exact service root from the observed 0775 mode to 0700.
func CorrectPRDReplacementBootstrapServiceRootMode(ctx context.Context,
	authorization PRDBootstrapRootModeCorrectionAuthorization) (PRDBootstrapRootModeCorrectionResult, error) {
	var result PRDBootstrapRootModeCorrectionResult
	if authorization.OperationID != prdBootstrapRootModeOperationID || !validBootstrapDigest(authorization.RequestSHA256) ||
		!validBootstrapDigest(authorization.ApprovalSHA256) || !validCatalogReleaseID(authorization.ReleaseID) ||
		!validBootstrapDigest(authorization.ReleaseSHA256) || !validBootstrapDigest(authorization.ContinuationSHA256) ||
		!replacementLinuxLocalIdentityMatches() {
		return result, ErrCatalogOperationInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := PRDReplacementBootstrapJournalRoot()
	if err != nil {
		return result, errors.Join(ErrCatalogOperationInvalid, err)
	}
	if err := ensureBootstrapJournalRootAtRoot(root); err != nil {
		return result, errors.Join(ErrCatalogOperationInvalid, err)
	}
	cohortLock, err := acquireBootstrapLock(filepath.Join(root, "linux-host-prd-bootstrap.lock"))
	if err != nil {
		return result, err
	}
	defer releaseBootstrapLock(cohortLock)
	operationLock, err := acquireBootstrapLock(filepath.Join(root, authorization.OperationID+".lock"))
	if err != nil {
		return result, err
	}
	defer releaseBootstrapLock(operationLock)

	journalPath := filepath.Join(root, authorization.OperationID+".json")
	journal, exists, err := readBootstrapJournal(journalPath)
	if err != nil || !exists || journal.OperationID != authorization.OperationID ||
		journal.RequestSHA256 != authorization.RequestSHA256 || journal.ApprovalSHA256 != authorization.ApprovalSHA256 ||
		journal.Release.HostID != "linux-host" || journal.Release.ReleaseID != authorization.ReleaseID ||
		journal.Release.ReleaseSHA256 != authorization.ReleaseSHA256 ||
		!equalPRDStrings(journal.CompletedSteps, prdReplacementBootstrapSteps) {
		return result, errors.Join(ErrCatalogOperationInvalid, err)
	}
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		return result, err
	}
	journalSHA := digestBytes(journalBytes)
	upgrade, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		return result, err
	}
	bootstrapPlan := PRDReplacementBootstrapPlan{OperationID: journal.OperationID, Absence: journal.Absence, Namespace: upgrade.Namespace}
	if validatePRDReplacementBootstrapDirectories(bootstrapPlan) != nil || journal.DatabaseSchema <= 0 ||
		!bundleDigestPattern.MatchString(journal.WorkInventorySHA256) {
		return result, ErrCatalogOperationInvalid
	}
	observation, err := ReadCatalogPRDFreshInstallObservation(ctx, "linux-host", authorization.OperationID)
	if err != nil || observation.Baseline == nil || observation.BootstrapReleaseID != authorization.ReleaseID ||
		observation.BootstrapReleaseSHA256 != authorization.ReleaseSHA256 ||
		observation.BootstrapBundleSHA256 != journal.Release.BundleSHA256 || observation.BootstrapJournalSHA256 != journalSHA ||
		observation.Baseline.ConfigurationSHA256 != journal.ConfigurationSHA256 ||
		observation.Baseline.DatabaseSchema != journal.DatabaseSchema || observation.Baseline.WorkInventorySHA256 != journal.WorkInventorySHA256 {
		return result, errors.Join(ErrPRDUpgradeGuard, err)
	}
	if observation.RootMode != prdBootstrapRootModeBefore && observation.RootMode != prdBootstrapRootModeAfter {
		return result, ErrCatalogOperationInvalid
	}
	intentPath := filepath.Join(root, authorization.OperationID+".root-mode-intent.json")
	completionPath := filepath.Join(root, authorization.OperationID+".root-mode-complete.json")
	intent, intentExists, err := readPRDBootstrapRootModeCorrectionRecord(intentPath)
	if err != nil {
		return result, err
	}
	complete, completeExists, err := readPRDBootstrapRootModeCorrectionRecord(completionPath)
	if err != nil {
		return result, err
	}
	candidate := rootModeCorrectionRecord("intent", authorization, observation, time.Now().UTC())
	if !intentExists {
		if completeExists || observation.RootMode != prdBootstrapRootModeBefore {
			return result, ErrCatalogOperationInvalid
		}
		intent = candidate
		if err := writePRDBootstrapRootModeCorrectionRecord(intentPath, intent); err != nil {
			return result, err
		}
		intentExists = true
	} else if !samePRDBootstrapRootModeCorrectionBindings(intent, candidate) || intent.Stage != "intent" || intent.IntentSHA256 != "" {
		return result, ErrCatalogOperationInvalid
	}
	intentBytes, err := os.ReadFile(intentPath)
	if err != nil {
		return result, err
	}
	intentSHA := digestBytes(intentBytes)
	if completeExists {
		if !samePRDBootstrapRootModeCorrectionBindings(complete, intent) || complete.Stage != "complete" ||
			complete.IntentSHA256 != intentSHA || observation.RootMode != prdBootstrapRootModeAfter {
			return result, ErrCatalogOperationInvalid
		}
		completeBytes, readErr := os.ReadFile(completionPath)
		if readErr != nil {
			return result, readErr
		}
		return PRDBootstrapRootModeCorrectionResult{OperationID: authorization.OperationID, ServiceRoot: observation.ServiceRoot,
			PreviousMode: complete.PreviousMode, CurrentMode: observation.RootMode, IntentSHA256: intentSHA,
			CompletionSHA256: digestBytes(completeBytes), ObservationSHA256: observation.EvidenceSHA256}, nil
	}
	if observation.RootMode == prdBootstrapRootModeBefore {
		if err := chmodFreshInstallRootPrivate(observation); err != nil {
			return result, err
		}
	} else if observation.RootMode != prdBootstrapRootModeAfter {
		return result, ErrCatalogOperationInvalid
	}
	post, err := ReadCatalogPRDFreshInstallObservation(ctx, "linux-host", authorization.OperationID)
	if err != nil || post.RootMode != prdBootstrapRootModeAfter || post.Baseline == nil ||
		!samePRDReplacementBootstrapBaseline(*observation.Baseline, *post.Baseline) ||
		post.RootDevice != observation.RootDevice || post.RootInode != observation.RootInode ||
		post.BootstrapJournalSHA256 != observation.BootstrapJournalSHA256 ||
		post.BootstrapReleaseID != observation.BootstrapReleaseID || post.BootstrapReleaseSHA256 != observation.BootstrapReleaseSHA256 ||
		post.BootstrapBundleSHA256 != observation.BootstrapBundleSHA256 {
		return result, errors.Join(ErrPRDUpgradeGuard, err)
	}
	complete = rootModeCorrectionRecord("complete", authorization, post, time.Now().UTC())
	complete.IntentSHA256 = intentSHA
	complete.EvidenceSHA256 = rootModeCorrectionRecordSHA256(complete)
	if err := writePRDBootstrapRootModeCorrectionRecord(completionPath, complete); err != nil {
		return result, err
	}
	completionBytes, err := os.ReadFile(completionPath)
	if err != nil {
		return result, err
	}
	return PRDBootstrapRootModeCorrectionResult{OperationID: authorization.OperationID, ServiceRoot: post.ServiceRoot,
		PreviousMode: prdBootstrapRootModeBefore, CurrentMode: post.RootMode, IntentSHA256: intentSHA,
		CompletionSHA256: digestBytes(completionBytes), ObservationSHA256: post.EvidenceSHA256}, nil
}

func rootModeCorrectionRecord(stage string, authorization PRDBootstrapRootModeCorrectionAuthorization,
	observation CatalogPRDFreshInstallObservation, recorded time.Time) prdBootstrapRootModeCorrectionRecord {
	record := prdBootstrapRootModeCorrectionRecord{FormatVersion: 1, Stage: stage,
		OperationID: authorization.OperationID, RequestSHA256: authorization.RequestSHA256,
		ApprovalSHA256: authorization.ApprovalSHA256, ReleaseID: authorization.ReleaseID,
		ReleaseSHA256: authorization.ReleaseSHA256, ContinuationSHA256: authorization.ContinuationSHA256,
		ServiceRoot: observation.ServiceRoot, RootDevice: observation.RootDevice, RootInode: observation.RootInode,
		RootUID: observation.RootUID, RootGID: observation.RootGID, PreviousMode: prdBootstrapRootModeBefore,
		TargetMode: prdBootstrapRootModeAfter, FreshInstallEvidenceSHA: observation.Baseline.EvidenceSHA256,
		BootstrapJournalSHA256: observation.BootstrapJournalSHA256,
		ConfigurationSHA256:    observation.Baseline.ConfigurationSHA256, SchemaVersion: observation.Baseline.DatabaseSchema,
		WorkInventorySHA256: observation.Baseline.WorkInventorySHA256, RecordedAtUTC: recorded.UTC().Format(time.RFC3339Nano)}
	record.EvidenceSHA256 = rootModeCorrectionRecordSHA256(record)
	return record
}

func rootModeCorrectionRecordSHA256(record prdBootstrapRootModeCorrectionRecord) string {
	record.EvidenceSHA256 = ""
	data, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func samePRDBootstrapRootModeCorrectionBindings(a, b prdBootstrapRootModeCorrectionRecord) bool {
	return a.FormatVersion == b.FormatVersion && a.OperationID == b.OperationID && a.RequestSHA256 == b.RequestSHA256 &&
		a.ApprovalSHA256 == b.ApprovalSHA256 && a.ReleaseID == b.ReleaseID && a.ReleaseSHA256 == b.ReleaseSHA256 &&
		a.ContinuationSHA256 == b.ContinuationSHA256 && a.ServiceRoot == b.ServiceRoot && a.RootDevice == b.RootDevice &&
		a.RootInode == b.RootInode && a.RootUID == b.RootUID && a.RootGID == b.RootGID &&
		a.PreviousMode == b.PreviousMode && a.TargetMode == b.TargetMode &&
		a.FreshInstallEvidenceSHA == b.FreshInstallEvidenceSHA && a.BootstrapJournalSHA256 == b.BootstrapJournalSHA256 &&
		a.ConfigurationSHA256 == b.ConfigurationSHA256 &&
		a.SchemaVersion == b.SchemaVersion && a.WorkInventorySHA256 == b.WorkInventorySHA256
}

func readPRDBootstrapRootModeCorrectionRecord(path string) (prdBootstrapRootModeCorrectionRecord, bool, error) {
	var record prdBootstrapRootModeCorrectionRecord
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	var stat unix.Stat_t
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > 64<<10 || unix.Lstat(path, &stat) != nil ||
		stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return record, false, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return record, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!bytes.Equal(data, append(mustJSONIndent(record), '\n')) || record.FormatVersion != 1 ||
		(record.Stage != "intent" && record.Stage != "complete") || record.OperationID != prdBootstrapRootModeOperationID ||
		!validBootstrapDigest(record.RequestSHA256) || !validBootstrapDigest(record.ApprovalSHA256) ||
		!validCatalogReleaseID(record.ReleaseID) || !validBootstrapDigest(record.ReleaseSHA256) ||
		!validBootstrapDigest(record.ContinuationSHA256) || !validBootstrapDigest(record.FreshInstallEvidenceSHA) ||
		!validBootstrapDigest(record.BootstrapJournalSHA256) ||
		!validBootstrapDigest(record.ConfigurationSHA256) || !validBootstrapDigest(record.WorkInventorySHA256) ||
		record.ServiceRoot != filepath.Join("/home", config.LinuxAccount, ".local", "share", "remote-session-runner") ||
		record.RootUID != uint32(os.Getuid()) || record.RootGID != uint32(os.Getgid()) || record.RootDevice == 0 || record.RootInode == 0 ||
		record.PreviousMode != prdBootstrapRootModeBefore || record.TargetMode != prdBootstrapRootModeAfter || record.SchemaVersion <= 0 ||
		!validCohortTimestamp(record.RecordedAtUTC) || record.EvidenceSHA256 != rootModeCorrectionRecordSHA256(record) ||
		(record.Stage == "intent" && record.IntentSHA256 != "") ||
		(record.Stage == "complete" && !validBootstrapDigest(record.IntentSHA256)) {
		return prdBootstrapRootModeCorrectionRecord{}, false, ErrCatalogOperationInvalid
	}
	return record, true, nil
}

func mustJSONIndent(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}

func writePRDBootstrapRootModeCorrectionRecord(path string, record prdBootstrapRootModeCorrectionRecord) error {
	if record.EvidenceSHA256 != rootModeCorrectionRecordSHA256(record) {
		return ErrCatalogOperationInvalid
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writePrivateSynced(path, data, 0o600); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func chmodFreshInstallRootPrivate(observation CatalogPRDFreshInstallObservation) error {
	if ValidateCatalogPRDFreshInstallObservation(observation, "linux-host", prdBootstrapRootModeOperationID) != nil ||
		observation.RootMode != prdBootstrapRootModeBefore {
		return ErrCatalogOperationInvalid
	}
	return chmodFreshInstallRootPrivateAt(observation.ServiceRoot, freshInstallRootStat{Device: observation.RootDevice,
		Inode: observation.RootInode, UID: observation.RootUID, GID: observation.RootGID, Mode: observation.RootMode})
}

func chmodFreshInstallRootPrivateAt(path string, expected freshInstallRootStat) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || expected.Device == 0 || expected.Inode == 0 ||
		expected.UID != uint32(os.Getuid()) || expected.GID != uint32(os.Getgid()) || expected.Mode != prdBootstrapRootModeBefore {
		return ErrCatalogOperationInvalid
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(ErrCatalogOperationInvalid, err)
	}
	defer unix.Close(fd)
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || uint64(before.Dev) != expected.Device || before.Ino != expected.Inode ||
		before.Uid != expected.UID || before.Gid != expected.GID || before.Mode&0o7777 != prdBootstrapRootModeBefore {
		return ErrCatalogOperationInvalid
	}
	if err := unix.Fchmod(fd, prdBootstrapRootModeAfter); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	var after unix.Stat_t
	var pathAfter unix.Stat_t
	if unix.Fstat(fd, &after) != nil || unix.Lstat(path, &pathAfter) != nil ||
		after.Dev != before.Dev || after.Ino != before.Ino || pathAfter.Dev != before.Dev || pathAfter.Ino != before.Ino ||
		after.Uid != before.Uid || after.Gid != before.Gid || after.Mode&0o7777 != prdBootstrapRootModeAfter ||
		pathAfter.Mode&unix.S_IFMT != unix.S_IFDIR || pathAfter.Mode&0o7777 != prdBootstrapRootModeAfter {
		return fmt.Errorf("replacement PRD service root identity or mode changed during correction: %w", ErrCatalogOperationInvalid)
	}
	return nil
}

func validBootstrapDigest(value string) bool { return bundleDigestPattern.MatchString(value) }
