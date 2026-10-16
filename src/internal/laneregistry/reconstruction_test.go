package laneregistry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneroot"
)

func TestLanesL107AcceptsExactAuthorityAndDescriptorReconstruction(t *testing.T) {
	proposal := l107ActiveProposal(t)
	before := proposal.Snapshot
	if err := ValidateRegistryReconstruction(proposal); err != nil {
		t.Fatalf("valid reconstruction proposal rejected: %v", err)
	}
	if proposal.Snapshot.Generation != before.Generation || !sameRecord(proposal.Snapshot.Records[0], before.Records[0]) {
		t.Fatal("validation mutated the proposed registry snapshot")
	}
}

func TestLanesL107OpenAndInitializeRemainFailClosedOnMissingOrCorruptRegistry(t *testing.T) {
	t.Run("missing registry file", func(t *testing.T) {
		path := fixtureRegistryPath(t)
		registry, err := Initialize(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		registryPath := filepath.Join(path, RegistryFile)
		if err := os.Remove(registryPath); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); !IsRegistryRecoveryStop(err) {
			t.Fatalf("Open(missing registry) = %v, want recovery stop", err)
		}
		if _, err := Initialize(path); !errors.Is(err, ErrAlreadyInitialized) {
			t.Fatalf("Initialize over incomplete prior registry = %v, want already initialized", err)
		}
		if _, err := os.Stat(registryPath); !os.IsNotExist(err) {
			t.Fatalf("failed Open/Initialize recreated registry bytes: stat err=%v", err)
		}
	})

	t.Run("corrupt bytes preserved", func(t *testing.T) {
		path := fixtureRegistryPath(t)
		registry, err := Initialize(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		registryPath := filepath.Join(path, RegistryFile)
		bad := []byte(`{"version":1,"version":1,"generation":0,"records":[]}`)
		backup := []byte(`preserved incident backup; not an authority`)
		if err := os.WriteFile(registryPath, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registryPath+".backup", backup, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); !IsRegistryRecoveryStop(err) {
			t.Fatalf("Open(corrupt registry) = %v, want recovery stop", err)
		}
		if _, err := Initialize(path); !errors.Is(err, ErrAlreadyInitialized) {
			t.Fatalf("Initialize over corrupt prior registry = %v, want already initialized", err)
		}
		got, err := os.ReadFile(registryPath)
		if err != nil || string(got) != string(bad) {
			t.Fatalf("corrupt registry evidence changed: bytes=%q err=%v", got, err)
		}
		got, err = os.ReadFile(registryPath + ".backup")
		if err != nil || string(got) != string(backup) {
			t.Fatalf("backup was consumed or changed: bytes=%q err=%v", got, err)
		}
	})

	t.Run("rollback generation stop", func(t *testing.T) {
		path := fixtureRegistryPath(t)
		registry, err := Initialize(path)
		if err != nil {
			t.Fatal(err)
		}
		record := fixtureRecord("dev-root", config.ReleaseLaneDev, "dev-inbox", "/Volumes/runner-dev", 811, 1, StatePrepared)
		if _, err := registry.Prepare(0, record); err != nil {
			t.Fatal(err)
		}
		if err := registry.Close(); err != nil {
			t.Fatal(err)
		}
		registryPath := filepath.Join(path, RegistryFile)
		before, err := os.ReadFile(registryPath)
		if err != nil {
			t.Fatal(err)
		}
		anchor := VerifiedRegistryGenerationAnchor{GenerationFloor: 2, EvidenceDigest: testDigest('a')}
		if _, err := OpenAtVerifiedGeneration(path, anchor); !errors.Is(err, ErrRegistryRecoveryRequired) || !IsRegistryRecoveryStop(err) {
			t.Fatalf("lower registry generation returned %v, want recovery stop", err)
		}
		after, err := os.ReadFile(registryPath)
		if err != nil || string(after) != string(before) {
			t.Fatalf("rollback stop changed registry bytes: err=%v", err)
		}
	})
}

func TestLanesL107RejectsGenerationRollbackAndMissingAuthorityEvidence(t *testing.T) {
	proposal := l107ActiveProposal(t)
	proposal.Cause = RecoveryRegistryRollback
	proposal.FailedBytesDigest = testDigest('f')
	proposal.ObservedGeneration = proposal.GenerationFloor - 1
	if err := ValidateRegistryReconstruction(proposal); err != nil {
		t.Fatalf("verified higher-generation reconstruction rejected: %v", err)
	}

	stale := proposal
	stale.Snapshot.Generation = stale.GenerationFloor
	if err := ValidateRegistryReconstruction(stale); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("stale proposed generation returned %v", err)
	}
	missingBytes := proposal
	missingBytes.FailedBytesDigest = ""
	if err := ValidateRegistryReconstruction(missingBytes); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("rollback proposal without preserved bytes returned %v", err)
	}
	missingAuthority := proposal
	missingAuthority.AuthorityEvidenceDigest = ""
	if err := ValidateRegistryReconstruction(missingAuthority); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("proposal without verified authority evidence returned %v", err)
	}
	missingIncident := proposal
	missingIncident.PreservedEvidenceDigest = ""
	if err := ValidateRegistryReconstruction(missingIncident); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("proposal without preserved incident evidence returned %v", err)
	}
}

