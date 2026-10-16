// Package mailboxfence coordinates a cooperative, file-only mailbox ingress
// fence. It intentionally contains no Runner authority or request semantics.
package mailboxfence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// LockFileName serializes native marker-last publishers with a retirement
	// or migration fence. It is an owner-only, top-level coordination artifact.
	LockFileName = ".runner-mailbox-ingress.lock"
	// SentinelFileName remains after retirement so a native publisher refuses
	// to create a new pair in a retired mailbox tree.
	SentinelFileName = ".runner-mailbox-retired"
	// MigrationFenceFileName is a durable marker written by the lane migration
	// controller while it holds the exclusive ingress lock. Native publishers
	// refuse new pairs while this marker is present.
	MigrationFenceFileName = ".runner-mailbox-migration-fence"
)

var (
	ErrConfiguration             = errors.New("mailbox fence configuration is invalid")
	ErrUnsafe                    = errors.New("mailbox fence path is unsafe")
	ErrRetired                   = errors.New("mailbox ingress is retired")
	ErrMigrationFenced           = errors.New("mailbox ingress is migration-fenced")
	ErrBusy                      = errors.New("mailbox ingress fence is busy")
	ErrResidue                   = errors.New("mailbox ingress has retained artifacts")
	ErrSnapshotRequiresExclusive = errors.New("mailbox ingress snapshot requires the exclusive lock")
	// ErrResidueRestored reports that a fresh retirement fence found ingress
	// residue and was durably removed before releasing its exclusive lock.
	// Callers may safely restore the prior service only for this error.
	ErrResidueRestored = errors.New("mailbox ingress residue fence was restored")
)

const (
	directoryOpenFlags   = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	sharedAcquireTimeout = 5 * time.Second
)

// Lock holds either a shared publisher lock or an exclusive retirement lock.
// Close always releases the advisory lock. Only a fresh fence that finds
// ingress residue before durable retirement can be unwound during acquisition.
type Lock struct {
	rootFD    int
	lockFD    int
	exclusive bool
	closed    bool
}

// AcquireShared rejects a mailbox that has already been retired or
// migration-fenced, then holds a shared lock until Close. A caller must publish
// both members of its marker-last pair while it holds this lock. It bounds an
// otherwise unbounded background context so a stuck exclusive fence cannot
// strand a publisher.
func AcquireShared(ctx context.Context, root string) (*Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, sharedAcquireTimeout)
	defer cancel()
	rootFD, err := openDirectMailboxRoot(root)
	if err != nil {
		return nil, err
	}
	return acquireSharedAtRoot(ctx, rootFD)
}

// AcquireRetirement creates a persistent fence at an existing mailbox rooted
// below the filesystem root, makes its inbox and ACK directories non-writable,
// and rejects any retained ingress entry. It holds the exclusive lock until
// Close so a caller can revalidate durable state and record retirement without
// a cooperating publisher racing that boundary. A fresh fence that finds
// residue is durably unwound and returns ErrResidueRestored.
func AcquireRetirement(ctx context.Context, root string) (*Lock, error) {
	rootFD, err := openDirectMailboxRoot(root)
	if err != nil {
		return nil, err
	}
	return acquireRetirementAtRoot(ctx, rootFD)
}

// AcquireRetirementUnder is the descriptor-safe retirement variant used by
// tests with a private trust root. Production uses AcquireRetirement.
func AcquireRetirementUnder(ctx context.Context, trustRoot, root string) (*Lock, error) {
	rootFD, err := openMailboxRootUnder(trustRoot, root)
	if err != nil {
		return nil, err
	}
	return acquireRetirementAtRoot(ctx, rootFD)
}

// AcquireMigrationExclusive serializes a migration snapshot or fence update
// against native publishers. It does not write a marker or change ingress
// permissions; the caller owns those higher-level state transitions.
func AcquireMigrationExclusive(ctx context.Context, root string) (*Lock, error) {
	rootFD, err := openDirectMailboxRoot(root)
	if err != nil {
		return nil, err
	}
	lock, err := acquireExclusiveAtRoot(ctx, rootFD)
	if err != nil {
		return nil, err
	}
	retired, err := lock.retirementSentinelPresent()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if retired {
		_ = lock.Close()
		return nil, ErrRetired
	}
	return lock, nil
}

