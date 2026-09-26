package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestP042LinuxReconcileQuarantinesPriorGeneration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P042 real-host gate runs on the designated Linux host")
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
	prepared, err := oldAdapter.Prepare(context.Background(), "session-p042", "generation-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := oldAdapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldAdapter.Cleanup(prepared.SessionID) }()
	record, err := oldAdapter.Inspect(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}

	newAdapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := record
	unknown.UID++
	unknownResult, unknownErr := newAdapter.ReconcileProcess(context.Background(), unknown, "generation-new", 100*time.Millisecond)
	if unknownErr == nil || !errors.Is(unknownErr, ErrLinuxRuntimeOwnership) || !unknownResult.CapacityRetained {
		t.Fatalf("unknown ownership result=%+v err=%v", unknownResult, unknownErr)
	}
	result, err := newAdapter.ReconcileProcess(context.Background(), record, "generation-new", 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Quarantined || result.Reattached || result.SessionID != prepared.SessionID || !strings.Contains(result.Reason, "generation mismatch") {
		t.Fatalf("reconciliation result = %+v", result)
	}
	if _, err := newAdapter.Shell(prepared.SessionID); !errors.Is(err, ErrLinuxRuntimeAccount) {
		t.Fatalf("new adapter shell lookup error = %v, want no reattachment", err)
	}
	if result.CleanupConfirmed && result.CapacityRetained {
		t.Fatalf("confirmed cleanup retained capacity: %+v", result)
	}
}
