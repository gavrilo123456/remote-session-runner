package releaseflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

func capacityReportForTest(t *testing.T, host, lane string, available uint64) HostCapacityReport {
	t.Helper()
	plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLane(lane))
	if err != nil {
		t.Fatal(err)
	}
	return HostCapacityReport{HostID: host, Lane: lane, BackupPath: plan.Namespace.Backups,
		ControllerRevision: strings.Repeat("a", 40), ObservedAtUTC: time.Now().UTC().Format(time.RFC3339Nano), AvailableBytes: available}
}

// Exercise the actual native SSH dispatch, replacing only the SSH executable.
// No remote directory is created or measured on the coordinator. Distinct
// returned byte counts must survive regardless of its local filesystem space.
func TestNativePromotionCapacityMeasuredOnEachWorker(t *testing.T) {
	for i, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		t.Run(host, func(t *testing.T) {
			report := capacityReportForTest(t, host, "dev", uint64(1_100_000_000+i*12345))
			b, commandLog := capacityBackendWithSSHResponse(t, report, "")
			var snapshot promotionLaneSnapshot
			if err := b.readPromotionCapacity(context.Background(), host, "dev", &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.AvailableBytes != report.AvailableBytes || snapshot.RequiredBytes != defaultPromotionOperatingBuffer {
				t.Fatalf("capacity was not taken from %s: %+v", host, snapshot)
			}
			args, err := os.ReadFile(commandLog)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"StrictHostKeyChecking=yes", "UserKnownHostsFile=", "host-capacity", host, "--lane", "dev", "--controller-revision", b.ControllerRevision} {
				if !strings.Contains(string(args), part) {
					t.Fatalf("host-local verified capacity command is missing %q: %s", part, args)
				}
			}
		})
	}
}

func capacityBackendWithSSHResponse(t *testing.T, report HostCapacityReport, extraJSON string) (*NativePromotionBackend, string) {
	t.Helper()
	root := t.TempDir()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "ssh-arguments")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(log) + "\nprintf '%s\\n' " + shellQuote(string(data)+extraJSON) + "\n"
	if err := os.WriteFile(filepath.Join(root, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	profile := HostProfile{HostID: report.HostID, Target: report.HostID, IdentityFile: filepath.Join(root, "key"),
		KnownHostsFile: filepath.Join(root, "known_hosts"), Repository: "/home/ubuntu/projects/remote-session-runner", GoBinary: "/verified/toolchain/bin/go"}
	return &NativePromotionBackend{Config: HostConfiguration{FormatVersion: 1, Hosts: map[string]HostProfile{report.HostID: profile}},
		ControllerRevision: report.ControllerRevision}, log
}

func TestPromotionCapacityReportRejectsWrongIdentityAndExpiredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*HostCapacityReport)
	}{
		{"host", func(r *HostCapacityReport) { r.HostID = "factory-sandbox" }},
		{"lane", func(r *HostCapacityReport) { r.Lane = "qas" }},
		{"path", func(r *HostCapacityReport) { r.BackupPath = "/tmp" }},
		{"controller", func(r *HostCapacityReport) { r.ControllerRevision = strings.Repeat("b", 40) }},
		{"stale", func(r *HostCapacityReport) {
			r.ObservedAtUTC = time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339Nano)
		}},
		{"future", func(r *HostCapacityReport) {
			r.ObservedAtUTC = time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano)
		}},
		{"timestamp", func(r *HostCapacityReport) { r.ObservedAtUTC = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := capacityReportForTest(t, "linux-host", "dev", 2_000_000_000)
			tc.edit(&report)
			snapshot := promotionLaneSnapshot{AvailableBytes: 123, RequiredBytes: 456}
			if err := applyPromotionCapacityReport(report, "linux-host", "dev", strings.Repeat("a", 40), 0, time.Now(), &snapshot); err == nil {
				t.Fatal("invalid remote capacity accepted")
			}
			if snapshot.AvailableBytes != 123 || snapshot.RequiredBytes != 456 {
				t.Fatal("invalid observation changed the baseline")
			}
		})
	}
}

func TestNativePromotionCapacityRejectsTrailingResponseData(t *testing.T) {
	report := capacityReportForTest(t, "linux-host", "dev", 2_000_000_000)
	b, _ := capacityBackendWithSSHResponse(t, report, "\n{}")
	if err := b.readPromotionCapacity(context.Background(), "linux-host", "dev", &promotionLaneSnapshot{}); err == nil {
		t.Fatal("multiple capacity response objects accepted")
	}
}

func TestPromotionCapacityDoesNotPromoteInsufficientSpaceToVerified(t *testing.T) {
	report := capacityReportForTest(t, "sandbox-host", "prd", 100_000_000)
	var snapshot promotionLaneSnapshot
	if err := applyPromotionCapacityReport(report, "sandbox-host", "prd", report.ControllerRevision, 0, time.Now(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.AvailableBytes >= snapshot.RequiredBytes || snapshot.RequiredBytes != 567_108_864 {
		t.Fatalf("insufficient host capacity lost: %+v", snapshot)
	}
}
