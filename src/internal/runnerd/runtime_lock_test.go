package runnerd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunnerdLifecycleLockIsExclusiveAndRetryable(t *testing.T) {
	root := t.TempDir()
	run := filepath.Join(root, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunnerdLifecycleLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRunnerdLifecycleLock(root); !errors.Is(err, ErrRunnerdLifecycleLockHeld) {
		t.Fatalf("second lifecycle lock error=%v, want held", err)
	}
	release()
	retryRelease, err := acquireRunnerdLifecycleLock(root)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	retryRelease()
}

func TestRunnerdLifecycleLockRefusesUnsafePath(t *testing.T) {
	root := t.TempDir()
	run := filepath.Join(root, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(run, "runnerd-lifecycle.lock")
	if err := os.WriteFile(lock, []byte("unsafe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRunnerdLifecycleLock(root); err == nil {
		t.Fatal("unsafe lifecycle lock unexpectedly accepted")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRunnerdLifecycleLock(root); err == nil {
		t.Fatal("symlinked lifecycle lock unexpectedly accepted")
	}
}
