package laneregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneroot"
)

type RegistryRecoveryCause string

const (
	RecoveryRegistryMissing  RegistryRecoveryCause = "missing"
	RecoveryRegistryCorrupt  RegistryRecoveryCause = "corrupt"
	RecoveryRegistryRollback RegistryRecoveryCause = "rollback_suspected"
)

type RecoveryOperationKind string

const (
	RecoveryDevQASOperation RecoveryOperationKind = "dev_qas"
	RecoveryPRDOperation    RecoveryOperationKind = "prd"
)

type RecoveryPRDPhase string

const (
	RecoveryPRDPreCommitFenced RecoveryPRDPhase = "precommit_fenced"
	RecoveryPRDPostCommit      RecoveryPRDPhase = "postcommit"
	RecoveryPRDAmbiguous       RecoveryPRDPhase = "ambiguous"
)

type RecoveryDispositionKind string

const (
	RecoveryRemainClosed          RecoveryDispositionKind = "remain_closed"
	RecoveryRequireFreshSuccessor RecoveryDispositionKind = "require_fresh_successor"
	RecoveryPRDV2FenceAwareAbort  RecoveryDispositionKind = "prd_v2_fence_aware_abort"
	RecoveryPRDV3ForwardRepair    RecoveryDispositionKind = "prd_v3_forward_repair"
)

// VerifiedRootFact is copied from an already verified authority/anchor or
// preserved incident source. EvidenceDigest names that source evidence; this
// package does not verify signatures or read authority state.
type VerifiedRootFact struct {
	Record         RootRecord
	EvidenceDigest string
}

// VerifiedAnchorFact binds a host surface's verified high-water context to an
// exact reconstructed root identity. It is evidence input, not an attestation
// verifier or runtime authorization.
type VerifiedAnchorFact struct {
	Surface             string
	RecordID            string
	Lane                config.ReleaseLane
	ConfigurationDigest string
	Epoch               uint64
	EvidenceDigest      string
}

// RecoveryRootInventory combines the fresh descriptor inventory from
// laneroot.InspectRoot with the independently observed Mac volume identity.
// The inventory is read-only and grants no root mutation authority.
type RecoveryRootInventory struct {
	Root   laneroot.RootInventory
	Volume *laneroot.VolumeIdentity
}

type RecoveryOperationFact struct {
	ID                   string
	Lane                 config.ReleaseLane
	Kind                 RecoveryOperationKind
	PRDPhase             RecoveryPRDPhase `json:"prd_phase,omitempty"`
	EvidenceDigest       string
	FenceMarkerDigest    string `json:"fence_marker_digest,omitempty"`
	PairedSnapshotDigest string `json:"paired_snapshot_digest,omitempty"`
	ServiceAnchorDigest  string `json:"service_anchor_digest,omitempty"`
	CommitEvidenceDigest string `json:"commit_evidence_digest,omitempty"`
}

// RecoveryOperationDisposition is an explicit closed decision for one exact
// in-flight operation. No disposition resumes a pointer, releases a group, or
// restarts V2. The evidence digests must come from the separate reviewed
// incident procedure; validation here is structural only.
type RecoveryOperationDisposition struct {
	OperationID             string
	Lane                    config.ReleaseLane
	Kind                    RecoveryDispositionKind
	OperationEvidenceDigest string
	ClosedEvidenceDigest    string
	FenceMarkerDigest       string `json:",omitempty"`
	PairedSnapshotDigest    string `json:",omitempty"`
	ServiceAnchorDigest     string `json:",omitempty"`
	CommitEvidenceDigest    string `json:",omitempty"`
}

// VerifiedRegistryGenerationAnchor carries the externally verified registry
// generation high-water mark. The evidence digest is checked for shape only;
// its authority/signature is verified by the caller before use.
type VerifiedRegistryGenerationAnchor struct {
	GenerationFloor uint64
	EvidenceDigest  string
}

// RegistryReconstructionProposal is the reviewable input to the separately
// approved A09 root-maintenance procedure. ValidateRegistryReconstruction
// never writes the proposal or repairs the registry.
type RegistryReconstructionProposal struct {
	IncidentID              string
	Cause                   RegistryRecoveryCause
	FailedBytesDigest       string
	PreservedEvidenceDigest string
	AuthorityEvidenceDigest string
	GenerationFloor         uint64
	ObservedGeneration      uint64
	Snapshot                Snapshot
	RootFacts               []VerifiedRootFact
	AnchorFacts             []VerifiedAnchorFact
	FreshInventory          []RecoveryRootInventory
	InFlight                []RecoveryOperationFact
	Dispositions            []RecoveryOperationDisposition
}

