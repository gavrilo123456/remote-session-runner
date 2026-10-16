package laneregistry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneroot"
)

var (
	ErrOperationBusy    = errors.New("lane already has an unarchived operation")
	ErrInvalidOperation = errors.New("invalid lane operation")
	operationIDPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	commitIDPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type LaneOperationMode string

const (
	OperationFresh       LaneOperationMode = "fresh"
	OperationReplacement LaneOperationMode = "replacement"
)

type LaneOperationState string

const (
	OperationPrepare                 LaneOperationState = "prepare"
	OperationCandidateClosed         LaneOperationState = "candidate_closed"
	OperationReopenPrepared          LaneOperationState = "reopen_prepared"
	OperationReopenAuthorized        LaneOperationState = "reopen_authorized"
	OperationTargetOpening           LaneOperationState = "target_opening"
	OperationReopened                LaneOperationState = "reopened"
	OperationAcceptancePending       LaneOperationState = "acceptance_pending"
	OperationAccepted                LaneOperationState = "accepted"
	OperationArchived                LaneOperationState = "archived"
	OperationIncumbentClosurePending LaneOperationState = "incumbent_closure_pending"
	OperationIncumbentQuiescing      LaneOperationState = "incumbent_quiescing"
	OperationIncumbentQuiesced       LaneOperationState = "incumbent_quiesced"
	OperationTargetClosurePending    LaneOperationState = "target_closure_pending"
	OperationTargetQuiescing         LaneOperationState = "target_quiescing"
	OperationFailedClosed            LaneOperationState = "failed_closed"
	OperationRecoveryCandidateClosed LaneOperationState = "recovery_candidate_closed"
	OperationRecoveryAborted         LaneOperationState = "recovery_aborted"
	OperationAborted                 LaneOperationState = "aborted"
)

const (
	SurfaceMac            = "mac-control-plane"
	SurfaceLinuxHost      = "linux-host"
	SurfaceSandboxHost    = "sandbox-host"
	SurfaceFactorySandbox = "factory-sandbox"
)

var operationSurfaces = []string{SurfaceFactorySandbox, SurfaceLinuxHost, SurfaceMac, SurfaceSandboxHost}
var operationRemoteSurfaces = []string{SurfaceFactorySandbox, SurfaceLinuxHost, SurfaceSandboxHost}

type OperationIdentity struct {
	RecordID            string                   `json:"record_id"`
	Lane                config.ReleaseLane       `json:"release_lane"`
	InboxID             string                   `json:"inbox_id"`
	NormalizedRoot      string                   `json:"normalized_root"`
	RootIdentity        laneroot.FileIdentity    `json:"filesystem_identity"`
	ConfigurationDigest string                   `json:"configuration_digest"`
	Epoch               uint64                   `json:"epoch"`
	Supersedes          string                   `json:"supersedes,omitempty"`
	Volume              *laneroot.VolumeIdentity `json:"volume,omitempty"`
}

type OperationTransition struct {
	Sequence       uint64             `json:"sequence"`
	From           LaneOperationState `json:"from,omitempty"`
	To             LaneOperationState `json:"to"`
	EvidenceDigest string             `json:"evidence_digest,omitempty"`
}

type IncumbentClosureAttestation struct {
	Surface        string `json:"surface"`
	Attempt        uint64 `json:"attempt"`
	DirectiveID    string `json:"directive_id"`
	DirectiveEpoch uint64 `json:"directive_epoch"`
	AttestationID  string `json:"attestation_id"`
	Closed         bool   `json:"closed"`
	EvidenceDigest string `json:"evidence_digest"`
}

type WorkDisposition struct {
	Class          string `json:"class"`
	Observed       uint64 `json:"observed"`
	Outcome        string `json:"outcome"`
	EvidenceDigest string `json:"evidence_digest"`
}

type DeferredWorkEvidence struct {
	Class         string `json:"class"`
	RequestID     string `json:"request_id"`
	RequestDigest string `json:"request_digest"`
	AckID         string `json:"ack_id,omitempty"`
	AckDigest     string `json:"ack_digest,omitempty"`
}

type IncumbentQuiescenceAttestation struct {
	Surface            string                 `json:"surface"`
	Attempt            uint64                 `json:"attempt"`
	InventoryDigest    string                 `json:"inventory_digest"`
	Dispositions       []WorkDisposition      `json:"dispositions"`
	DeferredEvidence   []DeferredWorkEvidence `json:"deferred_evidence"`
	RemainingWork      uint64                 `json:"remaining_work"`
	ZeroWorkPostflight bool                   `json:"zero_work_postflight"`
	PostflightDigest   string                 `json:"postflight_digest"`
}

type CandidateStageAttestation struct {
	Surface            string `json:"surface"`
	AttestationID      string `json:"attestation_id"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	SourceRevision     string `json:"source_revision"`
	ArtifactDigest     string `json:"artifact_digest"`
	ManifestDigest     string `json:"manifest_digest"`
	InstalledClosed    bool   `json:"installed_closed"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type CandidateClosedAttestation struct {
	Surface             string `json:"surface"`
	AttestationID       string `json:"attestation_id"`
	CandidateSetDigest  string `json:"candidate_set_digest"`
	SourceRevision      string `json:"source_revision"`
	ArtifactDigest      string `json:"artifact_digest"`
	ConfigurationDigest string `json:"configuration_digest"`
	ServiceReconciled   bool   `json:"service_reconciled"`
	AdmissionClosed     bool   `json:"admission_closed"`
	EvidenceDigest      string `json:"evidence_digest"`
}

type TransportReadyAttestation struct {
	Surface             string `json:"surface"`
	AttestationID       string `json:"attestation_id"`
	CandidateSetDigest  string `json:"candidate_set_digest"`
	RemoteHealthyClosed bool   `json:"remote_healthy_closed"`
	EvidenceDigest      string `json:"evidence_digest"`
}

type ReopenAuthorization struct {
	Surface            string `json:"surface"`
	AuthorizationID    string `json:"authorization_id"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	CandidateEpoch     uint64 `json:"candidate_epoch"`
	Fresh              bool   `json:"fresh"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type OpenAttestation struct {
	Surface            string `json:"surface"`
	DirectiveID        string `json:"directive_id"`
	DirectiveEpoch     uint64 `json:"directive_epoch"`
	HighWaterSequence  uint64 `json:"high_water_sequence"`
	AttestationID      string `json:"attestation_id"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	AdmissionOpen      bool   `json:"admission_open"`
	AggregateIngress   bool   `json:"aggregate_ingress,omitempty"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type LaneAcceptanceEvidence struct {
	AcceptanceID       string             `json:"acceptance_id"`
	EvidenceDigest     string             `json:"evidence_digest"`
	TransitionID       string             `json:"transition_id"`
	Lane               config.ReleaseLane `json:"release_lane"`
	CandidateSetDigest string             `json:"candidate_set_digest"`
	SourceRevision     string             `json:"source_revision"`
	LiveSuitePassed    bool               `json:"live_suite_passed"`
	ZeroWorkPostflight bool               `json:"zero_work_postflight"`
	AuthorityVerified  bool               `json:"authority_verified"`
}

type DirectiveHighWater struct {
	Surface         string `json:"surface"`
	DirectiveID     string `json:"directive_id"`
	DirectiveEpoch  uint64 `json:"directive_epoch"`
	HighestSequence uint64 `json:"highest_sequence"`
}

type TerminalSurfaceProof struct {
	Surface            string                         `json:"surface"`
	CandidateSetDigest string                         `json:"candidate_set_digest"`
	Closed             bool                           `json:"closed"`
	Quiescence         IncumbentQuiescenceAttestation `json:"quiescence"`
	EvidenceDigest     string                         `json:"evidence_digest"`
}

type OperationFailureEvidence struct {
	FailureID          string                 `json:"failure_id"`
	FailedFrom         LaneOperationState     `json:"failed_from"`
	CandidateSetDigest string                 `json:"candidate_set_digest"`
	FailureDigest      string                 `json:"failure_digest"`
	EvidenceDigest     string                 `json:"evidence_digest"`
	KnownDirectives    []DirectiveHighWater   `json:"known_directives"`
	TerminalProofs     []TerminalSurfaceProof `json:"terminal_proofs,omitempty"`
}

type TargetClosureAttestation struct {
	Surface            string `json:"surface"`
	Attempt            uint64 `json:"attempt"`
	DirectiveID        string `json:"directive_id"`
	DirectiveEpoch     uint64 `json:"directive_epoch"`
	HighestSequence    uint64 `json:"highest_sequence"`
	AttestationID      string `json:"attestation_id"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	Closed             bool   `json:"closed"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type TargetQuiescenceAttestation struct {
	CandidateSetDigest string                         `json:"candidate_set_digest"`
	Quiescence         IncumbentQuiescenceAttestation `json:"quiescence"`
}

type FailureDispositionKind string

const (
	DispositionAbort            FailureDispositionKind = "abort"
	DispositionRecoverIncumbent FailureDispositionKind = "recover_incumbent"
)

type FailureDisposition struct {
	Kind           FailureDispositionKind `json:"kind"`
	EvidenceDigest string                 `json:"evidence_digest"`
}

type RecoveryAttempt struct {
	ID                 string                       `json:"id"`
	Candidates         []OperationIdentity          `json:"candidates"`
	CandidateSetDigest string                       `json:"candidate_set_digest"`
	SourceRevision     string                       `json:"source_revision"`
	ArtifactDigest     string                       `json:"artifact_digest"`
	ManifestDigest     string                       `json:"manifest_digest"`
	ProvenanceDigest   string                       `json:"provenance_digest"`
	CandidateStages    []CandidateStageAttestation  `json:"candidate_stages,omitempty"`
	CandidateClosed    []CandidateClosedAttestation `json:"candidate_closed,omitempty"`
	TransportReady     []TransportReadyAttestation  `json:"transport_ready,omitempty"`
	Authorizations     []ReopenAuthorization        `json:"authorizations,omitempty"`
	OpenAttestations   []OpenAttestation            `json:"open_attestations,omitempty"`
	Acceptance         *LaneAcceptanceEvidence      `json:"acceptance,omitempty"`
	Failure            *OperationFailureEvidence    `json:"failure,omitempty"`
}

type LaneOperation struct {
	ID                  string                           `json:"id"`
	Lane                config.ReleaseLane               `json:"release_lane"`
	Mode                LaneOperationMode                `json:"mode"`
	State               LaneOperationState               `json:"state"`
	Candidates          []OperationIdentity              `json:"candidates"`
	Incumbents          []OperationIdentity              `json:"incumbents,omitempty"`
	CandidateSetDigest  string                           `json:"candidate_set_digest"`
	SourceRevision      string                           `json:"source_revision"`
	ArtifactDigest      string                           `json:"artifact_digest"`
	ManifestDigest      string                           `json:"manifest_digest"`
	RequiredSurfaces    []string                         `json:"required_surfaces"`
	IncumbentClosures   []IncumbentClosureAttestation    `json:"incumbent_closures,omitempty"`
	IncumbentQuiescence []IncumbentQuiescenceAttestation `json:"incumbent_quiescence,omitempty"`
	CandidateStages     []CandidateStageAttestation      `json:"candidate_stages,omitempty"`
	CandidateClosed     []CandidateClosedAttestation     `json:"candidate_closed,omitempty"`
	TransportReady      []TransportReadyAttestation      `json:"transport_ready,omitempty"`
	Authorizations      []ReopenAuthorization            `json:"authorizations,omitempty"`
	OpenAttestations    []OpenAttestation                `json:"open_attestations,omitempty"`
	Acceptance          *LaneAcceptanceEvidence          `json:"acceptance,omitempty"`
	Failure             *OperationFailureEvidence        `json:"failure,omitempty"`
	FailureDisposition  *FailureDisposition              `json:"failure_disposition,omitempty"`
	TargetClosures      []TargetClosureAttestation       `json:"target_closures,omitempty"`
	TargetQuiescence    []TargetQuiescenceAttestation    `json:"target_quiescence,omitempty"`
	Recovery            *RecoveryAttempt                 `json:"recovery,omitempty"`
	AbortProofs         []TerminalSurfaceProof           `json:"abort_proofs,omitempty"`
	History             []OperationTransition            `json:"history"`
}

type LaneOperationSpec struct {
	Lane               config.ReleaseLane
	Mode               LaneOperationMode
	CandidateRecordIDs []string
	IncumbentRecordIDs []string
	SourceRevision     string
	ArtifactDigest     string
	ManifestDigest     string
}

// NewLaneTransitionID returns a random RFC 9562 UUIDv7 operation identifier.
func NewLaneTransitionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrInvalidOperation
	}
	unixMilliseconds := uint64(time.Now().UnixMilli())
	for index := 5; index >= 0; index-- {
		raw[index] = byte(unixMilliseconds)
		unixMilliseconds >>= 8
	}
	raw[6] = raw[6]&0x0f | 0x70
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

// BeginLaneOperation allocates the one durable Dev/QAS transition for a lane.
// Candidates must already be prepared root claims. Replacement allocates only
// when it names the complete active incumbent set and exact higher-epoch
// successors for that same set.
func (r *Registry) BeginLaneOperation(expectedGeneration uint64, spec LaneOperationSpec) (Snapshot, LaneOperation, error) {
	operationID, err := NewLaneTransitionID()
	if err != nil {
		return Snapshot{}, LaneOperation{}, err
	}
	var created LaneOperation
	snapshot, err := r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		if spec.Lane != config.ReleaseLaneDev && spec.Lane != config.ReleaseLaneQAS {
			return false, ErrInvalidOperation
		}
		if spec.Mode != OperationFresh && spec.Mode != OperationReplacement {
			return false, ErrInvalidOperation
		}
		if hasUnarchivedOperation(snapshot, spec.Lane) {
			return false, ErrOperationBusy
		}
		if !commitIDPattern.MatchString(spec.SourceRevision) || !digestPattern.MatchString(spec.ArtifactDigest) || !digestPattern.MatchString(spec.ManifestDigest) {
			return false, ErrInvalidOperation
		}
		candidateIDs := append([]string(nil), spec.CandidateRecordIDs...)
		incumbentIDs := append([]string(nil), spec.IncumbentRecordIDs...)
		if len(candidateIDs) == 0 || len(candidateIDs) > 32 {
			return false, ErrInvalidOperation
		}
		candidates, err := operationIdentities(snapshot.Records, candidateIDs, spec.Lane, StatePrepared)
		if err != nil {
			return false, err
		}
		allPrepared := laneRecordIDs(snapshot.Records, spec.Lane, StatePrepared)
		if !sameStringSet(candidateIDs, allPrepared) {
			return false, ErrInvalidOperation
		}
		var incumbents []OperationIdentity
		activeIDs := laneRecordIDs(snapshot.Records, spec.Lane, StateActive)
		switch spec.Mode {
		case OperationFresh:
			if len(incumbentIDs) != 0 || len(activeIDs) != 0 {
				return false, ErrInvalidOperation
			}
			for _, candidate := range candidates {
				if candidate.Supersedes != "" {
					return false, ErrInvalidOperation
				}
			}
		case OperationReplacement:
			if len(incumbentIDs) == 0 || !sameStringSet(incumbentIDs, activeIDs) {
				return false, ErrInvalidOperation
			}
			incumbents, err = operationIdentities(snapshot.Records, incumbentIDs, spec.Lane, StateActive)
			if err != nil || !matchingSuccessorSets(candidates, incumbents) {
				return false, ErrInvalidOperation
			}
		}
		candidateDigest, err := digestCandidateSet(candidates)
		if err != nil {
			return false, ErrInvalidOperation
		}
		created = LaneOperation{
			ID: operationID, Lane: spec.Lane, Mode: spec.Mode, State: OperationPrepare,
			Candidates: candidates, Incumbents: incumbents, CandidateSetDigest: candidateDigest,
			SourceRevision: spec.SourceRevision, ArtifactDigest: spec.ArtifactDigest,
			ManifestDigest: spec.ManifestDigest, RequiredSurfaces: append([]string(nil), operationSurfaces...),
			History: []OperationTransition{{Sequence: 1, To: OperationPrepare}},
		}
		snapshot.Operations = append(snapshot.Operations, created)
		return true, nil
	})
	if err != nil {
		return Snapshot{}, LaneOperation{}, err
	}
	stored, ok := laneOperationByID(snapshot.Operations, operationID)
	if !ok {
		return Snapshot{}, LaneOperation{}, ErrInvalidOperation
	}
	return snapshot, cloneLaneOperation(stored), nil
}

func (r *Registry) BeginIncumbentClosure(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationIncumbentClosurePending, "", func(operation *LaneOperation) error {
		if operation.Mode != OperationReplacement || operation.State != OperationPrepare {
			return ErrInvalidTransition
		}
		return nil
	})
}

// RecordIncumbentClosure appends each closure attempt so retries and renewals
// remain durable. A failed or partial attempt cannot advance the graph.
func (r *Registry) RecordIncumbentClosure(expectedGeneration uint64, operationID string, attestation IncumbentClosureAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationIncumbentClosurePending || operation.Mode != OperationReplacement || !validClosureAttestation(*operation, attestation) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.IncumbentClosures {
			if prior.Surface == attestation.Surface && prior.Attempt >= attestation.Attempt {
				return false, ErrInvalidTransition
			}
			if prior.AttestationID == attestation.AttestationID || prior.DirectiveID == attestation.DirectiveID {
				return false, ErrCollision
			}
		}
		operation.IncumbentClosures = append(operation.IncumbentClosures, attestation)
		sort.Slice(operation.IncumbentClosures, func(i, j int) bool {
			if operation.IncumbentClosures[i].Surface == operation.IncumbentClosures[j].Surface {
				return operation.IncumbentClosures[i].Attempt < operation.IncumbentClosures[j].Attempt
			}
			return operation.IncumbentClosures[i].Surface < operation.IncumbentClosures[j].Surface
		})
		return true, nil
	})
}

