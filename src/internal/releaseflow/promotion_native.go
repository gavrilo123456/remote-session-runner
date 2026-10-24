package releaseflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

const (
	defaultPromotionOperatingBuffer = uint64(500_000_000)
	prdSelectorReserve              = uint64(67_108_864)
)

func promotionRequiredCapacity(lane string, operatingBuffer uint64) uint64 {
	if operatingBuffer == 0 {
		operatingBuffer = defaultPromotionOperatingBuffer
	}
	if lane == "prd" {
		return operatingBuffer + prdSelectorReserve
	}
	return operatingBuffer
}

func promotionPeerSnapshotDigest(snapshot promotionLaneSnapshot) string {
	// Filesystem headroom is a cohort precondition, not peer-lane identity.
	snapshot.AvailableBytes, snapshot.RequiredBytes = 0, 0
	return hashJSON(snapshot)
}

// legacyCompatibilityInputSHA binds an initial legacy-PRD rollback proof to
// the read-only plan. Catalog-managed PRD selectors carry rollback
// compatibility in the sealed predecessor manifest and do not need this file.
func legacyCompatibilityInputSHA(profile HostProfile) (string, error) {
	if profile.LegacyCompatibilityFile == "" {
		return "", fmt.Errorf("legacy PRD predecessor requires exact binary/config/schema semantic evidence: %w", ErrInvalidInput)
	}
	return fileSHA256(profile.LegacyCompatibilityFile)
}

// NativePromotionBackend is the only production executor. It delegates all
// service, backup, receipt, selector and rollback effects to runner-lane's
// existing guarded commands; it never edits those files or databases itself.
type NativePromotionBackend struct {
	Config             HostConfiguration
	Repository         string
	ControllerRevision string
	StoreRoot          string
	GitSSHCommand      string
	MirrorSSHCommand   string
	PublisherOwner     string
	PublisherControl   string
	PublishersPaused   bool
	NoNewAccepted      bool
	OperatingBuffer    uint64
	Approval           *Approval
	laneCommandRunner  func(context.Context, string, []string, []byte) ([]byte, error)
	capacityReader     func(context.Context, string, string, *promotionLaneSnapshot) error
}

type promotionReceiptView struct {
	Present bool                             `json:"present"`
	Receipt laneinstall.CatalogCohortReceipt `json:"receipt"`
}

type promotionAdmissionView struct {
	HostID        string                            `json:"host_id"`
	Lane          config.ReleaseLane                `json:"lane"`
	AdmissionHeld bool                              `json:"admission_held"`
	OperationID   string                            `json:"operation_id"`
	Receipt       *laneinstall.CatalogCohortReceipt `json:"receipt,omitempty"`
}

type promotionPreparedProbe struct {
	HostID              string          `json:"host_id"`
	Lane                string          `json:"lane"`
	Transport           string          `json:"transport"`
	OperationID         string          `json:"operation_id"`
	ControllerType      string          `json:"controller_type"`
	ControllerID        string          `json:"controller_id"`
	Environment         string          `json:"environment"`
	ConfigurationSHA256 string          `json:"configuration_sha256"`
	IdempotencyKey      string          `json:"idempotency_key"`
	RequestSHA256       string          `json:"request_body_sha256"`
	RequestBody         json.RawMessage `json:"request_body"`
	ExpiresAt           string          `json:"expires_at"`
	Token               string          `json:"one_time_token"`
	State               string          `json:"state"`
	JobID               string          `json:"job_id,omitempty"`
	IntentID            string          `json:"intent_id,omitempty"`
}

type promotionProbeScopeObservation struct {
	HostID              string                            `json:"host_id"`
	Lane                string                            `json:"lane"`
	Transport           laneinstall.CatalogProbeTransport `json:"transport"`
	OperationID         string                            `json:"operation_id"`
	ControllerType      string                            `json:"controller_type"`
	ControllerID        string                            `json:"controller_id"`
	Environment         string                            `json:"environment"`
	ConfigurationSHA256 string                            `json:"configuration_sha256"`
	IdempotencyKey      string                            `json:"idempotency_key"`
	RequestSHA256       string                            `json:"request_body_sha256"`
	CreatedAt           string                            `json:"created_at"`
	ExpiresAt           string                            `json:"expires_at"`
	State               string                            `json:"state"`
	JobID               string                            `json:"job_id,omitempty"`
	IntentID            string                            `json:"intent_id,omitempty"`
}

type promotionProbeAcceptanceEvidence struct {
	Method                              string `json:"method"`
	HelperExitCode                      int    `json:"helper_exit_code"`
	HelperContract                      string `json:"helper_contract"`
	ControllerFailureEventSequence      int    `json:"controller_failure_event_sequence"`
	ControllerFailureEventSHA256        string `json:"controller_failure_event_sha256"`
	ControllerFailureEventAtUTC         string `json:"controller_failure_event_at_utc"`
	ScopeCreatedAt                      string `json:"scope_created_at"`
	ScopeExpiresAt                      string `json:"scope_expires_at"`
	HelperVerifiedPositiveFinalSequence bool   `json:"helper_verified_positive_final_sequence"`
}

type promotionProbeOutcome struct {
	Transport           string                            `json:"transport"`
	HostID              string                            `json:"host_id"`
	OperationID         string                            `json:"operation_id"`
	AcceptedAt          string                            `json:"accepted_at"`
	JobID               string                            `json:"job_id"`
	SessionID           string                            `json:"session_id"`
	CommandID           string                            `json:"command_id"`
	Phase               string                            `json:"phase"`
	CommandState        string                            `json:"command_state"`
	ExitCode            *int                              `json:"exit_code"`
	FinalEventSequence  *int64                            `json:"final_event_sequence"`
	OutputComplete      bool                              `json:"output_complete"`
	OutputTruncated     bool                              `json:"output_truncated"`
	TeardownState       string                            `json:"teardown_state"`
	HTTPStatus          int                               `json:"http_status"`
	BridgeResponseType  string                            `json:"bridge_response_type"`
	ReadinessHTTPStatus int                               `json:"readiness_http_status"`
	ObservedExisting    bool                              `json:"observed_existing,omitempty"`
	ConfigurationSHA    string                            `json:"configuration_sha256"`
	RequestSHA256       string                            `json:"request_body_sha256"`
	RequestedTimeout    int                               `json:"requested_timeout_seconds"`
	AcceptanceEvidence  *promotionProbeAcceptanceEvidence `json:"acceptance_evidence,omitempty"`
}

type promotionLaneSnapshot struct {
	Selector       laneinstall.CatalogSelectorStatus `json:"selector"`
	Active         bool                              `json:"service_active"`
	Config         string                            `json:"configuration_sha256"`
	Schema         int                               `json:"schema_version"`
	Work           laneinstall.CatalogWorkInventory  `json:"work_inventory"`
	Empty          bool                              `json:"work_inventory_empty"`
	AvailableBytes uint64                            `json:"available_bytes"`
	RequiredBytes  uint64                            `json:"required_bytes"`
}

func (b *NativePromotionBackend) Plan(ctx context.Context, lane string, release Release, approvalSHA string) (PromotionPlan, error) {
	plan, err := BuildPlan(lane, "all", release)
	if err != nil || b == nil || !contains(LaneOrder, lane) {
		return PromotionPlan{}, ErrInvalidInput
	}
	if b.OperatingBuffer == 0 {
		b.OperatingBuffer = defaultPromotionOperatingBuffer
	}
	plan.CommonEffects = append(plan.CommonEffects,
		"hold and stop the selected lane across all four hosts before a named-host step",
		"perform ordered selector/start/check steps linux-host → sandbox-host → factory-sandbox → mac-control-plane",
		"peer lanes remain selected and configured as observed in this plan")
	plan.HostBaselines = make([]HostObservation, 0, len(HostOrder))
	plan.PeerLaneEvidence = make(map[string]map[string]string, len(HostOrder))
	plan.ExistingHolds = make(map[string]string, len(HostOrder))
	plan.PredecessorBundleSHA256 = make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		if err := b.inspectPublished(ctx, host, release); err != nil {
			return PromotionPlan{}, fmt.Errorf("%s sealed release inspection: %w", host, err)
		}
		selected, err := b.readLaneSnapshot(ctx, host, lane)
		if err != nil {
			return PromotionPlan{}, fmt.Errorf("%s %s read-only baseline: %w", host, lane, err)
		}
		baseline := HostObservation{HostID: host, Lane: lane, SelectedReleaseID: selectedReleaseID(selected.Selector),
			SelectedBundleSHA: selectedBundleSHA(selected.Selector), ServiceStopped: !selected.Active,
			WorkInventorySHA: selected.Work.SHA256(), WorkInventoryEmpty: selected.Empty,
			ConfigurationSHA: selected.Config, SchemaVersion: selected.Schema,
			PreviousReleaseID: selectedReleaseID(selected.Selector), PreviousBundleSHA: selectedBundleSHA(selected.Selector)}
		baseline.AvailableBytes, baseline.RequiredBytes = selected.AvailableBytes, selected.RequiredBytes
		baseline.CapacityVerified = selected.AvailableBytes >= selected.RequiredBytes
		if lane == "prd" && baseline.PreviousReleaseID == "" {
			baseline.CompatibilitySHA, err = legacyCompatibilityInputSHA(b.Config.Hosts[host])
			if err != nil {
				return PromotionPlan{}, fmt.Errorf("%s legacy rollback compatibility input: %w", host, err)
			}
		}
		plan.HostBaselines = append(plan.HostBaselines, baseline)
		plan.PredecessorBundleSHA256[host] = baseline.SelectedBundleSHA
		admission, err := b.readAdmission(ctx, host, lane)
		if err != nil {
			return PromotionPlan{}, fmt.Errorf("%s %s admission receipt inspection: %w", host, lane, err)
		}
		if admission.AdmissionHeld {
			plan.ExistingHolds[host] = admission.OperationID
		}
		peer := make(map[string]string, len(LaneOrder)-1)
		for _, other := range LaneOrder {
			if other == lane {
				continue
			}
			snapshot, snapshotErr := b.readLaneSnapshot(ctx, host, other)
			if snapshotErr != nil {
				return PromotionPlan{}, fmt.Errorf("%s peer lane %s isolation baseline: %w", host, other, snapshotErr)
			}
			peer[other] = promotionPeerSnapshotDigest(snapshot)
		}
		plan.PeerLaneEvidence[host] = peer
	}
	if len(plan.ExistingHolds) > 0 {
		plan.CommonEffects = append(plan.CommonEffects,
			"preserve any synchronized completed admission hold until the new four-host receipt is held, then supersede it with fresh per-host selector/work/config/schema evidence; unfinished or ambiguous holds remain blocked")
	}
	if lane == "qas" || lane == "prd" {
		marker, exists, err := b.readBranchMarker(ctx, lane)
		if err != nil {
			return PromotionPlan{}, err
		}
		plan.ExpectedBranchMarker, plan.ExpectedBranchMarkerExists = marker, exists
		if exists && !b.isAncestor(ctx, marker, release.Source.Commit) {
			return PromotionPlan{}, fmt.Errorf("%s branch marker %s is not an ancestor of candidate %s: %w", lane, marker, release.Source.Commit, ErrBranchConflict)
		}
	}
	if lane == "prd" && approvalSHA != "" && !digestPattern.MatchString(approvalSHA) {
		return PromotionPlan{}, ErrApprovalRequired
	}
	if lane == "prd" {
		plan.BackupRecoveryPackageSHA = hashJSON(map[string]any{
			"format": "runner-release-prd-backup-recovery-v2", "release_id": release.ID, "release_sha256": release.Digest,
			"hosts": plan.HostBaselines, "host_set": HostOrder,
			"backup":                 "consistent-online-backup-verified-before-switch; required-space-rechecked-after-backup",
			"rollback":               "reverse-selector-journals; preserve databases and accepted work; never auto-restore database",
			"operating_buffer_bytes": b.OperatingBuffer, "selector_reserve_bytes": prdSelectorReserve,
		})
	}
	return plan, nil
}

