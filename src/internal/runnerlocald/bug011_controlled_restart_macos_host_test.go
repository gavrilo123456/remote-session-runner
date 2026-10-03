package runnerlocald

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	bug011MacControlledRestartHelperEnv = "RSR_B011_MAC_CONTROLLED_RESTART_HELPER"
	bug011MacControlledRestartModeEnv   = "RSR_B011_MAC_CONTROLLED_RESTART_MODE"
	bug011MacP7FreezePathEnv            = "RSR_B011_MAC_P7_FREEZE_PATH"
	bug011MacP7FrozenPathEnv            = "RSR_B011_MAC_P7_FROZEN_PATH"

	bug011MacP7InitialMode          = "initial"
	bug011MacP7CandidatePausedMode  = "candidate_paused_before_claim"
	bug011MacP7ResumeMode           = "resume_after_candidate_crash"
	bug011MacP7InitialWorkerPeriod  = time.Hour
	bug011MacP7RecoveryWorkerPeriod = 50 * time.Millisecond
)

// bug011MacP7StartupResult is sent only through the isolated test subprocess
// barrier. It contains durable identifiers and lifecycle state, never a script
// or any production path.
type bug011MacP7StartupResult struct {
	Ready             bool                                  `json:"ready"`
	Mode              string                                `json:"mode"`
	Report            execution.StartupReconciliationReport `json:"report"`
	ControlledRestart bool                                  `json:"controlled_restart"`
	Rehydrated        bool                                  `json:"rehydrated"`
	PlanActive        bool                                  `json:"plan_active"`
	JobID             string                                `json:"job_id,omitempty"`
	SessionID         string                                `json:"session_id,omitempty"`
	CommandID         string                                `json:"command_id,omitempty"`
}

// bug011MacP7Fixture reuses the real Mac private-server fixture shape from P6,
// but every path is underneath one fresh 0700 /tmp root. Its helper is a test
// binary process, never the installed runner-locald LaunchAgent.
type bug011MacP7Fixture struct {
	fixture    *bug011MacFixture
	mode       string
	freezePath string
	frozenPath string
	queued     *bug011MacQueuedOneOff
}

// bug011MacControlledRestartP7 is the actual-Darwin P7 test. The first helper
// creates the exact retained-four/lone-queued state. The parent freezes its
// dispatch worker, writes the durable plan, and SIGKILLs that helper. A
// candidate rehydrates and activates the plan but is SIGKILLed before any
// worker claim. A third helper must rebuild the same queued identity and let
// the ordinary shared worker release the retained lost capacity and execute
// that command once.
func bug011MacControlledRestartP7(t *testing.T) {
	t.Helper()
	p7 := newBUG011MacP7Fixture(t)
	fixture := p7.fixture

	lost, queued := bug011MacPrepareFourLostPairsAndQueuedOneOff(t, fixture, false, false)
	p7.queued = &queued
	bug011MacReleaseFourthLoss(t, fixture, lost[len(lost)-1])
	bug011MacP7FreezeInitialWorker(t, p7)

	plan := bug011MacP7PreparePlan(t, fixture, lost, queued)
	bug011MacP7AssertPreparedBoundary(t, fixture, plan, queued)
	bug011MacP7CloseAuthority(t, p7)

	if err := fixture.harness.Kill(); err != nil {
		t.Fatalf("P7 hard-stop initial isolated locald helper: %v", err)
	}

	candidate := bug011MacP7Restart(t, p7, bug011MacP7CandidatePausedMode)
	if !candidate.ControlledRestart || !candidate.Rehydrated || !candidate.PlanActive ||
		candidate.JobID != string(queued.jobID) || candidate.SessionID != string(queued.sessionID) || candidate.CommandID != string(queued.commandID) {
		t.Fatalf("P7 candidate startup=%+v, want active rehydrated exact queued identity", candidate)
	}
	bug011MacP7OpenAuthority(t, p7)
	bug011MacP7AssertActiveBeforeClaim(t, fixture, plan, queued)
	bug011MacP7CloseAuthority(t, p7)

	if err := fixture.harness.Kill(); err != nil {
		t.Fatalf("P7 hard-stop isolated candidate before scheduler claim: %v", err)
	}

	resumed := bug011MacP7Restart(t, p7, bug011MacP7ResumeMode)
	if !resumed.ControlledRestart || !resumed.Rehydrated || !resumed.PlanActive ||
		resumed.JobID != string(queued.jobID) || resumed.SessionID != string(queued.sessionID) || resumed.CommandID != string(queued.commandID) {
		t.Fatalf("P7 resumed startup=%+v, want same active rehydrated queued identity", resumed)
	}
	bug011MacP7OpenAuthority(t, p7)
	bug011MacAssertRecoveredQueuedOneOff(t, fixture, lost, queued)
	if _, err := fixture.authority.ReadControlledRestartPlan(context.Background()); !errors.Is(err, store.ErrControlledRestartPlanNotFound) {
		t.Fatalf("P7 durable plan after exact queued claim err=%v, want not found", err)
	}
}

