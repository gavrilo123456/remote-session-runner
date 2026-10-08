package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// CommandlessLostRuntimeRecoveryRequest identifies a lost session whose
// one-off job had a planned command ID but no durable command record. It is
// an explicit offline-repair input and never supplies or reads script bytes.
type CommandlessLostRuntimeRecoveryRequest struct {
	SessionID domain.SessionID
}

// CommandlessLostRuntimeRecoveryResult records the durable candidate and its
// runtime ownership proof. It intentionally contains no command output or
// payload because no command projection exists.
type CommandlessLostRuntimeRecoveryResult struct {
	Recovery store.CommandlessLostRuntimeRecovery
	Runtime  RuntimeReconcileResult
}

// CheckLostRuntimeRecoverySet validates a complete explicit offline recovery
// inventory without inspecting process groups or changing durable capacity.
// It accepts ordinary lost command pairs and commandless lost sessions in one
// set so no recovery can silently ignore extra retained capacity.
func (s *Service) CheckLostRuntimeRecoverySet(ctx context.Context, pairs []LostRuntimeRecoveryRequest, sessions []CommandlessLostRuntimeRecoveryRequest) error {
	if s == nil || s.store == nil || s.runtime == nil {
		return ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	_, _, _, _, err := s.checkLostRuntimeRecoverySet(ctx, pairs, sessions)
	return err
}

// RecoverLostRuntimeRecoverySet proves and releases a complete explicit set
// of terminal lost runtimes. Each runtime proof is obtained while every slot
// and reservation remains held. The store then revalidates and releases the
// selected ordinary pairs and commandless reservations in one transaction.
// It never executes or replays a stored script.
func (s *Service) RecoverLostRuntimeRecoverySet(ctx context.Context, pairs []LostRuntimeRecoveryRequest, sessions []CommandlessLostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, []CommandlessLostRuntimeRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	pairResults, sessionResults, storePairs, storeSessions, err := s.checkLostRuntimeRecoverySet(ctx, pairs, sessions)
	if err != nil {
		return pairResults, sessionResults, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return pairResults, sessionResults, fmt.Errorf("%w: runtime does not support lost-runtime recovery", ErrLostRuntimeRecoveryIneligible)
	}
	for index := range pairResults {
		if pairResults[index].AlreadyRecovered {
			continue
		}
		result, reconcileErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: pairResults[index].Session})
		pairResults[index].Runtime = result
		if reconcileErr != nil || !result.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, pairResults[index].Session, string(pairs[index].CommandID))
			if reconcileErr != nil {
				return pairResults, sessionResults, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryUnconfirmed, reconcileErr), auditErr)
			}
			return pairResults, sessionResults, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, auditErr)
		}
	}
	for index := range sessionResults {
		if sessionResults[index].Recovery.AlreadyRecovered {
			continue
		}
		result, reconcileErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: sessionResults[index].Recovery.Session})
		sessionResults[index].Runtime = result
		if reconcileErr != nil || !result.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, sessionResults[index].Recovery.Session, string(sessionResults[index].Recovery.CommandID))
			if reconcileErr != nil {
				return pairResults, sessionResults, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryUnconfirmed, reconcileErr), auditErr)
			}
			return pairResults, sessionResults, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, auditErr)
		}
	}
	if err := s.store.ConfirmLostRuntimeRecoverySet(ctx, storePairs, storeSessions); err != nil {
		return pairResults, sessionResults, fmt.Errorf("record recovered runtime capacity: %w", err)
	}

	var finalizationErrors []error
	for index := range pairResults {
		finalized, finalizeErr := s.finalizeLostRuntimeRecovery(ctx, pairs[index], pairResults[index], recoverer)
		pairResults[index] = finalized
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	for index := range sessionResults {
		finalized, finalizeErr := s.finalizeCommandlessLostRuntimeRecovery(ctx, sessionResults[index], recoverer)
		sessionResults[index] = finalized
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	return pairResults, sessionResults, errors.Join(finalizationErrors...)
}

func (s *Service) checkLostRuntimeRecoverySet(ctx context.Context, pairs []LostRuntimeRecoveryRequest, sessions []CommandlessLostRuntimeRecoveryRequest) ([]LostRuntimeRecoveryResult, []CommandlessLostRuntimeRecoveryResult, []store.LostRuntimeRecoveryPair, []domain.SessionID, error) {
	if len(pairs) == 0 && len(sessions) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("%w: at least one lost runtime is required", ErrLostRuntimeRecoveryIneligible)
	}
	pairResults := make([]LostRuntimeRecoveryResult, 0, len(pairs))
	storePairs := make([]store.LostRuntimeRecoveryPair, 0, len(pairs))
	seenSessions := make(map[domain.SessionID]struct{}, len(pairs)+len(sessions))
	seenCommands := make(map[domain.CommandID]struct{}, len(pairs))
	unreleasedPairs := 0
	for _, request := range pairs {
		if request.SessionID == "" || request.CommandID == "" {
			return pairResults, nil, nil, nil, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return pairResults, nil, nil, nil, fmt.Errorf("%w: duplicate session %s", ErrLostRuntimeRecoveryIneligible, request.SessionID)
		}
		if _, exists := seenCommands[request.CommandID]; exists {
			return pairResults, nil, nil, nil, fmt.Errorf("%w: duplicate command %s", ErrLostRuntimeRecoveryIneligible, request.CommandID)
		}
		seenSessions[request.SessionID] = struct{}{}
		seenCommands[request.CommandID] = struct{}{}
		result, err := s.checkLostRuntimeRecoveryTarget(ctx, request)
		if err != nil {
			return pairResults, nil, nil, nil, err
		}
		if !result.AlreadyRecovered {
			unreleasedPairs++
		}
		pairResults = append(pairResults, result)
		storePairs = append(storePairs, store.LostRuntimeRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID})
	}
	storeSessions := make([]domain.SessionID, 0, len(sessions))
	for _, request := range sessions {
		if request.SessionID == "" {
			return pairResults, nil, nil, nil, fmt.Errorf("%w: commandless session ID is required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return pairResults, nil, nil, nil, fmt.Errorf("%w: duplicate session %s", ErrLostRuntimeRecoveryIneligible, request.SessionID)
		}
		seenSessions[request.SessionID] = struct{}{}
		storeSessions = append(storeSessions, request.SessionID)
	}
	storedSessions := make([]store.CommandlessLostRuntimeRecovery, 0, len(storeSessions))
	if len(storeSessions) != 0 {
		var err error
		storedSessions, err = s.store.CheckCommandlessLostRuntimeRecoveryBatch(ctx, storeSessions)
		if err != nil {
			return pairResults, nil, nil, nil, fmt.Errorf("check commandless lost runtime recovery: %w", err)
		}
	}
	sessionResults := make([]CommandlessLostRuntimeRecoveryResult, 0, len(storedSessions))
	unreleasedSessions := 0
	for _, recovery := range storedSessions {
		if !recovery.AlreadyRecovered {
			unreleasedSessions++
		}
		sessionResults = append(sessionResults, CommandlessLostRuntimeRecoveryResult{Recovery: recovery})
	}
	liveSlots, err := s.store.CountLiveCommandSlots(ctx)
	if err != nil {
		return pairResults, sessionResults, nil, nil, err
	}
	liveReservations, err := s.store.CountLiveSessionReservations(ctx)
	if err != nil {
		return pairResults, sessionResults, nil, nil, err
	}
	if liveSlots != unreleasedPairs || liveReservations != unreleasedPairs+unreleasedSessions {
		return pairResults, sessionResults, nil, nil, fmt.Errorf("%w: live command slots=%d live session reservations=%d, want %d/%d selected retained runtimes", ErrLostRuntimeRecoveryIneligible, liveSlots, liveReservations, unreleasedPairs, unreleasedPairs+unreleasedSessions)
	}
	return pairResults, sessionResults, storePairs, storeSessions, nil
}

