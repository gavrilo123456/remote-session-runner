package laneinstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

type catalogAbsenceProbeFixture struct {
	unitStates    []CatalogPRDUnitState
	listeners     []bool
	processes     []bool
	onProcess     func(int)
	unitCalls     int
	listenerCalls int
	processCalls  int
}

func nextAbsenceObservation[T any](values []T, calls *int) (T, error) {
	var zero T
	if len(values) == 0 {
		return zero, fmt.Errorf("fixture has no further observations")
	}
	if *calls >= len(values) {
		return values[len(values)-1], nil
	}
	value := values[*calls]
	*calls++
	return value, nil
}

func (p *catalogAbsenceProbeFixture) UnitState(context.Context, string) (CatalogPRDUnitState, error) {
	return nextAbsenceObservation(p.unitStates, &p.unitCalls)
}

func (p *catalogAbsenceProbeFixture) HasTCPListener(context.Context, int) (bool, error) {
	return nextAbsenceObservation(p.listeners, &p.listenerCalls)
}

func (p *catalogAbsenceProbeFixture) HasUnexpectedRunnerProcess(context.Context, string) (bool, error) {
	value, err := nextAbsenceObservation(p.processes, &p.processCalls)
	if err == nil && p.onProcess != nil {
		p.onProcess(p.processCalls)
	}
	return value, err
}

func TestReadCatalogPRDNamespaceAbsenceRequiresCompleteStableState(t *testing.T) {
	plan, root := catalogAbsenceFixturePlan(t)
	notFound := CatalogPRDUnitState{LoadState: "not-found", FileState: "not-found", ActiveState: "inactive", SubState: "dead"}
	probe := &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{false, false}, processes: []bool{false, false}}
	report, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev,
		plan.Namespace, uint32(os.Getuid()), probe)
	if err != nil {
		t.Fatalf("complete absence rejected: %v", err)
	}
	if report.ServiceRoot != root || report.State != "absent" || report.DirectListenerPort != 8443 || !report.ProcessScanComplete ||
		!report.DirectListenerAbsent || report.UnitMainPID != 0 || report.UnitControlPID != 0 || report.EvidenceSHA256 == "" {
		t.Fatalf("incomplete absence report: %+v", report)
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(report, "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev, plan.Namespace); err != nil {
		t.Fatalf("fresh report did not validate: %v", err)
	}
	report.ConfigurationState = "present"
	if err := validateCatalogNamespaceAbsenceReportForNamespace(report, "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev, plan.Namespace); err == nil {
		t.Fatal("tampered partial-namespace report validated")
	}
}

func TestReadCatalogQASNamespaceAbsenceRequiresCompleteStableState(t *testing.T) {
	namespace, root := catalogAbsenceFixtureSimpleNamespace(t, config.ReleaseLaneQAS)
	notFound := CatalogPRDUnitState{LoadState: "not-found", FileState: "not-found", ActiveState: "inactive", SubState: "dead"}
	probe := &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{false, false}, processes: []bool{false, false}}
	report, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev,
		namespace, uint32(os.Getuid()), probe)
	if err != nil {
		t.Fatalf("complete QAS absence rejected: %v", err)
	}
	if report.ServiceRoot != root || report.Lane != config.ReleaseLaneQAS || report.SelectedLane != config.ReleaseLaneDev ||
		report.ServiceUnit != "runnerd-qas.service" || report.DirectListenerPort != 8445 || !report.ProcessScanComplete {
		t.Fatalf("incorrect QAS absence report: %+v", report)
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(report, "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev, namespace); err != nil {
		t.Fatalf("fresh QAS absence report did not validate: %v", err)
	}
}

