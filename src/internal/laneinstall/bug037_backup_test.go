package laneinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

// The fixture uses the real selector/catalog verifier, actual schema-40 SQLite
// authority and online snapshot core. Only canonical host paths/service files
// are relocated; the public entrypoint's native account/config guards stay on.
func TestBUG037SelectedCatalogBackupPreservesRetainedLostWork(t *testing.T) {
	f, plan, db, baseline, unit := bug037BackupFixture(t)
	current := filepath.Join(f.layout.serviceRoot, "current")
	selectedBefore, err := os.Readlink(current)
	if err != nil {
		t.Fatal(err)
	}
	baselineCalls := 0
	classify := func() (CatalogFreshInstallBaseline, bool, error) {
		return artifactFreshInstallBaselineAt(f.layout, func() (CatalogFreshInstallBaseline, bool, error) {
			baselineCalls++
			return baseline()
		})
	}
	before := bug037History(t, db)
	beforeSchema, beforeWork, err := readPRDWorkSnapshot(context.Background(), db)
	if err != nil || beforeWork.ActiveSessionReservations != 1 || beforeWork.ActiveCommandSlots != 1 || beforeWork.ActiveSessions != 0 {
		t.Fatalf("fixture held LOST work=%+v schema=%d err=%v", beforeWork, beforeSchema, err)
	}
	baselineBefore, err := os.ReadFile(catalogFreshInstallBaselinePath(plan))
	if err != nil {
		t.Fatal(err)
	}
	estimated, err := estimatePRDFileBackupBytesUsing(plan, classify, []string{unit})
	if err != nil || estimated == 0 {
		t.Fatalf("selected-catalog backup estimate with retained LOST pair refused: bytes=%d err=%v", estimated, err)
	}
	_, fresh, err := classify()
	if err != nil || fresh {
		t.Fatalf("selected predecessor misclassified fresh=%v err=%v", fresh, err)
	}
	receipt, err := capturePRDBackupSnapshot(context.Background(), plan, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true, db, beforeSchema, fresh, []string{unit})
	if err != nil {
		t.Fatalf("consistent selected-catalog backup refused: %v", err)
	}
	if receipt.Manifest.DatabaseSHA256 == "" || receipt.Manifest.SchemaVersion != 40 {
		t.Fatalf("incomplete backup manifest: %+v", receipt.Manifest)
	}
	copied, err := store.OpenExistingRestartPreflightReadOnly(context.Background(), receipt.Manifest.DatabaseBackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	if got := bug037History(t, copied); !reflect.DeepEqual(got, before) {
		t.Fatalf("backup lost original history/held capacity: before=%v after=%v", before, got)
	}
	if got := bug037History(t, db); !reflect.DeepEqual(got, before) {
		t.Fatalf("backup mutated source history/holds: before=%v after=%v", before, got)
	}
	_, afterWork, err := readPRDWorkSnapshot(context.Background(), copied)
	if err != nil || afterWork != beforeWork {
		t.Fatalf("backup inventory changed: before=%+v after=%+v err=%v", beforeWork, afterWork, err)
	}
	baselineAfter, err := os.ReadFile(catalogFreshInstallBaselinePath(plan))
	if err != nil || string(baselineAfter) != string(baselineBefore) {
		t.Fatalf("historical first-install baseline changed: %v", err)
	}
	if baselineCalls != 0 {
		t.Fatalf("selected catalog re-read historical empty baseline %d times", baselineCalls)
	}
	selectedAfter, err := os.Readlink(current)
	if err != nil || selectedAfter != selectedBefore {
		t.Fatalf("backup changed selector: before=%q after=%q err=%v", selectedBefore, selectedAfter, err)
	}
	for _, item := range receipt.Manifest.Files {
		if item.TargetPath == unit {
			return
		}
	}
	t.Fatal("existing predecessor unit omitted from selected-catalog backup")
}

func TestBUG037UnselectedNonemptyAndInvalidSelectorsRemainRefused(t *testing.T) {
	for _, mode := range []string{"unselected", "broken", "regular", "tampered-bundle"} {
		t.Run(mode, func(t *testing.T) {
			f, plan, _, baseline, unit := bug037BackupFixture(t)
			current := filepath.Join(f.layout.serviceRoot, "current")
			if err := os.Remove(current); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "broken":
				if err := os.Symlink(filepath.Join(f.catalogRoot, "missing", "linux-host"), current); err != nil {
					t.Fatal(err)
				}
			case "regular":
				if err := os.WriteFile(current, []byte("not a selector"), 0600); err != nil {
					t.Fatal(err)
				}
			case "tampered-bundle":
				target := filepath.Join(f.catalogRoot, f.firstRelease, "linux-host")
				if err := os.Symlink(target, current); err != nil {
					t.Fatal(err)
				}
				component := filepath.Join(target, "bin", "runnerd")
				if err := os.Chmod(component, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(component, []byte("changed"), 0500); err != nil {
					t.Fatal(err)
				}
			}
			classify := func() (CatalogFreshInstallBaseline, bool, error) {
				return artifactFreshInstallBaselineAt(f.layout, baseline)
			}
			if _, err := estimatePRDFileBackupBytesUsing(plan, classify, []string{unit}); err == nil {
				t.Fatal("unsafe predecessor accepted")
			}
		})
	}
}

func TestBUG037SelectedCatalogCannotOmitMissingOrLinkedUnit(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "symlink"}[linked], func(t *testing.T) {
			f, plan, _, baseline, unit := bug037BackupFixture(t)
			if err := os.Remove(unit); err != nil {
				t.Fatal(err)
			}
			if linked {
				if err := os.Symlink(plan.Namespace.ConfigFile, unit); err != nil {
					t.Fatal(err)
				}
			}
			classify := func() (CatalogFreshInstallBaseline, bool, error) {
				return artifactFreshInstallBaselineAt(f.layout, baseline)
			}
			if _, err := estimatePRDFileBackupBytesUsing(plan, classify, []string{unit}); !errors.Is(err, ErrPRDUpgradeGuard) {
				t.Fatalf("missing/linked selected service unit accepted: %v", err)
			}
		})
	}
}

