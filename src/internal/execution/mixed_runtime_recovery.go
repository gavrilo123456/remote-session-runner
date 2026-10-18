package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// CheckMixedRuntimeRecoverySet validates one complete offline recovery
// inventory containing terminal lost commands and completed-command teardown
// failures. It performs no runtime action and writes no durable state.
func (s *Service) CheckMixedRuntimeRecoverySet(ctx context.Context, lost []LostRuntimeRecoveryRequest, teardown []CompletedTeardownRecoveryRequest) ([]LostRuntimeRecoveryResult, []CompletedTeardownRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	lostResults, teardownResults, _, _, err := s.checkMixedRuntimeRecoverySet(ctx, lost, teardown)
	return lostResults, teardownResults, err
}

// RecoverMixedRuntimeRecoverySet proves every selected process boundary first,
// atomically releases the full selected capacity set, and then finalizes each
// ownership marker. It never claims/replays commands or changes command, job,
// output, or event history. A retry after a post-release finalization failure
// performs only that selected finalization work.
func (s *Service) RecoverMixedRuntimeRecoverySet(ctx context.Context, lost []LostRuntimeRecoveryRequest, teardown []CompletedTeardownRecoveryRequest) ([]LostRuntimeRecoveryResult, []CompletedTeardownRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	lostResults, teardownResults, lostPairs, teardownPairs, err := s.checkMixedRuntimeRecoverySet(ctx, lost, teardown)
	if err != nil {
		return lostResults, teardownResults, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return lostResults, teardownResults, fmt.Errorf("%w: runtime does not support mixed offline recovery", ErrLostRuntimeRecoveryIneligible)
	}
	for index := range lostResults {
		result := &lostResults[index]
		if result.AlreadyRecovered && !result.FinalizationPending {
			continue
		}
		if result.AlreadyRecovered {
			continue
		}
		proof, proofErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Session})
		result.Runtime = proof
		if proofErr != nil || !proof.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, result.Session, string(lostPairs[index].CommandID))
			return lostResults, teardownResults, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, proofErr, auditErr)
		}
	}
	for index := range teardownResults {
		result := &teardownResults[index]
		if result.Recovery.AlreadyRecovered && !result.Recovery.FinalizationPending {
			continue
		}
		if result.Recovery.AlreadyRecovered {
			continue
		}
		proof, proofErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Recovery.Session})
		result.Runtime = proof
		if proofErr != nil || !proof.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, result.Recovery.Session, string(teardownPairs[index].CommandID))
			return lostResults, teardownResults, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, proofErr, auditErr)
		}
	}
	if err := s.store.ConfirmMixedRuntimeRecoverySet(ctx, lostPairs, teardownPairs); err != nil {
		return lostResults, teardownResults, fmt.Errorf("record mixed runtime cleanup and capacity release: %w", err)
	}
	var finalizationErrors []error
	for index := range lostResults {
		result := lostResults[index]
		if result.AlreadyRecovered && !result.FinalizationPending {
			continue
		}
		finalized, finalizeErr := s.finalizeLostRuntimeRecovery(ctx, lost[index], result, recoverer)
		lostResults[index] = finalized
		if finalizeErr == nil {
			lostResults[index].FinalizationPending = false
		}
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	for index := range teardownResults {
		result := teardownResults[index]
		if result.Recovery.AlreadyRecovered && !result.Recovery.FinalizationPending {
			continue
		}
		finalized, finalizeErr := s.finalizeCompletedTeardownRecovery(ctx, result, recoverer)
		teardownResults[index] = finalized
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	return lostResults, teardownResults, errors.Join(finalizationErrors...)
}

func (s *Service) checkMixedRuntimeRecoverySet(ctx context.Context, lost []LostRuntimeRecoveryRequest, teardown []CompletedTeardownRecoveryRequest) ([]LostRuntimeRecoveryResult, []CompletedTeardownRecoveryResult, []store.LostRuntimeRecoveryPair, []store.CompletedTeardownRecoveryPair, error) {
	lostPairs := make([]store.LostRuntimeRecoveryPair, 0, len(lost))
	teardownPairs := make([]store.CompletedTeardownRecoveryPair, 0, len(teardown))
	seenSessions := make(map[domain.SessionID]struct{}, len(lost)+len(teardown))
	seenCommands := make(map[domain.CommandID]struct{}, len(lost)+len(teardown))
	for _, request := range lost {
		if request.SessionID == "" || request.CommandID == "" {
			return nil, nil, nil, nil, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return nil, nil, nil, nil, fmt.Errorf("%w: duplicate session selection", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenCommands[request.CommandID]; exists {
			return nil, nil, nil, nil, fmt.Errorf("%w: duplicate command selection", ErrLostRuntimeRecoveryIneligible)
		}
		seenSessions[request.SessionID] = struct{}{}
		seenCommands[request.CommandID] = struct{}{}
		lostPairs = append(lostPairs, store.LostRuntimeRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID})
	}
	for _, request := range teardown {
		if request.SessionID == "" || request.CommandID == "" {
			return nil, nil, nil, nil, fmt.Errorf("%w: session and command IDs are required", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenSessions[request.SessionID]; exists {
			return nil, nil, nil, nil, fmt.Errorf("%w: duplicate session selection", ErrLostRuntimeRecoveryIneligible)
		}
		if _, exists := seenCommands[request.CommandID]; exists {
			return nil, nil, nil, nil, fmt.Errorf("%w: duplicate command selection", ErrLostRuntimeRecoveryIneligible)
		}
		seenSessions[request.SessionID] = struct{}{}
		seenCommands[request.CommandID] = struct{}{}
		teardownPairs = append(teardownPairs, store.CompletedTeardownRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID})
	}
	if len(lostPairs) == 0 && len(teardownPairs) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("%w: an explicit recovery selection is required", ErrLostRuntimeRecoveryIneligible)
	}
	set, err := s.store.CheckMixedRuntimeRecoverySet(ctx, lostPairs, teardownPairs)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%w: %w", ErrLostRuntimeRecoveryIneligible, err)
	}
	lostResults := make([]LostRuntimeRecoveryResult, 0, len(set.Lost))
	for _, recovery := range set.Lost {
		lostResults = append(lostResults, LostRuntimeRecoveryResult{
			Session: recovery.Session, Command: recovery.Command, CommandSlot: recovery.CommandSlot,
			SessionReservation: recovery.SessionReservation, AlreadyRecovered: recovery.AlreadyRecovered,
			FinalizationPending: recovery.FinalizationPending,
		})
	}
	teardownResults := make([]CompletedTeardownRecoveryResult, 0, len(set.Teardown))
	for _, recovery := range set.Teardown {
		teardownResults = append(teardownResults, CompletedTeardownRecoveryResult{Recovery: recovery})
	}
	return lostResults, teardownResults, lostPairs, teardownPairs, nil
}