func (r *Registry) CompleteIncumbentClosure(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationIncumbentQuiescing, "", func(operation *LaneOperation) error {
		if operation.State != OperationIncumbentClosurePending || !completeIncumbentClosures(*operation) {
			return ErrInvalidTransition
		}
		return nil
	})
}

// RecordIncumbentQuiescence retains the complete inventory, per-class terminal
// disposition, deferred request/ACK evidence, and postflight for each surface.
// A failed zero-work check stays in incumbent_quiescing for a higher retry.
func (r *Registry) RecordIncumbentQuiescence(expectedGeneration uint64, operationID string, attestation IncumbentQuiescenceAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationIncumbentQuiescing || !validQuiescenceAttestation(attestation) || !contains(operation.RequiredSurfaces, attestation.Surface) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.IncumbentQuiescence {
			if prior.Surface == attestation.Surface && prior.Attempt >= attestation.Attempt {
				return false, ErrInvalidTransition
			}
		}
		operation.IncumbentQuiescence = append(operation.IncumbentQuiescence, attestation)
		sort.Slice(operation.IncumbentQuiescence, func(i, j int) bool {
			if operation.IncumbentQuiescence[i].Surface == operation.IncumbentQuiescence[j].Surface {
				return operation.IncumbentQuiescence[i].Attempt < operation.IncumbentQuiescence[j].Attempt
			}
			return operation.IncumbentQuiescence[i].Surface < operation.IncumbentQuiescence[j].Surface
		})
		return true, nil
	})
}

func (r *Registry) CompleteIncumbentQuiescence(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationIncumbentQuiesced, "", func(operation *LaneOperation) error {
		if operation.State != OperationIncumbentQuiescing || !completeIncumbentQuiescence(*operation) {
			return ErrInvalidTransition
		}
		return nil
	})
}

func (r *Registry) RecordCandidateStage(expectedGeneration uint64, operationID string, attestation CandidateStageAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		stageState := operation.State == OperationPrepare && operation.Mode == OperationFresh ||
			operation.State == OperationIncumbentQuiesced && operation.Mode == OperationReplacement
		if !stageState || !validCandidateStage(*operation, attestation) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.CandidateStages {
			if prior.Surface == attestation.Surface {
				return false, ErrCollision
			}
		}
		operation.CandidateStages = append(operation.CandidateStages, attestation)
		sort.Slice(operation.CandidateStages, func(i, j int) bool {
			return operation.CandidateStages[i].Surface < operation.CandidateStages[j].Surface
		})
		return true, nil
	})
}

func (r *Registry) RecordCandidateClosed(expectedGeneration uint64, operationID string, attestation CandidateClosedAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		closedState := operation.State == OperationPrepare && operation.Mode == OperationFresh ||
			operation.State == OperationIncumbentQuiesced && operation.Mode == OperationReplacement
		if !closedState || !completeCandidateStages(*operation) || !validCandidateClosed(*operation, attestation) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.CandidateClosed {
			if prior.Surface == attestation.Surface {
				return false, ErrCollision
			}
		}
		operation.CandidateClosed = append(operation.CandidateClosed, attestation)
		sort.Slice(operation.CandidateClosed, func(i, j int) bool {
			return operation.CandidateClosed[i].Surface < operation.CandidateClosed[j].Surface
		})
		return true, nil
	})
}

// CompleteCandidateClosed flips all candidate root records active and their
// exact incumbents superseded in the same CAS that records candidate_closed.
func (r *Registry) CompleteCandidateClosed(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		closedState := operation.State == OperationPrepare && operation.Mode == OperationFresh ||
			operation.State == OperationIncumbentQuiesced && operation.Mode == OperationReplacement
		if !closedState || !completeCandidateStages(*operation) || !completeCandidateClosed(*operation) {
			return false, ErrInvalidTransition
		}
		if operation.Mode == OperationReplacement {
			for _, incumbent := range operation.Incumbents {
				index := recordIndex(snapshot.Records, incumbent.RecordID)
				if index < 0 || snapshot.Records[index].State != StateActive {
					return false, ErrInvalidTransition
				}
				snapshot.Records[index].State = StateSuperseded
			}
		}
		for _, candidate := range operation.Candidates {
			index := recordIndex(snapshot.Records, candidate.RecordID)
			if index < 0 || snapshot.Records[index].State != StatePrepared {
				return false, ErrInvalidTransition
			}
			snapshot.Records[index].State = StateActive
		}
		if err := appendOperationTransition(operation, OperationCandidateClosed, evidenceSetDigest(operation.CandidateClosed)); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (r *Registry) PrepareReopen(expectedGeneration uint64, operationID string, attestations []TransportReadyAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		recovery := recoveryPath(*operation)
		target := operationTargetContext(*operation)
		validState := operation.State == OperationCandidateClosed && !recovery ||
			operation.State == OperationRecoveryCandidateClosed && recovery
		if !validState || !validTransportReadySet(target, attestations) {
			return false, ErrInvalidTransition
		}
		stored := append([]TransportReadyAttestation(nil), attestations...)
		sort.Slice(stored, func(i, j int) bool { return stored[i].Surface < stored[j].Surface })
		if recovery {
			operation.Recovery.TransportReady = stored
		} else {
			operation.TransportReady = stored
		}
		if err := appendOperationTransition(operation, OperationReopenPrepared, evidenceSetDigest(stored)); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (r *Registry) AuthorizeReopen(expectedGeneration uint64, operationID string, authorizations []ReopenAuthorization) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationReopenPrepared {
			return false, ErrInvalidTransition
		}
		recovery := recoveryPath(*operation)
		target := operationTargetContext(*operation)
		transport := operation.TransportReady
		if recovery {
			transport = operation.Recovery.TransportReady
		}
		if !validTransportReadySet(target, transport) || !validAuthorizationSet(target, authorizations) {
			return false, ErrInvalidTransition
		}
		stored := append([]ReopenAuthorization(nil), authorizations...)
		sort.Slice(stored, func(i, j int) bool { return stored[i].Surface < stored[j].Surface })
		if recovery {
			operation.Recovery.Authorizations = stored
		} else {
			operation.Authorizations = stored
		}
		if err := appendOperationTransition(operation, OperationReopenAuthorized, evidenceSetDigest(stored)); err != nil {
			return false, err
		}
		return true, nil
	})
}

// BeginTargetOpening persists target_opening before any open directive may be
// recorded or sent by a caller.
func (r *Registry) BeginTargetOpening(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationTargetOpening, "", func(operation *LaneOperation) error {
		if operation.State != OperationReopenAuthorized {
			return ErrInvalidTransition
		}
		return nil
	})
}

func (r *Registry) RecordRemoteOpen(expectedGeneration uint64, operationID string, attestation OpenAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		target := operationTargetContext(*operation)
		if operation.State != OperationTargetOpening || !isRemoteSurface(attestation.Surface) ||
			!validAuthorizationSet(target, target.Authorizations) || !validOpenAttestation(target, attestation, false) {
			return false, ErrInvalidTransition
		}
		for _, prior := range target.OpenAttestations {
			if prior.Surface == attestation.Surface || prior.DirectiveID == attestation.DirectiveID || prior.AttestationID == attestation.AttestationID {
				return false, ErrCollision
			}
		}
		if recoveryPath(*operation) {
			operation.Recovery.OpenAttestations = append(operation.Recovery.OpenAttestations, attestation)
		} else {
			operation.OpenAttestations = append(operation.OpenAttestations, attestation)
		}
		return true, nil
	})
}

func (r *Registry) RecordMacOpen(expectedGeneration uint64, operationID string, attestation OpenAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		target := operationTargetContext(*operation)
		if operation.State != OperationTargetOpening || !validAuthorizationSet(target, target.Authorizations) ||
			!validOpenAttestation(target, attestation, true) || !completeRemoteOpens(target) {
			return false, ErrInvalidTransition
		}
		for _, prior := range target.OpenAttestations {
			if prior.Surface == attestation.Surface || prior.DirectiveID == attestation.DirectiveID || prior.AttestationID == attestation.AttestationID {
				return false, ErrCollision
			}
		}
		if recoveryPath(*operation) {
			operation.Recovery.OpenAttestations = append(operation.Recovery.OpenAttestations, attestation)
		} else {
			operation.OpenAttestations = append(operation.OpenAttestations, attestation)
		}
		if err := appendOperationTransition(operation, OperationReopened, evidenceSetDigest(targetOpenAttestations(*operation))); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (r *Registry) BeginAcceptance(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationAcceptancePending, "", func(operation *LaneOperation) error {
		target := operationTargetContext(*operation)
		if operation.State != OperationReopened || !completeRemoteOpens(target) || !hasMacOpen(target) {
			return ErrInvalidTransition
		}
		return nil
	})
}

