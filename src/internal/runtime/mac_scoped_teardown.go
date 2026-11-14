package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrMacScopedTeardownOwnership is a conservative refusal by the exact,
// non-signalling maintenance boundary. It never authorizes general recovery.
var ErrMacScopedTeardownOwnership = errors.New("scoped Mac teardown ownership proof refused")

// MacScopedTeardownSession is the durable metadata the operator must obtain
// from the complete, qualified retained-work read. It contains no script.
type MacScopedTeardownSession struct {
	SessionID  string
	Generation string
	SourceMode string
	SourcePath string
}

type macScopedFileIdentity struct {
	Device uint64
	Inode  uint64
	UID    uint32
	Mode   uint32
}

type macScopedOwnershipFile struct {
	Record       RuntimeOwnershipRecord
	SHA256       string
	Identity     macScopedFileIdentity
	ModifiedNano int64
	Size         int64
	Workspace    macScopedWorkspaceIdentity
}

// MacScopedTeardownWorkspaceIdentity is private maintenance-package metadata.
// A preserved original identity permits retry validation after target removal;
// the fingerprint alone is never process-exit or capacity-release proof.
type MacScopedTeardownWorkspaceIdentity struct {
	Path    string
	Present bool
	Device  uint64
	Inode   uint64
	UID     uint32
	Mode    uint32
}

type macScopedWorkspaceIdentity = MacScopedTeardownWorkspaceIdentity

// MacScopedTeardownOwnershipSnapshot is private operator evidence. Target has
// host paths and must not be dumped into a public report. PreservedSHA256 binds
// all peer marker bytes and identities, their canonical workspace identities,
// and the private containing directories. No process is inspected by the read.
//
// A missing target marker is represented explicitly for interrupted/repeated
// finalization. It is not cleanup proof. The caller may accept that state only
// after the store independently proves the exact reservation already released.
type MacScopedTeardownOwnershipSnapshot struct {
	Target                  RuntimeOwnershipRecord
	TargetPresent           bool
	TargetSHA256            string
	TargetIdentitySHA256    string
	TargetWorkspaceIdentity MacScopedTeardownWorkspaceIdentity
	PreservedSHA256         string

	root          string
	targetID      string
	expected      []MacScopedTeardownSession
	rootIdentity  macScopedFileIdentity
	ownerIdentity macScopedFileIdentity
	ownerPresent  bool
	targetFile    macScopedOwnershipFile
}

func scopedMacRefusal(reason string) error {
	return fmt.Errorf("%w: %s", ErrMacScopedTeardownOwnership, reason)
}

// ReadMacScopedTeardownOwnership reads only existing metadata. Unlike the
// ordinary runtime ownership reader, it never repairs modes or creates paths.
func ReadMacScopedTeardownOwnership(ctx context.Context, workspaceRoot, targetID string, sessions []MacScopedTeardownSession) (MacScopedTeardownOwnershipSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return readMacScopedTeardownOwnership(ctx, workspaceRoot, targetID, sessions)
}

