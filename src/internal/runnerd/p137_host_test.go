package runnerd

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

const p137LinuxHostGate = "RSR_P137_LINUX_HOST_GATE"

func TestP137UbuntuUnattributedOrphanBlocksProfileStartup(t *testing.T) {
	if os.Getenv(p137LinuxHostGate) != "1" {
		t.Skip("set RSR_P137_LINUX_HOST_GATE=1 to run the actual Ubuntu orphan/profile-block gate")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P137 Ubuntu gate must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "ubuntu" || current.Uid != "1001" {
		t.Fatalf("P137 Linux account=%v err=%v, want ubuntu uid 1001", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "oracle-yuta-konopka-ubuntu-micro-02" {
		t.Fatalf("P137 Linux host=%q err=%v, want oracle-yuta-konopka-ubuntu-micro-02", hostname, err)
	}

	fixture := testfixture.New(t)
	if err := os.Chmod(fixture.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	orphanAdapter, err := hostruntime.NewLinuxProcessAdapter(hostruntime.LinuxRuntimeOptions{
		Account: hostruntime.LinuxHostAccount, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := orphanAdapter.Prepare(context.Background(), "p137-linux-unattributed", "p137-orphan-generation")
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
			t.Errorf("clean P137 Ubuntu orphan process group: %v", err)
		}
	})

	db, authority, service, err := p135LinuxService(filepath.Join(fixture.Path(), "authority.db"), workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if report, err := service.ReconcileStartup(context.Background()); !errors.Is(err, hostruntime.ErrRuntimeOwnershipRecord) {
		t.Fatalf("startup reconciliation report=%+v err=%v, want unattributed owner profile block", report, err)
	}
	orphanProcess, err := orphanAdapter.Inspect(orphan.SessionID)
	if err != nil || syscall.Kill(orphanProcess.PID, 0) != nil {
		t.Fatalf("profile audit did not leave the unresolved Ubuntu owner untouched: process=%+v err=%v", orphanProcess, err)
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