// AcquireMigrationExclusiveUnder is the descriptor-safe fixture variant of
// AcquireMigrationExclusive. Production callers use AcquireMigrationExclusive.
func AcquireMigrationExclusiveUnder(ctx context.Context, trustRoot, root string) (*Lock, error) {
	rootFD, err := openMailboxRootUnder(trustRoot, root)
	if err != nil {
		return nil, err
	}
	lock, err := acquireExclusiveAtRoot(ctx, rootFD)
	if err != nil {
		return nil, err
	}
	retired, err := lock.retirementSentinelPresent()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if retired {
		_ = lock.Close()
		return nil, ErrRetired
	}
	return lock, nil
}

// PublishReadyPair acquires the cooperative shared lock, checks both durable
// fences, and publishes one immutable JSON/empty-ready pair through descriptors
// rooted at the same mailbox directory as the lock.
func PublishReadyPair(ctx context.Context, root, directory, logicalID string, raw []byte) error {
	lock, err := AcquireShared(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	return lock.PublishReadyPair(directory, logicalID, raw)
}

// PublishReadyPair writes JSON, then its empty ready marker, then fsyncs the
// ingress directory while the shared lock remains held. The lock's root
// descriptor is also used to open the ingress directory, so path replacement
// cannot redirect publication to another tree.
func (l *Lock) PublishReadyPair(directory, logicalID string, raw []byte) error {
	return l.publishReadyPairWithOps(directory, logicalID, raw, nil)
}

type pairWriteOps struct {
	writeJSON   func(int, string, []byte) error
	beforeReady func() error
	writeReady  func(int, string, []byte) error
	syncDir     func(int) error
}

func (l *Lock) publishReadyPairWithOps(directory, logicalID string, raw []byte, injected *pairWriteOps) error {
	if l == nil || l.closed || l.rootFD < 0 || l.exclusive {
		return ErrConfiguration
	}
	if (directory != "inbox" && directory != "acks") || !validMailboxID(logicalID) || len(raw) == 0 || len(raw) > maxIngressJSONBytes || !json.Valid(raw) {
		return ErrConfiguration
	}
	directoryFD, err := unix.Openat(l.rootFD, directory, directoryOpenFlags, 0)
	if err != nil {
		return fmt.Errorf("%w: open ingress directory for publication", ErrUnsafe)
	}
	var directoryStat unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryStat); err != nil || !isOwnerDirectory(directoryStat, 0o700) {
		_ = unix.Close(directoryFD)
		return ErrUnsafe
	}
	ops := pairWriteOps{writeJSON: writeSyncedAt, writeReady: writeSyncedAt, syncDir: unix.Fsync}
	if injected != nil {
		if injected.writeJSON != nil {
			ops.writeJSON = injected.writeJSON
		}
		ops.beforeReady = injected.beforeReady
		if injected.writeReady != nil {
			ops.writeReady = injected.writeReady
		}
		if injected.syncDir != nil {
			ops.syncDir = injected.syncDir
		}
	}
	jsonName := logicalID + ".json"
	readyName := logicalID + ".ready"
	if writeErr := ops.writeJSON(directoryFD, jsonName, raw); writeErr != nil {
		err = fmt.Errorf("write immutable JSON: %w", writeErr)
	} else if ops.beforeReady != nil {
		err = ops.beforeReady()
		if err != nil {
			err = fmt.Errorf("before ready marker: %w", err)
		}
	}
	if err == nil {
		if writeErr := ops.writeReady(directoryFD, readyName, nil); writeErr != nil {
			err = fmt.Errorf("publish ready marker: %w", writeErr)
		}
	}
	if err == nil {
		if syncErr := ops.syncDir(directoryFD); syncErr != nil {
			err = fmt.Errorf("sync ingress directory: %w", syncErr)
		}
	}
	closeErr := unix.Close(directoryFD)
	if err == nil && closeErr != nil {
		err = fmt.Errorf("close ingress directory: %w", closeErr)
	}
	return err
}

