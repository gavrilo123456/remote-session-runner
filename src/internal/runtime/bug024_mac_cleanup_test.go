package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	runtimeinfo "runtime"
	"syscall"
	"testing"
	"time"
)

func TestBUG024MacCleanupGroupProof(t *testing.T) {
	const group = 12345
	t.Run("transient member becomes absent", func(t *testing.T) {
		calls := 0
		err := waitMacCleanupGroupGone(context.Background(), group, time.Second, func(got int) (bool, error) {
			if got != group {
				t.Fatalf("inspected group %d, want owned group %d", got, group)
			}
			calls++
			return calls < 3, nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("cleanup proof calls=%d err=%v", calls, err)
		}
	})
	t.Run("persistent member remains unconfirmed", func(t *testing.T) {
		err := waitMacCleanupGroupGone(context.Background(), group, 20*time.Millisecond, func(int) (bool, error) {
			return true, nil
		})
		if !errors.Is(err, ErrRuntimeOwnershipRecord) {
			t.Fatalf("persistent group proof error=%v", err)
		}
	})
	t.Run("inspection error is not absence", func(t *testing.T) {
		probeErr := errors.New("group inspection unavailable")
		calls := 0
		err := waitMacCleanupGroupGone(context.Background(), group, time.Second, func(int) (bool, error) {
			calls++
			return false, probeErr
		})
		if !errors.Is(err, probeErr) || calls != 1 {
			t.Fatalf("inspection failure calls=%d err=%v", calls, err)
		}
	})
	t.Run("cancel before observation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := waitMacCleanupGroupGone(ctx, group, time.Second, func(int) (bool, error) {
			calls++
			return false, nil
		})
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("cancelled proof calls=%d err=%v", calls, err)
		}
	})
	t.Run("cancel during observation cannot confirm cleanup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := waitMacCleanupGroupGone(ctx, group, time.Second, func(int) (bool, error) {
			cancel()
			return false, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled absence proof error=%v", err)
		}
	})
}

func TestBUG024MacCleanupWaitsForRedirectedDescendantAfterRootExit(t *testing.T) {
	fixture := newBUG024MacCleanupFixture(t)
	rootGone := make(chan error, 1)
	go func() {
		if err := bug024WaitRootReaped(fixture.pid, 2*time.Second); err != nil {
			rootGone <- err
			return
		}
		rootGone <- os.WriteFile(fixture.release, nil, 0o600)
	}()
	if err := fixture.adapter.CleanupContext(context.Background(), fixture.prepared.SessionID); err != nil {
		t.Fatalf("cleanup did not allow owned descendant to finish: %v", err)
	}
	if err := <-rootGone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace after proven cleanup: %v", err)
	}
	if _, err := readRuntimeOwnership(fixture.root, fixture.prepared.SessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ownership marker after proven cleanup: %v", err)
	}
	if err := fixture.shell.Close(); err != nil {
		t.Fatalf("owned Bash was not closed idempotently: %v", err)
	}
	fixture.assertExecutedOnce(t)
}

func TestBUG024MacCleanupPersistentDescendantRetainsEvidenceAndCanRetry(t *testing.T) {
	fixture := newBUG024MacCleanupFixture(t)
	started := time.Now()
	err := fixture.adapter.CleanupContext(context.Background(), fixture.prepared.SessionID)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cleanup exceeded its post-reap proof window (%s): %v", elapsed, err)
	}
	if !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("persistent descendant cleanup error=%v", err)
	}
	fixture.assertRetained(t)
	if err := os.WriteFile(fixture.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitProcessGroupGone(context.Background(), fixture.pid, 2*time.Second) {
		t.Fatal("task-owned descendant did not finish after release")
	}
	if err := fixture.adapter.Cleanup(fixture.prepared.SessionID); err != nil {
		t.Fatalf("cleanup retry after exact group absence: %v", err)
	}
	fixture.assertExecutedOnce(t)
}

