package queueworker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := New(Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("New incomplete configuration error = %v, want ErrConfiguration", err)
	}
}

func TestWorkerStartWakeStopWithoutQueuedWork(t *testing.T) {
	root := testfixture.New(t)
	database, err := store.Open(t.Context(), filepath.Join(root.Path(), "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close authority database: %v", err)
		}
	})
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry()
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.NewExecutionService(authority, workerNoopRuntime{}, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := lifecycle.NewGate()
	worker, err := New(Options{
		Service:                              service,
		Authority:                            authority,
		DispatchGate:                         gate,
		RecoveryInterval:                     time.Hour,
		RetainedLostCapacityRecoveryInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	worker.Start(t.Context())
	worker.Wake()
	worker.Stop()
	waitContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := worker.Wait(waitContext); err != nil {
		t.Fatalf("wait for queue worker: %v", err)
	}
	if err := gate.Wait(waitContext); err != nil {
		t.Fatalf("wait for dispatch gate: %v", err)
	}
	if slots, err := authority.CountLiveCommandSlots(t.Context()); err != nil || slots != 0 {
		t.Fatalf("live command slots = %d, %v; want 0, nil", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(t.Context()); err != nil || reservations != 0 {
		t.Fatalf("live session reservations = %d, %v; want 0, nil", reservations, err)
	}
}

type workerNoopRuntime struct{}

func (workerNoopRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{}, nil
}

func (workerNoopRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{}, nil
}

func (workerNoopRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error {
	return nil
}