func newBUG011MacP7Fixture(t *testing.T) *bug011MacP7Fixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", bug011MacFixtureTag+"p7-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	fixture := &bug011MacFixture{
		root:          root,
		databasePath:  filepath.Join(root, "authority.db"),
		workspaceRoot: workspaceRoot,
		socketPath:    filepath.Join(root, "locald.sock"),
		mutated:       make(map[string]hostruntime.RuntimeOwnershipRecord),
		lostPIDs:      make(map[int]struct{}),
	}
	p7 := &bug011MacP7Fixture{
		fixture:    fixture,
		mode:       bug011MacP7InitialMode,
		freezePath: filepath.Join(root, "freeze-initial-worker"),
		frozenPath: filepath.Join(root, "initial-worker-frozen"),
	}
	fixture.harness = testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=^TestBUG011MacOnlineLostCapacityRecovery$")
		command.Env = append(os.Environ(),
			bug011MacControlledRestartHelperEnv+"=1",
			bug011MacControlledRestartModeEnv+"="+p7.mode,
			bug011MacDBEnv+"="+fixture.databasePath,
			bug011MacWorkEnv+"="+fixture.workspaceRoot,
			bug011MacSocketEnv+"="+fixture.socketPath,
			bug011MacP7FreezePathEnv+"="+p7.freezePath,
			bug011MacP7FrozenPathEnv+"="+p7.frozenPath,
		)
		return command
	})
	t.Cleanup(func() {
		bug011MacP7CleanupQueuedRuntime(t, p7)
		bug011MacCleanupFixture(t, fixture)
	})

	initial, err := fixture.harness.Start()
	if err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	fixture.process = initial
	result := bug011MacP7WaitReady(t, initial)
	if !result.Ready || result.Mode != bug011MacP7InitialMode || result.ControlledRestart || result.Rehydrated || result.Report.SessionsInspected != 0 {
		t.Fatalf("P7 initial isolated locald startup=%+v", result)
	}
	bug011MacAssertFixtureModes(t, fixture)
	bug011MacP7OpenAuthority(t, p7)
	return p7
}

func bug011MacP7Restart(t *testing.T, p7 *bug011MacP7Fixture, mode string) bug011MacP7StartupResult {
	t.Helper()
	if p7 == nil || p7.fixture == nil || p7.fixture.harness == nil || p7.fixture.process == nil || !p7.fixture.process.Exited() {
		t.Fatal("P7 helper restart requires a stopped isolated helper")
	}
	p7.mode = mode
	process, err := p7.fixture.harness.Restart()
	if err != nil {
		t.Fatal(err)
	}
	p7.fixture.process = process
	return bug011MacP7WaitReady(t, process)
}

