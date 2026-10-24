package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBUG026OneOffExitKeepsCompletionBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		status int
	}{
		{"preflight_exit", "set -euo pipefail\nprintf 'source_pin_mismatch\\n' >&2\nexit 1\nprintf must-not-run\n", 1},
		{"exit_zero", "exit 0\nprintf must-not-run\n", 0},
		{"exit_seven", "exit 7\nprintf must-not-run\n", 7},
		{"strict_failure", "set -euo pipefail\nfalse\nprintf must-not-run\n", 1},
		{"strict_pipeline", "set -euo pipefail\nfalse | true\nprintf must-not-run\n", 1},
		{"strict_substitution", "set -euo pipefail\nvalue=$(false)\nprintf must-not-run\n", 1},
		{"exec_failure", "exec /usr/bin/false\nprintf must-not-run\n", 1},
		{"return_failure", "return 3\nprintf must-not-run\n", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell := startBUG026Shell(t, 0)
			script := []byte("printf 'before\\000exit\\n'\n" + test.script)
			var stdout, stderr []byte
			result, err := shell.RunOneOffScriptWithOutput(context.Background(), "command-bug026-"+test.name, script, func(chunk OutputChunk) error {
				// Each stream has one drainer; these independent slices retain raw
				// bytes without depending on cross-stream scheduling.
				if chunk.Stream == OutputStreamStdout {
					stdout = append(stdout, chunk.Data...)
				} else {
					stderr = append(stderr, chunk.Data...)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != test.status {
				t.Fatalf("completion=%+v, want status %d", result.CommandComplete, test.status)
			}
			if got := string(result.Stdout); got != "before\x00exit\n" || got != string(stdout) {
				t.Fatalf("stdout=%q streamed=%q", got, stdout)
			}
			wantStderr := ""
			if test.name == "preflight_exit" {
				wantStderr = "source_pin_mismatch\n"
			}
			if got := string(result.Stderr); got != wantStderr || got != string(stderr) {
				t.Fatalf("stderr=%q streamed=%q, want %q", got, stderr, wantStderr)
			}
			if shell.CapacityRetained() || !shell.processAlive() {
				t.Fatalf("parent unavailable: retained=%t alive=%t", shell.CapacityRetained(), shell.processAlive())
			}
		})
	}
}

func TestBUG026OneOffChildCannotWriteParentControl(t *testing.T) {
	shell := startBUG026Shell(t, 0)
	result, err := shell.RunOneOffScriptWithOutput(context.Background(), "command-bug026-child-control", []byte("if (printf forged >&4) 2>/dev/null; then exit 99; fi\nprintf 'control-closed\\n'\nexit 1\n"), nil)
	if err != nil || result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 1 || string(result.Stdout) != "control-closed\n" {
		t.Fatalf("closed child control result=%+v err=%v", result, err)
	}
}

func TestBUG026OneOffLeakedOutputStillLosesBoundary(t *testing.T) {
	shell := startBUG026Shell(t, 50*time.Millisecond)
	_, err := shell.RunOneOffScriptWithOutput(context.Background(), "command-bug026-leaked-output", []byte("printf prefix\n(sleep 5) &\nexit 1\n"), nil)
	if !errors.Is(err, ErrOutputBoundary) || !shell.CapacityRetained() {
		t.Fatalf("leaked output err=%v retained=%t", err, shell.CapacityRetained())
	}
	if _, err := shell.RunOneOffScriptWithOutput(context.Background(), "command-bug026-after-loss", []byte("printf must-not-run\n"), nil); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-loss execution err=%v", err)
	}
}

func TestBUG026OneOffParentDeathStillLosesBoundary(t *testing.T) {
	shell := startBUG026Shell(t, 0)
	// Bash preserves $$ in a subshell: this kills only this fixture's owned
	// parent Bash, never the Go test process (which would be the inherited PPID).
	_, err := shell.RunOneOffScriptWithOutput(context.Background(), "command-bug026-parent-death", []byte("kill -KILL \"$$\"\n"), nil)
	if !errors.Is(err, ErrPersistentShellExited) || !shell.CapacityRetained() {
		t.Fatalf("parent death err=%v retained=%t", err, shell.CapacityRetained())
	}
}

func TestBUG026OneOffCallbackFailureStillLosesBoundary(t *testing.T) {
	shell := startBUG026Shell(t, 0)
	_, err := shell.RunOneOffScriptWithOutput(context.Background(), "command-bug026-callback", []byte("printf output\nexit 1\n"), func(OutputChunk) error {
		return errors.New("fixture output persistence failed")
	})
	if !errors.Is(err, ErrOutputCallback) || !shell.CapacityRetained() {
		t.Fatalf("callback failure err=%v retained=%t", err, shell.CapacityRetained())
	}
}

func TestBUG026OneOffCancellationDoesNotCompleteSuccess(t *testing.T) {
	shell := startBUG026Shell(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	var once sync.Once
	type outcome struct {
		result PersistentShellResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := shell.RunOneOffScriptWithOutput(ctx, "command-bug026-cancel", []byte("set -euo pipefail\nprintf 'ready\\n'\nsleep 30\nprintf must-not-run\n"), func(chunk OutputChunk) error {
			if strings.Contains(string(chunk.Data), "ready") {
				once.Do(func() { close(ready) })
			}
			return nil
		})
		done <- outcome{result, err}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("one-off child never started")
	}
	stop, stopErr := shell.CancelCurrentCommand(ctx, 750*time.Millisecond)
	select {
	case run := <-done:
		if stop.Confirmed {
			if stopErr != nil || run.err != nil || run.result.CommandComplete.ExitCode == nil || *run.result.CommandComplete.ExitCode == 0 || strings.Contains(string(run.result.Stdout), "must-not-run") {
				t.Fatalf("cancelled command stop=%+v stopErr=%v run=%+v err=%v", stop, stopErr, run.result, run.err)
			}
		} else if !errors.Is(stopErr, ErrPersistentShellLost) || run.err == nil || !shell.CapacityRetained() {
			t.Fatalf("unconfirmed stop=%+v err=%v runErr=%v retained=%t", stop, stopErr, run.err, shell.CapacityRetained())
		}
	case <-ctx.Done():
		t.Fatal("one-off cancellation did not settle within its bound")
	}
}

func startBUG026Shell(t *testing.T, boundary time.Duration) *PersistentShell {
	t.Helper()
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID: "session-bug026", Generation: "generation-bug026", Workspace: t.TempDir(), OutputBoundaryTimeout: boundary,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// This process group was created only by this disposable fixture. Kill
		// remaining test children before removing its temporary workspace.
		_ = signalProcessGroup(shell.cmd.Process.Pid, syscall.SIGKILL)
		_ = shell.Close()
	})
	return shell
}