func TestBUG024MacCleanupCancellationRetainsEvidence(t *testing.T) {
	t.Run("before close", func(t *testing.T) {
		fixture := newBUG024MacCleanupFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := fixture.adapter.CleanupContext(ctx, fixture.prepared.SessionID); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled cleanup error=%v", err)
		}
		fixture.assertRetained(t)
		if err := syscall.Kill(fixture.pid, 0); err != nil {
			t.Fatalf("pre-cancelled cleanup closed Bash: %v", err)
		}
	})
	t.Run("after root exit", func(t *testing.T) {
		fixture := newBUG024MacCleanupFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rootGone := make(chan error, 1)
		go func() {
			err := bug024WaitRootReaped(fixture.pid, 2*time.Second)
			cancel()
			rootGone <- err
		}()
		started := time.Now()
		if err := fixture.adapter.CleanupContext(ctx, fixture.prepared.SessionID); !errors.Is(err, context.Canceled) {
			t.Fatalf("cleanup cancellation after root exit error=%v", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("cleanup did not observe cancellation after root reap within its proof window: %s", elapsed)
		}
		if err := <-rootGone; err != nil {
			t.Fatal(err)
		}
		fixture.assertRetained(t)
	})
}

type bug024MacCleanupFixture struct {
	adapter  *MacProcessAdapter
	prepared MacPrepared
	shell    *PersistentShell
	root     string
	pid      int
	release  string
	executed string
}

func newBUG024MacCleanupFixture(t *testing.T) *bug024MacCleanupFixture {
	t.Helper()
	if runtimeinfo.GOOS != "darwin" {
		t.Skip("BUG024 native Mac cleanup fixture requires Darwin")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := t.TempDir()
	root := t.TempDir()
	adapter, err := NewMacProcessAdapter(MacRuntimeOptions{Account: current.Username, WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-bug024-cleanup", "generation-bug024-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shell.Close() })
	identity, err := adapter.Inspect(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &bug024MacCleanupFixture{adapter: adapter, prepared: prepared, shell: shell, root: root, pid: identity.PID,
		release: filepath.Join(controlRoot, "release"), executed: filepath.Join(controlRoot, "executed")}
	t.Cleanup(func() {
		if err := os.WriteFile(fixture.release, nil, 0o600); err != nil {
			t.Errorf("release BUG024 task-owned descendant: %v", err)
		}
		if err := shell.Close(); err != nil {
			t.Errorf("close BUG024 task-owned Bash: %v", err)
		}
		if !waitProcessGroupGone(context.Background(), fixture.pid, 3*time.Second) {
			t.Errorf("BUG024 task-owned group %d remains after release", fixture.pid)
		}
	})
	// Exec an external child so Bash's saved redirection descriptors close on
	// exec, then explicitly close the private control FDs. A background Bash
	// subshell would inherit saved stdout/stderr descriptors and could keep
	// exec.Cmd.Wait's pipe drainers alive until its natural deadline instead of
	// exercising the post-reap process-group observation. The child holds no
	// output/control pipe and has its own finite natural bound.
	holder := "attempt=0; while [ ! -e \"$1\" ] && [ \"$attempt\" -lt 300 ]; do attempt=$((attempt+1)); /bin/sleep 0.01; done"
	script := "printf 'once\\n' >>" + shellQuote(fixture.executed) + "\n" +
		"/bin/sh -c " + shellQuote(holder) + " bug024-holder " + shellQuote(fixture.release) + " </dev/null >/dev/null 2>&1 3<&- 4>&- &\n" +
		"printf BUG024_COMMAND_SUCCEEDED\n"
	result, err := shell.RunScript(context.Background(), "command-bug024-cleanup", []byte(script))
	if err != nil || string(result.Stdout) != "BUG024_COMMAND_SUCCEEDED" || result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 0 {
		t.Fatalf("BUG024 fixture command result=%+v err=%v", result, err)
	}
	return fixture
}

func (f *bug024MacCleanupFixture) assertRetained(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.prepared.Workspace); err != nil {
		t.Fatalf("unconfirmed cleanup removed workspace: %v", err)
	}
	owner, err := readRuntimeOwnership(f.root, f.prepared.SessionID)
	if err != nil || owner.PID != f.pid || owner.Generation != f.prepared.Generation {
		t.Fatalf("unconfirmed cleanup changed ownership: %+v err=%v", owner, err)
	}
	f.assertExecutedOnce(t)
}

func (f *bug024MacCleanupFixture) assertExecutedOnce(t *testing.T) {
	t.Helper()
	bytes, err := os.ReadFile(f.executed)
	if err != nil || string(bytes) != "once\n" {
		t.Fatalf("command replay marker=%q err=%v", bytes, err)
	}
}

func bug024WaitRootReaped(pid int, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("owned Bash %d was not reaped before %s", pid, limit)
}
