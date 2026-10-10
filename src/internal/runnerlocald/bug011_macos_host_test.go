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

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/queueworker"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	bug011MacHostGate   = "RSR_B011_MAC_HOST_GATE"
	bug011MacHelperEnv  = "RSR_B011_MAC_HELPER"
	bug011MacDBEnv      = "RSR_B011_MAC_DB"
	bug011MacWorkEnv    = "RSR_B011_MAC_WORKSPACES"
	bug011MacSocketEnv  = "RSR_B011_MAC_SOCKET"
	bug011MacFixtureTag = "rsr-bug011-mac-"

	bug011MacWorkerInterval = 50 * time.Millisecond
)

type bug011MacStartupResult struct {
	Ready  bool                                  `json:"ready"`
	Report execution.StartupReconciliationReport `json:"report"`
}

type bug011MacOwnership struct {
	path   string
	record hostruntime.RuntimeOwnershipRecord
}

type bug011MacLostCommand struct {
	sessionID    domain.SessionID
	commandID    domain.CommandID
	ownership    bug011MacOwnership
	liveChildPID int
}

type bug011MacQueuedOneOff struct {
	jobID     domain.JobID
	sessionID domain.SessionID
	commandID domain.CommandID
	marker    string
}

type bug011MacFixture struct {
	root          string
	databasePath  string
	workspaceRoot string
	socketPath    string
	releasePath   string
	fourthCommand domain.CommandID

	harness   *testfixture.PhaseHarness
	process   *testfixture.BarrierProcess
	database  *sql.DB
	authority *store.AuthorityStore
	client    *http.Client

	mutated        map[string]hostruntime.RuntimeOwnershipRecord
	lostPIDs       map[int]struct{}
	queuedOneOff   bool
	liveChildPID   int
	liveChildGroup int
}

