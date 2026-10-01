package runnerd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
)

// TestBUG007DispatcherProgressesQueuedOneOffAfterCapacityReturns exercises the
// production ingress boundary. The job is accepted through the direct handler
// while all four durable command slots are occupied; after one slot becomes
// available, the completing worker wakes the dispatcher to finish the
// original job without a second request or manual ResumeJob call.
func TestBUG007DispatcherProgressesQueuedOneOffAfterCapacityReturns(t *testing.T) {
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-capacity",
		commandStarted: make(chan domain.CommandID, 8),
		commandDone:    make(chan domain.CommandID, 8),
		releaseCommand: make(chan struct{}),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	_ = bug007DispatcherSaturate(t, service, authority, controller, 3, "capacity-occupied")
	releaseSession := bug007DispatcherCreateReadySession(t, service, controller, "sess-bug007-dispatcher-capacity-release")
	releaseCommand := bug007DispatcherAcceptQueuedCommand(t, service, releaseSession, controller, "cmd-bug007-dispatcher-capacity-release", "printf release")

	requestGate := lifecycle.NewGate()
	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	if started := bug007WaitForRuntimeCommandStart(t, runtimeAdapter.commandStarted); started != releaseCommand.CommandID {
		t.Fatalf("capacity-release command=%s, want %s", started, releaseCommand.CommandID)
	}
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, dispatcher.Wake)
	if err != nil {
		t.Fatal(err)
	}

	acceptedResponse := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf bug007-capacity"}`), "bug007-dispatcher-capacity-job")
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("one-off acceptance status=%d body=%s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted directJobAcceptance
	p107Decode(t, acceptedResponse, &accepted)
	jobID := domain.JobID(accepted.JobID)
	commandID := domain.CommandID(accepted.CommandID)
	queued := bug007WaitForJobPhase(t, authority, jobID, store.JobPhaseAwaitingCommand)
	if queued.Phase != store.JobPhaseAwaitingCommand || queued.CommandState == nil || *queued.CommandState != domain.CommandStateQueued {
		t.Fatalf("queued one-off job=%+v, want awaiting queued command", queued)
	}

	// The active runtime worker, rather than a second request or manual resume,
	// frees the final slot. Its completion wakes the dispatcher.
	runtimeAdapter.releaseCommand <- struct{}{}
	bug007WaitForCommandState(t, authority, releaseCommand.CommandID, domain.CommandStateSucceeded)
	if started := bug007WaitForRuntimeCommandStart(t, runtimeAdapter.commandStarted); started != commandID {
		t.Fatalf("queued one-off runtime command=%s, want %s", started, commandID)
	}
	runtimeAdapter.releaseCommand <- struct{}{}
	completed := bug007WaitForTerminalJob(t, authority, jobID)
	if completed.Phase != store.JobPhaseComplete || completed.CommandState == nil || *completed.CommandState != domain.CommandStateSucceeded || completed.TeardownState != store.JobTeardownClosed {
		t.Fatalf("completed queued one-off job=%+v", completed)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 2 {
		t.Fatalf("runtime command calls=%d, want capacity release plus one original job command", got)
	}
	bug007AssertExecutedOnce(t, authority, commandID)
}

// TestBUG007DispatcherWakeResumesAcceptedPreSessionJob proves that runnerd
// ingress returns after durable acceptance, while the worker independently
// creates the session and runs the immutable request. The blocked runtime
// start makes a synchronous handler regression observable without relying on
// a process restart.
func TestBUG007DispatcherWakeResumesAcceptedPreSessionJob(t *testing.T) {
	runtimeAdapter := &p108Runtime{
		generation:      "bug007-dispatcher-pre-session-wake",
		preparedSession: make(chan domain.SessionID, 1),
		releaseReady:    make(chan struct{}),
		commandStarted:  make(chan domain.CommandID, 1),
		commandDone:     make(chan domain.CommandID, 1),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	requestGate := lifecycle.NewGate()
	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	defer func() {
		select {
		case runtimeAdapter.releaseReady <- struct{}{}:
		default:
		}
	}()
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, dispatcher.Wake)
	if err != nil {
		t.Fatal(err)
	}

	responseChannel := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseChannel <- p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf bug007-pre-session-wake"}`), "bug007-dispatcher-pre-session-wake")
	}()
	var acceptedResponse *httptest.ResponseRecorder
	select {
	case acceptedResponse = <-responseChannel:
	case <-time.After(time.Second):
		t.Fatal("production job handler waited for runtime work instead of returning durable acceptance")
	}
	if acceptedResponse.Code != http.StatusAccepted {
		t.Fatalf("one-off acceptance status=%d body=%s", acceptedResponse.Code, acceptedResponse.Body.String())
	}
	var accepted directJobAcceptance
	p107Decode(t, acceptedResponse, &accepted)
	if accepted.KnownState.CommandState != "" {
		t.Fatalf("pre-session acceptance reported command state=%q, want empty", accepted.KnownState.CommandState)
	}

	select {
	case prepared := <-runtimeAdapter.preparedSession:
		if prepared != domain.SessionID(accepted.SessionID) {
			t.Fatalf("prepared session=%s, want accepted %s", prepared, accepted.SessionID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not resume accepted job before the session boundary")
	}
	runtimeAdapter.releaseReady <- struct{}{}
	completed := bug007WaitForTerminalJob(t, authority, domain.JobID(accepted.JobID))
	if completed.Phase != store.JobPhaseComplete || completed.CommandState == nil || *completed.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("accepted pre-session job=%+v, want complete/succeeded", completed)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 1 {
		t.Fatalf("accepted pre-session job runtime calls=%d, want one", got)
	}
	bug007AssertExecutedOnce(t, authority, domain.CommandID(accepted.CommandID))
}

// TestBUG007DispatcherClaimsGlobalOldestWorkAndExecutesEachClaimOnce leaves
// one host slot free, then verifies that the oldest queued command owns that
// slot. The next command cannot source until the older command releases it.
func TestBUG007DispatcherClaimsGlobalOldestWorkAndExecutesEachClaimOnce(t *testing.T) {
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-global-order",
		commandStarted: make(chan domain.CommandID, 8),
		commandDone:    make(chan domain.CommandID, 8),
		releaseCommand: make(chan struct{}),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	_ = bug007DispatcherSaturate(t, service, authority, controller, 3, "order-occupied")
	olderSession := bug007DispatcherCreateReadySession(t, service, controller, "sess-bug007-dispatcher-order-older")
	older := bug007DispatcherAcceptQueuedCommand(t, service, olderSession, controller, "cmd-bug007-dispatcher-order-older", "printf older")
	newerSession := bug007DispatcherCreateReadySession(t, service, controller, "sess-bug007-dispatcher-order-newer")
	newer := bug007DispatcherAcceptQueuedCommand(t, service, newerSession, controller, "cmd-bug007-dispatcher-order-newer", "printf newer")

	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	first := bug007WaitForRuntimeCommandStart(t, runtimeAdapter.commandStarted)
	if first != older.CommandID {
		t.Fatalf("first runtime command=%s, want globally older %s", first, older.CommandID)
	}
	select {
	case started := <-runtimeAdapter.commandStarted:
		t.Fatalf("new command %s started while the only free slot was owned by %s", started, older.CommandID)
	case <-time.After(75 * time.Millisecond):
	}

	runtimeAdapter.releaseCommand <- struct{}{}
	bug007WaitForCommandState(t, authority, older.CommandID, domain.CommandStateSucceeded)
	second := bug007WaitForRuntimeCommandStart(t, runtimeAdapter.commandStarted)
	if second != newer.CommandID {
		t.Fatalf("second runtime command=%s, want newer %s", second, newer.CommandID)
	}
	runtimeAdapter.releaseCommand <- struct{}{}
	bug007WaitForCommandState(t, authority, newer.CommandID, domain.CommandStateSucceeded)
	if got := runtimeAdapter.commandCalls.Load(); got != 2 {
		t.Fatalf("runtime command calls=%d, want exactly two claims", got)
	}
	bug007AssertExecutedOnce(t, authority, older.CommandID)
	bug007AssertExecutedOnce(t, authority, newer.CommandID)
}

// TestBUG007DispatcherNeverSourcesCancelledQueuedOneOff makes cancellation
// win while capacity is full, then frees capacity. The dispatcher may settle
// coordinator checkpoints, but it must never execute the cancelled script.
func TestBUG007DispatcherNeverSourcesCancelledQueuedOneOff(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-cancelled",
		commandStarted: make(chan domain.CommandID, 8),
		commandDone:    make(chan domain.CommandID, 8),
		releaseCommand: make(chan struct{}),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	_ = bug007DispatcherSaturate(t, service, authority, controller, 3, "cancel-occupied")
	releaseSession := bug007DispatcherCreateReadySession(t, service, controller, "sess-bug007-dispatcher-cancel-release")
	releaseCommand := bug007DispatcherAcceptQueuedCommand(t, service, releaseSession, controller, "cmd-bug007-dispatcher-cancel-release", "printf release")
	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	if started := bug007WaitForRuntimeCommandStart(t, runtimeAdapter.commandStarted); started != releaseCommand.CommandID {
		t.Fatalf("cancellation capacity-release command=%s, want %s", started, releaseCommand.CommandID)
	}
	request := bug007DispatcherRunJobRequest(t, controller, "job-bug007-dispatcher-cancelled", "sess-bug007-dispatcher-cancelled", "cmd-bug007-dispatcher-cancelled", "printf must-not-run")
	accepted, err := service.RunJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Job.Phase != store.JobPhaseAwaitingCommand || accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("accepted queued job=%+v", accepted)
	}
	cancelHash := bug007DispatcherHash(t, "cancel_command", map[string]string{"command_id": string(request.Acceptance.CommandID)})
	cancelled, err := service.CancelCommand(ctx, execution.CancelCommandRequest{
		CommandID:            request.Acceptance.CommandID,
		Controller:           controller,
		IdempotencyKey:       "bug007-dispatcher-cancelled-cancel",
		RequestHash:          cancelHash,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Command.State != domain.CommandStateCancelled {
		t.Fatalf("cancelled command=%+v", cancelled.Command)
	}

	runtimeAdapter.releaseCommand <- struct{}{}
	bug007WaitForCommandState(t, authority, releaseCommand.CommandID, domain.CommandStateSucceeded)
	settled := bug007WaitForTerminalJob(t, authority, request.Acceptance.JobID)
	if settled.CommandState == nil || *settled.CommandState != domain.CommandStateCancelled || settled.Phase != store.JobPhaseComplete {
		t.Fatalf("cancelled job settlement=%+v", settled)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 1 {
		t.Fatalf("cancelled queued job ran: runtime command calls=%d, want only capacity-release command", got)
	}
	events, err := authority.ListCommandEvents(ctx, request.Acceptance.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "command_started" {
			t.Fatalf("cancelled queued command has a started event: %+v", events)
		}
	}
}

// TestBUG007DispatcherStoppedGateCannotClaim confirms that shutdown closes
// the claim boundary before the dispatcher can write command_started.
func TestBUG007DispatcherStoppedGateCannotClaim(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-stopped-gate",
		commandStarted: make(chan domain.CommandID, 1),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	session := bug007DispatcherCreateReadySession(t, service, controller, "sess-bug007-dispatcher-stopped-gate")
	queued := bug007DispatcherAcceptQueuedCommand(t, service, session, controller, "cmd-bug007-dispatcher-stopped-gate", "printf blocked-by-gate")

	dispatchGate := lifecycle.NewGate()
	dispatchGate.Stop()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	dispatcher.Wake()
	select {
	case started := <-runtimeAdapter.commandStarted:
		t.Fatalf("stopped dispatch gate ran command %s", started)
	case <-time.After(100 * time.Millisecond):
	}
	stored, err := authority.GetCommand(ctx, queued.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.CommandStateQueued {
		t.Fatalf("stopped dispatch gate changed command=%+v, want queued", stored)
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 0 {
		t.Fatalf("stopped dispatch gate claimed live slots=%d err=%v", live, err)
	}
}

// TestBUG007DispatcherProductionJobAcceptanceNeverRunsAfterDispatchStops
// proves that the production /v1/jobs handler does not call RunJob itself.
// After shutdown closes the dispatch gate, it may still expose a durable
// accepted job, but it cannot create a session, claim a command, or enter the
// runtime path.
func TestBUG007DispatcherProductionJobAcceptanceNeverRunsAfterDispatchStops(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-production-stopped-gate",
		commandStarted: make(chan domain.CommandID, 1),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	requestGate := lifecycle.NewGate()
	dispatchGate := lifecycle.NewGate()
	dispatchGate.Stop()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	dispatcher.Start(t.Context())
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, dispatcher.Wake)
	if err != nil {
		t.Fatal(err)
	}

	response := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf must-not-run-after-stop"}`), "bug007-dispatcher-production-stopped-gate")
	if response.Code != http.StatusAccepted {
		t.Fatalf("one-off acceptance status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted directJobAcceptance
	p107Decode(t, response, &accepted)

	// Give the notified dispatcher a chance to observe the stopped gate.
	time.Sleep(100 * time.Millisecond)
	job, err := authority.GetJob(ctx, domain.JobID(accepted.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != store.JobPhaseCreatingSession || job.CommandState != nil {
		t.Fatalf("stopped-gate accepted job=%+v, want creating_session without command", job)
	}
	if _, err := authority.GetSession(ctx, domain.SessionID(accepted.SessionID)); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("stopped-gate accepted job created session: %v", err)
	}
	if _, err := authority.GetCommand(ctx, domain.CommandID(accepted.CommandID)); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("stopped-gate accepted job created command: %v", err)
	}
	select {
	case started := <-runtimeAdapter.commandStarted:
		t.Fatalf("stopped dispatch gate ran production accepted command %s", started)
	default:
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("stopped dispatch gate ran production accepted job calls=%d", got)
	}
}

// TestBUG007DispatcherRecoveryRecordsPreCrashQueuedJobWithoutReexecution
// covers the conservative restart policy. Startup reconciliation rejects a
// queued command left behind by a prior process; recovery records that
// terminal outcome in the job instead of sourcing the script again.
func TestBUG007DispatcherRecoveryRecordsPreCrashQueuedJobWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-restart",
		commandStarted: make(chan domain.CommandID, 8),
		commandDone:    make(chan domain.CommandID, 8),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	_ = bug007DispatcherSaturate(t, service, authority, controller, 4, "restart-occupied")
	request := bug007DispatcherRunJobRequest(t, controller, "job-bug007-dispatcher-restart", "sess-bug007-dispatcher-restart", "cmd-bug007-dispatcher-restart", "printf must-not-rerun")
	accepted, err := service.RunJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Job.Phase != store.JobPhaseAwaitingCommand || accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("pre-crash queued job=%+v", accepted)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("pre-crash setup unexpectedly ran command calls=%d", got)
	}

	reconciliation, err := service.ReconcileStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reconciliation.CommandsRejected < 1 {
		t.Fatalf("startup reconciliation=%+v, want queued job command rejection", reconciliation)
	}
	rejected, err := authority.GetCommand(ctx, request.Acceptance.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.State != domain.CommandStateRejected {
		t.Fatalf("pre-crash queued command=%+v, want rejected", rejected)
	}

	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	if err := dispatcher.RecoverNonterminalJobs(ctx); err != nil {
		t.Fatal(err)
	}
	settled := bug007WaitForTerminalJob(t, authority, request.Acceptance.JobID)
	if settled.Phase != store.JobPhaseLost || settled.CommandState == nil || *settled.CommandState != domain.CommandStateRejected {
		t.Fatalf("recovered pre-crash job=%+v, want lost/rejected", settled)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("restart recovery reexecuted command calls=%d", got)
	}
}

// TestBUG007DispatcherRecoveryStartsOnlyJobBeforeSessionBoundary covers the
// complementary restart case. An accepted job with no session row has not
// crossed a durable runtime boundary, so its immutable payload may safely
// create and execute the original job exactly once after reconciliation.
func TestBUG007DispatcherRecoveryStartsOnlyJobBeforeSessionBoundary(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{
		generation:     "bug007-dispatcher-before-session",
		commandStarted: make(chan domain.CommandID, 1),
		commandDone:    make(chan domain.CommandID, 1),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	request := bug007DispatcherRunJobRequest(t, controller, "job-bug007-dispatcher-before-session", "sess-bug007-dispatcher-before-session", "cmd-bug007-dispatcher-before-session", "printf starts-once")
	accepted, duplicate, err := authority.AcceptJob(ctx, request.Acceptance)
	if err != nil || duplicate {
		t.Fatalf("accept job before session = %+v duplicate=%v err=%v", accepted, duplicate, err)
	}
	if _, err := authority.GetSession(ctx, request.Acceptance.SessionID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("pre-recovery session lookup error=%v, want ErrSessionNotFound", err)
	}
	if report, err := service.ReconcileStartup(ctx); err != nil || report.SessionsInspected != 0 {
		t.Fatalf("startup reconciliation report=%+v err=%v", report, err)
	}

	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	if err := dispatcher.RecoverNonterminalJobs(ctx); err != nil {
		t.Fatal(err)
	}
	completed := bug007WaitForTerminalJob(t, authority, accepted.JobID)
	if completed.Phase != store.JobPhaseComplete || completed.CommandState == nil || *completed.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("recovered pre-session job=%+v, want complete/succeeded", completed)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 1 {
		t.Fatalf("recovered pre-session job command calls=%d, want 1", got)
	}
	bug007AssertExecutedOnce(t, authority, request.Acceptance.CommandID)
}

// TestBUG007DispatcherTerminalizesUnconfiguredEnvironmentBeforeSession makes
// the production acceptance boundary explicit: the HTTP request is accepted
// first, then a known permanent configuration denial becomes one durable
// failed/not_created result without a session, command, slot, or runtime call.
func TestBUG007DispatcherTerminalizesUnconfiguredEnvironmentBeforeSession(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{
		generation:      "bug007-dispatcher-unconfigured-environment",
		preparedSession: make(chan domain.SessionID, 1),
		commandStarted:  make(chan domain.CommandID, 1),
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	requestGate, dispatchGate := lifecycle.NewGate(), lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, dispatcher.Wake)
	if err != nil {
		t.Fatal(err)
	}

	// Publish before Start so this test has exactly one immediate dispatcher
	// attempt and can observe its durable result deterministically.
	response := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(`{"environment":"not-configured","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf must-not-run"}`), "bug007-unconfigured-environment")
	if response.Code != http.StatusAccepted {
		t.Fatalf("unconfigured environment acceptance status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted directJobAcceptance
	p107Decode(t, response, &accepted)
	dispatcher.Start(t.Context())

	terminal := bug007WaitForTerminalJob(t, authority, domain.JobID(accepted.JobID))
	if terminal.Phase != store.JobPhaseFailed || terminal.CommandState != nil || terminal.TeardownState != store.JobTeardownNotCreated || terminal.TeardownReason != "environment_not_configured" {
		t.Fatalf("unconfigured environment terminal job=%+v", terminal)
	}
	if terminal.Ingress != audit.IngressDirectMTLS {
		t.Fatalf("unconfigured environment job ingress=%q, want %q", terminal.Ingress, audit.IngressDirectMTLS)
	}
	audits, err := authority.ListAuditRecords(ctx, 10)
	if err != nil || len(audits) != 1 {
		t.Fatalf("unconfigured environment audits=%+v err=%v, want one", audits, err)
	}
	denial := audits[0]
	if denial.Ingress != audit.IngressDirectMTLS || denial.Action != audit.ActionCreate || denial.Outcome != audit.OutcomeDenied || denial.ReasonCode != audit.ReasonEnvironmentDenied || denial.SessionID != terminal.SessionID {
		t.Fatalf("unconfigured environment denial audit=%+v", denial)
	}
	if _, err := authority.GetSession(ctx, domain.SessionID(accepted.SessionID)); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("unconfigured environment created session: %v", err)
	}
	if _, err := authority.GetCommand(ctx, domain.CommandID(accepted.CommandID)); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("unconfigured environment created command: %v", err)
	}
	select {
	case prepared := <-runtimeAdapter.preparedSession:
		t.Fatalf("unconfigured environment entered runtime preparation for %s", prepared)
	default:
	}
	select {
	case started := <-runtimeAdapter.commandStarted:
		t.Fatalf("unconfigured environment started command %s", started)
	default:
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("unconfigured environment runtime command calls=%d, want 0", got)
	}
}

// TestBUG007PrivateQueuedIngressPreservesSSHBridgeAudit proves the production
// private bridge takes the same durable-only acceptance path as public HTTPS.
// The dispatcher resumes it using the saved adapter provenance, rather than
// mislabeling the resulting permanent denial as an internal action.
func TestBUG007PrivateQueuedIngressPreservesSSHBridgeAudit(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{generation: "bug007-private-ingress"}
	service, authority := newP046Service(t, runtimeAdapter)
	dispatchGate := lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })

	server, err := NewPrivateServer(PrivateServerOptions{
		Service:      service,
		SocketPath:   filepath.Join(p046SocketParent(t), "runnerd.sock"),
		RequestGate:  lifecycle.NewGate(),
		DispatchGate: dispatchGate,
		QueueWake:    dispatcher.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	t.Cleanup(func() { p048CloseServer(t, server, serveErr) })

	client := p046UnixClient(server.SocketPath())
	response := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/jobs", []byte(`{"job_id":"job-bug007-private-ingress","session_id":"sess-bug007-private-ingress","command_id":"cmd-bug007-private-ingress","idempotency_key":"bug007-private-ingress-key","environment":"not-configured","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"},"script":"printf must-not-run"}`))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("private queued acceptance status=%d body=%s", response.StatusCode, p046ReadBody(t, response))
	}
	var accepted jobAcceptanceResponse
	p046DecodeJSON(t, response, &accepted)
	if accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" || accepted.Duplicate {
		t.Fatalf("private queued acceptance=%+v, want one new durable identity tuple", accepted)
	}

	// Start after acceptance so the test observes a single background resume of
	// the saved durable job rather than a synchronous private-handler path.
	dispatcher.Start(t.Context())
	terminal := bug007WaitForTerminalJob(t, authority, domain.JobID(accepted.JobID))
	if terminal.Phase != store.JobPhaseFailed || terminal.CommandState != nil || terminal.TeardownState != store.JobTeardownNotCreated || terminal.Ingress != audit.IngressSSHBridge {
		t.Fatalf("private terminal job=%+v", terminal)
	}
	audits, err := authority.ListAuditRecords(ctx, 10)
	if err != nil || len(audits) != 1 {
		t.Fatalf("private queued audits=%+v err=%v, want one", audits, err)
	}
	denial := audits[0]
	if denial.Ingress != audit.IngressSSHBridge || denial.Action != audit.ActionCreate || denial.Outcome != audit.OutcomeDenied || denial.ReasonCode != audit.ReasonEnvironmentDenied || denial.SessionID != terminal.SessionID {
		t.Fatalf("private queued denial audit=%+v", denial)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("private queued terminal runtime command calls=%d, want 0", got)
	}
}

// TestBUG007DispatcherTerminalizesPreSessionPolicyDenial keeps a
// deterministic policy error distinct from a temporary authority failure.
// The accepted local-target request cannot run under linux-dev, so it must
// receive the same command-less terminal representation.
func TestBUG007DispatcherTerminalizesPreSessionPolicyDenial(t *testing.T) {
	runtimeAdapter := &p108Runtime{generation: "bug007-dispatcher-policy-denial"}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	requestGate, dispatchGate := lifecycle.NewGate(), lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, time.Hour)
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, dispatcher.Wake)
	if err != nil {
		t.Fatal(err)
	}

	response := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"other-linux-host"},"source":{"mode":"empty"},"script":"printf must-not-run"}`), "bug007-policy-denial")
	if response.Code != http.StatusAccepted {
		t.Fatalf("policy denial acceptance status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted directJobAcceptance
	p107Decode(t, response, &accepted)
	dispatcher.Start(t.Context())
	terminal := bug007WaitForTerminalJob(t, authority, domain.JobID(accepted.JobID))
	if terminal.Phase != store.JobPhaseFailed || terminal.CommandState != nil || terminal.TeardownState != store.JobTeardownNotCreated || terminal.TeardownReason != "session_policy_denied" {
		t.Fatalf("policy-denied terminal job=%+v", terminal)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("policy-denied runtime command calls=%d, want 0", got)
	}
}

// TestBUG007DispatcherDefersRetryablePreSessionFailureUntilRecoveryTick
// proves a generic resolver outage remains retryable but cannot spin on
// unrelated dispatcher wakes. Once the next bounded tick sees the resolver
// recover, the original accepted job completes without resubmission.
func TestBUG007DispatcherDefersRetryablePreSessionFailureUntilRecoveryTick(t *testing.T) {
	runtimeAdapter := &p108Runtime{generation: "bug007-dispatcher-transient-resolver"}
	resolver := &bug007TransientResolver{delegate: bug007DirectLinuxRegistry(t)}
	service, authority := newP046ServiceWithResolver(t, runtimeAdapter, resolver)
	controller := p107DirectController(t, "tomasz.walczuk")
	requestGate, dispatchGate := lifecycle.NewGate(), lifecycle.NewGate()
	dispatcher := bug007NewDispatcher(t, service, authority, dispatchGate, 200*time.Millisecond)
	t.Cleanup(func() { bug007StopDispatcher(t, dispatcher) })
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, dispatcher.Wake)
	if err != nil {
		t.Fatal(err)
	}

	response := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf retry-after-resolver-recovery"}`), "bug007-transient-resolver")
	if response.Code != http.StatusAccepted {
		t.Fatalf("transient resolver acceptance status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted directJobAcceptance
	p107Decode(t, response, &accepted)
	dispatcher.Start(t.Context())
	bug007WaitForResolverCalls(t, &resolver.calls, 1)
	// A completed job wake, an ingress wake, or an explicit caller wake must
	// not retry this known-transient resolver outage before the bounded tick.
	// The per-job deferral is what prevents the scheduler from spinning while
	// the configured resolver is temporarily unavailable.
	dispatcher.Wake()
	dispatcher.Wake()
	dispatcher.Wake()
	time.Sleep(75 * time.Millisecond)
	if got := resolver.calls.Load(); got != 1 {
		t.Fatalf("retryable resolver calls before recovery tick=%d, want 1", got)
	}
	pending, err := authority.GetJob(context.Background(), domain.JobID(accepted.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if pending.Phase != store.JobPhaseCreatingSession || pending.CommandState != nil || pending.TeardownState != store.JobTeardownPending {
		t.Fatalf("retryable resolver job=%+v, want creating_session/pending", pending)
	}

	resolver.available.Store(true)
	completed := bug007WaitForTerminalJob(t, authority, domain.JobID(accepted.JobID))
	if completed.Phase != store.JobPhaseComplete || completed.CommandState == nil || *completed.CommandState != domain.CommandStateSucceeded {
		t.Fatalf("recovered resolver job=%+v, want complete/succeeded", completed)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 1 {
		t.Fatalf("recovered resolver runtime command calls=%d, want 1", got)
	}
}

// TestBUG007PrivateReadExposesPreSessionTerminalWithFallbackCapabilities
// verifies the SSH bridge's private read route can return a terminal job after
// the requested environment disappeared. It uses a configured host profile
// only for the mandatory capability envelope; no new policy or runtime work
// is inferred from that fallback.
func TestBUG007PrivateReadExposesPreSessionTerminalWithFallbackCapabilities(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{generation: "bug007-private-terminal-read"}
	service, authority := newP046Service(t, runtimeAdapter)
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	request := bug007DispatcherRunJobRequestForEnvironment(t, controller, "not-configured", "job-bug007-private-terminal-read", "sess-bug007-private-terminal-read", "cmd-bug007-private-terminal-read", "printf must-not-run")
	accepted, err := service.AcceptJob(ctx, request.Acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResumeStoredJob(ctx, accepted.Job.JobID); !errors.Is(err, execution.ErrEnvironmentUnavailable) {
		t.Fatalf("resume unconfigured job error=%v, want environment unavailable", err)
	}

	server, serveErr := p048StartServer(t, service)
	client := p046UnixClient(server.SocketPath())
	defer p048CloseServer(t, server, serveErr)
	read := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/jobs/"+string(accepted.Job.JobID)+"?controller_type=queued_mac&controller_id=tomasz.walczuk", nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("pre-session terminal read status=%d body=%s", read.StatusCode, p046ReadBody(t, read))
	}
	var projection jobResponse
	p046DecodeJSON(t, read, &projection)
	if projection.JobPhase != string(store.JobPhaseFailed) || projection.CommandState != nil || projection.TeardownState != string(store.JobTeardownNotCreated) || projection.TeardownReason != "environment_not_configured" {
		t.Fatalf("pre-session terminal private projection=%+v", projection)
	}
	if projection.Capabilities.HostClass != "Ubuntu Linux host" || projection.Capabilities.Isolation != string(domain.IsolationOSUser) || projection.Capabilities.EffectiveAccount != "ubuntu" || len(projection.Capabilities.ServiceLimits) == 0 {
		t.Fatalf("pre-session terminal fallback capabilities=%+v", projection.Capabilities)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("private terminal read ran command calls=%d, want 0", got)
	}
	if _, err := authority.GetSession(ctx, accepted.Job.SessionID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("private terminal read job created session: %v", err)
	}
}

// TestBUG007DirectReadExposesPreSessionTerminalWithFallbackCapabilities proves
// the public mTLS resource read has the same truthful terminal representation
// as the private SSH-bridge read. The direct handler is kept separate because
// its request principal and response envelope differ from the private API.
func TestBUG007DirectReadExposesPreSessionTerminalWithFallbackCapabilities(t *testing.T) {
	ctx := context.Background()
	runtimeAdapter := &p108Runtime{generation: "bug007-direct-terminal-read"}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	request := bug007DispatcherRunJobRequestForEnvironment(t, controller, "not-configured", "job-bug007-direct-terminal-read", "sess-bug007-direct-terminal-read", "cmd-bug007-direct-terminal-read", "printf must-not-run")
	accepted, err := service.AcceptJob(ctx, request.Acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResumeStoredJob(ctx, accepted.Job.JobID); !errors.Is(err, execution.ErrEnvironmentNotConfigured) {
		t.Fatalf("resume unconfigured job error=%v, want environment-not-configured", err)
	}

	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	read := p107Do(handler, controller, true, http.MethodGet, "/v1/jobs/"+string(accepted.Job.JobID), nil, "")
	if read.Code != http.StatusOK {
		t.Fatalf("pre-session terminal direct read status=%d body=%s", read.Code, read.Body.String())
	}
	var projection directJobReadResponse
	p107Decode(t, read, &projection)
	if projection.Resource.Phase != string(store.JobPhaseFailed) || projection.Resource.CommandState != nil || projection.Resource.TeardownState != string(store.JobTeardownNotCreated) {
		t.Fatalf("pre-session terminal direct projection=%+v", projection.Resource)
	}
	if projection.Resource.Capabilities.HostClass != "Ubuntu Linux host" || projection.Resource.Capabilities.Isolation != string(domain.IsolationOSUser) || projection.Resource.Capabilities.EffectiveAccount != "ubuntu" || len(projection.Resource.Capabilities.ServiceLimits) == 0 {
		t.Fatalf("pre-session terminal direct fallback capabilities=%+v", projection.Resource.Capabilities)
	}
	if got := runtimeAdapter.commandCalls.Load(); got != 0 {
		t.Fatalf("direct terminal read ran command calls=%d, want 0", got)
	}
	if _, err := authority.GetSession(ctx, accepted.Job.SessionID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("direct terminal read job created session: %v", err)
	}
}

// TestBUG007FallbackEnvironmentUsesStoredTargetProfile ensures a terminal
// read never borrows capabilities from an unrelated configured host merely
// because it is listed first in the registry.
func TestBUG007FallbackEnvironmentUsesStoredTargetProfile(t *testing.T) {
	controller := p107DirectController(t, "tomasz.walczuk")
	otherTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "other-linux-host")
	if err != nil {
		t.Fatal(err)
	}
	linuxTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	other, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "a-other-linux", HostClass: "Other Ubuntu host", EffectiveAccount: "other-ubuntu",
		AllowedTargets: []domain.ExecutionTarget{otherTarget}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	linux, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "z-linux", HostClass: "Ubuntu Linux host", EffectiveAccount: "ubuntu",
		AllowedTargets: []domain.ExecutionTarget{linuxTarget}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(other, linux)
	if err != nil {
		t.Fatal(err)
	}
	profile, found := registry.FallbackEnvironmentForTarget(linuxTarget)
	if !found || profile.Name() != "z-linux" || profile.EffectiveAccount() != "ubuntu" {
		t.Fatalf("target-bound fallback profile=%+v found=%v, want z-linux/ubuntu", profile, found)
	}
	if profile, found := registry.FallbackEnvironmentForTarget(domain.ExecutionTarget{}); found || profile.Name() != "" {
		t.Fatalf("invalid target fallback profile=%+v found=%v, want none", profile, found)
	}
}

// TestBUG007DirectJobWakeFollowsAcceptanceWrite makes the F2 ordering
// contract observable. A production queue wake cannot begin runtime work
// until the handler has written the accepted response boundary.
func TestBUG007DirectJobWakeFollowsAcceptanceWrite(t *testing.T) {
	service, _ := newP046Service(t, &p108Runtime{generation: "bug007-acceptance-write-order"})
	requestGate, dispatchGate := lifecycle.NewGate(), lifecycle.NewGate()
	writer := &bug007OrderingResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	var wokeBeforeAcceptance atomic.Bool
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, requestGate, dispatchGate, func() {
		if !writer.headerWritten.Load() {
			wokeBeforeAcceptance.Store(true)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewBufferString(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf accepted-first"}`))
	request.ContentLength = -1
	request.Header.Set("Idempotency-Key", "bug007-acceptance-write-order")
	request = request.WithContext(context.WithValue(request.Context(), directPrincipalContextKey{}, DirectPrincipal{Controller: controller}))
	handler.ServeHTTP(writer, request)
	if wokeBeforeAcceptance.Load() {
		t.Fatal("dispatcher wake ran before the accepted response was written")
	}
	if writer.Code != http.StatusAccepted {
		t.Fatalf("acceptance status=%d body=%s", writer.Code, writer.Body.String())
	}
}

type bug007OrderingResponseWriter struct {
	*httptest.ResponseRecorder
	headerWritten atomic.Bool
}

func (w *bug007OrderingResponseWriter) WriteHeader(status int) {
	w.headerWritten.Store(true)
	w.ResponseRecorder.WriteHeader(status)
}

type bug007TransientResolver struct {
	delegate  execution.EnvironmentResolver
	available atomic.Bool
	calls     atomic.Int32
}

func (r *bug007TransientResolver) ResolveEnvironment(ctx context.Context, name string) (domain.Environment, error) {
	r.calls.Add(1)
	if !r.available.Load() {
		// Retain the broad public resolver sentinel deliberately: only the
		// registry-only ErrEnvironmentNotConfigured marker is terminal.
		return domain.Environment{}, fmt.Errorf("%w: BUG-007 temporary resolver outage", execution.ErrEnvironmentUnavailable)
	}
	return r.delegate.ResolveEnvironment(ctx, name)
}

func bug007WaitForResolverCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resolver calls=%d, want at least %d", calls.Load(), want)
}

func bug007DirectLinuxRegistry(t *testing.T) *execution.EnvironmentRegistry {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name:               "linux-dev",
		HostClass:          "Ubuntu Linux host",
		EffectiveAccount:   "ubuntu",
		AllowedTargets:     []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func bug007NewDispatcher(t *testing.T, service *execution.Service, authority *store.AuthorityStore, dispatchGate *lifecycle.Gate, recoveryInterval time.Duration) *RunnerDispatcher {
	t.Helper()
	dispatcher, err := NewRunnerDispatcher(RunnerDispatcherOptions{
		Service:          service,
		Authority:        authority,
		DispatchGate:     dispatchGate,
		RecoveryInterval: recoveryInterval,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func bug007StopDispatcher(t *testing.T, dispatcher *RunnerDispatcher) {
	t.Helper()
	dispatcher.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Wait(ctx); err != nil {
		t.Errorf("stop dispatcher: %v", err)
	}
}

func bug007DispatcherSaturate(t *testing.T, service *execution.Service, authority *store.AuthorityStore, controller domain.ControllerIdentity, count int, prefix string) []store.CommandRecord {
	t.Helper()
	claims := make([]store.CommandRecord, 0, count)
	for index := 0; index < count; index++ {
		sessionID := domain.SessionID("sess-bug007-dispatcher-" + prefix + "-" + string(rune('a'+index)))
		commandID := domain.CommandID("cmd-bug007-dispatcher-" + prefix + "-" + string(rune('a'+index)))
		session := bug007DispatcherCreateReadySession(t, service, controller, string(sessionID))
		queued := bug007DispatcherAcceptQueuedCommand(t, service, session, controller, string(commandID), "printf occupied")
		claim, err := service.ClaimNextEligibleCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if claim.CommandID != queued.CommandID || claim.State != domain.CommandStateRunning {
			t.Fatalf("occupied claim=%+v, want running %s", claim, queued.CommandID)
		}
		claims = append(claims, claim)
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != count {
		t.Fatalf("occupied live slots=%d err=%v, want %d", live, err, count)
	}
	return claims
}

func bug007DispatcherCreateReadySession(t *testing.T, service *execution.Service, controller domain.ControllerIdentity, sessionID string) store.SessionRecord {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	hash := bug007DispatcherHash(t, "create_session", map[string]any{
		"session_id":       sessionID,
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
	})
	created, err := service.CreateSession(context.Background(), execution.CreateSessionRequest{
		SessionID:            domain.SessionID(sessionID),
		IdempotencyKey:       "key-" + sessionID,
		RequestHash:          hash,
		Environment:          "linux-dev",
		Target:               target,
		Controller:           controller,
		Source:               domain.NewEmptySource(),
		MaxActiveSessions:    store.DefaultActiveSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Session.State != domain.SessionStateReady {
		t.Fatalf("created session=%+v, want ready", created.Session)
	}
	return created.Session
}

func bug007DispatcherAcceptQueuedCommand(t *testing.T, service *execution.Service, session store.SessionRecord, controller domain.ControllerIdentity, commandID, script string) store.CommandRecord {
	t.Helper()
	hash := bug007DispatcherHash(t, "submit_command", map[string]any{
		"session_id": string(session.SessionID), "script": script,
	})
	accepted, err := service.AcceptCommand(context.Background(), execution.SubmitCommandRequest{
		CommandID:            domain.CommandID(commandID),
		SessionID:            session.SessionID,
		Controller:           controller,
		IdempotencyKey:       "key-" + commandID,
		RequestHash:          hash,
		Script:               script,
		Timeout:              session.Limits.CommandTimeout,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("accepted command=%+v, want queued", accepted.Command)
	}
	return accepted.Command
}

func bug007DispatcherRunJobRequest(t *testing.T, controller domain.ControllerIdentity, jobID, sessionID, commandID, script string) execution.RunJobRequest {
	return bug007DispatcherRunJobRequestForEnvironment(t, controller, "linux-dev", jobID, sessionID, commandID, script)
}

func bug007DispatcherRunJobRequestForEnvironment(t *testing.T, controller domain.ControllerIdentity, environment, jobID, sessionID, commandID, script string) execution.RunJobRequest {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	source := domain.NewEmptySource()
	canonical, err := canonicalRunPayload(environment, target, source, script, domain.RequestedLimits{}, domain.IsolationRequirements{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.RunJobRequest{
		Acceptance: store.JobAcceptance{
			JobID:                domain.JobID(jobID),
			SessionID:            domain.SessionID(sessionID),
			CommandID:            domain.CommandID(commandID),
			Controller:           controller,
			IdempotencyKey:       "key-" + jobID,
			RequestHash:          hash,
			Environment:          environment,
			Target:               target,
			Source:               source,
			Script:               script,
			CanonicalPayload:     canonical,
			IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		},
		MaxActiveSessions:    store.DefaultActiveSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func bug007WaitForRuntimeCommandStart(t *testing.T, started <-chan domain.CommandID) domain.CommandID {
	t.Helper()
	select {
	case commandID := <-started:
		return commandID
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for dispatcher runtime command start")
		return ""
	}
}

func bug007WaitForCommandState(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID, want domain.CommandState) store.CommandRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		command, err := authority.GetCommand(context.Background(), commandID)
		if err != nil {
			t.Fatal(err)
		}
		if command.State == want {
			return command
		}
		time.Sleep(5 * time.Millisecond)
	}
	command, err := authority.GetCommand(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("command %s state=%s, want %s", commandID, command.State, want)
	return store.CommandRecord{}
}

func bug007WaitForJobPhase(t *testing.T, authority *store.AuthorityStore, jobID domain.JobID, want store.JobPhase) store.JobRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := authority.GetJob(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Phase == want {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, err := authority.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("job %s phase=%s, want %s", jobID, job.Phase, want)
	return store.JobRecord{}
}

func bug007WaitForTerminalJob(t *testing.T, authority *store.AuthorityStore, jobID domain.JobID) store.JobRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := authority.GetJob(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		switch job.Phase {
		case store.JobPhaseComplete, store.JobPhaseFailed, store.JobPhaseLost:
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, err := authority.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("job %s did not reach a terminal phase: %+v", jobID, job)
	return store.JobRecord{}
}

func bug007AssertExecutedOnce(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID) {
	t.Helper()
	events, err := authority.ListCommandEvents(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	started, terminal := 0, 0
	for _, event := range events {
		switch event.Type {
		case "command_started":
			started++
		case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
			terminal++
		}
	}
	if started != 1 || terminal != 1 {
		t.Fatalf("command %s events=%+v, want one start and one terminal event", commandID, events)
	}
}

func bug007DispatcherHash(t *testing.T, operation string, payload any) domain.CanonicalHash {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON(operation, encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
