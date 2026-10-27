package laneinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func retainedInventoryFixture(t *testing.T) catalogSelectorFixture {
	t.Helper()
	fixture, _ := retainedInventoryFixtureWithWriter(t)
	return fixture
}

func retainedInventoryFixtureWithWriter(t *testing.T) (catalogSelectorFixture, *sql.DB) {
	t.Helper()
	fixture := newCatalogSelectorFixture(t)
	db, err := store.Open(context.Background(), fixture.layout.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`INSERT INTO exec_sessions (
session_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,state,
command_timeout_ns,idle_timeout_ns,session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at
) VALUES ('sess-0123456789abcdef0123456789abcdef','local','mac-workstation','mac-dev','local_user','fixture-user','empty','ready',1,1,1,1,'now','now','later')`)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	return fixture, db
}

func TestCatalogRetainedInventoryBindsOneReadCutWithoutChangingAuthority(t *testing.T) {
	fixture := retainedInventoryFixture(t)
	before := retainedInventoryFixtureHashes(t, fixture)
	var statusCalls, validationCalls int
	report, err := readCatalogRetainedWorkInventoryAt(context.Background(), fixture.layout,
		func(context.Context) (bool, error) { statusCalls++; return true, nil },
		func() error { validationCalls++; return nil }, 0, store.ReadRetainedWorkInventory)
	if err != nil {
		t.Fatal(err)
	}
	if !report.InventoryComplete || !report.WorkInventory.Complete || report.FormatVersion != 1 ||
		report.RecoveryAuthorized || report.SafeToResume != "not_established" || !report.ServiceActive {
		t.Fatalf("wrong completeness or safety metadata: %+v", report)
	}
	if report.DatabaseSchema != 40 || report.DatabaseIdentity.Path != fixture.layout.databasePath ||
		report.DatabaseIdentity.Inode == 0 || len(report.WorkInventory.Sessions) != 1 ||
		report.HostID != fixture.layout.hostID || report.Lane != fixture.layout.lane {
		t.Fatalf("wrong authority binding or missing active identity: %+v", report)
	}
	if report.SelectedArtifact.Kind != "legacy" || report.SelectedArtifact.SelectorTarget != fixture.legacyTarget ||
		report.SelectedArtifact.SourceRevision != "0123456789abcdef0123456789abcdef01234567" ||
		!bundleDigestPattern.MatchString(report.SelectedArtifact.BundleSHA256) || !bundleDigestPattern.MatchString(report.ConfigurationSHA256) {
		t.Fatalf("missing selected-artifact/config provenance: %+v", report)
	}
	if report.ObservationStartedAt == "" || report.ObservationCompletedAt == "" || statusCalls != 2 || validationCalls != 2 {
		t.Fatalf("binding revalidation missing: status=%d validation=%d report=%+v", statusCalls, validationCalls, report)
	}
	if after := retainedInventoryFixtureHashes(t, fixture); !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only inventory changed fixture files: before=%v after=%v", before, after)
	}
}

func TestCatalogRetainedInventoryReportsCatalogSelectedSourceSeparately(t *testing.T) {
	fixture := retainedInventoryFixture(t)
	current := filepath.Join(fixture.layout.serviceRoot, "current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixture.catalogRoot, fixture.firstRelease, fixture.layout.hostID), current); err != nil {
		t.Fatal(err)
	}
	report, err := readCatalogRetainedWorkInventoryAt(context.Background(), fixture.layout,
		func(context.Context) (bool, error) { return true, nil }, func() error { return nil }, 0, store.ReadRetainedWorkInventory)
	if err != nil || report.SelectedArtifact.Kind != "catalog" || report.SelectedArtifact.ReleaseID != fixture.firstRelease ||
		report.SelectedArtifact.SourceRevision == "" || report.DiagnosticBuildRevision == "" {
		t.Fatalf("selected source and diagnostic build were not independently reported: %+v err=%v", report, err)
	}
}

