package laneregistry

import (
	"encoding/json"
	"sort"

	"remote-session-runner/src/internal/config"
)

// PRDOperationKind identifies the separately authorized maintenance or
// recovery operation held by the single durable PRD operation group.
type PRDOperationKind string

const (
	PRDOperationV3Maintenance   PRDOperationKind = "v3_maintenance"
	PRDOperationV3Restore       PRDOperationKind = "v3_restore"
	PRDOperationV3ForwardRepair PRDOperationKind = "v3_forward_repair"
)

type PRDOperationState string

const (
	PRDClosurePending            PRDOperationState = "closure_pending"
	PRDClosedVerified            PRDOperationState = "closed_verified"
	PRDQuiescing                 PRDOperationState = "quiescing"
	PRDQuiesced                  PRDOperationState = "quiesced"
	PRDRestoreRequired           PRDOperationState = "restore_required"
	PRDStaged                    PRDOperationState = "staged"
	PRDCandidateClosed           PRDOperationState = "candidate_closed"
	PRDCommitted                 PRDOperationState = "committed"
	PRDReopenPrepared            PRDOperationState = "reopen_prepared"
	PRDReopenAuthorized          PRDOperationState = "reopen_authorized"
	PRDReopened                  PRDOperationState = "reopened"
	PRDAcceptancePending         PRDOperationState = "acceptance_pending"
	PRDAccepted                  PRDOperationState = "accepted"
	PRDRecoveryCandidateClosing  PRDOperationState = "recovery_candidate_closing"
	PRDRecoveryCandidateClosed   PRDOperationState = "recovery_candidate_closed"
	PRDRecoveryReopenPrepared    PRDOperationState = "recovery_reopen_prepared"
	PRDRecoveryReopenAuthorized  PRDOperationState = "recovery_reopen_authorized"
	PRDRecoveryReopened          PRDOperationState = "recovery_reopened"
	PRDRecoveryAcceptancePending PRDOperationState = "recovery_acceptance_pending"
	PRDRecoveryAccepted          PRDOperationState = "recovery_accepted"
	PRDOperationAborted          PRDOperationState = "aborted"
	PRDOperationArchived         PRDOperationState = "archived"
)

// PRDHostEpoch records the externally observed anchor floor and the fresh
// closed and candidate-admission epochs selected for one host surface. The
// registry validates order; authority signatures and anchors are verified by
// their separate owners before callers submit these facts.
type PRDHostEpoch struct {
	Surface         string `json:"surface"`
	AnchorHighWater uint64 `json:"anchor_high_water"`
	ClosureEpoch    uint64 `json:"closure_epoch"`
	CandidateEpoch  uint64 `json:"candidate_epoch"`
}

type PRDClosureProof struct {
	Surface        string `json:"surface"`
	DirectiveID    string `json:"directive_id"`
	Epoch          uint64 `json:"epoch"`
	AttestationID  string `json:"attestation_id"`
	Closed         bool   `json:"closed"`
	EvidenceDigest string `json:"evidence_digest"`
}

type PRDQuiescenceProof struct {
	Surface            string                 `json:"surface"`
	CandidateSetDigest string                 `json:"candidate_set_digest"`
	Attempt            uint64                 `json:"attempt"`
	InventoryDigest    string                 `json:"inventory_digest"`
	Dispositions       []WorkDisposition      `json:"dispositions"`
	DeferredEvidence   []DeferredWorkEvidence `json:"deferred_evidence"`
	RemainingWork      uint64                 `json:"remaining_work"`
	ZeroWorkPostflight bool                   `json:"zero_work_postflight"`
	PostflightDigest   string                 `json:"postflight_digest"`
	EvidenceDigest     string                 `json:"evidence_digest"`
}

type PRDRecoveryClosedProof struct {
	Surface            string `json:"surface"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	CandidateEpoch     uint64 `json:"candidate_epoch"`
	AttestationID      string `json:"attestation_id"`
	ServiceReconciled  bool   `json:"service_reconciled"`
	AdmissionClosed    bool   `json:"admission_closed"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type PRDTransportReadyProof struct {
	Surface             string `json:"surface"`
	CandidateSetDigest  string `json:"candidate_set_digest"`
	AttestationID       string `json:"attestation_id"`
	RemoteHealthyClosed bool   `json:"remote_healthy_closed"`
	EvidenceDigest      string `json:"evidence_digest"`
}

type PRDAuthorizationProof struct {
	Surface            string `json:"surface"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	CandidateEpoch     uint64 `json:"candidate_epoch"`
	AuthorizationID    string `json:"authorization_id"`
	Fresh              bool   `json:"fresh"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type PRDRecoveryOpenProof struct {
	Surface            string `json:"surface"`
	CandidateSetDigest string `json:"candidate_set_digest"`
	CandidateEpoch     uint64 `json:"candidate_epoch"`
	DirectiveID        string `json:"directive_id"`
	AttestationID      string `json:"attestation_id"`
	AuthorizationID    string `json:"authorization_id"`
	AdmissionOpen      bool   `json:"admission_open"`
	AggregateIngress   bool   `json:"aggregate_ingress,omitempty"`
	EvidenceDigest     string `json:"evidence_digest"`
}

type PRDOperationTransition struct {
	Sequence       uint64            `json:"sequence"`
	From           PRDOperationState `json:"from,omitempty"`
	To             PRDOperationState `json:"to"`
	EvidenceDigest string            `json:"evidence_digest,omitempty"`
}

// PRDOperation is one immutable maintenance parent or one explicitly typed,
// linked recovery child. It contains evidence references and does not execute
// host commands or authorize a service mutation.
type PRDOperation struct {
	ID                           string                   `json:"id"`
	ParentID                     string                   `json:"parent_id,omitempty"`
	Kind                         PRDOperationKind         `json:"kind"`
	State                        PRDOperationState        `json:"state"`
	CandidateSetDigest           string                   `json:"candidate_set_digest"`
	IncumbentCandidateSetDigest  string                   `json:"incumbent_candidate_set_digest,omitempty"`
	SourceRevision               string                   `json:"source_revision"`
	ArtifactDigest               string                   `json:"artifact_digest"`
	ManifestDigest               string                   `json:"manifest_digest"`
	Epochs                       []PRDHostEpoch           `json:"epochs"`
	Closures                     []PRDClosureProof        `json:"closures,omitempty"`
	Quiescence                   []PRDQuiescenceProof     `json:"quiescence,omitempty"`
	QuiescenceSealDigest         string                   `json:"quiescence_seal_digest,omitempty"`
	FailureDigest                string                   `json:"failure_digest,omitempty"`
	RecoveryChildIDs             []string                 `json:"recovery_child_ids,omitempty"`
	LineageEvidenceDigest        string                   `json:"lineage_evidence_digest,omitempty"`
	AuthorityApprovalDigest      string                   `json:"authority_approval_digest,omitempty"`
	QASAcceptanceDigest          string                   `json:"qas_acceptance_digest,omitempty"`
	HostManagementManifestDigest string                   `json:"host_management_manifest_digest,omitempty"`
	RecoveryClosed               []PRDRecoveryClosedProof `json:"recovery_closed,omitempty"`
	TransportReady               []PRDTransportReadyProof `json:"transport_ready,omitempty"`
	Authorizations               []PRDAuthorizationProof  `json:"authorizations,omitempty"`
	RecoveryOpen                 []PRDRecoveryOpenProof   `json:"recovery_open,omitempty"`
	StageEvidenceDigest          string                   `json:"stage_evidence_digest,omitempty"`
	CommitEvidenceDigest         string                   `json:"commit_evidence_digest,omitempty"`
	Acceptance                   *LaneAcceptanceEvidence  `json:"acceptance,omitempty"`
	History                      []PRDOperationTransition `json:"history"`
}

// PRDOperationGroup is the durable exclusive-operation lock. A parent and at
// most one unarchived recovery child may be active at once. Archived operations
// remain attached as retained history and contribute to epoch high-water.
type PRDOperationGroup struct {
	ID                 string         `json:"id"`
	PrimaryOperationID string         `json:"primary_operation_id"`
	ActiveOperationID  string         `json:"active_operation_id,omitempty"`
	Operations         []PRDOperation `json:"operations"`
}

type PRDMaintenanceSpec struct {
	CandidateSetDigest          string
	IncumbentCandidateSetDigest string
	SourceRevision              string
	ArtifactDigest              string
	ManifestDigest              string
	Epochs                      []PRDHostEpoch
}

type PRDRecoverySpec struct {
	ParentID                     string
	Kind                         PRDOperationKind
	CandidateSetDigest           string
	SourceRevision               string
	ArtifactDigest               string
	ManifestDigest               string
	Epochs                       []PRDHostEpoch
	LineageEvidenceDigest        string
	AuthorityApprovalDigest      string
	QASAcceptanceDigest          string
	HostManagementManifestDigest string
}

// BeginPRDMaintenance acquires the one PRD operation group and records the
// incumbent closure and target admission epochs before any host action.
func (r *Registry) BeginPRDMaintenance(expectedGeneration uint64, spec PRDMaintenanceSpec) (Snapshot, PRDOperationGroup, error) {
	operationID, err := NewLaneTransitionID()
	if err != nil {
		return Snapshot{}, PRDOperationGroup{}, err
	}
	operation := PRDOperation{
		ID: operationID, Kind: PRDOperationV3Maintenance, State: PRDClosurePending,
		CandidateSetDigest: spec.CandidateSetDigest, IncumbentCandidateSetDigest: spec.IncumbentCandidateSetDigest,
		SourceRevision: spec.SourceRevision, ArtifactDigest: spec.ArtifactDigest,
		ManifestDigest: spec.ManifestDigest, Epochs: clonePRDEpochs(spec.Epochs),
	}
	if err := validatePRDMaintenanceSpec(spec); err != nil {
		return Snapshot{}, PRDOperationGroup{}, err
	}
	if err := appendPRDHistory(&operation, PRDClosurePending, ""); err != nil {
		return Snapshot{}, PRDOperationGroup{}, err
	}
	group := PRDOperationGroup{ID: operationID, PrimaryOperationID: operationID, ActiveOperationID: operationID, Operations: []PRDOperation{operation}}
	snapshot, err := r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		if hasActivePRDOperationGroup(snapshot) || hasActivePRDMigrationFence(snapshot) {
			return false, ErrOperationBusy
		}
		snapshot.PRDOperationGroups = append(snapshot.PRDOperationGroups, group)
		return true, nil
	})
	if err != nil {
		return Snapshot{}, PRDOperationGroup{}, err
	}
	stored, ok := prdOperationGroupByID(snapshot.PRDOperationGroups, operationID)
	if !ok {
		return Snapshot{}, PRDOperationGroup{}, ErrInvalidRegistry
	}
	return snapshot, clonePRDOperationGroup(stored), nil
}

