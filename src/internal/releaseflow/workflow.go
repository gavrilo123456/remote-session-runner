package releaseflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PromotionPlan struct {
	Lane                       string                       `json:"lane"`
	ReleaseID                  string                       `json:"release_id"`
	ReleaseSHA256              string                       `json:"release_sha256"`
	Target                     string                       `json:"target"`
	AffectedHosts              []string                     `json:"affected_hosts"`
	CommonEffects              []string                     `json:"common_effects"`
	HostOrder                  []string                     `json:"host_switch_order"`
	RequiredProof              []string                     `json:"required_proofs"`
	Approval                   string                       `json:"approval"`
	ExpectedBranchMarker       string                       `json:"expected_branch_marker,omitempty"`
	ExpectedBranchMarkerExists bool                         `json:"expected_branch_marker_exists"`
	HostBaselines              []HostObservation            `json:"host_baselines,omitempty"`
	PeerLaneEvidence           map[string]map[string]string `json:"peer_lane_evidence,omitempty"`
	PeerLaneEvidenceProfile    string                       `json:"peer_lane_evidence_profile,omitempty"`
	ExistingHolds              map[string]string            `json:"existing_admission_holds,omitempty"`
	PredecessorBundleSHA256    map[string]string            `json:"predecessor_bundle_sha256,omitempty"`
	BackupRecoveryPackageSHA   string                       `json:"backup_recovery_package_sha256,omitempty"`
}

type HostObservation struct {
	HostID                    string `json:"host_id"`
	Lane                      string `json:"lane"`
	SelectedReleaseID         string `json:"selected_release_id"`
	SelectedBundleSHA         string `json:"selected_bundle_sha256"`
	SelectorOperation         string `json:"selector_operation_id,omitempty"`
	JournalVerified           bool   `json:"selector_journal_verified"`
	ServiceStopped            bool   `json:"service_stopped"`
	WorkInventorySHA          string `json:"work_inventory_sha256"`
	WorkInventoryEmpty        bool   `json:"work_inventory_empty"`
	ConfigurationSHA          string `json:"configuration_sha256"`
	SchemaVersion             int    `json:"schema_version"`
	BackupVerified            bool   `json:"backup_verified"`
	CapacityVerified          bool   `json:"capacity_verified"`
	AvailableBytes            uint64 `json:"available_bytes"`
	RequiredBytes             uint64 `json:"required_bytes"`
	CompatibilitySHA          string `json:"legacy_compatibility_sha256,omitempty"`
	CanSwitch                 bool   `json:"can_switch"`
	PreviousReleaseID         string `json:"previous_release_id,omitempty"`
	PreviousBundleSHA         string `json:"previous_bundle_sha256,omitempty"`
	PreviousSelectorOperation string `json:"previous_selector_operation_id,omitempty"`
	BackupSHA                 string `json:"backup_manifest_sha256,omitempty"`
}

type Acceptance struct {
	Profile       string            `json:"profile"`
	ReleaseID     string            `json:"release_id"`
	ReleaseSHA256 string            `json:"release_sha256"`
	BundleHashes  map[string]string `json:"bundle_hashes"`
	Passed        bool              `json:"passed"`
	Gates         map[string]string `json:"gates"`
	EvidenceSHA   string            `json:"evidence_sha256"`
}

// Backend methods are the narrow seam to existing host-native catalog
// controls. Implementations must use runner-lane's guarded operations; the
// coordinator never edits selectors, receipts, databases, or service files.
type Backend interface {
	Plan(context.Context, string, Release, string) (PromotionPlan, error)
	PrepareCohort(context.Context, Operation) (any, error)
	ObserveHost(context.Context, Operation, string) (HostObservation, error)
	SwitchHost(context.Context, Operation, string) (HostObservation, error)
	AcceptCohort(context.Context, Operation) (Acceptance, error)
	ReleaseCohort(context.Context, Operation) (any, error)
	RollbackHost(context.Context, Operation, string) (HostObservation, error)
	UpdateSourceBranch(context.Context, string, string, string) error
}

