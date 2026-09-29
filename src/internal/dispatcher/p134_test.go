package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	p134RouterModeEnv   = "RSR_P134_ROUTER_MODE"
	p134RouterLocalEnv  = "RSR_P134_ROUTER_LOCAL_DB"
	p134RouterRemoteEnv = "RSR_P134_ROUTER_REMOTE_DB"
	p134RouterIntentEnv = "RSR_P134_ROUTER_INTENT"
)

func TestP134RouterKillRestartF01(t *testing.T) {
	t.Run("lease_commit_before_remote_send", func(t *testing.T) {
		root, localPath, remotePath, intent, submit := p134RouterFixture(t, "lease")
		mode := "lease"
		harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
			return p134RouterChildCommand(mode, localPath, remotePath, intent.IntentID)
		})
		child, err := harness.Start()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := child.WaitForBarrier(ctx, testfixture.BarrierRouterAfterLeaseCommit); err != nil {
			t.Fatalf("wait for post-lease barrier: %v; output: %s", err, child.Output())
		}
		localBefore := p134RouterLocalSnapshot(t, root, localPath, "router-lease-before-kill.json")
		p134AssertIntentState(t, localBefore, intent.IntentID, store.LocalIntentDispatching)
		p134AssertIntentState(t, localBefore, submit.IntentID, store.LocalIntentRecorded)
		remoteBefore := p134RemoteSnapshot(t, root, remotePath, "remote-lease-before-kill.json")
		p134AssertRemoteCounts(t, remoteBefore, 0, 0)
		if err := harness.Kill(); err != nil {
			t.Fatalf("kill leased Router: %v; output: %s", err, child.Output())
		}
		localAfter := p134RouterLocalSnapshot(t, root, localPath, "router-lease-after-kill.json")
		remoteAfter := p134RemoteSnapshot(t, root, remotePath, "remote-lease-after-kill.json")
		if !reflectSnapshotsEqual(localBefore, localAfter) || !reflectSnapshotsEqual(remoteBefore, remoteAfter) {
			t.Fatalf("lease barrier state changed across Router kill: local_equal=%t remote_equal=%t", reflectSnapshotsEqual(localBefore, localAfter), reflectSnapshotsEqual(remoteBefore, remoteAfter))
		}

		mode = "reconcile_lease"
		restarted, err := harness.Restart()
		if err != nil {
			t.Fatalf("restart leased Router: %v", err)
		}
		result := p134WaitRouterResult(t, ctx, restarted)
		if result.DeliveryState != string(store.LocalIntentAccepted) {
			t.Fatalf("reconciled lease result = %+v, want accepted", result)
		}
		if err := restarted.Wait(ctx); err != nil {
			t.Fatalf("reconciled Router exit: %v; output: %s", err, restarted.Output())
		}
		localAfterRestart := p134RouterLocalSnapshot(t, root, localPath, "router-lease-after-restart.json")
		p134AssertIntentState(t, localAfterRestart, intent.IntentID, store.LocalIntentAccepted)
		p134AssertIntentState(t, localAfterRestart, submit.IntentID, store.LocalIntentRecorded)
		remoteAfterRestart := p134RemoteSnapshot(t, root, remotePath, "remote-lease-after-restart.json")
		p134AssertRemoteCounts(t, remoteAfterRestart, 1, 1)
	})

	t.Run("accepted_remote_mutation_with_lost_reply", func(t *testing.T) {
		root, localPath, remotePath, intent, submit := p134RouterFixture(t, "uncertain")
		mode := "uncertain"
		harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
			return p134RouterChildCommand(mode, localPath, remotePath, intent.IntentID)
		})
		child, err := harness.Start()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := child.WaitForBarrier(ctx, testfixture.BarrierRouterAfterUncertainCommit); err != nil {
			t.Fatalf("wait for persisted uncertainty barrier: %v; output: %s", err, child.Output())
		}
		localBefore := p134RouterLocalSnapshot(t, root, localPath, "router-uncertain-before-kill.json")
		p134AssertIntentState(t, localBefore, intent.IntentID, store.LocalIntentUncertain)
		p134AssertIntentState(t, localBefore, submit.IntentID, store.LocalIntentRecorded)
		remoteBefore := p134RemoteSnapshot(t, root, remotePath, "remote-uncertain-before-kill.json")
		p134AssertRemoteCounts(t, remoteBefore, 1, 1)
		if err := harness.Kill(); err != nil {
			t.Fatalf("kill uncertain Router: %v; output: %s", err, child.Output())
		}
		localAfter := p134RouterLocalSnapshot(t, root, localPath, "router-uncertain-after-kill.json")
		remoteAfter := p134RemoteSnapshot(t, root, remotePath, "remote-uncertain-after-kill.json")
		if !reflectSnapshotsEqual(localBefore, localAfter) || !reflectSnapshotsEqual(remoteBefore, remoteAfter) {
			t.Fatalf("uncertain state changed across Router kill: local_equal=%t remote_equal=%t", reflectSnapshotsEqual(localBefore, localAfter), reflectSnapshotsEqual(remoteBefore, remoteAfter))
		}

		mode = "reconcile_uncertain"
		restarted, err := harness.Restart()
		if err != nil {
			t.Fatalf("restart uncertain Router: %v", err)
		}
		result := p134WaitRouterResult(t, ctx, restarted)
		if result.DeliveryState != string(store.LocalIntentAccepted) {
			t.Fatalf("reconciled uncertain result = %+v, want accepted", result)
		}
		if err := restarted.Wait(ctx); err != nil {
			t.Fatalf("uncertain Router recovery exit: %v; output: %s", err, restarted.Output())
		}
		localAfterRestart := p134RouterLocalSnapshot(t, root, localPath, "router-uncertain-after-restart.json")
		p134AssertIntentState(t, localAfterRestart, intent.IntentID, store.LocalIntentAccepted)
		p134AssertIntentState(t, localAfterRestart, submit.IntentID, store.LocalIntentRecorded)
		remoteAfterRestart := p134RemoteSnapshot(t, root, remotePath, "remote-uncertain-after-restart.json")
		p134AssertRemoteCounts(t, remoteAfterRestart, 1, 1)
		p134AssertOneTargetResource(t, remoteAfterRestart, intent)
	})
}