// RecordPRDMaintenanceClosure records one exact closed host surface. A partial
// fanout cannot advance the group to quiescence.
func (r *Registry) RecordPRDMaintenanceClosure(expectedGeneration uint64, operationID string, proof PRDClosureProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(group *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDClosurePending || !validPRDClosureProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.Closures {
			if prior.Surface == proof.Surface || prior.DirectiveID == proof.DirectiveID || prior.AttestationID == proof.AttestationID {
				return false, ErrCollision
			}
		}
		operation.Closures = append(operation.Closures, proof)
		sort.Slice(operation.Closures, func(i, j int) bool { return operation.Closures[i].Surface < operation.Closures[j].Surface })
		return true, nil
	})
}

func (r *Registry) CompletePRDMaintenanceClosure(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDClosurePending || !completePRDClosures(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		return true, appendPRDHistory(operation, PRDClosedVerified, evidenceDigest)
	})
}

func (r *Registry) BeginPRDMaintenanceQuiescence(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDClosedVerified || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		return true, appendPRDHistory(operation, PRDQuiescing, evidenceDigest)
	})
}

func (r *Registry) RecordPRDMaintenanceQuiescence(expectedGeneration uint64, operationID string, proof PRDQuiescenceProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDQuiescing || !validPRDQuiescenceProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.Quiescence {
			if prior.Surface == proof.Surface {
				return false, ErrCollision
			}
		}
		operation.Quiescence = append(operation.Quiescence, proof)
		sort.Slice(operation.Quiescence, func(i, j int) bool { return operation.Quiescence[i].Surface < operation.Quiescence[j].Surface })
		return true, nil
	})
}

func (r *Registry) CompletePRDMaintenanceQuiescence(expectedGeneration uint64, operationID, sealDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDQuiescing || !completePRDQuiescence(*operation) || !validEvidenceDigest(sealDigest) {
			return false, ErrInvalidTransition
		}
		operation.QuiescenceSealDigest = sealDigest
		return true, appendPRDHistory(operation, PRDQuiesced, sealDigest)
	})
}

// FailPRDMaintenance can create restore_required only after the incumbent is
// fully closed and quiesced. It deliberately has no direct reopen or archive
// transition.
func (r *Registry) FailPRDMaintenance(expectedGeneration uint64, operationID, failureDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance ||
			(operation.State != PRDQuiesced && operation.State != PRDStaged && operation.State != PRDCandidateClosed) ||
			!validEvidenceDigest(failureDigest) {
			return false, ErrInvalidTransition
		}
		operation.FailureDigest = failureDigest
		return true, appendPRDHistory(operation, PRDRestoreRequired, failureDigest)
	})
}

func (r *Registry) StagePRDMaintenance(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDQuiesced || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		operation.StageEvidenceDigest = evidenceDigest
		return true, appendPRDHistory(operation, PRDStaged, evidenceDigest)
	})
}

func (r *Registry) RecordPRDMaintenanceCandidateClosed(expectedGeneration uint64, operationID string, proof PRDRecoveryClosedProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDStaged || !validPRDRecoveryClosedProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		if err := appendUniquePRDClosedProof(operation, proof); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (r *Registry) CompletePRDMaintenanceCandidateClosed(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDStaged || !completePRDRecoveryClosed(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		return true, appendPRDHistory(operation, PRDCandidateClosed, evidenceDigest)
	})
}

func (r *Registry) CommitPRDMaintenance(expectedGeneration uint64, operationID, commitEvidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDCandidateClosed || !validEvidenceDigest(commitEvidenceDigest) {
			return false, ErrInvalidTransition
		}
		operation.CommitEvidenceDigest = commitEvidenceDigest
		return true, appendPRDHistory(operation, PRDCommitted, commitEvidenceDigest)
	})
}

