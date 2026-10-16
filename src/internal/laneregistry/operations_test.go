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

func TestLanesL106FreshFailureNeedsClosedProofBeforeAbortAndArchive(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationFresh)
	defer registry.Close()

	failure := l106Failure(operation, OperationPrepare)
	failure.TerminalProofs = l106TerminalProofs(operation.CandidateSetDigest, operation.RequiredSurfaces)
	failure.TerminalProofs = failure.TerminalProofs[:len(failure.TerminalProofs)-1]
	if _, err := registry.RecordOperationFailure(snapshot.Generation, operation.ID, failure); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("failure without every closed/zero-work proof returned %v", err)
	}
	failure.TerminalProofs = l106TerminalProofs(operation.CandidateSetDigest, operation.RequiredSurfaces)
	snapshot, err := registry.RecordOperationFailure(snapshot.Generation, operation.ID, failure)
	if err != nil {
		t.Fatalf("record closed fresh failure: %v", err)
	}
	if l105Operation(snapshot, operation.ID).State != OperationFailedClosed {
		t.Fatalf("failed fresh operation state = %q", l105Operation(snapshot, operation.ID).State)
	}
	snapshot, err = registry.AbortFailedClosed(snapshot.Generation, operation.ID, testDigest('a'))
	if err != nil {
		t.Fatalf("abort failed-closed fresh operation: %v", err)
	}
	if snapshot.Records[0].State != StateRetired {
		t.Fatalf("aborted prepared candidate state = %q, want retired", snapshot.Records[0].State)
	}
	snapshot, err = registry.ArchiveLaneOperation(snapshot.Generation, operation.ID)
	if err != nil || l105Operation(snapshot, operation.ID).State != OperationArchived {
		t.Fatalf("archive closed abort: state=%q err=%v", l105Operation(snapshot, operation.ID).State, err)
	}
}

func TestLanesL106ReplacementMayAbortOnlyBeforeIncumbentClosureWithClosedProof(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationReplacement)
	defer registry.Close()
	proofs := l106TerminalProofs(operation.CandidateSetDigest, operation.RequiredSurfaces)
	_, err := registry.AbortPreparedReplacement(snapshot.Generation, operation.ID, proofs[:len(proofs)-1])
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("replacement abort without all closed proofs returned %v", err)
	}
	snapshot, err = registry.AbortPreparedReplacement(snapshot.Generation, operation.ID, proofs)
	if err != nil {
		t.Fatalf("abort prepared replacement: %v", err)
	}
	incumbent, _ := recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	candidate, _ := recordByID(snapshot.Records, operation.Candidates[0].RecordID)
	if incumbent.State != StateActive || candidate.State != StateRetired {
		t.Fatalf("prepared replacement abort changed incumbent or retained target: %+v", snapshot.Records)
	}
	snapshot, err = registry.ArchiveLaneOperation(snapshot.Generation, operation.ID)
	if err != nil || l105Operation(snapshot, operation.ID).State != OperationArchived {
		t.Fatalf("archive prepared replacement abort: state=%q err=%v", l105Operation(snapshot, operation.ID).State, err)
	}
}