// ValidateExistingCohortHold allows replacement only for one synchronized,
// completed receipt whose exact selected bundles, config/schema identities,
// and empty work inventories still match the read-only plan. Interrupted or
// ambiguous cohorts remain blocked for their dedicated recovery path.
func (b *NativePromotionBackend) ValidateExistingCohortHold(ctx context.Context, plan PromotionPlan) error {
	operationID, err := commonExistingCohortHold(plan.ExistingHolds)
	if err != nil || operationID == "" {
		return errors.Join(ErrAdmissionHeld, err)
	}
	var canonical laneinstall.CatalogCohortReceipt
	for _, host := range HostOrder {
		view, readErr := b.readReceipt(ctx, host, plan.Lane, operationID)
		if readErr != nil || !view.Present {
			return errors.Join(fmt.Errorf("%s completed predecessor receipt is unavailable", host), readErr)
		}
		if canonical.OperationID == "" {
			canonical = view.Receipt
		} else if !reflectReceiptEqual(canonical, view.Receipt) {
			return fmt.Errorf("completed predecessor receipt differs across hosts: %w", ErrOperationCorrupted)
		}
	}
	if canonical.Stage != laneinstall.CatalogCohortComplete || canonical.Lane != config.ReleaseLane(plan.Lane) {
		return fmt.Errorf("held predecessor %s is not a completed cohort: %w", operationID, ErrAdmissionHeld)
	}
	for index, host := range HostOrder {
		if index >= len(canonical.Hosts) || index >= len(plan.HostBaselines) {
			return ErrOperationCorrupted
		}
		predecessor, baseline := canonical.Hosts[index], plan.HostBaselines[index]
		if predecessor.HostID != host || baseline.HostID != host || baseline.Lane != plan.Lane ||
			baseline.SelectedReleaseID != canonical.ReleaseID || baseline.SelectedBundleSHA != predecessor.Target.BundleSHA256 ||
			baseline.ConfigurationSHA != predecessor.ConfigurationSHA256 || baseline.SchemaVersion != predecessor.SchemaVersion ||
			!baseline.WorkInventoryEmpty {
			return fmt.Errorf("%s no longer matches the completed predecessor receipt or has retained work: %w", host, ErrAdmissionHeld)
		}
	}
	return nil
}

func (b *NativePromotionBackend) PrepareCohort(ctx context.Context, op Operation) (any, error) {
	if !b.PublishersPaused || !b.NoNewAccepted || strings.TrimSpace(b.PublisherOwner) == "" || strings.TrimSpace(b.PublisherControl) == "" {
		return nil, errors.New("execution requires explicit owner, publisher control, paused confirmation, and no-new-accepted confirmation")
	}
	if existing, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err == nil && existing.Present {
		if existing.Receipt.ReleaseID != op.Release.ID || existing.Receipt.ReleaseSHA256 != op.Release.Digest {
			return nil, ErrReleaseMismatch
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return nil, err
		}
		if err := b.finishInitialFencesAndHold(ctx, op, existing.Receipt); err != nil {
			return nil, err
		}
		if err := b.supersedeCompletedPredecessor(ctx, op, existing.Receipt); err != nil {
			return nil, err
		}
		return map[string]any{"cohort_operation_id": op.CohortOperationID, "release_id": op.Release.ID, "resumed": true,
			"superseded_predecessor_operation_id": op.PreviousCohortOperationID, "affected_hosts": HostOrder}, nil
	} else if err != nil {
		return nil, err
	}
	if op.PlanSnapshot == nil || len(op.PlanSnapshot.HostBaselines) != len(HostOrder) {
		return nil, ErrOperationCorrupted
	}
	if op.PreviousCohortOperationID != "" {
		if err := b.ValidateExistingCohortHold(ctx, *op.PlanSnapshot); err != nil {
			return nil, fmt.Errorf("revalidate completed predecessor cohort %s before backup: %w", op.PreviousCohortOperationID, err)
		}
	}
	for index, host := range HostOrder {
		baseline := op.PlanSnapshot.HostBaselines[index]
		if baseline.HostID != host || baseline.Lane != op.Lane {
			return nil, ErrOperationCorrupted
		}
		current, err := b.readLaneSnapshot(ctx, host, op.Lane)
		if err != nil || !current.Empty || selectedReleaseID(current.Selector) != baseline.SelectedReleaseID ||
			selectedBundleSHA(current.Selector) != baseline.SelectedBundleSHA || current.Config != baseline.ConfigurationSHA ||
			current.Schema != baseline.SchemaVersion || current.Work.SHA256() != baseline.WorkInventorySHA ||
			current.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
			return nil, errors.Join(fmt.Errorf("%s fresh pre-stop inventory, identity, or capacity no longer matches its read-only plan", host), err)
		}
		admission, err := b.readAdmission(ctx, host, op.Lane)
		if err != nil || admission.AdmissionHeld && admission.OperationID != op.PreviousCohortOperationID ||
			!admission.AdmissionHeld && op.PreviousCohortOperationID != "" {
			return nil, errors.Join(fmt.Errorf("%s lane already has an admission hold before this operation owns one", host), err)
		}
		for _, peer := range LaneOrder {
			if peer == op.Lane {
				continue
			}
			peerSnapshot, peerErr := b.readLaneSnapshot(ctx, host, peer)
			if peerErr != nil || op.PlanSnapshot.PeerLaneEvidence[host][peer] != promotionPeerSnapshotDigest(peerSnapshot) {
				return nil, errors.Join(fmt.Errorf("%s peer lane %s changed before common preparation", host, peer), peerErr)
			}
		}
	}
	reports := make(map[string]laneinstall.CatalogCohortHostPreflightReport, len(HostOrder))
	for _, host := range HostOrder {
		report, err := b.preflightReport(ctx, op, host)
		if err != nil {
			return nil, fmt.Errorf("%s preflight/backup: %w", host, err)
		}
		buffer := b.OperatingBuffer
		if buffer == 0 {
			buffer = defaultPromotionOperatingBuffer
		}
		reserve := uint64(0)
		if op.Lane == "prd" {
			reserve = prdSelectorReserve
		}
		if report.Preflight.AvailableBytes < buffer+reserve {
			return nil, fmt.Errorf("%s measured post-backup free space %d is below operating buffer %d plus selector reserve %d", host, report.Preflight.AvailableBytes, buffer, reserve)
		}
		reports[host] = report
	}
	manifestBytes, err := laneinstall.CatalogReleaseManifestBytes(catalogManifest(op.Release))
	if err != nil {
		return nil, err
	}
	manifestPath, removeManifest, err := writePrivateTemp(manifestBytes)
	if err != nil {
		return nil, err
	}
	defer removeManifest()
	preflightPaths := make([]string, 0, len(HostOrder))
	for _, host := range HostOrder {
		path, err := b.savePreflight(op, reports[host])
		if err != nil {
			return nil, err
		}
		preflightPaths = append(preflightPaths, path)
	}
	args := []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane, "--action", "create", "--operation-id", op.CohortOperationID,
		"--release-manifest", manifestPath, "--publisher-owner", b.PublisherOwner, "--publisher-control", b.PublisherControl,
		"--publishers-paused", "--no-new-accepted"}
	for _, path := range preflightPaths {
		args = append(args, "--preflight-file", path)
	}
	if _, err := b.runLane(ctx, "mac-control-plane", args, nil); err != nil {
		return nil, err
	}
	if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
		return nil, err
	}
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return nil, errors.Join(ErrOperationCorrupted, err)
	}
	if err := b.finishInitialFencesAndHold(ctx, op, view.Receipt); err != nil {
		return nil, err
	}
	if err := b.supersedeCompletedPredecessor(ctx, op, view.Receipt); err != nil {
		return nil, err
	}
	return map[string]any{"cohort_operation_id": op.CohortOperationID, "release_id": op.Release.ID,
		"receipt_stage": "held", "superseded_predecessor_operation_id": op.PreviousCohortOperationID,
		"affected_hosts": HostOrder, "host_preflights": reports}, nil
}

func (b *NativePromotionBackend) supersedeCompletedPredecessor(ctx context.Context, op Operation,
	successor laneinstall.CatalogCohortReceipt) error {
	predecessorID := op.PreviousCohortOperationID
	if predecessorID == "" {
		return nil
	}
	if predecessorID == successor.OperationID || successor.Stage != laneinstall.CatalogCohortHeld {
		return ErrOperationCorrupted
	}
	_, predecessor, err := b.synchronizeReceiptHead(ctx, op.Lane, predecessorID)
	if err != nil {
		return fmt.Errorf("synchronize completed predecessor history: %w", err)
	}
	if predecessor.Stage == laneinstall.CatalogCohortSuperseded {
		if cohortSupersessionSuccessor(predecessor) != successor.OperationID {
			return fmt.Errorf("completed predecessor was superseded by a different operation: %w", ErrOperationCorrupted)
		}
		return b.verifySuccessorHold(ctx, op.Lane, successor.OperationID)
	}
	if predecessor.Stage != laneinstall.CatalogCohortComplete {
		return fmt.Errorf("predecessor %s requires recovery before supersession: %w", predecessorID, ErrAdmissionHeld)
	}
	if current := cohortSupersessionSuccessor(predecessor); current != "" && current != successor.OperationID {
		return fmt.Errorf("predecessor supersession is bound to %s: %w", current, ErrOperationCorrupted)
	}
	if err := b.verifySuccessorHold(ctx, op.Lane, successor.OperationID); err != nil {
		return err
	}
	for _, host := range HostOrder {
		_, predecessor, err = b.synchronizeReceiptHead(ctx, op.Lane, predecessorID)
		if err != nil {
			return fmt.Errorf("synchronize predecessor before %s evidence: %w", host, err)
		}
		if cohortHostHasSupersession(predecessor, host, successor.OperationID) {
			continue
		}
		if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", op.Lane,
			"--action", "supersede-host", "--operation-id", predecessorID,
			"--successor-operation-id", successor.OperationID, "--subject-host", host}, nil); err != nil {
			return fmt.Errorf("record %s completed-predecessor observation: %w", host, err)
		}
		if _, _, err := b.synchronizeReceiptHead(ctx, op.Lane, predecessorID); err != nil {
			return fmt.Errorf("synchronize %s completed-predecessor observation: %w", host, err)
		}
	}
	_, predecessor, err = b.synchronizeReceiptHead(ctx, op.Lane, predecessorID)
	if err != nil || predecessor.Stage != laneinstall.CatalogCohortComplete {
		return errors.Join(ErrOperationCorrupted, err)
	}
	if cohortSupersessionSuccessor(predecessor) != successor.OperationID {
		return ErrOperationCorrupted
	}
	for _, host := range HostOrder {
		if !cohortHostHasSupersession(predecessor, host, successor.OperationID) {
			return fmt.Errorf("%s completed-predecessor evidence is missing: %w", host, ErrOperationCorrupted)
		}
	}
	if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
		"--action", "supersede", "--operation-id", predecessorID,
		"--successor-operation-id", successor.OperationID}, nil); err != nil {
		return fmt.Errorf("finalize completed-predecessor supersession: %w", err)
	}
	_, predecessor, err = b.synchronizeReceiptHead(ctx, op.Lane, predecessorID)
	if err != nil || predecessor.Stage != laneinstall.CatalogCohortSuperseded ||
		cohortSupersessionSuccessor(predecessor) != successor.OperationID {
		return errors.Join(ErrOperationCorrupted, err)
	}
	return b.verifySuccessorHold(ctx, op.Lane, successor.OperationID)
}

func (b *NativePromotionBackend) verifySuccessorHold(ctx context.Context, lane, operationID string) error {
	for _, host := range HostOrder {
		admission, err := b.readAdmission(ctx, host, lane)
		if err != nil || !admission.AdmissionHeld || admission.OperationID != operationID {
			return errors.Join(fmt.Errorf("%s does not report exactly the successor admission hold %s", host, operationID), err)
		}
	}
	return nil
}