// ExistingCohortHoldValidator is implemented by the native backend to allow
// a new operation to supersede one synchronized, completed cohort receipt.
// The check is read-only; the predecessor remains held until a fresh successor
// receipt is held and every host records its local supersession evidence.
type ExistingCohortHoldValidator interface {
	ValidateExistingCohortHold(context.Context, PromotionPlan) error
}

type PromoteRequest struct {
	Lane        string
	Target      string
	ReleaseID   string
	ResumeID    string
	Approval    string
	PlanOnly    bool
	PrepareOnly bool
	Profile     string
	OperationID string
}

type PromoteResult struct {
	Operation  Operation     `json:"operation"`
	Plan       PromotionPlan `json:"plan"`
	Acceptance *Acceptance   `json:"acceptance,omitempty"`
	Outcome    string        `json:"outcome"`
	Next       string        `json:"next_step,omitempty"`
}

func BuildPlan(lane, target string, release Release) (PromotionPlan, error) {
	if !contains(LaneOrder, lane) || (target != "all" && !contains(HostOrder, target)) || ValidateRelease(release) != nil {
		return PromotionPlan{}, ErrInvalidInput
	}
	p := PromotionPlan{Lane: lane, ReleaseID: release.ID, ReleaseSHA256: release.Digest, Target: target,
		AffectedHosts: append([]string(nil), HostOrder...), HostOrder: append([]string(nil), HostOrder...),
		CommonEffects: []string{"fence admissions and dispatch for the selected lane on all four hosts", "capture fresh work/ownership inventories and configuration/schema identities", "create/verify consistent backups and capacity headroom on all four hosts", "create and synchronize one durable cohort receipt"},
		RequiredProof: []string{"release manifest and all four host bundle hashes", "previous-lane acceptance bound to this exact release for QAS/PRD", "selected-lane publisher pause and drain evidence", "fresh preflight, backup, compatibility, and ownership evidence", "whole-cohort functional acceptance before admissions reopen"}}
	if lane == "prd" {
		p.Approval = "owner approval bound to this release digest, host set, predecessor identities, backup/recovery scope, and valid window"
		p.BackupRecoveryPackageSHA = digest([]byte("runner-release-prd-backup-recovery-v1|" + release.Digest + "|" + strings.Join(HostOrder, ",") + "|online-consistent-backup|no-database-restore|reverse-selector-journals|500000000-byte-operating-buffer|67108864-byte-selector-reserve"))
	} else {
		p.Approval = "none beyond existing lane/publisher controls"
	}
	return p, nil
}

func commonExistingCohortHold(holds map[string]string) (string, error) {
	if len(holds) == 0 {
		return "", nil
	}
	if len(holds) != len(HostOrder) {
		return "", ErrAdmissionHeld
	}
	wanted := ""
	for _, host := range HostOrder {
		id, ok := holds[host]
		if !ok || !operationPattern.MatchString(id) || wanted != "" && id != wanted {
			return "", ErrAdmissionHeld
		}
		wanted = id
	}
	return wanted, nil
}

