package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
)

func TestP040LinuxProcessAdapterRequiresLinuxHost(t *testing.T) {
	_, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: LinuxHostAccount})
	if runtime.GOOS != "linux" {
		if !errors.Is(err, ErrLinuxRuntimePlatform) {
			t.Fatalf("constructor error = %v, want ErrLinuxRuntimePlatform", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestP040LinuxProcessAdapterRealHostLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P040 real-host gate runs on the designated Linux host")
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
	prepared, err := adapter.Prepare(context.Background(), "session-p040", "generation-p040")
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.OwnedWorkspace || prepared.OwnerAccount != LinuxHostAccount || prepared.Workspace == workspaceRoot {
		t.Fatalf("prepared ownership = %+v", prepared)
	}
	info, err := os.Stat(prepared.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace mode = %o, want 700", info.Mode().Perm())
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.RunScript(context.Background(), "command-p040-identity", []byte("printf '%s|%s|%s' \"$(id -un)\" \"$(id -u)\" \"$PWD\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := current.Username + "|" + current.Uid + "|" + prepared.Workspace
	if string(result.Stdout) != want {
		t.Fatalf("identity output = %q, want %q", result.Stdout, want)
	}
	record, err := adapter.Inspect(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.SessionID != prepared.SessionID || record.Generation != prepared.Generation || record.Workspace != prepared.Workspace || record.Username != current.Username || record.UID != os.Getuid() {
		t.Fatalf("process ownership = %+v, prepared=%+v", record, prepared)
	}
	if err := adapter.Cleanup(prepared.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prepared.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace after cleanup error = %v, want not-exist", err)
	}
}
