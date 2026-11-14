package laneinstall

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"

	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

const scopedTeardownArchiveMaxBytes = 64 << 20
const scopedTeardownArchiveMaxEntries = 4096

type scopedTeardownArchiveEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Mode       uint32 `json:"mode"`
	Bytes      int64  `json:"bytes"`
	LinkTarget string `json:"link_target,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type scopedTeardownArchiveManifest struct {
	FormatVersion       int                          `json:"format_version"`
	SessionID           string                       `json:"session_id"`
	Generation          string                       `json:"generation"`
	MarkerSHA256        string                       `json:"original_marker_sha256"`
	Workspace           string                       `json:"workspace"`
	TargetBindingSHA256 string                       `json:"target_binding_sha256"`
	ArchiveSHA256       string                       `json:"archive_sha256"`
	Entries             []scopedTeardownArchiveEntry `json:"entries"`
}

type scopedTeardownTargetBinding struct {
	FormatVersion int                                            `json:"format_version"`
	Owner         hostruntime.RuntimeOwnershipRecord             `json:"owner"`
	Workspace     hostruntime.MacScopedTeardownWorkspaceIdentity `json:"workspace"`
}

func scopedTeardownArchiveDirectory(request CatalogPRDScopedTeardownRequest, sessionID string) string {
	if request.RuntimeArchiveDirectory != "" {
		return request.RuntimeArchiveDirectory
	}
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(request.BackupDirectory, "scoped-teardown-"+hex.EncodeToString(digest[:]))
}

func validateScopedRuntimeArchiveInput(plan PRDUpgradePlan, request CatalogPRDScopedTeardownRequest) error {
	if request.RuntimeArchiveDirectory == "" {
		return nil
	}
	directory := request.RuntimeArchiveDirectory
	if !pathContained(plan.Namespace.Backups, directory) || !scopedPrivateDirectory(plan.Namespace.Backups) {
		return ErrCatalogPRDScopedTeardown
	}
	relative, err := filepath.Rel(plan.Namespace.Backups, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrCatalogPRDScopedTeardown
	}
	current := plan.Namespace.Backups
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if !scopedPrivateDirectory(current) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	raw, err := readScopedPrivateFile(filepath.Join(directory, "manifest.json"), 2<<20)
	if err != nil || hashScopedBytes(raw) != request.ExpectedRuntimeArchiveManifestSHA256 {
		return ErrCatalogPRDScopedTeardown
	}
	var manifest scopedTeardownArchiveManifest
	binding, err := readScopedTargetBinding(directory, request.ExpectedTargetOwnershipSHA256)
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.FormatVersion != 1 ||
		manifest.SessionID != binding.Owner.SessionID || manifest.Generation != binding.Owner.Generation || manifest.Workspace != binding.Workspace.Path {
		return ErrCatalogPRDScopedTeardown
	}
	bindingRaw, err := readScopedPrivateFile(filepath.Join(directory, "target-binding.json"), 128<<10)
	if err != nil || manifest.TargetBindingSHA256 != hashScopedBytes(bindingRaw) {
		return ErrCatalogPRDScopedTeardown
	}
	markerRaw, err := readScopedPrivateFile(filepath.Join(directory, "target-marker.json"), 64<<10)
	var marker hostruntime.RuntimeOwnershipRecord
	if err != nil || manifest.MarkerSHA256 != hashScopedBytes(markerRaw) || json.Unmarshal(markerRaw, &marker) != nil {
		return ErrCatalogPRDScopedTeardown
	}
	marker.LostRecoveryCleanupConfirmedAt = ""
	owner := binding.Owner
	owner.LostRecoveryCleanupConfirmedAt = ""
	if !reflect.DeepEqual(marker, owner) {
		return ErrCatalogPRDScopedTeardown
	}
	archive, err := readScopedPrivateFile(filepath.Join(directory, "workspace.tar"), scopedTeardownArchiveMaxBytes+(4<<20))
	if err != nil || manifest.ArchiveSHA256 != hashScopedBytes(archive) {
		return ErrCatalogPRDScopedTeardown
	}
	return nil
}

func readScopedTargetBinding(directory, expected string) (scopedTeardownTargetBinding, error) {
	raw, err := readScopedPrivateFile(filepath.Join(directory, "target-binding.json"), 128<<10)
	var binding scopedTeardownTargetBinding
	if err != nil || json.Unmarshal(raw, &binding) != nil || binding.FormatVersion != 1 {
		return binding, ErrCatalogPRDScopedTeardown
	}
	digest, err := hostruntime.MacScopedTeardownTargetIdentitySHA256(binding.Owner, binding.Workspace)
	if err != nil || digest != expected {
		return binding, ErrCatalogPRDScopedTeardown
	}
	return binding, nil
}

func scopedTargetWorkspaceStillOriginal(binding scopedTeardownTargetBinding) error {
	var info unix.Stat_t
	err := unix.Lstat(binding.Workspace.Path, &info)
	if errors.Is(err, unix.ENOENT) {
		return nil // Caller separately gates missing-workspace retries on durable release.
	}
	if err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(info.Dev) != binding.Workspace.Device ||
		uint64(info.Ino) != binding.Workspace.Inode || info.Uid != binding.Workspace.UID || uint32(info.Mode&0o777) != binding.Workspace.Mode {
		return ErrCatalogPRDScopedTeardown
	}
	return nil
}

func validateScopedTeardownTargetBinding(plan PRDUpgradePlan, request CatalogPRDScopedTeardownRequest, inventory store.ScopedCompletedTeardownRecoveryPlan, ownership hostruntime.MacScopedTeardownOwnershipSnapshot) error {
	if ownership.TargetPresent && ownership.TargetIdentitySHA256 == request.ExpectedTargetOwnershipSHA256 {
		return nil
	}
	if !inventory.Recovery.AlreadyRecovered {
		return ErrCatalogPRDScopedTeardown
	}
	directory := scopedTeardownArchiveDirectory(request, string(inventory.Recovery.Pair.SessionID))
	verification := request
	verification.RuntimeArchiveDirectory = directory
	if verification.ExpectedRuntimeArchiveManifestSHA256 == "" {
		raw, err := readScopedPrivateFile(filepath.Join(directory, "manifest.json"), 2<<20)
		if err != nil {
			return ErrCatalogPRDScopedTeardown
		}
		verification.ExpectedRuntimeArchiveManifestSHA256 = hashScopedBytes(raw)
	}
	if validateScopedRuntimeArchiveInput(plan, verification) != nil {
		return ErrCatalogPRDScopedTeardown
	}
	binding, err := readScopedTargetBinding(directory, request.ExpectedTargetOwnershipSHA256)
	if err != nil || binding.Owner.SessionID != string(inventory.Recovery.Pair.SessionID) ||
		binding.Owner.Generation != inventory.Recovery.Session.RuntimeGeneration || scopedTargetWorkspaceStillOriginal(binding) != nil {
		return ErrCatalogPRDScopedTeardown
	}
	if !ownership.TargetPresent {
		if _, err := os.Lstat(binding.Workspace.Path); !errors.Is(err, os.ErrNotExist) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	if ownership.TargetPresent {
		old, current := binding.Owner, ownership.Target
		old.LostRecoveryCleanupConfirmedAt, current.LostRecoveryCleanupConfirmedAt = "", ""
		if !reflect.DeepEqual(old, current) {
			return ErrCatalogPRDScopedTeardown
		}
	}
	return nil
}

func scopedPrivateDirectory(path string) bool {
	var info unix.Stat_t
	return unix.Lstat(path, &info) == nil && info.Mode&unix.S_IFMT == unix.S_IFDIR && info.Mode&0o777 == 0o700 && info.Uid == uint32(os.Getuid())
}

func readScopedPrivateFile(path string, maximum int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrCatalogPRDScopedTeardown
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var info, named unix.Stat_t
	if unix.Fstat(fd, &info) != nil || unix.Lstat(path, &named) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG ||
		info.Mode&0o777 != 0o600 || info.Uid != uint32(os.Getuid()) || info.Nlink != 1 || info.Size < 0 || info.Size > maximum ||
		info.Dev != named.Dev || info.Ino != named.Ino {
		return nil, ErrCatalogPRDScopedTeardown
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) != info.Size {
		return nil, ErrCatalogPRDScopedTeardown
	}
	return raw, nil
}

// archiveScopedTeardownMarker runs before a cleanup stamp can modify the exact
// target marker. Repeats preserve the original bytes and accept only the same
// immutable runtime identity with optional forward progress in its proof stamp.
func archiveScopedTeardownMarker(plan PRDUpgradePlan, request CatalogPRDScopedTeardownRequest, ownership hostruntime.MacScopedTeardownOwnershipSnapshot) (string, error) {
	if !ownership.TargetPresent || !ownership.Target.OwnedWorkspace || !scopedPrivateDirectory(request.BackupDirectory) {
		return "", ErrCatalogPRDScopedTeardown
	}
	digest := sha256.Sum256([]byte(ownership.Target.SessionID))
	marker := filepath.Join(plan.Namespace.Workspaces, ".runner-runtime-ownership", hex.EncodeToString(digest[:])+".json")
	raw, err := readScopedPrivateFile(marker, 64<<10)
	if err != nil || hashScopedBytes(raw) != ownership.TargetSHA256 {
		return "", ErrCatalogPRDScopedTeardown
	}
	directory := scopedTeardownArchiveDirectory(request, ownership.Target.SessionID)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", ErrCatalogPRDScopedTeardown
	}
	if !scopedPrivateDirectory(directory) {
		return "", ErrCatalogPRDScopedTeardown
	}
	bindingPath := filepath.Join(directory, "target-binding.json")
	if _, err := os.Lstat(bindingPath); errors.Is(err, os.ErrNotExist) {
		if ownership.TargetIdentitySHA256 != request.ExpectedTargetOwnershipSHA256 {
			return "", ErrCatalogPRDScopedTeardown
		}
		binding := scopedTeardownTargetBinding{FormatVersion: 1, Owner: ownership.Target, Workspace: ownership.TargetWorkspaceIdentity}
		encoded, err := json.MarshalIndent(binding, "", "  ")
		if err != nil || writeScopedArchiveFileExclusive(bindingPath, append(encoded, '\n')) != nil {
			return "", ErrCatalogPRDScopedTeardown
		}
	} else {
		binding, readErr := readScopedTargetBinding(directory, request.ExpectedTargetOwnershipSHA256)
		if err != nil || readErr != nil || scopedTargetWorkspaceStillOriginal(binding) != nil {
			return "", ErrCatalogPRDScopedTeardown
		}
	}
	target := filepath.Join(directory, "target-marker.json")
	if previous, err := readScopedPrivateFile(target, 64<<10); err == nil {
		var original hostruntime.RuntimeOwnershipRecord
		if json.Unmarshal(previous, &original) != nil {
			return "", ErrCatalogPRDScopedTeardown
		}
		current := ownership.Target
		original.LostRecoveryCleanupConfirmedAt, current.LostRecoveryCleanupConfirmedAt = "", ""
		if !reflect.DeepEqual(original, current) {
			return "", ErrCatalogPRDScopedTeardown
		}
	} else {
		if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
			return "", ErrCatalogPRDScopedTeardown
		}
		if err := writeScopedArchiveFileExclusive(target, raw); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func writeScopedArchiveFileExclusive(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".scoped-metadata-*")
	if err != nil {
		return ErrCatalogPRDScopedTeardown
	}
	name := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(name) }()
	if file.Chmod(0o600) != nil {
		return ErrCatalogPRDScopedTeardown
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return ErrCatalogPRDScopedTeardown
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrCatalogPRDScopedTeardown
	}
	if file.Close() != nil || os.Link(name, path) != nil || os.Remove(name) != nil || syncDirectory(filepath.Dir(path)) != nil {
		return ErrCatalogPRDScopedTeardown
	}
	return nil
}

// openScopedArchiveRegular walks every directory descriptor with O_NOFOLLOW.
// Archive inspection never follows a link out of the exact owned workspace.
func openScopedArchiveRegular(root, relative string) (*os.File, os.FileInfo, error) {
	if relative == "" || relative == "." || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, ErrCatalogPRDScopedTeardown
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, ErrCatalogPRDScopedTeardown
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if index < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, nil, ErrCatalogPRDScopedTeardown
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), relative)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrCatalogPRDScopedTeardown
	}
	return file, info, nil
}

func archiveScopedTeardownWorkspace(ctx context.Context, directory string, target hostruntime.RuntimeOwnershipRecord, expectedTarget string) (string, error) {
	if !scopedPrivateDirectory(directory) || !target.OwnedWorkspace || !filepath.IsAbs(target.Workspace) {
		return "", ErrCatalogPRDScopedTeardown
	}
	marker, err := readScopedPrivateFile(filepath.Join(directory, "target-marker.json"), 64<<10)
	if err != nil {
		return "", err
	}
	bindingRaw, err := readScopedPrivateFile(filepath.Join(directory, "target-binding.json"), 128<<10)
	binding, bindingErr := readScopedTargetBinding(directory, expectedTarget)
	if err != nil || bindingErr != nil || binding.Owner.SessionID != target.SessionID ||
		binding.Owner.Generation != target.Generation || scopedTargetWorkspaceStillOriginal(binding) != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	if prior, err := readScopedPrivateFile(manifestPath, 2<<20); err == nil {
		var manifest scopedTeardownArchiveManifest
		if json.Unmarshal(prior, &manifest) != nil || manifest.FormatVersion != 1 || manifest.SessionID != target.SessionID ||
			manifest.Generation != target.Generation || manifest.Workspace != target.Workspace || manifest.MarkerSHA256 != hashScopedBytes(marker) ||
			manifest.TargetBindingSHA256 != hashScopedBytes(bindingRaw) || !prdDigestPattern.MatchString(manifest.ArchiveSHA256) {
			return "", ErrCatalogPRDScopedTeardown
		}
		archive, err := readScopedPrivateFile(filepath.Join(directory, "workspace.tar"), scopedTeardownArchiveMaxBytes+(4<<20))
		if err != nil || hashScopedBytes(archive) != manifest.ArchiveSHA256 || verifyScopedArchivedWorkspace(ctx, target.Workspace, manifest.Entries) != nil {
			return "", ErrCatalogPRDScopedTeardown
		}
		return hashScopedBytes(prior), nil
	}
	if _, err := os.Lstat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		return "", ErrCatalogPRDScopedTeardown
	}
	var entries []scopedTeardownArchiveEntry
	var total int64
	err = filepath.WalkDir(target.Workspace, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return ErrCatalogPRDScopedTeardown
		}
		info, err := os.Lstat(path)
		if err != nil || len(entries) >= scopedTeardownArchiveMaxEntries {
			return ErrCatalogPRDScopedTeardown
		}
		relative, err := filepath.Rel(target.Workspace, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrCatalogPRDScopedTeardown
		}
		item := scopedTeardownArchiveEntry{Path: filepath.ToSlash(relative), Mode: uint32(info.Mode()), Bytes: info.Size()}
		switch {
		case info.Mode().IsRegular():
			item.Kind = "regular"
			total += info.Size()
			if total > scopedTeardownArchiveMaxBytes || info.Size() < 0 {
				return ErrCatalogPRDScopedTeardown
			}
		case info.IsDir():
			item.Kind = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			item.Kind = "symlink"
			item.LinkTarget, err = os.Readlink(path)
			if err != nil {
				return ErrCatalogPRDScopedTeardown
			}
		case info.Mode()&os.ModeNamedPipe != 0:
			item.Kind = "fifo"
		case info.Mode()&os.ModeSocket != 0:
			item.Kind = "socket"
		default:
			item.Kind = "special"
		}
		entries = append(entries, item)
		return nil
	})
	if err != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	var capacity unix.Statfs_t
	if unix.Statfs(directory, &capacity) != nil || uint64(capacity.Bavail)*uint64(capacity.Bsize) < uint64(total)+(4<<20)+prdCatalogOperatingBufferBytes+prdCatalogSelectorHeadroom {
		return "", ErrCatalogPRDScopedTeardown
	}
	temporary, err := os.CreateTemp(directory, ".workspace-archive-*")
	if err != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	name := temporary.Name()
	defer func() { _ = temporary.Close(); _ = os.Remove(name) }()
	if temporary.Chmod(0o600) != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	archiveHash := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(temporary, archiveHash))
	for index := range entries {
		if ctx.Err() != nil {
			return "", ErrCatalogPRDScopedTeardown
		}
		item := &entries[index]
		header := &tar.Header{Name: item.Path, Mode: int64(os.FileMode(item.Mode).Perm()), Size: 0}
		switch item.Kind {
		case "regular":
			header.Typeflag, header.Size = tar.TypeReg, item.Bytes
		case "directory":
			header.Typeflag = tar.TypeDir
		case "symlink":
			header.Typeflag, header.Linkname = tar.TypeSymlink, item.LinkTarget
		case "fifo":
			header.Typeflag = tar.TypeFifo
		default:
			// Sockets and other special files are metadata-only. Never open
			// a FIFO/socket or follow a target just to make a backup fit.
			continue
		}
		if writer.WriteHeader(header) != nil {
			return "", ErrCatalogPRDScopedTeardown
		}
		if item.Kind != "regular" {
			continue
		}
		file, before, err := openScopedArchiveRegular(target.Workspace, filepath.FromSlash(item.Path))
		if err != nil || before.Size() != item.Bytes {
			if file != nil {
				_ = file.Close()
			}
			return "", ErrCatalogPRDScopedTeardown
		}
		digest := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(writer, digest), file, item.Bytes)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || written != item.Bytes || statErr != nil || closeErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
			return "", ErrCatalogPRDScopedTeardown
		}
		item.SHA256 = hex.EncodeToString(digest.Sum(nil))
	}
	if writer.Close() != nil || temporary.Sync() != nil || temporary.Close() != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	archivePath := filepath.Join(directory, "workspace.tar")
	// A prior partial archive is never overwritten. It remains preserved for
	// review; only a complete manifest-bound archive is reusable on retry.
	if err := os.Link(name, archivePath); err != nil {
		// An interruption after publishing the complete tar but before its
		// manifest is resumable only when the newly reconstructed archive is
		// byte-identical. Never overwrite the preserved prior archive.
		prior, readErr := readScopedPrivateFile(archivePath, scopedTeardownArchiveMaxBytes+(4<<20))
		if !errors.Is(err, os.ErrExist) || readErr != nil || hashScopedBytes(prior) != hex.EncodeToString(archiveHash.Sum(nil)) {
			return "", ErrCatalogPRDScopedTeardown
		}
	}
	if err := os.Remove(name); err != nil || syncDirectory(directory) != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	manifest := scopedTeardownArchiveManifest{FormatVersion: 1, SessionID: target.SessionID, Generation: target.Generation,
		MarkerSHA256: hashScopedBytes(marker), Workspace: target.Workspace, TargetBindingSHA256: hashScopedBytes(bindingRaw), ArchiveSHA256: hex.EncodeToString(archiveHash.Sum(nil)), Entries: entries}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || len(raw) > 2<<20 || writeScopedArchiveFileExclusive(manifestPath, append(raw, '\n')) != nil {
		return "", ErrCatalogPRDScopedTeardown
	}
	return hashScopedBytes(append(raw, '\n')), nil
}

// A retry may see a partly finalized workspace: missing archived entries are
// safe because their bytes remain preserved. Newly added or changed entries
// must never be silently deleted under a stale archive.
func verifyScopedArchivedWorkspace(ctx context.Context, root string, entries []scopedTeardownArchiveEntry) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	byPath := make(map[string]scopedTeardownArchiveEntry, len(entries))
	for _, entry := range entries {
		if _, duplicate := byPath[entry.Path]; duplicate {
			return ErrCatalogPRDScopedTeardown
		}
		byPath[entry.Path] = entry
	}
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return ErrCatalogPRDScopedTeardown
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return ErrCatalogPRDScopedTeardown
		}
		entry, ok := byPath[filepath.ToSlash(relative)]
		info, err := os.Lstat(path)
		if !ok || err != nil || uint32(info.Mode()) != entry.Mode {
			return ErrCatalogPRDScopedTeardown
		}
		if entry.Kind == "symlink" {
			link, err := os.Readlink(path)
			if err != nil || link != entry.LinkTarget {
				return ErrCatalogPRDScopedTeardown
			}
		}
		if entry.Kind != "regular" {
			return nil
		}
		file, fileInfo, err := openScopedArchiveRegular(root, relative)
		if err != nil || fileInfo.Size() != entry.Bytes {
			if file != nil {
				_ = file.Close()
			}
			return ErrCatalogPRDScopedTeardown
		}
		digest := sha256.New()
		count, copyErr := io.CopyN(digest, file, entry.Bytes)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || count != entry.Bytes || closeErr != nil || statErr != nil || !os.SameFile(fileInfo, after) ||
			fileInfo.ModTime() != after.ModTime() || fileInfo.Size() != after.Size() || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
			return ErrCatalogPRDScopedTeardown
		}
		return nil
	})
}

func hashScopedBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
