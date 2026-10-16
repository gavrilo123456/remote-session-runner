// Package laneroot validates external mailbox root handoffs without creating,
// repairing, reading, or deleting external-root contents.
package laneroot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

var (
	// ErrInvalidHandoff means a path, descriptor snapshot, layout, or volume
	// attestation does not match the fixed lane root contract.
	ErrInvalidHandoff = errors.New("invalid external mailbox root handoff")
	rootIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	volumeUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const (
	LayoutInitializedEmpty = "initialized_empty"
	LayoutRetained         = "retained"
	EntryRegularFile       = "regular_file"
	EntryDirectory         = "directory"
)

var mailboxChildren = []string{"inbox", "outbox", "events", "acks", "diagnostics"}
var directoryOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// FileIdentity is the descriptor-derived (st_dev, st_ino) pair used by the
// lane registry to detect aliases and same-path replacement.
type FileIdentity struct {
	Device uint64
	Inode  uint64
}

// AncillaryEntrySnapshot records only the top-level identity and safe
// metadata of a retained opaque entry. The validator never traverses or reads
// a retained directory's contents.
type AncillaryEntrySnapshot struct {
	Name     string
	Kind     string
	Identity FileIdentity
	OwnerUID uint32
	Mode     uint32
}

// VolumeIdentity is the expected or freshly observed identity of a quota-bound
// Mac APFS volume. Observed facts must be supplied by the host inventory; this
// package compares them and binds the device to the opened root descriptor.
type VolumeIdentity struct {
	Mountpoint string
	UUID       string
	Filesystem string
	Device     uint64
	QuotaBytes int64
}

// VolumeHandoff carries the approved storage identity and a separate fresh
// observation for the same root.
type VolumeHandoff struct {
	Expected VolumeIdentity
	Observed VolumeIdentity
}

// RootHandoff is the fixed inventory for one external mailbox root. Expected
// root and child identities are snapshots produced by a trusted storage/L0
// handoff, not instructions to create or repair those resources.
type RootHandoff struct {
	Host             config.HostKind
	Lane             config.ReleaseLane
	InboxID          string
	Path             string
	Layout           string
	OwnerUID         uint32
	RootIdentity     FileIdentity
	ChildIdentities  map[string]FileIdentity
	AncillaryEntries []AncillaryEntrySnapshot
	Volume           *VolumeHandoff
}

// ValidatedRoot is the safe identity used for an in-memory collision check.
// It contains no open descriptors and grants no authority to mutate a root.
type ValidatedRoot struct {
	Host         config.HostKind
	Lane         config.ReleaseLane
	InboxID      string
	Path         string
	RootIdentity FileIdentity
}

// RootInventory is a fresh descriptor-derived inventory of the root and its
// fixed children. Ancillary entries contain metadata only.
type RootInventory struct {
	Claim            ValidatedRoot
	ChildIdentities  map[string]FileIdentity
	AncillaryEntries []AncillaryEntrySnapshot
}

// ValidateHandoff checks the declarative portion of a storage handoff without
// filesystem access. It is suitable for hermetic shape and collision tests.
func ValidateHandoff(handoff RootHandoff) error {
	if !validLaneIdentity(handoff.Host, handoff.Lane) || !rootIDPattern.MatchString(handoff.InboxID) ||
		!canonicalAbsolutePath(handoff.Path) || handoff.OwnerUID == 0 ||
		!validIdentity(handoff.RootIdentity) || len(handoff.ChildIdentities) != len(mailboxChildren) {
		return ErrInvalidHandoff
	}
	if handoff.Layout != LayoutInitializedEmpty && handoff.Layout != LayoutRetained {
		return ErrInvalidHandoff
	}
	if handoff.Layout == LayoutInitializedEmpty && len(handoff.AncillaryEntries) != 0 {
		return ErrInvalidHandoff
	}
	seenIdentities := map[FileIdentity]struct{}{handoff.RootIdentity: {}}
	for _, name := range mailboxChildren {
		identity, exists := handoff.ChildIdentities[name]
		if !exists || !validIdentity(identity) || identity.Device != handoff.RootIdentity.Device {
			return ErrInvalidHandoff
		}
		if _, duplicate := seenIdentities[identity]; duplicate {
			return ErrInvalidHandoff
		}
		seenIdentities[identity] = struct{}{}
	}
	for name := range handoff.ChildIdentities {
		if !contains(mailboxChildren, name) {
			return ErrInvalidHandoff
		}
	}
	seenNames := make(map[string]struct{}, len(handoff.AncillaryEntries))
	for _, entry := range handoff.AncillaryEntries {
		if !safeAncillaryName(entry.Name) || (entry.Kind != EntryRegularFile && entry.Kind != EntryDirectory) ||
			!validIdentity(entry.Identity) || entry.Identity.Device != handoff.RootIdentity.Device ||
			entry.OwnerUID != handoff.OwnerUID || entry.Mode&0o022 != 0 || entry.Mode&0o7000 != 0 {
			return ErrInvalidHandoff
		}
		if _, duplicate := seenNames[entry.Name]; duplicate {
			return ErrInvalidHandoff
		}
		if _, duplicate := seenIdentities[entry.Identity]; duplicate {
			return ErrInvalidHandoff
		}
		seenNames[entry.Name] = struct{}{}
		seenIdentities[entry.Identity] = struct{}{}
	}
	if requiresAPFSVolume(handoff.Host, handoff.Lane) {
		if !validVolumeHandoff(handoff) {
			return ErrInvalidHandoff
		}
	} else if handoff.Volume != nil {
		return ErrInvalidHandoff
	}
	return nil
}

// InspectRoot performs descriptor/no-follow validation and compares the
// current root against the previously validated storage handoff. It is
// read-only; missing roots or children are failures.
func InspectRoot(handoff RootHandoff) (RootInventory, error) {
	if err := ValidateHandoff(handoff); err != nil {
		return RootInventory{}, err
	}
	rootFD, parentFD, rootName, err := openRootNoFollow(handoff.Path, handoff.OwnerUID)
	if err != nil {
		return RootInventory{}, ErrInvalidHandoff
	}
	defer unix.Close(rootFD)
	defer unix.Close(parentFD)

	rootStat, err := fstat(rootFD)
	if err != nil || fileIdentity(rootStat) != handoff.RootIdentity ||
		rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Uid != handoff.OwnerUID || rootStat.Mode&0o7777 != 0o700 {
		return RootInventory{}, ErrInvalidHandoff
	}
	if !descriptorMatchesPath(parentFD, rootName, rootStat) {
		return RootInventory{}, ErrInvalidHandoff
	}

	wantNames := append([]string(nil), mailboxChildren...)
	for _, entry := range handoff.AncillaryEntries {
		wantNames = append(wantNames, entry.Name)
	}
	sort.Strings(wantNames)
	initialNames, err := readDirectoryNames(rootFD)
	if err != nil || !sameStrings(initialNames, wantNames) {
		return RootInventory{}, ErrInvalidHandoff
	}

	children := make(map[string]FileIdentity, len(mailboxChildren))
	for _, name := range mailboxChildren {
		childFD, err := unix.Openat(rootFD, name, directoryOpenFlags, 0)
		if err != nil {
			return RootInventory{}, ErrInvalidHandoff
		}
		childStat, statErr := fstat(childFD)
		pathMatches := statErr == nil && descriptorMatchesPath(rootFD, name, childStat)
		if statErr != nil || !pathMatches || childStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
			childStat.Uid != handoff.OwnerUID || childStat.Mode&0o7777 != 0o700 ||
			fileIdentity(childStat) != handoff.ChildIdentities[name] {
			unix.Close(childFD)
			return RootInventory{}, ErrInvalidHandoff
		}
		if handoff.Layout == LayoutInitializedEmpty {
			names, readErr := readDirectoryNames(childFD)
			if readErr != nil || len(names) != 0 {
				unix.Close(childFD)
				return RootInventory{}, ErrInvalidHandoff
			}
		}
		children[name] = fileIdentity(childStat)
		if unix.Close(childFD) != nil {
			return RootInventory{}, ErrInvalidHandoff
		}
	}

	ancillary := make([]AncillaryEntrySnapshot, 0, len(handoff.AncillaryEntries))
	for _, expected := range handoff.AncillaryEntries {
		observed, err := inspectAncillaryEntry(rootFD, expected.Name, expected.Kind)
		if err != nil || observed != expected {
			return RootInventory{}, ErrInvalidHandoff
		}
		ancillary = append(ancillary, observed)
	}
	finalNames, err := readDirectoryNames(rootFD)
	if err != nil || !sameStrings(initialNames, finalNames) {
		return RootInventory{}, ErrInvalidHandoff
	}
	finalPathStat, err := fstatat(parentFD, rootName)
	if err != nil || fileIdentity(finalPathStat) != handoff.RootIdentity || fileIdentity(finalPathStat) != fileIdentity(rootStat) {
		return RootInventory{}, ErrInvalidHandoff
	}
	// Re-resolve every path component after inspection. The original descriptors
	// keep the inspected tree stable for this pass; this second no-follow walk
	// proves the configured path still names the same root afterward.
	recheckRootFD, recheckParentFD, recheckName, err := openRootNoFollow(handoff.Path, handoff.OwnerUID)
	if err != nil {
		return RootInventory{}, ErrInvalidHandoff
	}
	recheckStat, statErr := fstat(recheckRootFD)
	recheckPathStat, pathErr := fstatat(recheckParentFD, recheckName)
	closeRootErr := unix.Close(recheckRootFD)
	closeParentErr := unix.Close(recheckParentFD)
	if statErr != nil || pathErr != nil || closeRootErr != nil || closeParentErr != nil ||
		recheckName != rootName || fileIdentity(recheckStat) != handoff.RootIdentity ||
		fileIdentity(recheckPathStat) != handoff.RootIdentity || recheckStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return RootInventory{}, ErrInvalidHandoff
	}
	return RootInventory{
		Claim:           ValidatedRoot{Host: handoff.Host, Lane: handoff.Lane, InboxID: handoff.InboxID, Path: handoff.Path, RootIdentity: handoff.RootIdentity},
		ChildIdentities: children, AncillaryEntries: ancillary,
	}, nil
}

