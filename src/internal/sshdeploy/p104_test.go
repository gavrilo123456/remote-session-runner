package sshdeploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestP104ForcedWrapperMatchesMacSSHClientCommand(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	scriptPath := filepath.Join(repositoryRoot, "deploy", "ssh", "runner-ssh-bridge-forced.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	const checkedInBinary = `BRIDGE_BIN="/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge"`
	const checkedInRoot = `SERVICE_ROOT="/home/ubuntu/.local/share/remote-session-runner"`
	if strings.Count(string(script), checkedInBinary) != 1 {
		t.Fatalf("wrapper must contain exactly one fixed bridge-binary path %q", checkedInBinary)
	}
	if strings.Count(string(script), checkedInRoot) != 1 {
		t.Fatalf("wrapper must contain exactly one service root %q", checkedInRoot)
	}

	temporary := t.TempDir()
	serviceRoot := filepath.Join(temporary, "service")
	if err := os.Mkdir(serviceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(temporary, "runner-ssh-bridge")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'P104_FIXED_BINARY=%s\\n' \"$*\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(temporary, "forced.sh")
	rendered := strings.Replace(string(script), checkedInBinary, `BRIDGE_BIN="`+binary+`"`, 1)
	rendered = strings.Replace(rendered, checkedInRoot, `SERVICE_ROOT="`+serviceRoot+`"`, 1)
	if err := os.WriteFile(wrapper, []byte(rendered), 0o700); err != nil {
		t.Fatal(err)
	}

	run := func(originalCommand, sshTTY, authenticatedKey string) ([]byte, error) {
		t.Helper()
		command := exec.Command("/bin/sh", wrapper, authenticatedKey)
		command.Env = []string{"PATH=/usr/bin:/bin", "SSH_ORIGINAL_COMMAND=" + originalCommand}
		if sshTTY != "" {
			command.Env = append(command.Env, "SSH_TTY="+sshTTY)
		}
		return command.CombinedOutput()
	}

	fingerprint := "SHA256:" + strings.Repeat("A", 43)
	output, err := run("runner-ssh-bridge --stdio", "", fingerprint)
	want := "P104_FIXED_BINARY=--stdio --authenticated-key " + fingerprint + " --controller-map /home/ubuntu/.local/share/remote-session-runner/config/ssh-controller-map.yaml --runnerd-socket /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock\n"
	if err != nil || string(output) != want {
		t.Fatalf("fixed client command output=%q err=%v", output, err)
	}
	for _, forged := range []string{
		"/home/ubuntu/.local/share/remote-session-runner/bin/runner-ssh-bridge --stdio",
		"runner-ssh-bridge --stdio --shell",
		"sh -c id",
	} {
		output, err := run(forged, "", fingerprint)
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "requested command is not permitted") {
			t.Errorf("forged original command %q output=%q err=%v, want wrapper exit 126", forged, output, err)
		}
	}
	output, err = run("runner-ssh-bridge --stdio", "/dev/pts/7", fingerprint)
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "PTY is not permitted") {
		t.Fatalf("PTY wrapper output=%q err=%v, want exit 126", output, err)
	}
	output, err = run("runner-ssh-bridge --stdio", "", "SHA256:invalid")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "authenticated key identity is not permitted") {
		t.Fatalf("invalid authenticated key output=%q err=%v, want exit 126", output, err)
	}
	if err := os.Symlink("releases/"+strings.Repeat("a", 64), filepath.Join(serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	output, err = run("runner-ssh-bridge --stdio", "", fingerprint)
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 126 || !strings.Contains(string(output), "legacy wrapper is disabled for catalog-selected PRD") {
		t.Fatalf("legacy wrapper catalog-selection output=%q err=%v, want fail-closed exit 126", output, err)
	}
}
