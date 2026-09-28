package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestP128HealthWriteProbeCommitsAndDetectsReadOnlyDatabase(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), filepath.Join(root.Path(), "state", "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time {
		return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := authority.CheckWritable(context.Background(), "mac_ingress"); err != nil {
			t.Fatalf("writable probe failed: %v", err)
		}
	}
	var count int
	var checkedAt string
	if err := db.QueryRow("SELECT count(*), max(checked_at) FROM runner_health_probes WHERE component = ?", "mac_ingress").Scan(&count, &checkedAt); err != nil {
		t.Fatal(err)
	}
	if count != 1 || checkedAt == "" {
		t.Fatalf("health probe row count=%d checked_at=%q", count, checkedAt)
	}
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	if err := authority.CheckWritable(context.Background(), "mac_ingress"); !errors.Is(err, ErrHealthProbe) {
		t.Fatalf("read-only probe error=%v, want ErrHealthProbe", err)
	}
}
