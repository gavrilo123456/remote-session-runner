//go:build darwin && cgo

package runnerlocald

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

func TestMacSessionRuntimeRebuildQueuedOneOffReplacesGoneShellSameGeneration(t *testing.T) {
	fixture := newControlledRestartMacFixture(t, "same-generation")
	if err := fixture.shell.Close(); err != nil {
		t.Fatalf("close prior shell: %v", err)
	}
	controlledRestartWaitForMacGroupGone(t, fixture.record.ProcessGroupID)

	freshAdapter := newControlledRestartMacAdapter(t, fixture.workspaceRoot)
	freshRuntime, err := NewMacSessionRuntime(freshAdapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = freshAdapter.Cleanup(string(fixture.session.SessionID)) })

	var seam ControlledRestartRehydrator = freshRuntime
	started, err := seam.RebuildQueuedOneOff(context.Background(), fixture.session)
	if err != nil || started.RuntimeGeneration != fixture.session.RuntimeGeneration {
		t.Fatalf("controlled restart rebuild = %+v err=%v, want durable generation %q", started, err, fixture.session.RuntimeGeneration)
	}
	newRecord, err := freshAdapter.Inspect(string(fixture.session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if newRecord.ProcessGroupID != newRecord.PID || newRecord.ProcessStartIdentity == "" {
		t.Fatalf("replacement Mac shell identity = %+v", newRecord)
	}
	if newRecord.PID == fixture.record.PID && newRecord.ProcessStartIdentity == fixture.record.ProcessStartIdentity {
		t.Fatalf("replacement shell reused prior process identity: old=%+v new=%+v", fixture.record, newRecord)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prior owned workspace remains after exact reconciliation: %v", err)
	}
	if _, err := freshAdapter.Shell(string(fixture.session.SessionID)); err != nil {
		t.Fatalf("replacement shell is unavailable: %v", err)
	}
}

func TestMacSessionRuntimeRebuildQueuedOneOffRejectsNonEmptySourceBeforeCleanup(t *testing.T) {
	fixture := newControlledRestartMacFixture(t, "reject-source")
	source, err := domain.NewLocalWorktreeSource(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.Source = source

	freshRuntime, err := NewMacSessionRuntime(newControlledRestartMacAdapter(t, fixture.workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freshRuntime.RebuildQueuedOneOff(context.Background(), fixture.session); !errors.Is(err, ErrControlledRestartRehydration) {
		t.Fatalf("non-empty source rebuild error = %v, want controlled restart rejection", err)
	}
	if _, err := fixture.adapter.Inspect(string(fixture.session.SessionID)); err != nil {
		t.Fatalf("non-empty source rejection touched prior shell: %v", err)
	}
}

func TestMacSessionRuntimeRebuildQueuedOneOffRejectsGenerationMismatchBeforeCleanup(t *testing.T) {
	fixture := newControlledRestartMacFixture(t, "reject-generation")
	fixture.session.RuntimeGeneration = "different-generation"

	freshRuntime, err := NewMacSessionRuntime(newControlledRestartMacAdapter(t, fixture.workspaceRoot))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freshRuntime.RebuildQueuedOneOff(context.Background(), fixture.session); !errors.Is(err, ErrControlledRestartRehydration) {
		t.Fatalf("generation mismatch rebuild error = %v, want controlled restart rejection", err)
	}
	if _, err := fixture.adapter.Inspect(string(fixture.session.SessionID)); err != nil {
		t.Fatalf("generation mismatch rejection touched prior shell: %v", err)
	}
}

func TestMacSessionRuntimeRebuildQueuedOneOffRejectsDetachedZombie(t *testing.T) {
	fixture := newControlledRestartMacFixture(t, "reject-zombie")
	if _, err := fixture.shell.RunScript(context.Background(), "controlled-restart-detached-zombie", []byte("exit 1\n")); !errors.Is(err, hostruntime.ErrPersistentShellExited) {
		t.Fatalf("explicit exit error = %v, want persistent-shell exit", err)
	}
	if err := syscall.Kill(fixture.record.PID, 0); err != nil {
		t.Fatalf("detached zombie is unexpectedly absent before rebuild: %v", err)
	}

	freshAdapter := newControlledRestartMacAdapter(t, fixture.workspaceRoot)
	freshRuntime, err := NewMacSessionRuntime(freshAdapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freshRuntime.RebuildQueuedOneOff(context.Background(), fixture.session); !errors.Is(err, ErrControlledRestartRehydration) {
		t.Fatalf("detached zombie rebuild error = %v, want controlled restart rejection", err)
	}
	if _, err := freshAdapter.Shell(string(fixture.session.SessionID)); !errors.Is(err, hostruntime.ErrMacRuntimeSession) {
		t.Fatalf("detached zombie rebuild created a replacement shell: %v", err)
	}
	if err := syscall.Kill(fixture.record.PID, 0); err != nil {
		t.Fatalf("detached zombie was not retained after rejected rebuild: %v", err)
	}
}

func TestMacSessionRuntimeRebuildQueuedOneOffRetainsOwnerAfterPrepareFailureThenRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission fixture cannot force Mac preparation failure as root")
	}
	fixture := newControlledRestartMacFixture(t, "prepare-failure-retry")
	if err := fixture.shell.Close(); err != nil {
		t.Fatalf("close prior shell: %v", err)
	}
	controlledRestartWaitForMacGroupGone(t, fixture.record.ProcessGroupID)
	prior := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID))

	adapter := newControlledRestartMacAdapter(t, fixture.workspaceRoot)
	runtime, err := NewMacSessionRuntime(adapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Cleanup(string(fixture.session.SessionID)) })
	if err := os.Chmod(fixture.workspaceRoot, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(fixture.workspaceRoot, 0o700) }()
	if _, err := runtime.RebuildQueuedOneOff(context.Background(), fixture.session); !errors.Is(err, ErrControlledRestartRehydration) {
		t.Fatalf("prepare-failure rebuild error = %v, want controlled restart error", err)
	}
	if err := os.Chmod(fixture.workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID)); got != prior {
		t.Fatalf("prepare failure changed durable owner: got=%+v prior=%+v", got, prior)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); err != nil {
		t.Fatalf("prepare failure removed prior workspace: %v", err)
	}

	started, err := runtime.RebuildQueuedOneOff(context.Background(), fixture.session)
	if err != nil || started.RuntimeGeneration != fixture.session.RuntimeGeneration {
		t.Fatalf("retry after prepare failure = %+v err=%v", started, err)
	}
}

