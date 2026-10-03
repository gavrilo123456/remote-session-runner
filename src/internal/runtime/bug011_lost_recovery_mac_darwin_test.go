//go:build darwin && cgo

package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBUG011MacLostRecoveryReapsOwnedZombieThenFinalizes(t *testing.T) {
	fixture := newBUG011MacFixture(t, "zombie")
	if _, err := fixture.shell.RunScript(context.Background(), "command-bug011-mac-zombie", []byte("set -e\nfalse\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("sourced errexit error = %v, want ErrPersistentShellExited", err)
	}
	bug011WaitForMacZombie(t, fixture.record.PID, fixture.record.ProcessGroupID)

	confirmed, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond)
	if err != nil || !confirmed.CleanupConfirmed || !confirmed.CapacityRetained || confirmed.Reattached {
		t.Fatalf("lost cleanup proof = %+v err=%v", confirmed, err)
	}
	if err := syscall.Kill(fixture.record.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("owned zombie was not reaped by its adapter: kill(%d, 0) = %v, want ESRCH", fixture.record.PID, err)
	}
	owner, err := readRuntimeOwnership(fixture.workspaceRoot, fixture.prepared.SessionID)
	if err != nil || owner.LostRecoveryCleanupConfirmedAt == "" {
		t.Fatalf("recovery proof owner=%+v err=%v", owner, err)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); err != nil {
		t.Fatalf("stage A removed owned workspace: %v", err)
	}

	finalized, err := fixture.adapter.FinalizeLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation)
	if err != nil || !finalized.CleanupConfirmed || finalized.CapacityRetained {
		t.Fatalf("lost cleanup finalization = %+v err=%v", finalized, err)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage C did not remove owned workspace: %v", err)
	}
	if _, err := readRuntimeOwnership(fixture.workspaceRoot, fixture.prepared.SessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage C did not remove owner marker: %v", err)
	}
	retry, err := fixture.adapter.FinalizeLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation)
	if err != nil || !retry.CleanupConfirmed || retry.CapacityRetained {
		t.Fatalf("idempotent stage C retry = %+v err=%v", retry, err)
	}
}

func TestBUG011MacLostRecoveryStopsKnownLiveRootAndRetainsMetadata(t *testing.T) {
	fixture := newBUG011MacFixture(t, "live-root")
	bug011AssertMacRunnable(t, fixture.record.PID, fixture.record.ProcessGroupID)

	confirmed, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond)
	if err != nil || !confirmed.CleanupConfirmed || !confirmed.CapacityRetained {
		t.Fatalf("live-root cleanup proof = %+v err=%v", confirmed, err)
	}
	if err := syscall.Kill(fixture.record.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("known live root remained after bounded cleanup: %v", err)
	}
	owner, err := readRuntimeOwnership(fixture.workspaceRoot, fixture.prepared.SessionID)
	if err != nil || owner.LostRecoveryCleanupConfirmedAt == "" {
		t.Fatalf("live-root proof owner=%+v err=%v", owner, err)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); err != nil {
		t.Fatalf("stage A removed live-root workspace: %v", err)
	}

	if _, err := fixture.adapter.FinalizeLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation); err != nil {
		t.Fatalf("live-root finalization: %v", err)
	}
}

func TestBUG011MacLostRecoveryRetainsLiveUnknownDescendant(t *testing.T) {
	fixture := newBUG011MacFixture(t, "unknown-descendant")
	childPath := filepath.Join(fixture.prepared.Workspace, "bug011-live-child.pid")
	script := "sleep 30 </dev/null >/dev/null 2>&1 &\nprintf '%s\\n' \"$!\" >" + shellQuote(childPath) + "\n"
	if _, err := fixture.shell.RunScript(context.Background(), "command-bug011-live-descendant", []byte(script)); err != nil {
		t.Fatalf("start test-owned Bash descendant: %v", err)
	}
	encodedChildPID, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(encodedChildPID)))
	if err != nil || childPID <= 0 {
		t.Fatalf("test-owned child PID = %q err=%v", strings.TrimSpace(string(encodedChildPID)), err)
	}
	members := bug011WaitForMacGroupMember(t, fixture.record.ProcessGroupID, childPID)
	for _, member := range members {
		if member.PID == childPID && member.ParentPID != fixture.record.PID {
			t.Fatalf("test-owned Bash descendant parent=%d, want Bash PID %d", member.ParentPID, fixture.record.PID)
		}
	}

	confirmed, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond)
	if err == nil || confirmed.CleanupConfirmed || !confirmed.CapacityRetained {
		t.Fatalf("unknown descendant proof = %+v err=%v, want retained unconfirmed error", confirmed, err)
	}
	bug011AssertMacRunnable(t, fixture.record.PID, fixture.record.ProcessGroupID)
	bug011AssertMacRunnable(t, childPID, fixture.record.ProcessGroupID)
	bug011AssertMacProofAbsent(t, fixture.workspaceRoot, fixture.prepared.SessionID)
}