func (s *Service) finalizeCommandlessLostRuntimeRecovery(ctx context.Context, result CommandlessLostRuntimeRecoveryResult, recoverer LostRuntimeRecoverer) (CommandlessLostRuntimeRecoveryResult, error) {
	finalized, finalizeErr := recoverer.FinalizeLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Recovery.Session})
	result.Runtime = finalized
	if finalizeErr != nil || !finalized.CleanupConfirmed {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Recovery.Session, string(result.Recovery.CommandID))
		if finalizeErr != nil {
			return result, errors.Join(fmt.Errorf("%w: %v", ErrLostRuntimeRecoveryFinalization, finalizeErr), auditErr)
		}
		return result, errors.Join(ErrLostRuntimeRecoveryFinalization, auditErr)
	}
	if err := s.store.CompleteCommandlessLostRuntimeRecoveryFinalization(ctx, result.Recovery.SessionID); err != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Recovery.Session, string(result.Recovery.CommandID))
		return result, errors.Join(fmt.Errorf("%w: record finalization: %v", ErrLostRuntimeRecoveryFinalization, err), auditErr)
	}
	reservation, err := s.store.GetSessionReservation(ctx, result.Recovery.SessionID)
	if err != nil {
		return result, fmt.Errorf("read released commandless session reservation: %w", err)
	}
	result.Recovery.SessionReservation = reservation
	return result, nil
}
