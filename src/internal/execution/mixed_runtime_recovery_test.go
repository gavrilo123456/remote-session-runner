package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestRecoverMixedRuntimeRecoverySetPreservesFailedResultNeverReplaysAndRetriesFinalization(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-mixed-recovery", cancelResult: RuntimeCommandStopResult{}, commandResult: RuntimeCommandResult{Stdout: []byte("ordinary failure\n"), ExitCode: 2}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-mixed-recovery", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-mixed-recovery", CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	lostSession, lostCommand := pRecoveryLostPair(t, service, authority, "mixed-lost")
	failedRequest := pCommandlessLostRunJobRequest(t, "mixed-failed")
	failedBefore, failedErr := service.RunJob(context.Background(), failedRequest)
	if !errors.Is(failedErr, ErrStopUnconfirmed) || failedBefore.Command.State != domain.CommandStateFailed || failedBefore.Command.ExitCode == nil || *failedBefore.Command.ExitCode != 2 {
		t.Fatalf("seed ordinary failed command=%+v err=%v", failedBefore, failedErr)
	}
	failedEvents, err := authority.ListCommandEvents(context.Background(), failedBefore.Command.CommandID)
	if err != nil || len(failedEvents) == 0 || failedEvents[len(failedEvents)-1].Type != "command_failed" {
		t.Fatalf("failed command events=%+v err=%v", failedEvents, err)
	}
	lostEvents, err := authority.ListCommandEvents(context.Background(), lostCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	startsBefore, executesBefore := runtime.startCall, runtime.commandCall
	lostRequests := []LostRuntimeRecoveryRequest{{SessionID: lostSession.SessionID, CommandID: lostCommand.CommandID}}
	teardownRequests := []CompletedTeardownRecoveryRequest{{SessionID: failedBefore.Session.SessionID, CommandID: failedBefore.Command.CommandID}}
	checkedLost, checkedTeardown, err := service.CheckMixedRuntimeRecoverySet(context.Background(), lostRequests, teardownRequests)
	if err != nil || len(checkedLost) != 1 || len(checkedTeardown) != 1 || runtime.lostRecoveryCall != 0 {
		t.Fatalf("mixed read-only check lost=%+v teardown=%+v err=%v runtime=%+v", checkedLost, checkedTeardown, err, runtime)
	}

	runtime.lostFinalizeErr = errors.New("fixture finalization failure")
	_, _, err = service.RecoverMixedRuntimeRecoverySet(context.Background(), lostRequests, teardownRequests)
	if !errors.Is(err, ErrLostRuntimeRecoveryFinalization) || runtime.lostRecoveryCall != 2 || runtime.lostFinalizeCall != 2 || runtime.startCall != startsBefore || runtime.commandCall != executesBefore {
		t.Fatalf("initial mixed recovery err=%v runtime=%+v; expected two proofs, two finalization attempts, and no replay", err, runtime)
	}
	for _, id := range []domain.SessionID{lostSession.SessionID, failedBefore.Session.SessionID} {
		reservation, readErr := authority.GetSessionReservation(context.Background(), id)
		if readErr != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
			t.Fatalf("reservation %s=%+v err=%v; capacity should be atomically released after all proofs", id, reservation, readErr)
		}
	}
	if pending, pendingErr := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); pendingErr != nil || len(pending) != 1 {
		t.Fatalf("pending lost finalizations=%+v err=%v", pending, pendingErr)
	}
	if pending, pendingErr := authority.ListPendingCompletedTeardownRecoveryFinalizations(context.Background()); pendingErr != nil || len(pending) != 1 {
		t.Fatalf("pending teardown finalizations=%+v err=%v", pending, pendingErr)
	}

	runtime.lostFinalizeErr = nil
	gotLost, gotTeardown, err := service.RecoverMixedRuntimeRecoverySet(context.Background(), lostRequests, teardownRequests)
	if err != nil || len(gotLost) != 1 || len(gotTeardown) != 1 || gotLost[0].SessionReservation.ReleasedAt == nil || gotTeardown[0].Recovery.SessionReservation.ReleasedAt == nil {
		t.Fatalf("mixed finalization retry lost=%+v teardown=%+v err=%v", gotLost, gotTeardown, err)
	}
	if runtime.lostRecoveryCall != 2 || runtime.lostFinalizeCall != 4 || runtime.startCall != startsBefore || runtime.commandCall != executesBefore {
		t.Fatalf("retry must only finalize markers, never re-prove or replay: runtime=%+v", runtime)
	}
	if pending, pendingErr := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); pendingErr != nil || len(pending) != 0 {
		t.Fatalf("lost finalizations remain=%+v err=%v", pending, pendingErr)
	}
	if pending, pendingErr := authority.ListPendingCompletedTeardownRecoveryFinalizations(context.Background()); pendingErr != nil || len(pending) != 0 {
		t.Fatalf("teardown finalizations remain=%+v err=%v", pending, pendingErr)
	}
	failedAfter, err := service.ResumeJob(context.Background(), failedRequest.Acceptance.JobID, failedRequest.Acceptance.Controller, failedRequest)
	if err != nil || !reflect.DeepEqual(failedBefore, failedAfter) || failedAfter.Command.State != domain.CommandStateFailed || failedAfter.Command.ExitCode == nil || *failedAfter.Command.ExitCode != 2 {
		t.Fatalf("ordinary failed result changed: before=%+v after=%+v err=%v", failedBefore, failedAfter, err)
	}
	failedEventsAfter, err := authority.ListCommandEvents(context.Background(), failedBefore.Command.CommandID)
	if err != nil || !reflect.DeepEqual(failedEvents, failedEventsAfter) {
		t.Fatalf("failed terminal event history changed: before=%+v after=%+v err=%v", failedEvents, failedEventsAfter, err)
	}
	lostEventsAfter, err := authority.ListCommandEvents(context.Background(), lostCommand.CommandID)
	if err != nil || !reflect.DeepEqual(lostEvents, lostEventsAfter) {
		t.Fatalf("lost terminal event history changed: before=%+v after=%+v err=%v", lostEvents, lostEventsAfter, err)
	}
}