// BeginPRDRecoveryChild adds one typed child under the held parent group.
// Restore requires the exact prior accepted candidate lineage. Forward repair
// requires explicit authority, QAS acceptance, and the exact host manifest.
func (r *Registry) BeginPRDRecoveryChild(expectedGeneration uint64, spec PRDRecoverySpec) (Snapshot, PRDOperation, error) {
	childID, err := NewLaneTransitionID()
	if err != nil {
		return Snapshot{}, PRDOperation{}, err
	}
	child := PRDOperation{
		ID: childID, ParentID: spec.ParentID, Kind: spec.Kind, State: PRDRecoveryCandidateClosing,
		CandidateSetDigest: spec.CandidateSetDigest, SourceRevision: spec.SourceRevision,
		ArtifactDigest: spec.ArtifactDigest, ManifestDigest: spec.ManifestDigest, Epochs: clonePRDEpochs(spec.Epochs),
		LineageEvidenceDigest: spec.LineageEvidenceDigest, AuthorityApprovalDigest: spec.AuthorityApprovalDigest,
		QASAcceptanceDigest: spec.QASAcceptanceDigest, HostManagementManifestDigest: spec.HostManagementManifestDigest,
	}
	if err := validatePRDRecoverySpecShape(spec); err != nil {
		return Snapshot{}, PRDOperation{}, err
	}
	if err := appendPRDHistory(&child, PRDRecoveryCandidateClosing, ""); err != nil {
		return Snapshot{}, PRDOperation{}, err
	}
	snapshot, err := r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		group, ok := prdOperationGroupByIDMutable(&snapshot.PRDOperationGroups, spec.ParentID)
		if !ok {
			return false, ErrRecordNotFound
		}
		parent, ok := prdOperationByIDMutable(&group.Operations, group.PrimaryOperationID)
		if !ok || parent.State != PRDRestoreRequired {
			return false, ErrInvalidTransition
		}
		if group.ActiveOperationID != parent.ID {
			return false, ErrOperationBusy
		}
		for _, id := range parent.RecoveryChildIDs {
			prior, _ := prdOperationByIDMutable(&group.Operations, id)
			if prior == nil || prior.State != PRDOperationArchived {
				return false, ErrOperationBusy
			}
		}
		if !validPRDRecoverySpec(*parent, *group, spec) {
			return false, ErrInvalidTransition
		}
		parent.RecoveryChildIDs = append(parent.RecoveryChildIDs, childID)
		group.Operations = append(group.Operations, child)
		group.ActiveOperationID = childID
		return true, nil
	})
	if err != nil {
		return Snapshot{}, PRDOperation{}, err
	}
	group, ok := prdOperationGroupByID(snapshot.PRDOperationGroups, spec.ParentID)
	if !ok {
		return Snapshot{}, PRDOperation{}, ErrInvalidRegistry
	}
	stored, ok := prdOperationByID(group.Operations, childID)
	if !ok {
		return Snapshot{}, PRDOperation{}, ErrInvalidRegistry
	}
	return snapshot, clonePRDOperation(stored), nil
}

func (r *Registry) RecordPRDRecoveryCandidateClosed(expectedGeneration uint64, operationID string, proof PRDRecoveryClosedProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.ParentID == "" || operation.State != PRDRecoveryCandidateClosing || !validPRDRecoveryClosedProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.RecoveryClosed {
			if prior.Surface == proof.Surface || prior.AttestationID == proof.AttestationID {
				return false, ErrCollision
			}
		}
		operation.RecoveryClosed = append(operation.RecoveryClosed, proof)
		sort.Slice(operation.RecoveryClosed, func(i, j int) bool { return operation.RecoveryClosed[i].Surface < operation.RecoveryClosed[j].Surface })
		return true, nil
	})
}

func (r *Registry) CompletePRDRecoveryCandidateClosed(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.ParentID == "" || operation.State != PRDRecoveryCandidateClosing || !completePRDRecoveryClosed(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		return true, appendPRDHistory(operation, PRDRecoveryCandidateClosed, evidenceDigest)
	})
}

func (r *Registry) RecordPRDTransportReady(expectedGeneration uint64, operationID string, proof PRDTransportReadyProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDCommitted) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryCandidateClosed)
		if !validState || !validPRDTransportReadyProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.TransportReady {
			if prior.Surface == proof.Surface || prior.AttestationID == proof.AttestationID {
				return false, ErrCollision
			}
		}
		operation.TransportReady = append(operation.TransportReady, proof)
		sort.Slice(operation.TransportReady, func(i, j int) bool { return operation.TransportReady[i].Surface < operation.TransportReady[j].Surface })
		return true, nil
	})
}

func (r *Registry) PreparePRDReopen(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDCommitted) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryCandidateClosed)
		if !validState || !completePRDTransportReady(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		next := PRDReopenPrepared
		if operation.ParentID != "" {
			next = PRDRecoveryReopenPrepared
		}
		return true, appendPRDHistory(operation, next, evidenceDigest)
	})
}

func (r *Registry) RecordPRDAuthorization(expectedGeneration uint64, operationID string, proof PRDAuthorizationProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDReopenPrepared) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryReopenPrepared)
		if !validState || !validPRDAuthorizationProof(*operation, proof) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.Authorizations {
			if prior.Surface == proof.Surface || prior.AuthorizationID == proof.AuthorizationID {
				return false, ErrCollision
			}
		}
		operation.Authorizations = append(operation.Authorizations, proof)
		sort.Slice(operation.Authorizations, func(i, j int) bool { return operation.Authorizations[i].Surface < operation.Authorizations[j].Surface })
		return true, nil
	})
}

func (r *Registry) AuthorizePRDReopen(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDReopenPrepared) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryReopenPrepared)
		if !validState || !completePRDAuthorizations(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		next := PRDReopenAuthorized
		if operation.ParentID != "" {
			next = PRDRecoveryReopenAuthorized
		}
		return true, appendPRDHistory(operation, next, evidenceDigest)
	})
}

func (r *Registry) RecordPRDRecoveryOpen(expectedGeneration uint64, operationID string, proof PRDRecoveryOpenProof) (Snapshot, error) {
	return r.RecordPRDOpen(expectedGeneration, operationID, proof)
}

func (r *Registry) CompletePRDRecoveryOpen(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.CompletePRDOpen(expectedGeneration, operationID, evidenceDigest)
}

func (r *Registry) RecordPRDMaintenanceOpen(expectedGeneration uint64, operationID string, proof PRDRecoveryOpenProof) (Snapshot, error) {
	return r.RecordPRDOpen(expectedGeneration, operationID, proof)
}

func (r *Registry) RecordPRDOpen(expectedGeneration uint64, operationID string, proof PRDRecoveryOpenProof) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDReopenAuthorized) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryReopenAuthorized)
		if !validState || !validPRDRecoveryOpenProof(*operation, proof) || !remoteOpenPrecedesMac(*operation, proof.Surface) {
			return false, ErrInvalidTransition
		}
		for _, prior := range operation.RecoveryOpen {
			if prior.Surface == proof.Surface || prior.DirectiveID == proof.DirectiveID || prior.AttestationID == proof.AttestationID {
				return false, ErrCollision
			}
		}
		operation.RecoveryOpen = append(operation.RecoveryOpen, proof)
		sort.Slice(operation.RecoveryOpen, func(i, j int) bool { return operation.RecoveryOpen[i].Surface < operation.RecoveryOpen[j].Surface })
		return true, nil
	})
}