// RecordLaneAcceptance records the authority-verified, transition-bound result
// only after the live suite and its zero-work postflight passed.
func (r *Registry) RecordLaneAcceptance(expectedGeneration uint64, operationID string, acceptance LaneAcceptanceEvidence) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		target := operationTargetContext(*operation)
		if operation.State != OperationAcceptancePending || !validLaneAcceptance(target, acceptance) {
			return false, ErrInvalidTransition
		}
		copy := acceptance
		if recoveryPath(*operation) {
			operation.Recovery.Acceptance = &copy
		} else {
			operation.Acceptance = &copy
		}
		if err := appendOperationTransition(operation, OperationAccepted, acceptance.EvidenceDigest); err != nil {
			return false, err
		}
		if recoveryPath(*operation) {
			if err := activateRecoveryCandidates(snapshot, operation); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

func activateRecoveryCandidates(snapshot *Snapshot, operation *LaneOperation) error {
	if snapshot == nil || operation == nil || operation.Recovery == nil {
		return ErrInvalidTransition
	}
	for _, candidate := range operation.Candidates {
		index := recordIndex(snapshot.Records, candidate.RecordID)
		if index < 0 || snapshot.Records[index].State != StateActive {
			return ErrInvalidTransition
		}
	}
	for _, candidate := range operation.Recovery.Candidates {
		index := recordIndex(snapshot.Records, candidate.RecordID)
		if index < 0 || snapshot.Records[index].State != StatePrepared {
			return ErrInvalidTransition
		}
	}
	for _, candidate := range operation.Candidates {
		index := recordIndex(snapshot.Records, candidate.RecordID)
		snapshot.Records[index].State = StateSuperseded
	}
	for _, candidate := range operation.Recovery.Candidates {
		index := recordIndex(snapshot.Records, candidate.RecordID)
		snapshot.Records[index].State = StateActive
	}
	return nil
}

func (r *Registry) ArchiveLaneOperation(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationArchived, "", func(operation *LaneOperation) error {
		if operation.State == OperationAccepted {
			target := operationTargetContext(*operation)
			if target.Acceptance != nil && target.Acceptance.AuthorityVerified && validOperationArchiveProof(*operation) {
				return nil
			}
		}
		if operation.State == OperationAborted && validOperationAbortProof(*operation) {
			return nil
		}
		if operation.State == OperationRecoveryAborted && validRecoveryAbortProof(*operation) {
			return nil
		}
		return ErrInvalidTransition
	})
}

// RecordOperationFailure records an exact failure and its directive high-water
// inventory. Failures before target_opening need a complete closed/zero-work
// proof immediately; failures after target_opening enter target closure and
// quiescence without changing any mailbox item.
func (r *Registry) RecordOperationFailure(expectedGeneration uint64, operationID string, failure OperationFailureEvidence) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if !validFailureEvidence(*operation, failure) {
			return false, ErrInvalidTransition
		}
		target := operationTargetContext(*operation)
		preOpen := isPreOpenFailureState(operation.State, recoveryPath(*operation), operation.Mode)
		if preOpen {
			if !validTerminalSurfaceProofs(target.CandidateSetDigest, failure.TerminalProofs, operation.RequiredSurfaces) {
				return false, ErrInvalidTransition
			}
		} else if len(failure.TerminalProofs) != 0 {
			return false, ErrInvalidTransition
		}
		if recoveryPath(*operation) {
			if operation.Recovery.Failure != nil {
				return false, ErrInvalidTransition
			}
			copy := cloneFailureEvidence(failure)
			operation.Recovery.Failure = &copy
		} else {
			if operation.Failure != nil {
				return false, ErrInvalidTransition
			}
			copy := cloneFailureEvidence(failure)
			operation.Failure = &copy
		}
		if preOpen {
			next := OperationFailedClosed
			if recoveryPath(*operation) {
				next = OperationRecoveryAborted
			}
			if err := appendOperationTransition(operation, next, failure.FailureDigest); err != nil {
				return false, err
			}
			if next == OperationRecoveryAborted {
				if err := retireRecoveryCandidates(snapshot, operation); err != nil {
					return false, err
				}
			}
			return true, nil
		}
		return true, appendOperationTransition(operation, OperationTargetClosurePending, failure.FailureDigest)
	})
}

// RecordTargetClosure appends each per-surface closure attempt for the current
// candidate context. A failed or partial set remains pending and can only retry.
func (r *Registry) RecordTargetClosure(expectedGeneration uint64, operationID string, attestation TargetClosureAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		target := operationTargetContext(*operation)
		if operation.State != OperationTargetClosurePending || !validTargetClosure(target, attestation) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.TargetClosures {
			if prior.CandidateSetDigest == attestation.CandidateSetDigest && prior.Surface == attestation.Surface {
				if prior.Attempt >= attestation.Attempt || prior.DirectiveEpoch >= attestation.DirectiveEpoch {
					return false, ErrInvalidTransition
				}
			}
			if prior.DirectiveID == attestation.DirectiveID || prior.AttestationID == attestation.AttestationID {
				return false, ErrCollision
			}
		}
		operation.TargetClosures = append(operation.TargetClosures, attestation)
		sortTargetClosures(operation.TargetClosures)
		return true, nil
	})
}

func (r *Registry) CompleteTargetClosure(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.transitionOperation(expectedGeneration, operationID, OperationTargetQuiescing, "", func(operation *LaneOperation) error {
		target := operationTargetContext(*operation)
		if operation.State != OperationTargetClosurePending || !completeTargetClosures(*operation, target.CandidateSetDigest) {
			return ErrInvalidTransition
		}
		return nil
	})
}

func (r *Registry) RecordTargetQuiescence(expectedGeneration uint64, operationID string, attestation TargetQuiescenceAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		target := operationTargetContext(*operation)
		if operation.State != OperationTargetQuiescing || attestation.CandidateSetDigest != target.CandidateSetDigest ||
			!validQuiescenceAttestation(attestation.Quiescence) || !contains(operation.RequiredSurfaces, attestation.Quiescence.Surface) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.TargetQuiescence {
			if prior.CandidateSetDigest == attestation.CandidateSetDigest && prior.Quiescence.Surface == attestation.Quiescence.Surface &&
				prior.Quiescence.Attempt >= attestation.Quiescence.Attempt {
				return false, ErrInvalidTransition
			}
		}
		operation.TargetQuiescence = append(operation.TargetQuiescence, attestation)
		sortTargetQuiescence(operation.TargetQuiescence)
		return true, nil
	})
}

func (r *Registry) CompleteTargetQuiescence(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		target := operationTargetContext(*operation)
		if operation.State != OperationTargetQuiescing || !completeTargetQuiescence(*operation, target.CandidateSetDigest, operation.RequiredSurfaces) {
			return false, ErrInvalidTransition
		}
		next := OperationFailedClosed
		if recoveryPath(*operation) {
			next = OperationRecoveryAborted
		}
		if err := appendOperationTransition(operation, next, targetQuiescenceDigest(*operation, target.CandidateSetDigest)); err != nil {
			return false, err
		}
		if next == OperationRecoveryAborted {
			if err := retireRecoveryCandidates(snapshot, operation); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

func retireRecoveryCandidates(snapshot *Snapshot, operation *LaneOperation) error {
	if snapshot == nil || operation == nil || operation.Recovery == nil {
		return ErrInvalidTransition
	}
	for _, candidate := range operation.Recovery.Candidates {
		index := recordIndex(snapshot.Records, candidate.RecordID)
		if index < 0 || (snapshot.Records[index].State != StatePrepared && snapshot.Records[index].State != StateActive) {
			return ErrInvalidTransition
		}
	}
	for _, candidate := range operation.Recovery.Candidates {
		index := recordIndex(snapshot.Records, candidate.RecordID)
		snapshot.Records[index].State = StateRetired
	}
	return nil
}

// AbortPreparedReplacement is the only direct abort: before incumbent closure
// starts no runtime context was changed. Its complete closed/zero-work proofs
// still remain in the registry before this operation can be archived.
func (r *Registry) AbortPreparedReplacement(expectedGeneration uint64, operationID string, proofs []TerminalSurfaceProof) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.Mode != OperationReplacement || operation.State != OperationPrepare ||
			!validTerminalSurfaceProofs(operation.CandidateSetDigest, proofs, operation.RequiredSurfaces) {
			return false, ErrInvalidTransition
		}
		operation.AbortProofs = cloneTerminalProofs(proofs)
		if err := appendOperationTransition(operation, OperationAborted, evidenceSetDigest(proofs)); err != nil {
			return false, err
		}
		for _, candidate := range operation.Candidates {
			index := recordIndex(snapshot.Records, candidate.RecordID)
			if index < 0 || snapshot.Records[index].State != StatePrepared {
				return false, ErrInvalidTransition
			}
			snapshot.Records[index].State = StateRetired
		}
		return true, nil
	})
}

func (r *Registry) AbortFailedClosed(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationFailedClosed || operation.FailureDisposition != nil || !validInitialFailedClosedProof(*operation) ||
			!digestPattern.MatchString(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		operation.FailureDisposition = &FailureDisposition{Kind: DispositionAbort, EvidenceDigest: evidenceDigest}
		if err := appendOperationTransition(operation, OperationAborted, evidenceDigest); err != nil {
			return false, err
		}
		for _, candidate := range operation.Candidates {
			index := recordIndex(snapshot.Records, candidate.RecordID)
			if index < 0 || (snapshot.Records[index].State != StatePrepared && snapshot.Records[index].State != StateActive) {
				return false, ErrInvalidTransition
			}
			snapshot.Records[index].State = StateRetired
		}
		return true, nil
	})
}

// BeginIncumbentRecovery creates a new, higher-epoch prepared record for the
// exact incumbent configuration and explicitly supersedes the closed target.
// It never reactivates the historical incumbent record.
func (r *Registry) BeginIncumbentRecovery(expectedGeneration uint64, operationID string, records []RootRecord, sourceRevision, artifactDigest, manifestDigest, provenanceDigest string) (Snapshot, LaneOperation, error) {
	recoveryID, err := NewLaneTransitionID()
	if err != nil {
		return Snapshot{}, LaneOperation{}, err
	}
	var created RecoveryAttempt
	snapshot, err := r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationFailedClosed || operation.Mode != OperationReplacement ||
			operation.FailureDisposition != nil || !validInitialFailedClosedProof(*operation) || operation.Recovery != nil ||
			len(records) != len(operation.Incumbents) || len(records) == 0 || len(records) > 32 ||
			!commitIDPattern.MatchString(sourceRevision) || !digestPattern.MatchString(artifactDigest) ||
			!digestPattern.MatchString(manifestDigest) || !digestPattern.MatchString(provenanceDigest) {
			return false, ErrInvalidTransition
		}
		incumbentsByInbox := make(map[string]OperationIdentity, len(operation.Incumbents))
		targetsByInbox := make(map[string]OperationIdentity, len(operation.Candidates))
		for _, incumbent := range operation.Incumbents {
			incumbentsByInbox[incumbent.InboxID] = incumbent
		}
		for _, target := range operation.Candidates {
			targetsByInbox[target.InboxID] = target
		}
		seenInbox := map[string]struct{}{}
		var candidates []OperationIdentity
		for _, record := range records {
			incumbent, exists := incumbentsByInbox[record.InboxID]
			target, hasTarget := targetsByInbox[record.InboxID]
			if !exists || !hasTarget || record.State != StatePrepared || record.ID == target.RecordID ||
				record.Host != config.HostKindMac || record.Lane != operation.Lane || record.Supersedes != target.RecordID ||
				record.ConfigurationDigest != incumbent.ConfigurationDigest || record.NormalizedRoot != incumbent.NormalizedRoot ||
				record.RootIdentity != incumbent.RootIdentity || record.Epoch <= target.Epoch {
				return false, ErrInvalidOperation
			}
			if _, duplicate := seenInbox[record.InboxID]; duplicate {
				return false, ErrInvalidOperation
			}
			seenInbox[record.InboxID] = struct{}{}
			if err := validateRecord(record); err != nil {
				return false, err
			}
			if err := validateSuccessor(snapshot.Records, record, StateActive); err != nil {
				return false, err
			}
			if err := validateEpoch(snapshot.Records, record); err != nil {
				return false, err
			}
			if err := validateNoCollision(snapshot.Records, record); err != nil {
				return false, err
			}
			candidates = append(candidates, operationIdentity(record))
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].InboxID < candidates[j].InboxID })
		candidateDigest, err := digestCandidateSet(candidates)
		if err != nil {
			return false, ErrInvalidOperation
		}
		created = RecoveryAttempt{
			ID: recoveryID, Candidates: candidates, CandidateSetDigest: candidateDigest,
			SourceRevision: sourceRevision, ArtifactDigest: artifactDigest,
			ManifestDigest: manifestDigest, ProvenanceDigest: provenanceDigest,
		}
		for _, record := range records {
			sortAncillary(record.AncillaryEntries)
			snapshot.Records = append(snapshot.Records, record)
		}
		operation.Recovery = &created
		operation.FailureDisposition = &FailureDisposition{Kind: DispositionRecoverIncumbent, EvidenceDigest: provenanceDigest}
		return true, nil
	})
	if err != nil {
		return Snapshot{}, LaneOperation{}, err
	}
	stored, ok := laneOperationByID(snapshot.Operations, operationID)
	if !ok {
		return Snapshot{}, LaneOperation{}, ErrInvalidOperation
	}
	return snapshot, cloneLaneOperation(stored), nil
}