// TestBUG011MacOnlineLostCapacityRecovery is deliberately an opt-in real Mac
// process gate. It uses a source-built helper and fixture paths below /tmp;
// it never reads or changes an installed Runner service, mailbox, or database.
func TestBUG011MacOnlineLostCapacityRecovery(t *testing.T) {
	if os.Getenv(bug011MacControlledRestartHelperEnv) == "1" {
		bug011MacControlledRestartHelper(t)
		return
	}
	if os.Getenv(bug011MacHelperEnv) == "1" {
		bug011MacLocaldHelper(t)
		return
	}
	if os.Getenv(bug011MacHostGate) != "1" {
		t.Skip("set RSR_B011_MAC_HOST_GATE=1 to run the isolated actual Mac lost-capacity recovery gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("BUG-011 Mac host gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" || current.Uid != "502" {
		t.Fatalf("BUG-011 Mac account=%v err=%v, want tomasz.walczuk uid 502", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "AMAK2KJ6X9JJJ" {
		t.Fatalf("BUG-011 Mac host=%q err=%v, want AMAK2KJ6X9JJJ", hostname, err)
	}

	t.Run("exact_four_lost_pairs_recover_queued_identity", func(t *testing.T) {
		fixture := newBUG011MacFixture(t)
		// A temporary marker mismatch is the test-only recovery barrier. It
		// lets the real worker reach and fail its proof path before the test
		// observes every owned zombie. Restoring the original marker lets the
		// same worker retry automatically; no recovery method is called here.
		lost, queued := bug011MacPrepareFourLostPairsAndQueuedOneOff(t, fixture, true, false)
		cleanupFailures := bug011MacRuntimeCleanupFailureCount(t, fixture.authority, lost[0])
		bug011MacReleaseFourthLoss(t, fixture, lost[3])
		bug011MacWaitForNewRuntimeCleanupFailure(t, fixture.authority, lost[0], cleanupFailures)
		bug011MacAssertRetainedUnprovenRecovery(t, fixture, lost, queued)
		bug011MacRestoreFirstOwnership(t, fixture, lost[0])
		bug011MacAssertRecoveredQueuedOneOff(t, fixture, lost, queued)
	})

	t.Run("identity_mismatch_retains_then_recovers", func(t *testing.T) {
		fixture := newBUG011MacFixture(t)
		lost, queued := bug011MacPrepareFourLostPairsAndQueuedOneOff(t, fixture, true, true)
		cleanupFailures := bug011MacRuntimeCleanupFailureCount(t, fixture.authority, lost[0])
		bug011MacReleaseFourthLoss(t, fixture, lost[3])
		bug011MacWaitForNewRuntimeCleanupFailure(t, fixture.authority, lost[0], cleanupFailures)
		bug011MacAssertRetainedUnprovenRecovery(t, fixture, lost, queued)
		bug011MacStopKnownLiveChild(t, fixture, lost[0])
		bug011MacRestoreFirstOwnership(t, fixture, lost[0])
		bug011MacAssertRecoveredQueuedOneOff(t, fixture, lost, queued)
	})

	t.Run("controlled_restart_preserves_queued_one_off_across_candidate_crash", func(t *testing.T) {
		bug011MacControlledRestartP7(t)
	})
}

func newBUG011MacFixture(t *testing.T) *bug011MacFixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", bug011MacFixtureTag)
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
	fixture.harness = testfixture.NewPhaseHarness(t, func() *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=^TestBUG011MacOnlineLostCapacityRecovery$")
		command.Env = append(os.Environ(),
			bug011MacHelperEnv+"=1",
			bug011MacDBEnv+"="+fixture.databasePath,
			bug011MacWorkEnv+"="+fixture.workspaceRoot,
			bug011MacSocketEnv+"="+fixture.socketPath,
		)
		return command
	})
	process, err := fixture.harness.Start()
	if err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	fixture.process = process
	// Register after PhaseHarness.Start. Go runs cleanup in LIFO order, so
	// this releases and reaps only test-owned children while the helper still
	// owns their exec.Cmd handles, before the harness kills that helper.
	t.Cleanup(func() { bug011MacCleanupFixture(t, fixture) })
	bug011MacWaitReady(t, process)
	bug011MacAssertFixtureModes(t, fixture)

	database, err := store.Open(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	fixture.database = database
	fixture.authority = authority
	fixture.client = p060UnixClient(fixture.socketPath)
	return fixture
}

func bug011MacCleanupFixture(t *testing.T, fixture *bug011MacFixture) {
	t.Helper()
	if fixture == nil {
		return
	}
	for path, original := range fixture.mutated {
		if err := bug011MacReplaceOwnership(path, original); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore test-owned ownership marker during cleanup: %v", err)
		}
	}
	if fixture.liveChildPID > 0 {
		if err := bug011MacStopKnownPID(fixture.liveChildPID, fixture.liveChildGroup); err != nil {
			t.Errorf("stop known test-owned live child %d during cleanup: %v", fixture.liveChildPID, err)
		}
	}
	if fixture.releasePath != "" {
		if err := bug011MacPublishRelease(fixture.releasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("release test-owned fourth loss during cleanup: %v", err)
		}
		if fixture.authority != nil && fixture.fourthCommand != "" {
			if err := bug011MacWaitForCommandNotRunning(fixture.authority, fixture.fourthCommand, 4*time.Second); err != nil {
				t.Errorf("wait for test-owned fourth command before helper stop: %v", err)
			}
		}
	}
	if fixture.queuedOneOff && len(fixture.lostPIDs) == store.DefaultRunningCommandLimit {
		if err := bug011MacWaitForPIDsGone(fixture.lostPIDs, 4*time.Second); err != nil {
			t.Errorf("reap all test-owned lost Bash PIDs before helper stop: %v", err)
		}
	}
	if fixture.harness != nil && fixture.process != nil && !fixture.process.Exited() {
		if err := fixture.harness.Kill(); err != nil {
			t.Errorf("stop isolated BUG-011 Mac helper: %v", err)
		}
	}
	if fixture.database != nil {
		if err := fixture.database.Close(); err != nil {
			t.Errorf("close isolated BUG-011 Mac authority: %v", err)
		}
	}
	if fixture.root != "" {
		if err := os.RemoveAll(fixture.root); err != nil {
			t.Errorf("remove isolated BUG-011 Mac fixture root: %v", err)
		}
	}
}

func bug011MacLocaldHelper(t *testing.T) {
	reporter, err := testfixture.OpenPhaseBarrierReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	databasePath := os.Getenv(bug011MacDBEnv)
	workspaceRoot := os.Getenv(bug011MacWorkEnv)
	socketPath := os.Getenv(bug011MacSocketEnv)
	if databasePath == "" || workspaceRoot == "" || socketPath == "" {
		t.Fatal("BUG-011 Mac helper fixture paths are required")
	}
	database, authority, service, err := bug011MacService(databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	dispatchGate := lifecycle.NewGate()
	worker, err := queueworker.New(queueworker.Options{
		Service: service, Authority: authority, DispatchGate: dispatchGate,
		RecoveryInterval:                     bug011MacWorkerInterval,
		RetainedLostCapacityRecoveryInterval: bug011MacWorkerInterval,
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: socketPath,
		DispatchGate: dispatchGate, QueueWake: worker.Wake,
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	coordinator, err := lifecycle.NewCoordinator(&privateServerShutdown{server: server, worker: worker}, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: macShutdownDrainTimeout, CleanupTimeout: macShutdownCleanupTimeout,
	})
	if err != nil {
		_ = server.Close(context.Background())
		_ = database.Close()
		t.Fatal(err)
	}
	if err := worker.RecoverNonterminalJobs(context.Background()); err != nil {
		shutdownErr := coordinator.Shutdown(context.Background())
		closeErr := database.Close()
		t.Fatalf("recover isolated BUG-011 Mac jobs: %v", errors.Join(err, shutdownErr, closeErr))
	}
	worker.Start(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	if err := testfixture.PublishPhaseResult(reporter, bug011MacStartupResult{Ready: true, Report: report}); err != nil {
		t.Fatal(err)
	}
	serveErrResult := <-serveErr
	shutdownErr := coordinator.Shutdown(context.Background())
	closeErr := database.Close()
	if serveErrResult != nil || shutdownErr != nil || closeErr != nil {
		t.Fatal(errors.Join(serveErrResult, shutdownErr, closeErr))
	}
}

func bug011MacService(databasePath, workspaceRoot string) (*sql.DB, *store.AuthorityStore, *execution.Service, error) {
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		return nil, nil, nil, err
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		return nil, nil, nil, err
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		_ = database.Close()
		return nil, nil, nil, err
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		_ = database.Close()
		return nil, nil, nil, err
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS host", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		_ = database.Close()
		return nil, nil, nil, err
	}
	service, _, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash",
	}, environment)
	if err != nil {
		_ = database.Close()
		return nil, nil, nil, err
	}
	return database, authority, service, nil
}

func bug011MacPrepareFourLostPairsAndQueuedOneOff(t *testing.T, fixture *bug011MacFixture, mismatchFirst, firstHasLiveChild bool) ([]bug011MacLostCommand, bug011MacQueuedOneOff) {
	t.Helper()
	lost := make([]bug011MacLostCommand, 0, store.DefaultRunningCommandLimit)
	for index := 0; index < store.DefaultRunningCommandLimit-1; index++ {
		suffix := fmt.Sprintf("%d", index+1)
		lossScript := "exit 1\n"
		if firstHasLiveChild && index == 0 {
			lossScript = bug011MacLossWithLiveChildScript(filepath.Join(fixture.root, "unproven-child.pid"))
		}
		lost = append(lost, bug011MacCreateLostCommand(t, fixture, suffix, lossScript, mismatchFirst && index == 0, firstHasLiveChild && index == 0))
	}
	if slots, err := fixture.authority.CountLiveCommandSlots(context.Background()); err != nil || slots != store.DefaultRunningCommandLimit-1 {
		t.Fatalf("retained test-owned lost command slots before fourth=%d err=%v, want %d", slots, err, store.DefaultRunningCommandLimit-1)
	}

	fourthSuffix := strconv.Itoa(store.DefaultRunningCommandLimit)
	fourthSession := bug011MacCreateSession(t, fixture, fourthSuffix)
	fixture.releasePath = filepath.Join(fixture.root, "release-fourth-loss")
	fourthInput := bug011MacSubmitIntent(t, fixture, fourthSession, fourthSuffix, bug011MacLossAfterReleaseScript(fixture.releasePath))
	p135MacWaitCommandState(t, fixture.authority, fourthInput.CommandID, domain.CommandStateRunning)
	fourth := bug011MacLostCommand{
		sessionID: fourthSession, commandID: fourthInput.CommandID,
		ownership: bug011MacReadOwnershipRecord(t, fixture.workspaceRoot, fourthSession),
	}
	fixture.lostPIDs[fourth.ownership.record.PID] = struct{}{}
	fixture.fourthCommand = fourth.commandID
	lost = append(lost, fourth)
	if slots, err := fixture.authority.CountLiveCommandSlots(context.Background()); err != nil || slots != store.DefaultRunningCommandLimit {
		t.Fatalf("test-owned slots before queued one-off=%d err=%v, want full capacity %d", slots, err, store.DefaultRunningCommandLimit)
	}

	queued := bug011MacCreateQueuedOneOff(t, fixture)
	fixture.queuedOneOff = true
	bug011MacWaitJob(t, fixture.authority, queued.jobID, store.JobPhaseAwaitingCommand)
	p135MacWaitCommandState(t, fixture.authority, queued.commandID, domain.CommandStateQueued)
	if _, err := os.Stat(queued.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued one-off ran before the fourth loss was released: %v", err)
	}
	return lost, queued
}

func bug011MacCreateLostCommand(t *testing.T, fixture *bug011MacFixture, suffix, script string, mismatchBeforeLoss, hasLiveChild bool) bug011MacLostCommand {
	t.Helper()
	sessionID := bug011MacCreateSession(t, fixture, suffix)
	if mismatchBeforeLoss {
		ownership := bug011MacReadOwnershipRecord(t, fixture.workspaceRoot, sessionID)
		fixture.mutated[ownership.path] = ownership.record
		mismatched := ownership.record
		mismatched.ProcessStartIdentity = "test-mismatched-process-start-identity"
		if err := bug011MacReplaceOwnership(ownership.path, mismatched); err != nil {
			t.Fatalf("replace test-owned ownership marker before loss: %v", err)
		}
	}
	liveChildGroup := 0
	if hasLiveChild {
		liveChildGroup = bug011MacReadOwnershipRecord(t, fixture.workspaceRoot, sessionID).record.ProcessGroupID
	}
	input := bug011MacSubmitIntent(t, fixture, sessionID, suffix, script)
	childPID := 0
	if hasLiveChild {
		childPID = bug011MacReadPIDFile(t, filepath.Join(fixture.root, "unproven-child.pid"))
		// Record it before waiting for the terminal loss. If a fixture error
		// leaves the command running, cleanup can still stop only this known,
		// test-owned PID before the helper is torn down.
		fixture.liveChildPID = childPID
		fixture.liveChildGroup = liveChildGroup
	}
	p135MacWaitCommandState(t, fixture.authority, input.CommandID, domain.CommandStateLost)
	p135MacWaitSessionState(t, fixture.authority, sessionID, domain.SessionStateLost)
	ownership := bug011MacReadOwnershipRecord(t, fixture.workspaceRoot, sessionID)
	bug011MacWaitForZombie(t, ownership.record.PID)
	if err := syscall.Kill(ownership.record.PID, 0); err != nil {
		t.Fatalf("test-owned lost Bash PID %d is not inspectable: %v", ownership.record.PID, err)
	}
	fixture.lostPIDs[ownership.record.PID] = struct{}{}
	lost := bug011MacLostCommand{sessionID: sessionID, commandID: input.CommandID, ownership: ownership}
	if hasLiveChild {
		bug011MacAssertRunnableProcess(t, childPID, ownership.record.ProcessGroupID, 100*time.Millisecond)
		lost.liveChildPID = childPID
	}
	return lost
}

func bug011MacCreateSession(t *testing.T, fixture *bug011MacFixture, suffix string) domain.SessionID {
	t.Helper()
	intent := p060CreateIntent(t, fixture.authority, "intent-bug011-mac-create-"+suffix, "session-bug011-mac-"+suffix, "key-bug011-mac-create-"+suffix)
	p135MacAcceptIntent(t, fixture.client, intent, nil)
	p135MacWaitSessionState(t, fixture.authority, intent.SessionID, domain.SessionStateReady)
	return intent.SessionID
}

func bug011MacSubmitIntent(t *testing.T, fixture *bug011MacFixture, sessionID domain.SessionID, suffix, script string) store.LocalIntentRecord {
	t.Helper()
	input := p060SubmitIntent(t, "intent-bug011-mac-loss-"+suffix, string(sessionID), "command-bug011-mac-loss-"+suffix, "key-bug011-mac-loss-"+suffix, script)
	intent, err := fixture.authority.CreateLocalIntent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	p135MacAcceptIntent(t, fixture.client, intent, intent.IntentOrdinal)
	return intent
}

func bug011MacCreateQueuedOneOff(t *testing.T, fixture *bug011MacFixture) bug011MacQueuedOneOff {
	t.Helper()
	marker := filepath.Join(fixture.root, "queued-one-off-ran")
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		jobID     = "job-bug011-mac-queued"
		sessionID = "session-bug011-mac-queued"
		commandID = "command-bug011-mac-queued"
	)
	script := "test ! -e " + bug011MacShellQuote(marker) + " && printf 'queued_once\\n' > " + bug011MacShellQuote(marker) + "\n"
	payload := map[string]any{
		"operation": "run", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"job_id":           jobID, "session_id": sessionID, "command_id": commandID,
		"script": script, "source": map[string]string{"mode": "empty"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := fixture.authority.CreateLocalIntent(context.Background(), store.LocalIntentCreate{
		IntentID: "intent-bug011-mac-queued", Operation: "run", ResourceID: jobID,
		SessionID: sessionID, CommandID: commandID, JobID: jobID,
		Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(),
		RequestHash: hash, IdempotencyKey: "key-bug011-mac-queued", PayloadJSON: canonical, ScriptBytes: []byte(script),
	})
	if err != nil {
		t.Fatal(err)
	}
	p135MacAcceptIntent(t, fixture.client, intent, intent.IntentOrdinal)
	return bug011MacQueuedOneOff{jobID: jobID, sessionID: sessionID, commandID: commandID, marker: marker}
}

func bug011MacReleaseFourthLoss(t *testing.T, fixture *bug011MacFixture, fourth bug011MacLostCommand) {
	t.Helper()
	if err := bug011MacPublishRelease(fixture.releasePath); err != nil {
		t.Fatal(err)
	}
	p135MacWaitCommandState(t, fixture.authority, fourth.commandID, domain.CommandStateLost)
	p135MacWaitSessionState(t, fixture.authority, fourth.sessionID, domain.SessionStateLost)
	bug011MacWaitForZombie(t, fourth.ownership.record.PID)
}

func bug011MacAssertRetainedUnprovenRecovery(t *testing.T, fixture *bug011MacFixture, lost []bug011MacLostCommand, queued bug011MacQueuedOneOff) {
	t.Helper()
	if slots, err := fixture.authority.CountLiveCommandSlots(context.Background()); err != nil || slots != store.DefaultRunningCommandLimit {
		t.Fatalf("mismatched fixture retained slots=%d err=%v, want %d", slots, err, store.DefaultRunningCommandLimit)
	}
	for _, pair := range lost {
		command, err := fixture.authority.GetCommand(context.Background(), pair.commandID)
		if err != nil || command.State != domain.CommandStateLost {
			t.Fatalf("mismatched fixture lost command %s=%+v err=%v", pair.commandID, command, err)
		}
		bug011MacAssertEventCounts(t, fixture.authority, pair.commandID, 1, 1, "command_lost")
	}
	job, err := fixture.authority.GetJob(context.Background(), queued.jobID)
	if err != nil || job.Phase != store.JobPhaseAwaitingCommand || job.SessionID != queued.sessionID || job.CommandID != queued.commandID {
		t.Fatalf("mismatched fixture queued job=%+v err=%v", job, err)
	}
	command, err := fixture.authority.GetCommand(context.Background(), queued.commandID)
	if err != nil || command.State != domain.CommandStateQueued {
		t.Fatalf("mismatched fixture queued command=%+v err=%v", command, err)
	}
	bug011MacAssertEventCounts(t, fixture.authority, queued.commandID, 0, 0, "command_succeeded")
	if _, err := os.Stat(queued.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched fixture started queued one-off: %v", err)
	}
	first := bug011MacReadOwnershipRecord(t, fixture.workspaceRoot, lost[0].sessionID)
	if first.record.LostRecoveryCleanupConfirmedAt != "" {
		t.Fatal("mismatched test-owned ownership marker has an unexpected cleanup proof")
	}
	bug011MacWaitForZombie(t, lost[0].ownership.record.PID)
	if lost[0].liveChildPID > 0 {
		bug011MacAssertRunnableProcess(t, lost[0].liveChildPID, lost[0].ownership.record.ProcessGroupID, 150*time.Millisecond)
	}
}

func bug011MacRestoreFirstOwnership(t *testing.T, fixture *bug011MacFixture, first bug011MacLostCommand) {
	t.Helper()
	original, ok := fixture.mutated[first.ownership.path]
	if !ok {
		t.Fatal("missing saved test-owned ownership marker for mismatch restoration")
	}
	if err := bug011MacReplaceOwnership(first.ownership.path, original); err != nil {
		t.Fatalf("restore test-owned mismatched ownership marker: %v", err)
	}
	delete(fixture.mutated, first.ownership.path)
}

func bug011MacStopKnownLiveChild(t *testing.T, fixture *bug011MacFixture, first bug011MacLostCommand) {
	t.Helper()
	if first.liveChildPID <= 0 || fixture.liveChildPID != first.liveChildPID {
		t.Fatalf("missing known test-owned live child: command=%+v fixture_pid=%d", first, fixture.liveChildPID)
	}
	bug011MacAssertRunnableProcess(t, first.liveChildPID, first.ownership.record.ProcessGroupID, 100*time.Millisecond)
	if err := bug011MacStopKnownPID(first.liveChildPID, first.ownership.record.ProcessGroupID); err != nil {
		t.Fatalf("stop known test-owned live child %d: %v", first.liveChildPID, err)
	}
	if err := bug011MacWaitForPIDsGone(map[int]struct{}{first.liveChildPID: {}}, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	fixture.liveChildPID = 0
	fixture.liveChildGroup = 0
}

func bug011MacAssertRecoveredQueuedOneOff(t *testing.T, fixture *bug011MacFixture, lost []bug011MacLostCommand, queued bug011MacQueuedOneOff) {
	t.Helper()
	job := bug011MacWaitJob(t, fixture.authority, queued.jobID, store.JobPhaseComplete)
	if job.SessionID != queued.sessionID || job.CommandID != queued.commandID {
		t.Fatalf("recovered queued job changed durable identities: %+v", job)
	}
	command := bug011MacWaitCommand(t, fixture.authority, queued.commandID, domain.CommandStateSucceeded)
	if command.SessionID != queued.sessionID || command.CommandID != queued.commandID {
		t.Fatalf("recovered queued command changed durable identities: %+v", command)
	}
	bug011MacAssertEventCounts(t, fixture.authority, queued.commandID, 1, 1, "command_succeeded")
	marker, err := os.ReadFile(queued.marker)
	if err != nil || string(marker) != "queued_once\n" {
		t.Fatalf("queued one-off marker=%q err=%v", marker, err)
	}
	for _, pair := range lost {
		lostCommand, err := fixture.authority.GetCommand(context.Background(), pair.commandID)
		if err != nil || lostCommand.State != domain.CommandStateLost {
			t.Fatalf("recovered retained command %s=%+v err=%v, want lost", pair.commandID, lostCommand, err)
		}
		bug011MacAssertEventCounts(t, fixture.authority, pair.commandID, 1, 1, "command_lost")
	}
	if err := bug011MacWaitForPIDsGone(fixture.lostPIDs, 4*time.Second); err != nil {
		t.Fatalf("test-owned lost Bash processes were not reaped after recovery: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(fixture.workspaceRoot, ".runner-runtime-ownership")); err == nil && len(entries) != 0 {
		t.Fatalf("ownership records remain after test-owned recovery: %v", entries)
	}
	bug011MacAssertZeroCapacity(t, fixture.authority)
}

func bug011MacAssertZeroCapacity(t *testing.T, authority *store.AuthorityStore) {
	t.Helper()
	activeSessions, err := authority.CountActiveSessions(context.Background())
	if err != nil || activeSessions != 0 {
		t.Fatalf("active test-owned sessions=%d err=%v, want 0", activeSessions, err)
	}
	commandSlots, err := authority.CountLiveCommandSlots(context.Background())
	if err != nil || commandSlots != 0 {
		t.Fatalf("active test-owned command slots=%d err=%v, want 0", commandSlots, err)
	}
	reservations, err := authority.CountLiveSessionReservations(context.Background())
	if err != nil || reservations != 0 {
		t.Fatalf("active test-owned session reservations=%d err=%v, want 0", reservations, err)
	}
	metrics, err := authority.ReadRestartPreflightMetrics(context.Background())
	if err != nil || metrics != (store.RestartPreflightMetrics{}) {
		t.Fatalf("test-owned post-recovery restart metrics=%+v err=%v, want all zero", metrics, err)
	}
	jobs, err := authority.ListNonterminalJobs(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("test-owned resumable jobs=%d err=%v, want 0", len(jobs), err)
	}
}

func bug011MacAssertEventCounts(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID, wantStarted, wantTerminal int, terminalType string) {
	t.Helper()
	events, err := authority.ListCommandEvents(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	started, terminal := 0, 0
	for _, event := range events {
		if event.Type == "command_started" {
			started++
		}
		if event.Type == terminalType {
			terminal++
		}
	}
	if started != wantStarted || terminal != wantTerminal {
		t.Fatalf("command %s event counts started=%d terminal(%s)=%d, want %d/%d", commandID, started, terminalType, terminal, wantStarted, wantTerminal)
	}
}

func bug011MacWaitReady(t *testing.T, process *testfixture.BarrierProcess) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	encoded, err := process.WaitForResult(ctx)
	if err != nil {
		t.Fatalf("wait for isolated BUG-011 Mac helper: %v; output=%s", err, process.Output())
	}
	var result bug011MacStartupResult
	if err := json.Unmarshal(encoded, &result); err != nil || !result.Ready || result.Report.SessionsInspected != 0 {
		t.Fatalf("isolated BUG-011 Mac helper startup=%s result=%+v err=%v", encoded, result, err)
	}
}

func bug011MacWaitJob(t *testing.T, authority *store.AuthorityStore, id domain.JobID, phase store.JobPhase) store.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, err := authority.GetJob(context.Background(), id)
		if err == nil && job.Phase == phase {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := authority.GetJob(context.Background(), id)
	t.Fatalf("test-owned job %s=%+v err=%v, want %s", id, job, err, phase)
	return store.JobRecord{}
}

func bug011MacWaitCommand(t *testing.T, authority *store.AuthorityStore, id domain.CommandID, state domain.CommandState) store.CommandRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		command, err := authority.GetCommand(context.Background(), id)
		if err == nil && command.State == state {
			return command
		}
		time.Sleep(20 * time.Millisecond)
	}
	command, err := authority.GetCommand(context.Background(), id)
	t.Fatalf("test-owned command %s=%+v err=%v, want %s", id, command, err, state)
	return store.CommandRecord{}
}

func bug011MacRuntimeCleanupFailureCount(t *testing.T, authority *store.AuthorityStore, lost bug011MacLostCommand) int {
	t.Helper()
	records, err := authority.ListAuditRecords(context.Background(), 128)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range records {
		if record.Action == audit.ActionRuntimeCleanup && record.Outcome == audit.OutcomeFailed && record.SessionID == lost.sessionID && record.CommandID == lost.commandID && record.ReasonCode == audit.ReasonRuntimeCleanupUnconfirmed {
			count++
		}
	}
	return count
}

func bug011MacWaitForNewRuntimeCleanupFailure(t *testing.T, authority *store.AuthorityStore, lost bug011MacLostCommand, previous int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if count := bug011MacRuntimeCleanupFailureCount(t, authority, lost); count > previous {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("shared worker did not record a new unconfirmed cleanup audit for test-owned lost command %s after %d prior records", lost.commandID, previous)
}

func bug011MacWaitForCommandNotRunning(authority *store.AuthorityStore, id domain.CommandID, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		command, err := authority.GetCommand(context.Background(), id)
		if err != nil {
			return err
		}
		if command.State != domain.CommandStateRunning && command.State != domain.CommandStateCancelling {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("test-owned command %s remained running", id)
}

func bug011MacReadPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(encoded)))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("test-owned child PID=%q err=%v", strings.TrimSpace(string(encoded)), parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("test-owned live-child PID file was not written: %s", path)
	return 0
}

func bug011MacAssertRunnableProcess(t *testing.T, pid, group int, duration time.Duration) {
	t.Helper()
	if pid <= 0 || group <= 0 {
		t.Fatalf("invalid test-owned process identity pid=%d group=%d", pid, group)
	}
	deadline := time.Now().Add(duration)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("test-owned live process %d is not inspectable: %v", pid, err)
		}
		actualGroup, err := syscall.Getpgid(pid)
		if err != nil || actualGroup != group {
			t.Fatalf("test-owned live process %d group=%d err=%v, want %d", pid, actualGroup, err, group)
		}
		output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		state := strings.TrimSpace(string(output))
		if err != nil || state == "" || strings.HasPrefix(state, "Z") {
			t.Fatalf("test-owned live process %d state=%q err=%v, want runnable", pid, state, err)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func bug011MacStopKnownPID(pid, expectedGroup int) error {
	if pid <= 0 || expectedGroup <= 0 {
		return fmt.Errorf("invalid test-owned PID/group %d/%d", pid, expectedGroup)
	}
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	} else if err != nil {
		return err
	}
	actualGroup, err := syscall.Getpgid(pid)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if actualGroup != expectedGroup {
		return fmt.Errorf("test-owned PID %d group changed to %d, want %d; refusing signal", pid, actualGroup, expectedGroup)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func bug011MacWaitForPIDsGone(pids map[int]struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining := make([]string, 0, len(pids))
		for pid := range pids {
			if err := syscall.Kill(pid, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
				remaining = append(remaining, strconv.Itoa(pid))
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	remaining := make([]string, 0, len(pids))
	for pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
			remaining = append(remaining, strconv.Itoa(pid))
		}
	}
	return fmt.Errorf("test-owned PIDs remain after bounded cleanup: %s", strings.Join(remaining, ","))
}

func bug011MacReadOwnershipRecord(t *testing.T, workspaceRoot string, sessionID domain.SessionID) bug011MacOwnership {
	t.Helper()
	directory := filepath.Join(workspaceRoot, ".runner-runtime-ownership")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("test-owned ownership marker %s mode=%s, want regular 0600", path, info.Mode())
		}
		encoded, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record hostruntime.RuntimeOwnershipRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatal(err)
		}
		if record.SessionID == string(sessionID) {
			return bug011MacOwnership{path: path, record: record}
		}
	}
	t.Fatalf("test-owned ownership marker for session %s was not written", sessionID)
	return bug011MacOwnership{}
}

func bug011MacReplaceOwnership(path string, record hostruntime.RuntimeOwnershipRecord) error {
	directory := filepath.Dir(path)
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bug011-owner-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return err
	}
	return nil
}

