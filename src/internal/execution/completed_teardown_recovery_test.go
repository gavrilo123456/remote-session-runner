package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func bug024CompletedTeardownFixture(t *testing.T, suffix string) (*Service, *store.AuthorityStore, *p027Runtime, RunJobRequest, RunJobResult) {
	return bug024CompletedTeardownFixtureWithExit(t, suffix, 0)
}

func bug024CompletedTeardownFixtureWithExit(t *testing.T, suffix string, exitCode int) (*Service, *store.AuthorityStore, *p027Runtime, RunJobRequest, RunJobResult) {
	t.Helper()
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug024-" + suffix,
			commandResult: RuntimeCommandResult{Stdout: []byte("executed once\n"), ExitCode: exitCode}},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	request := pCommandlessLostRunJobRequest(t, "bug024-"+suffix)
	result, err := service.RunJob(context.Background(), request)
	wantState := domain.CommandStateSucceeded
	if exitCode != 0 {
		wantState = domain.CommandStateFailed
	}
	if !errors.Is(err, ErrStopUnconfirmed) || result.Job.Phase != store.JobPhaseLost || result.Command.State != wantState || result.Command.ExitCode == nil || *result.Command.ExitCode != exitCode {
		t.Fatalf("seed completed teardown result=%+v err=%v", result, err)
	}
	return service, authority, runtime, request, result
}

func bug024RecoveryRequest(result RunJobResult) []CompletedTeardownRecoveryRequest {
	return []CompletedTeardownRecoveryRequest{{SessionID: result.Session.SessionID, CommandID: result.Command.CommandID}}
}

func TestBUG024CompletedTeardownRecoveryPreservesOutcomeAndNeverReplays(t *testing.T) {
	service, authority, runtime, request, before := bug024CompletedTeardownFixture(t, "preserve")
	ctx := context.Background()
	beforeEvents, err := authority.ListCommandEvents(ctx, before.Command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSlot, err := authority.GetCommandSlot(ctx, before.Command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckCompletedTeardownRecoveryBatch(ctx, bug024RecoveryRequest(before))
	if err != nil || len(checked) != 1 || runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("read-only check=%+v err=%v runtime=%+v", checked, err, runtime)
	}
	results, err := service.RecoverCompletedTeardownRecoveryBatch(ctx, bug024RecoveryRequest(before))
	if err != nil || len(results) != 1 || results[0].Recovery.SessionReservation.ReleasedAt == nil {
		t.Fatalf("recovery=%+v err=%v", results, err)
	}
	after, err := service.ResumeJob(ctx, before.Job.JobID, before.Job.Controller, request)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("historical response changed: before=%+v after=%+v err=%v", before, after, err)
	}
	afterEvents, err := authority.ListCommandEvents(ctx, before.Command.CommandID)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatalf("event prefix changed: err=%v before=%+v after=%+v", err, beforeEvents, afterEvents)
	}
	afterSlot, err := authority.GetCommandSlot(ctx, before.Command.CommandID)
	if err != nil || !reflect.DeepEqual(beforeSlot, afterSlot) {
		t.Fatalf("completed command slot rewritten: err=%v before=%+v after=%+v", err, beforeSlot, afterSlot)
	}
	repeat, err := service.RecoverCompletedTeardownRecoveryBatch(ctx, bug024RecoveryRequest(before))
	if err != nil || len(repeat) != 1 || !repeat[0].Recovery.AlreadyRecovered || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 || runtime.stopCall != 1 {
		t.Fatalf("repeat=%+v err=%v calls=%+v", repeat, err, runtime)
	}
}