func (r *Registry) RecordRecoveryCandidateStage(expectedGeneration uint64, operationID string, attestation CandidateStageAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationFailedClosed || operation.Recovery == nil ||
			operation.FailureDisposition == nil || operation.FailureDisposition.Kind != DispositionRecoverIncumbent ||
			!validRecoveryCandidateStage(*operation, attestation) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.Recovery.CandidateStages {
			if prior.Surface == attestation.Surface {
				return false, ErrCollision
			}
		}
		operation.Recovery.CandidateStages = append(operation.Recovery.CandidateStages, attestation)
		sort.Slice(operation.Recovery.CandidateStages, func(i, j int) bool {
			return operation.Recovery.CandidateStages[i].Surface < operation.Recovery.CandidateStages[j].Surface
		})
		return true, nil
	})
}

func (r *Registry) RecordRecoveryCandidateClosed(expectedGeneration uint64, operationID string, attestation CandidateClosedAttestation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationFailedClosed || operation.Recovery == nil ||
			!completeRecoveryCandidateStages(*operation) || !validRecoveryCandidateClosed(*operation, attestation) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.Recovery.CandidateClosed {
			if prior.Surface == attestation.Surface {
				return false, ErrCollision
			}
		}
		operation.Recovery.CandidateClosed = append(operation.Recovery.CandidateClosed, attestation)
		sort.Slice(operation.Recovery.CandidateClosed, func(i, j int) bool {
			return operation.Recovery.CandidateClosed[i].Surface < operation.Recovery.CandidateClosed[j].Surface
		})
		return true, nil
	})
}

func (r *Registry) CompleteRecoveryCandidateClosed(expectedGeneration uint64, operationID string) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if operation.State != OperationFailedClosed || operation.Recovery == nil || !completeRecoveryCandidateStages(*operation) ||
			!completeRecoveryCandidateClosed(*operation) {
			return false, ErrInvalidTransition
		}
		for _, candidate := range operation.Recovery.Candidates {
			index := recordIndex(snapshot.Records, candidate.RecordID)
			if index < 0 || snapshot.Records[index].State != StatePrepared {
				return false, ErrInvalidTransition
			}
		}
		return true, appendOperationTransition(operation, OperationRecoveryCandidateClosed, evidenceSetDigest(operation.Recovery.CandidateClosed))
	})
}

func (r *Registry) transitionOperation(expectedGeneration uint64, operationID string, next LaneOperationState, evidenceDigest string, check func(*LaneOperation) error) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		operation, ok := mutableOperation(snapshot, operationID)
		if !ok {
			return false, ErrRecordNotFound
		}
		if err := check(operation); err != nil {
			return false, err
		}
		if err := appendOperationTransition(operation, next, evidenceDigest); err != nil {
			return false, err
		}
		return true, nil
	})
}

func appendOperationTransition(operation *LaneOperation, next LaneOperationState, evidenceDigest string) error {
	if operation == nil || !allowedOperationTransition(operation.Mode, operation.State, next) ||
		(evidenceDigest != "" && !digestPattern.MatchString(evidenceDigest)) {
		return ErrInvalidTransition
	}
	entry := OperationTransition{Sequence: uint64(len(operation.History) + 1), From: operation.State, To: next, EvidenceDigest: evidenceDigest}
	operation.History = append(operation.History, entry)
	operation.State = next
	return nil
}

func allowedOperationTransition(mode LaneOperationMode, from, to LaneOperationState) bool {
	switch {
	case from == OperationPrepare && mode == OperationFresh && (to == OperationCandidateClosed || to == OperationFailedClosed):
		return true
	case from == OperationPrepare && mode == OperationReplacement && (to == OperationIncumbentClosurePending || to == OperationAborted):
		return true
	case from == OperationIncumbentClosurePending && to == OperationIncumbentQuiescing:
		return true
	case from == OperationIncumbentQuiescing && to == OperationIncumbentQuiesced:
		return true
	case from == OperationIncumbentQuiesced && to == OperationCandidateClosed:
		return true
	case from == OperationCandidateClosed && (to == OperationReopenPrepared || to == OperationFailedClosed):
		return true
	case from == OperationReopenPrepared && (to == OperationReopenAuthorized || to == OperationFailedClosed || to == OperationRecoveryAborted):
		return true
	case from == OperationReopenAuthorized && (to == OperationTargetOpening || to == OperationFailedClosed || to == OperationRecoveryAborted):
		return true
	case from == OperationTargetOpening && (to == OperationReopened || to == OperationTargetClosurePending):
		return true
	case from == OperationReopened && (to == OperationAcceptancePending || to == OperationTargetClosurePending):
		return true
	case from == OperationAcceptancePending && (to == OperationAccepted || to == OperationTargetClosurePending):
		return true
	case from == OperationAccepted && to == OperationArchived:
		return true
	case from == OperationTargetClosurePending && to == OperationTargetQuiescing:
		return true
	case from == OperationTargetQuiescing && (to == OperationFailedClosed || to == OperationRecoveryAborted):
		return true
	case from == OperationFailedClosed && (to == OperationRecoveryCandidateClosed || to == OperationAborted):
		return true
	case from == OperationRecoveryCandidateClosed && (to == OperationReopenPrepared || to == OperationRecoveryAborted):
		return true
	case from == OperationAborted && to == OperationArchived:
		return true
	case from == OperationRecoveryAborted && to == OperationArchived:
		return true
	default:
		return false
	}
}

func hasUnarchivedOperation(snapshot *Snapshot, lane config.ReleaseLane) bool {
	for _, operation := range snapshot.Operations {
		if operation.Lane == lane && operation.State != OperationArchived {
			return true
		}
	}
	return false
}

func mutableOperation(snapshot *Snapshot, id string) (*LaneOperation, bool) {
	for index := range snapshot.Operations {
		if snapshot.Operations[index].ID == id {
			return &snapshot.Operations[index], true
		}
	}
	return nil, false
}

func laneOperationByID(operations []LaneOperation, id string) (LaneOperation, bool) {
	for _, operation := range operations {
		if operation.ID == id {
			return operation, true
		}
	}
	return LaneOperation{}, false
}

func cloneLaneOperation(operation LaneOperation) LaneOperation {
	data, err := json.Marshal(operation)
	if err != nil {
		return LaneOperation{}
	}
	var clone LaneOperation
	if json.Unmarshal(data, &clone) != nil {
		return LaneOperation{}
	}
	return clone
}

func operationIdentities(records []RootRecord, ids []string, lane config.ReleaseLane, state RecordState) ([]OperationIdentity, error) {
	if len(ids) == 0 {
		return nil, ErrInvalidOperation
	}
	seen := make(map[string]struct{}, len(ids))
	identities := make([]OperationIdentity, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrInvalidOperation
		}
		seen[id] = struct{}{}
		record, ok := recordByID(records, id)
		if !ok || record.Lane != lane || record.State != state {
			return nil, ErrInvalidOperation
		}
		identities = append(identities, operationIdentity(record))
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].InboxID < identities[j].InboxID })
	for index := 1; index < len(identities); index++ {
		if identities[index-1].InboxID == identities[index].InboxID {
			return nil, ErrInvalidOperation
		}
	}
	return identities, nil
}

func operationIdentity(record RootRecord) OperationIdentity {
	var volume *laneroot.VolumeIdentity
	if record.Volume != nil {
		copy := *record.Volume
		volume = &copy
	}
	return OperationIdentity{
		RecordID: record.ID, Lane: record.Lane, InboxID: record.InboxID,
		NormalizedRoot: record.NormalizedRoot, RootIdentity: record.RootIdentity,
		ConfigurationDigest: record.ConfigurationDigest, Epoch: record.Epoch,
		Supersedes: record.Supersedes, Volume: volume,
	}
}

