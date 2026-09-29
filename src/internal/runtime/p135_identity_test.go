package runtime

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"syscall"
	"testing"
)

func TestP135MacRejectsReusedPIDWithDifferentStartIdentity(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("Mac process birth identity test runs on macOS")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID: "p135-identity-session", Generation: "p135-identity-generation", Workspace: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	pid := shell.cmd.Process.Pid
	processGroupID, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	startIdentity, err := inspectMacProcessStartIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	record := RuntimeOwnershipRecord{
		Version: runtimeOwnershipVersion, HostOS: goruntime.GOOS,
		SessionID: "p135-identity-session", Generation: "p135-identity-generation",
		Workspace: workspace, PID: pid, ProcessGroupID: processGroupID,
		UID: os.Getuid(), Username: current.Username, Command: "/bin/bash",
		ProcessStartIdentity: startIdentity,
	}
	if err := verifyMacProcessIdentity(record); err != nil {
		t.Fatalf("current Mac process identity was rejected: %v", err)
	}
	reused := record
	reused.ProcessStartIdentity = startIdentity + "-different"
	if err := verifyMacProcessIdentity(reused); err == nil {
		t.Fatal("reused PID with a different process birth identity was accepted")
	}
}
