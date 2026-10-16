package mailboxfence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const maxIngressJSONBytes = 1 << 20

// FileIdentity is the identity and basic shape of one object observed through
// an already-open mailbox descriptor.
type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

// ReadyPairSnapshot records both members of one immutable request or ACK pair.
// Paths are root-relative names derived from the opened ingress directory;
// the identities and digests bind the observed bytes to the descriptors.
type ReadyPairSnapshot struct {
	Kind        string       `json:"kind"`
	InboxID     string       `json:"inbox_id"`
	LogicalID   string       `json:"logical_id"`
	JSONPath    string       `json:"json_path"`
	ReadyPath   string       `json:"ready_path"`
	Root        FileIdentity `json:"root"`
	Directory   FileIdentity `json:"directory"`
	JSON        FileIdentity `json:"json"`
	Ready       FileIdentity `json:"ready"`
	JSONSHA256  string       `json:"json_sha256"`
	ReadySHA256 string       `json:"ready_sha256"`
}

// IngressSnapshot is the stable, exclusive-lock view of both request and ACK
// ready-pair directories for one inbox.
type IngressSnapshot struct {
	InboxID string              `json:"inbox_id"`
	Root    FileIdentity        `json:"root"`
	Pairs   []ReadyPairSnapshot `json:"pairs"`
}

// SnapshotIngressPairs captures every complete request and ACK pair while the
// caller holds the exclusive migration lock. Incomplete pairs, unexpected
// entries, aliases, replaced names, and files outside the owner-only contract
// fail closed. It does not delete, repair, execute, or ACK retained work.
func (l *Lock) SnapshotIngressPairs(inboxID string) (IngressSnapshot, error) {
	if l == nil || l.closed || l.rootFD < 0 {
		return IngressSnapshot{}, ErrConfiguration
	}
	if !l.exclusive {
		return IngressSnapshot{}, ErrSnapshotRequiresExclusive
	}
	if !validMailboxID(inboxID) {
		return IngressSnapshot{}, ErrConfiguration
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(l.rootFD, &rootStat); err != nil || !isOwnerDirectory(rootStat, 0o700) {
		return IngressSnapshot{}, ErrUnsafe
	}
	rootIdentity := identityFromStat(rootStat)
	snapshot := IngressSnapshot{InboxID: inboxID, Root: rootIdentity, Pairs: make([]ReadyPairSnapshot, 0)}
	for _, item := range []struct {
		kind string
		name string
	}{
		{kind: "request", name: "inbox"},
		{kind: "ack", name: "acks"},
	} {
		directoryFD, err := unix.Openat(l.rootFD, item.name, directoryOpenFlags, 0)
		if err != nil {
			return IngressSnapshot{}, fmt.Errorf("%w: open %s for snapshot", ErrUnsafe, item.name)
		}
		var directoryStat unix.Stat_t
		if err := unix.Fstat(directoryFD, &directoryStat); err != nil || !isOwnerDirectory(directoryStat, 0o700) {
			_ = unix.Close(directoryFD)
			return IngressSnapshot{}, ErrUnsafe
		}
		pairs, snapshotErr := snapshotDirectory(directoryFD, item.kind, item.name, inboxID, rootIdentity, identityFromStat(directoryStat))
		closeErr := unix.Close(directoryFD)
		if snapshotErr != nil {
			return IngressSnapshot{}, snapshotErr
		}
		if closeErr != nil {
			return IngressSnapshot{}, fmt.Errorf("close %s snapshot directory: %w", item.name, closeErr)
		}
		snapshot.Pairs = append(snapshot.Pairs, pairs...)
	}
	sort.Slice(snapshot.Pairs, func(i, j int) bool {
		if snapshot.Pairs[i].Kind != snapshot.Pairs[j].Kind {
			return snapshot.Pairs[i].Kind < snapshot.Pairs[j].Kind
		}
		return snapshot.Pairs[i].LogicalID < snapshot.Pairs[j].LogicalID
	})
	return snapshot, nil
}

// RequireEmpty fails when an exclusive snapshot contains any retained pair.
// Callers must preserve the snapshot and obtain a supported disposition before
// continuing a migration.
func (s IngressSnapshot) RequireEmpty() error {
	if len(s.Pairs) != 0 {
		return ErrResidue
	}
	return nil
}

type pairMembers struct {
	json  bool
	ready bool
}

func snapshotDirectory(directoryFD int, kind, directoryName, inboxID string, rootIdentity, directoryIdentity FileIdentity) ([]ReadyPairSnapshot, error) {
	duplicateFD, err := unix.Dup(directoryFD)
	if err != nil {
		return nil, fmt.Errorf("duplicate ingress snapshot descriptor: %w", err)
	}
	directory := os.NewFile(uintptr(duplicateFD), directoryName)
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read %s snapshot entries: %w", directoryName, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %s snapshot entries: %w", directoryName, closeErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	members := make(map[string]pairMembers, len(entries)/2)
	for _, entry := range entries {
		name := entry.Name()
		suffix := ""
		switch {
		case strings.HasSuffix(name, ".json"):
			suffix = ".json"
		case strings.HasSuffix(name, ".ready"):
			suffix = ".ready"
		default:
			return nil, fmt.Errorf("%w: unexpected %s ingress entry", ErrResidue, directoryName)
		}
		logicalID := strings.TrimSuffix(name, suffix)
		if !validMailboxID(logicalID) {
			return nil, fmt.Errorf("%w: invalid ingress pair name", ErrResidue)
		}
		current := members[logicalID]
		if suffix == ".json" {
			if current.json {
				return nil, ErrUnsafe
			}
			current.json = true
		} else {
			if current.ready {
				return nil, ErrUnsafe
			}
			current.ready = true
		}
		members[logicalID] = current
	}
	logicalIDs := make([]string, 0, len(members))
	for logicalID, member := range members {
		if !member.json || !member.ready {
			return nil, fmt.Errorf("%w: incomplete %s pair %s", ErrResidue, kind, logicalID)
		}
		logicalIDs = append(logicalIDs, logicalID)
	}
	sort.Strings(logicalIDs)
	pairs := make([]ReadyPairSnapshot, 0, len(logicalIDs))
	for _, logicalID := range logicalIDs {
		jsonName := logicalID + ".json"
		readyName := logicalID + ".ready"
		jsonIdentity, jsonDigest, err := readSnapshotFileAt(directoryFD, jsonName, maxIngressJSONBytes, false)
		if err != nil {
			return nil, err
		}
		readyIdentity, readyDigest, err := readSnapshotFileAt(directoryFD, readyName, 0, true)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, ReadyPairSnapshot{
			Kind: kind, InboxID: inboxID, LogicalID: logicalID,
			JSONPath: directoryName + "/" + jsonName, ReadyPath: directoryName + "/" + readyName,
			Root: rootIdentity, Directory: directoryIdentity, JSON: jsonIdentity, Ready: readyIdentity,
			JSONSHA256: jsonDigest, ReadySHA256: readyDigest,
		})
	}
	return pairs, nil
}