func TestCatalogRetainedInventoryRejectsDriftOrPartialCutWithoutRows(t *testing.T) {
	for _, kind := range []string{"configuration", "selector", "database", "config-validation", "service", "read-error", "partial", "schema", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			fixture := retainedInventoryFixture(t)
			var validations, statusCalls int
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			read := func(ctx context.Context, db *sql.DB, limit int) (store.RetainedWorkInventory, error) {
				result, err := store.ReadRetainedWorkInventory(ctx, db, limit)
				if err != nil {
					return result, err
				}
				switch kind {
				case "configuration":
					if err := os.WriteFile(fixture.layout.configFile, []byte("changed: true\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "selector":
					current := filepath.Join(fixture.layout.serviceRoot, "current")
					if err := os.Remove(current); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(fixture.catalogRoot, fixture.firstRelease, fixture.layout.hostID), current); err != nil {
						t.Fatal(err)
					}
				case "database":
					if err := os.Rename(fixture.layout.databasePath, fixture.layout.databasePath+".preserved"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(fixture.layout.databasePath, []byte("replacement"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "read-error":
					return result, errors.New("untrusted SQL or payload detail must not be printed")
				case "partial":
					result.Complete = false
				case "schema":
					result.SchemaVersion = 41
				case "deadline":
					cancel()
				}
				return result, nil
			}
			report, err := readCatalogRetainedWorkInventoryAt(ctx, fixture.layout,
				func(context.Context) (bool, error) { statusCalls++; return kind != "service" || statusCalls == 1, nil },
				func() error {
					validations++
					if kind == "config-validation" && validations > 1 {
						return errors.New("changed configuration selection")
					}
					return nil
				}, 0, read)
			if !errors.Is(err, ErrCatalogRetainedInventory) || report.InventoryComplete || report.WorkInventory.Complete ||
				len(report.WorkInventory.Sessions) != 0 || len(report.WorkInventory.Commands) != 0 {
				t.Fatalf("%s returned partial or complete rows: report=%+v err=%v", kind, report, err)
			}
			if strings.Contains(err.Error(), "untrusted SQL") {
				t.Fatal("raw database/helper error escaped the metadata boundary")
			}
		})
	}
}

func TestCatalogRetainedInventoryRejectsNonPrivateOrUnavailableInputs(t *testing.T) {
	for _, kind := range []string{"database-mode", "config-mode", "missing-database", "database-symlink", "no-selector", "oversize-request"} {
		t.Run(kind, func(t *testing.T) {
			fixture := retainedInventoryFixture(t)
			limit := 0
			switch kind {
			case "database-mode":
				if err := os.Chmod(fixture.layout.databasePath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "config-mode":
				if err := os.Chmod(fixture.layout.configFile, 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing-database":
				if err := os.Remove(fixture.layout.databasePath); err != nil {
					t.Fatal(err)
				}
			case "database-symlink":
				if err := os.Rename(fixture.layout.databasePath, fixture.layout.databasePath+".preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(fixture.layout.databasePath+".preserved", fixture.layout.databasePath); err != nil {
					t.Fatal(err)
				}
			case "no-selector":
				if err := os.Remove(filepath.Join(fixture.layout.serviceRoot, "current")); err != nil {
					t.Fatal(err)
				}
			case "oversize-request":
				limit = store.MaxRetainedWorkInventoryRows + 1
			}
			report, err := readCatalogRetainedWorkInventoryAt(context.Background(), fixture.layout,
				func(context.Context) (bool, error) { return true, nil }, func() error { return nil }, limit, store.ReadRetainedWorkInventory)
			if !errors.Is(err, ErrCatalogRetainedInventory) || report.InventoryComplete {
				t.Fatalf("unsafe %s input accepted: report=%+v err=%v", kind, report, err)
			}
		})
	}
}

func TestCatalogRetainedInventoryPublicReadersRejectInventedAuthority(t *testing.T) {
	if _, err := ReadCatalogRetainedWorkInventory(context.Background(), Plan{}, nil, 0); !errors.Is(err, ErrCatalogRetainedInventory) {
		t.Fatalf("invalid Dev/QAS plan error=%v", err)
	}
	if _, err := ReadCatalogPRDRetainedWorkInventory(context.Background(), PRDUpgradePlan{}, nil, 0); !errors.Is(err, ErrCatalogRetainedInventory) {
		t.Fatalf("invalid PRD plan error=%v", err)
	}
	if retainedInventoryAccountAndPlatform("not-a-host", config.MacAccount) {
		t.Fatal("invented host passed platform/owner checks")
	}
}

func TestCatalogRetainedInventoryMissingWALPairNeverOpensSQLite(t *testing.T) {
	for _, suffix := range []string{"both-absent", "-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			fixture, writer := retainedInventoryFixtureWithWriter(t)
			if suffix == "both-absent" {
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(fixture.layout.databasePath + suffix); err != nil {
				t.Fatal(err)
			}
			before := retainedInventoryFixtureHashes(t, fixture)
			readerCalled := false
			report, err := readCatalogRetainedWorkInventoryAt(context.Background(), fixture.layout,
				func(context.Context) (bool, error) { return true, nil }, func() error { return nil }, 0,
				func(context.Context, *sql.DB, int) (store.RetainedWorkInventory, error) {
					readerCalled = true
					return store.RetainedWorkInventory{Complete: true, SchemaVersion: 40}, nil
				})
			if !errors.Is(err, ErrCatalogRetainedInventory) || report.InventoryComplete || readerCalled {
				t.Fatalf("missing sidecars reached SQLite metadata read: complete=%t reader=%t err=%v", report.InventoryComplete, readerCalled, err)
			}
			if after := retainedInventoryFixtureHashes(t, fixture); !reflect.DeepEqual(before, after) {
				t.Fatal("refusal created or changed database/sidecar files")
			}
		})
	}
}

func retainedInventoryFixtureHashes(t *testing.T, fixture catalogSelectorFixture) map[string]string {
	t.Helper()
	hashes := make(map[string]string)
	for _, root := range []string{fixture.layout.serviceRoot, fixture.catalogRoot} {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				hashes[path] = "symlink:" + target
				return nil
			}
			if path == fixture.layout.databasePath+"-shm" {
				// SQLite read transactions participate in the ordinary SHM
				// reader-lock protocol even with mode=ro. Its lockmarks are
				// volatile coordination, not durable application state. Prove
				// no SHM creation/replacement/resize/permission change instead.
				identity, err := retainedInventoryDatabaseIdentity(path)
				if err != nil {
					return err
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				metadata, err := json.Marshal(struct {
					Identity CatalogRetainedDatabaseIdentity
					Bytes    int64
					Mode     os.FileMode
				}{identity, info.Size(), info.Mode()})
				if err != nil {
					return err
				}
				hashes[path] = "sqlite-shm-metadata:" + sha256Hex(metadata)
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hashes[path] = sha256Hex(data)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Ensure this proof remains representable as evidence rather than silently
	// relying on map iteration order.
	if _, err := json.Marshal(hashes); err != nil {
		t.Fatal(err)
	}
	return hashes
}