func writeSyncedAt(directoryFD int, name string, data []byte) error {
	fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = unix.Unlinkat(directoryFD, name, 0)
		}
	}()
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = unix.Close(fd)
		return err
	}
	for len(data) > 0 {
		written, writeErr := unix.Write(fd, data)
		if writeErr != nil {
			_ = unix.Close(fd)
			return writeErr
		}
		if written == 0 {
			_ = unix.Close(fd)
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	if err := unix.Fsync(fd); err != nil {
		_ = unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return err
	}
	remove = false
	return nil
}

func acquireRetirementAtRoot(ctx context.Context, rootFD int) (*Lock, error) {
	lock, err := acquireExclusiveAtRoot(ctx, rootFD)
	if err != nil {
		return nil, err
	}
	migrationFenced, err := lock.migrationFencePresent()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if migrationFenced {
		_ = lock.Close()
		return nil, ErrMigrationFenced
	}
	sentinelCreated, err := lock.createRetirementSentinel()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	changedDirectories, err := lock.freezeAndScanIngress()
	if err != nil {
		if sentinelCreated && len(changedDirectories) == 2 && errors.Is(err, ErrResidue) {
			if rollbackErr := lock.restoreFreshResidueFence(changedDirectories); rollbackErr == nil {
				_ = lock.Close()
				return nil, fmt.Errorf("%w: %w", ErrResidueRestored, ErrResidue)
			} else {
				reassertErr := lock.reassertResidueFence()
				_ = lock.Close()
				if reassertErr != nil {
					return nil, fmt.Errorf("restore fresh mailbox residue fence: %w", errors.Join(rollbackErr, reassertErr))
				}
				return nil, fmt.Errorf("restore fresh mailbox residue fence after re-fencing ingress: %w", rollbackErr)
			}
		}
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func acquireSharedAtRoot(ctx context.Context, rootFD int) (*Lock, error) {
	lockFD, err := openFenceLock(rootFD)
	if err != nil {
		_ = unix.Close(rootFD)
		return nil, err
	}
	lock := &Lock{rootFD: rootFD, lockFD: lockFD}
	if err := lock.flock(ctx, unix.LOCK_SH); err != nil {
		_ = lock.Close()
		return nil, err
	}
	fenced, err := lock.retirementSentinelPresent()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if fenced {
		_ = lock.Close()
		return nil, ErrRetired
	}
	fenced, err = lock.migrationFencePresent()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if fenced {
		_ = lock.Close()
		return nil, ErrMigrationFenced
	}
	return lock, nil
}

func acquireExclusiveAtRoot(ctx context.Context, rootFD int) (*Lock, error) {
	lockFD, err := openFenceLock(rootFD)
	if err != nil {
		_ = unix.Close(rootFD)
		return nil, err
	}
	lock := &Lock{rootFD: rootFD, lockFD: lockFD, exclusive: true}
	if err := lock.flock(ctx, unix.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func (l *Lock) flock(ctx context.Context, operation int) error {
	if l == nil || l.lockFD < 0 {
		return ErrConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := unix.Flock(l.lockFD, operation|unix.LOCK_NB); err == nil {
			return nil
		} else if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("lock mailbox ingress: %w", err)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrBusy
			}
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *Lock) createRetirementSentinel() (bool, error) {
	if l == nil || l.rootFD < 0 {
		return false, ErrConfiguration
	}
	fd, err := unix.Openat(l.rootFD, SentinelFileName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		present, inspectErr := l.retirementSentinelPresent()
		if inspectErr != nil {
			return false, inspectErr
		}
		if present {
			return false, nil
		}
		return false, ErrUnsafe
	}
	if err != nil {
		return false, fmt.Errorf("create mailbox retirement sentinel: %w", err)
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = unix.Close(fd)
		return false, fmt.Errorf("restrict mailbox retirement sentinel: %w", err)
	}
	if err := unix.Fsync(fd); err != nil {
		_ = unix.Close(fd)
		return false, fmt.Errorf("sync mailbox retirement sentinel: %w", err)
	}
	if err := unix.Close(fd); err != nil {
		return false, fmt.Errorf("close mailbox retirement sentinel: %w", err)
	}
	if err := unix.Fsync(l.rootFD); err != nil {
		return false, fmt.Errorf("sync mailbox retirement root: %w", err)
	}
	return true, nil
}

func (l *Lock) retirementSentinelPresent() (bool, error) {
	if l == nil || l.rootFD < 0 {
		return false, ErrConfiguration
	}
	var stat unix.Stat_t
	err := unix.Fstatat(l.rootFD, SentinelFileName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil || !isOwnerFile(stat, 0o600) || stat.Size != 0 {
		return false, ErrUnsafe
	}
	return true, nil
}

func (l *Lock) migrationFencePresent() (bool, error) {
	if l == nil || l.rootFD < 0 {
		return false, ErrConfiguration
	}
	var stat unix.Stat_t
	err := unix.Fstatat(l.rootFD, MigrationFenceFileName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil || !isOwnerFile(stat, 0o600) {
		return false, ErrUnsafe
	}
	return true, nil
}

func (l *Lock) freezeAndScanIngress() ([]string, error) {
	if l == nil || l.rootFD < 0 {
		return nil, ErrConfiguration
	}
	fds := make([]int, 0, 2)
	changedDirectories := make([]string, 0, 2)
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	for _, name := range []string{"inbox", "acks"} {
		fd, err := unix.Openat(l.rootFD, name, directoryOpenFlags, 0)
		if err != nil {
			return changedDirectories, fmt.Errorf("%w: open retired mailbox %s", ErrUnsafe, name)
		}
		fds = append(fds, fd)
		changed, err := freezeIngressDirectory(fd)
		if changed {
			changedDirectories = append(changedDirectories, name)
		}
		if err != nil {
			return changedDirectories, err
		}
	}
	// Freeze both ingress directories before inspecting either one. A residue
	// failure must not leave one of the old writer entry points open.
	for _, fd := range fds {
		if err := scanFrozenIngressDirectory(fd); err != nil {
			return changedDirectories, err
		}
	}
	if err := unix.Fsync(l.rootFD); err != nil {
		return changedDirectories, fmt.Errorf("sync retired mailbox root: %w", err)
	}
	return changedDirectories, nil
}

// restoreFreshResidueFence undoes only a just-created fence after the empty
// ingress check finds residue. It runs while the caller still holds the
// exclusive flock. Any failure deliberately leaves the sentinel in place.
func (l *Lock) restoreFreshResidueFence(changedDirectories []string) error {
	if l == nil || l.rootFD < 0 {
		return ErrConfiguration
	}
	if len(changedDirectories) != 2 || changedDirectories[0] != "inbox" || changedDirectories[1] != "acks" {
		return ErrUnsafe
	}
	for _, name := range changedDirectories {
		fd, err := unix.Openat(l.rootFD, name, directoryOpenFlags, 0)
		if err != nil {
			return fmt.Errorf("%w: open fenced mailbox %s for residue restore", ErrUnsafe, name)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || !isOwnerDirectory(stat, 0o500) {
			_ = unix.Close(fd)
			return ErrUnsafe
		}
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("restore mailbox ingress directory after residue: %w", err)
		}
		if err := unix.Fsync(fd); err != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("sync restored mailbox ingress directory after residue: %w", err)
		}
		if err := unix.Fstat(fd, &stat); err != nil || !isOwnerDirectory(stat, 0o700) {
			_ = unix.Close(fd)
			return ErrUnsafe
		}
		if err := unix.Close(fd); err != nil {
			return fmt.Errorf("close restored mailbox ingress directory after residue: %w", err)
		}
	}
	if err := unix.Fsync(l.rootFD); err != nil {
		return fmt.Errorf("sync restored mailbox root after residue: %w", err)
	}
	present, err := l.retirementSentinelPresent()
	if err != nil {
		return err
	}
	if !present {
		return ErrUnsafe
	}
	if err := unix.Unlinkat(l.rootFD, SentinelFileName, 0); err != nil {
		return fmt.Errorf("remove mailbox retirement sentinel after residue: %w", err)
	}
	if err := unix.Fsync(l.rootFD); err != nil {
		return fmt.Errorf("sync mailbox retirement root after residue: %w", err)
	}
	return nil
}

// reassertResidueFence is best-effort repair after a fresh-residue rollback
// itself fails. It deliberately leaves the durable sentinel and both ingress
// directories frozen, so callers cannot mistake a partial rollback for the
// clean pre-boundary outcome.
func (l *Lock) reassertResidueFence() error {
	if l == nil || l.rootFD < 0 {
		return ErrConfiguration
	}
	if _, err := l.createRetirementSentinel(); err != nil {
		return err
	}
	for _, name := range []string{"inbox", "acks"} {
		fd, err := unix.Openat(l.rootFD, name, directoryOpenFlags, 0)
		if err != nil {
			return fmt.Errorf("%w: open mailbox %s to re-fence residue", ErrUnsafe, name)
		}
		_, freezeErr := freezeIngressDirectory(fd)
		closeErr := unix.Close(fd)
		if freezeErr != nil {
			return freezeErr
		}
		if closeErr != nil {
			return fmt.Errorf("close re-fenced mailbox ingress directory: %w", closeErr)
		}
	}
	if err := unix.Fsync(l.rootFD); err != nil {
		return fmt.Errorf("sync re-fenced mailbox root: %w", err)
	}
	return nil
}

func freezeIngressDirectory(fd int) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !isOwnerDirectory(stat, 0o700, 0o500) {
		return false, ErrUnsafe
	}
	if stat.Mode&0o7777 == 0o700 {
		if err := unix.Fchmod(fd, 0o500); err != nil {
			return false, fmt.Errorf("freeze retired mailbox ingress directory: %w", err)
		}
		if err := unix.Fsync(fd); err != nil {
			return true, fmt.Errorf("sync retired mailbox ingress directory: %w", err)
		}
		if err := unix.Fstat(fd, &stat); err != nil || !isOwnerDirectory(stat, 0o500) {
			return true, ErrUnsafe
		}
		return true, nil
	}
	return false, nil
}

func scanFrozenIngressDirectory(fd int) error {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return fmt.Errorf("duplicate retired mailbox ingress directory: %w", err)
	}
	directory := os.NewFile(uintptr(duplicate), "retired-mailbox-ingress")
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return fmt.Errorf("read retired mailbox ingress directory: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close retired mailbox ingress directory reader: %w", closeErr)
	}
	if len(entries) != 0 {
		return ErrResidue
	}
	return nil
}

func openFenceLock(rootFD int) (int, error) {
	fd, err := unix.Openat(rootFD, LockFileName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	readOnlyOpen := false
	if errors.Is(err, unix.EACCES) {
		// A prior restrictive umask can leave an owner-only lock unreadable for
		// O_RDWR. Normalize it through an owner descriptor, then reopen it for
		// flock. The no-follow open and post-open checks still reject aliases.
		fd, err = unix.Openat(rootFD, LockFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		readOnlyOpen = err == nil
	}
	if err != nil {
		return -1, fmt.Errorf("open mailbox ingress lock: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() {
		_ = unix.Close(fd)
		return -1, ErrUnsafe
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("restrict mailbox ingress lock: %w", err)
	}
	if err := unix.Fsync(fd); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("sync mailbox ingress lock: %w", err)
	}
	if readOnlyOpen {
		if err := unix.Close(fd); err != nil {
			return -1, fmt.Errorf("close normalized mailbox ingress lock: %w", err)
		}
		fd, err = unix.Openat(rootFD, LockFileName, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, fmt.Errorf("reopen normalized mailbox ingress lock: %w", err)
		}
	}
	if err := unix.Fstat(fd, &stat); err != nil || !isOwnerFile(stat, 0o600) {
		_ = unix.Close(fd)
		return -1, ErrUnsafe
	}
	return fd, nil
}

func openDirectMailboxRoot(root string) (int, error) {
	if !validAbsolutePath(root) {
		return -1, ErrConfiguration
	}
	fd, err := unix.Open(root, directoryOpenFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("open mailbox root: %w", err)
	}
	if err := validateMailboxRootFD(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func openMailboxRootUnder(trustRoot, root string) (int, error) {
	if !validAbsolutePath(trustRoot) || !validAbsolutePath(root) {
		return -1, ErrConfiguration
	}
	relative, err := filepath.Rel(trustRoot, root)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return -1, ErrConfiguration
	}
	fd, err := unix.Open(trustRoot, directoryOpenFlags, 0)
	if err != nil {
		return -1, fmt.Errorf("open mailbox trust root: %w", err)
	}
	if err := validateMailboxAncestorFD(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			_ = unix.Close(fd)
			return -1, ErrConfiguration
		}
		next, openErr := unix.Openat(fd, part, directoryOpenFlags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, fmt.Errorf("open mailbox path component: %w", openErr)
		}
		if index == len(parts)-1 {
			err = validateMailboxRootFD(next)
		} else {
			err = validateMailboxAncestorFD(next)
		}
		if err != nil {
			_ = unix.Close(next)
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

func validAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator) && !strings.ContainsRune(path, 0)
}

func validateMailboxAncestorFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o022 != 0 {
		return ErrUnsafe
	}
	return nil
}

func validateMailboxRootFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !isOwnerDirectory(stat, 0o700) {
		return ErrUnsafe
	}
	return nil
}

func isOwnerDirectory(stat unix.Stat_t, modes ...uint32) bool {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() {
		return false
	}
	for _, mode := range modes {
		if uint32(stat.Mode)&0o7777 == mode {
			return true
		}
	}
	return false
}

func isOwnerFile(stat unix.Stat_t, mode uint32) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && int(stat.Uid) == os.Geteuid() && uint32(stat.Mode)&0o7777 == mode
}

// Close releases the advisory lock and all descriptors. It does not remove a
// sentinel or restore ingress directory write permissions.
func (l *Lock) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	var result error
	if l.lockFD >= 0 {
		if err := unix.Flock(l.lockFD, unix.LOCK_UN); err != nil {
			result = fmt.Errorf("unlock mailbox ingress: %w", err)
		}
		if err := unix.Close(l.lockFD); err != nil && result == nil {
			result = fmt.Errorf("close mailbox ingress lock: %w", err)
		}
	}
	if l.rootFD >= 0 {
		if err := unix.Close(l.rootFD); err != nil && result == nil {
			result = fmt.Errorf("close mailbox root: %w", err)
		}
	}
	l.rootFD, l.lockFD = -1, -1
	return result
}
