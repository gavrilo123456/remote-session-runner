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
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
)

// ReplacementLinuxSSH identifies the explicitly approved replacement worker.
// This narrow target is shared by the release-profile guard and the local
// first-install observer so those two gates cannot drift independently.
const ReplacementLinuxSSH = "ubuntu@79.72.94.59"

// ReplacementLinuxPrivateIP binds local first-install commands to the already
// verified replacement instance, not another Ubuntu host with the same user.
const ReplacementLinuxPrivateIP = config.PRDReplacementLinuxPrivateIP

// CatalogNamespaceAbsenceReport records a complete, read-only observation
// that a narrowly eligible replacement Linux peer lane has no runtime
// namespace. It is not a health result and cannot be used as a selected-lane
// baseline.
type CatalogNamespaceAbsenceReport struct {
	FormatVersion             int                `json:"format_version"`
	HostID                    string             `json:"host_id"`
	Lane                      config.ReleaseLane `json:"lane"`
	SelectedLane              config.ReleaseLane `json:"selected_lane"`
	State                     string             `json:"state"`
	ServiceRoot               string             `json:"service_root"`
	ServiceRootState          string             `json:"service_root_state"`
	ToolchainRoot             string             `json:"toolchain_root,omitempty"`
	ToolchainState            string             `json:"toolchain_state,omitempty"`
	ToolchainVersion          string             `json:"toolchain_version,omitempty"`
	ToolchainBinarySHA256     string             `json:"toolchain_binary_sha256,omitempty"`
	ConfigurationPath         string             `json:"configuration_path"`
	ConfigurationState        string             `json:"configuration_state"`
	DatabasePath              string             `json:"database_path"`
	DatabaseState             string             `json:"database_state"`
	SelectorJournalRoot       string             `json:"selector_journal_root"`
	SelectorJournalsState     string             `json:"selector_journals_state"`
	CohortReceiptRoot         string             `json:"cohort_receipt_root"`
	CohortReceiptsState       string             `json:"cohort_receipts_state"`
	BackupRoot                string             `json:"backup_root"`
	BackupState               string             `json:"backup_state"`
	LifecycleLock             string             `json:"lifecycle_lock"`
	LifecycleLockState        string             `json:"lifecycle_lock_state"`
	ServiceUnit               string             `json:"service_unit"`
	UnitLoadState             string             `json:"unit_load_state"`
	UnitFileState             string             `json:"unit_file_state"`
	UnitActiveState           string             `json:"unit_active_state"`
	UnitSubState              string             `json:"unit_sub_state"`
	UnitMainPID               int                `json:"unit_main_pid"`
	UnitControlPID            int                `json:"unit_control_pid"`
	UnitControlGroup          string             `json:"unit_control_group"`
	UnitFragmentPath          string             `json:"unit_fragment_path"`
	DirectListenerPort        int                `json:"direct_listener_port"`
	DirectListenerAbsent      bool               `json:"direct_listener_absent"`
	ProcessScanComplete       bool               `json:"process_scan_complete"`
	UnexpectedRunnerProcesses int                `json:"unexpected_runner_processes"`
	ObservedAtUTC             string             `json:"observed_at_utc"`
	EvidenceSHA256            string             `json:"evidence_sha256"`
}

// CatalogPRDUnitState is the fixed systemd state used to prove that one
// canonical Runner service is not installed or loaded.
type CatalogPRDUnitState struct {
	LoadState    string
	FileState    string
	ActiveState  string
	SubState     string
	MainPID      int
	ControlPID   int
	ControlGroup string
	FragmentPath string
}

// CatalogNamespaceAbsenceProbe provides only fixed read-only observations.
type CatalogNamespaceAbsenceProbe interface {
	UnitState(context.Context, string) (CatalogPRDUnitState, error)
	HasTCPListener(context.Context, int) (bool, error)
	HasUnexpectedRunnerProcess(context.Context, string) (bool, error)
}