func TestBUG011MacLostRecoveryRejectsIdentityMismatchBeforeSignal(t *testing.T) {
	tests := []struct {
		name               string
		expectedGeneration string
		malformed          bool
		mutate             func(t *testing.T, fixture *bug011MacFixture, record *RuntimeOwnershipRecord)
	}{
		{
			name:               "requested_generation",
			expectedGeneration: "different-generation",
		},
		{
			name:      "malformed_json",
			malformed: true,
		},
		{
			name: "username",
			mutate: func(_ *testing.T, _ *bug011MacFixture, record *RuntimeOwnershipRecord) {
				record.Username = "different-selected-account"
			},
		},
		{
			name: "uid",
			mutate: func(_ *testing.T, _ *bug011MacFixture, record *RuntimeOwnershipRecord) {
				record.UID++
			},
		},
		{
			name: "process_start_identity",
			mutate: func(_ *testing.T, _ *bug011MacFixture, record *RuntimeOwnershipRecord) {
				record.ProcessStartIdentity = "different-start-identity"
			},
		},
		{
			name: "different_test_owned_process_group",
			mutate: func(t *testing.T, _ *bug011MacFixture, record *RuntimeOwnershipRecord) {
				other := newBUG011MacFixture(t, "identity-other")
				record.PID = other.record.PID
				record.ProcessGroupID = other.record.ProcessGroupID
				record.Command = other.record.Command
				record.ProcessStartIdentity = other.record.ProcessStartIdentity
				// Keep this record's session/generation/workspace so only the
				// current adapter identity check decides before any signal.
			},
		},
		{
			name: "pid_pgid_relationship",
			mutate: func(_ *testing.T, _ *bug011MacFixture, record *RuntimeOwnershipRecord) {
				record.ProcessGroupID++
			},
		},
		{
			name: "record_session_id",
			mutate: func(_ *testing.T, _ *bug011MacFixture, record *RuntimeOwnershipRecord) {
				record.SessionID = "different-session-id"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBUG011MacFixture(t, "identity-"+test.name)
			record, err := readRuntimeOwnership(fixture.workspaceRoot, fixture.prepared.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if test.malformed {
				bug011WriteMalformedMacOwnerRecord(t, fixture.workspaceRoot, fixture.prepared.SessionID)
			} else if test.mutate != nil {
				test.mutate(t, fixture, &record)
				bug011ReplaceMacOwnerRecord(t, fixture.workspaceRoot, fixture.prepared.SessionID, record)
			}
			expectedGeneration := test.expectedGeneration
			if expectedGeneration == "" {
				expectedGeneration = fixture.prepared.Generation
			}
			confirmed, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, expectedGeneration, 100*time.Millisecond)
			if err == nil || confirmed.CleanupConfirmed || !confirmed.CapacityRetained {
				t.Fatalf("identity mismatch proof = %+v err=%v, want retained unconfirmed error", confirmed, err)
			}
			bug011AssertMacRunnable(t, fixture.record.PID, fixture.record.ProcessGroupID)
			bug011AssertMacProofAbsent(t, fixture.workspaceRoot, fixture.prepared.SessionID)
		})
	}
}

