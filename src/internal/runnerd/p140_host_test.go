//go:build p140linuxhost

package runnerd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	p140LinuxHostGate = "RSR_P140_LINUX_HOST_GATE"
	p140LinuxHelper   = "RSR_P140_HELPER"
)

type p140LinuxServiceResult struct {
	Ready   bool                                  `json:"ready,omitempty"`
	Stopped bool                                  `json:"stopped,omitempty"`
	PID     int                                   `json:"pid"`
	Report  execution.StartupReconciliationReport `json:"report"`
}

func TestP140UbuntuOnlineBackupRestoreAndGenerationBumpHost(t *testing.T) {
	if os.Getenv(p140LinuxHelper) == "1" {
		p140LinuxRunnerdHelper(t)
		return
	}
	if os.Getenv(p140LinuxHostGate) != "1" {
		t.Skip("set RSR_P140_LINUX_HOST_GATE=1 to run the isolated Ubuntu backup/restore gate")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P140 Linux gate must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "ubuntu" || current.Uid != "1001" {
		t.Fatalf("P140 Linux account=%v err=%v, want ubuntu uid 1001", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "oracle-yuta-konopka-ubuntu-micro-02" {
		t.Fatalf("P140 Linux host=%q err=%v, want oracle-yuta-konopka-ubuntu-micro-02", hostname, err)
	}

	root, err := os.MkdirTemp("/tmp", "p140l-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "authority.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	socketPath := filepath.Join(root, "runnerd.sock")
	backupDir := filepath.Join(root, "backups")
	backupPath := filepath.Join(backupDir, "authority.snapshot.db")

	harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestP140UbuntuOnlineBackupRestoreAndGenerationBumpHost$")
		command.Env = append(os.Environ(), p140LinuxHelper+"=1",
			"RSR_P140_DB="+databasePath,
			"RSR_P140_WORKSPACES="+workspaceRoot,
			"RSR_P140_SOCKET="+socketPath,
			"RSR_P140_BACKUPS="+backupDir,
		)
		return command
	})
	var activeProcess *testfixture.BarrierProcess
	var database *sql.DB
	var authority *store.AuthorityStore
	var reader *sql.Conn
	var readSnapshot *sql.Tx
	t.Cleanup(func() {
		if readSnapshot != nil {
			_ = readSnapshot.Rollback()
		}
		if reader != nil {
			_ = reader.Close()
		}
		if database != nil {
			_ = database.Close()
		}
		if activeProcess != nil && !activeProcess.Exited() {
			_ = harness.Kill()
		}
		if _, err := os.Stat(databasePath); err == nil {
			if err := p135LinuxCleanupFixture(databasePath, workspaceRoot); err != nil {
				t.Errorf("reconcile isolated P140 Linux fixture after helper exit: %v", err)
			}
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove isolated P140 Linux fixture %s: %v", root, err)
		}
	})

	first, err := harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	activeProcess = first
	firstResult := p140LinuxWaitResult(t, first, true)
	if firstResult.Report.SessionsInspected != 0 {
		t.Fatalf("fresh isolated runnerd startup report=%+v, want no prior sessions", firstResult.Report)
	}

	database, authority = p135LinuxOpenAuthority(t, databasePath)
	client := p046UnixClient(socketPath)
	const oldSessionID = "p140-linux-old-session"
	createBody := []byte(`{"session_id":"p140-linux-old-session","idempotency_key":"p140-linux-create-old","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	created := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("P140 isolated Ubuntu session create status=%d body=%s", created.StatusCode, p046ReadBody(t, created))
	}
	var oldSession sessionResponse
	p046DecodeJSON(t, created, &oldSession)
	if oldSession.SessionState != string(domain.SessionStateReady) || oldSession.RuntimeGeneration == "" {
		t.Fatalf("P140 isolated Ubuntu old session=%+v, want ready with generation", oldSession)
	}

	ownership := p135LinuxReadOwnership(t, workspaceRoot, oldSessionID)
	if ownership.PID <= 0 || ownership.ProcessGroupID != ownership.PID || syscall.Kill(ownership.PID, 0) != nil {
		t.Fatalf("P140 isolated Ubuntu Bash ownership=%+v, want a live process group", ownership)
	}
	if processInfo, err := os.Stat(filepath.Join("/proc", strconv.Itoa(ownership.PID))); err != nil {
		t.Fatalf("inspect isolated P140 Bash PID %d: %v", ownership.PID, err)
	} else if stat, ok := processInfo.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		t.Fatalf("isolated P140 Bash uid=%v, want ubuntu uid %d", processInfo.Sys(), os.Getuid())
	}
	p135LinuxSubmit(t, client, oldSessionID, "p140-linux-command-before-backup", "p140-linux-before-backup-key", "printf 'p140-live-shell\\n'")
	p135LinuxWaitCommandState(t, authority, "p140-linux-command-before-backup", domain.CommandStateSucceeded)

	if _, err := database.ExecContext(context.Background(), "CREATE TABLE p140_backup_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	reader, err = database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	readSnapshot, err = reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var snapshotGeneration string
	if err := readSnapshot.QueryRowContext(context.Background(), "SELECT runtime_generation FROM exec_sessions WHERE session_id = ?", oldSessionID).Scan(&snapshotGeneration); err != nil {
		t.Fatal(err)
	}
	if snapshotGeneration != oldSession.RuntimeGeneration {
		t.Fatalf("held read snapshot generation=%q, want %q", snapshotGeneration, oldSession.RuntimeGeneration)
	}
	if _, err := database.ExecContext(context.Background(), "INSERT INTO p140_backup_probe(value) VALUES ('committed-in-wal')"); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := database.QueryRowContext(context.Background(), "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		t.Fatal(err)
	}
	if logFrames <= checkpointedFrames {
		t.Fatalf("P140 host WAL checkpoint log=%d checkpointed=%d busy=%d; want committed frames retained", logFrames, checkpointedFrames, busy)
	}
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOnlineBackup(context.Background(), database, backupPath); err != nil {
		t.Fatalf("create live P140 Linux WAL-aware backup: %v", err)
	}
	p140LinuxAssertBackup(t, backupPath, oldSessionID, oldSession.RuntimeGeneration)

	if err := p140LinuxStopService(t, first, firstResult.PID); err != nil {
		t.Fatal(err)
	}
	activeProcess = nil
	if err := p135LinuxWaitProcessGroupGone(ownership.ProcessGroupID, 3*time.Second); err != nil {
		t.Fatalf("old isolated Linux Bash process group after graceful runnerd stop: %v", err)
	}
	if err := readSnapshot.Rollback(); err != nil {
		t.Fatal(err)
	}
	readSnapshot = nil
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	reader = nil
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = nil

	if err := store.RestoreOnlineBackup(context.Background(), databasePath, backupPath); err != nil {
		t.Fatalf("restore isolated Linux authority after service and all DB handles exited: %v", err)
	}
	second, err := harness.Restart()
	if err != nil {
		t.Fatal(err)
	}
	activeProcess = second
	secondResult := p140LinuxWaitResult(t, second, true)
	if secondResult.Report.SessionsInspected != 1 || secondResult.Report.CleanupUnconfirmed != 0 || secondResult.Report.RuntimeFailures != 0 {
		t.Fatalf("post-restore isolated runnerd reconciliation=%+v, want one inspected session and no uncertain cleanup/runtime failure", secondResult.Report)
	}
	database, authority = p135LinuxOpenAuthority(t, databasePath)
	restored, err := authority.GetSession(context.Background(), domain.SessionID(oldSessionID))
	if err != nil || restored.State != domain.SessionStateLost || restored.RuntimeGeneration != oldSession.RuntimeGeneration {
		t.Fatalf("restored Linux session=%+v err=%v; want old generation %q marked lost", restored, err, oldSession.RuntimeGeneration)
	}
	if err := p140LinuxAssertNoOwnership(workspaceRoot, oldSessionID); err != nil {
		t.Fatal(err)
	}
	if err := p135LinuxWaitProcessGroupGone(ownership.ProcessGroupID, time.Second); err != nil {
		t.Fatalf("old Linux shell still exists after restored startup reconciliation: %v", err)
	}
	if err := authority.ConfirmSessionCleanup(context.Background(), domain.SessionID(oldSessionID)); err != nil {
		t.Fatalf("confirm old restored reservation after process and ownership proof: %v", err)
	}

	const newSessionID = "p140-linux-new-session"
	newCreateBody := []byte(`{"session_id":"p140-linux-new-session","idempotency_key":"p140-linux-create-new","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	newResponse := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", newCreateBody)
	if newResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("P140 post-restore Ubuntu session create status=%d body=%s", newResponse.StatusCode, p046ReadBody(t, newResponse))
	}
	var newSession sessionResponse
	p046DecodeJSON(t, newResponse, &newSession)
	if newSession.SessionState != string(domain.SessionStateReady) || newSession.RuntimeGeneration == "" || newSession.RuntimeGeneration == oldSession.RuntimeGeneration {
		t.Fatalf("P140 new Linux session=%+v err=<nil>; want ready and a fresh runtime generation distinct from %q", newSession, oldSession.RuntimeGeneration)
	}
	newOwnership := p135LinuxReadOwnership(t, workspaceRoot, newSessionID)
	if newOwnership.PID <= 0 || newOwnership.PID == ownership.PID || syscall.Kill(newOwnership.PID, 0) != nil {
		t.Fatalf("P140 fresh Linux Bash ownership=%+v, old=%+v; want a distinct live process", newOwnership, ownership)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = nil
	if err := p140LinuxStopService(t, second, secondResult.PID); err != nil {
		t.Fatal(err)
	}
	activeProcess = nil
	if err := p135LinuxWaitProcessGroupGone(newOwnership.ProcessGroupID, 3*time.Second); err != nil {
		t.Fatalf("fresh Linux shell after fixture shutdown: %v", err)
	}
	t.Logf("machine=%s os=Ubuntu 20.04.6 account=%s: online backup captured committed WAL frames; isolated runnerd stopped before offline restore; restored old generation %s became lost without reattachment; new ready session used generation %s", hostname, current.Username, oldSession.RuntimeGeneration, newSession.RuntimeGeneration)
}

func p140LinuxRunnerdHelper(t *testing.T) {
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	databasePath := os.Getenv("RSR_P140_DB")
	workspaceRoot := os.Getenv("RSR_P140_WORKSPACES")
	socketPath := os.Getenv("RSR_P140_SOCKET")
	backupDir := os.Getenv("RSR_P140_BACKUPS")
	db, authority, service, err := p135LinuxService(databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requestGate, dispatchGate := lifecycle.NewGate(), lifecycle.NewGate()
	privateServer, err := NewPrivateServer(PrivateServerOptions{
		Service: service, SocketPath: socketPath, RequestGate: requestGate, DispatchGate: dispatchGate,
	})
	if err != nil {
		t.Fatal(err)
	}
	pki := newP106TestPKI(t)
	tlsRoot := filepath.Join(backupDir, "loopback-tls")
	if err := os.MkdirAll(tlsRoot, 0o700); err != nil {
		t.Fatalf("create isolated P140 loopback TLS fixture directory: %v", err)
	}
	httpsOptions := writeP106TestFiles(t, tlsRoot, pki, validP106PrincipalMap)
	httpsOptions.Handler, err = newDirectHTTPSAPIHandler(service, requestGate, dispatchGate)
	if err != nil {
		t.Fatal(err)
	}
	httpsServer, err := NewDirectHTTPSServer(httpsOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := privateServer.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := httpsServer.Listen(); err != nil {
		_ = privateServer.Close(context.Background())
		t.Fatal(err)
	}
	hooks := &linuxShutdownHooks{
		private: privateServer, https: httpsServer, service: service, authority: authority,
		requestGate: requestGate, dispatchGate: dispatchGate,
	}
	coordinator, err := lifecycle.NewCoordinator(hooks, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: 8 * time.Second, CleanupTimeout: 4 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopSignals()
	serveResult := make(chan error, 1)
	go func() { serveResult <- serveUntilCoordinator(signalContext, privateServer, httpsServer, coordinator) }()
	if err := testfixture.PublishPhaseResult(reporter, p140LinuxServiceResult{Ready: true, PID: os.Getpid(), Report: report}); err != nil {
		t.Fatal(err)
	}
	select {
	case serveErr := <-serveResult:
		if serveErr != nil {
			t.Fatalf("isolated runnerd exited unexpectedly: %v", serveErr)
		}
	case <-signalContext.Done():
		serveErr := <-serveResult
		if serveErr != nil {
			t.Fatalf("graceful isolated runnerd shutdown: %v", serveErr)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.PublishPhaseResult(reporter, p140LinuxServiceResult{Stopped: true, PID: os.Getpid(), Report: report}); err != nil {
		t.Fatal(err)
	}
}

func p140LinuxWaitResult(t *testing.T, process *testfixture.BarrierProcess, wantReady bool) p140LinuxServiceResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait for isolated P140 Ubuntu runnerd result: %v; output=%s", err, process.Output())
	}
	var result p140LinuxServiceResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode isolated P140 Ubuntu runnerd result %s: %v", encoded, err)
	}
	if wantReady && (!result.Ready || result.Stopped) || !wantReady && (!result.Stopped || result.Ready) {
		t.Fatalf("isolated P140 runnerd result=%+v, wantReady=%v", result, wantReady)
	}
	return result
}

func p140LinuxStopService(t *testing.T, process *testfixture.BarrierProcess, pid int) error {
	t.Helper()
	if pid <= 0 {
		return fmt.Errorf("invalid isolated P140 runnerd PID %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to isolated P140 runnerd PID %d: %w", pid, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := process.Wait(ctx); err != nil {
		return fmt.Errorf("wait for graceful isolated P140 runnerd exit: %w; output=%s", err, process.Output())
	}
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		return fmt.Errorf("read isolated P140 runnerd shutdown result: %w; output=%s", err, process.Output())
	}
	var result p140LinuxServiceResult
	if err := json.Unmarshal(encoded, &result); err != nil || !result.Stopped || result.PID != pid {
		return fmt.Errorf("isolated P140 runnerd shutdown result=%s err=%v, want stopped PID %d", encoded, err, pid)
	}
	return nil
}

func p140LinuxAssertBackup(t *testing.T, backupPath, sessionID, generation string) {
	t.Helper()
	database, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open P140 Linux backup snapshot: %v", err)
	}
	defer database.Close()
	var probe, state, gotGeneration string
	if err := database.QueryRow("SELECT value FROM p140_backup_probe WHERE value = 'committed-in-wal'").Scan(&probe); err != nil || probe != "committed-in-wal" {
		t.Fatalf("P140 Linux snapshot WAL probe=%q err=%v", probe, err)
	}
	if err := database.QueryRow("SELECT state, runtime_generation FROM exec_sessions WHERE session_id = ?", sessionID).Scan(&state, &gotGeneration); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.SessionStateReady) || gotGeneration != generation {
		t.Fatalf("P140 Linux snapshot session state=%q generation=%q, want ready generation %q", state, gotGeneration, generation)
	}
}

func p140LinuxAssertNoOwnership(workspaceRoot, sessionID string) error {
	ownershipRoot := filepath.Join(workspaceRoot, ".runner-runtime-ownership")
	entries, err := os.ReadDir(ownershipRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ownershipRoot, entry.Name()))
		if err != nil {
			return err
		}
		var owner p135LinuxOwnership
		if err := json.Unmarshal(data, &owner); err != nil {
			return err
		}
		if owner.SessionID == sessionID {
			return fmt.Errorf("stale P140 runtime ownership remains for session %s", sessionID)
		}
	}
	return nil
}
