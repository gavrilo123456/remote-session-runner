package localapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	p134APIModeEnv = "RSR_P134_API_MODE"
	p134APIDBEnv   = "RSR_P134_API_DB"
	p134APISockEnv = "RSR_P134_API_SOCKET"
)

func TestP134APIKillAfterIntentCommitReplaysQueuedRequest(t *testing.T) {
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "local.sqlite")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	socketDir := p134ShortSocketDir(t)
	mode := "crash"
	socketPath := filepath.Join(socketDir, "api-crash.sock")
	harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		return p134APIChildCommand(mode, databasePath, socketPath)
	})
	child, err := harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p134WaitForSocket(t, ctx, child, socketPath)
	client := p134APIClient(socketPath)
	defer client.CloseIdleConnections()
	type httpResult struct {
		response *http.Response
		err      error
	}
	firstResult := make(chan httpResult, 1)
	go func() {
		request, requestErr := p134CreateSessionRequest()
		if requestErr != nil {
			firstResult <- httpResult{err: requestErr}
			return
		}
		response, requestErr := client.Do(request)
		firstResult <- httpResult{response: response, err: requestErr}
	}()

	if err := child.WaitForBarrier(ctx, testfixture.BarrierMacAPIAfterIntentCommit); err != nil {
		select {
		case result := <-firstResult:
			if result.response != nil {
				body, _ := io.ReadAll(result.response.Body)
				_ = result.response.Body.Close()
				t.Fatalf("wait for API intent-commit barrier: %v; HTTP returned %d %s; child output: %s", err, result.response.StatusCode, body, child.Output())
			}
			t.Fatalf("wait for API intent-commit barrier: %v; HTTP error: %v; child output: %s", err, result.err, child.Output())
		default:
			t.Fatalf("wait for API intent-commit barrier: %v; request is still pending; child output: %s", err, child.Output())
		}
	}
	beforeKill := p134APISnapshot(t, root, databasePath, "api-before-kill.json")
	wantSessionID, wantIntentID := p134AssertSingleRecordedAPIIntent(t, beforeKill)
	if err := harness.Kill(); err != nil {
		t.Fatalf("kill API child: %v; child output: %s", err, child.Output())
	}
	first := <-firstResult
	if first.response != nil {
		_ = first.response.Body.Close()
		t.Fatalf("API returned a response after being killed before writing it: status=%d", first.response.StatusCode)
	}
	if first.err == nil {
		t.Fatal("request unexpectedly succeeded after the API process was killed")
	}
	afterKill := p134APISnapshot(t, root, databasePath, "api-after-kill.json")
	if !reflect.DeepEqual(beforeKill, afterKill) {
		t.Fatalf("committed API intent changed across process death\nbefore: %+v\nafter:  %+v", beforeKill, afterKill)
	}

	mode = "replay"
	socketPath = filepath.Join(socketDir, "api-restarted.sock")
	restarted, err := harness.Restart()
	if err != nil {
		t.Fatalf("restart API child: %v", err)
	}
	p134WaitForSocket(t, ctx, restarted, socketPath)
	client = p134APIClient(socketPath)
	request, err := p134CreateSessionRequest()
	if err != nil {
		t.Fatal(err)
	}
	replayResult := make(chan httpResult, 1)
	go func() {
		response, requestErr := client.Do(request)
		replayResult <- httpResult{response: response, err: requestErr}
	}()
	if err := restarted.WaitForBarrier(ctx, testfixture.BarrierMacAPIAfterIntentCommit); err != nil {
		t.Fatalf("wait for replay intent-commit barrier: %v; child output: %s", err, restarted.Output())
	}
	if err := restarted.Release(testfixture.BarrierMacAPIAfterIntentCommit); err != nil {
		t.Fatalf("release replay intent-commit barrier: %v", err)
	}
	replayed := <-replayResult
	if replayed.err != nil {
		t.Fatalf("retry committed request after API restart: %v; child output: %s", replayed.err, restarted.Output())
	}
	response := replayed.response
	var replay struct {
		SessionID       string `json:"session_id"`
		IntentID        string `json:"intent_id"`
		AcceptanceScope string `json:"acceptance_scope"`
		KnownState      struct {
			DeliveryState string `json:"delivery_state"`
		} `json:"known_state"`
	}
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		_ = response.Body.Close()
		t.Fatalf("read replay response: %v", err)
	}
	_ = response.Body.Close()
	if err := json.Unmarshal(responseBody, &replay); err != nil {
		t.Fatalf("decode replay response: %v; body: %s", err, responseBody)
	}
	if response.StatusCode != http.StatusAccepted || replay.SessionID != wantSessionID || replay.IntentID != wantIntentID || replay.AcceptanceScope != "local_intent" || replay.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("idempotent replay status=%d body=%+v; want accepted local intent", response.StatusCode, replay)
	}
	if err := restarted.Wait(ctx); err != nil {
		t.Fatalf("restarted API child exit: %v; output: %s", err, restarted.Output())
	}
	afterRestart := p134APISnapshot(t, root, databasePath, "api-after-restart.json")
	if !reflect.DeepEqual(beforeKill, afterRestart) {
		t.Fatalf("same-key API retry created or changed durable intent state\nbefore: %+v\nafter:  %+v", beforeKill, afterRestart)
	}
	client.CloseIdleConnections()
}

