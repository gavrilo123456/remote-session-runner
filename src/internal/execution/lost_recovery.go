package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

var (
	// ErrLostRuntimeRecoveryIneligible means the requested session and command
	// do not describe one isolated lost runtime whose capacity may be repaired.
	ErrLostRuntimeRecoveryIneligible = errors.New("lost runtime recovery is not eligible")
	// ErrLostRuntimeRecoveryUnconfirmed means the runtime adapter could not
	// prove that the recorded process group is gone while capacity remains held.
	ErrLostRuntimeRecoveryUnconfirmed = errors.New("lost runtime cleanup remains unconfirmed")
	// ErrLostRuntimeRecoveryFinalization means paired capacity release is
	// durable, but its retained ownership marker and workspace could not yet be
	// finalized. Keep runnerd stopped and rerun the same explicit repair.
	ErrLostRuntimeRecoveryFinalization = errors.New("lost runtime recovery finalization remains unconfirmed")
)

// LostRuntimeRecoveryRequest identifies one explicitly selected lost command
// and its session. It is an operator repair boundary: it never executes the
// command script and never changes the truthful lost result.
type LostRuntimeRecoveryRequest struct {
	SessionID domain.SessionID
	CommandID domain.CommandID
}

// LostRuntimeRecoveryResult records the authoritative state before or after a
// repair. A successful repair releases only capacity that follows a confirmed
// runtime reconciliation.
type LostRuntimeRecoveryResult struct {
	Session            store.SessionRecord
	Command            store.CommandRecord
	SessionReservation store.SessionReservation
	CommandSlot        store.CommandSlotRecord
	Runtime            RuntimeReconcileResult
	AlreadyRecovered   bool
}

// CheckLostRuntimeRecovery verifies either one fully retained lost runtime or
// a previously released pair whose marker may still need finalization. It
// performs no runtime action and no durable write.
func (s *Service) CheckLostRuntimeRecovery(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return LostRuntimeRecoveryResult{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.checkLostRuntimeRecovery(ctx, request)
}

// RecoverLostRuntime runs one explicit, no-replay recovery attempt for an
// already-lost command. It durably marks the proven process-group boundary
// before atomically releasing both capacity records, then finalizes the owner
// marker and workspace. Any failed or unconfirmed pre-release check leaves
// capacity retained.
func (s *Service) RecoverLostRuntime(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return LostRuntimeRecoveryResult{}, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	result, err := s.checkLostRuntimeRecovery(ctx, request)
	if err != nil {
		return result, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return result, fmt.Errorf("%w: runtime does not support lost-runtime recovery", ErrLostRuntimeRecoveryIneligible)
	}
	if result.AlreadyRecovered {
		return s.finalizeLostRuntimeRecovery(ctx, request, result, recoverer)
	}

	// The runtime writes a durable cleanup proof into the retained ownership
	// marker before this paired SQLite write. If storage rejects the write, a
	// retry recognizes that proof without touching a potentially reused PID.
	runtimeResult, reconcileErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Session})
	result.Runtime = runtimeResult
	if reconcileErr != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryUnconfirmed, reconcileErr), auditErr)
	}
	if !runtimeResult.CleanupConfirmed {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, auditErr)
	}
	if err := s.store.ConfirmLostRuntimeRecovery(ctx, request.SessionID, request.CommandID); err != nil {
		return result, fmt.Errorf("record recovered runtime capacity: %w", err)
	}
	return s.finalizeLostRuntimeRecovery(ctx, request, result, recoverer)
}