// synchronizeReceiptHead selects only a receipt that safely extends every
// host copy. It never resolves divergent completed histories by guessing.
func (b *NativePromotionBackend) synchronizeReceiptHead(ctx context.Context, lane, operationID string) (string, laneinstall.CatalogCohortReceipt, error) {
	type copy struct {
		host    string
		receipt laneinstall.CatalogCohortReceipt
	}
	copies := make([]copy, 0, len(HostOrder))
	for _, host := range HostOrder {
		view, err := b.readReceipt(ctx, host, lane, operationID)
		if err != nil || !view.Present {
			return "", laneinstall.CatalogCohortReceipt{}, errors.Join(fmt.Errorf("%s receipt copy unavailable", host), err)
		}
		copies = append(copies, copy{host: host, receipt: view.Receipt})
	}
	var head *copy
	for index := range copies {
		candidate := &copies[index]
		advancesAll := true
		for other := range copies {
			if !laneinstall.CatalogCohortReceiptCanAdvance(copies[other].receipt, candidate.receipt) {
				advancesAll = false
				break
			}
		}
		if advancesAll {
			if head != nil && !reflectReceiptEqual(head.receipt, candidate.receipt) {
				return "", laneinstall.CatalogCohortReceipt{}, ErrOperationCorrupted
			}
			head = candidate
		}
	}
	if head == nil {
		return "", laneinstall.CatalogCohortReceipt{}, fmt.Errorf("receipt histories are not one safe linear extension: %w", ErrOperationCorrupted)
	}
	if err := b.syncReceiptFrom(ctx, head.host, lane, operationID); err != nil {
		return "", laneinstall.CatalogCohortReceipt{}, err
	}
	view, err := b.readReceipt(ctx, "mac-control-plane", lane, operationID)
	if err != nil || !view.Present {
		return "", laneinstall.CatalogCohortReceipt{}, errors.Join(ErrOperationCorrupted, err)
	}
	return head.host, view.Receipt, nil
}

func cohortSupersessionSuccessor(receipt laneinstall.CatalogCohortReceipt) string {
	successor := ""
	for _, transition := range receipt.Transitions {
		switch transition.Action {
		case "supersession-restart":
			successor = transition.SuccessorID
		case "supersede-host":
			if transition.Supersession != nil {
				successor = transition.Supersession.SuccessorOperationID
			}
		case "supersede":
			successor = transition.SuccessorID
		}
	}
	return successor
}

func cohortHostHasSupersession(receipt laneinstall.CatalogCohortReceipt, host, successor string) bool {
	start := 0
	for index, transition := range receipt.Transitions {
		if transition.Action == "supersession-restart" {
			start = index + 1
		}
	}
	for _, transition := range receipt.Transitions[start:] {
		if transition.Action == "supersede-host" && transition.HostID == host && transition.Supersession != nil &&
			transition.Supersession.SuccessorOperationID == successor {
			return true
		}
	}
	return false
}

func (b *NativePromotionBackend) finishInitialFencesAndHold(ctx context.Context, op Operation, receipt laneinstall.CatalogCohortReceipt) error {
	if op.Lane == "prd" {
		fenced := map[string]bool{}
		for _, transition := range receipt.Transitions {
			if transition.Action == "fence-host" {
				fenced[transition.HostID] = true
			}
		}
		for _, host := range HostOrder {
			if fenced[host] {
				continue
			}
			if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", op.Lane, "--action", "fence-host", "--operation-id", op.CohortOperationID, "--subject-host", host}, nil); err != nil {
				return fmt.Errorf("%s PRD fence: %w", host, err)
			}
			if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
				return fmt.Errorf("%s fence receipt synchronization: %w", host, err)
			}
			view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
			if err != nil || !view.Present {
				return errors.Join(ErrOperationCorrupted, err)
			}
			receipt = view.Receipt
		}
	}
	if receipt.Stage == laneinstall.CatalogCohortPrepared {
		evidence := digest([]byte(op.OperationID + "|" + op.Release.Digest + "|" + b.PublisherOwner + "|" + b.PublisherControl + "|paused|no-new-accepted"))
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane, "--action", "hold", "--operation-id", op.CohortOperationID, "--evidence-sha256", evidence}, nil); err != nil {
			return err
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return err
		}
	}
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present || view.Receipt.Stage != laneinstall.CatalogCohortHeld {
		return errors.Join(ErrOperationCorrupted, err)
	}
	return nil
}

func (b *NativePromotionBackend) preflightReport(ctx context.Context, op Operation, host string) (laneinstall.CatalogCohortHostPreflightReport, error) {
	// A retry gets a new read-only inventory and a fresh supported backup. A
	// previously captured preflight report is historical evidence; its age
	// alone cannot establish current work, selector, configuration, or capacity.
	args := []string{"catalog-host-preflight", "--host", host, "--lane", op.Lane, "--release-id", op.Release.ID}
	required := b.OperatingBuffer
	if required == 0 {
		required = defaultPromotionOperatingBuffer
	}
	if op.Lane == "prd" {
		required = prdSelectorReserve
		if requiresLegacyRollbackEvidence(op, host) {
			compatibility := b.Config.Hosts[host].LegacyCompatibilityFile
			if _, err := legacyCompatibilityInputSHA(b.Config.Hosts[host]); err != nil {
				return laneinstall.CatalogCohortHostPreflightReport{}, fmt.Errorf("%s legacy rollback compatibility input: %w", host, err)
			}
			args = append(args, "--legacy-compatibility-file", compatibility)
		}
	}
	args = append(args, "--required-free-bytes", fmt.Sprint(required))
	out, err := b.runLane(ctx, host, args, nil)
	if err != nil {
		return laneinstall.CatalogCohortHostPreflightReport{}, err
	}
	var report laneinstall.CatalogCohortHostPreflightReport
	if err := decodeStrictJSON(out, &report); err != nil || report.Preflight.HostID != host || report.Preflight.Target.ReleaseID != op.Release.ID ||
		report.Preflight.Target.ReleaseSHA256 != op.Release.Digest || report.Preflight.RequiredBytes != required || !report.Preflight.WorkInventoryEmpty {
		return laneinstall.CatalogCohortHostPreflightReport{}, ErrOperationCorrupted
	}
	if err := b.savePreflightBytes(op, host, out); err != nil {
		return laneinstall.CatalogCohortHostPreflightReport{}, err
	}
	return report, nil
}

func requiresLegacyRollbackEvidence(op Operation, host string) bool {
	if op.Lane != "prd" || op.PlanSnapshot == nil {
		return false
	}
	for _, baseline := range op.PlanSnapshot.HostBaselines {
		if baseline.HostID == host {
			return baseline.PreviousReleaseID == ""
		}
	}
	return false
}

func (b *NativePromotionBackend) savePreflight(op Operation, report laneinstall.CatalogCohortHostPreflightReport) (string, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	path := b.preflightPath(op, report.Preflight.HostID)
	if err := writePrivateAtomic(path, append(data, '\n')); err != nil {
		return "", err
	}
	return path, nil
}

func (b *NativePromotionBackend) savePreflightBytes(op Operation, host string, data []byte) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return ErrOperationCorrupted
	}
	var report laneinstall.CatalogCohortHostPreflightReport
	if decodeStrictJSON(data, &report) != nil || report.Preflight.HostID != host {
		return ErrOperationCorrupted
	}
	return writePrivateAtomic(b.preflightPath(op, host), data)
}

func (b *NativePromotionBackend) preflightPath(op Operation, host string) string {
	return filepath.Join(b.StoreRoot, "operations", op.Lane, op.OperationID, "preflights", host+".json")
}

func (b *NativePromotionBackend) ObserveHost(ctx context.Context, op Operation, host string) (HostObservation, error) {
	return b.observeHost(ctx, op, host, true)
}

func (b *NativePromotionBackend) observeHost(ctx context.Context, op Operation, host string, synchronizeActiveSwitch bool) (HostObservation, error) {
	status, snapshot, err := b.currentHost(ctx, host, op.Lane)
	if err != nil {
		return HostObservation{}, err
	}
	admission, err := b.readAdmission(ctx, host, op.Lane)
	if err != nil || !admission.AdmissionHeld || admission.OperationID != op.CohortOperationID {
		return HostObservation{}, errors.Join(ErrAdmissionHeld, err)
	}
	receiptView, err := b.readReceipt(ctx, host, op.Lane, op.CohortOperationID)
	if err != nil || !receiptView.Present {
		return HostObservation{}, errors.Join(ErrOperationCorrupted, err)
	}
	idx := hostIndex(host)
	if idx < 0 || idx >= len(receiptView.Receipt.Hosts) {
		return HostObservation{}, ErrOperationCorrupted
	}
	preflight := receiptView.Receipt.Hosts[idx]
	selectedID := selectedReleaseID(status)
	selectedSHA := selectedBundleSHA(status)
	selectorOp := ""
	journalVerified := false
	if selectedID == op.Release.ID && selectedSHA == op.Release.Bundles[host].SHA256 {
		ops, listErr := b.findSelectorOperations(ctx, host, op.Lane, op.CohortOperationID)
		if listErr != nil {
			return HostObservation{}, listErr
		}
		if len(ops) == 1 {
			selectorOp = ops[0].OperationID
			journalVerified = ops[0].Stage == "complete" && receiptHasSwitch(receiptView.Receipt, host)
		} else if len(ops) > 1 {
			return HostObservation{}, ErrOperationCorrupted
		}
	}
	if snapshot.Active {
		// A prior controller attempt can have completed the exact selected-lane
		// selector switch before receipt synchronization failed. Recover only
		// when that candidate is selected, its journal is complete, its switch
		// transition is present, and this operation still owns the admission
		// hold. syncReceiptFrom rechecks all four work/config/schema identities,
		// stops only this held lane, and synchronizes the preserved receipt
		// history before this observation is repeated.
		if !synchronizeActiveSwitch || selectedID != op.Release.ID ||
			selectedSHA != op.Release.Bundles[host].SHA256 || !journalVerified {
			return HostObservation{}, laneinstall.ErrSimpleLaneNotQuiescent
		}
		if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
			return HostObservation{}, fmt.Errorf("reconcile active selected %s before observation: %w", host, err)
		}
		return b.observeHost(ctx, op, host, false)
	}
	if !snapshot.Empty || snapshot.Config != preflight.ConfigurationSHA256 || snapshot.Schema != preflight.SchemaVersion || snapshot.Work.SHA256() != preflight.WorkInventorySHA256 {
		return HostObservation{}, laneinstall.ErrSimpleLaneNotQuiescent
	}
	capacityRequired := promotionRequiredCapacity(op.Lane, b.OperatingBuffer)
	capacityVerified := snapshot.AvailableBytes >= capacityRequired
	return HostObservation{HostID: host, Lane: op.Lane, SelectedReleaseID: selectedID, SelectedBundleSHA: selectedSHA,
		SelectorOperation: selectorOp, JournalVerified: journalVerified, ServiceStopped: !snapshot.Active,
		WorkInventorySHA: snapshot.Work.SHA256(), WorkInventoryEmpty: snapshot.Empty,
		ConfigurationSHA: snapshot.Config, SchemaVersion: snapshot.Schema, BackupVerified: validPreflightBackupReference(preflight),
		CapacityVerified: capacityVerified, AvailableBytes: snapshot.AvailableBytes, RequiredBytes: capacityRequired,
		CanSwitch:         capacityVerified,
		PreviousReleaseID: selectedID, PreviousBundleSHA: preflight.PreviousBundleSHA256,
		PreviousSelectorOperation: preflight.PreviousTarget, BackupSHA: preflight.BackupManifestSHA256}, nil
}

func (b *NativePromotionBackend) SwitchHost(ctx context.Context, op Operation, host string) (HostObservation, error) {
	view, err := b.readReceipt(ctx, host, op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return HostObservation{}, errors.Join(ErrOperationCorrupted, err)
	}
	existing, err := b.findSelectorOperations(ctx, host, op.Lane, op.CohortOperationID)
	if err != nil {
		return HostObservation{}, err
	}
	if len(existing) > 1 {
		return HostObservation{}, ErrOperationCorrupted
	}
	if len(existing) == 1 {
		if existing[0].Stage == "complete" || existing[0].Stage == "prepared" || existing[0].Stage == "switched" {
			if _, err := b.runLane(ctx, host, []string{"catalog-reconcile", "--host", host, "--lane", op.Lane,
				"--cohort-operation-id", op.CohortOperationID, "--selector-operation-id", existing[0].OperationID}, nil); err != nil {
				return HostObservation{}, err
			}
			if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
				return HostObservation{}, err
			}
			return b.verifiedHostObservation(ctx, op, host, existing[0].OperationID)
		}
		return HostObservation{}, errors.New("selector journal is outside the supported promotion reconciliation stages")
	}
	if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", op.Lane,
		"--action", "preflight", "--operation-id", op.CohortOperationID, "--subject-host", host}, nil); err != nil {
		return HostObservation{}, err
	}
	if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
		return HostObservation{}, err
	}
	view, err = b.readReceipt(ctx, host, op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return HostObservation{}, errors.Join(ErrOperationCorrupted, err)
	}
	preflightSHA := lastTransitionEvidence(view.Receipt, "preflight", host)
	if !digestPattern.MatchString(preflightSHA) {
		return HostObservation{}, ErrOperationCorrupted
	}
	preflight := view.Receipt.Hosts[hostIndex(host)]
	current, snapshot, err := b.currentHost(ctx, host, op.Lane)
	if err != nil || snapshot.Active || !snapshot.Empty || selectedBundleSHA(current) != preflight.PreviousBundleSHA256 ||
		current.Target != preflight.PreviousTarget || snapshot.Config != preflight.ConfigurationSHA256 ||
		snapshot.Schema != preflight.SchemaVersion || snapshot.Work.SHA256() != preflight.WorkInventorySHA256 ||
		snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
		return HostObservation{}, errors.Join(fmt.Errorf("%s fresh two-minute selector preflight state or operating capacity check failed", host), err)
	}
	args := []string{"catalog-select", "--host", host, "--lane", op.Lane, "--release-id", op.Release.ID,
		"--cohort-operation-id", op.CohortOperationID, "--evidence-sha256", preflightSHA}
	if op.Lane != "prd" {
		args = append(args, "--expected-target", preflight.PreviousTarget)
	}
	out, err := b.runLane(ctx, host, args, nil)
	if err != nil {
		return HostObservation{}, err
	}
	var selector laneinstall.CatalogSelectorOperation
	if decodeStrictJSON(out, &selector) != nil || selector.OperationID == "" || selector.Stage != "complete" {
		return HostObservation{}, ErrOperationCorrupted
	}
	if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
		return HostObservation{}, err
	}
	return b.verifiedHostObservation(ctx, op, host, selector.OperationID)
}