func bug011MacP7OpenAuthority(t *testing.T, p7 *bug011MacP7Fixture) {
	t.Helper()
	if p7 == nil || p7.fixture == nil {
		t.Fatal("P7 fixture is unavailable")
	}
	if p7.fixture.database != nil || p7.fixture.authority != nil {
		t.Fatal("P7 parent authority is already open")
	}
	database, err := store.Open(context.Background(), p7.fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	p7.fixture.database = database
	p7.fixture.authority = authority
	p7.fixture.client = p060UnixClient(p7.fixture.socketPath)
}

func bug011MacP7CloseAuthority(t *testing.T, p7 *bug011MacP7Fixture) {
	t.Helper()
	if p7 == nil || p7.fixture == nil || p7.fixture.database == nil {
		return
	}
	if err := p7.fixture.database.Close(); err != nil {
		t.Fatalf("close P7 parent authority: %v", err)
	}
	p7.fixture.database = nil
	p7.fixture.authority = nil
	p7.fixture.client = nil
}

func bug011MacP7FreezeInitialWorker(t *testing.T, p7 *bug011MacP7Fixture) {
	t.Helper()
	if err := bug011MacP7PublishEmptyOwnerFile(p7.freezePath); err != nil {
		t.Fatalf("publish P7 initial worker freeze marker: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(p7.frozenPath)
		if err == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
				t.Fatalf("P7 frozen worker marker mode=%s size=%d, want regular 0600 empty", info.Mode(), info.Size())
			}
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect P7 frozen worker marker: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("P7 initial helper did not freeze its dispatch worker; output=%s", p7.fixture.process.Output())
}

func bug011MacP7PreparePlan(t *testing.T, fixture *bug011MacFixture, lost []bug011MacLostCommand, queued bug011MacQueuedOneOff) store.ControlledRestartPlan {
	t.Helper()
	if len(lost) != store.DefaultRunningCommandLimit {
		t.Fatalf("P7 retained lost pairs=%d, want %d", len(lost), store.DefaultRunningCommandLimit)
	}
	if slots, err := fixture.authority.CountLiveCommandSlots(context.Background()); err != nil || slots != store.DefaultRunningCommandLimit {
		t.Fatalf("P7 pre-plan command slots=%d err=%v, want %d", slots, err, store.DefaultRunningCommandLimit)
	}
	if reservations, err := fixture.authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != store.DefaultRunningCommandLimit+1 {
		t.Fatalf("P7 pre-plan live session reservations=%d err=%v, want %d", reservations, err, store.DefaultRunningCommandLimit+1)
	}
	pairs, err := fixture.authority.ListRetainedLostRuntimeRecoveryPairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.authority.PrepareControlledRestartPlan(context.Background(), pairs)
	if err != nil {
		t.Fatalf("prepare P7 controlled restart plan: %v", err)
	}
	if plan.Activated || plan.ActivatedAt != nil || plan.JobID != queued.jobID || plan.SessionID != queued.sessionID || plan.CommandID != queued.commandID || len(plan.LostPairs) != store.DefaultRunningCommandLimit {
		t.Fatalf("prepared P7 plan=%+v, want untouched queued identity and four inactive lost pairs", plan)
	}
	return plan
}

func bug011MacP7AssertPreparedBoundary(t *testing.T, fixture *bug011MacFixture, plan store.ControlledRestartPlan, queued bug011MacQueuedOneOff) {
	t.Helper()
	stored, err := fixture.authority.ReadControlledRestartPlan(context.Background())
	if err != nil || stored.Activated || stored.JobID != plan.JobID || stored.SessionID != queued.sessionID || stored.CommandID != queued.commandID {
		t.Fatalf("P7 stored prepared plan=%+v err=%v", stored, err)
	}
	command, err := fixture.authority.GetCommand(context.Background(), queued.commandID)
	if err != nil || command.State != domain.CommandStateQueued {
		t.Fatalf("P7 prepared queued command=%+v err=%v, want queued", command, err)
	}
	if _, err := fixture.authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); !errors.Is(err, store.ErrControlledRestartPlanNotActive) {
		t.Fatalf("P7 scheduler claim while plan is prepared err=%v, want %v", err, store.ErrControlledRestartPlanNotActive)
	}
	if _, err := os.Stat(queued.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("P7 prepared plan ran queued script: %v", err)
	}
}

func bug011MacP7AssertActiveBeforeClaim(t *testing.T, fixture *bug011MacFixture, prepared store.ControlledRestartPlan, queued bug011MacQueuedOneOff) {
	t.Helper()
	plan, err := fixture.authority.ReadControlledRestartPlan(context.Background())
	if err != nil || !plan.Activated || plan.ActivatedAt == nil || plan.JobID != prepared.JobID || plan.SessionID != queued.sessionID || plan.CommandID != queued.commandID {
		t.Fatalf("P7 candidate durable plan=%+v err=%v, want same active plan", plan, err)
	}
	job, err := fixture.authority.GetJob(context.Background(), queued.jobID)
	if err != nil || job.Phase != store.JobPhaseAwaitingCommand || job.SessionID != queued.sessionID || job.CommandID != queued.commandID {
		t.Fatalf("P7 candidate queued job=%+v err=%v", job, err)
	}
	command, err := fixture.authority.GetCommand(context.Background(), queued.commandID)
	if err != nil || command.State != domain.CommandStateQueued {
		t.Fatalf("P7 candidate queued command=%+v err=%v, want queued", command, err)
	}
	bug011MacAssertEventCounts(t, fixture.authority, queued.commandID, 0, 0, "command_succeeded")
	if _, err := os.Stat(queued.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("P7 candidate ran queued script before parent hard-stop: %v", err)
	}
}

func bug011MacP7WaitReady(t *testing.T, process *testfixture.BarrierProcess) bug011MacP7StartupResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait for P7 isolated Mac helper: %v; output=%s", err, process.Output())
	}
	var result bug011MacP7StartupResult
	if err := json.Unmarshal(encoded, &result); err != nil || !result.Ready {
		t.Fatalf("P7 isolated Mac helper startup=%s result=%+v err=%v", encoded, result, err)
	}
	return result
}

