package laneregistry

import (
	"sort"
)

// BeginPRDPostCommitRepair durably records a failure after commit. The
// operation stays in its exclusive group and remains admission-closed until
// the repair handoff is sealed.
func (r *Registry) BeginPRDPostCommitRepair(expectedGeneration uint64, operationID, failureDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if !postCommitRepairEligible(operation.State) || !validEvidenceDigest(failureDigest) || operation.PostCommitFailureState != "" {
			return false, ErrInvalidTransition
		}
		operation.PostCommitFailureState = operation.State
		operation.PostCommitFailureDigest = failureDigest
		operation.RepairPriorCandidateDigest = operation.PriorAcceptedCandidateDigest
		operation.RepairPriorAcceptanceID = operation.PriorAcceptanceID
		operation.RepairPriorAcceptanceDigest = operation.PriorAcceptanceDigest
		if operation.Acceptance != nil {
			operation.RepairPriorCandidateDigest = operation.CandidateSetDigest
			operation.RepairPriorAcceptanceID = operation.Acceptance.AcceptanceID
			operation.RepairPriorAcceptanceDigest = operation.Acceptance.EvidenceDigest
		}
		return true, appendPRDHistory(operation, PRDPostCommitRepairRequired, failureDigest)
	})
}

// BeginPRDRepairClosure records fresh closure epoch allocations for the
// currently installed candidate. Each new closure epoch must exceed both the
// operation epoch and the externally observed host anchor high-water.
func (r *Registry) BeginPRDRepairClosure(expectedGeneration uint64, operationID string, epochs []PRDRepairEpoch, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.State != PRDPostCommitRepairRequired || !validEvidenceDigest(evidenceDigest) || !validPRDRepairEpochs(*operation, epochs) {
			return false, ErrInvalidTransition
		}
		operation.RepairEpochs = clonePRDRepairEpochs(epochs)
		return true, appendPRDHistory(operation, PRDRepairClosurePending, evidenceDigest)
	})
}

func (r *Registry) RecordPRDRepairClosure(expectedGeneration uint64, operationID string, proof PRDRepairClosureProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.State != PRDRepairClosurePending || !validPRDRepairClosureProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.RepairClosures {
			if prior.Surface == proof.Surface || prior.DirectiveID == proof.DirectiveID || prior.AttestationID == proof.AttestationID {
				return false, ErrCollision
			}
		}
		operation.RepairClosures = append(operation.RepairClosures, proof)
		sort.Slice(operation.RepairClosures, func(i, j int) bool { return operation.RepairClosures[i].Surface < operation.RepairClosures[j].Surface })
		return true, nil
	})
}

func (r *Registry) CompletePRDRepairClosure(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.State != PRDRepairClosurePending || !completePRDRepairClosures(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		return true, appendPRDHistory(operation, PRDRepairQuiescing, evidenceDigest)
	})
}

func (r *Registry) RecordPRDRepairQuiescence(expectedGeneration uint64, operationID string, proof PRDQuiescenceProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.State != PRDRepairQuiescing || !validPRDRepairQuiescenceProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.RepairQuiescence {
			if prior.Surface == proof.Surface {
				return false, ErrCollision
			}
		}
		operation.RepairQuiescence = append(operation.RepairQuiescence, proof)
		sort.Slice(operation.RepairQuiescence, func(i, j int) bool {
			return operation.RepairQuiescence[i].Surface < operation.RepairQuiescence[j].Surface
		})
		return true, nil
	})
}

func (r *Registry) CompletePRDRepairQuiescence(expectedGeneration uint64, operationID, sealDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.State != PRDRepairQuiescing || !completePRDRepairQuiescence(*operation) || !validEvidenceDigest(sealDigest) {
			return false, ErrInvalidTransition
		}
		operation.RepairQuiescenceSealDigest = sealDigest
		return true, appendPRDHistory(operation, PRDRepairClosed, sealDigest)
	})
}

// CompletePRDPostCommitRepair seals the next-candidate handoff. A primary
// archives alone; a recovery child and its restore_required parent archive
// atomically. In both cases the operation group releases only at this CAS.
func (r *Registry) CompletePRDPostCommitRepair(expectedGeneration uint64, operationID, handoffDigest, nextCandidateCriteriaDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(group *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.State != PRDRepairClosed || !completePRDRepairClosures(*operation) || !completePRDRepairQuiescence(*operation) ||
			!validEvidenceDigest(operation.RepairQuiescenceSealDigest) || !validEvidenceDigest(handoffDigest) ||
			!validEvidenceDigest(nextCandidateCriteriaDigest) || group.ActiveOperationID != operation.ID {
			return false, ErrInvalidTransition
		}
		operation.RepairHandoffDigest = handoffDigest
		operation.NextQASCandidateCriteriaDigest = nextCandidateCriteriaDigest
		if err := appendPRDHistory(operation, PRDOperationArchived, handoffDigest); err != nil {
			return false, err
		}
		if operation.ParentID != "" {
			parent, ok := prdOperationByIDMutable(&group.Operations, operation.ParentID)
			if !ok || parent.State != PRDRestoreRequired || !containsPRDString(parent.RecoveryChildIDs, operation.ID) {
				return false, ErrInvalidTransition
			}
			if err := appendPRDHistory(parent, PRDOperationAborted, handoffDigest); err != nil {
				return false, err
			}
			if err := appendPRDHistory(parent, PRDOperationArchived, handoffDigest); err != nil {
				return false, err
			}
		}
		group.ActiveOperationID = ""
		return true, nil
	})
}

