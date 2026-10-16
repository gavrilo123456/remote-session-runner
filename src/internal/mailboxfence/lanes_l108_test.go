package mailboxfence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLanesL108ExclusiveSnapshotBindsCompleteRequestAndAckPairs(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	emptyLock, err := AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	emptySnapshot, err := emptyLock.SnapshotIngressPairs("default")
	if err != nil || len(emptySnapshot.Pairs) != 0 || emptySnapshot.RequireEmpty() != nil {
		_ = emptyLock.Close()
		t.Fatalf("empty exclusive snapshot=%+v err=%v", emptySnapshot, err)
	}
	if err := emptyLock.Close(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		directory string
		id        string
		body      string
	}{
		{directory: "inbox", id: "req-l108-request", body: `{"request_id":"req-l108-request"}`},
		{directory: "acks", id: "req-l108-ack", body: `{"request_id":"req-l108-ack","response_revision":2}`},
	} {
		if err := os.WriteFile(filepath.Join(root, item.directory, item.id+".json"), []byte(item.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, item.directory, item.id+".ready"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	shared, err := AcquireShared(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.SnapshotIngressPairs("default"); !errors.Is(err, ErrSnapshotRequiresExclusive) {
		t.Fatalf("shared-lock snapshot error=%v, want %v", err, ErrSnapshotRequiresExclusive)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}

	exclusive, err := AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive.Close()
	snapshot, err := exclusive.SnapshotIngressPairs("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pairs) != 2 {
		t.Fatalf("snapshot pair count=%d, want request+ACK: %+v", len(snapshot.Pairs), snapshot.Pairs)
	}
	if err := snapshot.RequireEmpty(); !errors.Is(err, ErrResidue) {
		t.Fatalf("nonempty snapshot empty-gate error=%v, want %v", err, ErrResidue)
	}
	request, ack := snapshot.Pairs[0], snapshot.Pairs[1]
	if request.Kind != "ack" || request.LogicalID != "req-l108-ack" || request.JSONPath != "acks/req-l108-ack.json" || request.JSONSHA256 == "" || request.JSON.Inode == 0 || request.Ready.Size != 0 {
		t.Fatalf("ACK snapshot is not descriptor/digest bound: %+v", request)
	}
	if ack.Kind != "request" || ack.LogicalID != "req-l108-request" || ack.JSONPath != "inbox/req-l108-request.json" || ack.JSONSHA256 == "" || ack.JSON.Inode == 0 || ack.Ready.Size != 0 {
		t.Fatalf("request snapshot is not descriptor/digest bound: %+v", ack)
	}
}

func TestLanesL108ExclusiveSnapshotRejectsIncompleteOrUnsafePairs(t *testing.T) {
	for _, fixture := range []struct {
		name        string
		json        bool
		ready       bool
		symlinkJSON bool
		want        error
	}{
		{name: "json without ready", json: true, want: ErrResidue},
		{name: "ready without json", ready: true, want: ErrResidue},
		{name: "symlinked JSON", json: true, ready: true, symlinkJSON: true, want: ErrUnsafe},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			trustRoot := t.TempDir()
			root := newFenceMailboxTree(t, trustRoot)
			jsonPath := filepath.Join(root, "inbox", "req-l108-incomplete.json")
			readyPath := filepath.Join(root, "inbox", "req-l108-incomplete.ready")
			if fixture.json {
				if fixture.symlinkJSON {
					if err := os.WriteFile(filepath.Join(trustRoot, "outside.json"), []byte(`{"request_id":"outside"}`), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(trustRoot, "outside.json"), jsonPath); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(jsonPath, []byte(`{"request_id":"req-l108-incomplete"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.ready {
				if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			exclusive, err := AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
			if err != nil {
				t.Fatal(err)
			}
			defer exclusive.Close()
			if _, err := exclusive.SnapshotIngressPairs("default"); !errors.Is(err, fixture.want) {
				t.Fatalf("snapshot error=%v, want %v", err, fixture.want)
			}
			if fixture.json && !fixture.symlinkJSON {
				if _, err := os.Stat(jsonPath); err != nil {
					t.Fatalf("incomplete JSON was altered or removed: %v", err)
				}
			}
		})
	}
}

func TestLanesL108RawWriterBypassesAdvisoryLockAndEmptySnapshotStops(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	exclusive, err := AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive.Close()

	// This models a direct workspace/raw writer which does not participate in
	// the cooperative lock. The lock cannot stop it; the exclusive snapshot must
	// surface its retained work so the migration empty-inbox gate fails closed.
	jsonPath := filepath.Join(root, "inbox", "req-l108-raw.json")
	readyPath := filepath.Join(root, "inbox", "req-l108-raw.ready")
	if err := os.WriteFile(jsonPath, []byte(`{"request_id":"req-l108-raw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := exclusive.SnapshotIngressPairs("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.RequireEmpty(); !errors.Is(err, ErrResidue) {
		t.Fatalf("raw-writer residue empty-gate error=%v, want %v", err, ErrResidue)
	}
	if data, err := os.ReadFile(jsonPath); err != nil || string(data) != `{"request_id":"req-l108-raw"}` {
		t.Fatalf("raw-writer evidence data=%q err=%v", data, err)
	}
}

func TestLanesL108MigrationFenceMarkerBlocksSharedPublishers(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	exclusive, err := AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, MigrationFenceFileName)
	if err := os.WriteFile(markerPath, []byte(`{"lane_transition_id":"l108-fixture"}`), 0o600); err != nil {
		_ = exclusive.Close()
		t.Fatal(err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	if shared, err := AcquireShared(context.Background(), root); !errors.Is(err, ErrMigrationFenced) || shared != nil {
		if shared != nil {
			_ = shared.Close()
		}
		t.Fatalf("shared publisher lock=%v err=%v, want %v", shared, err, ErrMigrationFenced)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("migration fence marker was not preserved: %v", err)
	}
	if retired, err := AcquireRetirementUnder(context.Background(), trustRoot, root); !errors.Is(err, ErrMigrationFenced) || retired != nil {
		if retired != nil {
			_ = retired.Close()
		}
		t.Fatalf("retirement over migration fence lock=%v err=%v, want migration fence", retired, err)
	}
	if _, err := os.Stat(filepath.Join(root, SentinelFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retirement crossed migration fence; sentinel err=%v", err)
	}
}

func TestLanesL108UnsafeMigrationFenceMarkerFailsClosed(t *testing.T) {
	trustRoot := t.TempDir()
	root := newFenceMailboxTree(t, trustRoot)
	target := filepath.Join(trustRoot, "marker-target")
	if err := os.WriteFile(target, []byte(`{"state":"fencing"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, MigrationFenceFileName)); err != nil {
		t.Fatal(err)
	}
	if shared, err := AcquireShared(context.Background(), root); !errors.Is(err, ErrUnsafe) || shared != nil {
		if shared != nil {
			_ = shared.Close()
		}
		t.Fatalf("symlinked migration marker lock=%v err=%v, want %v", shared, err, ErrUnsafe)
	}
}

func TestLanesL108PublisherLockSpansJSONReadyAndDirectorySync(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		directory string
		id        string
		body      string
	}{
		{name: "request", directory: "inbox", id: "req-l108-request-lock", body: `{"request_id":"req-l108-request-lock"}`},
		{name: "ack", directory: "acks", id: "req-l108-ack-lock", body: `{"request_id":"req-l108-ack-lock","response_revision":1}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			trustRoot := t.TempDir()
			root := newFenceMailboxTree(t, trustRoot)
			beforeSync := make(chan struct{})
			continueSync := make(chan struct{})
			publishResult := make(chan error, 1)
			go func() {
				shared, err := AcquireShared(context.Background(), root)
				if err == nil {
					err = shared.publishReadyPairWithOps(fixture.directory, fixture.id, []byte(fixture.body), &pairWriteOps{
						syncDir: func(fd int) error {
							close(beforeSync)
							<-continueSync
							return unix.Fsync(fd)
						},
					})
					if closeErr := shared.Close(); err == nil {
						err = closeErr
					}
				}
				publishResult <- err
			}()
			<-beforeSync
			for _, suffix := range []string{".json", ".ready"} {
				if _, err := os.Stat(filepath.Join(root, fixture.directory, fixture.id+suffix)); err != nil {
					t.Fatalf("pair member %s missing before parent fsync: %v", suffix, err)
				}
			}

			exclusiveResult := make(chan struct {
				lock *Lock
				err  error
			}, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				lock, err := AcquireMigrationExclusive(ctx, root)
				exclusiveResult <- struct {
					lock *Lock
					err  error
				}{lock: lock, err: err}
			}()
			select {
			case result := <-exclusiveResult:
				if result.lock != nil {
					_ = result.lock.Close()
				}
				if !errors.Is(result.err, ErrBusy) {
					close(continueSync)
					<-publishResult
					t.Fatalf("exclusive snapshot crossed publisher before directory fsync: %v", result.err)
				}
			case <-time.After(2 * time.Second):
				close(continueSync)
				<-publishResult
				t.Fatal("exclusive lock attempt did not finish while the publisher was blocked")
			}
			close(continueSync)
			if err := <-publishResult; err != nil {
				t.Fatalf("publish pair: %v", err)
			}
			exclusive, err := AcquireMigrationExclusive(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer exclusive.Close()
			snapshot, err := exclusive.SnapshotIngressPairs("default")
			if err != nil {
				t.Fatal(err)
			}
			wantKind := map[string]string{"inbox": "request", "acks": "ack"}[fixture.directory]
			if len(snapshot.Pairs) != 1 || snapshot.Pairs[0].Kind != wantKind || snapshot.Pairs[0].LogicalID != fixture.id {
				t.Fatalf("snapshot after synchronized pair=%+v", snapshot)
			}
		})
	}
}

func TestLanesL108InterruptedPublicationRetainsEvidenceAndCannotReplay(t *testing.T) {
	crash := errors.New("simulated process interruption")
	for _, fixture := range []struct {
		name        string
		failAt      string
		directory   string
		id          string
		body        string
		readyExists bool
	}{
		{name: "after JSON before ready", failAt: "before-ready", directory: "inbox", id: "req-l108-draft", body: `{"request_id":"req-l108-draft"}`},
		{name: "after ready before directory sync", failAt: "directory-sync", directory: "acks", id: "req-l108-ack-draft", body: `{"request_id":"req-l108-ack-draft","response_revision":3}`, readyExists: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			trustRoot := t.TempDir()
			root := newFenceMailboxTree(t, trustRoot)
			ops := &pairWriteOps{}
			if fixture.failAt == "before-ready" {
				ops.beforeReady = func() error { return crash }
			} else {
				ops.syncDir = func(int) error { return crash }
			}
			shared, err := AcquireShared(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			err = shared.publishReadyPairWithOps(fixture.directory, fixture.id, []byte(fixture.body), ops)
			if closeErr := shared.Close(); err == nil {
				err = closeErr
			}
			if !errors.Is(err, crash) {
				t.Fatalf("interrupted publish error=%v, want %v", err, crash)
			}
			jsonPath := filepath.Join(root, fixture.directory, fixture.id+".json")
			readyPath := filepath.Join(root, fixture.directory, fixture.id+".ready")
			data, err := os.ReadFile(jsonPath)
			if err != nil || string(data) != fixture.body {
				t.Fatalf("retained JSON=%q err=%v", data, err)
			}
			if _, err := os.Stat(readyPath); fixture.readyExists && err != nil {
				t.Fatalf("ready marker missing after simulated sync interruption: %v", err)
			} else if !fixture.readyExists && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("draft unexpectedly has a ready marker: %v", err)
			}

			exclusive, err := AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, snapshotErr := exclusive.SnapshotIngressPairs("default")
			if fixture.readyExists {
				if snapshotErr != nil || snapshot.RequireEmpty() == nil {
					_ = exclusive.Close()
					t.Fatalf("uncertain complete pair was not retained as a stop condition: snapshot=%+v err=%v", snapshot, snapshotErr)
				}
			} else if !errors.Is(snapshotErr, ErrResidue) {
				_ = exclusive.Close()
				t.Fatalf("incomplete pair snapshot error=%v, want retained-residue stop", snapshotErr)
			}
			if err := exclusive.Close(); err != nil {
				t.Fatal(err)
			}

			if err := PublishReadyPair(context.Background(), root, fixture.directory, fixture.id, []byte(fixture.body)); err == nil {
				t.Fatal("retry reused retained request ID after interruption")
			}
			data, err = os.ReadFile(jsonPath)
			if err != nil || string(data) != fixture.body {
				t.Fatalf("retry changed retained JSON=%q err=%v", data, err)
			}
		})
	}
}
