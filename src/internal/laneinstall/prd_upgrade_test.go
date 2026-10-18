package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestBuildPRDUpgradePlanIsFixedToExistingUnsuffixedHosts(t *testing.T) {
	mac, err := BuildPRDUpgradePlan("mac-control-plane")
	if err != nil {
		t.Fatal(err)
	}
	if mac.Namespace.Lane != config.ReleaseLanePRD || mac.Namespace.ServiceRoot != config.MacServiceRoot || mac.Namespace.RunnerUnit != "" {
		t.Fatalf("Mac PRD namespace=%+v", mac.Namespace)
	}
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		plan, err := BuildPRDUpgradePlan(host)
		if err != nil || plan.Namespace.Lane != config.ReleaseLanePRD || plan.Namespace.RemoteProfile != host || plan.Namespace.RunnerUnit != "runnerd.service" || plan.Namespace.ServiceRoot != "/home/ubuntu/.local/share/remote-session-runner" {
			t.Fatalf("BuildPRDUpgradePlan(%s)=%+v err=%v", host, plan, err)
		}
	}
	for _, host := range []string{"", "mac-dev", "unknown-host"} {
		if _, err := BuildPRDUpgradePlan(host); err == nil {
			t.Errorf("BuildPRDUpgradePlan(%q) accepted a noncanonical target", host)
		}
	}
	if _, err := BuildSimplePlan("mac-control-plane", config.ReleaseLanePRD); err == nil {
		t.Fatal("the existing Dev/QAS installer now accepts PRD")
	}
}

func TestPRDServiceControllerUsesOnlyFixedUnsuffixedServiceIdentity(t *testing.T) {
	var host string
	for candidate, platform := range simpleHostPlatforms {
		if platform.goos == runtime.GOOS && platform.goarch == runtime.GOARCH {
			host = candidate
			break
		}
	}
	if host == "" {
		t.Skip("no canonical PRD host profile for this test platform")
	}
	plan, err := BuildPRDUpgradePlan(host)
	if err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{
		macActive:             map[string]bool{plan.Namespace.LocalAgentLabel: true, plan.Namespace.LocalDAgentLabel: true},
		macStopPollsRemaining: map[string]int{},
		macBootoutRequested:   map[string]bool{},
		macStatusPolls:        map[string]int{},
		linuxOn:               runtime.GOOS == "linux",
	}
	controller := &PRDServiceController{base: simpleTestServiceController(runner, current.Uid, current.Username, runtime.GOOS, runtime.GOARCH)}
	active, err := controller.Status(context.Background(), plan)
	if err != nil || !active {
		t.Fatalf("status active=%t err=%v", active, err)
	}
	if err := controller.Stop(context.Background(), plan); err != nil {
		t.Fatalf("stop fixed PRD service: %v", err)
	}
	active, err = controller.Status(context.Background(), plan)
	if err != nil || active {
		t.Fatalf("post-stop status active=%t err=%v", active, err)
	}
	if err := controller.Start(context.Background(), plan); err != nil {
		t.Fatalf("start fixed PRD service: %v", err)
	}
	active, err = controller.Status(context.Background(), plan)
	if err != nil || !active {
		t.Fatalf("post-start status active=%t err=%v", active, err)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "runnerd-dev") || strings.Contains(joined, "runnerd-qas") || strings.Contains(joined, "local-dev") || strings.Contains(joined, "locald-qas") {
			t.Fatalf("PRD service controller touched another lane: %v", call)
		}
	}
}

func TestPRDWorkInventoryRequiresEveryRuntimeAndRecoveryGateEmpty(t *testing.T) {
	if (PRDWorkInventory{}).Empty() == false {
		t.Fatal("zero inventory should be empty")
	}
	for name, inventory := range map[string]PRDWorkInventory{
		"active session":       {ActiveSessions: 1},
		"command slot":         {ActiveCommandSlots: 1},
		"session reservation":  {ActiveSessionReservations: 1},
		"queued command":       {QueuedCommands: 1},
		"running command":      {RunningCommands: 1},
		"resumable job":        {ResumableJobs: 1},
		"pending finalization": {PendingRecoveryFinalizations: 1},
	} {
		if inventory.Empty() {
			t.Errorf("%s did not block the maintenance inventory", name)
		}
	}
	retainedTerminal := PRDWorkInventory{ActiveCommandSlots: 2, ActiveSessionReservations: 3, PendingRecoveryFinalizations: 1}
	if !retainedTerminal.InstallableForGuardedRecovery() || retainedTerminal.Empty() {
		t.Fatal("terminal retained reservations should allow installing guarded recovery but must still block migration")
	}
	for name, inventory := range map[string]PRDWorkInventory{
		"active session":  {ActiveSessions: 1},
		"running command": {RunningCommands: 1},
		"queued command":  {QueuedCommands: 1},
		"resumable job":   {ResumableJobs: 1},
	} {
		if inventory.InstallableForGuardedRecovery() {
			t.Errorf("%s did not block candidate installation", name)
		}
	}
}

