package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestRecoverLostRuntimeReleasesOnlyConfirmedCapacity(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{RuntimeGeneration: "generation-recovered", CleanupConfirmed: true}, nil)
	beforeEvents, err := authority.ListCommandEvents(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	beforeStart, beforeExecute := runtime.startCall, runtime.commandCall

	result, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyRecovered || !result.Runtime.CleanupConfirmed || result.Runtime.RuntimeGeneration != "generation-recovered" {
		t.Fatalf("recovery result=%+v", result)
	}
	if runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 || runtime.startCall != beforeStart || runtime.commandCall != beforeExecute {
		t.Fatalf("runtime calls recover=%d finalize=%d start=%d execute=%d; recovery must not start or execute a script", runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall)
	}
	afterCommand, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	afterSession, err := authority.GetSession(context.Background(), session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	afterEvents, err := authority.ListCommandEvents(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCommand.State != domain.CommandStateLost || afterCommand.OutputComplete || afterCommand.FinalEventSequence == nil || afterSession.State != domain.SessionStateLost {
		t.Fatalf("recovery changed truthful lost outcome: session=%+v command=%+v", afterSession, afterCommand)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("recovery changed event history: before=%+v after=%+v", beforeEvents, afterEvents)
	}
	for i := range beforeEvents {
		if beforeEvents[i].CommandID != afterEvents[i].CommandID || beforeEvents[i].Sequence != afterEvents[i].Sequence || beforeEvents[i].Type != afterEvents[i].Type || beforeEvents[i].ByteCount != afterEvents[i].ByteCount || !beforeEvents[i].OccurredAt.Equal(afterEvents[i].OccurredAt) || !bytes.Equal(beforeEvents[i].Payload, afterEvents[i].Payload) {
			t.Fatalf("recovery changed event %d: before=%+v after=%+v", i, beforeEvents[i], afterEvents[i])
		}
	}
	if result.CommandSlot.StopConfirmedAt == nil || result.CommandSlot.ReleasedAt == nil || result.SessionReservation.CleanupConfirmedAt == nil || result.SessionReservation.ReleasedAt == nil {
		t.Fatalf("recovery did not record both releases: %+v", result)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 0 {
		t.Fatalf("live command slots=%d err=%v, want 0", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 0 {
		t.Fatalf("live session reservations=%d err=%v, want 0", reservations, err)
	}

	repeat, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if err != nil || !repeat.AlreadyRecovered || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 2 {
		t.Fatalf("idempotent recovery=%+v err=%v recover calls=%d finalize calls=%d", repeat, err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
}

func TestRecoverLostRuntimeRetainsCapacityWhenRuntimeIsUnconfirmed(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{RuntimeGeneration: "generation-unconfirmed"}, nil)
	result, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || result.Runtime.CleanupConfirmed || runtime.lostRecoveryCall != 1 {
		t.Fatalf("recovery result=%+v err=%v recovery calls=%d", result, err, runtime.lostRecoveryCall)
	}
	pRecoveryAssertRetained(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverLostRuntimeRetainsCapacityWhenRuntimeErrors(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{}, errors.New("fixture reconciliation error"))
	_, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 1 {
		t.Fatalf("recovery err=%v recovery calls=%d", err, runtime.lostRecoveryCall)
	}
	pRecoveryAssertRetained(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverLostRuntimeKeepsCapacityWhenPairedStoreWriteFails(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{RuntimeGeneration: "generation-store-failure", CleanupConfirmed: true}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.lostRecoveryHook = func(context.Context) { cancel() }

	_, err := service.RecoverLostRuntime(ctx, LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if err == nil || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("store failure err=%v recover calls=%d finalize calls=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertRetained(t, authority, session.SessionID, command.CommandID)

	runtime.lostRecoveryHook = nil
	retry, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if err != nil || retry.AlreadyRecovered || runtime.lostRecoveryCall != 2 || runtime.lostFinalizeCall != 1 {
		t.Fatalf("store retry=%+v err=%v recover calls=%d finalize calls=%d", retry, err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertReleased(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverLostRuntimeRetriesFinalizationAfterDurableCapacityRelease(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{RuntimeGeneration: "generation-finalize-retry", CleanupConfirmed: true}, nil)
	runtime.lostFinalizeErr = errors.New("fixture finalization failure")

	_, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if !errors.Is(err, ErrLostRuntimeRecoveryFinalization) || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 {
		t.Fatalf("initial finalization err=%v recover calls=%d finalize calls=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertReleased(t, authority, session.SessionID, command.CommandID)

	runtime.lostFinalizeErr = nil
	retry, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if err != nil || !retry.AlreadyRecovered || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 2 {
		t.Fatalf("finalization retry=%+v err=%v recover calls=%d finalize calls=%d", retry, err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertReleased(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverLostRuntimeRefusesOtherLiveWorkBeforeRuntimeAction(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{CleanupConfirmed: true}, nil)
	secondRequest := p020Request(t, "session-lost-recovery-other", "key-lost-recovery-other", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	second, err := service.CreateSession(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondCommand := p022QueueCommand(t, authority, second.Session, "command-lost-recovery-other", "key-lost-recovery-other")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID}); !errors.Is(err, ErrLostRuntimeRecoveryIneligible) {
		t.Fatalf("recovery with other live work error=%v, want ineligible", err)
	}
	if runtime.lostRecoveryCall != 0 {
		t.Fatalf("recovery called runtime before rejecting other work: %d", runtime.lostRecoveryCall)
	}
	pRecoveryAssertRetained(t, authority, session.SessionID, command.CommandID)
	if running, err := authority.GetCommand(context.Background(), secondCommand.CommandID); err != nil || running.State != domain.CommandStateRunning {
		t.Fatalf("other command=%+v err=%v, want running untouched", running, err)
	}
}

func TestRecoverLostRuntimeRefusesMismatchedCommandBeforeRuntimeAction(t *testing.T) {
	service, authority, runtime, session, command := pRecoveryLostFixture(t, RuntimeReconcileResult{CleanupConfirmed: true}, nil)
	_, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: domain.CommandID("cmd-lost-recovery-not-found")})
	if !errors.Is(err, store.ErrCommandNotFound) || runtime.lostRecoveryCall != 0 {
		t.Fatalf("mismatched recovery error=%v recovery calls=%d", err, runtime.lostRecoveryCall)
	}
	pRecoveryAssertRetained(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverLostRuntimeUsesMetadataWhenScriptIsCorrupt(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-lost-recovery-corrupt-script", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-corrupt-script", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-corrupt-script", CleanupConfirmed: true},
	}
	service, authority, database := newP027ServiceWithDatabase(t, runtime)
	session, command := pRecoveryLostPair(t, service, authority, "corrupt-script")
	if _, err := database.Exec(`UPDATE exec_commands SET script_bytes = ? WHERE command_id = ?`, []byte("tampered"), string(command.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetCommand(context.Background(), command.CommandID); !errors.Is(err, store.ErrCommandPayloadCorrupt) {
		t.Fatalf("fixture command error=%v, want corrupt payload", err)
	}

	result, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID})
	if err != nil || result.Command.CommandID != command.CommandID || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 1 {
		t.Fatalf("metadata-only recovery=%+v err=%v recovery=%d finalize=%d", result, err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertReleased(t, authority, session.SessionID, command.CommandID)
}

func TestRecoverLostRuntimeBatchProvesAllPairsBeforeAtomicRelease(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-lost-recovery-batch", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-batch", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-batch", CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "batch-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "batch-second")
	beforeStart, beforeExecute := runtime.startCall, runtime.commandCall

	results, err := service.RecoverLostRuntimeBatch(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
	})
	if err != nil || len(results) != 2 || runtime.lostRecoveryCall != 2 || runtime.lostFinalizeCall != 2 || runtime.startCall != beforeStart || runtime.commandCall != beforeExecute {
		t.Fatalf("batch results=%+v err=%v recovery=%d finalize=%d start=%d execute=%d", results, err, runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall)
	}
	pRecoveryAssertReleased(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertReleased(t, authority, secondSession.SessionID, secondCommand.CommandID)
}

func TestRecoverLostRuntimeBatchRetainsEveryPairWhenOneProofFails(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-lost-recovery-batch-failure", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-batch-failure", CleanupConfirmed: false},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-batch-failure", CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "batch-failure-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "batch-failure-second")
	beforeStart, beforeExecute := runtime.startCall, runtime.commandCall

	_, err := service.RecoverLostRuntimeBatch(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
	})
	if !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 1 || runtime.lostFinalizeCall != 0 || runtime.startCall != beforeStart || runtime.commandCall != beforeExecute {
		t.Fatalf("batch proof failure err=%v recovery=%d finalize=%d start=%d execute=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.startCall, runtime.commandCall)
	}
	pRecoveryAssertRetained(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertRetained(t, authority, secondSession.SessionID, secondCommand.CommandID)
}

func TestRecoverLostRuntimeBatchRetainsEveryPairWhenLaterProofFailsThenRetries(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-lost-recovery-batch-later-failure", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-batch-later-failure", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-lost-recovery-batch-later-failure", CleanupConfirmed: true},
	}
	runtime.lostRecoveryHook = func(context.Context) {
		if runtime.lostRecoveryCall == 2 {
			runtime.lostRecoveryResult.CleanupConfirmed = false
		}
	}
	service, authority := newP027Service(t, runtime)
	firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "batch-later-failure-first")
	secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "batch-later-failure-second")
	requests := []LostRuntimeRecoveryRequest{
		{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
		{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
	}

	if _, err := service.RecoverLostRuntimeBatch(context.Background(), requests); !errors.Is(err, ErrLostRuntimeRecoveryUnconfirmed) || runtime.lostRecoveryCall != 2 || runtime.lostFinalizeCall != 0 {
		t.Fatalf("later proof failure err=%v recovery=%d finalize=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertRetained(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertRetained(t, authority, secondSession.SessionID, secondCommand.CommandID)

	runtime.lostRecoveryHook = nil
	runtime.lostRecoveryResult.CleanupConfirmed = true
	if _, err := service.RecoverLostRuntimeBatch(context.Background(), requests); err != nil || runtime.lostRecoveryCall != 4 || runtime.lostFinalizeCall != 2 {
		t.Fatalf("later proof retry err=%v recovery=%d finalize=%d", err, runtime.lostRecoveryCall, runtime.lostFinalizeCall)
	}
	pRecoveryAssertReleased(t, authority, firstSession.SessionID, firstCommand.CommandID)
	pRecoveryAssertReleased(t, authority, secondSession.SessionID, secondCommand.CommandID)
}

func TestFinalizeReleasedLostRuntimeRecoveryBatchPreflightsEveryPair(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-finalization-preflight"},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-finalization-preflight", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-finalization-preflight", CleanupConfirmed: true},
		lostFinalizeErr:    errors.New("fixture finalization failure"),
	}
	service, authority := newP027Service(t, runtime)
	releasedSession, releasedCommand := pRecoveryLostPair(t, service, authority, "finalization-preflight-released")
	if _, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: releasedSession.SessionID, CommandID: releasedCommand.CommandID}); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed released finalization error=%v, want pending finalization", err)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 1 {
		t.Fatalf("seed pending finalizations=%+v err=%v, want one", pending, err)
	}
	unreleasedSession, unreleasedCommand := pRecoveryLostPair(t, service, authority, "finalization-preflight-unreleased")

	_, err := service.FinalizeReleasedLostRuntimeRecoveryBatch(context.Background(), []LostRuntimeRecoveryRequest{
		{SessionID: releasedSession.SessionID, CommandID: releasedCommand.CommandID},
		{SessionID: unreleasedSession.SessionID, CommandID: unreleasedCommand.CommandID},
	})
	if !errors.Is(err, ErrLostRuntimeRecoveryIneligible) {
		t.Fatalf("mixed finalization batch error=%v, want ineligible", err)
	}
	if runtime.lostFinalizeCall != 1 {
		t.Fatalf("finalizer calls after rejected mixed batch=%d, want only the seed call", runtime.lostFinalizeCall)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 1 || pending[0].CommandID != releasedCommand.CommandID {
		t.Fatalf("pending finalization after rejected mixed batch=%+v err=%v", pending, err)
	}
}

func TestReconcileStartupFinalizesPendingLostRecoveryBeforeOwnershipAudit(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-startup-finalization"},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-startup-finalization", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-startup-finalization", CleanupConfirmed: true},
		lostFinalizeErr:    errors.New("fixture finalization failure"),
	}
	service, authority := newP027Service(t, runtime)
	session, command := pRecoveryLostPair(t, service, authority, "startup-finalization")
	if _, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID}); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed pending finalization error=%v, want pending finalization", err)
	}

	runtime.lostFinalizeErr = nil
	runtime.ownershipHook = func() error {
		pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background())
		if err != nil {
			return err
		}
		if len(pending) != 0 {
			return fmt.Errorf("pending finalizations remain before ownership audit: %v", pending)
		}
		return nil
	}
	if report, err := service.ReconcileStartup(context.Background()); err != nil || report.SessionsInspected != 0 {
		t.Fatalf("startup reconciliation=%+v err=%v, want only pending finalization cleanup", report, err)
	}
	if runtime.ownershipAudit != 1 || runtime.lostFinalizeCall != 2 {
		t.Fatalf("startup calls audit=%d finalization=%d, want 1/2", runtime.ownershipAudit, runtime.lostFinalizeCall)
	}
}

func TestReconcileStartupBlocksOwnershipAuditWhenPendingLostFinalizationFails(t *testing.T) {
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-startup-finalization-fails"},
		lostRecoveryResult: RuntimeReconcileResult{RuntimeGeneration: "generation-startup-finalization-fails", CleanupConfirmed: true},
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: "generation-startup-finalization-fails", CleanupConfirmed: true},
		lostFinalizeErr:    errors.New("fixture finalization failure"),
	}
	service, authority := newP027Service(t, runtime)
	session, command := pRecoveryLostPair(t, service, authority, "startup-finalization-fails")
	if _, err := service.RecoverLostRuntime(context.Background(), LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: command.CommandID}); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed pending finalization error=%v, want pending finalization", err)
	}

	if _, err := service.ReconcileStartup(context.Background()); !errors.Is(err, ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("startup finalization error=%v, want finalization pending", err)
	}
	if runtime.ownershipAudit != 0 {
		t.Fatalf("ownership audit ran despite unresolved finalization: %d", runtime.ownershipAudit)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != 1 {
		t.Fatalf("pending finalization after blocked startup=%+v err=%v, want one", pending, err)
	}
}

func pRecoveryLostFixture(t *testing.T, reconcile RuntimeReconcileResult, reconcileErr error) (*Service, *store.AuthorityStore, *p027Runtime, store.SessionRecord, store.CommandRecord) {
	t.Helper()
	runtime := &p027Runtime{
		p020FakeRuntime:    p020FakeRuntime{generation: "generation-lost-recovery", cancelResult: RuntimeCommandStopResult{}},
		lostRecoveryResult: reconcile,
		lostRecoveryErr:    reconcileErr,
		lostFinalizeResult: RuntimeReconcileResult{RuntimeGeneration: reconcile.RuntimeGeneration, CleanupConfirmed: true},
	}
	service, authority := newP027Service(t, runtime)
	session, command := pRecoveryLostPair(t, service, authority, "single")
	return service, authority, runtime, session, command
}

func pRecoveryLostPair(t *testing.T, service *Service, authority *store.AuthorityStore, suffix string) (store.SessionRecord, store.CommandRecord) {
	t.Helper()
	request := p020Request(t, "session-lost-recovery-"+suffix, "key-lost-recovery-"+suffix, p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	created, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	command := p022QueueCommand(t, authority, created.Session, "command-lost-recovery-"+suffix, "key-command-lost-recovery-"+suffix)
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	closed, err := service.CloseSession(context.Background(), p022CloseRequest(t, created.Session.SessionID, "close-lost-recovery-"+suffix, "graceful"))
	if !errors.Is(err, ErrStopUnconfirmed) || closed.Session.State != domain.SessionStateLost {
		t.Fatalf("seed lost session result=%+v err=%v", closed, err)
	}
	lost, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || lost.State != domain.CommandStateLost || lost.OutputComplete {
		t.Fatalf("seed lost command=%+v err=%v", lost, err)
	}
	return closed.Session, lost
}

func pRecoveryAssertReleased(t *testing.T, authority *store.AuthorityStore, sessionID domain.SessionID, commandID domain.CommandID) {
	t.Helper()
	slot, err := authority.GetCommandSlot(context.Background(), commandID)
	if err != nil || slot.StopConfirmedAt == nil || slot.ReleasedAt == nil {
		t.Fatalf("released command slot=%+v err=%v", slot, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), sessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("released session reservation=%+v err=%v", reservation, err)
	}
}

func pRecoveryAssertRetained(t *testing.T, authority *store.AuthorityStore, sessionID domain.SessionID, commandID domain.CommandID) {
	t.Helper()
	slot, err := authority.GetCommandSlot(context.Background(), commandID)
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("retained command slot=%+v err=%v", slot, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), sessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("retained session reservation=%+v err=%v", reservation, err)
	}
}
