package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestP041LinuxProcessAdapterTracksGroupDescendantsAndStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P041 real-host gate runs on the designated Linux host")
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
	adapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-p041", "generation-p041")
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
	defer func() { _ = adapter.Cleanup(prepared.SessionID) }()

	record, err := adapter.Inspect(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ProcessGroupID != record.PID || record.Generation != prepared.Generation {
		t.Fatalf("process-group ownership = %+v, prepared=%+v", record, prepared)
	}
	if _, err := shell.RunScript(context.Background(), "command-p041-child", []byte("(sleep 5 >/dev/null 2>&1) &\nprintf 'child-started'\n")); err != nil {
		t.Fatal(err)
	}
	var descendants []DescendantProcess
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		descendants, err = adapter.InspectDescendants(prepared.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(descendants) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(descendants) == 0 {
		t.Fatal("expected a known background descendant")
	}
	cleanup, err := adapter.CleanupDescendants(context.Background(), prepared.SessionID, 750*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanup.Confirmed || len(cleanup.Remaining) != 0 {
		t.Fatalf("descendant cleanup = %+v, want confirmed empty", cleanup)
	}
	retained, err := adapter.CapacityRetained(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if retained {
		t.Fatal("confirmed descendant cleanup retained capacity")
	}

	commandDone := make(chan error, 1)
	go func() {
		_, runErr := shell.RunScript(context.Background(), "command-p041-stop", []byte("sleep 30\n"))
		commandDone <- runErr
	}()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if descendants, inspectErr := adapter.InspectDescendants(prepared.SessionID); inspectErr == nil && len(descendants) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop, stopErr := adapter.StopCommand(context.Background(), prepared.SessionID, 500*time.Millisecond)
	if stopErr != nil && !errors.Is(stopErr, ErrPersistentShellCommand) && !errors.Is(stopErr, ErrPersistentShellLost) {
		t.Fatalf("stop error = %v", stopErr)
	}
	select {
	case <-commandDone:
	case <-time.After(3 * time.Second):
		t.Fatal("stopped command did not return")
	}
	if stop.CommandID != "command-p041-stop" && stop.CommandID != "" {
		t.Fatalf("stop command ID = %q", stop.CommandID)
	}
	currentShell, shellErr := adapter.Shell(prepared.SessionID)
	if shellErr != nil {
		t.Fatal(shellErr)
	}
	if currentShell != shell {
		t.Fatal("adapter created a replacement shell")
	}
}
