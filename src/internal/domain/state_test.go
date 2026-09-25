package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
)

func TestP008D01SessionTransitionTable(t *testing.T) {
	states := []SessionState{
		SessionStateRequested,
		SessionStateCreating,
		SessionStateReady,
		SessionStateBusy,
		SessionStateClosing,
		SessionStateClosed,
		SessionStateExpired,
		SessionStateFailed,
		SessionStateLost,
	}
	allowed := map[SessionState]map[SessionState]bool{
		SessionStateRequested: {SessionStateCreating: true},
		SessionStateCreating: {
			SessionStateReady: true, SessionStateFailed: true,
			SessionStateLost: true, SessionStateClosing: true,
		},
		SessionStateReady: {
			SessionStateBusy: true, SessionStateClosing: true,
			SessionStateExpired: true, SessionStateLost: true,
		},
		SessionStateBusy: {
			SessionStateReady: true, SessionStateClosing: true,
			SessionStateExpired: true, SessionStateLost: true,
		},
		SessionStateClosing: {SessionStateClosed: true, SessionStateLost: true},
	}

	for _, current := range states {
		if !current.Valid() {
			t.Errorf("session state %q is not recognized", current)
		}
		for _, next := range states {
			want := allowed[current][next]
			if got := current.CanTransitionTo(next); got != want {
				t.Errorf("session edge %s -> %s allowed = %t, want %t", current, next, got, want)
			}
			err := ValidateSessionTransition(current, next)
			if want && err != nil {
				t.Errorf("valid session edge %s -> %s rejected: %v", current, next, err)
			} else if !want && !errors.Is(err, ErrIllegalSessionTransition) {
				t.Errorf("invalid session edge %s -> %s error = %v, want ErrIllegalSessionTransition", current, next, err)
			}
		}
	}
	if (SessionState("unknown")).Valid() || SessionState("unknown").IsTerminal() {
		t.Error("unknown session state was treated as valid or terminal")
	}
	if err := ValidateSessionTransition(SessionState("unknown"), SessionStateReady); !errors.Is(err, ErrInvalidSessionState) {
		t.Errorf("unknown current session state error = %v, want ErrInvalidSessionState", err)
	}
	if err := ValidateSessionTransition(SessionStateReady, SessionState("unknown")); !errors.Is(err, ErrInvalidSessionState) {
		t.Errorf("unknown next session state error = %v, want ErrInvalidSessionState", err)
	}
}

func TestP008D01CommandTransitionTable(t *testing.T) {
	states := []CommandState{
		CommandStateQueued,
		CommandStateRunning,
		CommandStateCancelling,
		CommandStateSucceeded,
		CommandStateFailed,
		CommandStateCancelled,
		CommandStateTimedOut,
		CommandStateRejected,
		CommandStateLost,
	}
	allowed := map[CommandState]map[CommandState]bool{
		CommandStateQueued: {
			CommandStateRunning: true, CommandStateCancelled: true,
			CommandStateRejected: true,
		},
		CommandStateRunning: {
			CommandStateSucceeded: true, CommandStateFailed: true,
			CommandStateCancelling: true, CommandStateTimedOut: true,
			CommandStateLost: true,
		},
		CommandStateCancelling: {
			CommandStateCancelled: true, CommandStateTimedOut: true,
			CommandStateSucceeded: true, CommandStateFailed: true,
			CommandStateLost: true,
		},
	}

	for _, current := range states {
		if !current.Valid() {
			t.Errorf("command state %q is not recognized", current)
		}
		for _, next := range states {
			want := allowed[current][next]
			if got := current.CanTransitionTo(next); got != want {
				t.Errorf("command edge %s -> %s allowed = %t, want %t", current, next, got, want)
			}
			err := ValidateCommandTransition(current, next)
			if want && err != nil {
				t.Errorf("valid command edge %s -> %s rejected: %v", current, next, err)
			} else if !want && !errors.Is(err, ErrIllegalCommandTransition) {
				t.Errorf("invalid command edge %s -> %s error = %v, want ErrIllegalCommandTransition", current, next, err)
			}
		}
	}
	if (CommandState("unknown")).Valid() || CommandState("unknown").IsTerminal() {
		t.Error("unknown command state was treated as valid or terminal")
	}
	if err := ValidateCommandTransition(CommandState("unknown"), CommandStateQueued); !errors.Is(err, ErrInvalidCommandState) {
		t.Errorf("unknown current command state error = %v, want ErrInvalidCommandState", err)
	}
	if err := ValidateCommandTransition(CommandStateQueued, CommandState("unknown")); !errors.Is(err, ErrInvalidCommandState) {
		t.Errorf("unknown next command state error = %v, want ErrInvalidCommandState", err)
	}
}