func (r *Registry) CompletePRDMaintenanceOpen(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.CompletePRDOpen(expectedGeneration, operationID, evidenceDigest)
}

func (r *Registry) CompletePRDOpen(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDReopenAuthorized) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryReopenAuthorized)
		if !validState || !completePRDRecoveryOpen(*operation) || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		next := PRDReopened
		if operation.ParentID != "" {
			next = PRDRecoveryReopened
		}
		return true, appendPRDHistory(operation, next, evidenceDigest)
	})
}

func (r *Registry) BeginPRDRecoveryAcceptance(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.BeginPRDAcceptance(expectedGeneration, operationID, evidenceDigest)
}

func (r *Registry) BeginPRDAcceptance(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(_ *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDReopened) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryReopened)
		if !validState || !validEvidenceDigest(evidenceDigest) {
			return false, ErrInvalidTransition
		}
		next := PRDAcceptancePending
		if operation.ParentID != "" {
			next = PRDRecoveryAcceptancePending
		}
		return true, appendPRDHistory(operation, next, evidenceDigest)
	})
}

func (r *Registry) RecordPRDRecoveryAcceptance(expectedGeneration uint64, operationID string, acceptance LaneAcceptanceEvidence) (Snapshot, error) {
	return r.RecordPRDAcceptance(expectedGeneration, operationID, acceptance)
}

func (r *Registry) RecordPRDAcceptance(expectedGeneration uint64, operationID string, acceptance LaneAcceptanceEvidence) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(group *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		validState := (operation.Kind == PRDOperationV3Maintenance && operation.State == PRDAcceptancePending) ||
			(operation.ParentID != "" && operation.State == PRDRecoveryAcceptancePending)
		if !validState || !validPRDRecoveryAcceptance(*operation, *group, acceptance) {
			return false, ErrInvalidTransition
		}
		operation.Acceptance = &acceptance
		next := PRDAccepted
		if operation.ParentID != "" {
			next = PRDRecoveryAccepted
		}
		return true, appendPRDHistory(operation, next, acceptance.EvidenceDigest)
	})
}

func (r *Registry) ArchivePRDMaintenance(expectedGeneration uint64, operationID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, operationID, func(group *PRDOperationGroup, operation *PRDOperation) (bool, error) {
		if operation.Kind != PRDOperationV3Maintenance || operation.State != PRDAccepted || operation.Acceptance == nil || !validEvidenceDigest(evidenceDigest) ||
			group.ActiveOperationID != operation.ID {
			return false, ErrInvalidTransition
		}
		if err := appendPRDHistory(operation, PRDOperationArchived, evidenceDigest); err != nil {
			return false, err
		}
		group.ActiveOperationID = ""
		return true, nil
	})
}

// CompletePRDRecovery archives the accepted child and failed parent in one
// registry compare-and-swap; only then is the PRD group released.
func (r *Registry) CompletePRDRecovery(expectedGeneration uint64, childID string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, childID, func(group *PRDOperationGroup, child *PRDOperation) (bool, error) {
		if child.ParentID == "" || child.State != PRDRecoveryAccepted || child.Acceptance == nil || group.ActiveOperationID != child.ID {
			return false, ErrInvalidTransition
		}
		parent, ok := prdOperationByIDMutable(&group.Operations, child.ParentID)
		if !ok || parent.State != PRDRestoreRequired || !containsPRDString(parent.RecoveryChildIDs, child.ID) {
			return false, ErrInvalidTransition
		}
		if err := appendPRDHistory(child, PRDOperationArchived, child.Acceptance.EvidenceDigest); err != nil {
			return false, err
		}
		if err := appendPRDHistory(parent, PRDOperationAborted, child.Acceptance.EvidenceDigest); err != nil {
			return false, err
		}
		if err := appendPRDHistory(parent, PRDOperationArchived, child.Acceptance.EvidenceDigest); err != nil {
			return false, err
		}
		group.ActiveOperationID = ""
		return true, nil
	})
}

func (r *Registry) AbortPRDRecoveryChild(expectedGeneration uint64, childID, failureDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, childID, func(_ *PRDOperationGroup, child *PRDOperation) (bool, error) {
		if child.ParentID == "" || child.State != PRDRecoveryCandidateClosed || !completePRDRecoveryClosed(*child) || !validEvidenceDigest(failureDigest) {
			return false, ErrInvalidTransition
		}
		child.FailureDigest = failureDigest
		return true, appendPRDHistory(child, PRDOperationAborted, failureDigest)
	})
}

func (r *Registry) ArchiveAbortedPRDRecoveryChild(expectedGeneration uint64, childID, evidenceDigest string) (Snapshot, error) {
	return r.updatePRDOperation(expectedGeneration, childID, func(group *PRDOperationGroup, child *PRDOperation) (bool, error) {
		if child.ParentID == "" || child.State != PRDOperationAborted || !validEvidenceDigest(evidenceDigest) ||
			evidenceDigest != child.FailureDigest || group.ActiveOperationID != child.ID {
			return false, ErrInvalidTransition
		}
		parent, ok := prdOperationByIDMutable(&group.Operations, child.ParentID)
		if !ok || parent.State != PRDRestoreRequired {
			return false, ErrInvalidTransition
		}
		if err := appendPRDHistory(child, PRDOperationArchived, evidenceDigest); err != nil {
			return false, err
		}
		group.ActiveOperationID = parent.ID
		return true, nil
	})
}

func (r *Registry) updatePRDOperation(expectedGeneration uint64, operationID string, mutate func(*PRDOperationGroup, *PRDOperation) (bool, error)) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		for groupIndex := range snapshot.PRDOperationGroups {
			group := &snapshot.PRDOperationGroups[groupIndex]
			for operationIndex := range group.Operations {
				operation := &group.Operations[operationIndex]
				if operation.ID == operationID {
					return mutate(group, operation)
				}
			}
		}
		return false, ErrRecordNotFound
	})
}

func validatePRDMaintenanceSpec(spec PRDMaintenanceSpec) error {
	if !validEvidenceDigest(spec.CandidateSetDigest) || !validEvidenceDigest(spec.IncumbentCandidateSetDigest) ||
		spec.CandidateSetDigest == spec.IncumbentCandidateSetDigest || !commitIDPattern.MatchString(spec.SourceRevision) ||
		!validEvidenceDigest(spec.ArtifactDigest) || !validEvidenceDigest(spec.ManifestDigest) || !validPRDEpochs(spec.Epochs, true) {
		return ErrInvalidOperation
	}
	return nil
}

func validatePRDRecoverySpecShape(spec PRDRecoverySpec) error {
	if !operationIDPattern.MatchString(spec.ParentID) || (spec.Kind != PRDOperationV3Restore && spec.Kind != PRDOperationV3ForwardRepair) ||
		!validEvidenceDigest(spec.CandidateSetDigest) || !commitIDPattern.MatchString(spec.SourceRevision) ||
		!validEvidenceDigest(spec.ArtifactDigest) || !validEvidenceDigest(spec.ManifestDigest) || !validPRDEpochs(spec.Epochs, true) {
		return ErrInvalidOperation
	}
	if spec.Kind == PRDOperationV3Restore {
		if !validEvidenceDigest(spec.LineageEvidenceDigest) || spec.AuthorityApprovalDigest != "" || spec.QASAcceptanceDigest != "" || spec.HostManagementManifestDigest != "" {
			return ErrInvalidOperation
		}
	} else if !validEvidenceDigest(spec.AuthorityApprovalDigest) || !validEvidenceDigest(spec.QASAcceptanceDigest) ||
		!validEvidenceDigest(spec.HostManagementManifestDigest) || spec.LineageEvidenceDigest != "" {
		return ErrInvalidOperation
	}
	return nil
}

