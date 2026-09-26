package execution

import (
	"context"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// PolicySweepReport describes one deterministic fake-clock policy pass.
// Counters count durable outcomes, not runtime attempts.
type PolicySweepReport struct {
	SessionsInspected int
	IdleExpired       int
	CommandsTimedOut  int
	LifetimeExpired   int
	SessionsLost      int
	CommandsLost      int
}

// ExpirationReport is the design-facing name for PolicySweepReport.
type ExpirationReport = PolicySweepReport

// EnforcePolicies evaluates command timeout, idle timeout, and maximum
// lifetime rules against the authoritative store clock. A single pass is
// serialized with cancel/close mutations; it never treats an unconfirmed stop
// as an expired or released resource.
func (s *Service) EnforcePolicies(ctx context.Context) (PolicySweepReport, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return PolicySweepReport{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return PolicySweepReport{}, err
	}
	report := PolicySweepReport{SessionsInspected: len(sessions)}
	var firstErr error
	now := s.clock.Now().UTC()
	for _, session := range sessions {
		if session.State.IsTerminal() {
			continue
		}
		commands, err := s.store.ListSessionCommands(ctx, session.SessionID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !session.ExpiresAt.After(now) && (session.State == domain.SessionStateReady || session.State == domain.SessionStateBusy) {
			outcome, err := s.expireSession(ctx, session, commands, "session_max_lifetime")
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if outcome.expired {
				report.LifetimeExpired++
			} else {
				report.SessionsLost += outcome.sessionsLost
				report.CommandsLost += outcome.commandsLost
			}
			continue
		}

		if session.State == domain.SessionStateBusy {
			for _, command := range commands {
				if command.State != domain.CommandStateRunning || now.Before(command.UpdatedAt.Add(command.Timeout)) {
					continue
				}
				outcome, err := s.timeoutCommand(ctx, session, command)
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if outcome.timedOut {
					report.CommandsTimedOut++
				} else {
					report.SessionsLost += outcome.sessionsLost
					report.CommandsLost += outcome.commandsLost
				}
				break
			}
			continue
		}

		if session.State == domain.SessionStateReady && noAuthoritativeWork(commands) {
			idleSince := session.UpdatedAt
			for _, command := range commands {
				if command.UpdatedAt.After(idleSince) {
					idleSince = command.UpdatedAt
				}
			}
			if !now.Before(idleSince.Add(session.Limits.IdleTimeout)) {
				outcome, err := s.expireSession(ctx, session, commands, "session_idle_timeout")
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if outcome.expired {
					report.IdleExpired++
				} else {
					report.SessionsLost += outcome.sessionsLost
					report.CommandsLost += outcome.commandsLost
				}
			}
		}
	}
	return report, firstErr
}

// SweepExpirations is an explicit alias for callers that run this pass from a
// periodic policy worker.
func (s *Service) SweepExpirations(ctx context.Context) (PolicySweepReport, error) {
	return s.EnforcePolicies(ctx)
}

// EnforceTimeouts is kept as a concise service entry point for timeout workers;
// the pass also enforces idle and maximum-lifetime policy as required by D-13.
func (s *Service) EnforceTimeouts(ctx context.Context) (PolicySweepReport, error) {
	return s.EnforcePolicies(ctx)
}

type policyOutcome struct {
	expired      bool
	timedOut     bool
	sessionsLost int
	commandsLost int
}

func noAuthoritativeWork(commands []store.CommandRecord) bool {
	for _, command := range commands {
		switch command.State {
		case domain.CommandStateQueued, domain.CommandStateRunning, domain.CommandStateCancelling:
			return false
		}
	}
	return true
}

func (s *Service) timeoutCommand(ctx context.Context, session store.SessionRecord, command store.CommandRecord) (policyOutcome, error) {
	if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelling}); err != nil {
		return policyOutcome{}, err
	}
	command.State = domain.CommandStateCancelling
	control, ok := s.runtime.(RuntimeCommandControl)
	if !ok {
		return s.completePolicyCommandLoss(ctx, command, domain.SessionStateLost, "command_timeout_stop_unconfirmed", ErrStopUnconfirmed)
	}
	stopped, stopErr := control.CancelCommand(ctx, RuntimeCommandRequest{Session: session, Command: command})
	if stopErr != nil {
		stopped.Confirmed = false
	}
	if err := s.appendStopOutput(ctx, command.CommandID, stopped); err != nil {
		stopped.Confirmed = false
		if stopErr == nil {
			stopErr = err
		}
	}
	if stopErr != nil || !stopped.Confirmed {
		if stopErr == nil {
			stopErr = ErrStopUnconfirmed
		}
		return s.completePolicyCommandLoss(ctx, command, domain.SessionStateLost, "command_timeout_stop_unconfirmed", stopErr)
	}
	completed, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID: command.CommandID, NextState: domain.CommandStateTimedOut, OutputComplete: true,
	}, domain.SessionStateReady, "command_timed_out", true)
	if err != nil {
		return policyOutcome{}, err
	}
	_ = completed
	return policyOutcome{timedOut: true}, nil
}

