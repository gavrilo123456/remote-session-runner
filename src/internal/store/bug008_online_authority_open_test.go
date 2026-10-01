package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestBUG008OpenExistingCurrentRejectsMissingDatabaseWithoutCreatingIt(t *testing.T) {
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "missing", "authority.db")

	database, err := OpenExistingCurrent(context.Background(), path)
	if database != nil {
		_ = database.Close()
		t.Fatal("OpenExistingCurrent returned a database for a missing path")
	}
	if !errors.Is(err, ErrDatabaseMissing) {
		t.Fatalf("OpenExistingCurrent(missing) error=%v, want %v", err, ErrDatabaseMissing)
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing database parent was changed: stat error=%v, want not exist", statErr)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing database file was created: stat error=%v, want not exist", statErr)
	}
}

func TestBUG008OpenExistingCurrentRejectsOlderOrTamperedSchemaWithoutMigration(t *testing.T) {
	ctx := context.Background()
	t.Run("older schema", func(t *testing.T) {
		path := b008CurrentAuthorityPath(t)
		b008SetAuthoritySchemaVersion(t, path, CurrentSchemaVersion-1)
		before := b008ReadSchemaState(t, path)
		beforeMode := b008FileMode(t, path)

		database, err := OpenExistingCurrent(ctx, path)
		if database != nil {
			_ = database.Close()
			t.Fatal("OpenExistingCurrent returned an older-schema database")
		}
		if !errors.Is(err, ErrSchemaVersion) {
			t.Fatalf("OpenExistingCurrent(older schema) error=%v, want %v", err, ErrSchemaVersion)
		}
		if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
			t.Fatalf("older schema was migrated or changed: after=%+v before=%+v", after, before)
		}
		if afterMode := b008FileMode(t, path); afterMode != beforeMode {
			t.Fatalf("older database mode changed from %04o to %04o", beforeMode, afterMode)
		}
	})

	t.Run("tampered history", func(t *testing.T) {
		path := b008CurrentAuthorityPath(t)
		database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `UPDATE runner_schema_migrations SET checksum = '0000000000000000000000000000000000000000000000000000000000000000' WHERE version = 1`); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		before := b008ReadSchemaState(t, path)

		database, err = OpenExistingCurrent(ctx, path)
		if database != nil {
			_ = database.Close()
			t.Fatal("OpenExistingCurrent returned a database with tampered migration history")
		}
		if !errors.Is(err, ErrSchemaHistory) {
			t.Fatalf("OpenExistingCurrent(tampered history) error=%v, want %v", err, ErrSchemaHistory)
		}
		if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(after, before) {
			t.Fatalf("tampered migration history was changed: after=%+v before=%+v", after, before)
		}
	})
}

func TestBUG008OpenExistingCurrentAcceptsCurrentWritableAuthorityWithoutMigration(t *testing.T) {
	ctx := context.Background()
	path := b008CurrentAuthorityPath(t)
	before := b008ReadSchemaState(t, path)
	beforeMode := b008FileMode(t, path)

	database, err := OpenExistingCurrent(ctx, path)
	if err != nil {
		t.Fatalf("OpenExistingCurrent(current) error=%v", err)
	}
	checkConnectionSettings(t, database)
	if after := b008ReadSchemaStateFromDatabase(t, database); !reflect.DeepEqual(after, before) {
		_ = database.Close()
		t.Fatalf("current schema/history changed while opening: after=%+v before=%+v", after, before)
	}
	if afterMode := b008FileMode(t, path); afterMode != beforeMode {
		_ = database.Close()
		t.Fatalf("current database mode changed from %04o to %04o", beforeMode, afterMode)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE bug008_writable_authority_check (id INTEGER PRIMARY KEY)`); err != nil {
		_ = database.Close()
		t.Fatalf("returned current authority is not writable: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBUG008OpenExistingCurrentCoexistsWithLiveWALAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "authority.db")
	primary, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	primaryConnection, err := primary.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer primaryConnection.Close()

	var journalMode string
	if err := primaryConnection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("live authority journal_mode=%q, want wal", journalMode)
	}
	if _, err := primaryConnection.ExecContext(ctx, `CREATE TABLE bug008_live_wal_authority_check (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	before := b008ReadSchemaStateFromDatabase(t, primary)

	type openResult struct {
		database *sql.DB
		err      error
	}
	result := make(chan openResult, 1)
	go func() {
		database, openErr := OpenExistingCurrent(ctx, path)
		result <- openResult{database: database, err: openErr}
	}()

	var helper *sql.DB
	select {
	case opened := <-result:
		if opened.err != nil {
			t.Fatalf("open alongside live WAL authority: %v", opened.err)
		}
		helper = opened.database
	case <-ctx.Done():
		t.Fatalf("open alongside live WAL authority exceeded deadline: %v", ctx.Err())
	}
	defer helper.Close()

	if _, err := helper.ExecContext(ctx, `INSERT INTO bug008_live_wal_authority_check(id, value) VALUES(1, 'helper-write')`); err != nil {
		t.Fatalf("bounded helper write alongside live authority: %v", err)
	}
	var value string
	if err := primaryConnection.QueryRowContext(ctx, `SELECT value FROM bug008_live_wal_authority_check WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("live authority did not remain usable after helper write: %v", err)
	}
	if value != "helper-write" {
		t.Fatalf("live authority observed value=%q, want helper-write", value)
	}
	if err := primaryConnection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("helper changed live authority journal_mode to %q, want wal", journalMode)
	}
	if after := b008ReadSchemaStateFromDatabase(t, primary); !reflect.DeepEqual(after, before) {
		t.Fatalf("helper changed current schema/history beside live authority: after=%+v before=%+v", after, before)
	}
}

type b008SchemaState struct {
	Version int
	Ledger  []string
}

func b008CurrentAuthorityPath(t *testing.T) string {
	t.Helper()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "authority.db")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func b008SetAuthoritySchemaVersion(t *testing.T, path string, version int) {
	t.Helper()
	database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM runner_schema_migrations WHERE version > ?`, version); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = ` + strconv.Itoa(version)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func b008ReadSchemaState(t *testing.T, path string) b008SchemaState {
	t.Helper()
	database, err := sql.Open("sqlite", sqlitePathURI(path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	return b008ReadSchemaStateFromDatabase(t, database)
}

func b008ReadSchemaStateFromDatabase(t *testing.T, database *sql.DB) b008SchemaState {
	t.Helper()
	var state b008SchemaState
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&state.Version); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(`SELECT printf('%d:%s:%s', version, name, checksum) FROM runner_schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		state.Ledger = append(state.Ledger, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return state
}

func b008FileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