func laneRecordIDs(records []RootRecord, lane config.ReleaseLane, state RecordState) []string {
	var ids []string
	for _, record := range records {
		if record.Lane == lane && record.State == state {
			ids = append(ids, record.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func matchingSuccessorSets(candidates, incumbents []OperationIdentity) bool {
	if len(candidates) != len(incumbents) {
		return false
	}
	for index, candidate := range candidates {
		incumbent := incumbents[index]
		if candidate.InboxID != incumbent.InboxID || candidate.RecordID == incumbent.RecordID ||
			candidate.NormalizedRoot != incumbent.NormalizedRoot || candidate.RootIdentity != incumbent.RootIdentity ||
			candidate.Lane != incumbent.Lane || candidate.Epoch <= incumbent.Epoch ||
			candidate.Supersedes != incumbent.RecordID {
			return false
		}
		if !reflect.DeepEqual(candidate.Volume, incumbent.Volume) {
			return false
		}
	}
	return true
}

func validateOperations(snapshot Snapshot) error {
	if len(snapshot.Operations) > 64 {
		return ErrInvalidRegistry
	}
	seenIDs := make(map[string]struct{}, len(snapshot.Operations))
	activeLanes := make(map[config.ReleaseLane]struct{}, 2)
	for index, operation := range snapshot.Operations {
		if index > 0 && snapshot.Operations[index-1].ID >= operation.ID {
			return ErrInvalidRegistry
		}
		if _, duplicate := seenIDs[operation.ID]; duplicate {
			return ErrInvalidRegistry
		}
		seenIDs[operation.ID] = struct{}{}
		if !validOperationShape(operation) || !validOperationHistory(operation) || !validOperationEvidence(operation) ||
			!validOperationRecordLinks(snapshot, operation) {
			return ErrInvalidRegistry
		}
		if operation.State != OperationArchived {
			if _, duplicate := activeLanes[operation.Lane]; duplicate {
				return ErrInvalidRegistry
			}
			activeLanes[operation.Lane] = struct{}{}
		}
	}
	return nil
}

func validOperationShape(operation LaneOperation) bool {
	if !operationIDPattern.MatchString(operation.ID) ||
		(operation.Lane != config.ReleaseLaneDev && operation.Lane != config.ReleaseLaneQAS) ||
		(operation.Mode != OperationFresh && operation.Mode != OperationReplacement) ||
		!validOperationState(operation.State) || len(operation.Candidates) == 0 || len(operation.Candidates) > 32 ||
		!sameStringSet(operation.RequiredSurfaces, operationSurfaces) ||
		!commitIDPattern.MatchString(operation.SourceRevision) ||
		!digestPattern.MatchString(operation.ArtifactDigest) || !digestPattern.MatchString(operation.ManifestDigest) ||
		!digestPattern.MatchString(operation.CandidateSetDigest) || len(operation.History) == 0 {
		return false
	}
	for index, candidate := range operation.Candidates {
		if !validOperationIdentity(candidate, operation.Lane) || (index > 0 && operation.Candidates[index-1].InboxID >= candidate.InboxID) {
			return false
		}
	}
	if operation.Mode == OperationFresh {
		if len(operation.Incumbents) != 0 {
			return false
		}
	} else {
		if len(operation.Incumbents) == 0 || len(operation.Incumbents) != len(operation.Candidates) {
			return false
		}
		for index, incumbent := range operation.Incumbents {
			if !validOperationIdentity(incumbent, operation.Lane) || (index > 0 && operation.Incumbents[index-1].InboxID >= incumbent.InboxID) {
				return false
			}
		}
		if !matchingSuccessorSets(operation.Candidates, operation.Incumbents) {
			return false
		}
	}
	digest, err := digestCandidateSet(operation.Candidates)
	if err != nil || digest != operation.CandidateSetDigest {
		return false
	}
	if operation.FailureDisposition != nil {
		if !digestPattern.MatchString(operation.FailureDisposition.EvidenceDigest) ||
			(operation.FailureDisposition.Kind != DispositionAbort && operation.FailureDisposition.Kind != DispositionRecoverIncumbent) {
			return false
		}
		if operation.FailureDisposition.Kind == DispositionRecoverIncumbent && operation.Recovery == nil {
			return false
		}
		if operation.FailureDisposition.Kind == DispositionRecoverIncumbent &&
			operation.FailureDisposition.EvidenceDigest != operation.Recovery.ProvenanceDigest {
			return false
		}
		if operation.FailureDisposition.Kind == DispositionAbort && operation.Recovery != nil {
			return false
		}
	}
	if operation.Recovery == nil {
		return operation.FailureDisposition == nil || operation.FailureDisposition.Kind == DispositionAbort
	}
	return operation.FailureDisposition != nil && operation.FailureDisposition.Kind == DispositionRecoverIncumbent &&
		validRecoveryAttemptShape(operation)
}

func validRecoveryAttemptShape(operation LaneOperation) bool {
	recovery := operation.Recovery
	if recovery == nil || !operationIDPattern.MatchString(recovery.ID) ||
		len(recovery.Candidates) == 0 || len(recovery.Candidates) != len(operation.Incumbents) || len(recovery.Candidates) > 32 ||
		!commitIDPattern.MatchString(recovery.SourceRevision) || !digestPattern.MatchString(recovery.ArtifactDigest) ||
		!digestPattern.MatchString(recovery.ManifestDigest) || !digestPattern.MatchString(recovery.ProvenanceDigest) ||
		!digestPattern.MatchString(recovery.CandidateSetDigest) {
		return false
	}
	for index, candidate := range recovery.Candidates {
		if !validOperationIdentity(candidate, operation.Lane) || (index > 0 && recovery.Candidates[index-1].InboxID >= candidate.InboxID) {
			return false
		}
	}
	if !matchingRecoverySuccessors(operation.Candidates, operation.Incumbents, recovery.Candidates) {
		return false
	}
	digest, err := digestCandidateSet(recovery.Candidates)
	if err != nil || digest != recovery.CandidateSetDigest || !validRecoveryCandidateEvidence(operation) {
		return false
	}
	if recovery.Acceptance != nil && !validLaneAcceptance(recoveryOperationContext(operation), *recovery.Acceptance) {
		return false
	}
	return true
}

func matchingRecoverySuccessors(targets, incumbents, recovery []OperationIdentity) bool {
	if len(targets) != len(incumbents) || len(recovery) != len(incumbents) {
		return false
	}
	for index, incumbent := range incumbents {
		target := targets[index]
		candidate := recovery[index]
		if target.InboxID != incumbent.InboxID || candidate.InboxID != incumbent.InboxID ||
			candidate.Lane != incumbent.Lane || candidate.ConfigurationDigest != incumbent.ConfigurationDigest ||
			candidate.NormalizedRoot != incumbent.NormalizedRoot || candidate.RootIdentity != incumbent.RootIdentity ||
			candidate.RecordID == target.RecordID || candidate.Supersedes != target.RecordID || candidate.Epoch <= target.Epoch ||
			!reflect.DeepEqual(candidate.Volume, incumbent.Volume) {
			return false
		}
	}
	return true
}

func validOperationIdentity(identity OperationIdentity, lane config.ReleaseLane) bool {
	if !registryIDPattern.MatchString(identity.RecordID) || identity.Lane != lane ||
		!registryIDPattern.MatchString(identity.InboxID) || !canonicalPath(identity.NormalizedRoot) ||
		!validIdentity(identity.RootIdentity) || !digestPattern.MatchString(identity.ConfigurationDigest) || identity.Epoch == 0 {
		return false
	}
	if identity.Supersedes != "" && !registryIDPattern.MatchString(identity.Supersedes) {
		return false
	}
	if requiresAPFS(config.HostKindMac, lane) {
		return identity.Volume != nil && validVolume(*identity.Volume) &&
			identity.Volume.Mountpoint == identity.NormalizedRoot && identity.Volume.Device == identity.RootIdentity.Device
	}
	return identity.Volume == nil
}

func validOperationState(state LaneOperationState) bool {
	switch state {
	case OperationPrepare, OperationIncumbentClosurePending, OperationIncumbentQuiescing,
		OperationIncumbentQuiesced, OperationCandidateClosed, OperationReopenPrepared,
		OperationReopenAuthorized, OperationTargetOpening, OperationReopened,
		OperationAcceptancePending, OperationAccepted, OperationArchived,
		OperationTargetClosurePending, OperationTargetQuiescing, OperationFailedClosed,
		OperationRecoveryCandidateClosed, OperationRecoveryAborted, OperationAborted:
		return true
	default:
		return false
	}
}

func validOperationHistory(operation LaneOperation) bool {
	if len(operation.History) == 0 || len(operation.History) > 32 {
		return false
	}
	for index, entry := range operation.History {
		if entry.Sequence != uint64(index+1) || !validOperationState(entry.To) ||
			(index == 0 && entry.From != "") ||
			(index > 0 && entry.From != operation.History[index-1].To) ||
			(index > 0 && !allowedOperationTransition(operation.Mode, entry.From, entry.To)) ||
			(entry.EvidenceDigest != "" && !digestPattern.MatchString(entry.EvidenceDigest)) {
			return false
		}
	}
	if operation.History[0].To != OperationPrepare || operation.History[0].Sequence != 1 ||
		operation.History[0].EvidenceDigest != "" {
		return false
	}
	for index, entry := range operation.History {
		if entry.To == OperationArchived && index != len(operation.History)-1 {
			return false
		}
		if entry.To == OperationRecoveryCandidateClosed && operation.Mode != OperationReplacement {
			return false
		}
	}
	return operation.State == operation.History[len(operation.History)-1].To
}

func recoveryPath(operation LaneOperation) bool {
	return operation.Recovery != nil && stateAtOrAfter(operation, OperationRecoveryCandidateClosed)
}

func operationTargetContext(operation LaneOperation) LaneOperation {
	if !recoveryPath(operation) {
		return operation
	}
	return recoveryOperationContext(operation)
}

func recoveryOperationContext(operation LaneOperation) LaneOperation {
	if operation.Recovery == nil {
		return operation
	}
	target := operation
	target.Candidates = operation.Recovery.Candidates
	target.CandidateSetDigest = operation.Recovery.CandidateSetDigest
	target.SourceRevision = operation.Recovery.SourceRevision
	target.ArtifactDigest = operation.Recovery.ArtifactDigest
	target.ManifestDigest = operation.Recovery.ManifestDigest
	target.TransportReady = operation.Recovery.TransportReady
	target.Authorizations = operation.Recovery.Authorizations
	target.OpenAttestations = operation.Recovery.OpenAttestations
	target.Acceptance = operation.Recovery.Acceptance
	return target
}

func targetOpenAttestations(operation LaneOperation) []OpenAttestation {
	if recoveryPath(operation) {
		return operation.Recovery.OpenAttestations
	}
	return operation.OpenAttestations
}

func isPreOpenFailureState(state LaneOperationState, recovery bool, mode LaneOperationMode) bool {
	if recovery {
		return state == OperationRecoveryCandidateClosed || state == OperationReopenPrepared || state == OperationReopenAuthorized
	}
	if state == OperationPrepare {
		return mode == OperationFresh
	}
	return state == OperationCandidateClosed || state == OperationReopenPrepared || state == OperationReopenAuthorized
}

func validFailureEvidence(operation LaneOperation, failure OperationFailureEvidence) bool {
	target := operationTargetContext(operation)
	if failure.FailedFrom != operation.State || !validFailurePayload(target, failure) {
		return false
	}
	if isPreOpenFailureState(operation.State, recoveryPath(operation), operation.Mode) {
		return len(failure.KnownDirectives) == 0
	}
	return operation.State == OperationTargetOpening || operation.State == OperationReopened || operation.State == OperationAcceptancePending
}

func validFailurePayload(target LaneOperation, failure OperationFailureEvidence) bool {
	return validEvidenceID(failure.FailureID) && failure.CandidateSetDigest == target.CandidateSetDigest &&
		digestPattern.MatchString(failure.FailureDigest) && digestPattern.MatchString(failure.EvidenceDigest) &&
		validKnownDirectives(failure.KnownDirectives, target.OpenAttestations)
}

func validStoredFailureEvidence(operation LaneOperation, failure OperationFailureEvidence, recovery bool) bool {
	target := operation
	if recovery {
		if operation.Recovery == nil || !stateAtOrAfter(operation, OperationRecoveryCandidateClosed) {
			return false
		}
		target = operationTargetContext(operation)
	}
	if !validFailurePayload(target, failure) {
		return false
	}
	preOpen := isPreOpenFailureState(failure.FailedFrom, recovery, operation.Mode)
	wantTo := OperationTargetClosurePending
	if preOpen {
		if recovery {
			wantTo = OperationRecoveryAborted
		} else {
			wantTo = OperationFailedClosed
		}
		return len(failure.KnownDirectives) == 0 &&
			validTerminalSurfaceProofs(target.CandidateSetDigest, failure.TerminalProofs, operation.RequiredSurfaces) &&
			hasFailureTransition(operation, failure.FailedFrom, wantTo, failure.FailureDigest)
	}
	if len(failure.TerminalProofs) != 0 ||
		(failure.FailedFrom != OperationTargetOpening && failure.FailedFrom != OperationReopened && failure.FailedFrom != OperationAcceptancePending) {
		return false
	}
	return hasFailureTransition(operation, failure.FailedFrom, wantTo, failure.FailureDigest)
}

func hasFailureTransition(operation LaneOperation, from, to LaneOperationState, digest string) bool {
	for _, entry := range operation.History {
		if entry.From == from && entry.To == to && entry.EvidenceDigest == digest {
			return true
		}
	}
	return false
}

func validKnownDirectives(directives []DirectiveHighWater, opens []OpenAttestation) bool {
	seenSurface, seenID := map[string]struct{}{}, map[string]struct{}{}
	for _, directive := range directives {
		if !isOperationSurface(directive.Surface) || !validEvidenceID(directive.DirectiveID) ||
			directive.DirectiveEpoch == 0 || directive.HighestSequence == 0 {
			return false
		}
		if _, duplicate := seenSurface[directive.Surface]; duplicate {
			return false
		}
		if _, duplicate := seenID[directive.DirectiveID]; duplicate {
			return false
		}
		seenSurface[directive.Surface], seenID[directive.DirectiveID] = struct{}{}, struct{}{}
	}
	for _, open := range opens {
		found := false
		for _, directive := range directives {
			if directive.Surface == open.Surface && directive.DirectiveID == open.DirectiveID &&
				directive.DirectiveEpoch == open.DirectiveEpoch && directive.HighestSequence >= open.HighWaterSequence {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validTerminalSurfaceProofs(candidateDigest string, proofs []TerminalSurfaceProof, surfaces []string) bool {
	if !completeSurfaceSet(surfaces, terminalProofSurfaces(proofs)) {
		return false
	}
	seen := map[string]struct{}{}
	for _, proof := range proofs {
		if !contains(surfaces, proof.Surface) || proof.CandidateSetDigest != candidateDigest || !proof.Closed ||
			proof.Quiescence.Surface != proof.Surface || !proof.Quiescence.ZeroWorkPostflight ||
			proof.Quiescence.RemainingWork != 0 || !validQuiescenceAttestation(proof.Quiescence) ||
			!digestPattern.MatchString(proof.EvidenceDigest) {
			return false
		}
		if _, duplicate := seen[proof.Surface]; duplicate {
			return false
		}
		seen[proof.Surface] = struct{}{}
	}
	return true
}

func terminalProofSurfaces(proofs []TerminalSurfaceProof) []string {
	surfaces := make([]string, 0, len(proofs))
	for _, proof := range proofs {
		surfaces = append(surfaces, proof.Surface)
	}
	return surfaces
}

func validTargetClosure(target LaneOperation, attestation TargetClosureAttestation) bool {
	failure, hasFailure := failureForTarget(target)
	minimumEpoch := candidateOperationEpoch(target)
	if hasFailure {
		for _, known := range failure.KnownDirectives {
			if known.Surface == attestation.Surface && known.DirectiveEpoch > minimumEpoch {
				minimumEpoch = known.DirectiveEpoch
			}
		}
	}
	return hasFailure && (failure.FailedFrom == OperationTargetOpening || failure.FailedFrom == OperationReopened ||
		failure.FailedFrom == OperationAcceptancePending) &&
		hasFailureTransition(target, failure.FailedFrom, OperationTargetClosurePending, failure.FailureDigest) &&
		contains(target.RequiredSurfaces, attestation.Surface) && attestation.Attempt > 0 &&
		validEvidenceID(attestation.DirectiveID) && validEvidenceID(attestation.AttestationID) &&
		attestation.DirectiveEpoch > minimumEpoch && attestation.HighestSequence > 0 &&
		attestation.CandidateSetDigest == target.CandidateSetDigest && digestPattern.MatchString(attestation.EvidenceDigest)
}

func failureForTarget(target LaneOperation) (OperationFailureEvidence, bool) {
	if target.Recovery != nil && target.CandidateSetDigest == target.Recovery.CandidateSetDigest {
		if target.Recovery.Failure == nil {
			return OperationFailureEvidence{}, false
		}
		return *target.Recovery.Failure, true
	}
	if target.Failure == nil {
		return OperationFailureEvidence{}, false
	}
	return *target.Failure, true
}

func completeTargetClosures(operation LaneOperation, candidateDigest string) bool {
	latest := map[string]TargetClosureAttestation{}
	for _, attestation := range operation.TargetClosures {
		if attestation.CandidateSetDigest != candidateDigest {
			continue
		}
		if current, ok := latest[attestation.Surface]; !ok || attestation.Attempt > current.Attempt {
			latest[attestation.Surface] = attestation
		}
	}
	if len(latest) != len(operation.RequiredSurfaces) {
		return false
	}
	for _, surface := range operation.RequiredSurfaces {
		attestation, ok := latest[surface]
		if !ok || !attestation.Closed {
			return false
		}
	}
	return true
}

func completeTargetQuiescence(operation LaneOperation, candidateDigest string, surfaces []string) bool {
	latest := map[string]IncumbentQuiescenceAttestation{}
	for _, attestation := range operation.TargetQuiescence {
		if attestation.CandidateSetDigest != candidateDigest {
			continue
		}
		quiescence := attestation.Quiescence
		if current, ok := latest[quiescence.Surface]; !ok || quiescence.Attempt > current.Attempt {
			latest[quiescence.Surface] = quiescence
		}
	}
	if len(latest) != len(surfaces) {
		return false
	}
	for _, surface := range surfaces {
		attestation, ok := latest[surface]
		if !ok || !attestation.ZeroWorkPostflight || attestation.RemainingWork != 0 {
			return false
		}
	}
	return true
}

func targetQuiescenceDigest(operation LaneOperation, candidateDigest string) string {
	var evidence []TargetQuiescenceAttestation
	for _, attestation := range operation.TargetQuiescence {
		if attestation.CandidateSetDigest == candidateDigest {
			evidence = append(evidence, attestation)
		}
	}
	return evidenceSetDigest(evidence)
}

func sortTargetClosures(values []TargetClosureAttestation) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].CandidateSetDigest != values[j].CandidateSetDigest {
			return values[i].CandidateSetDigest < values[j].CandidateSetDigest
		}
		if values[i].Surface != values[j].Surface {
			return values[i].Surface < values[j].Surface
		}
		return values[i].Attempt < values[j].Attempt
	})
}

func sortTargetQuiescence(values []TargetQuiescenceAttestation) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].CandidateSetDigest != values[j].CandidateSetDigest {
			return values[i].CandidateSetDigest < values[j].CandidateSetDigest
		}
		if values[i].Quiescence.Surface != values[j].Quiescence.Surface {
			return values[i].Quiescence.Surface < values[j].Quiescence.Surface
		}
		return values[i].Quiescence.Attempt < values[j].Quiescence.Attempt
	})
}

