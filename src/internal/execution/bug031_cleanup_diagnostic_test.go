package execution

import (
	"context"
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type bug031TypedCleanupFailure struct{ cause error }

func (e *bug031TypedCleanupFailure) Error() string { return "private runtime cleanup details" }
func (e *bug031TypedCleanupFailure) Unwrap() error { return e.cause }

func TestBUG031LostTeardownPreservesTypedCauseAndFailedCommandHistory(t *testing.T) {
	cause := errors.New("fixture cleanup inspection denied")
	typed := &bug031TypedCleanupFailure{cause: cause}
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation: "generation-bug031-typed-cleanup", stopErr: typed,
		commandResult: RuntimeCommandResult{Stdout: []byte("failed once\n"), ExitCode: 2},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := p025Request(t, "job-bug031-typed-cleanup", "session-bug031-typed-cleanup", "command-bug031-typed-cleanup", "run-bug031-typed-cleanup", "exit 2")
	result, err := service.RunJob(context.Background(), request)
	var observed *bug031TypedCleanupFailure
	if !errors.Is(err, ErrStopUnconfirmed) || !errors.Is(err, cause) || !errors.As(err, &observed) || observed != typed {
		t.Fatalf("teardown did not retain the stop sentinel and typed cause: %v", err)
	}
	if result.Command.State != domain.CommandStateFailed || result.Command.ExitCode == nil || *result.Command.ExitCode != 2 ||
		!result.Command.OutputComplete || result.Command.OutputTruncated || result.Session.State != domain.SessionStateLost ||
		result.Job.Phase != store.JobPhaseLost || result.Job.TeardownState != store.JobTeardownLost ||
		result.Job.TeardownReason != "runtime_cleanup_unconfirmed" {
		t.Fatalf("diagnostic propagation changed truthful terminal state: %+v", result)
	}
	events, err := authority.ListCommandEvents(context.Background(), request.Acceptance.CommandID)
	if err != nil || len(events) == 0 || events[len(events)-1].Type != "command_failed" {
		t.Fatalf("failed event tail=%+v err=%v", events, err)
	}
	reservation, err := authority.GetSessionReservation(context.Background(), request.Acceptance.SessionID)
	if err != nil || reservation.ReleasedAt != nil || reservation.CleanupConfirmedAt != nil {
		t.Fatalf("diagnostic propagation released unconfirmed capacity: %+v err=%v", reservation, err)
	}
	slot, err := authority.GetCommandSlot(context.Background(), request.Acceptance.CommandID)
	if err != nil || slot.StopConfirmedAt == nil || slot.ReleasedAt == nil {
		t.Fatalf("completed command slot=%+v err=%v", slot, err)
	}
	retry, err := service.RunJob(context.Background(), request)
	if err != nil || retry.Job.Phase != store.JobPhaseLost || runtime.commandCall != 1 || runtime.stopCall != 1 {
		t.Fatalf("terminal retry replayed command/cleanup: %+v err=%v execute=%d stop=%d", retry, err, runtime.commandCall, runtime.stopCall)
	}
	after, err := authority.ListCommandEvents(context.Background(), request.Acceptance.CommandID)
	if err != nil || len(after) != len(events) || after[len(after)-1].Type != "command_failed" ||
		after[len(after)-1].Sequence != events[len(events)-1].Sequence {
		t.Fatalf("terminal retry changed event history: %+v err=%v", after, err)
	}
}
