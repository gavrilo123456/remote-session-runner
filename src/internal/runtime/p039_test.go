package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"runtime"
	"testing"
)

func TestP039LinuxCapabilitiesAreTruthful(t *testing.T) {
	capabilities := linuxHostCapabilities(LinuxHostAccount)
	if capabilities.HostClass != LinuxHostProfile || capabilities.EffectiveAccount != LinuxHostAccount || capabilities.Isolation != "os-user" {
		t.Fatalf("unexpected Linux capabilities: %+v", capabilities)
	}
	if len(capabilities.ServiceControls) != 0 {
		t.Fatalf("preflight invented service controls: %v", capabilities.ServiceControls)
	}
	for _, unsupported := range []string{"filesystem", "cpu", "memory", "pid", "disk", "network", "mounts", "volumes", "privilege"} {
		if !containsLinuxString(capabilities.UnsupportedIsolationControls, unsupported) {
			t.Errorf("unsupported control %q missing from capabilities", unsupported)
		}
	}
}

func TestP039LinuxProfileRequiresLinuxHost(t *testing.T) {
	_, err := NewLinuxProcessProfile(LinuxRuntimeOptions{Account: LinuxHostAccount})
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

func TestP039LinuxDoctorRejectsUnsafeServicePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	profile := newLinuxProcessProfile(LinuxRuntimeOptions{
		Account:       current.Username,
		ServiceRoot:   root,
		WorkspaceRoot: root,
	}, linuxProfileHooks{
		currentUser: func() (*user.User, error) { return current, nil },
		currentUID:  func() int { return uid },
		hostname:    func() (string, error) { return "test-host", nil },
		lookPath:    func(path string) (string, error) { return "/bin/bash", nil },
	})
	report, err := profile.Doctor(context.Background())
	if err == nil || !errors.Is(err, ErrLinuxProfileNotReady) {
		t.Fatalf("doctor error = %v, want ErrLinuxProfileNotReady", err)
	}
	if report.Ready || len(report.Failures) == 0 {
		t.Fatalf("unsafe path report = %+v", report)
	}
}

func TestP039LinuxDoctorRealHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P039 real-host gate runs on the designated Linux host")
	}
	root := t.TempDir()
	workspace := root + "/workspaces"
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	profile, err := NewLinuxProcessProfile(LinuxRuntimeOptions{
		Account:       LinuxHostAccount,
		ServiceRoot:   root,
		WorkspaceRoot: workspace,
		ShellPath:     "/usr/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := profile.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Linux doctor report = %+v, error = %v", report, err)
	}
	if !report.Ready || report.Account != LinuxHostAccount || report.UID != os.Getuid() || report.Process.UID != os.Getuid() {
		t.Fatalf("unexpected ready report: %+v", report)
	}
	if !report.Process.Inspected || !report.Process.Signaled || !report.Process.Exited {
		t.Fatalf("process probe did not complete: %+v", report.Process)
	}
	for _, path := range report.Paths {
		if !path.OwnerOnly || !path.Writable || !path.Directory {
			t.Fatalf("service path check failed: %+v", path)
		}
	}
}

func containsLinuxString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