func postCommitRepairEligible(state PRDOperationState) bool {
	switch state {
	case PRDCommitted, PRDReopenPrepared, PRDReopenAuthorized, PRDReopened, PRDAcceptancePending, PRDAccepted,
		PRDRecoveryReopenPrepared, PRDRecoveryReopenAuthorized, PRDRecoveryReopened, PRDRecoveryAcceptancePending, PRDRecoveryAccepted:
		return true
	default:
		return false
	}
}

func validPRDRepairProof(operation PRDOperation) bool {
	repairStarted := prdHistoryReached(operation, PRDPostCommitRepairRequired)
	if !repairStarted {
		return operation.PostCommitFailureState == "" && operation.PostCommitFailureDigest == "" &&
			operation.RepairPriorCandidateDigest == "" && operation.RepairPriorAcceptanceID == "" && operation.RepairPriorAcceptanceDigest == "" &&
			len(operation.RepairEpochs) == 0 && len(operation.RepairClosures) == 0 && len(operation.RepairQuiescence) == 0 &&
			operation.RepairQuiescenceSealDigest == "" && operation.RepairHandoffDigest == "" && operation.NextQASCandidateCriteriaDigest == ""
	}
	if !postCommitRepairEligible(operation.PostCommitFailureState) || !validEvidenceDigest(operation.PostCommitFailureDigest) ||
		!validEvidenceDigest(operation.ContinuityBundleDigest) || !validEvidenceDigest(operation.RepairPriorCandidateDigest) ||
		operation.RepairPriorAcceptanceID == "" || !validEvidenceDigest(operation.RepairPriorAcceptanceDigest) {
		return false
	}
	var failureIndex int = -1
	for index, event := range operation.History {
		if event.To == PRDPostCommitRepairRequired {
			failureIndex = index
			break
		}
	}
	if failureIndex <= 0 || operation.History[failureIndex].From != operation.PostCommitFailureState ||
		operation.History[failureIndex].EvidenceDigest != operation.PostCommitFailureDigest {
		return false
	}
	if operation.PostCommitFailureState == PRDAccepted || operation.PostCommitFailureState == PRDRecoveryAccepted {
		if operation.Acceptance == nil || operation.RepairPriorCandidateDigest != operation.CandidateSetDigest ||
			operation.RepairPriorAcceptanceID != operation.Acceptance.AcceptanceID || operation.RepairPriorAcceptanceDigest != operation.Acceptance.EvidenceDigest {
			return false
		}
	} else if operation.RepairPriorCandidateDigest != operation.PriorAcceptedCandidateDigest ||
		operation.RepairPriorAcceptanceID != operation.PriorAcceptanceID || operation.RepairPriorAcceptanceDigest != operation.PriorAcceptanceDigest {
		return false
	}
	if !prdHistoryReached(operation, PRDRepairClosurePending) {
		return len(operation.RepairEpochs) == 0 && len(operation.RepairClosures) == 0 && len(operation.RepairQuiescence) == 0 &&
			operation.RepairQuiescenceSealDigest == "" && operation.RepairHandoffDigest == "" && operation.NextQASCandidateCriteriaDigest == ""
	}
	if !validPRDRepairEpochs(operation, operation.RepairEpochs) || !validPRDRepairClosureSet(operation) || !validPRDRepairQuiescenceSet(operation) {
		return false
	}
	if prdHistoryReached(operation, PRDRepairQuiescing) && !completePRDRepairClosures(operation) {
		return false
	}
	if prdHistoryReached(operation, PRDRepairClosed) && (!completePRDRepairClosures(operation) || !completePRDRepairQuiescence(operation) || !validEvidenceDigest(operation.RepairQuiescenceSealDigest)) {
		return false
	}
	if prdHistoryReached(operation, PRDRepairClosed) {
		sealed := false
		for _, event := range operation.History {
			if event.To == PRDRepairClosed && event.EvidenceDigest == operation.RepairQuiescenceSealDigest {
				sealed = true
			}
		}
		if !sealed {
			return false
		}
	}
	if prdHistoryReached(operation, PRDOperationArchived) && prdHistoryReached(operation, PRDRepairClosed) {
		if !validEvidenceDigest(operation.RepairHandoffDigest) || !validEvidenceDigest(operation.NextQASCandidateCriteriaDigest) {
			return false
		}
		for index := len(operation.History) - 1; index >= 0; index-- {
			if operation.History[index].To == PRDOperationArchived {
				return operation.History[index].EvidenceDigest == operation.RepairHandoffDigest
			}
		}
		return false
	}
	return operation.RepairHandoffDigest == "" && operation.NextQASCandidateCriteriaDigest == ""
}