func (b *NativePromotionBackend) verifiedHostObservation(ctx context.Context, op Operation, host, selectorOperation string) (HostObservation, error) {
	status, snapshot, err := b.currentHost(ctx, host, op.Lane)
	if err != nil {
		return HostObservation{}, err
	}
	selector := status
	obs := HostObservation{HostID: host, Lane: op.Lane, SelectedReleaseID: selectedReleaseID(selector), SelectedBundleSHA: selectedBundleSHA(selector),
		SelectorOperation: selectorOperation, JournalVerified: selectorOperation != "", ServiceStopped: !snapshot.Active,
		WorkInventorySHA: snapshot.Work.SHA256(), WorkInventoryEmpty: snapshot.Empty, ConfigurationSHA: snapshot.Config,
		SchemaVersion: snapshot.Schema, BackupVerified: true,
		CapacityVerified: snapshot.AvailableBytes >= promotionRequiredCapacity(op.Lane, b.OperatingBuffer),
		AvailableBytes:   snapshot.AvailableBytes, RequiredBytes: promotionRequiredCapacity(op.Lane, b.OperatingBuffer),
		CanSwitch: snapshot.AvailableBytes >= promotionRequiredCapacity(op.Lane, b.OperatingBuffer)}
	if obs.SelectedReleaseID != op.Release.ID || obs.SelectedBundleSHA != op.Release.Bundles[host].SHA256 || !snapshot.Empty {
		return HostObservation{}, errors.New("selected host post-switch identity or empty-work guard failed")
	}
	return obs, nil
}

func (b *NativePromotionBackend) startIfStopped(ctx context.Context, host, lane string, active bool) error {
	if active {
		return nil
	}
	_, err := b.runLane(ctx, host, serviceAction(lane, "start", host), nil)
	return err
}

// startHostUnderHold starts only the selected lane, and only after checking
// the exact cohort hold, selected bytes, empty work inventory, and operating
// buffer. Receipt synchronization stops the lane again before publishing the
// next host receipt, so the hold remains in force throughout the sequence.
func (b *NativePromotionBackend) startHostUnderHold(ctx context.Context, op Operation, host string) error {
	admission, err := b.readAdmission(ctx, host, op.Lane)
	if err != nil || !admission.AdmissionHeld || admission.OperationID != op.CohortOperationID {
		return errors.Join(fmt.Errorf("%s selected-lane admission hold changed before service start", host), err)
	}
	status, snapshot, err := b.currentHost(ctx, host, op.Lane)
	wantID, wantBundle := op.Release.ID, op.Release.Bundles[host].SHA256
	if op.Stage == StageRollingBack {
		index := hostIndex(host)
		if index < 0 {
			return ErrInvalidInput
		}
		wantID, wantBundle = op.Hosts[index].PreviousReleaseID, op.Hosts[index].PreviousBundleSHA256
	}
	if err != nil || wantID == "" || selectedReleaseID(status) != wantID || selectedBundleSHA(status) != wantBundle ||
		!snapshot.Empty || snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
		return errors.Join(fmt.Errorf("%s selected identity, empty-work, or capacity guard failed before service start", host), err)
	}
	if err := b.startIfStopped(ctx, host, op.Lane, snapshot.Active); err != nil {
		return err
	}
	status, snapshot, err = b.currentHost(ctx, host, op.Lane)
	if err != nil || selectedReleaseID(status) != wantID || selectedBundleSHA(status) != wantBundle ||
		!snapshot.Active || !snapshot.Empty || snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
		return errors.Join(fmt.Errorf("%s selected service did not start with the held identity and empty inventory", host), err)
	}
	return nil
}

// startReleasedCohort runs only after one released receipt has synchronized
// to all hosts. It starts the selected lane in cohort order and verifies each
// exact selector, active service, empty inventory, and capacity before the
// promotion can report completion.
func (b *NativePromotionBackend) startReleasedCohort(ctx context.Context, op Operation) error {
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present || view.Receipt.Stage != laneinstall.CatalogCohortReleased {
		return errors.Join(ErrOperationCorrupted, err)
	}
	for index, host := range HostOrder {
		admission, err := b.readAdmission(ctx, host, op.Lane)
		if err != nil || admission.AdmissionHeld || admission.OperationID != "" {
			return errors.Join(fmt.Errorf("%s admissions are not released from the synchronized cohort receipt", host), err)
		}
		status, snapshot, err := b.currentHost(ctx, host, op.Lane)
		if err != nil {
			return err
		}
		wantID, wantBundle := op.Release.ID, op.Release.Bundles[host].SHA256
		if op.Stage == StageRollingBack {
			wantID, wantBundle = op.Hosts[index].PreviousReleaseID, op.Hosts[index].PreviousBundleSHA256
		}
		if wantID == "" || selectedReleaseID(status) != wantID || selectedBundleSHA(status) != wantBundle || !snapshot.Empty ||
			snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
			return fmt.Errorf("%s released selector, empty-work, or capacity postflight failed", host)
		}
		// Acceptance runs while selected processes are started under the cohort
		// hold. Their admission state is read once during startup, so changing
		// the synchronized receipt to Released does not reopen an already-running
		// process. Restart an active, empty selected-lane service after release
		// so it reloads the now-open admission state. Recheck the stopped state
		// before starting it again.
		if snapshot.Active {
			if _, err := b.runLane(ctx, host, serviceAction(op.Lane, "stop", host), nil); err != nil {
				return fmt.Errorf("stop %s service to reload released admissions: %w", host, err)
			}
			status, snapshot, err = b.currentHost(ctx, host, op.Lane)
			if err != nil || selectedReleaseID(status) != wantID || selectedBundleSHA(status) != wantBundle ||
				snapshot.Active || !snapshot.Empty || snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
				return errors.Join(fmt.Errorf("%s did not stop cleanly with the released selector and empty-work guard", host), err)
			}
		}
		if err := b.startIfStopped(ctx, host, op.Lane, snapshot.Active); err != nil {
			return fmt.Errorf("start released %s service: %w", host, err)
		}
		status, snapshot, err = b.currentHost(ctx, host, op.Lane)
		if err != nil || !snapshot.Active || !snapshot.Empty || selectedReleaseID(status) != wantID ||
			selectedBundleSHA(status) != wantBundle || snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
			return errors.Join(fmt.Errorf("%s released service postflight failed", host), err)
		}
	}
	if op.PlanSnapshot != nil {
		peerEvidence, err := b.readPeerEvidence(ctx, op.Lane)
		if err != nil || !equalNestedStringMaps(peerEvidence, op.PlanSnapshot.PeerLaneEvidence) {
			return errors.Join(errors.New("peer-lane isolation changed during selected-lane restart"), err)
		}
	}
	return nil
}

func (b *NativePromotionBackend) AcceptCohort(ctx context.Context, op Operation) (Acceptance, error) {
	macPlan, err := laneinstall.BuildCatalogPlan("mac-control-plane", config.ReleaseLane(op.Lane))
	if err != nil {
		return Acceptance{}, err
	}
	// The maintenance probe guard is intentionally available only after the
	// synchronized cohort receipt enters Verifying. Prepare that durable state
	// before issuing any probe capability; previously ReleaseCohort advanced
	// the receipt only after acceptance, which made the first probe impossible.
	if err := b.ensureCohortVerifying(ctx, op); err != nil {
		return Acceptance{}, err
	}
	for _, host := range HostOrder {
		if err := b.startHostUnderHold(ctx, op, host); err != nil {
			return Acceptance{}, err
		}
		status, snapshot, err := b.currentHost(ctx, host, op.Lane)
		if err != nil || selectedReleaseID(status) != op.Release.ID || selectedBundleSHA(status) != op.Release.Bundles[host].SHA256 ||
			!snapshot.Active || !snapshot.Empty || snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
			return Acceptance{}, errors.Join(fmt.Errorf("%s runtime identity, service, empty-work, or capacity check failed", host), err)
		}
	}
	loaded, err := config.LoadFile(macPlan.Namespace.ConfigFile)
	if err != nil || loaded.Kind() != config.HostKindMac || loaded.ReleaseLane() != config.ReleaseLane(op.Lane) {
		return Acceptance{}, errors.New("validated Mac lane configuration is unavailable for functional acceptance")
	}
	results := make([]promotionProbeOutcome, 0, 7)
	for _, host := range HostOrder[:3] {
		remote, ok := loaded.RemoteHost(host)
		if !ok || remote.DirectEndpoint == nil || remote.DirectEndpoint.ControllerID == "" {
			return Acceptance{}, fmt.Errorf("%s configured direct-mTLS controller binding is unavailable", host)
		}
		for _, transport := range []string{"direct-mtls", "queued-ssh"} {
			controller := remote.DirectEndpoint.ControllerID
			if transport == "queued-ssh" {
				controller = config.MacAccount
			}
			outcome, err := b.runProbe(ctx, op, host, transport, controller, macPlan.Namespace.ConfigFile, "")
			if err != nil {
				return Acceptance{}, fmt.Errorf("%s %s functional probe: %w", host, transport, err)
			}
			results = append(results, outcome)
		}
	}
	local, err := b.runProbe(ctx, op, "mac-control-plane", "mac-local", config.MacAccount, "", macPlan.Namespace.APISocket)
	if err != nil {
		return Acceptance{}, fmt.Errorf("Mac-local functional probe: %w", err)
	}
	results = append(results, local)
	peerEvidence, err := b.readPeerEvidence(ctx, op.Lane)
	if err != nil {
		return Acceptance{}, err
	}
	if op.PlanSnapshot == nil || !equalNestedStringMaps(peerEvidence, op.PlanSnapshot.PeerLaneEvidence) {
		return Acceptance{}, errors.New("peer lane state changed since the promotion plan; isolation acceptance failed")
	}
	for _, host := range HostOrder {
		_, snapshot, err := b.currentHost(ctx, host, op.Lane)
		if err != nil || !snapshot.Empty || snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
			return Acceptance{}, errors.Join(fmt.Errorf("%s post-probe work inventory or capacity check failed", host), err)
		}
	}
	for _, result := range results {
		if !promotionProbeOutcomeAccepted(result) {
			return Acceptance{}, errors.New("maintenance probe did not produce a complete successful terminal result and confirmed teardown")
		}
	}
	hashes := make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		hashes[host] = op.Release.Bundles[host].SHA256
	}
	gateEvidence := map[string]any{"probes": results, "peer_lane_evidence": peerEvidence, "operating_buffer_bytes": b.OperatingBuffer,
		"all_host_selected_hashes": hashes, "post_probe_work_empty": true}
	return Acceptance{Profile: op.Profile, ReleaseID: op.Release.ID, ReleaseSHA256: op.Release.Digest, BundleHashes: hashes,
		Passed: true, Gates: map[string]string{"direct-mtls": "passed", "queued-ssh": "passed", "mac-local": "passed", "limits": "passed",
			"capacity": "passed", "lifecycle": "passed", "teardown": "passed", "isolation": "passed"}, EvidenceSHA: hashJSON(gateEvidence)}, nil
}

