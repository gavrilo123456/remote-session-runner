package runnerlocald

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	p141fixture "remote-session-runner/src/internal/testfixture/p141"
)

const p141MacHostGate = "RSR_P141_MAC_HOST_GATE"

func TestP141F05MacTwentySessionsFourSlotsAndBoundedSlowSubscriber(t *testing.T) {
	if os.Getenv(p141MacHostGate) != "1" {
		t.Skip("set RSR_P141_MAC_HOST_GATE=1 to run the actual Mac quota and bounded-subscriber soak")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P141 Mac gate must run on macOS, got %s", runtime.GOOS)
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
			t.Errorf("close isolated P141 Mac database: %v", err)
		}
	})
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS workstation", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, runtimeAdapter, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	p141fixture.RunReferenceHostSoak(t, p141fixture.Options{
		Service: service, Authority: authority, Runtime: runtimeAdapter, Environment: environment,
		Target: target, Controller: controller, WorkspaceRoot: workspaceRoot,
		ExpectedOS: "darwin", ExpectedUser: "tomasz.walczuk", ExpectedUID: "501", ExpectedHost: "AMAK2KJ6X9JJJ",
	})
}
