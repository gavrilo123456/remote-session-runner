package runnerd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	p142fixture "remote-session-runner/src/internal/testfixture/p142"
)

const p142LinuxHostGate = "RSR_P142_LINUX_HOST_GATE"

func TestP142F05UbuntuPersistedOutputVisibilityAndLongLoad(t *testing.T) {
	if os.Getenv(p142LinuxHostGate) != "1" {
		t.Skip("set RSR_P142_LINUX_HOST_GATE=1 to run the actual Ubuntu P142 visibility and stability gate")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P142 Ubuntu gate must run on Linux, got %s", runtime.GOOS)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), filepath.Join(root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close isolated P142 Ubuntu database: %v", err)
		}
	})
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "linux-dev", HostClass: "Ubuntu Linux host", EffectiveAccount: "ubuntu",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, runtimeAdapter, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account: "ubuntu", WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("find selected Ubuntu python3: %v", err)
	}
	pythonPath, err = filepath.Abs(pythonPath)
	if err != nil {
		t.Fatalf("resolve selected Ubuntu python3 path: %v", err)
	}
	p142fixture.RunReferenceHostVisibilitySoak(t, p142fixture.Options{
		Service: service, Authority: authority, Runtime: runtimeAdapter, Environment: environment,
		Target: target, Controller: controller, WorkspaceRoot: workspaceRoot, PythonPath: pythonPath,
		ExpectedOS: "linux", ExpectedUser: "ubuntu", ExpectedUID: "1001", ExpectedHost: "oracle-yuta-konopka-ubuntu-micro-02",
	})
}
