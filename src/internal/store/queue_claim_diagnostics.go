package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type queueClaimDiagnosticContextKey struct{}

type queueClaimDiagnosticScope struct {
	mu    sync.RWMutex
	stage string
}

type queueClaimDiagnosticError struct {
	stage string
	cause error
}

func (e *queueClaimDiagnosticError) Error() string { return e.cause.Error() }
func (e *queueClaimDiagnosticError) Unwrap() error { return e.cause }

// QueueClaimDiagnostic contains only bounded, sanitized metadata for one
// unexpected queue-claim failure. It never includes the underlying error text.
type QueueClaimDiagnostic struct {
	Stage              string
	Category           string
	SQLitePrimaryCode  int
	SQLiteExtendedCode int
	HasSQLiteCode      bool
	OSErrorCode        int
	HasOSErrorCode     bool
}

func withQueueClaimDiagnosticScope(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	scope := &queueClaimDiagnosticScope{stage: "validate_claim_configuration"}
	return context.WithValue(ctx, queueClaimDiagnosticContextKey{}, scope)
}

func setQueueClaimDiagnosticStage(ctx context.Context, stage string) {
	if ctx == nil {
		return
	}
	scope, _ := ctx.Value(queueClaimDiagnosticContextKey{}).(*queueClaimDiagnosticScope)
	if scope == nil {
		return
	}
	scope.mu.Lock()
	scope.stage = stage
	scope.mu.Unlock()
}

func queueClaimDiagnosticErrorFor(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var existing *queueClaimDiagnosticError
	if errors.As(err, &existing) {
		return err
	}
	if ctx == nil {
		return err
	}
	scope, _ := ctx.Value(queueClaimDiagnosticContextKey{}).(*queueClaimDiagnosticScope)
	if scope == nil {
		return err
	}
	scope.mu.RLock()
	stage := scope.stage
	scope.mu.RUnlock()
	return &queueClaimDiagnosticError{stage: stage, cause: err}
}

// QueueClaimDiagnostics classifies a queue-claim error without exposing its
// unrestricted error text, SQL, values, scripts, credentials, or paths.
func QueueClaimDiagnostics(err error) QueueClaimDiagnostic {
	diagnostic := QueueClaimDiagnostic{Stage: "unclassified", Category: "logical"}
	var claimError *queueClaimDiagnosticError
	if errors.As(err, &claimError) && claimError.stage != "" {
		diagnostic.Stage = claimError.stage
	}
	var sqliteError *sqlitedriver.Error
	if errors.As(err, &sqliteError) {
		code := sqliteError.Code()
		diagnostic.SQLitePrimaryCode = code & 0xff
		diagnostic.SQLiteExtendedCode = code
		diagnostic.HasSQLiteCode = true
		diagnostic.Category = sqliteFailureCategory(diagnostic.SQLitePrimaryCode)
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		diagnostic.OSErrorCode = int(errno)
		diagnostic.HasOSErrorCode = true
		if !diagnostic.HasSQLiteCode {
			diagnostic.Category = "os"
		}
	} else {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			var pathErrno syscall.Errno
			if errors.As(pathError.Err, &pathErrno) {
				diagnostic.Category = "os"
				diagnostic.OSErrorCode = int(pathErrno)
				diagnostic.HasOSErrorCode = true
			}
		}
	}
	return diagnostic
}

func sqliteFailureCategory(primaryCode int) string {
	switch primaryCode {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return "sqlite_lock"
	case sqlite3.SQLITE_CONSTRAINT:
		return "sqlite_constraint"
	case sqlite3.SQLITE_IOERR:
		return "sqlite_io"
	case sqlite3.SQLITE_FULL:
		return "sqlite_capacity"
	case sqlite3.SQLITE_READONLY:
		return "sqlite_readonly"
	case sqlite3.SQLITE_CANTOPEN:
		return "sqlite_open"
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return "sqlite_corrupt"
	default:
		return "sqlite"
	}
}