// ensureCohortVerifying performs the receipt transitions required before
// catalog-probe-prepare can mint one-time capabilities. Each mutation uses
// the public host command and synchronizes the complete receipt while the
// selected lane is stopped and empty. It is idempotent across interruptions.
func (b *NativePromotionBackend) ensureCohortVerifying(ctx context.Context, op Operation) error {
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return errors.Join(ErrOperationCorrupted, err)
	}
	if view.Receipt.Stage == laneinstall.CatalogCohortHeld || view.Receipt.Stage == laneinstall.CatalogCohortSwitching {
		if err := b.startHostUnderHold(ctx, op, "mac-control-plane"); err != nil {
			return err
		}
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
			"--action", "verify-start", "--operation-id", op.CohortOperationID, "--evidence-sha256", operationAcceptanceDigest(op)}, nil); err != nil {
			return err
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return err
		}
	}
	for _, host := range HostOrder {
		view, err = b.readReceipt(ctx, host, op.Lane, op.CohortOperationID)
		if err != nil || !view.Present {
			return errors.Join(ErrOperationCorrupted, err)
		}
		if view.Receipt.Stage != laneinstall.CatalogCohortVerifying {
			return fmt.Errorf("%s cohort receipt is not in verifying state before acceptance: %w", host, ErrOperationCorrupted)
		}
		if receiptHasVerify(view.Receipt, host) {
			continue
		}
		if err := b.startHostUnderHold(ctx, op, host); err != nil {
			return err
		}
		if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", op.Lane,
			"--action", "verify", "--operation-id", op.CohortOperationID, "--subject-host", host,
			"--evidence-sha256", operationAcceptanceDigest(op)}, nil); err != nil {
			return err
		}
		if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
			return err
		}
	}
	view, err = b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present || view.Receipt.Stage != laneinstall.CatalogCohortVerifying {
		return errors.Join(errors.New("cohort receipt did not reach synchronized verifying state before acceptance"), err)
	}
	for _, host := range HostOrder {
		if !receiptHasVerify(view.Receipt, host) {
			return fmt.Errorf("cohort receipt lacks verification for %s before acceptance: %w", host, ErrOperationCorrupted)
		}
	}
	return nil
}

func (b *NativePromotionBackend) runProbe(ctx context.Context, op Operation, host, transport, controller, configPath, socket string) (promotionProbeOutcome, error) {
	preparedArgs := []string{"catalog-probe-prepare", "--host", host, "--lane", op.Lane, "--transport", transport,
		"--operation-id", op.CohortOperationID, "--controller-id", controller, "--ttl-seconds", "120"}
	observeArgs := append(append([]string(nil), preparedArgs...), "--observe-existing")
	observedBytes, err := b.runLane(ctx, host, observeArgs, nil)
	if err != nil {
		return promotionProbeOutcome{}, err
	}
	var existing promotionProbeScopeObservation
	if decodeStrictJSON(observedBytes, &existing) != nil {
		return promotionProbeOutcome{}, ErrOperationCorrupted
	}
	disposition, err := existingProbeDisposition(existing, host, op.Lane, transport, op.CohortOperationID, controller, time.Now().UTC())
	if err != nil {
		return promotionProbeOutcome{}, err
	}
	if disposition == "observe" {
		if recovered, ok := recoverAcceptedProbeFromOperation(op, existing, host, transport, controller); ok {
			return recovered, nil
		}
		observationBytes, err := json.Marshal(existing)
		if err != nil {
			return promotionProbeOutcome{}, err
		}
		return b.observeAcceptedProbe(ctx, op, host, transport, configPath, socket, observationBytes, existing)
	}
	preparedBytes, err := b.runLane(ctx, host, preparedArgs, nil)
	if err != nil {
		return promotionProbeOutcome{}, err
	}
	var prepared promotionPreparedProbe
	if decodeStrictJSON(preparedBytes, &prepared) != nil || validatePreparedPromotionProbe(prepared, host, op.Lane, transport, op.CohortOperationID, controller) != nil {
		return promotionProbeOutcome{}, ErrOperationCorrupted
	}
	if err := validatePromotionProbeRequest(prepared); err != nil {
		return promotionProbeOutcome{}, errors.Join(ErrOperationCorrupted, err)
	}
	fileBytes, _ := json.Marshal(prepared)
	path, cleanup, err := writePrivateTemp(fileBytes)
	if err != nil {
		return promotionProbeOutcome{}, err
	}
	defer cleanup()
	mode := map[string]string{"direct-mtls": "direct", "queued-ssh": "queued", "mac-local": "local"}[transport]
	if mode == "" {
		return promotionProbeOutcome{}, ErrInvalidInput
	}
	profile := b.Config.Hosts["mac-control-plane"]
	args := []string{"run", "./src/cmd/catalog-probe-once", "--mode", mode, "--prepared", path}
	if mode == "local" {
		args = append(args, "--socket", socket)
	} else {
		args = append(args, "--config", configPath)
	}
	cmd := exec.CommandContext(ctx, profile.GoBinary, args...)
	cmd.Dir = b.Repository
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	probeErr := cmd.Run()
	var outcome promotionProbeOutcome
	if decodeStrictJSON(stdout.Bytes(), &outcome) != nil || outcome.HostID != host || outcome.OperationID != op.CohortOperationID ||
		outcome.Transport != transport || outcome.AcceptanceEvidence != nil {
		return promotionProbeOutcome{}, fmt.Errorf("catalog-probe-once did not emit a valid bounded outcome (exit=%v): %w", probeErr, ErrOperationCorrupted)
	}
	if probeErr != nil {
		return outcome, fmt.Errorf("catalog-probe-once failed: %w", probeErr)
	}
	outcome.ConfigurationSHA = prepared.ConfigurationSHA256
	outcome.RequestSHA256 = prepared.RequestSHA256
	outcome.RequestedTimeout = 5
	return outcome, nil
}

func (b *NativePromotionBackend) observeAcceptedProbe(ctx context.Context, op Operation, host, transport, configPath, socket string,
	observationBytes []byte, observation promotionProbeScopeObservation) (promotionProbeOutcome, error) {
	path, cleanup, err := writePrivateTemp(observationBytes)
	if err != nil {
		return promotionProbeOutcome{}, err
	}
	defer cleanup()
	profile := b.Config.Hosts["mac-control-plane"]
	args := []string{"run", "./src/cmd/catalog-probe-once", "--mode", "observe", "--prepared", path}
	if transport == "mac-local" {
		args = append(args, "--socket", socket)
	} else {
		args = append(args, "--config", configPath)
	}
	cmd := exec.CommandContext(ctx, profile.GoBinary, args...)
	cmd.Dir = b.Repository
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	observeErr := cmd.Run()
	var outcome promotionProbeOutcome
	if decodeStrictJSON(stdout.Bytes(), &outcome) != nil || outcome.HostID != host || outcome.OperationID != op.CohortOperationID ||
		outcome.Transport != transport || outcome.JobID != observation.JobID || !outcome.ObservedExisting || outcome.AcceptanceEvidence != nil {
		return promotionProbeOutcome{}, fmt.Errorf("existing accepted probe could not be safely observed (exit=%v): %w", observeErr, ErrOperationCorrupted)
	}
	if observeErr != nil {
		return outcome, fmt.Errorf("existing accepted probe did not pass bounded status observation: %w", observeErr)
	}
	outcome.ConfigurationSHA = observation.ConfigurationSHA256
	outcome.RequestSHA256 = observation.RequestSHA256
	outcome.RequestedTimeout = 5
	return outcome, nil
}

func validProbeJobIdentity(value string) bool {
	return len(value) == 36 && strings.HasPrefix(value, "job-") && strings.Trim(value[4:], "0123456789abcdef") == ""
}

const (
	acceptedProbeDecodeFailure  = "catalog-probe-once did not emit a valid bounded outcome (exit=<nil>): release workflow record is invalid or corrupted"
	acceptedProbeRecoveryMethod = "hash-chained-controller-error-and-helper-exit-zero"
	acceptedProbeHelperContract = "catalog-probe-once/accepted-outcome-v1"
)

// recoverAcceptedProbeFromOperation reconstructs only the fields guaranteed by
// catalog-probe-once's exit-zero contract. A controller event records that the
// helper itself exited successfully, but its JSON was rejected by the older
// controller decoder. The exact accepted scope binds the job and request; the
// event must fall inside that scope's creation/expiry interval. A later failed
// status-only observation of this same accepted scope does not erase that
// immutable proof. This path is read-only and deliberately leaves the numeric
// terminal sequence and helper output unavailable rather than inventing them.
func recoverAcceptedProbeFromOperation(op Operation, existing promotionProbeScopeObservation,
	host, transport, controller string) (promotionProbeOutcome, bool) {
	if ValidateOperation(op) != nil || op.CohortOperationID == "" ||
		op.OperationID != op.CohortOperationID || len(op.Events) == 0 {
		return promotionProbeOutcome{}, false
	}
	switch op.Stage {
	case StageBlocked, StagePrepared, StageVerifying:
	default:
		return promotionProbeOutcome{}, false
	}
	wantError := host + " " + transport + " functional probe: " + acceptedProbeDecodeFailure
	if op.Stage == StageBlocked && op.LastError == "" || op.Stage != StageBlocked && op.LastError != "" {
		return promotionProbeOutcome{}, false
	}
	originalIndex := -1
	for index, candidate := range op.Events {
		if candidate.Action != "acceptance" || candidate.HostID != "" || candidate.Intent || candidate.Outcome != "failed" {
			continue
		}
		var evidence struct {
			Error string `json:"error"`
		}
		if decodeStrictJSON(candidate.Evidence, &evidence) == nil && evidence.Error == wantError {
			if originalIndex >= 0 {
				return promotionProbeOutcome{}, false
			}
			originalIndex = index
		}
	}
	if originalIndex < 0 {
		return promotionProbeOutcome{}, false
	}
	for _, later := range op.Events[originalIndex+1:] {
		if later.Action != "acceptance" || later.HostID != "" || later.Intent || later.Outcome != "failed" {
			return promotionProbeOutcome{}, false
		}
		var laterEvidence struct {
			Error string `json:"error"`
		}
		if decodeStrictJSON(later.Evidence, &laterEvidence) != nil || !acceptedProbeObserveFailure(laterEvidence.Error, host, transport) {
			return promotionProbeOutcome{}, false
		}
	}
	if op.Stage == StageBlocked {
		var latestEvidence struct {
			Error string `json:"error"`
		}
		if decodeStrictJSON(op.Events[len(op.Events)-1].Evidence, &latestEvidence) != nil || latestEvidence.Error != op.LastError {
			return promotionProbeOutcome{}, false
		}
	}
	event := op.Events[originalIndex]
	if event.Sequence != originalIndex+1 {
		return promotionProbeOutcome{}, false
	}
	eventAt, eventErr := time.Parse(time.RFC3339Nano, event.AtUTC)
	scopeCreated, createdErr := time.Parse(time.RFC3339Nano, existing.CreatedAt)
	scopeExpires, expiresErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
	if eventErr != nil || createdErr != nil || expiresErr != nil || !scopeExpires.After(scopeCreated) ||
		scopeExpires.Sub(scopeCreated) > 10*time.Minute || eventAt.Before(scopeCreated) || !eventAt.Before(scopeExpires) {
		return promotionProbeOutcome{}, false
	}
	if disposition, err := existingProbeDisposition(existing, host, op.Lane, transport, op.CohortOperationID, controller, eventAt); err != nil || disposition != "observe" {
		return promotionProbeOutcome{}, false
	}
	if expectedConfig, ok := promotionBaselineConfigurationSHA(op, host); !ok || existing.ConfigurationSHA256 != expectedConfig {
		return promotionProbeOutcome{}, false
	}
	zero := 0
	return promotionProbeOutcome{
		Transport: transport, HostID: host, OperationID: op.CohortOperationID, JobID: existing.JobID,
		Phase: "complete", CommandState: "succeeded", ExitCode: &zero,
		OutputComplete: true, OutputTruncated: false, TeardownState: "closed", ObservedExisting: true,
		ConfigurationSHA: existing.ConfigurationSHA256, RequestSHA256: existing.RequestSHA256, RequestedTimeout: 5,
		AcceptanceEvidence: &promotionProbeAcceptanceEvidence{
			Method: acceptedProbeRecoveryMethod, HelperExitCode: 0, HelperContract: acceptedProbeHelperContract,
			ControllerFailureEventSequence: event.Sequence, ControllerFailureEventSHA256: event.SHA256,
			ControllerFailureEventAtUTC: event.AtUTC, ScopeCreatedAt: existing.CreatedAt, ScopeExpiresAt: existing.ExpiresAt,
			HelperVerifiedPositiveFinalSequence: true,
		},
	}, true
}