// CatalogPRDAbsenceProbe is retained as a source-compatible name for existing
// callers that observe the replacement worker's PRD peer.
type CatalogPRDAbsenceProbe = CatalogNamespaceAbsenceProbe

// ReadCatalogPRDNamespaceAbsence is restricted to the replacement Linux host
// and its unsuffixed PRD namespace. It succeeds only for complete absence;
// present, partial, unreadable, or changing state is an error.
func ReadCatalogPRDNamespaceAbsence(ctx context.Context, hostID string, probe CatalogPRDAbsenceProbe) (CatalogNamespaceAbsenceReport, error) {
	return ReadCatalogPeerNamespaceAbsence(ctx, hostID, config.ReleaseLanePRD, config.ReleaseLaneDev, probe)
}

// ReadCatalogPeerNamespaceAbsence is the narrow first-install observer for
// the replacement Linux worker. It can record PRD absence during Dev/QAS or
// QAS absence during Dev; it never treats an absent lane as healthy or usable.
func ReadCatalogPeerNamespaceAbsence(ctx context.Context, hostID string, peerLane, selectedLane config.ReleaseLane, probe CatalogNamespaceAbsenceProbe) (CatalogNamespaceAbsenceReport, error) {
	var report CatalogNamespaceAbsenceReport
	if !CatalogNamespaceAbsenceScopeAllowed(hostID, peerLane, selectedLane) || probe == nil {
		return report, ErrInvalidCatalogRelease
	}
	var namespace config.LaneNamespace
	if peerLane == config.ReleaseLanePRD {
		plan, err := BuildCatalogPlan(hostID, peerLane)
		if err != nil {
			return report, ErrInvalidCatalogRelease
		}
		namespace = plan.Namespace
	} else {
		plan, err := BuildSimplePlan(hostID, peerLane)
		if err != nil {
			return report, ErrInvalidCatalogRelease
		}
		namespace = plan.Namespace
	}
	current, err := user.Current()
	if err != nil || current.Username != namespace.Account {
		return report, ErrInvalidCatalogRelease
	}
	return readCatalogNamespaceAbsence(ctx, hostID, peerLane, selectedLane, namespace, current.Username, uint32(os.Getuid()), probe)
}

func readCatalogNamespaceAbsence(ctx context.Context, hostID string, peerLane, selectedLane config.ReleaseLane,
	namespace config.LaneNamespace, currentUsername string, currentUID uint32, probe CatalogNamespaceAbsenceProbe) (CatalogNamespaceAbsenceReport, error) {
	var report CatalogNamespaceAbsenceReport
	expected, err := catalogNamespaceForAbsence(hostID, peerLane)
	if err != nil || !CatalogNamespaceAbsenceScopeAllowed(hostID, peerLane, selectedLane) || probe == nil ||
		currentUsername != namespace.Account || namespace != expected {
		return report, ErrInvalidCatalogRelease
	}
	return observeCatalogNamespaceAbsence(ctx, hostID, peerLane, selectedLane, namespace, currentUID, probe)
}