func cloneFailureEvidence(failure OperationFailureEvidence) OperationFailureEvidence {
	data, _ := json.Marshal(failure)
	var clone OperationFailureEvidence
	_ = json.Unmarshal(data, &clone)
	return clone
}

func cloneTerminalProofs(proofs []TerminalSurfaceProof) []TerminalSurfaceProof {
	data, _ := json.Marshal(proofs)
	var clone []TerminalSurfaceProof
	_ = json.Unmarshal(data, &clone)
	return clone
}

func validInitialFailedClosedProof(operation LaneOperation) bool {
	if operation.Failure == nil || !validStoredFailureEvidence(operation, *operation.Failure, false) {
		return false
	}
	if isPreOpenFailureState(operation.Failure.FailedFrom, false, operation.Mode) {
		return validTerminalSurfaceProofs(operation.CandidateSetDigest, operation.Failure.TerminalProofs, operation.RequiredSurfaces)
	}
	return completeTargetClosures(operation, operation.CandidateSetDigest) &&
		completeTargetQuiescence(operation, operation.CandidateSetDigest, operation.RequiredSurfaces)
}

func validRecoveryFailedClosedProof(operation LaneOperation) bool {
	if operation.Recovery == nil || operation.Recovery.Failure == nil || !validStoredFailureEvidence(operation, *operation.Recovery.Failure, true) {
		return false
	}
	if isPreOpenFailureState(operation.Recovery.Failure.FailedFrom, true, operation.Mode) {
		return validTerminalSurfaceProofs(operation.Recovery.CandidateSetDigest, operation.Recovery.Failure.TerminalProofs, operation.RequiredSurfaces)
	}
	return completeTargetClosures(operation, operation.Recovery.CandidateSetDigest) &&
		completeTargetQuiescence(operation, operation.Recovery.CandidateSetDigest, operation.RequiredSurfaces)
}

func validOperationArchiveProof(operation LaneOperation) bool {
	target := operationTargetContext(operation)
	accepted := operation.State == OperationAccepted || operation.State == OperationArchived && historyEndsWith(operation, OperationAccepted, OperationArchived)
	return accepted && target.Acceptance != nil &&
		validLaneAcceptance(target, *target.Acceptance) && completeRemoteOpens(target) && hasMacOpen(target)
}

func validOperationAbortProof(operation LaneOperation) bool {
	aborted := operation.State == OperationAborted || operation.State == OperationArchived && historyEndsWith(operation, OperationAborted, OperationArchived)
	if !aborted {
		return false
	}
	if operation.FailureDisposition != nil {
		return operation.FailureDisposition.Kind == DispositionAbort && digestPattern.MatchString(operation.FailureDisposition.EvidenceDigest) &&
			operation.HistoryDigestFor(OperationFailedClosed, OperationAborted) == operation.FailureDisposition.EvidenceDigest &&
			validInitialFailedClosedProof(operation)
	}
	return operation.Mode == OperationReplacement && hasDirectPreparedAbort(operation) &&
		evidenceSetDigest(operation.AbortProofs) == operation.HistoryDigestFor(OperationPrepare, OperationAborted) &&
		validTerminalSurfaceProofs(operation.CandidateSetDigest, operation.AbortProofs, operation.RequiredSurfaces)
}

func hasDirectPreparedAbort(operation LaneOperation) bool {
	for _, transition := range operation.History {
		if transition.From == OperationPrepare && transition.To == OperationAborted {
			return true
		}
	}
	return false
}

func (operation LaneOperation) HistoryDigestFor(from, to LaneOperationState) string {
	for _, transition := range operation.History {
		if transition.From == from && transition.To == to {
			return transition.EvidenceDigest
		}
	}
	return ""
}

func validRecoveryAbortProof(operation LaneOperation) bool {
	recoveryAborted := operation.State == OperationRecoveryAborted || operation.State == OperationArchived && historyEndsWith(operation, OperationRecoveryAborted, OperationArchived)
	return recoveryAborted && operation.Recovery != nil &&
		operation.FailureDisposition != nil && operation.FailureDisposition.Kind == DispositionRecoverIncumbent &&
		digestPattern.MatchString(operation.FailureDisposition.EvidenceDigest) && validRecoveryFailedClosedProof(operation)
}

func historyEndsWith(operation LaneOperation, prior, last LaneOperationState) bool {
	return len(operation.History) >= 2 && operation.History[len(operation.History)-2].To == prior &&
		operation.History[len(operation.History)-1].To == last
}

func validOperationEvidence(operation LaneOperation) bool {
	if !validClosureHistory(operation) || !validQuiescenceHistory(operation) ||
		!validCandidateStages(operation) || !validCandidateClosures(operation) ||
		!validTransportReady(operation) || !validAuthorizations(operation) || !validOpenHistory(operation) ||
		!operationEvidenceIDsUnique(operation) || !validTargetEvidenceHistory(operation) {
		return false
	}
	if operation.State == OperationPrepare && (len(operation.IncumbentClosures) != 0 || len(operation.IncumbentQuiescence) != 0 || operation.Failure != nil) {
		return false
	}
	if operation.Mode == OperationReplacement && !stateAtOrAfter(operation, OperationIncumbentClosurePending) && len(operation.IncumbentClosures) != 0 {
		return false
	}
	if !stateAtOrAfter(operation, OperationIncumbentQuiescing) && len(operation.IncumbentQuiescence) != 0 {
		return false
	}
	if len(operation.IncumbentQuiescence) != 0 && operation.Mode == OperationReplacement && !completeIncumbentClosures(operation) {
		return false
	}
	if operation.Mode == OperationReplacement && !stateAtOrAfter(operation, OperationIncumbentQuiesced) && len(operation.CandidateStages) != 0 {
		return false
	}
	if len(operation.CandidateClosed) != 0 && !completeCandidateStages(operation) {
		return false
	}
	if operation.Mode == OperationFresh && (len(operation.IncumbentClosures) != 0 || len(operation.IncumbentQuiescence) != 0) {
		return false
	}
	if stateAtOrAfter(operation, OperationIncumbentQuiescing) && operation.Mode == OperationReplacement &&
		!completeIncumbentClosures(operation) {
		return false
	}
	if stateAtOrAfter(operation, OperationIncumbentQuiesced) && operation.Mode == OperationReplacement &&
		!completeIncumbentQuiescence(operation) {
		return false
	}
	if stateAtOrAfter(operation, OperationCandidateClosed) && !completeCandidateStages(operation) {
		return false
	}
	if stateAtOrAfter(operation, OperationCandidateClosed) && !completeCandidateClosed(operation) {
		return false
	}
	if !validMainAttemptEvidence(operation) {
		return false
	}
	if operation.Failure != nil && !validStoredFailureEvidence(operation, *operation.Failure, false) {
		return false
	}
	if operation.FailureDisposition != nil {
		switch operation.FailureDisposition.Kind {
		case DispositionAbort:
			if !validOperationAbortProof(operation) {
				return false
			}
		case DispositionRecoverIncumbent:
			if operation.Mode != OperationReplacement || !stateAtOrAfter(operation, OperationFailedClosed) ||
				!validInitialFailedClosedProof(operation) {
				return false
			}
		default:
			return false
		}
	}
	if operation.Recovery != nil && !validRecoveryAttemptShape(operation) {
		return false
	}
	if stateAtOrAfter(operation, OperationFailedClosed) && !validInitialFailedClosedProof(operation) {
		return false
	}
	if operation.State == OperationFailedClosed && operation.FailureDisposition != nil && operation.FailureDisposition.Kind != DispositionRecoverIncumbent {
		return false
	}
	if operation.State == OperationTargetClosurePending || operation.State == OperationTargetQuiescing {
		failure := operation.Failure
		target := operation
		if recoveryPath(operation) {
			failure = operation.Recovery.Failure
			target = recoveryOperationContext(operation)
		}
		if failure == nil || failure.CandidateSetDigest != target.CandidateSetDigest {
			return false
		}
		if operation.State == OperationTargetQuiescing && !completeTargetClosures(operation, target.CandidateSetDigest) {
			return false
		}
	}
	if operation.State == OperationAborted && !validOperationAbortProof(operation) {
		return false
	}
	if operation.State == OperationRecoveryAborted && !validRecoveryAbortProof(operation) {
		return false
	}
	if operation.State == OperationArchived && !validOperationArchiveProof(operation) &&
		!validOperationAbortProof(operation) && !validRecoveryAbortProof(operation) {
		return false
	}
	return true
}

func validMainAttemptEvidence(operation LaneOperation) bool {
	if operation.State == OperationArchived && (historyEndsWith(operation, OperationAborted, OperationArchived) ||
		historyEndsWith(operation, OperationRecoveryAborted, OperationArchived)) {
		return operation.Acceptance == nil
	}
	state := operation.State
	if operation.Failure != nil {
		state = operation.Failure.FailedFrom
	}
	needsTransport := state == OperationReopenPrepared || state == OperationReopenAuthorized || state == OperationTargetOpening ||
		state == OperationReopened || state == OperationAcceptancePending || state == OperationAccepted || state == OperationArchived
	needsAuthorization := state == OperationReopenAuthorized || state == OperationTargetOpening || state == OperationReopened ||
		state == OperationAcceptancePending || state == OperationAccepted || state == OperationArchived
	needsOpen := state == OperationReopened || state == OperationAcceptancePending || state == OperationAccepted || state == OperationArchived
	if needsTransport && !validTransportReadySet(operation, operation.TransportReady) {
		return false
	}
	if !needsTransport && len(operation.TransportReady) != 0 && !validTransportReadySet(operation, operation.TransportReady) {
		return false
	}
	if needsAuthorization && !validAuthorizationSet(operation, operation.Authorizations) {
		return false
	}
	if len(operation.Authorizations) != 0 && !validAuthorizationSet(operation, operation.Authorizations) {
		return false
	}
	if needsAuthorization && !validTransportReadySet(operation, operation.TransportReady) {
		return false
	}
	if len(operation.OpenAttestations) != 0 && (!validAuthorizationSet(operation, operation.Authorizations) || !validOpenHistory(operation)) {
		return false
	}
	if needsOpen && (!completeRemoteOpens(operation) || !hasMacOpen(operation)) {
		return false
	}
	if state == OperationAccepted || state == OperationArchived && operation.Recovery == nil {
		if operation.Acceptance == nil || !validLaneAcceptance(operation, *operation.Acceptance) {
			return false
		}
	} else if operation.Acceptance != nil {
		return false
	}
	return true
}

func validTargetEvidenceHistory(operation LaneOperation) bool {
	if len(operation.TargetClosures) > len(operation.RequiredSurfaces)*64 ||
		len(operation.TargetQuiescence) > len(operation.RequiredSurfaces)*64 {
		return false
	}
	latestClosure := map[string]uint64{}
	latestClosureEpoch := map[string]uint64{}
	latestQuiescence := map[string]uint64{}
	seenIDs := map[string]struct{}{}
	for _, attestation := range operation.TargetClosures {
		target, ok := targetContextForDigest(operation, attestation.CandidateSetDigest)
		if !ok || !validTargetClosure(target, attestation) {
			return false
		}
		key := attestation.CandidateSetDigest + "\x00" + attestation.Surface
		if attestation.Attempt <= latestClosure[key] || attestation.DirectiveEpoch <= latestClosureEpoch[key] {
			return false
		}
		latestClosure[key] = attestation.Attempt
		latestClosureEpoch[key] = attestation.DirectiveEpoch
		for _, id := range []string{attestation.DirectiveID, attestation.AttestationID} {
			if _, duplicate := seenIDs[id]; duplicate {
				return false
			}
			seenIDs[id] = struct{}{}
		}
	}
	for _, attestation := range operation.TargetQuiescence {
		target, ok := targetContextForDigest(operation, attestation.CandidateSetDigest)
		q := attestation.Quiescence
		if !ok || q.Surface == "" || !contains(operation.RequiredSurfaces, q.Surface) || !validQuiescenceAttestation(q) {
			return false
		}
		failure, exists := failureForTarget(target)
		if !exists || !hasCleanupTransitionsForFailure(operation, failure) ||
			!completeTargetClosures(operation, attestation.CandidateSetDigest) {
			return false
		}
		key := attestation.CandidateSetDigest + "\x00" + q.Surface
		if q.Attempt <= latestQuiescence[key] {
			return false
		}
		latestQuiescence[key] = q.Attempt
	}
	return true
}

func targetContextForDigest(operation LaneOperation, candidateDigest string) (LaneOperation, bool) {
	if candidateDigest == operation.CandidateSetDigest {
		return operation, true
	}
	if operation.Recovery != nil && candidateDigest == operation.Recovery.CandidateSetDigest {
		return recoveryOperationContext(operation), true
	}
	return LaneOperation{}, false
}

func hasCleanupTransitionsForFailure(operation LaneOperation, failure OperationFailureEvidence) bool {
	closureIndex := -1
	for index, transition := range operation.History {
		if transition.From == failure.FailedFrom && transition.To == OperationTargetClosurePending &&
			transition.EvidenceDigest == failure.FailureDigest {
			closureIndex = index
			break
		}
	}
	if closureIndex < 0 {
		return false
	}
	for index := closureIndex + 1; index < len(operation.History); index++ {
		transition := operation.History[index]
		if transition.From == OperationTargetClosurePending && transition.To == OperationTargetQuiescing {
			return true
		}
		if transition.To == OperationFailedClosed || transition.To == OperationRecoveryAborted {
			return false
		}
	}
	return false
}