func TestBUG011MacProofRetryNeverSignalsReplacementPID(t *testing.T) {
	fixture := newBUG011MacFixture(t, "proof-retry-no-pid")
	if _, err := fixture.shell.RunScript(context.Background(), "command-bug011-proof-retry-no-pid", []byte("set -e\nfalse\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("sourced errexit error = %v, want ErrPersistentShellExited", err)
	}
	bug011WaitForMacZombie(t, fixture.record.PID, fixture.record.ProcessGroupID)
	if _, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond); err != nil {
		t.Fatalf("stage A proof: %v", err)
	}

	sentinel := exec.Command("/bin/sleep", "30")
	sentinel.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sentinel.Start(); err != nil {
		t.Fatalf("start test-owned sentinel: %v", err)
	}
	t.Cleanup(func() {
		if sentinel.Process != nil {
			_ = sentinel.Process.Kill()
		}
		_ = sentinel.Wait()
	})
	sentinelPID := sentinel.Process.Pid
	sentinelGroup, err := syscall.Getpgid(sentinelPID)
	if err != nil {
		t.Fatal(err)
	}
	sentinelStart, err := inspectMacProcessStartIdentity(sentinelPID)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := readRuntimeOwnership(fixture.workspaceRoot, fixture.prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	owner.PID = sentinelPID
	owner.ProcessGroupID = sentinelGroup
	owner.Command = "/bin/sleep"
	owner.ProcessStartIdentity = sentinelStart
	bug011ReplaceMacOwnerRecord(t, fixture.workspaceRoot, fixture.prepared.SessionID, owner)

	retry, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond)
	if err != nil || !retry.CleanupConfirmed || !retry.CapacityRetained {
		t.Fatalf("proved-marker stage A retry = %+v err=%v", retry, err)
	}
	if err := syscall.Kill(sentinelPID, 0); err != nil {
		t.Fatalf("proved-marker retry touched replacement PID %d: %v", sentinelPID, err)
	}
}

func TestBUG011MacLostRecoveryRetainsDetachedZombie(t *testing.T) {
	fixture := newBUG011MacFixture(t, "detached-zombie")
	if _, err := fixture.shell.RunScript(context.Background(), "command-bug011-detached-zombie", []byte("set -e\nfalse\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("sourced errexit error = %v, want ErrPersistentShellExited", err)
	}
	bug011WaitForMacZombie(t, fixture.record.PID, fixture.record.ProcessGroupID)
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewMacProcessAdapter(MacRuntimeOptions{Account: current.Username, WorkspaceRoot: fixture.workspaceRoot, ShellPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := fresh.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 100*time.Millisecond)
	if err == nil || confirmed.CleanupConfirmed || !confirmed.CapacityRetained {
		t.Fatalf("fresh-adapter zombie proof = %+v err=%v, want retained unconfirmed error", confirmed, err)
	}
	bug011WaitForMacZombie(t, fixture.record.PID, fixture.record.ProcessGroupID)
	bug011AssertMacProofAbsent(t, fixture.workspaceRoot, fixture.prepared.SessionID)
}

func TestBUG011MacLostRecoveryFinalizationRetriesAfterPostProofFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permission fixture cannot force removal failure as root")
	}
	fixture := newBUG011MacFixture(t, "finalization-retry")
	if _, err := fixture.shell.RunScript(context.Background(), "command-bug011-finalization-retry", []byte("set -e\nfalse\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("sourced errexit error = %v, want ErrPersistentShellExited", err)
	}
	bug011WaitForMacZombie(t, fixture.record.PID, fixture.record.ProcessGroupID)
	if _, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond); err != nil {
		t.Fatalf("stage A proof: %v", err)
	}
	if err := os.Chmod(fixture.workspaceRoot, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fixture.workspaceRoot, 0o700) })
	failed, err := fixture.adapter.FinalizeLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation)
	if err == nil || failed.CleanupConfirmed || !failed.CapacityRetained {
		t.Fatalf("forced finalization failure = %+v err=%v, want retained error", failed, err)
	}
	if err := os.Chmod(fixture.workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	retry, err := fixture.adapter.FinalizeLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation)
	if err != nil || !retry.CleanupConfirmed || retry.CapacityRetained {
		t.Fatalf("finalization retry = %+v err=%v", retry, err)
	}
}