func TestBUG024FailedCommandTeardownRecoveryPreservesExit2AndNeverReplays(t *testing.T) {
	service, authority, runtime, request, before := bug024CompletedTeardownFixtureWithExit(t, "failed-exit-2", 2)
	ctx := context.Background()
	beforeEvents, err := authority.ListCommandEvents(ctx, before.Command.CommandID)
	if err != nil || len(beforeEvents) == 0 || beforeEvents[len(beforeEvents)-1].Type != "command_failed" {
		t.Fatalf("failed terminal events=%+v err=%v", beforeEvents, err)
	}
	beforeSlot, err := authority.GetCommandSlot(ctx, before.Command.CommandID)
	if err != nil || beforeSlot.ReleasedAt == nil || beforeSlot.StopConfirmedAt == nil {
		t.Fatalf("failed command slot=%+v err=%v", beforeSlot, err)
	}
	results, err := service.RecoverCompletedTeardownRecoveryBatch(ctx, bug024RecoveryRequest(before))
	if err != nil || len(results) != 1 || results[0].Recovery.SessionReservation.ReleasedAt == nil {
		t.Fatalf("failed-command recovery=%+v err=%v", results, err)
	}
	after, err := service.ResumeJob(ctx, before.Job.JobID, before.Job.Controller, request)
	if err != nil || !reflect.DeepEqual(before, after) || after.Command.State != domain.CommandStateFailed || after.Command.ExitCode == nil || *after.Command.ExitCode != 2 {
		t.Fatalf("historical failed result changed: before=%+v after=%+v err=%v", before, after, err)
	}
	afterEvents, err := authority.ListCommandEvents(ctx, before.Command.CommandID)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatalf("failed event history changed: before=%+v after=%+v err=%v", beforeEvents, afterEvents, err)
	}
	afterSlot, err := authority.GetCommandSlot(ctx, before.Command.CommandID)
	if err != nil || !reflect.DeepEqual(beforeSlot, afterSlot) {
		t.Fatalf("released command slot changed: before=%+v after=%+v err=%v", beforeSlot, afterSlot, err)
	}
	repeat, err := service.RecoverCompletedTeardownRecoveryBatch(ctx, bug024RecoveryRequest(before))
	if err != nil || len(repeat) != 1 || !repeat[0].Recovery.AlreadyRecovered || runtime.commandCall != 1 || runtime.startCall != 1 || runtime.lostRecoveryCall != 1 {
		t.Fatalf("failed recovery repeat=%+v err=%v calls=%+v", repeat, err, runtime)
	}
}

func TestBUG024RemoteQueuedTeardownRecoveryPreservesFailedResultAndNeverReplays(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-bug024-remote-queued", commandResult: RuntimeCommandResult{Stdout: []byte("failed once\n"), ExitCode: 1}},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{CleanupConfirmed: true},
	}
	service, authority, request := bug024RemoteQueuedFixture(t, runtime)
	before, runErr := service.RunJob(context.Background(), request)
	if !errors.Is(runErr, ErrStopUnconfirmed) || before.Job.Phase != store.JobPhaseLost || before.Command.State != domain.CommandStateFailed || before.Command.ExitCode == nil || *before.Command.ExitCode != 1 {
		t.Fatalf("seed remote completed command=%+v err=%v", before, runErr)
	}
	beforeEvents, err := authority.ListCommandEvents(context.Background(), before.Command.CommandID)
	if err != nil || len(beforeEvents) == 0 || beforeEvents[len(beforeEvents)-1].Type != "command_failed" {
		t.Fatalf("remote failed event history=%+v err=%v", beforeEvents, err)
	}
	beforeSlot, err := authority.GetCommandSlot(context.Background(), before.Command.CommandID)
	if err != nil || beforeSlot.StopConfirmedAt == nil || beforeSlot.ReleasedAt == nil {
		t.Fatalf("remote command slot=%+v err=%v", beforeSlot, err)
	}
	results, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before))
	if err != nil || len(results) != 1 || results[0].Recovery.SessionReservation.ReleasedAt == nil || !results[0].Runtime.CleanupConfirmed {
		t.Fatalf("remote teardown recovery=%+v err=%v", results, err)
	}
	after, err := service.ResumeJob(context.Background(), before.Job.JobID, before.Job.Controller, request)
	if err != nil || !reflect.DeepEqual(before, after) || after.Command.State != domain.CommandStateFailed || after.Command.ExitCode == nil || *after.Command.ExitCode != 1 {
		t.Fatalf("remote historical result changed: before=%+v after=%+v err=%v", before, after, err)
	}
	afterEvents, err := authority.ListCommandEvents(context.Background(), before.Command.CommandID)
	if err != nil || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatalf("remote event history changed: before=%+v after=%+v err=%v", beforeEvents, afterEvents, err)
	}
	afterSlot, err := authority.GetCommandSlot(context.Background(), before.Command.CommandID)
	if err != nil || !reflect.DeepEqual(beforeSlot, afterSlot) {
		t.Fatalf("remote command slot changed: before=%+v after=%+v err=%v", beforeSlot, afterSlot, err)
	}
	if runtime.startCall != 1 || runtime.commandCall != 1 || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 {
		t.Fatalf("remote recovery replayed or skipped cleanup: calls=%+v", runtime)
	}
}

