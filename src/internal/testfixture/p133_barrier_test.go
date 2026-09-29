package testfixture

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const (
	p133WorkerModeEnv  = "RSR_P133_WORKER_MODE"
	p133WorkerDBEnv    = "RSR_P133_WORKER_DB"
	p133WorkerPointEnv = "RSR_P133_WORKER_POINT"
)

func TestP133BarrierHarnessSelfTests(t *testing.T) {
	for _, point := range NamedPhaseBarriers() {
		point := point
		t.Run(string(point), func(t *testing.T) {
			root := New(t)
			dbPath := filepath.Join(root.Path(), "phase-state.sqlite")
			mode := "commit-and-wait"
			harness := NewPhaseHarness(t, func() *exec.Cmd {
				return p133WorkerCommand(mode, dbPath, point)
			})

			child, err := harness.Start()
			if err != nil {
				t.Fatalf("start %s child: %v", point, err)
			}
			waitFor := func(process *BarrierProcess) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := process.WaitForBarrier(ctx, point); err != nil {
					t.Fatalf("wait for %s barrier: %v; child output: %s", point, err, process.Output())
				}
			}
			waitFor(child)

			beforeKill := p133CaptureSnapshot(t, root, dbPath, "before-kill.json")
			p133AssertSnapshotState(t, beforeKill, point, 1, 0, "accepted")
			if err := harness.Kill(); err != nil {
				t.Fatalf("kill %s child: %v; child output: %s", point, err, child.Output())
			}
			afterKill := p133CaptureSnapshot(t, root, dbPath, "after-kill.json")
			p133AssertSnapshotState(t, afterKill, point, 1, 0, "accepted")
			if !reflect.DeepEqual(beforeKill, afterKill) {
				t.Fatalf("database state changed across kill\nbefore: %+v\nafter:  %+v", beforeKill, afterKill)
			}

			mode = "recover"
			restarted, err := harness.Restart()
			if err != nil {
				t.Fatalf("restart %s child: %v", point, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resultBytes, err := restarted.WaitForResult(ctx)
			if err != nil {
				t.Fatalf("read %s restart result: %v; child output: %s", point, err, restarted.Output())
			}
			var result p133WorkerResult
			if err := json.Unmarshal(resultBytes, &result); err != nil {
				t.Fatalf("decode %s restart result %q: %v", point, resultBytes, err)
			}
			if result.Point != point || result.Status != "recovered" || result.OperationCount != 1 {
				t.Fatalf("restart result = %+v, want recovered %s with one durable operation", result, point)
			}
			if err := restarted.Wait(ctx); err != nil {
				t.Fatalf("%s restart child exited unsuccessfully: %v; output: %s", point, err, restarted.Output())
			}

			afterRestart := p133CaptureSnapshot(t, root, dbPath, "after-restart.json")
			p133AssertSnapshotState(t, afterRestart, point, 1, 1, "accepted")
			if !p133SnapshotIsOwnerOnly(t, root.Path(), "before-kill.json") ||
				!p133SnapshotIsOwnerOnly(t, root.Path(), "after-kill.json") ||
				!p133SnapshotIsOwnerOnly(t, root.Path(), "after-restart.json") {
				t.Fatal("captured database snapshots must be mode 0600")
			}
		})
	}
}

func TestP133BarrierCanBeReleasedByExactName(t *testing.T) {
	root := New(t)
	dbPath := filepath.Join(root.Path(), "release-state.sqlite")
	point := BarrierBashAfterScriptSource
	harness := NewPhaseHarness(t, func() *exec.Cmd {
		return p133WorkerCommand("commit-and-wait", dbPath, point)
	})
	child, err := harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := child.WaitForBarrier(ctx, point); err != nil {
		t.Fatalf("wait for named barrier: %v; output: %s", err, child.Output())
	}
	if _, err := harness.Restart(); err == nil {
		t.Fatal("harness restarted a child that was still blocked at a barrier")
	}
	if err := child.Release(point); err != nil {
		t.Fatalf("release named barrier: %v", err)
	}
	resultBytes, err := child.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait for release result: %v; output: %s", err, child.Output())
	}
	var result p133WorkerResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("decode release result %q: %v", resultBytes, err)
	}
	if result.Point != point || result.Status != "released" || result.OperationCount != 1 {
		t.Fatalf("release result = %+v, want released %s with one operation", result, point)
	}
	if err := child.Wait(ctx); err != nil {
		t.Fatalf("child exit after release: %v; output: %s", err, child.Output())
	}
}

