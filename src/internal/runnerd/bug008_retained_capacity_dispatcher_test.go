package runnerd

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

// TestBUG008DispatcherStartsPreservedQueuedOneOffsAfterRetainedCapacityRelease
// exercises the manual online-recovery preservation boundary with a real
// dispatcher. Four terminal-lost pairs occupy every command slot while two
// durable one-off jobs remain ready/queued. B009 later made the dispatcher
// recover that exact shape automatically; this test therefore performs the
// explicit B008 service recovery before starting the dispatcher, then proves
// the preserved original queued IDs execute normally.
func TestBUG008DispatcherStartsPreservedQueuedOneOffsAfterRetainedCapacityRelease(t *testing.T) {
	ctx := context.Background()
	runtime, service, authority, database := b008NewDispatcherRecoveryService(t, true)
	controller := p107DirectController(t, "tomasz.walczuk")
	lostRequests := b008SeedFourLostPairs(t, service, authority, controller, "release")
	first := b008SeedQueuedOneOff(t, service, authority, controller, "release-first")
	second := b008SeedQueuedOneOff(t, service, authority, controller, "release-second")

	// No dispatcher owns a claim before the explicit recovery, so the exact
	// original queued boundary can be asserted without relying on the later
	// automatic B009 recovery policy.
	b008AssertQueuedNotStarted(t, authority, first)
	b008AssertQueuedNotStarted(t, authority, second)
	if got := runtime.commandCalls.Load(); got != 0 {
		t.Fatalf("runtime command calls before retained-capacity release=%d, want 0", got)
	}

	results, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(ctx, lostRequests)
	if err != nil || len(results) != len(lostRequests) {
		t.Fatalf("queue-preserving recovery results=%+v err=%v", results, err)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != int32(len(lostRequests)) {
		t.Fatalf("lost runtime proofs=%d, want %d", got, len(lostRequests))
	}
	if got := runtime.lostFinalizeCalls.Load(); got != int32(len(lostRequests)) {
		t.Fatalf("lost runtime finalizations=%d, want %d", got, len(lostRequests))
	}

	dispatcher := bug007NewDispatcher(t, service, authority, lifecycle.NewGate(), 25*time.Millisecond)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	// Starting the ordinary dispatcher after the explicit release must claim
	// the same queued identities; it does not replay a lost script.
	firstTerminal := bug007WaitForTerminalJob(t, authority, first.JobID)
	secondTerminal := bug007WaitForTerminalJob(t, authority, second.JobID)
	for _, terminal := range []store.JobRecord{firstTerminal, secondTerminal} {
		if terminal.Phase != store.JobPhaseComplete || terminal.CommandState == nil || *terminal.CommandState != domain.CommandStateSucceeded || terminal.TeardownState != store.JobTeardownClosed {
			t.Fatalf("preserved queued job terminal result=%+v", terminal)
		}
	}
	if got := b008DurableCommandStartOrder(t, database, first.CommandID, second.CommandID); len(got) != 2 || got[0] != first.CommandID || got[1] != second.CommandID {
		t.Fatalf("durable command start order=%v, want [%s %s]", got, first.CommandID, second.CommandID)
	}
	bug007AssertExecutedOnce(t, authority, first.CommandID)
	bug007AssertExecutedOnce(t, authority, second.CommandID)
	if got := runtime.commandCalls.Load(); got != 2 {
		t.Fatalf("runtime command calls after retained-capacity release=%d, want exactly two preserved commands", got)
	}
}

// TestBUG008DispatcherDoesNotStartQueuedWorkWhenLostCleanupIsUnconfirmed
// proves a failed explicit proof retains all four slots. B009 separately
// covers automatic tick recovery and its retry throttle. No queued command may
// cross command_started before a later successful explicit recovery.
func TestBUG008DispatcherDoesNotStartQueuedWorkWhenLostCleanupIsUnconfirmed(t *testing.T) {
	ctx := context.Background()
	runtime, service, authority, _ := b008NewDispatcherRecoveryService(t, false)
	controller := p107DirectController(t, "tomasz.walczuk")
	lostRequests := b008SeedFourLostPairs(t, service, authority, controller, "unconfirmed")
	first := b008SeedQueuedOneOff(t, service, authority, controller, "unconfirmed-first")
	second := b008SeedQueuedOneOff(t, service, authority, controller, "unconfirmed-second")

	if _, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(ctx, lostRequests); !errors.Is(err, execution.ErrLostRuntimeRecoveryUnconfirmed) {
		t.Fatalf("queue-preserving recovery error=%v, want cleanup unconfirmed", err)
	}
	if got := runtime.lostRecoveryCalls.Load(); got != 1 {
		t.Fatalf("lost runtime proof calls=%d, want first failed proof only", got)
	}
	if got := runtime.lostFinalizeCalls.Load(); got != 0 {
		t.Fatalf("lost runtime finalization calls=%d, want 0", got)
	}

	// The recovery returned without a durable capacity release. The original
	// commands must retain their exact queued boundary.
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != store.DefaultRunningCommandLimit {
		t.Fatalf("live retained slots=%d err=%v, want %d", live, err, store.DefaultRunningCommandLimit)
	}
	b008AssertQueuedNotStarted(t, authority, first)
	b008AssertQueuedNotStarted(t, authority, second)
	if got := runtime.commandCalls.Load(); got != 0 {
		t.Fatalf("runtime command calls after unconfirmed cleanup=%d, want 0", got)
	}
}

type b008DispatcherRuntime struct {
	generation        string
	cleanupConfirmed  atomic.Bool
	finalizeFailures  atomic.Int32
	commandCalls      atomic.Int32
	lostRecoveryCalls atomic.Int32
	lostFinalizeCalls atomic.Int32
}

func (r *b008DispatcherRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *b008DispatcherRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (*b008DispatcherRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error {
	return nil
}

func (r *b008DispatcherRuntime) ExecuteCommand(_ context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.commandCalls.Add(1)
	return execution.RuntimeCommandResult{Stdout: []byte("bug008-dispatcher-output\n"), ExitCode: 0}, nil
}

func (r *b008DispatcherRuntime) ReconcileLostRuntime(_ context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	r.lostRecoveryCalls.Add(1)
	return execution.RuntimeReconcileResult{RuntimeGeneration: request.Session.RuntimeGeneration, CleanupConfirmed: r.cleanupConfirmed.Load()}, nil
}

func (r *b008DispatcherRuntime) FinalizeLostRuntime(_ context.Context, request execution.RuntimeReconcileRequest) (execution.RuntimeReconcileResult, error) {
	r.lostFinalizeCalls.Add(1)
	for {
		remaining := r.finalizeFailures.Load()
		if remaining <= 0 {
			break
		}
		if r.finalizeFailures.CompareAndSwap(remaining, remaining-1) {
			return execution.RuntimeReconcileResult{RuntimeGeneration: request.Session.RuntimeGeneration, CleanupConfirmed: true}, errors.New("fixture finalization failure")
		}
	}
	return execution.RuntimeReconcileResult{RuntimeGeneration: request.Session.RuntimeGeneration, CleanupConfirmed: true}, nil
}

type b008DispatcherClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *b008DispatcherClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Nanosecond)
	return c.now
}

func b008NewDispatcherRecoveryService(t *testing.T, cleanupConfirmed bool) (*b008DispatcherRuntime, *execution.Service, *store.AuthorityStore, *sql.DB) {
	t.Helper()
	fixture := testfixture.New(t)
	database, err := store.Open(context.Background(), filepath.Join(fixture.Path(), "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	clock := &b008DispatcherClock{now: time.Now().UTC()}
	authority, err := store.NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &b008DispatcherRuntime{generation: "bug008-dispatcher-generation"}
	runtime.cleanupConfirmed.Store(cleanupConfirmed)
	service, err := execution.NewExecutionService(authority, runtime, bug007DirectLinuxRegistry(t), execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, service, authority, database
}

func b008SeedFourLostPairs(t *testing.T, service *execution.Service, authority *store.AuthorityStore, controller domain.ControllerIdentity, suffix string) []execution.LostRuntimeRecoveryRequest {
	t.Helper()
	requests := make([]execution.LostRuntimeRecoveryRequest, 0, store.DefaultRunningCommandLimit)
	for _, label := range []string{"a", "b", "c", "d"} {
		requests = append(requests, b008SeedLostPair(t, service, authority, controller, suffix+"-"+label))
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != store.DefaultRunningCommandLimit {
		t.Fatalf("seeded live slots=%d err=%v, want %d", live, err, store.DefaultRunningCommandLimit)
	}
	return requests
}

func b008SeedLostPair(t *testing.T, service *execution.Service, authority *store.AuthorityStore, controller domain.ControllerIdentity, suffix string) execution.LostRuntimeRecoveryRequest {
	t.Helper()
	session := bug007DispatcherCreateReadySession(t, service, controller, "sess-bug008-dispatcher-lost-"+suffix)
	queued := bug007DispatcherAcceptQueuedCommand(t, service, session, controller, "cmd-bug008-dispatcher-lost-"+suffix, "printf seed-lost")
	claim, err := service.ClaimNextEligibleCommand(context.Background())
	if err != nil || claim.CommandID != queued.CommandID || claim.State != domain.CommandStateRunning {
		t.Fatalf("claim lost seed=%+v err=%v, want running %s", claim, err, queued.CommandID)
	}
	lost, err := authority.CompleteRunningCommand(context.Background(), store.CommandTransition{
		CommandID: claim.CommandID, NextState: domain.CommandStateLost, OutputComplete: false,
	}, domain.SessionStateLost, "runtime_lost", false)
	if err != nil || lost.State != domain.CommandStateLost || lost.OutputComplete {
		t.Fatalf("mark lost seed=%+v err=%v", lost, err)
	}
	return execution.LostRuntimeRecoveryRequest{SessionID: session.SessionID, CommandID: queued.CommandID}
}

func b008SeedQueuedOneOff(t *testing.T, service *execution.Service, authority *store.AuthorityStore, controller domain.ControllerIdentity, suffix string) store.JobRecord {
	t.Helper()
	request := bug007DispatcherRunJobRequest(t, controller,
		"job-bug008-dispatcher-"+suffix,
		"sess-bug008-dispatcher-"+suffix,
		"cmd-bug008-dispatcher-"+suffix,
		"printf preserved-"+suffix)
	accepted, err := service.AcceptJob(context.Background(), request.Acceptance)
	if err != nil || accepted.Duplicate {
		t.Fatalf("accept preserved queued one-off=%+v err=%v", accepted, err)
	}
	session := bug007RecoveryCreateReadySession(t, service, accepted.Job, request)
	if _, err := authority.CheckpointJob(context.Background(), accepted.Job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseCreatingSession, NextPhase: store.JobPhaseAcceptingCommand,
	}); err != nil {
		t.Fatal(err)
	}
	queued := bug007RecoveryAcceptCommand(t, service, accepted.Job, session)
	job, err := authority.CheckpointJob(context.Background(), accepted.Job.JobID, store.JobCheckpoint{
		ExpectedPhase: store.JobPhaseAcceptingCommand, NextPhase: store.JobPhaseAwaitingCommand, Command: &queued,
	})
	if err != nil || job.CommandState == nil || *job.CommandState != domain.CommandStateQueued {
		t.Fatalf("checkpoint preserved queued one-off=%+v err=%v", job, err)
	}
	return job
}

func b008AssertQueuedNotStarted(t *testing.T, authority *store.AuthorityStore, job store.JobRecord) {
	t.Helper()
	command, err := authority.GetCommand(context.Background(), job.CommandID)
	if err != nil || command.State != domain.CommandStateQueued {
		t.Fatalf("preserved command=%+v err=%v, want queued", command, err)
	}
	events, err := authority.ListCommandEvents(context.Background(), job.CommandID)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 || events[0].Type != "command_queued" {
		t.Fatalf("preserved command events=%+v err=%v, want exact queued boundary", events, err)
	}
}

func b008DurableCommandStartOrder(t *testing.T, database *sql.DB, first, second domain.CommandID) []domain.CommandID {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `
SELECT command_id, occurred_at
FROM exec_command_events
WHERE event_type = 'command_started' AND command_id IN (?, ?)`, string(first), string(second))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type startedEvent struct {
		commandID  domain.CommandID
		occurredAt time.Time
	}
	var events []startedEvent
	for rows.Next() {
		var value, occurredAtValue string
		if err := rows.Scan(&value, &occurredAtValue); err != nil {
			t.Fatal(err)
		}
		id, err := domain.NewCommandID(value)
		if err != nil {
			t.Fatal(err)
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, occurredAtValue)
		if err != nil {
			t.Fatalf("parse command-start timestamp %q: %v", occurredAtValue, err)
		}
		events = append(events, startedEvent{commandID: id, occurredAt: occurredAt.UTC()})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].occurredAt.Equal(events[j].occurredAt) {
			return events[i].occurredAt.Before(events[j].occurredAt)
		}
		return events[i].commandID < events[j].commandID
	})
	result := make([]domain.CommandID, 0, len(events))
	for _, event := range events {
		result = append(result, event.commandID)
	}
	return result
}
