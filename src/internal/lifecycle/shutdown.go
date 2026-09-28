// Package lifecycle provides process-independent lifecycle coordination for
// the Mac and Linux Runner service compositions.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrAdmissionClosed means new work cannot be admitted because shutdown
	// has started.
	ErrAdmissionClosed = errors.New("work admission is closed")
	// ErrShutdownStageTimeout means a bounded shutdown stage did not finish in
	// its configured interval.
	ErrShutdownStageTimeout = errors.New("shutdown stage timed out")
)

// Gate atomically stops new work and tracks work admitted before the stop.
// Stop is idempotent. Wait also stops the gate, then waits for all previously
// admitted work to release its token.
type Gate struct {
	mu      sync.Mutex
	stopped bool
	active  int
	idle    chan struct{}
}

// NewGate creates an open gate with no active work.
func NewGate() *Gate {
	idle := make(chan struct{})
	close(idle)
	return &Gate{idle: idle}
}

// Enter admits one unit of work and returns an idempotent release function.
func (g *Gate) Enter() (func(), error) {
	if g == nil {
		return nil, ErrAdmissionClosed
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return nil, ErrAdmissionClosed
	}
	if g.active == 0 {
		g.idle = make(chan struct{})
	}
	g.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			if g.active == 0 {
				close(g.idle)
			}
		})
	}, nil
}

// Stop permanently rejects new work admitted through this gate.
func (g *Gate) Stop() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	if g.active == 0 {
		close(g.idle)
	}
}

// Wait closes the gate and waits for all admitted work to finish.
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return ErrAdmissionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.Stop()
	g.mu.Lock()
	idle := g.idle
	g.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Clock supplies timeout notifications to the coordinator. Tests can advance
// a fake clock without sleeping.
type Clock interface {
	After(time.Duration) <-chan time.Time
}

// RealClock uses wall-clock timers.
type RealClock struct{}

// After implements Clock.
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Hooks are supplied by a process composition root. Stop methods must be
// quick and idempotent. Drain waits for already admitted operations and
// dispatches. CancelRemaining must use the normal runtime/state-transition
// path and return only after its durable result is known. Flush waits for
// pending event/audit writes. CloseStreams must close each stream at a cursor
// from which its consumer can resume. Blocking hooks must honor their context.
type Hooks interface {
	StopAccepting()
	StopDispatch()
	Drain(context.Context) error
	CancelRemaining(context.Context) error
	Flush(context.Context) error
	CloseStreams(context.Context) error
}

// Config bounds draining and each subsequent cleanup stage. All durations
// must be positive; host-specific entrypoints choose values compatible with
// their service-manager stop deadline.
type Config struct {
	DrainTimeout   time.Duration
	CleanupTimeout time.Duration
}

// Coordinator runs the shared shutdown sequence at most once. If a caller's
// context ends while shutdown is in progress, that caller returns promptly;
// the coordinator still attempts bounded cancellation, flush, and stream
// closure in the background.
type Coordinator struct {
	hooks  Hooks
	clock  Clock
	config Config

	start sync.Once
	done  chan struct{}
	err   error
}

// NewCoordinator creates a one-shot shutdown coordinator.
func NewCoordinator(hooks Hooks, clock Clock, config Config) (*Coordinator, error) {
	if hooks == nil || config.DrainTimeout <= 0 || config.CleanupTimeout <= 0 {
		return nil, errors.New("invalid shutdown coordinator configuration")
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Coordinator{hooks: hooks, clock: clock, config: config, done: make(chan struct{})}, nil
}

// Shutdown freezes acceptance and dispatch, drains in-flight work up to the
// configured deadline, cancels remaining work through the normal hook when
// needed, flushes durable events/audit, then closes resumable streams.
func (c *Coordinator) Shutdown(ctx context.Context) error {
	if c == nil {
		return errors.New("shutdown coordinator is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.start.Do(func() {
		go c.run(ctx)
	})
	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) run(ctx context.Context) {
	defer close(c.done)
	c.hooks.StopAccepting()
	c.hooks.StopDispatch()

	var result []error
	drainErr := c.runStage(ctx, "drain", c.config.DrainTimeout, c.hooks.Drain)
	if drainErr != nil {
		cancelErr := c.runStage(context.Background(), "cancel remaining work", c.config.CleanupTimeout, c.hooks.CancelRemaining)
		if cancelErr != nil {
			result = append(result, drainErr, cancelErr)
		} else if !errors.Is(drainErr, ErrShutdownStageTimeout) && !errors.Is(drainErr, context.Canceled) && !errors.Is(drainErr, context.DeadlineExceeded) {
			result = append(result, fmt.Errorf("drain work: %w", drainErr))
		}
	}

	flushErr := c.runStage(context.Background(), "flush events and audit", c.config.CleanupTimeout, c.hooks.Flush)
	if flushErr != nil {
		result = append(result, flushErr)
	}
	closeErr := c.runStage(context.Background(), "close resumable streams", c.config.CleanupTimeout, c.hooks.CloseStreams)
	if closeErr != nil {
		result = append(result, closeErr)
	}
	c.err = errors.Join(result...)
}

func (c *Coordinator) runStage(ctx context.Context, name string, timeout time.Duration, run func(context.Context) error) error {
	stageContext, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- run(stageContext)
	}()
	<-started
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	case <-c.clock.After(timeout):
		cancel()
		return fmt.Errorf("%w: %s after %s", ErrShutdownStageTimeout, name, timeout)
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}
