package laneregistry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestLanesL105FreshGraphRequiresRemoteFirstOpenAndSealedAcceptance(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationFresh)
	defer registry.Close()

	for _, surface := range operation.RequiredSurfaces {
		var err error
		snapshot, err = registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, surface))
		if err != nil {
			t.Fatalf("record stage for %s: %v", surface, err)
		}
	}
	operation = l105Operation(snapshot, operation.ID)
	for _, surface := range operation.RequiredSurfaces {
		var err error
		snapshot, err = registry.RecordCandidateClosed(snapshot.Generation, operation.ID, l105Closed(operation, surface))
		if err != nil {
			t.Fatalf("record candidate close for %s: %v", surface, err)
		}
	}
	snapshot, err := registry.CompleteCandidateClosed(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Records[0].State != StateActive || l105Operation(snapshot, operation.ID).State != OperationCandidateClosed {
		t.Fatalf("candidate_closed did not atomically activate the candidate: %+v", snapshot)
	}

	operation = l105Operation(snapshot, operation.ID)
	transport := make([]TransportReadyAttestation, 0, len(operationRemoteSurfaces))
	for index, surface := range operationRemoteSurfaces {
		transport = append(transport, TransportReadyAttestation{
			Surface: surface, AttestationID: "transport-" + surface,
			CandidateSetDigest: operation.CandidateSetDigest, RemoteHealthyClosed: true,
			EvidenceDigest: testDigest(byte('a' + index)),
		})
	}
	snapshot, err = registry.PrepareReopen(snapshot.Generation, operation.ID, transport)
	if err != nil {
		t.Fatalf("prepare reopen: %v", err)
	}
	operation = l105Operation(snapshot, operation.ID)
	authorizations := make([]ReopenAuthorization, 0, len(operation.RequiredSurfaces))
	for index, surface := range operation.RequiredSurfaces {
		authorizations = append(authorizations, ReopenAuthorization{
			Surface: surface, AuthorizationID: "authorization-" + surface,
			CandidateSetDigest: operation.CandidateSetDigest, CandidateEpoch: candidateOperationEpoch(operation),
			Fresh: true, EvidenceDigest: testDigest(byte('b' + index)),
		})
	}
	snapshot, err = registry.AuthorizeReopen(snapshot.Generation, operation.ID, authorizations)
	if err != nil {
		t.Fatalf("authorize reopen: %v", err)
	}
	snapshot, err = registry.BeginTargetOpening(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatalf("persist target_opening: %v", err)
	}
	operation = l105Operation(snapshot, operation.ID)
	if _, err := registry.RecordMacOpen(snapshot.Generation, operation.ID, l105Open(operation, SurfaceMac, true)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Mac opened before remote surfaces: %v", err)
	}
	for index, surface := range operationRemoteSurfaces {
		snapshot, err = registry.RecordRemoteOpen(snapshot.Generation, operation.ID, l105Open(operation, surface, false))
		if err != nil {
			t.Fatalf("record remote open for %s: %v", surface, err)
		}
		if index < len(operationRemoteSurfaces)-1 {
			if _, err := registry.RecordMacOpen(snapshot.Generation, operation.ID, l105Open(operation, SurfaceMac, true)); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("Mac opened after only %d remote attestations: %v", index+1, err)
			}
		}
	}
	snapshot, err = registry.RecordMacOpen(snapshot.Generation, operation.ID, l105Open(operation, SurfaceMac, true))
	if err != nil {
		t.Fatalf("record aggregate Mac ingress open: %v", err)
	}
	snapshot, err = registry.BeginAcceptance(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatalf("begin acceptance: %v", err)
	}
	if _, err := registry.ArchiveLaneOperation(snapshot.Generation, operation.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("archived before acceptance: %v", err)
	}
	operation = l105Operation(snapshot, operation.ID)
	acceptance := LaneAcceptanceEvidence{
		AcceptanceID: "acceptance-fresh", EvidenceDigest: testDigest('f'), TransitionID: operation.ID,
		Lane: operation.Lane, CandidateSetDigest: operation.CandidateSetDigest, SourceRevision: operation.SourceRevision,
		LiveSuitePassed: true, ZeroWorkPostflight: true, AuthorityVerified: true,
	}
	snapshot, err = registry.RecordLaneAcceptance(snapshot.Generation, operation.ID, acceptance)
	if err != nil {
		t.Fatalf("record sealed acceptance: %v", err)
	}
	snapshot, err = registry.ArchiveLaneOperation(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatalf("archive accepted operation: %v", err)
	}
	if l105Operation(snapshot, operation.ID).State != OperationArchived || snapshot.Records[0].State != StateActive {
		t.Fatalf("final fresh snapshot = %+v", snapshot)
	}
}

