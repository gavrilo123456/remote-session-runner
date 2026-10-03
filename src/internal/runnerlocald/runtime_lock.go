package runnerlocald

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const runnerLocaldLifecycleLockExit = 78

var ErrRunnerLocaldLifecycleLockHeld = errors.New("runner-locald lifecycle lock is held")

// acquireRunnerLocaldLifecycleLock serializes the offline lost-runtime repair
// with normal runner-locald startup. The lock is deliberately nonblocking: a
// launchd restart while repair is in progress exits with 78 and must be
// retried only after the repair's postflight succeeds.
func acquireRunnerLocaldLifecycleLock(serviceRoot string) (func(), error) {
	runDirectory := filepath.Join(serviceRoot, "run")
	if err := validateRunnerLocaldLifecycleLockDirectory(runDirectory); err != nil {
		return nil, err
	}
	path := filepath.Join(runDirectory, "runner-locald-lifecycle.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runner-locald lifecycle lock: %w", err)
	}
	closeFD := func() { _ = unix.Close(fd) }
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		closeFD()
		return nil, fmt.Errorf("inspect runner-locald lifecycle lock: %w", err)
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Uid != uint32(os.Getuid()) || opened.Mode&0o777 != 0o600 {
		closeFD()
		return nil, fmt.Errorf("runner-locald lifecycle lock must be a current-user mode-0600 regular file")
	}
	var named unix.Stat_t
	if err := unix.Lstat(path, &named); err != nil {
		closeFD()
		return nil, fmt.Errorf("reinspect runner-locald lifecycle lock: %w", err)
	}
	if named.Mode&unix.S_IFMT != unix.S_IFREG || named.Dev != opened.Dev || named.Ino != opened.Ino {
		closeFD()
		return nil, fmt.Errorf("runner-locald lifecycle lock changed while opening")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeFD()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrRunnerLocaldLifecycleLockHeld
		}
		return nil, fmt.Errorf("lock runner-locald lifecycle: %w", err)
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		closeFD()
	}, nil
}

func validateRunnerLocaldLifecycleLockDirectory(path string) error {
	var info unix.Stat_t
	if err := unix.Lstat(path, &info); err != nil {
		return fmt.Errorf("inspect runner-locald run directory: %w", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != uint32(os.Getuid()) || info.Mode&0o077 != 0 {
		return fmt.Errorf("runner-locald run directory must be a current-user owner-only directory")
	}
	return nil
}
