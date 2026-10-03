package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBUG011OpenExistingCurrentReadOnlyRejectsMissingDatabaseWithoutCreatingIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "authority.db")

	database, err := OpenExistingCurrentReadOnly(context.Background(), path)
	if database != nil {
		_ = database.Close()
		t.Fatal("OpenExistingCurrentReadOnly returned a database for a missing path")
	}
	if !errors.Is(err, ErrDatabaseMissing) {
		t.Fatalf("OpenExistingCurrentReadOnly(missing) error=%v, want %v", err, ErrDatabaseMissing)
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing database parent was changed: stat error=%v, want not exist", statErr)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing database file was created: stat error=%v, want not exist", statErr)
	}
}

func TestBUG011OpenExistingCurrentReadOnlyReadsCurrentAuthorityWithoutWriting(t *testing.T) {
	ctx := context.Background()
	path := b008CurrentAuthorityPath(t)
	before := b008ReadSchemaState(t, path)
	beforeMode := b008FileMode(t, path)

	database, err := OpenExistingCurrentReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenExistingCurrentReadOnly(current) error=%v", err)
	}
	defer database.Close()
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ReadOperationalMetrics(ctx); err != nil {
		t.Fatalf("read operational metrics through read-only authority: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE bug011_readonly_write_check (id INTEGER PRIMARY KEY)`); err == nil {
		t.Fatal("read-only authority unexpectedly accepted a write")
	}
	if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only open changed schema/history: after=%+v before=%+v", after, before)
	}
	if afterMode := b008FileMode(t, path); afterMode != beforeMode {
		t.Fatalf("read-only open changed database mode from %04o to %04o", beforeMode, afterMode)
	}
}

func TestBUG011OpenExistingRestartPreflightReadOnlyAcceptsSupportedAuthoritiesWithoutWriting(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "current",
			path: b008CurrentAuthorityPath,
		},
		{
			name: "schema 24",
			path: bug011V24AuthorityPath,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := fixture.path(t)
			bug011AssertRestartPreflightReadOnly(t, ctx, path, RestartPreflightMetrics{})
		})
	}
}

func TestBUG011RestartPreflightMetricsCountEveryResumableJobPhase(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []JobPhase{
		JobPhaseCreatingSession,
		JobPhaseAcceptingCommand,
		JobPhaseAwaitingCommand,
		JobPhaseClosingSession,
	} {
		t.Run(string(phase), func(t *testing.T) {
			path := b008CurrentAuthorityPath(t)
			writable, err := OpenExistingCurrent(ctx, path)
			if err != nil {
				t.Fatalf("open current authority for fixture: %v", err)
			}
			authority, err := NewAuthorityStore(writable)
			if err != nil {
				_ = writable.Close()
				t.Fatal(err)
			}
			input := p024Acceptance(t,
				"job-bug011-"+string(phase),
				"session-bug011-"+string(phase),
				"command-bug011-"+string(phase),
				"key-bug011-"+string(phase),
				"echo restart preflight")
			job, duplicate, err := authority.AcceptJob(ctx, input)
			if err != nil || duplicate {
				_ = writable.Close()
				t.Fatalf("accept %s job=%+v duplicate=%v err=%v", phase, job, duplicate, err)
			}
			if phase != JobPhaseCreatingSession {
				job, err = authority.CheckpointJob(ctx, job.JobID, JobCheckpoint{
					ExpectedPhase: JobPhaseCreatingSession,
					NextPhase:     phase,
				})
				if err != nil {
					_ = writable.Close()
					t.Fatalf("checkpoint %s: %v", phase, err)
				}
			}
			if job.Phase != phase {
				_ = writable.Close()
				t.Fatalf("fixture job phase=%q, want %q", job.Phase, phase)
			}
			if err := writable.Close(); err != nil {
				t.Fatal(err)
			}

			bug011AssertRestartPreflightReadOnly(t, ctx, path, RestartPreflightMetrics{ResumableJobs: 1})
		})
	}
}

func TestBUG011OpenExistingRestartPreflightReadOnlyRejectsUnsupportedOrTamperedSchemaWithoutWriting(t *testing.T) {
	ctx := context.Background()
	t.Run("unsupported schema", func(t *testing.T) {
		path := b008CurrentAuthorityPath(t)
		b008SetAuthoritySchemaVersion(t, path, CurrentSchemaVersion-1)
		bug011AssertRestartPreflightOpenRejectedWithoutWriting(t, ctx, path, ErrSchemaVersion)
	})
	t.Run("tampered migration history", func(t *testing.T) {
		path := b008CurrentAuthorityPath(t)
		database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `UPDATE runner_schema_migrations
SET checksum = lower(hex(zeroblob(32)))
WHERE version = 1`); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		bug011AssertRestartPreflightOpenRejectedWithoutWriting(t, ctx, path, ErrSchemaHistory)
	})
}

