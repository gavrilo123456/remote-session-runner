package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestP033R02ExitMarksShellLostWithoutReplacement(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p033-exit", Generation: "generation-p033-exit"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	if _, err := shell.RunScript(context.Background(), "command-p033-exit", []byte("printf 'before-exit'\nexit 7\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("exit error = %v, want ErrPersistentShellExited", err)
	}
	if _, err := shell.RunScript(context.Background(), "command-p033-after-exit", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-exit error = %v, want ErrPersistentShellLost", err)
	}
}

func TestP033R02ExecMarksShellLostWithoutReplacement(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p033-exec", Generation: "generation-p033-exec"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	if _, err := shell.RunScript(context.Background(), "command-p033-exec", []byte("exec /bin/true\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("exec error = %v, want ErrPersistentShellExited", err)
	}
	if _, err := shell.RunScript(context.Background(), "command-p033-after-exec", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-exec error = %v, want ErrPersistentShellLost", err)
	}
}

func TestP033R02ReservedControlDescriptorBreakMarksShellLost(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p033-fd", Generation: "generation-p033-fd"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	if _, err := shell.RunScript(context.Background(), "command-p033-fd", []byte("exec 4>&-\nprintf 'must-not-spoof'\n")); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("reserved-fd error = %v, want ErrPersistentShellExited", err)
	}
	if _, err := shell.RunScript(context.Background(), "command-p033-after-fd", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-fd error = %v, want ErrPersistentShellLost", err)
	}
}

func TestP033R02DelayedPipeReaderMakesBoundaryUncertainAndBlocksNextCommand(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:             "session-p033-delayed",
		Generation:            "generation-p033-delayed",
		OutputBoundaryTimeout: 60 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	started := time.Now()
	_, err = shell.RunScript(context.Background(), "command-p033-delayed", []byte("printf 'prefix'\n(sleep 0.4) &\n"))
	if !errors.Is(err, ErrOutputBoundary) {
		t.Fatalf("delayed reader error = %v, want ErrOutputBoundary", err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("boundary wait elapsed = %s, want bounded timeout", elapsed)
	}
	if _, err := shell.RunScript(context.Background(), "command-p033-after-delay", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-boundary error = %v, want ErrPersistentShellLost", err)
	}
}
