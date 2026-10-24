//go:build darwin

package runnerlocald

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// This runs only disposable Bash processes and a temporary database. It never
// contacts installed Runner services, clients, repositories, or mailboxes.
func TestBUG026MacOneOffExplicitPreflightFailureCompletesAndDoesNotReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaces, 0o700); err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewMacProcessAdapter(hostruntime.MacRuntimeOptions{
		Account: current.Username, WorkspaceRoot: workspaces, ShellPath: "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewMacSessionRuntime(adapter)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, filepath.Join(root, "state", "bug026.db"))
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
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID(current.Username))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "mac", EffectiveAccount: current.Username,
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

	const sessionID = domain.SessionID("session-bug026-mac-preflight")
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = adapter.CleanupContext(cleanup, string(sessionID))
	})
	invocations := filepath.Join(root, "invocations")
	pytestMarker := filepath.Join(root, "pytest-started")
	script := fmt.Sprintf("set -euo pipefail\nprintf 'attempt\\n' >> %q\nprintf 'preflight_started\\n'\nactual=actual\nexpected=wrong\nif [[ \"$actual\" != \"$expected\" ]]; then\n  printf 'source_pin_mismatch expected=%%s actual=%%s\\n' \"$expected\" \"$actual\" >&2\n  exit 1\nfi\nprintf 'pytest_would_start\\n' > %q\n", invocations, pytestMarker)
	payload, err := json.Marshal(map[string]any{
		"operation": "run", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source":           map[string]string{"mode": "empty"}, "script": script,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := execution.RunJobRequest{Acceptance: store.JobAcceptance{
		JobID: "job-bug026-mac-preflight", SessionID: sessionID, CommandID: "command-bug026-mac-preflight",
		Controller: controller, IdempotencyKey: "run-bug026-mac-preflight", RequestHash: hash,
		Environment: "mac-dev", Target: target, Source: domain.NewEmptySource(), Script: script,
		CanonicalPayload: canonical, IdempotencyRetention: time.Hour,
	}, MaxActiveSessions: store.DefaultActiveSessionLimit, IdempotencyRetention: time.Hour}

	result, err := service.RunJob(ctx, request)
	if err != nil {
		t.Fatalf("one-off explicit exit error=%v result=%+v", err, result)
	}
	if result.Job.Phase != store.JobPhaseComplete || result.Job.TeardownState != store.JobTeardownClosed || result.Session.State != domain.SessionStateClosed {
		t.Fatalf("one-off did not complete confirmed teardown: %+v", result)
	}
	if result.Command.State != domain.CommandStateFailed || result.Command.ExitCode == nil || *result.Command.ExitCode != 1 || !result.Command.OutputComplete || result.Command.OutputTruncated || result.Command.OutputUnavailableReason != "" {
		t.Fatalf("explicit exit did not preserve failed/complete output: %+v", result.Command)
	}
	if result.Job.CommandState == nil || *result.Job.CommandState != domain.CommandStateFailed || result.Job.ExitCode == nil || *result.Job.ExitCode != 1 || !result.Job.OutputComplete {
		t.Fatalf("job did not preserve command failure: %+v", result.Job)
	}
	events, err := authority.ListCommandEvents(ctx, result.Command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	for _, event := range events {
		switch event.Type {
		case "stdout":
			stdout.Write(event.Payload)
		case "stderr":
			stderr.Write(event.Payload)
		case "command_lost":
			t.Fatalf("explicit exit unexpectedly recorded lost: %+v", events)
		}
	}
	if stdout.String() != "preflight_started\n" || stderr.String() != "source_pin_mismatch expected=wrong actual=actual\n" || len(events) == 0 || events[len(events)-1].Type != "command_failed" {
		t.Fatalf("preflight output stdout=%q stderr=%q events=%+v", stdout.String(), stderr.String(), events)
	}
	if _, err := os.Stat(pytestMarker); !os.IsNotExist(err) {
		t.Fatalf("post-preflight test marker exists or cannot be checked: %v", err)
	}
	if metrics, err := authority.ReadRestartPreflightMetrics(ctx); err != nil || metrics != (store.RestartPreflightMetrics{}) {
		t.Fatalf("one-off retained work or capacity: metrics=%+v err=%v", metrics, err)
	}

	retry, err := service.RunJob(ctx, request)
	if err != nil || !retry.Duplicate || retry.Job.Phase != store.JobPhaseComplete || retry.Command.CommandID != result.Command.CommandID || retry.Command.ExitCode == nil || *retry.Command.ExitCode != 1 {
		t.Fatalf("same request changed completed result: %+v err=%v", retry, err)
	}
	if attempts, err := os.ReadFile(invocations); err != nil || string(attempts) != "attempt\n" {
		t.Fatalf("same request replayed execution: attempts=%q err=%v", attempts, err)
	}
	if retryEvents, err := authority.ListCommandEvents(ctx, result.Command.CommandID); err != nil || len(retryEvents) != len(events) {
		t.Fatalf("same request changed terminal event history: events=%+v err=%v", retryEvents, err)
	}
}
