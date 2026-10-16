package laneregistry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneroot"
)

func TestLanesL104OwnerOnlyDurableJSONRestartWithoutSecondAuthority(t *testing.T) {
	path := fixtureRegistryPath(t)
	registry, err := Initialize(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Snapshot()
	if err != nil || snapshot.Version != FormatVersion || snapshot.Generation != 0 || len(snapshot.Records) != 0 {
		t.Fatalf("initial snapshot = %+v, err=%v", snapshot, err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("registry directory mode = %v, err=%v", info, err)
	}
	for name := range map[string]struct{}{RegistryFile: {}, LockFile: {}} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, err=%v", name, info, err)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("registry entries = %v, err=%v; want only JSON and lock", entries, err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if recovered, err := reopened.Snapshot(); err != nil || recovered.Generation != snapshot.Generation || len(recovered.Records) != 0 {
		t.Fatalf("reopened snapshot = %+v, err=%v", recovered, err)
	}
}

func TestLanesL104CompareAndSwapSerializesConcurrentClaimsAndOwnRetry(t *testing.T) {
	path := fixtureRegistryPath(t)
	first, err := Initialize(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	records := []RootRecord{
		fixtureRecord("dev-claim-a", config.ReleaseLaneDev, "inbox-a", "/Volumes/runner-dev-a", 101, 1, StatePrepared),
		fixtureRecord("dev-claim-b", config.ReleaseLaneDev, "inbox-b", "/Volumes/runner-dev-b", 102, 1, StatePrepared),
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index, registry := range []*Registry{first, second} {
		wait.Add(1)
		go func(index int, registry *Registry) {
			defer wait.Done()
			<-start
			_, err := registry.Prepare(0, records[index])
			results <- err
		}(index, registry)
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrGenerationConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Prepare error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent claims successes=%d conflicts=%d", successes, conflicts)
	}
	snapshot, err := first.Snapshot()
	if err != nil || snapshot.Generation != 1 || len(snapshot.Records) != 1 {
		t.Fatalf("serialized state = %+v, err=%v", snapshot, err)
	}
	prepared := snapshot.Records[0]
	if _, err := first.Prepare(snapshot.Generation, prepared); err != nil {
		t.Fatalf("matching prepared retry rejected: %v", err)
	}
	afterRetry, err := first.Snapshot()
	if err != nil || afterRetry.Generation != snapshot.Generation {
		t.Fatalf("matching retry changed generation: %+v, err=%v", afterRetry, err)
	}
	changed := prepared
	changed.ConfigurationDigest = testDigest('b')
	if _, err := first.Prepare(afterRetry.Generation, changed); !errors.Is(err, ErrCollision) {
		t.Fatalf("nonmatching retry error = %v, want collision", err)
	}
}

func TestLanesL104EveryRetainedStateRemainsInCollisionSet(t *testing.T) {
	states := []RecordState{StateLegacyPRDReserved, StatePrepared, StateActive, StateSuperseded, StateRetired}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			lane := config.ReleaseLaneDev
			if state == StateLegacyPRDReserved {
				lane = config.ReleaseLanePRD
			}
			prior := fixtureRecord("claim-a", lane, "inbox-a", "/Volumes/runner-root", 201, 1, state)
			next := fixtureRecord("claim-b", lane, "inbox-b", "/Volumes/runner-root", 201, 1, StateRetired)
			if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Generation: 2, Records: []RootRecord{prior, next}}); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("collision with %s tombstone returned %v", state, err)
			}
		})
	}

	t.Run("nested paths", func(t *testing.T) {
		dev := fixtureRecord("claim-dev", config.ReleaseLaneDev, "inbox-dev", "/Volumes/runner", 211, 1, StateActive)
		qas := fixtureRecord("claim-qas", config.ReleaseLaneQAS, "inbox-qas", "/Volumes/runner/qas", 212, 1, StateRetired)
		if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Generation: 2, Records: []RootRecord{dev, qas}}); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("nested root returned %v", err)
		}
	})
	t.Run("descriptor alias", func(t *testing.T) {
		dev := fixtureRecord("claim-dev", config.ReleaseLaneDev, "inbox-dev", "/Volumes/runner-dev", 221, 1, StateActive)
		qas := fixtureRecord("claim-qas", config.ReleaseLaneQAS, "inbox-qas", "/Volumes/runner-qas", 221, 1, StateRetired)
		if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Generation: 2, Records: []RootRecord{dev, qas}}); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("descriptor alias returned %v", err)
		}
	})
}