func acceptedProbeObserveFailure(errorText, host, transport string) bool {
	prefix := host + " " + transport + " functional probe: existing accepted probe could not be safely observed (exit="
	suffix := "): " + ErrOperationCorrupted.Error()
	return strings.HasPrefix(errorText, prefix) && strings.HasSuffix(errorText, suffix) &&
		len(errorText) > len(prefix)+len(suffix)
}

func promotionBaselineConfigurationSHA(op Operation, host string) (string, bool) {
	if op.PlanSnapshot == nil {
		return "", false
	}
	for _, baseline := range op.PlanSnapshot.HostBaselines {
		if baseline.HostID == host && digestPattern.MatchString(baseline.ConfigurationSHA) {
			return baseline.ConfigurationSHA, true
		}
	}
	return "", false
}

func promotionProbeOutcomeAccepted(result promotionProbeOutcome) bool {
	terminalFieldsPass := result.Phase == "complete" && result.CommandState == "succeeded" &&
		result.ExitCode != nil && *result.ExitCode == 0 && result.OutputComplete && !result.OutputTruncated && result.TeardownState == "closed"
	if !terminalFieldsPass {
		return false
	}
	if result.AcceptanceEvidence == nil {
		return result.FinalEventSequence != nil && *result.FinalEventSequence > 0
	}
	proof := result.AcceptanceEvidence
	eventAt, eventErr := time.Parse(time.RFC3339Nano, proof.ControllerFailureEventAtUTC)
	created, createdErr := time.Parse(time.RFC3339Nano, proof.ScopeCreatedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, proof.ScopeExpiresAt)
	return result.FinalEventSequence == nil && result.AcceptedAt == "" && result.ObservedExisting &&
		contains(HostOrder, result.HostID) && operationPattern.MatchString(result.OperationID) && expectedProbeControllerType(result.Transport) != "" &&
		validProbeJobIdentity(result.JobID) &&
		digestPattern.MatchString(result.ConfigurationSHA) && digestPattern.MatchString(result.RequestSHA256) && result.RequestedTimeout == 5 &&
		proof.Method == acceptedProbeRecoveryMethod && proof.HelperExitCode == 0 &&
		proof.HelperContract == acceptedProbeHelperContract && proof.ControllerFailureEventSequence > 0 &&
		digestPattern.MatchString(proof.ControllerFailureEventSHA256) && proof.HelperVerifiedPositiveFinalSequence &&
		eventErr == nil && createdErr == nil && expiresErr == nil && !eventAt.Before(created) && eventAt.Before(expires) &&
		expires.After(created) && expires.Sub(created) <= 10*time.Minute
}

func existingProbeDisposition(existing promotionProbeScopeObservation, host, lane, transport, operationID, controller string, now time.Time) (string, error) {
	expectedKey := "catalog-probe-" + operationID + "-" + host
	if transport != "direct-mtls" {
		expectedKey += "-" + transport
	}
	if existing.HostID != host || existing.Lane != lane || string(existing.Transport) != transport || existing.OperationID != operationID ||
		existing.ControllerType != expectedProbeControllerType(transport) || existing.ControllerID != controller || existing.Environment == "" ||
		!digestPattern.MatchString(existing.ConfigurationSHA256) || !digestPattern.MatchString(existing.RequestSHA256) || existing.IdempotencyKey != expectedKey {
		return "", ErrOperationCorrupted
	}
	switch existing.State {
	case "absent":
		if existing.JobID != "" || existing.IntentID != "" || existing.ExpiresAt != "" {
			return "", ErrOperationCorrupted
		}
		return "prepare", nil
	case "ready":
		created, createdErr := time.Parse(time.RFC3339Nano, existing.CreatedAt)
		expires, expiresErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if createdErr != nil || expiresErr != nil || !expires.After(created) || expires.Sub(created) > 10*time.Minute || existing.JobID != "" || existing.IntentID != "" {
			return "", ErrOperationCorrupted
		}
		if expires.After(now) {
			return "", errors.New("unused maintenance probe capability remains live; refusing to replace or replay it")
		}
		return "prepare", nil
	case "reserved":
		return "", errors.New("maintenance probe scope is reserved without a safely observable accepted result; refusing replay")
	case "accepted":
		if !validProbeJobIdentity(existing.JobID) || (existing.IntentID != "" && transport != "mac-local") {
			return "", ErrOperationCorrupted
		}
		created, createdErr := time.Parse(time.RFC3339Nano, existing.CreatedAt)
		expires, expiresErr := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if createdErr != nil || expiresErr != nil || !expires.After(created) || expires.Sub(created) > 10*time.Minute {
			return "", ErrOperationCorrupted
		}
		return "observe", nil
	default:
		return "", ErrOperationCorrupted
	}
}

func validatePreparedPromotionProbe(prepared promotionPreparedProbe, host, lane, transport, operationID, controllerID string) error {
	_, expiryErr := time.Parse(time.RFC3339Nano, prepared.ExpiresAt)
	if prepared.HostID != host || prepared.Lane != lane || prepared.Transport != transport ||
		prepared.OperationID != operationID || prepared.ControllerType != expectedProbeControllerType(transport) ||
		prepared.ControllerID != controllerID || !digestPattern.MatchString(prepared.ConfigurationSHA256) || prepared.Token == "" || expiryErr != nil ||
		prepared.State != "ready" || prepared.JobID != "" || prepared.IntentID != "" {
		return ErrOperationCorrupted
	}
	return nil
}

func expectedProbeControllerType(transport string) string {
	switch transport {
	case "direct-mtls":
		return "direct_mtls"
	case "queued-ssh":
		return "queued_mac"
	case "mac-local":
		return "local_user"
	default:
		return ""
	}
}

func validatePromotionProbeRequest(prepared promotionPreparedProbe) error {
	var request struct {
		Environment string `json:"environment"`
		Target      struct {
			Kind    string `json:"kind"`
			Profile string `json:"profile"`
		} `json:"execution_target"`
		Source struct {
			Mode string `json:"mode"`
		} `json:"source"`
		Script         string         `json:"script"`
		TimeoutSeconds int            `json:"timeout_seconds"`
		Limits         map[string]int `json:"limits"`
	}
	if len(prepared.RequestBody) == 0 || json.Unmarshal(prepared.RequestBody, &request) != nil ||
		request.Environment != prepared.Environment || request.Source.Mode != "empty" ||
		request.Script != "printf 'catalog release maintenance probe\\n'" || !digestPattern.MatchString(prepared.RequestSHA256) {
		return ErrOperationCorrupted
	}
	var compact bytes.Buffer
	if json.Compact(&compact, prepared.RequestBody) != nil || digest(compact.Bytes()) != prepared.RequestSHA256 {
		return ErrOperationCorrupted
	}
	if prepared.Transport == "mac-local" {
		if prepared.HostID != "mac-control-plane" || request.Target.Kind != "local" || request.Target.Profile != "mac-workstation" {
			return ErrOperationCorrupted
		}
	} else if prepared.Transport == "direct-mtls" || prepared.Transport == "queued-ssh" {
		if prepared.HostID == "mac-control-plane" || request.Target.Kind != "remote" || request.Target.Profile != prepared.HostID {
			return ErrOperationCorrupted
		}
	} else {
		return ErrOperationCorrupted
	}
	if prepared.Transport == "queued-ssh" {
		if request.TimeoutSeconds != 0 || len(request.Limits) != 1 || request.Limits["command_timeout_seconds"] != 5 {
			return ErrOperationCorrupted
		}
		return nil
	}
	if request.TimeoutSeconds != 5 || len(request.Limits) != 0 {
		return ErrOperationCorrupted
	}
	return nil
}

func (b *NativePromotionBackend) ReleaseCohort(ctx context.Context, op Operation) (any, error) {
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return nil, errors.Join(ErrOperationCorrupted, err)
	}
	if op.Stage == StageRollingBack {
		if err := b.stopAndFenceForRollback(ctx, op); err != nil {
			return HostObservation{}, err
		}
		if view.Receipt.Stage == laneinstall.CatalogCohortRollingBack {
			evidence := digest([]byte(op.OperationID + "|rollback-complete"))
			if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
				"--action", "rollback-complete", "--operation-id", op.CohortOperationID, "--evidence-sha256", evidence}, nil); err != nil {
				return nil, err
			}
			if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
				return nil, err
			}
		}
		return b.openAdmissions(ctx, op)
	}
	if view.Receipt.Stage == laneinstall.CatalogCohortHeld || view.Receipt.Stage == laneinstall.CatalogCohortSwitching {
		if err := b.startHostUnderHold(ctx, op, "mac-control-plane"); err != nil {
			return nil, err
		}
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
			"--action", "verify-start", "--operation-id", op.CohortOperationID, "--evidence-sha256", operationAcceptanceDigest(op)}, nil); err != nil {
			return nil, err
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return nil, err
		}
		for _, host := range HostOrder {
			view, err = b.readReceipt(ctx, host, op.Lane, op.CohortOperationID)
			if err != nil || !view.Present {
				return nil, errors.Join(ErrOperationCorrupted, err)
			}
			if receiptHasVerify(view.Receipt, host) {
				continue
			}
			if err := b.startHostUnderHold(ctx, op, host); err != nil {
				return nil, err
			}
			if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", op.Lane,
				"--action", "verify", "--operation-id", op.CohortOperationID, "--subject-host", host,
				"--evidence-sha256", operationAcceptanceDigest(op)}, nil); err != nil {
				return nil, err
			}
			if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
				return nil, err
			}
		}
		view, err = b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
		if err != nil || !view.Present {
			return nil, errors.Join(ErrOperationCorrupted, err)
		}
	}
	if view.Receipt.Stage == laneinstall.CatalogCohortVerifying {
		if err := b.startHostUnderHold(ctx, op, "mac-control-plane"); err != nil {
			return nil, err
		}
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
			"--action", "complete", "--operation-id", op.CohortOperationID, "--evidence-sha256", operationAcceptanceDigest(op)}, nil); err != nil {
			return nil, err
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return nil, err
		}
	}
	return b.openAdmissions(ctx, op)
}

func (b *NativePromotionBackend) openAdmissions(ctx context.Context, op Operation) (any, error) {
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return nil, errors.Join(ErrOperationCorrupted, err)
	}
	if view.Receipt.Stage != laneinstall.CatalogCohortReleased {
		if view.Receipt.Stage != laneinstall.CatalogCohortComplete && view.Receipt.Stage != laneinstall.CatalogCohortRolledBack {
			return nil, errors.New("cohort is not complete on all hosts; admissions remain held")
		}
		if err := b.startHostUnderHold(ctx, op, "mac-control-plane"); err != nil {
			return nil, err
		}
		evidence := digest([]byte(op.OperationID + "|open-admissions|" + op.Release.Digest))
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
			"--action", "open", "--operation-id", op.CohortOperationID, "--evidence-sha256", evidence}, nil); err != nil {
			return nil, err
		}
	}
	if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
		return nil, err
	}
	if err := b.startReleasedCohort(ctx, op); err != nil {
		return nil, err
	}
	return map[string]any{"cohort_operation_id": op.CohortOperationID, "receipt_stage": "released", "synchronized_hosts": HostOrder}, nil
}