func TestP133HarnessCleanupReapsBlockedChild(t *testing.T) {
	root := New(t)
	dbPath := filepath.Join(root.Path(), "cleanup-state.sqlite")
	var child *BarrierProcess
	t.Cleanup(func() {
		if child == nil || !child.Exited() {
			t.Error("phase harness cleanup left its child running")
			return
		}
		if child.cmd.ProcessState == nil || child.cmd.ProcessState.ExitCode() != -1 {
			t.Errorf("cleanup child exit state = %v, want SIGKILL", child.cmd.ProcessState)
		}
	})
	harness := NewPhaseHarness(t, func() *exec.Cmd {
		return p133WorkerCommand("commit-and-wait", dbPath, BarrierMacAPIAfterIntentCommit)
	})
	var err error
	child, err = harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := child.WaitForBarrier(ctx, BarrierMacAPIAfterIntentCommit); err != nil {
		t.Fatalf("wait for cleanup test barrier: %v; output: %s", err, child.Output())
	}
}

func TestP133SQLiteSnapshotRejectsMutatingStatements(t *testing.T) {
	root := New(t)
	db, err := sql.Open("sqlite", filepath.Join(root.Path(), "query-only.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE snapshot_guard (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM snapshot_guard`,
		`SELECT 1; DELETE FROM snapshot_guard`,
	} {
		if _, err := CaptureSQLiteSnapshot(context.Background(), db, SQLiteQuery{Name: "unsafe", SQL: statement}); err == nil {
			t.Fatalf("mutating/multiple statement was accepted: %q", statement)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM snapshot_guard`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected snapshot query changed database; row count = %d", count)
	}
}

func TestP133SQLiteSnapshotPreservesBinaryAndScalarValues(t *testing.T) {
	root := New(t)
	db, err := sql.Open("sqlite", filepath.Join(root.Path(), "value-types.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE snapshot_values (nullable TEXT, payload BLOB, amount REAL, count INTEGER)`); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0x00, 0x80, 0xff}
	if _, err := db.Exec(`INSERT INTO snapshot_values (nullable, payload, amount, count) VALUES (?, ?, ?, ?)`, nil, payload, 3.25, 7); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CaptureSQLiteSnapshot(context.Background(), db, SQLiteQuery{
		Name: "value_types",
		SQL:  `SELECT nullable, payload, amount, count FROM snapshot_values ORDER BY count`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Queries) != 1 || len(snapshot.Queries[0].Rows) != 1 {
		t.Fatalf("snapshot shape = %+v", snapshot)
	}
	row := snapshot.Queries[0].Rows[0]
	want := []SQLiteCell{
		{Type: "null"},
		{Type: "blob_base64", Value: "AID/"},
		{Type: "real", Value: "3.25"},
		{Type: "integer", Value: "7"},
	}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("snapshot cells = %+v, want %+v", row, want)
	}
}

func TestP133BarrierChild(t *testing.T) {
	mode := os.Getenv(p133WorkerModeEnv)
	if mode == "" {
		return
	}
	point := BarrierPoint(os.Getenv(p133WorkerPointEnv))
	if !validBarrierPoint(point) {
		t.Fatalf("invalid child barrier point %q", point)
	}
	dbPath := os.Getenv(p133WorkerDBEnv)
	if dbPath == "" {
		t.Fatal("child database path is missing")
	}
	reporter, err := OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open child database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if mode == "commit-and-wait" {
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			t.Fatalf("enable WAL: %v", err)
		}
		if _, err := db.Exec(`PRAGMA synchronous = FULL`); err != nil {
			t.Fatalf("set synchronous mode: %v", err)
		}
		if _, err := db.Exec(`CREATE TABLE phase_barrier_actions (
			barrier TEXT PRIMARY KEY,
			operation_count INTEGER NOT NULL,
			recovery_count INTEGER NOT NULL,
			status TEXT NOT NULL
		)`); err != nil {
			t.Fatalf("create child table: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO phase_barrier_actions
			(barrier, operation_count, recovery_count, status) VALUES (?, 1, 0, 'accepted')`, string(point)); err != nil {
			t.Fatalf("commit child operation: %v", err)
		}
		if err := WaitAtPhaseBarrier(os.Stdin, reporter, point); err != nil {
			t.Fatalf("wait at child barrier: %v", err)
		}
		if err := PublishPhaseResult(reporter, p133WorkerResult{Point: point, Status: "released", OperationCount: 1}); err != nil {
			t.Fatalf("publish barrier release result: %v", err)
		}
		return
	}
	if mode != "recover" {
		t.Fatalf("unknown child mode %q", mode)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin recovery transaction: %v", err)
	}
	var operationCount int
	var status string
	if err := tx.QueryRow(`SELECT operation_count, status FROM phase_barrier_actions WHERE barrier = ?`, string(point)).Scan(&operationCount, &status); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read durable child operation: %v", err)
	}
	if operationCount != 1 || status != "accepted" {
		_ = tx.Rollback()
		t.Fatalf("recovered row = count %d status %q", operationCount, status)
	}
	if _, err := tx.Exec(`UPDATE phase_barrier_actions SET recovery_count = recovery_count + 1 WHERE barrier = ?`, string(point)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("record recovery observation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recovery observation: %v", err)
	}
	if err := PublishPhaseResult(reporter, p133WorkerResult{Point: point, Status: "recovered", OperationCount: operationCount}); err != nil {
		t.Fatalf("publish recovery result: %v", err)
	}
}

type p133WorkerResult struct {
	Point          BarrierPoint `json:"point"`
	Status         string       `json:"status"`
	OperationCount int          `json:"operation_count"`
}

func p133WorkerCommand(mode, dbPath string, point BarrierPoint) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestP133BarrierChild$")
	command.Env = setEnvironment(os.Environ(), p133WorkerModeEnv, mode)
	command.Env = setEnvironment(command.Env, p133WorkerDBEnv, dbPath)
	command.Env = setEnvironment(command.Env, p133WorkerPointEnv, string(point))
	return command
}

func p133CaptureSnapshot(t *testing.T, root *Root, dbPath, filename string) SQLiteSnapshot {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open database for snapshot %s: %v", filename, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	snapshot, err := CaptureSQLiteSnapshot(context.Background(), db,
		SQLiteQuery{
			Name: "phase_barrier_actions",
			SQL:  `SELECT barrier, operation_count, recovery_count, status FROM phase_barrier_actions ORDER BY barrier`,
		},
	)
	if err != nil {
		t.Fatalf("capture database snapshot %s: %v", filename, err)
	}
	if _, err := snapshot.Save(root, filename); err != nil {
		t.Fatalf("save database snapshot %s: %v", filename, err)
	}
	return snapshot
}

func p133AssertSnapshotState(t *testing.T, snapshot SQLiteSnapshot, point BarrierPoint, operations, recoveries int64, status string) {
	t.Helper()
	if snapshot.Version != 1 || len(snapshot.Queries) != 1 {
		t.Fatalf("snapshot shape = %+v", snapshot)
	}
	query := snapshot.Queries[0]
	if query.Name != "phase_barrier_actions" || len(query.Rows) != 1 || len(query.Rows[0]) != 4 {
		t.Fatalf("snapshot query = %+v", query)
	}
	row := query.Rows[0]
	want := []SQLiteCell{
		{Type: "text", Value: string(point)},
		{Type: "integer", Value: strconv.FormatInt(operations, 10)},
		{Type: "integer", Value: strconv.FormatInt(recoveries, 10)},
		{Type: "text", Value: status},
	}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("snapshot row = %+v, want %+v", row, want)
	}
}

func p133SnapshotIsOwnerOnly(t *testing.T, root, name string) bool {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("stat saved snapshot %s: %v", name, err)
	}
	return info.Mode().Perm() == 0o600
}