func TestPRDBinaryRollbackRestoresOnlyKnownBundleComponents(t *testing.T) {
	root := simpleTestTempDir(t)
	binaryRoot := filepath.Join(root, "bin")
	backupRoot := filepath.Join(root, "backup")
	if err := os.Mkdir(binaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(backupRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldRunner := []byte("old runner")
	target := filepath.Join(binaryRoot, "runnerd")
	backupRelative := filepath.Join("bin", "runnerd")
	if err := os.WriteFile(target, oldRunner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupRoot, backupRelative), oldRunner, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(oldRunner)
	manifest := PRDBackupManifest{
		DatabasePath:       filepath.Join(root, "live", "remote.db"),
		DatabaseBackupPath: filepath.Join(backupRoot, "state.db.online"),
		Files:              []PRDBackupFile{{TargetPath: target, BackupPath: backupRelative, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(oldRunner)), Mode: 0o700}},
	}
	bundle, err := NewSimpleBundle("linux-host", "0123456789abcdef0123456789abcdef01234567", simpleFixtureComponents("linux-host", "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installPRDBundleFiles(binaryRoot, bundle, manifest); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(binaryRoot, "diagnostic-helper")
	if err := os.WriteFile(foreign, []byte("unrelated file"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restorePRDBinarySnapshot(binaryRoot, "linux-host", manifest); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != string(oldRunner) {
		t.Fatalf("restored runnerd=%q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(binaryRoot, "runner-ssh-bridge")); !os.IsNotExist(err) {
		t.Fatalf("new candidate component was not removed during rollback: %v", err)
	}
	if contents, err := os.ReadFile(foreign); err != nil || string(contents) != "unrelated file" {
		t.Fatalf("unrelated binary-root file=%q err=%v", contents, err)
	}
}

func TestPRDBackupTargetVerificationAllowsCandidateBinaryButPinsConfig(t *testing.T) {
	root := simpleTestTempDir(t)
	bin := filepath.Join(root, "bin")
	configPath := filepath.Join(root, "config", "linux.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(bin, "runnerd")
	if err := os.WriteFile(binary, []byte("candidate"), 0o700); err != nil {
		t.Fatal(err)
	}
	configBytes := []byte("original config")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	backupBytes := []byte("old binary")
	binaryDigest := sha256.Sum256(backupBytes)
	configDigest := sha256.Sum256(configBytes)
	manifest := PRDBackupManifest{Files: []PRDBackupFile{
		{TargetPath: binary, BackupPath: filepath.Join("bin", "runnerd"), SHA256: hex.EncodeToString(binaryDigest[:]), Bytes: int64(len(backupBytes))},
		{TargetPath: configPath, BackupPath: filepath.Join("config", "linux.yaml"), SHA256: hex.EncodeToString(configDigest[:]), Bytes: int64(len(configBytes)), Mode: 0o600},
	}}
	if err := verifyPRDBackupTargets(manifest, false); err != nil {
		t.Fatalf("verify rollback inputs after candidate binary install: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("changed config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPRDBackupTargets(manifest, false); err == nil {
		t.Fatal("rollback accepted changed PRD configuration")
	}
}

func TestPRDWorkInventoryReadSupportsCurrentFixtureAuthority(t *testing.T) {
	root := simpleTestTempDir(t)
	path := filepath.Join(root, "state", "local.db")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	inventory, err := readPRDWorkInventory(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Empty() {
		t.Fatalf("fresh isolated authority inventory=%+v", inventory)
	}
}
