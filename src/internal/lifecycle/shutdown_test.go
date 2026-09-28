package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestP130F01ShutdownFlushesBeforeClosingResumableStreams(t *testing.T) {
	runtime := &p130FakeRuntime{cursor: 41}
	coordinator := p130Coordinator(t, runtime, RealClock{}, Config{DrainTimeout: time.Second, CleanupTimeout: time.Second})
	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	want := []string{"stop_accepting", "stop_dispatch", "drain", "flush", "close_streams"}
	if got := runtime.stages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown order = %v, want %v", got, want)
	}
	if !runtime.flushCompleted || !runtime.streamsClosed || runtime.closedCursor != 41 {
		t.Fatalf("flush=%v streams_closed=%v cursor=%d", runtime.flushCompleted, runtime.streamsClosed, runtime.closedCursor)
	}
	if runtime.cancelCalls != 0 {
		t.Fatalf("cancel calls = %d, want none after clean drain", runtime.cancelCalls)
	}
}

func TestP130D17DrainDeadlineCancelsThroughRuntimeBeforeFlush(t *testing.T) {
	clock := newP130FakeClock()
	runtime := &p130FakeRuntime{admission: NewGate(), dispatch: NewGate(), cursor: 7, drainStarted: make(chan struct{})}
	admissionRelease, err := runtime.admission.Enter()
	if err != nil {
		t.Fatal(err)
	}
	dispatchRelease, err := runtime.dispatch.Enter()
	if err != nil {
		t.Fatal(err)
	}
	runtime.releases = []func(){admissionRelease, dispatchRelease}
	const drainLimit = 37 * time.Second
	coordinator := p130Coordinator(t, runtime, clock, Config{DrainTimeout: drainLimit, CleanupTimeout: time.Second})
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- coordinator.Shutdown(context.Background()) }()

	timer := <-clock.requests
	if timer.duration != drainLimit {
		t.Fatalf("first timer duration = %s, want drain deadline %s", timer.duration, drainLimit)
	}
	<-runtime.drainStarted
	timer.channel <- time.Unix(1, 0)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown after confirmed cancellation: %v", err)
	}
	want := []string{"stop_accepting", "stop_dispatch", "drain", "cancel_remaining", "flush", "close_streams"}
	if got := runtime.stages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown order = %v, want %v", got, want)
	}
	if runtime.cancelCalls != 1 || !runtime.runtimeStopConfirmed {
		t.Fatalf("cancel calls=%d runtime stop confirmed=%v", runtime.cancelCalls, runtime.runtimeStopConfirmed)
	}
	if !runtime.noAdmissionAfterStop {
		t.Fatal("drain admitted new work after shutdown began")
	}
	if !runtime.flushCompleted || !runtime.streamsClosed || runtime.closedCursor != 7 {
		t.Fatalf("flush=%v streams_closed=%v cursor=%d", runtime.flushCompleted, runtime.streamsClosed, runtime.closedCursor)
	}
	if _, err := runtime.admission.Enter(); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("admission after shutdown = %v, want ErrAdmissionClosed", err)
	}
}

func TestP130ShutdownReportsCancellationAndFlushFailures(t *testing.T) {
	clock := newP130FakeClock()
	cancelFailure := errors.New("runtime stop was not confirmed")
	flushFailure := errors.New("audit flush failed")
	runtime := &p130FakeRuntime{
		admission: NewGate(), cancelErr: cancelFailure, flushErr: flushFailure,
		drainStarted: make(chan struct{}),
	}
	activeRelease, err := runtime.admission.Enter()
	if err != nil {
		t.Fatal(err)
	}
	defer activeRelease()
	coordinator := p130Coordinator(t, runtime, clock, Config{DrainTimeout: time.Second, CleanupTimeout: time.Second})
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- coordinator.Shutdown(context.Background()) }()
	timer := <-clock.requests
	<-runtime.drainStarted
	timer.channel <- time.Unix(2, 0)
	err = <-shutdownDone
	if !errors.Is(err, cancelFailure) || !errors.Is(err, flushFailure) {
		t.Fatalf("shutdown error = %v, want cancellation and flush errors", err)
	}
	want := []string{"stop_accepting", "stop_dispatch", "drain", "cancel_remaining", "flush", "close_streams"}
	if got := runtime.stages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown order = %v, want %v", got, want)
	}
	if runtime.runtimeStopConfirmed {
		t.Fatal("failed runtime cancellation was reported as confirmed")
	}
	if !runtime.streamsClosed {
		t.Fatal("resumable streams were not closed after a flush error")
	}
}