// ValidateRootSet rejects equal, nested, aliased, or duplicate claims across
// all active and reserved roots. It performs no filesystem access.
func ValidateRootSet(roots []ValidatedRoot) error {
	for i, root := range roots {
		if !validLaneIdentity(root.Host, root.Lane) || !rootIDPattern.MatchString(root.InboxID) ||
			!canonicalAbsolutePath(root.Path) || !validIdentity(root.RootIdentity) {
			return ErrInvalidHandoff
		}
		for _, prior := range roots[:i] {
			if sameOrNestedPath(root.Path, prior.Path) || root.RootIdentity == prior.RootIdentity {
				return ErrInvalidHandoff
			}
		}
	}
	return nil
}

func validVolumeHandoff(handoff RootHandoff) bool {
	if handoff.Volume == nil {
		return false
	}
	expected, observed := handoff.Volume.Expected, handoff.Volume.Observed
	return validVolumeIdentity(expected) && validVolumeIdentity(observed) &&
		expected == observed && expected.Mountpoint == handoff.Path &&
		expected.Device == handoff.RootIdentity.Device
}

func validVolumeIdentity(volume VolumeIdentity) bool {
	return canonicalAbsolutePath(volume.Mountpoint) && volume.Filesystem == "apfs" &&
		volumeUUIDPattern.MatchString(volume.UUID) && volume.Device > 0 && volume.QuotaBytes > 0
}

