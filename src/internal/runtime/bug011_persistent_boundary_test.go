package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBUG011PersistentShellBoundaryContract freezes the harmless fixtures
// used to distinguish an ordinary command failure from the two boundaries
// that leave a persistent shell unavailable.  These scripts are deliberately
// local-only and do not use a repository, network, mailbox, or installed
// Runner service.
func TestBUG011PersistentShellBoundaryContract(t *testing.T) {
	t.Run("ordinary_nonzero_keeps_shell_usable", func(t *testing.T) {
		shell := bug011StartShell(t, "ordinary-nonzero", 0)
		defer func() { _ = shell.Close() }()

		result, err := shell.RunScript(context.Background(), "command-bug011-ordinary-nonzero", []byte("printf 'before\\n'\nfalse\n"))
		if err != nil {
			t.Fatalf("ordinary nonzero error = %v", err)
		}
		if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 1 {
			if result.CommandComplete.ExitCode == nil {
				t.Fatalf("ordinary nonzero completion = %+v, want exit 1", result.CommandComplete)
			}
			t.Fatalf("ordinary nonzero exit = %d, want 1", *result.CommandComplete.ExitCode)
		}
		if got, want := string(result.Stdout), "before\n"; got != want {
			t.Fatalf("ordinary nonzero stdout = %q, want %q", got, want)
		}
		if shell.CapacityRetained() {
			t.Fatal("ordinary nonzero retained capacity")
		}

		after, err := shell.RunScript(context.Background(), "command-bug011-ordinary-after", []byte("printf 'after\\n'\n"))
		if err != nil {
			t.Fatalf("post-nonzero command error = %v", err)
		}
		if got, want := string(after.Stdout), "after\n"; got != want {
			t.Fatalf("post-nonzero stdout = %q, want %q", got, want)
		}
	})

	t.Run("sourced_errexit_is_a_complete_command_failure", func(t *testing.T) {
		shell := bug011StartShell(t, "sourced-errexit", 0)
		defer func() { _ = shell.Close() }()

		result, err := shell.RunScript(context.Background(), "command-bug011-sourced-errexit", []byte("cd /tmp\nexport RSR_BUG015_STATE=before-errexit\nrsr_bug015_function() { printf function; }\nprintf 'stdout-before-errexit\\n'\nprintf 'stderr-before-errexit\\n' >&2\nset -euo pipefail\n/bin/sh -c 'exit 1'\nprintf 'must-not-reach\\n'\n"))
		if err != nil {
			t.Fatalf("sourced errexit error = %v", err)
		}
		if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 1 {
			t.Fatalf("sourced errexit completion = %+v, want exit 1", result.CommandComplete)
		}
		if got, want := string(result.Stdout), "stdout-before-errexit\n"; got != want {
			t.Fatalf("sourced errexit stdout = %q, want %q", got, want)
		}
		if got, want := string(result.Stderr), "stderr-before-errexit\n"; got != want {
			t.Fatalf("sourced errexit stderr = %q, want %q", got, want)
		}
		if shell.CapacityRetained() || !shell.processAlive() {
			t.Fatalf("sourced errexit left shell unavailable: retained=%t alive=%t", shell.CapacityRetained(), shell.processAlive())
		}
		after, err := shell.RunScript(context.Background(), "command-bug011-sourced-errexit-after", []byte("printf 'state=%s|%s|%s\\n' \"$PWD\" \"$RSR_BUG015_STATE\" \"$(rsr_bug015_function)\"\n"))
		if err != nil {
			t.Fatalf("post-errexit command error = %v", err)
		}
		if got, want := string(after.Stdout), "state=/tmp|before-errexit|function\n"; got != want {
			t.Fatalf("post-errexit state = %q, want %q", got, want)
		}
	})

	t.Run("sourced_errexit_command_substitution_is_a_complete_command_failure", func(t *testing.T) {
		shell := bug011StartShell(t, "sourced-errexit-command-substitution", 0)
		defer func() { _ = shell.Close() }()

		result, err := shell.RunScript(context.Background(), "command-bug011-sourced-errexit-command-substitution", []byte("set -euo pipefail\nvalue=$(false)\nprintf 'must-not-reach\\n'\n"))
		if err != nil {
			t.Fatalf("sourced errexit command substitution error = %v", err)
		}
		if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 1 {
			t.Fatalf("sourced errexit command substitution completion = %+v, want exit 1", result.CommandComplete)
		}
		if got := string(result.Stdout); got != "" {
			t.Fatalf("sourced errexit command substitution stdout = %q, want empty", got)
		}
		if got := string(result.Stderr); got != "" {
			t.Fatalf("sourced errexit command substitution stderr = %q, want empty", got)
		}
		if shell.CapacityRetained() || !shell.processAlive() {
			t.Fatalf("sourced errexit command substitution left shell unavailable: retained=%t alive=%t", shell.CapacityRetained(), shell.processAlive())
		}
	})

	t.Run("output_boundary_is_independent_of_shell_exit", func(t *testing.T) {
		shell, childPIDPath := bug011StartOutputBoundaryShell(t)
		defer func() { _ = shell.Close() }()

		script := "printf 'prefix\\n'\n(sleep 0.5) &\nprintf '%s\\n' \"$!\" >" + shellQuote(childPIDPath) + "\n"
		_, err := shell.RunScript(context.Background(), "command-bug011-output-boundary", []byte(script))
		if !errors.Is(err, ErrOutputBoundary) {
			t.Fatalf("output boundary error = %v, want ErrOutputBoundary", err)
		}
		if errors.Is(err, ErrPersistentShellExited) {
			t.Fatalf("output boundary error = %v, must not be ErrPersistentShellExited", err)
		}
		if !shell.processAlive() {
			t.Fatal("output-boundary fixture unexpectedly exited the persistent shell")
		}
		if !shell.CapacityRetained() {
			t.Fatal("output-boundary fixture did not retain capacity")
		}
		if _, err := shell.RunScript(context.Background(), "command-bug011-output-boundary-after", []byte("printf 'must-not-run\\n'\n")); !errors.Is(err, ErrPersistentShellLost) {
			t.Fatalf("post-boundary command error = %v, want ErrPersistentShellLost", err)
		}
		if _, err := os.Stat(childPIDPath); err != nil {
			t.Fatalf("test-owned child PID marker = %v", err)
		}

		cleanup, cleanupErr := shell.CleanupDescendants(context.Background(), time.Second)
		if cleanupErr != nil || !cleanup.Confirmed || len(cleanup.Remaining) != 0 {
			t.Fatalf("test-owned descendant cleanup = %+v, err = %v", cleanup, cleanupErr)
		}
		if shell.CapacityRetained() {
			t.Fatal("confirmed test-owned descendant cleanup still retained capacity")
		}
	})
}