func TestLanesL107RejectsRootFactDescriptorMismatchAndCollision(t *testing.T) {
	proposal := l107ActiveProposal(t)
	wrongFact := proposal
	wrongFact.RootFacts = append([]VerifiedRootFact(nil), proposal.RootFacts...)
	wrongFact.RootFacts[0].Record.ConfigurationDigest = testDigest('9')
	if err := ValidateRegistryReconstruction(wrongFact); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("root fact with a different configuration returned %v", err)
	}

	wrongDescriptor := proposal
	wrongDescriptor.FreshInventory = append([]RecoveryRootInventory(nil), proposal.FreshInventory...)
	wrongDescriptor.FreshInventory[0].Root.Claim.RootIdentity.Inode++
	if err := ValidateRegistryReconstruction(wrongDescriptor); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("descriptor with a replaced root identity returned %v", err)
	}

	missingDescriptor := proposal
	missingDescriptor.FreshInventory = nil
	if err := ValidateRegistryReconstruction(missingDescriptor); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("active root without fresh descriptor returned %v", err)
	}

	aliased := l107ActiveProposal(t)
	second := fixtureRecord("qas-root", config.ReleaseLaneQAS, "qas-inbox", "/Volumes/runner-qas", 901, 1, StateActive)
	aliased.Snapshot.Records = append(aliased.Snapshot.Records, second)
	aliased.RootFacts = append(aliased.RootFacts, VerifiedRootFact{Record: second, EvidenceDigest: testDigest('9')})
	aliased.FreshInventory = append(aliased.FreshInventory, l107Inventory(second))
	aliased.AnchorFacts = append(aliased.AnchorFacts, l107Anchors(second)...)
	aliased.Snapshot.Generation++
	aliased.GenerationFloor++
	aliased.FreshInventory[1].Root.Claim.RootIdentity = aliased.FreshInventory[0].Root.Claim.RootIdentity
	if err := ValidateRegistryReconstruction(aliased); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("aliased descriptor roots returned %v", err)
	}
}

func TestLanesL107RequiresCompleteAnchorsForActiveRoots(t *testing.T) {
	proposal := l107ActiveProposal(t)
	proposal.AnchorFacts = proposal.AnchorFacts[:len(proposal.AnchorFacts)-1]
	if err := ValidateRegistryReconstruction(proposal); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("active root missing a host anchor returned %v", err)
	}

	wrongAnchor := l107ActiveProposal(t)
	wrongAnchor.AnchorFacts[0].ConfigurationDigest = testDigest('9')
	if err := ValidateRegistryReconstruction(wrongAnchor); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("anchor from a different configuration returned %v", err)
	}
}

func TestLanesL107RequiresExactClosedDispositionForEveryInflightOperation(t *testing.T) {
	registry, snapshot, operation := l105StartOperation(t, OperationReplacement)
	defer registry.Close()
	proposal := l107ProposalForSnapshot(snapshot)
	operationDigest := laneOperationEvidenceDigest(operation)
	proposal.InFlight = []RecoveryOperationFact{{
		ID: operation.ID, Lane: operation.Lane, Kind: RecoveryDevQASOperation, EvidenceDigest: operationDigest,
	}}
	proposal.Dispositions = []RecoveryOperationDisposition{{
		OperationID: operation.ID, Lane: operation.Lane, Kind: RecoveryRemainClosed,
		OperationEvidenceDigest: operationDigest, ClosedEvidenceDigest: testDigest('e'),
	}}
	if err := ValidateRegistryReconstruction(proposal); err != nil {
		t.Fatalf("exact closed in-flight disposition rejected: %v", err)
	}

	missing := proposal
	missing.Dispositions = nil
	if err := ValidateRegistryReconstruction(missing); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("in-flight operation without disposition returned %v", err)
	}

	wrongEvidence := proposal
	wrongEvidence.Dispositions = append([]RecoveryOperationDisposition(nil), proposal.Dispositions...)
	wrongEvidence.Dispositions[0].OperationEvidenceDigest = testDigest('9')
	if err := ValidateRegistryReconstruction(wrongEvidence); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("disposition detached from preserved operation returned %v", err)
	}

	unsafe := proposal
	unsafe.Dispositions = append([]RecoveryOperationDisposition(nil), proposal.Dispositions...)
	unsafe.Dispositions[0].Kind = RecoveryDispositionKind("resume_old_pointer")
	if err := ValidateRegistryReconstruction(unsafe); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("inferred operation resume returned %v", err)
	}
}