func stateAtOrAfter(operation LaneOperation, state LaneOperationState) bool {
	for _, entry := range operation.History {
		if entry.To == state {
			return true
		}
	}
	return false
}

func validOperationRecordLinks(snapshot Snapshot, operation LaneOperation) bool {
	candidateClosed := stateAtOrAfter(operation, OperationCandidateClosed)
	recoveryClosed := stateAtOrAfter(operation, OperationRecoveryCandidateClosed)
	mainAborted := operation.State == OperationAborted || operation.State == OperationArchived && historyEndsWith(operation, OperationAborted, OperationArchived)
	recoveryAborted := operation.State == OperationRecoveryAborted || operation.State == OperationArchived && historyEndsWith(operation, OperationRecoveryAborted, OperationArchived)
	recoveryPromoted := historyHasAfter(operation, OperationRecoveryCandidateClosed, OperationAccepted)
	for _, identity := range operation.Candidates {
		record, ok := recordByID(snapshot.Records, identity.RecordID)
		if !ok || !operationIdentityMatchesRecord(identity, record) {
			return false
		}
		if operation.Mode == OperationFresh && record.Supersedes != "" {
			return false
		}
		if recoveryClosed {
			if recoveryPromoted {
				if record.State != StateSuperseded && record.State != StateRetired {
					return false
				}
			} else if operation.State == OperationArchived && recoveryAborted {
				if record.State != StateActive && record.State != StateSuperseded && record.State != StateRetired {
					return false
				}
			} else if record.State != StateActive {
				return false
			}
		} else if candidateClosed {
			if mainAborted {
				if record.State != StateRetired {
					return false
				}
			} else if operation.State != OperationArchived && record.State != StateActive {
				return false
			} else if operation.State == OperationArchived && record.State != StateActive && record.State != StateSuperseded && record.State != StateRetired {
				return false
			}
		} else if mainAborted && record.State == StateRetired {
			continue
		} else if record.State != StatePrepared {
			return false
		}
	}
	for _, identity := range operation.Incumbents {
		record, ok := recordByID(snapshot.Records, identity.RecordID)
		if !ok || !operationIdentityMatchesRecord(identity, record) {
			return false
		}
		if candidateClosed {
			if operation.State != OperationArchived && record.State != StateSuperseded {
				return false
			}
			if operation.State == OperationArchived && record.State != StateSuperseded && record.State != StateRetired {
				return false
			}
		} else if record.State != StateActive {
			return false
		}
	}
	if operation.Recovery != nil {
		for _, identity := range operation.Recovery.Candidates {
			record, ok := recordByID(snapshot.Records, identity.RecordID)
			if !ok || !operationIdentityMatchesRecord(identity, record) {
				return false
			}
			if recoveryClosed {
				if recoveryAborted {
					if record.State != StateRetired {
						return false
					}
				} else if recoveryPromoted && operation.State != OperationArchived && record.State != StateActive {
					return false
				} else if recoveryPromoted && operation.State == OperationArchived && record.State != StateActive && record.State != StateSuperseded && record.State != StateRetired {
					return false
				} else if !recoveryPromoted && record.State != StatePrepared {
					return false
				}
			} else if record.State != StatePrepared {
				return false
			}
		}
	}
	return true
}

func operationIdentityMatchesRecord(identity OperationIdentity, record RootRecord) bool {
	return identity.RecordID == record.ID && identity.Lane == record.Lane && identity.InboxID == record.InboxID &&
		identity.NormalizedRoot == record.NormalizedRoot && identity.RootIdentity == record.RootIdentity &&
		identity.ConfigurationDigest == record.ConfigurationDigest && identity.Epoch == record.Epoch &&
		identity.Supersedes == record.Supersedes && reflect.DeepEqual(identity.Volume, record.Volume)
}

func validClosureAttestation(operation LaneOperation, attestation IncumbentClosureAttestation) bool {
	if attestation.Attempt == 0 || !contains(operation.RequiredSurfaces, attestation.Surface) ||
		!validEvidenceID(attestation.DirectiveID) || !validEvidenceID(attestation.AttestationID) ||
		!digestPattern.MatchString(attestation.EvidenceDigest) || attestation.DirectiveEpoch == 0 {
		return false
	}
	var maxEpoch uint64
	for _, incumbent := range operation.Incumbents {
		if incumbent.Epoch > maxEpoch {
			maxEpoch = incumbent.Epoch
		}
	}
	return attestation.DirectiveEpoch > maxEpoch
}

func completeIncumbentClosures(operation LaneOperation) bool {
	latest := make(map[string]IncumbentClosureAttestation, len(operation.RequiredSurfaces))
	for _, attestation := range operation.IncumbentClosures {
		if current, ok := latest[attestation.Surface]; !ok || attestation.Attempt > current.Attempt {
			latest[attestation.Surface] = attestation
		}
	}
	if len(latest) != len(operation.RequiredSurfaces) {
		return false
	}
	for _, surface := range operation.RequiredSurfaces {
		attestation, ok := latest[surface]
		if !ok || !attestation.Closed {
			return false
		}
	}
	return true
}

func validQuiescenceAttestation(attestation IncumbentQuiescenceAttestation) bool {
	if attestation.Attempt == 0 || !isOperationSurface(attestation.Surface) ||
		!digestPattern.MatchString(attestation.InventoryDigest) ||
		(attestation.ZeroWorkPostflight && attestation.RemainingWork != 0) ||
		(attestation.PostflightDigest != "" && !digestPattern.MatchString(attestation.PostflightDigest)) ||
		(attestation.ZeroWorkPostflight && attestation.PostflightDigest == "") {
		return false
	}
	classes := map[string]WorkDisposition{}
	for _, disposition := range attestation.Dispositions {
		if _, duplicate := classes[disposition.Class]; duplicate || !isWorkClass(disposition.Class) ||
			!validWorkOutcome(disposition.Outcome) || !digestPattern.MatchString(disposition.EvidenceDigest) {
			return false
		}
		if disposition.Observed == 0 && disposition.Outcome != "none" || disposition.Observed > 0 && disposition.Outcome == "none" {
			return false
		}
		classes[disposition.Class] = disposition
	}
	if len(classes) != 4 {
		return false
	}
	deferred := make(map[string]struct{})
	for _, evidence := range attestation.DeferredEvidence {
		key := evidence.Class + "\x00" + evidence.RequestID
		if !isWorkClass(evidence.Class) || evidence.RequestID == "" ||
			!digestPattern.MatchString(evidence.RequestDigest) ||
			(evidence.AckID == "") != (evidence.AckDigest == "") ||
			(evidence.AckID != "" && !digestPattern.MatchString(evidence.AckDigest)) {
			return false
		}
		if _, duplicate := deferred[key]; duplicate {
			return false
		}
		deferred[key] = struct{}{}
	}
	for class, disposition := range classes {
		if disposition.Outcome == "deferred" && !hasDeferredEvidence(deferred, class) {
			return false
		}
	}
	return true
}

func hasDeferredEvidence(evidence map[string]struct{}, class string) bool {
	for key := range evidence {
		if strings.HasPrefix(key, class+"\x00") {
			return true
		}
	}
	return false
}

func completeIncumbentQuiescence(operation LaneOperation) bool {
	latest := make(map[string]IncumbentQuiescenceAttestation, len(operation.RequiredSurfaces))
	for _, attestation := range operation.IncumbentQuiescence {
		if current, ok := latest[attestation.Surface]; !ok || attestation.Attempt > current.Attempt {
			latest[attestation.Surface] = attestation
		}
	}
	if len(latest) != len(operation.RequiredSurfaces) {
		return false
	}
	for _, surface := range operation.RequiredSurfaces {
		attestation, ok := latest[surface]
		if !ok || !attestation.ZeroWorkPostflight || attestation.RemainingWork != 0 {
			return false
		}
	}
	return true
}

func validCandidateStage(operation LaneOperation, attestation CandidateStageAttestation) bool {
	return contains(operation.RequiredSurfaces, attestation.Surface) && validEvidenceID(attestation.AttestationID) &&
		attestation.CandidateSetDigest == operation.CandidateSetDigest && attestation.SourceRevision == operation.SourceRevision &&
		attestation.ArtifactDigest == operation.ArtifactDigest && attestation.ManifestDigest == operation.ManifestDigest &&
		attestation.InstalledClosed && digestPattern.MatchString(attestation.EvidenceDigest)
}

func validRecoveryCandidateEvidence(operation LaneOperation) bool {
	recovery := operation.Recovery
	if recovery == nil {
		return false
	}
	target := recoveryOperationContext(operation)
	if len(recovery.CandidateStages) > len(operation.RequiredSurfaces) || len(recovery.CandidateClosed) > len(operation.RequiredSurfaces) {
		return false
	}
	seen := map[string]struct{}{}
	for _, attestation := range recovery.CandidateStages {
		if !validCandidateStage(target, attestation) {
			return false
		}
		if _, duplicate := seen[attestation.Surface]; duplicate {
			return false
		}
		seen[attestation.Surface] = struct{}{}
	}
	seen = map[string]struct{}{}
	for _, attestation := range recovery.CandidateClosed {
		if !validCandidateClosed(target, attestation) || !completeRecoveryCandidateStages(operation) {
			return false
		}
		if _, duplicate := seen[attestation.Surface]; duplicate {
			return false
		}
		seen[attestation.Surface] = struct{}{}
	}
	if stateAtOrAfter(operation, OperationRecoveryCandidateClosed) &&
		(!completeRecoveryCandidateStages(operation) || !completeRecoveryCandidateClosed(operation)) {
		return false
	}
	if !stateAtOrAfter(operation, OperationRecoveryCandidateClosed) &&
		(len(recovery.TransportReady) != 0 || len(recovery.Authorizations) != 0 || len(recovery.OpenAttestations) != 0 || recovery.Acceptance != nil) {
		return false
	}
	if len(recovery.TransportReady) != 0 && !validTransportReadySet(target, recovery.TransportReady) {
		return false
	}
	if len(recovery.Authorizations) != 0 && (!validTransportReadySet(target, recovery.TransportReady) || !validAuthorizationSet(target, recovery.Authorizations)) {
		return false
	}
	if len(recovery.OpenAttestations) != 0 && (!validAuthorizationSet(target, recovery.Authorizations) || !validOpenHistory(target)) {
		return false
	}
	if recovery.Acceptance != nil && (!completeRemoteOpens(target) || !hasMacOpen(target)) {
		return false
	}
	if recovery.Failure != nil && !validStoredFailureEvidence(operation, *recovery.Failure, true) {
		return false
	}
	if historyHasAfter(operation, OperationRecoveryCandidateClosed, OperationReopenPrepared) && !validTransportReadySet(target, recovery.TransportReady) {
		return false
	}
	if historyHasAfter(operation, OperationRecoveryCandidateClosed, OperationReopenAuthorized) && !validAuthorizationSet(target, recovery.Authorizations) {
		return false
	}
	if historyHasAfter(operation, OperationRecoveryCandidateClosed, OperationReopened) && (!completeRemoteOpens(target) || !hasMacOpen(target)) {
		return false
	}
	if historyHasAfter(operation, OperationRecoveryCandidateClosed, OperationAccepted) != (recovery.Acceptance != nil) {
		return false
	}
	return true
}

func completeRecoveryCandidateStages(operation LaneOperation) bool {
	if operation.Recovery == nil {
		return false
	}
	return completeSurfaceSet(operation.RequiredSurfaces, candidateStageSurfaces(operation.Recovery.CandidateStages))
}

func validRecoveryCandidateStage(operation LaneOperation, attestation CandidateStageAttestation) bool {
	if operation.Recovery == nil {
		return false
	}
	return validCandidateStage(recoveryOperationContext(operation), attestation)
}

func validRecoveryCandidateClosed(operation LaneOperation, attestation CandidateClosedAttestation) bool {
	if operation.Recovery == nil {
		return false
	}
	return validCandidateClosed(recoveryOperationContext(operation), attestation)
}

func completeRecoveryCandidateClosed(operation LaneOperation) bool {
	if operation.Recovery == nil {
		return false
	}
	return completeSurfaceSet(operation.RequiredSurfaces, candidateClosedSurfaces(operation.Recovery.CandidateClosed))
}

func historyHasAfter(operation LaneOperation, anchor, target LaneOperationState) bool {
	anchorIndex := -1
	for index, transition := range operation.History {
		if transition.To == anchor {
			anchorIndex = index
			break
		}
	}
	if anchorIndex < 0 {
		return false
	}
	for index := anchorIndex + 1; index < len(operation.History); index++ {
		if operation.History[index].To == target {
			return true
		}
	}
	return false
}

func completeCandidateStages(operation LaneOperation) bool {
	return completeSurfaceSet(operation.RequiredSurfaces, candidateStageSurfaces(operation.CandidateStages))
}

func candidateStageSurfaces(attestations []CandidateStageAttestation) []string {
	surfaces := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		surfaces = append(surfaces, attestation.Surface)
	}
	return surfaces
}

