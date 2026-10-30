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
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/store"
)

const (
	defaultPromotionOperatingBuffer = uint64(500_000_000)
	prdSelectorReserve              = uint64(67_108_864)
	maxPromotionProbeOutputBytes    = 8 << 20
	maxPromotionProbeStderrBytes    = 4 << 10
	peerLaneStableIdentityV1        = "stable-identity-v1"
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

// Blank profiles retain the exact digest semantics of preserved operations.
// New operations distinguish peer installation identity from ordinary work;
// selected-lane work and capacity guards remain independent and unchanged.
func promotionPeerEvidenceProfile(plan *PromotionPlan) (string, error) {
	if plan == nil || plan.PeerLaneEvidenceProfile == "" {
		return "", nil
	}
	if plan.PeerLaneEvidenceProfile != peerLaneStableIdentityV1 {
		return "", fmt.Errorf("unknown peer-lane evidence profile: %w", ErrOperationCorrupted)
	}
	return plan.PeerLaneEvidenceProfile, nil
}

func promotionPeerSnapshotDigestForProfile(snapshot promotionLaneSnapshot, profile string) (string, error) {
	if _, err := promotionPeerEvidenceProfile(&PromotionPlan{PeerLaneEvidenceProfile: profile}); err != nil {
		return "", err
	}
	if profile == peerLaneStableIdentityV1 {
		snapshot.Work = laneinstall.CatalogWorkInventory{}
		snapshot.Empty = false
	}
	return promotionPeerSnapshotDigest(snapshot), nil
}

func promotionPeerAbsenceDigest(report laneinstall.CatalogNamespaceAbsenceReport) string {
	return digest([]byte("promotion-peer-namespace-absence-v1\x00" + report.EvidenceSHA256))
}

func promotionPRDAbsencePredecessorSHA(report laneinstall.CatalogNamespaceAbsenceReport) string {
	if report.HostID != "linux-host" || report.Lane != config.ReleaseLanePRD || report.SelectedLane != config.ReleaseLanePRD ||
		laneinstall.ValidateCatalogNamespaceAbsenceReport(report, "linux-host", config.ReleaseLanePRD, config.ReleaseLanePRD) != nil {
		return ""
	}
	return digest([]byte("runner-release-prd-absent-predecessor-v1\x00" + report.EvidenceSHA256))
}

func promotionPRDFreshInstallPredecessorSHA(baseline laneinstall.CatalogFreshInstallBaseline) string {
	if baseline.OperationID == "" || baseline.Absence.HostID != "linux-host" || baseline.Absence.Lane != config.ReleaseLanePRD ||
		baseline.Absence.SelectedLane != config.ReleaseLanePRD || !digestPattern.MatchString(baseline.EvidenceSHA256) ||
		!digestPattern.MatchString(baseline.Absence.EvidenceSHA256) {
		return ""
	}
	return digest([]byte("runner-release-prd-fresh-install-predecessor-v1\x00" + baseline.EvidenceSHA256))
}

func validatePromotionPeerAbsenceEvidence(plan *PromotionPlan) error {
	if plan == nil || len(plan.PeerLaneAbsenceEvidence) == 0 {
		return nil
	}
	if plan.Lane == "prd" || plan.PeerLaneEvidenceProfile != peerLaneStableIdentityV1 {
		return ErrOperationCorrupted
	}
	for host, lanes := range plan.PeerLaneAbsenceEvidence {
		if host != "linux-host" || len(lanes) == 0 || len(lanes) > len(LaneOrder)-1 {
			return ErrOperationCorrupted
		}
		for peer, report := range lanes {
			peerLane, selectedLane := config.ReleaseLane(peer), config.ReleaseLane(plan.Lane)
			if !laneinstall.CatalogNamespaceAbsenceScopeAllowed(host, peerLane, selectedLane) ||
				laneinstall.ValidateCatalogNamespaceAbsenceReport(report, host, peerLane, selectedLane) != nil ||
				plan.PeerLaneEvidence[host][peer] != promotionPeerAbsenceDigest(report) {
				return ErrOperationCorrupted
			}
		}
	}
	return nil
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
	HostID              string                                  `json:"host_id"`
	Lane                string                                  `json:"lane"`
	Transport           laneinstall.CatalogProbeTransport       `json:"transport"`
	OperationID         string                                  `json:"operation_id"`
	ControllerType      string                                  `json:"controller_type"`
	ControllerID        string                                  `json:"controller_id"`
	Environment         string                                  `json:"environment"`
	ConfigurationSHA256 string                                  `json:"configuration_sha256"`
	IdempotencyKey      string                                  `json:"idempotency_key"`
	RequestSHA256       string                                  `json:"request_body_sha256"`
	CreatedAt           string                                  `json:"created_at"`
	ExpiresAt           string                                  `json:"expires_at"`
	State               string                                  `json:"state"`
	JobID               string                                  `json:"job_id,omitempty"`
	IntentID            string                                  `json:"intent_id,omitempty"`
	TerminalResult      *laneinstall.CatalogProbeTerminalResult `json:"terminal_result,omitempty"`
}

type promotionProbeAcceptanceEvidence struct {
	Method                              string                                  `json:"method"`
	HelperExitCode                      int                                     `json:"helper_exit_code"`
	HelperContract                      string                                  `json:"helper_contract"`
	ControllerFailureEventSequence      int                                     `json:"controller_failure_event_sequence"`
	ControllerFailureEventSHA256        string                                  `json:"controller_failure_event_sha256"`
	ControllerFailureEventAtUTC         string                                  `json:"controller_failure_event_at_utc"`
	ScopeCreatedAt                      string                                  `json:"scope_created_at"`
	ScopeExpiresAt                      string                                  `json:"scope_expires_at"`
	HelperVerifiedPositiveFinalSequence bool                                    `json:"helper_verified_positive_final_sequence"`
	HostReadOnlyResult                  *laneinstall.CatalogProbeTerminalResult `json:"host_read_only_result,omitempty"`
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
	IdempotencyKey      string                            `json:"idempotency_key,omitempty"`
	RequestedTimeout    int                               `json:"requested_timeout_seconds"`
	AcceptanceEvidence  *promotionProbeAcceptanceEvidence `json:"acceptance_evidence,omitempty"`
}

type promotionLaneSnapshot struct {
	Selector                laneinstall.CatalogSelectorStatus              `json:"selector"`
	NamespaceAbsence        *laneinstall.CatalogNamespaceAbsenceReport     `json:"namespace_absence,omitempty"`
	FreshInstall            *laneinstall.CatalogFreshInstallBaseline       `json:"fresh_install_baseline,omitempty"`
	FreshInstallObservation *laneinstall.CatalogPRDFreshInstallObservation `json:"fresh_install_observation,omitempty"`
	Active                  bool                                           `json:"service_active"`
	Config                  string                                         `json:"configuration_sha256"`
	Schema                  int                                            `json:"schema_version"`
	Work                    laneinstall.CatalogWorkInventory               `json:"work_inventory"`
	Empty                   bool                                           `json:"work_inventory_empty"`
	AvailableBytes          uint64                                         `json:"available_bytes"`
	RequiredBytes           uint64                                         `json:"required_bytes"`
}

type prdBackupRecoveryHostEvidence struct {
	HostID                     string `json:"host_id"`
	SelectedReleaseID          string `json:"selected_release_id"`
	SelectedBundleSHA256       string `json:"selected_bundle_sha256"`
	NamespaceAbsenceSHA256     string `json:"namespace_absence_evidence_sha256,omitempty"`
	FreshInstallEvidenceSHA256 string `json:"fresh_install_evidence_sha256,omitempty"`
	FreshInstallAbsenceSHA256  string `json:"fresh_install_absence_evidence_sha256,omitempty"`
	PreviousSelectorOperation  string `json:"previous_selector_operation_id,omitempty"`
	ConfigurationSHA256        string `json:"configuration_sha256"`
	SchemaVersion              int    `json:"schema_version"`
	WorkInventorySHA256        string `json:"work_inventory_sha256"`
	WorkInventoryEmpty         bool   `json:"work_inventory_empty"`
	LegacyCompatibilitySHA256  string `json:"legacy_compatibility_sha256,omitempty"`
}

func prdBackupRecoveryPackageDigest(release Release, baselines []HostObservation, operatingBuffer uint64) string {
	if operatingBuffer == 0 {
		operatingBuffer = defaultPromotionOperatingBuffer
	}
	hosts := make([]prdBackupRecoveryHostEvidence, 0, len(baselines))
	for _, baseline := range baselines {
		hosts = append(hosts, prdBackupRecoveryHostEvidence{
			HostID: baseline.HostID, SelectedReleaseID: baseline.SelectedReleaseID,
			SelectedBundleSHA256:       baseline.SelectedBundleSHA,
			NamespaceAbsenceSHA256:     baseline.NamespaceAbsenceSHA256,
			FreshInstallEvidenceSHA256: baseline.FreshInstallEvidenceSHA256,
			FreshInstallAbsenceSHA256:  baseline.FreshInstallAbsenceSHA256,
			PreviousSelectorOperation:  baseline.PreviousSelectorOperation,
			ConfigurationSHA256:        baseline.ConfigurationSHA, SchemaVersion: baseline.SchemaVersion,
			WorkInventorySHA256: baseline.WorkInventorySHA, WorkInventoryEmpty: baseline.WorkInventoryEmpty,
			LegacyCompatibilitySHA256: baseline.CompatibilitySHA,
		})
	}
	return hashJSON(map[string]any{
		"format": "runner-release-prd-backup-recovery-v2", "release_id": release.ID, "release_sha256": release.Digest,
		"hosts": hosts, "host_set": HostOrder,
		"backup":                 "consistent-online-backup-verified-before-switch; required-space-rechecked-after-backup",
		"rollback":               "reverse-selector-journals; preserve databases and accepted work; never auto-restore database",
		"operating_buffer_bytes": operatingBuffer, "selector_reserve_bytes": prdSelectorReserve,
		"required_free_bytes": promotionRequiredCapacity("prd", operatingBuffer),
	})
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
	plan.PeerLaneAbsenceEvidence = make(map[string]map[string]laneinstall.CatalogNamespaceAbsenceReport)
	plan.PeerLaneEvidenceProfile = peerLaneStableIdentityV1
	plan.ExistingHolds = make(map[string]string, len(HostOrder))
	plan.PredecessorBundleSHA256 = make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		if err := b.inspectPublished(ctx, host, release); err != nil {
			return PromotionPlan{}, fmt.Errorf("%s sealed release inspection: %w", host, err)
		}
		selected, err := b.readLaneSnapshotForPlan(ctx, host, lane)
		if err != nil {
			return PromotionPlan{}, fmt.Errorf("%s %s read-only baseline: %w", host, lane, err)
		}
		baseline := HostObservation{HostID: host, Lane: lane, SelectedReleaseID: selectedReleaseID(selected.Selector),
			SelectedBundleSHA: selectedBundleSHA(selected.Selector), ServiceStopped: !selected.Active,
			WorkInventorySHA: selected.Work.SHA256(), WorkInventoryEmpty: selected.Empty,
			ConfigurationSHA: selected.Config, SchemaVersion: selected.Schema,
			PreviousReleaseID: selectedReleaseID(selected.Selector), PreviousBundleSHA: selectedBundleSHA(selected.Selector)}
		if selected.NamespaceAbsence != nil {
			baseline.SelectedNamespaceAbsence = selected.NamespaceAbsence
			baseline.NamespaceAbsenceSHA256 = selected.NamespaceAbsence.EvidenceSHA256
		}
		if selected.FreshInstall != nil {
			baseline.FreshInstallEvidenceSHA256 = selected.FreshInstall.EvidenceSHA256
			baseline.FreshInstallAbsenceSHA256 = selected.FreshInstall.Absence.EvidenceSHA256
			baseline.FreshInstallOperationID = selected.FreshInstall.OperationID
			bindFreshInstallBootstrapIdentity(&baseline, selected.FreshInstallObservation)
		}
		baseline.AvailableBytes, baseline.RequiredBytes = selected.AvailableBytes, selected.RequiredBytes
		baseline.CapacityVerified = selected.AvailableBytes >= selected.RequiredBytes
		if lane == "prd" && baseline.PreviousReleaseID == "" && baseline.SelectedNamespaceAbsence == nil && selected.FreshInstall == nil {
			baseline.CompatibilitySHA, err = legacyCompatibilityInputSHA(b.Config.Hosts[host])
			if err != nil {
				return PromotionPlan{}, fmt.Errorf("%s legacy rollback compatibility input: %w", host, err)
			}
		}
		plan.HostBaselines = append(plan.HostBaselines, baseline)
		predecessor, predecessorErr := promotionSelectedPredecessorIdentity(lane, selected)
		if predecessorErr != nil {
			return PromotionPlan{}, fmt.Errorf("%s predecessor identity is incomplete: %w", host, predecessorErr)
		}
		plan.PredecessorBundleSHA256[host] = predecessor
		if selected.NamespaceAbsence != nil {
			if err := b.verifySelectedNamespaceAbsentNoAdmission(ctx, host, lane, *selected.NamespaceAbsence); err != nil {
				return PromotionPlan{}, fmt.Errorf("%s %s absent namespace admission check: %w", host, lane, err)
			}
		} else {
			admission, err := b.readAdmission(ctx, host, lane)
			if err != nil {
				return PromotionPlan{}, fmt.Errorf("%s %s admission receipt inspection: %w", host, lane, err)
			}
			if admission.AdmissionHeld {
				plan.ExistingHolds[host] = admission.OperationID
			}
		}
		peer := make(map[string]string, len(LaneOrder)-1)
		for _, other := range LaneOrder {
			if other == lane {
				continue
			}
			var absence *laneinstall.CatalogNamespaceAbsenceReport
			peer[other], absence, err = b.readPeerSnapshotEvidence(ctx, host, other, lane, plan.PeerLaneEvidenceProfile, nil, true)
			if err != nil {
				return PromotionPlan{}, fmt.Errorf("%s peer lane %s isolation baseline: %w", host, other, err)
			}
			if absence != nil {
				if plan.PeerLaneAbsenceEvidence[host] == nil {
					plan.PeerLaneAbsenceEvidence[host] = make(map[string]laneinstall.CatalogNamespaceAbsenceReport)
				}
				plan.PeerLaneAbsenceEvidence[host][other] = *absence
				plan.CommonEffects = append(plan.CommonEffects,
					fmt.Sprintf("%s peer lane %s is not installed; preserve complete read-only namespace-absence evidence and require it to remain absent throughout this %s operation", host, other, lane))
			}
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
		plan.BackupRecoveryPackageSHA = prdBackupRecoveryPackageDigest(release, plan.HostBaselines, b.OperatingBuffer)
	}
	return plan, nil
}

func bindFreshInstallBootstrapIdentity(baseline *HostObservation, observation *laneinstall.CatalogPRDFreshInstallObservation) {
	if baseline == nil || observation == nil {
		return
	}
	baseline.FreshInstallBootstrapReleaseID = observation.BootstrapReleaseID
	baseline.FreshInstallBootstrapReleaseSHA256 = observation.BootstrapReleaseSHA256
	baseline.FreshInstallBootstrapBundleSHA256 = observation.BootstrapBundleSHA256
	baseline.FreshInstallBootstrapJournalSHA256 = observation.BootstrapJournalSHA256
}

func promotionSelectedPredecessorIdentity(lane string, snapshot promotionLaneSnapshot) (string, error) {
	if lane == "prd" && snapshot.NamespaceAbsence != nil {
		value := promotionPRDAbsencePredecessorSHA(*snapshot.NamespaceAbsence)
		if digestPattern.MatchString(value) {
			return value, nil
		}
		return "", ErrOperationCorrupted
	}
	if lane == "prd" && snapshot.FreshInstall != nil {
		if laneinstall.ValidateCatalogFreshInstallBaseline(*snapshot.FreshInstall) != nil {
			return "", ErrOperationCorrupted
		}
		value := promotionPRDFreshInstallPredecessorSHA(*snapshot.FreshInstall)
		if digestPattern.MatchString(value) {
			return value, nil
		}
		return "", ErrOperationCorrupted
	}
	if digestPattern.MatchString(selectedBundleSHA(snapshot.Selector)) {
		return selectedBundleSHA(snapshot.Selector), nil
	}
	return "", ErrOperationCorrupted
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
	peerProfile, err := promotionPeerEvidenceProfile(op.PlanSnapshot)
	if err != nil {
		return nil, err
	}
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
			peerDigest, _, peerErr := b.readPeerSnapshotEvidence(ctx, host, peer, op.Lane, peerProfile,
				op.PlanSnapshot.PeerLaneAbsenceEvidence[host], false)
			if peerErr != nil || op.PlanSnapshot.PeerLaneEvidence[host][peer] != peerDigest {
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
			if baseline.SelectedNamespaceAbsence != nil ||
				baseline.FreshInstallEvidenceSHA256 != "" || baseline.FreshInstallAbsenceSHA256 != "" {
				return false
			}
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
	receipt, err := b.syncReceiptFromAndReturn(ctx, host, op.Lane, op.CohortOperationID)
	if err != nil {
		return HostObservation{}, err
	}
	preflightSHA := lastTransitionEvidence(receipt, "preflight", host)
	if !digestPattern.MatchString(preflightSHA) {
		return HostObservation{}, ErrOperationCorrupted
	}
	index := hostIndex(host)
	if index < 0 {
		return HostObservation{}, ErrOperationCorrupted
	}
	preflight := receipt.Hosts[index]
	// catalog-select repeats the work, selector, configuration, schema,
	// backup, and capacity checks locally under the host's maintenance locks.
	// Avoid another coordinator RPC sequence after the synchronized fresh
	// preflight transition, so slower hosts retain the full two-minute window.
	args := []string{"catalog-select", "--host", host, "--lane", op.Lane, "--release-id", op.Release.ID,
		"--expected-target", preflight.PreviousTarget, "--cohort-operation-id", op.CohortOperationID,
		"--evidence-sha256", preflightSHA}
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
	if _, err := promotionPeerEvidenceProfile(op.PlanSnapshot); err != nil {
		return err
	}
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
		peerEvidence, err := b.readPeerEvidenceForPlan(ctx, *op.PlanSnapshot)
		if err != nil || !equalNestedStringMaps(peerEvidence, op.PlanSnapshot.PeerLaneEvidence) {
			return errors.Join(errors.New("peer-lane isolation changed during selected-lane restart"), err)
		}
	}
	return nil
}

func (b *NativePromotionBackend) AcceptCohort(ctx context.Context, op Operation) (Acceptance, error) {
	if _, err := promotionPeerEvidenceProfile(op.PlanSnapshot); err != nil {
		return Acceptance{}, err
	}
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
		if !ok {
			return Acceptance{}, fmt.Errorf("%s configured direct-mTLS controller binding is unavailable", host)
		}
		for _, transport := range []string{"direct-mtls", "queued-ssh"} {
			controller, controllerErr := promotionProbeControllerID(loaded.SchemaVersion(), remote, transport)
			if controllerErr != nil {
				return Acceptance{}, fmt.Errorf("%s configured %s controller binding is unavailable: %w", host, transport, controllerErr)
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
	peerEvidence, err := b.readPeerEvidenceForPlan(ctx, *op.PlanSnapshot)
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

// promotionProbeControllerID resolves the identity used by the configured
// Mac route. Legacy V1/V2 mTLS profiles bind the authenticated certificate to
// the Mac account through their validated environment policy, so they do not
// carry a per-endpoint controller_id. V3 profiles carry that identity
// explicitly and must never fall back to the legacy rule.
func promotionProbeControllerID(schemaVersion int, remote config.RemoteHostProfile, transport string) (string, error) {
	switch transport {
	case "queued-ssh":
		if remote.QueuedBridge == nil || remote.QueuedBridge.Host == "" || remote.QueuedBridge.Port <= 0 {
			return "", ErrInvalidInput
		}
		return config.MacAccount, nil
	case "direct-mtls":
		endpoint := remote.DirectEndpoint
		if endpoint == nil || strings.TrimSpace(endpoint.Name) == "" || strings.TrimSpace(endpoint.Endpoint) == "" {
			return "", ErrInvalidInput
		}
		switch schemaVersion {
		case config.VersionV1, config.VersionV2:
			return config.MacAccount, nil
		case config.VersionV3:
			if endpoint.ControllerType != string(domain.ControllerTypeDirectMTLS) || strings.TrimSpace(endpoint.ControllerID) == "" {
				return "", ErrInvalidInput
			}
			return endpoint.ControllerID, nil
		default:
			return "", ErrInvalidInput
		}
	default:
		return "", ErrInvalidInput
	}
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
	if transport != "mac-local" {
		observeArgs = append(observeArgs, "--include-terminal-result")
	}
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
		if existing.TerminalResult != nil && existing.TerminalResult.Terminal {
			outcome, err := acceptedProbeFromHostReadOnlyResult(op, existing, *existing.TerminalResult, host, transport, controller)
			if err != nil {
				return outcome, err
			}
			return outcome, nil
		}
		if recovered, ok := recoverAcceptedProbeFromOperation(op, existing, host, transport, controller); ok {
			return recovered, nil
		}
		observationBytes, err := json.Marshal(existing)
		if err != nil {
			return promotionProbeOutcome{}, err
		}
		return b.observeAcceptedProbe(ctx, op, host, transport, configPath, socket, observationBytes, existing)
	}
	retryAfterJobID := ""
	if disposition == "retry" {
		retryAfterJobID = "job-7fae7172e801f2e22e7d6fce6014403d"
		preparedArgs = append(preparedArgs, "--retry-after-job-id", retryAfterJobID)
	}
	preparedBytes, err := b.runLane(ctx, host, preparedArgs, nil)
	if err != nil {
		return promotionProbeOutcome{}, err
	}
	var prepared promotionPreparedProbe
	if decodeStrictJSON(preparedBytes, &prepared) != nil || validatePreparedPromotionProbe(prepared, host, op.Lane, transport, op.CohortOperationID, controller) != nil ||
		(retryAfterJobID != "" && prepared.IdempotencyKey != "catalog-probe-"+op.CohortOperationID+"-"+host+"-retry-1") {
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
	stdout := &boundedProcessOutput{limit: maxPromotionProbeOutputBytes}
	stderr := &boundedProcessOutput{limit: maxPromotionProbeStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	probeErr := cmd.Run()
	outcome, decodeFailure := decodePromotionProbeOutcome(stdout.Bytes(), stdout.truncated, host, op.CohortOperationID, transport, "", false)
	if decodeFailure != "" {
		return promotionProbeOutcome{}, fmt.Errorf("catalog-probe-once result_decode=%s; helper_stderr=%q; helper_exit_code=%s: %w",
			decodeFailure, sanitizedProbeHelperStderr(stderr.Bytes(), stderr.truncated), probeExitCode(probeErr), ErrOperationCorrupted)
	}
	if outcome.IdempotencyKey != prepared.IdempotencyKey {
		return promotionProbeOutcome{}, errors.New("catalog-probe-once returned a different idempotency identity than its prepared scope")
	}
	if probeErr != nil {
		return outcome, fmt.Errorf("catalog-probe-once failed; helper_stderr=%q; helper_exit_code=%s: %w",
			sanitizedProbeHelperStderr(stderr.Bytes(), stderr.truncated), probeExitCode(probeErr), probeErr)
	}
	outcome.ConfigurationSHA = prepared.ConfigurationSHA256
	outcome.RequestSHA256 = prepared.RequestSHA256
	outcome.RequestedTimeout = 5
	return outcome, nil
}

func acceptedProbeFromHostReadOnlyResult(op Operation, scope promotionProbeScopeObservation, result laneinstall.CatalogProbeTerminalResult,
	host, transport, controller string) (promotionProbeOutcome, error) {
	outcome := promotionProbeOutcome{
		Transport: transport, HostID: host, OperationID: op.CohortOperationID, AcceptedAt: result.JobCreatedAtUTC,
		JobID: result.JobID, SessionID: result.SessionID, CommandID: result.CommandID, Phase: result.Phase,
		CommandState: result.CommandState, ExitCode: result.ExitCode, FinalEventSequence: result.FinalEventSequence,
		OutputComplete: result.OutputComplete, OutputTruncated: result.OutputTruncated, TeardownState: result.TeardownState,
		ObservedExisting: true, ConfigurationSHA: result.ConfigurationSHA256, RequestSHA256: result.RequestSHA256,
		IdempotencyKey: result.IdempotencyKey, RequestedTimeout: 5,
		AcceptanceEvidence: &promotionProbeAcceptanceEvidence{Method: "host-local-read-only-sqlite-result",
			ScopeCreatedAt: scope.CreatedAt, ScopeExpiresAt: scope.ExpiresAt, HostReadOnlyResult: &result},
	}
	baselineConfig, baselineFound := promotionBaselineConfigurationSHA(op, host)
	created, createdErr := time.Parse(time.RFC3339Nano, scope.CreatedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, scope.ExpiresAt)
	acceptedAt, acceptedErr := time.Parse(time.RFC3339Nano, result.JobCreatedAtUTC)
	if ValidateOperation(op) != nil || op.CohortOperationID == "" || op.OperationID != op.CohortOperationID ||
		scope.HostID != host || scope.Lane != op.Lane || string(scope.Transport) != transport ||
		scope.OperationID != op.CohortOperationID || scope.ControllerType != expectedProbeControllerType(transport) ||
		scope.ControllerID != controller || scope.Environment == "" || scope.State != "accepted" ||
		!result.Terminal || !result.Accepted || !baselineFound || result.HostID != host || result.Lane != op.Lane ||
		string(result.Transport) != transport || result.OperationID != op.CohortOperationID || result.ControllerID != controller ||
		result.ControllerType != scope.ControllerType || result.Environment != scope.Environment ||
		result.ConfigurationSHA256 != scope.ConfigurationSHA256 || result.ConfigurationSHA256 != baselineConfig ||
		result.IdempotencyKey != scope.IdempotencyKey || result.RequestSHA256 != scope.RequestSHA256 || result.JobID != scope.JobID ||
		result.SchemaVersion != store.CurrentSchemaVersion || result.EvidenceSHA256 == "" || !validCatalogProbeTerminalResultDigest(result) ||
		createdErr != nil || expiresErr != nil || acceptedErr != nil || !expires.After(created) ||
		acceptedAt.Before(created) || !acceptedAt.Before(expires) || result.ExitCode == nil || *result.ExitCode != 0 ||
		!promotionProbeOutcomeAccepted(outcome) || result.OutputBytes != int64(len("catalog release maintenance probe\n")) ||
		result.OutputSHA256 != digest([]byte("catalog release maintenance probe\n")) ||
		!result.CommandStopConfirmed || !result.CommandSlotReleased || !result.SessionCleanupConfirmed ||
		!result.SessionReservationReleased || result.SessionState != "closed" || result.OutputUnavailableReason != "" ||
		result.TeardownReason != "runtime_closed" || !result.WorkInventory.Empty() || len(result.EventTypes) < 4 ||
		result.EventTypes[0] != "command_queued" || result.EventTypes[1] != "command_started" ||
		result.EventTypes[len(result.EventTypes)-1] != "command_succeeded" || result.EventChainSHA256 == "" {
		return outcome, errors.New("host-local read-only probe result did not match the accepted scope and complete success guards")
	}
	for _, eventType := range result.EventTypes[2 : len(result.EventTypes)-1] {
		if eventType != "stdout" {
			return outcome, errors.New("host-local read-only probe event sequence is outside the fixed successful shape")
		}
	}
	return outcome, nil
}

func validCatalogProbeTerminalResultDigest(result laneinstall.CatalogProbeTerminalResult) bool {
	want := result.EvidenceSHA256
	result.EvidenceSHA256 = ""
	encoded, err := json.Marshal(result)
	if err != nil {
		return false
	}
	return want == digest(encoded)
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
	stdout := &boundedProcessOutput{limit: maxPromotionProbeOutputBytes}
	stderr := &boundedProcessOutput{limit: maxPromotionProbeStderrBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	observeErr := cmd.Run()
	outcome, decodeFailure := decodePromotionProbeOutcome(stdout.Bytes(), stdout.truncated, host, op.CohortOperationID,
		transport, observation.JobID, true)
	if decodeFailure != "" {
		return promotionProbeOutcome{}, fmt.Errorf("existing accepted probe observation result_decode=%s; helper_stderr=%s; helper_exit_code=%s: %w",
			decodeFailure, sanitizedProbeHelperStderr(stderr.Bytes(), stderr.truncated), probeExitCode(observeErr), ErrOperationCorrupted)
	}
	if observeErr != nil {
		return outcome, fmt.Errorf("existing accepted probe did not pass bounded status observation; helper_stderr=%q; helper_exit_code=%s: %w",
			sanitizedProbeHelperStderr(stderr.Bytes(), stderr.truncated), probeExitCode(observeErr), observeErr)
	}
	outcome.ConfigurationSHA = observation.ConfigurationSHA256
	outcome.RequestSHA256 = observation.RequestSHA256
	outcome.RequestedTimeout = 5
	return outcome, nil
}

// boundedProcessOutput keeps subprocess diagnostics and result decoding within
// fixed memory bounds. It reports the original byte count to the child while
// recording whether any output was omitted locally.
type boundedProcessOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedProcessOutput) Write(value []byte) (int, error) {
	if b.limit < 0 {
		return 0, errors.New("negative subprocess output limit")
	}
	remaining := b.limit - b.Len()
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		if _, err := b.Buffer.Write(value[:remaining]); err != nil {
			return 0, err
		}
	}
	if remaining < len(value) {
		b.truncated = true
	}
	return len(value), nil
}

func decodePromotionProbeOutcome(data []byte, truncated bool, host, operationID, transport, jobID string, observed bool) (promotionProbeOutcome, string) {
	if truncated || len(data) > maxPromotionProbeOutputBytes {
		return promotionProbeOutcome{}, "stdout exceeded 8 MiB bound"
	}
	if len(data) == 0 {
		return promotionProbeOutcome{}, "empty stdout"
	}
	if !json.Valid(data) {
		return promotionProbeOutcome{}, "stdout is not valid JSON"
	}
	var outcome promotionProbeOutcome
	if decodeStrictJSON(data, &outcome) != nil {
		return promotionProbeOutcome{}, "stdout failed strict outcome decoding"
	}
	if outcome.HostID != host || outcome.OperationID != operationID || outcome.Transport != transport || outcome.AcceptanceEvidence != nil ||
		(jobID != "" && outcome.JobID != jobID) || (observed && !outcome.ObservedExisting) || (!observed && outcome.ObservedExisting) {
		return promotionProbeOutcome{}, "outcome identity or observation contract mismatch"
	}
	return outcome, ""
}

func sanitizedProbeHelperStderr(data []byte, truncated bool) string {
	if truncated || len(data) > maxPromotionProbeStderrBytes {
		return "stderr exceeded 4 KiB bound; content omitted"
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return "empty stderr"
	}
	lines := strings.Split(string(data), "\n")
	diagnostic := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || safeGoRunExitLine(line) {
			continue
		}
		if !safeProbeHelperDiagnosticLine(line) || diagnostic != "" {
			return "unrecognized helper stderr; content omitted"
		}
		diagnostic = line
	}
	if diagnostic == "" {
		return "empty helper diagnostic"
	}
	return diagnostic
}

func safeGoRunExitLine(line string) bool {
	const prefix = "exit status "
	if !strings.HasPrefix(line, prefix) {
		return false
	}
	value := strings.TrimPrefix(line, prefix)
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func safeProbeHelperDiagnosticLine(line string) bool {
	if safeQueuedProbeBridgeDiagnostic(line) {
		return true
	}
	fixed := []string{
		"fence check does not accept a prepared probe capability",
		"direct fence host is invalid",
		"Mac-local fence does not accept a worker host",
		"invalid probe helper arguments",
		"prepared capability is unavailable",
		"prepared capability is invalid",
		"accepted probe observation scope is invalid",
		"prepared capability transport mismatch",
		"prepared request body digest is invalid",
		"prepared capability request does not match its bound host, lane, or environment",
		"Mac status-route configuration could not be validated",
		"accepted probe host binding is invalid",
		"accepted Mac-local probe configuration binding changed",
		"accepted Mac-local probe policy binding changed",
		"accepted probe request identity is invalid",
		"Mac-local accepted probe route is invalid",
		"worker accepted probe has a Mac host identity",
		"configured worker status route is unavailable",
		"accepted probe Mac route policy or controller binding changed",
		"unsupported Mac status-route configuration",
		"unsupported worker status transport",
		"invalid worker status target",
		"invalid worker status controller",
		"worker status environment policy is missing or ambiguous",
		"configured server CA is unavailable",
		"configured client certificate is unavailable",
		"configured client key is unavailable",
		"configured direct TLS material failed certificate, CA, fingerprint, or URI validation",
		"accepted probe status transport read failed",
		"accepted probe did not reach a terminal state within the bound",
		"accepted Mac-local probe status transport read failed",
		"accepted Mac-local probe did not reach a terminal state within the bound",
		"accepted probe status response violated the job schema",
		"accepted probe status response job identity did not match its scope",
		"prepared request fields are inconsistent",
		"prepared Mac-local request is inconsistent",
		"prepared worker request is inconsistent",
		"prepared direct request is inconsistent",
		"prepared queued request is inconsistent",
		"prepared request body is incomplete",
		"prepared request body digest mismatch",
		"Mac lane config could not be validated",
		"configured direct route is unavailable",
		"direct fence request transport failed",
		"Mac-local API socket is invalid",
		"Mac-local fence request transport failed",
		"configured queued route is unavailable",
		"configured pinned SSH bridge identity failed validation",
		"could not allocate one-off probe identity",
		"queued probe body is invalid",
		"queued probe body could not be prepared",
		"queued controller identity is invalid",
		"queued probe acknowledgement identities did not match",
		"queued probe status response violated the job schema",
		"queued probe did not reach a terminal state within the bound",
		"direct probe acceptance lacked stable identities",
		"direct probe status read failed",
		"direct probe did not reach a terminal state within the bound",
		"configured direct endpoint is unavailable",
		"unsupported direct TLS configuration version",
		"configured direct CA is invalid",
		"configured direct client identity is invalid",
		"configured direct client certificate is not currently valid",
		"invalid direct endpoint",
		"invalid direct trust root",
		"invalid client key pair",
		"client certificate mismatch",
		"server identity mismatch",
		"Mac-local probe acceptance lacked stable identities",
		"Mac-local probe status read failed",
		"Mac-local probe did not reach a terminal state within the bound",
		"request could not be constructed",
		"request transport failed",
		"bounded response read failed",
		"invalid job status",
		"invalid job resource",
		"probe result could not be encoded",
	}
	for _, allowed := range fixed {
		if line == allowed {
			return true
		}
	}
	const prefix = "accepted probe status read returned HTTP "
	if !strings.HasPrefix(line, prefix) {
		return false
	}
	rest := strings.TrimPrefix(line, prefix)
	status, codePart, ok := strings.Cut(rest, " (response_code ")
	if !ok || len(status) != 3 || !strings.HasSuffix(codePart, ")") {
		return false
	}
	for _, char := range status {
		if char < '0' || char > '9' {
			return false
		}
	}
	code := strings.TrimSuffix(codePart, ")")
	if code == "" || len(code) > 80 {
		return false
	}
	for _, char := range code {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func probeExitCode(err error) string {
	if err == nil {
		return "0"
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return fmt.Sprintf("%d", exitError.ExitCode())
	}
	return "unknown"
}

func validProbeJobIdentity(value string) bool {
	return len(value) == 36 && strings.HasPrefix(value, "job-") && strings.Trim(value[4:], "0123456789abcdef") == ""
}

func validProbeResourceIdentity(sessionID, commandID string) bool {
	_, sessionErr := domain.NewSessionID(sessionID)
	_, commandErr := domain.NewCommandID(commandID)
	return sessionErr == nil && commandErr == nil
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
	legacyPrefix := host + " " + transport + " functional probe: existing accepted probe could not be safely observed (exit="
	legacySuffix := "): " + ErrOperationCorrupted.Error()
	if strings.HasPrefix(errorText, legacyPrefix) && strings.HasSuffix(errorText, legacySuffix) {
		return len(errorText) > len(legacyPrefix)+len(legacySuffix)
	}
	prefix := host + " " + transport + " functional probe: existing accepted probe observation result_decode="
	suffix := ": " + ErrOperationCorrupted.Error()
	if !strings.HasPrefix(errorText, prefix) || !strings.HasSuffix(errorText, suffix) {
		return false
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(errorText, prefix), suffix)
	decodeFailure, rest, ok := strings.Cut(payload, "; helper_stderr=")
	if !ok || !knownProbeResultDecodeFailure(decodeFailure) {
		return false
	}
	stderr, exitPart, ok := strings.Cut(rest, "; helper_exit_code=")
	if !ok || !safeAcceptedProbeDiagnostic(stderr) {
		return false
	}
	return validProbeExitCode(exitPart)
}

func knownProbeResultDecodeFailure(value string) bool {
	switch value {
	case "empty stdout", "stdout exceeded 8 MiB bound", "stdout is not valid JSON",
		"stdout failed strict outcome decoding", "outcome identity or observation contract mismatch":
		return true
	default:
		return false
	}
}

func safeAcceptedProbeDiagnostic(value string) bool {
	switch value {
	case "empty stderr", "empty helper diagnostic", "stderr exceeded 4 KiB bound; content omitted",
		"unrecognized helper stderr; content omitted":
		return true
	default:
		return safeProbeHelperDiagnosticLine(value)
	}
}

func validProbeExitCode(value string) bool {
	if value == "unknown" {
		return true
	}
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
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
	if proof.Method == "host-local-read-only-sqlite-result" {
		return promotionHostReadOnlyResultMatchesOutcome(result, proof)
	}
	eventAt, eventErr := time.Parse(time.RFC3339Nano, proof.ControllerFailureEventAtUTC)
	created, createdErr := time.Parse(time.RFC3339Nano, proof.ScopeCreatedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, proof.ScopeExpiresAt)
	return result.FinalEventSequence == nil && result.AcceptedAt == "" && result.ObservedExisting &&
		contains(HostOrder, result.HostID) && operationPattern.MatchString(result.OperationID) && expectedProbeControllerType(result.Transport) != "" &&
		validProbeJobIdentity(result.JobID) &&
		digestPattern.MatchString(result.ConfigurationSHA) && digestPattern.MatchString(result.RequestSHA256) && result.RequestedTimeout == 5 &&
		proof.Method == acceptedProbeRecoveryMethod && proof.HelperExitCode == 0 &&
		proof.HostReadOnlyResult == nil &&
		proof.HelperContract == acceptedProbeHelperContract && proof.ControllerFailureEventSequence > 0 &&
		digestPattern.MatchString(proof.ControllerFailureEventSHA256) && proof.HelperVerifiedPositiveFinalSequence &&
		eventErr == nil && createdErr == nil && expiresErr == nil && !eventAt.Before(created) && eventAt.Before(expires) &&
		expires.After(created) && expires.Sub(created) <= 10*time.Minute
}

func promotionHostReadOnlyResultMatchesOutcome(outcome promotionProbeOutcome, proof *promotionProbeAcceptanceEvidence) bool {
	if proof == nil || proof.Method != "host-local-read-only-sqlite-result" || proof.HostReadOnlyResult == nil ||
		proof.HelperExitCode != 0 || proof.HelperContract != "" || proof.ControllerFailureEventSequence != 0 ||
		proof.ControllerFailureEventSHA256 != "" || proof.ControllerFailureEventAtUTC != "" || proof.HelperVerifiedPositiveFinalSequence {
		return false
	}
	r := proof.HostReadOnlyResult
	if r == nil || r.FinalEventSequence == nil || r.ExitCode == nil {
		return false
	}
	created, createdErr := time.Parse(time.RFC3339Nano, proof.ScopeCreatedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, proof.ScopeExpiresAt)
	jobCreated, jobCreatedErr := time.Parse(time.RFC3339Nano, r.JobCreatedAtUTC)
	return r.Terminal && r.Accepted && r.EvidenceSHA256 != "" && validCatalogProbeTerminalResultDigest(*r) &&
		createdErr == nil && expiresErr == nil && jobCreatedErr == nil && expires.After(created) &&
		expires.Sub(created) <= 10*time.Minute && !jobCreated.Before(created) && jobCreated.Before(expires) &&
		contains(HostOrder, r.HostID) && operationPattern.MatchString(r.OperationID) && expectedProbeControllerType(string(r.Transport)) == r.ControllerType &&
		r.ControllerID != "" && r.Environment != "" && digestPattern.MatchString(r.ConfigurationSHA256) && digestPattern.MatchString(r.RequestSHA256) &&
		validProbeJobIdentity(r.JobID) && validProbeResourceIdentity(r.SessionID, r.CommandID) &&
		outcome.AcceptedAt == r.JobCreatedAtUTC && outcome.ObservedExisting && outcome.FinalEventSequence != nil &&
		*r.FinalEventSequence > 0 && outcome.Transport == string(r.Transport) && outcome.HostID == r.HostID &&
		outcome.OperationID == r.OperationID && outcome.JobID == r.JobID && outcome.SessionID == r.SessionID &&
		outcome.CommandID == r.CommandID && outcome.Phase == r.Phase && outcome.CommandState == r.CommandState &&
		outcome.ExitCode != nil && r.ExitCode != nil && *outcome.ExitCode == *r.ExitCode &&
		*outcome.FinalEventSequence == *r.FinalEventSequence && outcome.OutputComplete == r.OutputComplete &&
		outcome.OutputTruncated == r.OutputTruncated && outcome.TeardownState == r.TeardownState &&
		outcome.ConfigurationSHA == r.ConfigurationSHA256 && outcome.RequestSHA256 == r.RequestSHA256 &&
		outcome.IdempotencyKey == r.IdempotencyKey && outcome.RequestedTimeout == 5 &&
		r.SchemaVersion == store.CurrentSchemaVersion && r.OutputComplete && !r.OutputTruncated &&
		r.SessionState == "closed" && r.OutputUnavailableReason == "" && r.TeardownReason == "runtime_closed" &&
		r.OutputBytes == int64(len("catalog release maintenance probe\n")) &&
		r.OutputSHA256 == digest([]byte("catalog release maintenance probe\n")) &&
		r.CommandStopConfirmed && r.CommandSlotReleased && r.SessionCleanupConfirmed && r.SessionReservationReleased &&
		r.WorkInventory.Empty() && len(r.EventTypes) >= 4 && r.EventTypes[0] == "command_queued" &&
		r.EventTypes[1] == "command_started" && r.EventTypes[len(r.EventTypes)-1] == "command_succeeded" &&
		digestPattern.MatchString(r.EventChainSHA256) && *r.ExitCode == 0 &&
		fixedProbeEventShape(r.EventTypes)
}

func fixedProbeEventShape(eventTypes []string) bool {
	if len(eventTypes) < 4 || eventTypes[0] != "command_queued" || eventTypes[1] != "command_started" ||
		eventTypes[len(eventTypes)-1] != "command_succeeded" {
		return false
	}
	for _, eventType := range eventTypes[2 : len(eventTypes)-1] {
		if eventType != "stdout" {
			return false
		}
	}
	return true
}

func existingProbeDisposition(existing promotionProbeScopeObservation, host, lane, transport, operationID, controller string, now time.Time) (string, error) {
	expectedKey := "catalog-probe-" + operationID + "-" + host
	if transport != "direct-mtls" {
		expectedKey += "-" + transport
	}
	retryKey := expectedKey + "-retry-1"
	probeKeyMatches := existing.IdempotencyKey == expectedKey ||
		(host == "linux-host" && lane == "prd" && transport == "direct-mtls" && operationID == "63471d41af07b7afaf577d6a0989cc03" && existing.IdempotencyKey == retryKey)
	if existing.HostID != host || existing.Lane != lane || string(existing.Transport) != transport || existing.OperationID != operationID ||
		existing.ControllerType != expectedProbeControllerType(transport) || existing.ControllerID != controller || existing.Environment == "" ||
		!digestPattern.MatchString(existing.ConfigurationSHA256) || !digestPattern.MatchString(existing.RequestSHA256) || !probeKeyMatches {
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
		if existing.IdempotencyKey == retryKey && host == "linux-host" && lane == "prd" && transport == "direct-mtls" &&
			operationID == "63471d41af07b7afaf577d6a0989cc03" {
			return "retry", nil
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
		if existing.IdempotencyKey == expectedKey && host == "linux-host" && lane == "prd" && transport == "direct-mtls" &&
			operationID == "63471d41af07b7afaf577d6a0989cc03" {
			if existing.JobID != "job-7fae7172e801f2e22e7d6fce6014403d" {
				return "", ErrOperationCorrupted
			}
			if expires.After(now) {
				return "observe", nil
			}
			return "retry", nil
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
	if _, err := promotionPeerEvidenceProfile(op.PlanSnapshot); err != nil {
		return nil, err
	}
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
		} else if view.Receipt.Stage == laneinstall.CatalogCohortRolledBack {
			// A retry after the rollback receipt was written must still establish
			// synchronized, stopped, empty state before reporting completion.
			if err := b.syncReceiptFrom(ctx, "mac-control-plane", op.Lane, op.CohortOperationID); err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("rollback cannot finish from cohort stage %s: %w", view.Receipt.Stage, ErrOperationCorrupted)
		}
		return b.finishRollback(ctx, op)
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

// finishRollback keeps a successfully rolled-back fresh/absent PRD namespace
// stopped and held. Such a predecessor has no selected service to start, and
// opening the receipt before discovering that fact would leave the cohort
// partially admitted. Catalog-managed and verified legacy predecessors keep
// the ordinary rollback release path.
func (b *NativePromotionBackend) finishRollback(ctx context.Context, op Operation) (any, error) {
	keepHeld, err := rollbackHasUnselectedPredecessor(op)
	if err != nil {
		return nil, err
	}
	if !keepHeld {
		return b.openAdmissions(ctx, op)
	}
	if err := b.verifyRolledBackHeldCohort(ctx, op); err != nil {
		return nil, err
	}
	return map[string]any{"cohort_operation_id": op.CohortOperationID, "receipt_stage": "rolled_back",
		"admissions_held": true, "services_stopped": true, "synchronized_hosts": HostOrder}, nil
}

// rollbackHasUnselectedPredecessor returns true only for a PRD host whose
// predecessor was explicitly proven absent or freshly bootstrapped. Partial
// evidence is corruption, never a reason to choose the open-admissions path.
func rollbackHasUnselectedPredecessor(op Operation) (bool, error) {
	if op.Lane != "prd" {
		return false, nil
	}
	if op.PlanSnapshot == nil || len(op.PlanSnapshot.HostBaselines) != len(HostOrder) || len(op.Hosts) != len(HostOrder) {
		return false, ErrOperationCorrupted
	}
	needsHeld := false
	for index, host := range HostOrder {
		baseline := op.PlanSnapshot.HostBaselines[index]
		hp := op.Hosts[index]
		if baseline.HostID != host || baseline.Lane != op.Lane || hp.HostID != host ||
			hp.PreviousReleaseID != baseline.PreviousReleaseID || hp.PreviousBundleSHA256 != baseline.PreviousBundleSHA {
			return false, ErrOperationCorrupted
		}
		freshEvidencePresent := baseline.FreshInstallOperationID != "" || baseline.FreshInstallEvidenceSHA256 != "" ||
			baseline.FreshInstallAbsenceSHA256 != "" || baseline.FreshInstallBootstrapReleaseID != "" ||
			baseline.FreshInstallBootstrapReleaseSHA256 != "" || baseline.FreshInstallBootstrapBundleSHA256 != "" ||
			baseline.FreshInstallBootstrapJournalSHA256 != ""
		if freshEvidencePresent {
			if baseline.PreviousReleaseID != "" || baseline.PreviousBundleSHA != "" || baseline.PreviousSelectorOperation != "" ||
				baseline.SelectedReleaseID != "" || baseline.SelectedBundleSHA != "" || baseline.SelectedNamespaceAbsence != nil ||
				baseline.NamespaceAbsenceSHA256 != "" || baseline.CompatibilitySHA != "" || !baseline.ServiceStopped ||
				!baseline.WorkInventoryEmpty || !digestPattern.MatchString(baseline.WorkInventorySHA) ||
				!digestPattern.MatchString(baseline.ConfigurationSHA) || baseline.SchemaVersion <= 0 ||
				!operationPattern.MatchString(baseline.FreshInstallOperationID) ||
				!digestPattern.MatchString(baseline.FreshInstallEvidenceSHA256) ||
				!digestPattern.MatchString(baseline.FreshInstallAbsenceSHA256) ||
				!releasePattern.MatchString(baseline.FreshInstallBootstrapReleaseID) ||
				!digestPattern.MatchString(baseline.FreshInstallBootstrapReleaseSHA256) ||
				!digestPattern.MatchString(baseline.FreshInstallBootstrapBundleSHA256) ||
				!digestPattern.MatchString(baseline.FreshInstallBootstrapJournalSHA256) {
				return false, fmt.Errorf("%s fresh-install predecessor evidence is incomplete or conflicts with the recorded selector: %w", host, ErrOperationCorrupted)
			}
			needsHeld = true
			continue
		}
		if baseline.SelectedNamespaceAbsence != nil || baseline.NamespaceAbsenceSHA256 != "" {
			report := baseline.SelectedNamespaceAbsence
			if baseline.PreviousReleaseID != "" || baseline.PreviousBundleSHA != "" || baseline.PreviousSelectorOperation != "" ||
				baseline.SelectedReleaseID != "" || baseline.SelectedBundleSHA != "" || report == nil ||
				!digestPattern.MatchString(baseline.NamespaceAbsenceSHA256) ||
				report.EvidenceSHA256 != baseline.NamespaceAbsenceSHA256 ||
				laneinstall.ValidateCatalogNamespaceAbsenceReport(*report, host, config.ReleaseLanePRD, config.ReleaseLanePRD) != nil {
				return false, fmt.Errorf("%s absent predecessor evidence is incomplete or conflicts with the recorded selector: %w", host, ErrOperationCorrupted)
			}
			needsHeld = true
			continue
		}
		if baseline.PreviousReleaseID == "" && baseline.PreviousBundleSHA == "" {
			return false, fmt.Errorf("%s unselected predecessor lacks bound absence evidence: %w", host, ErrOperationCorrupted)
		}
	}
	return needsHeld, nil
}

// verifyRolledBackHeldCohort is the postflight for a rollback whose recorded
// predecessor is intentionally unselected. It verifies the synchronized
// receipt, exact hold, selector identity, unchanged configuration/schema/work
// identity, stopped service, and fresh capacity on every host.
func (b *NativePromotionBackend) verifyRolledBackHeldCohort(ctx context.Context, op Operation) error {
	var canonical laneinstall.CatalogCohortReceipt
	for _, host := range HostOrder {
		view, err := b.readReceipt(ctx, host, op.Lane, op.CohortOperationID)
		if err != nil || !view.Present {
			return errors.Join(fmt.Errorf("%s rolled-back receipt is unavailable", host), err)
		}
		if canonical.OperationID == "" {
			canonical = view.Receipt
		} else if !reflectReceiptEqual(canonical, view.Receipt) {
			return fmt.Errorf("%s rolled-back receipt differs from the synchronized cohort: %w", host, ErrOperationCorrupted)
		}
	}
	if canonical.OperationID != op.CohortOperationID || canonical.Lane != config.ReleaseLanePRD ||
		canonical.ReleaseID != op.Release.ID || canonical.Stage != laneinstall.CatalogCohortRolledBack ||
		laneinstall.ValidateCatalogCohortReceipt(canonical) != nil {
		return fmt.Errorf("rolled-back cohort receipt is not valid and synchronized: %w", ErrOperationCorrupted)
	}
	for index, host := range HostOrder {
		baseline := op.PlanSnapshot.HostBaselines[index]
		hp := op.Hosts[index]
		if hp.Switched != receiptHasSwitch(canonical, host) || hp.RolledBack != receiptHasRollback(canonical, host) ||
			hp.Switched && (!hp.RolledBack || hp.SelectorOperation == "") {
			return fmt.Errorf("%s operation journal does not match the rollback receipt: %w", host, ErrOperationCorrupted)
		}
		preflight := canonical.Hosts[index]
		if preflight.HostID != host || preflight.ConfigurationSHA256 != baseline.ConfigurationSHA ||
			preflight.SchemaVersion != baseline.SchemaVersion || preflight.WorkInventorySHA256 != baseline.WorkInventorySHA ||
			preflight.WorkInventoryEmpty != baseline.WorkInventoryEmpty {
			return fmt.Errorf("%s rollback receipt conflicts with the operation baseline: %w", host, ErrOperationCorrupted)
		}
		if baseline.FreshInstallEvidenceSHA256 != "" &&
			(preflight.FreshInstall == nil || preflight.FreshInstall.OperationID != baseline.FreshInstallOperationID ||
				preflight.FreshInstall.EvidenceSHA256 != baseline.FreshInstallEvidenceSHA256 ||
				preflight.FreshInstall.Absence.EvidenceSHA256 != baseline.FreshInstallAbsenceSHA256) {
			return fmt.Errorf("%s receipt does not retain the exact fresh-install predecessor proof: %w", host, ErrOperationCorrupted)
		}
		admission, err := b.readAdmission(ctx, host, op.Lane)
		if err != nil || !admission.AdmissionHeld || admission.OperationID != op.CohortOperationID {
			return errors.Join(fmt.Errorf("%s rollback admission hold is not preserved", host), err)
		}
		status, snapshot, err := b.currentHost(ctx, host, op.Lane)
		if err != nil {
			return err
		}
		if baseline.PreviousReleaseID == "" && baseline.PreviousBundleSHA == "" {
			if selectedReleaseID(status) != "" || selectedBundleSHA(status) != "" || status.Kind != "unselected" {
				return fmt.Errorf("%s selector is not restored to the recorded unselected predecessor: %w", host, ErrOperationCorrupted)
			}
		} else if selectedReleaseID(status) != baseline.PreviousReleaseID || selectedBundleSHA(status) != baseline.PreviousBundleSHA {
			return fmt.Errorf("%s selector differs from the recorded rollback predecessor: %w", host, ErrOperationCorrupted)
		}
		if snapshot.Active || !snapshot.Empty || snapshot.Work.SHA256() != baseline.WorkInventorySHA ||
			snapshot.Config != baseline.ConfigurationSHA || snapshot.Schema != baseline.SchemaVersion ||
			snapshot.AvailableBytes < promotionRequiredCapacity(op.Lane, b.OperatingBuffer) {
			return fmt.Errorf("%s rollback service, work, configuration, schema, or capacity postflight failed: %w", host, ErrOperationCorrupted)
		}
	}
	return nil
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
	return b.readLaneSnapshotWithSelectedAbsence(ctx, host, lane, false)
}

func (b *NativePromotionBackend) readLaneSnapshotForPlan(ctx context.Context, host, lane string) (promotionLaneSnapshot, error) {
	return b.readLaneSnapshotWithSelectedAbsence(ctx, host, lane, lane == "prd")
}

func (b *NativePromotionBackend) readLaneSnapshotWithSelectedAbsence(ctx context.Context, host, lane string,
	allowSelectedPRDAbsence bool) (promotionLaneSnapshot, error) {
	var snapshot promotionLaneSnapshot
	if !contains(HostOrder, host) || !contains(LaneOrder, lane) {
		return snapshot, ErrInvalidInput
	}
	freshInstallObserved := false
	statusBytes, err := b.runLane(ctx, host, []string{"catalog-status", "--host", host, "--lane", lane}, nil)
	if err != nil {
		// A completed replacement bootstrap is no longer an absent namespace.
		// Before its narrowly scoped root-mode correction, strict selector
		// inspection can fail on the service root's known 0775 mode. Accept only
		// the operation-bound observer proving the fresh baseline, absent current
		// selector, stopped service, no listener, and a stable complete process
		// scan. If no prepared baseline exists, retain the existing full-absence
		// observer used for the initial PRD plan.
		if allowSelectedPRDAbsence && lane == "prd" && host == "linux-host" &&
			promotionMayUseAbsentPeer(b.Config, host, lane, lane) {
			observationBytes, observationErr := b.runLane(ctx, host, []string{"catalog-prd-fresh-install-observe",
				"--host", host, "--operation-id", prdBootstrapContinuationOperation}, nil)
			var observation laneinstall.CatalogPRDFreshInstallObservation
			if observationErr == nil && decodeStrictJSON(observationBytes, &observation) == nil &&
				laneinstall.ValidateCatalogPRDFreshInstallObservation(observation, host, prdBootstrapContinuationOperation) == nil &&
				observation.Baseline != nil {
				snapshot.Selector = laneinstall.CatalogSelectorStatus{HostID: host, Lane: config.ReleaseLanePRD, Kind: "unselected"}
				snapshot.Active = false
				snapshot.Config = observation.Baseline.ConfigurationSHA256
				snapshot.Schema = observation.Baseline.DatabaseSchema
				snapshot.Work = laneinstall.CatalogWorkInventory{}
				snapshot.Empty = true
				snapshot.FreshInstall = observation.Baseline
				snapshot.FreshInstallObservation = &observation
				freshInstallObserved = true
			} else {
				report, absenceErr := b.readCatalogNamespaceAbsence(ctx, host, lane, lane)
				if absenceErr != nil {
					return snapshot, errors.Join(fmt.Errorf("%s selected %s namespace absence or fresh-install state is unproven", host, lane), err, observationErr, absenceErr)
				}
				snapshot.NamespaceAbsence = &report
				snapshot.Empty = true
			}
		} else {
			if !allowSelectedPRDAbsence || lane != "prd" || !promotionMayUseAbsentPeer(b.Config, host, lane, lane) {
				return snapshot, errors.Join(ErrOperationCorrupted, err)
			}
			report, absenceErr := b.readCatalogNamespaceAbsence(ctx, host, lane, lane)
			if absenceErr != nil {
				return snapshot, errors.Join(fmt.Errorf("%s selected %s namespace absence is unproven", host, lane), err, absenceErr)
			}
			snapshot.NamespaceAbsence = &report
			snapshot.Empty = true
		}
	} else if decodeStrictJSON(statusBytes, &snapshot.Selector) != nil {
		return snapshot, ErrOperationCorrupted
	}
	if snapshot.NamespaceAbsence == nil && !freshInstallObserved {
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
		if lane == "prd" && host == "linux-host" && snapshot.Selector.Kind == "unselected" {
			baselineBytes, baselineErr := b.runLane(ctx, host, []string{"catalog-prd-fresh-install-baseline", "--host", host}, nil)
			if baselineErr != nil {
				return snapshot, baselineErr
			}
			var report laneinstall.CatalogFreshInstallBaselineReport
			if decodeStrictJSON(baselineBytes, &report) != nil || report.HostID != host || report.Present != (report.Baseline != nil) {
				return snapshot, ErrOperationCorrupted
			}
			if report.Present {
				if laneinstall.ValidateCatalogFreshInstallBaseline(*report.Baseline) != nil ||
					report.Baseline.Absence.SelectedLane != config.ReleaseLanePRD {
					return snapshot, ErrOperationCorrupted
				}
				snapshot.FreshInstall = report.Baseline
			}
		}
	}
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
	_, err := b.syncReceiptFromAndReturn(ctx, source, lane, id)
	return err
}

func (b *NativePromotionBackend) syncReceiptFromAndReturn(ctx context.Context, source, lane, id string) (laneinstall.CatalogCohortReceipt, error) {
	view, err := b.readReceipt(ctx, source, lane, id)
	if err != nil || !view.Present {
		return laneinstall.CatalogCohortReceipt{}, errors.Join(ErrOperationCorrupted, err)
	}
	needsReconciliation := false
	histories := make([]laneinstall.CatalogCohortReceipt, 0, len(HostOrder))
	sourceIndex := hostIndex(source)
	if sourceIndex < 0 {
		return laneinstall.CatalogCohortReceipt{}, ErrInvalidInput
	}
	copyViews := make([]promotionReceiptView, len(HostOrder))
	copyViews[sourceIndex] = view
	readErrors := b.runHostTasks(ctx, func(index int, host string) error {
		if host == source {
			return nil
		}
		copyView, readErr := b.readReceipt(ctx, host, lane, id)
		if readErr != nil {
			return fmt.Errorf("inspect %s receipt before synchronization: %w", host, readErr)
		}
		copyViews[index] = copyView
		return nil
	})
	for index, readErr := range readErrors {
		if readErr != nil {
			return laneinstall.CatalogCohortReceipt{}, readErr
		}
		copyView := copyViews[index]
		if !copyView.Present {
			continue
		}
		histories = append(histories, copyView.Receipt)
		if !laneinstall.CatalogCohortReceiptCanAdvance(copyView.Receipt, view.Receipt) {
			needsReconciliation = true
		}
	}
	// A freshly prepared PRD receipt must be distributed before the per-host
	// admission fences can be recorded. The host-local sync command validates
	// the captured empty-work, configuration, schema, selector, and
	// ServiceActiveAtPreflight identities, so synchronization itself does not
	// need to stop those still-active peers. Once fencing starts, retain the
	// stopped/empty guard for every subsequent synchronization.
	if !(lane == string(config.ReleaseLanePRD) && view.Receipt.Stage == laneinstall.CatalogCohortPrepared) {
		if err := b.stopCohortForReceiptSync(ctx, lane, view.Receipt); err != nil {
			return laneinstall.CatalogCohortReceipt{}, err
		}
	}
	if needsReconciliation {
		if len(histories) < 2 {
			return laneinstall.CatalogCohortReceipt{}, ErrOperationCorrupted
		}
		files := make([]string, 0, len(histories))
		for _, history := range histories {
			data, err := json.MarshalIndent(history, "", "  ")
			if err != nil {
				return laneinstall.CatalogCohortReceipt{}, err
			}
			path, remove, err := writePrivateTemp(append(data, '\n'))
			if err != nil {
				for _, oldPath := range files {
					_ = os.Remove(oldPath)
				}
				return laneinstall.CatalogCohortReceipt{}, err
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
			return laneinstall.CatalogCohortReceipt{}, fmt.Errorf("supported receipt-history reconciliation: %w", err)
		}
		view, err = b.readReceipt(ctx, "mac-control-plane", lane, id)
		if err != nil || !view.Present {
			return laneinstall.CatalogCohortReceipt{}, errors.Join(ErrOperationCorrupted, err)
		}
		for _, host := range HostOrder {
			copyView, inspectErr := b.readReceipt(ctx, host, lane, id)
			if inspectErr != nil || copyView.Present && !laneinstall.CatalogCohortReceiptCanAdvance(copyView.Receipt, view.Receipt) {
				return laneinstall.CatalogCohortReceipt{}, errors.Join(fmt.Errorf("reconciled receipt does not safely extend %s history", host), inspectErr)
			}
		}
	}
	data, err := json.MarshalIndent(view.Receipt, "", "  ")
	if err != nil {
		return laneinstall.CatalogCohortReceipt{}, err
	}
	data = append(data, '\n')
	syncErrors := b.runHostTasks(ctx, func(_ int, host string) error {
		if _, syncErr := b.runLane(ctx, host, []string{"catalog-cohort", "--host", host, "--lane", lane, "--action", "sync", "--receipt-file", "@promo-stdin@"}, data); syncErr != nil {
			return fmt.Errorf("synchronize %s receipt to %s: %w", id, host, syncErr)
		}
		return nil
	})
	for _, syncErr := range syncErrors {
		if syncErr != nil {
			return laneinstall.CatalogCohortReceipt{}, syncErr
		}
	}
	verifyErrors := b.runHostTasks(ctx, func(index int, host string) error {
		copyView, readErr := b.readReceipt(ctx, host, lane, id)
		if readErr != nil || !copyView.Present || !reflectReceiptEqual(copyView.Receipt, view.Receipt) {
			return errors.Join(fmt.Errorf("receipt synchronization verification failed on %s", host), readErr)
		}
		return nil
	})
	for _, verifyErr := range verifyErrors {
		if verifyErr != nil {
			return laneinstall.CatalogCohortReceipt{}, verifyErr
		}
	}
	return view.Receipt, nil
}

// runHostTasks keeps fixture-backed tests deterministic while production
// performs independent per-host receipt reads and writes concurrently. Every
// task is joined before returning so a partial sync remains visible to the
// existing supported reconciliation path.
func (b *NativePromotionBackend) runHostTasks(ctx context.Context, task func(index int, host string) error) []error {
	errorsByHost := make([]error, len(HostOrder))
	if b.laneCommandRunner != nil {
		for index, host := range HostOrder {
			errorsByHost[index] = task(index, host)
		}
		return errorsByHost
	}
	var group sync.WaitGroup
	for index, host := range HostOrder {
		group.Add(1)
		go func(index int, host string) {
			defer group.Done()
			errorsByHost[index] = task(index, host)
		}(index, host)
	}
	group.Wait()
	return errorsByHost
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
	views := make([]hostView, len(HostOrder))
	preflightErrors := b.runHostTasks(ctx, func(index int, host string) error {
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
		views[index] = hostView{report: report}
		return nil
	})
	for _, preflightErr := range preflightErrors {
		if preflightErr != nil {
			return preflightErr
		}
	}
	for index, host := range HostOrder {
		before := views[index].report
		if !before.ServiceActive {
			continue
		}
		if _, err := b.runLane(ctx, host, serviceAction(lane, "stop", host), nil); err != nil {
			return fmt.Errorf("stop %s before receipt synchronization: %w", host, err)
		}
	}
	postStopErrors := b.runHostTasks(ctx, func(index int, host string) error {
		before := views[index].report
		if !before.ServiceActive {
			return nil
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
		return nil
	})
	for _, postStopErr := range postStopErrors {
		if postStopErr != nil {
			return postStopErr
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

func (b *NativePromotionBackend) readPeerEvidence(ctx context.Context, lane, profile string) (map[string]map[string]string, error) {
	return b.readPeerEvidenceWithAbsence(ctx, lane, profile, nil)
}

func (b *NativePromotionBackend) readPeerEvidenceForPlan(ctx context.Context, plan PromotionPlan) (map[string]map[string]string, error) {
	if err := validatePromotionPeerAbsenceEvidence(&plan); err != nil {
		return nil, err
	}
	return b.readPeerEvidenceWithAbsence(ctx, plan.Lane, plan.PeerLaneEvidenceProfile, plan.PeerLaneAbsenceEvidence)
}

func (b *NativePromotionBackend) readPeerEvidenceWithAbsence(ctx context.Context, lane, profile string,
	expected map[string]map[string]laneinstall.CatalogNamespaceAbsenceReport) (map[string]map[string]string, error) {
	if _, err := promotionPeerEvidenceProfile(&PromotionPlan{PeerLaneEvidenceProfile: profile}); err != nil {
		return nil, err
	}
	result := make(map[string]map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		result[host] = make(map[string]string, len(LaneOrder)-1)
		for _, peer := range LaneOrder {
			if peer == lane {
				continue
			}
			var err error
			result[host][peer], _, err = b.readPeerSnapshotEvidence(ctx, host, peer, lane, profile, expected[host], false)
			if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (b *NativePromotionBackend) readPeerSnapshotEvidence(ctx context.Context, host, peer, selectedLane, profile string,
	expected map[string]laneinstall.CatalogNamespaceAbsenceReport, allowNewAbsence bool) (string, *laneinstall.CatalogNamespaceAbsenceReport, error) {
	if peer == selectedLane {
		return "", nil, ErrOperationCorrupted
	}
	if report, ok := expected[peer]; ok {
		if !promotionMayUseAbsentPeer(b.Config, host, peer, selectedLane) {
			return "", nil, ErrOperationCorrupted
		}
		fresh, err := b.readCatalogNamespaceAbsence(ctx, host, peer, selectedLane)
		if err != nil || fresh.EvidenceSHA256 != report.EvidenceSHA256 {
			return "", nil, errors.Join(fmt.Errorf("%s absent %s namespace evidence changed", host, peer), err)
		}
		return promotionPeerAbsenceDigest(fresh), &fresh, nil
	}
	snapshot, err := b.readLaneSnapshot(ctx, host, peer)
	if err == nil {
		digestValue, digestErr := promotionPeerSnapshotDigestForProfile(snapshot, profile)
		return digestValue, nil, digestErr
	}
	if !allowNewAbsence || !promotionMayUseAbsentPeer(b.Config, host, peer, selectedLane) {
		return "", nil, err
	}
	report, absenceErr := b.readCatalogNamespaceAbsence(ctx, host, peer, selectedLane)
	if absenceErr != nil {
		return "", nil, errors.Join(err, absenceErr)
	}
	return promotionPeerAbsenceDigest(report), &report, nil
}

func promotionMayUseAbsentPeer(cfg HostConfiguration, host, peer, selectedLane string) bool {
	if host != "linux-host" || !laneinstall.CatalogNamespaceAbsenceScopeAllowed(host, config.ReleaseLane(peer), config.ReleaseLane(selectedLane)) {
		return false
	}
	profile, ok := cfg.Hosts[host]
	return ok && profile.HostID == host && profile.Target == replacementLinuxTarget && profile.AllowFirstInstallPeerAbsence
}

func (b *NativePromotionBackend) readCatalogNamespaceAbsence(ctx context.Context, host, peerLane, selectedLane string) (laneinstall.CatalogNamespaceAbsenceReport, error) {
	var report laneinstall.CatalogNamespaceAbsenceReport
	peer, selected := config.ReleaseLane(peerLane), config.ReleaseLane(selectedLane)
	if !laneinstall.CatalogNamespaceAbsenceScopeAllowed(host, peer, selected) {
		return report, ErrInvalidInput
	}
	out, err := b.runLane(ctx, host, []string{"catalog-namespace-absence", "--host", host,
		"--peer-lane", peerLane, "--selected-lane", selectedLane}, nil)
	if err != nil {
		return report, err
	}
	if decodeStrictJSON(out, &report) != nil || laneinstall.ValidateCatalogNamespaceAbsenceReport(report, host, peer, selected) != nil {
		return laneinstall.CatalogNamespaceAbsenceReport{}, ErrOperationCorrupted
	}
	return report, nil
}

func (b *NativePromotionBackend) verifySelectedNamespaceAbsentNoAdmission(ctx context.Context, host, lane string,
	expected laneinstall.CatalogNamespaceAbsenceReport) error {
	if lane != "prd" || !promotionMayUseAbsentPeer(b.Config, host, lane, lane) ||
		laneinstall.ValidateCatalogNamespaceAbsenceReport(expected, host, config.ReleaseLane(lane), config.ReleaseLane(lane)) != nil {
		return ErrOperationCorrupted
	}
	fresh, err := b.readCatalogNamespaceAbsence(ctx, host, lane, lane)
	if err != nil || fresh.EvidenceSHA256 != expected.EvidenceSHA256 {
		return errors.Join(fmt.Errorf("selected namespace absence or receipt-root evidence changed: %w", ErrOperationCorrupted), err)
	}
	return nil
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

func receiptHasRollback(receipt laneinstall.CatalogCohortReceipt, host string) bool {
	for _, transition := range receipt.Transitions {
		if transition.Action == "rollback-host" && transition.HostID == host {
			return true
		}
	}
	return false
}

func safeQueuedProbeBridgeDiagnostic(line string) bool {
	for _, stage := range []string{"preflight", "call", "status"} {
		prefix := "queued probe " + stage + " bridge "
		if line == prefix+"transport failed" || line == prefix+"returned a malformed error response" ||
			line == prefix+"returned an unexpected response type" {
			return true
		}
		for _, phase := range []string{"before_send", "after_send"} {
			if line == prefix+"transport failed phase="+phase {
				return true
			}
		}
		const codePrefix = "rejected code="
		if strings.HasPrefix(line, prefix+codePrefix) {
			code := strings.TrimPrefix(line, prefix+codePrefix)
			if code == "" || len(code) > 64 {
				return false
			}
			for _, char := range code {
				if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
					return false
				}
			}
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