func validPRDRepairEpochs(operation PRDOperation, epochs []PRDRepairEpoch) bool {
	if len(epochs) != len(operationSurfaces) {
		return false
	}
	for index, surface := range operationSurfaces {
		epoch := epochs[index]
		base, ok := prdEpochForSurface(operation.Epochs, surface)
		floor := epoch.AnchorHighWater
		if !ok || epoch.Surface != surface || epoch.AnchorHighWater < base.AnchorHighWater {
			return false
		}
		if base.CandidateEpoch > floor {
			floor = base.CandidateEpoch
		}
		if epoch.ClosureEpoch <= floor {
			return false
		}
	}
	return true
}

func validPRDRepairClosureProof(operation PRDOperation, proof PRDRepairClosureProof) bool {
	epoch, ok := prdRepairEpochForSurface(operation.RepairEpochs, proof.Surface)
	return ok && proof.CandidateSetDigest == operation.CandidateSetDigest && proof.Epoch == epoch.ClosureEpoch &&
		proof.DirectiveID != "" && proof.AttestationID != "" && proof.Closed && validEvidenceDigest(proof.EvidenceDigest)
}

func validPRDRepairQuiescenceProof(operation PRDOperation, proof PRDQuiescenceProof) bool {
	attestation := IncumbentQuiescenceAttestation{
		Surface: proof.Surface, Attempt: proof.Attempt, InventoryDigest: proof.InventoryDigest,
		Dispositions: proof.Dispositions, DeferredEvidence: proof.DeferredEvidence,
		RemainingWork: proof.RemainingWork, ZeroWorkPostflight: proof.ZeroWorkPostflight,
		PostflightDigest: proof.PostflightDigest,
	}
	return proof.CandidateSetDigest == operation.CandidateSetDigest && validQuiescenceAttestation(attestation) &&
		proof.RemainingWork == 0 && proof.ZeroWorkPostflight && validEvidenceDigest(proof.EvidenceDigest)
}

func validPRDRepairClosureSet(operation PRDOperation) bool {
	if len(operation.RepairClosures) > len(operationSurfaces) {
		return false
	}
	seen := map[string]struct{}{}
	priorSurface := ""
	for _, proof := range operation.RepairClosures {
		if proof.Surface <= priorSurface || !validPRDRepairClosureProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
		for _, id := range []string{proof.DirectiveID, proof.AttestationID} {
			if _, exists := seen[id]; exists {
				return false
			}
			seen[id] = struct{}{}
		}
	}
	return true
}

func validPRDRepairQuiescenceSet(operation PRDOperation) bool {
	if len(operation.RepairQuiescence) > len(operationSurfaces) {
		return false
	}
	priorSurface := ""
	for _, proof := range operation.RepairQuiescence {
		if proof.Surface <= priorSurface || !validPRDRepairQuiescenceProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
	}
	return true
}

func completePRDRepairClosures(operation PRDOperation) bool {
	if len(operation.RepairClosures) != len(operationSurfaces) {
		return false
	}
	for index, surface := range operationSurfaces {
		if operation.RepairClosures[index].Surface != surface || !validPRDRepairClosureProof(operation, operation.RepairClosures[index]) {
			return false
		}
	}
	return true
}

func completePRDRepairQuiescence(operation PRDOperation) bool {
	if len(operation.RepairQuiescence) != len(operationSurfaces) {
		return false
	}
	for index, surface := range operationSurfaces {
		if operation.RepairQuiescence[index].Surface != surface || !validPRDRepairQuiescenceProof(operation, operation.RepairQuiescence[index]) {
			return false
		}
	}
	return true
}

func prdRepairEpochForSurface(epochs []PRDRepairEpoch, surface string) (PRDRepairEpoch, bool) {
	index := sort.Search(len(epochs), func(i int) bool { return epochs[i].Surface >= surface })
	if index < len(epochs) && epochs[index].Surface == surface {
		return epochs[index], true
	}
	return PRDRepairEpoch{}, false
}

func clonePRDRepairEpochs(epochs []PRDRepairEpoch) []PRDRepairEpoch {
	cloned := append([]PRDRepairEpoch(nil), epochs...)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Surface < cloned[j].Surface })
	return cloned
}
