package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG035PolicySweepRetainsLostHistoryAndEnforcesActiveLimits(t *testing.T) {
	ctx := context.Background()
	runtime := &p020FakeRuntime{generation: "generation-bug035", cancelResult: RuntimeCommandStopResult{Confirmed: false}}
	service, authority, _ := newP020Service(t, runtime)
	lost := p023ReadySession(t, service, "session-bug035-retained-lost", time.Second, 20*time.Minute, 2*time.Hour)
	lostCommand := p022QueueCommand(t, authority, lost, "command-bug035-retained-lost", "submit-bug035-retained-lost")
	if _, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 2*time.Second)
	if report, err := service.EnforcePolicies(ctx); !errors.Is(err, ErrStopUnconfirmed) || report.SessionsLost != 1 || report.CommandsLost != 1 {
		t.Fatalf("retained lost fixture = %+v, err = %v", report, err)
	}
	lostBefore, err := authority.GetSession(ctx, lost.SessionID)
	if err != nil || lostBefore.State != domain.SessionStateLost {
		t.Fatalf("lost session = %+v, err = %v", lostBefore, err)
	}
	commandBefore, err := authority.GetCommand(ctx, lostCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	reservationBefore, err := authority.GetSessionReservation(ctx, lost.SessionID)
	if err != nil || reservationBefore.CleanupConfirmedAt != nil || reservationBefore.ReleasedAt != nil {
		t.Fatalf("retained reservation = %+v, err = %v", reservationBefore, err)
	}
	slotBefore, err := authority.GetCommandSlot(ctx, lostCommand.CommandID)
	if err != nil || slotBefore.StopConfirmedAt != nil || slotBefore.ReleasedAt != nil {
		t.Fatalf("retained command slot = %+v, err = %v", slotBefore, err)
	}
	eventsBefore, err := authority.ListCommandEvents(ctx, lostCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	historyBefore, err := authority.ListSessionLifecycle(ctx, lost.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.cancelResult.Confirmed = true
	runtime.stopConfirmed = true
	p023ReadySession(t, service, "session-bug035-active-idle", time.Minute, 5*time.Second, time.Hour)
	p023ReadySession(t, service, "session-bug035-active-lifetime", time.Minute, 20*time.Minute, 5*time.Second)
	busy := p023ReadySession(t, service, "session-bug035-active-command", 5*time.Second, 20*time.Minute, 2*time.Hour)
	command := p022QueueCommand(t, authority, busy, "command-bug035-active-timeout", "submit-bug035-active-timeout")
	if _, err := authority.StartNextEligibleCommand(ctx, store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	p023Advance(service, 5*time.Second)
	report, err := service.EnforcePolicies(ctx)
	if err != nil || report.SessionsInspected != 3 || report.IdleExpired != 1 || report.LifetimeExpired != 1 || report.CommandsTimedOut != 1 || report.SessionsLost != 0 || report.CommandsLost != 0 {
		t.Fatalf("active-only sweep = %+v, err = %v", report, err)
	}
	if timedOut, err := authority.GetCommand(ctx, command.CommandID); err != nil || timedOut.State != domain.CommandStateTimedOut {
		t.Fatalf("active command = %+v, err = %v", timedOut, err)
	}
	lostAfter, err := authority.GetSession(ctx, lost.SessionID)
	if err != nil || !reflect.DeepEqual(lostAfter, lostBefore) {
		t.Fatalf("lost session changed: before=%+v after=%+v err=%v", lostBefore, lostAfter, err)
	}
	commandAfter, err := authority.GetCommand(ctx, lostCommand.CommandID)
	if err != nil || !reflect.DeepEqual(commandAfter, commandBefore) {
		t.Fatalf("lost command changed: before=%+v after=%+v err=%v", commandBefore, commandAfter, err)
	}
	reservationAfter, err := authority.GetSessionReservation(ctx, lost.SessionID)
	if err != nil || !reflect.DeepEqual(reservationAfter, reservationBefore) {
		t.Fatalf("lost reservation changed: before=%+v after=%+v err=%v", reservationBefore, reservationAfter, err)
	}
	slotAfter, err := authority.GetCommandSlot(ctx, lostCommand.CommandID)
	if err != nil || !reflect.DeepEqual(slotAfter, slotBefore) {
		t.Fatalf("lost command slot changed: before=%+v after=%+v err=%v", slotBefore, slotAfter, err)
	}
	eventsAfter, err := authority.ListCommandEvents(ctx, lostCommand.CommandID)
	if err != nil || !reflect.DeepEqual(eventsAfter, eventsBefore) {
		t.Fatalf("lost command events changed: err=%v", err)
	}
	historyAfter, err := authority.ListSessionLifecycle(ctx, lost.SessionID)
	if err != nil || !reflect.DeepEqual(historyAfter, historyBefore) {
		t.Fatalf("lost session lifecycle changed: err=%v", err)
	}
	if count, err := authority.CountLiveSessionReservations(ctx); err != nil || count != 2 {
		t.Fatalf("session reservations = %d, err = %v, want retained LOST plus ready active session", count, err)
	}
	if count, err := authority.CountLiveCommandSlots(ctx); err != nil || count != 1 {
		t.Fatalf("command slots = %d, err = %v, want retained LOST only", count, err)
	}
}

func TestBUG035CancelledPolicySweepDoesNotTouchActiveWork(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-bug035-cancel", stopConfirmed: true}
	service, authority, _ := newP020Service(t, runtime)
	session := p023ReadySession(t, service, "session-bug035-cancel", time.Minute, time.Second, time.Hour)
	p023Advance(service, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report, err := service.EnforcePolicies(ctx); !errors.Is(err, context.Canceled) || report != (PolicySweepReport{}) {
		t.Fatalf("cancelled sweep = %+v, err = %v", report, err)
	}
	if runtime.stopCall != 0 || runtime.cancelCall != 0 {
		t.Fatalf("cancelled sweep reached runtime: stop=%d cancel=%d", runtime.stopCall, runtime.cancelCall)
	}
	if got, err := authority.GetSession(context.Background(), session.SessionID); err != nil || !reflect.DeepEqual(got, session) {
		t.Fatalf("cancelled sweep changed session: got=%+v err=%v", got, err)
	}
}