func TestMacSessionRuntimeRebuildQueuedOneOffRetainsOwnerAfterStartFailureAndFreshRetry(t *testing.T) {
	fixture := newControlledRestartMacFixture(t, "start-failure-fresh-retry")
	if err := fixture.shell.Close(); err != nil {
		t.Fatalf("close prior shell: %v", err)
	}
	controlledRestartWaitForMacGroupGone(t, fixture.record.ProcessGroupID)
	prior := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID))

	failedAdapter := newControlledRestartMacAdapterWithShell(t, fixture.workspaceRoot, "/no/such/runner-bash")
	failedRuntime, err := NewMacSessionRuntime(failedAdapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failedRuntime.RebuildQueuedOneOff(context.Background(), fixture.session); !errors.Is(err, ErrControlledRestartRehydration) {
		t.Fatalf("start-failure rebuild error = %v, want controlled restart error", err)
	}
	if got := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID)); got != prior {
		t.Fatalf("start failure changed durable owner: got=%+v prior=%+v", got, prior)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); err != nil {
		t.Fatalf("start failure removed prior workspace: %v", err)
	}
	if _, err := failedAdapter.Shell(string(fixture.session.SessionID)); !errors.Is(err, hostruntime.ErrMacRuntimeSession) {
		t.Fatalf("start failure retained replacement shell: %v", err)
	}

	freshAdapter := newControlledRestartMacAdapter(t, fixture.workspaceRoot)
	freshRuntime, err := NewMacSessionRuntime(freshAdapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = freshAdapter.Cleanup(string(fixture.session.SessionID)) })
	started, err := freshRuntime.RebuildQueuedOneOff(context.Background(), fixture.session)
	if err != nil || started.RuntimeGeneration != fixture.session.RuntimeGeneration {
		t.Fatalf("fresh retry after start failure = %+v err=%v", started, err)
	}
	current := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID))
	if current.Generation != prior.Generation || current.PID == prior.PID && current.ProcessStartIdentity == prior.ProcessStartIdentity {
		t.Fatalf("fresh retry did not publish replacement owner: current=%+v prior=%+v", current, prior)
	}
}