func validPRDRecoverySpec(parent PRDOperation, group PRDOperationGroup, spec PRDRecoverySpec) bool {
	if parent.ID != spec.ParentID || parent.State != PRDRestoreRequired || parent.QuiescenceSealDigest == "" ||
		parent.FailureDigest == "" || !validEvidenceDigest(parent.QuiescenceSealDigest) || !validEvidenceDigest(parent.FailureDigest) ||
		!samePRDSurfaces(parent.Epochs, spec.Epochs) {
		return false
	}
	if spec.Kind == PRDOperationV3Restore {
		if spec.CandidateSetDigest != parent.IncumbentCandidateSetDigest || !validEvidenceDigest(spec.LineageEvidenceDigest) {
			return false
		}
	} else if spec.CandidateSetDigest == parent.IncumbentCandidateSetDigest || spec.CandidateSetDigest == parent.CandidateSetDigest ||
		!validEvidenceDigest(spec.AuthorityApprovalDigest) || !validEvidenceDigest(spec.QASAcceptanceDigest) ||
		!validEvidenceDigest(spec.HostManagementManifestDigest) {
		return false
	}
	for i, epoch := range spec.Epochs {
		floor := epoch.AnchorHighWater
		for _, prior := range group.Operations {
			if prior.ID == parent.ID || prior.ParentID == parent.ID {
				if i < len(prior.Epochs) && prior.Epochs[i].CandidateEpoch > floor {
					floor = prior.Epochs[i].CandidateEpoch
				}
			}
		}
		if epoch.ClosureEpoch <= floor || epoch.CandidateEpoch <= epoch.ClosureEpoch {
			return false
		}
	}
	return true
}

func validPRDEpochs(epochs []PRDHostEpoch, requireClosure bool) bool {
	if len(epochs) != len(operationSurfaces) {
		return false
	}
	for i, surface := range operationSurfaces {
		epoch := epochs[i]
		if epoch.Surface != surface || epoch.CandidateEpoch == 0 || epoch.CandidateEpoch <= epoch.ClosureEpoch ||
			(requireClosure && epoch.ClosureEpoch <= epoch.AnchorHighWater) {
			return false
		}
	}
	return true
}

func validPRDClosureProof(operation PRDOperation, proof PRDClosureProof) bool {
	epoch, ok := prdEpochForSurface(operation.Epochs, proof.Surface)
	return ok && proof.Closed && proof.Epoch == epoch.ClosureEpoch && proof.DirectiveID != "" && proof.AttestationID != "" && validEvidenceDigest(proof.EvidenceDigest)
}

func validPRDQuiescenceProof(operation PRDOperation, proof PRDQuiescenceProof) bool {
	attestation := IncumbentQuiescenceAttestation{
		Surface: proof.Surface, Attempt: proof.Attempt, InventoryDigest: proof.InventoryDigest,
		Dispositions: proof.Dispositions, DeferredEvidence: proof.DeferredEvidence,
		RemainingWork: proof.RemainingWork, ZeroWorkPostflight: proof.ZeroWorkPostflight,
		PostflightDigest: proof.PostflightDigest,
	}
	return proof.CandidateSetDigest == operation.IncumbentCandidateSetDigest && validQuiescenceAttestation(attestation) &&
		proof.RemainingWork == 0 && proof.ZeroWorkPostflight && validEvidenceDigest(proof.EvidenceDigest)
}

func validPRDRecoveryClosedProof(operation PRDOperation, proof PRDRecoveryClosedProof) bool {
	epoch, ok := prdEpochForSurface(operation.Epochs, proof.Surface)
	return ok && proof.CandidateSetDigest == operation.CandidateSetDigest && proof.CandidateEpoch == epoch.CandidateEpoch &&
		proof.AttestationID != "" && proof.ServiceReconciled && proof.AdmissionClosed && validEvidenceDigest(proof.EvidenceDigest)
}

func validPRDRecoveryOpenProof(operation PRDOperation, proof PRDRecoveryOpenProof) bool {
	epoch, ok := prdEpochForSurface(operation.Epochs, proof.Surface)
	if !ok || proof.CandidateSetDigest != operation.CandidateSetDigest || proof.CandidateEpoch != epoch.CandidateEpoch ||
		proof.DirectiveID == "" || proof.AttestationID == "" || proof.AuthorizationID == "" || !proof.AdmissionOpen || !validEvidenceDigest(proof.EvidenceDigest) {
		return false
	}
	authorization, exists := prdAuthorizationForSurface(operation.Authorizations, proof.Surface)
	if !exists || !authorization.Fresh || authorization.AuthorizationID != proof.AuthorizationID {
		return false
	}
	return proof.Surface != SurfaceMac || proof.AggregateIngress
}

func validPRDTransportReadyProof(operation PRDOperation, proof PRDTransportReadyProof) bool {
	if proof.Surface == SurfaceMac || !containsPRDRemoteSurface(proof.Surface) || proof.CandidateSetDigest != operation.CandidateSetDigest ||
		proof.AttestationID == "" || !proof.RemoteHealthyClosed || !validEvidenceDigest(proof.EvidenceDigest) {
		return false
	}
	return true
}

func validPRDAuthorizationProof(operation PRDOperation, proof PRDAuthorizationProof) bool {
	epoch, ok := prdEpochForSurface(operation.Epochs, proof.Surface)
	return ok && proof.CandidateSetDigest == operation.CandidateSetDigest && proof.CandidateEpoch == epoch.CandidateEpoch &&
		proof.AuthorizationID != "" && proof.Fresh && validEvidenceDigest(proof.EvidenceDigest)
}

func validPRDRecoveryAcceptance(operation PRDOperation, group PRDOperationGroup, acceptance LaneAcceptanceEvidence) bool {
	if acceptance.AcceptanceID == "" || !validEvidenceDigest(acceptance.EvidenceDigest) || acceptance.TransitionID != operation.ID ||
		acceptance.Lane != config.ReleaseLanePRD || acceptance.CandidateSetDigest != operation.CandidateSetDigest ||
		acceptance.SourceRevision != operation.SourceRevision || !commitIDPattern.MatchString(acceptance.SourceRevision) || !acceptance.LiveSuitePassed ||
		!acceptance.ZeroWorkPostflight || !acceptance.AuthorityVerified {
		return false
	}
	for _, prior := range group.Operations {
		if prior.Acceptance != nil && prior.Acceptance.AcceptanceID == acceptance.AcceptanceID {
			return false
		}
	}
	return true
}

func validPRDAcceptanceForOperation(operation PRDOperation) bool {
	if operation.Acceptance == nil {
		return false
	}
	acceptance := operation.Acceptance
	return acceptance.AcceptanceID != "" && validEvidenceDigest(acceptance.EvidenceDigest) && acceptance.TransitionID == operation.ID &&
		acceptance.Lane == config.ReleaseLanePRD && acceptance.CandidateSetDigest == operation.CandidateSetDigest &&
		acceptance.SourceRevision == operation.SourceRevision && commitIDPattern.MatchString(acceptance.SourceRevision) &&
		acceptance.LiveSuitePassed && acceptance.ZeroWorkPostflight && acceptance.AuthorityVerified
}

