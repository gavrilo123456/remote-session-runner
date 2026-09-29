package unixsocket

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func shortSocketDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "us-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestRemoveStaleOwnedRemovesOnlyDisconnectedSocket(t *testing.T) {
	root := shortSocketDir(t)
	path := filepath.Join(root, "stale.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	unixListener := listener.(*net.UnixListener)
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStaleOwned(path); err != nil {
		t.Fatalf("remove stale socket: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket remains: %v", err)
	}
}

func TestRemoveStaleOwnedPreservesActiveAndNonSocketPaths(t *testing.T) {
	root := shortSocketDir(t)
	activePath := filepath.Join(root, "active.sock")
	listener, err := net.Listen("unix", activePath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := RemoveStaleOwned(activePath); !errors.Is(err, ErrSocketPathOccupied) {
		t.Fatalf("active socket error = %v, want occupied", err)
	}
	if _, err := os.Lstat(activePath); err != nil {
		t.Fatalf("active socket was removed: %v", err)
	}

	regularPath := filepath.Join(root, "not-a-socket")
	if err := os.WriteFile(regularPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStaleOwned(regularPath); !errors.Is(err, ErrSocketPathOccupied) {
		t.Fatalf("regular path error = %v, want occupied", err)
	}
	contents, err := os.ReadFile(regularPath)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("regular path changed: contents=%q err=%v", contents, err)
	}
}