func TestReadCatalogQASNamespaceAbsenceRejectsRootCreatedDuringObservation(t *testing.T) {
	namespace, _ := catalogAbsenceFixtureSimpleNamespace(t, config.ReleaseLaneQAS)
	notFound := CatalogPRDUnitState{LoadState: "not-found", FileState: "not-found", ActiveState: "inactive", SubState: "dead"}
	probe := &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{false, false}, processes: []bool{false, false}}
	probe.onProcess = func(call int) {
		if call == 2 {
			_ = os.Mkdir(namespace.ServiceRoot, 0o700)
		}
	}
	if _, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev,
		namespace, uint32(os.Getuid()), probe); err == nil {
		t.Fatal("namespace created during observation was reported absent")
	}
}

func TestReadCatalogPRDNamespaceAbsenceRecordsOnlyVerifiedToolchain(t *testing.T) {
	plan, root := catalogAbsenceFixturePlan(t)
	toolchainRoot := filepath.Join(root, "toolchains")
	goRoot := filepath.Join(toolchainRoot, "go1.27.1")
	binRoot := filepath.Join(goRoot, "bin")
	for _, path := range []string{root, toolchainRoot, goRoot, binRoot} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(goRoot, "VERSION"), []byte("go1.27.1\ntime 2026-08-28T16:20:06Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binRoot, "go"), []byte("verified-toolchain-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectCatalogRuntimeRoot(plan, uint32(os.Getuid())); err != nil {
		t.Fatalf("inspect fixture toolchain root: %v", err)
	}
	notFound := CatalogPRDUnitState{LoadState: "not-found", FileState: "", ActiveState: "inactive", SubState: "dead"}
	probe := &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{false, false}, processes: []bool{false, false}}
	report, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev,
		plan.Namespace, uint32(os.Getuid()), probe)
	if err != nil {
		t.Fatalf("toolchain-only namespace rejected: %v", err)
	}
	if report.ServiceRootState != "toolchain-only" || report.ToolchainRoot != toolchainRoot || report.ToolchainState != "owner-toolchain-only" ||
		report.ToolchainVersion != "go1.27.1" || report.ToolchainBinarySHA256 == "" {
		t.Fatalf("toolchain exception was not recorded exactly: %+v", report)
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(report, "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev, plan.Namespace); err != nil {
		t.Fatalf("toolchain-only report failed validation: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "unexpected"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev,
		plan.Namespace, uint32(os.Getuid()), probe); err == nil {
		t.Fatal("additional runtime-root content was accepted")
	}
}

func TestReadCatalogPRDNamespaceAbsenceRejectsPartialOrChangingState(t *testing.T) {
	plan, _ := catalogAbsenceFixturePlan(t)
	notFound := CatalogPRDUnitState{LoadState: "not-found", FileState: "not-found", ActiveState: "inactive", SubState: "dead"}
	cases := []struct {
		name  string
		probe *catalogAbsenceProbeFixture
	}{
		{name: "unit present", probe: &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{{LoadState: "loaded", FileState: "enabled", ActiveState: "inactive", SubState: "dead"}, notFound}, listeners: []bool{false, false}, processes: []bool{false, false}}},
		{name: "listener present", probe: &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{true, false}, processes: []bool{false, false}}},
		{name: "unexpected process", probe: &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{false, false}, processes: []bool{true, false}}},
		{name: "unit changes", probe: &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, {LoadState: "loaded", FileState: "enabled", ActiveState: "inactive", SubState: "dead"}}, listeners: []bool{false, false}, processes: []bool{false, false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev,
				plan.Namespace, uint32(os.Getuid()), tc.probe); err == nil {
				t.Fatal("partial or changing PRD namespace was accepted")
			}
		})
	}
}

func TestReadCatalogPRDNamespaceAbsenceRejectsExistingAndSymlinkedRoots(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			plan, root := catalogAbsenceFixturePlan(t)
			if symlink {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLanePRD, config.ReleaseLaneDev,
				plan.Namespace, uint32(os.Getuid()), &catalogAbsenceProbeFixture{}); err == nil {
				t.Fatal("present or symlinked runtime root was accepted")
			}
		})
	}
}