func TestLanesL107ClassifiesPRDDispositionsWithoutV2Restart(t *testing.T) {
	proposal := l107EmptyProposal()
	fact := RecoveryOperationFact{
		ID: "00000000-0000-7000-8000-000000000001", Lane: config.ReleaseLanePRD,
		Kind: RecoveryPRDOperation, PRDPhase: RecoveryPRDPreCommitFenced, EvidenceDigest: testDigest('a'),
		FenceMarkerDigest: testDigest('c'), PairedSnapshotDigest: testDigest('d'),
		ServiceAnchorDigest: testDigest('e'), CommitEvidenceDigest: testDigest('f'),
	}
	proposal.InFlight = []RecoveryOperationFact{fact}
	proposal.Dispositions = []RecoveryOperationDisposition{{
		OperationID: fact.ID, Lane: fact.Lane, Kind: RecoveryPRDV2FenceAwareAbort,
		OperationEvidenceDigest: fact.EvidenceDigest, ClosedEvidenceDigest: testDigest('b'),
		FenceMarkerDigest: fact.FenceMarkerDigest, PairedSnapshotDigest: fact.PairedSnapshotDigest,
		ServiceAnchorDigest: fact.ServiceAnchorDigest, CommitEvidenceDigest: fact.CommitEvidenceDigest,
	}}
	if err := ValidateRegistryReconstruction(proposal); err != nil {
		t.Fatalf("precommit fence-aware disposition rejected: %v", err)
	}

	wrongPhase := proposal
	wrongPhase.InFlight = append([]RecoveryOperationFact(nil), proposal.InFlight...)
	wrongPhase.InFlight[0].PRDPhase = RecoveryPRDPostCommit
	if err := ValidateRegistryReconstruction(wrongPhase); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("V2 abort selected after commit returned %v", err)
	}
	incompleteFence := proposal
	incompleteFence.InFlight = append([]RecoveryOperationFact(nil), proposal.InFlight...)
	incompleteFence.InFlight[0].PairedSnapshotDigest = ""
	if err := ValidateRegistryReconstruction(incompleteFence); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("fenced PRD operation without paired snapshot evidence returned %v", err)
	}

	unsafe := proposal
	unsafe.Dispositions = append([]RecoveryOperationDisposition(nil), proposal.Dispositions...)
	unsafe.Dispositions[0].Kind = RecoveryDispositionKind("restart_v2")
	if err := ValidateRegistryReconstruction(unsafe); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("inferred V2 restart returned %v", err)
	}

	postCommit := l107EmptyProposal()
	fact.PRDPhase = RecoveryPRDPostCommit
	fact.FenceMarkerDigest = ""
	fact.PairedSnapshotDigest = ""
	postCommit.InFlight = []RecoveryOperationFact{fact}
	postCommit.Dispositions = []RecoveryOperationDisposition{{
		OperationID: fact.ID, Lane: fact.Lane, Kind: RecoveryPRDV3ForwardRepair,
		OperationEvidenceDigest: fact.EvidenceDigest, ClosedEvidenceDigest: testDigest('b'),
		CommitEvidenceDigest: fact.CommitEvidenceDigest, ServiceAnchorDigest: fact.ServiceAnchorDigest,
	}}
	if err := ValidateRegistryReconstruction(postCommit); err != nil {
		t.Fatalf("postcommit V3 repair disposition rejected: %v", err)
	}

	ambiguous := l107EmptyProposal()
	fact.PRDPhase = RecoveryPRDAmbiguous
	ambiguous.InFlight = []RecoveryOperationFact{fact}
	ambiguous.Dispositions = []RecoveryOperationDisposition{{
		OperationID: fact.ID, Lane: fact.Lane, Kind: RecoveryRemainClosed,
		OperationEvidenceDigest: fact.EvidenceDigest, ClosedEvidenceDigest: testDigest('b'),
	}}
	if err := ValidateRegistryReconstruction(ambiguous); err != nil {
		t.Fatalf("ambiguous PRD operation could not remain closed: %v", err)
	}
}

