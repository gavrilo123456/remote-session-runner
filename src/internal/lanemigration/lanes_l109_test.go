package lanemigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneregistry"
	"remote-session-runner/src/internal/laneroot"
	"remote-session-runner/src/internal/mailboxfence"
)

const l109TransitionID = "0196c2a7-7a11-7000-8000-000000000001"

func TestLanesL109PersistsFencingBeforeAnyV2ActionAndAcrossRestart(t *testing.T) {
	fixture := newL109Fixture(t)
	snapshot, err := fixture.controller.Begin(fixture.generation, fixture.record)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PRDMigrationFences) != 1 || snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationFencing {
		t.Fatalf("durable initial state=%+v", snapshot.PRDMigrationFences)
	}
	for _, member := range snapshot.PRDMigrationFences[0].V2Members {
		if !member.InitiallyEnabled || member.PauseEvidenceDigest != "" || member.StopEvidenceDigest != "" || member.DisableEvidenceDigest != "" {
			t.Fatalf("pre-action V2 inventory was not persisted: %+v", member)
		}
	}
	for _, publisher := range snapshot.PRDMigrationFences[0].Publishers {
		if publisher.PauseEvidenceDigest != "" {
			t.Fatalf("publisher pause action preceded fencing state: %+v", publisher)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.root, mailboxfence.MigrationFenceFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fencing intent unexpectedly wrote a mailbox marker: %v", err)
	}
	if err := fixture.registry.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := laneregistry.Open(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.registry = reopened
	fixture.controller.Registry = reopened
	restarted, err := reopened.Snapshot()
	if err != nil || restarted.Generation != snapshot.Generation || restarted.PRDMigrationFences[0].State != laneregistry.PRDMigrationFencing {
		t.Fatalf("restart state=%+v err=%v", restarted, err)
	}
}

func TestLanesL109PublisherPreflightRejectsUntestedCapabilitiesAndRawWriterControl(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*laneregistry.PRDMigrationFenceRecord)
	}{
		{name: "raw writer without tested pause", mutate: func(record *laneregistry.PRDMigrationFenceRecord) { record.Publishers[1].PauseControlTested = false }},
		{name: "cooperating publisher without tested capability", mutate: func(record *laneregistry.PRDMigrationFenceRecord) {
			record.Publishers[0].MigrationCapabilityTested = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newL109Fixture(t)
			tc.mutate(&fixture.record)
			if _, err := fixture.controller.Begin(fixture.generation, fixture.record); !errors.Is(err, laneregistry.ErrInvalidRegistry) {
				t.Fatalf("unsafe publisher inventory error=%v", err)
			}
			snapshot, err := fixture.registry.Snapshot()
			if err != nil || len(snapshot.PRDMigrationFences) != 0 || snapshot.Generation != fixture.generation {
				t.Fatalf("invalid preflight changed registry: %+v err=%v", snapshot, err)
			}
		})
	}
}