func TestLanesL105ReplacementClosesThenQuiescesBeforeHigherEpochStaging(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationReplacement)
	defer registry.Close()
	incumbentID := operation.Incumbents[0].RecordID
	if operation.Candidates[0].Epoch <= operation.Incumbents[0].Epoch || operation.Candidates[0].Supersedes != incumbentID {
		t.Fatalf("replacement candidate identity = %+v; incumbent = %+v", operation.Candidates[0], operation.Incumbents[0])
	}
	if _, err := registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, SurfaceMac)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("candidate staged before incumbent closure and quiescence: %v", err)
	}
	snapshot, err := registry.BeginIncumbentClosure(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	for index, surface := range operation.RequiredSurfaces {
		attempt := uint64(1)
		closed := true
		if index == 0 {
			closed = false
		}
		var err error
		snapshot, err = registry.RecordIncumbentClosure(snapshot.Generation, operation.ID, l105Closure(surface, attempt, closed))
		if err != nil {
			t.Fatalf("record closure attempt %s/%d: %v", surface, attempt, err)
		}
		if index == 0 {
			snapshot, err = registry.RecordIncumbentClosure(snapshot.Generation, operation.ID, l105Closure(surface, 2, true))
			if err != nil {
				t.Fatalf("record renewed closure %s: %v", surface, err)
			}
		}
	}
	snapshot, err = registry.CompleteIncumbentClosure(snapshot.Generation, operation.ID)
	if err != nil || l105Operation(snapshot, operation.ID).State != OperationIncumbentQuiescing {
		t.Fatalf("closure completion = state %q, err=%v", l105Operation(snapshot, operation.ID).State, err)
	}
	if _, err := registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, SurfaceMac)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("candidate staged before zero-work proof: %v", err)
	}
	for index, surface := range operation.RequiredSurfaces {
		attestation := l105Quiescence(surface, 1, 0)
		if index == 0 {
			attestation = l105Quiescence(surface, 1, 1)
		}
		snapshot, err = registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, attestation)
		if err != nil {
			t.Fatalf("record quiescence %s: %v", surface, err)
		}
		if index == 0 {
			if _, err := registry.CompleteIncumbentQuiescence(snapshot.Generation, operation.ID); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("quiesced despite remaining work: %v", err)
			}
			if _, err := registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, l105Quiescence(surface, 1, 0)); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("accepted reused quiescence attempt number: %v", err)
			}
			snapshot, err = registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, l105Quiescence(surface, 2, 0))
			if err != nil {
				t.Fatalf("record zero-work retry %s: %v", surface, err)
			}
		}
	}
	snapshot, err = registry.CompleteIncumbentQuiescence(snapshot.Generation, operation.ID)
	if err != nil || l105Operation(snapshot, operation.ID).State != OperationIncumbentQuiesced {
		t.Fatalf("quiescence completion = state %q, err=%v", l105Operation(snapshot, operation.ID).State, err)
	}
	operation = l105Operation(snapshot, operation.ID)
	for _, surface := range operation.RequiredSurfaces {
		snapshot, err = registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, surface))
		if err != nil {
			t.Fatalf("stage after complete zero-work proof on %s: %v", surface, err)
		}
	}
	for _, surface := range operation.RequiredSurfaces {
		snapshot, err = registry.RecordCandidateClosed(snapshot.Generation, operation.ID, l105Closed(operation, surface))
		if err != nil {
			t.Fatalf("close staged candidate on %s: %v", surface, err)
		}
	}
	snapshot, err = registry.CompleteCandidateClosed(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]RecordState{}
	for _, record := range snapshot.Records {
		states[record.ID] = record.State
	}
	if states[incumbentID] != StateSuperseded || states[operation.Candidates[0].RecordID] != StateActive {
		t.Fatalf("candidate cutover was not atomic: %v", states)
	}
	stored := l105Operation(snapshot, operation.ID)
	if len(stored.IncumbentQuiescence) != 5 || stored.IncumbentQuiescence[0].DeferredEvidence[0].RequestID != "request-uncertain" {
		t.Fatalf("failed attempt and retained deferred work evidence lost: %+v", stored.IncumbentQuiescence)
	}
}