func TestLanesL107RejectsMalformedOrCollidingDescriptorInventories(t *testing.T) {
	proposal := l107ActiveProposal(t)
	missingChild := proposal
	missingChild.FreshInventory = append([]RecoveryRootInventory(nil), proposal.FreshInventory...)
	missingChild.FreshInventory[0].Root.ChildIdentities = map[string]laneroot.FileIdentity{"inbox": {Device: 77, Inode: 1}}
	if err := ValidateRegistryReconstruction(missingChild); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("incomplete descriptor child inventory returned %v", err)
	}

	aliasedChild := l107ActiveProposal(t)
	aliasedChild.FreshInventory = append([]RecoveryRootInventory(nil), proposal.FreshInventory...)
	aliasedChild.FreshInventory[0].Root.ChildIdentities["outbox"] = aliasedChild.FreshInventory[0].Root.ChildIdentities["inbox"]
	if err := ValidateRegistryReconstruction(aliasedChild); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("aliased mailbox children returned %v", err)
	}

	invalidCause := l107ActiveProposal(t)
	invalidCause.Cause = RegistryRecoveryCause("restore_backup")
	if err := ValidateRegistryReconstruction(invalidCause); !errors.Is(err, ErrInvalidReconstruction) {
		t.Fatalf("unapproved recovery cause returned %v", err)
	}
}

func l107EmptyProposal() RegistryReconstructionProposal {
	return RegistryReconstructionProposal{
		IncidentID: "incident-107", Cause: RecoveryRegistryMissing,
		PreservedEvidenceDigest: testDigest('a'), AuthorityEvidenceDigest: testDigest('b'),
		GenerationFloor: 8,
		Snapshot:        Snapshot{Version: FormatVersion, Generation: 9, Records: []RootRecord{}},
	}
}

func l107ActiveProposal(t *testing.T) RegistryReconstructionProposal {
	t.Helper()
	record := fixtureRecord("dev-root", config.ReleaseLaneDev, "dev-inbox", "/Volumes/runner-dev", 801, 1, StateActive)
	return l107ProposalForSnapshot(Snapshot{
		Version: FormatVersion, Generation: 9, Records: []RootRecord{record},
	})
}

func l107ProposalForSnapshot(snapshot Snapshot) RegistryReconstructionProposal {
	proposal := RegistryReconstructionProposal{
		IncidentID: "incident-107", Cause: RecoveryRegistryCorrupt,
		FailedBytesDigest: testDigest('f'), PreservedEvidenceDigest: testDigest('a'),
		AuthorityEvidenceDigest: testDigest('b'), GenerationFloor: snapshot.Generation - 1,
		Snapshot: snapshot,
	}
	type inventoryKey struct {
		path     string
		identity laneroot.FileIdentity
	}
	seenInventories := make(map[inventoryKey]struct{})
	for _, record := range snapshot.Records {
		proposal.RootFacts = append(proposal.RootFacts, VerifiedRootFact{Record: record, EvidenceDigest: testDigest('c')})
		if record.State == StateActive {
			proposal.AnchorFacts = append(proposal.AnchorFacts, l107Anchors(record)...)
		}
		if record.State != StateActive && record.State != StatePrepared && record.State != StateLegacyPRDReserved {
			continue
		}
		key := inventoryKey{path: record.NormalizedRoot, identity: record.RootIdentity}
		if _, exists := seenInventories[key]; exists {
			continue
		}
		seenInventories[key] = struct{}{}
		proposal.FreshInventory = append(proposal.FreshInventory, l107Inventory(record))
	}
	return proposal
}

func l107Inventory(record RootRecord) RecoveryRootInventory {
	children := map[string]laneroot.FileIdentity{
		"acks":        {Device: record.RootIdentity.Device, Inode: 1001},
		"diagnostics": {Device: record.RootIdentity.Device, Inode: 1002},
		"events":      {Device: record.RootIdentity.Device, Inode: 1003},
		"inbox":       {Device: record.RootIdentity.Device, Inode: 1004},
		"outbox":      {Device: record.RootIdentity.Device, Inode: 1005},
	}
	claim := laneroot.ValidatedRoot{
		Host: record.Host, Lane: record.Lane, InboxID: record.InboxID,
		Path: record.NormalizedRoot, RootIdentity: record.RootIdentity,
	}
	var volume *laneroot.VolumeIdentity
	if record.Volume != nil {
		copy := *record.Volume
		volume = &copy
	}
	return RecoveryRootInventory{
		Root:   laneroot.RootInventory{Claim: claim, ChildIdentities: children, AncillaryEntries: append([]laneroot.AncillaryEntrySnapshot(nil), record.AncillaryEntries...)},
		Volume: volume,
	}
}

func l107Anchors(record RootRecord) []VerifiedAnchorFact {
	anchors := make([]VerifiedAnchorFact, 0, len(recoverySurfaces))
	for _, surface := range recoverySurfaces {
		anchors = append(anchors, VerifiedAnchorFact{
			Surface: surface, RecordID: record.ID, Lane: record.Lane,
			ConfigurationDigest: record.ConfigurationDigest, Epoch: record.Epoch,
			EvidenceDigest: testDigest('d'),
		})
	}
	return anchors
}
