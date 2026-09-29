package store

import (
	"context"
	"database/sql"
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

const backupPageBatch = 128

type onlineBackuper interface {
	NewBackup(string) (*sqlitedriver.Backup, error)
	NewRestore(string) (*sqlitedriver.Backup, error)
}

// CreateOnlineBackup publishes an owner-only, consistent SQLite snapshot at
// backupPath. It uses SQLite's online backup API, so committed pages still in
// the source WAL are included. The destination must not already exist.
func CreateOnlineBackup(ctx context.Context, source *sql.DB, backupPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return errors.New("online backup requires a source database")
	}
	if !validAbsoluteSQLitePath(backupPath) {
		return ErrDatabasePath
	}
	parent := filepath.Dir(backupPath)
	if err := ensurePrivateDirectory(parent); err != nil {
		return err
	}
	if _, err := os.Lstat(backupPath); err == nil {
		return fmt.Errorf("online backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect online backup destination: %w", err)
	}

	temporary, err := os.CreateTemp(parent, ".runner-online-backup-*")
	if err != nil {
		return fmt.Errorf("create private online backup staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure online backup staging file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close online backup staging file: %w", err)
	}

	connection, err := source.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve online backup source connection: %w", err)
	}
	defer connection.Close()
	if err := runSQLiteBackup(ctx, connection, func(raw any) (*sqlitedriver.Backup, error) {
		backuper, ok := raw.(onlineBackuper)
		if !ok {
			return nil, errors.New("SQLite driver does not support online backup")
		}
		return backuper.NewBackup(sqlitePathURI(temporaryPath, "rwc"))
	}); err != nil {
		return fmt.Errorf("copy SQLite online backup: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return fmt.Errorf("secure completed online backup: %w", err)
	}
	if err := validatePrivateDatabasePath(temporaryPath); err != nil {
		return fmt.Errorf("validate completed online backup file: %w", err)
	}
	if err := validateDatabaseContents(ctx, temporaryPath); err != nil {
		return fmt.Errorf("validate completed online backup contents: %w", err)
	}
	file, err := os.OpenFile(temporaryPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open completed online backup for sync: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync completed online backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close completed online backup: %w", err)
	}
	if err := os.Link(temporaryPath, backupPath); err != nil {
		return fmt.Errorf("publish online backup without replacing an existing path: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("remove online backup staging link: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync online backup directory: %w", err)
	}
	return nil
}

// RestoreOnlineBackup replaces databasePath from backupPath using SQLite's
// backup API. The caller must first stop every service using this authority and
// close every other handle to the database. After restoration this function
// reopens the database, checkpoints WAL, and validates its schema and contents.
func RestoreOnlineBackup(ctx context.Context, databasePath, backupPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validAbsoluteSQLitePath(databasePath) || !validAbsoluteSQLitePath(backupPath) || databasePath == backupPath {
		return ErrDatabasePath
	}
	if err := validatePrivateDatabasePath(backupPath); err != nil {
		return fmt.Errorf("validate restore backup file: %w", err)
	}
	if err := validateDatabaseContents(ctx, backupPath); err != nil {
		return fmt.Errorf("validate restore backup contents: %w", err)
	}
	databaseInfo, err := os.Lstat(databasePath)
	if err != nil {
		return fmt.Errorf("inspect stopped authority database: %w", err)
	}
	if err := validateDatabaseFile(databasePath, databaseInfo); err != nil {
		return fmt.Errorf("validate stopped authority database: %w", err)
	}
	backupInfo, err := os.Lstat(backupPath)
	if err != nil {
		return fmt.Errorf("inspect restore backup: %w", err)
	}
	if os.SameFile(databaseInfo, backupInfo) {
		return errors.New("restore backup and authority database refer to the same file")
	}

	database, err := Open(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("open stopped authority for restore: %w", err)
	}
	database.SetMaxOpenConns(1)
	connection, err := database.Conn(ctx)
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("reserve authority restore connection: %w", err)
	}
	restoreErr := runSQLiteBackup(ctx, connection, func(raw any) (*sqlitedriver.Backup, error) {
		backuper, ok := raw.(onlineBackuper)
		if !ok {
			return nil, errors.New("SQLite driver does not support online restore")
		}
		return backuper.NewRestore(sqlitePathURI(backupPath, "ro"))
	})
	connectionErr := connection.Close()
	if restoreErr != nil || connectionErr != nil {
		_ = database.Close()
		return fmt.Errorf("restore SQLite online backup: %w", errors.Join(restoreErr, connectionErr))
	}
	if err := checkpointAndClose(ctx, database); err != nil {
		return fmt.Errorf("checkpoint restored authority: %w", err)
	}

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("reopen restored authority: %w", err)
	}
	defer reopened.Close()
	connection, err = reopened.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve restored authority validation connection: %w", err)
	}
	defer connection.Close()
	if err := validateDatabaseConnection(ctx, connection); err != nil {
		return fmt.Errorf("validate restored authority: %w", err)
	}
	return nil
}