// Promote serializes one lane, persists intent before each backend effect, and
// reconciles observed selectors before retrying. Named-host calls still run
// common four-host preparation and stop after the requested host; only `all`
// performs whole-cohort acceptance and releases the hold.
func Promote(ctx context.Context, store *Store, backend Backend, req PromoteRequest, now func() time.Time) (result PromoteResult, err error) {
	if store == nil || backend == nil || now == nil {
		return result, ErrBackendUnavailable
	}
	if req.ResumeID != "" {
		if !contains(LaneOrder, req.Lane) || !operationPattern.MatchString(req.ResumeID) {
			return result, ErrInvalidInput
		}
	} else if !contains(LaneOrder, req.Lane) || (req.Target != "all" && !contains(HostOrder, req.Target)) || !releasePattern.MatchString(req.ReleaseID) {
		return result, ErrInvalidInput
	}
	var op Operation
	var release Release
	if req.ResumeID != "" {
		op, err = store.LoadOperation(req.Lane, req.ResumeID)
		if err != nil {
			return result, err
		}
		release = op.Release
		if req.Profile == "" {
			req.Profile = op.Profile
		}
		if req.ReleaseID != "" && req.ReleaseID != release.ID {
			return result, ErrReleaseMismatch
		}
		if req.Target == "" {
			req.Target = "all"
		}
	} else {
		release, err = store.LoadRelease(req.ReleaseID)
		if err != nil {
			return result, err
		}
	}
	if ValidateRelease(release) != nil {
		return result, ErrInvalidInput
	}
	if req.Profile == "" {
		req.Profile = "catalog-functional-v1"
	}
	if req.Lane == "qas" || req.Lane == "prd" {
		previousLane := "dev"
		if req.Lane == "prd" {
			previousLane = "qas"
		}
		proof, proofErr := store.LoadAcceptance(previousLane, release.ID)
		if proofErr != nil || proof.ReleaseSHA256 != release.Digest || proof.Profile != req.Profile || !sameBundleHashes(proof.BundleHashes, release) {
			return result, fmt.Errorf("%s requires a passed %s acceptance for the exact release digest, profile, and host bundles: %w", req.Lane, previousLane, ErrReleaseMismatch)
		}
	}
	if req.Target == "" {
		req.Target = "all"
	}
	plan, err := backend.Plan(ctx, req.Lane, release, req.Approval)
	if err != nil {
		return result, err
	}
	plan.Target = req.Target
	if req.PlanOnly {
		return PromoteResult{Plan: plan, Outcome: "planned_no_effects", Next: "run promote-to without --plan after its listed prerequisites pass"}, nil
	}
	for _, baseline := range plan.HostBaselines {
		if baseline.RequiredBytes > 0 && !baseline.CapacityVerified {
			return result, fmt.Errorf("%s measured free space %d is below the read-only minimum %d before any selected-lane hold, stop, or backup", baseline.HostID, baseline.AvailableBytes, baseline.RequiredBytes)
		}
	}
	heldOperation, holdErr := commonExistingCohortHold(plan.ExistingHolds)
	if holdErr != nil {
		return result, holdErr
	}
	if heldOperation != "" {
		allowedResumeHold := req.ResumeID != "" &&
			(heldOperation == op.CohortOperationID || heldOperation == op.PreviousCohortOperationID)
		if allowedResumeHold {
			if heldOperation == op.PreviousCohortOperationID {
				validator, ok := backend.(ExistingCohortHoldValidator)
				if !ok {
					return result, ErrAdmissionHeld
				}
				if err := validator.ValidateExistingCohortHold(ctx, plan); err != nil {
					return result, fmt.Errorf("validate held predecessor cohort %s: %w", heldOperation, err)
				}
			}
		} else {
			if req.ResumeID != "" || req.OperationID == heldOperation {
				return result, fmt.Errorf("selected lane admission is held by %s: %w", heldOperation, ErrAdmissionHeld)
			}
			validator, ok := backend.(ExistingCohortHoldValidator)
			if !ok {
				return result, fmt.Errorf("selected lane admission is held by %s: %w", heldOperation, ErrAdmissionHeld)
			}
			if err := validator.ValidateExistingCohortHold(ctx, plan); err != nil {
				return result, fmt.Errorf("validate held predecessor cohort %s: %w", heldOperation, err)
			}
		}
	}
	return result, store.WithLaneLock(req.Lane, func() error {
		if req.ResumeID != "" && req.Lane == "prd" && req.Approval != "" && req.Approval != op.ApprovalSHA {
			if op.PlanSnapshot == nil || !digestPattern.MatchString(op.ApprovalSHA) || !digestPattern.MatchString(req.Approval) {
				return ErrApprovalRequired
			}
			renewal := map[string]string{
				"previous_approval_sha256": op.ApprovalSHA,
				"current_approval_sha256":  req.Approval,
				"plan_snapshot_sha256":     hashJSON(*op.PlanSnapshot),
			}
			if err := AppendEvent(&op, "approval-renewed", "", false, "applied", renewal, now()); err != nil {
				return err
			}
			op.ApprovalSHA = req.Approval
			if err := store.SaveOperation(op); err != nil {
				return err
			}
		}
		if req.ResumeID == "" {
			if req.OperationID == "" {
				return ErrInvalidInput
			}
			approvalHash := req.Approval
			if req.Lane == "prd" {
				if !digestPattern.MatchString(approvalHash) {
					return ErrApprovalRequired
				}
			}
			op, err = NewOperation(req.OperationID, req.Lane, req.Target, release, req.Profile, approvalHash, now())
			if err != nil {
				return err
			}
			op.PreviousCohortOperationID = heldOperation
			if err = AppendEvent(&op, "operation-created", "", false, "recorded", map[string]any{"plan": plan}, now()); err != nil {
				return err
			}
			op.ExpectedBranchMarker = plan.ExpectedBranchMarker
			op.ExpectedBranchMarkerExists = plan.ExpectedBranchMarkerExists
			op.PlanSnapshot = &plan
			if err = store.SaveOperation(op); err != nil {
				return err
			}
		}
		if req.PrepareOnly {
			if op.Stage != StagePlanned && op.Stage != StagePreparing && op.Stage != StagePrepared && op.Stage != StageBlocked {
				return fmt.Errorf("prepare-only cannot be used after selector work has started: %w", ErrOperationCorrupted)
			}
			for _, progress := range op.Hosts {
				if progress.SwitchIntent || progress.Switched || progress.Verified {
					return fmt.Errorf("prepare-only cannot be used after selector work has started: %w", ErrOperationCorrupted)
				}
			}
			for _, event := range op.Events {
				if event.Action == "switch-host" || event.Action == "host-verified" || event.Action == "verify-switch" {
					return fmt.Errorf("prepare-only cannot be used after selector work has started: %w", ErrOperationCorrupted)
				}
			}
		}
		if op.Stage == StageComplete || op.Stage == StageBranchSyncPending {
			return finishBranchSync(ctx, store, backend, &op, plan, &result, now)
		}
		if op.Stage == StageRollbackComplete {
			result = PromoteResult{Operation: op, Plan: plan, Outcome: "rolled_back", Next: "start a new operation only after inspecting the rollback evidence"}
			return nil
		}
		if op.Stage == StageBlocked {
			// Re-enter through durable history. A completed common preparation is
			// reused; an uncertain effect is reconciled by ObserveHost before a
			// retry. No failed effect is silently marked complete.
			op.Stage = StagePlanned
			for _, event := range op.Events {
				if event.Action == "prepare-cohort" && !event.Intent && event.Outcome == "complete" {
					op.Stage = StagePrepared
				}
			}
			op.LastError = ""
			if err := store.SaveOperation(op); err != nil {
				return err
			}
		}
		if op.Stage == StageAccepted {
			if _, callErr := backend.ReleaseCohort(ctx, op); callErr != nil {
				return persistFailure(store, &op, "release-cohort", "", callErr, now)
			}
			if err := AppendEvent(&op, "cohort-released", "", false, "complete", nil, now()); err != nil {
				return err
			}
			op.Stage = StageComplete
			op.BranchSync = "pending"
			if err := store.SaveOperation(op); err != nil {
				return err
			}
			return finishBranchSync(ctx, store, backend, &op, plan, &result, now)
		}
		if op.Stage == StagePlanned || op.Stage == StagePreparing {
			op.Stage = StagePreparing
			if err := persistIntent(store, &op, "prepare-cohort", "", now); err != nil {
				return err
			}
			evidence, callErr := backend.PrepareCohort(ctx, op)
			if callErr != nil {
				return persistFailure(store, &op, "prepare-cohort", "", callErr, now)
			}
			cohortID, idErr := cohortOperationIDFromEvidence(evidence)
			if idErr != nil {
				return persistFailure(store, &op, "prepare-cohort-result", "", idErr, now)
			}
			op.CohortOperationID = cohortID
			if err := AppendEvent(&op, "prepare-cohort", "", false, "complete", evidence, now()); err != nil {
				return err
			}
			op.Stage = StagePrepared
			if err := store.SaveOperation(op); err != nil {
				return err
			}
		}
		if req.PrepareOnly {
			if op.Stage != StagePrepared || op.CohortOperationID == "" {
				return fmt.Errorf("prepare-only is available only at the held, pre-switch checkpoint: %w", ErrOperationCorrupted)
			}
			result = PromoteResult{Operation: op, Plan: plan, Outcome: "prepared_hold_active",
				Next: "inspect the synchronized cohort hold, complete any supported receipt supersession, then resume this operation"}
			return nil
		}
		if op.Stage != StagePrepared && op.Stage != StageSwitching && op.Stage != StageVerifying {
			return ErrOperationCorrupted
		}
		limit := len(HostOrder)
		if req.Target != "all" {
			targetIndex := hostIndex(req.Target)
			nextIndex := nextUnverifiedHost(op)
			if targetIndex >= 0 && targetIndex < nextIndex {
				limit = targetIndex + 1
			} else if targetIndex != nextIndex {
				return ErrHostOutOfOrder
			} else {
				limit = targetIndex + 1
			}
		}
		for i := 0; i < limit; i++ {
			host := HostOrder[i]
			progress := &op.Hosts[i]
			if progress.Verified {
				continue
			}
			observed, observeErr := backend.ObserveHost(ctx, op, host)
			if observeErr != nil {
				return persistFailure(store, &op, "observe-host", host, observeErr, now)
			}
			if observed.HostID != host || observed.Lane != op.Lane || observed.SelectedReleaseID != release.ID || observed.SelectedBundleSHA != release.Bundles[host].SHA256 || !observed.JournalVerified || observed.ServiceStopped {
				if !observed.CanSwitch || !observed.ServiceStopped || !observed.WorkInventoryEmpty || !observed.BackupVerified || !observed.CapacityVerified || observed.ConfigurationSHA == "" || observed.WorkInventorySHA == "" {
					return persistFailure(store, &op, "host-guard", host, ErrOperationCorrupted, now)
				}
				if i != nextUnverifiedHost(op) {
					return ErrHostOutOfOrder
				}
				// Persist exact rollback and identity evidence before selector intent.
				progress.PreviousReleaseID = observed.PreviousReleaseID
				progress.PreviousBundleSHA256 = observed.PreviousBundleSHA
				progress.PreviousSelectorOperation = observed.PreviousSelectorOperation
				progress.EvidenceSHA256 = hashJSON(observed)
				if observed.BackupSHA != "" {
					progress.EvidenceSHA256 = hashJSON(map[string]string{"observation": progress.EvidenceSHA256, "backup": observed.BackupSHA})
				}
				if err := store.SaveOperation(op); err != nil {
					return err
				}
				op.Stage = StageSwitching
				if err := persistIntent(store, &op, "switch-host", host, now); err != nil {
					return err
				}
				observed, observeErr = backend.SwitchHost(ctx, op, host)
				if observeErr != nil {
					return persistFailure(store, &op, "switch-host", host, observeErr, now)
				}
				if observed.HostID != host || observed.Lane != op.Lane || observed.SelectedReleaseID != release.ID || observed.SelectedBundleSHA != release.Bundles[host].SHA256 || !observed.JournalVerified || observed.SelectorOperation == "" {
					return persistFailure(store, &op, "verify-switch", host, ErrOperationCorrupted, now)
				}
			}
			if observed.SelectorOperation == "" {
				return persistFailure(store, &op, "verify-switch", host, ErrOperationCorrupted, now)
			}
			progress.SwitchIntent = true
			progress.Switched = true
			progress.Verified = true
			progress.SelectorOperation = observed.SelectorOperation
			progress.ObservedBundleSHA = observed.SelectedBundleSHA
			if progress.EvidenceSHA256 == "" {
				progress.EvidenceSHA256 = hashJSON(observed)
			}
			if err := AppendEvent(&op, "host-verified", host, false, "verified", observed, now()); err != nil {
				return err
			}
			op.Stage = StageVerifying
			if err := store.SaveOperation(op); err != nil {
				return err
			}
		}
		if req.Target != "all" || !allVerified(op) {
			result = PromoteResult{Operation: op, Plan: plan, Outcome: "cohort_pending", Next: "continue with promote-to " + op.Lane + " all --release " + release.ID + " --resume " + op.OperationID}
			return nil
		}
		acceptance, callErr := backend.AcceptCohort(ctx, op)
		if callErr != nil {
			return persistFailure(store, &op, "acceptance", "", callErr, now)
		}
		if !acceptance.Passed || acceptance.ReleaseID != release.ID || acceptance.ReleaseSHA256 != release.Digest || acceptance.Profile != op.Profile || !validAcceptanceHashes(acceptance, release) || !validRequiredGates(acceptance.Gates) {
			return persistFailure(store, &op, "acceptance", "", ErrReleaseMismatch, now)
		}
		proof := AcceptanceRecord{FormatVersion: FormatVersion, Lane: op.Lane, OperationID: op.OperationID, Profile: op.Profile,
			ReleaseID: release.ID, ReleaseSHA256: release.Digest, BundleHashes: acceptance.BundleHashes, EvidenceSHA: acceptance.EvidenceSHA,
			Passed: true, Gates: acceptance.Gates, AcceptedUTC: now().UTC().Format(time.RFC3339Nano)}
		if err := store.SaveAcceptance(proof); err != nil {
			return persistFailure(store, &op, "persist-acceptance", "", err, now)
		}
		if err := AppendEvent(&op, "cohort-accepted", "", false, "passed", acceptance, now()); err != nil {
			return err
		}
		op.Stage = StageAccepted
		if err := store.SaveOperation(op); err != nil {
			return err
		}
		if _, callErr := backend.ReleaseCohort(ctx, op); callErr != nil {
			return persistFailure(store, &op, "release-cohort", "", callErr, now)
		}
		if err := AppendEvent(&op, "cohort-released", "", false, "complete", nil, now()); err != nil {
			return err
		}
		op.Stage = StageComplete
		op.BranchSync = "pending"
		if err := store.SaveOperation(op); err != nil {
			return err
		}
		result.Acceptance = &acceptance
		return finishBranchSync(ctx, store, backend, &op, plan, &result, now)
	})
}