func TestLanesL109RequiresOrderedV2StopDisableAbsenceAndZeroWorkProof(t *testing.T) {
	fixture := newL109Fixture(t)
	snapshot := fixture.beginAndProve(t, false)
	if _, err := fixture.controller.RecordV2Progress(snapshot.Generation, l109TransitionID, "member-direct", laneregistry.MigrationV2Member{
		ID: "member-direct", Surface: laneregistry.MigrationSurfaceLinuxHost, Kind: laneregistry.MigrationMemberDirectListener,
		InitiallyEnabled: true, StopEvidenceDigest: testL109Digest('a'),
	}); !errors.Is(err, laneregistry.ErrMigrationFenceProof) {
		t.Fatalf("stop without prior pause evidence error=%v", err)
	}
	if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); !errors.Is(err, ErrV2ProofMissing) {
		t.Fatalf("fence without all V2 proof error=%v", err)
	}

	fixture = newL109Fixture(t)
	snapshot, err := fixture.controller.Begin(fixture.generation, fixture.record)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range snapshot.PRDMigrationFences[0].V2Members {
		member.PauseEvidenceDigest = testL109Digest('c')
		member.StopEvidenceDigest = testL109Digest('d')
		member.DisableEvidenceDigest = testL109Digest('e')
		member.AbsenceEvidenceDigest = testL109Digest('f')
		member.ZeroWorkEvidenceDigest = testL109Digest('1')
		snapshot, err = fixture.controller.RecordV2Progress(snapshot.Generation, l109TransitionID, member.ID, member)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); !errors.Is(err, ErrV2ProofMissing) {
		t.Fatalf("fence without publisher pause proof error=%v", err)
	}

	fixture = newL109Fixture(t)
	snapshot = fixture.beginAndProve(t, true)
	writeL109Pair(t, fixture.root, "inbox", "req-l109-retained", `{"request_id":"req-l109-retained"}`)
	_, err = fixture.controller.FenceEmpty(context.Background(), l109TransitionID)
	if !errors.Is(err, mailboxfence.ErrResidue) {
		t.Fatalf("nonempty paired snapshot error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, mailboxfence.MigrationFenceFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nonempty snapshot wrote a migration marker: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(fixture.root, "inbox", "req-l109-retained.json")); err != nil || string(data) != `{"request_id":"req-l109-retained"}` {
		t.Fatalf("retained work changed: %q err=%v generation=%d", data, err, snapshot.Generation)
	}
}