var recoverySurfaces = []string{
	SurfaceFactorySandbox,
	SurfaceLinuxHost,
	SurfaceMac,
	SurfaceSandboxHost,
}

var recoveryMailboxChildren = []string{"acks", "diagnostics", "events", "inbox", "outbox"}

// OpenAtVerifiedGeneration opens only a structurally valid registry at or
// above a separately verified monotonic generation floor. A lower generation
// returns a recovery stop and leaves the bytes untouched.
func OpenAtVerifiedGeneration(path string, anchor VerifiedRegistryGenerationAnchor) (*Registry, error) {
	if !digestPattern.MatchString(anchor.EvidenceDigest) {
		return nil, ErrInvalidReconstruction
	}
	registry, err := Open(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := registry.Snapshot()
	if err != nil {
		registry.Close()
		return nil, err
	}
	if snapshot.Generation < anchor.GenerationFloor {
		registry.Close()
		return nil, ErrRegistryRecoveryRequired
	}
	return registry, nil
}

// ValidateRegistryReconstruction checks that a proposed replacement snapshot
// is supported by preserved incident evidence, verified root/anchor facts,
// fresh descriptor identities, and explicit closed dispositions. It performs
// no filesystem reads or writes and does not verify evidence signatures.
func ValidateRegistryReconstruction(proposal RegistryReconstructionProposal) error {
	if !registryIDPattern.MatchString(proposal.IncidentID) ||
		!digestPattern.MatchString(proposal.PreservedEvidenceDigest) ||
		!digestPattern.MatchString(proposal.AuthorityEvidenceDigest) ||
		proposal.GenerationFloor == math.MaxUint64 || proposal.Snapshot.Generation != proposal.GenerationFloor+1 {
		return ErrInvalidReconstruction
	}
	switch proposal.Cause {
	case RecoveryRegistryMissing:
		if proposal.FailedBytesDigest != "" || proposal.ObservedGeneration != 0 {
			return ErrInvalidReconstruction
		}
	case RecoveryRegistryCorrupt:
		if !digestPattern.MatchString(proposal.FailedBytesDigest) ||
			(proposal.ObservedGeneration != 0 && proposal.ObservedGeneration > proposal.GenerationFloor) {
			return ErrInvalidReconstruction
		}
	case RecoveryRegistryRollback:
		if !digestPattern.MatchString(proposal.FailedBytesDigest) || proposal.ObservedGeneration >= proposal.GenerationFloor {
			return ErrInvalidReconstruction
		}
	default:
		return ErrInvalidReconstruction
	}
	if err := ValidateSnapshot(proposal.Snapshot); err != nil {
		return ErrInvalidReconstruction
	}
	if err := validateRecoveryRootFacts(proposal.Snapshot, proposal.RootFacts); err != nil {
		return err
	}
	if err := validateRecoveryInventory(proposal.Snapshot, proposal.FreshInventory); err != nil {
		return err
	}
	if err := validateRecoveryAnchors(proposal.Snapshot, proposal.AnchorFacts); err != nil {
		return err
	}
	if err := validateRecoveryOperations(proposal); err != nil {
		return err
	}
	return nil
}

func validateRecoveryRootFacts(snapshot Snapshot, facts []VerifiedRootFact) error {
	if len(facts) != len(snapshot.Records) {
		return ErrInvalidReconstruction
	}
	factsByID := make(map[string]VerifiedRootFact, len(facts))
	for _, fact := range facts {
		if !digestPattern.MatchString(fact.EvidenceDigest) || validateRecord(fact.Record) != nil {
			return ErrInvalidReconstruction
		}
		if _, duplicate := factsByID[fact.Record.ID]; duplicate {
			return ErrInvalidReconstruction
		}
		factsByID[fact.Record.ID] = fact
	}
	for _, record := range snapshot.Records {
		fact, exists := factsByID[record.ID]
		if !exists || !sameRecord(fact.Record, record) {
			return ErrInvalidReconstruction
		}
	}
	return nil
}

func validateRecoveryInventory(snapshot Snapshot, inventories []RecoveryRootInventory) error {
	records := snapshot.Records
	roots := make([]laneroot.ValidatedRoot, 0, len(inventories))
	seenObjects := make(map[laneroot.FileIdentity]struct{})
	for _, inventory := range inventories {
		root := inventory.Root
		claim := root.Claim
		if claim.Host != config.HostKindMac || !validLane(claim.Lane) || !registryIDPattern.MatchString(claim.InboxID) ||
			!canonicalPath(claim.Path) || !validIdentity(claim.RootIdentity) || !validRecoveryChildren(root) ||
			!validRecoveryAncillary(root) {
			return ErrInvalidReconstruction
		}
		if requiresAPFS(claim.Host, claim.Lane) {
			if inventory.Volume == nil || !validVolume(*inventory.Volume) ||
				inventory.Volume.Mountpoint != claim.Path || inventory.Volume.Device != claim.RootIdentity.Device {
				return ErrInvalidReconstruction
			}
		} else if inventory.Volume != nil {
			return ErrInvalidReconstruction
		}
		roots = append(roots, claim)
		objects := []laneroot.FileIdentity{claim.RootIdentity}
		for _, name := range recoveryMailboxChildren {
			objects = append(objects, root.ChildIdentities[name])
		}
		for _, entry := range root.AncillaryEntries {
			objects = append(objects, entry.Identity)
		}
		for _, identity := range objects {
			if _, duplicate := seenObjects[identity]; duplicate {
				return ErrInvalidReconstruction
			}
			seenObjects[identity] = struct{}{}
		}
		matched := false
		for _, record := range records {
			if record.Host != claim.Host || record.Lane != claim.Lane || record.InboxID != claim.InboxID ||
				record.NormalizedRoot != claim.Path || record.RootIdentity != claim.RootIdentity {
				continue
			}
			if !reflect.DeepEqual(record.Volume, inventory.Volume) {
				return ErrInvalidReconstruction
			}
			if record.State == StateActive || record.State == StatePrepared || record.State == StateLegacyPRDReserved {
				if !sameRecoveryAncillary(record.AncillaryEntries, root.AncillaryEntries) {
					return ErrInvalidReconstruction
				}
			}
			matched = true
		}
		if !matched {
			return ErrInvalidReconstruction
		}
	}
	if laneroot.ValidateRootSet(roots) != nil {
		return ErrInvalidReconstruction
	}
	for _, record := range records {
		if record.State != StateActive && record.State != StatePrepared && record.State != StateLegacyPRDReserved {
			continue
		}
		matches := 0
		for _, inventory := range inventories {
			claim := inventory.Root.Claim
			if claim.Host == record.Host && claim.Lane == record.Lane && claim.InboxID == record.InboxID &&
				claim.Path == record.NormalizedRoot && claim.RootIdentity == record.RootIdentity {
				matches++
			}
		}
		if matches != 1 {
			return ErrInvalidReconstruction
		}
	}
	return nil
}

func validRecoveryChildren(root laneroot.RootInventory) bool {
	if len(root.ChildIdentities) != len(recoveryMailboxChildren) {
		return false
	}
	seen := map[laneroot.FileIdentity]struct{}{root.Claim.RootIdentity: {}}
	for _, name := range recoveryMailboxChildren {
		identity, exists := root.ChildIdentities[name]
		if !exists || !validIdentity(identity) || identity.Device != root.Claim.RootIdentity.Device {
			return false
		}
		if _, duplicate := seen[identity]; duplicate {
			return false
		}
		seen[identity] = struct{}{}
	}
	return true
}

func validRecoveryAncillary(root laneroot.RootInventory) bool {
	seenNames := make(map[string]struct{}, len(root.AncillaryEntries))
	seenIDs := map[laneroot.FileIdentity]struct{}{root.Claim.RootIdentity: {}}
	for _, name := range recoveryMailboxChildren {
		seenIDs[root.ChildIdentities[name]] = struct{}{}
	}
	for _, entry := range root.AncillaryEntries {
		if !ancillaryNamePattern.MatchString(entry.Name) || entry.Name == "." || entry.Name == ".." || isFixedMailboxEntry(entry.Name) ||
			(entry.Kind != laneroot.EntryRegularFile && entry.Kind != laneroot.EntryDirectory) ||
			!validIdentity(entry.Identity) || entry.Identity.Device != root.Claim.RootIdentity.Device ||
			entry.OwnerUID == 0 || entry.Mode&0o022 != 0 || entry.Mode&0o7000 != 0 {
			return false
		}
		if _, duplicate := seenNames[entry.Name]; duplicate {
			return false
		}
		if _, duplicate := seenIDs[entry.Identity]; duplicate {
			return false
		}
		seenNames[entry.Name] = struct{}{}
		seenIDs[entry.Identity] = struct{}{}
	}
	return true
}

func sameRecoveryAncillary(left, right []laneroot.AncillaryEntrySnapshot) bool {
	left = append([]laneroot.AncillaryEntrySnapshot(nil), left...)
	right = append([]laneroot.AncillaryEntrySnapshot(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].Name < left[j].Name })
	sort.Slice(right, func(i, j int) bool { return right[i].Name < right[j].Name })
	return reflect.DeepEqual(left, right)
}

