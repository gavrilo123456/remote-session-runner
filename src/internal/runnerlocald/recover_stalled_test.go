package runnerlocald

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
)

type macRecoveryOwnershipAuditor func(context.Context, map[string]struct{}) error

func (f macRecoveryOwnershipAuditor) AuditOwnership(ctx context.Context, attributable map[string]struct{}) error {
	return f(ctx, attributable)
}

func TestParseMacLostRecoveryPairsRejectsAmbiguousInput(t *testing.T) {
	valid := []string{
		"sess-00000000000000000000000000000001:cmd-00000000000000000000000000000001",
		"sess-00000000000000000000000000000002:cmd-00000000000000000000000000000002",
		"sess-00000000000000000000000000000003:cmd-00000000000000000000000000000003",
	}
	pairs, err := parseMacLostRecoveryPairs(valid)
	if err != nil || len(pairs) != 3 {
		t.Fatalf("valid pairs=%+v err=%v", pairs, err)
	}
	for _, values := range [][]string{
		{valid[0], valid[0]},
		{valid[0], "sess-00000000000000000000000000000002:cmd-00000000000000000000000000000001"},
		{"not-a-pair"},
		{"sess-00000000000000000000000000000001:cmd-00000000000000000000000000000001:extra"},
	} {
		if _, err := parseMacLostRecoveryPairs(values); err == nil {
			t.Fatalf("ambiguous pairs %q unexpectedly accepted", values)
		}
	}
}

