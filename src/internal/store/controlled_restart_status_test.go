package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestControlledRestartStatusReadOnlyRecognizesOnlyAcceptedStates(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range []struct {
		name string
		seed func(t *testing.T) string
		want ControlledRestartStatus
	}{
		{
			name: "legacy schema 33 without plan",
			seed: controlledRestartStatusLegacyAuthorityPath,
			want: ControlledRestartStatusLegacy,
		},
		{
			name: "schema 34 with plan tables",
			seed: controlledRestartStatusSchema34AuthorityPath,
			want: ControlledRestartStatusMigratedWithoutPlan,
		},
		{
			name: "current schema without plan",
			seed: b008CurrentAuthorityPath,
			want: ControlledRestartStatusMigratedWithoutPlan,
		},
		{
			name: "prepared plan",
			seed: func(t *testing.T) string {
				path, authority, closeDatabase := controlledRestartStatusAuthority(t)
				defer closeDatabase()
				pairs := pBUG008LostPairs(t, authority, "controlled-restart-status-prepared")
				_ = pControlledRestartQueuedOneOff(t, authority, "controlled-restart-status-prepared")
				if _, err := authority.PrepareControlledRestartPlan(ctx, pairs); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: ControlledRestartStatusPrepared,
		},
		{
			name: "active plan",
			seed: func(t *testing.T) string {
				path, authority, closeDatabase := controlledRestartStatusAuthority(t)
				defer closeDatabase()
				pairs := pBUG008LostPairs(t, authority, "controlled-restart-status-active")
				_ = pControlledRestartQueuedOneOff(t, authority, "controlled-restart-status-active")
				plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := authority.ActivateControlledRestartPlan(ctx, plan); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: ControlledRestartStatusActive,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := fixture.seed(t)
			before := b008ReadSchemaState(t, path)
			beforeMode := b008FileMode(t, path)
			beforeFiles := controlledRestartStatusFileState(t, path)

			database, err := OpenExistingControlledRestartStatusReadOnly(ctx, path)
			if err != nil {
				t.Fatalf("open controlled restart status reader: %v", err)
			}
			status, err := ReadControlledRestartStatus(ctx, database)
			if err != nil {
				_ = database.Close()
				t.Fatalf("read controlled restart status: %v", err)
			}
			if status != fixture.want {
				_ = database.Close()
				t.Fatalf("status=%q, want %q", status, fixture.want)
			}
			if _, err := database.ExecContext(ctx, `CREATE TABLE controlled_restart_status_readonly_write_check (id INTEGER PRIMARY KEY)`); err == nil {
				_ = database.Close()
				t.Fatal("controlled restart status reader unexpectedly accepted a write")
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
				t.Fatalf("status reader changed schema/history: after=%+v before=%+v", after, before)
			}
			if afterMode := b008FileMode(t, path); afterMode != beforeMode {
				t.Fatalf("status reader changed database mode from %04o to %04o", beforeMode, afterMode)
			}
			if afterFiles := controlledRestartStatusFileState(t, path); !reflect.DeepEqual(afterFiles, beforeFiles) {
				t.Fatalf("status reader changed database or SQLite sidecars: after=%+v before=%+v", afterFiles, beforeFiles)
			}
		})
	}
}

func TestControlledRestartStatusRejectsMalformedOrUnsupportedAuthorityWithoutWriting(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range []struct {
		name  string
		setup func(t *testing.T) string
		want  error
	}{
		{
			name: "unsupported schema",
			setup: func(t *testing.T) string {
				path := b008CurrentAuthorityPath(t)
				b008SetAuthoritySchemaVersion(t, path, controlledRestartStatusLegacySchemaVersion-1)
				return path
			},
			want: ErrSchemaVersion,
		},
		{
			name: "legacy schema with plan table",
			setup: func(t *testing.T) string {
				path := b008CurrentAuthorityPath(t)
				b008SetAuthoritySchemaVersion(t, path, controlledRestartStatusLegacySchemaVersion)
				return path
			},
			want: ErrSchemaVersion,
		},
		{
			name: "plan generation mismatch",
			setup: func(t *testing.T) string {
				path, authority, closeDatabase := controlledRestartStatusAuthority(t)
				pairs := pBUG008LostPairs(t, authority, "controlled-restart-status-malformed")
				queued := pControlledRestartQueuedOneOff(t, authority, "controlled-restart-status-malformed")
				plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
				if err != nil {
					closeDatabase()
					t.Fatal(err)
				}
				if _, err := authority.db.ExecContext(ctx, `UPDATE exec_sessions SET runtime_generation = ? WHERE session_id = ?`, plan.RuntimeGeneration+"-mismatch", string(queued.SessionID)); err != nil {
					closeDatabase()
					t.Fatal(err)
				}
				closeDatabase()
				return path
			},
			want: ErrControlledRestartPlanCorrupt,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := fixture.setup(t)
			before := b008ReadSchemaState(t, path)
			beforeMode := b008FileMode(t, path)
			beforeFiles := controlledRestartStatusFileState(t, path)
			database, err := OpenExistingControlledRestartStatusReadOnly(ctx, path)
			if fixture.want == ErrSchemaVersion {
				if database != nil {
					_ = database.Close()
					t.Fatal("unsupported status authority was opened")
				}
				if !errors.Is(err, fixture.want) {
					t.Fatalf("open error=%v, want %v", err, fixture.want)
				}
			} else {
				if err != nil {
					t.Fatalf("open malformed current authority: %v", err)
				}
				_, err = ReadControlledRestartStatus(ctx, database)
				_ = database.Close()
				if !errors.Is(err, fixture.want) {
					t.Fatalf("status error=%v, want %v", err, fixture.want)
				}
			}
			if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected status authority changed schema/history: after=%+v before=%+v", after, before)
			}
			if afterMode := b008FileMode(t, path); afterMode != beforeMode {
				t.Fatalf("rejected status authority changed database mode from %04o to %04o", beforeMode, afterMode)
			}
			if afterFiles := controlledRestartStatusFileState(t, path); !reflect.DeepEqual(afterFiles, beforeFiles) {
				t.Fatalf("rejected status authority changed database or SQLite sidecars: after=%+v before=%+v", afterFiles, beforeFiles)
			}
		})
	}
}

func TestControlledRestartStatusMissingAuthorityCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "authority.db")
	database, err := OpenExistingControlledRestartStatusReadOnly(context.Background(), path)
	if database != nil {
		_ = database.Close()
		t.Fatal("missing controlled restart authority was opened")
	}
	if !errors.Is(err, ErrDatabaseMissing) {
		t.Fatalf("missing authority error=%v, want %v", err, ErrDatabaseMissing)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing authority parent changed: %v", err)
	}
}

func controlledRestartStatusAuthority(t *testing.T) (string, *AuthorityStore, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "authority.db")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	closed := false
	return path, authority, func() {
		if closed {
			return
		}
		closed = true
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func controlledRestartStatusLegacyAuthorityPath(t *testing.T) string {
	t.Helper()
	path := b008CurrentAuthorityPath(t)
	database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE exec_controlled_restart_plan_lost_pairs`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE exec_controlled_restart_plans`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE exec_commandless_lost_runtime_recovery_finalizations`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM runner_schema_migrations WHERE version IN (34, 35)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = ` + strconv.Itoa(controlledRestartStatusLegacySchemaVersion)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func controlledRestartStatusSchema34AuthorityPath(t *testing.T) string {
	t.Helper()
	path := b008CurrentAuthorityPath(t)
	database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE exec_commandless_lost_runtime_recovery_finalizations`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM runner_schema_migrations WHERE version = 35`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = 34`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

type controlledRestartStatusFileSnapshot struct {
	Exists bool
	Mode   os.FileMode
	SHA256 [sha256.Size]byte
}

func controlledRestartStatusFileState(t *testing.T, databasePath string) map[string]controlledRestartStatusFileSnapshot {
	t.Helper()
	state := make(map[string]controlledRestartStatusFileSnapshot, 4)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		path := databasePath + suffix
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			state[suffix] = controlledRestartStatusFileSnapshot{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		state[suffix] = controlledRestartStatusFileSnapshot{Exists: true, Mode: info.Mode(), SHA256: sha256.Sum256(contents)}
	}
	return state
}