func observeCatalogNamespaceAbsence(ctx context.Context, hostID string, peerLane, selectedLane config.ReleaseLane,
	namespace config.LaneNamespace, currentUID uint32, probe CatalogNamespaceAbsenceProbe) (CatalogNamespaceAbsenceReport, error) {
	var report CatalogNamespaceAbsenceReport
	if !CatalogNamespaceAbsenceScopeAllowed(hostID, peerLane, selectedLane) || probe == nil {
		return report, ErrInvalidCatalogRelease
	}
	parent := filepath.Dir(namespace.ServiceRoot)
	if err := validateCatalogAbsenceParent(parent, currentUID); err != nil {
		return report, fmt.Errorf("inspect %s namespace parent: %w", peerLane, ErrCatalogOperationInvalid)
	}
	root := catalogRuntimeRootObservation{State: "absent"}
	var err error
	if peerLane == config.ReleaseLanePRD {
		root, err = inspectCatalogRuntimeRoot(PRDUpgradePlan{HostID: hostID, Namespace: namespace}, currentUID)
		if err != nil {
			return report, err
		} else if err := requireCatalogPathAbsent(namespace.ServiceRoot); err != nil && root.State != "toolchain-only" {
			return report, err
		}
	} else if err := requireCatalogPathAbsent(namespace.ServiceRoot); err != nil {
		return report, err
	}
	unitA, err := probe.UnitState(ctx, namespace.RunnerUnit)
	if err != nil {
		return report, fmt.Errorf("inspect canonical %s unit: %w", peerLane, err)
	}
	listenerA, err := probe.HasTCPListener(ctx, namespace.DirectPort)
	if err != nil {
		return report, fmt.Errorf("inspect canonical %s direct port: %w", peerLane, err)
	}
	processA, err := probe.HasUnexpectedRunnerProcess(ctx, namespace.RunnerUnit)
	if err != nil {
		return report, fmt.Errorf("scan Runner process state: %w", err)
	}
	unitB, err := probe.UnitState(ctx, namespace.RunnerUnit)
	if err != nil {
		return report, fmt.Errorf("recheck canonical %s unit: %w", peerLane, err)
	}
	listenerB, err := probe.HasTCPListener(ctx, namespace.DirectPort)
	if err != nil {
		return report, fmt.Errorf("recheck canonical %s direct port: %w", peerLane, err)
	}
	processB, err := probe.HasUnexpectedRunnerProcess(ctx, namespace.RunnerUnit)
	if err != nil {
		return report, fmt.Errorf("recheck Runner process state: %w", err)
	}
	for _, path := range catalogNamespaceArtifacts(namespace) {
		if err := requireCatalogPathAbsent(path); err != nil {
			return report, fmt.Errorf("namespace artifact is present or unreadable: %w", err)
		}
	}
	rootAgain := catalogRuntimeRootObservation{State: "absent"}
	if peerLane == config.ReleaseLanePRD {
		rootAgain, err = inspectCatalogRuntimeRoot(PRDUpgradePlan{HostID: hostID, Namespace: namespace}, currentUID)
	} else if err := requireCatalogPathAbsent(namespace.ServiceRoot); err != nil {
		return report, err
	}
	if err != nil || rootAgain != root {
		return report, errors.Join(fmt.Errorf("recheck namespace runtime root: %w", ErrCatalogOperationInvalid), err)
	}
	if unitA != unitB || listenerA || listenerB || processA || processB ||
		unitA.LoadState != "not-found" || (unitA.FileState != "" && unitA.FileState != "not-found") || unitA.ActiveState != "inactive" || unitA.SubState != "dead" ||
		unitA.MainPID != 0 || unitA.ControlPID != 0 || unitA.ControlGroup != "" || unitA.FragmentPath != "" {
		return report, fmt.Errorf("namespace unit, listener, or process state is present or changed: %w", ErrCatalogOperationInvalid)
	}
	report = CatalogNamespaceAbsenceReport{
		FormatVersion: 1, HostID: hostID, Lane: peerLane, SelectedLane: selectedLane, State: "absent",
		ServiceRoot: namespace.ServiceRoot, ServiceRootState: root.State, ToolchainRoot: root.ToolchainRoot,
		ToolchainState: root.ToolchainState, ToolchainVersion: root.ToolchainVersion, ToolchainBinarySHA256: root.ToolchainBinarySHA256,
		ConfigurationPath: namespace.ConfigFile, ConfigurationState: "absent",
		DatabasePath: namespace.Database, DatabaseState: "absent",
		SelectorJournalRoot: filepath.Join(namespace.Staging, "catalog-operations"), SelectorJournalsState: "absent",
		CohortReceiptRoot: filepath.Join(namespace.Staging, "catalog-cohorts"), CohortReceiptsState: "absent",
		BackupRoot: namespace.Backups, BackupState: "absent", LifecycleLock: namespace.LifecycleLock, LifecycleLockState: "absent",
		ServiceUnit: namespace.RunnerUnit, UnitLoadState: unitA.LoadState, UnitFileState: unitA.FileState,
		UnitActiveState: unitA.ActiveState, UnitSubState: unitA.SubState, UnitMainPID: unitA.MainPID,
		UnitControlPID: unitA.ControlPID, UnitControlGroup: unitA.ControlGroup, UnitFragmentPath: unitA.FragmentPath,
		DirectListenerPort: namespace.DirectPort, DirectListenerAbsent: true, ProcessScanComplete: true,
		UnexpectedRunnerProcesses: 0, ObservedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	report.EvidenceSHA256 = catalogNamespaceAbsenceEvidenceSHA256(report)
	if validationErr := validateCatalogNamespaceAbsenceReportForNamespace(report, hostID, peerLane, selectedLane, namespace); validationErr != nil {
		return report, fmt.Errorf("validate namespace absence report: %w", validationErr)
	}
	return report, nil
}

// CatalogNamespaceAbsenceScopeAllowed enumerates the fixed replacement-host
// absence observations used by promotion planning and first-install checks.
func CatalogNamespaceAbsenceScopeAllowed(hostID string, peerLane, selectedLane config.ReleaseLane) bool {
	return hostID == "linux-host" && ((peerLane == config.ReleaseLanePRD &&
		(selectedLane == config.ReleaseLaneDev || selectedLane == config.ReleaseLaneQAS || selectedLane == config.ReleaseLanePRD)) ||
		(peerLane == config.ReleaseLaneQAS && selectedLane == config.ReleaseLaneDev))
}

func validateCatalogAbsenceParent(path string, uid uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidCatalogRelease
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	parts := splitPath(path[len(volume):])
	for index, part := range parts {
		current = filepath.Join(current, part)
		var st unix.Stat_t
		if unix.Lstat(current, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return ErrInvalidCatalogRelease
		}
		if st.Mode&unix.S_IFLNK != 0 {
			return ErrInvalidCatalogRelease
		}
		worldWritable := st.Mode&0o002 != 0
		groupWritable := st.Mode&0o020 != 0
		sticky := st.Mode&unix.S_ISVTX != 0
		if index == len(parts)-1 {
			if st.Uid != uid || worldWritable || groupWritable && st.Gid != uint32(os.Getgid()) {
				return ErrInvalidCatalogRelease
			}
		} else if (worldWritable || groupWritable && st.Gid != uint32(os.Getgid())) && !sticky {
			return ErrInvalidCatalogRelease
		}
	}
	return nil
}

type catalogRuntimeRootObservation struct {
	State                 string
	ToolchainRoot         string
	ToolchainState        string
	ToolchainVersion      string
	ToolchainBinarySHA256 string
}

func inspectCatalogRuntimeRoot(plan PRDUpgradePlan, uid uint32) (catalogRuntimeRootObservation, error) {
	var observed catalogRuntimeRootObservation
	root := plan.Namespace.ServiceRoot
	var rootStat unix.Stat_t
	if err := unix.Lstat(root, &rootStat); errors.Is(err, unix.ENOENT) {
		observed.State = "absent"
		return observed, nil
	} else if err != nil {
		return observed, ErrCatalogOperationInvalid
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || !catalogOwnedDirectory(rootStat, uid) {
		return observed, ErrCatalogOperationInvalid
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "toolchains" || !entries[0].IsDir() {
		return observed, ErrCatalogOperationInvalid
	}
	toolchainRoot := filepath.Join(root, "toolchains")
	var toolchainStat unix.Stat_t
	if unix.Lstat(toolchainRoot, &toolchainStat) != nil || toolchainStat.Mode&unix.S_IFMT != unix.S_IFDIR || !catalogOwnedDirectory(toolchainStat, uid) {
		return observed, ErrCatalogOperationInvalid
	}
	versions, err := os.ReadDir(toolchainRoot)
	if err != nil || len(versions) != 1 || versions[0].Name() != "go1.27.1" || !versions[0].IsDir() {
		return observed, ErrCatalogOperationInvalid
	}
	goRoot := filepath.Join(toolchainRoot, "go1.27.1")
	var goRootStat unix.Stat_t
	if unix.Lstat(goRoot, &goRootStat) != nil || goRootStat.Mode&unix.S_IFMT != unix.S_IFDIR || !catalogOwnedDirectory(goRootStat, uid) {
		return observed, ErrCatalogOperationInvalid
	}
	versionPath := filepath.Join(goRoot, "VERSION")
	var versionStat unix.Stat_t
	if unix.Lstat(versionPath, &versionStat) != nil || versionStat.Mode&unix.S_IFMT != unix.S_IFREG || versionStat.Uid != uid || versionStat.Size > 256 {
		return observed, ErrCatalogOperationInvalid
	}
	versionBytes, err := os.ReadFile(versionPath)
	if err != nil || len(versionBytes) == 0 || len(versionBytes) > 256 || strings.SplitN(strings.TrimSpace(string(versionBytes)), "\n", 2)[0] != "go1.27.1" {
		return observed, ErrCatalogOperationInvalid
	}
	binRoot := filepath.Join(goRoot, "bin")
	var binStat unix.Stat_t
	if unix.Lstat(binRoot, &binStat) != nil || binStat.Mode&unix.S_IFMT != unix.S_IFDIR || binStat.Uid != uid {
		return observed, ErrCatalogOperationInvalid
	}
	goBinary := filepath.Join(binRoot, "go")
	var goStat unix.Stat_t
	if unix.Lstat(goBinary, &goStat) != nil || goStat.Mode&unix.S_IFMT != unix.S_IFREG || goStat.Uid != uid || goStat.Size <= 0 || goStat.Size > 64<<20 || goStat.Mode&0o111 == 0 {
		return observed, ErrCatalogOperationInvalid
	}
	binaryBytes, err := os.ReadFile(goBinary)
	if err != nil || int64(len(binaryBytes)) != goStat.Size {
		return observed, ErrCatalogOperationInvalid
	}
	binarySHA := sha256.Sum256(binaryBytes)
	observed = catalogRuntimeRootObservation{State: "toolchain-only", ToolchainRoot: toolchainRoot, ToolchainState: "owner-toolchain-only",
		ToolchainVersion: "go1.27.1", ToolchainBinarySHA256: hex.EncodeToString(binarySHA[:])}
	return observed, nil
}

func catalogOwnedDirectory(stat unix.Stat_t, uid uint32) bool {
	return stat.Uid == uid && stat.Mode&0o002 == 0 && (stat.Mode&0o020 == 0 || stat.Gid == uint32(os.Getgid()))
}

func catalogNamespaceArtifacts(namespace config.LaneNamespace) []string {
	paths := []string{namespace.ConfigFile, namespace.Database, namespace.DatabaseWAL, namespace.DatabaseSHM,
		namespace.Binaries, namespace.Staging, namespace.Backups, namespace.LifecycleLock}
	if namespace.MailboxRoot != "" {
		paths = append(paths, namespace.MailboxRoot)
	}
	return paths
}

// ValidateCatalogNamespaceAbsenceReport validates the exact replacement-host
// absent-peer evidence without trusting its timestamp as a status claim.
func ValidateCatalogNamespaceAbsenceReport(report CatalogNamespaceAbsenceReport, hostID string, peerLane, selectedLane config.ReleaseLane) error {
	if !CatalogNamespaceAbsenceScopeAllowed(hostID, peerLane, selectedLane) {
		return ErrCatalogOperationInvalid
	}
	namespace, err := catalogNamespaceForAbsence(hostID, peerLane)
	if err != nil {
		return ErrCatalogOperationInvalid
	}
	return validateCatalogNamespaceAbsenceReportForNamespace(report, hostID, peerLane, selectedLane, namespace)
}

func validateCatalogNamespaceAbsenceReportForNamespace(report CatalogNamespaceAbsenceReport, hostID string,
	peerLane, selectedLane config.ReleaseLane, namespace config.LaneNamespace) error {
	rootStateValid := report.ServiceRootState == "absent" && report.ToolchainRoot == "" && report.ToolchainState == "" &&
		report.ToolchainVersion == "" && report.ToolchainBinarySHA256 == "" ||
		peerLane == config.ReleaseLanePRD && report.ServiceRootState == "toolchain-only" && report.ToolchainRoot == filepath.Join(namespace.ServiceRoot, "toolchains") &&
			report.ToolchainState == "owner-toolchain-only" && report.ToolchainVersion == "go1.27.1" && prdDigestPattern.MatchString(report.ToolchainBinarySHA256)
	if !CatalogNamespaceAbsenceScopeAllowed(hostID, peerLane, selectedLane) || report.FormatVersion != 1 || report.HostID != hostID || report.Lane != peerLane || report.SelectedLane != selectedLane || report.State != "absent" ||
		report.ServiceRoot != namespace.ServiceRoot || !rootStateValid ||
		report.ConfigurationPath != namespace.ConfigFile || report.ConfigurationState != "absent" ||
		report.DatabasePath != namespace.Database || report.DatabaseState != "absent" ||
		report.SelectorJournalRoot != filepath.Join(namespace.Staging, "catalog-operations") || report.SelectorJournalsState != "absent" ||
		report.CohortReceiptRoot != filepath.Join(namespace.Staging, "catalog-cohorts") || report.CohortReceiptsState != "absent" ||
		report.BackupRoot != namespace.Backups || report.BackupState != "absent" ||
		report.LifecycleLock != namespace.LifecycleLock || report.LifecycleLockState != "absent" ||
		report.ServiceUnit != namespace.RunnerUnit || report.UnitLoadState != "not-found" || (report.UnitFileState != "" && report.UnitFileState != "not-found") ||
		report.UnitActiveState != "inactive" || report.UnitSubState != "dead" || report.UnitMainPID != 0 || report.UnitControlPID != 0 ||
		report.UnitControlGroup != "" || report.UnitFragmentPath != "" || report.DirectListenerPort != namespace.DirectPort ||
		!report.DirectListenerAbsent || !report.ProcessScanComplete || report.UnexpectedRunnerProcesses != 0 || !validCohortTimestamp(report.ObservedAtUTC) ||
		report.EvidenceSHA256 == "" || report.EvidenceSHA256 != catalogNamespaceAbsenceEvidenceSHA256(report) {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func catalogNamespaceForAbsence(hostID string, lane config.ReleaseLane) (config.LaneNamespace, error) {
	if hostID != "linux-host" {
		return config.LaneNamespace{}, ErrInvalidCatalogRelease
	}
	if lane == config.ReleaseLanePRD {
		plan, err := BuildCatalogPlan(hostID, lane)
		return plan.Namespace, err
	}
	plan, err := BuildSimplePlan(hostID, lane)
	return plan.Namespace, err
}

func catalogNamespaceAbsenceEvidenceSHA256(report CatalogNamespaceAbsenceReport) string {
	report.ObservedAtUTC = ""
	report.EvidenceSHA256 = ""
	data, err := json.Marshal(report)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func requireCatalogPathAbsent(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidCatalogRelease
	}
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrCatalogOperationInvalid
}

type linuxCatalogPRDAbsenceProbe struct{}

func (linuxCatalogPRDAbsenceProbe) UnitState(ctx context.Context, unit string) (CatalogPRDUnitState, error) {
	if !catalogRunnerUnit(unit) {
		return CatalogPRDUnitState{}, ErrInvalidCatalogRelease
	}
	commandContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, "/usr/bin/systemctl", "show", "--no-pager", "--property=LoadState", "--property=UnitFileState",
		"--property=ActiveState", "--property=SubState", "--property=MainPID", "--property=ControlPID", "--property=ControlGroup", "--property=FragmentPath", unit)
	output, err := runCatalogReadOnlyCommand(command, 4096)
	if err != nil {
		return CatalogPRDUnitState{}, ErrCatalogOperationInvalid
	}
	values := make(map[string]string, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" || len(values) >= 8 {
			return CatalogPRDUnitState{}, ErrCatalogOperationInvalid
		}
		if _, duplicate := values[key]; duplicate {
			return CatalogPRDUnitState{}, ErrCatalogOperationInvalid
		}
		values[key] = value
	}
	if len(values) != 8 {
		return CatalogPRDUnitState{}, ErrCatalogOperationInvalid
	}
	mainPID, errMain := strconv.Atoi(values["MainPID"])
	controlPID, errControl := strconv.Atoi(values["ControlPID"])
	if errMain != nil || errControl != nil || mainPID < 0 || controlPID < 0 {
		return CatalogPRDUnitState{}, ErrCatalogOperationInvalid
	}
	return CatalogPRDUnitState{LoadState: values["LoadState"], FileState: values["UnitFileState"], ActiveState: values["ActiveState"],
		SubState: values["SubState"], MainPID: mainPID, ControlPID: controlPID, ControlGroup: values["ControlGroup"], FragmentPath: values["FragmentPath"]}, nil
}

func (linuxCatalogPRDAbsenceProbe) HasTCPListener(ctx context.Context, port int) (bool, error) {
	if port != 8443 && port != 8444 && port != 8445 {
		return false, ErrInvalidCatalogRelease
	}
	commandContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, "/usr/bin/ss", "-H", "-ltn", "sport", "=", ":"+strconv.Itoa(port))
	output, err := runCatalogReadOnlyCommand(command, 64<<10)
	if err != nil {
		return false, ErrCatalogOperationInvalid
	}
	return len(strings.TrimSpace(string(output))) != 0, nil
}

func (linuxCatalogPRDAbsenceProbe) HasUnexpectedRunnerProcess(ctx context.Context, absentUnit string) (bool, error) {
	if !catalogRunnerUnit(absentUnit) {
		return false, ErrInvalidCatalogRelease
	}
	return linuxRunnerProcessScan(ctx, "/proc", absentUnit)
}

func catalogRunnerUnit(unit string) bool {
	switch unit {
	case "runnerd.service", "runnerd-dev.service", "runnerd-qas.service":
		return true
	default:
		return false
	}
}

// NewCatalogPRDAbsenceProbe returns the fixed Linux read-only host observer.
func NewCatalogPRDAbsenceProbe() CatalogPRDAbsenceProbe { return linuxCatalogPRDAbsenceProbe{} }

func linuxRunnerProcessScan(ctx context.Context, procRoot, absentUnit string) (bool, error) {
	if procRoot != "/proc" {
		return false, ErrInvalidCatalogRelease
	}
	if !catalogRunnerUnit(absentUnit) {
		return false, ErrInvalidCatalogRelease
	}
	scanContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return linuxRunnerProcessScanAt(scanContext, procRoot, absentUnit)
}

type catalogOutputBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *catalogOutputBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || len(value) > 0
		return len(value), nil
	}
	if len(value) > remaining {
		b.truncated = true
		_, _ = b.Buffer.Write(value[:remaining])
		return len(value), nil
	}
	_, err := b.Buffer.Write(value)
	return len(value), err
}

