package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	runtimeinfo "runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type macScopedFixture struct {
	root     string
	expected []MacScopedTeardownSession
	records  []RuntimeOwnershipRecord
}

func newMacScopedFixture(t *testing.T, count int) macScopedFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	fixture := macScopedFixture{root: root}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("sess-scoped-%02d", index)
		generation := fmt.Sprintf("generation-scoped-%02d", index)
		workspace := filepath.Join(root, fmt.Sprintf("workspace-%02d", index))
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "preserved.txt"), []byte(id+" original bytes\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		record := RuntimeOwnershipRecord{
			Version: runtimeOwnershipVersion, HostOS: runtimeinfo.GOOS,
			SessionID: id, Generation: generation, Workspace: workspace, OwnedWorkspace: true,
			PID: 100000 + index, ProcessGroupID: 100000 + index,
			UID: os.Getuid(), Username: account.Username, Command: "/bin/bash",
			ProcessStartIdentity: strconv.FormatInt(1700000000000000+int64(index), 10),
		}
		if err := writeRuntimeOwnership(root, record); err != nil {
			t.Fatal(err)
		}
		fixture.records = append(fixture.records, record)
		fixture.expected = append(fixture.expected, MacScopedTeardownSession{SessionID: id, Generation: generation, SourceMode: "empty"})
	}
	return fixture
}