func bug011MacControlledRestartHelper(t *testing.T) {
	t.Helper()
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()

	mode := os.Getenv(bug011MacControlledRestartModeEnv)
	if mode != bug011MacP7InitialMode && mode != bug011MacP7CandidatePausedMode && mode != bug011MacP7ResumeMode {
		t.Fatalf("invalid P7 isolated helper mode %q", mode)
	}
	databasePath := os.Getenv(bug011MacDBEnv)
	workspaceRoot := os.Getenv(bug011MacWorkEnv)
	socketPath := os.Getenv(bug011MacSocketEnv)
	freezePath := os.Getenv(bug011MacP7FreezePathEnv)
	frozenPath := os.Getenv(bug011MacP7FrozenPathEnv)
	if databasePath == "" || workspaceRoot == "" || socketPath == "" || freezePath == "" || frozenPath == "" {
		t.Fatal("P7 isolated helper fixture paths are required")
	}

	database, authority, service, err := bug011MacService(databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var report execution.StartupReconciliationReport
	var startup execution.ControlledRestartStartup
	switch mode {
	case bug011MacP7InitialMode:
		report, err = service.ReconcileStartup(context.Background())
	case bug011MacP7CandidatePausedMode, bug011MacP7ResumeMode:
		report, startup, err = service.ReconcileStartupWithControlledRestartPlan(context.Background())
	}
	if err != nil {
		t.Fatalf("P7 helper %s startup reconciliation: %v", mode, err)
	}

	dispatchGate := lifecycle.NewGate()
	workerPeriod := bug011MacP7RecoveryWorkerPeriod
	if mode == bug011MacP7InitialMode {
		workerPeriod = bug011MacP7InitialWorkerPeriod
	}
	worker, err := queueworker.New(queueworker.Options{
		Service: service, Authority: authority, DispatchGate: dispatchGate,
		RecoveryInterval: workerPeriod, RetainedLostCapacityRecoveryInterval: workerPeriod,
	})
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
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()

	result := bug011MacP7StartupResult{
		Ready:             true,
		Mode:              mode,
		Report:            report,
		ControlledRestart: startup.Plan != nil,
		Rehydrated:        startup.Rehydrated,
	}
	if startup.Plan != nil {
		result.PlanActive = startup.Plan.Activated
		result.JobID = string(startup.Plan.JobID)
		result.SessionID = string(startup.Plan.SessionID)
		result.CommandID = string(startup.Plan.CommandID)
	}

	if mode == bug011MacP7CandidatePausedMode {
		if !result.ControlledRestart || !result.Rehydrated || !result.PlanActive {
			t.Fatalf("P7 candidate did not activate an exact rehydrated plan: %+v", result)
		}
		if err := testfixture.PublishPhaseResult(reporter, result); err != nil {
			t.Fatal(err)
		}
		// Deliberately do not start or recover the queue worker. The parent
		// SIGKILLs this exact helper after observing the durable active plan.
		select {}
	}

	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		shutdownErr := coordinator.Shutdown(context.Background())
		t.Fatalf("P7 helper %s recover queued jobs: %v", mode, errors.Join(err, shutdownErr))
	}
	if err := testfixture.PublishPhaseResult(reporter, result); err != nil {
		t.Fatal(err)
	}
	worker.Start(context.Background())

	var freezeDone <-chan error
	if mode == bug011MacP7InitialMode {
		done := make(chan error, 1)
		freezeDone = done
		go func() { done <- bug011MacP7WaitAndFreezeWorker(worker, dispatchGate, freezePath, frozenPath) }()
	}
	for {
		select {
		case freezeErr := <-freezeDone:
			freezeDone = nil
			if freezeErr != nil {
				shutdownErr := coordinator.Shutdown(context.Background())
				t.Fatalf("P7 initial worker freeze: %v", errors.Join(freezeErr, shutdownErr))
			}
		case serveErrResult := <-serveErr:
			shutdownErr := coordinator.Shutdown(context.Background())
			if serveErrResult != nil || shutdownErr != nil {
				t.Fatal(errors.Join(serveErrResult, shutdownErr))
			}
			return
		}
	}
}

