package runnerlocald

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

type scopedStartupRuntime struct {
	*bug011P4Runtime
	adapter *hostruntime.MacProcessAdapter
	target  domain.SessionID
	audits  int
}

func (r *scopedStartupRuntime) AuditOwnership(ctx context.Context, attributable map[string]struct{}) error {
	r.audits++
	if len(attributable) != 8 {
		return fmt.Errorf("expected eight preserved attributions, got %d", len(attributable))
	}
	if _, selectedStillHeld := attributable[string(r.target)]; selectedStillHeld {
		return fmt.Errorf("released target is still attributable")
	}
	return r.adapter.AuditOwnership(ctx, attributable)
}

// This is a native Mac startup fixture, not a production stop/start. It joins
// the new target-only store/finalization with the unchanged executor's actual
// startup reconciliation and native marker audit. It never starts a process.
func TestScopedTeardownMacStartupPreservesEightKnownLostOwners(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native Mac ownership/startup audit; portable store/runtime fixtures run separately")
	}
	ctx := context.Background()
	authority, seedService, fake := newBUG011P4Service(t, true)
	for index := 0; index < 7; index++ {
		fake.generation = fmt.Sprintf("scoped-peer-generation-%d", index)
		seedBUG024MacCompletedTeardown(t, authority, seedService, fake, fmt.Sprintf("scoped-peer-%d", index))
	}
	fake.generation = "scoped-peer-lost-generation"
	seedBUG011P4LostPair(t, authority, seedService, fake, "scoped-peer-held-slot")
	// The queue fixture invokes the executor directly. Record the ingress
	// delivery already demonstrated by its created/lost session, rather than
	// leave a genuinely unprocessed intent in this idle-authority fixture.
	for _, next := range []store.LocalIntentDeliveryState{store.LocalIntentDispatching, store.LocalIntentAccepted} {
		if _, err := authority.TransitionLocalIntent(ctx, "intent-bug011-p4-create-lost-scoped-peer-held-slot", next, "fixture_executor_observed"); err != nil {
			t.Fatal(err)
		}
	}
	fake.generation = "scoped-target-generation"
	target := seedBUG024MacCompletedTeardown(t, authority, seedService, fake, "scoped-target")
	pair := store.CompletedTeardownRecoveryPair{SessionID: target.SessionID, CommandID: target.CommandID}
	plan, err := authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, pair)
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(base, "workspaces")
	owners := filepath.Join(workspaceRoot, ".runner-runtime-ownership")
	if err := os.MkdirAll(owners, 0o700); err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	var selectedWorkspace, selectedMarker string
	peerHashes := make(map[string][32]byte)
	sessions := make([]hostruntime.MacScopedTeardownSession, 0, len(plan.Sessions))
	for index, session := range plan.Sessions {
		workspace := filepath.Join(workspaceRoot, fmt.Sprintf("owned-%d", index))
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		// Deliberately impossible Darwin PIDs: startup must only audit these
		// terminal attributions and must not inspect, signal or reattach them.
		pid := 2_000_000_000 + index
		record := hostruntime.RuntimeOwnershipRecord{
			Version: 1, HostOS: "darwin", SessionID: string(session.SessionID), Generation: session.RuntimeGeneration,
			Workspace: workspace, OwnedWorkspace: true, PID: pid, ProcessGroupID: pid,
			UID: os.Getuid(), Username: account.Username, Command: "/bin/bash", ProcessStartIdentity: fmt.Sprintf("10000000000000%d", index),
		}
		if session.SessionID == pair.SessionID {
			// Fixture-only prior proof: production obtains this through the
			// separately tested no-signal native exit boundary before release.
			record.LostRecoveryCleanupConfirmedAt = time.Now().UTC().Format(time.RFC3339Nano)
			selectedWorkspace = workspace
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		name := sha256.Sum256([]byte(record.SessionID))
		marker := filepath.Join(owners, hex.EncodeToString(name[:])+".json")
		if err := os.WriteFile(marker, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if session.SessionID == pair.SessionID {
			selectedMarker = marker
		} else {
			peerHashes[marker] = sha256.Sum256(raw)
		}
		sessions = append(sessions, hostruntime.MacScopedTeardownSession{
			SessionID: record.SessionID, Generation: record.Generation, SourceMode: string(session.Source.Mode()), SourcePath: session.Source.Path(),
		})
	}
	if err := authority.ConfirmScopedCompletedTeardownRecovery(ctx, pair, plan.PreservedInventorySHA256); err != nil {
		t.Fatal(err)
	}
	snapshot, err := hostruntime.ReadMacScopedTeardownOwnership(ctx, workspaceRoot, string(pair.SessionID), sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Finalize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, pair, plan.PreservedInventorySHA256); err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewMacProcessAdapter(hostruntime.MacRuntimeOptions{Account: account.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	observer := &scopedStartupRuntime{bug011P4Runtime: fake, adapter: adapter, target: pair.SessionID}
	targetRoute, _ := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{targetRoute}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits()})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	started, err := execution.NewExecutionService(authority, observer, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := started.ReconcileStartup(ctx)
	if err != nil || report.SessionsInspected != 0 || observer.audits != 1 || fake.recoveryCount() != 0 || fake.finalizationCount() != 0 {
		t.Fatalf("startup changed terminal retained work: report=%+v audits=%d err=%v", report, observer.audits, err)
	}
	after, err := authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, pair)
	if err != nil || after.PreservedInventorySHA256 != plan.PreservedInventorySHA256 || !after.Recovery.AlreadyRecovered || after.Recovery.FinalizationPending {
		t.Fatalf("startup changed the preserved authority: %+v err=%v", after, err)
	}
	if count, err := authority.CountLiveSessionReservations(ctx); err != nil || count != 8 {
		t.Fatalf("preserved reservations=%d err=%v", count, err)
	}
	if count, err := authority.CountLiveCommandSlots(ctx); err != nil || count != 1 {
		t.Fatalf("preserved command slots=%d err=%v", count, err)
	}
	for marker, expected := range peerHashes {
		raw, err := os.ReadFile(marker)
		info, statErr := os.Lstat(marker)
		if err != nil || statErr != nil || sha256.Sum256(raw) != expected || info.Mode().Perm() != 0o600 {
			t.Fatal("startup changed a preserved owner marker")
		}
	}
	for _, path := range []string{selectedMarker, selectedWorkspace} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("finalized target reappeared: %s err=%v", path, err)
		}
	}
}