func bug024RemoteQueuedFixture(t *testing.T, runtime *p027Runtime) (*Service, *store.AuthorityStore, RunJobRequest) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir()+"/state/remote-completed-teardown.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &p020Clock{now: time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	target := p020Target(t, domain.TargetKindRemote, "sandbox-host")
	controller := p020Controller(t, domain.ControllerTypeQueuedMac)
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name:               "linux-dev",
		HostClass:          "Ubuntu remote worker",
		EffectiveAccount:   "ubuntu",
		AllowedTargets:     []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, clock, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	const script = "printf remote-completed-teardown"
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"sandbox-host"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := RunJobRequest{Acceptance: store.JobAcceptance{
		JobID: domain.JobID("job-bug024-remote-queued"), SessionID: domain.SessionID("sess-bug024-remote-queued"), CommandID: domain.CommandID("cmd-bug024-remote-queued"),
		Controller: controller, IdempotencyKey: "key-bug024-remote-queued", RequestHash: hash, Environment: "linux-dev", Target: target,
		Source: domain.NewEmptySource(), Script: script, CanonicalPayload: canonical, IdempotencyRetention: time.Hour,
	}, MaxActiveSessions: store.DefaultActiveSessionLimit, IdempotencyRetention: time.Hour}
	return service, authority, request
}

func TestBUG024FailedCommandTeardownFinalizationRetryDoesNotReplay(t *testing.T) {
	service, authority, runtime, request, before := bug024CompletedTeardownFixtureWithExit(t, "failed-finalize-retry", 2)
	runtime.lostFinalizeErr = errors.New("fixture marker finalization failure")
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before)); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("initial failed-command finalization=%v", err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), before.Session.SessionID)
	if err != nil || reservation.ReleasedAt == nil || reservation.CleanupConfirmedAt == nil {
		t.Fatalf("released reservation with pending finalization=%+v err=%v", reservation, err)
	}
	pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(context.Background())
	if err != nil || len(pending) != 1 || pending[0].Pair.SessionID != before.Session.SessionID {
		t.Fatalf("pending finalization=%+v err=%v", pending, err)
	}
	runtime.lostFinalizeErr = nil
	results, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before))
	if err != nil || len(results) != 1 || !results[0].Recovery.AlreadyRecovered || results[0].Recovery.FinalizationPending || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 2 || runtime.commandCall != 1 || runtime.startCall != 1 {
		t.Fatalf("failed finalization retry=%+v err=%v calls=%+v", results, err, runtime)
	}
	after, err := service.ResumeJob(context.Background(), before.Job.JobID, before.Job.Controller, request)
	if err != nil || !reflect.DeepEqual(before, after) || after.Command.State != domain.CommandStateFailed || after.Command.ExitCode == nil || *after.Command.ExitCode != 2 {
		t.Fatalf("finalization retry changed failed result: before=%+v after=%+v err=%v", before, after, err)
	}
	if events, err := authority.ListCommandEvents(context.Background(), before.Command.CommandID); err != nil || len(events) == 0 || events[len(events)-1].Type != "command_failed" {
		t.Fatalf("finalization retry changed terminal event: events=%+v err=%v", events, err)
	}
}

func TestBUG024CompletedTeardownRecoveryHoldsReservationWithoutProof(t *testing.T) {
	for _, exitCode := range []int{0, 2} {
		for _, proofErr := range []error{nil, errors.New("fixture ownership inspection failure")} {
			t.Run(fmt.Sprintf("exit-%d/%v", exitCode, proofErr), func(t *testing.T) {
				service, authority, runtime, _, before := bug024CompletedTeardownFixtureWithExit(t, fmt.Sprintf("unconfirmed-%d", exitCode), exitCode)
				runtime.lostRecoveryResult.CleanupConfirmed = false
				runtime.lostRecoveryErr = proofErr
				_, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before))
				if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostFinalizeCall != 0 {
					t.Fatalf("recovery err=%v finalize=%d", err, runtime.lostFinalizeCall)
				}
				reservation, err := authority.GetSessionReservation(context.Background(), before.Session.SessionID)
				if err != nil || reservation.ReleasedAt != nil || reservation.CleanupConfirmedAt != nil {
					t.Fatalf("unconfirmed reservation=%+v err=%v", reservation, err)
				}
			})
		}
	}
}

