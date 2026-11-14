package laneinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

func TestScopedTeardownFailureReportsSafeLimitWithoutLeakingCause(t *testing.T) {
	for _, apply := range []bool{false, true} {
		cause := fmt.Errorf("private SQL and /private/owner/path: %w", store.ErrRetainedWorkInventoryLimit)
		err := scopedTeardownFailure("inventory", cause, apply)
		if !errors.Is(err, ErrCatalogPRDScopedTeardown) || !errors.Is(err, store.ErrRetainedWorkInventoryLimit) ||
			!strings.Contains(err.Error(), "failure_stage=inventory failure_category=inventory_limit") ||
			strings.Contains(err.Error(), "private SQL") || strings.Contains(err.Error(), "/private/owner") ||
			strings.Contains(err.Error(), "recovery incomplete") != apply {
			t.Fatalf("unsafe or ambiguous boundary error: %v", err)
		}
	}
}

func validScopedTeardownRequest() CatalogPRDScopedTeardownRequest {
	return CatalogPRDScopedTeardownRequest{ReleaseID: "rr-20261111-bug031-0b7c031", ExpectedReleaseSHA256: strings.Repeat("a", 64),
		ExpectedBundleSHA256: strings.Repeat("b", 64), ExpectedConfigurationSHA256: strings.Repeat("c", 64),
		TeardownPair:    "sess-0123456789abcdef0123456789abcdef:cmd-0123456789abcdef0123456789abcdef",
		BackupDirectory: "/private/test-only/scoped-backup", ExpectedBackupManifestSHA: strings.Repeat("d", 64),
		ExpectedPreservedInventorySHA256: strings.Repeat("e", 64), ExpectedPreservedOwnershipSHA256: strings.Repeat("f", 64),
		ExpectedTargetOwnershipSHA256: strings.Repeat("1", 64)}
}

func TestCatalogPRDScopedTeardownRequestKeepsPlanAndApplyBindingsSeparate(t *testing.T) {
	valid := validScopedTeardownRequest()
	if ValidateCatalogPRDScopedTeardownRequest(valid, true) != nil {
		t.Fatal("complete scoped apply request refused")
	}
	plan := CatalogPRDScopedTeardownRequest{ReleaseID: valid.ReleaseID, ExpectedReleaseSHA256: valid.ExpectedReleaseSHA256,
		ExpectedBundleSHA256: valid.ExpectedBundleSHA256, TeardownPair: valid.TeardownPair}
	if ValidateCatalogPRDScopedTeardownRequest(plan, false) != nil || ValidateCatalogPRDScopedTeardownRequest(plan, true) == nil {
		t.Fatal("read-only plan bindings were confused with mutation authorization")
	}
	for _, mutate := range []func(*CatalogPRDScopedTeardownRequest){
		func(r *CatalogPRDScopedTeardownRequest) { r.TeardownPair += ":extra" },
		func(r *CatalogPRDScopedTeardownRequest) { r.ExpectedConfigurationSHA256 = "" },
		func(r *CatalogPRDScopedTeardownRequest) { r.ExpectedTargetOwnershipSHA256 = "" },
		func(r *CatalogPRDScopedTeardownRequest) { r.ExpectedPreservedInventorySHA256 = "" },
		func(r *CatalogPRDScopedTeardownRequest) { r.ExpectedPreservedOwnershipSHA256 = "" },
		func(r *CatalogPRDScopedTeardownRequest) { r.BackupDirectory = "relative" },
		func(r *CatalogPRDScopedTeardownRequest) { r.ExpectedBackupManifestSHA = "not-a-digest" },
	} {
		changed := valid
		mutate(&changed)
		if ValidateCatalogPRDScopedTeardownRequest(changed, true) == nil {
			t.Fatal("incomplete or ambiguous apply accepted")
		}
	}
}