func validateRecoveryAnchors(snapshot Snapshot, anchors []VerifiedAnchorFact) error {
	records := make(map[string]RootRecord, len(snapshot.Records))
	for _, record := range snapshot.Records {
		records[record.ID] = record
	}
	seen := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		if !isRecoverySurface(anchor.Surface) || !registryIDPattern.MatchString(anchor.RecordID) ||
			!digestPattern.MatchString(anchor.ConfigurationDigest) || anchor.Epoch == 0 ||
			!digestPattern.MatchString(anchor.EvidenceDigest) {
			return ErrInvalidReconstruction
		}
		record, exists := records[anchor.RecordID]
		if !exists || record.Lane != anchor.Lane || record.ConfigurationDigest != anchor.ConfigurationDigest || record.Epoch != anchor.Epoch {
			return ErrInvalidReconstruction
		}
		key := anchor.Surface + "\x00" + anchor.RecordID
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidReconstruction
		}
		seen[key] = struct{}{}
	}
	for _, record := range snapshot.Records {
		if record.State != StateActive {
			continue
		}
		for _, surface := range recoverySurfaces {
			if _, exists := seen[surface+"\x00"+record.ID]; !exists {
				return ErrInvalidReconstruction
			}
		}
	}
	return nil
}

func isRecoverySurface(surface string) bool {
	for _, allowed := range recoverySurfaces {
		if surface == allowed {
			return true
		}
	}
	return false
}

