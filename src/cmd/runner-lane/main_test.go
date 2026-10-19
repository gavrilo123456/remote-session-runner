package main

import (
	"bytes"
	"encoding/json"
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

func TestCatalogStatusRejectsWrongHostAndProductionLaneBeforeReadingSelectors(t *testing.T) {
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
		{"--host", currentHost, "--lane", "prd"},
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