func TestLanesL104SnapshotAndAncillaryShapeFailClosed(t *testing.T) {
	record := fixtureRecord("dev-active", config.ReleaseLaneDev, "inbox-dev", "/Volumes/runner-dev", 231, 1, StateActive)
	if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Records: []RootRecord{record}}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("record without a generation returned %v", err)
	}
	snapshot := Snapshot{Version: FormatVersion, Generation: 1, Records: []RootRecord{record}}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("valid single-record snapshot rejected: %v", err)
	}
	unsafe := record
	unsafe.AncillaryEntries = []laneroot.AncillaryEntrySnapshot{{
		Name: "inbox", Kind: laneroot.EntryDirectory,
		Identity: laneroot.FileIdentity{Device: record.RootIdentity.Device, Inode: 232},
		OwnerUID: uint32(os.Getuid()), Mode: 0o700,
	}}
	snapshot.Records = []RootRecord{unsafe}
	if err := ValidateSnapshot(snapshot); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("fixed mailbox child declared as ancillary returned %v", err)
	}
}

func TestLanesL104SuccessorsRetainHistoryAndRequireHigherEpochReconciliation(t *testing.T) {
	registry, err := Initialize(fixtureRegistryPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	first := fixtureRecord("dev-epoch-one", config.ReleaseLaneDev, "inbox-dev", "/Volumes/runner-dev", 301, 1, StatePrepared)
	snapshot, err := registry.Prepare(0, first)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.Activate(snapshot.Generation, first.ID, reconciliationFor(first))
	if err != nil {
		t.Fatal(err)
	}

	second := first
	second.ID, second.Epoch, second.State, second.Supersedes = "dev-epoch-two", 2, StatePrepared, first.ID
	second.ConfigurationDigest = testDigest('2')
	snapshot, err = registry.Prepare(snapshot.Generation, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Activate(snapshot.Generation, second.ID, RuntimeReconciliation{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("activation without service reconciliation returned %v", err)
	}
	wrongProof := reconciliationFor(second)
	wrongProof.ConfigurationDigest = testDigest('f')
	if _, err := registry.Activate(snapshot.Generation, second.ID, wrongProof); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("activation with mismatched reconciliation returned %v", err)
	}
	snapshot, err = registry.Activate(snapshot.Generation, second.ID, reconciliationFor(second))
	if err != nil {
		t.Fatal(err)
	}

	rollback := second
	rollback.ID, rollback.Epoch, rollback.State, rollback.Supersedes = "dev-rollback-epoch-three", 3, StatePrepared, second.ID
	rollback.ConfigurationDigest = first.ConfigurationDigest
	snapshot, err = registry.Prepare(snapshot.Generation, rollback)
	if err != nil {
		t.Fatalf("higher-epoch rollback candidate rejected: %v", err)
	}
	snapshot, err = registry.Activate(snapshot.Generation, rollback.ID, reconciliationFor(rollback))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 6 || len(snapshot.Records) != 3 {
		t.Fatalf("successor snapshot = %+v", snapshot)
	}
	states := make(map[string]RecordState, len(snapshot.Records))
	for _, record := range snapshot.Records {
		states[record.ID] = record.State
	}
	if states[first.ID] != StateSuperseded || states[second.ID] != StateSuperseded || states[rollback.ID] != StateActive {
		t.Fatalf("successor history states = %v", states)
	}
	if _, err := registry.Activate(snapshot.Generation, second.ID, reconciliationFor(second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("historical record reactivation returned %v", err)
	}
}

func TestLanesL104LegacyPRDReservationRefreshPreservesV2AndAllowsSuccessor(t *testing.T) {
	registry, err := Initialize(fixtureRegistryPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	legacyRoot := filepath.Join(fixtureDirectory(t), "retained-prd")
	if err := os.Mkdir(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(legacyRoot, "v2-config.json")
	if err := os.WriteFile(sentinel, []byte("retained V2 bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := fixtureRecord("prd-legacy-one", config.ReleaseLanePRD, "inbox-prd", legacyRoot, 401, 1, StateLegacyPRDReserved)
	snapshot, err := registry.ReserveLegacyPRD(0, legacy)
	if err != nil {
		t.Fatal(err)
	}
	ancillary := []laneroot.AncillaryEntrySnapshot{{
		Name: "v2-config.json", Kind: laneroot.EntryRegularFile,
		Identity: laneroot.FileIdentity{Device: legacy.RootIdentity.Device, Inode: 402},
		OwnerUID: uint32(os.Getuid()), Mode: 0o600,
	}}
	snapshot, err = registry.RefreshLegacyPRD(snapshot.Generation, legacy.ID, "prd-legacy-two", testDigest('2'), ancillary)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Records[0].State != StateSuperseded || snapshot.Records[1].State != StateLegacyPRDReserved {
		t.Fatalf("refreshed legacy history = %+v", snapshot.Records)
	}

	candidate := fixtureRecord("prd-v3-candidate", config.ReleaseLanePRD, legacy.InboxID, legacyRoot, 401, 3, StatePrepared)
	candidate.Supersedes = "prd-legacy-two"
	candidate.ConfigurationDigest = testDigest('3')
	snapshot, err = registry.Prepare(snapshot.Generation, candidate)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.Activate(snapshot.Generation, candidate.ID, reconciliationFor(candidate))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Records[1].State != StateSuperseded || snapshot.Records[2].State != StateActive {
		t.Fatalf("PRD successor history = %+v", snapshot.Records)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "retained V2 bytes\n" {
		t.Fatalf("V2 sentinel changed: %q, err=%v", contents, err)
	}
}

func TestLanesL104MissingCorruptNoncanonicalAndUnsafeRegistryFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) error
	}{
		{name: "missing file", mutate: func(path string) error { return os.Remove(filepath.Join(path, RegistryFile)) }},
		{name: "truncated", mutate: func(path string) error {
			return os.WriteFile(filepath.Join(path, RegistryFile), []byte("{\"version\":"), 0o600)
		}},
		{name: "unknown field", mutate: func(path string) error {
			return os.WriteFile(filepath.Join(path, RegistryFile), []byte("{\"unknown\":1}"), 0o600)
		}},
		{name: "duplicate key", mutate: func(path string) error {
			return os.WriteFile(filepath.Join(path, RegistryFile), []byte("{\"version\":1,\"version\":1,\"generation\":0,\"records\":[]}"), 0o600)
		}},
		{name: "unsafe mode", mutate: func(path string) error { return os.Chmod(filepath.Join(path, RegistryFile), 0o644) }},
		{name: "symlink file", mutate: func(path string) error {
			file := filepath.Join(path, RegistryFile)
			backup := filepath.Join(path, "saved.json")
			if err := os.Rename(file, backup); err != nil {
				return err
			}
			return os.Symlink(backup, file)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := fixtureRegistryPath(t)
			registry, err := Initialize(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Close(); err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(path); err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(path); err == nil {
				opened.Close()
				t.Fatal("Open accepted missing, corrupt, or unsafe registry state")
			}
			if test.name == "missing file" {
				if _, err := os.Stat(filepath.Join(path, RegistryFile)); !os.IsNotExist(err) {
					t.Fatalf("Open recreated missing registry: stat err=%v", err)
				}
			}
		})
	}

	t.Run("symlink directory", func(t *testing.T) {
		path := fixtureRegistryPath(t)
		registry, err := Initialize(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		alias := path + "-alias"
		if err := os.Symlink(path, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(alias); err == nil {
			t.Fatal("Open followed a registry-directory symlink")
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		path := filepath.Join(fixtureDirectory(t), "never-created", "registry")
		if _, err := Open(path); err == nil {
			t.Fatal("Open accepted a missing registry directory")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Open created missing registry state: stat err=%v", err)
		}
	})
}

func fixtureDirectory(t *testing.T) string {
	t.Helper()
	raw := t.TempDir()
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	return resolved
}

func fixtureRegistryPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(fixtureDirectory(t), "registry")
}

func fixtureRecord(id string, lane config.ReleaseLane, inbox, root string, inode, epoch uint64, state RecordState) RootRecord {
	record := RootRecord{
		ID: id, Host: config.HostKindMac, Lane: lane, InboxID: inbox,
		NormalizedRoot: root, RootIdentity: laneroot.FileIdentity{Device: 77, Inode: inode},
		ConfigurationDigest: testDigest('1'), Epoch: epoch, State: state,
	}
	if lane == config.ReleaseLaneDev || lane == config.ReleaseLaneQAS {
		record.Volume = &laneroot.VolumeIdentity{
			Mountpoint: root, UUID: "11111111-2222-3333-4444-555555555555", Filesystem: "apfs",
			Device: record.RootIdentity.Device, QuotaBytes: 20 << 30,
		}
	}
	return record
}

func reconciliationFor(record RootRecord) RuntimeReconciliation {
	return RuntimeReconciliation{
		RecordID: record.ID, Lane: record.Lane, InboxID: record.InboxID,
		RootIdentity: record.RootIdentity, ConfigurationDigest: record.ConfigurationDigest,
		Epoch: record.Epoch, ServiceReconciled: true,
	}
}

func testDigest(character byte) string { return "sha256:" + strings.Repeat(string(character), 64) }
