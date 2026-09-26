package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestP011_D14OpensBothSelectedPathShapesWithRequiredSQLiteSettings(t *testing.T) {
	for _, fixture := range []struct {
		name string
		dir  string
	}{
		{name: "Mac-style path with spaces", dir: "Application Support/Runner/state"},
		{name: "Linux-style path", dir: "home/ubuntu/.local/share/remote-session-runner/state"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := testfixture.New(t)
			path := filepath.Join(root.Path(), filepath.FromSlash(fixture.dir), "authority.db")
			db, err := Open(context.Background(), path)
			if err != nil {
				t.Fatalf("Open(%s) error = %v", fixture.name, err)
			}
			defer db.Close()

			if got := readUserVersion(t, db); got != CurrentSchemaVersion {
				t.Fatalf("schema version = %d, want %d", got, CurrentSchemaVersion)
			}
			checkConnectionSettings(t, db)
			checkPrivateModes(t, path)
		})
	}
}

func TestP011_D14MigratesPriorSchemaPreservingRowsAndConstraints(t *testing.T) {
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "prior.db")
	createPriorSchema(t, path)

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(prior schema) error = %v", err)
	}

	var label string
	if err := db.QueryRow("SELECT label FROM prior_records WHERE id = 7").Scan(&label); err != nil {
		t.Fatalf("read preserved prior-schema row: %v", err)
	}
	if label != "retained-value" {
		t.Fatalf("preserved label = %q, want retained-value", label)
	}
	if _, err := db.Exec("INSERT INTO prior_records(id, label, token) VALUES(8, 'duplicate', 'unique-token')"); err == nil {
		t.Fatal("prior-schema UNIQUE constraint was not preserved")
	}
	if _, err := db.Exec("INSERT INTO prior_children(id, parent_id) VALUES(9, 404)"); err == nil {
		t.Fatal("foreign-key constraint was not enabled after migration")
	}
	if got := readUserVersion(t, db); got != CurrentSchemaVersion {
		t.Fatalf("migrated user_version = %d, want %d", got, CurrentSchemaVersion)
	}
	var name, checksum, appliedAt string
	if err := db.QueryRow("SELECT name, checksum, applied_at FROM runner_schema_migrations WHERE version = 1").Scan(&name, &checksum, &appliedAt); err != nil {
		t.Fatalf("read migration record: %v", err)
	}
	if name != migrations[0].name || checksum != migrationChecksum(migrations[0]) {
		t.Fatalf("migration record = (%q, %q), want (%q, %q)", name, checksum, migrations[0].name, migrationChecksum(migrations[0]))
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil || parsedTime.Location() != time.UTC {
		t.Fatalf("migration applied_at = %q, error = %v; want RFC3339 UTC", appliedAt, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated database: %v", err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer reopened.Close()
	if got := readUserVersion(t, reopened); got != CurrentSchemaVersion {
		t.Fatalf("reopened schema version = %d, want %d", got, CurrentSchemaVersion)
	}
	if err := reopened.QueryRow("SELECT label FROM prior_records WHERE id = 7").Scan(&label); err != nil || label != "retained-value" {
		t.Fatalf("reopened preserved row = %q, error = %v", label, err)
	}
	checkConnectionSettings(t, reopened)
	checkPrivateModes(t, path)
}

func TestP011_RejectsUnsafeDatabasePathsAndModes(t *testing.T) {
	t.Run("relative path", func(t *testing.T) {
		if _, err := Open(context.Background(), "relative.db"); !errors.Is(err, ErrDatabasePath) {
			t.Fatalf("Open(relative path) error = %v, want ErrDatabasePath", err)
		}
	})

	t.Run("insecure parent directory", func(t *testing.T) {
		root := testfixture.New(t)
		path := filepath.Join(root.Path(), "state", "local.db")
		if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrDatabasePermissions) {
			t.Fatalf("Open(insecure parent) error = %v, want ErrDatabasePermissions", err)
		}
	})

	t.Run("group-readable database", func(t *testing.T) {
		root := testfixture.New(t)
		path := filepath.Join(root.Path(), "state", "local.db")
		if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrDatabasePermissions) {
			t.Fatalf("Open(group-readable database) error = %v, want ErrDatabasePermissions", err)
		}
	})

	t.Run("symlink database", func(t *testing.T) {
		root := testfixture.New(t)
		target := filepath.Join(root.Path(), "state", "target.db")
		if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root.Path(), "state", "linked.db")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), link); !errors.Is(err, ErrDatabasePermissions) {
			t.Fatalf("Open(symlink database) error = %v, want ErrDatabasePermissions", err)
		}
	})
}

