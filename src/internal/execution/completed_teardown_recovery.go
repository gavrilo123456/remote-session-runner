package execution

import (
	"context"
	"errors"
	"fmt"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// CompletedTeardownRecoveryRequest selects an already successful one-off
// command whose terminal lost session still holds its reservation. This is
// an offline operator boundary, not a retry of the original request body.
type CompletedTeardownRecoveryRequest struct {
	SessionID domain.SessionID
	CommandID domain.CommandID
}

type CompletedTeardownRecoveryResult struct {
	Recovery store.CompletedTeardownRecovery
	Runtime  RuntimeReconcileResult
}

// CheckCompletedTeardownRecoveryBatch checks the complete idle inventory
// without reading scripts, touching process groups, or writing durable state.
func (s *Service) CheckCompletedTeardownRecoveryBatch(ctx context.Context, requests []CompletedTeardownRecoveryRequest) ([]CompletedTeardownRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	results, _, err := s.checkCompletedTeardownRecoveryBatch(ctx, requests)
	return results, err
}

// RecoverCompletedTeardownRecoveryBatch durably proves all selected runtime
// boundaries before atomically releasing only their session reservations.
// Command results, terminal job outcomes, and event histories remain intact.
func (s *Service) RecoverCompletedTeardownRecoveryBatch(ctx context.Context, requests []CompletedTeardownRecoveryRequest) ([]CompletedTeardownRecoveryResult, error) {
	if s == nil || s.store == nil || s.runtime == nil {
		return nil, ErrExecutionServiceConfiguration
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	results, pairs, err := s.checkCompletedTeardownRecoveryBatch(ctx, requests)
	if err != nil {
		return results, err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return results, fmt.Errorf("%w: runtime does not support teardown recovery", ErrLostRuntimeRecoveryIneligible)
	}
	for index := range results {
		if results[index].Recovery.AlreadyRecovered {
			continue
		}
		proof, proofErr := recoverer.ReconcileLostRuntime(ctx, RuntimeReconcileRequest{Session: results[index].Recovery.Session})
		results[index].Runtime = proof
		if proofErr != nil || !proof.CleanupConfirmed {
			s.store.RecordCleanupFailure()
			auditErr := s.recordRuntimeCleanupFailure(ctx, results[index].Recovery.Session, string(results[index].Recovery.Pair.CommandID))
			return results, errors.Join(ErrLostRuntimeRecoveryUnconfirmed, proofErr, auditErr)
		}
	}
	// Repeat the complete durable inventory check in the release transaction.
	// The runtime keeps ownership proof until this commit has succeeded.
	if err := s.store.ConfirmCompletedTeardownRecoveryBatch(ctx, pairs); err != nil {
		return results, fmt.Errorf("record completed teardown capacity release: %w", err)
	}
	var finalizationErrors []error
	for index := range results {
		// A completed repeat is a no-op; it must not touch an old process ID.
		if results[index].Recovery.AlreadyRecovered && !results[index].Recovery.FinalizationPending {
			continue
		}
		finalized, finalizeErr := s.finalizeCompletedTeardownRecovery(ctx, results[index], recoverer)
		results[index] = finalized
		if finalizeErr != nil {
			finalizationErrors = append(finalizationErrors, finalizeErr)
		}
	}
	return results, errors.Join(finalizationErrors...)
}

func (s *Service) checkCompletedTeardownRecoveryBatch(ctx context.Context, requests []CompletedTeardownRecoveryRequest) ([]CompletedTeardownRecoveryResult, []store.CompletedTeardownRecoveryPair, error) {
	pairs := make([]store.CompletedTeardownRecoveryPair, 0, len(requests))
	for _, request := range requests {
		pairs = append(pairs, store.CompletedTeardownRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID})
	}
	recoveries, err := s.store.CheckCompletedTeardownRecoveryBatch(ctx, pairs)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrLostRuntimeRecoveryIneligible, err)
	}
	results := make([]CompletedTeardownRecoveryResult, 0, len(recoveries))
	for _, recovery := range recoveries {
		results = append(results, CompletedTeardownRecoveryResult{Recovery: recovery})
	}
	return results, pairs, nil
}

func (s *Service) finalizeCompletedTeardownRecovery(ctx context.Context, result CompletedTeardownRecoveryResult, recoverer LostRuntimeRecoverer) (CompletedTeardownRecoveryResult, error) {
	finalized, finalizeErr := recoverer.FinalizeLostRuntime(ctx, RuntimeReconcileRequest{Session: result.Recovery.Session})
	result.Runtime = finalized
	if finalizeErr != nil || !finalized.CleanupConfirmed {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Recovery.Session, string(result.Recovery.Pair.CommandID))
		return result, errors.Join(ErrLostRuntimeRecoveryFinalization, finalizeErr, auditErr)
	}
	if err := s.store.CompleteCompletedTeardownRecoveryFinalization(ctx, result.Recovery.Pair); err != nil {
		s.store.RecordCleanupFailure()
		auditErr := s.recordRuntimeCleanupFailure(ctx, result.Recovery.Session, string(result.Recovery.Pair.CommandID))
		return result, errors.Join(fmt.Errorf("%w: record completed teardown finalization: %w", ErrLostRuntimeRecoveryFinalization, err), auditErr)
	}
	reservation, err := s.store.GetSessionReservation(ctx, result.Recovery.Pair.SessionID)
	if err != nil {
		return result, err
	}
	result.Recovery.SessionReservation = reservation
	result.Recovery.FinalizationPending = false
	return result, nil
}

// Startup resumes only durable post-release work. This path never proves or
// signals an old PID and must run before ownership attribution is audited.
func (s *Service) finalizePendingCompletedTeardownRecoveries(ctx context.Context) error {
	pending, err := s.store.ListPendingCompletedTeardownRecoveryFinalizations(ctx)
	if err != nil || len(pending) == 0 {
		return err
	}
	recoverer, ok := s.runtime.(LostRuntimeRecoverer)
	if !ok {
		return fmt.Errorf("%w: runtime does not support teardown finalization", ErrLostRuntimeRecoveryFinalization)
	}
	var finalizationErrors []error
	for _, recovery := range pending {
		if _, err := s.finalizeCompletedTeardownRecovery(ctx, CompletedTeardownRecoveryResult{Recovery: recovery}, recoverer); err != nil {
			finalizationErrors = append(finalizationErrors, err)
		}
	}
	return errors.Join(finalizationErrors...)
}