func TestCheckMixedRuntimeRecoverySetRejectsUnselectedWorkBeforeRuntimeAction(t *testing.T) {
	t.Run("unselected retained session reservation", func(t *testing.T) {
		runtime := &p027Runtime{
			p020FakeRuntime:    p020FakeRuntime{generation: "generation-mixed-extra", cancelResult: RuntimeCommandStopResult{}, commandResult: RuntimeCommandResult{ExitCode: 2}},
			lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: true},
		}
		service, authority := newP027Service(t, runtime)
		lostSession, lostCommand := pRecoveryLostPair(t, service, authority, "mixed-extra-lost")
		failed, err := service.RunJob(context.Background(), pCommandlessLostRunJobRequest(t, "mixed-extra-failed"))
		if !errors.Is(err, ErrStopUnconfirmed) {
			t.Fatalf("seed failed result err=%v", err)
		}
		if _, err := service.CreateSession(context.Background(), p020Request(t, "session-mixed-extra", "key-mixed-extra", p020Target(t, domain.TargetKindLocal, "mac-workstation"))); err != nil {
			t.Fatal(err)
		}
		_, _, err = service.RecoverMixedRuntimeRecoverySet(context.Background(),
			[]LostRuntimeRecoveryRequest{{SessionID: lostSession.SessionID, CommandID: lostCommand.CommandID}},
			[]CompletedTeardownRecoveryRequest{{SessionID: failed.Session.SessionID, CommandID: failed.Command.CommandID}},
		)
		if !errors.Is(err, ErrLostRuntimeRecoveryIneligible) || runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 {
			t.Fatalf("extra live work err=%v runtime=%+v; expected refusal before cleanup", err, runtime)
		}
	})
}

func TestRecoverMixedRuntimeRecoverySetUnconfirmedCleanupReleasesNothing(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-mixed-unconfirmed", cancelResult: RuntimeCommandStopResult{}, commandResult: RuntimeCommandResult{ExitCode: 2}},
		lostRecoveryResult: RuntimeReconcileResult{CleanupConfirmed: false},
	}
	service, authority := newP027Service(t, runtime)
	lostSession, lostCommand := pRecoveryLostPair(t, service, authority, "mixed-unconfirmed-lost")
	failed, err := service.RunJob(context.Background(), pCommandlessLostRunJobRequest(t, "mixed-unconfirmed-failed"))
	if !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("seed failed result err=%v", err)
	}
	_, _, err = service.RecoverMixedRuntimeRecoverySet(context.Background(),
		[]LostRuntimeRecoveryRequest{{SessionID: lostSession.SessionID, CommandID: lostCommand.CommandID}},
		[]CompletedTeardownRecoveryRequest{{SessionID: failed.Session.SessionID, CommandID: failed.Command.CommandID}},
	)
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("unconfirmed mixed cleanup err=%v runtime=%+v", err, runtime)
	}
	for _, id := range []domain.SessionID{lostSession.SessionID, failed.Session.SessionID} {
		reservation, readErr := authority.GetSessionReservation(context.Background(), id)
		if readErr != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
			t.Fatalf("unconfirmed cleanup released %s reservation=%+v err=%v", id, reservation, readErr)
		}
	}
}