func requiresAPFSVolume(host config.HostKind, lane config.ReleaseLane) bool {
	return host == config.HostKindMac && (lane == config.ReleaseLaneDev || lane == config.ReleaseLaneQAS)
}

func validLaneIdentity(host config.HostKind, lane config.ReleaseLane) bool {
	if host != config.HostKindMac && host != config.HostKindLinux {
		return false
	}
	return lane == config.ReleaseLaneDev || lane == config.ReleaseLaneQAS || lane == config.ReleaseLanePRD
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func validIdentity(identity FileIdentity) bool { return identity.Device != 0 && identity.Inode != 0 }

func safeAncillaryName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || strings.IndexByte(name, 0) >= 0 ||
		filepath.Base(name) != name || contains(mailboxChildren, name) {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

func openRootNoFollow(path string, ownerUID uint32) (int, int, string, error) {
	if !canonicalAbsolutePath(path) {
		return -1, -1, "", ErrInvalidHandoff
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 {
		return -1, -1, "", ErrInvalidHandoff
	}
	currentFD, err := unix.Open(string(filepath.Separator), directoryOpenFlags, 0)
	if err != nil {
		return -1, -1, "", ErrInvalidHandoff
	}
	if err := validateAncestorFD(currentFD, ownerUID); err != nil {
		unix.Close(currentFD)
		return -1, -1, "", err
	}
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(currentFD)
			return -1, -1, "", ErrInvalidHandoff
		}
		nextFD, err := unix.Openat(currentFD, part, directoryOpenFlags, 0)
		if err != nil {
			unix.Close(currentFD)
			return -1, -1, "", ErrInvalidHandoff
		}
		if index == len(parts)-1 {
			return nextFD, currentFD, part, nil
		}
		if err := validateAncestorFD(nextFD, ownerUID); err != nil {
			unix.Close(nextFD)
			unix.Close(currentFD)
			return -1, -1, "", err
		}
		if err := unix.Close(currentFD); err != nil {
			unix.Close(nextFD)
			return -1, -1, "", ErrInvalidHandoff
		}
		currentFD = nextFD
	}
	unix.Close(currentFD)
	return -1, -1, "", ErrInvalidHandoff
}

func validateAncestorFD(fd int, ownerUID uint32) error {
	stat, err := fstat(fd)
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || (stat.Mode&0o022) != 0 ||
		(stat.Uid != 0 && stat.Uid != ownerUID) {
		return ErrInvalidHandoff
	}
	return nil
}

func inspectAncillaryEntry(rootFD int, name, expectedKind string) (AncillaryEntrySnapshot, error) {
	stat, err := fstatat(rootFD, name)
	if err != nil {
		return AncillaryEntrySnapshot{}, ErrInvalidHandoff
	}
	kind := ""
	var openFlags int
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = EntryRegularFile
		openFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	case unix.S_IFDIR:
		kind = EntryDirectory
		openFlags = directoryOpenFlags
	default:
		return AncillaryEntrySnapshot{}, ErrInvalidHandoff
	}
	if kind != expectedKind {
		return AncillaryEntrySnapshot{}, ErrInvalidHandoff
	}
	fd, err := unix.Openat(rootFD, name, openFlags, 0)
	if err != nil {
		return AncillaryEntrySnapshot{}, ErrInvalidHandoff
	}
	opened, statErr := fstat(fd)
	closeErr := unix.Close(fd)
	if statErr != nil || closeErr != nil || !sameStatIdentity(stat, opened) ||
		opened.Mode&unix.S_IFMT != stat.Mode&unix.S_IFMT {
		return AncillaryEntrySnapshot{}, ErrInvalidHandoff
	}
	return AncillaryEntrySnapshot{
		Name: name, Kind: kind, Identity: fileIdentity(opened), OwnerUID: opened.Uid,
		Mode: uint32(opened.Mode & 0o7777),
	}, nil
}