func completePRDClosures(operation PRDOperation) bool {
	if len(operation.Closures) != len(operationSurfaces) {
		return false
	}
	for i, surface := range operationSurfaces {
		if operation.Closures[i].Surface != surface || !validPRDClosureProof(operation, operation.Closures[i]) {
			return false
		}
	}
	return true
}

func completePRDQuiescence(operation PRDOperation) bool {
	if len(operation.Quiescence) != len(operationSurfaces) {
		return false
	}
	for i, surface := range operationSurfaces {
		if operation.Quiescence[i].Surface != surface || !validPRDQuiescenceProof(operation, operation.Quiescence[i]) {
			return false
		}
	}
	return true
}

func completePRDRecoveryClosed(operation PRDOperation) bool {
	if len(operation.RecoveryClosed) != len(operationSurfaces) {
		return false
	}
	for i, surface := range operationSurfaces {
		if operation.RecoveryClosed[i].Surface != surface || !validPRDRecoveryClosedProof(operation, operation.RecoveryClosed[i]) {
			return false
		}
	}
	return true
}

func completePRDRecoveryOpen(operation PRDOperation) bool {
	if len(operation.RecoveryOpen) != len(operationSurfaces) {
		return false
	}
	for i, surface := range operationSurfaces {
		if operation.RecoveryOpen[i].Surface != surface || !validPRDRecoveryOpenProof(operation, operation.RecoveryOpen[i]) {
			return false
		}
	}
	return true
}

func completePRDTransportReady(operation PRDOperation) bool {
	if len(operation.TransportReady) != len(operationRemoteSurfaces) {
		return false
	}
	for i, surface := range operationRemoteSurfaces {
		if operation.TransportReady[i].Surface != surface || !validPRDTransportReadyProof(operation, operation.TransportReady[i]) {
			return false
		}
	}
	return true
}

func validPRDTransportReadySet(operation PRDOperation) bool {
	if len(operation.TransportReady) > len(operationRemoteSurfaces) {
		return false
	}
	priorSurface := ""
	seenIDs := map[string]struct{}{}
	for _, proof := range operation.TransportReady {
		if proof.Surface <= priorSurface || !validPRDTransportReadyProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
		if _, exists := seenIDs[proof.AttestationID]; exists {
			return false
		}
		seenIDs[proof.AttestationID] = struct{}{}
	}
	return true
}

func completePRDAuthorizations(operation PRDOperation) bool {
	if len(operation.Authorizations) != len(operationSurfaces) {
		return false
	}
	for i, surface := range operationSurfaces {
		if operation.Authorizations[i].Surface != surface || !validPRDAuthorizationProof(operation, operation.Authorizations[i]) {
			return false
		}
	}
	return true
}

func validPRDAuthorizationSet(operation PRDOperation) bool {
	if len(operation.Authorizations) > len(operationSurfaces) {
		return false
	}
	priorSurface := ""
	seenIDs := map[string]struct{}{}
	for _, proof := range operation.Authorizations {
		if proof.Surface <= priorSurface || !validPRDAuthorizationProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
		if _, exists := seenIDs[proof.AuthorizationID]; exists {
			return false
		}
		seenIDs[proof.AuthorizationID] = struct{}{}
	}
	return true
}

func prdAuthorizationForSurface(proofs []PRDAuthorizationProof, surface string) (PRDAuthorizationProof, bool) {
	index := sort.Search(len(proofs), func(i int) bool { return proofs[i].Surface >= surface })
	if index < len(proofs) && proofs[index].Surface == surface {
		return proofs[index], true
	}
	return PRDAuthorizationProof{}, false
}

func containsPRDRemoteSurface(surface string) bool {
	for _, required := range operationRemoteSurfaces {
		if surface == required {
			return true
		}
	}
	return false
}

