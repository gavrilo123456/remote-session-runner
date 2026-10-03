package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentShellStartupCommitGateBlocksOrdinaryLoopUntilCommit(t *testing.T) {
	workspace := t.TempDir()
	shell, err := startPersistentShellAwaitingCommit(context.Background(), PersistentShellOptions{
		SessionID:  "session-startup-commit",
		Generation: "generation-startup-commit",
		Workspace:  workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close startup-gated shell: %v", err)
		}
	})

	// This bypasses RunScript only to prove the child itself, rather than just
	// the Go API, cannot reach its normal stdin command loop before commit.
	sentinel := filepath.Join(workspace, "ordinary-loop-entered")
	if _, err := io.WriteString(shell.stdin, "printf blocked >"+shellQuote(sentinel)+"\n"); err != nil {
		t.Fatalf("queue ordinary stdin before commit: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary Bash loop ran before startup commit: stat error=%v", err)
	}
	if _, err := shell.RunScript(context.Background(), "command-before-startup-commit", []byte("printf must-not-run\n")); !errors.Is(err, ErrPersistentShellCommand) {
		t.Fatalf("RunScript before startup commit error = %v, want persistent-shell command error", err)
	}

	if err := shell.commitStartup(); err != nil {
		t.Fatalf("commit startup: %v", err)
	}
	if err := waitForStartupCommitPath(sentinel); err != nil {
		t.Fatal(err)
	}
	result, err := shell.RunScript(context.Background(), "command-after-startup-commit", []byte("printf 'committed\\n'\n"))
	if err != nil {
		t.Fatalf("run after startup commit: %v", err)
	}
	if got, want := string(result.Stdout), "committed\n"; got != want {
		t.Fatalf("stdout after startup commit = %q, want %q", got, want)
	}
	if err := shell.commitStartup(); !errors.Is(err, ErrPersistentShellCommand) {
		t.Fatalf("second startup commit error = %v, want unavailable commit error", err)
	}
}

func TestPersistentShellStartupCommitGateExitsWithoutExactlyOneExpectedByte(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "eof_before_commit", payload: ""},
		{name: "wrong_byte", payload: "X"},
		{name: "second_byte", payload: persistentShellStartupCommitByte + persistentShellStartupCommitByte},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			shell, err := startPersistentShellAwaitingCommit(context.Background(), PersistentShellOptions{
				SessionID:  "session-startup-commit-" + test.name,
				Generation: "generation-startup-commit-" + test.name,
				Workspace:  workspace,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = shell.Close() })

			sentinel := filepath.Join(workspace, "ordinary-loop-entered")
			if _, err := io.WriteString(shell.stdin, "printf must-not-run >"+shellQuote(sentinel)+"\n"); err != nil {
				t.Fatalf("queue ordinary stdin before invalid commit: %v", err)
			}

			// Detach the private writer only in this same-package test so it can
			// exercise the Bash prelude's EOF, wrong-byte, and extra-byte paths.
			shell.mu.Lock()
			commit := shell.startupCommit
			shell.startupCommit = nil
			shell.startupCommitWait = false
			shell.mu.Unlock()
			if commit == nil {
				t.Fatal("startup commit writer is missing")
			}
			if _, err := io.WriteString(commit, test.payload); err != nil {
				t.Fatalf("write invalid startup commit %q: %v", test.payload, err)
			}
			if err := commit.Close(); err != nil {
				t.Fatalf("close invalid startup commit: %v", err)
			}
			if err := waitForStartupCommitShellExit(shell); err != nil {
				t.Fatalf("startup-gated shell did not exit after invalid commit: %v", err)
			}
			if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ordinary Bash loop ran after invalid startup commit: stat error=%v", err)
			}
		})
	}
}

func waitForStartupCommitPath(path string) error {
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if time.Now().After(deadline) {
			return os.ErrNotExist
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForStartupCommitShellExit waits without closing stdin. That proves the
// child exits because its private gate saw EOF or an invalid byte, rather than
// because the test closed the ordinary command channel.
func waitForStartupCommitShellExit(shell *PersistentShell) error {
	done := make(chan error, 1)
	go func() { done <- shell.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-time.After(time.Second):
		return errors.New("timed out waiting for startup-gated Bash to exit")
	}
	shell.mu.Lock()
	shell.closed = true
	shell.startupCommitWait = false
	shell.mu.Unlock()
	_ = shell.stdin.Close()
	_ = shell.control.Close()
	return nil
}
