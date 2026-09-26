package execution

import (
	"context"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p027Runtime struct {
	p020FakeRuntime
	reconcileResult RuntimeReconcileResult
	reconcileErr    error
	reconcileCall   int
	reconcileSaw    []store.SessionRecord
}

func (r *p027Runtime) Reconcile(_ context.Context, request RuntimeReconcileRequest) (RuntimeReconcileResult, error) {
	r.reconcileCall++
	r.reconcileSaw = append(r.reconcileSaw, request.Session)
	return r.reconcileResult, r.reconcileErr
}

func TestP027CreatingSessionBecomesFailedAfterConfirmedStartupCleanup(t *testing.T) {
	runtime := &p027Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-old"}, reconcileResult: RuntimeReconcileResult{
		RuntimeGeneration: "generation-old",
		CleanupConfirmed:  true,
	}}
	service, authority := newP027Service(t, runtime)
	request := p020Request(t, "session-p027-creating", "key-p027-creating", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	accepted := p027AcceptCreating(t, authority, request)

	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settled, err := authority.GetSession(context.Background(), accepted.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != domain.SessionStateFailed || report.CreatingFailed != 1 || report.SessionReservationsReleased != 1 || report.CleanupConfirmed != 1 {
		t.Fatalf("creating reconciliation = %+v report=%+v", settled, report)
	}
	if runtime.prepareCall != 0 || runtime.startCall != 0 || runtime.reconcileCall != 1 {
		t.Fatalf("runtime calls prepare=%d start=%d reconcile=%d", runtime.prepareCall, runtime.startCall, runtime.reconcileCall)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live reservations=%d err=%v, want 0", got, err)
	}
}

func TestP027CreatingSessionBecomesLostWhenCleanupIsUnconfirmed(t *testing.T) {
	runtime := &p027Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-partial"}, reconcileResult: RuntimeReconcileResult{
		RuntimeGeneration: "generation-partial",
		CleanupConfirmed:  false,
	}}
	service, authority := newP027Service(t, runtime)
	request := p020Request(t, "session-p027-partial", "key-p027-partial", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	accepted := p027AcceptCreating(t, authority, request)

	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settled, err := authority.GetSession(context.Background(), accepted.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != domain.SessionStateLost || report.CreatingLost != 1 || report.SessionReservationsReleased != 0 || report.CleanupUnconfirmed != 1 {
		t.Fatalf("uncertain creating reconciliation = %+v report=%+v", settled, report)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live reservations=%d err=%v, want retained 1", got, err)
	}
}

func TestP027BusyRestartMarksCommandLostRejectsQueuedWorkAndRetainsSlot(t *testing.T) {
	runtime := &p027Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-old"}, reconcileResult: RuntimeReconcileResult{
		RuntimeGeneration: "generation-new",
		CleanupConfirmed:  false,
	}}
	service, authority := newP027Service(t, runtime)
	request := p020Request(t, "session-p027-busy", "key-p027-busy", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	created, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	first := p027AcceptCommand(t, authority, created.Session, "command-p027-running", "key-p027-running", "printf running")
	second := p027AcceptCommand(t, authority, created.Session, "command-p027-queued", "key-p027-queued", "printf queued")
	started, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID != first.CommandID || created.Session.RuntimeGeneration != "generation-old" || second.State != domain.CommandStateQueued {
		t.Fatalf("seed state started=%+v second=%+v session=%+v", started, second, created.Session)
	}

	beforeStart, beforeCommand := runtime.startCall, runtime.commandCall
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session, err := authority.GetSession(context.Background(), created.Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	lost, err := authority.GetCommand(context.Background(), first.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := authority.GetCommand(context.Background(), second.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != domain.SessionStateLost || lost.State != domain.CommandStateLost || lost.OutputComplete || rejected.State != domain.CommandStateRejected {
		t.Fatalf("restart settlement session=%+v lost=%+v rejected=%+v", session, lost, rejected)
	}
	if report.GenerationMismatches != 1 || report.CommandsLost != 1 || report.CommandsRejected != 1 || report.CommandSlotsReleased != 0 || report.SessionReservationsReleased != 0 {
		t.Fatalf("restart report=%+v", report)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 1 {
		t.Fatalf("live command slots=%d err=%v, want retained 1", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 1 {
		t.Fatalf("live session reservations=%d err=%v, want retained 1", reservations, err)
	}
	if runtime.startCall != beforeStart || runtime.commandCall != beforeCommand {
		t.Fatalf("restart reattached or executed work: start %d->%d command %d->%d", beforeStart, runtime.startCall, beforeCommand, runtime.commandCall)
	}
	if _, err := service.ReconcileStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.reconcileCall != 1 {
		t.Fatalf("terminal retry called runtime reconcile %d times, want 1", runtime.reconcileCall)
	}
}

func TestP027ClosingSessionResumesToClosedOnlyAfterConfirmedCleanup(t *testing.T) {
	runtime := &p027Runtime{p020FakeRuntime: p020FakeRuntime{generation: "generation-close"}, reconcileResult: RuntimeReconcileResult{
		RuntimeGeneration: "generation-close",
		CleanupConfirmed:  true,
	}}
	service, authority := newP027Service(t, runtime)
	request := p020Request(t, "session-p027-closing", "key-p027-closing", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	created, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), created.Session.SessionID, domain.SessionStateClosing, "close_requested"); err != nil {
		t.Fatal(err)
	}
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closed, err := authority.GetSession(context.Background(), created.Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != domain.SessionStateClosed || report.SessionsClosed != 1 || report.SessionReservationsReleased != 1 {
		t.Fatalf("closing reconciliation session=%+v report=%+v", closed, report)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live reservations=%d err=%v, want 0", got, err)
	}
}

func p027AcceptCreating(t *testing.T, authority *store.AuthorityStore, request CreateSessionRequest) store.SessionRecord {
	t.Helper()
	environment := p020Environment(t)
	limits, err := environment.ValidateSessionPolicy(domain.SessionPolicyRequest{
		Target: request.Target, Source: request.Source, Controller: request.Controller,
		Limits: request.RequestedLimits, Isolation: request.Isolation,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, duplicate, err := authority.AcceptSessionCreate(context.Background(), store.SessionCreateAcceptance{
		SessionCreate: store.SessionCreate{
			SessionID: request.SessionID, Target: request.Target, Environment: environment.Name(),
			Controller: request.Controller, Source: request.Source, Limits: limits, Reason: "session_created",
		},
		IdempotencyKey: request.IdempotencyKey, RequestHash: request.RequestHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate || record.State != domain.SessionStateCreating {
		t.Fatalf("accepted creating=%+v duplicate=%v", record, duplicate)
	}
	return record
}

func p027AcceptCommand(t *testing.T, authority *store.AuthorityStore, session store.SessionRecord, commandID, key, script string) store.CommandRecord {
	t.Helper()
	hash, err := domain.HashMutationRequestJSON("submit_command", []byte(fmt.Sprintf(`{"command_id":%q,"script":%q}`, commandID, script)), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record, duplicate, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: commandIDForP027(t, commandID), SessionID: session.SessionID, RequestHash: hash,
		IdempotencyKey: key, Script: script, Timeout: session.Limits.CommandTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate || record.State != domain.CommandStateQueued {
		t.Fatalf("accepted command=%+v duplicate=%v", record, duplicate)
	}
	return record
}

func commandIDForP027(t *testing.T, value string) domain.CommandID {
	t.Helper()
	id, err := domain.NewCommandID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newP027Service(t *testing.T, runtime *p027Runtime) (*Service, *store.AuthorityStore) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir()+"/state/p027.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := &p020Clock{now: time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(db, now.Now)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(p020Environment(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, now, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	return service, authority
}
