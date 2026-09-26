package runtime

import (
	"context"
	"os"
	"os/user"
	goruntime "runtime"
	"testing"
)

func TestP037MacProcessAdapterUsesCurrentAccountAndOwnerWorkspace(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("P037 is a macOS host gate")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if current.Username != "tomasz.walczuk" {
		t.Fatalf("current account = %q, want tomasz.walczuk", current.Username)
	}
	root := t.TempDir()
	adapter, err := NewMacProcessAdapter(MacRuntimeOptions{Account: current.Username, WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-p037", "generation-p037")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(prepared.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("workspace mode = %o, want 700", got)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.RunScript(context.Background(), "command-p037-uid", []byte("printf '%s|%s' \"$(id -u)\" \"$PWD\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) == "" {
		t.Fatal("Mac adapter command returned empty identity output")
	}
	if err := adapter.Cleanup(prepared.SessionID); err != nil {
		t.Fatal(err)
	}
}