func validateRecoveryOperations(proposal RegistryReconstructionProposal) error {
	facts := make(map[string]RecoveryOperationFact, len(proposal.InFlight))
	for _, fact := range proposal.InFlight {
		if !operationIDPattern.MatchString(fact.ID) || !digestPattern.MatchString(fact.EvidenceDigest) {
			return ErrInvalidReconstruction
		}
		if _, duplicate := facts[fact.ID]; duplicate {
			return ErrInvalidReconstruction
		}
		switch fact.Kind {
		case RecoveryDevQASOperation:
			if (fact.Lane != config.ReleaseLaneDev && fact.Lane != config.ReleaseLaneQAS) || fact.PRDPhase != "" ||
				fact.FenceMarkerDigest != "" || fact.PairedSnapshotDigest != "" ||
				fact.ServiceAnchorDigest != "" || fact.CommitEvidenceDigest != "" {
				return ErrInvalidReconstruction
			}
		case RecoveryPRDOperation:
			if fact.Lane != config.ReleaseLanePRD ||
				(fact.PRDPhase != RecoveryPRDPreCommitFenced && fact.PRDPhase != RecoveryPRDPostCommit && fact.PRDPhase != RecoveryPRDAmbiguous) ||
				!digestPattern.MatchString(fact.ServiceAnchorDigest) || !digestPattern.MatchString(fact.CommitEvidenceDigest) ||
				((fact.FenceMarkerDigest == "") != (fact.PairedSnapshotDigest == "")) ||
				(fact.FenceMarkerDigest != "" && (!digestPattern.MatchString(fact.FenceMarkerDigest) || !digestPattern.MatchString(fact.PairedSnapshotDigest))) {
				return ErrInvalidReconstruction
			}
			if fact.PRDPhase == RecoveryPRDPreCommitFenced &&
				(!digestPattern.MatchString(fact.FenceMarkerDigest) || !digestPattern.MatchString(fact.PairedSnapshotDigest)) {
				return ErrInvalidReconstruction
			}
		default:
			return ErrInvalidReconstruction
		}
		facts[fact.ID] = fact
	}
	dispositions := make(map[string]RecoveryOperationDisposition, len(proposal.Dispositions))
	for _, disposition := range proposal.Dispositions {
		if !operationIDPattern.MatchString(disposition.OperationID) ||
			!digestPattern.MatchString(disposition.OperationEvidenceDigest) ||
			!digestPattern.MatchString(disposition.ClosedEvidenceDigest) {
			return ErrInvalidReconstruction
		}
		fact, exists := facts[disposition.OperationID]
		if !exists || fact.Lane != disposition.Lane || fact.EvidenceDigest != disposition.OperationEvidenceDigest {
			return ErrInvalidReconstruction
		}
		if _, duplicate := dispositions[disposition.OperationID]; duplicate || !validRecoveryDisposition(fact, disposition) {
			return ErrInvalidReconstruction
		}
		dispositions[disposition.OperationID] = disposition
	}
	if len(dispositions) != len(facts) {
		return ErrInvalidReconstruction
	}
	operations := make(map[string]LaneOperation, len(proposal.Snapshot.Operations))
	for _, operation := range proposal.Snapshot.Operations {
		operations[operation.ID] = operation
		if operation.State == OperationArchived {
			continue
		}
		fact, exists := facts[operation.ID]
		if !exists || fact.Kind != RecoveryDevQASOperation || fact.Lane != operation.Lane ||
			fact.EvidenceDigest != laneOperationEvidenceDigest(operation) {
			return ErrInvalidReconstruction
		}
	}
	for _, fact := range proposal.InFlight {
		if fact.Kind == RecoveryDevQASOperation {
			operation, exists := operations[fact.ID]
			if !exists || operation.State == OperationArchived || fact.EvidenceDigest != laneOperationEvidenceDigest(operation) {
				return ErrInvalidReconstruction
			}
		}
	}
	for _, record := range proposal.Snapshot.Records {
		if record.State != StatePrepared {
			continue
		}
		referenced := false
		for _, operation := range proposal.Snapshot.Operations {
			if operation.State == OperationArchived {
				continue
			}
			if operationReferencesRecord(operation, record.ID) {
				referenced = true
				break
			}
		}
		if !referenced {
			return ErrInvalidReconstruction
		}
	}
	return nil
}