func TestP008D01TerminalStateRules(t *testing.T) {
	sessionCases := []struct {
		state    SessionState
		terminal bool
	}{
		{SessionStateRequested, false},
		{SessionStateCreating, false},
		{SessionStateReady, false},
		{SessionStateBusy, false},
		{SessionStateClosing, false},
		{SessionStateClosed, true},
		{SessionStateExpired, true},
		{SessionStateFailed, true},
		{SessionStateLost, true},
	}
	for _, test := range sessionCases {
		if got := test.state.IsTerminal(); got != test.terminal {
			t.Errorf("session state %q terminal = %t, want %t", test.state, got, test.terminal)
		}
	}

	commandCases := []struct {
		state    CommandState
		terminal bool
	}{
		{CommandStateQueued, false},
		{CommandStateRunning, false},
		{CommandStateCancelling, false},
		{CommandStateSucceeded, true},
		{CommandStateFailed, true},
		{CommandStateCancelled, true},
		{CommandStateTimedOut, true},
		{CommandStateRejected, true},
		{CommandStateLost, true},
	}
	for _, test := range commandCases {
		if got := test.state.IsTerminal(); got != test.terminal {
			t.Errorf("command state %q terminal = %t, want %t", test.state, got, test.terminal)
		}
	}
}

func TestP008D01StateValuesMatchV1Schema(t *testing.T) {
	data, err := p002SchemaFiles.ReadFile("schemas/v1/states.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode v1 state schema: %v", err)
	}

	sessionValues := []string{
		string(SessionStateRequested), string(SessionStateCreating), string(SessionStateReady),
		string(SessionStateBusy), string(SessionStateClosing), string(SessionStateClosed),
		string(SessionStateExpired), string(SessionStateFailed), string(SessionStateLost),
	}
	commandValues := []string{
		string(CommandStateQueued), string(CommandStateRunning), string(CommandStateCancelling),
		string(CommandStateSucceeded), string(CommandStateFailed), string(CommandStateCancelled),
		string(CommandStateTimedOut), string(CommandStateRejected), string(CommandStateLost),
	}
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "session", got: sessionValues, want: document.Definitions["sessionState"].Enum},
		{name: "command", got: commandValues, want: document.Definitions["commandState"].Enum},
	}
	for _, test := range tests {
		sort.Strings(test.got)
		sort.Strings(test.want)
		if !reflect.DeepEqual(test.got, test.want) {
			t.Errorf("%s state vocabulary = %v, schema has %v", test.name, test.got, test.want)
		}
	}
}

func TestP008D01SessionTargetCannotChangeDuringTransition(t *testing.T) {
	target, err := NewExecutionTarget(TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	requested, err := NewSessionLifecycle(SessionID("session-1"), target, SessionStateRequested)
	if err != nil {
		t.Fatal(err)
	}
	creating, err := requested.Transition(SessionStateCreating)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := creating.Transition(SessionStateReady)
	if err != nil {
		t.Fatal(err)
	}
	busy, err := ready.Transition(SessionStateBusy)
	if err != nil {
		t.Fatal(err)
	}

	for _, lifecycle := range []SessionLifecycle{requested, creating, ready, busy} {
		if lifecycle.SessionID() != SessionID("session-1") {
			t.Errorf("transition changed session ID to %q", lifecycle.SessionID())
		}
		if got := lifecycle.Target(); got != target {
			t.Errorf("transition changed immutable target to %+v, want %+v", got, target)
		}
	}
	if requested.State() != SessionStateRequested {
		t.Errorf("pure transition mutated original value to %q", requested.State())
	}
	if _, err := ready.Transition(SessionStateFailed); !errors.Is(err, ErrIllegalSessionTransition) {
		t.Errorf("ready -> failed error = %v, want ErrIllegalSessionTransition", err)
	}
	if got := ready.Target(); got != target || ready.State() != SessionStateReady {
		t.Errorf("rejected transition changed lifecycle: state=%q target=%+v", ready.State(), got)
	}
}

func TestP008D01CommandInheritsSessionIdentityWithoutTargetOverride(t *testing.T) {
	queued, err := NewCommandLifecycle(CommandID("command-1"), SessionID("session-1"), CommandStateQueued)
	if err != nil {
		t.Fatal(err)
	}
	running, err := queued.Transition(CommandStateRunning)
	if err != nil {
		t.Fatal(err)
	}
	cancelling, err := running.Transition(CommandStateCancelling)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := cancelling.Transition(CommandStateCancelled)
	if err != nil {
		t.Fatal(err)
	}
	for _, lifecycle := range []CommandLifecycle{queued, running, cancelling, cancelled} {
		if lifecycle.CommandID() != CommandID("command-1") || lifecycle.SessionID() != SessionID("session-1") {
			t.Errorf("command transition changed resource identity: command=%q session=%q", lifecycle.CommandID(), lifecycle.SessionID())
		}
	}
	if _, err := cancelled.Transition(CommandStateSucceeded); !errors.Is(err, ErrIllegalCommandTransition) {
		t.Errorf("terminal command transition error = %v, want ErrIllegalCommandTransition", err)
	}
}