func TestLanesL105OnlyOneUnarchivedOperationPerLaneAndCompareAndSwap(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationFresh)
	defer registry.Close()
	if _, _, err := registry.BeginLaneOperation(snapshot.Generation-1, l105Spec(OperationFresh, []string{operation.Candidates[0].RecordID}, nil)); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale operation allocation error = %v", err)
	}
	if _, _, err := registry.BeginLaneOperation(snapshot.Generation, l105Spec(OperationFresh, []string{operation.Candidates[0].RecordID}, nil)); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("second lane operation error = %v", err)
	}
	candidate, ok := recordByID(snapshot.Records, operation.Candidates[0].RecordID)
	if !ok {
		t.Fatal("candidate record disappeared after operation allocation")
	}
	retried, err := registry.Prepare(snapshot.Generation, candidate)
	if err != nil || retried.Generation != snapshot.Generation {
		t.Fatalf("identical prepared retry changed or failed during operation: %+v, err=%v", retried, err)
	}
	if _, err := registry.Activate(snapshot.Generation, candidate.ID, reconciliationFor(candidate)); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("direct activation bypassed the active operation: %v", err)
	}
	if _, err := registry.RetirePrepared(snapshot.Generation, candidate.ID); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("candidate retirement bypassed the active operation: %v", err)
	}
	if _, err := registry.Prepare(snapshot.Generation, fixtureRecord("dev-other", config.ReleaseLaneDev, "other-inbox", "/Volumes/other", 702, 1, StatePrepared)); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("new same-lane root allocated while operation active: %v", err)
	}
	if _, err := registry.Prepare(snapshot.Generation, fixtureRecord("qas-independent", config.ReleaseLaneQAS, "qas-inbox", "/Volumes/qas", 703, 1, StatePrepared)); err != nil {
		t.Fatalf("independent QAS source operation blocked by Dev operation: %v", err)
	}
}

