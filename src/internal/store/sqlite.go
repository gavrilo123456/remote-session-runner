package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	// BusyTimeout is the bounded wait SQLite uses for database locks.
	BusyTimeout = 5 * time.Second

	// CurrentSchemaVersion is the last migration applied before Open returns.
	CurrentSchemaVersion = 7
)

var (
	ErrDatabasePath        = errors.New("database path must be an absolute clean file path")
	ErrDatabasePermissions = errors.New("database path must be owned by the current user with owner-only modes")
	ErrSchemaVersion       = errors.New("database schema version is unsupported")
	ErrSchemaHistory       = errors.New("database schema migration history is inconsistent")
	ErrSQLitePragmas       = errors.New("required SQLite settings are unavailable")
)

//go:embed migrations/0001_schema_migrations.sql
var schemaMigrationsSQL string

//go:embed migrations/0002_exec_sessions.sql
var execSessionsSQL string

//go:embed migrations/0003_session_acceptance.sql
var sessionAcceptanceSQL string

//go:embed migrations/0004_exec_commands.sql
var execCommandsSQL string

//go:embed migrations/0005_exec_command_slots.sql
var execCommandSlotsSQL string

//go:embed migrations/0006_exec_jobs.sql
var execJobsSQL string

//go:embed migrations/0007_retention_gc.sql
var retentionGCSQL string

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{{
	version: 1,
	name:    "schema_migrations",
	sql:     schemaMigrationsSQL,
}, {
	version: 2,
	name:    "exec_sessions",
	sql:     execSessionsSQL,
}, {
	version: 3,
	name:    "session_acceptance",
	sql:     sessionAcceptanceSQL,
}, {
	version: 4,
	name:    "exec_commands",
	sql:     execCommandsSQL,
}, {
	version: 5,
	name:    "exec_command_slots",
	sql:     execCommandSlotsSQL,
}, {
	version: 6,
	name:    "exec_jobs",
	sql:     execJobsSQL,
}, {
	version: 7,
	name:    "retention_gc",
	sql:     retentionGCSQL,
}}

// Open opens a private SQLite database, applies required per-connection
// settings, and completes all schema migrations before returning it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, ErrDatabasePath
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := ensurePrivateDatabaseFile(path); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := checkPrivateSidecar(path + suffix); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to SQLite database: %w", err)
	}
	if err := checkSupportedSchemaVersion(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureWAL(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := secureCreatedSidecar(path + suffix); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func dataSourceName(path string) string {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("_busy_timeout", strconv.FormatInt(BusyTimeout.Milliseconds(), 10))
	query.Set("_foreign_keys", "on")
	query.Set("_synchronous", "FULL")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func ensureWAL(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite WAL connection: %w", err)
	}
	defer connection.Close()

	deadline := time.Now().Add(BusyTimeout)
	delay := 10 * time.Millisecond
	for {
		var mode string
		err := connection.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode)
		if err == nil {
			if mode != "wal" {
				return fmt.Errorf("%w: journal_mode=%q", ErrSQLitePragmas, mode)
			}
			return nil
		}
		if !isSQLiteBusy(err) {
			return fmt.Errorf("set SQLite journal mode to WAL: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("set SQLite journal mode to WAL within %s: %w", BusyTimeout, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 100*time.Millisecond {
			delay *= 2
			if delay > 100*time.Millisecond {
				delay = 100 * time.Millisecond
			}
		}
	}
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite connection: %w", err)
	}
	defer connection.Close()

	var journalMode string
	var foreignKeys, busyTimeout, synchronous int64
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read SQLite journal mode: %w", err)
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read SQLite foreign-key setting: %w", err)
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read SQLite busy timeout: %w", err)
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("read SQLite synchronous setting: %w", err)
	}
	if journalMode != "wal" || foreignKeys != 1 || busyTimeout != BusyTimeout.Milliseconds() || synchronous != 2 {
		return fmt.Errorf("%w: journal_mode=%q foreign_keys=%d busy_timeout=%d synchronous=%d", ErrSQLitePragmas, journalMode, foreignKeys, busyTimeout, synchronous)
	}
	return nil
}