func readMacScopedTeardownOwnership(ctx context.Context, workspaceRoot, targetID string, sessions []MacScopedTeardownSession) (MacScopedTeardownOwnershipSnapshot, error) {
	var result MacScopedTeardownOwnershipSnapshot
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if targetID == "" || len(sessions) == 0 || len(sessions) > 10000 || !filepath.IsAbs(workspaceRoot) {
		return result, scopedMacRefusal("invalid selection")
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil || filepath.Clean(workspaceRoot) != root {
		return result, scopedMacRefusal("workspace root unavailable")
	}
	root = filepath.Clean(root)
	rootIdentity, err := readMacScopedDirectory(root, true)
	if err != nil {
		return result, err
	}
	account, err := user.Current()
	if err != nil {
		return result, scopedMacRefusal("account unavailable")
	}
	expected := append([]MacScopedTeardownSession(nil), sessions...)
	sort.Slice(expected, func(i, j int) bool { return expected[i].SessionID < expected[j].SessionID })
	known := make(map[string]MacScopedTeardownSession, len(expected))
	generations := make(map[string]struct{}, len(expected))
	for _, item := range expected {
		if item.SessionID == "" || item.Generation == "" || strings.ContainsAny(item.SessionID+item.Generation+item.SourcePath, "\x00") {
			return result, scopedMacRefusal("invalid durable runtime identity")
		}
		if _, duplicate := known[item.SessionID]; duplicate {
			return result, scopedMacRefusal("duplicate durable session")
		}
		if _, duplicate := generations[item.Generation]; duplicate {
			return result, scopedMacRefusal("duplicate durable generation")
		}
		if item.SourceMode != "empty" && item.SourceMode != "git_revision" && item.SourceMode != "local_worktree" {
			return result, scopedMacRefusal("unsupported durable source")
		}
		known[item.SessionID], generations[item.Generation] = item, struct{}{}
	}
	if _, selected := known[targetID]; !selected {
		return result, scopedMacRefusal("target missing from durable selection")
	}
	result = MacScopedTeardownOwnershipSnapshot{root: root, targetID: targetID, expected: expected, rootIdentity: rootIdentity}
	directory := filepath.Join(root, runtimeOwnershipDirectory)
	ownerIdentity, err := readMacScopedDirectory(directory, true)
	if errors.Is(err, os.ErrNotExist) {
		if len(known) != 1 {
			return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("preserved ownership directory absent")
		}
	} else if err != nil {
		return MacScopedTeardownOwnershipSnapshot{}, err
	} else {
		result.ownerIdentity, result.ownerPresent = ownerIdentity, true
	}
	files := make(map[string]macScopedOwnershipFile, len(known))
	var ownershipNames []string
	if result.ownerPresent {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) > 10000 {
			return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("ownership directory unreadable or unbounded")
		}
		for _, entry := range entries {
			ownershipNames = append(ownershipNames, entry.Name())
			if err := ctx.Err(); err != nil {
				return MacScopedTeardownOwnershipSnapshot{}, err
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("unresolved ownership entry")
			}
			path := filepath.Join(directory, entry.Name())
			file, err := readMacScopedOwnershipFile(path)
			if err != nil {
				return MacScopedTeardownOwnershipSnapshot{}, err
			}
			record := file.Record
			if start, err := strconv.ParseUint(record.ProcessStartIdentity, 10, 64); err != nil || start == 0 {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("invalid process incarnation")
			}
			selection, ok := known[record.SessionID]
			if !ok || record.Generation != selection.Generation || record.Username != account.Username || record.UID != os.Getuid() {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("ownership is not bound to the durable selection")
			}
			canonical, err := runtimeOwnershipPath(root, record.SessionID)
			if err != nil || canonical != path {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("ownership filename mismatch")
			}
			if _, duplicate := files[record.SessionID]; duplicate {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("duplicate owner record")
			}
			workspace, err := readMacScopedWorkspace(root, record, selection, record.SessionID == targetID)
			if err != nil {
				return MacScopedTeardownOwnershipSnapshot{}, err
			}
			file.Workspace = workspace
			files[record.SessionID] = file
		}
	}
	peers := make([]macScopedOwnershipFile, 0, len(known)-1)
	for _, item := range expected {
		file, present := files[item.SessionID]
		if item.SessionID == targetID {
			if present {
				result.Target, result.TargetPresent, result.TargetSHA256, result.targetFile = file.Record, true, file.SHA256, file
				result.TargetWorkspaceIdentity = file.Workspace
				if file.Workspace.Present {
					result.TargetIdentitySHA256, err = MacScopedTeardownTargetIdentitySHA256(file.Record, file.Workspace)
					if err != nil {
						return MacScopedTeardownOwnershipSnapshot{}, err
					}
				}
			}
			continue
		}
		if !present {
			return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("preserved owner record absent")
		}
		peers = append(peers, file)
	}
	all := append([]macScopedOwnershipFile(nil), peers...)
	if result.TargetPresent {
		all = append(all, result.targetFile)
	}
	for i := range all {
		for j := 0; j < i; j++ {
			if j%64 == 0 {
				if err := ctx.Err(); err != nil {
					return MacScopedTeardownOwnershipSnapshot{}, err
				}
			}
			left, right := all[i], all[j]
			if left.Record.PID == right.Record.PID || left.Record.ProcessGroupID == right.Record.ProcessGroupID ||
				(left.Identity.Device == right.Identity.Device && left.Identity.Inode == right.Identity.Inode) ||
				macScopedPathsOverlap(left.Workspace.Path, right.Workspace.Path) ||
				(left.Workspace.Present && right.Workspace.Present && left.Workspace.Device == right.Workspace.Device && left.Workspace.Inode == right.Workspace.Inode) {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("runtime identities or workspaces overlap")
			}
		}
	}
	if err := validateMacScopedWorkspaceAncestry(ctx, all, result.ownerIdentity); err != nil {
		return MacScopedTeardownOwnershipSnapshot{}, err
	}
	encoded, err := json.Marshal(struct {
		Version      int
		Root         string
		RootIdentity macScopedFileIdentity
		OwnerPresent bool
		Owner        macScopedFileIdentity
		Peers        []macScopedOwnershipFile
	}{1, root, rootIdentity, result.ownerPresent, result.ownerIdentity, peers})
	if err != nil {
		return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("preservation fingerprint unavailable")
	}
	digest := sha256.Sum256(encoded)
	result.PreservedSHA256 = hex.EncodeToString(digest[:])
	if finalRoot, err := readMacScopedDirectory(root, true); err != nil || finalRoot != rootIdentity {
		return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("workspace root changed during read")
	}
	if result.ownerPresent {
		if finalOwner, err := readMacScopedDirectory(directory, true); err != nil || finalOwner != result.ownerIdentity {
			return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("ownership directory changed during read")
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != len(ownershipNames) {
			return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("ownership entries changed during read")
		}
		for index, entry := range entries {
			if entry.Name() != ownershipNames[index] {
				return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("ownership entries changed during read")
			}
		}
	}
	return result, nil
}

func readMacScopedDirectory(path string, private bool) (macScopedFileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return macScopedFileIdentity{}, os.ErrNotExist
		}
		return macScopedFileIdentity{}, scopedMacRefusal("directory metadata unavailable")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) ||
		(private && info.Mode().Perm() != 0o700) || (!private && info.Mode().Perm()&0o022 != 0) {
		return macScopedFileIdentity{}, scopedMacRefusal("unsafe directory metadata")
	}
	return scopedMacFileIdentity(info)
}