func TestP011_RejectsFutureSchemaAndTamperedMigrationHistory(t *testing.T) {
	t.Run("future schema", func(t *testing.T) {
		root := testfixture.New(t)
		path := filepath.Join(root.Path(), "state", "future.db")
		createSQLiteFile(t, path, "PRAGMA user_version = 99")
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrSchemaVersion) {
			t.Fatalf("Open(future schema) error = %v, want ErrSchemaVersion", err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var journalMode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatal(err)
		}
		if journalMode != "delete" {
			t.Fatalf("unsupported future schema was changed to journal mode %q", journalMode)
		}
	})

	t.Run("tampered history", func(t *testing.T) {
		root := testfixture.New(t)
		path := filepath.Join(root.Path(), "state", "tampered.db")
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE runner_schema_migrations SET checksum = ? WHERE version = 1", "0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrSchemaHistory) {
			t.Fatalf("Open(tampered migration history) error = %v, want ErrSchemaHistory", err)
		}
	})
}

func TestP011_ConcurrentOpenersApplyMigrationsOnce(t *testing.T) {
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "shared.db")

	const openers = 6
	dbs := make([]*sql.DB, openers)
	errs := make([]error, openers)
	var wait sync.WaitGroup
	for i := range dbs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			dbs[index], errs[index] = Open(context.Background(), path)
		}(i)
	}
	wait.Wait()
	for i, err := range errs {
		if err != nil {
			for _, db := range dbs {
				if db != nil {
					_ = db.Close()
				}
			}
			t.Fatalf("opener %d error = %v", i, err)
		}
	}
	defer func() {
		for _, db := range dbs {
			if db != nil {
				_ = db.Close()
			}
		}
	}()
	for i, db := range dbs {
		if got := readUserVersion(t, db); got != CurrentSchemaVersion {
			t.Fatalf("opener %d schema version = %d, want %d", i, got, CurrentSchemaVersion)
		}
	}
	var count int
	if err := dbs[0].QueryRow("SELECT count(*) FROM runner_schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("migration record count = %d, want %d", count, len(migrations))
	}
}

func createPriorSchema(t *testing.T, path string) {
	t.Helper()
	createSQLiteFile(t, path, `
CREATE TABLE prior_records (
    id INTEGER PRIMARY KEY,
    label TEXT NOT NULL,
    token TEXT NOT NULL UNIQUE
);
CREATE TABLE prior_children (
    id INTEGER PRIMARY KEY,
    parent_id INTEGER NOT NULL REFERENCES prior_records(id)
);
INSERT INTO prior_records(id, label, token) VALUES(7, 'retained-value', 'unique-token');
INSERT INTO prior_children(id, parent_id) VALUES(8, 7);
PRAGMA user_version = 0;
`)
}

func createSQLiteFile(t *testing.T, path, statements string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(fixture) error = %v", err)
	}
	if _, err := db.Exec(statements); err != nil {
		_ = db.Close()
		t.Fatalf("create SQLite fixture schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close SQLite fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("set SQLite fixture mode: %v", err)
	}
}

func readUserVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

func checkConnectionSettings(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxOpenConns(4)
	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < cap(connections); i++ {
		connection, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("open pooled connection %d: %v", i, err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	for i, connection := range connections {
		var journalMode string
		var foreignKeys, busyTimeout, synchronous int64
		for _, check := range []struct {
			pragma string
			dest   any
		}{
			{"journal_mode", &journalMode},
			{"foreign_keys", &foreignKeys},
			{"busy_timeout", &busyTimeout},
			{"synchronous", &synchronous},
		} {
			if err := connection.QueryRowContext(context.Background(), "PRAGMA "+check.pragma).Scan(check.dest); err != nil {
				t.Fatalf("connection %d read PRAGMA %s: %v", i, check.pragma, err)
			}
		}
		if journalMode != "wal" || foreignKeys != 1 || busyTimeout != BusyTimeout.Milliseconds() || synchronous != 2 {
			t.Errorf("connection %d PRAGMAs = journal %q, fk %d, timeout %d, synchronous %d", i, journalMode, foreignKeys, busyTimeout, synchronous)
		}
	}
}

func checkPrivateModes(t *testing.T, path string) {
	t.Helper()
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("database directory mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("database mode = %04o, want 0600", got)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			t.Errorf("SQLite sidecar %s was not present while the database is open", suffix)
			continue
		}
		if err != nil {
			t.Fatalf("stat SQLite sidecar %s: %v", suffix, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("SQLite sidecar %s mode = %04o, want 0600", suffix, got)
		}
	}
}
