package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP023D13ReadyIdleExpiresAfterThreshold(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-idle", stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-idle", 1*time.Minute, 10*time.Second, 2*time.Hour)
	p023Advance(service, 10*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if err != nil || report.IdleExpired != 1 || report.SessionsLost != 0 {
		t.Fatalf("idle report = %+v, err = %v", report, err)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || updated.State != domain.SessionStateExpired {
		t.Fatalf("idle session = %+v, err = %v", updated, err)
	}
	if runtime.stopCall != 1 {
		t.Fatalf("stop calls = %d, want 1", runtime.stopCall)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live session reservations = %d, err = %v, want 0", got, err)
	}
}

func TestP023D13AcceptedQueuedWorkPausesIdleClock(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-queued", stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-queued", time.Minute, 10*time.Second, 2*time.Hour)
	command := p022QueueCommand(t, authority, session, "command-p023-queued", "submit-p023-queued")
	p023Advance(service, time.Hour)
	report, err := service.EnforcePolicies(context.Background())
	if err != nil || report.IdleExpired != 0 || report.LifetimeExpired != 0 {
		t.Fatalf("queued report = %+v, err = %v", report, err)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || updated.State != domain.SessionStateReady {
		t.Fatalf("queued session = %+v, err = %v", updated, err)
	}
	queued, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || queued.State != domain.CommandStateQueued {
		t.Fatalf("queued command = %+v, err = %v", queued, err)
	}
	if runtime.stopCall != 0 {
		t.Fatalf("idle sweep stopped queued session %d times", runtime.stopCall)
	}
}

func TestP023D13ActiveCommandIgnoresIdleThenTimesOut(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-timeout", cancelResult: RuntimeCommandStopResult{Confirmed: true}}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-timeout", 10*time.Second, 2*time.Second, 2*time.Hour)
	command := p022QueueCommand(t, authority, session, "command-p023-timeout", "submit-p023-timeout")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 3*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if err != nil || report.IdleExpired != 0 || report.CommandsTimedOut != 0 {
		t.Fatalf("active-before-timeout report = %+v, err = %v", report, err)
	}
	busy, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || busy.State != domain.SessionStateBusy {
		t.Fatalf("active session = %+v, err = %v", busy, err)
	}
	p023Advance(service, 8*time.Second)
	report, err = service.EnforcePolicies(context.Background())
	if err != nil || report.CommandsTimedOut != 1 || report.IdleExpired != 0 {
		t.Fatalf("timeout report = %+v, err = %v", report, err)
	}
	timedOut, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || timedOut.State != domain.CommandStateTimedOut {
		t.Fatalf("timed-out command = %+v, err = %v", timedOut, err)
	}
	ready, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || ready.State != domain.SessionStateReady {
		t.Fatalf("session after timeout = %+v, err = %v", ready, err)
	}
	if runtime.cancelCall != 1 {
		t.Fatalf("cancel calls = %d, want 1", runtime.cancelCall)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 0 {
		t.Fatalf("live command slots = %d, err = %v, want 0", got, err)
	}
}

func TestP023D13FailedCommandTimeoutIsLostAndRetainsSlot(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-timeout-lost", cancelResult: RuntimeCommandStopResult{Confirmed: false}}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-timeout-lost", 10*time.Second, 2*time.Second, 2*time.Hour)
	command := p022QueueCommand(t, authority, session, "command-p023-timeout-lost", "submit-p023-timeout-lost")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 11*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if !errors.Is(err, ErrStopUnconfirmed) || report.CommandsTimedOut != 0 || report.SessionsLost != 1 || report.CommandsLost != 1 {
		t.Fatalf("failed timeout report = %+v, err = %v", report, err)
	}
	lost, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || lost.State != domain.CommandStateLost {
		t.Fatalf("failed timeout command = %+v, err = %v", lost, err)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || updated.State != domain.SessionStateLost {
		t.Fatalf("session after failed timeout = %+v, err = %v", updated, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live command slots after failed timeout = %d, err = %v, want 1", got, err)
	}
}

func TestP023D13MaximumLifetimeAppliesWhileBusy(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-lifetime", cancelResult: RuntimeCommandStopResult{Confirmed: true}, stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-lifetime", 20*time.Minute, 20*time.Minute, 10*time.Second)
	command := p022QueueCommand(t, authority, session, "command-p023-lifetime", "submit-p023-lifetime")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 11*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if err != nil || report.LifetimeExpired != 1 || report.CommandsTimedOut != 0 {
		t.Fatalf("lifetime report = %+v, err = %v", report, err)
	}
	expired, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || expired.State != domain.SessionStateExpired {
		t.Fatalf("expired session = %+v, err = %v", expired, err)
	}
	timedOut, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || timedOut.State != domain.CommandStateTimedOut {
		t.Fatalf("lifetime command = %+v, err = %v", timedOut, err)
	}
	if runtime.cancelCall != 1 || runtime.stopCall != 1 {
		t.Fatalf("runtime calls = cancel %d stop %d, want one each", runtime.cancelCall, runtime.stopCall)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 0 {
		t.Fatalf("live command slots = %d, err = %v, want 0", got, err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 0 {
		t.Fatalf("live session reservations = %d, err = %v, want 0", got, err)
	}
}

func TestP023D13FailedIdleTeardownIsLostAndRetainsReservation(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-idle-lost", stopConfirmed: false}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-idle-lost", time.Minute, 10*time.Second, 2*time.Hour)
	p023Advance(service, 10*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if !errors.Is(err, ErrStopUnconfirmed) || report.IdleExpired != 0 || report.SessionsLost != 1 {
		t.Fatalf("failed idle report = %+v, err = %v", report, err)
	}
	lost, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || lost.State != domain.SessionStateLost {
		t.Fatalf("lost idle session = %+v, err = %v", lost, err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live reservations after failed idle teardown = %d, err = %v, want 1", got, err)
	}
}

func TestP023D13FailedLifetimeCommandStopIsLostAndRetainsCapacity(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-lifetime-lost", cancelResult: RuntimeCommandStopResult{Confirmed: false}, stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-lifetime-lost", 20*time.Minute, 20*time.Minute, 10*time.Second)
	command := p022QueueCommand(t, authority, session, "command-p023-lifetime-lost", "submit-p023-lifetime-lost")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 11*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if !errors.Is(err, ErrStopUnconfirmed) || report.LifetimeExpired != 0 || report.SessionsLost != 1 || report.CommandsLost != 1 {
		t.Fatalf("failed lifetime report = %+v, err = %v", report, err)
	}
	lost, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || lost.State != domain.SessionStateLost {
		t.Fatalf("lost lifetime session = %+v, err = %v", lost, err)
	}
	lostCommand, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || lostCommand.State != domain.CommandStateLost {
		t.Fatalf("lost lifetime command = %+v, err = %v", lostCommand, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live command slots after failed lifetime stop = %d, err = %v, want 1", got, err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live reservations after failed lifetime stop = %d, err = %v, want 1", got, err)
	}
}

func TestP023D13LifetimeSessionTeardownFailureAfterCommandStopIsLost(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-session-stop-lost", cancelResult: RuntimeCommandStopResult{Confirmed: true}, stopConfirmed: false}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-p023-session-stop-lost", 20*time.Minute, 20*time.Minute, 10*time.Second)
	command := p022QueueCommand(t, authority, session, "command-p023-session-stop-lost", "submit-p023-session-stop-lost")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 11*time.Second)
	report, err := service.EnforcePolicies(context.Background())
	if !errors.Is(err, ErrStopUnconfirmed) || report.LifetimeExpired != 0 || report.SessionsLost != 1 {
		t.Fatalf("failed session teardown report = %+v, err = %v", report, err)
	}
	lost, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil || lost.State != domain.SessionStateLost {
		t.Fatalf("session after failed teardown = %+v, err = %v", lost, err)
	}
	timedOut, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || timedOut.State != domain.CommandStateTimedOut {
		t.Fatalf("command after failed teardown = %+v, err = %v", timedOut, err)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 0 {
		t.Fatalf("live command slots after confirmed command stop = %d, err = %v, want 0", got, err)
	}
	if got, err := authority.CountLiveSessionReservations(context.Background()); err != nil || got != 1 {
		t.Fatalf("live reservations after failed session stop = %d, err = %v, want 1", got, err)
	}
}

func TestP023D13ExpiredSessionIsNotStoppedAgain(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-p023-once", stopConfirmed: true}
	service, _, _ := newP020Service(t, runtime)
	p023ReadySession(t, service, "session-p023-once", time.Minute, 10*time.Second, 2*time.Hour)
	p023Advance(service, 10*time.Second)
	if _, err := service.EnforcePolicies(context.Background()); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, time.Hour)
	second, err := service.EnforcePolicies(context.Background())
	if err != nil || second.IdleExpired != 0 || second.LifetimeExpired != 0 || runtime.stopCall != 1 {
		t.Fatalf("second sweep = %+v, err = %v, stop calls = %d", second, err, runtime.stopCall)
	}
}

func p023ReadySession(t *testing.T, service *Service, sessionID string, commandTimeout, idleTimeout, lifetime time.Duration) store.SessionRecord {
	t.Helper()
	request := p020Request(t, sessionID, "key-"+sessionID, p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	request.RequestedLimits = domain.RequestedLimits{
		CommandTimeout:     commandTimeout,
		IdleTimeout:        idleTimeout,
		SessionMaxLifetime: lifetime,
	}
	result, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result.Session
}

func p023Advance(service *Service, delta time.Duration) {
	clock := service.clock.(*p020Clock)
	clock.now = clock.now.Add(delta)
}