func cohortOperationIDFromEvidence(evidence any) (string, error) {
	if evidence == nil {
		return "", ErrOperationCorrupted
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		return "", ErrOperationCorrupted
	}
	var result struct {
		CohortOperationID string `json:"cohort_operation_id"`
		OperationID       string `json:"operation_id"`
	}
	if json.Unmarshal(body, &result) != nil {
		return "", ErrOperationCorrupted
	}
	if result.CohortOperationID == "" {
		result.CohortOperationID = result.OperationID
	}
	if !operationPattern.MatchString(result.CohortOperationID) {
		return "", ErrOperationCorrupted
	}
	return result.CohortOperationID, nil
}

func finishBranchSync(ctx context.Context, s *Store, b Backend, op *Operation, plan PromotionPlan, result *PromoteResult, now func() time.Time) error {
	if op.Stage == StageBranchSyncPending || op.Stage == StageComplete {
		if op.Lane == "dev" {
			op.BranchSync = "not_applicable"
		} else {
			if err := b.UpdateSourceBranch(ctx, op.Lane, op.Release.Source.Commit, branchMarkerExpectation(op)); err != nil {
				op.Stage = StageBranchSyncPending
				op.BranchSync = "pending"
				op.LastError = err.Error()
				_ = AppendEvent(op, "branch-sync", "", false, "pending", map[string]string{"error": err.Error()}, now())
				if saveErr := s.SaveOperation(*op); saveErr != nil {
					return saveErr
				}
				*result = PromoteResult{Operation: *op, Plan: plan, Outcome: "runtime_accepted_branch_sync_pending", Acceptance: nil, Next: "retry runner-release branch-sync --lane " + op.Lane + " --operation " + op.OperationID}
				return nil
			}
			op.BranchSync = "complete"
		}
		op.Stage = StageComplete
		op.LastError = ""
		if err := s.SaveOperation(*op); err != nil {
			return err
		}
	}
	*result = PromoteResult{Operation: *op, Plan: plan, Outcome: "completed"}
	return nil
}

