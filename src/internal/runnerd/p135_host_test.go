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
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	p135LinuxHostGate = "RSR_P135_LINUX_HOST_GATE"
	p135LinuxHelper   = "RSR_P135_HELPER"
)

type p135LinuxOwnership struct {
	SessionID      string `json:"session_id"`
	PID            int    `json:"pid"`
	ProcessGroupID int    `json:"process_group_id"`
}

type p135LinuxStartupResult struct {
	Ready  bool                                  `json:"ready"`
	Report execution.StartupReconciliationReport `json:"report"`
}

func TestP135UbuntuRunnerdRestartWithKnownSurvivingChild(t *testing.T) {
	if os.Getenv(p135LinuxHelper) == "1" {
		p135LinuxRunnerdHelper(t)
		return
	}
	if os.Getenv(p135LinuxHostGate) != "1" {
		t.Skip("set RSR_P135_LINUX_HOST_GATE=1 to run the actual Ubuntu runnerd kill/restart recovery gate")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P135 Ubuntu gate must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != hostruntime.LinuxHostAccount {
		t.Fatalf("P135 Linux account=%v err=%v, want %s", current, err, hostruntime.LinuxHostAccount)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "oracle-yuta-konopka-ubuntu-micro-02" {
		t.Fatalf("P135 Linux host=%q err=%v, want oracle-yuta-konopka-ubuntu-micro-02", hostname, err)
	}

	root, err := os.MkdirTemp("/tmp", "p135l-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "state.db")
	workspaceRoot := filepath.Join(root, "work")
	socketPath := filepath.Join(root, "runnerd.sock")
	childPIDPath := filepath.Join(root, "child.pid")
	laterCommandMarker := filepath.Join(root, "later-command-ran")

	harness := testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=^TestP135UbuntuRunnerdRestartWithKnownSurvivingChild$")
		command.Env = append(os.Environ(), p135LinuxHelper+"=1",
			"RSR_P135_DB="+databasePath,
			"RSR_P135_WORKSPACES="+workspaceRoot,
			"RSR_P135_SOCKET="+socketPath,
		)
		return command
	})
	t.Cleanup(func() {
		_ = harness.Kill()
		if err := p135LinuxCleanupFixture(databasePath, workspaceRoot); err != nil {
			t.Errorf("clean isolated P135 runtime after helper exit: %v", err)
		}
		_ = os.RemoveAll(root)
	})

	first, err := harness.Start()
	if err != nil {
		t.Fatal(err)
	}
	firstResult := p135LinuxWaitReady(t, first)
	if firstResult.Report.SessionsInspected != 0 {
		t.Fatalf("fresh runnerd startup reconciliation = %+v, want no prior sessions", firstResult.Report)
	}

	db, authority := p135LinuxOpenAuthority(t, databasePath)
	client := p046UnixClient(socketPath)
	createBody := []byte(`{"session_id":"p135-linux-session","idempotency_key":"p135-linux-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	created := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("P135 Ubuntu session create status=%d body=%s", created.StatusCode, p046ReadBody(t, created))
	}
	var session sessionResponse
	p046DecodeJSON(t, created, &session)
	if session.SessionState != string(domain.SessionStateReady) || session.RuntimeGeneration == "" {
		t.Fatalf("P135 Ubuntu created session = %+v, want ready with generation", session)
	}

	firstScript := fmt.Sprintf("(trap '' TERM; exec /bin/sleep 120) >/dev/null 2>&1 &\necho $! > %s\nwait\n", childPIDPath)
	p135LinuxSubmit(t, client, "p135-linux-session", "p135-linux-running", "p135-linux-running-key", firstScript)
	p135LinuxWaitFile(t, childPIDPath)
	childPID := p135LinuxParsePID(t, childPIDPath)
	childInfo, err := os.Stat(filepath.Join("/proc", strconv.Itoa(childPID)))
	if err != nil {
		t.Fatalf("inspect known Ubuntu child PID %d: %v", childPID, err)
	}
	childStat, ok := childInfo.Sys().(*syscall.Stat_t)
	if !ok || int(childStat.Uid) != os.Getuid() {
		t.Fatalf("known Ubuntu child uid=%v, want executor uid %d", childInfo.Sys(), os.Getuid())
	}
	secondScript := fmt.Sprintf("printf replacement > %s\n", laterCommandMarker)
	p135LinuxSubmit(t, client, "p135-linux-session", "p135-linux-later", "p135-linux-later-key", secondScript)
	p135LinuxWaitCommandState(t, authority, domain.CommandID("p135-linux-running"), domain.CommandStateRunning)
	p135LinuxWaitCommandState(t, authority, domain.CommandID("p135-linux-later"), domain.CommandStateQueued)
	ownership := p135LinuxReadOwnership(t, workspaceRoot, "p135-linux-session")
	if ownership.PID <= 0 || ownership.ProcessGroupID != ownership.PID {
		t.Fatalf("persisted Ubuntu Bash identity = %+v, want positive process and group IDs", ownership)
	}
	if childPID == ownership.PID || syscall.Kill(childPID, 0) != nil {
		t.Fatalf("known child PID %d is not live and distinct from Bash PID %d", childPID, ownership.PID)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := harness.Kill(); err != nil {
		t.Fatalf("SIGKILL isolated runnerd helper: %v", err)
	}
	second, err := harness.Restart()
	if err != nil {
		t.Fatal(err)
	}
	restarted := p135LinuxWaitReady(t, second)
	if restarted.Report.SessionsInspected != 1 || restarted.Report.CommandsLost != 1 || restarted.Report.CommandsRejected != 1 || restarted.Report.CleanupConfirmed != 1 || restarted.Report.CleanupUnconfirmed != 0 || restarted.Report.RuntimeFailures != 0 {
		t.Fatalf("restarted runnerd reconciliation = %+v, want one inspected session, one lost command, one rejected queued command, and confirmed cleanup", restarted.Report)
	}

	db, authority = p135LinuxOpenAuthority(t, databasePath)
	recoveredSession, err := authority.GetSession(context.Background(), domain.SessionID("p135-linux-session"))
	if err != nil || recoveredSession.State != domain.SessionStateLost {
		t.Fatalf("recovered Ubuntu session = %+v, err=%v; want lost", recoveredSession, err)
	}
	firstCommand, err := authority.GetCommand(context.Background(), domain.CommandID("p135-linux-running"))
	if err != nil || firstCommand.State != domain.CommandStateLost {
		t.Fatalf("recovered running Ubuntu command = %+v, err=%v; want lost", firstCommand, err)
	}
	secondCommand, err := authority.GetCommand(context.Background(), domain.CommandID("p135-linux-later"))
	if err != nil || secondCommand.State != domain.CommandStateRejected {
		t.Fatalf("recovered queued Ubuntu command = %+v, err=%v; want rejected", secondCommand, err)
	}
	if _, err := os.Stat(laterCommandMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued command ran in a replacement shell: marker stat error=%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p135LinuxWaitProcessGroupGone(ownership.ProcessGroupID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(workspaceRoot, ".runner-runtime-ownership")); err == nil && len(entries) != 0 {
		t.Fatalf("runtime ownership records remain after confirmed recovery: %v", entries)
	}
	if err := harness.Kill(); err != nil {
		t.Fatal(err)
	}
	t.Logf("machine=oracle-yuta-konopka-ubuntu-micro-02 account=%s: isolated runnerd SIGKILL/restart stopped process group %d; session and active command are lost, queued command rejected, no shell reattached", current.Username, ownership.ProcessGroupID)
}

func p135LinuxRunnerdHelper(t *testing.T) {
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	databasePath := os.Getenv("RSR_P135_DB")
	workspaceRoot := os.Getenv("RSR_P135_WORKSPACES")
	socketPath := os.Getenv("RSR_P135_SOCKET")
	db, _, service, err := p135LinuxService(databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	if err := testfixture.PublishPhaseResult(reporter, p135LinuxStartupResult{Ready: true, Report: report}); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func p135LinuxService(databasePath, workspaceRoot string) (*sql.DB, *store.AuthorityStore, *execution.Service, error) {
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		return nil, nil, nil, err
	}
	if err := os.Chmod(workspaceRoot, 0o700); err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		return nil, nil, nil, err
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	queuedID, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	queued, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, queuedID)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	direct, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, queuedID)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "linux-dev", HostClass: "Ubuntu Linux host", EffectiveAccount: hostruntime.LinuxHostAccount,
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{queued, direct}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	service, _, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account: hostruntime.LinuxHostAccount, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash",
	}, environment)
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	return db, authority, service, nil
}

func p135LinuxOpenAuthority(t *testing.T, databasePath string) (*sql.DB, *store.AuthorityStore) {
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

func p135LinuxSubmit(t *testing.T, client *http.Client, sessionID, commandID, idempotencyKey, script string) {
	t.Helper()
	body, err := json.Marshal(struct {
		CommandID      string `json:"command_id"`
		SessionID      string `json:"session_id"`
		IdempotencyKey string `json:"idempotency_key"`
		Controller     struct {
			Type string `json:"controller_type"`
			ID   string `json:"controller_id"`
		} `json:"controller"`
		Script string `json:"script"`
	}{
		CommandID: commandID, SessionID: sessionID, IdempotencyKey: idempotencyKey,
		Controller: struct {
			Type string `json:"controller_type"`
			ID   string `json:"controller_id"`
		}{Type: "direct_mtls", ID: "tomasz.walczuk"},
		Script: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions/"+sessionID+"/commands", body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("P135 Ubuntu command submit status=%d body=%s", response.StatusCode, p046ReadBody(t, response))
	}
	_ = p046ReadBody(t, response)
}

func p135LinuxWaitReady(t *testing.T, process *testfixture.BarrierProcess) p135LinuxStartupResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait for isolated Ubuntu runnerd startup result: %v; output=%s", err, process.Output())
	}
	var result p135LinuxStartupResult
	if err := json.Unmarshal(encoded, &result); err != nil || !result.Ready {
		t.Fatalf("isolated Ubuntu runnerd startup result=%s err=%v", encoded, err)
	}
	return result
}

func p135LinuxWaitCommandState(t *testing.T, authority *store.AuthorityStore, id domain.CommandID, want domain.CommandState) {
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
	t.Fatalf("Ubuntu command %s state=%+v err=%v, want %s", id, record, err, want)
}

func p135LinuxWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("known Ubuntu child did not publish its PID marker %s", path)
}

func p135LinuxParsePID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid known Ubuntu child PID %q: %v", data, err)
	}
	return pid
}

func p135LinuxReadOwnership(t *testing.T, workspaceRoot, sessionID string) p135LinuxOwnership {
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
		var record p135LinuxOwnership
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.SessionID == sessionID {
			return record
		}
	}
	t.Fatalf("Ubuntu runtime ownership record for session %s was not written", sessionID)
	return p135LinuxOwnership{}
}

func p135LinuxWaitProcessGroupGone(processGroupID int, timeout time.Duration) error {
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

func p135LinuxCleanupFixture(databasePath, workspaceRoot string) error {
	db, _, service, err := p135LinuxService(databasePath, workspaceRoot)
	if err != nil {
		return err
	}
	_, reconcileErr := service.ReconcileStartup(context.Background())
	closeErr := db.Close()
	return errors.Join(reconcileErr, closeErr)
}