func TestLanesL106PostOpenFailureQuiescesBeforeExactIncumbentRecovery(t *testing.T) {
	registry, snapshot, operation := l106FailedOpenReplacement(t)
	defer registry.Close()

	snapshot, err := l106CloseAndQuiesce(t, registry, snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	if operation.State != OperationFailedClosed || operation.Failure == nil ||
		operation.Failure.KnownDirectives[0].HighestSequence != 2 ||
		operation.TargetQuiescence[0].Quiescence.DeferredEvidence[0].AckID != "ack-uncertain" {
		t.Fatalf("failed target evidence was not retained: %+v", operation)
	}

	incumbent, ok := recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	if !ok {
		t.Fatal("recorded incumbent disappeared")
	}
	recovery := incumbent
	recovery.ID = "dev-exact-recovery"
	recovery.Epoch = operation.Candidates[0].Epoch + 1
	recovery.State = StatePrepared
	recovery.Supersedes = operation.Candidates[0].RecordID
	wrongRecovery := recovery
	wrongRecovery.ID = "dev-wrong-recovery"
	wrongRecovery.ConfigurationDigest = testDigest('9')
	if _, _, err := registry.BeginIncumbentRecovery(snapshot.Generation, operation.ID, []RootRecord{wrongRecovery}, strings.Repeat("c", 40), testDigest('c'), testDigest('d'), testDigest('e')); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("accepted recovery with a changed incumbent configuration: %v", err)
	}
	snapshot, operation, err = registry.BeginIncumbentRecovery(snapshot.Generation, operation.ID, []RootRecord{recovery}, strings.Repeat("c", 40), testDigest('c'), testDigest('d'), testDigest('e'))
	if err != nil {
		t.Fatalf("allocate exact incumbent recovery: %v", err)
	}
	if operation.Recovery == nil || operation.Recovery.Candidates[0].ConfigurationDigest != operation.Incumbents[0].ConfigurationDigest ||
		operation.Recovery.Candidates[0].RecordID == operation.Incumbents[0].RecordID ||
		operation.Recovery.Candidates[0].Supersedes != operation.Candidates[0].RecordID ||
		operation.Recovery.Candidates[0].Epoch <= operation.Candidates[0].Epoch {
		t.Fatalf("recovery did not allocate a new exact-incumbent successor: %+v", operation.Recovery)
	}
	snapshot, operation, err = l106CloseRecoveryCandidate(t, registry, snapshot, operation)
	if err != nil {
		t.Fatalf("close recovery candidate: %v", err)
	}
	incumbent, _ = recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	failedTarget, _ := recordByID(snapshot.Records, operation.Candidates[0].RecordID)
	activeRecovery, _ := recordByID(snapshot.Records, operation.Recovery.Candidates[0].RecordID)
	if incumbent.State != StateSuperseded || failedTarget.State != StateActive || activeRecovery.State != StatePrepared {
		t.Fatalf("closed recovery changed root authority before acceptance: %+v", snapshot.Records)
	}

	transport := l106Transport(operationTargetContext(operation), "recovery-")
	snapshot, err = registry.PrepareReopen(snapshot.Generation, operation.ID, transport)
	if err != nil {
		t.Fatalf("prepare recovery reopen: %v", err)
	}
	operation = l105Operation(snapshot, operation.ID)
	target := operationTargetContext(operation)
	snapshot, err = registry.AuthorizeReopen(snapshot.Generation, operation.ID, l106Authorizations(target, "recovery-"))
	if err != nil {
		t.Fatalf("authorize recovery reopen: %v", err)
	}
	snapshot, err = registry.BeginTargetOpening(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatalf("persist recovery target opening: %v", err)
	}
	operation = l105Operation(snapshot, operation.ID)
	target = operationTargetContext(operation)
	for _, surface := range operationRemoteSurfaces {
		open := l106Open(target, surface, false, "recovery-")
		snapshot, err = registry.RecordRemoteOpen(snapshot.Generation, operation.ID, open)
		if err != nil {
			t.Fatalf("open recovery remote %s: %v", surface, err)
		}
	}
	snapshot, err = registry.RecordMacOpen(snapshot.Generation, operation.ID, l106Open(target, SurfaceMac, true, "recovery-"))
	if err != nil {
		t.Fatalf("open recovery Mac ingress: %v", err)
	}
	snapshot, err = registry.BeginAcceptance(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatalf("begin recovery acceptance: %v", err)
	}
	preparedRecovery, _ := recordByID(snapshot.Records, operation.Recovery.Candidates[0].RecordID)
	reservedTarget, _ := recordByID(snapshot.Records, operation.Candidates[0].RecordID)
	if preparedRecovery.State != StatePrepared || reservedTarget.State != StateActive {
		t.Fatalf("recovery roots changed before lane acceptance: %+v", snapshot.Records)
	}
	operation = l105Operation(snapshot, operation.ID)
	target = operationTargetContext(operation)
	acceptance := LaneAcceptanceEvidence{
		AcceptanceID: "recovery-acceptance", EvidenceDigest: testDigest('f'), TransitionID: operation.ID,
		Lane: operation.Lane, CandidateSetDigest: target.CandidateSetDigest, SourceRevision: target.SourceRevision,
		LiveSuitePassed: true, ZeroWorkPostflight: true, AuthorityVerified: true,
	}
	snapshot, err = registry.RecordLaneAcceptance(snapshot.Generation, operation.ID, acceptance)
	if err != nil {
		t.Fatalf("record recovery acceptance: %v", err)
	}
	snapshot, err = registry.ArchiveLaneOperation(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatalf("archive accepted recovery: %v", err)
	}
	incumbent, _ = recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	failedTarget, _ = recordByID(snapshot.Records, operation.Candidates[0].RecordID)
	activeRecovery, _ = recordByID(snapshot.Records, operation.Recovery.Candidates[0].RecordID)
	if incumbent.State != StateSuperseded || failedTarget.State != StateSuperseded || activeRecovery.State != StateActive {
		t.Fatalf("final recovery root states = %+v", snapshot.Records)
	}
	stored := l105Operation(snapshot, operation.ID)
	if stored.Failure == nil || stored.Failure.FailureDigest != testDigest('1') ||
		len(stored.OpenAttestations) != 1 || len(stored.Recovery.OpenAttestations) != len(operation.RequiredSurfaces) ||
		stored.TargetQuiescence[0].Quiescence.DeferredEvidence[0].RequestID != "request-uncertain" ||
		stored.TargetQuiescence[0].Quiescence.DeferredEvidence[0].AckID != "ack-uncertain" {
		t.Fatalf("recovery did not preserve original failure/directive/retained-work evidence: %+v", stored)
	}
}