// finalizeLostRuntimeRecovery removes the retained ownership evidence only
// after the paired durable capacity release exists. A failure here deliberately
// leaves runnerd stopped: a retry sees the already-released pair and performs
// only this idempotent finalization, never a script replay or capacity write.
func (s *Service) finalizeLostRuntimeRecovery(ctx context.Context, request LostRuntimeRecoveryRequest, result LostRuntimeRecoveryResult, recoverer LostRuntimeRecoverer) (LostRuntimeRecoveryResult, error) {
	finalized, finalizeErr := recoverer.FinalizeLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Session})
	result.Runtime = finalized
	if finalizeErr != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryFinalization, finalizeErr), auditErr)
	}
	if !finalized.CleanupConfirmed {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(request.CommandID))
		return result, errors.Join(ErrLostRuntimeRecoveryFinalization, auditErr)
	}
	var err error
	result.CommandSlot, err = s.store.GetCommandSlot(ctx, request.CommandID)
	if err != nil {
		return result, fmt.Errorf("read released command slot: %w", err)
	}
	result.SessionReservation, err = s.store.GetSessionReservation(ctx, request.SessionID)
	if err != nil {
		return result, fmt.Errorf("read released session reservation: %w", err)
	}
	return result, nil
}

func (s *Service) checkLostRuntimeRecovery(ctx context.Context, request LostRuntimeRecoveryRequest) (LostRuntimeRecoveryResult, error) {
	if request.SessionID == "" || request.CommandID == "" {
		return LostRuntimeRecoveryResult{}, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
	}
	result := LostRuntimeRecoveryResult{}
	var err error
	result.Session, err = s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	result.Command, err = s.store.GetCommand(ctx, request.CommandID)
	if err != nil {
		return result, err
	}
	if result.Command.SessionID != result.Session.SessionID {
		return result, fmt.Errorf("%w: command does not belong to session", ErrLostRuntimeRecoveryIneligible)
	}
	result.CommandSlot, err = s.store.GetCommandSlot(ctx, request.CommandID)
	if err != nil {
		return result, err
	}
	result.SessionReservation, err = s.store.GetSessionReservation(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	if result.Session.State != domain.SessionStateLost || result.Command.State != domain.CommandStateLost {
		return result, fmt.Errorf("%w: session=%s command=%s, want lost/lost", ErrLostRuntimeRecoveryIneligible, result.Session.State, result.Command.State)
	}
	commands, err := s.store.ListSessionCommands(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	for _, command := range commands {
		if command.CommandID != request.CommandID && !command.State.IsTerminal() {
			return result, fmt.Errorf("%w: session has nonterminal command %s", ErrLostRuntimeRecoveryIneligible, command.CommandID)
		}
	}
	commandReleased := result.CommandSlot.StopConfirmedAt != nil && result.CommandSlot.ReleasedAt != nil
	sessionReleased := result.SessionReservation.CleanupConfirmedAt != nil && result.SessionReservation.ReleasedAt != nil
	if commandReleased && sessionReleased {
		// Event retention may later remove the command_lost record. The paired
		// release itself is enough to resume only post-release finalization.
		result.AlreadyRecovered = true
		return result, nil
	}
	if result.CommandSlot.StopConfirmedAt != nil || result.CommandSlot.ReleasedAt != nil || result.SessionReservation.CleanupConfirmedAt != nil || result.SessionReservation.ReleasedAt != nil {
		return result, fmt.Errorf("%w: capacity has already been partially released", ErrLostRuntimeRecoveryIneligible)
	}
	if result.Command.OutputComplete || result.Command.FinalEventSequence == nil {
		return result, fmt.Errorf("%w: output_complete=%t final_event_present=%t", ErrLostRuntimeRecoveryIneligible, result.Command.OutputComplete, result.Command.FinalEventSequence != nil)
	}
	events, err := s.store.ListCommandEvents(ctx, request.CommandID)
	if err != nil {
		return result, err
	}
	if len(events) == 0 || events[len(events)-1].Sequence != *result.Command.FinalEventSequence || events[len(events)-1].Type != "command_lost" {
		return result, fmt.Errorf("%w: command has no final command_lost event", ErrLostRuntimeRecoveryIneligible)
	}
	liveSlots, err := s.store.CountLiveCommandSlots(ctx)
	if err != nil {
		return result, err
	}
	liveReservations, err := s.store.CountLiveSessionReservations(ctx)
	if err != nil {
		return result, err
	}
	if liveSlots != 1 || liveReservations != 1 {
		return result, fmt.Errorf("%w: live command slots=%d live session reservations=%d, want one matching retained runtime", ErrLostRuntimeRecoveryIneligible, liveSlots, liveReservations)
	}
	return result, nil
}