// TestP134APIProcessChild runs the production Unix-socket HTTP server in the
// child process controlled by the named kill/restart harness.
func TestP134APIProcessChild(t *testing.T) {
	mode := os.Getenv(p134APIModeEnv)
	if mode == "" {
		return
	}
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	db, err := store.Open(context.Background(), os.Getenv(p134APIDBEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{Authority: authority, Owner: owner, SocketPath: os.Getenv(p134APISockEnv)})
	if err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{})
	switch mode {
	case "crash":
		server.afterIntentCommit = func() {
			if err := testfixture.WaitAtPhaseBarrier(os.Stdin, reporter, testfixture.BarrierMacAPIAfterIntentCommit); err != nil {
				fmt.Fprintf(os.Stderr, "wait for API barrier: %v\n", err)
			}
		}
	case "replay":
		server.afterIntentCommit = func() {
			close(committed)
			if err := testfixture.WaitAtPhaseBarrier(os.Stdin, reporter, testfixture.BarrierMacAPIAfterIntentCommit); err != nil {
				fmt.Fprintf(os.Stderr, "wait for replay API barrier: %v\n", err)
			}
		}
	default:
		t.Fatalf("unknown P134 API child mode %q", mode)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	if mode == "crash" {
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
		return
	}
	<-committed
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func p134APIChildCommand(mode, databasePath, socketPath string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestP134APIProcessChild$")
	command.Env = p134SetEnvironment(os.Environ(), p134APIModeEnv, mode)
	command.Env = p134SetEnvironment(command.Env, p134APIDBEnv, databasePath)
	command.Env = p134SetEnvironment(command.Env, p134APISockEnv, socketPath)
	return command
}

func p134APIClient(socketPath string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}}
}

func p134CreateSessionRequest() (*http.Request, error) {
	const body = `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "p134-api-create-session")
	return request, nil
}

func p134APISnapshot(t *testing.T, root *testfixture.Root, databasePath, name string) testfixture.SQLiteSnapshot {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshot, err := testfixture.CaptureSQLiteSnapshot(context.Background(), db,
		testfixture.SQLiteQuery{Name: "local_intents", SQL: `SELECT intent_id, operation, resource_id, request_hash, idempotency_key, delivery_state, attempt_count FROM local_intents ORDER BY intent_id`},
		testfixture.SQLiteQuery{Name: "local_idempotency", SQL: `SELECT controller_type, controller_id, operation, idempotency_key, intent_id, resource_id, request_hash FROM local_idempotency ORDER BY controller_type, controller_id, operation, idempotency_key`},
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

func p134AssertSingleRecordedAPIIntent(t *testing.T, snapshot testfixture.SQLiteSnapshot) (string, string) {
	t.Helper()
	if len(snapshot.Queries) != 3 || len(snapshot.Queries[0].Rows) != 1 || len(snapshot.Queries[1].Rows) != 1 || len(snapshot.Queries[2].Rows) != 1 {
		t.Fatalf("committed API snapshot does not contain exactly one intent, key binding, and lifecycle row: %+v", snapshot)
	}
	if got := snapshot.Queries[0].Rows[0][5]; got.Type != "text" || got.Value != string(store.LocalIntentRecorded) {
		t.Fatalf("queued API delivery state = %+v, want recorded", got)
	}
	row := snapshot.Queries[0].Rows[0]
	return row[2].Value, row[0].Value
}

func p134ShortSocketDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "rsr-p134-api-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func p134WaitForSocket(t *testing.T, ctx context.Context, process *testfixture.BarrierProcess, socketPath string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(socketPath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect API socket %s: %v", socketPath, err)
		}
		if process.Exited() {
			t.Fatalf("API child exited before creating socket %s: %s", socketPath, process.Output())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for API socket %s: %v; child output: %s", socketPath, ctx.Err(), process.Output())
		case <-ticker.C:
		}
	}
}

func p134SetEnvironment(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name != key {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}