func TestBUG011MacFinalizationNeverSignalsReplacementPID(t *testing.T) {
	fixture := newBUG011MacFixture(t, "finalization-no-pid")
	if _, err := fixture.shell.RunScript(context.Background(), "command-bug011-finalization-no-pid", []byte("set -e\nfalse\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("sourced errexit error = %v, want ErrPersistentShellExited", err)
	}
	bug011WaitForMacZombie(t, fixture.record.PID, fixture.record.ProcessGroupID)
	if _, err := fixture.adapter.ConfirmLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation, 500*time.Millisecond); err != nil {
		t.Fatalf("stage A proof: %v", err)
	}

	sentinel := exec.Command("/bin/sleep", "30")
	sentinel.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sentinel.Start(); err != nil {
		t.Fatalf("start test-owned sentinel: %v", err)
	}
	t.Cleanup(func() {
		if sentinel.Process != nil {
			_ = sentinel.Process.Kill()
		}
		_ = sentinel.Wait()
	})
	sentinelPID := sentinel.Process.Pid
	sentinelGroup, err := syscall.Getpgid(sentinelPID)
	if err != nil {
		t.Fatal(err)
	}
	sentinelStart, err := inspectMacProcessStartIdentity(sentinelPID)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := readRuntimeOwnership(fixture.workspaceRoot, fixture.prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	owner.PID = sentinelPID
	owner.ProcessGroupID = sentinelGroup
	owner.Command = "/bin/sleep"
	owner.ProcessStartIdentity = sentinelStart
	bug011ReplaceMacOwnerRecord(t, fixture.workspaceRoot, fixture.prepared.SessionID, owner)

	if _, err := fixture.adapter.FinalizeLostRecoveryCleanup(context.Background(), fixture.prepared.SessionID, fixture.prepared.Generation); err != nil {
		t.Fatalf("finalization against replacement identity: %v", err)
	}
	if err := syscall.Kill(sentinelPID, 0); err != nil {
		t.Fatalf("finalization touched replacement PID %d: %v", sentinelPID, err)
	}
}

type bug011MacFixture struct {
	adapter       *MacProcessAdapter
	workspaceRoot string
	prepared      MacPrepared
	shell         *PersistentShell
	record        MacProcessRecord
}

func newBUG011MacFixture(t *testing.T, suffix string) *bug011MacFixture {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("BUG-011 Mac lost-runtime proof requires Darwin")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewMacProcessAdapter(MacRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-bug011-mac-"+suffix, "generation-bug011-mac-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := adapter.Inspect(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &bug011MacFixture{adapter: adapter, workspaceRoot: workspaceRoot, prepared: prepared, shell: shell, record: record}
	t.Cleanup(func() {
		_ = os.Chmod(workspaceRoot, 0o700)
		_ = syscall.Kill(-record.ProcessGroupID, syscall.SIGKILL)
		_ = shell.Close()
	})
	return fixture
}

func bug011WaitForMacZombie(t *testing.T, pid, processGroupID int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		members, err := inspectMacProcessGroupMembers(processGroupID)
		if err == nil {
			for _, member := range members {
				if member.PID == pid && member.Status == macProcessStatusZombie {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("test-owned Bash PID %d did not become a zombie in group %d: members=%+v err=%v", pid, processGroupID, members, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bug011WaitForMacGroupMember(t *testing.T, processGroupID, pid int) []macProcessGroupMember {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		members, err := inspectMacProcessGroupMembers(processGroupID)
		if err == nil {
			for _, member := range members {
				if member.PID == pid && member.runnable() {
					return members
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("test-owned process PID %d did not join group %d: members=%+v err=%v", pid, processGroupID, members, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bug011AssertMacRunnable(t *testing.T, pid, processGroupID int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("test-owned PID %d is not live: %v", pid, err)
	}
	members, err := inspectMacProcessGroupMembers(processGroupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.PID == pid && member.runnable() {
			return
		}
	}
	t.Fatalf("test-owned PID %d is not a runnable group %d member: %+v", pid, processGroupID, members)
}

func bug011AssertMacProofAbsent(t *testing.T, workspaceRoot, sessionID string) {
	t.Helper()
	path, err := runtimeOwnershipPath(workspaceRoot, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("lost_recovery_cleanup_confirmed_at")) {
		t.Fatalf("owner marker unexpectedly contains a lost-recovery proof: %s", path)
	}
}

func bug011ReplaceMacOwnerRecord(t *testing.T, workspaceRoot, sessionID string, record RuntimeOwnershipRecord) {
	t.Helper()
	path, err := runtimeOwnershipPath(workspaceRoot, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishRuntimeOwnership(path, filepath.Dir(path), record); err != nil {
		t.Fatal(err)
	}
}

func bug011WriteMalformedMacOwnerRecord(t *testing.T, workspaceRoot, sessionID string) {
	t.Helper()
	path, err := runtimeOwnershipPath(workspaceRoot, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{malformed owner record"), 0o600); err != nil {
		t.Fatal(err)
	}
}
