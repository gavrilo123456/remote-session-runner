package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP017AtomicCommandEventsReplayAndTerminalBookkeeping(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/events.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p017-events", "key-p017-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: "command-p017-events", SessionID: created.SessionID, IdempotencyKey: "key-p017-command", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p017-events","script":"printf data"}`), Script: "printf data", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if running, err := store.GetCommand(context.Background(), command.CommandID); err != nil || running.State != domain.CommandStateRunning || running.FinalEventSequence != nil {
		t.Fatalf("running command = %+v err=%v", running, err)
	}
	payload := []byte{0x00, 0xff, 0x01}
	if event, err := store.AppendCommandEvent(context.Background(), CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: payload, ByteCount: int64(len(payload))}); err != nil || event.Sequence != 3 || string(event.Payload) != string(payload) {
		t.Fatalf("stdout event = %+v err=%v", event, err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	terminal, err := store.GetCommand(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State != domain.CommandStateSucceeded || terminal.FinalEventSequence == nil || *terminal.FinalEventSequence != 4 || !terminal.OutputComplete || terminal.OutputTruncated {
		t.Fatalf("terminal command = %+v", terminal)
	}
	events, err := store.ListCommandEvents(context.Background(), command.CommandID)
	if err != nil || len(events) != 4 || events[0].Sequence != 1 || events[0].Type != "command_queued" || events[1].Type != "command_started" || events[2].Type != "stdout" || events[3].Type != "command_succeeded" {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	replay, err := store.ReplayCommandEvents(context.Background(), command.CommandID, 2)
	if err != nil || len(replay) != 2 || replay[0].Sequence != 3 || replay[1].Sequence != 4 {
		t.Fatalf("replay = %+v err=%v", replay, err)
	}
	if replay, err := store.ReplayCommandEvents(context.Background(), command.CommandID, 4); err != nil || len(replay) != 0 {
		t.Fatalf("tail replay = %+v err=%v", replay, err)
	}
	if _, err := store.AppendCommandEvent(context.Background(), CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("late"), ByteCount: 4}); !errors.Is(err, ErrCommandTerminal) {
		t.Fatalf("append after terminal error = %v", err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateFailed}); !errors.Is(err, ErrCommandTransition) {
		t.Fatalf("second terminal transition error = %v", err)
	}
}

func TestP017FailedEventAppendRollsBackStateAndGapFailsReplay(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/rollback.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p017-gap", "key-p017-gap-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: "command-p017-gap", SessionID: created.SessionID, IdempotencyKey: "key-p017-gap-command", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p017-gap","script":"echo gap"}`), Script: "echo gap", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE exec_command_events SET sequence = 4 WHERE command_id = ? AND sequence = 2", string(command.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateFailed, ExitCode: intPointer(1), OutputComplete: false}); !errors.Is(err, ErrCommandReplayGap) {
		t.Fatalf("gapped transition error = %v", err)
	}
	unchanged, err := store.GetCommand(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.State != domain.CommandStateRunning || unchanged.FinalEventSequence != nil {
		t.Fatalf("state changed after failed append = %+v", unchanged)
	}
	if _, err := store.ReplayCommandEvents(context.Background(), command.CommandID, 0); !errors.Is(err, ErrCommandReplayGap) {
		t.Fatalf("gap replay error = %v", err)
	}
}

func TestP017CancellingStateClosesWithOneTerminalEvent(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/cancelling.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p017-cancel", "key-p017-cancel-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: "command-p017-cancel", SessionID: created.SessionID, IdempotencyKey: "key-p017-cancel-command", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p017-cancel","script":"sleep 1"}`), Script: "sleep 1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelling}); err != nil {
		t.Fatal(err)
	}
	if cancelling, err := store.GetCommand(context.Background(), command.CommandID); err != nil || cancelling.State != domain.CommandStateCancelling || cancelling.FinalEventSequence != nil {
		t.Fatalf("cancelling command = %+v err=%v", cancelling, err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListCommandEvents(context.Background(), command.CommandID)
	if err != nil || len(events) != 3 || events[2].Type != "command_cancelled" {
		t.Fatalf("cancelling events = %+v err=%v", events, err)
	}
}

func intPointer(value int) *int { return &value }