func TestParseMacLostRecoverySessionsAndCrossSelection(t *testing.T) {
	sessions, err := parseMacLostRecoverySessions([]string{
		"sess-00000000000000000000000000000004",
		"sess-00000000000000000000000000000005",
	})
	if err != nil || len(sessions) != 2 || sessions[0].SessionID != domain.SessionID("sess-00000000000000000000000000000004") {
		t.Fatalf("parsed commandless sessions=%+v err=%v", sessions, err)
	}
	for _, values := range [][]string{{"sess-00000000000000000000000000000004", "sess-00000000000000000000000000000004"}, {""}} {
		if _, err := parseMacLostRecoverySessions(values); err == nil {
			t.Fatalf("invalid commandless sessions %q unexpectedly accepted", values)
		}
	}
	pairs, err := parseMacLostRecoveryPairs([]string{"sess-00000000000000000000000000000004:cmd-00000000000000000000000000000004"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDistinctMacLostRecoverySessions(pairs, sessions); err == nil {
		t.Fatal("pair and commandless selection for the same Mac session unexpectedly accepted")
	}
	if err := validateDistinctMacLostRecoverySessions(pairs, []execution.CommandlessLostRuntimeRecoveryRequest{{SessionID: "sess-00000000000000000000000000000006"}}); err != nil {
		t.Fatalf("distinct pair and commandless Mac selections rejected: %v", err)
	}
}

func TestRunnerLocaldRecoverStalledRequiresExplicitApplyAndSafePair(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--config", "/fixture/mac.yaml", "--lost-pair", "sess-00000000000000000000000000000001:cmd-00000000000000000000000000000001"},
		{"--config", "/fixture/mac.yaml", "--apply", "--lost-pair", "invalid"},
	} {
		var stdout, stderr strings.Builder
		if code := runRecoverStalled(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q, want usage exit 2", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestRunnerLocaldRecoverFailedStartupRequiresExplicitApplyAndExactSession(t *testing.T) {
	valid := "sess-00000000000000000000000000000007"
	for _, args := range [][]string{
		nil,
		{"--config", "/fixture/mac.yaml", "--failed-session", valid},
		{"--config", "/fixture/mac.yaml", "--apply", "--failed-session", valid, "--failed-session", valid},
	} {
		var stdout, stderr strings.Builder
		if code := runRecoverFailedStartup(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q, want usage exit 2", args, code, stdout.String(), stderr.String())
		}
	}
	sessions, err := parseMacFailedStartupRecoverySessions([]string{valid})
	if err != nil || len(sessions) != 1 || sessions[0] != domain.SessionID(valid) {
		t.Fatalf("failed startup sessions=%+v err=%v", sessions, err)
	}
}

func TestRequireRunnerLocaldServicesStoppedRejectsLoadedAgentsAndSockets(t *testing.T) {
	absent := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	loaded := func(string) (bool, error) { return true, nil }
	unloaded := func(string) (bool, error) { return false, nil }
	if err := requireRunnerLocaldServicesStoppedWith(501, unloaded, absent, "/tmp/local-api.sock", "/tmp/locald.sock"); err != nil {
		t.Fatalf("unloaded services with absent sockets: %v", err)
	}
	if err := requireRunnerLocaldServicesStoppedWith(501, loaded, absent, "/tmp/local-api.sock", "/tmp/locald.sock"); err == nil {
		t.Fatal("loaded LaunchAgent unexpectedly accepted")
	}
	fixture, err := os.CreateTemp(t.TempDir(), "socket-present")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	if err := requireRunnerLocaldServicesStoppedWith(501, unloaded, os.Lstat, fixture.Name(), "/tmp/locald.sock"); err == nil {
		t.Fatal("present private socket path unexpectedly accepted")
	}
}

func TestMacStalledRecoveryReleasesOnlyExactCompleteThreePairSet(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	requests := make([]execution.LostRuntimeRecoveryRequest, 0, 3)
	for _, suffix := range []string{"one", "two", "three"} {
		session, command := seedBUG011P4LostPair(t, authority, service, runtime, suffix)
		requests = append(requests, execution.LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	}
	audited := false
	auditor := macRecoveryOwnershipAuditor(func(_ context.Context, attributable map[string]struct{}) error {
		audited = true
		if len(attributable) != len(requests) {
			return errors.New("selected owner set is incomplete")
		}
		return nil
	})
	if err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, requests); err != nil {
		t.Fatalf("exact three-pair preflight: %v", err)
	}
	if !audited {
		t.Fatal("ownership audit was not called")
	}
	recovered, err := service.RecoverLostRuntimeBatch(context.Background(), requests)
	if err != nil || len(recovered) != len(requests) {
		t.Fatalf("recover exact three-pair set=%+v err=%v", recovered, err)
	}
	if err := requireMacStalledRecoveryPostflight(context.Background(), authority); err != nil {
		t.Fatalf("three-pair postflight: %v", err)
	}
	for _, request := range requests {
		command, err := authority.GetCommand(context.Background(), request.CommandID)
		if err != nil || command.State != domain.CommandStateLost {
			t.Fatalf("lost command changed or unavailable: command=%+v err=%v", command, err)
		}
		if runtime.executionCount(request.CommandID) != 0 {
			t.Fatalf("lost command %s was replayed", request.CommandID)
		}
	}
}

func TestMacStalledRecoveryRefusesOmittedOrUnprovenPairWithoutRelease(t *testing.T) {
	for _, fixture := range []struct {
		name        string
		confirmed   bool
		selectCount int
	}{
		{name: "omitted_pair", confirmed: true, selectCount: 2},
		{name: "unproven_cleanup", confirmed: false, selectCount: 3},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			authority, service, runtime := newBUG011P4Service(t, fixture.confirmed)
			requests := make([]execution.LostRuntimeRecoveryRequest, 0, 3)
			for _, suffix := range []string{"one", "two", "three"} {
				session, command := seedBUG011P4LostPair(t, authority, service, runtime, fixture.name+"-"+suffix)
				requests = append(requests, execution.LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
			}
			auditor := macRecoveryOwnershipAuditor(func(context.Context, map[string]struct{}) error { return nil })
			selected := requests[:fixture.selectCount]
			if err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, selected); err != nil {
				if fixture.name == "unproven_cleanup" {
					t.Fatalf("unproven cleanup must pass read-only inventory before runtime proof: %v", err)
				}
			} else if fixture.name == "omitted_pair" {
				t.Fatal("omitted retained pair unexpectedly passed inventory")
			}
			if fixture.name == "unproven_cleanup" {
				if _, err := service.RecoverLostRuntimeBatch(context.Background(), selected); !errors.Is(err, execution.ErrLostRuntimeRecoveryUnconfirmed) {
					t.Fatalf("unproven recovery error=%v, want cleanup-unconfirmed", err)
				}
			}
			slots, err := authority.CountLiveCommandSlots(context.Background())
			if err != nil || slots != 3 {
				t.Fatalf("live slots=%d err=%v, want retained 3", slots, err)
			}
			reservations, err := authority.CountLiveSessionReservations(context.Background())
			if err != nil || reservations != 3 {
				t.Fatalf("live reservations=%d err=%v, want retained 3", reservations, err)
			}
			for _, request := range requests {
				if runtime.executionCount(request.CommandID) != 0 {
					t.Fatalf("lost command %s was replayed", request.CommandID)
				}
			}
		})
	}
}

func TestMacStalledRecoveryRefusesNonterminalJob(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	session, command := seedBUG011P4LostPair(t, authority, service, runtime, "nonterminal")
	_ = acceptBUG011P4QueuedOneOff(t, authority, service)
	auditor := macRecoveryOwnershipAuditor(func(context.Context, map[string]struct{}) error { return nil })
	err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, []execution.LostRuntimeRecoveryRequest{{SessionID: session.SessionID, CommandID: command.CommandID}})
	if err == nil {
		t.Fatal("nonterminal job unexpectedly accepted for offline recovery")
	}
}