func TestP130GateStopsAdmissionAndWaitsForExistingWork(t *testing.T) {
	gate := NewGate()
	release, err := gate.Enter()
	if err != nil {
		t.Fatal(err)
	}
	gate.Stop()
	if _, err := gate.Enter(); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("admission after stop = %v, want ErrAdmissionClosed", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- gate.Wait(context.Background()) }()
	select {
	case err := <-waitDone:
		t.Fatalf("wait finished while work remained active: %v", err)
	default:
	}
	release()
	release()
	if err := <-waitDone; err != nil {
		t.Fatalf("wait after release: %v", err)
	}
}

func TestP130ShutdownIsIdempotent(t *testing.T) {
	runtime := &p130FakeRuntime{}
	coordinator := p130Coordinator(t, runtime, RealClock{}, Config{DrainTimeout: time.Second, CleanupTimeout: time.Second})
	for i := 0; i < 2; i++ {
		if err := coordinator.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown call %d: %v", i+1, err)
		}
	}
	if got, want := runtime.stages(), []string{"stop_accepting", "stop_dispatch", "drain", "flush", "close_streams"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks ran more than once: got %v, want %v", got, want)
	}
}

func p130Coordinator(t *testing.T, runtime Hooks, clock Clock, config Config) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(runtime, clock, config)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

type p130FakeRuntime struct {
	mu                   sync.Mutex
	stageLog             []string
	drainStarted         chan struct{}
	drainStartOnce       sync.Once
	admission            *Gate
	dispatch             *Gate
	releases             []func()
	cancelErr            error
	flushErr             error
	cursor               int64
	closedCursor         int64
	cancelCalls          int
	runtimeStopConfirmed bool
	noAdmissionAfterStop bool
	flushCompleted       bool
	streamsClosed        bool
}

func (r *p130FakeRuntime) StopAccepting() {
	r.record("stop_accepting")
	if r.admission != nil {
		r.admission.Stop()
	}
}

func (r *p130FakeRuntime) StopDispatch() {
	r.record("stop_dispatch")
	if r.dispatch != nil {
		r.dispatch.Stop()
	}
}

func (r *p130FakeRuntime) Drain(ctx context.Context) error {
	r.record("drain")
	if r.drainStarted != nil {
		r.drainStartOnce.Do(func() { close(r.drainStarted) })
	}
	for _, gate := range []*Gate{r.admission, r.dispatch} {
		if gate == nil {
			continue
		}
		if _, err := gate.Enter(); !errors.Is(err, ErrAdmissionClosed) {
			r.noAdmissionAfterStop = false
			return errors.New("shutdown gate admitted new work")
		}
		r.noAdmissionAfterStop = true
		if err := gate.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *p130FakeRuntime) CancelRemaining(context.Context) error {
	r.record("cancel_remaining")
	r.cancelCalls++
	if r.cancelErr != nil {
		return r.cancelErr
	}
	r.runtimeStopConfirmed = true
	for _, release := range r.releases {
		release()
	}
	return nil
}

func (r *p130FakeRuntime) Flush(context.Context) error {
	r.record("flush")
	if r.flushErr != nil {
		return r.flushErr
	}
	r.flushCompleted = true
	return nil
}

func (r *p130FakeRuntime) CloseStreams(context.Context) error {
	r.record("close_streams")
	r.closedCursor = r.cursor
	r.streamsClosed = true
	return nil
}

func (r *p130FakeRuntime) record(stage string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stageLog = append(r.stageLog, stage)
}

func (r *p130FakeRuntime) stages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stageLog...)
}

type p130Timer struct {
	duration time.Duration
	channel  chan time.Time
}

type p130FakeClock struct {
	requests chan p130Timer
}

func newP130FakeClock() *p130FakeClock {
	return &p130FakeClock{requests: make(chan p130Timer, 8)}
}

func (c *p130FakeClock) After(duration time.Duration) <-chan time.Time {
	timer := p130Timer{duration: duration, channel: make(chan time.Time, 1)}
	c.requests <- timer
	return timer.channel
}
