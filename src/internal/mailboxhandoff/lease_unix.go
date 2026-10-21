//go:build darwin || linux

package mailboxhandoff

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

type ownedRoot struct {
	mailbox               Mailbox
	rootFD                int
	lockFD                int
	lockDevice, lockInode uint64
	blockedIDs            map[string]bool
	blockedKeys           map[string]bool
}

type Lease struct {
	mu                         sync.Mutex
	receiptPath, receiptSHA256 string
	receipt                    Receipt
	receiptBuffer              [32 << 10]byte
	roots                      map[string]*ownedRoot
	definitions                map[string]bool
	closed                     bool
}

// LoadReceipt reads a regular owner-only file through no-follow descriptors.
// Only its exact target staging path is accepted. Inactive receipts can be read
// by an operator; Acquire additionally requires phase active.
func LoadReceipt(path string) (Receipt, string, error) {
	var receipt Receipt
	raw, err := readPrivatePath(path, 4<<20)
	if err != nil {
		return receipt, "", err
	}
	if err := decodeStrict(raw, &receipt); err != nil || ValidateReceipt(receipt) != nil || ReceiptPath(receipt) != path {
		return Receipt{}, "", ErrInvalid
	}
	serviceFD, err := openPrivateDirectory(receipt.TargetServiceRoot)
	if err != nil {
		return Receipt{}, "", err
	}
	_ = unix.Close(serviceFD)
	return receipt, SHA256(raw), nil
}

// Acquire owns all transferred roots for one ingress process. Its lock is
// independent of the shared publisher lock, so normal clients remain usable.
func Acquire(receiptPath, lane, serviceRoot, configSHA string, definitions []Definition) (*Lease, error) {
	r, digest, err := LoadReceipt(receiptPath)
	if err != nil {
		return nil, err
	}
	if lane != "qas" || lane != r.TargetLane || serviceRoot != r.TargetServiceRoot || configSHA != r.TargetConfigSHA256 || r.Phase != "active" {
		return nil, ErrOwnership
	}
	l := &Lease{receiptPath: receiptPath, receiptSHA256: digest, receipt: r, roots: map[string]*ownedRoot{}, definitions: map[string]bool{}}
	valid := false
	defer func() {
		if !valid {
			_ = l.Close()
		}
	}()
	byID := map[string]Mailbox{}
	for _, m := range r.Mailboxes {
		byID[m.TargetID] = m
	}
	paths := map[string]bool{}
	for _, d := range definitions {
		if !namePattern.MatchString(d.ID) || !cleanAbsolute(d.Root) || l.definitions[d.ID] || paths[d.Root] {
			return nil, ErrInvalid
		}
		l.definitions[d.ID], paths[d.Root] = true, true
		m, transferred := byID[d.ID]
		if nested(serviceRoot, d.Root) {
			if transferred {
				return nil, ErrOwnership
			}
			continue
		}
		if !transferred || m.Root != d.Root {
			return nil, ErrOwnership
		}
	}
	for _, m := range r.Mailboxes {
		if !l.definitions[m.TargetID] {
			return nil, ErrOwnership
		}
	}
	mailboxes := append([]Mailbox(nil), r.Mailboxes...)
	sort.Slice(mailboxes, func(i, j int) bool { return mailboxes[i].Root < mailboxes[j].Root })
	for _, m := range mailboxes {
		rootFD, err := openPrivateDirectory(m.Root)
		if err != nil {
			return nil, err
		}
		owned := &ownedRoot{mailbox: m, rootFD: rootFD, lockFD: -1, blockedIDs: map[string]bool{}, blockedKeys: map[string]bool{}}
		l.roots[m.TargetID] = owned
		if err := rootMatches(rootFD, m); err != nil {
			return nil, err
		}
		lockFD, dev, ino, err := acquireOwnerLock(rootFD)
		if err != nil {
			return nil, err
		}
		owned.lockFD, owned.lockDevice, owned.lockInode = lockFD, dev, ino
		for _, id := range m.BlockedRequestIDs {
			owned.blockedIDs[id] = true
		}
		for _, key := range m.BlockedIdempotencySHA256 {
			owned.blockedKeys[key] = true
		}
		if err := l.checkRootLocked(owned); err != nil {
			return nil, err
		}
	}
	valid = true
	return l, nil
}