func descriptorMatchesPath(parentFD int, name string, descriptor unix.Stat_t) bool {
	pathStat, err := fstatat(parentFD, name)
	return err == nil && sameStatIdentity(pathStat, descriptor) && pathStat.Mode&unix.S_IFMT == descriptor.Mode&unix.S_IFMT
}

func fstat(fd int) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func fstatat(parentFD int, name string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func fileIdentity(stat unix.Stat_t) FileIdentity {
	return FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}
}

func sameStatIdentity(left, right unix.Stat_t) bool { return fileIdentity(left) == fileIdentity(right) }

func readDirectoryNames(fd int) ([]string, error) {
	// Open the directory again instead of dup'ing its descriptor: dup shares
	// the directory cursor, which would make the final stability check see EOF.
	readerFD, err := unix.Openat(fd, ".", directoryOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(readerFD), "lane-root-directory")
	if file == nil {
		unix.Close(readerFD)
		return nil, ErrInvalidHandoff
	}
	names, readErr := file.Readdirnames(-1)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	filtered := names[:0]
	for _, name := range names {
		if name != "." && name != ".." {
			filtered = append(filtered, name)
		}
	}
	sort.Strings(filtered)
	return filtered, nil
}

func sameOrNestedPath(left, right string) bool {
	if left == right {
		return true
	}
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
