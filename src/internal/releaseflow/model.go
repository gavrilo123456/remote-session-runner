// Package releaseflow implements the durable, lane-neutral release workflow
// used by build-release, promote-to, and runner-release. It owns workflow
// records only; service, selector, backup, and receipt mutations remain behind
// the existing catalog operations exposed by the backend.
package releaseflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

const FormatVersion = 1

var (
	ErrInvalidInput       = errors.New("invalid release workflow input")
	ErrOperationConflict  = errors.New("another operation already owns this lane")
	ErrOperationCorrupted = errors.New("release workflow record is invalid or corrupted")
	ErrReleaseMismatch    = errors.New("release identity does not match the operation")
	ErrHostOutOfOrder     = errors.New("host is not the next permitted cohort step")
	ErrApprovalRequired   = errors.New("PRD requires release-bound owner approval")
	ErrBackendUnavailable = errors.New("release workflow backend is unavailable")
	ErrAdmissionHeld      = errors.New("a selected-lane cohort admission hold already exists")
	ErrBranchConflict     = errors.New("source marker is missing, divergent, or changed from its planned identity")
)

var (
	shaPattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	releasePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	operationPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	branchPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

var HostOrder = []string{"linux-host", "sandbox-host", "factory-sandbox", "mac-control-plane"}
var LaneOrder = []string{"dev", "qas", "prd"}

type Source struct {
	Branch      string `json:"branch,omitempty"`
	CapturedTip string `json:"captured_branch_tip"`
	Commit      string `json:"commit"`
	PrimaryRef  string `json:"primary_ref"`
	MirrorRef   string `json:"mirror_ref"`
}

type Bundle struct {
	HostID            string                              `json:"host_id"`
	GOOS              string                              `json:"goos"`
	GOARCH            string                              `json:"goarch"`
	Toolchain         string                              `json:"toolchain"`
	SourceRevision    string                              `json:"source_revision"`
	DatabaseSchemaMax int                                 `json:"database_schema_max"`
	Components        []laneinstall.SimpleBundleComponent `json:"components"`
	SHA256            string                              `json:"bundle_sha256"`
	ManifestSHA       string                              `json:"manifest_sha256"`
	Bytes             int64                               `json:"bytes"`
}

type Release struct {
	ID             string            `json:"release_id"`
	Digest         string            `json:"release_sha256"`
	Source         Source            `json:"source"`
	Toolchain      string            `json:"toolchain"`
	ProvenanceSHA  string            `json:"provenance_sha256"`
	Bundles        map[string]Bundle `json:"bundles"`
	PublishedHosts []string          `json:"published_hosts"`
	Sealed         bool              `json:"sealed"`
}

type Stage string

const (
	StagePlanned           Stage = "planned"
	StagePreparing         Stage = "preparing"
	StagePrepared          Stage = "prepared"
	StageSwitching         Stage = "switching"
	StageVerifying         Stage = "verifying"
	StageAccepted          Stage = "accepted"
	StageBranchSyncPending Stage = "branch_sync_pending"
	StageComplete          Stage = "complete"
	StageRollingBack       Stage = "rolling_back"
	StageRollbackComplete  Stage = "rollback_complete"
	StageBlocked           Stage = "blocked"
)

type Event struct {
	Sequence int             `json:"sequence"`
	AtUTC    string          `json:"at_utc"`
	Action   string          `json:"action"`
	HostID   string          `json:"host_id,omitempty"`
	Intent   bool            `json:"intent"`
	Outcome  string          `json:"outcome"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
	Previous string          `json:"previous_sha256,omitempty"`
	SHA256   string          `json:"sha256"`
}

type HostProgress struct {
	HostID                    string `json:"host_id"`
	SwitchIntent              bool   `json:"switch_intent"`
	Switched                  bool   `json:"switched"`
	Verified                  bool   `json:"verified"`
	RolledBack                bool   `json:"rolled_back"`
	SelectorOperation         string `json:"selector_operation,omitempty"`
	ObservedBundleSHA         string `json:"observed_bundle_sha256,omitempty"`
	EvidenceSHA256            string `json:"evidence_sha256,omitempty"`
	PreviousReleaseID         string `json:"previous_release_id,omitempty"`
	PreviousBundleSHA256      string `json:"previous_bundle_sha256,omitempty"`
	PreviousSelectorOperation string `json:"previous_selector_operation_id,omitempty"`
}

type Operation struct {
	FormatVersion              int            `json:"format_version"`
	OperationID                string         `json:"operation_id"`
	Lane                       string         `json:"lane"`
	Release                    Release        `json:"release"`
	Target                     string         `json:"target"`
	Stage                      Stage          `json:"stage"`
	CreatedUTC                 string         `json:"created_utc"`
	UpdatedUTC                 string         `json:"updated_utc"`
	ApprovalSHA                string         `json:"approval_sha256,omitempty"`
	CohortOperationID          string         `json:"cohort_operation_id,omitempty"`
	PreviousCohortOperationID  string         `json:"previous_cohort_operation_id,omitempty"`
	ExpectedBranchMarker       string         `json:"expected_branch_marker,omitempty"`
	ExpectedBranchMarkerExists bool           `json:"expected_branch_marker_exists"`
	PlanSnapshot               *PromotionPlan `json:"plan_snapshot,omitempty"`
	Profile                    string         `json:"acceptance_profile"`
	Hosts                      []HostProgress `json:"hosts"`
	BranchSync                 string         `json:"branch_sync"`
	LastError                  string         `json:"last_error,omitempty"`
	Events                     []Event        `json:"events"`
}

type BuildRecord struct {
	FormatVersion            int                          `json:"format_version"`
	BuildID                  string                       `json:"build_id"`
	ReleaseID                string                       `json:"release_id"`
	Source                   Source                       `json:"source"`
	Stage                    string                       `json:"stage"`
	CreatedUTC               string                       `json:"created_utc"`
	UpdatedUTC               string                       `json:"updated_utc"`
	Bundles                  []Bundle                     `json:"bundles"`
	BuildSettings            map[string]HostBuildSettings `json:"build_settings,omitempty"`
	QualificationReceiptRefs []string                     `json:"qualification_receipt_refs,omitempty"`
	Artifacts                map[string]string            `json:"artifact_paths"`
	PublishedHosts           []string                     `json:"published_hosts"`
	LastError                string                       `json:"last_error,omitempty"`
	ProvenanceSHA            string                       `json:"provenance_sha256,omitempty"`
}

// SourceTestReceipt is an owner-authored record of native full-suite gates
// completed for one exact source commit on one exact host/toolchain. The
// referenced evidence digest points to the captured test/vet/build/smoke log.
type SourceTestReceipt struct {
	FormatVersion         int               `json:"format_version"`
	SourceCommit          string            `json:"source_commit"`
	HostID                string            `json:"host_id"`
	GOOS                  string            `json:"goos"`
	GOARCH                string            `json:"goarch"`
	Toolchain             string            `json:"toolchain"`
	ToolchainBinarySHA256 string            `json:"toolchain_binary_sha256"`
	CoverageProfile       string            `json:"coverage_profile"`
	Gates                 map[string]string `json:"gates"`
	EvidenceSHA256        string            `json:"evidence_sha256"`
	TestedAtUTC           string            `json:"tested_at_utc"`
}

const NativeFullCoverageProfile = "native-full-v1"

var RequiredSourceTestGates = []string{"test", "vet", "build", "smoke"}

func validSourceTestReceipt(r SourceTestReceipt) bool {
	if r.FormatVersion != FormatVersion || !shaPattern.MatchString(r.SourceCommit) || !contains(HostOrder, r.HostID) ||
		r.GOOS == "" || r.GOARCH == "" || r.Toolchain != "go1.27.1" || !digestPattern.MatchString(r.ToolchainBinarySHA256) ||
		r.CoverageProfile != NativeFullCoverageProfile || !digestPattern.MatchString(r.EvidenceSHA256) || len(r.Gates) != len(RequiredSourceTestGates) {
		return false
	}
	for _, gate := range RequiredSourceTestGates {
		if r.Gates[gate] != "passed" {
			return false
		}
	}
	_, err := time.Parse(time.RFC3339Nano, r.TestedAtUTC)
	return err == nil
}

type HostBuildSettings struct {
	HostID             string   `json:"host_id"`
	ToolchainBinarySHA string   `json:"toolchain_binary_sha256"`
	GOOS               string   `json:"goos"`
	GOARCH             string   `json:"goarch"`
	CGOEnabled         bool     `json:"cgo_enabled"`
	Flags              []string `json:"flags"`
}

type Approval struct {
	FormatVersion               int               `json:"format_version"`
	ReleaseID                   string            `json:"release_id"`
	ReleaseSHA256               string            `json:"release_sha256"`
	HostSet                     []string          `json:"hosts"`
	WindowStart                 string            `json:"window_start_utc"`
	WindowEnd                   string            `json:"window_end_utc"`
	Owner                       string            `json:"owner"`
	Scope                       string            `json:"scope"`
	PredecessorBundleSHA256     map[string]string `json:"predecessor_bundle_sha256"`
	BackupRecoveryPackageSHA256 string            `json:"backup_recovery_package_sha256"`
}

// AcceptanceRecord is a durable, release-bound proof used as the prerequisite
// for the next lane. A selector or source branch alone is not acceptance.
type AcceptanceRecord struct {
	FormatVersion int               `json:"format_version"`
	Lane          string            `json:"lane"`
	OperationID   string            `json:"operation_id"`
	Profile       string            `json:"profile"`
	ReleaseID     string            `json:"release_id"`
	ReleaseSHA256 string            `json:"release_sha256"`
	BundleHashes  map[string]string `json:"bundle_hashes"`
	EvidenceSHA   string            `json:"evidence_sha256"`
	Passed        bool              `json:"passed"`
	Gates         map[string]string `json:"gates"`
	AcceptedUTC   string            `json:"accepted_utc"`
}

var RequiredAcceptanceGates = []string{"direct-mtls", "queued-ssh", "mac-local", "limits", "capacity", "lifecycle", "teardown", "isolation"}

func validRequiredGates(gates map[string]string) bool {
	if len(gates) < len(RequiredAcceptanceGates) {
		return false
	}
	for _, gate := range RequiredAcceptanceGates {
		if gates[gate] != "passed" {
			return false
		}
	}
	return true
}

func ValidateRelease(r Release) error {
	if !releasePattern.MatchString(r.ID) || !digestPattern.MatchString(r.Digest) ||
		!shaPattern.MatchString(r.Source.Commit) || !digestPattern.MatchString(r.ProvenanceSHA) ||
		r.Toolchain == "" || len(r.Bundles) != len(HostOrder) || !r.Sealed {
		return ErrInvalidInput
	}
	if r.Source.Branch == "" || !validBranch(r.Source.Branch) || !shaPattern.MatchString(r.Source.CapturedTip) ||
		r.Source.PrimaryRef != r.Source.CapturedTip || r.Source.MirrorRef != r.Source.CapturedTip {
		return ErrInvalidInput
	}
	for _, host := range HostOrder {
		b, ok := r.Bundles[host]
		if !ok || b.HostID != host || !digestPattern.MatchString(b.SHA256) ||
			!digestPattern.MatchString(b.ManifestSHA) || b.Bytes <= 0 || b.GOOS == "" || b.GOARCH == "" || b.DatabaseSchemaMax <= 0 || len(b.Components) == 0 ||
			b.Toolchain != r.Toolchain || b.SourceRevision != r.Source.Commit {
			return ErrInvalidInput
		}
	}
	ordered := make([]Bundle, 0, len(r.Bundles))
	for _, host := range HostOrderByName {
		ordered = append(ordered, r.Bundles[host])
	}
	want, err := CanonicalReleaseDigest(r.ID, r.Source.Commit, r.Toolchain, ordered)
	if err != nil || want != r.Digest {
		return ErrReleaseMismatch
	}
	if r.Source.Branch != "" && !branchPattern.MatchString(r.Source.Branch) {
		return ErrInvalidInput
	}
	return nil
}

func ValidateOperation(op Operation) error {
	if op.FormatVersion != FormatVersion || !operationPattern.MatchString(op.OperationID) ||
		!contains(LaneOrder, op.Lane) || (op.Target != "all" && !contains(HostOrder, op.Target)) ||
		ValidateRelease(op.Release) != nil || op.Profile == "" ||
		(len(op.Hosts) != len(HostOrder)) {
		return ErrInvalidInput
	}
	for i, host := range HostOrder {
		if op.Hosts[i].HostID != host {
			return ErrInvalidInput
		}
	}
	if op.ExpectedBranchMarkerExists && !shaPattern.MatchString(op.ExpectedBranchMarker) ||
		!op.ExpectedBranchMarkerExists && op.ExpectedBranchMarker != "" {
		return ErrInvalidInput
	}
	if op.PlanSnapshot != nil && (op.PlanSnapshot.Lane != op.Lane || op.PlanSnapshot.ReleaseID != op.Release.ID ||
		op.PlanSnapshot.ReleaseSHA256 != op.Release.Digest || op.PlanSnapshot.Target != op.Target ||
		len(op.PlanSnapshot.AffectedHosts) != len(HostOrder)) {
		return ErrOperationCorrupted
	}
	if _, err := promotionPeerEvidenceProfile(op.PlanSnapshot); err != nil {
		return err
	}
	if op.CohortOperationID != "" && !operationPattern.MatchString(op.CohortOperationID) {
		return ErrInvalidInput
	}
	if op.PreviousCohortOperationID != "" && (!operationPattern.MatchString(op.PreviousCohortOperationID) ||
		op.PreviousCohortOperationID == op.CohortOperationID) {
		return ErrInvalidInput
	}
	if err := validateEvents(op.Events); err != nil {
		return err
	}
	return nil
}

func validateEvents(events []Event) error {
	previous := ""
	for i := range events {
		e := events[i]
		if e.Sequence != i+1 || e.Action == "" || e.Outcome == "" || !digestPattern.MatchString(e.SHA256) || e.Previous != previous {
			return ErrOperationCorrupted
		}
		computed, err := eventDigest(e)
		if err != nil || computed != e.SHA256 {
			return ErrOperationCorrupted
		}
		previous = e.SHA256
	}
	return nil
}

func AppendEvent(op *Operation, action, host string, intent bool, outcome string, evidence any, at time.Time) error {
	if op == nil || action == "" || outcome == "" || (host != "" && !contains(HostOrder, host)) {
		return ErrInvalidInput
	}
	var raw json.RawMessage
	if evidence != nil {
		data, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		raw = data
	}
	previous := ""
	if n := len(op.Events); n > 0 {
		previous = op.Events[n-1].SHA256
	}
	e := Event{Sequence: len(op.Events) + 1, AtUTC: at.UTC().Format(time.RFC3339Nano), Action: action,
		HostID: host, Intent: intent, Outcome: outcome, Evidence: raw, Previous: previous}
	sha, err := eventDigest(e)
	if err != nil {
		return err
	}
	e.SHA256 = sha
	op.Events = append(op.Events, e)
	op.UpdatedUTC = e.AtUTC
	return nil
}

func eventDigest(e Event) (string, error) {
	e.SHA256 = ""
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func ValidateApproval(a Approval, r Release, now time.Time) error {
	if a.FormatVersion != 1 || a.ReleaseID != r.ID || a.ReleaseSHA256 != r.Digest ||
		a.Owner == "" || a.Scope == "" || !sameStrings(a.HostSet, HostOrder) ||
		len(a.PredecessorBundleSHA256) != len(HostOrder) || !digestPattern.MatchString(a.BackupRecoveryPackageSHA256) {
		return ErrApprovalRequired
	}
	for _, host := range HostOrder {
		if !digestPattern.MatchString(a.PredecessorBundleSHA256[host]) {
			return ErrApprovalRequired
		}
	}
	start, err1 := time.Parse(time.RFC3339, a.WindowStart)
	end, err2 := time.Parse(time.RFC3339, a.WindowEnd)
	if err1 != nil || err2 != nil || !end.After(start) || now.Before(start) || !now.Before(end) {
		return ErrApprovalRequired
	}
	return nil
}

func CanonicalReleaseDigest(releaseID, sourceCommit, toolchain string, bundles []Bundle) (string, error) {
	if !releasePattern.MatchString(releaseID) || !shaPattern.MatchString(sourceCommit) || toolchain == "" || len(bundles) != len(HostOrder) {
		return "", ErrInvalidInput
	}
	copyOf := append([]Bundle(nil), bundles...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i].HostID < copyOf[j].HostID })
	manifests := make(map[string]laneinstall.SimpleBundleManifest, len(copyOf))
	for i, b := range copyOf {
		if b.HostID != HostOrderByName[i] || !digestPattern.MatchString(b.SHA256) || !digestPattern.MatchString(b.ManifestSHA) || b.Toolchain != toolchain || b.SourceRevision != sourceCommit {
			return "", ErrInvalidInput
		}
		manifests[b.HostID] = laneinstall.SimpleBundleManifest{FormatVersion: 1, HostID: b.HostID, GOOS: b.GOOS, GOARCH: b.GOARCH, Toolchain: b.Toolchain, SourceRevision: b.SourceRevision, DatabaseSchemaMax: b.DatabaseSchemaMax, Components: b.Components, BundleSHA256: b.SHA256}
	}
	manifest, err := laneinstall.NewCatalogReleaseManifestFromManifests(releaseID, manifests)
	if err != nil {
		return "", err
	}
	body, err := laneinstall.CatalogReleaseManifestBytes(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

var HostOrderByName = []string{"factory-sandbox", "linux-host", "mac-control-plane", "sandbox-host"}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func NewOperation(id, lane, target string, release Release, profile, approvalSHA string, now time.Time) (Operation, error) {
	if !operationPattern.MatchString(id) || !contains(LaneOrder, lane) || (target != "all" && !contains(HostOrder, target)) || ValidateRelease(release) != nil || profile == "" || (lane == "prd" && !digestPattern.MatchString(approvalSHA)) {
		return Operation{}, fmt.Errorf("%w: cannot create promotion operation", ErrInvalidInput)
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	plan, _ := BuildPlan(lane, target, release)
	op := Operation{FormatVersion: FormatVersion, OperationID: id, CohortOperationID: id, Lane: lane, Release: release, Target: target, Stage: StagePlanned, CreatedUTC: stamp, UpdatedUTC: stamp, ApprovalSHA: approvalSHA, Profile: profile, BranchSync: "not_started", PlanSnapshot: &plan, Hosts: make([]HostProgress, len(HostOrder))}
	for i, h := range HostOrder {
		op.Hosts[i].HostID = h
	}
	return op, nil
}
