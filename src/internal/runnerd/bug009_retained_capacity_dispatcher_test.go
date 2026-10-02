package runnerd

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
)

func TestBUG009DispatcherAutomaticallyRecoversConfirmedLostCapacity(t *testing.T) {
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, true)
	controller := p107DirectController(t, "tomasz.walczuk")
	lostPairs := b008SeedFourLostPairs(t, service, authority, controller, "automatic")
	queued := b008SeedQueuedOneOff(t, service, authority, controller, "automatic-queued")

	dispatcher := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), 5*time.Millisecond, time.Minute)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	terminal := bug007WaitForTerminalJob(t, authority, queued.JobID)
	if terminal.Phase != store.JobPhaseComplete || terminal.CommandState == nil || *terminal.CommandState != domain.CommandStateSucceeded || terminal.TeardownState != store.JobTeardownClosed {
		t.Fatalf("queued job terminal result=%+v", terminal)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != int32(len(lostPairs)) {
		t.Fatalf("automatic lost-runtime proofs=%d, want %d", got, len(lostPairs))
	}
	if got := runtime.lostFinalizeCalls.Load(); got != int32(len(lostPairs)) {
		t.Fatalf("automatic lost-runtime finalizations=%d, want %d", got, len(lostPairs))
	}
	if got := runtime.commandCalls.Load(); got != 1 {
		t.Fatalf("queued runtime command calls=%d, want 1", got)
	}
	bug007AssertExecutedOnce(t, authority, queued.CommandID)
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != 0 {
		t.Fatalf("live command slots after automatic recovery=%d err=%v, want 0", live, err)
	}
}

func TestBUG009DispatcherRetainsCapacityAndThrottlesUnconfirmedProof(t *testing.T) {
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, false)
	controller := p107DirectController(t, "tomasz.walczuk")
	b008SeedFourLostPairs(t, service, authority, controller, "unconfirmed")
	queued := b008SeedQueuedOneOff(t, service, authority, controller, "unconfirmed-queued")

	interval := 5 * time.Millisecond
	dispatcher := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), interval, 250*time.Millisecond)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	// The existing recovery service stops at the first unconfirmed process
	// group. One failed proof is therefore one complete safe attempt; no
	// further group is inspected or released in that attempt.
	bug009WaitForLostRecoveryCalls(t, runtime, 1)
	time.Sleep(10 * interval)
	if got := runtime.lostRecoveryCalls.Load(); got != 1 {
		t.Fatalf("lost-runtime proofs after throttled ticks=%d, want 1", got)
	}
	if got := runtime.lostFinalizeCalls.Load(); got != 0 {
		t.Fatalf("lost-runtime finalizations after unconfirmed proof=%d, want 0", got)
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != store.DefaultRunningCommandLimit {
		t.Fatalf("live retained slots=%d err=%v, want %d", live, err, store.DefaultRunningCommandLimit)
	}
	b008AssertQueuedNotStarted(t, authority, queued)
	if got := runtime.commandCalls.Load(); got != 0 {
		t.Fatalf("queued runtime command calls after unconfirmed proof=%d, want 0", got)
	}
}

func TestBUG009DispatcherRetriesAfterRestartWithoutDuplicatingQueuedCommand(t *testing.T) {
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, false)
	controller := p107DirectController(t, "tomasz.walczuk")
	lostPairs := b008SeedFourLostPairs(t, service, authority, controller, "restart")
	queued := b008SeedQueuedOneOff(t, service, authority, controller, "restart-queued")

	first := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), 5*time.Millisecond, time.Minute)
	first.Start(t.Context())
	bug009WaitForLostRecoveryCalls(t, runtime, 1)
	bug007StopDispatcher(t, first)
	b008AssertQueuedNotStarted(t, authority, queued)

	// This is the modeled post-restart state: the first dispatcher left the
	// durable lost capacity untouched because proof was unavailable, then the
	// same process group became provably stopped before the next instance tick.
	runtime.cleanupConfirmed.Store(true)
	second := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), 5*time.Millisecond, time.Minute)
	second.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, second) })

	terminal := bug007WaitForTerminalJob(t, authority, queued.JobID)
	if terminal.Phase != store.JobPhaseComplete || terminal.CommandState == nil || *terminal.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("queued job terminal after restart=%+v", terminal)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != int32(1+len(lostPairs)) {
		t.Fatalf("lost-runtime proofs across restart=%d, want %d", got, 1+len(lostPairs))
	}
	if got := runtime.commandCalls.Load(); got != 1 {
		t.Fatalf("queued runtime command calls across restart=%d, want 1", got)
	}
	bug007AssertExecutedOnce(t, authority, queued.CommandID)
}