func runCatalogReadOnlyCommand(command *exec.Cmd, maxBytes int) ([]byte, error) {
	if command == nil || maxBytes <= 0 {
		return nil, ErrInvalidCatalogRelease
	}
	var stdout, stderr catalogOutputBuffer
	stdout.limit, stderr.limit = maxBytes, 4096
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stdout.truncated || stderr.truncated {
		return nil, ErrCatalogOperationInvalid
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func linuxRunnerProcessScanAt(ctx context.Context, procRoot string, absentUnits ...string) (bool, error) {
	absentUnit := "runnerd.service"
	if len(absentUnits) > 1 {
		return false, ErrInvalidCatalogRelease
	}
	if len(absentUnits) == 1 {
		absentUnit = absentUnits[0]
	}
	if !catalogRunnerUnit(absentUnit) {
		return false, ErrInvalidCatalogRelease
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil || len(entries) > 100_000 {
		return false, ErrCatalogOperationInvalid
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !entry.IsDir() || !numericPID(entry.Name()) {
			continue
		}
		pidDir := filepath.Join(procRoot, entry.Name())
		before, beforeErr := readProcessStartTime(filepath.Join(pidDir, "stat"))
		if errors.Is(beforeErr, os.ErrNotExist) {
			continue
		}
		if beforeErr != nil {
			return false, ErrCatalogOperationInvalid
		}
		comm, commErr := readCatalogBoundedFile(filepath.Join(pidDir, "comm"), 256)
		if errors.Is(commErr, os.ErrNotExist) {
			if _, statErr := os.Stat(filepath.Join(pidDir, "stat")); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return false, ErrCatalogOperationInvalid
		}
		if commErr != nil || len(comm) == 0 || len(comm) > 256 {
			return false, ErrCatalogOperationInvalid
		}
		name := strings.TrimSpace(string(comm))
		cgroup, cgroupErr := readCatalogBoundedFile(filepath.Join(pidDir, "cgroup"), 16<<10)
		if errors.Is(cgroupErr, os.ErrNotExist) {
			if _, statErr := os.Stat(filepath.Join(pidDir, "stat")); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return false, ErrCatalogOperationInvalid
		}
		if cgroupErr != nil || len(cgroup) == 0 || len(cgroup) > 16<<10 {
			return false, ErrCatalogOperationInvalid
		}
		after, afterErr := readProcessStartTime(filepath.Join(pidDir, "stat"))
		if errors.Is(afterErr, os.ErrNotExist) {
			continue
		}
		if afterErr != nil || before != after {
			return false, ErrCatalogOperationInvalid
		}
		relevant, allowed, classifyErr := classifyRunnerProcessForAbsentUnit(name, string(cgroup), absentUnit)
		if classifyErr != nil {
			return false, ErrCatalogOperationInvalid
		}
		if relevant && !allowed {
			return true, nil
		}
	}
	return false, nil
}

func numericPID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func readProcessStartTime(path string) (uint64, error) {
	data, err := readCatalogBoundedFile(path, 32<<10)
	if err != nil {
		return 0, err
	}
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return 0, ErrCatalogOperationInvalid
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) <= 19 {
		return 0, ErrCatalogOperationInvalid
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func readCatalogBoundedFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, ErrInvalidCatalogRelease
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrCatalogOperationInvalid
	}
	return data, nil
}

func isRunnerRuntimeProcess(name string) bool {
	switch name {
	case "runnerd", "runner-log-capture", "runner-session-agent", "runner-local", "runner-locald":
		return true
	default:
		return false
	}
}

func classifyRunnerProcess(name, cgroup string) (relevant, peer bool, err error) {
	return classifyRunnerProcessForAbsentUnit(name, cgroup, "runnerd.service")
}

func classifyRunnerProcessForAbsentUnit(name, cgroup, absentUnit string) (relevant, allowed bool, err error) {
	lines := strings.Split(strings.TrimSpace(cgroup), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return false, false, ErrCatalogOperationInvalid
	}
	for _, line := range lines {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || !numericPID(fields[0]) || !strings.HasPrefix(fields[2], "/") {
			return false, false, ErrCatalogOperationInvalid
		}
		for _, part := range strings.Split(fields[2], "/") {
			if part == "runnerd.service" || part == "runnerd-dev.service" || part == "runnerd-qas.service" {
				relevant = true
				if part == absentUnit {
					return true, false, nil
				}
				allowed = true
			} else if strings.HasPrefix(part, "runnerd") && strings.HasSuffix(part, ".service") {
				return true, false, nil
			}
		}
	}
	if isRunnerRuntimeProcess(name) {
		return true, allowed, nil
	}
	return relevant, allowed, nil
}

var _ CatalogPRDAbsenceProbe = linuxCatalogPRDAbsenceProbe{}