func TestBUG024CompletedTeardownRecoveryProvesEntireSetBeforeRelease(t *testing.T) {
	service, authority, runtime, _, first := bug024CompletedTeardownFixture(t, "batch-first")
	second, err := service.RunJob(context.Background(), pCommandlessLostRunJobRequest(t, "bug024-batch-second"))
	if !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("seed second completed teardown: %v", err)
	}
	requests := append(bug024RecoveryRequest(first), bug024RecoveryRequest(second)...)
	runtime.lostRecoveryHook = func(context.Context) {
		if runtime.lostRecoveryCall == 2 {
			runtime.lostRecoveryResult.CleanupConfirmed = false
		}
	}
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), requests); !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostFinalizeCall != 0 {
		t.Fatalf("partial proof err=%v finalization calls=%d", err, runtime.lostFinalizeCall)
	}
	for _, result := range []RunJobResult{first, second} {
		reservation, err := authority.GetSessionReservation(context.Background(), result.Session.SessionID)
		if err != nil || reservation.ReleasedAt != nil {
			t.Fatalf("partial proof released reservation=%+v err=%v", reservation, err)
		}
	}
	runtime.lostRecoveryHook = nil
	runtime.lostRecoveryResult.CleanupConfirmed = true
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), requests); err != nil || runtime.commandCall != 2 || runtime.lostFinalizeCall != 2 {
		t.Fatalf("whole-set retry err=%v calls=%+v", err, runtime)
	}
}

func TestBUG024CompletedCommandCannotEnterLostCommandRecovery(t *testing.T) {
	service, _, runtime, _, before := bug024CompletedTeardownFixture(t, "legacy-refusal")
	_, err := service.RecoverLostRuntimeBatch(context.Background(), []LostRuntimeRecoveryRequest{{SessionID: before.Session.SessionID, CommandID: before.Command.CommandID}})
	if !errors.Is(err, ErrLostRuntimeRecoveryIneligible) || runtime.lostRecoveryCall != 0 {
		t.Fatalf("completed command entered lost-command recovery: err=%v calls=%d", err, runtime.lostRecoveryCall)
	}
}

func TestBUG024CompletedTeardownRecoveryRefusesExtraWorkBeforeRuntime(t *testing.T) {
	service, _, runtime, _, before := bug024CompletedTeardownFixture(t, "extra-work")
	if _, err := service.CreateSession(context.Background(), p020Request(t, "sess-bug024-other", "bug024-other", p020Target(t, domain.TargetKindLocal, "mac-workstation"))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before)); !errors.Is(err, ErrLostRuntimeRecoveryIneligible) || runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("extra-work recovery err=%v calls=%+v", err, runtime)
	}
}

func TestBUG024CompletedTeardownRecoveryRetainsProofAfterStoreFailure(t *testing.T) {
	service, authority, runtime, _, before := bug024CompletedTeardownFixture(t, "store-failure")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.lostRecoveryHook = func(context.Context) { cancel() }
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(ctx, bug024RecoveryRequest(before)); err == nil || runtime.lostFinalizeCall != 0 {
		t.Fatalf("store failure err=%v finalize=%d", err, runtime.lostFinalizeCall)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), before.Session.SessionID)
	if err != nil || reservation.ReleasedAt != nil {
		t.Fatalf("store failure reservation=%+v err=%v", reservation, err)
	}
	runtime.lostRecoveryHook = nil
	if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before)); err != nil || runtime.commandCall != 1 {
		t.Fatalf("store retry err=%v command calls=%d", err, runtime.commandCall)
	}
}

func TestBUG024StartupFinalizesReleasedTeardownBeforeOwnershipAudit(t *testing.T) {
	for _, failAgain := range []bool{false, true} {
		t.Run(fmt.Sprint(failAgain), func(t *testing.T) {
			service, authority, runtime, _, before := bug024CompletedTeardownFixture(t, "startup")
			runtime.lostFinalizeErr = errors.New("fixture marker removal failure")
			if _, err := service.RecoverCompletedTeardownRecoveryBatch(context.Background(), bug024RecoveryRequest(before)); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
				t.Fatalf("seed pending finalization=%v", err)
			}
			if !failAgain {
				runtime.lostFinalizeErr = nil
			}
			runtime.ownershipHook = func() error {
				pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(context.Background())
				if err != nil || len(pending) != 0 {
					return fmt.Errorf("pending finalizations before ownership audit: count=%d err=%v", len(pending), err)
				}
				return nil
			}
			_, err := service.ReconcileStartup(context.Background())
			if failAgain {
				if !errors.Is(err, ErrLostRuntimeRecoveryFinalization) || runtime.ownershipAudit != 0 {
					t.Fatalf("unsafe startup err=%v ownership audits=%d", err, runtime.ownershipAudit)
				}
			} else if err != nil || runtime.ownershipAudit != 1 {
				t.Fatalf("startup err=%v ownership audits=%d", err, runtime.ownershipAudit)
			}
			if runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 2 || runtime.commandCall != 1 || runtime.startCall != 1 {
				t.Fatalf("startup replayed runtime or proof: %+v", runtime)
			}
		})
	}
}