func TestCatalogPRDScopedTeardownDeltaPreservesOtherRetainedCapacity(t *testing.T) {
	before := CatalogWorkInventory{ActiveSessionReservations: 9, ActiveCommandSlots: 1}
	after := CatalogWorkInventory{ActiveSessionReservations: 8, ActiveCommandSlots: 1}
	if !scopedTeardownWorkDelta(before, after, false) || catalogPRDRecoveryWorkMatches(before, "completed_teardown_pairs", 1) {
		t.Fatal("new exact delta failed, or existing whole-set refusal was weakened")
	}
	for _, mutate := range []func(*CatalogWorkInventory){
		func(w *CatalogWorkInventory) { w.ActiveSessionReservations = 0 },
		func(w *CatalogWorkInventory) { w.ActiveCommandSlots = 0 },
		func(w *CatalogWorkInventory) { w.ActiveSessions = 1 },
		func(w *CatalogWorkInventory) { w.RunningCommands = 1 },
		func(w *CatalogWorkInventory) { w.QueuedCommands = 1 },
		func(w *CatalogWorkInventory) { w.ResumableJobs = 1 },
		func(w *CatalogWorkInventory) { w.PendingRecoveryFinalizations = 1 },
	} {
		changed := after
		mutate(&changed)
		if scopedTeardownWorkDelta(before, changed, false) {
			t.Fatal("collateral release or unsafe postflight accepted")
		}
	}
	pending := after
	pending.PendingRecoveryFinalizations = 1
	if !scopedTeardownWorkDelta(pending, after, true) {
		t.Fatal("target-only finalization retry demanded another reservation release")
	}
}