func TestMacStalledRecoveryRetriesOnlyExplicitPendingFinalizations(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	requests := make([]execution.LostRuntimeRecoveryRequest, 0, 2)
	for _, suffix := range []string{"pending-one", "pending-two"} {
		session, command := seedBUG011P4LostPair(t, authority, service, runtime, suffix)
		requests = append(requests, execution.LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	}
	runtime.setFinalizationError(errors.New("marker cleanup unavailable"))
	if _, err := service.RecoverLostRuntimeBatch(context.Background(), requests); !errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed pending finalization recovery error=%v, want finalization error", err)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != len(requests) {
		t.Fatalf("pending finalizations=%+v err=%v, want %d", pending, err, len(requests))
	}
	auditor := macRecoveryOwnershipAuditor(func(_ context.Context, attributable map[string]struct{}) error {
		if len(attributable) != len(requests) {
			return errors.New("selected owner set is incomplete")
		}
		return nil
	})
	if err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, requests[:1]); err == nil {
		t.Fatal("omitted pending finalization unexpectedly accepted")
	}
	if err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, requests); err != nil {
		t.Fatalf("exact pending finalization retry preflight: %v", err)
	}
	runtime.setFinalizationError(nil)
	recovered, err := service.RecoverLostRuntimeBatch(context.Background(), requests)
	if err != nil || len(recovered) != len(requests) {
		t.Fatalf("retry pending finalizations=%+v err=%v", recovered, err)
	}
	if err := requireMacStalledRecoveryPostflight(context.Background(), authority); err != nil {
		t.Fatalf("postflight after pending finalization retry: %v", err)
	}
	if runtime.recoveryCount() != len(requests) || runtime.finalizationCount() != 2*len(requests) {
		t.Fatalf("reconcile/finalize counts=%d/%d, want %d/%d", runtime.recoveryCount(), runtime.finalizationCount(), len(requests), 2*len(requests))
	}
	for _, request := range requests {
		if runtime.executionCount(request.CommandID) != 0 {
			t.Fatalf("lost command %s was replayed", request.CommandID)
		}
	}
	if err := requireMacStalledRecoveryInventory(context.Background(), authority, service, auditor, requests[:1]); err == nil {
		t.Fatal("historical released pair without pending finalization unexpectedly accepted")
	}
}

func TestRunnerLocaldLifecycleLockIsExclusiveAndRetryable(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunnerLocaldLifecycleLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRunnerLocaldLifecycleLock(root); !errors.Is(err, ErrRunnerLocaldLifecycleLockHeld) {
		t.Fatalf("second lifecycle lock error=%v, want held", err)
	}
	release()
	retryRelease, err := acquireRunnerLocaldLifecycleLock(root)
	if err != nil {
		t.Fatal(err)
	}
	retryRelease()
}
