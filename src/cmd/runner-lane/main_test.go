package main

import (
	"bytes"
	"encoding/json"
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