func TestLanesL109EmptyFenceMarkerAndRegistryMatchAndSurviveRestart(t *testing.T) {
	fixture := newL109Fixture(t)
	fixture.beginAndProve(t, true)
	snapshot, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID)
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.PRDMigrationFences[0]
	if record.State != laneregistry.PRDMigrationFenced || record.Snapshot == nil || record.SnapshotDigest == "" || record.MarkerDigest == "" {
		t.Fatalf("registry fence incomplete: %+v", record)
	}
	lock := fixture.lock(t)
	marker, markerDigest, err := lock.ReadMigrationFenceMarker()
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != mailboxfence.MigrationFenceFenced || markerDigest != record.MarkerDigest || marker.LaneTransitionID != record.LaneTransitionID || marker.Root != record.MailboxRoot || marker.SnapshotDigest != record.SnapshotDigest {
		t.Fatalf("marker/registry mismatch marker=%+v record=%+v digest=%s", marker, record, markerDigest)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if publisher, err := mailboxfence.AcquireShared(context.Background(), fixture.root); !errors.Is(err, mailboxfence.ErrMigrationFenced) || publisher != nil {
		if publisher != nil {
			_ = publisher.Close()
		}
		t.Fatalf("shared publisher lock=%v err=%v", publisher, err)
	}
	if err := fixture.registry.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := laneregistry.Open(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.registry, fixture.controller.Registry = reopened, reopened
	if _, err := fixture.controller.Reconcile(context.Background(), l109TransitionID); err != nil {
		t.Fatalf("matching fenced state did not survive restart: %v", err)
	}
}

func TestLanesL109RejectsMarkerMismatchAndReplacedPairIdentity(t *testing.T) {
	t.Run("marker digest mismatch", func(t *testing.T) {
		fixture := newL109Fixture(t)
		fixture.beginAndProve(t, true)
		if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); err != nil {
			t.Fatal(err)
		}
		markerPath := filepath.Join(fixture.root, mailboxfence.MigrationFenceFileName)
		data, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatal(err)
		}
		lock := fixture.lock(t)
		marker, _, err := lock.ReadMigrationFenceMarker()
		if err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		marker.SnapshotDigest = testL109Digest('f')
		corrupt, err := marshalL109Marker(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(markerPath, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.Reconcile(context.Background(), l109TransitionID); !errors.Is(err, ErrBlocked) {
			t.Fatalf("mismatched marker error=%v prior=%q", err, data)
		}
	})

	t.Run("deferred JSON replacement", func(t *testing.T) {
		fixture := newL109Fixture(t)
		fixture.beginAndProve(t, true)
		if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); err != nil {
			t.Fatal(err)
		}
		jsonPath, _ := writeL109Pair(t, fixture.root, "inbox", "req-l109-replaced", `{"request_id":"req-l109-replaced"}`)
		if _, err := fixture.controller.ScanDeferred(context.Background(), l109TransitionID); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(jsonPath, []byte(`{"request_id":"replacement"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.ScanDeferred(context.Background(), l109TransitionID); !errors.Is(err, laneregistry.ErrMigrationFenceProof) {
			t.Fatalf("replacement pair identity error=%v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.root, "inbox", "req-l109-replaced.ready")); err != nil {
			t.Fatalf("replacement attempt deleted original ready evidence: %v", err)
		}
	})
}

func TestLanesL109DeferredLedgerCapturesRequestAndAckDescriptorsWithoutMutation(t *testing.T) {
	fixture := newL109Fixture(t)
	fixture.beginAndProve(t, true)
	if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); err != nil {
		t.Fatal(err)
	}
	requestJSON := `{"request_id":"req-l109-deferred"}`
	ackJSON := `{"request_id":"req-l109-deferred","response_revision":2}`
	requestPath, requestReady := writeL109Pair(t, fixture.root, "inbox", "req-l109-deferred", requestJSON)
	ackPath, ackReady := writeL109Pair(t, fixture.root, "acks", "req-l109-deferred", ackJSON)
	snapshot, err := fixture.controller.ScanDeferred(context.Background(), l109TransitionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PRDMigrationFences) != 1 {
		t.Fatalf("fence record count=%d", len(snapshot.PRDMigrationFences))
	}
	record := snapshot.PRDMigrationFences[0]
	if len(record.DeferredPairs) != 2 || len(record.DeferredObservations) != 1 {
		t.Fatalf("deferred ledger pairs=%+v observations=%+v", record.DeferredPairs, record.DeferredObservations)
	}
	for _, path := range []string{requestPath, requestReady, ackPath, ackReady} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("deferred pair member %s was removed: %v", path, err)
		}
	}
	if data, err := os.ReadFile(requestPath); err != nil || string(data) != requestJSON {
		t.Fatalf("request evidence changed: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(ackPath); err != nil || string(data) != ackJSON {
		t.Fatalf("ACK evidence changed: %q err=%v", data, err)
	}
	if _, err := fixture.controller.Reconcile(context.Background(), l109TransitionID); err != nil {
		t.Fatalf("restart scan did not preserve descriptor/digest ledger: %v", err)
	}
}

func TestLanesL109FenceAwareAbortPreservesPairsAndRemovesOnlyAbortedMarker(t *testing.T) {
	fixture := newL109Fixture(t)
	fixture.beginAndProve(t, true)
	if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); err != nil {
		t.Fatal(err)
	}
	requestJSON := `{"request_id":"req-l109-abort"}`
	ackJSON := `{"request_id":"req-l109-abort","response_revision":2}`
	requestPath, requestReady := writeL109Pair(t, fixture.root, "inbox", "req-l109-abort", requestJSON)
	ackPath, ackReady := writeL109Pair(t, fixture.root, "acks", "req-l109-abort", ackJSON)
	snapshot, err := fixture.controller.Abort(context.Background(), l109TransitionID, testL109Digest('d'))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationReleased || snapshot.PRDMigrationFences[0].FenceReleaseEvidenceDigest == "" {
		t.Fatalf("abort state=%s", snapshot.PRDMigrationFences[0].State)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, mailboxfence.MigrationFenceFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted migration marker remains or removal failed: %v", err)
	}
	for _, path := range []string{requestPath, requestReady, ackPath, ackReady} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("abort removed deferred evidence %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(requestPath); err != nil || string(data) != requestJSON {
		t.Fatalf("request changed during abort: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(ackPath); err != nil || string(data) != ackJSON {
		t.Fatalf("ACK changed during abort: %q err=%v", data, err)
	}
	shared, err := mailboxfence.AcquireShared(context.Background(), fixture.root)
	if err != nil {
		t.Fatalf("exact aborted marker was not removed for a future explicitly authorized publisher: %v", err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLanesL109CrashGapsReconcileWithoutInferredV2Restart(t *testing.T) {
	t.Run("marker written before registry fenced", func(t *testing.T) {
		fixture := newL109Fixture(t)
		snapshot := fixture.beginAndProve(t, true)
		lock := fixture.lock(t)
		observed, err := lock.SnapshotIngressPairsAt(fixture.record.InboxID, snapshot.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err = fixture.registry.RecordPRDMigrationSnapshot(snapshot.Generation, l109TransitionID, observed)
		if err != nil {
			t.Fatal(err)
		}
		record := snapshot.PRDMigrationFences[0]
		marker := mailboxfence.MigrationFenceMarker{
			LaneTransitionID: record.LaneTransitionID, InboxID: record.InboxID, Root: record.MailboxRoot,
			ConfigurationDigest: record.ConfigurationDigest, State: mailboxfence.MigrationFenceFenced,
			SnapshotDigest: record.SnapshotDigest, ObservationGeneration: record.Snapshot.ObservationGeneration,
		}
		if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		snapshot, err = fixture.controller.Reconcile(context.Background(), l109TransitionID)
		if err != nil || snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationFenced {
			t.Fatalf("registry/marker crash gap reconcile=%+v err=%v", snapshot.PRDMigrationFences, err)
		}
	})

	t.Run("pending marker blocks publishers and is reconciled", func(t *testing.T) {
		fixture := newL109Fixture(t)
		snapshot := fixture.beginAndProve(t, true)
		lock := fixture.lock(t)
		observed, err := lock.SnapshotIngressPairsAt(fixture.record.InboxID, snapshot.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err = fixture.registry.RecordPRDMigrationSnapshot(snapshot.Generation, l109TransitionID, observed)
		if err != nil {
			t.Fatal(err)
		}
		record := snapshot.PRDMigrationFences[0]
		marker := mailboxfence.MigrationFenceMarker{
			LaneTransitionID: record.LaneTransitionID, InboxID: record.InboxID, Root: record.MailboxRoot,
			ConfigurationDigest: record.ConfigurationDigest, State: mailboxfence.MigrationFenceFenced,
			SnapshotDigest: record.SnapshotDigest, ObservationGeneration: record.Snapshot.ObservationGeneration,
		}
		data, err := marshalL109Marker(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.root, mailboxfence.MigrationFencePendingFileName), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		if publisher, err := mailboxfence.AcquireShared(context.Background(), fixture.root); !errors.Is(err, mailboxfence.ErrMigrationFenced) || publisher != nil {
			if publisher != nil {
				_ = publisher.Close()
			}
			t.Fatalf("pending marker did not block publisher: lock=%v err=%v", publisher, err)
		}
		snapshot, err = fixture.controller.Reconcile(context.Background(), l109TransitionID)
		if err != nil || snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationFenced {
			t.Fatalf("pending marker restart reconcile=%+v err=%v", snapshot.PRDMigrationFences, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.root, mailboxfence.MigrationFencePendingFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending marker was not atomically promoted: %v", err)
		}
	})

	t.Run("durable fencing without full proof remains blocked", func(t *testing.T) {
		fixture := newL109Fixture(t)
		if _, err := fixture.controller.Begin(fixture.generation, fixture.record); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.Reconcile(context.Background(), l109TransitionID); !errors.Is(err, ErrV2ProofMissing) {
			t.Fatalf("partial fencing restart error=%v", err)
		}
		snapshot, err := fixture.registry.Snapshot()
		if err != nil || snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationFencing {
			t.Fatalf("incomplete restart inferred state=%+v err=%v", snapshot.PRDMigrationFences, err)
		}
	})

	t.Run("abort intent crash resumes only after pair verification", func(t *testing.T) {
		fixture := newL109Fixture(t)
		fixture.beginAndProve(t, true)
		if _, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID); err != nil {
			t.Fatal(err)
		}
		requestJSON := `{"request_id":"req-l109-reconcile"}`
		requestPath, _ := writeL109Pair(t, fixture.root, "inbox", "req-l109-reconcile", requestJSON)
		snapshot, err := fixture.controller.ScanDeferred(context.Background(), l109TransitionID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.registry.BeginPRDMigrationAbort(snapshot.Generation, l109TransitionID, testL109Digest('e')); err != nil {
			t.Fatal(err)
		}
		snapshot, err = fixture.controller.Reconcile(context.Background(), l109TransitionID)
		if err != nil || snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationReleased {
			t.Fatalf("abort-intent restart reconcile=%+v err=%v", snapshot.PRDMigrationFences, err)
		}
		if data, err := os.ReadFile(requestPath); err != nil || string(data) != requestJSON {
			t.Fatalf("reconciled abort changed deferred request: %q err=%v", data, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.root, mailboxfence.MigrationFenceFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reconciled abort did not remove exact marker: %v", err)
		}
	})
}

func TestLanesL113AbortMarkerRemovalReceiptGatesV3OperationGroup(t *testing.T) {
	fixture := newL109Fixture(t)
	fixture.beginAndProve(t, true)
	snapshot, err := fixture.controller.FenceEmpty(context.Background(), l109TransitionID)
	if err != nil {
		t.Fatal(err)
	}
	lock := fixture.lock(t)
	marker, _, err := lock.ReadMigrationFenceMarker()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation == ^uint64(0) {
		t.Fatal("registry generation overflow")
	}
	observed, err := lock.SnapshotIngressPairsAt(fixture.record.InboxID, snapshot.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = fixture.registry.RecordPRDMigrationDeferredPairs(snapshot.Generation, l109TransitionID, observed)
	if err != nil {
		t.Fatal(err)
	}
	abortDigest := testL109Digest('e')
	snapshot, err = fixture.registry.BeginPRDMigrationAbort(snapshot.Generation, l109TransitionID, abortDigest)
	if err != nil {
		t.Fatal(err)
	}
	marker.State = mailboxfence.MigrationFenceAborting
	if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
		t.Fatal(err)
	}
	snapshot, err = fixture.registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.PRDMigrationFences[0]
	observed, err = lock.SnapshotIngressPairsAt(record.InboxID, snapshot.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = fixture.registry.CompletePRDMigrationAbort(snapshot.Generation, l109TransitionID, abortDigest, observed)
	if err != nil {
		t.Fatal(err)
	}
	marker.State = mailboxfence.MigrationFenceAborted
	if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
		t.Fatal(err)
	}
	snapshot, err = fixture.registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.registry.BeginPRDMaintenance(snapshot.Generation, l113MaintenanceSpec()); !errors.Is(err, laneregistry.ErrOperationBusy) {
		t.Fatalf("V3 group acquired before marker removal: %v", err)
	}
	record = snapshot.PRDMigrationFences[0]
	earlyRelease := laneregistry.MigrationFenceReleaseEvidence{
		LaneTransitionID: record.LaneTransitionID, AbortedMarkerDigest: record.MarkerDigest,
		MailboxRoot: record.MailboxRoot, EvidenceDigest: testL109Digest('f'),
	}
	if _, err := fixture.registry.RecordPRDMigrationFenceRelease(snapshot.Generation, earlyRelease); !errors.Is(err, laneregistry.ErrMigrationFenceProof) {
		t.Fatalf("registry accepted a release receipt without marker-absence proof: %v", err)
	}
	if err := lock.RemoveMigrationFenceMarker(marker); err != nil {
		t.Fatal(err)
	}
	if err := lock.VerifyMigrationFenceMarkerAbsent(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a process exit after durable removal but before the registry
	// receipt. Reconcile must verify absence and write that receipt first.
	snapshot, err = fixture.controller.Reconcile(context.Background(), l109TransitionID)
	if err != nil || snapshot.PRDMigrationFences[0].State != laneregistry.PRDMigrationReleased {
		t.Fatalf("post-removal reconciliation=%+v err=%v", snapshot.PRDMigrationFences, err)
	}
	if _, group, err := fixture.registry.BeginPRDMaintenance(snapshot.Generation, l113MaintenanceSpec()); err != nil || group.ActiveOperationID == "" {
		t.Fatalf("V3 group did not acquire after release receipt: group=%+v err=%v", group, err)
	}
}

func l113MaintenanceSpec() laneregistry.PRDMaintenanceSpec {
	epochs := []laneregistry.PRDHostEpoch{
		{Surface: laneregistry.SurfaceFactorySandbox, AnchorHighWater: 10, ClosureEpoch: 11, CandidateEpoch: 12},
		{Surface: laneregistry.SurfaceLinuxHost, AnchorHighWater: 10, ClosureEpoch: 11, CandidateEpoch: 12},
		{Surface: laneregistry.SurfaceMac, AnchorHighWater: 10, ClosureEpoch: 11, CandidateEpoch: 12},
		{Surface: laneregistry.SurfaceSandboxHost, AnchorHighWater: 10, ClosureEpoch: 11, CandidateEpoch: 12},
	}
	return laneregistry.PRDMaintenanceSpec{
		CandidateSetDigest: testL109Digest('a'), IncumbentCandidateSetDigest: testL109Digest('b'),
		ConfigurationDigest: testL109Digest('c'), SourceRevision: strings.Repeat("1", 40),
		ArtifactDigest: testL109Digest('d'), ManifestDigest: testL109Digest('e'), ContinuityBundleDigest: testL109Digest('f'),
		PriorAcceptedCandidateDigest: testL109Digest('b'), PriorAcceptanceID: "prior-prd-acceptance",
		PriorAcceptanceDigest: testL109Digest('8'), Epochs: epochs,
	}
}

type l109Fixture struct {
	root         string
	trustRoot    string
	registryPath string
	registry     *laneregistry.Registry
	controller   *Controller
	generation   uint64
	record       laneregistry.PRDMigrationFenceRecord
}

func newL109Fixture(t *testing.T) *l109Fixture {
	t.Helper()
	trustRoot := t.TempDir()
	if err := os.Chmod(trustRoot, 0o700); err != nil {
		t.Fatalf("restrict fixture trust root: %v", err)
	}
	root := filepath.Join(trustRoot, "prd-mailbox")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"inbox", "outbox", "events", "acks", "diagnostics"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := mailboxfence.AcquireMigrationExclusiveUnder(context.Background(), trustRoot, root)
	if err != nil {
		t.Fatalf("open fixture mailbox root: %v", err)
	}
	mailboxRoot, err := lock.RootIdentity()
	if err != nil {
		t.Fatalf("read fixture mailbox root identity: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	registryParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture registry parent: %v", err)
	}
	if err := os.Chmod(registryParent, 0o700); err != nil {
		t.Fatalf("restrict fixture registry parent: %v", err)
	}
	registryPath := filepath.Join(registryParent, "registry")
	registry, err := laneregistry.Initialize(registryPath)
	if err != nil {
		t.Fatalf("initialize fixture registry: %v", err)
	}
	rootRecord := laneregistry.RootRecord{
		ID: "prd-legacy-reservation", Host: config.HostKindMac, Lane: config.ReleaseLanePRD,
		InboxID: "default", NormalizedRoot: root,
		RootIdentity:        laneroot.FileIdentity{Device: mailboxRoot.Device, Inode: mailboxRoot.Inode},
		ConfigurationDigest: testL109Digest('a'), Epoch: 1, State: laneregistry.StateLegacyPRDReserved,
	}
	snapshot, err := registry.ReserveLegacyPRD(0, rootRecord)
	if err != nil {
		t.Fatalf("reserve fixture PRD root: %v", err)
	}
	controller := New(registry)
	controller.Open = func(ctx context.Context, path string) (*mailboxfence.Lock, error) {
		return mailboxfence.AcquireMigrationExclusiveUnder(ctx, trustRoot, path)
	}
	fixture := &l109Fixture{
		root: root, trustRoot: trustRoot, registryPath: registryPath, registry: registry,
		controller: controller, generation: snapshot.Generation, record: fixtureMigrationRecord(rootRecord, mailboxRoot),
	}
	t.Cleanup(func() {
		if fixture.registry != nil {
			_ = fixture.registry.Close()
		}
	})
	return fixture
}

func fixtureMigrationRecord(root laneregistry.RootRecord, mailboxRoot mailboxfence.FileIdentity) laneregistry.PRDMigrationFenceRecord {
	return laneregistry.PRDMigrationFenceRecord{
		LaneTransitionID: l109TransitionID, RootRecordID: root.ID, InboxID: root.InboxID,
		NormalizedRoot: root.NormalizedRoot, RootIdentity: root.RootIdentity, MailboxRoot: mailboxRoot,
		ConfigurationDigest: root.ConfigurationDigest, PreflightDigest: testL109Digest('b'),
		State: laneregistry.PRDMigrationFencing,
		Publishers: []laneregistry.MigrationPublisher{
			{ID: "publisher-native", Surface: laneregistry.MigrationSurfaceMac, Kind: "native-client", MigrationCapabilityVersion: "1", MigrationCapabilityTested: true, PauseControl: "launchctl disable fixture", PauseControlTested: true},
			{ID: "publisher-raw", Surface: laneregistry.MigrationSurfaceFactorySandbox, Kind: "workspace-raw-writer", RawWriter: true, PauseControl: "owner pause fixture", PauseControlTested: true},
		},
		V2Members: []laneregistry.MigrationV2Member{
			{ID: "member-direct", Surface: laneregistry.MigrationSurfaceLinuxHost, Kind: laneregistry.MigrationMemberDirectListener, InitiallyEnabled: true},
			{ID: "member-local", Surface: laneregistry.MigrationSurfaceMac, Kind: laneregistry.MigrationMemberMacLocalIngress, InitiallyEnabled: true},
			{ID: "member-importer", Surface: laneregistry.MigrationSurfaceMac, Kind: laneregistry.MigrationMemberMailboxImporter, InitiallyEnabled: true},
			{ID: "member-bridge", Surface: laneregistry.MigrationSurfaceSandboxHost, Kind: laneregistry.MigrationMemberQueuedBridge, InitiallyEnabled: true},
		},
	}
}

func (f *l109Fixture) beginAndProve(t *testing.T, all bool) laneregistry.Snapshot {
	t.Helper()
	snapshot, err := f.controller.Begin(f.generation, f.record)
	if err != nil {
		t.Fatal(err)
	}
	if !all {
		return snapshot
	}
	for _, member := range snapshot.PRDMigrationFences[0].V2Members {
		member.PauseEvidenceDigest = testL109Digest('c')
		member.StopEvidenceDigest = testL109Digest('d')
		member.DisableEvidenceDigest = testL109Digest('e')
		member.AbsenceEvidenceDigest = testL109Digest('f')
		member.ZeroWorkEvidenceDigest = testL109Digest('1')
		snapshot, err = f.controller.RecordV2Progress(snapshot.Generation, l109TransitionID, member.ID, member)
		if err != nil {
			t.Fatalf("record V2 progress %s: %v", member.ID, err)
		}
	}
	for _, publisher := range snapshot.PRDMigrationFences[0].Publishers {
		snapshot, err = f.controller.RecordPublisherPause(snapshot.Generation, l109TransitionID, publisher.ID, testL109Digest('2'))
		if err != nil {
			t.Fatalf("record publisher pause %s: %v", publisher.ID, err)
		}
	}
	return snapshot
}

func (f *l109Fixture) lock(t *testing.T) *mailboxfence.Lock {
	t.Helper()
	lock, err := mailboxfence.AcquireMigrationExclusiveUnder(context.Background(), f.trustRoot, f.root)
	if err != nil {
		t.Fatal(err)
	}
	return lock
}

func writeL109Pair(t *testing.T, root, directory, id, jsonBody string) (string, string) {
	t.Helper()
	jsonPath := filepath.Join(root, directory, id+".json")
	readyPath := filepath.Join(root, directory, id+".ready")
	if err := os.WriteFile(jsonPath, []byte(jsonBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return jsonPath, readyPath
}

func testL109Digest(char byte) string { return "sha256:" + string(makeL109Digest(char)) }

func makeL109Digest(char byte) []byte {
	result := make([]byte, 64)
	for index := range result {
		result[index] = char
	}
	return result
}

func marshalL109Marker(marker mailboxfence.MigrationFenceMarker) ([]byte, error) {
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
