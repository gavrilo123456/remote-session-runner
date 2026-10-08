package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"
)

func TestLostRecoveryProofRetainsThenFinalizesLinuxOwnership(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("lost-recovery process proof requires the Linux host adapter")
	}
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
	oldAdapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := oldAdapter.Prepare(context.Background(), "session-lost-recovery-runtime", "generation-lost-recovery-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := oldAdapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldAdapter.Cleanup(prepared.SessionID) }()

	recoveryAdapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	proved, err := recoveryAdapter.ConfirmLostRecoveryCleanup(context.Background(), prepared.SessionID, prepared.Generation, 500*time.Millisecond)
	if err != nil || !proved.CleanupConfirmed {
		t.Fatalf("stage-A proof=%+v err=%v", proved, err)
	}
	owner, err := readRuntimeOwnership(workspaceRoot, prepared.SessionID)
	if err != nil || owner.LostRecoveryCleanupConfirmedAt == "" {
		t.Fatalf("proof record=%+v err=%v", owner, err)
	}
	if _, err := os.Stat(prepared.Workspace); err != nil {
		t.Fatalf("stage-A removed owned workspace before durable capacity release: %v", err)
	}
	reproved, err := recoveryAdapter.ConfirmLostRecoveryCleanup(context.Background(), prepared.SessionID, prepared.Generation, 500*time.Millisecond)
	if err != nil || !reproved.CleanupConfirmed {
		t.Fatalf("idempotent stage-A proof=%+v err=%v", reproved, err)
	}

	finalized, err := recoveryAdapter.FinalizeLostRecoveryCleanup(context.Background(), prepared.SessionID, prepared.Generation)
	if err != nil || !finalized.CleanupConfirmed {
		t.Fatalf("stage-C finalization=%+v err=%v", finalized, err)
	}
	if _, err := os.Stat(prepared.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage-C did not remove owned workspace: %v", err)
	}
	if _, err := readRuntimeOwnership(workspaceRoot, prepared.SessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage-C did not remove owner record: %v", err)
	}
	repeated, err := recoveryAdapter.FinalizeLostRecoveryCleanup(context.Background(), prepared.SessionID, prepared.Generation)
	if err != nil || !repeated.CleanupConfirmed {
		t.Fatalf("idempotent stage-C finalization=%+v err=%v", repeated, err)
	}
}

func TestPreStartLostRecoveryRequiresAbsentLinuxOwnership(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("pre-start lost-recovery proof requires the Linux host adapter")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("absent ownership marker", func(t *testing.T) {
		workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
		if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		adapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
		if err != nil {
			t.Fatal(err)
		}
		proved, err := adapter.ConfirmLostRecoveryCleanup(context.Background(), "session-pre-start-linux-absent", "", 100*time.Millisecond)
		if err != nil || !proved.CleanupConfirmed || !proved.CapacityRetained {
			t.Fatalf("absent pre-start proof=%+v err=%v", proved, err)
		}
		finalized, err := adapter.FinalizeLostRecoveryCleanup(context.Background(), "session-pre-start-linux-absent", "")
		if err != nil || !finalized.CleanupConfirmed || finalized.CapacityRetained {
			t.Fatalf("absent pre-start finalization=%+v err=%v", finalized, err)
		}
	})

	t.Run("present ownership marker", func(t *testing.T) {
		workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
		if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		adapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := adapter.Prepare(context.Background(), "session-pre-start-linux-present", "generation-pre-start-linux-present")
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.StartAgent(context.Background(), prepared); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = adapter.Cleanup(prepared.SessionID) })
		proved, err := adapter.ConfirmLostRecoveryCleanup(context.Background(), prepared.SessionID, "", 100*time.Millisecond)
		if err == nil || proved.CleanupConfirmed || !proved.CapacityRetained {
			t.Fatalf("present pre-start proof=%+v err=%v, want retained refusal", proved, err)
		}
		if _, ownerErr := readRuntimeOwnership(workspaceRoot, prepared.SessionID); ownerErr != nil {
			t.Fatalf("present marker after refusal: %v", ownerErr)
		}
	})
}