func (s *Service) completePolicyCommandLoss(ctx context.Context, command store.CommandRecord, nextSession domain.SessionState, reason string, cause error) (policyOutcome, error) {
	completed, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{
		CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false,
	}, nextSession, reason, false)
	if err != nil {
		return policyOutcome{}, err
	}
	_ = completed
	return policyOutcome{sessionsLost: 1, commandsLost: 1}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, cause)
}

func (s *Service) expireSession(ctx context.Context, session store.SessionRecord, commands []store.CommandRecord, reason string) (policyOutcome, error) {
	var active *store.CommandRecord
	for index := range commands {
		if commands[index].State == domain.CommandStateRunning || commands[index].State == domain.CommandStateCancelling {
			candidate := commands[index]
			active = &candidate
			break
		}
	}
	// Cancel accepted queued work before asking the runtime to tear down. This
	// keeps a waiting command from becoming eligible during the lifetime pass.
	for _, command := range commands {
		if command.State != domain.CommandStateQueued {
			continue
		}
		if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelled, OutputComplete: true}); err != nil {
			return policyOutcome{}, err
		}
	}
	control, hasControl := s.runtime.(RuntimeCommandControl)
	if active == nil {
		if !hasControl {
			return s.expireLostSession(ctx, session, ErrStopUnconfirmed)
		}
		confirmed, stopErr := control.StopSession(ctx, session)
		if stopErr != nil || !confirmed {
			if stopErr == nil {
				stopErr = ErrStopUnconfirmed
			}
			return s.expireLostSession(ctx, session, stopErr)
		}
		if _, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateExpired, reason); err != nil {
			return policyOutcome{}, err
		}
		if err := s.store.ConfirmSessionCleanup(ctx, session.SessionID); err != nil {
			return policyOutcome{}, err
		}
		return policyOutcome{expired: true}, nil
	}
	command := *active
	if command.State == domain.CommandStateRunning {
		if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateCancelling}); err != nil {
			return policyOutcome{}, err
		}
		command.State = domain.CommandStateCancelling
	}
	if !hasControl {
		return s.expireLost(ctx, command, ErrStopUnconfirmed)
	}
	stopped, stopErr := control.CancelCommand(ctx, RuntimeCommandRequest{Session: session, Command: command})
	if stopErr != nil {
		stopped.Confirmed = false
	}
	if err := s.appendStopOutput(ctx, command.CommandID, stopped); err != nil {
		stopped.Confirmed = false
		if stopErr == nil {
			stopErr = err
		}
	}
	if stopErr != nil || !stopped.Confirmed {
		if stopErr == nil {
			stopErr = ErrStopUnconfirmed
		}
		return s.expireLost(ctx, command, stopErr)
	}
	confirmed, stopErr := control.StopSession(ctx, session)
	if stopErr != nil || !confirmed {
		if stopErr == nil {
			stopErr = ErrStopUnconfirmed
		}
		completed, completionErr := s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateTimedOut, OutputComplete: true}, domain.SessionStateLost, reason+"_command_stop_unconfirmed", true)
		if completionErr != nil {
			return policyOutcome{}, completionErr
		}
		_ = completed
		return policyOutcome{sessionsLost: 1}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, stopErr)
	}
	if _, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateTimedOut, OutputComplete: true}, domain.SessionStateExpired, reason+"_command", true); err != nil {
		return policyOutcome{}, err
	}
	if err := s.store.ConfirmSessionCleanup(ctx, session.SessionID); err != nil {
		return policyOutcome{}, err
	}
	return policyOutcome{expired: true}, nil
}

func (s *Service) expireLost(ctx context.Context, command store.CommandRecord, cause error) (policyOutcome, error) {
	if _, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateLost, OutputComplete: false}, domain.SessionStateLost, "policy_stop_unconfirmed", false); err != nil {
		return policyOutcome{}, err
	}
	return policyOutcome{sessionsLost: 1, commandsLost: 1}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, cause)
}

func (s *Service) expireLostSession(ctx context.Context, session store.SessionRecord, cause error) (policyOutcome, error) {
	if _, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateLost, "policy_cleanup_unconfirmed"); err != nil {
		return policyOutcome{}, err
	}
	return policyOutcome{sessionsLost: 1}, fmt.Errorf("%w: %v", ErrStopUnconfirmed, cause)
}
