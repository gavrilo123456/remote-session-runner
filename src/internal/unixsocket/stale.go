// Package unixsocket contains safe owner-only Unix-socket restart helpers.
package unixsocket

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var ErrSocketPathOccupied = errors.New("Unix socket path is occupied")

// RemoveStaleOwned removes only a stale Unix socket owned by the current
// account. A live listener, a non-socket, a replaced path, or another user's
// socket is left intact and reported as occupied.
func RemoveStaleOwned(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: path must be absolute", ErrSocketPathOccupied)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect: %v", ErrSocketPathOccupied, err)
	}
	if info.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: path is not an owned Unix socket", ErrSocketPathOccupied)
	}

	connection, dialErr := net.DialTimeout("unix", path, 150*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: a listener is active", ErrSocketPathOccupied)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
		return fmt.Errorf("%w: probe failed: %v", ErrSocketPathOccupied, dialErr)
	}

	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("%w: path changed during stale check", ErrSocketPathOccupied)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("%w: remove stale socket: %v", ErrSocketPathOccupied, err)
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