func TestScopedTeardownLifecycleLockNeverCreatesOrRepairsMissingState(t *testing.T) {
	root := catalogTestTempDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runner-locald-lifecycle.lock")
	if _, err := acquireScopedTeardownLifecycleLock(path); err == nil {
		t.Fatal("missing lifecycle lock was created")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing lock changed")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := acquireScopedTeardownLifecycleLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireScopedTeardownLifecycleLock(path); err == nil {
		t.Fatal("held lock accepted")
	}
	releasePRDUpgradeLock(fd)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireScopedTeardownLifecycleLock(path); err == nil {
		t.Fatal("unsafe lock mode accepted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatal("refusal repaired lock mode")
	}
}

func TestScopedTeardownRequiresBothUnloadedLabelsAndAbsentSockets(t *testing.T) {
	plan := PRDUpgradePlan{Namespace: config.LaneNamespace{APISocket: "/private/fixture/api.sock", LocalDSocket: "/private/fixture/locald.sock"}}
	var labels []string
	absent := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if requireScopedTeardownServicesStoppedWith(plan, func(label string) (bool, error) { labels = append(labels, label); return false, nil }, absent) != nil || len(labels) != 2 {
		t.Fatal("complete stopped proof refused")
	}
	if requireScopedTeardownServicesStoppedWith(plan, func(string) (bool, error) { return true, nil }, absent) == nil {
		t.Fatal("loaded label accepted")
	}
	if requireScopedTeardownServicesStoppedWith(plan, func(string) (bool, error) { return false, nil }, func(string) (os.FileInfo, error) { return nil, os.ErrPermission }) == nil {
		t.Fatal("uninspectable socket accepted")
	}
}

func TestScopedTeardownOfflineCurrentOpenAcceptsCleanStopWithoutSidecars(t *testing.T) {
	root := catalogTestTempDir(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "authority.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Fixture-only: all writers are closed and SQLite's clean-close state is
	// checkpointed. The operator never manually creates/removes live sidecars.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if _, err := retainedInventorySidecarIdentities(path); err == nil {
		t.Fatal("fixture retained sidecars")
	}
	db, err = store.OpenExistingCurrent(context.Background(), path)
	if err != nil {
		t.Fatalf("exclusive offline no-migration opener required preexisting sidecars: %v", err)
	}
	defer db.Close()
	var schema int
	if db.QueryRow("PRAGMA user_version").Scan(&schema) != nil || schema != 40 {
		t.Fatal("offline opener changed schema")
	}
}

func scopedArchiveFixture(t *testing.T) (PRDUpgradePlan, CatalogPRDScopedTeardownRequest, hostruntime.MacScopedTeardownOwnershipSnapshot) {
	t.Helper()
	root := catalogTestTempDir(t)
	workspaces, backups := filepath.Join(root, "w"), filepath.Join(root, "b")
	for _, path := range []string{workspaces, backups, filepath.Join(workspaces, ".runner-runtime-ownership"), filepath.Join(workspaces, "target")} {
		if os.Mkdir(path, 0o700) != nil {
			t.Fatal("create archive fixture")
		}
	}
	target := hostruntime.RuntimeOwnershipRecord{Version: 1, HostOS: runtime.GOOS, SessionID: "sess-0123456789abcdef0123456789abcdef", Generation: "fixture-generation", Workspace: filepath.Join(workspaces, "target"), OwnedWorkspace: true, PID: 999999, ProcessGroupID: 999999, UID: os.Getuid(), Username: "fixture-user", Command: "/bin/bash", ProcessStartIdentity: "123456789"}
	var info unix.Stat_t
	if unix.Lstat(target.Workspace, &info) != nil {
		t.Fatal("inspect fixture workspace")
	}
	identity := hostruntime.MacScopedTeardownWorkspaceIdentity{Path: target.Workspace, Present: true, Device: uint64(info.Dev), Inode: uint64(info.Ino), UID: info.Uid, Mode: uint32(info.Mode & 0o777)}
	raw, _ := json.Marshal(target)
	digest := sha256BytesForScopedTest([]byte(target.SessionID))
	if os.WriteFile(filepath.Join(workspaces, ".runner-runtime-ownership", digest+".json"), raw, 0o600) != nil {
		t.Fatal("write fixture marker")
	}
	bound, err := hostruntime.MacScopedTeardownTargetIdentitySHA256(target, identity)
	if err != nil {
		t.Fatal(err)
	}
	request := validScopedTeardownRequest()
	request.BackupDirectory = backups
	request.ExpectedTargetOwnershipSHA256 = bound
	return PRDUpgradePlan{Namespace: config.LaneNamespace{Workspaces: workspaces}}, request, hostruntime.MacScopedTeardownOwnershipSnapshot{Target: target, TargetPresent: true, TargetSHA256: hashScopedBytes(raw), TargetWorkspaceIdentity: identity, TargetIdentitySHA256: bound}
}

func sha256BytesForScopedTest(raw []byte) string { return hashScopedBytes(raw) }

func TestScopedTeardownArchivePreservesMarkerAndNeverFollowsSpecialNodes(t *testing.T) {
	plan, request, snapshot := scopedArchiveFixture(t)
	workspace := snapshot.Target.Workspace
	if os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("exact-target-output"), 0o600) != nil {
		t.Fatal("write fixture output")
	}
	outside := filepath.Join(filepath.Dir(workspace), "outside")
	if os.WriteFile(outside, []byte("OUTSIDE-PAYLOAD-MUST-NOT-BE-READ"), 0o600) != nil || os.Symlink(outside, filepath.Join(workspace, "link")) != nil {
		t.Fatal("write fixture link")
	}
	if unix.Mkfifo(filepath.Join(workspace, "fifo"), 0o600) != nil {
		t.Fatal("write fixture FIFO")
	}
	t.Chdir(workspace)
	listener, err := net.Listen("unix", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	directory, err := archiveScopedTeardownMarker(plan, request, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifestSHA, err := archiveScopedTeardownWorkspace(context.Background(), directory, snapshot.Target, request.ExpectedTargetOwnershipSHA256)
	if err != nil || !prdDigestPattern.MatchString(manifestSHA) {
		t.Fatalf("archive regular/FIFO/socket metadata: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "workspace.tar"))
	if err != nil || bytes.Contains(raw, []byte("OUTSIDE-PAYLOAD-MUST-NOT-BE-READ")) {
		t.Fatal("archive followed a symlink")
	}
	reader := tar.NewReader(bytes.NewReader(raw))
	var output []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "result.txt" {
			output, err = io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if string(output) != "exact-target-output" {
		t.Fatal("target bytes not preserved")
	}
	if repeat, err := archiveScopedTeardownWorkspace(context.Background(), directory, snapshot.Target, request.ExpectedTargetOwnershipSHA256); err != nil || repeat != manifestSHA {
		t.Fatal("archive repeat was not idempotent")
	}
	if os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("changed"), 0o600) != nil {
		t.Fatal("modify fixture")
	}
	if _, err := archiveScopedTeardownWorkspace(context.Background(), directory, snapshot.Target, request.ExpectedTargetOwnershipSHA256); err == nil {
		t.Fatal("stale archive accepted changed target bytes")
	}
}

func TestScopedArchiveRegularReaderRefusesSymlinkAndFIFOBeforeReading(t *testing.T) {
	root := catalogTestTempDir(t)
	if unix.Mkfifo(filepath.Join(root, "fifo"), 0o600) != nil {
		t.Fatal("create FIFO")
	}
	if os.Symlink(root, filepath.Join(root, "link")) != nil {
		t.Fatal("create symlink")
	}
	done := make(chan error, 1)
	go func() { _, _, err := openScopedArchiveRegular(root, "fifo"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO was read as regular input")
		}
	case <-time.After(time.Second):
		t.Fatal("archive reader blocked on FIFO")
	}
	if _, _, err := openScopedArchiveRegular(root, "link/nonexistent"); err == nil {
		t.Fatal("archive followed directory symlink")
	}
}