func validCandidateClosed(operation LaneOperation, attestation CandidateClosedAttestation) bool {
	return contains(operation.RequiredSurfaces, attestation.Surface) && validEvidenceID(attestation.AttestationID) &&
		attestation.CandidateSetDigest == operation.CandidateSetDigest && attestation.SourceRevision == operation.SourceRevision &&
		attestation.ArtifactDigest == operation.ArtifactDigest &&
		attestation.ConfigurationDigest == aggregateConfigurationDigest(operation.Candidates) &&
		attestation.ServiceReconciled && attestation.AdmissionClosed && digestPattern.MatchString(attestation.EvidenceDigest)
}

func completeCandidateClosed(operation LaneOperation) bool {
	return completeSurfaceSet(operation.RequiredSurfaces, candidateClosedSurfaces(operation.CandidateClosed))
}

func candidateClosedSurfaces(attestations []CandidateClosedAttestation) []string {
	surfaces := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		surfaces = append(surfaces, attestation.Surface)
	}
	return surfaces
}

func validTransportReadySet(operation LaneOperation, attestations []TransportReadyAttestation) bool {
	if !completeSurfaceSet(operationRemoteSurfaces, transportReadySurfaces(attestations)) {
		return false
	}
	for _, attestation := range attestations {
		if !isRemoteSurface(attestation.Surface) || !validEvidenceID(attestation.AttestationID) ||
			attestation.CandidateSetDigest != operation.CandidateSetDigest || !attestation.RemoteHealthyClosed ||
			!digestPattern.MatchString(attestation.EvidenceDigest) {
			return false
		}
	}
	return true
}

func transportReadySurfaces(attestations []TransportReadyAttestation) []string {
	surfaces := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		surfaces = append(surfaces, attestation.Surface)
	}
	return surfaces
}

func validAuthorizationSet(operation LaneOperation, authorizations []ReopenAuthorization) bool {
	if !completeSurfaceSet(operation.RequiredSurfaces, authorizationSurfaces(authorizations)) {
		return false
	}
	epoch := candidateOperationEpoch(operation)
	for _, authorization := range authorizations {
		if !contains(operation.RequiredSurfaces, authorization.Surface) || !validEvidenceID(authorization.AuthorizationID) ||
			authorization.CandidateSetDigest != operation.CandidateSetDigest || authorization.CandidateEpoch != epoch ||
			!authorization.Fresh || !digestPattern.MatchString(authorization.EvidenceDigest) {
			return false
		}
	}
	return true
}

func authorizationSurfaces(authorizations []ReopenAuthorization) []string {
	surfaces := make([]string, 0, len(authorizations))
	for _, authorization := range authorizations {
		surfaces = append(surfaces, authorization.Surface)
	}
	return surfaces
}

func validOpenAttestation(operation LaneOperation, attestation OpenAttestation, mac bool) bool {
	if attestation.Surface == SurfaceMac != mac || !contains(operation.RequiredSurfaces, attestation.Surface) ||
		!validEvidenceID(attestation.DirectiveID) || !validEvidenceID(attestation.AttestationID) ||
		attestation.DirectiveEpoch != candidateOperationEpoch(operation) || attestation.HighWaterSequence == 0 ||
		attestation.CandidateSetDigest != operation.CandidateSetDigest || !attestation.AdmissionOpen ||
		!digestPattern.MatchString(attestation.EvidenceDigest) {
		return false
	}
	if mac {
		return attestation.AggregateIngress
	}
	return !attestation.AggregateIngress
}

func validOpenHistory(operation LaneOperation) bool {
	if len(operation.OpenAttestations) > len(operation.RequiredSurfaces) {
		return false
	}
	seenSurface, seenDirective, seenAttestation := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	macSeen := false
	for _, attestation := range operation.OpenAttestations {
		mac := attestation.Surface == SurfaceMac
		if !validOpenAttestation(operation, attestation, mac) {
			return false
		}
		if mac {
			if !completeSurfaceSet(operationRemoteSurfaces, mapKeys(seenSurface)) {
				return false
			}
			macSeen = true
		} else if macSeen {
			return false
		}
		if _, ok := seenSurface[attestation.Surface]; ok {
			return false
		}
		if _, ok := seenDirective[attestation.DirectiveID]; ok {
			return false
		}
		if _, ok := seenAttestation[attestation.AttestationID]; ok {
			return false
		}
		seenSurface[attestation.Surface] = struct{}{}
		seenDirective[attestation.DirectiveID] = struct{}{}
		seenAttestation[attestation.AttestationID] = struct{}{}
	}
	return true
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func completeRemoteOpens(operation LaneOperation) bool {
	var surfaces []string
	for _, attestation := range operation.OpenAttestations {
		if isRemoteSurface(attestation.Surface) {
			surfaces = append(surfaces, attestation.Surface)
		}
	}
	return completeSurfaceSet(operationRemoteSurfaces, surfaces)
}

func hasMacOpen(operation LaneOperation) bool {
	for _, attestation := range operation.OpenAttestations {
		if attestation.Surface == SurfaceMac && attestation.AdmissionOpen && attestation.AggregateIngress {
			return true
		}
	}
	return false
}

func validLaneAcceptance(operation LaneOperation, acceptance LaneAcceptanceEvidence) bool {
	return validEvidenceID(acceptance.AcceptanceID) && digestPattern.MatchString(acceptance.EvidenceDigest) &&
		acceptance.TransitionID == operation.ID && acceptance.Lane == operation.Lane &&
		acceptance.CandidateSetDigest == operation.CandidateSetDigest && acceptance.SourceRevision == operation.SourceRevision &&
		acceptance.LiveSuitePassed && acceptance.ZeroWorkPostflight && acceptance.AuthorityVerified
}

func digestCandidateSet(candidates []OperationIdentity) (string, error) {
	data, err := json.Marshal(candidates)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func aggregateConfigurationDigest(candidates []OperationIdentity) string {
	type configIdentity struct {
		InboxID string `json:"inbox_id"`
		Digest  string `json:"configuration_digest"`
	}
	identities := make([]configIdentity, 0, len(candidates))
	for _, candidate := range candidates {
		identities = append(identities, configIdentity{InboxID: candidate.InboxID, Digest: candidate.ConfigurationDigest})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].InboxID < identities[j].InboxID })
	data, _ := json.Marshal(identities)
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func evidenceSetDigest(values any) string {
	data, _ := json.Marshal(values)
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func candidateOperationEpoch(operation LaneOperation) uint64 {
	var epoch uint64
	for _, candidate := range operation.Candidates {
		if candidate.Epoch > epoch {
			epoch = candidate.Epoch
		}
	}
	return epoch
}

func completeSurfaceSet(required, actual []string) bool {
	if len(required) != len(actual) {
		return false
	}
	seen := make(map[string]struct{}, len(actual))
	for _, surface := range actual {
		if _, duplicate := seen[surface]; duplicate {
			return false
		}
		seen[surface] = struct{}{}
	}
	for _, surface := range required {
		if _, exists := seen[surface]; !exists {
			return false
		}
	}
	return true
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func isOperationSurface(surface string) bool {
	return contains(operationSurfaces, surface)
}

func isRemoteSurface(surface string) bool {
	return contains(operationRemoteSurfaces, surface)
}

func isWorkClass(class string) bool {
	switch class {
	case "running", "queued", "resumable", "uncertain":
		return true
	default:
		return false
	}
}

func validWorkOutcome(outcome string) bool {
	switch outcome {
	case "none", "completed", "failed", "deferred", "retained":
		return true
	default:
		return false
	}
}

func validEvidenceID(id string) bool {
	return len(id) > 0 && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

func validClosureHistory(operation LaneOperation) bool {
	if len(operation.IncumbentClosures) > len(operation.RequiredSurfaces)*64 {
		return false
	}
	latestAttempt := map[string]uint64{}
	seenIDs := map[string]struct{}{}
	for _, attestation := range operation.IncumbentClosures {
		if !validClosureAttestation(operation, attestation) || attestation.Attempt <= latestAttempt[attestation.Surface] {
			return false
		}
		latestAttempt[attestation.Surface] = attestation.Attempt
		for _, id := range []string{attestation.DirectiveID, attestation.AttestationID} {
			if _, duplicate := seenIDs[id]; duplicate {
				return false
			}
			seenIDs[id] = struct{}{}
		}
	}
	return true
}

func validQuiescenceHistory(operation LaneOperation) bool {
	if len(operation.IncumbentQuiescence) > len(operation.RequiredSurfaces)*64 {
		return false
	}
	latestAttempt := map[string]uint64{}
	for _, attestation := range operation.IncumbentQuiescence {
		if !validQuiescenceAttestation(attestation) || !contains(operation.RequiredSurfaces, attestation.Surface) ||
			attestation.Attempt <= latestAttempt[attestation.Surface] {
			return false
		}
		latestAttempt[attestation.Surface] = attestation.Attempt
	}
	return true
}

func validCandidateStages(operation LaneOperation) bool {
	if len(operation.CandidateStages) > len(operation.RequiredSurfaces) {
		return false
	}
	seen := map[string]struct{}{}
	for _, attestation := range operation.CandidateStages {
		if !validCandidateStage(operation, attestation) {
			return false
		}
		if _, duplicate := seen[attestation.Surface]; duplicate {
			return false
		}
		seen[attestation.Surface] = struct{}{}
	}
	return true
}

func validCandidateClosures(operation LaneOperation) bool {
	if len(operation.CandidateClosed) > len(operation.RequiredSurfaces) {
		return false
	}
	seen := map[string]struct{}{}
	for _, attestation := range operation.CandidateClosed {
		if !validCandidateClosed(operation, attestation) {
			return false
		}
		if _, duplicate := seen[attestation.Surface]; duplicate {
			return false
		}
		seen[attestation.Surface] = struct{}{}
	}
	return true
}

func validTransportReady(operation LaneOperation) bool {
	if len(operation.TransportReady) > len(operationRemoteSurfaces) {
		return false
	}
	seen := map[string]struct{}{}
	for _, attestation := range operation.TransportReady {
		if !isRemoteSurface(attestation.Surface) || !validEvidenceID(attestation.AttestationID) ||
			attestation.CandidateSetDigest != operation.CandidateSetDigest || !attestation.RemoteHealthyClosed ||
			!digestPattern.MatchString(attestation.EvidenceDigest) {
			return false
		}
		if _, duplicate := seen[attestation.Surface]; duplicate {
			return false
		}
		seen[attestation.Surface] = struct{}{}
	}
	return true
}

func validAuthorizations(operation LaneOperation) bool {
	if len(operation.Authorizations) > len(operation.RequiredSurfaces) {
		return false
	}
	seen := map[string]struct{}{}
	for _, authorization := range operation.Authorizations {
		if !contains(operation.RequiredSurfaces, authorization.Surface) || !validEvidenceID(authorization.AuthorizationID) ||
			authorization.CandidateSetDigest != operation.CandidateSetDigest ||
			authorization.CandidateEpoch != candidateOperationEpoch(operation) || !authorization.Fresh ||
			!digestPattern.MatchString(authorization.EvidenceDigest) {
			return false
		}
		if _, duplicate := seen[authorization.Surface]; duplicate {
			return false
		}
		seen[authorization.Surface] = struct{}{}
	}
	return true
}

func operationEvidenceIDsUnique(operation LaneOperation) bool {
	seen := map[string]struct{}{}
	add := func(id string) bool {
		if id == "" {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
		return true
	}
	if operation.Failure != nil && !add(operation.Failure.FailureID) {
		return false
	}
	if operation.Recovery != nil && (!add(operation.Recovery.ID) || operation.Recovery.ID == operation.ID) {
		return false
	}
	for _, attestation := range operation.IncumbentClosures {
		if !add(attestation.DirectiveID) || !add(attestation.AttestationID) {
			return false
		}
	}
	for _, attestation := range operation.CandidateStages {
		if !add(attestation.AttestationID) {
			return false
		}
	}
	for _, attestation := range operation.CandidateClosed {
		if !add(attestation.AttestationID) {
			return false
		}
	}
	for _, attestation := range operation.TransportReady {
		if !add(attestation.AttestationID) {
			return false
		}
	}
	for _, authorization := range operation.Authorizations {
		if !add(authorization.AuthorizationID) {
			return false
		}
	}
	for _, attestation := range operation.OpenAttestations {
		if !add(attestation.DirectiveID) || !add(attestation.AttestationID) {
			return false
		}
	}
	if operation.Acceptance != nil && !add(operation.Acceptance.AcceptanceID) {
		return false
	}
	for _, attestation := range operation.TargetClosures {
		if !add(attestation.DirectiveID) || !add(attestation.AttestationID) {
			return false
		}
	}
	if operation.Recovery != nil {
		for _, attestation := range operation.Recovery.CandidateStages {
			if !add(attestation.AttestationID) {
				return false
			}
		}
		for _, attestation := range operation.Recovery.CandidateClosed {
			if !add(attestation.AttestationID) {
				return false
			}
		}
		for _, attestation := range operation.Recovery.TransportReady {
			if !add(attestation.AttestationID) {
				return false
			}
		}
		for _, authorization := range operation.Recovery.Authorizations {
			if !add(authorization.AuthorizationID) {
				return false
			}
		}
		for _, attestation := range operation.Recovery.OpenAttestations {
			if !add(attestation.DirectiveID) || !add(attestation.AttestationID) {
				return false
			}
		}
		if operation.Recovery.Acceptance != nil && !add(operation.Recovery.Acceptance.AcceptanceID) {
			return false
		}
		if operation.Recovery.Failure != nil && !add(operation.Recovery.Failure.FailureID) {
			return false
		}
	}
	return true
}
