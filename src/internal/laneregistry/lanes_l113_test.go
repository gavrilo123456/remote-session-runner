package laneregistry

import (
	"errors"
	"testing"
)

func TestLanesL113PRDPostCommitRepairRequiresFreshClosureQuiescenceAndHandoff(t *testing.T) {
	registry, snapshot, group := l112QuiescedMaintenance(t)
	registryPath := registry.path
	defer func() { _ = registry.Close() }()
	parent := group.Operations[0]
	if _, err := registry.BeginPRDPostCommitRepair(snapshot.Generation, parent.ID, testDigest('1')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pre-commit operation entered post-commit repair: %v", err)
	}
	snapshot, parent = l113CommitMaintenance(t, registry, snapshot, parent)
	snapshot, err := registry.BeginPRDPostCommitRepair(snapshot.Generation, parent.ID, testDigest('2'))
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	if parent.State != PRDPostCommitRepairRequired || parent.PostCommitFailureState != PRDCommitted ||
		parent.PostCommitFailureDigest != testDigest('2') || parent.RepairPriorCandidateDigest != parent.PriorAcceptedCandidateDigest ||
		parent.RepairPriorAcceptanceID != parent.PriorAcceptanceID || parent.ContinuityBundleDigest == "" {
		t.Fatalf("post-commit failure lineage was not retained: %+v", parent)
	}
	badEpochs := l113RepairEpochs(parent)
	badEpochs[0].ClosureEpoch = parent.Epochs[0].CandidateEpoch
	if _, err := registry.BeginPRDRepairClosure(snapshot.Generation, parent.ID, badEpochs, testDigest('3')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("repair accepted a stale closure epoch: %v", err)
	}
	snapshot, err = registry.BeginPRDRepairClosure(snapshot.Generation, parent.ID, l113RepairEpochs(parent), testDigest('3'))
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	for index, surface := range operationSurfaces[:len(operationSurfaces)-1] {
		proof := l113RepairClosure(parent, surface, index)
		if index == 0 {
			proof.CandidateSetDigest = testDigest('f')
			if _, err := registry.RecordPRDRepairClosure(snapshot.Generation, parent.ID, proof); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("closure proof for a different candidate was accepted: %v", err)
			}
			proof = l113RepairClosure(parent, surface, index)
		}
		snapshot, err = registry.RecordPRDRepairClosure(snapshot.Generation, parent.ID, proof)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.CompletePRDRepairClosure(snapshot.Generation, parent.ID, testDigest('4')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial closure fanout was treated as global: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err = Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	surface := operationSurfaces[len(operationSurfaces)-1]
	snapshot, err = registry.RecordPRDRepairClosure(snapshot.Generation, parent.ID, l113RepairClosure(parent, surface, 3))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.CompletePRDRepairClosure(snapshot.Generation, parent.ID, testDigest('4'))
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	unsafe := l113RepairQuiescence(parent, operationSurfaces[0], 0)
	unsafe.RemainingWork = 1
	if _, err := registry.RecordPRDRepairQuiescence(snapshot.Generation, parent.ID, unsafe); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("nonzero-work repair postflight was accepted: %v", err)
	}
	unsafe = l113RepairQuiescence(parent, operationSurfaces[0], 0)
	unsafe.DeferredEvidence = nil
	if _, err := registry.RecordPRDRepairQuiescence(snapshot.Generation, parent.ID, unsafe); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("deferred uncertain work without retained pair evidence was accepted: %v", err)
	}
	for index, surface := range operationSurfaces[:2] {
		snapshot, err = registry.RecordPRDRepairQuiescence(snapshot.Generation, parent.ID, l113RepairQuiescence(parent, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.CompletePRDRepairQuiescence(snapshot.Generation, parent.ID, testDigest('5')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial zero-work quiescence was accepted: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err = Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	for index, surface := range operationSurfaces[2:] {
		snapshot, err = registry.RecordPRDRepairQuiescence(snapshot.Generation, parent.ID, l113RepairQuiescence(parent, surface, index+2))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompletePRDRepairQuiescence(snapshot.Generation, parent.ID, testDigest('5'))
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	if len(parent.RepairQuiescence) != len(operationSurfaces) || len(parent.RepairQuiescence[0].DeferredEvidence) != 1 {
		t.Fatalf("handoff lost deferred request/ACK evidence: %+v", parent.RepairQuiescence)
	}
	if _, err := registry.CompletePRDPostCommitRepair(snapshot.Generation, parent.ID, "", testDigest('6')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("handoff archived without a seal: %v", err)
	}
	snapshot, err = registry.CompletePRDPostCommitRepair(snapshot.Generation, parent.ID, testDigest('7'), testDigest('8'))
	if err != nil {
		t.Fatal(err)
	}
	parent = l113Primary(snapshot)
	group, ok := prdOperationGroupByID(snapshot.PRDOperationGroups, parent.ID)
	if !ok || group.ActiveOperationID != "" || parent.State != PRDOperationArchived || parent.RepairHandoffDigest != testDigest('7') || parent.NextQASCandidateCriteriaDigest != testDigest('8') {
		t.Fatalf("repair handoff did not archive and release the primary: group=%+v operation=%+v", group, parent)
	}
	reopened, err := Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Snapshot()
	if err != nil || persisted.PRDOperationGroups[0].ActiveOperationID != "" {
		t.Fatalf("repair handoff did not persist across restart: %+v err=%v", persisted, err)
	}
}

func TestLanesL113RecoveryChildRepairHandoffArchivesParentAndPreventsReplacement(t *testing.T) {
	registry, snapshot, group := l112FailedMaintenance(t)
	defer registry.Close()
	parent := group.Operations[0]
	wrongBundle := l112RestoreSpec(parent)
	wrongBundle.ContinuityBundleDigest = testDigest('1')
	if _, _, err := registry.BeginPRDRecoveryChild(snapshot.Generation, wrongBundle); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("restore child changed the preserved continuity bundle: %v", err)
	}
	snapshot, child, err := registry.BeginPRDRecoveryChild(snapshot.Generation, l112RestoreSpec(parent))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDRecoveryCandidateClosed(snapshot.Generation, child.ID, l112RecoveryClosed(child, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompletePRDRecoveryCandidateClosed(snapshot.Generation, child.ID, testDigest('1'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationRemoteSurfaces {
		snapshot, err = registry.RecordPRDTransportReady(snapshot.Generation, child.ID, l112TransportReady(child, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.PreparePRDReopen(snapshot.Generation, child.ID, testDigest('2'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDAuthorization(snapshot.Generation, child.ID, l112Authorization(child, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.AuthorizePRDReopen(snapshot.Generation, child.ID, testDigest('3'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationRemoteSurfaces {
		snapshot, err = registry.RecordPRDRecoveryOpen(snapshot.Generation, child.ID, l112RecoveryOpen(child, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.RecordPRDRecoveryOpen(snapshot.Generation, child.ID, l112RecoveryOpen(child, SurfaceMac, 3))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.CompletePRDRecoveryOpen(snapshot.Generation, child.ID, testDigest('4'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginPRDRecoveryAcceptance(snapshot.Generation, child.ID, testDigest('5'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.RecordPRDRecoveryAcceptance(snapshot.Generation, child.ID, l112Acceptance(child))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginPRDPostCommitRepair(snapshot.Generation, child.ID, testDigest('6'))
	if err != nil {
		t.Fatal(err)
	}
	child = l113Operation(snapshot, child.ID)
	if child.RepairPriorCandidateDigest != child.CandidateSetDigest || child.RepairPriorAcceptanceID != child.Acceptance.AcceptanceID || child.RepairPriorAcceptanceDigest != child.Acceptance.EvidenceDigest {
		t.Fatalf("accepted child did not become the last accepted repair lineage: %+v", child)
	}
	snapshot = l113CompleteRepair(t, registry, snapshot, child)
	snapshot, err = registry.CompletePRDPostCommitRepair(snapshot.Generation, child.ID, testDigest('4'), testDigest('5'))
	if err != nil {
		t.Fatal(err)
	}
	group, ok := prdOperationGroupByID(snapshot.PRDOperationGroups, parent.ID)
	if !ok || group.ActiveOperationID != "" || group.Operations[0].State != PRDOperationArchived || group.Operations[1].State != PRDOperationArchived {
		t.Fatalf("child repair handoff did not atomically archive parent and child: %+v", group)
	}
	if _, _, err := registry.BeginPRDRecoveryChild(snapshot.Generation, l112RestoreSpec(group.Operations[0])); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("replacement child was allowed after terminal repair handoff: %v", err)
	}
}

func TestLanesL113AlteredRepairHistoryIsRejected(t *testing.T) {
	t.Run("failure event digest", func(t *testing.T) {
		registry, snapshot, group := l112QuiescedMaintenance(t)
		defer registry.Close()
		snapshot, operation := l113CommitMaintenance(t, registry, snapshot, group.Operations[0])
		snapshot, err := registry.BeginPRDPostCommitRepair(snapshot.Generation, operation.ID, testDigest('1'))
		if err != nil {
			t.Fatal(err)
		}
		operation = l113Primary(snapshot)
		operation.History[len(operation.History)-1].EvidenceDigest = testDigest('2')
		snapshot.PRDOperationGroups[0].Operations[0] = operation
		if err := ValidateSnapshot(snapshot); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("altered post-commit failure event was accepted: %v", err)
		}
	})
	t.Run("stale repair closure epoch", func(t *testing.T) {
		registry, snapshot, group := l112QuiescedMaintenance(t)
		defer registry.Close()
		snapshot, operation := l113CommitMaintenance(t, registry, snapshot, group.Operations[0])
		snapshot, err := registry.BeginPRDPostCommitRepair(snapshot.Generation, operation.ID, testDigest('1'))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err = registry.BeginPRDRepairClosure(snapshot.Generation, operation.ID, l113RepairEpochs(l113Primary(snapshot)), testDigest('2'))
		if err != nil {
			t.Fatal(err)
		}
		operation = l113Primary(snapshot)
		snapshot.PRDOperationGroups[0].Operations[0].RepairEpochs[0].ClosureEpoch = operation.Epochs[0].CandidateEpoch
		if err := ValidateSnapshot(snapshot); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("altered repair epoch history was accepted: %v", err)
		}
	})
}

func l113CommitMaintenance(t *testing.T, registry *Registry, snapshot Snapshot, operation PRDOperation) (Snapshot, PRDOperation) {
	t.Helper()
	var err error
	snapshot, err = registry.StagePRDMaintenance(snapshot.Generation, operation.ID, testDigest('1'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDMaintenanceCandidateClosed(snapshot.Generation, operation.ID, l112RecoveryClosed(operation, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompletePRDMaintenanceCandidateClosed(snapshot.Generation, operation.ID, testDigest('2'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.CommitPRDMaintenance(snapshot.Generation, operation.ID, testDigest('3'))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, l113Primary(snapshot)
}

func l113CompleteRepair(t *testing.T, registry *Registry, snapshot Snapshot, operation PRDOperation) Snapshot {
	t.Helper()
	var err error
	snapshot, err = registry.BeginPRDRepairClosure(snapshot.Generation, operation.ID, l113RepairEpochs(operation), testDigest('1'))
	if err != nil {
		t.Fatal(err)
	}
	operation = l113Operation(snapshot, operation.ID)
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDRepairClosure(snapshot.Generation, operation.ID, l113RepairClosure(operation, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompletePRDRepairClosure(snapshot.Generation, operation.ID, testDigest('2'))
	if err != nil {
		t.Fatal(err)
	}
	operation = l113Operation(snapshot, operation.ID)
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDRepairQuiescence(snapshot.Generation, operation.ID, l113RepairQuiescence(operation, surface, index))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompletePRDRepairQuiescence(snapshot.Generation, operation.ID, testDigest('3'))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func l113RepairEpochs(operation PRDOperation) []PRDRepairEpoch {
	epochs := make([]PRDRepairEpoch, 0, len(operationSurfaces))
	for _, prior := range operation.Epochs {
		epochs = append(epochs, PRDRepairEpoch{
			Surface: prior.Surface, AnchorHighWater: prior.CandidateEpoch,
			ClosureEpoch: prior.CandidateEpoch + 1,
		})
	}
	return epochs
}

func l113RepairClosure(operation PRDOperation, surface string, index int) PRDRepairClosureProof {
	epoch, _ := prdRepairEpochForSurface(operation.RepairEpochs, surface)
	return PRDRepairClosureProof{
		Surface: surface, CandidateSetDigest: operation.CandidateSetDigest, Epoch: epoch.ClosureEpoch,
		DirectiveID:   "repair-closure-directive-" + surface,
		AttestationID: "repair-closure-attestation-" + surface, Closed: true,
		EvidenceDigest: testDigest(byte('a' + index)),
	}
}

func l113RepairQuiescence(operation PRDOperation, surface string, index int) PRDQuiescenceProof {
	proof := l112Quiescence(operation, surface, index)
	proof.CandidateSetDigest = operation.CandidateSetDigest
	return proof
}

func l113Primary(snapshot Snapshot) PRDOperation {
	return l113Operation(snapshot, snapshot.PRDOperationGroups[0].PrimaryOperationID)
}

func l113Operation(snapshot Snapshot, id string) PRDOperation {
	for _, group := range snapshot.PRDOperationGroups {
		if operation, ok := prdOperationByID(group.Operations, id); ok {
			return operation
		}
	}
	return PRDOperation{}
}
