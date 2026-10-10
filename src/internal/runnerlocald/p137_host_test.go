package runnerlocald

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/testfixture"
)

const p137MacHostGate = "RSR_P137_MAC_HOST_GATE"

func TestP137MacUnattributedOrphanBlocksProfileStartup(t *testing.T) {
	if os.Getenv(p137MacHostGate) != "1" {
		t.Skip("set RSR_P137_MAC_HOST_GATE=1 to run the actual Mac orphan/profile-block gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P137 Mac gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" || current.Uid != "502" {
		t.Fatalf("P137 Mac account=%v err=%v, want tomasz.walczuk uid 502", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "AMAK2KJ6X9JJJ" {
		t.Fatalf("P137 Mac host=%q err=%v, want AMAK2KJ6X9JJJ", hostname, err)
	}

	fixture := testfixture.New(t)
	if err := os.Chmod(fixture.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	orphanAdapter, err := hostruntime.NewMacProcessAdapter(hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := orphanAdapter.Prepare(context.Background(), "p137-mac-unattributed", "p137-orphan-generation")
	if err != nil {
		t.Fatal(err)
	}
	if err := orphanAdapter.StartAgent(context.Background(), orphan); err != nil {
		_ = orphanAdapter.Cleanup(orphan.SessionID)
		t.Fatal(err)
	}
	orphanResolved := false
	t.Cleanup(func() {
		if orphanResolved {
			return
		}
		if err := orphanAdapter.Cleanup(orphan.SessionID); err != nil {
			t.Errorf("clean P137 Mac orphan process group: %v", err)
		}
	})

	db, authority, service, err := p135MacService(filepath.Join(fixture.Path(), "authority.db"), workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if report, err := service.ReconcileStartup(context.Background()); !errors.Is(err, hostruntime.ErrRuntimeOwnershipRecord) {
		t.Fatalf("startup reconciliation report=%+v err=%v, want unattributed owner profile block", report, err)
	}
	orphanProcess, err := orphanAdapter.Inspect(orphan.SessionID)
	if err != nil || syscall.Kill(orphanProcess.PID, 0) != nil {
		t.Fatalf("profile audit did not leave the unresolved Mac owner untouched: process=%+v err=%v", orphanProcess, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 0 {
		t.Fatalf("empty authority reservations=%d err=%v, want no attributed slot", reservations, err)
	}
	if err := orphanAdapter.Cleanup(orphan.SessionID); err != nil {
		t.Fatalf("resolve and clean the test-owned orphan: %v", err)
	}
	orphanResolved = true
	if report, err := service.ReconcileStartup(context.Background()); err != nil || report.SessionsInspected != 0 {
		t.Fatalf("startup after orphan resolution report=%+v err=%v, want clean readiness gate", report, err)
	}
}