func bug011MacP7WaitAndFreezeWorker(worker *queueworker.Worker, dispatchGate *lifecycle.Gate, freezePath, frozenPath string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(freezePath); err == nil {
			dispatchGate.Stop()
			worker.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			workerErr := worker.Wait(ctx)
			gateErr := dispatchGate.Wait(ctx)
			cancel()
			if workerErr != nil || gateErr != nil {
				return errors.Join(workerErr, gateErr)
			}
			return bug011MacP7PublishEmptyOwnerFile(frozenPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect P7 initial freeze marker: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("timed out waiting for P7 initial worker freeze marker")
}

func bug011MacP7PublishEmptyOwnerFile(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		return fmt.Errorf("published P7 marker has unsafe mode or contents")
	}
	return nil
}

// bug011MacP7CleanupQueuedRuntime is only failure cleanup for a test-owned
// path. It asks a fresh Mac adapter to apply the same exact-root/group proof;
// it never sends an unconstrained signal. A successful test has already closed
// this shell through the ordinary one-off completion path.
func bug011MacP7CleanupQueuedRuntime(t *testing.T, p7 *bug011MacP7Fixture) {
	t.Helper()
	if p7 == nil || p7.fixture == nil || p7.queued == nil {
		return
	}
	record, found, err := bug011MacP7ReadQueuedOwner(p7.fixture.workspaceRoot, p7.queued.sessionID)
	if err != nil {
		t.Errorf("read test-owned P7 queued ownership during cleanup: %v", err)
		return
	}
	if !found {
		return
	}
	if record.Workspace == "" || !bug011MacP7PathWithin(p7.fixture.workspaceRoot, record.Workspace) {
		t.Errorf("refuse P7 cleanup for ownership workspace outside fixture: %q", record.Workspace)
		return
	}
	adapter, err := hostruntime.NewMacProcessAdapter(hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: p7.fixture.workspaceRoot, ShellPath: "/bin/bash",
	})
	if err != nil {
		t.Errorf("construct test-owned P7 cleanup adapter: %v", err)
		return
	}
	result, err := adapter.ReconcileExactSession(context.Background(), string(p7.queued.sessionID), record.Generation, 500*time.Millisecond)
	if err != nil || !result.CleanupConfirmed {
		t.Errorf("test-owned P7 queued runtime remained after cleanup proof result=%+v err=%v", result, err)
	}
}

func bug011MacP7ReadQueuedOwner(workspaceRoot string, sessionID domain.SessionID) (hostruntime.RuntimeOwnershipRecord, bool, error) {
	directory := filepath.Join(workspaceRoot, ".runner-runtime-ownership")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return hostruntime.RuntimeOwnershipRecord{}, false, nil
	}
	if err != nil {
		return hostruntime.RuntimeOwnershipRecord{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return hostruntime.RuntimeOwnershipRecord{}, false, err
		}
		var record hostruntime.RuntimeOwnershipRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			return hostruntime.RuntimeOwnershipRecord{}, false, err
		}
		if record.SessionID == string(sessionID) {
			return record, true, nil
		}
	}
	return hostruntime.RuntimeOwnershipRecord{}, false, nil
}

func bug011MacP7PathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
