package laneregistry

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestLanesL112PRDMaintenanceRequiresFullClosureAndZeroWork(t *testing.T) {
	registry, snapshot, group := l112StartMaintenance(t)
	defer registry.Close()
	operation := group.Operations[0]
	if _, _, err := registry.BeginPRDMaintenance(snapshot.Generation, l112MaintenanceSpec()); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("second PRD group was not excluded: %v", err)
	}
	if _, err := registry.BeginPRDMaintenanceQuiescence(snapshot.Generation, operation.ID, testDigest('f')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("quiescence began before closure: %v", err)
	}
	if _, err := registry.CompletePRDMaintenanceClosure(snapshot.Generation, operation.ID, testDigest('f')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial closure advanced: %v", err)
	}
	for index, surface := range operationSurfaces {
		proof := l112Closure(operation, surface, index)
		var err error
		snapshot, err = registry.RecordPRDMaintenanceClosure(snapshot.Generation, operation.ID, proof)
		if err != nil {
			t.Fatalf("record closure %s: %v", surface, err)
		}
	}
	snapshot, err := registry.CompletePRDMaintenanceClosure(snapshot.Generation, operation.ID, testDigest('1'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginPRDMaintenanceQuiescence(snapshot.Generation, operation.ID, testDigest('2'))
	if err != nil {
		t.Fatal(err)
	}
	if state := snapshot.PRDOperationGroups[0].Operations[0].State; state != PRDQuiescing {
		t.Fatalf("maintenance state after BeginQuiescence = %s", state)
	}
	unsafeQuiescence := l112Quiescence(operation, operationSurfaces[0], 0)
	unsafeQuiescence.RemainingWork = 1
	if _, err := registry.RecordPRDMaintenanceQuiescence(snapshot.Generation, operation.ID, unsafeQuiescence); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("nonzero-work postflight was accepted: %v", err)
	}
	if _, err := registry.CompletePRDMaintenanceQuiescence(snapshot.Generation, operation.ID, testDigest('6')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("incomplete quiescence advanced: %v", err)
	}
	for index, surface := range operationSurfaces {
		proof := l112Quiescence(operation, surface, index)
		if !validPRDQuiescenceProof(operation, proof) {
			t.Fatalf("fixture quiescence proof is invalid: %+v", proof)
		}
		snapshot, err = registry.RecordPRDMaintenanceQuiescence(snapshot.Generation, operation.ID, proof)
		if err != nil {
			t.Fatalf("record quiescence %s: %v", surface, err)
		}
	}
	snapshot, err = registry.CompletePRDMaintenanceQuiescence(snapshot.Generation, operation.ID, testDigest('7'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.FailPRDMaintenance(snapshot.Generation, operation.ID, testDigest('8'))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := registry.Snapshot()
	if err != nil || stored.PRDOperationGroups[0].Operations[0].State != PRDRestoreRequired || stored.PRDOperationGroups[0].ActiveOperationID != operation.ID {
		t.Fatalf("failed maintenance did not retain its group: snapshot=%+v err=%v", stored, err)
	}
}

func TestLanesL112PRDRecoveryIsTypedLinkedExclusiveAndMonotonic(t *testing.T) {
	registry, snapshot, group := l112FailedMaintenance(t)
	defer registry.Close()
	parent := group.Operations[0]
	wrongType := l112RestoreSpec(parent)
	wrongType.CandidateSetDigest = testDigest('c')
	if _, _, err := registry.BeginPRDRecoveryChild(snapshot.Generation, wrongType); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("restore accepted a candidate outside incumbent lineage: %v", err)
	}
	staleEpoch := l112RestoreSpec(parent)
	for i := range staleEpoch.Epochs {
		staleEpoch.Epochs[i].AnchorHighWater = 0
		staleEpoch.Epochs[i].ClosureEpoch = parent.Epochs[i].CandidateEpoch
		staleEpoch.Epochs[i].CandidateEpoch = parent.Epochs[i].CandidateEpoch + 1
	}
	if _, _, err := registry.BeginPRDRecoveryChild(snapshot.Generation, staleEpoch); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("recovery accepted a non-increasing epoch: %v", err)
	}
	snapshot, child, err := registry.BeginPRDRecoveryChild(snapshot.Generation, l112RestoreSpec(parent))
	if err != nil {
		t.Fatal(err)
	}
	secondChild := l112RestoreSpec(parent)
	if _, _, err := registry.BeginPRDRecoveryChild(snapshot.Generation, secondChild); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("second active child was not excluded: %v", err)
	}
	if _, err := registry.CompletePRDRecoveryCandidateClosed(snapshot.Generation, child.ID, testDigest('9')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("incomplete child closed set advanced: %v", err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDRecoveryCandidateClosed(snapshot.Generation, child.ID, l112RecoveryClosed(child, surface, index))
		if err != nil {
			t.Fatalf("record child closed proof %s: %v", surface, err)
		}
	}
	if _, err := registry.AbortPRDRecoveryChild(snapshot.Generation, child.ID, testDigest('b')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("recovery child aborted before full closure completion: %v", err)
	}
	snapshot, err = registry.CompletePRDRecoveryCandidateClosed(snapshot.Generation, child.ID, testDigest('a'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.AbortPRDRecoveryChild(snapshot.Generation, child.ID, testDigest('b'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.ArchiveAbortedPRDRecoveryChild(snapshot.Generation, child.ID, testDigest('b'))
	if err != nil {
		t.Fatal(err)
	}
	parent = snapshot.PRDOperationGroups[0].Operations[0]
	missingForwardApproval := l112ForwardRepairSpec(parent, 15)
	missingForwardApproval.AuthorityApprovalDigest = ""
	if _, _, err := registry.BeginPRDRecoveryChild(snapshot.Generation, missingForwardApproval); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("forward repair without exact approvals accepted: %v", err)
	}
	forward := l112ForwardRepairSpec(parent, 15)
	snapshot, replacement, err := registry.BeginPRDRecoveryChild(snapshot.Generation, forward)
	if err != nil {
		t.Fatalf("higher-epoch forward-repair replacement: %v", err)
	}
	if replacement.ParentID != parent.ID || replacement.Kind != PRDOperationV3ForwardRepair || replacement.Epochs[0].CandidateEpoch <= child.Epochs[0].CandidateEpoch {
		t.Fatalf("replacement child link/epoch = %+v; original child=%+v", replacement, child)
	}
}

func TestLanesL112PRDRecoveryRequiresFreshChildAcceptanceAndArchivesAtomically(t *testing.T) {
	registry, snapshot, group := l112FailedMaintenance(t)
	defer registry.Close()
	parent := group.Operations[0]
	snapshot, child, err := registry.BeginPRDRecoveryChild(snapshot.Generation, l112RestoreSpec(parent))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RecordPRDRecoveryOpen(snapshot.Generation, child.ID, l112RecoveryOpen(child, SurfaceMac, 0)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("child opened before all candidate-closed proofs: %v", err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDRecoveryCandidateClosed(snapshot.Generation, child.ID, l112RecoveryClosed(child, surface, index))
		if err != nil {
			t.Fatalf("record child closed proof %s: %v", surface, err)
		}
	}
	snapshot, err = registry.CompletePRDRecoveryCandidateClosed(snapshot.Generation, child.ID, testDigest('a'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationRemoteSurfaces {
		snapshot, err = registry.RecordPRDTransportReady(snapshot.Generation, child.ID, l112TransportReady(child, surface, index))
		if err != nil {
			t.Fatalf("record child transport-ready proof %s: %v", surface, err)
		}
	}
	snapshot, err = registry.PreparePRDReopen(snapshot.Generation, child.ID, testDigest('c'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		authorization := l112Authorization(child, surface, index)
		if index == 0 {
			authorization.Fresh = false
			if _, err := registry.RecordPRDAuthorization(snapshot.Generation, child.ID, authorization); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("stale child authorization accepted: %v", err)
			}
			authorization.Fresh = true
		}
		snapshot, err = registry.RecordPRDAuthorization(snapshot.Generation, child.ID, authorization)
		if err != nil {
			t.Fatalf("record child authorization %s: %v", surface, err)
		}
	}
	snapshot, err = registry.AuthorizePRDReopen(snapshot.Generation, child.ID, testDigest('d'))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RecordPRDRecoveryOpen(snapshot.Generation, child.ID, l112RecoveryOpen(child, SurfaceMac, 0)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Mac opened before remote surfaces: %v", err)
	}
	for index, surface := range operationSurfaces {
		if surface == SurfaceMac {
			continue
		}
		snapshot, err = registry.RecordPRDRecoveryOpen(snapshot.Generation, child.ID, l112RecoveryOpen(child, surface, index))
		if err != nil {
			t.Fatalf("record child open proof %s: %v", surface, err)
		}
	}
	snapshot, err = registry.RecordPRDRecoveryOpen(snapshot.Generation, child.ID, l112RecoveryOpen(child, SurfaceMac, 3))
	if err != nil {
		t.Fatalf("record aggregate Mac open proof: %v", err)
	}
	snapshot, err = registry.CompletePRDRecoveryOpen(snapshot.Generation, child.ID, testDigest('b'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginPRDRecoveryAcceptance(snapshot.Generation, child.ID, testDigest('c'))
	if err != nil {
		t.Fatal(err)
	}
	invalid := l112Acceptance(child)
	invalid.TransitionID = parent.ID
	if _, err := registry.RecordPRDRecoveryAcceptance(snapshot.Generation, child.ID, invalid); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("parent/stale acceptance was accepted for child: %v", err)
	}
	invalid = l112Acceptance(child)
	invalid.ZeroWorkPostflight = false
	if _, err := registry.RecordPRDRecoveryAcceptance(snapshot.Generation, child.ID, invalid); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("child acceptance without zero-work proof was accepted: %v", err)
	}
	snapshot, err = registry.RecordPRDRecoveryAcceptance(snapshot.Generation, child.ID, l112Acceptance(child))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PRDOperationGroups[0].ActiveOperationID != child.ID {
		t.Fatal("accepted child released the group before atomic archive")
	}
	snapshot, err = registry.CompletePRDRecovery(snapshot.Generation, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	group = snapshot.PRDOperationGroups[0]
	if group.ActiveOperationID != "" || group.Operations[0].State != PRDOperationArchived || group.Operations[1].State != PRDOperationArchived {
		t.Fatalf("parent and accepted child were not archived atomically: %+v", group)
	}
	reopened, err := Open(registry.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Snapshot()
	if err != nil || len(persisted.PRDOperationGroups) != 1 || persisted.PRDOperationGroups[0].ActiveOperationID != "" {
		t.Fatalf("PRD group state did not survive reopen: %+v err=%v", persisted, err)
	}
}

func TestLanesL112PRDRecoveryRejectsAlteredPersistedGroup(t *testing.T) {
	registry, snapshot, group := l112StartMaintenance(t)
	defer registry.Close()
	group.Operations[0].Epochs[0].CandidateEpoch = group.Operations[0].Epochs[0].ClosureEpoch
	snapshot.PRDOperationGroups[0] = group
	if err := ValidateSnapshot(snapshot); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("snapshot accepted a non-monotonic PRD epoch: %v", err)
	}
}

func TestLanesL112PRDMaintenanceAcceptsFreshLaneAcceptanceBeforeRelease(t *testing.T) {
	registry, snapshot, group := l112QuiescedMaintenance(t)
	defer registry.Close()
	operation := group.Operations[0]
	var err error
	snapshot, err = registry.StagePRDMaintenance(snapshot.Generation, operation.ID, testDigest('1'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDMaintenanceCandidateClosed(snapshot.Generation, operation.ID, l112RecoveryClosed(operation, surface, index))
		if err != nil {
			t.Fatalf("record maintenance candidate closure %s: %v", surface, err)
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
	for index, surface := range operationRemoteSurfaces {
		snapshot, err = registry.RecordPRDTransportReady(snapshot.Generation, operation.ID, l112TransportReady(operation, surface, index))
		if err != nil {
			t.Fatalf("record maintenance transport-ready %s: %v", surface, err)
		}
	}
	snapshot, err = registry.PreparePRDReopen(snapshot.Generation, operation.ID, testDigest('4'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDAuthorization(snapshot.Generation, operation.ID, l112Authorization(operation, surface, index))
		if err != nil {
			t.Fatalf("record maintenance authorization %s: %v", surface, err)
		}
	}
	snapshot, err = registry.AuthorizePRDReopen(snapshot.Generation, operation.ID, testDigest('5'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		if surface == SurfaceMac {
			continue
		}
		snapshot, err = registry.RecordPRDMaintenanceOpen(snapshot.Generation, operation.ID, l112RecoveryOpen(operation, surface, index))
		if err != nil {
			t.Fatalf("record remote maintenance open %s: %v", surface, err)
		}
	}
	snapshot, err = registry.RecordPRDMaintenanceOpen(snapshot.Generation, operation.ID, l112RecoveryOpen(operation, SurfaceMac, 3))
	if err != nil {
		t.Fatalf("record aggregate Mac open: %v", err)
	}
	snapshot, err = registry.CompletePRDMaintenanceOpen(snapshot.Generation, operation.ID, testDigest('6'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginPRDAcceptance(snapshot.Generation, operation.ID, testDigest('7'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.RecordPRDAcceptance(snapshot.Generation, operation.ID, l112Acceptance(operation))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PRDOperationGroups[0].ActiveOperationID != operation.ID {
		t.Fatal("accepted maintenance released the group before archive")
	}
	snapshot, err = registry.ArchivePRDMaintenance(snapshot.Generation, operation.ID, testDigest('8'))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PRDOperationGroups[0].ActiveOperationID != "" || snapshot.PRDOperationGroups[0].Operations[0].State != PRDOperationArchived {
		t.Fatal("accepted and archived maintenance did not release the PRD group")
	}
}

func l112StartMaintenance(t *testing.T) (*Registry, Snapshot, PRDOperationGroup) {
	t.Helper()
	registry, err := Initialize(filepath.Join(fixtureDirectory(t), "registry"))
	if err != nil {
		t.Fatalf("initialize fixture registry: %v", err)
	}
	snapshot, group, err := registry.BeginPRDMaintenance(0, l112MaintenanceSpec())
	if err != nil {
		registry.Close()
		t.Fatalf("begin fixture PRD maintenance: %v", err)
	}
	return registry, snapshot, group
}

func l112FailedMaintenance(t *testing.T) (*Registry, Snapshot, PRDOperationGroup) {
	t.Helper()
	registry, snapshot, group := l112QuiescedMaintenance(t)
	operation := group.Operations[0]
	snapshot, err := registry.FailPRDMaintenance(snapshot.Generation, operation.ID, testDigest('8'))
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	group = snapshot.PRDOperationGroups[0]
	return registry, snapshot, group
}

func l112QuiescedMaintenance(t *testing.T) (*Registry, Snapshot, PRDOperationGroup) {
	t.Helper()
	registry, snapshot, group := l112StartMaintenance(t)
	operation := group.Operations[0]
	var err error
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDMaintenanceClosure(snapshot.Generation, operation.ID, l112Closure(operation, surface, index))
		if err != nil {
			t.Fatalf("record parent closure %s: %v", surface, err)
		}
	}
	snapshot, err = registry.CompletePRDMaintenanceClosure(snapshot.Generation, operation.ID, testDigest('1'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginPRDMaintenanceQuiescence(snapshot.Generation, operation.ID, testDigest('2'))
	if err != nil {
		t.Fatal(err)
	}
	for index, surface := range operationSurfaces {
		snapshot, err = registry.RecordPRDMaintenanceQuiescence(snapshot.Generation, operation.ID, l112Quiescence(operation, surface, index))
		if err != nil {
			registry.Close()
			t.Fatalf("record parent quiescence %s: %v", surface, err)
		}
	}
	snapshot, err = registry.CompletePRDMaintenanceQuiescence(snapshot.Generation, operation.ID, testDigest('7'))
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	group = snapshot.PRDOperationGroups[0]
	return registry, snapshot, group
}

func l112MaintenanceSpec() PRDMaintenanceSpec {
	epochs := make([]PRDHostEpoch, 0, len(operationSurfaces))
	for _, surface := range operationSurfaces {
		epochs = append(epochs, PRDHostEpoch{Surface: surface, AnchorHighWater: 10, ClosureEpoch: 11, CandidateEpoch: 12})
	}
	return PRDMaintenanceSpec{
		CandidateSetDigest: testDigest('a'), IncumbentCandidateSetDigest: testDigest('b'),
		SourceRevision: strings.Repeat("1", 40), ArtifactDigest: testDigest('c'), ManifestDigest: testDigest('d'), Epochs: epochs,
	}
}

func l112RestoreSpec(parent PRDOperation) PRDRecoverySpec {
	epochs := make([]PRDHostEpoch, 0, len(operationSurfaces))
	for _, prior := range parent.Epochs {
		epochs = append(epochs, PRDHostEpoch{Surface: prior.Surface, AnchorHighWater: prior.CandidateEpoch, ClosureEpoch: prior.CandidateEpoch + 1, CandidateEpoch: prior.CandidateEpoch + 2})
	}
	return PRDRecoverySpec{
		ParentID: parent.ID, Kind: PRDOperationV3Restore, CandidateSetDigest: parent.IncumbentCandidateSetDigest,
		SourceRevision: strings.Repeat("2", 40), ArtifactDigest: testDigest('e'), ManifestDigest: testDigest('f'),
		Epochs: epochs, LineageEvidenceDigest: testDigest('7'),
	}
}

func l112ForwardRepairSpec(parent PRDOperation, firstClosureEpoch uint64) PRDRecoverySpec {
	epochs := make([]PRDHostEpoch, 0, len(operationSurfaces))
	for _, prior := range parent.Epochs {
		closureEpoch := firstClosureEpoch
		if closureEpoch <= prior.CandidateEpoch {
			closureEpoch = prior.CandidateEpoch + 1
		}
		epochs = append(epochs, PRDHostEpoch{Surface: prior.Surface, AnchorHighWater: closureEpoch - 1, ClosureEpoch: closureEpoch, CandidateEpoch: closureEpoch + 1})
	}
	return PRDRecoverySpec{
		ParentID: parent.ID, Kind: PRDOperationV3ForwardRepair, CandidateSetDigest: testDigest('c'),
		SourceRevision: strings.Repeat("3", 40), ArtifactDigest: testDigest('8'), ManifestDigest: testDigest('9'),
		Epochs: epochs, AuthorityApprovalDigest: testDigest('a'), QASAcceptanceDigest: testDigest('b'),
		HostManagementManifestDigest: testDigest('c'),
	}
}

func l112Closure(operation PRDOperation, surface string, index int) PRDClosureProof {
	epoch, _ := prdEpochForSurface(operation.Epochs, surface)
	return PRDClosureProof{
		Surface: surface, DirectiveID: "closure-" + surface, Epoch: epoch.ClosureEpoch,
		AttestationID: "closed-" + surface, Closed: true, EvidenceDigest: testDigest(byte('a' + index)),
	}
}

func l112RecoveryClosed(operation PRDOperation, surface string, index int) PRDRecoveryClosedProof {
	epoch, _ := prdEpochForSurface(operation.Epochs, surface)
	return PRDRecoveryClosedProof{
		Surface: surface, CandidateSetDigest: operation.CandidateSetDigest, CandidateEpoch: epoch.CandidateEpoch,
		AttestationID: "candidate-closed-" + surface, ServiceReconciled: true, AdmissionClosed: true,
		EvidenceDigest: testDigest(byte('a' + index)),
	}
}

func l112Quiescence(operation PRDOperation, surface string, index int) PRDQuiescenceProof {
	classes := []string{"running", "queued", "resumable", "uncertain"}
	dispositions := make([]WorkDisposition, 0, len(classes))
	for classIndex, class := range classes {
		observed, outcome := uint64(0), "none"
		if class == "uncertain" {
			observed, outcome = 1, "deferred"
		}
		dispositions = append(dispositions, WorkDisposition{
			Class: class, Observed: observed, Outcome: outcome, EvidenceDigest: testDigest(byte('1' + classIndex)),
		})
	}
	return PRDQuiescenceProof{
		Surface: surface, CandidateSetDigest: operation.IncumbentCandidateSetDigest, Attempt: 1,
		InventoryDigest: testDigest(byte('a' + index)), Dispositions: dispositions,
		DeferredEvidence: []DeferredWorkEvidence{{
			Class: "uncertain", RequestID: "retained-request-" + surface, RequestDigest: testDigest('b'),
			AckID: "retained-ack-" + surface, AckDigest: testDigest('c'),
		}},
		RemainingWork: 0, ZeroWorkPostflight: true, PostflightDigest: testDigest(byte('5' + index)), EvidenceDigest: testDigest('d'),
	}
}

func l112RecoveryOpen(operation PRDOperation, surface string, index int) PRDRecoveryOpenProof {
	epoch, _ := prdEpochForSurface(operation.Epochs, surface)
	return PRDRecoveryOpenProof{
		Surface: surface, CandidateSetDigest: operation.CandidateSetDigest, CandidateEpoch: epoch.CandidateEpoch,
		DirectiveID: "open-" + surface, AttestationID: "opened-" + surface, AuthorizationID: "authorization-" + surface, AdmissionOpen: true,
		AggregateIngress: surface == SurfaceMac, EvidenceDigest: testDigest(byte('1' + index)),
	}
}

func l112TransportReady(operation PRDOperation, surface string, index int) PRDTransportReadyProof {
	return PRDTransportReadyProof{
		Surface: surface, CandidateSetDigest: operation.CandidateSetDigest, AttestationID: "transport-" + surface,
		RemoteHealthyClosed: true, EvidenceDigest: testDigest(byte('a' + index)),
	}
}

func l112Authorization(operation PRDOperation, surface string, index int) PRDAuthorizationProof {
	epoch, _ := prdEpochForSurface(operation.Epochs, surface)
	return PRDAuthorizationProof{
		Surface: surface, CandidateSetDigest: operation.CandidateSetDigest, CandidateEpoch: epoch.CandidateEpoch,
		AuthorizationID: "authorization-" + surface, Fresh: true, EvidenceDigest: testDigest(byte('a' + index)),
	}
}

func l112Acceptance(operation PRDOperation) LaneAcceptanceEvidence {
	return LaneAcceptanceEvidence{
		AcceptanceID: "acceptance-" + operation.ID, EvidenceDigest: testDigest('f'), TransitionID: operation.ID,
		Lane: config.ReleaseLanePRD, CandidateSetDigest: operation.CandidateSetDigest, SourceRevision: operation.SourceRevision,
		LiveSuitePassed: true, ZeroWorkPostflight: true, AuthorityVerified: true,
	}
}
