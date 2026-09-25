package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidSessionState means a value is outside the v1 session state set.
	ErrInvalidSessionState = errors.New("invalid session state")
	// ErrIllegalSessionTransition means a session state edge is not in D-01.
	ErrIllegalSessionTransition = errors.New("illegal session state transition")
	// ErrInvalidCommandState means a value is outside the v1 command state set.
	ErrInvalidCommandState = errors.New("invalid command state")
	// ErrIllegalCommandTransition means a command state edge is not in D-01.
	ErrIllegalCommandTransition = errors.New("illegal command state transition")
)

// SessionState is one value from the shared v1 session state vocabulary.
type SessionState string

const (
	SessionStateRequested SessionState = "requested"
	SessionStateCreating  SessionState = "creating"
	SessionStateReady     SessionState = "ready"
	SessionStateBusy      SessionState = "busy"
	SessionStateClosing   SessionState = "closing"
	SessionStateClosed    SessionState = "closed"
	SessionStateExpired   SessionState = "expired"
	SessionStateFailed    SessionState = "failed"
	SessionStateLost      SessionState = "lost"
)

var sessionStateTransitions = map[SessionState]map[SessionState]struct{}{
	SessionStateRequested: {
		SessionStateCreating: {},
	},
	SessionStateCreating: {
		SessionStateReady:   {},
		SessionStateFailed:  {},
		SessionStateLost:    {},
		SessionStateClosing: {},
	},
	SessionStateReady: {
		SessionStateBusy:    {},
		SessionStateClosing: {},
		SessionStateExpired: {},
		SessionStateLost:    {},
	},
	SessionStateBusy: {
		SessionStateReady:   {},
		SessionStateClosing: {},
		SessionStateExpired: {},
		SessionStateLost:    {},
	},
	SessionStateClosing: {
		SessionStateClosed: {},
		SessionStateLost:   {},
	},
}

// Valid reports whether s is part of the shared v1 session vocabulary.
func (s SessionState) Valid() bool {
	switch s {
	case SessionStateRequested, SessionStateCreating, SessionStateReady,
		SessionStateBusy, SessionStateClosing, SessionStateClosed,
		SessionStateExpired, SessionStateFailed, SessionStateLost:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether no later session state may follow s.
func (s SessionState) IsTerminal() bool {
	switch s {
	case SessionStateClosed, SessionStateExpired, SessionStateFailed, SessionStateLost:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether next is a legal D-01 session-state edge.
// Runtime conditions and the state/event transaction are enforced by later layers.
func (s SessionState) CanTransitionTo(next SessionState) bool {
	_, ok := sessionStateTransitions[s][next]
	return ok
}

// ValidateSessionTransition checks state vocabulary and the D-01 edge table.
func ValidateSessionTransition(current, next SessionState) error {
	if !current.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidSessionState, current)
	}
	if !next.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidSessionState, next)
	}
	if !current.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalSessionTransition, current, next)
	}
	return nil
}

// SessionLifecycle is a pure immutable value for one session's ID, target,
// and current state. Transition returns a copy that retains the original ID
// and target; it does not persist lifecycle records or events.
type SessionLifecycle struct {
	sessionID SessionID
	target    ExecutionTarget
	state     SessionState
}

// NewSessionLifecycle constructs a lifecycle value with a validated target and state.
func NewSessionLifecycle(sessionID SessionID, target ExecutionTarget, state SessionState) (SessionLifecycle, error) {
	if err := validateID("session ID", string(sessionID)); err != nil {
		return SessionLifecycle{}, err
	}
	validatedTarget, err := NewExecutionTarget(target.Kind(), target.Profile())
	if err != nil {
		return SessionLifecycle{}, err
	}
	if !state.Valid() {
		return SessionLifecycle{}, fmt.Errorf("%w: %q", ErrInvalidSessionState, state)
	}
	return SessionLifecycle{sessionID: sessionID, target: validatedTarget, state: state}, nil
}

// SessionID returns the lifecycle value's session identifier.
func (s SessionLifecycle) SessionID() SessionID { return s.sessionID }

// Target returns the immutable execution target selected for the session.
func (s SessionLifecycle) Target() ExecutionTarget { return s.target }

// State returns the current session state.
func (s SessionLifecycle) State() SessionState { return s.state }