func readSnapshotFileAt(directoryFD int, name string, maxBytes int64, requireEmpty bool) (FileIdentity, string, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return FileIdentity{}, "", fmt.Errorf("%w: open ingress snapshot member", ErrUnsafe)
	}
	file := os.NewFile(uintptr(fd), name)
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || !isOwnerFile(before, 0o600) || before.Size < 0 || before.Size > maxBytes || (requireEmpty && before.Size != 0) {
		_ = file.Close()
		if errors.Is(err, unix.EBADF) {
			return FileIdentity{}, "", ErrUnsafe
		}
		return FileIdentity{}, "", ErrUnsafe
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	var after unix.Stat_t
	statErr := unix.Fstat(fd, &after)
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || int64(len(data)) != before.Size || int64(len(data)) > maxBytes || !sameFileIdentity(before, after) {
		return FileIdentity{}, "", fmt.Errorf("%w: ingress member changed while snapshotting", ErrUnsafe)
	}
	var current unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameFileIdentity(after, current) {
		return FileIdentity{}, "", fmt.Errorf("%w: ingress member name changed while snapshotting", ErrUnsafe)
	}
	digest := sha256.Sum256(data)
	return identityFromStat(after), hex.EncodeToString(digest[:]), nil
}

func identityFromStat(stat unix.Stat_t) FileIdentity {
	return FileIdentity{
		Device: uint64(stat.Dev), Inode: uint64(stat.Ino), UID: uint32(stat.Uid),
		Mode: uint32(stat.Mode), Size: stat.Size,
	}
}

func sameFileIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid && a.Mode == b.Mode && a.Size == b.Size
}

func validMailboxID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (index > 0 && (r == '.' || r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}