func TestLanesL105RejectsUnsafeOrInconsistentTransitionEvidence(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationFresh)
	defer registry.Close()
	var err error
	wrongSurface := l105Stage(operation, "unknown-host")
	if _, err := registry.RecordCandidateStage(snapshot.Generation, operation.ID, wrongSurface); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted a foreign stage surface: %v", err)
	}
	badStage := l105Stage(operation, SurfaceMac)
	badStage.ArtifactDigest = testDigest('f')
	if _, err := registry.RecordCandidateStage(snapshot.Generation, operation.ID, badStage); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted a mismatched staged artifact: %v", err)
	}
	snapshot, err = registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, SurfaceMac))
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	partial := l105Operation(snapshot, operation.ID)
	partial.CandidateSetDigest = testDigest('f')
	if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Generation: snapshot.Generation, Records: snapshot.Records, Operations: []LaneOperation{partial}}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("snapshot accepted a candidate-set digest mismatch: %v", err)
	}
	partial = l105Operation(snapshot, operation.ID)
	partial.History[0].Sequence = 2
	if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Generation: snapshot.Generation, Records: snapshot.Records, Operations: []LaneOperation{partial}}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("snapshot accepted non-monotonic history: %v", err)
	}
	for _, surface := range operation.RequiredSurfaces {
		if surface == SurfaceMac {
			continue
		}
		snapshot, err = registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, surface))
		if err != nil {
			t.Fatal(err)
		}
	}
	operation = l105Operation(snapshot, operation.ID)
	for _, surface := range operation.RequiredSurfaces {
		snapshot, err = registry.RecordCandidateClosed(snapshot.Generation, operation.ID, l105Closed(operation, surface))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompleteCandidateClosed(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	if _, err := registry.PrepareReopen(snapshot.Generation, operation.ID, []TransportReadyAttestation{{
		Surface: SurfaceLinuxHost, AttestationID: "transport-only-linux", CandidateSetDigest: operation.CandidateSetDigest,
		RemoteHealthyClosed: true, EvidenceDigest: testDigest('c'),
	}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted incomplete transport set: %v", err)
	}
}

func TestLanesL105ClosureNeedsEverySurfaceAndStrictlyHigherDirectiveEpoch(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationReplacement)
	defer registry.Close()
	snapshot, err := registry.BeginIncumbentClosure(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalid := l105Closure(SurfaceMac, 1, true)
	invalid.DirectiveEpoch = operation.Incumbents[0].Epoch
	if _, err := registry.RecordIncumbentClosure(snapshot.Generation, operation.ID, invalid); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted incumbent closure at a non-increasing epoch: %v", err)
	}
	for _, surface := range operation.RequiredSurfaces[:len(operation.RequiredSurfaces)-1] {
		snapshot, err = registry.RecordIncumbentClosure(snapshot.Generation, operation.ID, l105Closure(surface, 1, true))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.CompleteIncumbentClosure(snapshot.Generation, operation.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("advanced closure without every required surface: %v", err)
	}
}

func TestLanesL105QuiescenceRequiresAllFourClassesAndPreservesEvidence(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationReplacement)
	defer registry.Close()
	snapshot, err := registry.BeginIncumbentClosure(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	for _, surface := range operation.RequiredSurfaces {
		snapshot, err = registry.RecordIncumbentClosure(snapshot.Generation, operation.ID, l105Closure(surface, 1, true))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompleteIncumbentClosure(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalid := l105Quiescence(SurfaceMac, 1, 0)
	invalid.Dispositions = invalid.Dispositions[:3]
	if _, err := registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, invalid); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted incomplete work-class inventory: %v", err)
	}
	deferred := l105Quiescence(SurfaceMac, 1, 1)
	deferred.DeferredEvidence = nil
	if _, err := registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, deferred); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted deferred work without retained request/ACK refs: %v", err)
	}
	snapshot, err = registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, l105Quiescence(SurfaceMac, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(registry.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	stored := l105Operation(restarted, operation.ID)
	if len(stored.IncumbentQuiescence) != 1 || stored.IncumbentQuiescence[0].DeferredEvidence[0].AckID != "ack-uncertain" {
		t.Fatalf("deferred mailbox evidence did not survive registry restart: %+v", stored.IncumbentQuiescence)
	}
}

func TestLanesL105KeepsLegacySnapshotsWithoutOperationsValid(t *testing.T) {
	snapshot := Snapshot{Version: FormatVersion, Generation: 0, Records: []RootRecord{}}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"operations"`) {
		t.Fatalf("empty operation field changed the existing snapshot shape: %s", data)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(decoded); err != nil {
		t.Fatalf("legacy-shaped snapshot rejected: %v", err)
	}
}

func l105StartOperation(t *testing.T, mode LaneOperationMode) (*Registry, Snapshot, LaneOperation) {
	t.Helper()
	registry, err := Initialize(fixtureRegistryPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	var candidate RootRecord
	var incumbentIDs []string
	if mode == OperationFresh {
		candidate = fixtureRecord("dev-candidate", config.ReleaseLaneDev, "dev-inbox", "/Volumes/dev-fresh", 701, 1, StatePrepared)
		snapshot, err = registry.Prepare(0, candidate)
	} else {
		incumbent := fixtureRecord("dev-incumbent", config.ReleaseLaneDev, "dev-inbox", "/Volumes/dev-replace", 704, 1, StatePrepared)
		snapshot, err = registry.Prepare(0, incumbent)
		if err == nil {
			snapshot, err = registry.Activate(snapshot.Generation, incumbent.ID, reconciliationFor(incumbent))
		}
		candidate = incumbent
		candidate.ID, candidate.Epoch, candidate.State, candidate.Supersedes = "dev-candidate", 2, StatePrepared, incumbent.ID
		candidate.ConfigurationDigest = testDigest('2')
		if err == nil {
			snapshot, err = registry.Prepare(snapshot.Generation, candidate)
		}
		incumbentIDs = []string{incumbent.ID}
	}
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	snapshot, operation, err := registry.BeginLaneOperation(snapshot.Generation, l105Spec(mode, []string{candidate.ID}, incumbentIDs))
	if err != nil {
		registry.Close()
		t.Fatal(err)
	}
	return registry, snapshot, operation
}

func l105Spec(mode LaneOperationMode, candidates, incumbents []string) LaneOperationSpec {
	return LaneOperationSpec{
		Lane: config.ReleaseLaneDev, Mode: mode, CandidateRecordIDs: candidates, IncumbentRecordIDs: incumbents,
		SourceRevision: strings.Repeat("a", 40), ArtifactDigest: testDigest('a'), ManifestDigest: testDigest('b'),
	}
}

func l105Operation(snapshot Snapshot, id string) LaneOperation {
	operation, _ := laneOperationByID(snapshot.Operations, id)
	return operation
}

func l105Stage(operation LaneOperation, surface string) CandidateStageAttestation {
	return CandidateStageAttestation{
		Surface: surface, AttestationID: "stage-" + surface, CandidateSetDigest: operation.CandidateSetDigest,
		SourceRevision: operation.SourceRevision, ArtifactDigest: operation.ArtifactDigest,
		ManifestDigest: operation.ManifestDigest, InstalledClosed: true, EvidenceDigest: testDigest('c'),
	}
}

func l105Closed(operation LaneOperation, surface string) CandidateClosedAttestation {
	return CandidateClosedAttestation{
		Surface: surface, AttestationID: "closed-" + surface, CandidateSetDigest: operation.CandidateSetDigest,
		SourceRevision: operation.SourceRevision, ArtifactDigest: operation.ArtifactDigest,
		ConfigurationDigest: aggregateConfigurationDigest(operation.Candidates),
		ServiceReconciled:   true, AdmissionClosed: true, EvidenceDigest: testDigest('d'),
	}
}

func l105Closure(surface string, attempt uint64, closed bool) IncumbentClosureAttestation {
	return IncumbentClosureAttestation{
		Surface: surface, Attempt: attempt, DirectiveID: "closure-directive-" + surface + "-" + string(rune('0'+attempt)),
		DirectiveEpoch: 2, AttestationID: "closure-attestation-" + surface + "-" + string(rune('0'+attempt)),
		Closed: closed, EvidenceDigest: testDigest('e'),
	}
}

func l105Quiescence(surface string, attempt, remaining uint64) IncumbentQuiescenceAttestation {
	classes := []string{"running", "queued", "resumable", "uncertain"}
	dispositions := make([]WorkDisposition, 0, len(classes))
	for _, class := range classes {
		observed, outcome := uint64(0), "none"
		if class == "uncertain" && remaining > 0 {
			observed, outcome = remaining, "deferred"
		}
		dispositions = append(dispositions, WorkDisposition{Class: class, Observed: observed, Outcome: outcome, EvidenceDigest: testDigest('a')})
	}
	attestation := IncumbentQuiescenceAttestation{
		Surface: surface, Attempt: attempt, InventoryDigest: testDigest('b'), Dispositions: dispositions,
		RemainingWork: remaining, ZeroWorkPostflight: remaining == 0, PostflightDigest: testDigest('c'),
	}
	if remaining > 0 {
		attestation.DeferredEvidence = []DeferredWorkEvidence{{
			Class: "uncertain", RequestID: "request-uncertain", RequestDigest: testDigest('d'),
			AckID: "ack-uncertain", AckDigest: testDigest('e'),
		}}
	}
	return attestation
}

func l105Open(operation LaneOperation, surface string, mac bool) OpenAttestation {
	return OpenAttestation{
		Surface: surface, DirectiveID: "open-directive-" + surface, AttestationID: "open-attestation-" + surface,
		CandidateSetDigest: operation.CandidateSetDigest, AdmissionOpen: true, AggregateIngress: mac,
		EvidenceDigest: testDigest('f'),
	}
}