func scopedMacFileIdentity(info os.FileInfo) (macScopedFileIdentity, error) {
	value, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return macScopedFileIdentity{}, scopedMacRefusal("filesystem identity unavailable")
	}
	return macScopedFileIdentity{Device: uint64(value.Dev), Inode: uint64(value.Ino), UID: value.Uid, Mode: uint32(info.Mode().Perm())}, nil
}

func readMacScopedOwnershipFile(path string) (macScopedOwnershipFile, error) {
	var result macScopedOwnershipFile
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, os.ErrNotExist
		}
		return result, scopedMacRefusal("ownership file metadata unavailable")
	}
	value, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) || value.Nlink != 1 {
		return result, scopedMacRefusal("unsafe ownership file metadata")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return result, scopedMacRefusal("ownership file unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, scopedMacRefusal("ownership file changed before read")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return result, scopedMacRefusal("ownership record unreadable or unbounded")
	}
	if err := validateMacScopedOwnershipJSONFields(raw); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Record); err != nil {
		return macScopedOwnershipFile{}, scopedMacRefusal("invalid ownership record")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || validateRuntimeOwnershipRecord(result.Record) != nil {
		return macScopedOwnershipFile{}, scopedMacRefusal("invalid ownership identity")
	}
	final, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, final) || info.Size() != final.Size() || !info.ModTime().Equal(final.ModTime()) || info.Mode() != final.Mode() {
		return macScopedOwnershipFile{}, scopedMacRefusal("ownership file changed during read")
	}
	result.Identity, err = scopedMacFileIdentity(info)
	if err != nil {
		return macScopedOwnershipFile{}, err
	}
	finalIdentity, err := scopedMacFileIdentity(final)
	finalStat, finalOK := final.Sys().(*syscall.Stat_t)
	if err != nil || finalIdentity != result.Identity || !finalOK || finalStat.Nlink != 1 {
		return macScopedOwnershipFile{}, scopedMacRefusal("ownership file identity changed during read")
	}
	result.Size, result.ModifiedNano = info.Size(), info.ModTime().UnixNano()
	digest := sha256.Sum256(raw)
	result.SHA256 = hex.EncodeToString(digest[:])
	return result, nil
}

func validateMacScopedOwnershipJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return scopedMacRefusal("invalid ownership object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return scopedMacRefusal("ambiguous ownership fields")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return scopedMacRefusal("invalid ownership field")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return scopedMacRefusal("invalid ownership object boundary")
	}
	return nil
}

func readMacScopedWorkspace(root string, record RuntimeOwnershipRecord, expected MacScopedTeardownSession, target bool) (macScopedWorkspaceIdentity, error) {
	path := filepath.Clean(record.Workspace)
	if path != record.Workspace || !filepath.IsAbs(path) {
		return macScopedWorkspaceIdentity{}, scopedMacRefusal("noncanonical workspace path")
	}
	if macScopedPathsOverlap(path, filepath.Join(root, runtimeOwnershipDirectory)) {
		return macScopedWorkspaceIdentity{}, scopedMacRefusal("workspace overlaps shared runtime metadata")
	}
	switch expected.SourceMode {
	case "empty", "git_revision":
		if expected.SourcePath != "" || !record.OwnedWorkspace || !macScopedPathWithin(root, path) {
			return macScopedWorkspaceIdentity{}, scopedMacRefusal("owned workspace differs from durable source")
		}
	case "local_worktree":
		if record.OwnedWorkspace || expected.SourcePath != path {
			return macScopedWorkspaceIdentity{}, scopedMacRefusal("worktree differs from durable source")
		}
	default:
		return macScopedWorkspaceIdentity{}, scopedMacRefusal("unsupported workspace source")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) && target && record.LostRecoveryCleanupConfirmedAt != "" {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
		if parentErr != nil || parent != filepath.Dir(path) {
			return macScopedWorkspaceIdentity{}, scopedMacRefusal("missing workspace parent is ambiguous")
		}
		return macScopedWorkspaceIdentity{Path: path}, nil
	}
	if err != nil || canonical != path {
		return macScopedWorkspaceIdentity{}, scopedMacRefusal("workspace unavailable or aliased")
	}
	identity, err := readMacScopedDirectory(path, record.OwnedWorkspace)
	if err != nil {
		return macScopedWorkspaceIdentity{}, err
	}
	return macScopedWorkspaceIdentity{Path: path, Present: true, Device: identity.Device, Inode: identity.Inode, UID: identity.UID, Mode: identity.Mode}, nil
}

// MacScopedTeardownTargetIdentitySHA256 is a pure, versioned fingerprint for
// an original marker plus canonical workspace identity. Only the cleanup stamp
// is normalized away. The operator may recompute it from its preserved package
// after a durably released target's workspace is absent; it must not establish
// a new identity baseline from the absence or trust a stored hash alone.
func MacScopedTeardownTargetIdentitySHA256(record RuntimeOwnershipRecord, workspace MacScopedTeardownWorkspaceIdentity) (string, error) {
	if validateRuntimeOwnershipRecord(record) != nil || !record.OwnedWorkspace || !workspace.Present ||
		workspace.Path != record.Workspace || filepath.Clean(workspace.Path) != workspace.Path ||
		workspace.Inode == 0 || workspace.UID != uint32(os.Getuid()) || workspace.Mode != 0o700 {
		return "", scopedMacRefusal("invalid original target identity")
	}
	if start, err := strconv.ParseUint(record.ProcessStartIdentity, 10, 64); err != nil || start == 0 {
		return "", scopedMacRefusal("invalid original target incarnation")
	}
	record.LostRecoveryCleanupConfirmedAt = ""
	encoded, err := json.Marshal(struct {
		Version   int                                `json:"format_version"`
		Record    RuntimeOwnershipRecord             `json:"owner"`
		Workspace MacScopedTeardownWorkspaceIdentity `json:"workspace"`
	}{1, record, workspace})
	if err != nil {
		return "", scopedMacRefusal("target identity fingerprint unavailable")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func macScopedPathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func macScopedPathsOverlap(left, right string) bool {
	return left == right || macScopedPathWithin(left, right) || macScopedPathWithin(right, left)
}

// Identity-based ancestor checks also catch case/Unicode aliases on Mac
// filesystems, where cleaned path strings alone do not establish disjointness.
func validateMacScopedWorkspaceAncestry(ctx context.Context, files []macScopedOwnershipFile, control macScopedFileIdentity) error {
	type inode struct{ device, number uint64 }
	known := make(map[inode]string, len(files))
	for _, file := range files {
		if file.Workspace.Present {
			if file.Workspace.Device == control.Device && file.Workspace.Inode == control.Inode {
				return scopedMacRefusal("workspace aliases shared runtime metadata")
			}
			known[inode{file.Workspace.Device, file.Workspace.Inode}] = file.Record.SessionID
		}
	}
	for _, file := range files {
		path := filepath.Dir(file.Workspace.Path)
		for depth := 0; ; depth++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if depth >= 256 {
				return scopedMacRefusal("workspace ancestry is unbounded")
			}
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return scopedMacRefusal("workspace ancestry is unavailable or aliased")
			}
			identity, err := scopedMacFileIdentity(info)
			if err != nil {
				return err
			}
			if identity.Device == control.Device && identity.Inode == control.Inode {
				return scopedMacRefusal("workspace ancestry enters shared runtime metadata")
			}
			if sessionID, exists := known[inode{identity.Device, identity.Inode}]; exists && sessionID != file.Record.SessionID {
				return scopedMacRefusal("workspace ancestry overlaps another runtime")
			}
			parent := filepath.Dir(path)
			if parent == path {
				break
			}
			path = parent
		}
	}
	return nil
}