func TestBUG009DispatcherRetriesAfterThrottleWithoutRestart(t *testing.T) {
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, false)
	controller := p107DirectController(t, "tomasz.walczuk")
	lostPairs := b008SeedFourLostPairs(t, service, authority, controller, "same-process-retry")
	queued := b008SeedQueuedOneOff(t, service, authority, controller, "same-process-retry-queued")

	interval := 5 * time.Millisecond
	dispatcher := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), interval, 100*time.Millisecond)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	bug009WaitForLostRecoveryCalls(t, runtime, 1)
	time.Sleep(4 * interval)
	if got := runtime.lostRecoveryCalls.Load(); got != 1 {
		t.Fatalf("lost-runtime proofs before retry interval=%d, want 1", got)
	}
	b008AssertQueuedNotStarted(t, authority, queued)

	// The same dispatcher instance observes the process group become safe after
	// its bounded retry interval; no restart or replacement request is needed.
	runtime.cleanupConfirmed.Store(true)
	terminal := bug007WaitForTerminalJob(t, authority, queued.JobID)
	if terminal.Phase != store.JobPhaseComplete || terminal.CommandState == nil || *terminal.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("queued job terminal after same-process retry=%+v", terminal)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != int32(1+len(lostPairs)) {
		t.Fatalf("lost-runtime proofs after same-process retry=%d, want %d", got, 1+len(lostPairs))
	}
	bug007AssertExecutedOnce(t, authority, queued.CommandID)
}

func TestBUG009DispatcherRecoversCapacityWhileFinalizationRemainsPending(t *testing.T) {
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, true)
	// Keep the old finalization pending through several ticks. The recovery of
	// fresh retained capacity still has to run and resume the original queued ID.
	runtime.finalizeFailures.Store(1000)
	controller := p107DirectController(t, "tomasz.walczuk")
	firstLostPairs := b008SeedFourLostPairs(t, service, authority, controller, "pending-finalization")
	if _, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), firstLostPairs); !errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization) {
		t.Fatalf("seed pending finalization recovery error=%v, want finalization pending", err)
	}
	if pending, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background()); err != nil || len(pending) != len(firstLostPairs) {
		t.Fatalf("seed pending finalizations=%+v err=%v, want %d", pending, err, len(firstLostPairs))
	}

	secondLostPairs := b008SeedFourLostPairs(t, service, authority, controller, "capacity-after-pending")
	queued := b008SeedQueuedOneOff(t, service, authority, controller, "capacity-after-pending-queued")
	dispatcher := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), 5*time.Millisecond, 250*time.Millisecond)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	terminal := bug007WaitForTerminalJob(t, authority, queued.JobID)
	if terminal.Phase != store.JobPhaseComplete || terminal.CommandState == nil || *terminal.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("queued job terminal while finalization remains pending=%+v", terminal)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != int32(len(firstLostPairs)+len(secondLostPairs)) {
		t.Fatalf("lost-runtime proofs with pending finalization=%d, want %d", got, len(firstLostPairs)+len(secondLostPairs))
	}
	bug007AssertExecutedOnce(t, authority, queued.CommandID)
}

func TestBUG009DispatcherContinuesAfterLostRecoveryFinalizationPending(t *testing.T) {
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, true)
	runtime.finalizeFailures.Store(1)
	controller := p107DirectController(t, "tomasz.walczuk")
	lostPairs := b008SeedFourLostPairs(t, service, authority, controller, "finalization")
	queued := b008SeedQueuedOneOff(t, service, authority, controller, "finalization-queued")

	dispatcher := bug009NewDispatcher(t, service, authority, lifecycle.NewGate(), 5*time.Millisecond, 15*time.Millisecond)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	terminal := bug007WaitForTerminalJob(t, authority, queued.JobID)
	if terminal.Phase != store.JobPhaseComplete || terminal.CommandState == nil || *terminal.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("queued job terminal after finalization warning=%+v", terminal)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != int32(len(lostPairs)) {
		t.Fatalf("lost-runtime proofs before finalization warning=%d, want %d", got, len(lostPairs))
	}
	if got := runtime.commandCalls.Load(); got != 1 {
		t.Fatalf("queued runtime command calls after finalization warning=%d, want 1", got)
	}
	bug007AssertExecutedOnce(t, authority, queued.CommandID)
	bug009WaitForNoPendingLostRuntimeFinalizations(t, authority)
	if got := runtime.lostFinalizeCalls.Load(); got != int32(1+len(lostPairs)) {
		t.Fatalf("lost-runtime finalizations with durable retry=%d, want %d", got, 1+len(lostPairs))
	}
}

func bug009NewDispatcher(t *testing.T, service *execution.Service, authority *store.AuthorityStore, dispatchGate *lifecycle.Gate, recoveryInterval, retainedRecoveryInterval time.Duration) *RunnerDispatcher {
	t.Helper()
	dispatcher, err := NewRunnerDispatcher(RunnerDispatcherOptions{
		Service:                              service,
		Authority:                            authority,
		DispatchGate:                         dispatchGate,
		RecoveryInterval:                     recoveryInterval,
		RetainedLostCapacityRecoveryInterval: retainedRecoveryInterval,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func bug009WaitForLostRecoveryCalls(t *testing.T, runtime *b008DispatcherRuntime, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.lostRecoveryCalls.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("lost-runtime proof calls=%d, want at least %d", runtime.lostRecoveryCalls.Load(), want)
}

func bug009WaitForNoPendingLostRuntimeFinalizations(t *testing.T, authority *store.AuthorityStore) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pairs, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(pairs) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	pairs, err := authority.ListPendingLostRuntimeRecoveryFinalizations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("pending lost runtime finalizations=%+v, want none", pairs)
}