func checkSupportedSchemaVersion(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite schema connection: %w", err)
	}
	defer connection.Close()
	version, err := userVersion(ctx, connection)
	if err != nil {
		return err
	}
	if version < 0 || version > CurrentSchemaVersion {
		return fmt.Errorf("%w: got %d, current %d", ErrSchemaVersion, version, CurrentSchemaVersion)
	}
	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite migration connection: %w", err)
	}
	defer connection.Close()

	for {
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return fmt.Errorf("begin SQLite migration transaction: %w", err)
		}
		committed := false
		rollback := func() {
			if !committed {
				_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			}
		}

		version, err := userVersion(ctx, connection)
		if err != nil {
			rollback()
			return err
		}
		if version > CurrentSchemaVersion || version < 0 {
			rollback()
			return fmt.Errorf("%w: got %d, current %d", ErrSchemaVersion, version, CurrentSchemaVersion)
		}
		if err := verifyMigrationHistory(ctx, connection, version); err != nil {
			rollback()
			return err
		}
		if version == CurrentSchemaVersion {
			if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
				rollback()
				return fmt.Errorf("commit SQLite migration check: %w", err)
			}
			committed = true
			return nil
		}

		next := migrations[version]
		if _, err := connection.ExecContext(ctx, next.sql); err != nil {
			rollback()
			return fmt.Errorf("apply SQLite migration %d (%s): %w", next.version, next.name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(next.sql)))
		if _, err := connection.ExecContext(ctx,
			"INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES(?, ?, ?, ?)",
			next.version, next.name, checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			rollback()
			return fmt.Errorf("record SQLite migration %d: %w", next.version, err)
		}
		if _, err := connection.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(next.version)); err != nil {
			rollback()
			return fmt.Errorf("set SQLite schema version %d: %w", next.version, err)
		}
		if err := verifyMigrationHistory(ctx, connection, next.version); err != nil {
			rollback()
			return err
		}
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			rollback()
			return fmt.Errorf("commit SQLite migration %d: %w", next.version, err)
		}
		committed = true
	}
}

func userVersion(ctx context.Context, connection *sql.Conn) (int, error) {
	var version int
	if err := connection.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read SQLite schema version: %w", err)
	}
	return version, nil
}

func verifyMigrationHistory(ctx context.Context, connection *sql.Conn, version int) error {
	if version == 0 {
		var ledgerCount int
		if err := connection.QueryRowContext(ctx,
			"SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='runner_schema_migrations'").Scan(&ledgerCount); err != nil {
			return fmt.Errorf("inspect SQLite migration ledger: %w", err)
		}
		if ledgerCount != 0 {
			return ErrSchemaHistory
		}
		return nil
	}

	rows, err := connection.QueryContext(ctx, "SELECT version, name, checksum FROM runner_schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read SQLite migration ledger: %w", ErrSchemaHistory)
	}
	defer rows.Close()
	if version > len(migrations) {
		return ErrSchemaVersion
	}
	index := 0
	for rows.Next() {
		var got migrationRecord
		if err := rows.Scan(&got.version, &got.name, &got.checksum); err != nil {
			return fmt.Errorf("read SQLite migration record: %w", ErrSchemaHistory)
		}
		if index >= version || got.version != migrations[index].version || got.name != migrations[index].name || got.checksum != migrationChecksum(migrations[index]) {
			return ErrSchemaHistory
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite migration ledger: %w", err)
	}
	if index != version {
		return ErrSchemaHistory
	}
	return nil
}

type migrationRecord struct {
	version  int
	name     string
	checksum string
}

func migrationChecksum(value migration) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value.sql)))
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private database directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: database directory", ErrDatabasePermissions)
	}
	return nil
}

func ensurePrivateDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		return validateDatabaseFile(path, info)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect SQLite database file: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(path)
			if statErr == nil {
				return validateDatabaseFile(path, info)
			}
		}
		return fmt.Errorf("create owner-only SQLite database file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set SQLite database file mode: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close new SQLite database file: %w", err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect new SQLite database file: %w", err)
	}
	return validateDatabaseFile(path, info)
}

func validateDatabaseFile(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: %s", ErrDatabasePermissions, filepath.Base(path))
	}
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDatabasePermissions, filepath.Base(path))
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("%w: %s", ErrDatabasePermissions, filepath.Base(path))
	}
	return nil
}

func checkPrivateSidecar(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect SQLite sidecar: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: SQLite sidecar %s", ErrDatabasePermissions, filepath.Base(path))
	}
	return nil
}

func secureCreatedSidecar(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: SQLite sidecar %s", ErrDatabasePermissions, filepath.Base(path))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure SQLite sidecar %s: %w", filepath.Base(path), err)
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint32(stat.Uid) == uint32(os.Geteuid())
}
