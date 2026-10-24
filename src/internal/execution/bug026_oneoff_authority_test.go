package execution

import (
	"context"
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
)

type bug026ObservedRuntime struct {
	*p020FakeRuntime
	requests []RuntimeCommandRequest
}

func (r *bug026ObservedRuntime) ExecuteCommand(ctx context.Context, request RuntimeCommandRequest) (RuntimeCommandResult, error) {
	r.requests = append(r.requests, request)
	return r.p020FakeRuntime.ExecuteCommand(ctx, request)
}

// One-off isolation comes from the stored job binding, not an inference from
// script contents or the choice of adapter. Ordinary persistent commands keep
// their existing shared-shell execution contract.
func TestBUG026OneOffRuntimeModeComesFromDurableJob(t *testing.T) {
	t.Run("one_off_run", func(t *testing.T) {
		fake := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
			generation: "generation-bug026-oneoff", stopConfirmed: true,
			commandResult: RuntimeCommandResult{ExitCode: 1},
		}}
		service, _, _ := newP025Service(t, fake)
		observed := &bug026ObservedRuntime{p020FakeRuntime: fake.p020FakeRuntime}
		service.runtime = observed
		request := p025Request(t, "job-bug026-mode", "session-bug026-mode", "command-bug026-mode", "run-bug026-mode", "exit 1")
		result, err := service.RunJob(context.Background(), request)
		if err != nil || result.Command.State != domain.CommandStateFailed {
			t.Fatalf("one-off result=%+v err=%v", result, err)
		}
		if len(observed.requests) != 1 || !observed.requests[0].OneOff {
			t.Fatalf("runtime requests=%+v, want one durable one-off", observed.requests)
		}
	})

	t.Run("persistent_exec", func(t *testing.T) {
		fake := &p020FakeRuntime{generation: "generation-bug026-persistent", commandResult: RuntimeCommandResult{ExitCode: 1}}
		service, _, _ := newP020Service(t, fake)
		observed := &bug026ObservedRuntime{p020FakeRuntime: fake}
		service.runtime = observed
		session := p021ReadySession(t, service, "session-bug026-persistent", "key-bug026-persistent")
		if _, err := service.SubmitCommand(context.Background(), p021SubmitRequest(t, session.SessionID, "command-bug026-persistent", "submit-bug026-persistent", "exit 1")); err != nil {
			t.Fatal(err)
		}
		if len(observed.requests) != 1 || observed.requests[0].OneOff {
			t.Fatalf("runtime requests=%+v, persistent exec must not become one-off", observed.requests)
		}
	})

	t.Run("different_job_session_cannot_change_persistent_exec", func(t *testing.T) {
		fake := &p020FakeRuntime{generation: "generation-bug026-unrelated", commandResult: RuntimeCommandResult{ExitCode: 1}}
		service, authority, _ := newP020Service(t, fake)
		observed := &bug026ObservedRuntime{p020FakeRuntime: fake}
		service.runtime = observed
		session := p021ReadySession(t, service, "session-bug026-unrelated", "key-bug026-unrelated")
		request := p025Request(t, "job-bug026-other-session", "session-bug026-other-job", "command-bug026-unrelated", "run-bug026-other-job", "exit 1")
		if _, _, err := authority.AcceptJob(context.Background(), request.Acceptance); err != nil {
			t.Fatal(err)
		}
		if _, err := service.SubmitCommand(context.Background(), p021SubmitRequest(t, session.SessionID, string(request.Acceptance.CommandID), "submit-bug026-unrelated", "exit 1")); !errors.Is(err, ErrCommandNotReady) {
			t.Fatalf("mismatched durable job/session binding error=%v", err)
		}
		if len(observed.requests) != 0 {
			t.Fatalf("mismatched durable binding reached runtime: %+v", observed.requests)
		}
	})
}

// A complete one-off exit is only command proof. If adapter cleanup cannot
// prove every owned descendant gone, the session reservation must remain held.
func TestBUG026OneOffFailureDoesNotReleaseUnconfirmedCleanup(t *testing.T) {
	fake := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation: "generation-bug026-cleanup-unconfirmed", stopConfirmed: false,
		commandResult: RuntimeCommandResult{ExitCode: 1, Stderr: []byte("source_pin_mismatch\n")},
	}}
	service, authority, _ := newP025Service(t, fake)
	observed := &bug026ObservedRuntime{p020FakeRuntime: fake.p020FakeRuntime}
	service.runtime = observed
	request := p025Request(t, "job-bug026-cleanup", "session-bug026-cleanup", "command-bug026-cleanup", "run-bug026-cleanup", "sleep 60 >/dev/null 2>&1 &\nexit 1")
	result, err := service.RunJob(context.Background(), request)
	if !errors.Is(err, ErrStopUnconfirmed) || result.Command.State != domain.CommandStateFailed || result.Command.ExitCode == nil || *result.Command.ExitCode != 1 || !result.Command.OutputComplete {
		t.Fatalf("cleanup uncertainty overwrote the proven command failure: result=%+v err=%v", result, err)
	}
	if result.Job.TeardownState != "lost" || result.Job.TeardownReason != "runtime_cleanup_unconfirmed" || result.Session.State != domain.SessionStateLost {
		t.Fatalf("cleanup uncertainty was hidden: %+v", result)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 1 {
		t.Fatalf("unproven cleanup released session capacity: count=%d err=%v", reservations, err)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 0 {
		t.Fatalf("completed command slot count=%d err=%v", slots, err)
	}
	retry, err := service.RunJob(context.Background(), request)
	if err != nil || retry.Job.TeardownState != result.Job.TeardownState || retry.Command.CommandID != result.Command.CommandID || len(observed.requests) != 1 || !observed.requests[0].OneOff || fake.stopCall != 1 {
		t.Fatalf("cleanup retry replayed or altered work: result=%+v err=%v requests=%+v stop=%d", retry, err, observed.requests, fake.stopCall)
	}
}