func remoteOpenPrecedesMac(operation PRDOperation, surface string) bool {
	if surface != SurfaceMac {
		return containsPRDRemoteSurface(surface)
	}
	for _, remote := range operationRemoteSurfaces {
		found := false
		for _, proof := range operation.RecoveryOpen {
			if proof.Surface == remote && validPRDRecoveryOpenProof(operation, proof) {
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

func appendUniquePRDClosedProof(operation *PRDOperation, proof PRDRecoveryClosedProof) error {
	for _, prior := range operation.RecoveryClosed {
		if prior.Surface == proof.Surface || prior.AttestationID == proof.AttestationID {
			return ErrCollision
		}
	}
	operation.RecoveryClosed = append(operation.RecoveryClosed, proof)
	sort.Slice(operation.RecoveryClosed, func(i, j int) bool { return operation.RecoveryClosed[i].Surface < operation.RecoveryClosed[j].Surface })
	return nil
}

func validatePRDOperationGroups(snapshot Snapshot) error {
	if len(snapshot.PRDOperationGroups) > 64 {
		return ErrInvalidRegistry
	}
	activeGroups := 0
	for i, group := range snapshot.PRDOperationGroups {
		if !operationIDPattern.MatchString(group.ID) || group.PrimaryOperationID != group.ID || len(group.Operations) == 0 || len(group.Operations) > 128 ||
			(i > 0 && snapshot.PRDOperationGroups[i-1].ID >= group.ID) {
			return ErrInvalidRegistry
		}
		if group.ActiveOperationID != "" {
			activeGroups++
		}
		if err := validatePRDOperationGroup(group); err != nil {
			return err
		}
	}
	if activeGroups > 1 || (activeGroups != 0 && hasActivePRDMigrationFence(&snapshot)) {
		return ErrInvalidRegistry
	}
	return nil
}

func validatePRDOperationGroup(group PRDOperationGroup) error {
	if len(group.Operations) == 0 || group.Operations[0].ID != group.PrimaryOperationID {
		return ErrInvalidRegistry
	}
	seenIDs := make(map[string]struct{}, len(group.Operations))
	activeChildren := 0
	var parent *PRDOperation
	for i := range group.Operations {
		operation := &group.Operations[i]
		if !operationIDPattern.MatchString(operation.ID) || !validEvidenceDigest(operation.CandidateSetDigest) ||
			!commitIDPattern.MatchString(operation.SourceRevision) || !validEvidenceDigest(operation.ArtifactDigest) ||
			!validEvidenceDigest(operation.ManifestDigest) || !validPRDEpochs(operation.Epochs, true) || !validPRDHistory(*operation) {
			return ErrInvalidRegistry
		}
		if _, exists := seenIDs[operation.ID]; exists {
			return ErrInvalidRegistry
		}
		seenIDs[operation.ID] = struct{}{}
		if i == 0 {
			if operation.ID != group.ID || operation.ParentID != "" || operation.Kind != PRDOperationV3Maintenance ||
				!validEvidenceDigest(operation.IncumbentCandidateSetDigest) || operation.CandidateSetDigest == operation.IncumbentCandidateSetDigest {
				return ErrInvalidRegistry
			}
			parent = operation
		} else {
			if parent == nil || operation.ParentID != parent.ID ||
				(operation.Kind != PRDOperationV3Restore && operation.Kind != PRDOperationV3ForwardRepair) ||
				!validPRDChildLineage(*parent, group, *operation, i) {
				return ErrInvalidRegistry
			}
			if operation.State != PRDOperationArchived {
				activeChildren++
			}
		}
		if err := validatePRDOperationProof(*operation); err != nil {
			return err
		}
	}
	if activeChildren > 1 {
		return ErrInvalidRegistry
	}
	if parent == nil || len(parent.RecoveryChildIDs) != len(group.Operations)-1 {
		return ErrInvalidRegistry
	}
	activeID := ""
	if parent.State != PRDOperationArchived {
		activeID = parent.ID
	}
	for _, child := range group.Operations[1:] {
		if child.State != PRDOperationArchived {
			activeID = child.ID
		}
	}
	if group.ActiveOperationID != activeID {
		return ErrInvalidRegistry
	}
	return nil
}

func validPRDChildLineage(parent PRDOperation, group PRDOperationGroup, child PRDOperation, index int) bool {
	if index-1 >= len(parent.RecoveryChildIDs) || parent.RecoveryChildIDs[index-1] != child.ID ||
		!prdHistoryReached(parent, PRDRestoreRequired) || child.IncumbentCandidateSetDigest != "" {
		return false
	}
	if child.Kind == PRDOperationV3Restore {
		if child.CandidateSetDigest != parent.IncumbentCandidateSetDigest || !validEvidenceDigest(child.LineageEvidenceDigest) ||
			child.AuthorityApprovalDigest != "" || child.QASAcceptanceDigest != "" || child.HostManagementManifestDigest != "" {
			return false
		}
	} else if child.CandidateSetDigest == parent.IncumbentCandidateSetDigest || child.CandidateSetDigest == parent.CandidateSetDigest ||
		!validEvidenceDigest(child.AuthorityApprovalDigest) || !validEvidenceDigest(child.QASAcceptanceDigest) ||
		!validEvidenceDigest(child.HostManagementManifestDigest) || child.LineageEvidenceDigest != "" {
		return false
	}
	for hostIndex, epoch := range child.Epochs {
		floor := parent.Epochs[hostIndex].CandidateEpoch
		for _, prior := range group.Operations[1:index] {
			if prior.Epochs[hostIndex].CandidateEpoch > floor {
				floor = prior.Epochs[hostIndex].CandidateEpoch
			}
		}
		if epoch.ClosureEpoch <= floor || epoch.CandidateEpoch <= epoch.ClosureEpoch {
			return false
		}
	}
	return true
}

func validatePRDOperationProof(operation PRDOperation) error {
	switch operation.Kind {
	case PRDOperationV3Maintenance:
		if operation.ParentID != "" || !validEvidenceDigest(operation.IncumbentCandidateSetDigest) ||
			!validPRDClosureSet(operation.Closures, operation.Epochs) || !validPRDQuiescenceSet(operation) ||
			!validPRDRecoveryClosedSet(operation) || !validPRDTransportReadySet(operation) || !validPRDAuthorizationSet(operation) || !validPRDRecoveryOpenSet(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDClosedVerified) && !completePRDClosures(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDQuiesced) && (!completePRDClosures(operation) || !completePRDQuiescence(operation) || !validEvidenceDigest(operation.QuiescenceSealDigest)) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDRestoreRequired) && !validEvidenceDigest(operation.FailureDigest) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDStaged) && !validEvidenceDigest(operation.StageEvidenceDigest) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDCandidateClosed) && !completePRDRecoveryClosed(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDCommitted) && (!completePRDRecoveryClosed(operation) || !validEvidenceDigest(operation.CommitEvidenceDigest)) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDReopenPrepared) && !completePRDTransportReady(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDReopenAuthorized) && !completePRDAuthorizations(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDReopened) && !completePRDRecoveryOpen(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDAccepted) && !validPRDAcceptanceForOperation(operation) {
			return ErrInvalidRegistry
		}
		if operation.Acceptance != nil && !validPRDAcceptanceForOperation(operation) {
			return ErrInvalidRegistry
		}
		if len(operation.RecoveryChildIDs) != len(uniquePRDStrings(operation.RecoveryChildIDs)) {
			return ErrInvalidRegistry
		}
	case PRDOperationV3Restore, PRDOperationV3ForwardRepair:
		if operation.ParentID == "" || len(operation.RecoveryChildIDs) != 0 || len(operation.Closures) != 0 || len(operation.Quiescence) != 0 ||
			!validPRDRecoveryClosedSet(operation) || !validPRDTransportReadySet(operation) || !validPRDAuthorizationSet(operation) || !validPRDRecoveryOpenSet(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDRecoveryCandidateClosed) && !completePRDRecoveryClosed(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDRecoveryReopened) && (!completePRDRecoveryClosed(operation) || !completePRDRecoveryOpen(operation)) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDRecoveryReopenPrepared) && !completePRDTransportReady(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDRecoveryReopenAuthorized) && !completePRDAuthorizations(operation) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDRecoveryAccepted) && (operation.Acceptance == nil || !validEvidenceDigest(operation.Acceptance.EvidenceDigest)) {
			return ErrInvalidRegistry
		}
		if operation.Acceptance != nil && (operation.Acceptance.TransitionID != operation.ID || operation.Acceptance.CandidateSetDigest != operation.CandidateSetDigest ||
			operation.Acceptance.Lane != config.ReleaseLanePRD || operation.Acceptance.SourceRevision != operation.SourceRevision) {
			return ErrInvalidRegistry
		}
		if prdHistoryReached(operation, PRDOperationAborted) && !validEvidenceDigest(operation.FailureDigest) {
			return ErrInvalidRegistry
		}
	default:
		return ErrInvalidRegistry
	}
	return nil
}

func validPRDClosureSet(proofs []PRDClosureProof, epochs []PRDHostEpoch) bool {
	if len(proofs) > len(operationSurfaces) {
		return false
	}
	priorSurface := ""
	seenIDs := map[string]struct{}{}
	for _, proof := range proofs {
		if !containsPRDSurface(proof.Surface) || proof.Surface <= priorSurface {
			return false
		}
		priorSurface = proof.Surface
		epoch, ok := prdEpochForSurface(epochs, proof.Surface)
		if !ok || !proof.Closed || proof.Epoch != epoch.ClosureEpoch || proof.DirectiveID == "" || proof.AttestationID == "" || !validEvidenceDigest(proof.EvidenceDigest) {
			return false
		}
		if _, exists := seenIDs[proof.DirectiveID]; exists {
			return false
		}
		seenIDs[proof.DirectiveID] = struct{}{}
		if _, exists := seenIDs[proof.AttestationID]; exists {
			return false
		}
		seenIDs[proof.AttestationID] = struct{}{}
	}
	return true
}

func validPRDQuiescenceSet(operation PRDOperation) bool {
	if len(operation.Quiescence) > len(operationSurfaces) {
		return false
	}
	priorSurface := ""
	for _, proof := range operation.Quiescence {
		if proof.Surface <= priorSurface || !validPRDQuiescenceProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
	}
	return true
}

func validPRDRecoveryClosedSet(operation PRDOperation) bool {
	if len(operation.RecoveryClosed) > len(operationSurfaces) {
		return false
	}
	priorSurface := ""
	seenIDs := map[string]struct{}{}
	for _, proof := range operation.RecoveryClosed {
		if proof.Surface <= priorSurface || !validPRDRecoveryClosedProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
		if _, exists := seenIDs[proof.AttestationID]; exists {
			return false
		}
		seenIDs[proof.AttestationID] = struct{}{}
	}
	return true
}