// TestP134RouterProcessChild runs the production RemoteDriver in a killable
// process against a durable fake remote authority.
func TestP134RouterProcessChild(t *testing.T) {
	mode := os.Getenv(p134RouterModeEnv)
	if mode == "" {
		return
	}
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	localDB, err := store.Open(context.Background(), os.Getenv(p134RouterLocalEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer localDB.Close()
	authority, err := store.NewAuthorityStore(localDB)
	if err != nil {
		t.Fatal(err)
	}
	remoteDB, err := sql.Open("sqlite", os.Getenv(p134RouterRemoteEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer remoteDB.Close()
	remoteDB.SetMaxOpenConns(1)
	caller := &p134SQLiteRemoteCaller{db: remoteDB, mode: mode, reporter: reporter}
	driver, err := NewRemoteDriver(authority, caller, "router-p134-process", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	intentID := domain.IntentID(os.Getenv(p134RouterIntentEnv))
	switch mode {
	case "lease", "uncertain":
		_, _, dispatchErr := driver.DispatchIntent(ctx, intentID)
		if mode == "lease" {
			if dispatchErr != nil {
				t.Fatalf("dispatch after released lease barrier: %v", dispatchErr)
			}
			return
		}
		if dispatchErr == nil {
			t.Fatal("remote response loss did not leave an uncertain result")
		}
		current, readErr := authority.GetLocalIntent(ctx, intentID)
		if readErr != nil || current.DeliveryState != store.LocalIntentUncertain {
			t.Fatalf("local state after accepted remote mutation and lost response = %+v, %v", current, readErr)
		}
		if _, _, nextErr := driver.DispatchNext(ctx); !errors.Is(nextErr, ErrNoRemoteDispatchWork) {
			t.Fatalf("later same-session command while create acceptance is uncertain = %v, want no dispatch work", nextErr)
		}
		later, readErr := authority.GetLocalIntent(ctx, domain.IntentID("intent-p134-submit-uncertain"))
		if readErr != nil || later.DeliveryState != store.LocalIntentRecorded {
			t.Fatalf("later command while predecessor uncertain = %+v, %v; want still recorded", later, readErr)
		}
		if err := testfixture.WaitAtPhaseBarrier(os.Stdin, reporter, testfixture.BarrierRouterAfterUncertainCommit); err != nil {
			t.Fatalf("wait after uncertain commit: %v", err)
		}
	case "reconcile_lease", "reconcile_uncertain":
		recovered, _, reconcileErr := driver.ReconcileIntent(ctx, intentID)
		if reconcileErr != nil {
			t.Fatalf("reconcile intent: %v", reconcileErr)
		}
		if recovered.DeliveryState != store.LocalIntentAccepted {
			t.Fatalf("reconciliation state = %s, want accepted", recovered.DeliveryState)
		}
		if err := testfixture.PublishPhaseResult(reporter, p134RouterResult{DeliveryState: string(recovered.DeliveryState), IntentID: string(intentID)}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown P134 Router child mode %q", mode)
	}
}

type p134SQLiteRemoteCaller struct {
	db       *sql.DB
	mode     string
	reporter io.Writer
}

func (c *p134SQLiteRemoteCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if frame.Operation == sshbridge.OperationCreateOrResumeSession && c.mode == "lease" {
		if err := testfixture.WaitAtPhaseBarrier(os.Stdin, c.reporter, testfixture.BarrierRouterAfterLeaseCommit); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
	}
	if frame.Operation == sshbridge.OperationCreateOrResumeSession {
		if err := c.acceptSession(ctx, frame); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		if c.mode == "uncertain" {
			return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("simulated lost response after target commit")}
		}
		return p072SessionReply(frame, domain.SessionStateReady), nil
	}
	if frame.Operation == sshbridge.OperationGetSession {
		sessionID := string(frame.Payload)
		var request struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(frame.Payload, &request); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		sessionID = request.SessionID
		var found string
		err := c.db.QueryRowContext(ctx, `SELECT session_id FROM fake_remote_sessions WHERE session_id = ?`, sessionID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			payload, _ := json.Marshal(sshbridge.ErrorPayload{Code: "resource_not_found", Message: "session is not present"})
			return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: frame.RequestID, ResponseType: "error", Payload: payload}, nil
		}
		if err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		return p072SessionReply(frame, domain.SessionStateReady), nil
	}
	return sshbridge.ReplyFrame{}, fmt.Errorf("unexpected P134 fake remote operation %q", frame.Operation)
}

func (c *p134SQLiteRemoteCaller) acceptSession(ctx context.Context, frame sshbridge.RequestFrame) error {
	var input struct {
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal(frame.Payload, &input); err != nil {
		return err
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO fake_remote_sessions
		(session_id, intent_id, resource_id, idempotency_key, environment, create_calls)
		VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT(session_id) DO UPDATE SET create_calls = fake_remote_sessions.create_calls + 1`, frame.ResourceID, frame.RequestID, frame.ResourceID, frame.IdempotencyKey, input.Environment)
	if err != nil {
		return err
	}
	var intentID, resourceID, idempotencyKey, environment string
	if err := c.db.QueryRowContext(ctx, `SELECT intent_id, resource_id, idempotency_key, environment
		FROM fake_remote_sessions WHERE session_id = ?`, frame.ResourceID).Scan(&intentID, &resourceID, &idempotencyKey, &environment); err != nil {
		return err
	}
	if intentID != frame.RequestID || resourceID != frame.ResourceID || idempotencyKey != frame.IdempotencyKey || environment != input.Environment {
		return fmt.Errorf("remote idempotency identity changed on retry")
	}
	return nil
}

type p134RouterResult struct {
	DeliveryState string `json:"delivery_state"`
	IntentID      string `json:"intent_id"`
}

func p134RouterFixture(t *testing.T, suffix string) (*testfixture.Root, string, string, store.LocalIntentCreate, store.LocalIntentCreate) {
	t.Helper()
	root := testfixture.New(t)
	localPath := filepath.Join(root.Path(), "state", "local.sqlite")
	remotePath := filepath.Join(root.Path(), "target", "remote.sqlite")
	if err := os.MkdirAll(filepath.Dir(remotePath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), localPath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	intent := p072CreateIntent(t, "intent-p134-"+suffix, "session-p134-"+suffix, domain.SessionStateReady)
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	submit := p068SubmitIntent(t, "intent-p134-submit-"+suffix, string(intent.SessionID), "command-p134-"+suffix, domain.TargetKindRemote, "echo P134 must wait for its session")
	if _, err := authority.CreateLocalIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	remoteDB, err := sql.Open("sqlite", remotePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remoteDB.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := remoteDB.Exec(`CREATE TABLE fake_remote_sessions (
		session_id TEXT PRIMARY KEY,
		intent_id TEXT NOT NULL UNIQUE,
		resource_id TEXT NOT NULL,
		idempotency_key TEXT NOT NULL,
		environment TEXT NOT NULL,
		create_calls INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := remoteDB.Close(); err != nil {
		t.Fatal(err)
	}
	return root, localPath, remotePath, intent, submit
}

func p134RouterChildCommand(mode, localPath, remotePath string, intentID domain.IntentID) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestP134RouterProcessChild$")
	command.Env = p134RouterSetEnv(os.Environ(), p134RouterModeEnv, mode)
	command.Env = p134RouterSetEnv(command.Env, p134RouterLocalEnv, localPath)
	command.Env = p134RouterSetEnv(command.Env, p134RouterRemoteEnv, remotePath)
	command.Env = p134RouterSetEnv(command.Env, p134RouterIntentEnv, string(intentID))
	return command
}

func p134RouterSetEnv(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name != key {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

func p134RouterLocalSnapshot(t *testing.T, root *testfixture.Root, path, name string) testfixture.SQLiteSnapshot {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := testfixture.CaptureSQLiteSnapshot(context.Background(), db,
		testfixture.SQLiteQuery{Name: "local_intents", SQL: `SELECT intent_id, operation, resource_id, idempotency_key, delivery_state, reason, attempt_count FROM local_intents ORDER BY intent_id`},
		testfixture.SQLiteQuery{Name: "local_intent_lifecycle", SQL: `SELECT intent_id, lifecycle_sequence, previous_state, new_state, reason FROM local_intent_lifecycle ORDER BY intent_id, lifecycle_sequence`},
	)
	if err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	if _, err := snapshot.Save(root, name); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return snapshot
}

func p134RemoteSnapshot(t *testing.T, root *testfixture.Root, path, name string) testfixture.SQLiteSnapshot {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := testfixture.CaptureSQLiteSnapshot(context.Background(), db,
		testfixture.SQLiteQuery{Name: "fake_remote_sessions", SQL: `SELECT session_id, intent_id, resource_id, idempotency_key, environment, create_calls FROM fake_remote_sessions ORDER BY session_id`},
	)
	if err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	if _, err := snapshot.Save(root, name); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return snapshot
}

func p134AssertIntentState(t *testing.T, snapshot testfixture.SQLiteSnapshot, intentID domain.IntentID, want store.LocalIntentDeliveryState) {
	t.Helper()
	if len(snapshot.Queries) != 2 || len(snapshot.Queries[0].Rows) != 2 || len(snapshot.Queries[1].Rows) < 2 {
		t.Fatalf("local intent snapshot shape = %+v", snapshot)
	}
	for _, row := range snapshot.Queries[0].Rows {
		if row[0].Value == string(intentID) {
			state := row[4]
			if state.Type != "text" || state.Value != string(want) {
				t.Fatalf("local intent %s delivery state = %+v, want %s", intentID, state, want)
			}
			return
		}
	}
	t.Fatalf("local intent %s missing from snapshot", intentID)
}

func p134AssertRemoteCounts(t *testing.T, snapshot testfixture.SQLiteSnapshot, resources, calls int) {
	t.Helper()
	if len(snapshot.Queries) != 1 || len(snapshot.Queries[0].Rows) != resources {
		t.Fatalf("fake remote resources = %+v, want %d", snapshot, resources)
	}
	for _, row := range snapshot.Queries[0].Rows {
		if len(row) != 6 || row[5].Type != "integer" || row[5].Value != fmt.Sprint(calls) {
			t.Fatalf("remote mutation was not idempotent: row=%+v calls=%d", row, calls)
		}
	}
}

func p134AssertOneTargetResource(t *testing.T, snapshot testfixture.SQLiteSnapshot, intent store.LocalIntentCreate) {
	t.Helper()
	if len(snapshot.Queries) != 1 || len(snapshot.Queries[0].Rows) != 1 {
		t.Fatalf("target accepted resource count = %+v", snapshot)
	}
	row := snapshot.Queries[0].Rows[0]
	want := []string{string(intent.SessionID), string(intent.IntentID), string(intent.SessionID), intent.IdempotencyKey, intent.Environment}
	for i, expected := range want {
		if row[i].Type != "text" || row[i].Value != expected {
			t.Fatalf("target resource field %d = %+v, want %q", i, row[i], expected)
		}
	}
}

func p134WaitRouterResult(t *testing.T, ctx context.Context, process *testfixture.BarrierProcess) p134RouterResult {
	t.Helper()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("read Router result: %v; output: %s", err, process.Output())
	}
	var result p134RouterResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode Router result %q: %v", encoded, err)
	}
	return result
}

func reflectSnapshotsEqual(left, right testfixture.SQLiteSnapshot) bool {
	return reflect.DeepEqual(left, right)
}

var _ RemoteCaller = (*p134SQLiteRemoteCaller)(nil)
