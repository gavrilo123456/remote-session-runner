//go:build p128opshost

package runnerlocald

import (
	"bytes"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/opshealth"
)

func TestP128MacLocalExecutorDoctorHost(t *testing.T) {
	if os.Getenv("RSR_P128_OPS_HOST_GATE") != "1" {
		t.Skip("set RSR_P128_OPS_HOST_GATE=1 to run the actual Mac local-executor doctor gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P128 Mac local-executor doctor gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P128 Mac account=%v err=%v, want %s", current, err, config.MacAccount)
	}
	configPath := strings.TrimSpace(os.Getenv("RUNNER_P128_MAC_CONFIG"))
	if configPath == "" || !filepath.IsAbs(configPath) {
		t.Fatal("RUNNER_P128_MAC_CONFIG must name the owner-only temporary Mac host configuration")
	}

	var output, stderr bytes.Buffer
	if code := runDoctor([]string{"--config", configPath}, &output, &stderr); code != 0 {
		t.Fatalf("runner-locald doctor exit=%d stderr=%q report=%s", code, stderr.String(), output.String())
	}
	var report opshealth.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode runner-locald doctor report: %v; output=%s", err, output.String())
	}
	if report.Component != "mac_local_executor" || report.Readiness != opshealth.StateReady || len(report.Checks) != 2 || report.Checks[0].Component != "sqlite_writes" || report.Checks[0].State != opshealth.StateReady || report.Checks[1].Component != "mac_host_profile" || report.Checks[1].State != opshealth.StateReady {
		t.Fatalf("runner-locald doctor did not report a ready Mac host profile: %+v", report)
	}
	if strings.Contains(output.String(), "dispatcher_ed25519") || strings.Contains(output.String(), "direct-client.key") || strings.Contains(output.String(), "PRIVATE KEY") {
		t.Fatal("runner-locald doctor report exposed a secret path or private-key marker")
	}

	databasePath := filepath.Join(config.MacServiceRoot, "state", "local.db")
	if err := os.Chmod(databasePath, 0o400); err != nil {
		t.Fatalf("make isolated Mac local-executor doctor database read-only: %v", err)
	}
	defer func() {
		if err := os.Chmod(databasePath, 0o600); err != nil {
			t.Errorf("restore isolated Mac local-executor doctor database mode: %v", err)
		}
	}()
	output.Reset()
	stderr.Reset()
	if code := runDoctor([]string{"--config", configPath}, &output, &stderr); code != 1 {
		t.Fatalf("runner-locald doctor with read-only isolated DB exit=%d stderr=%q report=%s", code, stderr.String(), output.String())
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode read-only runner-locald doctor report: %v; output=%s", err, output.String())
	}
	if report.Readiness != opshealth.StateNotReady || len(report.Checks) == 0 || report.Checks[0].Component != "sqlite_writes" || report.Checks[0].State != opshealth.StateNotReady || report.Checks[0].Reason != "database_not_ready" {
		t.Fatalf("runner-locald doctor did not classify the isolated DB failure: %+v", report)
	}
	if strings.Contains(output.String(), "direct-client.key") || strings.Contains(output.String(), "PRIVATE KEY") {
		t.Fatal("failed runner-locald doctor report exposed credential information")
	}
	t.Logf("machine=Mac account=%s uid=%s runner-locald doctor reports host profile readiness and a secret-free database failure", current.Username, current.Uid)
}