func validRecoveryDisposition(fact RecoveryOperationFact, disposition RecoveryOperationDisposition) bool {
	noPRDEvidence := disposition.FenceMarkerDigest == "" && disposition.PairedSnapshotDigest == "" &&
		disposition.ServiceAnchorDigest == "" && disposition.CommitEvidenceDigest == ""
	switch fact.Kind {
	case RecoveryDevQASOperation:
		return noPRDEvidence && (disposition.Kind == RecoveryRemainClosed || disposition.Kind == RecoveryRequireFreshSuccessor)
	case RecoveryPRDOperation:
		switch disposition.Kind {
		case RecoveryRemainClosed:
			return noPRDEvidence
		case RecoveryPRDV2FenceAwareAbort:
			return fact.PRDPhase == RecoveryPRDPreCommitFenced &&
				disposition.FenceMarkerDigest == fact.FenceMarkerDigest &&
				disposition.PairedSnapshotDigest == fact.PairedSnapshotDigest &&
				disposition.ServiceAnchorDigest == fact.ServiceAnchorDigest &&
				disposition.CommitEvidenceDigest == fact.CommitEvidenceDigest
		case RecoveryPRDV3ForwardRepair:
			return fact.PRDPhase == RecoveryPRDPostCommit && digestPattern.MatchString(disposition.CommitEvidenceDigest) &&
				disposition.CommitEvidenceDigest == fact.CommitEvidenceDigest &&
				disposition.ServiceAnchorDigest == fact.ServiceAnchorDigest &&
				disposition.FenceMarkerDigest == "" && disposition.PairedSnapshotDigest == ""
		default:
			return false
		}
	default:
		return false
	}
}

func operationReferencesRecord(operation LaneOperation, recordID string) bool {
	for _, candidate := range operation.Candidates {
		if candidate.RecordID == recordID {
			return true
		}
	}
	for _, incumbent := range operation.Incumbents {
		if incumbent.RecordID == recordID {
			return true
		}
	}
	if operation.Recovery != nil {
		for _, candidate := range operation.Recovery.Candidates {
			if candidate.RecordID == recordID {
				return true
			}
		}
	}
	return false
}

func laneOperationEvidenceDigest(operation LaneOperation) string {
	data, err := json.Marshal(operation)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// IsRegistryRecoveryStop reports whether an Open error requires the A09 stop
// path. The caller must still preserve bytes and independently diagnose the
// condition; no automatic recovery is attempted here.
func IsRegistryRecoveryStop(err error) bool {
	return errors.Is(err, ErrInvalidRegistry) || errors.Is(err, ErrRegistryRecoveryRequired)
}
