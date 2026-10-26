package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

func TestRunPlanRendersOnlyFixedDevOrQASNamespace(t *testing.T) {
	for _, lane := range []string{"dev", "qas"} {
		var stdout, stderr bytes.Buffer
		if err := runPlan([]string{"--host", "sandbox-host", "--lane", lane}, &stdout, &stderr); err != nil {
			t.Fatalf("runPlan(%s): %v (%s)", lane, err, stderr.String())
		}
		var rendered struct {
			HostID    string               `json:"host_id"`
			Lane      config.ReleaseLane   `json:"lane"`
			Namespace config.LaneNamespace `json:"namespace"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &rendered); err != nil {
			t.Fatalf("decode plan: %v", err)
		}
		plan, err := laneinstall.BuildSimplePlan("sandbox-host", config.ReleaseLane(lane))
		if err != nil {
			t.Fatal(err)
		}
		if rendered.HostID != plan.HostID || rendered.Lane != plan.Lane || rendered.Namespace != plan.Namespace {
			t.Fatalf("plan output differs from fixed derivation: %+v", rendered)
		}
		if strings.Contains(stdout.String(), "2224") || strings.Contains(stdout.String(), "2225") || strings.Contains(stdout.String(), "cgroup") {
			t.Fatalf("plan includes superseded rollout infrastructure: %s", stdout.String())
		}
	}
}

func TestRunPlanRejectsPRDAndUnknownHost(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "linux-host", "--lane", "prd"},
		{"--host", "unlisted-host", "--lane", "dev"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runPlan(args, &stdout, &stderr); err == nil {
			t.Errorf("runPlan(%v) unexpectedly succeeded", args)
		}
	}
}

func TestPRDMaintenanceCommandsRejectNoncanonicalHostBeforeOperating(t *testing.T) {
	for _, args := range [][]string{
		{"prd-preflight", "--host", "unlisted"},
		{"prd-status", "--host", "unlisted"},
		{"prd-start", "--host", "unlisted"},
		{"prd-stop", "--host", "unlisted"},
		{"prd-backup", "--host", "unlisted", "--bundle-sha256", strings.Repeat("a", 64)},
		{"prd-install", "--host", "unlisted", "--bundle-dir", "/private/tmp/no-bundle", "--bundle-sha256", strings.Repeat("a", 64), "--backup-dir", "/private/tmp/no-backup"},
		{"prd-migrate", "--host", "unlisted"},
		{"prd-rollback", "--host", "unlisted", "--backup-dir", "/private/tmp/no-backup"},
	} {
		var stdout, stderr bytes.Buffer
		if status := run(args, &stdout, &stderr); status == 0 {
			t.Errorf("run(%v) accepted a noncanonical PRD host", args)
		}
	}
}

func TestProbeDispositionInventoryAcceptsOnlyExactSingleProbeStates(t *testing.T) {
	tests := []struct {
		name      string
		inventory laneinstall.CatalogWorkInventory
		want      bool
	}{
		{
			name: "accepted probe before service stop",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessions: 1, ActiveSessionReservations: 1, ResumableJobs: 1,
			},
			want: true,
		},
		{
			name: "creating probe after service stop",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 1, ResumableJobs: 1,
			},
			want: true,
		},
		{
			name: "lost probe awaiting supported cleanup",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 1, ResumableJobs: 0,
			},
			want: true,
		},
		{
			name: "unrelated resumable work",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 1, ResumableJobs: 2,
			},
		},
		{
			name: "command slot remains",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 1, ResumableJobs: 1, ActiveCommandSlots: 1,
			},
		},
		{
			name: "queued work remains",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 1, ResumableJobs: 1, QueuedCommands: 1,
			},
		},
		{
			name: "multiple reservations remain",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 2, ResumableJobs: 1,
			},
		},
		{
			name: "recovery finalization remains",
			inventory: laneinstall.CatalogWorkInventory{
				ActiveSessionReservations: 1, ResumableJobs: 1, PendingRecoveryFinalizations: 1,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := probeDispositionInventory(test.inventory); got != test.want {
				t.Fatalf("probeDispositionInventory(%+v) = %v, want %v", test.inventory, got, test.want)
			}
		})
	}
}

func TestProbeTerminalResultFlagRequiresAnExistingWorkerObservation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runCatalogProbePrepare([]string{
		"--host", "mac-control-plane", "--lane", "prd", "--transport", "mac-local",
		"--operation-id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--controller-id", "catalog-probe-test-controller",
		"--include-terminal-result",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "terminal-result observation requires an existing worker probe scope") {
		t.Fatalf("unscoped terminal result request error=%v, want fail-closed validation", err)
	}
}

func TestCatalogHostPreflightRejectsInvalidScopeAndMissingCapacityBeforeOperating(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "unlisted", "--lane", "dev", "--release-id", "cat-20261019-f0e2843", "--required-free-bytes", "1"},
		{"--host", "linux-host", "--lane", "prd", "--release-id", "cat-20261019-f0e2843", "--required-free-bytes", "1"},
		{"--host", "linux-host", "--lane", "dev", "--release-id", "cat-20261019-f0e2843", "--required-free-bytes", "0"},
	} {
		var stdout, stderr bytes.Buffer
		if status := run(append([]string{"catalog-host-preflight"}, args...), &stdout, &stderr); status == 0 {
			t.Errorf("catalog-host-preflight accepted invalid scope or capacity: %v", args)
		}
	}
}

type catalogHostPreflightOrderService struct {
	events  *[]string
	stopErr error
}

func (s catalogHostPreflightOrderService) Status(context.Context, laneinstall.Plan) (bool, error) {
	return true, nil
}

func (s catalogHostPreflightOrderService) Stop(context.Context, laneinstall.Plan) error {
	*s.events = append(*s.events, "stop")
	return s.stopErr
}

func TestCatalogHostPreflightStopsBeforeAcquiringLaneMaintenanceLease(t *testing.T) {
	plan, err := laneinstall.BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	service := catalogHostPreflightOrderService{events: &events}
	beginErr := errors.New("fixture maintenance lease reached")
	_, err = stopAndBeginCatalogLaneMaintenance(context.Background(), plan, service, func() (*laneinstall.CatalogLaneMaintenance, error) {
		events = append(events, "begin")
		return nil, beginErr
	})
	if !errors.Is(err, beginErr) {
		t.Fatalf("maintenance acquisition error=%v, want fixture error", err)
	}
	if strings.Join(events, ",") != "stop,begin" {
		t.Fatalf("maintenance events=%v, want stop before begin", events)
	}
}

func TestCatalogHostPreflightStopGuardFailureDoesNotAcquireLaneMaintenanceLease(t *testing.T) {
	plan, err := laneinstall.BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	stopErr := errors.New("fixture stop guard refused")
	service := catalogHostPreflightOrderService{events: &events, stopErr: stopErr}
	_, err = stopAndBeginCatalogLaneMaintenance(context.Background(), plan, service, func() (*laneinstall.CatalogLaneMaintenance, error) {
		events = append(events, "begin")
		return nil, nil
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("stop error=%v, want guard failure", err)
	}
	if strings.Join(events, ",") != "stop" {
		t.Fatalf("maintenance events=%v, want no lease attempt after failed guard", events)
	}
}

func TestCatalogHostPreflightReportRetainsEveryRestartInventoryCategory(t *testing.T) {
	want := laneinstall.CatalogWorkInventory{
		ActiveSessions:               1,
		ActiveCommandSlots:           2,
		ActiveSessionReservations:    3,
		QueuedCommands:               4,
		RunningCommands:              5,
		ResumableJobs:                6,
		PendingRecoveryFinalizations: 7,
	}
	encoded, err := json.Marshal(laneinstall.CatalogCohortHostPreflightReport{WorkInventory: want})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		WorkInventory laneinstall.CatalogWorkInventory `json:"work_inventory"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WorkInventory != want {
		t.Fatalf("work inventory round trip = %+v, want %+v", decoded.WorkInventory, want)
	}
	inventoryJSON, err := json.Marshal(laneinstall.CatalogWorkInventoryReport{WorkInventory: want})
	if err != nil {
		t.Fatal(err)
	}
	var inventoryDecoded struct {
		WorkInventory laneinstall.CatalogWorkInventory `json:"work_inventory"`
	}
	if err := json.Unmarshal(inventoryJSON, &inventoryDecoded); err != nil || inventoryDecoded.WorkInventory != want {
		t.Fatalf("read-only inventory did not retain resumable-job counts: got %+v, err=%v", inventoryDecoded.WorkInventory, err)
	}
}

func TestCatalogSelectorSelectPreservesFailuresAndRejectsEmptySuccess(t *testing.T) {
	wantErr := errors.New("fixture selector failure")
	if _, err := runCatalogSelectorSelect(func() (laneinstall.CatalogSelectorOperation, error) {
		return laneinstall.CatalogSelectorOperation{}, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("selector failure was lost: got %v, want %v", err, wantErr)
	}
	if _, err := runCatalogSelectorSelect(func() (laneinstall.CatalogSelectorOperation, error) {
		return laneinstall.CatalogSelectorOperation{}, nil
	}); !errors.Is(err, laneinstall.ErrCatalogOperationInvalid) {
		t.Fatalf("empty successful selector result was accepted: %v", err)
	}

	want := laneinstall.CatalogSelectorOperation{
		OperationID: "fixture-selector-operation", HostID: "linux-host",
		NextTarget:           "/home/ubuntu/projects-runtime/runner-releases/release/linux-host",
		BackupManifestSHA256: strings.Repeat("a", 64),
	}
	got, err := runCatalogSelectorSelect(func() (laneinstall.CatalogSelectorOperation, error) {
		return want, nil
	})
	if err != nil || got != want {
		t.Fatalf("valid selector result = %+v, err=%v; want %+v", got, err, want)
	}
}

func TestCatalogWorkInventoryScopeIncludesCanonicalLanes(t *testing.T) {
	for _, lane := range []string{"dev", "qas", "prd"} {
		if !supportedCatalogWorkInventoryLane(lane) {
			t.Errorf("catalog-work-inventory rejected canonical lane %q", lane)
		}
	}
	for _, lane := range []string{"", "other", "DEV"} {
		if supportedCatalogWorkInventoryLane(lane) {
			t.Errorf("catalog-work-inventory accepted unsupported lane %q", lane)
		}
	}
}

func TestCatalogWorkInventoryRejectsInvalidScopeBeforeReading(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "unlisted", "--lane", "dev"},
		{"--host", "linux-host", "--lane", "other"},
	} {
		var stdout, stderr bytes.Buffer
		if status := run(append([]string{"catalog-work-inventory"}, args...), &stdout, &stderr); status == 0 {
			t.Errorf("catalog-work-inventory accepted invalid scope: %v", args)
		}
	}
}

func TestBundleOutputCannotTargetCheckout(t *testing.T) {
	if !pathWithin("/source/checkout", "/source/checkout/artifact") || pathWithin("/source/checkout", "/private/tmp/artifact") {
		t.Fatal("checkout-boundary predicate does not distinguish repository and external paths")
	}
}

func TestCatalogManifestCommandSealsFourPerHostMetadataFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for hostID, components := range runnerLaneFixtureComponents() {
		bundle, err := laneinstall.NewSimpleBundle(hostID, "0123456789abcdef0123456789abcdef01234567", components)
		if err != nil {
			t.Fatalf("build %s fixture bundle: %v", hostID, err)
		}
		directory := filepath.Join(root, hostID)
		if err := laneinstall.WriteSimpleBundleDirectory(directory, bundle); err != nil {
			t.Fatalf("write %s fixture bundle: %v", hostID, err)
		}
		paths[hostID] = filepath.Join(directory, "manifest.json")
	}
	args := []string{
		"--release-id", "v1.5.0",
		"--factory-manifest", paths["factory-sandbox"],
		"--linux-manifest", paths["linux-host"],
		"--mac-manifest", paths["mac-control-plane"],
		"--sandbox-manifest", paths["sandbox-host"],
	}
	var stdout, stderr bytes.Buffer
	if err := runCatalogManifest(args, &stdout, &stderr); err != nil {
		t.Fatalf("catalog-manifest: %v (%s)", err, stderr.String())
	}
	manifest, err := laneinstall.LoadCatalogReleaseManifest(writeCatalogManifestFixture(t, stdout.Bytes()))
	if err != nil {
		t.Fatalf("read command output manifest: %v", err)
	}
	if manifest.ReleaseID != "v1.5.0" || len(manifest.Hosts) != 4 {
		t.Fatalf("incomplete command output manifest: %+v", manifest)
	}

	args[5] = paths["factory-sandbox"] // linux-host cannot borrow another host's identity.
	stdout.Reset()
	if err := runCatalogManifest(args, &stdout, &stderr); err == nil {
		t.Fatal("catalog-manifest accepted a host label bound to another host's manifest")
	}
}

