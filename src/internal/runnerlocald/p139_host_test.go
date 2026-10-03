package runnerlocald

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/queueworker"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	p139MacHostGate = "RSR_P139_MAC_HOST_GATE"
	p139HelperEnv   = "RSR_P139_HELPER"
)

type p139MacServiceResult struct {
	Ready   bool                                  `json:"ready,omitempty"`
	Stopped bool                                  `json:"stopped,omitempty"`
	PID     int                                   `json:"pid"`
	Report  execution.StartupReconciliationReport `json:"report"`
}

func TestP139MacOnlineBackupRestoreHost(t *testing.T) {
	if os.Getenv(p139HelperEnv) == "1" {
		p139MacLocaldHelper(t)
		return
	}
	if os.Getenv(p139MacHostGate) != "1" {
		t.Skip("set RSR_P139_MAC_HOST_GATE=1 to run the actual Mac online-backup/restore gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P139 Mac gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" || current.Uid != "501" {
		t.Fatalf("P139 Mac account=%v err=%v, want tomasz.walczuk uid 501", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "AMAK2KJ6X9JJJ" {
		t.Fatalf("P139 Mac host=%q err=%v, want AMAK2KJ6X9JJJ", hostname, err)
	}

	fixture := testfixture.New(t)
	if err := os.Chmod(fixture.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(fixture.Path(), "state", "authority.db")
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	socketRoot, err := os.MkdirTemp("/private/tmp", "p139-socket-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketRoot, "locald.sock")
	backupPath := filepath.Join(fixture.Path(), "backups", "authority.snapshot.db")

	harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestP139MacOnlineBackupRestoreHost$")
		command.Env = append(os.Environ(), p139HelperEnv+"=1",
			"RSR_P139_DB="+databasePath,
			"RSR_P139_WORKSPACES="+workspaceRoot,
			"RSR_P139_SOCKET="+socketPath,
		)
		return command
	})
	var activeProcess *testfixture.BarrierProcess
	t.Cleanup(func() {
		if activeProcess != nil && !activeProcess.Exited() {
			_ = harness.Kill()
		}
		if _, err := os.Stat(databasePath); err == nil {
			if err := p135MacCleanupFixture(databasePath, workspaceRoot); err != nil {
				t.Errorf("reconcile isolated P139 fixture after helper exit: %v", err)
			}
		}
		if err := os.RemoveAll(socketRoot); err != nil {
			t.Errorf("remove isolated P139 socket directory: %v", err)
		}
	})

	first, err := harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	activeProcess = first
	firstResult := p139MacWaitResult(t, first)
	if !firstResult.Ready || firstResult.Report.SessionsInspected != 0 {
		t.Fatalf("fresh isolated locald startup=%+v, want ready with no old sessions", firstResult)
	}

	database, authority := p135MacOpenAuthority(t, databasePath)
	created := p060CreateIntent(t, authority, "p139-mac-old-create", "p139-mac-old-session", "p139-mac-old-create-key")
	p135MacAcceptIntent(t, p060UnixClient(socketPath), created, nil)
	p135MacWaitSessionState(t, authority, created.SessionID, domain.SessionStateReady)
	oldSession, err := authority.GetSession(context.Background(), created.SessionID)
	if err != nil || oldSession.RuntimeGeneration == "" {
		t.Fatalf("read old Mac session generation=%+v err=%v", oldSession, err)
	}
	oldOwnership := p135MacReadOwnership(t, workspaceRoot, string(created.SessionID))
	if oldOwnership.PID <= 0 || oldOwnership.ProcessGroupID != oldOwnership.PID || syscall.Kill(oldOwnership.PID, 0) != nil {
		t.Fatalf("old Mac runtime ownership=%+v, want a live Bash process group", oldOwnership)
	}

	if _, err := database.ExecContext(context.Background(), "CREATE TABLE p139_backup_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	reader, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	readTransaction, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var snapshotGeneration string
	if err := readTransaction.QueryRowContext(context.Background(), "SELECT runtime_generation FROM exec_sessions WHERE session_id = ?", created.SessionID).Scan(&snapshotGeneration); err != nil {
		t.Fatal(err)
	}
	if snapshotGeneration != oldSession.RuntimeGeneration {
		t.Fatalf("held read snapshot generation=%q, want %q", snapshotGeneration, oldSession.RuntimeGeneration)
	}
	if _, err := database.ExecContext(context.Background(), "INSERT INTO p139_backup_probe(value) VALUES ('committed-in-wal')"); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := database.QueryRowContext(context.Background(), "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		t.Fatal(err)
	}
	if logFrames <= checkpointedFrames {
		t.Fatalf("host WAL checkpoint log=%d checkpointed=%d busy=%d; want committed frames retained", logFrames, checkpointedFrames, busy)
	}
	if err := store.CreateOnlineBackup(context.Background(), database, backupPath); err != nil {
		t.Fatalf("create live WAL-aware authority backup: %v", err)
	}
	p139AssertBackupState(t, backupPath, created.SessionID, oldSession.RuntimeGeneration)

	if err := p139MacStopService(t, first, firstResult.PID); err != nil {
		t.Fatal(err)
	}
	activeProcess = nil
	if err := p135MacWaitProcessGroupGone(oldOwnership.ProcessGroupID, 3*time.Second); err != nil {
		t.Fatalf("old Bash process group after graceful locald stop: %v", err)
	}
	if err := readTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if err := store.RestoreOnlineBackup(context.Background(), databasePath, backupPath); err != nil {
		t.Fatalf("restore authority after locald exited and closed its DB handle: %v", err)
	}

	second, err := harness.Restart()
	if err != nil {
		t.Fatal(err)
	}
	activeProcess = second
	secondResult := p139MacWaitResult(t, second)
	if !secondResult.Ready {
		t.Fatalf("locald did not become ready after restore: %+v", secondResult)
	}
	database, authority = p135MacOpenAuthority(t, databasePath)
	p135MacWaitSessionState(t, authority, created.SessionID, domain.SessionStateLost)
	recovered, err := authority.GetSession(context.Background(), created.SessionID)
	if err != nil || recovered.RuntimeGeneration != oldSession.RuntimeGeneration {
		t.Fatalf("restored old session=%+v err=%v, want old generation %q marked lost", recovered, err, oldSession.RuntimeGeneration)
	}
	if err := p139AssertNoOwnership(t, workspaceRoot, string(created.SessionID)); err != nil {
		t.Fatal(err)
	}
	if err := p135MacWaitProcessGroupGone(oldOwnership.ProcessGroupID, time.Second); err != nil {
		t.Fatalf("old Mac process group after restore: %v", err)
	}
	if err := authority.ConfirmSessionCleanup(context.Background(), created.SessionID); err != nil {
		t.Fatalf("release isolated old-session reservation after proving its group and owner record are absent: %v", err)
	}

	newIntent := p060CreateIntent(t, authority, "p139-mac-new-create", "p139-mac-new-session", "p139-mac-new-create-key")
	p135MacAcceptIntent(t, p060UnixClient(socketPath), newIntent, nil)
	p135MacWaitSessionState(t, authority, newIntent.SessionID, domain.SessionStateReady)
	newSession, err := authority.GetSession(context.Background(), newIntent.SessionID)
	if err != nil || newSession.RuntimeGeneration == "" || newSession.RuntimeGeneration == oldSession.RuntimeGeneration {
		t.Fatalf("post-restore Mac session=%+v err=%v; want a fresh runtime generation distinct from %q", newSession, err, oldSession.RuntimeGeneration)
	}
	newOwnership := p135MacReadOwnership(t, workspaceRoot, string(newIntent.SessionID))
	if newOwnership.PID <= 0 || newOwnership.ProcessGroupID != newOwnership.PID || syscall.Kill(newOwnership.PID, 0) != nil {
		t.Fatalf("fresh Mac runtime ownership=%+v, want a live Bash process group", newOwnership)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p139MacStopService(t, second, secondResult.PID); err != nil {
		t.Fatal(err)
	}
	activeProcess = nil
	if err := p135MacWaitProcessGroupGone(newOwnership.ProcessGroupID, 3*time.Second); err != nil {
		t.Fatalf("new Bash process group after graceful locald stop: %v", err)
	}
	t.Logf("machine=%s os=macOS account=%s: online backup captured committed WAL frames; locald stopped and closed SQLite before restore; restored old generation %s was marked lost without shell reattachment; new ready session used generation %s", hostname, current.Username, oldSession.RuntimeGeneration, newSession.RuntimeGeneration)
}

func p139MacLocaldHelper(t *testing.T) {
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	databasePath := os.Getenv("RSR_P139_DB")
	workspaceRoot := os.Getenv("RSR_P139_WORKSPACES")
	socketPath := os.Getenv("RSR_P139_SOCKET")
	database, authority, service, err := p135MacService(databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dispatchGate := lifecycle.NewGate()
	worker, err := queueworker.New(queueworker.Options{Service: service, Authority: authority, DispatchGate: dispatchGate})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: socketPath,
		DispatchGate: dispatchGate, QueueWake: worker.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopSignals()
	coordinator, err := lifecycle.NewCoordinator(&privateServerShutdown{server: server, worker: worker}, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: macShutdownDrainTimeout, CleanupTimeout: macShutdownCleanupTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RecoverNonterminalJobs(signalContext); err != nil {
		_ = coordinator.Shutdown(context.Background())
		t.Fatal(err)
	}
	worker.Start(signalContext)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve() }()
	if err := testfixture.PublishPhaseResult(reporter, p139MacServiceResult{Ready: true, PID: os.Getpid(), Report: report}); err != nil {
		t.Fatal(err)
	}
	select {
	case serveErr := <-serveErrors:
		shutdownErr := coordinator.Shutdown(context.Background())
		if serveErr != nil || shutdownErr != nil {
			t.Fatalf("unexpected locald helper service exit: %v", errors.Join(serveErr, shutdownErr))
		}
	case <-signalContext.Done():
		shutdownErr := coordinator.Shutdown(context.Background())
		serveErr := <-serveErrors
		if shutdownErr != nil || serveErr != nil {
			t.Fatalf("graceful locald helper shutdown: %v", errors.Join(shutdownErr, serveErr))
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.PublishPhaseResult(reporter, p139MacServiceResult{Stopped: true, PID: os.Getpid(), Report: report}); err != nil {
		t.Fatal(err)
	}
}

func p139MacWaitResult(t *testing.T, process *testfixture.BarrierProcess) p139MacServiceResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		exitErr := errors.New("child has not exited")
		if process.Exited() {
			exitErr = process.Wait(context.Background())
		}
		t.Fatalf("wait for isolated P139 locald result: %v; child_exit=%v; output=%s", err, exitErr, process.Output())
	}
	var result p139MacServiceResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode isolated P139 locald result %s: %v", encoded, err)
	}
	return result
}

func p139MacStopService(t *testing.T, process *testfixture.BarrierProcess, pid int) error {
	t.Helper()
	if pid <= 0 {
		return fmt.Errorf("invalid isolated runner-locald PID %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to isolated runner-locald PID %d: %w", pid, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := process.Wait(ctx); err != nil {
		return fmt.Errorf("wait for graceful isolated runner-locald exit: %w; output=%s", err, process.Output())
	}
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		return fmt.Errorf("read isolated runner-locald shutdown result: %w; output=%s", err, process.Output())
	}
	var result p139MacServiceResult
	if err := json.Unmarshal(encoded, &result); err != nil || !result.Stopped || result.PID != pid {
		return fmt.Errorf("isolated locald shutdown result=%s err=%v, want stopped PID %d", encoded, err, pid)
	}
	return nil
}

func p139AssertBackupState(t *testing.T, backupPath string, sessionID domain.SessionID, generation string) {
	t.Helper()
	database, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open P139 snapshot: %v", err)
	}
	defer database.Close()
	var probe, state, gotGeneration string
	if err := database.QueryRow("SELECT value FROM p139_backup_probe WHERE value = 'committed-in-wal'").Scan(&probe); err != nil || probe != "committed-in-wal" {
		t.Fatalf("snapshot WAL probe=%q err=%v", probe, err)
	}
	if err := database.QueryRow("SELECT state, runtime_generation FROM exec_sessions WHERE session_id = ?", sessionID).Scan(&state, &gotGeneration); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.SessionStateReady) || gotGeneration != generation {
		t.Fatalf("snapshot session state=%q generation=%q; want ready generation %q", state, gotGeneration, generation)
	}
}

func p139AssertNoOwnership(t *testing.T, workspaceRoot, sessionID string) error {
	t.Helper()
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
		var owner p135RuntimeOwnership
		if err := json.Unmarshal(data, &owner); err != nil {
			return err
		}
		if owner.SessionID == sessionID {
			return fmt.Errorf("stale runtime owner record remains for session %s", sessionID)
		}
	}
	return nil
}
