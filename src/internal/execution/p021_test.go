package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP021D04SuccessPersistsOutputBeforeTerminalAndReleasesSlot(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-command", commandResult: RuntimeCommandResult{
		Stdout: []byte("out\n"), Stderr: []byte("err\n"), ExitCode: 0,
	}}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-command-success", "key-command-success")
	result, err := service.SubmitCommand(context.Background(), p021SubmitRequest(t, session.SessionID, "command-success", "submit-success", "printf out"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate || result.Command.State != domain.CommandStateSucceeded || result.Command.ExitCode == nil || *result.Command.ExitCode != 0 || !result.Command.OutputComplete {
		t.Fatalf("command result = %+v duplicate=%v", result.Command, result.Duplicate)
	}
	if runtime.commandCall != 1 {
		t.Fatalf("command runtime calls = %d, want 1", runtime.commandCall)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != domain.SessionStateReady {
		t.Fatalf("session state = %q, want ready", updated.State)
	}
	events, err := authority.ListCommandEvents(context.Background(), result.Command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"command_queued", "command_started", "stdout", "stderr", "command_succeeded"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %+v, want %d events", events, len(wantTypes))
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("event %d type = %q, want %q", i, events[i].Type, want)
		}
	}
	if string(events[2].Payload) != "out\n" || string(events[3].Payload) != "err\n" {
		t.Fatalf("output events = %q and %q", events[2].Payload, events[3].Payload)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 0 {
		t.Fatalf("live command slots = %d, err = %v, want 0", got, err)
	}
}

func TestP021I02NonzeroExitIsCommandFailureNotTransportFailure(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-failed", commandResult: RuntimeCommandResult{Stdout: []byte("before-failure"), ExitCode: 7}}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-command-failed", "key-command-failed")
	result, err := service.SubmitCommand(context.Background(), p021SubmitRequest(t, session.SessionID, "command-failed", "submit-failed", "false"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Command.State != domain.CommandStateFailed || result.Command.ExitCode == nil || *result.Command.ExitCode != 7 {
		t.Fatalf("command result = %+v, want failed exit 7", result.Command)
	}
	if result.Command.OutputComplete != true {
		t.Fatal("nonzero shell exit did not retain complete output")
	}
	if sessionAfter, err := service.GetSession(context.Background(), session.SessionID, session.Controller); err != nil || sessionAfter.State != domain.SessionStateReady {
		t.Fatalf("session after nonzero exit = %+v, err = %v", sessionAfter, err)
	}
	if events, err := authority.ListCommandEvents(context.Background(), result.Command.CommandID); err != nil || events[len(events)-1].Type != "command_failed" {
		t.Fatalf("terminal events = %+v, err = %v", events, err)
	}
}

func TestP021D11ShellExitMarksCommandAndSessionLostAndRetainsSlot(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-shell-exit", commandResult: RuntimeCommandResult{
		Stdout: []byte("partial"), ExitCode: 0, ShellExited: true,
	}}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-shell-exit", "key-shell-exit")
	result, err := service.SubmitCommand(context.Background(), p021SubmitRequest(t, session.SessionID, "command-shell-exit", "submit-shell-exit", "exit"))
	if !errors.Is(err, ErrShellExited) {
		t.Fatalf("submit error = %v, want ErrShellExited", err)
	}
	if result.Command.State != domain.CommandStateLost || result.Command.OutputComplete {
		t.Fatalf("command result = %+v, want lost/incomplete", result.Command)
	}
	updated, err := service.GetSession(context.Background(), session.SessionID, session.Controller)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != domain.SessionStateLost {
		t.Fatalf("session state = %q, want lost", updated.State)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live command slots = %d, err = %v, want retained 1", got, err)
	}
	if events, err := authority.ListCommandEvents(context.Background(), result.Command.CommandID); err != nil || events[len(events)-1].Type != "command_lost" {
		t.Fatalf("terminal events = %+v, err = %v", events, err)
	}
}

func TestP021I02TransportFailureIsDistinctAndDoesNotRerun(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-transport", commandErr: errors.New("bridge disconnected")}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-transport", "key-transport")
	request := p021SubmitRequest(t, session.SessionID, "command-transport", "submit-transport", "echo transport")
	result, err := service.SubmitCommand(context.Background(), request)
	if !errors.Is(err, ErrCommandTransport) {
		t.Fatalf("submit error = %v, want ErrCommandTransport", err)
	}
	if result.Command.State != domain.CommandStateLost {
		t.Fatalf("command result = %+v, want lost", result.Command)
	}
	if runtime.commandCall != 1 {
		t.Fatalf("command runtime calls = %d, want one", runtime.commandCall)
	}
	if got, err := authority.CountLiveCommandSlots(context.Background()); err != nil || got != 1 {
		t.Fatalf("live command slots = %d, err = %v, want 1", got, err)
	}
}

func TestP021DuplicateSubmitReturnsOriginalWithoutSecondExecution(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-duplicate", commandResult: RuntimeCommandResult{Stdout: []byte("once"), ExitCode: 0}}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-duplicate", "key-duplicate-session")
	request := p021SubmitRequest(t, session.SessionID, "command-duplicate", "submit-duplicate", "echo once")
	first, err := service.SubmitCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SubmitCommand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Command.CommandID != first.Command.CommandID || second.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("duplicate result = %+v duplicate=%v", second.Command, second.Duplicate)
	}
	if runtime.commandCall != 1 {
		t.Fatalf("command runtime calls = %d, want 1", runtime.commandCall)
	}
	if events, err := authority.ListCommandEvents(context.Background(), first.Command.CommandID); err != nil || len(events) != 4 {
		t.Fatalf("duplicate event count = %d, err = %v, want 4", len(events), err)
	}
}

func TestP021PreReadySubmitLeavesNoCommand(t *testing.T) {
	runtime := &p020FakeRuntime{generation: "generation-not-ready", prepareErr: errors.New("creation failed")}
	service, authority, _ := newP020Service(t, runtime)
	create := p020Request(t, "session-not-ready", "key-not-ready", p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	created, err := service.CreateSession(context.Background(), create)
	if !errors.Is(err, ErrRuntimeUnavailable) || created.Session.State != domain.SessionStateFailed {
		t.Fatalf("create result = %+v, err = %v", created.Session, err)
	}
	_, err = service.SubmitCommand(context.Background(), p021SubmitRequest(t, create.SessionID, "command-not-ready", "submit-not-ready", "echo no"))
	if !errors.Is(err, ErrSessionNotReady) {
		t.Fatalf("submit error = %v, want ErrSessionNotReady", err)
	}
	if _, err := authority.GetCommand(context.Background(), domain.CommandID("command-not-ready")); !errors.Is(err, store.ErrCommandNotFound) {
		t.Fatalf("command lookup = %v, want not found", err)
	}
}

func p021ReadySession(t *testing.T, service *Service, sessionID, key string) store.SessionRecord {
	t.Helper()
	request := p020Request(t, sessionID, key, p020Target(t, domain.TargetKindLocal, "mac-workstation"))
	result, err := service.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result.Session
}

func p021SubmitRequest(t *testing.T, sessionID domain.SessionID, commandID, key, script string) SubmitCommandRequest {
	t.Helper()
	raw := []byte(fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":%q}`, sessionID, script))
	hash, err := domain.HashMutationRequestJSON("submit_command", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return SubmitCommandRequest{
		CommandID:      domain.CommandID(commandID),
		SessionID:      sessionID,
		Controller:     p020Controller(t, domain.ControllerTypeLocalUser),
		IdempotencyKey: key,
		RequestHash:    hash,
		Script:         script,
	}
}