func TestMacSessionRuntimeRebuildQueuedOneOffFreshRetriesAfterReplacementStopsBeforeClaim(t *testing.T) {
	fixture := newControlledRestartMacFixture(t, "replacement-stops-before-claim")
	if err := fixture.shell.Close(); err != nil {
		t.Fatalf("close prior shell: %v", err)
	}
	controlledRestartWaitForMacGroupGone(t, fixture.record.ProcessGroupID)

	firstAdapter := newControlledRestartMacAdapter(t, fixture.workspaceRoot)
	firstRuntime, err := NewMacSessionRuntime(firstAdapter)
	if err != nil {
		t.Fatal(err)
	}
	firstStarted, err := firstRuntime.RebuildQueuedOneOff(context.Background(), fixture.session)
	if err != nil || firstStarted.RuntimeGeneration != fixture.session.RuntimeGeneration {
		t.Fatalf("first replacement = %+v err=%v", firstStarted, err)
	}
	firstOwner := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID))
	firstShell, err := firstAdapter.Shell(string(fixture.session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	// Model the replacement executor stopping before the durable scheduler has
	// claimed the queued command. Do not call Cleanup: that would intentionally
	// remove the marker rather than exercising the crash-retry handoff.
	if err := firstShell.Close(); err != nil {
		t.Fatalf("stop replacement before scheduler claim: %v", err)
	}
	controlledRestartWaitForMacGroupGone(t, firstOwner.ProcessGroupID)

	secondAdapter := newControlledRestartMacAdapter(t, fixture.workspaceRoot)
	secondRuntime, err := NewMacSessionRuntime(secondAdapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondAdapter.Cleanup(string(fixture.session.SessionID)) })
	secondStarted, err := secondRuntime.RebuildQueuedOneOff(context.Background(), fixture.session)
	if err != nil || secondStarted.RuntimeGeneration != fixture.session.RuntimeGeneration {
		t.Fatalf("fresh retry before scheduler claim = %+v err=%v", secondStarted, err)
	}
	secondOwner := controlledRestartReadMacOwner(t, fixture.workspaceRoot, string(fixture.session.SessionID))
	if secondOwner.Generation != firstOwner.Generation || secondOwner.PID == firstOwner.PID && secondOwner.ProcessStartIdentity == firstOwner.ProcessStartIdentity {
		t.Fatalf("fresh retry did not replace stopped replacement owner: second=%+v first=%+v", secondOwner, firstOwner)
	}
	if fixture.session.State != domain.SessionStateReady || fixture.session.RuntimeGeneration != secondStarted.RuntimeGeneration {
		t.Fatalf("rebuild changed durable queued-session identity fixture: %+v", fixture.session)
	}
}

type controlledRestartMacFixture struct {
	adapter       *hostruntime.MacProcessAdapter
	workspaceRoot string
	prepared      hostruntime.MacPrepared
	shell         *hostruntime.PersistentShell
	record        hostruntime.MacProcessRecord
	session       store.SessionRecord
}

func newControlledRestartMacFixture(t *testing.T, suffix string) *controlledRestartMacFixture {
	t.Helper()
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := newControlledRestartMacAdapter(t, workspaceRoot)
	sessionID, err := domain.NewSessionID("session-controlled-restart-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	generation := "generation-controlled-restart-" + suffix
	prepared, err := adapter.Prepare(context.Background(), string(sessionID), generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	record, err := adapter.Inspect(string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &controlledRestartMacFixture{
		adapter:       adapter,
		workspaceRoot: workspaceRoot,
		prepared:      prepared,
		shell:         shell,
		record:        record,
		session: store.SessionRecord{
			SessionID:         sessionID,
			Target:            target,
			Source:            domain.NewEmptySource(),
			RuntimeGeneration: generation,
			State:             domain.SessionStateReady,
		},
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-record.ProcessGroupID, syscall.SIGKILL)
		_ = shell.Close()
	})
	return fixture
}

func newControlledRestartMacAdapter(t *testing.T, workspaceRoot string) *hostruntime.MacProcessAdapter {
	return newControlledRestartMacAdapterWithShell(t, workspaceRoot, "/bin/bash")
}

func newControlledRestartMacAdapterWithShell(t *testing.T, workspaceRoot, shellPath string) *hostruntime.MacProcessAdapter {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewMacProcessAdapter(hostruntime.MacRuntimeOptions{
		Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: shellPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func controlledRestartReadMacOwner(t *testing.T, workspaceRoot, sessionID string) hostruntime.RuntimeOwnershipRecord {
	t.Helper()
	// The owner marker is intentionally opaque outside the runtime package.
	// Reopen it only through the adapter-independent JSON file here to prove
	// exact durability across a fresh adapter boundary.
	digest := sha256.Sum256([]byte(sessionID))
	path := filepath.Join(workspaceRoot, ".runner-runtime-ownership", hex.EncodeToString(digest[:])+".json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record hostruntime.RuntimeOwnershipRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func controlledRestartWaitForMacGroupGone(t *testing.T, processGroupID int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(-processGroupID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Mac process group %d did not disappear", processGroupID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
