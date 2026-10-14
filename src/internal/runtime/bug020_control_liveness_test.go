package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test-owned helper escapes the Bash group and retains its inherited
// control writer. Killing Bash must therefore be detected independently of
// control-pipe EOF, without signaling the escaped process or reaping Bash.
func TestBUG020ControlWriterHelper(t *testing.T) {
	if os.Getenv("RSR_BUG020_CONTROL_WRITER") != "1" {
		return
	}
	if _, err := syscall.Setsid(); err != nil {
		os.Exit(2)
	}
	pidPath := os.Getenv("RSR_BUG020_PID_FILE")
	if err := os.WriteFile(pidPath+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(3)
	}
	if err := os.Rename(pidPath+".tmp", pidPath); err != nil {
		os.Exit(4)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestBUG020OwnedBashLossDoesNotWaitForInheritedControlEOF(t *testing.T) {
	shell, childPID, done := bug020ControlWriterFixture(t, context.Background(), false)
	if err := syscall.Kill(shell.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPersistentShellExited) {
			t.Fatalf("owned Bash loss error = %v, want shell exited", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owned Bash loss waited for an escaped control writer")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("escaped test-owned writer was unexpectedly stopped: %v", err)
	}
	if shell.cmd.ProcessState != nil {
		t.Fatal("liveness detection reaped the Bash ownership handle")
	}
	bug020AssertLostWithoutReplay(t, shell)
}

func TestBUG020CancelMissingControlCompletionDoesNotDeadlock(t *testing.T) {
	shell, childPID, done := bug020ControlWriterFixture(t, context.Background(), true)
	stopped := make(chan PersistentShellStopResult, 1)
	stopErrors := make(chan error, 1)
	go func() {
		result, err := shell.CancelCurrentCommand(context.Background(), 50*time.Millisecond)
		stopped <- result
		stopErrors <- err
	}()
	select {
	case result := <-stopped:
		if result.Confirmed {
			t.Fatalf("missing completion was falsely confirmed: %+v", result)
		}
		if err := <-stopErrors; !errors.Is(err, ErrPersistentShellLost) {
			t.Fatalf("missing completion cancellation error = %v, want shell lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for the execution lock held by control ReadFull")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPersistentShellExited) {
			t.Fatalf("interrupted control wait error = %v, want shell exited", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the control reader")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("cancellation signaled the escaped test-owned writer: %v", err)
	}
	bug020AssertLostWithoutReplay(t, shell)
}

func TestBUG020CommandContextCancellationInterruptsControlWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shell, _, done := bug020ControlWriterFixture(t, ctx, true)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPersistentShellExited) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled control wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("command context did not interrupt the control reader")
	}
	bug020AssertLostWithoutReplay(t, shell)
}

func bug020ControlWriterFixture(t *testing.T, ctx context.Context, suppressCompletion bool) (*PersistentShell, int, <-chan error) {
	t.Helper()
	workspace := t.TempDir()
	pidPath := filepath.Join(workspace, "escaped-writer.pid")
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:  "session-bug020-control-liveness",
		Generation: "generation-bug020-control-liveness",
		Workspace:  workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	childPID := 0
	done := make(chan error, 1)
	t.Cleanup(func() {
		if childPID > 0 {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
		_ = syscall.Kill(-shell.cmd.Process.Pid, syscall.SIGKILL)
		// Ensure cleanup can release a still-pending read even on test failure.
		shell.markLost()
		_ = shell.Close()
	})
	script := "RSR_BUG020_CONTROL_WRITER=1 RSR_BUG020_PID_FILE=" + shellQuote(pidPath) + " " + shellQuote(os.Args[0]) + " -test.run='^TestBUG020ControlWriterHelper$' &\n"
	if suppressCompletion {
		// Keep Bash live but unable to publish completion. The escaped writer
		// prevents fd 4 redirection from making EOF immediately visible.
		script += "trap '' INT\nexec 4>/dev/null\nsleep 30\n"
	} else {
		script += "wait\n"
	}
	go func() {
		_, err := shell.RunScript(ctx, "command-bug020-control-liveness", []byte(script))
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		encoded, err := os.ReadFile(pidPath)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(encoded)))
			if err != nil || childPID <= 0 {
				t.Fatalf("test-owned writer PID is invalid: %q", encoded)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("command returned before writer started: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("test-owned escaped control writer did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return shell, childPID, done
}

func bug020AssertLostWithoutReplay(t *testing.T, shell *PersistentShell) {
	t.Helper()
	if !shell.CapacityRetained() {
		t.Fatal("unproven process/output boundary released capacity")
	}
	sentinel := filepath.Join(shell.workspace, "must-not-replay")
	_, err := shell.RunScript(context.Background(), "command-bug020-after-loss", []byte("printf replayed >"+shellQuote(sentinel)+"\n"))
	if !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("follow-up command error = %v, want shell lost", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost shell executed a follow-up body: %v", err)
	}
}