func runSQLiteBackup(ctx context.Context, connection *sql.Conn, create func(any) (*sqlitedriver.Backup, error)) error {
	var backup *sqlitedriver.Backup
	if err := connection.Raw(func(raw any) error {
		var err error
		backup, err = create(raw)
		return err
	}); err != nil {
		return err
	}
	var copyErr error
	busyDeadline := time.Now().Add(BusyTimeout)
	delay := 10 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			copyErr = err
			break
		}
		more, err := backup.Step(backupPageBatch)
		if err == nil {
			if !more {
				break
			}
			continue
		}
		if !isSQLiteBackupBusy(err) {
			copyErr = err
			break
		}
		if time.Now().After(busyDeadline) {
			copyErr = fmt.Errorf("SQLite online backup remained busy for %s: %w", BusyTimeout, err)
			break
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
			copyErr = ctx.Err()
		case <-timer.C:
		}
		if copyErr != nil {
			break
		}
		if delay < 250*time.Millisecond {
			delay *= 2
			if delay > 250*time.Millisecond {
				delay = 250 * time.Millisecond
			}
		}
	}
	return errors.Join(copyErr, backup.Finish())
}

func isSQLiteBackupBusy(err error) bool {
	if isSQLiteBusy(err) {
		return true
	}
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_LOCKED
}

func validatePrivateDatabasePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateDatabaseFile(path, info)
}

func validAbsoluteSQLitePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func sqlitePathURI(path, mode string) string {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", mode)
	query.Set("_busy_timeout", strconv.FormatInt(BusyTimeout.Milliseconds(), 10))
	uri.RawQuery = query.Encode()
	return uri.String()
}

func validateDatabaseContents(ctx context.Context, path string) error {
	database, err := sql.Open("sqlite", sqlitePathURI(path, "ro"))
	if err != nil {
		return fmt.Errorf("open SQLite snapshot read-only: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve SQLite snapshot validation connection: %w", err)
	}
	defer connection.Close()
	return validateDatabaseConnection(ctx, connection)
}

func validateDatabaseConnection(ctx context.Context, connection *sql.Conn) error {
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return fmt.Errorf("enable SQLite snapshot query-only mode: %w", err)
	}
	rows, err := connection.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("run SQLite integrity check: %w", err)
	}
	checks := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read SQLite integrity check: %w", err)
		}
		checks++
		if result != "ok" {
			_ = rows.Close()
			return fmt.Errorf("SQLite integrity check reported %q", result)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate SQLite integrity check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close SQLite integrity check: %w", err)
	}
	if checks == 0 {
		return errors.New("SQLite integrity check returned no result")
	}
	rows, err = connection.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run SQLite foreign-key check: %w", err)
	}
	if rows.Next() {
		_ = rows.Close()
		return errors.New("SQLite foreign-key check found a violation")
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate SQLite foreign-key check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close SQLite foreign-key check: %w", err)
	}
	version, err := userVersion(ctx, connection)
	if err != nil {
		return err
	}
	if version != CurrentSchemaVersion {
		return fmt.Errorf("SQLite snapshot schema version = %d, want %d", version, CurrentSchemaVersion)
	}
	if err := verifyMigrationHistory(ctx, connection, version); err != nil {
		return err
	}
	return nil
}

func checkpointAndClose(ctx context.Context, database *sql.DB) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("reserve restored database checkpoint connection: %w", err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := connection.QueryRowContext(ctx, "PRAGMA wal_checkpoint(FULL)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		_ = connection.Close()
		_ = database.Close()
		return fmt.Errorf("run restored database WAL checkpoint: %w", err)
	}
	connectionErr := connection.Close()
	closeErr := database.Close()
	var checkpointErr error
	if busy != 0 || logFrames != checkpointedFrames {
		checkpointErr = fmt.Errorf("restored database WAL checkpoint incomplete: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	return errors.Join(checkpointErr, connectionErr, closeErr)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