func (b *NativePromotionBackend) RollbackHost(ctx context.Context, op Operation, host string) (HostObservation, error) {
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return HostObservation{}, errors.Join(ErrOperationCorrupted, err)
	}
	if view.Receipt.Stage == laneinstall.CatalogCohortComplete || view.Receipt.Stage == laneinstall.CatalogCohortReleased ||
		view.Receipt.Stage == laneinstall.CatalogCohortBlocked {
		if err := b.stopAndFenceForRollback(ctx, op); err != nil {
			return HostObservation{}, err
		}
		view, err = b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
		if err != nil || !view.Present {
			return HostObservation{}, errors.Join(ErrOperationCorrupted, err)
		}
	}
	if view.Receipt.Stage != laneinstall.CatalogCohortRollingBack {
		if view.Receipt.Stage != laneinstall.CatalogCohortBlocked {
			evidence := digest([]byte(op.OperationID + "|rollback-blocked"))
			if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
				"--action", "block", "--operation-id", op.CohortOperationID, "--evidence-sha256", evidence}, nil); err != nil {
				return HostObservation{}, err
			}
			if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
				return HostObservation{}, err
			}
		}
		evidence := digest([]byte(op.OperationID + "|rollback-start"))
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
			"--action", "rollback-start", "--operation-id", op.CohortOperationID, "--evidence-sha256", evidence}, nil); err != nil {
			return HostObservation{}, err
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return HostObservation{}, err
		}
	}
	hostIndexValue := hostIndex(host)
	if hostIndexValue < 0 || op.Hosts[hostIndexValue].SelectorOperation == "" {
		return HostObservation{}, ErrOperationCorrupted
	}
	if _, err := b.runLane(ctx, host, []string{"catalog-rollback", "--host", host, "--lane", op.Lane,
		"--cohort-operation-id", op.CohortOperationID, "--selector-operation-id", op.Hosts[hostIndexValue].SelectorOperation}, nil); err != nil {
		return HostObservation{}, err
	}
	if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
		return HostObservation{}, err
	}
	selector, _, err := b.currentHost(ctx, host, op.Lane)
	if err != nil {
		return HostObservation{}, err
	}
	return HostObservation{HostID: host, Lane: op.Lane, SelectedReleaseID: op.Hosts[hostIndexValue].PreviousReleaseID,
		SelectedBundleSHA: selectedBundleSHA(selector), SelectorOperation: op.Hosts[hostIndexValue].SelectorOperation, JournalVerified: true}, nil
}

// stopAndFenceForRollback first stops the selected lane on the full cohort,
// then records a fresh host proof and synchronizes each proof before the next
// host. A released cohort has open admissions, so stopping all four service
// groups precedes the first receipt transition that restores the hold.
func (b *NativePromotionBackend) stopAndFenceForRollback(ctx context.Context, op Operation) error {
	view, err := b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return errors.Join(ErrOperationCorrupted, err)
	}
	if view.Receipt.Stage == laneinstall.CatalogCohortRollingBack || view.Receipt.Stage == laneinstall.CatalogCohortRolledBack {
		return nil
	}
	if view.Receipt.Stage != laneinstall.CatalogCohortComplete && view.Receipt.Stage != laneinstall.CatalogCohortReleased && view.Receipt.Stage != laneinstall.CatalogCohortBlocked {
		return errors.New("rollback requires a complete, released, or blocked cohort receipt")
	}
	if !b.PublishersPaused || !b.NoNewAccepted || strings.TrimSpace(b.PublisherOwner) == "" || strings.TrimSpace(b.PublisherControl) == "" {
		return errors.New("rollback requires explicit publisher pause ownership and no-new-accepted confirmation")
	}
	for _, host := range HostOrder {
		if _, err := b.runLane(ctx, host, serviceAction(op.Lane, "stop", host), nil); err != nil {
			return fmt.Errorf("stop %s before rollback fencing: %w", host, err)
		}
	}
	for _, host := range HostOrder {
		_, snapshot, err := b.currentHost(ctx, host, op.Lane)
		if err != nil {
			return err
		}
		if snapshot.Active || !snapshot.Empty {
			return fmt.Errorf("%s remains active or has work after selected-lane stop: %w", host, laneinstall.ErrSimpleLaneNotQuiescent)
		}
	}
	if view.Receipt.Stage == laneinstall.CatalogCohortComplete || view.Receipt.Stage == laneinstall.CatalogCohortReleased {
		fenced := make(map[string]bool)
		for _, transition := range view.Receipt.Transitions {
			if transition.Action == "rollback-fence-host" {
				fenced[transition.HostID] = true
			}
		}
		for _, host := range HostOrder {
			if fenced[host] {
				continue
			}
			if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", op.Lane,
				"--action", "rollback-fence-host", "--operation-id", op.CohortOperationID, "--subject-host", host}, nil); err != nil {
				return fmt.Errorf("record %s rollback fence: %w", host, err)
			}
			if err := b.syncReceiptFrom(ctx, host, op.Lane, op.CohortOperationID); err != nil {
				return err
			}
		}
	}
	view, err = b.readReceipt(ctx, "mac-control-plane", op.Lane, op.CohortOperationID)
	if err != nil || !view.Present {
		return errors.Join(ErrOperationCorrupted, err)
	}
	if view.Receipt.Stage != laneinstall.CatalogCohortRollingBack {
		evidence := digest([]byte(op.OperationID + "|rollback-start"))
		if _, err := b.runLane(ctx, "mac-control-plane", []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", op.Lane,
			"--action", "rollback-start", "--operation-id", op.CohortOperationID, "--evidence-sha256", evidence}, nil); err != nil {
			return err
		}
		if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
			return err
		}
	}
	return nil
}

func (b *NativePromotionBackend) UpdateSourceBranch(ctx context.Context, lane, commit, expectedOld string) error {
	if (lane != "qas" && lane != "prd") || !shaPattern.MatchString(commit) {
		return ErrInvalidInput
	}
	ref := "refs/heads/" + lane
	old, exists, err := b.remoteRef(ctx, "origin", b.GitSSHCommand, ref)
	if err != nil {
		return errors.Join(ErrBranchConflict, err)
	}
	// A previous push may have advanced the primary successfully before its
	// automatic mirror caught up. Treat that exact desired ref as an idempotent
	// retry and verify only mirror delivery; any other unexpected ref remains a
	// conflict. The server still enforces fast-forward on the first push.
	alreadyAdvanced := exists && old == commit
	if !alreadyAdvanced && (expectedOld == "" && exists || expectedOld != "" && (!exists || old != expectedOld)) {
		return errors.Join(ErrBranchConflict, err)
	}
	if !alreadyAdvanced {
		push := exec.CommandContext(ctx, "git", "-C", b.Repository, "push", "origin", commit+":"+ref)
		push.Env = append(os.Environ(), "GIT_SSH_COMMAND="+b.GitSSHCommand)
		if output, err := push.CombinedOutput(); err != nil {
			return fmt.Errorf("primary %s fast-forward marker update failed: %w: %s", lane, err, strings.TrimSpace(string(output)))
		}
	}
	primary, ok, err := b.remoteRef(ctx, "origin", b.GitSSHCommand, ref)
	if err != nil || !ok || primary != commit {
		return errors.Join(ErrBranchConflict, err)
	}
	mirror, ok, err := b.remoteRef(ctx, "github", b.MirrorSSHCommand, ref)
	if err != nil || !ok || mirror != commit {
		return errors.Join(errors.New("primary accepted runtime marker but GitHub mirror synchronization is pending"), err)
	}
	return nil
}