func TestReadCatalogPeerNamespaceAbsenceSupportsOnlyFirstInstallPeers(t *testing.T) {
	if !CatalogNamespaceAbsenceScopeAllowed("linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev) ||
		!CatalogNamespaceAbsenceScopeAllowed("linux-host", config.ReleaseLanePRD, config.ReleaseLaneQAS) ||
		CatalogNamespaceAbsenceScopeAllowed("linux-host", config.ReleaseLaneQAS, config.ReleaseLaneQAS) ||
		CatalogNamespaceAbsenceScopeAllowed("sandbox-host", config.ReleaseLaneQAS, config.ReleaseLaneDev) {
		t.Fatal("first-install absence scope broadened beyond the replacement Linux peer lanes")
	}
	namespace, root := catalogAbsenceFixtureSimpleNamespace(t, config.ReleaseLaneQAS)
	notFound := CatalogPRDUnitState{LoadState: "not-found", FileState: "", ActiveState: "inactive", SubState: "dead"}
	probe := &catalogAbsenceProbeFixture{unitStates: []CatalogPRDUnitState{notFound, notFound}, listeners: []bool{false, false}, processes: []bool{false, false}}
	report, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev,
		namespace, uint32(os.Getuid()), probe)
	if err != nil {
		t.Fatalf("complete first-install QAS peer absence rejected: %v", err)
	}
	if report.Lane != config.ReleaseLaneQAS || report.SelectedLane != config.ReleaseLaneDev || report.ServiceRoot != root ||
		report.ServiceUnit != "runnerd-qas.service" || report.DirectListenerPort != 8445 {
		t.Fatalf("QAS peer absence was not bound to its exact namespace: %+v", report)
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(report, "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev, namespace); err != nil {
		t.Fatalf("first-install QAS peer report failed validation: %v", err)
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(report, "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneQAS, namespace); err == nil {
		t.Fatal("QAS absence evidence was reusable as selected-QAS evidence")
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := observeCatalogNamespaceAbsence(context.Background(), "linux-host", config.ReleaseLaneQAS, config.ReleaseLaneDev,
		namespace, uint32(os.Getuid()), probe); err == nil {
		t.Fatal("an existing QAS namespace root was accepted as absent")
	}
}

func TestLinuxRunnerProcessScanAtRejectsUnsuffixedAndUnreadableProcesses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cgroup     string
		omitCgroup bool
		wantFound  bool
		wantError  bool
	}{
		{name: "dev peer", cgroup: "0::/system.slice/runnerd-dev.service", wantFound: false},
		{name: "qas peer", cgroup: "0::/system.slice/runnerd-qas.service", wantFound: false},
		{name: "unsuffixed PRD", cgroup: "0::/system.slice/runnerd.service", wantFound: true},
		{name: "nested unrelated runner", cgroup: "0::/user.slice/runnerd-work.service/child", wantFound: true},
		{name: "live process with denied metadata", omitCgroup: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := t.TempDir()
			pid := filepath.Join(proc, "1234")
			if err := os.Mkdir(pid, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pid, "stat"), processStat(1234, 77), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pid, "comm"), []byte("runnerd\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !tc.omitCgroup {
				if err := os.WriteFile(filepath.Join(pid, "cgroup"), []byte(tc.cgroup+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			found, err := linuxRunnerProcessScanAt(context.Background(), proc, "runnerd.service")
			if tc.wantError && err == nil || !tc.wantError && err != nil {
				t.Fatalf("process scan error = %v, wantError %t", err, tc.wantError)
			}
			if !tc.wantError && found != tc.wantFound {
				t.Fatalf("unexpected runner process = %t, want %t", found, tc.wantFound)
			}
		})
	}
}

func TestLinuxRunnerProcessScanAtRejectsAbsentUnitButAllowsOtherLane(t *testing.T) {
	for _, tc := range []struct {
		name, cgroup, absentUnit string
		wantFound                bool
	}{
		{name: "QAS process is unexpected while proving QAS absent", cgroup: "0::/system.slice/runnerd-qas.service", absentUnit: "runnerd-qas.service", wantFound: true},
		{name: "Dev process is allowed while proving QAS absent", cgroup: "0::/system.slice/runnerd-dev.service", absentUnit: "runnerd-qas.service", wantFound: false},
		{name: "PRD process is allowed while proving QAS absent", cgroup: "0::/system.slice/runnerd.service", absentUnit: "runnerd-qas.service", wantFound: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := t.TempDir()
			pid := filepath.Join(proc, "1234")
			if err := os.Mkdir(pid, 0o700); err != nil {
				t.Fatal(err)
			}
			for path, contents := range map[string][]byte{
				"stat": processStat(1234, 91), "comm": []byte("runnerd\n"), "cgroup": []byte(tc.cgroup + "\n"),
			} {
				if err := os.WriteFile(filepath.Join(pid, path), contents, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			found, err := linuxRunnerProcessScanAt(context.Background(), proc, tc.absentUnit)
			if err != nil || found != tc.wantFound {
				t.Fatalf("scan found=%t err=%v, want found=%t", found, err, tc.wantFound)
			}
		})
	}
}

func TestReadProcessStartTimeBindsPIDIncarnation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, processStat(1234, 77), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := readProcessStartTime(path)
	if err != nil || first != 77 {
		t.Fatalf("first process identity = %d, %v", first, err)
	}
	if err := os.WriteFile(path, processStat(1234, 78), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := readProcessStartTime(path)
	if err != nil || second == first {
		t.Fatalf("PID reuse identity was not distinguished: %d, %v", second, err)
	}
	if err := os.WriteFile(path, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProcessStartTime(path); err == nil {
		t.Fatal("malformed process identity was accepted")
	}
}

func catalogAbsenceFixturePlan(t *testing.T) (PRDUpgradePlan, string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "runner-prd")
	namespace := config.LaneNamespace{Host: config.HostKindLinux, Lane: config.ReleaseLanePRD, Account: "test-operator",
		ServiceRoot: root, ConfigFile: filepath.Join(root, "config", "linux.yaml"), Database: filepath.Join(root, "state", "local.db"), Binaries: filepath.Join(root, "bin"),
		Staging: filepath.Join(root, "staging"), Backups: filepath.Join(root, "backups"), LifecycleLock: filepath.Join(root, "run", "lifecycle.lock"),
		RunnerUnit: "runnerd.service", DirectPort: 8443}
	namespace.DatabaseWAL, namespace.DatabaseSHM = namespace.Database+"-wal", namespace.Database+"-shm"
	return PRDUpgradePlan{HostID: "linux-host", Namespace: namespace}, root
}

func catalogAbsenceFixtureSimpleNamespace(t *testing.T, lane config.ReleaseLane) (config.LaneNamespace, string) {
	t.Helper()
	plan, err := BuildSimplePlan("linux-host", lane)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "runnerd-"+string(lane))
	namespace := plan.Namespace
	namespace.ServiceRoot = root
	namespace.ConfigFile = filepath.Join(root, "config", "linux.yaml")
	namespace.Database = filepath.Join(root, "state", "remote.db")
	namespace.DatabaseWAL, namespace.DatabaseSHM = namespace.Database+"-wal", namespace.Database+"-shm"
	namespace.Binaries = filepath.Join(root, "bin")
	namespace.Staging = filepath.Join(root, "staging")
	namespace.Backups = filepath.Join(root, "backups")
	namespace.LifecycleLock = filepath.Join(root, "run", "runnerd-lifecycle.lock")
	return namespace, root
}

func processStat(pid int, startTime uint64) []byte {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[19] = fmt.Sprintf("%d", startTime)
	return []byte(fmt.Sprintf("%d (runnerd) %s\n", pid, strings.Join(fields, " ")))
}
