package queueworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
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
	var logOutput bytes.Buffer
	worker, err := New(Options{
		Service:                              service,
		Authority:                            authority,
		DispatchGate:                         gate,
		RecoveryInterval:                     time.Hour,
		RetainedLostCapacityRecoveryInterval: time.Hour,
		Logger:                               slog.New(slog.NewTextHandler(&logOutput, nil)),
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
	if strings.Contains(logOutput.String(), "queue worker could not claim command") {
		t.Fatalf("normal empty-queue result emitted a claim failure diagnostic: %q", logOutput.String())
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

type workerMacStartupFailureRuntime struct{ prepareCalls int }

func (r *workerMacStartupFailureRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	r.prepareCalls++
	return execution.RuntimePrepared{}, fmt.Errorf("%w: fixture workspace startup rejected", execution.ErrRuntimeUnavailable)
}

func (*workerMacStartupFailureRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{}, errors.New("unexpected StartAgent call")
}

func (*workerMacStartupFailureRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error {
	return errors.New("unexpected Cleanup call")
}

func TestMacLocalOneOffStartupFailureIsLoggedAndNotRetried(t *testing.T) {
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
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "fixture-owner")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name:               "mac-workstation-dev",
		HostClass:          "Mac local process",
		EffectiveAccount:   "fixture-owner",
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
	runtime := &workerMacStartupFailureRuntime{}
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const script = "script payload must not appear in startup diagnostics"
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"mac-workstation-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptJob(audit.WithIngress(t.Context(), audit.IngressLocalWorker), store.JobAcceptance{
		JobID: "job-mac-startup-diagnostic", SessionID: "sess-mac-startup-diagnostic", CommandID: "cmd-mac-startup-diagnostic",
		Controller: controller, IdempotencyKey: "run-mac-startup-diagnostic", RequestHash: hash,
		Environment: environment.Name(), Target: target, Source: domain.NewEmptySource(), Script: script,
		CanonicalPayload: canonical, IdempotencyRetention: time.Hour,
	})
	if err != nil || accepted.Job.Phase != store.JobPhaseCreatingSession {
		t.Fatalf("accept one-off job=%+v err=%v", accepted.Job, err)
	}

	var logOutput bytes.Buffer
	worker, err := New(Options{
		Service: service, Authority: authority, DispatchGate: lifecycle.NewGate(),
		Logger: slog.New(slog.NewTextHandler(&logOutput, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	worker.resumeStoredJobNow(accepted.Job.JobID)
	worker.resumeStoredJobNow(accepted.Job.JobID)
	logged := logOutput.String()
	for _, expected := range []string{"Mac local empty-source startup failed", "job_id=job-mac-startup-diagnostic", "session_id=sess-mac-startup-diagnostic", "reason=runtime_unavailable", "fixture workspace startup rejected"} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("startup diagnostic %q does not contain %q", logged, expected)
		}
	}
	if strings.Contains(logged, script) {
		t.Fatalf("startup diagnostic exposed the stored script payload: %q", logged)
	}
	if runtime.prepareCalls != 1 {
		t.Fatalf("terminal pre-start failure was retried: prepare calls=%d, want 1", runtime.prepareCalls)
	}
	job, err := authority.GetJob(t.Context(), accepted.Job.JobID)
	if err != nil || job.Phase != store.JobPhaseFailed {
		t.Fatalf("terminal job=%+v err=%v", job, err)
	}
	if _, err := authority.GetCommand(t.Context(), accepted.Job.CommandID); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("pre-start failure created a command: %v", err)
	}
	if slots, err := authority.CountLiveCommandSlots(t.Context()); err != nil || slots != 0 {
		t.Fatalf("live command slots=%d err=%v, want zero", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(t.Context()); err != nil || reservations != 0 {
		t.Fatalf("live session reservations=%d err=%v, want zero", reservations, err)
	}
}
