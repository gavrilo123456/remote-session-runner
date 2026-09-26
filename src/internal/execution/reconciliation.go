package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// StartupReconciliationReport summarizes the durable decisions made before an
// executor advertises readiness. Counts are idempotent for terminal sessions:
// rerunning reconciliation after a successful sweep does not change them.
type StartupReconciliationReport struct {
	SessionsInspected           int
	CreatingFailed              int
	CreatingLost                int
	SessionsLost                int
	SessionsClosed              int
	CommandsLost                int
	CommandsRejected            int
	CommandSlotsReleased        int
	SessionReservationsReleased int
	GenerationMismatches        int
	CleanupConfirmed            int
	CleanupUnconfirmed          int
	RuntimeFailures             int
}

// ReconcileStartup settles durable sessions left by a previous executor
// generation. The PoC never reattaches a shell. Ready/busy sessions are
// converted to lost, active commands become lost, queued commands behind the
// unusable session become rejected, and capacity is released only after the
// runtime proves cleanup. Creating sessions become failed after confirmed
// partial-runtime cleanup or lost when cleanup remains uncertain. Closing
// sessions resume toward closed only after confirmed cleanup.
func (s *Service) ReconcileStartup(ctx context.Context) (StartupReconciliationReport, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return StartupReconciliationReport{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		return StartupReconciliationReport{}, err
	}
	var report StartupReconciliationReport
	var runtimeFailures []error
	for _, session := range sessions {
		if session.State.IsTerminal() {
			continue
		}
		report.SessionsInspected++
		confirmed, observedGeneration, reconcileErr := s.reconcileRuntime(ctx, session)
		if reconcileErr != nil {
			report.RuntimeFailures++
			runtimeFailures = append(runtimeFailures, fmt.Errorf("session %s: %w", session.SessionID, reconcileErr))
		}
		if observedGeneration != "" && session.RuntimeGeneration != "" && observedGeneration != session.RuntimeGeneration {
			report.GenerationMismatches++
		}
		if confirmed {
			report.CleanupConfirmed++
		} else {
			report.CleanupUnconfirmed++
		}

		switch session.State {
		case domain.SessionStateCreating:
			if err := s.reconcileCreating(ctx, session, confirmed, &report); err != nil {
				return report, joinReconciliationErrors(runtimeFailures, err)
			}
		case domain.SessionStateReady, domain.SessionStateBusy, domain.SessionStateClosing:
			if err := s.reconcileLiveSession(ctx, session, confirmed, &report); err != nil {
				return report, joinReconciliationErrors(runtimeFailures, err)
			}
		}
	}
	if len(runtimeFailures) > 0 {
		return report, errors.Join(runtimeFailures...)
	}
	return report, nil
}

func joinReconciliationErrors(previous []error, current error) error {
	if len(previous) == 0 {
		return current
	}
	return errors.Join(append(previous, current)...)
}

func (s *Service) reconcileRuntime(ctx context.Context, session store.SessionRecord) (bool, string, error) {
	if reconciler, ok := s.runtime.(RuntimeReconciler); ok {
		result, err := reconciler.Reconcile(ctx, RuntimeReconcileRequest{Session: session})
		if err != nil {
			return false, result.RuntimeGeneration, err
		}
		return result.CleanupConfirmed, result.RuntimeGeneration, nil
	}

	// Creating has a distinct partial-runtime cleanup operation. For live and
	// closing sessions, an optional stop controller is the fake-runtime proof
	// boundary until a real adapter supplies generation-aware inspection.
	if session.State == domain.SessionStateCreating {
		err := s.runtime.Cleanup(ctx, RuntimeCleanupRequest{
			Session:           session,
			Prepared:          RuntimePrepared{RuntimeGeneration: session.RuntimeGeneration},
			RuntimeGeneration: session.RuntimeGeneration,
		})
		return err == nil, session.RuntimeGeneration, err
	}
	if control, ok := s.runtime.(RuntimeCommandControl); ok {
		confirmed, err := control.StopSession(ctx, session)
		if err != nil {
			return false, session.RuntimeGeneration, err
		}
		if !confirmed {
			return false, session.RuntimeGeneration, nil
		}
		return true, session.RuntimeGeneration, nil
	}
	return false, session.RuntimeGeneration, nil
}

func (s *Service) reconcileCreating(ctx context.Context, session store.SessionRecord, confirmed bool, report *StartupReconciliationReport) error {
	next := domain.SessionStateLost
	reason := "startup_runtime_cleanup_unconfirmed"
	if confirmed {
		next = domain.SessionStateFailed
		reason = "startup_runtime_cleanup_confirmed"
	}
	if _, err := s.store.CompleteSessionCreation(ctx, session.SessionID, next, session.RuntimeGeneration, session.ResolvedRevision, reason); err != nil {
		return err
	}
	if confirmed {
		report.CreatingFailed++
		if err := s.store.ConfirmSessionCleanup(ctx, session.SessionID); err != nil {
			return err
		}
		report.SessionReservationsReleased++
	} else {
		report.CreatingLost++
	}
	s.publishLatestLifecycle(ctx, session.SessionID)
	return nil
}

func (s *Service) reconcileLiveSession(ctx context.Context, session store.SessionRecord, confirmed bool, report *StartupReconciliationReport) error {
	commands, err := s.store.ListSessionCommands(ctx, session.SessionID)
	if err != nil {
		return err
	}
	for _, command := range commands {
		switch command.State {
		case domain.CommandStateRunning, domain.CommandStateCancelling:
			completed, err := s.store.CompleteRunningCommand(ctx, store.CommandTransition{
				CommandID:      command.CommandID,
				NextState:      domain.CommandStateLost,
				OutputComplete: false,
			}, domain.SessionStateLost, "startup_runtime_lost", confirmed)
			if err != nil {
				return err
			}
			_ = completed
			report.CommandsLost++
			if confirmed {
				report.CommandSlotsReleased++
			}
		case domain.CommandStateQueued:
			if _, err := s.store.TransitionCommand(ctx, store.CommandTransition{
				CommandID:      command.CommandID,
				NextState:      domain.CommandStateRejected,
				OutputComplete: true,
			}); err != nil {
				return err
			}
			report.CommandsRejected++
		}
	}

	current, err := s.store.GetSession(ctx, session.SessionID)
	if err != nil {
		return err
	}
	if current.State == domain.SessionStateClosing && confirmed {
		if _, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateClosed, "startup_runtime_closed"); err != nil {
			return err
		}
		report.SessionsClosed++
	} else if !current.State.IsTerminal() {
		if _, err := s.store.TransitionSession(ctx, session.SessionID, domain.SessionStateLost, "startup_runtime_lost"); err != nil {
			return err
		}
		report.SessionsLost++
	}
	if confirmed {
		if err := s.store.ConfirmSessionCleanup(ctx, session.SessionID); err != nil {
			return err
		}
		report.SessionReservationsReleased++
	}
	s.publishLatestLifecycle(ctx, session.SessionID)
	return nil
}
