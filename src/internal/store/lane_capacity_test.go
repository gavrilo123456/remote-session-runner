package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"remote-session-runner/src/internal/testfixture"
)

func TestLaneByteReservationPersistsAndRejectsLimitOrIdentityChanges(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "lane-capacity.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE lane_capacity_test_effects (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := authority.SetLaneByteBudget(ctx, "dev", 1000, "not-a-digest"); !errors.Is(err, ErrLaneCapacityInvalid) {
		t.Fatalf("invalid policy digest = %v", err)
	}
	if err := authority.SetLaneByteBudget(ctx, "dev", 1000, digest); err != nil {
		t.Fatal(err)
	}
	mutate := func(id string) func(context.Context, *sql.Conn) error {
		return func(ctx context.Context, connection *sql.Conn) error {
			_, err := connection.ExecContext(ctx, `INSERT OR IGNORE INTO lane_capacity_test_effects(id) VALUES(?)`, id)
			return err
		}
	}
	if err := authority.AdmitLaneBytes(ctx, "dev", "session-1", []LaneByteAllocation{{Category: "workspace", Bytes: 600}}, mutate("session-1")); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen authority: %v", err)
	}
	defer database.Close()
	authority, err = NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := authority.LaneByteCapacitySnapshot(ctx, "dev")
	if err != nil || snapshot.MaxBytes != 1000 || snapshot.ReservedBytes != 600 || snapshot.PolicyDigest != digest {
		t.Fatalf("reopened capacity snapshot=%+v err=%v", snapshot, err)
	}
	if err := authority.AdmitLaneBytes(ctx, "dev", "session-1", []LaneByteAllocation{{Category: "workspace", Bytes: 600}}, mutate("session-1")); err != nil {
		t.Fatalf("same reservation retry: %v", err)
	}
	if err := authority.AdmitLaneBytes(ctx, "dev", "session-1", []LaneByteAllocation{{Category: "workspace", Bytes: 601}}, mutate("changed")); !errors.Is(err, ErrLaneCapacityReservationConflict) {
		t.Fatalf("changed reservation = %v, want identity conflict", err)
	}
	if err := authority.AdmitLaneBytes(ctx, "dev", "session-1", []LaneByteAllocation{{Category: "response", Bytes: 600}}, mutate("changed-category")); !errors.Is(err, ErrLaneCapacityReservationConflict) {
		t.Fatalf("changed reservation category = %v, want identity conflict", err)
	}
	if err := authority.AdmitLaneBytes(ctx, "dev", "session-3", []LaneByteAllocation{{Category: "arbitrary", Bytes: 1}}, mutate("unknown-category")); !errors.Is(err, ErrLaneCapacityInvalid) {
		t.Fatalf("unknown allocation category = %v, want invalid reservation", err)
	}
	if err := authority.AdmitLaneBytes(ctx, "dev", "session-2", []LaneByteAllocation{{Category: "workspace", Bytes: 401}}, mutate("over-limit")); !errors.Is(err, ErrLaneCapacityExceeded) {
		t.Fatalf("over-limit admission = %v, want capacity exceeded", err)
	}
	if err := authority.SetLaneByteBudget(ctx, "dev", 500, digest); !errors.Is(err, ErrLaneCapacityExceeded) {
		t.Fatalf("budget shrink below retained reservation = %v, want capacity exceeded", err)
	}
	var effects int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM lane_capacity_test_effects`).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("acceptance effects=%d err=%v, want one durable effect", effects, err)
	}
}

func TestLaneByteAdmissionRollsBackReservationAndAcceptanceTogether(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, authority := laneCapacityAuthority(t, filepath.Join(root.Path(), "state", "lane-capacity-rollback.db"))
	defer database.Close()
	callbackCalled := false
	if err := authority.AdmitLaneBytes(ctx, "qas", "request-unconfigured", []LaneByteAllocation{{Category: "response", Bytes: 1}}, func(context.Context, *sql.Conn) error {
		callbackCalled = true
		return nil
	}); !errors.Is(err, ErrLaneCapacityUnconfigured) || callbackCalled {
		t.Fatalf("unconfigured admission err=%v callbackCalled=%v", err, callbackCalled)
	}
	if err := authority.SetLaneByteBudget(ctx, "qas", 100, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err != nil {
		t.Fatal(err)
	}
	marker := errors.New("simulated later acceptance failure")
	called := false
	err := authority.AdmitLaneBytes(ctx, "qas", "request-rollback", []LaneByteAllocation{{Category: "response", Bytes: 60}}, func(context.Context, *sql.Conn) error {
		called = true
		return marker
	})
	if !called || !errors.Is(err, marker) {
		t.Fatalf("admission callback called=%v err=%v", called, err)
	}
	snapshot, err := authority.LaneByteCapacitySnapshot(ctx, "qas")
	if err != nil || snapshot.ReservedBytes != 0 {
		t.Fatalf("rolled-back reservation snapshot=%+v err=%v", snapshot, err)
	}
}

func TestLaneByteReservationRetryRequiresTheCompleteCategorySet(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, authority := laneCapacityAuthority(t, filepath.Join(root.Path(), "state", "lane-capacity-categories.db"))
	defer database.Close()
	if err := authority.SetLaneByteBudget(ctx, "qas", 500, "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); err != nil {
		t.Fatal(err)
	}
	allocations := []LaneByteAllocation{{Category: "workspace", Bytes: 100}, {Category: "response", Bytes: 50}}
	noOp := func(context.Context, *sql.Conn) error { return nil }
	if err := authority.AdmitLaneBytes(ctx, "qas", "exchange-1", allocations, noOp); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdmitLaneBytes(ctx, "qas", "exchange-1", []LaneByteAllocation{{Category: "response", Bytes: 50}, {Category: "workspace", Bytes: 100}}, noOp); err != nil {
		t.Fatalf("same allocation set in a different order: %v", err)
	}
	if err := authority.AdmitLaneBytes(ctx, "qas", "exchange-1", []LaneByteAllocation{{Category: "workspace", Bytes: 100}}, noOp); !errors.Is(err, ErrLaneCapacityReservationConflict) {
		t.Fatalf("omitted retained category = %v, want reservation conflict", err)
	}
	snapshot, err := authority.LaneByteCapacitySnapshot(ctx, "qas")
	if err != nil || snapshot.ReservedBytes != 150 {
		t.Fatalf("category-set snapshot=%+v err=%v", snapshot, err)
	}
}

func TestLaneByteReservationReleaseRequiresSuccessfulCleanup(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, authority := laneCapacityAuthority(t, filepath.Join(root.Path(), "state", "lane-capacity-release.db"))
	defer database.Close()
	if err := authority.SetLaneByteBudget(ctx, "prd", 100, "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdmitLaneBytes(ctx, "prd", "job-1", []LaneByteAllocation{{Category: "output", Bytes: 80}}, func(context.Context, *sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cleanupFailure := errors.New("cleanup is not confirmed")
	if err := authority.ReleaseLaneBytes(ctx, "prd", "job-1", func(context.Context, *sql.Conn) error { return cleanupFailure }); !errors.Is(err, cleanupFailure) {
		t.Fatalf("failed cleanup release=%v, want retained reservation", err)
	}
	snapshot, err := authority.LaneByteCapacitySnapshot(ctx, "prd")
	if err != nil || snapshot.ReservedBytes != 80 {
		t.Fatalf("reservation after failed cleanup=%+v err=%v", snapshot, err)
	}
	if err := authority.ReleaseLaneBytes(ctx, "prd", "job-1", func(context.Context, *sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err = authority.LaneByteCapacitySnapshot(ctx, "prd")
	if err != nil || snapshot.ReservedBytes != 0 {
		t.Fatalf("reservation after confirmed cleanup=%+v err=%v", snapshot, err)
	}
}

func TestLaneByteConcurrentAdmissionsCannotOvercommit(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, authority := laneCapacityAuthority(t, filepath.Join(root.Path(), "state", "lane-capacity-concurrent.db"))
	defer database.Close()
	if err := authority.SetLaneByteBudget(ctx, "dev", 100, "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, id := range []string{"parallel-a", "parallel-b"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			<-start
			results <- authority.AdmitLaneBytes(ctx, "dev", id, []LaneByteAllocation{{Category: "workspace", Bytes: 60}}, func(context.Context, *sql.Conn) error { return nil })
		}(id)
	}
	close(start)
	group.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrLaneCapacityExceeded):
			rejected++
		default:
			t.Fatalf("concurrent admission failed unexpectedly: %v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want one each", accepted, rejected)
	}
	snapshot, err := authority.LaneByteCapacitySnapshot(ctx, "dev")
	if err != nil || snapshot.ReservedBytes != 60 {
		t.Fatalf("concurrent reservation snapshot=%+v err=%v", snapshot, err)
	}
}

func TestLaneCapacityMigrationFromSchema38AddsEmptyDurableLedger(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "lane-capacity-migrate.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE lane_capacity_reservations`,
		`DROP TABLE lane_capacity_budgets`,
		`DELETE FROM runner_schema_migrations WHERE version = 39`,
		`PRAGMA user_version = 38`,
	} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			_ = database.Close()
			t.Fatalf("prepare schema 38 fixture (%s): %v", statement, err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate schema 38: %v", err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("migrated version=%d err=%v, want %d", version, err, CurrentSchemaVersion)
	}
	var migrationName string
	if err := database.QueryRowContext(ctx, `SELECT name FROM runner_schema_migrations WHERE version = 39`).Scan(&migrationName); err != nil || migrationName != "lane_capacity_reservations" {
		t.Fatalf("migration 39=%q err=%v", migrationName, err)
	}
	var budgets, reservations int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM lane_capacity_budgets`).Scan(&budgets); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM lane_capacity_reservations`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if budgets != 0 || reservations != 0 {
		t.Fatalf("migration invented capacity state: budgets=%d reservations=%d", budgets, reservations)
	}
}

func laneCapacityAuthority(t *testing.T, path string) (*sql.DB, *AuthorityStore) {
	t.Helper()
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	return database, authority
}
