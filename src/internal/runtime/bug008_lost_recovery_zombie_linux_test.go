package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestBUG008LostRecoveryTreatsZombieOnlyGroupAsClean proves the Linux
// definition of a gone process group: a zombie remains visible to kill(2),
// but it cannot run, be reattached, or consume Runner capacity. The recovery
// path may mark its cleanup proof and finalize workspace metadata without
// reaping that PID. The test itself reaps only its own child during cleanup.
func TestBUG008LostRecoveryTreatsZombieOnlyGroupAsClean(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionID := "session-bug008-zombie"
	generation := "generation-bug008-zombie"
	workspace := filepath.Join(workspaceRoot, sessionID)
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}

	pid, releaseChild := b008StartOwnedProcessGroup(t, workspace)
	observed, err := inspectLinuxPID(pid)
	if err != nil {
		t.Fatalf("inspect live fixture process: %v", err)
	}
	if observed.ProcessGroupID != pid {
		t.Fatalf("fixture process group=%d, want its PID %d", observed.ProcessGroupID, pid)
	}
	record := RuntimeOwnershipRecord{
		Version: runtimeOwnershipVersion, HostOS: "linux",
		SessionID: sessionID, Generation: generation,
		Workspace: workspace, OwnedWorkspace: true,
		PID: pid, ProcessGroupID: observed.ProcessGroupID,
		UID: observed.UID, Username: observed.Username, Command: observed.Command,
		ProcessStartIdentity: observed.ProcessStartIdentity,
	}
	if err := writeRuntimeOwnership(workspaceRoot, record); err != nil {
		t.Fatalf("write owned zombie fixture record: %v", err)
	}
	if err := releaseChild(); err != nil {
		t.Fatalf("release fixture process: %v", err)
	}
	b008WaitForZombie(t, pid)

	// A traditional process-group probe still sees the zombie. Runner must use
	// live-member inspection instead of treating this signal result as capacity
	// retention.
	if err := syscall.Kill(-pid, 0); err != nil {
		t.Fatalf("zombie-only process group disappeared from kill(2): %v", err)
	}
	members, err := linuxProcessGroupMembers(pid)
	if err != nil {
		t.Fatalf("inspect zombie-only process group: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("zombie-only process group has live members: %+v", members)
	}
	exists, err := processGroupExists(pid)
	if err != nil || exists {
		t.Fatalf("zombie-only process group exists=%t err=%v, want no runnable members", exists, err)
	}

	recoveryAdapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{
		Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	proved, err := recoveryAdapter.ConfirmLostRecoveryCleanup(context.Background(), sessionID, generation, 100*time.Millisecond)
	if err != nil || !proved.CleanupConfirmed || !proved.CapacityRetained {
		t.Fatalf("zombie lost-recovery proof=%+v err=%v", proved, err)
	}
	owner, err := readRuntimeOwnership(workspaceRoot, sessionID)
	if err != nil || owner.LostRecoveryCleanupConfirmedAt == "" {
		t.Fatalf("zombie proof record=%+v err=%v", owner, err)
	}

	// Confirmation and finalization deliberately did not waitpid/reap the
	// recorded PID. It remains a zombie until this test's owned cleanup runs.
	b008AssertStillZombie(t, pid)
	finalized, err := recoveryAdapter.FinalizeLostRecoveryCleanup(context.Background(), sessionID, generation)
	if err != nil || !finalized.CleanupConfirmed || finalized.CapacityRetained {
		t.Fatalf("zombie lost-recovery finalization=%+v err=%v", finalized, err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalization did not remove owned workspace: %v", err)
	}
	if _, err := readRuntimeOwnership(workspaceRoot, sessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalization did not remove owner record: %v", err)
	}
	b008AssertStillZombie(t, pid)
}

// b008StartOwnedProcessGroup starts one inert Bash child in its own process
// group. Its stdin controls the exit boundary, allowing the test to capture a
// valid owner record before the direct child becomes a zombie. The cleanup is
// restricted to that test-owned PID/group and is the only place that reaps it.
func b008StartOwnedProcessGroup(t *testing.T, directory string) (int, func() error) {
	t.Helper()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	closeBoth := func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	}
	pid, err := syscall.ForkExec("/usr/bin/bash", []string{"/usr/bin/bash", "-c", "IFS= read -r _; exit 0"}, &syscall.ProcAttr{
		Dir: directory,
		Env: os.Environ(),
		Files: []uintptr{
			readEnd.Fd(), os.Stdout.Fd(), os.Stderr.Fd(),
		},
		Sys: &syscall.SysProcAttr{Setpgid: true},
	})
	if err != nil {
		closeBoth()
		t.Fatal(err)
	}
	if err := readEnd.Close(); err != nil {
		_ = writeEnd.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writeEnd.Close()
		// This signal is scoped to the child-owned process group. It covers an
		// assertion failure before the normal stdin release, without touching
		// any ambient host process.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		deadline := time.Now().Add(2 * time.Second)
		for {
			var status syscall.WaitStatus
			waited, waitErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
			if waited == pid || errors.Is(waitErr, syscall.ECHILD) {
				return
			}
			if waitErr != nil {
				t.Errorf("reap owned zombie fixture: %v", waitErr)
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("owned zombie fixture PID %d did not exit", pid)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	released := false
	return pid, func() error {
		if released {
			return nil
		}
		released = true
		if _, err := writeEnd.Write([]byte("\n")); err != nil {
			return err
		}
		return writeEnd.Close()
	}
}

func b008WaitForZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state, processGroupID, _, err := linuxProcessStat(pid)
		if err == nil && state == 'Z' && processGroupID == pid {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture PID %d did not become a zombie in its own group: state=%q group=%d err=%v", pid, state, processGroupID, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func b008AssertStillZombie(t *testing.T, pid int) {
	t.Helper()
	state, processGroupID, _, err := linuxProcessStat(pid)
	if err != nil || state != 'Z' || processGroupID != pid {
		t.Fatalf("recovery reaped or changed zombie fixture: state=%q group=%d err=%v", state, processGroupID, err)
	}
}
