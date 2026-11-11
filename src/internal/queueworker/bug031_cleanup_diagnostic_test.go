package queueworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type bug031WorkerRuntime struct {
	stopErr                    error
	commandCalls, cleanupCalls int
}

type bug031CleanupDiagnosticError struct {
	diagnostic domain.RuntimeCleanupDiagnostic
}

func (*bug031CleanupDiagnosticError) Error() string {
	return "private-cleanup-error-token /private/secret script credential"
}
func (*bug031CleanupDiagnosticError) Unwrap() error { return execution.ErrRuntimeUnavailable }
func (e *bug031CleanupDiagnosticError) RuntimeCleanupDiagnostic() domain.RuntimeCleanupDiagnostic {
	return e.diagnostic
}

func bug031ValidCleanupDiagnostic() domain.RuntimeCleanupDiagnostic {
	return domain.RuntimeCleanupDiagnostic{
		Stage: "group_wait", Code: "group_still_runnable", RootPID: 54321,
		RootStartIdentity: "1794346826034072", ProcessGroupID: 54321, OwnerUID: 502,
		ElapsedMS: 500, GraceMS: 500, SnapshotCount: 51, MemberCount: 1, RunnableCount: 1,
		ObservedStartedAt: "2026-11-11T00:20:42Z", ObservedEndedAt: "2026-11-11T00:20:42.5Z", DeadlineAt: "2026-11-11T00:20:42.5Z",
		Members: []domain.RuntimeCleanupMember{{PID: 54322, ParentPID: 1, ProcessGroupID: 54321, UID: 502, Status: 1, ProcessStartIdentity: "1794346826034073"}},
	}
}

func (*bug031WorkerRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: "generation-bug031-worker"}, nil
}

func (*bug031WorkerRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: "generation-bug031-worker"}, nil
}

func (*bug031WorkerRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error {
	return errors.New("unexpected pre-start cleanup")
}

func (r *bug031WorkerRuntime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.commandCalls++
	return execution.RuntimeCommandResult{Stdout: []byte("ordinary failure\n"), ExitCode: 2}, nil
}

func (*bug031WorkerRuntime) CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	return execution.RuntimeCommandStopResult{}, errors.New("unexpected command cancellation")
}

func (r *bug031WorkerRuntime) StopSession(context.Context, store.SessionRecord) (bool, error) {
	r.cleanupCalls++
	return false, r.stopErr
}

