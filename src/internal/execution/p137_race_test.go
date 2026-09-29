package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

type p137CompletionRaceRuntime struct {
	p020FakeRuntime
	executionStarted chan struct{}
	finishExecution  chan struct{}
	cancelStarted    chan struct{}
	finishCancel     chan struct{}
}

func (r *p137CompletionRaceRuntime) ExecuteCommand(context.Context, RuntimeCommandRequest) (RuntimeCommandResult, error) {
	close(r.executionStarted)
	<-r.finishExecution
	return RuntimeCommandResult{Stdout: []byte("completed"), ExitCode: 0}, nil
}

func (r *p137CompletionRaceRuntime) CancelCommand(context.Context, RuntimeCommandRequest) (RuntimeCommandStopResult, error) {
	close(r.cancelStarted)
	<-r.finishCancel
	return RuntimeCommandStopResult{Confirmed: false}, nil
}

func TestP137R03RunningCompletionWinsConcurrentCancellation(t *testing.T) {
	runtime := &p137CompletionRaceRuntime{
		p020FakeRuntime:  p020FakeRuntime{generation: "generation-p137-race"},
		executionStarted: make(chan struct{}), finishExecution: make(chan struct{}),
		cancelStarted: make(chan struct{}), finishCancel: make(chan struct{}),
	}
	service, authority := newP137CompletionRaceService(t, runtime)
	session := p021ReadySession(t, service, "session-p137-completion-race", "key-p137-completion-race")
	command := p022QueueCommand(t, authority, session, "command-p137-completion-race", "submit-p137-completion-race")

	type result struct {
		command SubmitCommandResult
		err     error
	}
	runDone := make(chan result, 1)
	go func() {
		completed, err := service.ResumeCommand(context.Background(), command.CommandID, session.Controller)
		runDone <- result{command: completed, err: err}
	}()
	select {
	case <-runtime.executionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime command did not reach the controlled completion barrier")
	}

	cancelDone := make(chan result, 1)
	cancelRequest := p022CancelRequest(t, command.CommandID, "cancel-p137-completion-race", "cancel while completing")
	go func() {
		cancelled, err := service.CancelCommand(context.Background(), cancelRequest)
		cancelDone <- result{command: SubmitCommandResult{Command: cancelled.Command}, err: err}
	}()
	select {
	case <-runtime.cancelStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach the controlled stop barrier")
	}
	close(runtime.finishExecution)

	var completed result
	select {
	case completed = <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("running command did not commit its normal result")
	}
	if completed.err != nil || completed.command.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("running result=%+v err=%v, want succeeded", completed.command.Command, completed.err)
	}
	close(runtime.finishCancel)

	var cancelled result
	select {
	case cancelled = <-cancelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return after the competing result committed")
	}
	if cancelled.err != nil || cancelled.command.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("cancel race winner=%+v err=%v, want the preserved succeeded result", cancelled.command.Command, cancelled.err)
	}
	stored, err := authority.GetCommand(context.Background(), command.CommandID)
	if err != nil || stored.State != domain.CommandStateSucceeded || !stored.OutputComplete {
		t.Fatalf("stored command=%+v err=%v, want one complete success", stored, err)
	}
	events, err := authority.ListCommandEvents(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events {
		switch event.Type {
		case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
			terminal++
		}
	}
	if terminal != 1 || len(events) == 0 || events[len(events)-1].Type != "command_succeeded" {
		t.Fatalf("terminal events=%+v, want one command_succeeded event", events)
	}
	if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 0 {
		t.Fatalf("live command slots=%d err=%v, want the successful command slot released", slots, err)
	}
}

func newP137CompletionRaceService(t *testing.T, runtime SessionRuntime) (*Service, *store.AuthorityStore) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	databaseDirectory := filepath.Join(root, "state")
	if err := os.Mkdir(databaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(databaseDirectory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := &p020Clock{now: time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)}
	authority, err := store.NewAuthorityStoreWithClock(db, now.Now)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewEnvironmentRegistry(p020Environment(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(authority, runtime, registry, now, &p020Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	return service, authority
}
