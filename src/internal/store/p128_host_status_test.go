//go:build p128hoststatus

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"testing"
	"time"
)

const p128UbuntuDatabasePath = "/home/ubuntu/.local/share/remote-session-runner/state/remote.db"

type p128ActiveWorkCounts struct {
	Sessions int64 `json:"active_sessions"`
	Commands int64 `json:"running_commands"`
	Slots    int64 `json:"unreleased_slots"`
	Jobs     int64 `json:"unfinished_jobs"`
}

func TestP128UbuntuNoActiveWorkBeforeRestart(t *testing.T) {
	if os.Getenv("RSR_P128_HOST_STATUS") != "1" {
		t.Skip("set RSR_P128_HOST_STATUS=1 to inspect Ubuntu Runner work before restart")
	}
	p128RequireUbuntuHost(t)
	db := p128OpenUbuntuDatabaseReadOnly(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts := p128ReadActiveWorkCounts(t, ctx, db)
	t.Logf("P128 Ubuntu read-only active-work counts: %s", p128CountsJSON(counts))
	if counts.Sessions != 0 || counts.Commands != 0 || counts.Slots != 0 || counts.Jobs != 0 {
		t.Fatalf("refusing P128 service restart while Ubuntu work remains active: %s", p128CountsJSON(counts))
	}
}

func TestP128UbuntuHealthWriteProbeRejectsReadOnlyDatabase(t *testing.T) {
	if os.Getenv("RSR_P128_HOST_STATUS") != "1" {
		t.Skip("set RSR_P128_HOST_STATUS=1 to inspect Ubuntu Runner work before restart")
	}
	p128RequireUbuntuHost(t)
	db := p128OpenUbuntuDatabaseReadOnly(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read Ubuntu schema version: %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("Ubuntu schema version=%d; P128 health migration version=%d is required before the read-only write-probe gate", version, CurrentSchemaVersion)
	}
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.CheckWritable(ctx, "linux_runnerd_host_gate"); !errors.Is(err, ErrHealthProbe) {
		t.Fatalf("read-only Ubuntu health write probe error=%v, want ErrHealthProbe", err)
	}
	t.Log("P128 Ubuntu read-only authority reports the expected secret-free SQLite write-probe failure")
}

func p128RequireUbuntuHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Fatalf("P128 Ubuntu host reader must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "ubuntu" || current.Uid != "1001" {
		t.Fatalf("P128 Ubuntu host account=%v err=%v, want ubuntu uid 1001", current, err)
	}
}

func p128OpenUbuntuDatabaseReadOnly(t *testing.T) *sql.DB {
	t.Helper()
	databaseURI := url.URL{Scheme: "file", Path: p128UbuntuDatabasePath}
	query := databaseURI.Query()
	query.Set("mode", "ro")
	query.Set("_busy_timeout", strconv.FormatInt(BusyTimeout.Milliseconds(), 10))
	databaseURI.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURI.String())
	if err != nil {
		t.Fatalf("open Ubuntu authority database read-only: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to Ubuntu authority database read-only: %v", err)
	}
	return db
}

func p128ReadActiveWorkCounts(t *testing.T, ctx context.Context, db *sql.DB) p128ActiveWorkCounts {
	t.Helper()
	var counts p128ActiveWorkCounts
	queries := []struct {
		query string
		dest  *int64
	}{
		{`SELECT count(*) FROM exec_sessions WHERE state IN ('requested', 'creating', 'ready', 'busy', 'closing')`, &counts.Sessions},
		{`SELECT count(*) FROM exec_commands WHERE state IN ('running', 'cancelling')`, &counts.Commands},
		{`SELECT count(*) FROM exec_command_slots WHERE stop_confirmed_at IS NULL AND released_at IS NULL`, &counts.Slots},
		{`SELECT count(*) FROM exec_jobs WHERE phase NOT IN ('complete', 'failed', 'lost')`, &counts.Jobs},
	}
	for _, item := range queries {
		if err := db.QueryRowContext(ctx, item.query).Scan(item.dest); err != nil {
			t.Fatalf("read bounded Ubuntu active-work count: %v", err)
		}
	}
	return counts
}

func p128CountsJSON(counts p128ActiveWorkCounts) string {
	return fmt.Sprintf("{\"active_sessions\":%d,\"running_commands\":%d,\"unreleased_slots\":%d,\"unfinished_jobs\":%d}", counts.Sessions, counts.Commands, counts.Slots, counts.Jobs)
}