func TestBUG011OpenExistingRestartPreflightReadOnlyRejectsMissingDatabaseAndOrphanSidecarWithoutWrites(t *testing.T) {
	ctx := context.Background()
	t.Run("missing database with no sidecars", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "missing", "authority.db")
		bug011AssertRestartPreflightOpenFails(t, ctx, path, ErrDatabaseMissing)
		if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing database parent was changed: stat error=%v, want not exist", err)
		}
		bug011AssertNoPath(t, path)
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			bug011AssertNoPath(t, path+suffix)
		}
	})

	t.Run("private orphan sidecar", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state, "authority.db")
		sidecarPath := path + "-wal"
		contents := []byte("orphaned private SQLite WAL fixture")
		if err := os.WriteFile(sidecarPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		beforeMode := b008FileMode(t, sidecarPath)

		// A sidecar without its database is not a first-install authority. The
		// read-only store must reject it and leave the evidence intact; the
		// installer additionally checks this artifact before allowing its true
		// first-install exception.
		bug011AssertRestartPreflightOpenFails(t, ctx, path, ErrDatabaseMissing)
		bug011AssertNoPath(t, path)
		afterContents, err := os.ReadFile(sidecarPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(afterContents) != string(contents) {
			t.Fatalf("orphan sidecar changed from %q to %q", contents, afterContents)
		}
		if afterMode := b008FileMode(t, sidecarPath); afterMode != beforeMode {
			t.Fatalf("orphan sidecar mode changed from %04o to %04o", beforeMode, afterMode)
		}
	})
}

func bug011AssertRestartPreflightReadOnly(t *testing.T, ctx context.Context, path string, want RestartPreflightMetrics) {
	t.Helper()
	before := b008ReadSchemaState(t, path)
	beforeMode := b008FileMode(t, path)

	database, err := OpenExistingRestartPreflightReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenExistingRestartPreflightReadOnly(%s) error=%v", path, err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	got, err := authority.ReadRestartPreflightMetrics(ctx)
	if err != nil {
		_ = database.Close()
		t.Fatalf("read restart preflight metrics: %v", err)
	}
	if got != want {
		_ = database.Close()
		t.Fatalf("restart preflight metrics=%+v, want %+v", got, want)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE bug011_restart_preflight_readonly_write_check (id INTEGER PRIMARY KEY)`); err == nil {
		_ = database.Close()
		t.Fatal("restart preflight reader unexpectedly accepted a write")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
		t.Fatalf("restart preflight reader changed schema/history: after=%+v before=%+v", after, before)
	}
	if afterMode := b008FileMode(t, path); afterMode != beforeMode {
		t.Fatalf("restart preflight reader changed database mode from %04o to %04o", beforeMode, afterMode)
	}
}

func bug011AssertRestartPreflightOpenRejectedWithoutWriting(t *testing.T, ctx context.Context, path string, want error) {
	t.Helper()
	before := b008ReadSchemaState(t, path)
	beforeMode := b008FileMode(t, path)
	bug011AssertRestartPreflightOpenFails(t, ctx, path, want)
	if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected restart preflight changed schema/history: after=%+v before=%+v", after, before)
	}
	if afterMode := b008FileMode(t, path); afterMode != beforeMode {
		t.Fatalf("rejected restart preflight changed database mode from %04o to %04o", beforeMode, afterMode)
	}
}

func bug011AssertRestartPreflightOpenFails(t *testing.T, ctx context.Context, path string, want error) {
	t.Helper()
	database, err := OpenExistingRestartPreflightReadOnly(ctx, path)
	if database != nil {
		_ = database.Close()
		t.Fatalf("OpenExistingRestartPreflightReadOnly(%s) returned a database", path)
	}
	if !errors.Is(err, want) {
		t.Fatalf("OpenExistingRestartPreflightReadOnly(%s) error=%v, want %v", path, err, want)
	}
}

func bug011AssertNoPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s changed: stat error=%v, want not exist", path, err)
	}
}

func bug011V24AuthorityPath(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "authority.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	p152BuildActualV24Database(t, ctx, database)
	var journalMode string
	if err := database.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&journalMode); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if journalMode != "wal" {
		_ = database.Close()
		t.Fatalf("legacy fixture journal mode=%q, want wal", journalMode)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
