package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p029Runtime struct {
	mu              sync.Mutex
	generation      string
	reconcileResult RuntimeReconcileResult
	prepareCall     int
	startCall       int
	reconcileCall   int
}

type p029Publisher struct {
	mu      sync.Mutex
	records []store.SessionLifecycleRecord
}

func (p *p029Publisher) PublishSessionLifecycle(_ context.Context, record store.SessionLifecycleRecord) {
	p.mu.Lock()
	p.records = append(p.records, record)
	p.mu.Unlock()
}

func (r *p029Runtime) Prepare(context.Context, RuntimePrepareRequest) (RuntimePrepared, error) {
	r.mu.Lock()
	r.prepareCall++
	generation := r.generation
	r.mu.Unlock()
	return RuntimePrepared{RuntimeGeneration: generation}, nil
}

func (r *p029Runtime) StartAgent(context.Context, RuntimeStartRequest) (RuntimeStarted, error) {
	r.mu.Lock()
	r.startCall++
	generation := r.generation
	r.mu.Unlock()
	return RuntimeStarted{RuntimeGeneration: generation}, nil
}

func (r *p029Runtime) Cleanup(context.Context, RuntimeCleanupRequest) error { return nil }

func (r *p029Runtime) Reconcile(_ context.Context, _ RuntimeReconcileRequest) (RuntimeReconcileResult, error) {
	r.mu.Lock()
	r.reconcileCall++
	result := r.reconcileResult
	r.mu.Unlock()
	return result, nil
}

func TestP029D15ConcurrentCreatesCapReservationsAndGCTerminalMetadata(t *testing.T) {
	runtime := &p029Runtime{generation: "generation-p029-capacity"}
	service, authority, clock := newP029Service(t, runtime)
	const requests = 24
	start := make(chan struct{})
	type outcome struct {
		result CreateSessionResult
		err    error
	}
	outcomes := make(chan outcome, requests)
	var wait sync.WaitGroup
	for i := 0; i < requests; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			id := fmt.Sprintf("session-p029-cap-%02d", index)
			request := p020Request(t, id, "key-p029-cap-"+id, p020Target(t, domain.TargetKindLocal, "mac-workstation"))
			request.MaxActiveSessions = 20
			result, err := service.CreateSession(context.Background(), request)
			outcomes <- outcome{result: result, err: err}
		}(i)
	}
	close(start)
	wait.Wait()
	close(outcomes)

	var created []CreateSessionResult
	for result := range outcomes {
		if result.err != nil {
			if !errors.Is(result.err, store.ErrSessionCapacityExceeded) {
				t.Fatalf("concurrent create error = %v, want capacity exhaustion", result.err)
			}
			continue
		}
		created = append(created, result.result)
	}
	if len(created) != 20 {
		t.Fatalf("concurrent successful creates = %d, want 20", len(created))
	}
	if live, err := authority.CountLiveSessionReservations(context.Background()); err != nil || live != 20 {
		t.Fatalf("live reservations = %d err=%v, want 20", live, err)
	}

	for _, result := range created {
		if _, err := authority.TransitionSession(context.Background(), result.Session.SessionID, domain.SessionStateClosing, "close_requested"); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.TransitionSession(context.Background(), result.Session.SessionID, domain.SessionStateClosed, "runtime_closed"); err != nil {
			t.Fatal(err)
		}
		if err := authority.ConfirmSessionCleanup(context.Background(), result.Session.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(91 * 24 * time.Hour)
	report, err := authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionsDeleted != 20 || report.IdempotencyRecordsDeleted != 20 {
		t.Fatalf("capacity GC report = %+v, want 20 sessions and create keys", report)
	}
	if live, err := authority.CountLiveSessionReservations(context.Background()); err != nil || live != 0 {
		t.Fatalf("live reservations after GC = %d err=%v, want 0", live, err)
	}
}

func TestP029D17RestartGCAndConfirmedStopReleaseResidualSlot(t *testing.T) {
	runtime := &p029Runtime{generation: "generation-p029-residual", reconcileResult: RuntimeReconcileResult{
		RuntimeGeneration: "generation-p029-residual",
		CleanupConfirmed:  false,
	}}
	service, authority, clock := newP029Service(t, runtime)
	request := p020Request(t, "session-p029-residual", "key-p029-residual", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	created, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	command := p027AcceptCommand(t, authority, created.Session, "command-p029-residual", "key-p029-residual-command", "printf residual")
	if _, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	report, err := service.ReconcileStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.CommandsLost != 1 || report.CommandSlotsReleased != 0 || report.SessionReservationsReleased != 0 {
		t.Fatalf("restart report = %+v", report)
	}
	clock.Advance(91 * 24 * time.Hour)
	gc, err := authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gc.CommandsDeleted != 0 || gc.SessionsDeleted != 0 {
		t.Fatalf("residual GC deleted pinned metadata: %+v", gc)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 1 {
		t.Fatalf("residual live slots = %d err=%v, want 1", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 1 {
		t.Fatalf("residual live reservations = %d err=%v, want 1", reservations, err)
	}
	if err := authority.ConfirmCommandSlotRelease(context.Background(), command.CommandID); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmSessionCleanup(context.Background(), created.Session.SessionID); err != nil {
		t.Fatal(err)
	}
	gc, err = authority.CollectGarbage(context.Background(), store.GarbageCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gc.CommandsDeleted != 1 || gc.SessionsDeleted != 1 {
		t.Fatalf("confirmed residual GC report = %+v", gc)
	}
	if runtime.reconcileCall != 1 {
		t.Fatalf("runtime reconciliation calls = %d, want one", runtime.reconcileCall)
	}
}

func (c *p020Clock) Advance(delta time.Duration) { c.now = c.now.Add(delta) }

func newP029Service(t *testing.T, runtime *p029Runtime) (*Service, *store.AuthorityStore, *p020Clock) {
	t.Helper()
	db, err := store.Open(context.Background(), t.TempDir()+"/state/p029.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &p020Clock{now: time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(p020Environment(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, clock, &p029Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	return service, authority, clock
}
