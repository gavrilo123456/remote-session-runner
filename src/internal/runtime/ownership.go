package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	runtimeinfo "runtime"
	"strings"
	"syscall"
	"time"
)

const runtimeOwnershipVersion = 1

var ErrRuntimeOwnershipRecord = errors.New("runtime ownership record is invalid")

// RuntimeOwnershipRecord is the owner-only host record needed to identify and
// stop a prior session process after its executor has died. It contains no
// script, output, or credential data and never authorizes shell reattachment.
type RuntimeOwnershipRecord struct {
	Version                        int    `json:"version"`
	HostOS                         string `json:"host_os"`
	SessionID                      string `json:"session_id"`
	Generation                     string `json:"generation"`
	Workspace                      string `json:"workspace"`
	OwnedWorkspace                 bool   `json:"owned_workspace"`
	PID                            int    `json:"pid"`
	ProcessGroupID                 int    `json:"process_group_id"`
	UID                            int    `json:"uid"`
	Username                       string `json:"username"`
	Command                        string `json:"command"`
	ProcessStartIdentity           string `json:"process_start_identity"`
	LostRecoveryCleanupConfirmedAt string `json:"lost_recovery_cleanup_confirmed_at,omitempty"`
}

const runtimeOwnershipDirectory = ".runner-runtime-ownership"

func runtimeOwnershipPath(workspaceRoot, sessionID string) (string, error) {
	if strings.TrimSpace(workspaceRoot) == "" || strings.TrimSpace(sessionID) == "" || strings.IndexByte(sessionID, 0) >= 0 {
		return "", fmt.Errorf("%w: workspace root and session ID are required", ErrRuntimeOwnershipRecord)
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("%w: resolve workspace root: %v", ErrRuntimeOwnershipRecord, err)
	}
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(root, runtimeOwnershipDirectory, hex.EncodeToString(digest[:])+".json"), nil
}

func writeRuntimeOwnership(workspaceRoot string, record RuntimeOwnershipRecord) error {
	if err := validateRuntimeOwnershipRecord(record); err != nil {
		return err
	}
	path, err := runtimeOwnershipPath(workspaceRoot, record.SessionID)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create runtime ownership directory: %w", err)
	}
	if err := validateOwnerDirectory(directory); err != nil {
		return err
	}
	if current, err := readRuntimeOwnershipPath(path); err == nil {
		if current != record {
			return fmt.Errorf("%w: refusing to replace an existing process owner record", ErrRuntimeOwnershipRecord)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return publishRuntimeOwnership(path, directory, record)
}

// replaceRuntimeOwnership atomically hands a session owner record from one
// proven-gone runtime to its already-started replacement. It never removes the
// old record first: a crash before rename retains expected, while a completed
// rename leaves replacement. The immutable session and generation must not
// change across this narrow handoff.
func replaceRuntimeOwnership(workspaceRoot string, expected, replacement RuntimeOwnershipRecord) error {
	if err := validateRuntimeOwnershipRecord(expected); err != nil {
		return err
	}
	if err := validateRuntimeOwnershipRecord(replacement); err != nil {
		return err
	}
	if expected.SessionID != replacement.SessionID || expected.Generation != replacement.Generation ||
		expected.LostRecoveryCleanupConfirmedAt != "" || replacement.LostRecoveryCleanupConfirmedAt != "" {
		return fmt.Errorf("%w: invalid controlled-restart ownership replacement", ErrRuntimeOwnershipRecord)
	}
	path, err := runtimeOwnershipPath(workspaceRoot, expected.SessionID)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := validateOwnerDirectory(directory); err != nil {
		return err
	}
	current, err := readRuntimeOwnershipPath(path)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("%w: runtime ownership record changed before controlled-restart replacement", ErrRuntimeOwnershipRecord)
	}
	return publishRuntimeOwnership(path, directory, replacement)
}

// markLostRecoveryCleanupConfirmed adds the one recovery proof that is safe
// to publish over an existing owner record. The immutable process identity
// must still exactly match expected. The atomic replacement and directory sync
// ensure that a later retry never needs to inspect or signal a reused PID once
// capacity release has begun.
func markLostRecoveryCleanupConfirmed(workspaceRoot, sessionID string, expected RuntimeOwnershipRecord, now time.Time) (RuntimeOwnershipRecord, error) {
	if err := validateRuntimeOwnershipRecord(expected); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	if expected.SessionID != sessionID || expected.LostRecoveryCleanupConfirmedAt != "" {
		return RuntimeOwnershipRecord{}, fmt.Errorf("%w: invalid lost-recovery ownership proof request", ErrRuntimeOwnershipRecord)
	}
	path, err := runtimeOwnershipPath(workspaceRoot, sessionID)
	if err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	directory := filepath.Dir(path)
	if err := validateOwnerDirectory(directory); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	current, err := readRuntimeOwnershipPath(path)
	if err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	if !sameRuntimeOwnershipIdentity(current, expected) {
		return RuntimeOwnershipRecord{}, fmt.Errorf("%w: runtime ownership record changed before lost-recovery proof", ErrRuntimeOwnershipRecord)
	}
	if current.LostRecoveryCleanupConfirmedAt != "" {
		return current, nil
	}
	current.LostRecoveryCleanupConfirmedAt = now.UTC().Format(time.RFC3339Nano)
	if err := validateRuntimeOwnershipRecord(current); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	if err := publishRuntimeOwnership(path, directory, current); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	return current, nil
}