func branchMarkerExpectation(op *Operation) string {
	if op.ExpectedBranchMarkerExists {
		return op.ExpectedBranchMarker
	}
	return ""
}

func sameBundleHashes(hashes map[string]string, release Release) bool {
	if len(hashes) != len(HostOrder) {
		return false
	}
	for _, host := range HostOrder {
		if hashes[host] != release.Bundles[host].SHA256 {
			return false
		}
	}
	return true
}

func persistIntent(s *Store, op *Operation, action, host string, now func() time.Time) error {
	if err := AppendEvent(op, action, host, true, "intent", nil, now()); err != nil {
		return err
	}
	return s.SaveOperation(*op)
}
func persistFailure(s *Store, op *Operation, action, host string, cause error, now func() time.Time) error {
	op.LastError = cause.Error()
	if op.Stage != StageBlocked {
		op.Stage = StageBlocked
	}
	_ = AppendEvent(op, action, host, false, "failed", map[string]string{"error": cause.Error()}, now())
	if err := s.SaveOperation(*op); err != nil {
		return err
	}
	return cause
}
func nextUnverifiedHost(op Operation) int {
	for i, h := range op.Hosts {
		if !h.Verified && !h.RolledBack {
			return i
		}
	}
	return len(op.Hosts)
}
func allVerified(op Operation) bool {
	for _, h := range op.Hosts {
		if !h.Verified || h.RolledBack {
			return false
		}
	}
	return true
}
func hostIndex(host string) int {
	for i, h := range HostOrder {
		if h == host {
			return i
		}
	}
	return -1
}
func validAcceptanceHashes(a Acceptance, r Release) bool {
	if !digestPattern.MatchString(a.EvidenceSHA) || len(a.BundleHashes) != len(HostOrder) {
		return false
	}
	for _, h := range HostOrder {
		if a.BundleHashes[h] != r.Bundles[h].SHA256 {
			return false
		}
	}
	return true
}
func hashJSON(v any) string { b, _ := jsonMarshal(v); return digest(b) }