func bug031DiagnosticWorker(t *testing.T, runtime *bug031WorkerRuntime, output *bytes.Buffer) (*Worker, *store.AuthorityStore, store.JobRecord) {
	t.Helper()
	root := testfixture.New(t)
	database, err := store.Open(t.Context(), filepath.Join(root.Path(), "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
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
		Name: "mac-dev", HostClass: "Mac local process", EffectiveAccount: "fixture-owner",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const script = "stored-script-secret-must-not-enter-cleanup-logs"
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptJob(t.Context(), store.JobAcceptance{
		JobID: "job-bug031-worker", SessionID: "sess-bug031-worker", CommandID: "cmd-bug031-worker",
		Controller: controller, IdempotencyKey: "run-bug031-worker", RequestHash: hash,
		Environment: environment.Name(), Target: target, Source: domain.NewEmptySource(), Script: script,
		CanonicalPayload: canonical, IdempotencyRetention: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := New(Options{
		Service: service, Authority: authority, DispatchGate: lifecycle.NewGate(),
		Logger: slog.New(slog.NewJSONHandler(output, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker, authority, accepted.Job
}

func TestBUG031TerminalCompletedTeardownFailureEmitsOneSanitizedDiagnostic(t *testing.T) {
	var output bytes.Buffer
	runtime := &bug031WorkerRuntime{stopErr: &bug031CleanupDiagnosticError{diagnostic: bug031ValidCleanupDiagnostic()}}
	worker, authority, job := bug031DiagnosticWorker(t, runtime, &output)
	worker.resumeStoredJobNow(job.JobID)
	claim, err := authority.StartNextDispatchableCommand(t.Context(), store.DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	worker.executeClaim(claim, func() {})
	worker.resumeStoredJobNow(job.JobID)
	worker.resumeStoredJobNow(job.JobID)
	logged := output.String()
	if count := strings.Count(logged, "Mac local completed-command teardown failed"); count != 1 {
		t.Fatalf("cleanup diagnostic count=%d, want exactly one: %s", count, logged)
	}
	for _, expected := range []string{"job-bug031-worker", "sess-bug031-worker", "cmd-bug031-worker", "runtime_cleanup_unconfirmed",
		"generation-bug031-worker", `"diagnostic_available":true`, `"failure_stage":"group_wait"`, `"error_category":"group_still_runnable"`,
		`"root_pid":54321`, `"root_start_identity":"1794346826034072"`, `"owner_uid":502`, `"member_count":1`, `"pid":54322`,
		`"observed_started_at":"2026-11-11T00:20:42Z"`, `"observed_ended_at":"2026-11-11T00:20:42.5Z"`, `"deadline_at":"2026-11-11T00:20:42.5Z"`} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("cleanup diagnostic lacks %q: %s", expected, logged)
		}
	}
	if strings.Contains(logged, "Mac local empty-source startup failed") {
		t.Fatalf("cleanup diagnostic was also routed through the raw startup-error logger: %s", logged)
	}
	for _, forbidden := range []string{runtime.stopErr.Error(), "private-cleanup-error-token", "/private/secret", "stored-script-secret", "credential"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("cleanup diagnostic leaked %q: %s", forbidden, logged)
		}
	}
	if len(logged) > 16000 {
		t.Fatalf("cleanup diagnostic exceeded bounded size: %d", len(logged))
	}
	after, err := authority.GetJob(t.Context(), job.JobID)
	if err != nil || after.Phase != store.JobPhaseLost || after.TeardownState != store.JobTeardownLost ||
		after.CommandState == nil || *after.CommandState != domain.CommandStateFailed || after.ExitCode == nil || *after.ExitCode != 2 {
		t.Fatalf("terminal job changed: %+v err=%v", after, err)
	}
	if runtime.commandCalls != 1 || runtime.cleanupCalls != 1 {
		t.Fatalf("terminal command/cleanup replayed: execute=%d cleanup=%d", runtime.commandCalls, runtime.cleanupCalls)
	}
	if holds, err := authority.CountLiveSessionReservations(t.Context()); err != nil || holds != 1 {
		t.Fatalf("unconfirmed session capacity changed: %d err=%v", holds, err)
	}
	if slots, err := authority.CountLiveCommandSlots(t.Context()); err != nil || slots != 0 {
		t.Fatalf("completed command slot changed: %d err=%v", slots, err)
	}
}

func TestBUG031CleanupDiagnosticRejectsUnexpectedMetadataAndBoundsIdentities(t *testing.T) {
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "fixture-owner")
	if err != nil {
		t.Fatal(err)
	}
	failed := domain.CommandStateFailed
	job := store.JobRecord{
		JobID: "job-bug031-sanitize", SessionID: "sess-bug031-sanitize", CommandID: "cmd-bug031-sanitize",
		Controller: controller, Target: target, Phase: store.JobPhaseLost, CommandState: &failed,
		TeardownState: store.JobTeardownLost, TeardownReason: "runtime_cleanup_unconfirmed",
	}
	session := store.SessionRecord{SessionID: job.SessionID, RuntimeGeneration: "generation-bug031-sanitize"}
	for name, mutate := range map[string]func(*domain.RuntimeCleanupDiagnostic){
		"unexpected category":      func(d *domain.RuntimeCleanupDiagnostic) { d.Code = "private-metadata-secret" },
		"unexpected stage":         func(d *domain.RuntimeCleanupDiagnostic) { d.Stage = "private-metadata-secret" },
		"unexpected root identity": func(d *domain.RuntimeCleanupDiagnostic) { d.RootStartIdentity = "private-metadata-secret" },
		"unexpected member identity": func(d *domain.RuntimeCleanupDiagnostic) {
			d.Members[0].ProcessStartIdentity = "private-metadata-secret"
		},
		"unbounded members": func(d *domain.RuntimeCleanupDiagnostic) {
			d.Members = make([]domain.RuntimeCleanupMember, domain.MaxRuntimeCleanupDiagnosticMembers+1)
			d.MemberCount, d.RunnableCount = len(d.Members), len(d.Members)
		},
		"invalid counters":     func(d *domain.RuntimeCleanupDiagnostic) { d.ElapsedMS = -1 },
		"unexpected timestamp": func(d *domain.RuntimeCleanupDiagnostic) { d.ObservedEndedAt = "private-metadata-secret" },
	} {
		t.Run(name, func(t *testing.T) {
			diagnostic := bug031ValidCleanupDiagnostic()
			mutate(&diagnostic)
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			cause := &bug031CleanupDiagnosticError{diagnostic: diagnostic}
			if !logMacLocalCompletedTeardownFailure(logger, job, session, fmt.Errorf("%w: %w", execution.ErrStopUnconfirmed, cause)) {
				t.Fatal("lost teardown was not classified for diagnostics")
			}
			var row map[string]any
			if err := json.Unmarshal(output.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if row["diagnostic_available"] != false || row["error_category"] != "diagnostic_unavailable" || row["diagnostic"] != nil {
				t.Fatalf("invalid metadata was emitted: %s", output.String())
			}
			for _, secret := range []string{"private-metadata-secret", cause.Error(), "private-cleanup-error-token", "root_pid", "members"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("fallback leaked invalid metadata %q: %s", secret, output.String())
				}
			}
		})
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	unsafe := job
	unsafe.JobID = domain.JobID(strings.Repeat("private-job-secret", 100))
	unsafe.SessionID = domain.SessionID("sess-\nprivate-session-secret")
	unsafe.CommandID = domain.CommandID("cmd-/private/command-secret")
	session.SessionID, session.RuntimeGeneration = unsafe.SessionID, strings.Repeat("private-generation-secret", 100)
	if !logMacLocalCompletedTeardownFailure(logger, unsafe, session, fmt.Errorf("%w: %w", execution.ErrStopUnconfirmed, errors.New("private-untyped-error-secret"))) {
		t.Fatal("untyped teardown failure was not logged")
	}
	if !strings.Contains(output.String(), `"diagnostic_available":false`) || len(output.String()) > 1500 {
		t.Fatalf("untyped diagnostic fallback not bounded: %s", output.String())
	}
	for _, secret := range []string{"private-job-secret", "private-session-secret", "private-command-secret", "private-generation-secret", "private-untyped-error-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("diagnostic leaked unbounded identity/error %q: %s", secret, output.String())
		}
	}
	output.Reset()
	remote := job
	remote.Target, err = domain.NewExecutionTarget(domain.TargetKindRemote, "sandbox-host")
	if err != nil {
		t.Fatal(err)
	}
	if logMacLocalCompletedTeardownFailure(logger, remote, session, execution.ErrStopUnconfirmed) || output.Len() != 0 {
		t.Fatal("Mac-local teardown diagnostic logged remote work")
	}
	if logMacLocalCompletedTeardownFailure(logger, job, session, errors.New("unrelated error")) || output.Len() != 0 {
		t.Fatal("Mac-local teardown diagnostic logged unrelated failure")
	}
}
