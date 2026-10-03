package execution

import (
	"context"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// TestBUG013SuccessfulStatusAndPostMarkerCloseOneOff keeps the execution
// contract separate from the real-shell capture fixture: once a runtime has
// a complete zero-exit status/marker result, one-off coordination must record
// succeeded output and a closed teardown.
func TestBUG013SuccessfulStatusAndPostMarkerCloseOneOff(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-bug013-success-status",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{
			Stdout:   []byte("GIT_PUSH_DRY_RUN_WITH_EXPLICIT_KEY_OK\n"),
			Stderr:   []byte("Everything up-to-date\n"),
			ExitCode: 0,
		},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-bug013-success-status", "session-bug013-success-status", "command-bug013-success-status", "run-bug013-success-status", "set -euo pipefail\nprintf '%s\\n' 'Everything up-to-date' >&2\nprintf '%s\\n' 'GIT_PUSH_DRY_RUN_WITH_EXPLICIT_KEY_OK'")

	result, err := service.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Phase != store.JobPhaseComplete || result.Session.State != domain.SessionStateClosed || result.Job.TeardownState != store.JobTeardownClosed || result.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("successful status one-off result = %+v", result)
	}
	if result.Command.ExitCode == nil || *result.Command.ExitCode != 0 || !result.Command.OutputComplete || result.Command.OutputTruncated {
		t.Fatalf("successful status command result = %+v", result.Command)
	}
	events, err := authority.ListCommandEvents(context.Background(), result.Command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 || events[0].Type != "command_queued" || events[1].Type != "command_started" || events[2].Type != "stdout" || events[3].Type != "stderr" || events[4].Type != "command_succeeded" {
		t.Fatalf("successful status events = %+v", events)
	}
	if runtime.commandCall != 1 || len(runtime.scripts) != 1 || runtime.scripts[0] != request.Acceptance.Script {
		t.Fatalf("successful status runtime calls=%d scripts=%q", runtime.commandCall, runtime.scripts)
	}
}