func bug011MacWaitForZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "Z") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	output, err := exec.Command("/bin/ps", "-o", "pid=,stat=", "-p", strconv.Itoa(pid)).CombinedOutput()
	t.Fatalf("test-owned Bash PID %d did not become a zombie: output=%q err=%v", pid, output, err)
}

func bug011MacPublishRelease(path string) error {
	if path == "" {
		return errors.New("test-owned fourth-loss release path is empty")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func bug011MacLossAfterReleaseScript(releasePath string) string {
	return "while [ ! -f " + bug011MacShellQuote(releasePath) + " ]; do /bin/sleep 0.02; done\nexit 1\n"
}

func bug011MacLossWithLiveChildScript(childPIDPath string) string {
	// The persistent Bash reserves descriptor 3 for its read side and 4 for
	// control frames. Closing both in the background subshell prevents the
	// sentinel from holding the Runner control pipe open after Bash exits.
	return "( exec 3>&- 4>&-; exec /bin/sleep 30 ) </dev/null >/dev/null 2>&1 &\nprintf '%s\\n' \"$!\" > " + bug011MacShellQuote(childPIDPath) + "\nexit 1\n"
}

func bug011MacShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func bug011MacAssertFixtureModes(t *testing.T, fixture *bug011MacFixture) {
	t.Helper()
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{fixture.root, 0o700},
		{fixture.workspaceRoot, 0o700},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatalf("inspect isolated BUG-011 fixture path %s: %v", check.path, err)
		}
		if info.Mode().Perm() != check.mode {
			t.Fatalf("isolated BUG-011 fixture path %s mode=%v, want %o", check.path, info.Mode(), check.mode)
		}
	}
	info, err := os.Lstat(fixture.socketPath)
	if err != nil {
		t.Fatalf("inspect isolated BUG-011 socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("isolated BUG-011 socket mode=%v, want socket 0600", info.Mode())
	}
}