func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var closeErr error
	for _, root := range l.roots {
		if root.lockFD >= 0 {
			closeErr = errors.Join(closeErr, unix.Flock(root.lockFD, unix.LOCK_UN), unix.Close(root.lockFD))
			root.lockFD = -1
		}
		if root.rootFD >= 0 {
			closeErr = errors.Join(closeErr, unix.Close(root.rootFD))
			root.rootFD = -1
		}
	}
	return closeErr
}

// Check validates current ownership before import, reconciliation or cleanup.
func (l *Lease) Check(mailboxID string) error { return l.CheckRequest(mailboxID, "", "") }

// Covers reports whether this receipt transfers the given mailbox. Native
// lane-local definitions remain governed by their existing durable registry.
func (l *Lease) Covers(mailboxID string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.closed && l.roots[mailboxID] != nil
}

// CheckRequest fences historical request IDs and SHA-256 identities of raw
// idempotency keys. Empty identities request only an ownership check.
func (l *Lease) CheckRequest(mailboxID, requestID, idempotencyKey string) error {
	if l == nil {
		return ErrClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	root, err := l.checkLocked(mailboxID)
	if err != nil || root == nil {
		return err
	}
	if requestID != "" && root.blockedIDs[requestID] || idempotencyKey != "" && root.blockedKeys[SHA256([]byte(idempotencyKey))] {
		return ErrReplay
	}
	return nil
}

func (l *Lease) CheckACK(mailboxID, requestID string) error {
	return l.CheckRequest(mailboxID, requestID, "")
}

// ResolveEnvironment changes only explicitly declared aliases for transferred
// mailboxes. The caller must still resolve the resulting pair through its
// validated target-lane policy, retaining the immutable source request bytes.
func (l *Lease) ResolveEnvironment(mailboxID, environment string) (string, error) {
	if l == nil {
		return "", ErrClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	root, err := l.checkLocked(mailboxID)
	if err != nil {
		return "", err
	}
	if root != nil {
		if alias, ok := l.receipt.EnvironmentAliases[environment]; ok {
			return alias, nil
		}
	}
	return environment, nil
}

func (l *Lease) checkLocked(mailboxID string) (*ownedRoot, error) {
	if l.closed {
		return nil, ErrClosed
	}
	if !l.definitions[mailboxID] {
		return nil, ErrOwnership
	}
	root := l.roots[mailboxID]
	if root == nil {
		return nil, nil
	}
	if err := l.checkRootLocked(root); err != nil {
		return nil, err
	}
	return root, nil
}

func (l *Lease) checkRootLocked(root *ownedRoot) error {
	// Acquire fully validates the receipt and pins its exact bytes. Repeating
	// JSON decoding and history validation here scales every idle ownership
	// check with all preserved request IDs and keys. Keep the same guarded
	// file read and compare its digest instead; unchanged bytes retain the
	// already-validated meaning without rebuilding that history.
	digest, err := hashPrivatePath(l.receiptPath, 4<<20, l.receiptBuffer[:])
	if err != nil || digest != l.receiptSHA256 {
		return ErrOwnership
	}
	// LoadReceipt also requires this ancestor to remain a private service
	// root, beyond the safe-ancestor checks in readPrivatePath.
	serviceFD, err := openPrivateDirectory(l.receipt.TargetServiceRoot)
	if err != nil {
		return ErrOwnership
	}
	_ = unix.Close(serviceFD)
	currentFD, err := openPrivateDirectory(root.mailbox.Root)
	if err != nil {
		return ErrOwnership
	}
	defer unix.Close(currentFD)
	if rootMatches(currentFD, root.mailbox) != nil || rootMatches(root.rootFD, root.mailbox) != nil {
		return ErrOwnership
	}
	var marker Marker
	raw, err := readPrivateAt(currentFD, MarkerFileName, 4096)
	if err != nil || decodeStrict(raw, &marker) != nil || marker.OperationID != l.receipt.OperationID || marker.OwnerLane != l.receipt.TargetLane ||
		marker.ReceiptSHA256 != l.receiptSHA256 || marker.Device != root.mailbox.Device || marker.Inode != root.mailbox.Inode {
		return ErrOwnership
	}
	var lockStat unix.Stat_t
	if unix.Fstatat(currentFD, LockFileName, &lockStat, unix.AT_SYMLINK_NOFOLLOW) != nil || !privateRegular(lockStat) || uint64(lockStat.Dev) != root.lockDevice || uint64(lockStat.Ino) != root.lockInode {
		return ErrOwnership
	}
	return nil
}

// MarkerExists distinguishes absent metadata from unsafe or unreadable metadata;
// callers must fail closed for the latter before opening execution authority.
func MarkerExists(root string) (bool, error) {
	fd, err := openPrivateDirectory(root)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	raw, err := readPrivateAt(fd, MarkerFileName, 4096)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var marker Marker
	if decodeStrict(raw, &marker) != nil || validateMarker(marker) != nil {
		return false, ErrInvalid
	}
	return true, nil
}

// WriteMarker performs an atomic, synced owner-only update only while no runtime
// holds this mailbox. It never changes publisher fencing or directory contents.
func WriteMarker(root string, marker Marker) error {
	if err := validateMarker(marker); err != nil {
		return err
	}
	fd, err := openPrivateDirectory(root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if rootMatches(fd, Mailbox{Device: marker.Device, Inode: marker.Inode}) != nil {
		return ErrOwnership
	}
	lock, _, _, err := acquireOwnerLock(fd)
	if err != nil {
		return err
	}
	defer unix.Close(lock)
	defer unix.Flock(lock, unix.LOCK_UN)
	if _, err := readPrivateAt(fd, MarkerFileName, 4096); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return ErrInvalid
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrUnsafe
	}
	temporary := ".runner-mailbox-owner-" + hex.EncodeToString(nonce[:]) + ".tmp"
	tempFD, err := unix.Openat(fd, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return ErrUnsafe
	}
	defer unix.Unlinkat(fd, temporary, 0)
	file := os.NewFile(uintptr(tempFD), temporary)
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return ErrUnsafe
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrUnsafe
	}
	if err := file.Close(); err != nil {
		return ErrUnsafe
	}
	if err := unix.Renameat(fd, temporary, fd, MarkerFileName); err != nil {
		return ErrUnsafe
	}
	if err := unix.Fsync(fd); err != nil {
		return ErrUnsafe
	}
	return nil
}

// RemoveMarker completes a verified return after the coordinator has restored
// source directories and both original configurations. Only an exact "none"
// transition marker can be removed, with no active target runtime owner. This
// never removes publisher, migration or retirement fences.
func RemoveMarker(root string, expected Marker) error {
	if validateMarker(expected) != nil || expected.OwnerLane != "none" {
		return ErrInvalid
	}
	fd, err := openPrivateDirectory(root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if rootMatches(fd, Mailbox{Device: expected.Device, Inode: expected.Inode}) != nil {
		return ErrOwnership
	}
	lock, _, _, err := acquireOwnerLock(fd)
	if err != nil {
		return err
	}
	defer unix.Close(lock)
	defer unix.Flock(lock, unix.LOCK_UN)
	raw, err := readPrivateAt(fd, MarkerFileName, 4096)
	var actual Marker
	if err != nil || decodeStrict(raw, &actual) != nil || actual != expected {
		return ErrOwnership
	}
	if err := unix.Unlinkat(fd, MarkerFileName, 0); err != nil {
		return ErrUnsafe
	}
	if err := unix.Fsync(fd); err != nil {
		return ErrUnsafe
	}
	return nil
}

func validateMarker(marker Marker) error {
	if !operationPattern.MatchString(marker.OperationID) || (marker.OwnerLane != "qas" && marker.OwnerLane != "prd" && marker.OwnerLane != "none") || !digestPattern.MatchString(marker.ReceiptSHA256) || marker.Inode == 0 {
		return ErrInvalid
	}
	return nil
}

func acquireOwnerLock(rootFD int) (int, uint64, uint64, error) {
	fd, err := unix.Openat(rootFD, LockFileName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return -1, 0, 0, ErrUnsafe
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !privateRegular(stat) {
		_ = unix.Close(fd)
		return -1, 0, 0, ErrUnsafe
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		return -1, 0, 0, ErrBusy
	}
	var current unix.Stat_t
	if unix.Fstatat(rootFD, LockFileName, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameFile(stat, current) {
		_ = unix.Close(fd)
		return -1, 0, 0, ErrUnsafe
	}
	return fd, uint64(stat.Dev), uint64(stat.Ino), nil
}

func rootMatches(fd int, m Mailbox) error {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !privateDirectory(stat) || uint64(stat.Dev) != m.Device || uint64(stat.Ino) != m.Inode {
		return ErrOwnership
	}
	return nil
}

func readPrivatePath(path string, maximum int64) ([]byte, error) {
	if !cleanAbsolute(path) {
		return nil, ErrUnsafe
	}
	fd, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return readPrivateAt(fd, filepath.Base(path), maximum)
}

func readPrivateAt(parent int, name string, maximum int64) ([]byte, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, unix.ENOENT
		}
		return nil, ErrUnsafe
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after, current unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !privateRegular(before) || before.Size < 1 || before.Size > maximum {
		return nil, ErrUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum || int64(len(raw)) != before.Size || unix.Fstat(fd, &after) != nil || !sameFile(before, after) || before.Size != after.Size ||
		unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameFile(after, current) || !privateRegular(current) {
		return nil, ErrUnsafe
	}
	return raw, nil
}

// hashPrivatePath keeps the receipt's no-follow, ownership, size and identity
// checks without allocating the entire immutable history on every guard call.
// Lease callers serialize reuse of the fixed buffer with their mutex.
func hashPrivatePath(path string, maximum int64, buffer []byte) (string, error) {
	if !cleanAbsolute(path) || len(buffer) == 0 {
		return "", ErrUnsafe
	}
	parent, err := openPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer unix.Close(parent)
	name := filepath.Base(path)
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", ErrUnsafe
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after, current unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !privateRegular(before) || before.Size < 1 || before.Size > maximum {
		return "", ErrUnsafe
	}
	digest := sha256.New()
	count, err := io.CopyBuffer(digest, io.LimitReader(file, maximum+1), buffer)
	if err != nil || count != before.Size || unix.Fstat(fd, &after) != nil || !sameFile(before, after) || before.Size != after.Size ||
		unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameFile(after, current) || !privateRegular(current) {
		return "", ErrUnsafe
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Every path component is pinned with O_NOFOLLOW. Ancestors may be root-owned
// or account-owned, without writable group/other bits; root-owned sticky temp
// ancestors are allowed for private test roots. The final directory is 0700.
func openPrivateDirectory(path string) (int, error) {
	if !cleanAbsolute(path) {
		return -1, ErrUnsafe
	}
	fd, err := unix.Open(string(filepath.Separator), directoryFlags, 0)
	if err != nil {
		return -1, ErrUnsafe
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		var parent unix.Stat_t
		if unix.Fstat(fd, &parent) != nil || !safeAncestor(parent) {
			_ = unix.Close(fd)
			return -1, ErrUnsafe
		}
		next, err := unix.Openat(fd, part, directoryFlags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, errors.Join(ErrUnsafe, err)
		}
		fd = next
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || (i == len(parts)-1 && !privateDirectory(stat)) {
			_ = unix.Close(fd)
			return -1, ErrUnsafe
		}
	}
	return fd, nil
}

func privateDirectory(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o777 == 0o700 && stat.Uid == uint32(os.Geteuid())
}
func privateRegular(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o777 == 0o600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
func safeAncestor(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())) &&
		(stat.Mode&0o022 == 0 || stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0)
}
func sameFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Nlink == b.Nlink
}
func decodeStrict(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
