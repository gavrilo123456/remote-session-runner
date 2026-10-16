package execution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func bug024CompletedTeardownFixture(t *testing.T, suffix string) (*Service, *store.AuthorityStore, *p027Runtime, RunJobRequest, RunJobResult) {
	t.Helper()
	runtime := &p027Runtime{
		p020FakeRuntime: p020FakeRuntime{generation: "generation-bug024-" + suffix,
			commandResult: RuntimeCommandResult{Stdout: []byte("executed once\n"), ExitCode: 0}},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	request := pCommandlessLostRunJobRequest(t, "bug024-"+suffix)
	result, err := service.RunJob(context.Background(), request)
	if !errors.Is(err, ErrStopUnconfirmed) || result.Job.Phase != store.JobPhaseLost || result.Command.State != domain.CommandStateSucceeded {
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

func TestBUG024CompletedTeardownRecoveryHoldsReservationWithoutProof(t *testing.T) {
	for _, proofErr := range []error{nil, errors.New("fixture ownership inspection failure")} {
		t.Run(fmt.Sprint(proofErr), func(t *testing.T) {
			service, authority, runtime, _, before := bug024CompletedTeardownFixture(t, "unconfirmed")
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