func validPRDRecoveryOpenSet(operation PRDOperation) bool {
	if len(operation.RecoveryOpen) > len(operationSurfaces) {
		return false
	}
	priorSurface := ""
	seenIDs := map[string]struct{}{}
	for _, proof := range operation.RecoveryOpen {
		if proof.Surface <= priorSurface || !validPRDRecoveryOpenProof(operation, proof) {
			return false
		}
		priorSurface = proof.Surface
		if _, exists := seenIDs[proof.DirectiveID]; exists {
			return false
		}
		seenIDs[proof.DirectiveID] = struct{}{}
		if _, exists := seenIDs[proof.AttestationID]; exists {
			return false
		}
		seenIDs[proof.AttestationID] = struct{}{}
	}
	return true
}

func validPRDHistory(operation PRDOperation) bool {
	if len(operation.History) == 0 || operation.History[len(operation.History)-1].To != operation.State {
		return false
	}
	for i, event := range operation.History {
		if event.Sequence != uint64(i+1) || event.To == "" || (i == 0 && (event.From != "" || event.To != initialPRDState(operation.Kind))) ||
			(i > 0 && (event.From != operation.History[i-1].To || !validPRDTransition(event.From, event.To))) ||
			(event.EvidenceDigest != "" && !validEvidenceDigest(event.EvidenceDigest)) {
			return false
		}
	}
	return true
}

func validPRDTransition(from, to PRDOperationState) bool {
	allowed := map[PRDOperationState][]PRDOperationState{
		PRDClosurePending:            {PRDClosedVerified},
		PRDClosedVerified:            {PRDQuiescing},
		PRDQuiescing:                 {PRDQuiesced},
		PRDQuiesced:                  {PRDRestoreRequired, PRDStaged},
		PRDStaged:                    {PRDCandidateClosed, PRDRestoreRequired},
		PRDCandidateClosed:           {PRDCommitted, PRDRestoreRequired},
		PRDCommitted:                 {PRDReopenPrepared},
		PRDReopenPrepared:            {PRDReopenAuthorized},
		PRDReopenAuthorized:          {PRDReopened},
		PRDReopened:                  {PRDAcceptancePending},
		PRDAcceptancePending:         {PRDAccepted},
		PRDAccepted:                  {PRDOperationArchived},
		PRDRestoreRequired:           {PRDOperationAborted},
		PRDRecoveryCandidateClosing:  {PRDRecoveryCandidateClosed, PRDOperationAborted},
		PRDRecoveryCandidateClosed:   {PRDRecoveryReopenPrepared, PRDOperationAborted},
		PRDRecoveryReopenPrepared:    {PRDRecoveryReopenAuthorized},
		PRDRecoveryReopenAuthorized:  {PRDRecoveryReopened},
		PRDRecoveryReopened:          {PRDRecoveryAcceptancePending},
		PRDRecoveryAcceptancePending: {PRDRecoveryAccepted},
		PRDRecoveryAccepted:          {PRDOperationArchived},
		PRDOperationAborted:          {PRDOperationArchived},
	}
	for _, next := range allowed[from] {
		if next == to {
			return true
		}
	}
	return false
}

func initialPRDState(kind PRDOperationKind) PRDOperationState {
	if kind == PRDOperationV3Maintenance {
		return PRDClosurePending
	}
	return PRDRecoveryCandidateClosing
}

func appendPRDHistory(operation *PRDOperation, next PRDOperationState, evidenceDigest string) error {
	if operation == nil || (len(operation.History) > 0 && !validPRDTransition(operation.State, next)) ||
		(len(operation.History) == 0 && next != initialPRDState(operation.Kind)) ||
		(evidenceDigest != "" && !validEvidenceDigest(evidenceDigest)) {
		return ErrInvalidTransition
	}
	from := operation.State
	if len(operation.History) == 0 {
		from = ""
	}
	operation.History = append(operation.History, PRDOperationTransition{Sequence: uint64(len(operation.History) + 1), From: from, To: next, EvidenceDigest: evidenceDigest})
	operation.State = next
	return nil
}

func prdHistoryReached(operation PRDOperation, state PRDOperationState) bool {
	for _, event := range operation.History {
		if event.To == state {
			return true
		}
	}
	return false
}

func prdEpochForSurface(epochs []PRDHostEpoch, surface string) (PRDHostEpoch, bool) {
	index := sort.Search(len(epochs), func(i int) bool { return epochs[i].Surface >= surface })
	if index < len(epochs) && epochs[index].Surface == surface {
		return epochs[index], true
	}
	return PRDHostEpoch{}, false
}

func samePRDSurfaces(left, right []PRDHostEpoch) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Surface != right[i].Surface {
			return false
		}
	}
	return true
}

func containsPRDSurface(surface string) bool {
	for _, required := range operationSurfaces {
		if surface == required {
			return true
		}
	}
	return false
}

func containsPRDString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasActivePRDOperationGroup(snapshot *Snapshot) bool {
	for _, group := range snapshot.PRDOperationGroups {
		if group.ActiveOperationID != "" {
			return true
		}
	}
	return false
}

func hasActivePRDMigrationFence(snapshot *Snapshot) bool {
	for _, fence := range snapshot.PRDMigrationFences {
		if fence.State != PRDMigrationAborted {
			return true
		}
	}
	return false
}

func prdOperationGroupByID(groups []PRDOperationGroup, id string) (PRDOperationGroup, bool) {
	for _, group := range groups {
		if group.ID == id {
			return group, true
		}
	}
	return PRDOperationGroup{}, false
}

func prdOperationGroupByIDMutable(groups *[]PRDOperationGroup, id string) (*PRDOperationGroup, bool) {
	for index := range *groups {
		if (*groups)[index].ID == id {
			return &(*groups)[index], true
		}
	}
	return nil, false
}

func prdOperationByID(operations []PRDOperation, id string) (PRDOperation, bool) {
	for _, operation := range operations {
		if operation.ID == id {
			return operation, true
		}
	}
	return PRDOperation{}, false
}

func prdOperationByIDMutable(operations *[]PRDOperation, id string) (*PRDOperation, bool) {
	for index := range *operations {
		if (*operations)[index].ID == id {
			return &(*operations)[index], true
		}
	}
	return nil, false
}

func clonePRDEpochs(epochs []PRDHostEpoch) []PRDHostEpoch {
	cloned := append([]PRDHostEpoch(nil), epochs...)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Surface < cloned[j].Surface })
	return cloned
}

func clonePRDOperation(operation PRDOperation) PRDOperation {
	data, err := json.Marshal(operation)
	if err != nil {
		return PRDOperation{}
	}
	var cloned PRDOperation
	if json.Unmarshal(data, &cloned) != nil {
		return PRDOperation{}
	}
	return cloned
}

func clonePRDOperationGroup(group PRDOperationGroup) PRDOperationGroup {
	data, err := json.Marshal(group)
	if err != nil {
		return PRDOperationGroup{}
	}
	var cloned PRDOperationGroup
	if json.Unmarshal(data, &cloned) != nil {
		return PRDOperationGroup{}
	}
	return cloned
}

func validEvidenceDigest(value string) bool { return digestPattern.MatchString(value) }

func uniquePRDStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