func TestLanesL106PreOpenRecoveryFailureSealsRecoveryAbort(t *testing.T) {
	registry, snapshot, operation := l106FailedOpenReplacement(t)
	defer registry.Close()
	snapshot, err := l106CloseAndQuiesce(t, registry, snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	incumbent, _ := recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	recovery := incumbent
	recovery.ID = "dev-aborted-recovery"
	recovery.Epoch = operation.Candidates[0].Epoch + 1
	recovery.State = StatePrepared
	recovery.Supersedes = operation.Candidates[0].RecordID
	snapshot, operation, err = registry.BeginIncumbentRecovery(snapshot.Generation, operation.ID, []RootRecord{recovery}, strings.Repeat("c", 40), testDigest('c'), testDigest('d'), testDigest('e'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, operation, err = l106CloseRecoveryCandidate(t, registry, snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	transport := l106Transport(operationTargetContext(operation), "recovery-abort-")
	snapshot, err = registry.PrepareReopen(snapshot.Generation, operation.ID, transport)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	target := operationTargetContext(operation)
	failure := l106Failure(target, OperationReopenPrepared)
	failure.FailureID = "recovery-failure"
	failure.TerminalProofs = l106TerminalProofs(target.CandidateSetDigest, operation.RequiredSurfaces)
	snapshot, err = registry.RecordOperationFailure(snapshot.Generation, operation.ID, failure)
	if err != nil {
		t.Fatalf("record pre-open recovery failure: %v", err)
	}
	recoveryRecord, _ := recordByID(snapshot.Records, operation.Recovery.Candidates[0].RecordID)
	if l105Operation(snapshot, operation.ID).State != OperationRecoveryAborted || recoveryRecord.State != StateRetired {
		t.Fatalf("recovery did not close and retire failed candidate: operation=%+v records=%+v", l105Operation(snapshot, operation.ID), snapshot.Records)
	}
	if _, err := registry.AbortFailedClosed(snapshot.Generation, operation.ID, testDigest('a')); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second disposition was accepted after recovery abort: %v", err)
	}
	snapshot, err = registry.ArchiveLaneOperation(snapshot.Generation, operation.ID)
	if err != nil || l105Operation(snapshot, operation.ID).State != OperationArchived {
		t.Fatalf("archive recovery abort: state=%q err=%v", l105Operation(snapshot, operation.ID).State, err)
	}
}

func TestLanesL106RecoveryOpenFailureClosesAndQuiescesBeforeArchive(t *testing.T) {
	registry, snapshot, operation := l106FailedOpenReplacement(t)
	defer registry.Close()
	snapshot, err := l106CloseAndQuiesce(t, registry, snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	incumbent, _ := recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	recovery := incumbent
	recovery.ID = "dev-open-failed-recovery"
	recovery.Epoch = operation.Candidates[0].Epoch + 1
	recovery.State = StatePrepared
	recovery.Supersedes = operation.Candidates[0].RecordID
	snapshot, operation, err = registry.BeginIncumbentRecovery(snapshot.Generation, operation.ID, []RootRecord{recovery}, strings.Repeat("c", 40), testDigest('c'), testDigest('d'), testDigest('e'))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, operation, err = l106CloseRecoveryCandidate(t, registry, snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	target := operationTargetContext(operation)
	snapshot, err = registry.PrepareReopen(snapshot.Generation, operation.ID, l106Transport(target, "recovery-fail-"))
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	target = operationTargetContext(operation)
	snapshot, err = registry.AuthorizeReopen(snapshot.Generation, operation.ID, l106Authorizations(target, "recovery-fail-"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginTargetOpening(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	target = operationTargetContext(operation)
	opened := l106Open(target, operationRemoteSurfaces[0], false, "recovery-fail-")
	snapshot, err = registry.RecordRemoteOpen(snapshot.Generation, operation.ID, opened)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	target = operationTargetContext(operation)
	failure := l106Failure(target, OperationTargetOpening)
	failure.FailureID = "recovery-open-failure"
	failure.KnownDirectives = []DirectiveHighWater{{
		Surface: opened.Surface, DirectiveID: opened.DirectiveID, DirectiveEpoch: opened.DirectiveEpoch,
		HighestSequence: opened.HighWaterSequence + 1,
	}}
	snapshot, err = registry.RecordOperationFailure(snapshot.Generation, operation.ID, failure)
	if err != nil {
		t.Fatalf("record recovery partial-open failure: %v", err)
	}
	snapshot, err = l106CloseAndQuiesce(t, registry, snapshot, l105Operation(snapshot, operation.ID))
	if err != nil {
		t.Fatalf("close/quiesce recovery target: %v", err)
	}
	operation = l105Operation(snapshot, operation.ID)
	incumbent, _ = recordByID(snapshot.Records, operation.Incumbents[0].RecordID)
	failedTarget, _ := recordByID(snapshot.Records, operation.Candidates[0].RecordID)
	failedRecovery, _ := recordByID(snapshot.Records, operation.Recovery.Candidates[0].RecordID)
	if operation.State != OperationRecoveryAborted || incumbent.State != StateSuperseded ||
		failedTarget.State != StateActive || failedRecovery.State != StateRetired {
		t.Fatalf("recovery open failure lost closed state or retained history: op=%+v records=%+v", operation, snapshot.Records)
	}
	snapshot, err = registry.ArchiveLaneOperation(snapshot.Generation, operation.ID)
	if err != nil || l105Operation(snapshot, operation.ID).State != OperationArchived {
		t.Fatalf("archive closed recovery failure: state=%q err=%v", l105Operation(snapshot, operation.ID).State, err)
	}
}

func TestLanesL106RejectsUnsafeFailureAndClosureEvidence(t *testing.T) {
	registry, snapshot, operation := l106FailedOpenReplacement(t)
	defer registry.Close()

	badFailure := l106Failure(operationTargetContext(operation), OperationTargetOpening)
	badFailure.KnownDirectives = nil
	if _, err := registry.RecordOperationFailure(snapshot.Generation, operation.ID, badFailure); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted failure omitting an opened directive high-water tuple: %v", err)
	}
	staleClosure := TargetClosureAttestation{
		Surface: SurfaceMac, Attempt: 1, DirectiveID: "stale-target-close", DirectiveEpoch: candidateOperationEpoch(operation),
		HighestSequence: 1, AttestationID: "stale-target-attestation", CandidateSetDigest: operation.CandidateSetDigest,
		Closed: true, EvidenceDigest: testDigest('a'),
	}
	if _, err := registry.RecordTargetClosure(snapshot.Generation, operation.ID, staleClosure); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted a closure that did not advance the known directive epoch: %v", err)
	}

	corrupt := l105Operation(snapshot, operation.ID)
	corrupt.Failure.KnownDirectives[0].HighestSequence = 0
	if err := ValidateSnapshot(Snapshot{Version: FormatVersion, Generation: snapshot.Generation, Records: snapshot.Records, Operations: []LaneOperation{corrupt}}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("snapshot accepted an invalid known directive high-water tuple: %v", err)
	}
}

func l106FailedOpenReplacement(t *testing.T) (*Registry, Snapshot, LaneOperation) {
	t.Helper()
	registry, snapshot, operation := l105StartOperation(t, OperationReplacement)
	var err error
	snapshot, err = registry.BeginIncumbentClosure(snapshot.Generation, operation.ID)
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
	for _, surface := range operation.RequiredSurfaces {
		snapshot, err = registry.RecordIncumbentQuiescence(snapshot.Generation, operation.ID, l105Quiescence(surface, 1, 0))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = registry.CompleteIncumbentQuiescence(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	for _, surface := range operation.RequiredSurfaces {
		snapshot, err = registry.RecordCandidateStage(snapshot.Generation, operation.ID, l105Stage(operation, surface))
		if err != nil {
			t.Fatal(err)
		}
	}
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
	snapshot, err = registry.PrepareReopen(snapshot.Generation, operation.ID, l106Transport(operation, "main-"))
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	snapshot, err = registry.AuthorizeReopen(snapshot.Generation, operation.ID, l106Authorizations(operation, "main-"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = registry.BeginTargetOpening(snapshot.Generation, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	opened := l105Open(operation, operationRemoteSurfaces[0], false)
	snapshot, err = registry.RecordRemoteOpen(snapshot.Generation, operation.ID, opened)
	if err != nil {
		t.Fatal(err)
	}
	operation = l105Operation(snapshot, operation.ID)
	failure := l106Failure(operation, OperationTargetOpening)
	failure.KnownDirectives = []DirectiveHighWater{{
		Surface: opened.Surface, DirectiveID: opened.DirectiveID, DirectiveEpoch: opened.DirectiveEpoch,
		HighestSequence: opened.HighWaterSequence + 1,
	}}
	snapshot, err = registry.RecordOperationFailure(snapshot.Generation, operation.ID, failure)
	if err != nil {
		registry.Close()
		t.Fatalf("record partial-open failure: %v", err)
	}
	return registry, snapshot, l105Operation(snapshot, operation.ID)
}

func l106CloseAndQuiesce(t *testing.T, registry *Registry, snapshot Snapshot, operation LaneOperation) (Snapshot, error) {
	t.Helper()
	var err error
	target := operationTargetContext(operation)
	for index, surface := range operation.RequiredSurfaces {
		attestation := l106TargetClosure(target, surface, 1, candidateOperationEpoch(target)+1, true)
		if index == 0 {
			stale := attestation
			stale.DirectiveEpoch = candidateOperationEpoch(target)
			if _, err := registry.RecordTargetClosure(snapshot.Generation, operation.ID, stale); !errors.Is(err, ErrInvalidTransition) {
				return snapshot, errors.New("target closure accepted a stale directive epoch")
			}
		}
		snapshot, err = registry.RecordTargetClosure(snapshot.Generation, operation.ID, attestation)
		if err != nil {
			return snapshot, err
		}
	}
	snapshot, err = registry.CompleteTargetClosure(snapshot.Generation, operation.ID)
	if err != nil {
		return snapshot, err
	}
	for index, surface := range operation.RequiredSurfaces {
		attempt, remaining := uint64(1), uint64(0)
		if index == 0 {
			remaining = 1
		}
		attestation := TargetQuiescenceAttestation{CandidateSetDigest: target.CandidateSetDigest, Quiescence: l105Quiescence(surface, attempt, remaining)}
		snapshot, err = registry.RecordTargetQuiescence(snapshot.Generation, operation.ID, attestation)
		if err != nil {
			return snapshot, err
		}
	}
	if _, err := registry.CompleteTargetQuiescence(snapshot.Generation, operation.ID); !errors.Is(err, ErrInvalidTransition) {
		return snapshot, errors.New("target quiescence completed with remaining work")
	}
	first := operation.RequiredSurfaces[0]
	snapshot, err = registry.RecordTargetQuiescence(snapshot.Generation, operation.ID, TargetQuiescenceAttestation{
		CandidateSetDigest: target.CandidateSetDigest, Quiescence: l105Quiescence(first, 2, 0),
	})
	if err != nil {
		return snapshot, err
	}
	return registry.CompleteTargetQuiescence(snapshot.Generation, operation.ID)
}

func l106CloseRecoveryCandidate(t *testing.T, registry *Registry, snapshot Snapshot, operation LaneOperation) (Snapshot, LaneOperation, error) {
	t.Helper()
	var err error
	target := recoveryOperationContext(operation)
	for _, surface := range operation.RequiredSurfaces {
		stage := l105Stage(target, surface)
		stage.AttestationID = "recovery-stage-" + surface
		snapshot, err = registry.RecordRecoveryCandidateStage(snapshot.Generation, operation.ID, stage)
		if err != nil {
			return snapshot, operation, err
		}
	}
	for _, surface := range operation.RequiredSurfaces {
		closed := l105Closed(target, surface)
		closed.AttestationID = "recovery-closed-" + surface
		snapshot, err = registry.RecordRecoveryCandidateClosed(snapshot.Generation, operation.ID, closed)
		if err != nil {
			return snapshot, operation, err
		}
	}
	snapshot, err = registry.CompleteRecoveryCandidateClosed(snapshot.Generation, operation.ID)
	if err != nil {
		return snapshot, operation, err
	}
	return snapshot, l105Operation(snapshot, operation.ID), nil
}

func l106Failure(operation LaneOperation, failedFrom LaneOperationState) OperationFailureEvidence {
	return OperationFailureEvidence{
		FailureID: "failure-" + string(failedFrom), FailedFrom: failedFrom, CandidateSetDigest: operation.CandidateSetDigest,
		FailureDigest: testDigest('1'), EvidenceDigest: testDigest('2'),
	}
}

func l106TerminalProofs(candidateDigest string, surfaces []string) []TerminalSurfaceProof {
	proofs := make([]TerminalSurfaceProof, 0, len(surfaces))
	for _, surface := range surfaces {
		proofs = append(proofs, TerminalSurfaceProof{
			Surface: surface, CandidateSetDigest: candidateDigest, Closed: true,
			Quiescence: l105Quiescence(surface, 1, 0), EvidenceDigest: testDigest('3'),
		})
	}
	return proofs
}

func l106TargetClosure(operation LaneOperation, surface string, attempt, epoch uint64, closed bool) TargetClosureAttestation {
	prefix := strings.TrimPrefix(operation.CandidateSetDigest, "sha256:")[:8]
	return TargetClosureAttestation{
		Surface: surface, Attempt: attempt, DirectiveID: "target-close-" + prefix + "-" + surface + "-" + string(rune('0'+attempt)),
		DirectiveEpoch: epoch, HighestSequence: 1, AttestationID: "target-closed-" + prefix + "-" + surface + "-" + string(rune('0'+attempt)),
		CandidateSetDigest: operation.CandidateSetDigest, Closed: closed, EvidenceDigest: testDigest('4'),
	}
}

func l106Transport(operation LaneOperation, prefix string) []TransportReadyAttestation {
	transport := make([]TransportReadyAttestation, 0, len(operationRemoteSurfaces))
	for index, surface := range operationRemoteSurfaces {
		transport = append(transport, TransportReadyAttestation{
			Surface: surface, AttestationID: prefix + "transport-" + surface,
			CandidateSetDigest: operation.CandidateSetDigest, RemoteHealthyClosed: true,
			EvidenceDigest: testDigest(byte('a' + index)),
		})
	}
	return transport
}

func l106Authorizations(operation LaneOperation, prefix string) []ReopenAuthorization {
	authorizations := make([]ReopenAuthorization, 0, len(operation.RequiredSurfaces))
	for index, surface := range operation.RequiredSurfaces {
		authorizations = append(authorizations, ReopenAuthorization{
			Surface: surface, AuthorizationID: prefix + "authorization-" + surface,
			CandidateSetDigest: operation.CandidateSetDigest, CandidateEpoch: candidateOperationEpoch(operation),
			Fresh: true, EvidenceDigest: testDigest(byte('b' + index)),
		})
	}
	return authorizations
}

func l106Open(operation LaneOperation, surface string, mac bool, prefix string) OpenAttestation {
	open := l105Open(operation, surface, mac)
	open.DirectiveID = prefix + open.DirectiveID
	open.AttestationID = prefix + open.AttestationID
	return open
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
		DirectiveEpoch: candidateOperationEpoch(operation), HighWaterSequence: 1,
		CandidateSetDigest: operation.CandidateSetDigest, AdmissionOpen: true, AggregateIngress: mac,
		EvidenceDigest: testDigest('f'),
	}
}