func TestCatalogStatusRejectsWrongHostAndInvalidLaneBeforeReadingSelectors(t *testing.T) {
	wrongHost := "mac-control-plane"
	currentHost := "linux-host"
	switch runtime.GOOS {
	case "darwin":
		wrongHost, currentHost = "linux-host", "mac-control-plane"
	case "linux":
		if runtime.GOARCH == "arm64" {
			wrongHost, currentHost = "mac-control-plane", "sandbox-host"
		}
	default:
		t.Skip("catalog host mapping is defined for the supported Mac and Ubuntu platforms")
	}

	for _, args := range [][]string{
		{"--host", wrongHost, "--lane", "dev"},
		{"--host", currentHost, "--lane", "invalid"},
		{"--host", currentHost, "--lane", "dev", "unexpected"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runCatalogStatus(args, &stdout, &stderr); err == nil {
			t.Errorf("catalog-status accepted unsafe arguments %v", args)
		}
		if stdout.Len() != 0 {
			t.Errorf("catalog-status emitted a status before rejecting %v: %s", args, stdout.String())
		}
	}
}

func TestCatalogAdoptRejectsWrongHostAndProductionLaneBeforeCopying(t *testing.T) {
	wrongHost, currentHost := "mac-control-plane", "linux-host"
	switch runtime.GOOS {
	case "darwin":
		wrongHost, currentHost = "linux-host", "mac-control-plane"
	case "linux":
		if runtime.GOARCH == "arm64" {
			wrongHost, currentHost = "mac-control-plane", "sandbox-host"
		}
	default:
		t.Skip("catalog host mapping is defined for the supported Mac and Ubuntu platforms")
	}
	for _, args := range [][]string{
		{"--host", wrongHost, "--lane", "dev", "--output-dir", "/private/tmp/catalog-adoption-test"},
		{"--host", currentHost, "--lane", "prd", "--output-dir", "/private/tmp/catalog-adoption-test"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runCatalogAdopt(args, &stdout, &stderr); err == nil {
			t.Errorf("catalog-adopt accepted unsafe arguments %v", args)
		}
		if stdout.Len() != 0 {
			t.Errorf("catalog-adopt emitted a manifest before rejecting %v", args)
		}
	}
}

func runnerLaneFixtureComponents() map[string]map[string][]byte {
	names := map[string][]string{
		"mac-control-plane": {"runner", "runner-local", "runner-locald", "runner-log-capture"},
		"linux-host":        {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
		"sandbox-host":      {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
		"factory-sandbox":   {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
	}
	result := make(map[string]map[string][]byte, len(names))
	for hostID, hostComponents := range names {
		components := make(map[string][]byte, len(hostComponents))
		for _, name := range hostComponents {
			components[name] = []byte("fixture " + hostID + " " + name)
		}
		result[hostID] = components
	}
	return result
}

func writeCatalogManifestFixture(t *testing.T, data []byte) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "release.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