func (fixture macScopedFixture) read(t *testing.T) MacScopedTeardownOwnershipSnapshot {
	t.Helper()
	result, err := ReadMacScopedTeardownOwnership(context.Background(), fixture.root, fixture.records[0].SessionID, fixture.expected)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (fixture macScopedFixture) path(t *testing.T, index int) string {
	t.Helper()
	path, err := runtimeOwnershipPath(fixture.root, fixture.records[index].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func (fixture macScopedFixture) rewrite(t *testing.T, index int, change func(*RuntimeOwnershipRecord)) {
	t.Helper()
	record := fixture.records[index]
	change(&record)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.path(t, index), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func macScopedPeerState(t *testing.T, fixture macScopedFixture) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for index := 1; index < len(fixture.records); index++ {
		path := fixture.path(t, index)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = fmt.Sprintf("%x/%o/%d", sha256.Sum256(raw), info.Mode().Perm(), info.ModTime().UnixNano())
		content, err := os.ReadFile(filepath.Join(fixture.records[index].Workspace, "preserved.txt"))
		if err != nil {
			t.Fatal(err)
		}
		result[fixture.records[index].Workspace] = string(content)
	}
	return result
}

type macScopedObserver struct {
	root         func(int) (bool, error)
	group        func(int) (bool, error)
	calls        []string
	signalCalls  int // There is intentionally no signalling method in the observer contract.
	inspectionID int
}

func (observer *macScopedObserver) RootAbsent(_ context.Context, record RuntimeOwnershipRecord) (bool, error) {
	observer.calls = append(observer.calls, "root")
	observer.inspectionID = record.PID
	if observer.root != nil {
		return observer.root(record.PID)
	}
	return true, nil
}

func (observer *macScopedObserver) GroupEmpty(_ context.Context, id int) (bool, error) {
	observer.calls = append(observer.calls, "group")
	observer.inspectionID = id
	if observer.group != nil {
		return observer.group(id)
	}
	return true, nil
}

func macScopedNow() time.Time { return time.Date(2026, 11, 14, 18, 0, 0, 123, time.UTC) }

func TestMacScopedTeardownReadAndProofPreserveEightPeerMarkers(t *testing.T) {
	fixture := newMacScopedFixture(t, 9)
	before := macScopedPeerState(t, fixture)
	snapshot := fixture.read(t)
	if !snapshot.TargetPresent || snapshot.Target != fixture.records[0] || len(snapshot.PreservedSHA256) != 64 || len(snapshot.TargetSHA256) != 64 || len(snapshot.TargetIdentitySHA256) != 64 {
		t.Fatalf("incomplete bound snapshot: %+v", snapshot)
	}
	targetIdentity := snapshot.TargetIdentitySHA256
	targetRaw := snapshot.TargetSHA256
	if again := fixture.read(t); again.PreservedSHA256 != snapshot.PreservedSHA256 || !reflect.DeepEqual(before, macScopedPeerState(t, fixture)) {
		t.Fatal("read-only snapshot changed peer state or fingerprint")
	}
	observer := &macScopedObserver{}
	readOnlyTarget, _ := os.ReadFile(fixture.path(t, 0))
	if err := snapshot.checkExited(context.Background(), &macScopedObserver{}); err != nil {
		t.Fatalf("read-only exit check failed: %v", err)
	}
	readOnlyAfter, _ := os.ReadFile(fixture.path(t, 0))
	if string(readOnlyAfter) != string(readOnlyTarget) || !reflect.DeepEqual(before, macScopedPeerState(t, fixture)) {
		t.Fatal("read-only exit check changed selected or preserved state")
	}
	marked, err := snapshot.confirmExited(context.Background(), observer, macScopedNow)
	if err != nil || marked.LostRecoveryCleanupConfirmedAt != macScopedNow().Format(time.RFC3339Nano) {
		t.Fatalf("proof failed: %+v err=%v", marked, err)
	}
	if !reflect.DeepEqual(observer.calls, []string{"root", "group", "root"}) || observer.inspectionID != fixture.records[0].PID || observer.signalCalls != 0 {
		t.Fatalf("proof made wrong process operations: %+v", observer)
	}
	if !reflect.DeepEqual(before, macScopedPeerState(t, fixture)) {
		t.Fatal("target proof changed a preserved peer")
	}
	if snapshot.PreservedSHA256 != fixture.read(t).PreservedSHA256 {
		t.Fatal("selected proof stamp changed peer fingerprint")
	}
	if snapshot.TargetIdentitySHA256 != targetIdentity || snapshot.TargetSHA256 == targetRaw {
		t.Fatal("proof stamp changed immutable target identity or failed to change raw marker")
	}
	if _, err := os.Stat(fixture.records[0].Workspace); err != nil {
		t.Fatal("proof removed target workspace before capacity release")
	}
	// The operator must establish durable release before invoking this boundary.
	if err := snapshot.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot.TargetPresent {
		t.Fatal("finalization retained selected marker")
	}
	for _, path := range []string{fixture.path(t, 0), fixture.records[0].Workspace} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("selected finalization retained %s: %v", path, err)
		}
	}
	if !reflect.DeepEqual(before, macScopedPeerState(t, fixture)) || observer.signalCalls != 0 {
		t.Fatal("finalization changed peers or signalled a process")
	}
	if err := snapshot.Finalize(context.Background()); err != nil {
		t.Fatalf("idempotent target finalization failed: %v", err)
	}
}

func TestMacScopedTeardownOriginalTargetIdentitySurvivesOnlyPermittedProgress(t *testing.T) {
	fixture := newMacScopedFixture(t, 2)
	snapshot := fixture.read(t)
	originalRecord := snapshot.Target
	originalWorkspace := snapshot.TargetWorkspaceIdentity
	originalHash := snapshot.TargetIdentitySHA256
	if _, err := snapshot.confirmExited(context.Background(), &macScopedObserver{}, macScopedNow); err != nil {
		t.Fatal(err)
	}
	fromArchive, err := MacScopedTeardownTargetIdentitySHA256(snapshot.Target, originalWorkspace)
	if err != nil || fromArchive != originalHash {
		t.Fatalf("archived identity was not stable across proof stamp: %s err=%v", fromArchive, err)
	}
	changedWorkspace := originalWorkspace
	changedWorkspace.Inode++
	changed, err := MacScopedTeardownTargetIdentitySHA256(originalRecord, changedWorkspace)
	if err != nil || changed == originalHash {
		t.Fatal("replacement workspace inode retained original identity")
	}
	if err := os.RemoveAll(fixture.records[0].Workspace); err != nil {
		t.Fatal(err)
	}
	retry := fixture.read(t)
	if retry.TargetWorkspaceIdentity.Present || retry.TargetIdentitySHA256 != "" {
		t.Fatal("missing workspace invented an original identity")
	}
	fromArchive, err = MacScopedTeardownTargetIdentitySHA256(retry.Target, originalWorkspace)
	if err != nil || fromArchive != originalHash {
		t.Fatalf("released retry cannot verify preserved original identity: %s err=%v", fromArchive, err)
	}
}

func TestMacScopedTeardownProofTemporaryArtifactsStayInsideSelectedWorkspace(t *testing.T) {
	fixture := newMacScopedFixture(t, 2)
	// A process crash before the proof rename can leave only this selected
	// scratch artifact. It must not create an unknown shared owner entry.
	leftover := filepath.Join(fixture.records[0].Workspace, ".runner-scoped-proof-interrupted.tmp")
	if err := os.WriteFile(leftover, []byte("interrupted staging"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := fixture.read(t)
	if _, err := snapshot.confirmExited(context.Background(), &macScopedObserver{}, macScopedNow); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.root, runtimeOwnershipDirectory))
	if err != nil || len(entries) != 2 {
		t.Fatalf("proof staging polluted shared owner directory: count=%d err=%v", len(entries), err)
	}
	if err := snapshot.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selected workspace finalization retained only-owned staging artifact")
	}
}