func bug011StartShell(t *testing.T, suffix string, boundaryTimeout time.Duration) *PersistentShell {
	t.Helper()
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:             "session-bug011-" + suffix,
		Generation:            "generation-bug011-" + suffix,
		Workspace:             t.TempDir(),
		OutputBoundaryTimeout: boundaryTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return shell
}

// bug011StartOutputBoundaryShell replaces the host-wide ps walk with a narrow
// fixture inspector that can observe and signal only the PID written by this
// test-owned script in its temporary workspace.  It makes the output-boundary
// test hermetic without weakening the production process inspector.
func bug011StartOutputBoundaryShell(t *testing.T) (*PersistentShell, string) {
	t.Helper()
	workspace := t.TempDir()
	childPIDPath := filepath.Join(workspace, "bug011-child.pid")
	readPID := func() (int, error) {
		encoded, err := os.ReadFile(childPIDPath)
		if err != nil {
			return 0, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(encoded)))
		if err != nil || pid <= 0 {
			return 0, fmt.Errorf("invalid test-owned child PID: %q", strings.TrimSpace(string(encoded)))
		}
		return pid, nil
	}
	inspect := func(int) ([]DescendantProcess, error) {
		pid, err := readPID()
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err := syscall.Kill(pid, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil, nil
			}
			return nil, err
		}
		return []DescendantProcess{{PID: pid, Command: "test-owned sleep"}}, nil
	}
	kill := func(_ int, signal syscall.Signal) int {
		pid, err := readPID()
		if err != nil || syscall.Kill(pid, signal) != nil {
			return 0
		}
		return 1
	}
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:             "session-bug011-output-boundary",
		Generation:            "generation-bug011-output-boundary",
		Workspace:             workspace,
		OutputBoundaryTimeout: 50 * time.Millisecond,
		ProcessInspector:      inspect,
		DescendantKiller:      kill,
	})
	if err != nil {
		t.Fatal(err)
	}
	return shell, childPIDPath
}
