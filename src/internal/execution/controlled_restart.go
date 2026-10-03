package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	// ErrControlledRestartUnavailable means the selected runtime cannot prove
	// and rebuild the one deliberately preserved local queued session. The
	// durable plan remains in place so an operator can repair the host without
	// allowing ordinary startup reconciliation to reject the command.
	ErrControlledRestartUnavailable = errors.New("controlled restart rehydration is unavailable")
	// ErrControlledRestartPlanState means the durable plan no longer describes
	// an untouched ready/queued one-off. Failing closed protects every other
	// queued command from an accidental restart admission.
	ErrControlledRestartPlanState = errors.New("controlled restart plan state is invalid")
)

// ControlledRestartRehydrator is implemented only by a runtime which can
// replace a proven-gone persistent shell for the same durable session
// generation. It deliberately receives no command bytes: command execution
// remains owned by the ordinary durable scheduler after capacity recovery.
type ControlledRestartRehydrator interface {
	RebuildQueuedOneOff(context.Context, store.SessionRecord) (RuntimeStarted, error)
}

// ControlledRestartStartup describes the one explicit startup exception used
// by the Mac controlled restart. When Plan is nil, startup followed the
// ordinary no-reattachment reconciliation path. When Plan is present, exactly
// its queued session was rebuilt and excluded from generic reconciliation;
// all other sessions retain the normal reconciliation contract.
type ControlledRestartStartup struct {
	Plan       *store.ControlledRestartPlan
	Rehydrated bool
}

// ReconcileStartupWithControlledRestartPlan performs ordinary startup unless
// a validated durable controlled-restart plan exists. The plan path rebuilds
// only its exact local ready/queued empty-source one-off before the normal
// sweep. It never consumes the plan: StartNextEligibleCommand consumes it in
// the same transaction that claims that exact command.
func (s *Service) ReconcileStartupWithControlledRestartPlan(ctx context.Context) (StartupReconciliationReport, ControlledRestartStartup, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, ErrExecutionServiceConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	plan, err := s.store.ReadControlledRestartPlan(ctx)
	if errors.Is(err, store.ErrControlledRestartPlanNotFound) {
		report, reconcileErr := s.reconcileStartupLocked(ctx, "")
		return report, ControlledRestartStartup{}, reconcileErr
	}
	if err != nil {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, fmt.Errorf("read controlled restart plan: %w", err)
	}

	session, err := s.store.GetSession(ctx, plan.SessionID)
	if err != nil {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, fmt.Errorf("read controlled restart session: %w", err)
	}
	if err := s.validateControlledRestartQueuedOneOff(ctx, plan, session); err != nil {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, err
	}
	rehydrator, ok := s.runtime.(ControlledRestartRehydrator)
	if !ok {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, ErrControlledRestartUnavailable
	}
	started, err := rehydrator.RebuildQueuedOneOff(ctx, session)
	if err != nil {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, fmt.Errorf("%w: %v", ErrControlledRestartUnavailable, err)
	}
	if started.RuntimeGeneration == "" || started.RuntimeGeneration != session.RuntimeGeneration || started.RuntimeGeneration != plan.RuntimeGeneration {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, fmt.Errorf("%w: replacement runtime generation mismatch", ErrControlledRestartUnavailable)
	}

	// The prepared plan blocks every scheduler claim, including claims from a
	// previous executor that was frozen during the installer handoff. Only the
	// replacement shell can move it to active, and that happens after the
	// runtime-generation handshake above. The store rechecks the untouched job
	// and exact durable identities in the same transaction.
	activePlan, err := s.store.ActivateControlledRestartPlan(ctx, plan)
	if err != nil {
		return StartupReconciliationReport{}, ControlledRestartStartup{}, fmt.Errorf("activate controlled restart plan: %w", err)
	}
	plan = activePlan

	report, err := s.reconcileStartupLocked(ctx, plan.SessionID)
	if err != nil {
		return report, ControlledRestartStartup{Plan: &plan, Rehydrated: true}, err
	}
	return report, ControlledRestartStartup{Plan: &plan, Rehydrated: true}, nil
}

func (s *Service) validateControlledRestartQueuedOneOff(ctx context.Context, plan store.ControlledRestartPlan, session store.SessionRecord) error {
	if session.SessionID != plan.SessionID || session.State != domain.SessionStateReady ||
		session.RuntimeGeneration == "" || session.RuntimeGeneration != plan.RuntimeGeneration || session.Target.Kind() != domain.TargetKindLocal ||
		session.Source.Mode() != domain.SourceModeEmpty {
		return ErrControlledRestartPlanState
	}
	job, err := s.store.GetJob(ctx, plan.JobID)
	if err != nil {
		return fmt.Errorf("%w: read planned job: %v", ErrControlledRestartPlanState, err)
	}
	if job.SessionID != plan.SessionID || job.CommandID != plan.CommandID ||
		job.Phase != store.JobPhaseAwaitingCommand || job.Source.Mode() != domain.SourceModeEmpty {
		return ErrControlledRestartPlanState
	}
	commands, err := s.store.ListSessionCommands(ctx, session.SessionID)
	if err != nil {
		return fmt.Errorf("%w: read planned command: %v", ErrControlledRestartPlanState, err)
	}
	if len(commands) != 1 || commands[0].CommandID != plan.CommandID || commands[0].State != domain.CommandStateQueued {
		return ErrControlledRestartPlanState
	}
	return nil
}