func TestMacScopedTeardownRefusesUnsafeOrUnboundOwnershipWithoutRepair(t *testing.T) {
	for _, name := range []string{"directory-mode", "file-mode", "hardlink", "symlink", "missing-peer", "unknown-entry", "orphan", "shared-pgid", "equal-workspace", "nested-workspace", "aliased-workspace", "generation", "incarnation", "duplicate-json-field", "control-directory-workspace"} {
		t.Run(name, func(t *testing.T) {
			fixture := newMacScopedFixture(t, 2)
			ownerDirectory := filepath.Join(fixture.root, runtimeOwnershipDirectory)
			switch name {
			case "directory-mode":
				if err := os.Chmod(ownerDirectory, 0o755); err != nil {
					t.Fatal(err)
				}
			case "file-mode":
				if err := os.Chmod(fixture.path(t, 1), 0o644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(fixture.path(t, 1), filepath.Join(fixture.root, "hardlinked-marker")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				path := fixture.path(t, 1)
				if err := os.Rename(path, filepath.Join(fixture.root, "saved-marker")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(fixture.root, "saved-marker"), path); err != nil {
					t.Fatal(err)
				}
			case "missing-peer":
				if err := os.Remove(fixture.path(t, 1)); err != nil {
					t.Fatal(err)
				}
			case "unknown-entry":
				if err := os.WriteFile(filepath.Join(ownerDirectory, ".unknown.tmp"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "orphan":
				extra := fixture.records[1]
				extra.SessionID = "sess-unattributed"
				if err := writeRuntimeOwnership(fixture.root, extra); err != nil {
					t.Fatal(err)
				}
			case "shared-pgid":
				fixture.rewrite(t, 0, func(r *RuntimeOwnershipRecord) { r.PID = fixture.records[1].PID; r.ProcessGroupID = r.PID })
			case "equal-workspace":
				fixture.rewrite(t, 0, func(r *RuntimeOwnershipRecord) { r.Workspace = fixture.records[1].Workspace })
			case "nested-workspace":
				path := filepath.Join(fixture.records[1].Workspace, "nested")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				fixture.rewrite(t, 0, func(r *RuntimeOwnershipRecord) { r.Workspace = path })
			case "aliased-workspace":
				alias := filepath.Join(fixture.root, "workspace-alias")
				if err := os.Symlink(fixture.records[0].Workspace, alias); err != nil {
					t.Fatal(err)
				}
				fixture.rewrite(t, 0, func(r *RuntimeOwnershipRecord) { r.Workspace = alias })
			case "generation":
				fixture.expected[1].Generation = "different-durable-generation"
			case "incarnation":
				fixture.rewrite(t, 0, func(r *RuntimeOwnershipRecord) { r.ProcessStartIdentity = "not-a-native-incarnation" })
			case "duplicate-json-field":
				raw, err := os.ReadFile(fixture.path(t, 0))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fixture.path(t, 0), append([]byte(`{"session_id":"ambiguous",`), raw[1:]...), 0o600); err != nil {
					t.Fatal(err)
				}
			case "control-directory-workspace":
				fixture.rewrite(t, 0, func(record *RuntimeOwnershipRecord) { record.Workspace = ownerDirectory })
			}
			if _, err := ReadMacScopedTeardownOwnership(context.Background(), fixture.root, fixture.records[0].SessionID, fixture.expected); err == nil {
				t.Fatal("unsafe ownership passed scoped read")
			}
			if name == "directory-mode" {
				info, _ := os.Stat(ownerDirectory)
				if info.Mode().Perm() != 0o755 {
					t.Fatal("read repaired directory permissions")
				}
			}
			if name == "file-mode" {
				info, _ := os.Stat(fixture.path(t, 1))
				if info.Mode().Perm() != 0o644 {
					t.Fatal("read repaired file permissions")
				}
			}
		})
	}
}

func TestMacScopedTeardownProofRefusesLiveNonemptyDeniedAndRacedRootWithoutSignals(t *testing.T) {
	for _, name := range []string{"live-or-reused-root", "denied-root", "nonempty-or-zombie-group", "denied-group", "root-reappeared"} {
		t.Run(name, func(t *testing.T) {
			fixture := newMacScopedFixture(t, 2)
			snapshot := fixture.read(t)
			before, _ := os.ReadFile(fixture.path(t, 0))
			observer := &macScopedObserver{}
			switch name {
			case "live-or-reused-root":
				observer.root = func(int) (bool, error) { return false, nil }
			case "denied-root":
				observer.root = func(int) (bool, error) { return false, os.ErrPermission }
			case "nonempty-or-zombie-group":
				observer.group = func(int) (bool, error) { return false, nil }
			case "denied-group":
				observer.group = func(int) (bool, error) { return false, os.ErrPermission }
			case "root-reappeared":
				calls := 0
				observer.root = func(int) (bool, error) { calls++; return calls == 1, nil }
			}
			if _, err := snapshot.confirmExited(context.Background(), observer, macScopedNow); err == nil {
				t.Fatal("unproven process absence passed recovery proof")
			}
			readOnlyObserver := &macScopedObserver{root: observer.root, group: observer.group}
			// Use a fresh stateful root observer for this independent readonly
			// fixture rather than carrying the raced-root observer's call count.
			if name == "root-reappeared" {
				calls := 0
				readOnlyObserver.root = func(int) (bool, error) { calls++; return calls == 1, nil }
			}
			if err := snapshot.checkExited(context.Background(), readOnlyObserver); err == nil {
				t.Fatal("read-only check accepted unproven absence")
			}
			after, _ := os.ReadFile(fixture.path(t, 0))
			if string(after) != string(before) || observer.signalCalls != 0 {
				t.Fatal("refused proof changed marker or signalled a process")
			}
		})
	}
}

func TestMacScopedTeardownProofRevalidatesPeersButDoesNotFreezeTheirContents(t *testing.T) {
	for _, drift := range []bool{true, false} {
		t.Run(strconv.FormatBool(drift), func(t *testing.T) {
			fixture := newMacScopedFixture(t, 2)
			snapshot := fixture.read(t)
			observer := &macScopedObserver{group: func(int) (bool, error) {
				if drift {
					file, err := os.OpenFile(fixture.path(t, 1), os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						return false, err
					}
					_, err = file.WriteString("\n")
					_ = file.Close()
					return err == nil, err
				}
				return true, os.WriteFile(filepath.Join(fixture.records[1].Workspace, "peer-new-file"), []byte("peer-owned work"), 0o600)
			}}
			_, err := snapshot.confirmExited(context.Background(), observer, macScopedNow)
			if drift && err == nil {
				t.Fatal("peer-marker drift passed target proof")
			}
			if !drift && err != nil {
				t.Fatalf("peer content write incorrectly changed identity baseline: %v", err)
			}
			if observer.signalCalls != 0 {
				t.Fatal("proof signalled a process")
			}
		})
	}
}

func TestMacScopedTeardownInterruptedProofAndFinalizationNeverReinspectPID(t *testing.T) {
	for _, phase := range []string{"proof-before-release", "workspace-removed", "marker-removed"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newMacScopedFixture(t, 2)
			snapshot := fixture.read(t)
			baseline := snapshot.PreservedSHA256
			if _, err := snapshot.confirmExited(context.Background(), &macScopedObserver{}, macScopedNow); err != nil {
				t.Fatal(err)
			}
			if phase == "workspace-removed" || phase == "marker-removed" {
				if err := os.RemoveAll(fixture.records[0].Workspace); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "marker-removed" {
				if err := os.Remove(fixture.path(t, 0)); err != nil {
					t.Fatal(err)
				}
			}
			retry := fixture.read(t)
			if retry.PreservedSHA256 != baseline {
				t.Fatal("target progress changed peer baseline")
			}
			observer := &macScopedObserver{root: func(int) (bool, error) { t.Fatal("retry inspected possibly reused PID"); return false, nil }}
			if phase != "marker-removed" {
				if _, err := retry.confirmExited(context.Background(), observer, macScopedNow); err != nil {
					t.Fatal(err)
				}
			}
			// The operator gates this retry on its already-persisted release.
			if err := retry.Finalize(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(observer.calls) != 0 || observer.signalCalls != 0 {
				t.Fatal("durable proof retry inspected/signalled PID")
			}
		})
	}
}

func TestMacScopedTeardownFinalizationRefusesUnstampedOrUnsafeTarget(t *testing.T) {
	fixture := newMacScopedFixture(t, 2)
	snapshot := fixture.read(t)
	if err := snapshot.CheckFinalization(context.Background()); err != nil {
		t.Fatalf("safe unstamped target failed read-only applicability: %v", err)
	}
	if err := snapshot.Finalize(context.Background()); err == nil {
		t.Fatal("unstamped target finalized")
	}
	if _, err := snapshot.confirmExited(context.Background(), &macScopedObserver{}, macScopedNow); err != nil {
		t.Fatal(err)
	}
	peer := filepath.Join(fixture.records[1].Workspace, "preserved.txt")
	if err := os.Link(peer, filepath.Join(fixture.records[0].Workspace, "unsafe-hardlink")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(fixture.path(t, 0))
	if err := snapshot.CheckFinalization(context.Background()); err == nil {
		t.Fatal("aliased target passed read-only applicability")
	}
	after, _ := os.ReadFile(fixture.path(t, 0))
	if string(before) != string(after) {
		t.Fatal("read-only applicability changed selected proof")
	}
	if err := snapshot.Finalize(context.Background()); err == nil {
		t.Fatal("aliased target workspace finalized")
	}
	if _, err := os.Stat(peer); err != nil {
		t.Fatal("refused finalization changed peer")
	}
	if _, err := os.Stat(fixture.path(t, 0)); err != nil {
		t.Fatal("refused finalization removed target marker")
	}
}

func TestMacScopedTeardownReadCancellationAndMissingTargetAreNotProof(t *testing.T) {
	fixture := newMacScopedFixture(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadMacScopedTeardownOwnership(ctx, fixture.root, fixture.records[0].SessionID, fixture.expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	if err := os.Remove(fixture.path(t, 0)); err != nil {
		t.Fatal(err)
	}
	snapshot := fixture.read(t)
	if snapshot.TargetPresent {
		t.Fatal("missing selected marker presented as found")
	}
	if _, err := snapshot.confirmExited(context.Background(), &macScopedObserver{}, macScopedNow); err == nil {
		t.Fatal("missing marker became runtime proof")
	}
}

func TestMacScopedTeardownErrorDoesNotExposeHostPaths(t *testing.T) {
	_, err := ReadMacScopedTeardownOwnership(context.Background(), "/private/secret-path-that-does-not-exist", "selected", []MacScopedTeardownSession{{SessionID: "selected", Generation: "g", SourceMode: "empty"}})
	if err == nil || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("host path leaked: %v", err)
	}
}
