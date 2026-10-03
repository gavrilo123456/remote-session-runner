package runnerlocald

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/queueworker"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	p135MacHostGate = "RSR_P135_MAC_HOST_GATE"
	p135HelperEnv   = "RSR_P135_HELPER"
)

type p135RuntimeOwnership struct {
	SessionID      string `json:"session_id"`
	PID            int    `json:"pid"`
	ProcessGroupID int    `json:"process_group_id"`
}

type p135StartupResult struct {
	Ready  bool                                  `json:"ready"`
	Report execution.StartupReconciliationReport `json:"report"`
}

func TestP135MacLocaldRestartWithKnownSurvivingChild(t *testing.T) {
	if os.Getenv(p135HelperEnv) == "1" {
		p135MacLocaldHelper(t)
		return
	}
	if os.Getenv(p135MacHostGate) != "1" {
		t.Skip("set RSR_P135_MAC_HOST_GATE=1 to run the actual Mac locald kill/restart recovery gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P135 Mac gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" {
		t.Fatalf("P135 Mac account=%v err=%v, want tomasz.walczuk", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	root, err := os.MkdirTemp("/tmp", "p135m-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "state.db")
	workspaceRoot := filepath.Join(root, "work")
	socketPath := filepath.Join(root, "locald.sock")
	childPIDPath := filepath.Join(root, "child.pid")
	laterCommandMarker := filepath.Join(root, "later-command-ran")

	harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=^TestP135MacLocaldRestartWithKnownSurvivingChild$")
		command.Env = append(os.Environ(), p135HelperEnv+"=1",
			"RSR_P135_DB="+databasePath,
			"RSR_P135_WORKSPACES="+workspaceRoot,
			"RSR_P135_SOCKET="+socketPath,
		)
		return command
	})
	t.Cleanup(func() {
		_ = harness.Kill()
		if err := p135MacCleanupFixture(databasePath, workspaceRoot); err != nil {
			t.Errorf("clean isolated P135 runtime after helper exit: %v", err)
		}
		_ = os.RemoveAll(root)
	})

	first, err := harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	firstResult := p135MacWaitReady(t, first)
	if firstResult.Report.SessionsInspected != 0 {
		t.Fatalf("fresh locald startup reconciliation = %+v, want no prior sessions", firstResult.Report)
	}

	db, authority := p135MacOpenAuthority(t, databasePath)
	create := p060CreateIntent(t, authority, "p135-mac-create", "p135-mac-session", "p135-mac-create-key")
	p135MacAcceptIntent(t, p060UnixClient(socketPath), create, nil)
	p135MacWaitSessionState(t, authority, create.SessionID, domain.SessionStateReady)

	firstScript := fmt.Sprintf("(trap '' TERM; exec /bin/sleep 120) >/dev/null 2>&1 &\necho $! > %s\nwait\n", childPIDPath)
	firstIntent := p060SubmitIntent(t, "p135-mac-running-intent", string(create.SessionID), "p135-mac-running", "p135-mac-running-key", firstScript)
	firstStored, err := authority.CreateLocalIntent(context.Background(), firstIntent)
	if err != nil {
		t.Fatal(err)
	}
	p135MacAcceptIntent(t, p060UnixClient(socketPath), firstStored, firstStored.IntentOrdinal)
	p135MacWaitFile(t, childPIDPath)
	childPID := p135MacParsePID(t, childPIDPath)

	secondScript := fmt.Sprintf("printf replacement > %s\n", laterCommandMarker)
	secondIntent := p060SubmitIntent(t, "p135-mac-later-intent", string(create.SessionID), "p135-mac-later", "p135-mac-later-key", secondScript)
	secondStored, err := authority.CreateLocalIntent(context.Background(), secondIntent)
	if err != nil {
		t.Fatal(err)
	}
	p135MacAcceptIntent(t, p060UnixClient(socketPath), secondStored, secondStored.IntentOrdinal)
	p135MacWaitCommandState(t, authority, domain.CommandID("p135-mac-running"), domain.CommandStateRunning)
	p135MacWaitCommandState(t, authority, domain.CommandID("p135-mac-later"), domain.CommandStateQueued)
	ownership := p135MacReadOwnership(t, workspaceRoot, string(create.SessionID))
	if ownership.PID <= 0 || ownership.ProcessGroupID != ownership.PID {
		t.Fatalf("persisted Mac shell identity = %+v, want positive process and group IDs", ownership)
	}
	if childPID == ownership.PID || syscall.Kill(childPID, 0) != nil {
		t.Fatalf("known child PID %d is not live and distinct from Bash PID %d", childPID, ownership.PID)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := harness.Kill(); err != nil {
		t.Fatalf("SIGKILL isolated locald helper: %v", err)
	}
	second, err := harness.Restart()
	if err != nil {
		t.Fatal(err)
	}
	restarted := p135MacWaitReady(t, second)
	if restarted.Report.SessionsInspected != 1 || restarted.Report.CommandsLost != 1 || restarted.Report.CommandsRejected != 1 || restarted.Report.CleanupConfirmed != 1 || restarted.Report.CleanupUnconfirmed != 0 || restarted.Report.RuntimeFailures != 0 {
		t.Fatalf("restarted locald reconciliation = %+v, want one inspected session, one lost command, one rejected queued command, and confirmed cleanup", restarted.Report)
	}

	db, authority = p135MacOpenAuthority(t, databasePath)
	session, err := authority.GetSession(context.Background(), create.SessionID)
	if err != nil || session.State != domain.SessionStateLost {
		t.Fatalf("recovered Mac session = %+v, err=%v; want lost", session, err)
	}
	firstCommand, err := authority.GetCommand(context.Background(), domain.CommandID("p135-mac-running"))
	if err != nil || firstCommand.State != domain.CommandStateLost {
		t.Fatalf("recovered running Mac command = %+v, err=%v; want lost", firstCommand, err)
	}
	secondCommand, err := authority.GetCommand(context.Background(), domain.CommandID("p135-mac-later"))
	if err != nil || secondCommand.State != domain.CommandStateRejected {
		t.Fatalf("recovered queued Mac command = %+v, err=%v; want rejected", secondCommand, err)
	}
	if _, err := os.Stat(laterCommandMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued command ran in a replacement shell: marker stat error=%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p135MacWaitProcessGroupGone(ownership.ProcessGroupID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(workspaceRoot, ".runner-runtime-ownership")); err == nil && len(entries) != 0 {
		t.Fatalf("runtime ownership records remain after confirmed recovery: %v", entries)
	}
	if err := harness.Kill(); err != nil {
		t.Fatal(err)
	}
	t.Logf("machine=%s os=macOS account=%s: isolated locald SIGKILL/restart stopped process group %d; session and active command are lost, queued command rejected, no shell reattached", hostname, current.Username, ownership.ProcessGroupID)
}

func p135MacLocaldHelper(t *testing.T) {
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	databasePath := os.Getenv("RSR_P135_DB")
	workspaceRoot := os.Getenv("RSR_P135_WORKSPACES")
	socketPath := os.Getenv("RSR_P135_SOCKET")
	db, authority, service, err := p135MacService(databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
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
	coordinator, err := lifecycle.NewCoordinator(&privateServerShutdown{server: server, worker: worker}, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: macShutdownDrainTimeout, CleanupTimeout: macShutdownCleanupTimeout,
	})
	if err != nil {
		_ = server.Close(context.Background())
		_ = db.Close()
		t.Fatal(err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		shutdownErr := coordinator.Shutdown(context.Background())
		closeErr := db.Close()
		t.Fatalf("recover queued jobs after startup reconciliation: %v", errors.Join(err, shutdownErr, closeErr))
	}
	worker.Start(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	if err := testfixture.PublishPhaseResult(reporter, p135StartupResult{Ready: true, Report: report}); err != nil {
		t.Fatal(err)
	}
	serveErrResult := <-serveErr
	shutdownErr := coordinator.Shutdown(context.Background())
	if serveErrResult != nil || shutdownErr != nil {
		t.Fatal(errors.Join(serveErrResult, shutdownErr))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func p135MacService(databasePath, workspaceRoot string) (*sql.DB, *store.AuthorityStore, *execution.Service, error) {
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		return nil, nil, nil, err
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS host", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	service, _, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash",
	}, environment)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	return db, authority, service, nil
}

func p135MacOpenAuthority(t *testing.T, databasePath string) (*sql.DB, *store.AuthorityStore) {
	t.Helper()
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, authority
}

func p135MacAcceptIntent(t *testing.T, client *http.Client, intent store.LocalIntentRecord, ordinal *int64) {
	t.Helper()
	body := fmt.Sprintf(`{"intent_id":%q,"request_hash":%q`, intent.IntentID, intent.RequestHash.String())
	if ordinal != nil {
		body += fmt.Sprintf(`,"intent_ordinal":%d`, *ordinal)
	}
	body += `}`
	response := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", []byte(body))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("P135 Mac intent acceptance status=%d body=%s", response.StatusCode, p060ReadBody(t, response))
	}
	_ = p060ReadBody(t, response)
}

func p135MacWaitReady(t *testing.T, process *testfixture.BarrierProcess) p135StartupResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait for isolated Mac locald startup result: %v; output=%s", err, process.Output())
	}
	var result p135StartupResult
	if err := json.Unmarshal(encoded, &result); err != nil || !result.Ready {
		t.Fatalf("isolated Mac locald startup result=%s err=%v", encoded, err)
	}
	return result
}

func p135MacWaitSessionState(t *testing.T, authority *store.AuthorityStore, id domain.SessionID, want domain.SessionState) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		record, err := authority.GetSession(context.Background(), id)
		if err == nil && record.State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, err := authority.GetSession(context.Background(), id)
	t.Fatalf("Mac session state=%+v err=%v, want %s", record, err, want)
}

func p135MacWaitCommandState(t *testing.T, authority *store.AuthorityStore, id domain.CommandID, want domain.CommandState) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		record, err := authority.GetCommand(context.Background(), id)
		if err == nil && record.State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, err := authority.GetCommand(context.Background(), id)
	t.Fatalf("Mac command %s state=%+v err=%v, want %s", id, record, err, want)
}

func p135MacWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("known child did not publish its PID marker %s", path)
}

func p135MacReadOwnership(t *testing.T, workspaceRoot, sessionID string) p135RuntimeOwnership {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workspaceRoot, ".runner-runtime-ownership"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workspaceRoot, ".runner-runtime-ownership", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var record p135RuntimeOwnership
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.SessionID == sessionID {
			return record
		}
	}
	t.Fatalf("runtime ownership record for session %s was not written", sessionID)
	return p135RuntimeOwnership{}
}

func p135MacWaitProcessGroupGone(processGroupID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-processGroupID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("process group %d still exists", processGroupID)
}

func p135MacCleanupFixture(databasePath, workspaceRoot string) error {
	db, _, service, err := p135MacService(databasePath, workspaceRoot)
	if err != nil {
		return err
	}
	_, reconcileErr := service.ReconcileStartup(context.Background())
	closeErr := db.Close()
	return errors.Join(reconcileErr, closeErr)
}

func p135MacParsePID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || value <= 0 {
		t.Fatalf("invalid known child PID %q: %v", data, err)
	}
	return value
}