func TestBUG037UnselectedEmptyBaselineStillAccepted(t *testing.T) {
	f, _, db, baseline, _ := bug037BackupFixture(t)
	if err := os.Remove(filepath.Join(f.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE exec_capacity_reservations SET cleanup_confirmed_at='confirmed',released_at='released'`,
		`UPDATE exec_command_slots SET stop_confirmed_at='confirmed',released_at='released'`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	_, fresh, err := artifactFreshInstallBaselineAt(f.layout, baseline)
	if err != nil || !fresh {
		t.Fatalf("unchanged empty first-install guard rejected: fresh=%v err=%v", fresh, err)
	}
}

func TestBUG037LegacySelectorStillUsesOriginalFreshInstallGuard(t *testing.T) {
	f, _, _, baseline, _ := bug037BackupFixture(t)
	if err := os.Remove(filepath.Join(f.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.legacyTarget, filepath.Join(f.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	called := false
	_, _, err := artifactFreshInstallBaselineAt(f.layout, func() (CatalogFreshInstallBaseline, bool, error) { called = true; return baseline() })
	if !called || err == nil {
		t.Fatalf("legacy baseline behavior changed: called=%v err=%v", called, err)
	}
}

func bug037BackupFixture(t *testing.T) (catalogSelectorFixture, PRDUpgradePlan, *sql.DB, func() (CatalogFreshInstallBaseline, bool, error), string) {
	t.Helper()
	f := newCatalogSelectorFixture(t)
	f.layout.lane = config.ReleaseLanePRD
	if err := os.Remove(filepath.Join(f.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.catalogRoot, f.firstRelease, "linux-host"), filepath.Join(f.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	plan.Namespace.ServiceRoot = f.layout.serviceRoot
	plan.Namespace.Binaries = filepath.Join(f.layout.serviceRoot, "bin")
	plan.Namespace.ConfigFile = f.layout.configFile
	plan.Namespace.Staging = filepath.Dir(f.layout.operationRoot)
	plan.Namespace.Backups = f.layout.backupRoot
	path, _ := seedCatalogProbeResultFixture(t, "complete")
	plan.Namespace.Database = path
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, query := range []string{
		`UPDATE exec_sessions SET state='lost'`,
		`UPDATE exec_commands SET state='lost',exit_code=NULL,output_complete=0`,
		`UPDATE exec_jobs SET phase='lost',command_state='lost',exit_code=NULL,output_complete=0,teardown_state='lost',teardown_reason='capture_boundary_unconfirmed'`,
		`UPDATE exec_command_events SET event_type='command_lost' WHERE sequence=4`,
		`UPDATE exec_capacity_reservations SET cleanup_confirmed_at=NULL,released_at=NULL`,
		`UPDATE exec_command_slots SET stop_confirmed_at=NULL,released_at=NULL`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{plan.Namespace.Binaries, filepath.Join(f.layout.serviceRoot, "secrets")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(plan.Namespace.Binaries, "runner-entrypoint.sh"), []byte("fixture launcher\n"), 0700); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(f.layout.serviceRoot, "runnerd.service")
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/fixture/runnerd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	configSHA, err := fileSHA256(plan.Namespace.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewCatalogFreshInstallBaseline("0123456789abcdef0123456789abcdef", validFreshPRDAbsenceForTest(t, now.Add(-time.Minute)), configSHA, 40, CatalogWorkInventory{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogFreshInstallBaselinePath(plan), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := func() (CatalogFreshInstallBaseline, bool, error) {
		raw, err := os.ReadFile(catalogFreshInstallBaselinePath(plan))
		if err != nil {
			return CatalogFreshInstallBaseline{}, false, err
		}
		var old CatalogFreshInstallBaseline
		if err := json.Unmarshal(raw, &old); err != nil {
			return old, false, err
		}
		schema, work, err := readPRDWorkSnapshot(context.Background(), db)
		if err != nil {
			return old, false, err
		}
		err = validateCatalogFreshInstallBaseline(old, CatalogCohortHostPreflight{HostID: "linux-host", ConfigurationSHA256: configSHA, SchemaVersion: schema, WorkInventorySHA256: work.SHA256(), WorkInventoryEmpty: work.Empty()})
		return old, err == nil, err
	}
	return f, plan, db, baseline, unit
}

func bug037History(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var result []string
	for _, query := range []string{
		`SELECT session_id||':'||state FROM exec_sessions ORDER BY session_id`,
		`SELECT command_id||':'||state||':'||quote(exit_code)||':'||output_complete||':'||final_event_sequence FROM exec_commands ORDER BY command_id`,
		`SELECT job_id||':'||phase||':'||quote(exit_code)||':'||teardown_state FROM exec_jobs ORDER BY job_id`,
		`SELECT command_id||':'||sequence||':'||event_type||':'||hex(payload) FROM exec_command_events ORDER BY command_id,sequence`,
		`SELECT session_id||':'||quote(cleanup_confirmed_at)||':'||quote(released_at) FROM exec_capacity_reservations ORDER BY session_id`,
		`SELECT command_id||':'||quote(stop_confirmed_at)||':'||quote(released_at) FROM exec_command_slots ORDER BY command_id`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