func sameRuntimeOwnershipIdentity(left, right RuntimeOwnershipRecord) bool {
	left.LostRecoveryCleanupConfirmedAt = ""
	right.LostRecoveryCleanupConfirmedAt = ""
	return left == right
}

func publishRuntimeOwnership(path, directory string, record RuntimeOwnershipRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode runtime ownership record: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".ownership-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary runtime ownership record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set runtime ownership record mode: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write runtime ownership record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync runtime ownership record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close runtime ownership record: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish runtime ownership record: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync runtime ownership directory: %w", err)
	}
	return nil
}

func readRuntimeOwnership(workspaceRoot, sessionID string) (RuntimeOwnershipRecord, error) {
	path, err := runtimeOwnershipPath(workspaceRoot, sessionID)
	if err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	if err := validateOwnerDirectory(filepath.Dir(path)); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	record, err := readRuntimeOwnershipPath(path)
	if err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	if record.SessionID != sessionID {
		return RuntimeOwnershipRecord{}, fmt.Errorf("%w: session ID does not match record name", ErrRuntimeOwnershipRecord)
	}
	return record, nil
}

// auditRuntimeOwnership blocks startup when a host owner marker is not backed
// by a live durable session reservation. It does not stop or delete an
// unattributed process: the profile remains unavailable until an operator or
// later recovery can resolve ownership safely.
func auditRuntimeOwnership(ctx context.Context, workspaceRoot string, attributable map[string]struct{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return fmt.Errorf("%w: workspace root is empty", ErrRuntimeOwnershipRecord)
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve workspace root: %v", ErrRuntimeOwnershipRecord, err)
	}
	directory := filepath.Join(root, runtimeOwnershipDirectory)
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect runtime ownership directory: %w", err)
	}
	if err := validateOwnerDirectory(directory); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read runtime ownership directory: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("%w: unresolved runtime ownership entry blocks profile readiness", ErrRuntimeOwnershipRecord)
		}
		path := filepath.Join(directory, entry.Name())
		record, err := readRuntimeOwnershipPath(path)
		if err != nil {
			return fmt.Errorf("%w: unresolved runtime ownership entry blocks profile readiness: %v", ErrRuntimeOwnershipRecord, err)
		}
		canonicalPath, err := runtimeOwnershipPath(root, record.SessionID)
		if err != nil || canonicalPath != path {
			return fmt.Errorf("%w: runtime ownership record name does not match its session", ErrRuntimeOwnershipRecord)
		}
		if _, ok := attributable[record.SessionID]; !ok {
			return fmt.Errorf("%w: unattributed runtime owner blocks profile readiness", ErrRuntimeOwnershipRecord)
		}
	}
	return nil
}