type macScopedExitObserver interface {
	RootAbsent(context.Context, RuntimeOwnershipRecord) (bool, error)
	GroupEmpty(context.Context, int) (bool, error)
}

type macScopedNativeExitObserver struct{}

func (macScopedNativeExitObserver) RootAbsent(ctx context.Context, record RuntimeOwnershipRecord) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return inspectMacScopedRootAbsent(record.PID)
}

func (macScopedNativeExitObserver) GroupEmpty(ctx context.Context, pgid int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	members, err := inspectMacProcessGroupMembers(pgid)
	return err == nil && len(members) == 0, err
}

// CheckExited is the read-only pre-maintenance applicability check. It makes
// the same bounded root/group/root observations as ConfirmExited but never
// stamps proof, syncs files, signals a process or changes an owner/workspace.
// A missing marker is not process proof; the operator must handle only a
// separately established durable release/finalization retry in that case.
func (snapshot *MacScopedTeardownOwnershipSnapshot) CheckExited(ctx context.Context) error {
	return snapshot.checkExited(ctx, macScopedNativeExitObserver{})
}

func (snapshot *MacScopedTeardownOwnershipSnapshot) checkExited(ctx context.Context, observer macScopedExitObserver) error {
	if snapshot == nil || snapshot.root == "" || observer == nil {
		return scopedMacRefusal("unbound ownership snapshot")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := snapshot.revalidate(ctx, true)
	if err != nil || !current.TargetPresent {
		return scopedMacRefusal("target ownership is absent or changed")
	}
	if current.Target.LostRecoveryCleanupConfirmedAt == "" {
		if err := observeMacScopedExit(ctx, current.Target, observer); err != nil {
			return err
		}
	}
	_, err = current.revalidate(ctx, true)
	return err
}

func observeMacScopedExit(ctx context.Context, target RuntimeOwnershipRecord, observer macScopedExitObserver) error {
	for _, phase := range []string{"root", "group", "root"} {
		var absent bool
		var err error
		if phase == "root" {
			absent, err = observer.RootAbsent(ctx, target)
		} else {
			absent, err = observer.GroupEmpty(ctx, target.ProcessGroupID)
		}
		if err != nil || !absent {
			return scopedMacRefusal("target process absence is unproven")
		}
	}
	return nil
}

// ConfirmExited never signals, stops, reaps or adopts a process. It permits only
// an already-absent root and completely empty group, observed root/group/root.
// A prior durable target proof makes retries free of PID inspection altogether.
func (snapshot *MacScopedTeardownOwnershipSnapshot) ConfirmExited(ctx context.Context) (RuntimeOwnershipRecord, error) {
	return snapshot.confirmExited(ctx, macScopedNativeExitObserver{}, time.Now)
}

func (snapshot *MacScopedTeardownOwnershipSnapshot) confirmExited(ctx context.Context, observer macScopedExitObserver, now func() time.Time) (RuntimeOwnershipRecord, error) {
	if snapshot == nil || snapshot.root == "" || observer == nil || now == nil {
		return RuntimeOwnershipRecord{}, scopedMacRefusal("unbound ownership snapshot")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := snapshot.revalidate(ctx, true)
	if err != nil || !current.TargetPresent {
		return RuntimeOwnershipRecord{}, scopedMacRefusal("target ownership is absent or changed")
	}
	if current.Target.LostRecoveryCleanupConfirmedAt != "" {
		if err := syncMacScopedProof(current); err != nil {
			return RuntimeOwnershipRecord{}, err
		}
		*snapshot = current
		return current.Target, nil
	}
	if err := observeMacScopedExit(ctx, current.Target, observer); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	current, err = snapshot.revalidate(ctx, true)
	if err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	replacement := current.Target
	replacement.LostRecoveryCleanupConfirmedAt = now().UTC().Format(time.RFC3339Nano)
	if validateRuntimeOwnershipRecord(replacement) != nil {
		return RuntimeOwnershipRecord{}, scopedMacRefusal("invalid target proof timestamp")
	}
	if err := publishMacScopedTargetProof(ctx, current, replacement); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	after, err := readMacScopedTeardownOwnership(ctx, snapshot.root, snapshot.targetID, snapshot.expected)
	if err != nil || after.PreservedSHA256 != snapshot.PreservedSHA256 || !after.TargetPresent || after.Target != replacement {
		return RuntimeOwnershipRecord{}, scopedMacRefusal("ownership changed after target proof")
	}
	*snapshot = after
	return replacement, nil
}

func syncMacScopedProof(snapshot MacScopedTeardownOwnershipSnapshot) error {
	path, _ := runtimeOwnershipPath(snapshot.root, snapshot.targetID)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return scopedMacRefusal("target proof unavailable for durability check")
	}
	file := os.NewFile(uintptr(fd), path)
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return scopedMacRefusal("target proof metadata unavailable for durability check")
	}
	identity, identityErr := scopedMacFileIdentity(info)
	if identityErr != nil || identity != snapshot.targetFile.Identity {
		_ = file.Close()
		return scopedMacRefusal("target proof changed before durability check")
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil || closeErr != nil || syncDirectory(filepath.Dir(path)) != nil {
		return scopedMacRefusal("target proof durability is unconfirmed")
	}
	return nil
}

func publishMacScopedTargetProof(ctx context.Context, snapshot MacScopedTeardownOwnershipSnapshot, replacement RuntimeOwnershipRecord) error {
	if !snapshot.Target.OwnedWorkspace || !snapshot.targetFile.Workspace.Present ||
		snapshot.targetFile.Workspace.Device != snapshot.ownerIdentity.Device {
		return scopedMacRefusal("target proof requires an existing same-volume owned workspace")
	}
	encoded, err := json.Marshal(replacement)
	if err != nil {
		return scopedMacRefusal("target proof encoding failed")
	}
	// Stage inside only the selected workspace. A crash before rename leaves
	// no unresolved temporary entry in the shared ownership directory.
	file, err := os.CreateTemp(snapshot.Target.Workspace, ".runner-scoped-proof-*.tmp")
	if err != nil {
		return scopedMacRefusal("target proof staging failed")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if file.Chmod(0o600) != nil {
		_ = file.Close()
		return scopedMacRefusal("target proof staging mode failed")
	}
	_, writeErr := file.Write(encoded)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return scopedMacRefusal("target proof staging durability failed")
	}
	if _, err := snapshot.revalidate(ctx, true); err != nil {
		return err
	}
	path, _ := runtimeOwnershipPath(snapshot.root, snapshot.targetID)
	if os.Rename(temporary, path) != nil || syncDirectory(filepath.Dir(path)) != nil || syncDirectory(snapshot.Target.Workspace) != nil {
		return scopedMacRefusal("target proof could not be durably published")
	}
	return nil
}

func (snapshot *MacScopedTeardownOwnershipSnapshot) revalidate(ctx context.Context, exactTarget bool) (MacScopedTeardownOwnershipSnapshot, error) {
	if snapshot == nil || snapshot.root == "" || snapshot.PreservedSHA256 == "" {
		return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("unbound ownership snapshot")
	}
	current, err := readMacScopedTeardownOwnership(ctx, snapshot.root, snapshot.targetID, snapshot.expected)
	if err != nil || current.PreservedSHA256 != snapshot.PreservedSHA256 {
		return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("preserved ownership changed")
	}
	if exactTarget && (current.TargetPresent != snapshot.TargetPresent || current.targetFile != snapshot.targetFile) {
		return MacScopedTeardownOwnershipSnapshot{}, scopedMacRefusal("selected ownership changed")
	}
	return current, nil
}

// CheckFinalization is a read-only applicability check for target filesystem
// cleanup. It intentionally does not require a process proof stamp, allowing
// unsupported contents to be refused before any target proof or DB mutation.
// Missing target artifacts are not release proof; the operator must separately
// authorize their absence from the exact durable finalization/release records.
func (snapshot *MacScopedTeardownOwnershipSnapshot) CheckFinalization(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := snapshot.revalidate(ctx, true)
	if err != nil {
		return err
	}
	if !current.TargetPresent || !current.targetFile.Workspace.Present {
		return nil
	}
	if !current.Target.OwnedWorkspace {
		return scopedMacRefusal("target filesystem cleanup is not owned")
	}
	if err := validateMacScopedFinalizationTree(ctx, current.Target.Workspace); err != nil {
		return err
	}
	_, err = current.revalidate(ctx, true)
	return err
}

// Finalize removes only the selected, already-proven owned workspace and owner
// record. CALLER MUST FIRST prove the exact reservation durably released. This
// method intentionally contains no PID inspection or signal call. A missing
// marker is an idempotent no-op only under that caller's durable release proof.
func (snapshot *MacScopedTeardownOwnershipSnapshot) Finalize(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := snapshot.revalidate(ctx, true)
	if err != nil {
		return err
	}
	if !current.TargetPresent {
		*snapshot = current
		return nil
	}
	if current.Target.LostRecoveryCleanupConfirmedAt == "" ||
		(snapshot.TargetPresent && !sameRuntimeOwnershipIdentity(snapshot.Target, current.Target)) {
		return scopedMacRefusal("target lacks durable cleanup proof")
	}
	if current.Target.OwnedWorkspace && current.targetFile.Workspace.Present {
		if err := validateMacScopedFinalizationTree(ctx, current.Target.Workspace); err != nil {
			return err
		}
		if _, err := current.revalidate(ctx, true); err != nil {
			return err
		}
		if err := os.RemoveAll(current.Target.Workspace); err != nil {
			return scopedMacRefusal("target workspace finalization failed")
		}
	}
	if current.Target.OwnedWorkspace {
		// A retry may find the workspace already absent. Sync its parent in
		// either case before removing the last owner marker, so a crash cannot
		// acknowledge marker finalization while directory removal is unproven.
		if err := syncDirectory(filepath.Dir(current.Target.Workspace)); err != nil {
			return scopedMacRefusal("target workspace removal could not be synced")
		}
	}
	beforeUnlink, err := readMacScopedTeardownOwnership(ctx, current.root, current.targetID, current.expected)
	if err != nil || beforeUnlink.PreservedSHA256 != snapshot.PreservedSHA256 || !beforeUnlink.TargetPresent || beforeUnlink.Target != current.Target {
		return scopedMacRefusal("ownership changed during target finalization")
	}
	path, _ := runtimeOwnershipPath(current.root, current.targetID)
	if err := os.Remove(path); err != nil {
		return scopedMacRefusal("target marker finalization failed")
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return scopedMacRefusal("target marker removal could not be synced")
	}
	after, err := readMacScopedTeardownOwnership(ctx, current.root, current.targetID, current.expected)
	if err != nil || after.TargetPresent || after.PreservedSHA256 != snapshot.PreservedSHA256 {
		return scopedMacRefusal("preserved ownership changed after finalization")
	}
	*snapshot = after
	return nil
}

func validateMacScopedFinalizationTree(ctx context.Context, path string) error {
	count := 0
	return filepath.WalkDir(path, func(_ string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry == nil {
			return scopedMacRefusal("target workspace could not be inspected")
		}
		count++
		if count > 10000 || entry.Type()&os.ModeSymlink != 0 {
			return scopedMacRefusal("target workspace is unbounded or aliased")
		}
		info, err := entry.Info()
		if err != nil || !ownedByCurrentUser(info) {
			return scopedMacRefusal("target workspace ownership is unproven")
		}
		value, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (!info.IsDir() && value.Nlink != 1) ||
			(!info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeNamedPipe == 0) {
			return scopedMacRefusal("target workspace has ambiguous file identity")
		}
		return nil
	})
}
