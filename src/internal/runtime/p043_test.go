package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP043LinuxIsolationValidationRejectsUnavailableControls(t *testing.T) {
	cases := []struct {
		name string
		set  func(*domain.IsolationRequirements)
	}{
		{"filesystem", func(r *domain.IsolationRequirements) { r.FilesystemBoundary = true }},
		{"cpu", func(r *domain.IsolationRequirements) { r.CPUControl = true }},
		{"memory", func(r *domain.IsolationRequirements) { r.MemoryControl = true }},
		{"pid", func(r *domain.IsolationRequirements) { r.PIDControl = true }},
		{"disk", func(r *domain.IsolationRequirements) { r.DiskControl = true }},
		{"network", func(r *domain.IsolationRequirements) { r.NetworkControl = true }},
		{"mounts", func(r *domain.IsolationRequirements) { r.Mounts = true }},
		{"volumes", func(r *domain.IsolationRequirements) { r.Volumes = true }},
		{"privilege", func(r *domain.IsolationRequirements) { r.PrivilegeControl = true }},
	}
	if err := ValidateLinuxIsolationRequirements(domain.IsolationRequirements{}); err != nil {
		t.Fatalf("empty isolation request error = %v", err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var request domain.IsolationRequirements
			test.set(&request)
			if err := ValidateLinuxIsolationRequirements(request); !errors.Is(err, domain.ErrUnsupportedIsolationRequirement) {
				t.Fatalf("validation error = %v, want ErrUnsupportedIsolationRequirement", err)
			}
		})
	}
}

func TestP043LinuxStatusAndOSAccessRealHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P043 real-host gate runs on the designated Linux host")
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
	if capabilities := adapter.Capabilities(); capabilities.Isolation != "os-user" || capabilities.EffectiveAccount != LinuxHostAccount || len(capabilities.ServiceControls) != 0 {
		t.Fatalf("truthful capabilities = %+v", capabilities)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-p043", "generation-p043")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Cleanup(prepared.SessionID) }()
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.RunScript(context.Background(), "command-p043-access", []byte("printf '%s|%s|%s' \"$(id -un)\" \"$(id -u)\" \"$(test -w \"$PWD\" && echo writable)\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := current.Username + "|" + current.Uid + "|writable"
	if string(result.Stdout) != want {
		t.Fatalf("OS access output = %q, want %q", result.Stdout, want)
	}
	status, err := adapter.Status(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.CapacityRetained || status.Process.UID != os.Getuid() || status.Process.Username != current.Username || status.Generation != prepared.Generation {
		t.Fatalf("runtime status = %+v", status)
	}
}