func readRuntimeOwnershipPath(path string) (RuntimeOwnershipRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return RuntimeOwnershipRecord{}, fmt.Errorf("%w: record must be an owner-only regular file", ErrRuntimeOwnershipRecord)
	}
	file, err := os.Open(path)
	if err != nil {
		return RuntimeOwnershipRecord{}, fmt.Errorf("open runtime ownership record: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	var record RuntimeOwnershipRecord
	if err := decoder.Decode(&record); err != nil {
		return RuntimeOwnershipRecord{}, fmt.Errorf("%w: decode: %v", ErrRuntimeOwnershipRecord, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return RuntimeOwnershipRecord{}, fmt.Errorf("%w: trailing record data", ErrRuntimeOwnershipRecord)
	}
	if err := validateRuntimeOwnershipRecord(record); err != nil {
		return RuntimeOwnershipRecord{}, err
	}
	return record, nil
}

func removeRuntimeOwnership(workspaceRoot, sessionID string) error {
	path, err := runtimeOwnershipPath(workspaceRoot, sessionID)
	if err != nil {
		return err
	}
	if err := validateOwnerDirectory(filepath.Dir(path)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime ownership record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: refusing to remove an unowned or unsafe record", ErrRuntimeOwnershipRecord)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove runtime ownership record: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync runtime ownership removal: %w", err)
	}
	// The ownership directory is only a container for live records. Remove it
	// when empty so short-lived isolated workspace roots leave no metadata
	// debris. Failure here is harmless because the record removal is already
	// synced; a concurrent writer may have kept the directory non-empty.
	if err := os.Remove(filepath.Dir(path)); err == nil {
		_ = syncDirectory(filepath.Dir(filepath.Dir(path)))
	}
	return nil
}

func validateRuntimeOwnershipRecord(record RuntimeOwnershipRecord) error {
	if record.Version != runtimeOwnershipVersion || record.HostOS != runtimeinfo.GOOS || record.SessionID == "" || record.Generation == "" || !filepath.IsAbs(record.Workspace) || record.PID <= 0 || record.ProcessGroupID != record.PID || record.UID != os.Getuid() || strings.TrimSpace(record.Username) == "" || strings.TrimSpace(record.Command) == "" || strings.TrimSpace(record.ProcessStartIdentity) == "" {
		return fmt.Errorf("%w: required identity fields are missing or inconsistent", ErrRuntimeOwnershipRecord)
	}
	if strings.IndexByte(record.SessionID, 0) >= 0 || strings.IndexByte(record.Generation, 0) >= 0 || strings.IndexByte(record.Workspace, 0) >= 0 || strings.IndexByte(record.Username, 0) >= 0 || strings.IndexByte(record.Command, 0) >= 0 || strings.IndexByte(record.ProcessStartIdentity, 0) >= 0 || strings.IndexByte(record.LostRecoveryCleanupConfirmedAt, 0) >= 0 {
		return fmt.Errorf("%w: NUL is not permitted", ErrRuntimeOwnershipRecord)
	}
	if proof := record.LostRecoveryCleanupConfirmedAt; proof != "" {
		parsed, err := time.Parse(time.RFC3339Nano, proof)
		if err != nil || !strings.HasSuffix(proof, "Z") || parsed.UTC().Format(time.RFC3339Nano) != proof {
			return fmt.Errorf("%w: lost-recovery cleanup proof must be canonical UTC RFC3339Nano", ErrRuntimeOwnershipRecord)
		}
	}
	return nil
}

func validateOwnerDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect runtime ownership directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: ownership directory is not a real directory owned by this account", ErrRuntimeOwnershipRecord)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect runtime ownership directory: %w", err)
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func processGroupExists(processGroupID int) (bool, error) {
	if processGroupID <= 0 {
		return false, fmt.Errorf("%w: invalid process group ID", ErrRuntimeOwnershipRecord)
	}
	if runtimeinfo.GOOS == "linux" {
		// kill(-pgid, 0) keeps reporting a group containing only zombies as
		// present. They cannot execute or be reattached, and a container init
		// may take longer than the bounded cleanup window to reap them. On
		// Linux, define cleanup completion by the absence of live group members.
		members, err := linuxProcessGroupMembers(processGroupID)
		if err != nil {
			return false, err
		}
		return len(members) != 0, nil
	}
	err := syscall.Kill(-processGroupID, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func waitProcessGroupGone(ctx context.Context, processGroupID int, timeout time.Duration) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exists, err := processGroupExists(processGroupID)
		if err == nil && !exists {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			exists, err := processGroupExists(processGroupID)
			return err == nil && !exists
		case <-ticker.C:
		}
	}
}

func stopOwnedProcessGroup(ctx context.Context, processGroupID int, grace time.Duration) error {
	if processGroupID <= 0 {
		return fmt.Errorf("%w: invalid process group ID %d", ErrRuntimeOwnershipRecord, processGroupID)
	}
	if err := signalOwnedProcessGroup(processGroupID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal process group %d with TERM: %w", processGroupID, err)
	}
	return finishOwnedProcessGroupStop(ctx, processGroupID, grace)
}

func signalOwnedProcessGroup(processGroupID int, signal syscall.Signal) error {
	if processGroupID <= 0 {
		return fmt.Errorf("%w: invalid process group ID %d", ErrRuntimeOwnershipRecord, processGroupID)
	}
	if err := syscall.Kill(-processGroupID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func finishOwnedProcessGroupStop(ctx context.Context, processGroupID int, grace time.Duration) error {
	if processGroupID <= 0 {
		return fmt.Errorf("%w: invalid process group ID %d", ErrRuntimeOwnershipRecord, processGroupID)
	}
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	if waitProcessGroupGone(ctx, processGroupID, grace) {
		return nil
	}
	if err := signalOwnedProcessGroup(processGroupID, syscall.SIGKILL); err != nil {
		return fmt.Errorf("signal process group %d with KILL: %w", processGroupID, err)
	}
	if waitProcessGroupGone(context.Background(), processGroupID, 2*time.Second) {
		return nil
	}
	return fmt.Errorf("%w: process group %d remains after bounded cleanup", ErrRuntimeOwnershipRecord, processGroupID)
}