// Transition returns a new lifecycle value while preserving its session ID and target.
func (s SessionLifecycle) Transition(next SessionState) (SessionLifecycle, error) {
	if err := ValidateSessionTransition(s.state, next); err != nil {
		return SessionLifecycle{}, err
	}
	s.state = next
	return s, nil
}

// CommandState is one value from the shared v1 command state vocabulary.
type CommandState string

const (
	CommandStateQueued     CommandState = "queued"
	CommandStateRunning    CommandState = "running"
	CommandStateCancelling CommandState = "cancelling"
	CommandStateSucceeded  CommandState = "succeeded"
	CommandStateFailed     CommandState = "failed"
	CommandStateCancelled  CommandState = "cancelled"
	CommandStateTimedOut   CommandState = "timed_out"
	CommandStateRejected   CommandState = "rejected"
	CommandStateLost       CommandState = "lost"
)

var commandStateTransitions = map[CommandState]map[CommandState]struct{}{
	CommandStateQueued: {
		CommandStateRunning:   {},
		CommandStateCancelled: {},
		CommandStateRejected:  {},
	},
	CommandStateRunning: {
		CommandStateSucceeded:  {},
		CommandStateFailed:     {},
		CommandStateCancelling: {},
		CommandStateTimedOut:   {},
		CommandStateLost:       {},
	},
	CommandStateCancelling: {
		CommandStateCancelled: {},
		CommandStateTimedOut:  {},
		CommandStateSucceeded: {},
		CommandStateFailed:    {},
		CommandStateLost:      {},
	},
}

// Valid reports whether s is part of the shared v1 command vocabulary.
func (s CommandState) Valid() bool {
	switch s {
	case CommandStateQueued, CommandStateRunning, CommandStateCancelling,
		CommandStateSucceeded, CommandStateFailed, CommandStateCancelled,
		CommandStateTimedOut, CommandStateRejected, CommandStateLost:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether no later command state may follow s.
func (s CommandState) IsTerminal() bool {
	switch s {
	case CommandStateSucceeded, CommandStateFailed, CommandStateCancelled,
		CommandStateTimedOut, CommandStateRejected, CommandStateLost:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether next is a legal D-01 command-state edge.
// Runtime conditions and the state/event transaction are enforced by later layers.
func (s CommandState) CanTransitionTo(next CommandState) bool {
	_, ok := commandStateTransitions[s][next]
	return ok
}

// ValidateCommandTransition checks state vocabulary and the D-01 edge table.
func ValidateCommandTransition(current, next CommandState) error {
	if !current.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidCommandState, current)
	}
	if !next.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidCommandState, next)
	}
	if !current.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalCommandTransition, current, next)
	}
	return nil
}

// CommandLifecycle is a pure immutable value for a command's ID, parent
// session, and state. A command has no execution-target field; it inherits
// the target from its parent session.
type CommandLifecycle struct {
	commandID CommandID
	sessionID SessionID
	state     CommandState
}

// NewCommandLifecycle constructs a lifecycle value with validated IDs and state.
func NewCommandLifecycle(commandID CommandID, sessionID SessionID, state CommandState) (CommandLifecycle, error) {
	if err := validateID("command ID", string(commandID)); err != nil {
		return CommandLifecycle{}, err
	}
	if err := validateID("session ID", string(sessionID)); err != nil {
		return CommandLifecycle{}, err
	}
	if !state.Valid() {
		return CommandLifecycle{}, fmt.Errorf("%w: %q", ErrInvalidCommandState, state)
	}
	return CommandLifecycle{commandID: commandID, sessionID: sessionID, state: state}, nil
}

// CommandID returns the lifecycle value's command identifier.
func (c CommandLifecycle) CommandID() CommandID { return c.commandID }

// SessionID returns the command's immutable parent session identifier.
func (c CommandLifecycle) SessionID() SessionID { return c.sessionID }

// State returns the current command state.
func (c CommandLifecycle) State() CommandState { return c.state }

// Transition returns a new lifecycle value while preserving command/session identity.
func (c CommandLifecycle) Transition(next CommandState) (CommandLifecycle, error) {
	if err := ValidateCommandTransition(c.state, next); err != nil {
		return CommandLifecycle{}, err
	}
	c.state = next
	return c, nil
}