func Rollback(ctx context.Context, s *Store, b Backend, lane, id, approvalSHA string, now func() time.Time) (Operation, error) {
	if s == nil || b == nil || now == nil {
		return Operation{}, ErrBackendUnavailable
	}
	var out Operation
	err := s.WithLaneLock(lane, func() error {
		op, err := s.LoadOperation(lane, id)
		if err != nil {
			return err
		}
		if lane == "prd" && (!digestPattern.MatchString(approvalSHA) || op.ApprovalSHA != approvalSHA) {
			return ErrApprovalRequired
		}
		op.Stage = StageRollingBack
		if err := s.SaveOperation(op); err != nil {
			return err
		}
		for i := len(HostOrder) - 1; i >= 0; i-- {
			host := HostOrder[i]
			hp := &op.Hosts[i]
			if !hp.Switched || hp.RolledBack {
				continue
			}
			if err := persistIntent(s, &op, "rollback-host", host, now); err != nil {
				return err
			}
			observed, err := b.RollbackHost(ctx, op, host)
			if err != nil {
				return persistFailure(s, &op, "rollback-host", host, err, now)
			}
			if observed.HostID != host || observed.Lane != lane || !observed.JournalVerified ||
				observed.SelectedBundleSHA != hp.PreviousBundleSHA256 || observed.SelectedReleaseID != hp.PreviousReleaseID {
				return persistFailure(s, &op, "verify-rollback", host, ErrOperationCorrupted, now)
			}
			hp.RolledBack = true
			if err := AppendEvent(&op, "host-rolled-back", host, false, "verified", observed, now()); err != nil {
				return err
			}
			if err := s.SaveOperation(op); err != nil {
				return err
			}
		}
		if _, err := b.ReleaseCohort(ctx, op); err != nil {
			return persistFailure(s, &op, "release-after-rollback", "", err, now)
		}
		op.Stage = StageRollbackComplete
		if err := AppendEvent(&op, "rollback-complete", "", false, "complete", nil, now()); err != nil {
			return err
		}
		if err := s.SaveOperation(op); err != nil {
			return err
		}
		out = op
		return nil
	})
	return out, err
}

func Status(s *Store, lane, id string) (Operation, error) {
	if s == nil {
		return Operation{}, errors.New("store unavailable")
	}
	return s.LoadOperation(lane, id)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func digest(b []byte) string            { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