func (b *NativePromotionBackend) runLane(ctx context.Context, host string, args []string, stdin []byte) ([]byte, error) {
	if b.laneCommandRunner != nil {
		return b.laneCommandRunner(ctx, host, append([]string(nil), args...), append([]byte(nil), stdin...))
	}
	profile, ok := b.Config.Hosts[host]
	if !ok || !contains(HostOrder, host) {
		return nil, ErrInvalidInput
	}
	req := HostLaneRPCRequest{HostID: host, Repository: profile.Repository, GoBinary: profile.GoBinary,
		ControllerRevision: b.ControllerRevision, Arguments: args, Stdin: stdin}
	if host == "mac-control-plane" {
		return RunHostLaneRPC(ctx, req)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	sshArgs := []string{"-i", profile.IdentityFile, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + profile.KnownHostsFile, "-o", "PreferredAuthentications=publickey", profile.Target}
	command := "cd -- " + shellQuote(profile.Repository) + " && GOTOOLCHAIN=local " + shellQuote(profile.GoBinary) + " run ./src/cmd/runner-release host-lane-rpc"
	cmd := exec.CommandContext(ctx, "ssh", append(sshArgs, command)...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s guarded runner-lane call %s failed: %w: %s", host, args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (b *NativePromotionBackend) readLaneSnapshot(ctx context.Context, host, lane string) (promotionLaneSnapshot, error) {
	var snapshot promotionLaneSnapshot
	if !contains(HostOrder, host) || !contains(LaneOrder, lane) {
		return snapshot, ErrInvalidInput
	}
	statusBytes, err := b.runLane(ctx, host, []string{"catalog-status", "--host", host, "--lane", lane}, nil)
	if err != nil || decodeStrictJSON(statusBytes, &snapshot.Selector) != nil {
		return snapshot, errors.Join(ErrOperationCorrupted, err)
	}
	out, err := b.runLane(ctx, host, promotionWorkInventoryArgs(host, lane), nil)
	if err != nil {
		return snapshot, err
	}
	var inventory laneinstall.CatalogWorkInventoryReport
	if decodeStrictJSON(out, &inventory) != nil || inventory.HostID != host || string(inventory.Lane) != lane {
		return snapshot, ErrOperationCorrupted
	}
	snapshot.Active, snapshot.Config, snapshot.Schema, snapshot.Work, snapshot.Empty = inventory.ServiceActive, inventory.ConfigurationSHA256,
		inventory.DatabaseSchema, inventory.WorkInventory, inventory.WorkInventoryEmpty
	if err := b.readPromotionCapacity(ctx, host, lane, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func promotionWorkInventoryArgs(host, lane string) []string {
	return []string{"catalog-work-inventory", "--host", host, "--lane", lane}
}

func (b *NativePromotionBackend) currentHost(ctx context.Context, host, lane string) (laneinstall.CatalogSelectorStatus, promotionLaneSnapshot, error) {
	snapshot, err := b.readLaneSnapshot(ctx, host, lane)
	return snapshot.Selector, snapshot, err
}

func (b *NativePromotionBackend) readAdmission(ctx context.Context, host, lane string) (promotionAdmissionView, error) {
	out, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", lane, "--action", "discover"}, nil)
	if err != nil {
		return promotionAdmissionView{}, err
	}
	return decodePromotionAdmission(out, host, lane)
}

func decodePromotionAdmission(data []byte, host, lane string) (promotionAdmissionView, error) {
	var view promotionAdmissionView
	if decodeStrictJSON(data, &view) != nil || view.HostID != host || string(view.Lane) != lane {
		return promotionAdmissionView{}, ErrOperationCorrupted
	}
	if view.AdmissionHeld {
		if !operationPattern.MatchString(view.OperationID) || view.Receipt == nil ||
			view.Receipt.OperationID != view.OperationID || string(view.Receipt.Lane) != lane {
			return promotionAdmissionView{}, ErrOperationCorrupted
		}
	} else if view.OperationID != "" || view.Receipt != nil {
		return promotionAdmissionView{}, ErrOperationCorrupted
	}
	return view, nil
}

func (b *NativePromotionBackend) readReceipt(ctx context.Context, host, lane, id string) (promotionReceiptView, error) {
	out, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", lane, "--action", "inspect", "--operation-id", id}, nil)
	if err != nil {
		return promotionReceiptView{}, err
	}
	var view promotionReceiptView
	if decodeStrictJSON(out, &view) != nil {
		return view, ErrOperationCorrupted
	}
	if view.Present && (view.Receipt.OperationID != id || string(view.Receipt.Lane) != lane) {
		return view, ErrOperationCorrupted
	}
	return view, nil
}

func (b *NativePromotionBackend) syncReceiptFrom(ctx context.Context, source, lane, id string) error {
	view, err := b.readReceipt(ctx, source, lane, id)
	if err != nil || !view.Present {
		return errors.Join(ErrOperationCorrupted, err)
	}
	needsReconciliation := false
	histories := make([]laneinstall.CatalogCohortReceipt, 0, len(HostOrder))
	for _, host := range HostOrder {
		copyView, err := b.readReceipt(ctx, host, lane, id)
		if err != nil {
			return fmt.Errorf("inspect %s receipt before synchronization: %w", host, err)
		}
		if !copyView.Present {
			continue
		}
		histories = append(histories, copyView.Receipt)
		if !laneinstall.CatalogCohortReceiptCanAdvance(copyView.Receipt, view.Receipt) {
			needsReconciliation = true
		}
	}
	if err := b.stopCohortForReceiptSync(ctx, lane, view.Receipt); err != nil {
		return err
	}
	if needsReconciliation {
		if len(histories) < 2 {
			return ErrOperationCorrupted
		}
		files := make([]string, 0, len(histories))
		for _, history := range histories {
			data, err := json.MarshalIndent(history, "", "  ")
			if err != nil {
				return err
			}
			path, remove, err := writePrivateTemp(append(data, '\n'))
			if err != nil {
				for _, oldPath := range files {
					_ = os.Remove(oldPath)
				}
				return err
			}
			files = append(files, path)
			_ = remove // files are removed together after the public reconciliation call
		}
		defer func() {
			for _, path := range files {
				_ = os.Remove(path)
			}
		}()
		args := []string{"catalog-cohort", "--host", "mac-control-plane", "--lane", lane, "--action", "reconcile-history", "--operation-id", id}
		for _, path := range files {
			args = append(args, "--history-file", path)
		}
		if _, err := b.runLane(ctx, "mac-control-plane", args, nil); err != nil {
			return fmt.Errorf("supported receipt-history reconciliation: %w", err)
		}
		view, err = b.readReceipt(ctx, "mac-control-plane", lane, id)
		if err != nil || !view.Present {
			return errors.Join(ErrOperationCorrupted, err)
		}
		for _, host := range HostOrder {
			copyView, inspectErr := b.readReceipt(ctx, host, lane, id)
			if inspectErr != nil || copyView.Present && !laneinstall.CatalogCohortReceiptCanAdvance(copyView.Receipt, view.Receipt) {
				return errors.Join(fmt.Errorf("reconciled receipt does not safely extend %s history", host), inspectErr)
			}
		}
	}
	data, err := json.MarshalIndent(view.Receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for _, host := range HostOrder {
		if _, err := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", lane, "--action", "sync", "--receipt-file", "@promo-stdin@"}, data); err != nil {
			return fmt.Errorf("synchronize %s receipt to %s: %w", id, host, err)
		}
	}
	for _, host := range HostOrder {
		copyView, err := b.readReceipt(ctx, host, lane, id)
		if err != nil || !copyView.Present || !reflectReceiptEqual(copyView.Receipt, view.Receipt) {
			return errors.Join(fmt.Errorf("receipt synchronization verification failed on %s", host), err)
		}
	}
	return nil
}

// stopCohortForReceiptSync preserves the host-native stopped/empty guard used
// by catalog-cohort sync. A selector or verification transition can leave a
// selected-lane service running; stop only that lane, after every host reports
// an empty inventory and the exact operation remains fenced. Peer lanes are
// never addressed here.
func (b *NativePromotionBackend) stopCohortForReceiptSync(ctx context.Context, lane string,
	receipt laneinstall.CatalogCohortReceipt) error {
	if receipt.Lane != config.ReleaseLane(lane) || receipt.OperationID == "" {
		return ErrOperationCorrupted
	}
	type hostView struct {
		report laneinstall.CatalogWorkInventoryReport
	}
	views := make(map[string]hostView, len(HostOrder))
	for index, host := range HostOrder {
		out, err := b.runLane(ctx, host, promotionWorkInventoryArgs(host, lane), nil)
		if err != nil {
			return fmt.Errorf("read %s before receipt synchronization: %w", host, err)
		}
		var report laneinstall.CatalogWorkInventoryReport
		if decodeStrictJSON(out, &report) != nil || report.HostID != host || string(report.Lane) != lane ||
			!report.WorkInventoryEmpty || !report.WorkInventory.Empty() {
			return fmt.Errorf("%s selected lane is not empty before receipt synchronization: %w", host, laneinstall.ErrSimpleLaneNotQuiescent)
		}
		preflight := receipt.Hosts[index]
		if preflight.HostID != host || report.ConfigurationSHA256 != preflight.ConfigurationSHA256 ||
			report.DatabaseSchema != preflight.SchemaVersion || report.WorkInventory.SHA256() != preflight.WorkInventorySHA256 {
			return fmt.Errorf("%s configuration, schema, or work identity changed before receipt synchronization: %w", host, ErrOperationCorrupted)
		}
		if report.ServiceActive {
			admission, err := b.readAdmission(ctx, host, lane)
			if err != nil {
				return fmt.Errorf("inspect %s admission before receipt synchronization: %w", host, err)
			}
			localReceipt, err := b.readReceipt(ctx, host, lane, receipt.OperationID)
			if err != nil {
				return fmt.Errorf("inspect %s operation receipt before synchronization: %w", host, err)
			}
			if admission.AdmissionHeld {
				if admission.OperationID != receipt.OperationID {
					return fmt.Errorf("%s active service is held by another operation: %w", host, ErrAdmissionHeld)
				}
			} else if receipt.Stage != laneinstall.CatalogCohortReleased || !localReceipt.Present ||
				localReceipt.Receipt.Stage != laneinstall.CatalogCohortReleased {
				return fmt.Errorf("%s active service has no matching selected-lane admission hold: %w", host, ErrAdmissionHeld)
			}
		}
		views[host] = hostView{report: report}
	}
	for _, host := range HostOrder {
		before := views[host].report
		if !before.ServiceActive {
			continue
		}
		if _, err := b.runLane(ctx, host, serviceAction(lane, "stop", host), nil); err != nil {
			return fmt.Errorf("stop %s before receipt synchronization: %w", host, err)
		}
	}
	for _, host := range HostOrder {
		before := views[host].report
		if !before.ServiceActive {
			continue
		}
		out, err := b.runLane(ctx, host, promotionWorkInventoryArgs(host, lane), nil)
		if err != nil {
			return fmt.Errorf("verify %s stopped before receipt synchronization: %w", host, err)
		}
		var after laneinstall.CatalogWorkInventoryReport
		if decodeStrictJSON(out, &after) != nil || after.HostID != host || string(after.Lane) != lane ||
			after.ServiceActive || !after.WorkInventoryEmpty || !after.WorkInventory.Empty() ||
			after.SelectorTarget != before.SelectorTarget || after.BundleSHA256 != before.BundleSHA256 ||
			after.ConfigurationSHA256 != before.ConfigurationSHA256 || after.DatabaseSchema != before.DatabaseSchema {
			return fmt.Errorf("%s selected lane did not remain empty and identity-stable after stop: %w", host, laneinstall.ErrSimpleLaneNotQuiescent)
		}
	}
	return nil
}

func (b *NativePromotionBackend) inspectPublished(ctx context.Context, host string, release Release) error {
	out, err := b.runLane(ctx, host, []string{"catalog-inspect", "--host", host, "--release-id", release.ID}, nil)
	if err != nil {
		return err
	}
	var status laneinstall.CatalogReleaseStatus
	if decodeStrictJSON(out, &status) != nil || status.Identity.ReleaseID != release.ID || status.Identity.ReleaseSHA256 != release.Digest || status.Identity.BundleSHA256 != release.Bundles[host].SHA256 {
		return ErrReleaseMismatch
	}
	return nil
}

func (b *NativePromotionBackend) findSelectorOperations(ctx context.Context, host, lane, cohortID string) ([]laneinstall.CatalogSelectorOperation, error) {
	out, err := b.runLane(ctx, host, []string{"catalog-selector-find", "--host", host, "--lane", lane, "--cohort-operation-id", cohortID}, nil)
	if err != nil {
		return nil, err
	}
	var operations []laneinstall.CatalogSelectorOperation
	if decodeStrictJSON(out, &operations) != nil {
		return nil, ErrOperationCorrupted
	}
	return operations, nil
}

func (b *NativePromotionBackend) readPeerEvidence(ctx context.Context, lane string) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		result[host] = make(map[string]string, len(LaneOrder)-1)
		for _, peer := range LaneOrder {
			if peer == lane {
				continue
			}
			snapshot, err := b.readLaneSnapshot(ctx, host, peer)
			if err != nil {
				return nil, err
			}
			result[host][peer] = promotionPeerSnapshotDigest(snapshot)
		}
	}
	return result, nil
}

func (b *NativePromotionBackend) readBranchMarker(ctx context.Context, lane string) (string, bool, error) {
	ref := "refs/heads/" + lane
	primary, primaryExists, err := b.remoteRef(ctx, "origin", b.GitSSHCommand, ref)
	if err != nil {
		return "", false, err
	}
	mirror, mirrorExists, err := b.remoteRef(ctx, "github", b.MirrorSSHCommand, ref)
	if err != nil {
		return "", false, err
	}
	if primaryExists != mirrorExists || primaryExists && primary != mirror {
		return "", false, fmt.Errorf("primary and mirror %s branch refs differ: %w", lane, ErrBranchConflict)
	}
	return primary, primaryExists, nil
}

func (b *NativePromotionBackend) remoteRef(ctx context.Context, remote, sshCommand, ref string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", b.Repository, "ls-remote", remote, ref)
	cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCommand)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("git ls-remote %s %s: %w: %s", remote, ref, err, strings.TrimSpace(string(out)))
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", false, nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[1] != ref || !shaPattern.MatchString(fields[0]) {
		return "", false, ErrOperationCorrupted
	}
	return fields[0], true, nil
}

func (b *NativePromotionBackend) isAncestor(ctx context.Context, ancestor, descendant string) bool {
	if !shaPattern.MatchString(ancestor) || !shaPattern.MatchString(descendant) {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-C", b.Repository, "merge-base", "--is-ancestor", ancestor, descendant)
	return cmd.Run() == nil
}

func serviceAction(lane, action, host string) []string {
	if lane == "prd" {
		return []string{"prd-" + action, "--host", host}
	}
	return []string{action, "--host", host, "--lane", lane}
}

func selectedReleaseID(status laneinstall.CatalogSelectorStatus) string {
	if status.Release != nil {
		return status.Release.Identity.ReleaseID
	}
	if status.Kind == "legacy" {
		return "legacy"
	}
	return ""
}

func selectedBundleSHA(status laneinstall.CatalogSelectorStatus) string {
	if status.Release != nil {
		return status.Release.Identity.BundleSHA256
	}
	return status.Bundle.BundleSHA256
}

func validPreflightBackupReference(preflight laneinstall.CatalogCohortHostPreflight) bool {
	return preflight.BackupDirectory != "" && digestPattern.MatchString(preflight.BackupManifestSHA256)
}

func receiptHasSwitch(receipt laneinstall.CatalogCohortReceipt, host string) bool {
	for _, transition := range receipt.Transitions {
		if (transition.Action == "switch" || transition.Action == "switch-reconciled") && transition.HostID == host {
			return true
		}
	}
	return false
}

func receiptHasVerify(receipt laneinstall.CatalogCohortReceipt, host string) bool {
	for _, transition := range receipt.Transitions {
		if transition.Action == "verify" && transition.HostID == host {
			return true
		}
	}
	return false
}

func lastTransitionEvidence(receipt laneinstall.CatalogCohortReceipt, action, host string) string {
	for i := len(receipt.Transitions) - 1; i >= 0; i-- {
		transition := receipt.Transitions[i]
		if transition.Action == action && transition.HostID == host {
			return transition.EvidenceSHA256
		}
	}
	return ""
}

func operationAcceptanceDigest(op Operation) string {
	for i := len(op.Events) - 1; i >= 0; i-- {
		if op.Events[i].Action == "cohort-accepted" && len(op.Events[i].Evidence) > 0 {
			return hashJSON(json.RawMessage(op.Events[i].Evidence))
		}
	}
	return digest([]byte(op.OperationID + "|" + op.Release.Digest + "|" + op.Profile))
}

func equalNestedStringMaps(left, right map[string]map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for host, row := range right {
		other, ok := left[host]
		if !ok || len(other) != len(row) {
			return false
		}
		for lane, value := range row {
			if other[lane] != value {
				return false
			}
		}
	}
	return true
}

func reflectReceiptEqual(left, right laneinstall.CatalogCohortReceipt) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func decodeStrictJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > 8<<20 {
		return ErrOperationCorrupted
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrOperationCorrupted
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrOperationCorrupted
	}
	return nil
}

func writePrivateAtomic(path string, data []byte) error {
	if !absoluteClean(path) || len(data) == 0 || len(data) > 8<<20 {
		return ErrInvalidInput
	}
	if err := ensurePrivatePath(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".preflight-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readPrivateBytes(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > max {
		return nil, ErrOperationCorrupted
	}
	return os.ReadFile(path)
}

func laneDiskFree(path string) (uint64, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return uint64(stats.Bavail) * uint64(stats.Bsize), nil
}

var _ Backend = (*NativePromotionBackend)(nil)
