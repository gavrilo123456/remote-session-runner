package runtime

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRuntimeOwnershipRecordIsOwnerOnlyAndGenerationBound(t *testing.T) {
	root, workspace, record := runtimeOwnershipFixture(t, "session-owner-record")
	if err := writeRuntimeOwnership(root, record); err != nil {
		t.Fatal(err)
	}

	directory := filepath.Join(root, runtimeOwnershipDirectory)
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("ownership directory mode = %o, want 700", got)
	}
	path, err := runtimeOwnershipPath(root, record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("ownership record mode = %o, want 600", got)
	}

	loaded, err := readRuntimeOwnership(root, record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != record {
		t.Fatalf("loaded ownership record = %+v, want %+v", loaded, record)
	}

	otherGeneration := record
	otherGeneration.Generation = "generation-replacement"
	if err := writeRuntimeOwnership(root, otherGeneration); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("replacement generation error = %v, want ownership rejection", err)
	}
	replacementProcess := record
	replacementProcess.PID++
	replacementProcess.ProcessGroupID++
	if err := writeRuntimeOwnership(root, replacementProcess); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("replacement process error = %v, want ownership rejection", err)
	}
	invalidProcessGroup := record
	invalidProcessGroup.ProcessGroupID++
	if err := writeRuntimeOwnership(root, invalidProcessGroup); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("mismatched process group error = %v, want ownership rejection", err)
	}
	loaded, err = readRuntimeOwnership(root, record.SessionID)
	if err != nil || loaded != record {
		t.Fatalf("existing ownership record changed after replacement attempts: loaded=%+v err=%v", loaded, err)
	}
	if err := removeRuntimeOwnership(root, record.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("removing the ownership record changed its workspace: %v", err)
	}
}

func TestReplaceRuntimeOwnershipKeepsExpectedIdentityBound(t *testing.T) {
	root, workspace, expected := runtimeOwnershipFixture(t, "session-owner-replacement")
	if err := writeRuntimeOwnership(root, expected); err != nil {
		t.Fatal(err)
	}
	replacementWorkspace := filepath.Join(root, "replacement-workspace")
	if err := os.Mkdir(replacementWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := expected
	replacement.Workspace = replacementWorkspace
	replacement.PID++
	replacement.ProcessGroupID = replacement.PID
	replacement.ProcessStartIdentity = "replacement-start-identity"
	if err := replaceRuntimeOwnership(root, expected, replacement); err != nil {
		t.Fatalf("replace exact owner: %v", err)
	}
	loaded, err := readRuntimeOwnership(root, expected.SessionID)
	if err != nil || loaded != replacement {
		t.Fatalf("replacement owner = %+v err=%v, want %+v", loaded, err, replacement)
	}
	if err := replaceRuntimeOwnership(root, expected, expected); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("stale expected owner replacement error=%v, want identity rejection", err)
	}
	loaded, err = readRuntimeOwnership(root, expected.SessionID)
	if err != nil || loaded != replacement {
		t.Fatalf("stale replacement changed owner: loaded=%+v err=%v", loaded, err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("atomic replacement removed expected workspace: %v", err)
	}
}

func TestRuntimeOwnershipRecordRejectsUnsafeFileAndDirectory(t *testing.T) {
	root, _, record := runtimeOwnershipFixture(t, "session-unsafe-record")
	if err := writeRuntimeOwnership(root, record); err != nil {
		t.Fatal(err)
	}
	path, err := runtimeOwnershipPath(root, record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeOwnership(root, record.SessionID); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("insecure record mode error = %v, want ownership rejection", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeOwnership(root, record.SessionID); err != nil {
		t.Fatalf("owner directory was not repaired to private mode: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("owner directory mode after validation = %o, want 700", got)
	}
}

func runtimeOwnershipFixture(t *testing.T, sessionID string) (string, string, RuntimeOwnershipRecord) {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "ro-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, workspace, RuntimeOwnershipRecord{
		Version: runtimeOwnershipVersion, HostOS: runtime.GOOS, SessionID: sessionID,
		Generation: "generation-original", Workspace: workspace, OwnedWorkspace: true,
		PID: os.Getpid(), ProcessGroupID: os.Getpid(), UID: os.Getuid(), Username: current.Username,
		Command: "/bin/bash", ProcessStartIdentity: "start-observed-at-test-fixture",
	}
}

func TestLostRecoveryCleanupProofIsAtomicAndIdentityBound(t *testing.T) {
	root, _, record := runtimeOwnershipFixture(t, "session-lost-recovery-proof")
	if err := writeRuntimeOwnership(root, record); err != nil {
		t.Fatal(err)
	}
	proofTime := time.Date(2026, 9, 30, 20, 30, 0, 123456789, time.UTC)
	marked, err := markLostRecoveryCleanupConfirmed(root, record.SessionID, record, proofTime)
	if err != nil {
		t.Fatal(err)
	}
	wantProof := proofTime.Format(time.RFC3339Nano)
	if marked.LostRecoveryCleanupConfirmedAt != wantProof || !sameRuntimeOwnershipIdentity(marked, record) {
		t.Fatalf("marked ownership=%+v, want immutable identity plus proof %q", marked, wantProof)
	}
	path, err := runtimeOwnershipPath(root, record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marked ownership mode=%v err=%v, want 0600", info.Mode(), err)
	}
	loaded, err := readRuntimeOwnership(root, record.SessionID)
	if err != nil || loaded != marked {
		t.Fatalf("loaded marked ownership=%+v err=%v, want %+v", loaded, err, marked)
	}
	repeated, err := markLostRecoveryCleanupConfirmed(root, record.SessionID, record, proofTime.Add(time.Second))
	if err != nil || repeated != marked {
		t.Fatalf("idempotent proof=%+v err=%v, want %+v", repeated, err, marked)
	}
	changed := record
	changed.Generation = "different-generation"
	if _, err := markLostRecoveryCleanupConfirmed(root, record.SessionID, changed, proofTime); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("changed proof identity error=%v, want ownership rejection", err)
	}
	invalid := record
	invalid.LostRecoveryCleanupConfirmedAt = "not-a-timestamp"
	if err := writeRuntimeOwnership(root, invalid); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("invalid proof timestamp error=%v, want ownership rejection", err)
	}
}
