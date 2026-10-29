package releaseflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

const (
	prdBootstrapContinuationOperation = "02bfdc603d2ea3360ecd32501fe347d1"
	prdBootstrapContinuationFile      = "window-continuation.json"
	prdBootstrapContinuationFormat    = 1
	prdBootstrapContinuationDuration  = 4 * time.Hour
	prdBootstrapContinuationScope     = "Complete the Mac route stage of this existing replacement bootstrap only; PRD cohort deployment requires a fresh plan and release-bound approval after the route configuration changes."
	prdBootstrapWindowRenewalFile     = "window-renewal.json"
	prdBootstrapWindowRenewalFormat   = 1
	prdBootstrapWindowRenewalRelease  = "rr-309de02fca-ddc84f50"
	prdBootstrapWindowRenewalDeadline = "2026-10-30T10:00:00Z"
	prdBootstrapWindowRenewalScope    = "Continue only replacement bootstrap operation 02bfdc603d2ea3360ecd32501fe347d1 through its Mac connection stage by the owner-authorized deadline; this is not approval for PRD cohort deployment."
)

// PRDReplacementBootstrapContinuation is immutable, operation-specific
// evidence for one continuation window. It supplements, and never rewrites,
// the original approval or coordinator journal.
type PRDReplacementBootstrapContinuation struct {
	FormatVersion             int           `json:"format_version"`
	OperationID               string        `json:"operation_id"`
	ReleaseID                 string        `json:"release_id"`
	ReleaseSHA256             string        `json:"release_sha256"`
	RequestSHA256             string        `json:"request_sha256"`
	OriginalApprovalSHA256    string        `json:"original_approval_sha256"`
	CoordinatorBindingsSHA256 string        `json:"coordinator_bindings_sha256"`
	ControllerRevision        string        `json:"controller_revision"`
	HostControlSHA256         string        `json:"host_control_sha256"`
	PreflightSHA256           string        `json:"preflight_sha256"`
	PreflightPlan             PromotionPlan `json:"preflight_plan"`
	PreflightAtUTC            string        `json:"preflight_at_utc"`
	WindowStartUTC            string        `json:"window_start_utc"`
	WindowEndUTC              string        `json:"window_end_utc"`
	Owner                     string        `json:"owner"`
	Scope                     string        `json:"scope"`
	OwnerAuthorized           bool          `json:"owner_authorized"`
}

type prdBootstrapContinuationResult struct {
	OperationID            string `json:"operation_id"`
	ReleaseID              string `json:"release_id"`
	OriginalApprovalSHA256 string `json:"original_approval_sha256"`
	PreflightSHA256        string `json:"preflight_sha256"`
	RecordSHA256           string `json:"record_sha256"`
	WindowStartUTC         string `json:"window_start_utc"`
	WindowEndUTC           string `json:"window_end_utc"`
	RecordPath             string `json:"record_path"`
}

// PRDReplacementBootstrapWindowRenewal is separate append-only evidence that
// extends the effective deadline for the one owner-authorized bootstrap. It
// never changes the original approval or continuation record.
type PRDReplacementBootstrapWindowRenewal struct {
	FormatVersion             int           `json:"format_version"`
	OperationID               string        `json:"operation_id"`
	ReleaseID                 string        `json:"release_id"`
	ReleaseSHA256             string        `json:"release_sha256"`
	RequestSHA256             string        `json:"request_sha256"`
	OriginalApprovalSHA256    string        `json:"original_approval_sha256"`
	ContinuationSHA256        string        `json:"continuation_sha256"`
	CoordinatorBindingsSHA256 string        `json:"coordinator_bindings_sha256"`
	ControllerRevision        string        `json:"controller_revision"`
	HostControlSHA256         string        `json:"host_control_sha256"`
	PreviousDeadlineUTC       string        `json:"previous_deadline_utc"`
	AuthorizedDeadlineUTC     string        `json:"authorized_deadline_utc"`
	PreflightSHA256           string        `json:"preflight_sha256"`
	PreflightPlan             PromotionPlan `json:"preflight_plan"`
	PreflightAtUTC            string        `json:"preflight_at_utc"`
	Owner                     string        `json:"owner"`
	Scope                     string        `json:"scope"`
	OwnerAuthorized           bool          `json:"owner_authorized"`
}

type prdBootstrapWindowRenewalResult struct {
	OperationID            string `json:"operation_id"`
	ReleaseID              string `json:"release_id"`
	OriginalApprovalSHA256 string `json:"original_approval_sha256"`
	ContinuationSHA256     string `json:"continuation_sha256"`
	PreflightSHA256        string `json:"preflight_sha256"`
	RecordSHA256           string `json:"record_sha256"`
	PreviousDeadlineUTC    string `json:"previous_deadline_utc"`
	AuthorizedDeadlineUTC  string `json:"authorized_deadline_utc"`
	RecordPath             string `json:"record_path"`
}

var prdBootstrapContinuationPlanForTests func(context.Context, *NativePromotionBackend, Release, string) (PromotionPlan, error)
var prdBootstrapWindowRenewalNowForTests func() time.Time

func prdReplacementBootstrapContinuationPath(storeRoot, operationID string) (string, error) {
	if operationID != prdBootstrapContinuationOperation {
		return "", ErrInvalidInput
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(storeRoot, operationID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(journalPath), operationID+"."+prdBootstrapContinuationFile), nil
}

func prdReplacementBootstrapWindowRenewalPath(storeRoot, operationID string) (string, error) {
	if operationID != prdBootstrapContinuationOperation {
		return "", ErrInvalidInput
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(storeRoot, operationID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(journalPath), operationID+"."+prdBootstrapWindowRenewalFile), nil
}

func readPRDReplacementBootstrapWindowRenewal(path string) (PRDReplacementBootstrapWindowRenewal, bool, error) {
	var record PRDReplacementBootstrapWindowRenewal
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 2<<20 {
		return record, false, ErrOperationCorrupted
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return record, false, ErrOperationCorrupted
	}
	if err := readStrictJSON(path, &record); err != nil || validatePRDReplacementBootstrapWindowRenewalShape(record) != nil {
		return PRDReplacementBootstrapWindowRenewal{}, false, ErrOperationCorrupted
	}
	data, err := os.ReadFile(path)
	canonical, marshalErr := json.MarshalIndent(record, "", "  ")
	if err != nil || marshalErr != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return PRDReplacementBootstrapWindowRenewal{}, false, ErrOperationCorrupted
	}
	return record, true, nil
}

func validatePRDReplacementBootstrapWindowRenewalShape(record PRDReplacementBootstrapWindowRenewal) error {
	if record.FormatVersion != prdBootstrapWindowRenewalFormat || record.OperationID != prdBootstrapContinuationOperation ||
		record.ReleaseID != prdBootstrapWindowRenewalRelease || !digestPattern.MatchString(record.ReleaseSHA256) ||
		!digestPattern.MatchString(record.RequestSHA256) || !digestPattern.MatchString(record.OriginalApprovalSHA256) ||
		!digestPattern.MatchString(record.ContinuationSHA256) || !digestPattern.MatchString(record.CoordinatorBindingsSHA256) ||
		!shaPattern.MatchString(record.ControllerRevision) || !digestPattern.MatchString(record.HostControlSHA256) ||
		!validPRDReplacementBootstrapTimestamp(record.PreviousDeadlineUTC) ||
		record.AuthorizedDeadlineUTC != prdBootstrapWindowRenewalDeadline ||
		!validPRDReplacementBootstrapTimestamp(record.PreflightAtUTC) ||
		!digestPattern.MatchString(record.PreflightSHA256) || record.PreflightSHA256 != hashJSON(record.PreflightPlan) ||
		strings.TrimSpace(record.Owner) == "" || record.Scope != prdBootstrapWindowRenewalScope || !record.OwnerAuthorized ||
		!prdBootstrapContinuationPlanValid(record.PreflightPlan, record.ReleaseID, record.ReleaseSHA256, nil) {
		return ErrOperationCorrupted
	}
	previous, previousErr := time.Parse(time.RFC3339Nano, record.PreviousDeadlineUTC)
	deadline, deadlineErr := time.Parse(time.RFC3339, record.AuthorizedDeadlineUTC)
	preflight, preflightErr := time.Parse(time.RFC3339Nano, record.PreflightAtUTC)
	if previousErr != nil || deadlineErr != nil || preflightErr != nil || previous.Location() != time.UTC ||
		deadline.Location() != time.UTC || preflight.Location() != time.UTC || !previous.Before(deadline) || !preflight.Before(deadline) {
		return ErrOperationCorrupted
	}
	return nil
}

func validatePRDReplacementBootstrapWindowRenewal(record PRDReplacementBootstrapWindowRenewal,
	continuation PRDReplacementBootstrapContinuation, continuationBytes []byte,
	journal PRDReplacementBootstrapCoordinatorJournal, request laneinstall.PRDReplacementBootstrapRequest,
	requestSHA string, release Release, approval Approval, approvalSHA, owner, storeRoot, controllerRevision,
	hostControlSHA string, freshPlan PromotionPlan, now time.Time) error {
	if validatePRDReplacementBootstrapWindowRenewalShape(record) != nil ||
		record.OperationID != journal.OperationID || record.ReleaseID != release.ID || record.ReleaseSHA256 != release.Digest ||
		record.RequestSHA256 != requestSHA || record.OriginalApprovalSHA256 != approvalSHA ||
		record.ContinuationSHA256 != digest(continuationBytes) || record.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(journal) ||
		validatePRDBootstrapControllerHistoryBinding(storeRoot, journal, record.ControllerRevision, record.HostControlSHA256,
			controllerRevision, hostControlSHA) != nil ||
		record.PreviousDeadlineUTC != continuation.WindowEndUTC || record.Owner != approval.Owner || record.Owner != strings.TrimSpace(owner) ||
		record.Scope != prdBootstrapWindowRenewalScope || !record.OwnerAuthorized ||
		journal.ApprovalSHA256 != approvalSHA || journal.PublisherOwner != owner ||
		request.OperationID != journal.OperationID || request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest ||
		!request.PublishersPaused || !request.NoNewAccepted || request.PublisherOwner != owner || request.PublisherControl != journal.PublisherControl ||
		!sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:4]) || journal.MacJournalSHA256 != "" ||
		!sameStringMap(approval.PredecessorBundleSHA256, journal.PredecessorBundles) ||
		approval.BackupRecoveryPackageSHA256 != journal.BackupPackageSHA256 ||
		!prdBootstrapContinuationPlanValid(record.PreflightPlan, release.ID, release.Digest, &approval) ||
		!samePRDBootstrapRenewalPlanIdentity(record.PreflightPlan, freshPlan) || now.IsZero() {
		return ErrApprovalRequired
	}
	preflightAt, err := time.Parse(time.RFC3339Nano, record.PreflightAtUTC)
	if err != nil || now.UTC().Before(preflightAt) {
		return ErrApprovalRequired
	}
	return nil
}

func samePRDBootstrapRenewalPlanIdentity(recorded, current PromotionPlan) bool {
	if !prdBootstrapContinuationPlanValid(recorded, recorded.ReleaseID, recorded.ReleaseSHA256, nil) ||
		!prdBootstrapContinuationPlanValid(current, current.ReleaseID, current.ReleaseSHA256, nil) ||
		recorded.Lane != current.Lane || recorded.ReleaseID != current.ReleaseID || recorded.ReleaseSHA256 != current.ReleaseSHA256 ||
		!sameStrings(recorded.HostOrder, current.HostOrder) || !sameStrings(recorded.AffectedHosts, current.AffectedHosts) ||
		!sameStringMap(recorded.PredecessorBundleSHA256, current.PredecessorBundleSHA256) || len(recorded.HostBaselines) != len(current.HostBaselines) {
		return false
	}
	for index := range recorded.HostBaselines {
		a, b := recorded.HostBaselines[index], current.HostBaselines[index]
		if a.HostID != b.HostID || a.Lane != b.Lane || a.WorkInventorySHA != b.WorkInventorySHA ||
			a.WorkInventoryEmpty != b.WorkInventoryEmpty || a.SelectedReleaseID != b.SelectedReleaseID ||
			a.SelectedBundleSHA != b.SelectedBundleSHA || a.ConfigurationSHA != b.ConfigurationSHA ||
			a.SchemaVersion != b.SchemaVersion || a.ServiceStopped != b.ServiceStopped ||
			a.FreshInstallOperationID != b.FreshInstallOperationID || a.FreshInstallEvidenceSHA256 != b.FreshInstallEvidenceSHA256 ||
			a.FreshInstallAbsenceSHA256 != b.FreshInstallAbsenceSHA256 ||
			a.FreshInstallBootstrapReleaseID != b.FreshInstallBootstrapReleaseID ||
			a.FreshInstallBootstrapReleaseSHA256 != b.FreshInstallBootstrapReleaseSHA256 ||
			a.FreshInstallBootstrapBundleSHA256 != b.FreshInstallBootstrapBundleSHA256 ||
			a.FreshInstallBootstrapJournalSHA256 != b.FreshInstallBootstrapJournalSHA256 ||
			b.AvailableBytes < b.RequiredBytes || !b.CapacityVerified {
			return false
		}
	}
	return true
}

func readPRDReplacementBootstrapContinuation(path string) (PRDReplacementBootstrapContinuation, bool, error) {
	var record PRDReplacementBootstrapContinuation
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 2<<20 {
		return record, false, ErrOperationCorrupted
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return record, false, ErrOperationCorrupted
	}
	if err := readStrictJSON(path, &record); err != nil || validatePRDReplacementBootstrapContinuationShape(record) != nil {
		return PRDReplacementBootstrapContinuation{}, false, ErrOperationCorrupted
	}
	data, err := os.ReadFile(path)
	canonical, marshalErr := json.MarshalIndent(record, "", "  ")
	if err != nil || marshalErr != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return PRDReplacementBootstrapContinuation{}, false, ErrOperationCorrupted
	}
	return record, true, nil
}

func validatePRDReplacementBootstrapContinuationShape(record PRDReplacementBootstrapContinuation) error {
	if record.FormatVersion != prdBootstrapContinuationFormat || record.OperationID != prdBootstrapContinuationOperation ||
		!releasePattern.MatchString(record.ReleaseID) || !digestPattern.MatchString(record.ReleaseSHA256) ||
		!digestPattern.MatchString(record.RequestSHA256) || !digestPattern.MatchString(record.OriginalApprovalSHA256) ||
		!digestPattern.MatchString(record.CoordinatorBindingsSHA256) || !shaPattern.MatchString(record.ControllerRevision) ||
		!digestPattern.MatchString(record.HostControlSHA256) || !digestPattern.MatchString(record.PreflightSHA256) ||
		strings.TrimSpace(record.Owner) == "" || record.Scope != prdBootstrapContinuationScope || !record.OwnerAuthorized ||
		!validPRDReplacementBootstrapTimestamp(record.PreflightAtUTC) || record.PreflightAtUTC != record.WindowStartUTC ||
		!validPRDReplacementBootstrapTimestamp(record.WindowStartUTC) || !validPRDReplacementBootstrapTimestamp(record.WindowEndUTC) ||
		record.PreflightSHA256 != hashJSON(record.PreflightPlan) ||
		!prdBootstrapContinuationPlanValid(record.PreflightPlan, record.ReleaseID, record.ReleaseSHA256, nil) {
		return ErrOperationCorrupted
	}
	start, startErr := time.Parse(time.RFC3339Nano, record.WindowStartUTC)
	end, endErr := time.Parse(time.RFC3339Nano, record.WindowEndUTC)
	if startErr != nil || endErr != nil || start.Location() != time.UTC || end.Location() != time.UTC ||
		!end.Equal(start.Add(prdBootstrapContinuationDuration)) {
		return ErrOperationCorrupted
	}
	return nil
}

func prdBootstrapContinuationPlanValid(plan PromotionPlan, releaseID, releaseSHA string, approval *Approval) bool {
	if plan.Lane != "prd" || plan.Target != "all" || plan.ReleaseID != releaseID || plan.ReleaseSHA256 != releaseSHA ||
		!sameStrings(plan.AffectedHosts, HostOrder) || !sameStrings(plan.HostOrder, HostOrder) ||
		len(plan.HostBaselines) != len(HostOrder) || len(plan.PredecessorBundleSHA256) != len(HostOrder) ||
		len(plan.ExistingHolds) != 0 || !digestPattern.MatchString(plan.BackupRecoveryPackageSHA) {
		return false
	}
	required := promotionRequiredCapacity("prd", defaultPromotionOperatingBuffer)
	for index, host := range HostOrder {
		baseline := plan.HostBaselines[index]
		if baseline.HostID != host || baseline.Lane != "prd" || !digestPattern.MatchString(baseline.WorkInventorySHA) ||
			!baseline.WorkInventoryEmpty || !baseline.CapacityVerified || baseline.RequiredBytes != required || baseline.AvailableBytes < required {
			return false
		}
		if host == "linux-host" {
			if baseline.SelectedNamespaceAbsence != nil || baseline.SelectedReleaseID != "" || baseline.SelectedBundleSHA != "" ||
				!baseline.ServiceStopped || baseline.FreshInstallOperationID != prdBootstrapContinuationOperation ||
				!digestPattern.MatchString(baseline.FreshInstallEvidenceSHA256) ||
				!digestPattern.MatchString(baseline.FreshInstallAbsenceSHA256) ||
				baseline.FreshInstallBootstrapReleaseID != releaseID || baseline.FreshInstallBootstrapReleaseSHA256 != releaseSHA ||
				!digestPattern.MatchString(baseline.FreshInstallBootstrapBundleSHA256) ||
				!digestPattern.MatchString(baseline.FreshInstallBootstrapJournalSHA256) ||
				!digestPattern.MatchString(baseline.ConfigurationSHA) || baseline.SchemaVersion <= 0 {
				return false
			}
			continue
		}
		if host == "mac-control-plane" && !baseline.ServiceStopped {
			return false
		}
		if !releasePattern.MatchString(baseline.SelectedReleaseID) || !digestPattern.MatchString(baseline.SelectedBundleSHA) ||
			!digestPattern.MatchString(baseline.ConfigurationSHA) || baseline.SchemaVersion <= 0 {
			return false
		}
		if approval != nil && baseline.SelectedBundleSHA != approval.PredecessorBundleSHA256[host] {
			return false
		}
	}
	if approval != nil {
		for _, host := range HostOrder {
			if host != "linux-host" && plan.PredecessorBundleSHA256[host] != approval.PredecessorBundleSHA256[host] {
				return false
			}
		}
	}
	return true
}

func createPRDReplacementBootstrapContinuation(native *NativePromotionBackend, store *Store,
	request laneinstall.PRDReplacementBootstrapRequest, requestSHA string, release Release, approval Approval,
	approvalSHA, owner, control string, plan PromotionPlan, now time.Time) (PRDReplacementBootstrapContinuation, string, error) {
	if native == nil || store == nil || now.IsZero() || request.OperationID != prdBootstrapContinuationOperation ||
		request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest || requestSHA == "" ||
		strings.TrimSpace(owner) == "" || strings.TrimSpace(control) == "" || !request.PublishersPaused || !request.NoNewAccepted ||
		request.PublisherOwner != owner || request.PublisherControl != control || approvalSHA == "" ||
		approval.Owner != owner || ValidateApprovalScope(approval, release) != nil {
		return PRDReplacementBootstrapContinuation{}, "", ErrInvalidInput
	}
	approvalEnd, err := time.Parse(time.RFC3339, approval.WindowEnd)
	if err != nil || now.Before(approvalEnd) {
		return PRDReplacementBootstrapContinuation{}, "", errors.New("bootstrap continuation is available only after the original approval expires")
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		return PRDReplacementBootstrapContinuation{}, "", err
	}
	journal, exists, err := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !exists {
		return PRDReplacementBootstrapContinuation{}, "", errors.Join(ErrOperationCorrupted, err)
	}
	if fields := bootstrapCoordinatorIdentityMismatches(journal, request, requestSHA, release, approvalSHA, approval, owner, control); len(fields) != 0 {
		return PRDReplacementBootstrapContinuation{}, "", fmt.Errorf("bootstrap continuation rejected: coordinator bindings differ: %s", strings.Join(fields, ", "))
	}
	if !sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:4]) ||
		!digestPattern.MatchString(journal.LinuxJournalSHA256) || journal.MacJournalSHA256 != "" ||
		!sameStringMap(approval.PredecessorBundleSHA256, journal.PredecessorBundles) ||
		approval.BackupRecoveryPackageSHA256 != journal.BackupPackageSHA256 {
		return PRDReplacementBootstrapContinuation{}, "", errors.New("bootstrap continuation requires the exact linux_namespace_ready checkpoint with no Mac route journal")
	}
	if !prdBootstrapContinuationPlanValid(plan, release.ID, release.Digest, &approval) {
		return PRDReplacementBootstrapContinuation{}, "", errors.New("fresh PRD route-continuation preflight failed a work, capacity, hold, selector, or replacement-namespace guard")
	}
	linuxBaseline := plan.HostBaselines[0]
	if linuxBaseline.HostID != "linux-host" || linuxBaseline.FreshInstallBootstrapBundleSHA256 != release.Bundles["linux-host"].SHA256 {
		return PRDReplacementBootstrapContinuation{}, "", errors.New("fresh-install observer is not bound to the approved Linux release bundle")
	}
	profile, ok := native.Config.Hosts["linux-host"]
	if !ok || profile.HostID != "linux-host" || profile.Target != replacementLinuxTarget || !profile.AllowFirstInstallPeerAbsence {
		return PRDReplacementBootstrapContinuation{}, "", ErrInvalidInput
	}
	lookup := prdBootstrapHostKeysForTests
	if lookup == nil {
		lookup = lookupPinnedReplacementHostKeys
	}
	knownHostOutput, err := lookup("79.72.94.59", profile.KnownHostsFile)
	if err != nil || !replacementHostKeyPinned(request.SSHHostPublicKey, knownHostOutput) {
		return PRDReplacementBootstrapContinuation{}, "", errors.Join(ErrOperationCorrupted, err)
	}
	hostControlSHA, err := replacementBootstrapHostControlSHA(native, profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil {
		return PRDReplacementBootstrapContinuation{}, "", err
	}
	if !shaPattern.MatchString(native.ControllerRevision) {
		return PRDReplacementBootstrapContinuation{}, "", ErrOperationCorrupted
	}
	continuationPath, err := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
	if err != nil {
		return PRDReplacementBootstrapContinuation{}, "", err
	}
	if existing, found, readErr := readPRDReplacementBootstrapContinuation(continuationPath); readErr != nil {
		return PRDReplacementBootstrapContinuation{}, "", readErr
	} else if found {
		if err := validatePRDReplacementBootstrapContinuation(existing, journal, request, requestSHA, release, approval,
			approvalSHA, owner, control, native.ControllerRevision, hostControlSHA, store.Root, now); err != nil {
			return PRDReplacementBootstrapContinuation{}, "", err
		}
		data, _ := os.ReadFile(continuationPath)
		return existing, digest(data), nil
	}
	preflightAt := now.UTC()
	preflightAtText := preflightAt.Format(time.RFC3339Nano)
	record := PRDReplacementBootstrapContinuation{
		FormatVersion: prdBootstrapContinuationFormat, OperationID: journal.OperationID, ReleaseID: release.ID,
		ReleaseSHA256: release.Digest, RequestSHA256: requestSHA, OriginalApprovalSHA256: approvalSHA,
		CoordinatorBindingsSHA256: prdReplacementBootstrapStableBindingsSHA(journal), ControllerRevision: native.ControllerRevision,
		HostControlSHA256: hostControlSHA, PreflightSHA256: hashJSON(plan), PreflightPlan: plan,
		PreflightAtUTC: preflightAtText, WindowStartUTC: preflightAtText,
		WindowEndUTC: preflightAt.Add(prdBootstrapContinuationDuration).Format(time.RFC3339Nano),
		Owner:        approval.Owner, Scope: prdBootstrapContinuationScope, OwnerAuthorized: true,
	}
	if err := validatePRDReplacementBootstrapContinuationShape(record); err != nil {
		return PRDReplacementBootstrapContinuation{}, "", err
	}
	if err := writeCanonicalImmutable(continuationPath, record); err != nil {
		return PRDReplacementBootstrapContinuation{}, "", err
	}
	data, err := os.ReadFile(continuationPath)
	if err != nil {
		return PRDReplacementBootstrapContinuation{}, "", err
	}
	return record, digest(data), nil
}

func validatePRDReplacementBootstrapContinuation(record PRDReplacementBootstrapContinuation,
	journal PRDReplacementBootstrapCoordinatorJournal, request laneinstall.PRDReplacementBootstrapRequest, requestSHA string,
	release Release, approval Approval, approvalSHA, owner, control, controllerRevision, hostControlSHA, storeRoot string, now time.Time) error {
	if err := validatePRDReplacementBootstrapContinuationShape(record); err != nil {
		return err
	}
	approvalEnd, approvalErr := time.Parse(time.RFC3339, approval.WindowEnd)
	start, startErr := time.Parse(time.RFC3339Nano, record.WindowStartUTC)
	end, endErr := time.Parse(time.RFC3339Nano, record.WindowEndUTC)
	var mismatches []string
	if approvalErr != nil || startErr != nil || endErr != nil || now.Before(approvalEnd) || now.Before(start) {
		mismatches = append(mismatches, "window")
	}
	if record.OperationID != journal.OperationID || request.OperationID != journal.OperationID {
		mismatches = append(mismatches, "operation_id")
	}
	if record.ReleaseID != release.ID || record.ReleaseSHA256 != release.Digest || request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest {
		mismatches = append(mismatches, "release")
	}
	if record.RequestSHA256 != requestSHA {
		mismatches = append(mismatches, "request_sha256")
	}
	if record.OriginalApprovalSHA256 != approvalSHA || journal.ApprovalSHA256 != approvalSHA {
		mismatches = append(mismatches, "approval_sha256")
	}
	if record.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(journal) {
		mismatches = append(mismatches, "coordinator_bindings_sha256")
	}
	if validatePRDBootstrapControllerHistoryBinding(storeRoot, journal, record.ControllerRevision, record.HostControlSHA256,
		controllerRevision, hostControlSHA) != nil {
		mismatches = append(mismatches, "controller_history")
	}
	if record.Owner != approval.Owner || record.Owner != strings.TrimSpace(owner) || journal.PublisherOwner != owner || journal.PublisherControl != control {
		mismatches = append(mismatches, "publisher_binding")
	}
	if !request.PublishersPaused || !request.NoNewAccepted || request.PublisherOwner != owner || request.PublisherControl != control {
		mismatches = append(mismatches, "publisher_pause")
	}
	if !sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:4]) || journal.MacJournalSHA256 != "" {
		mismatches = append(mismatches, "bootstrap_checkpoint")
	}
	if !sameStringMap(approval.PredecessorBundleSHA256, journal.PredecessorBundles) || approval.BackupRecoveryPackageSHA256 != journal.BackupPackageSHA256 {
		mismatches = append(mismatches, "predecessors_or_backup")
	}
	if !prdBootstrapContinuationPlanValid(record.PreflightPlan, release.ID, release.Digest, &approval) {
		mismatches = append(mismatches, "preflight_plan")
	}
	if len(mismatches) != 0 {
		return fmt.Errorf("bootstrap continuation rejected: %s: %w", strings.Join(mismatches, ", "), ErrApprovalRequired)
	}
	effectiveEnd := end
	renewalPath, pathErr := prdReplacementBootstrapWindowRenewalPath(storeRoot, journal.OperationID)
	if pathErr != nil {
		return errors.Join(ErrApprovalRequired, pathErr)
	}
	renewal, found, readErr := readPRDReplacementBootstrapWindowRenewal(renewalPath)
	if readErr != nil {
		return errors.Join(ErrApprovalRequired, readErr)
	}
	if found {
		continuationPath, continuationPathErr := prdReplacementBootstrapContinuationPath(storeRoot, journal.OperationID)
		continuationBytes, bytesErr := os.ReadFile(continuationPath)
		if continuationPathErr != nil || bytesErr != nil || validatePRDReplacementBootstrapWindowRenewal(renewal,
			record, continuationBytes, journal, request, requestSHA, release, approval, approvalSHA, owner,
			storeRoot, controllerRevision, hostControlSHA, renewal.PreflightPlan, now) != nil {
			return errors.Join(ErrApprovalRequired, continuationPathErr, bytesErr)
		}
		renewedEnd, renewalEndErr := time.Parse(time.RFC3339, renewal.AuthorizedDeadlineUTC)
		if renewalEndErr != nil {
			return ErrOperationCorrupted
		}
		effectiveEnd = renewedEnd
	}
	if !now.Before(effectiveEnd) {
		return ErrApprovalRequired
	}
	return nil
}

func validatePRDBootstrapControllerHistoryBinding(storeRoot string, journal PRDReplacementBootstrapCoordinatorJournal,
	recordedRevision, recordedHostControlSHA, currentRevision, currentHostControlSHA string) error {
	if !shaPattern.MatchString(recordedRevision) || !digestPattern.MatchString(recordedHostControlSHA) ||
		!shaPattern.MatchString(currentRevision) || !digestPattern.MatchString(currentHostControlSHA) {
		return ErrOperationCorrupted
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(storeRoot, journal.OperationID)
	if err != nil {
		return err
	}
	chain, found, err := readPRDReplacementBootstrapControllerUpdateChain(journalPath, journal)
	if err != nil {
		return err
	}
	if !found {
		if recordedRevision != currentRevision || recordedHostControlSHA != currentHostControlSHA {
			return ErrOperationConflict
		}
		return nil
	}
	latest := chain[len(chain)-1]
	if latest.ControllerRevision != currentRevision || latest.HostControlSHA256 != currentHostControlSHA {
		return ErrOperationConflict
	}
	if recordedRevision == prdBootstrapPreviousController && recordedHostControlSHA == journal.HostControlSHA256 {
		return nil
	}
	for _, update := range chain {
		if update.ControllerRevision == recordedRevision && update.HostControlSHA256 == recordedHostControlSHA {
			return nil
		}
	}
	return ErrOperationConflict
}

func createPRDReplacementBootstrapWindowRenewal(native *NativePromotionBackend, store *Store,
	request laneinstall.PRDReplacementBootstrapRequest, requestSHA string, release Release, approval Approval,
	approvalSHA, owner, control string, plan PromotionPlan, now time.Time) (PRDReplacementBootstrapWindowRenewal, string, error) {
	if native == nil || store == nil || now.IsZero() || request.OperationID != prdBootstrapContinuationOperation ||
		request.ReleaseID != prdBootstrapWindowRenewalRelease || release.ID != prdBootstrapWindowRenewalRelease ||
		request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest || requestSHA == "" ||
		strings.TrimSpace(owner) == "" || strings.TrimSpace(control) == "" || !request.PublishersPaused || !request.NoNewAccepted ||
		request.PublisherOwner != owner || request.PublisherControl != control || approvalSHA == "" ||
		approval.Owner != owner || ValidateApprovalScope(approval, release) != nil ||
		!prdBootstrapContinuationPlanValid(plan, release.ID, release.Digest, &approval) {
		return PRDReplacementBootstrapWindowRenewal{}, "", ErrInvalidInput
	}
	deadline, err := time.Parse(time.RFC3339, prdBootstrapWindowRenewalDeadline)
	if err != nil || !now.UTC().Before(deadline) {
		return PRDReplacementBootstrapWindowRenewal{}, "", ErrApprovalRequired
	}
	originalApprovalEnd, err := time.Parse(time.RFC3339, approval.WindowEnd)
	if err != nil || now.UTC().Before(originalApprovalEnd) {
		return PRDReplacementBootstrapWindowRenewal{}, "", errors.New("bootstrap window renewal requires the original approval to have expired")
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	journal, exists, err := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !exists {
		return PRDReplacementBootstrapWindowRenewal{}, "", errors.Join(ErrOperationCorrupted, err)
	}
	if fields := bootstrapCoordinatorIdentityMismatches(journal, request, requestSHA, release, approvalSHA, approval, owner, control); len(fields) != 0 ||
		!sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:4]) || !digestPattern.MatchString(journal.LinuxJournalSHA256) || journal.MacJournalSHA256 != "" ||
		!sameStringMap(approval.PredecessorBundleSHA256, journal.PredecessorBundles) || approval.BackupRecoveryPackageSHA256 != journal.BackupPackageSHA256 {
		return PRDReplacementBootstrapWindowRenewal{}, "", fmt.Errorf("bootstrap window renewal rejected: journal checkpoint or bindings differ: %s", strings.Join(fields, ", "))
	}
	continuationPath, err := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
	if err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	continuation, found, err := readPRDReplacementBootstrapContinuation(continuationPath)
	continuationBytes, bytesErr := os.ReadFile(continuationPath)
	if err != nil || !found || bytesErr != nil || continuation.OperationID != journal.OperationID ||
		continuation.ReleaseID != release.ID || continuation.ReleaseSHA256 != release.Digest ||
		continuation.RequestSHA256 != requestSHA || continuation.OriginalApprovalSHA256 != approvalSHA ||
		continuation.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(journal) ||
		!prdBootstrapContinuationPlanValid(continuation.PreflightPlan, release.ID, release.Digest, &approval) {
		return PRDReplacementBootstrapWindowRenewal{}, "", errors.Join(ErrOperationCorrupted, bytesErr)
	}
	profile, ok := native.Config.Hosts["linux-host"]
	if !ok || profile.HostID != "linux-host" || profile.Target != replacementLinuxTarget || !profile.AllowFirstInstallPeerAbsence {
		return PRDReplacementBootstrapWindowRenewal{}, "", ErrInvalidInput
	}
	lookup := prdBootstrapHostKeysForTests
	if lookup == nil {
		lookup = lookupPinnedReplacementHostKeys
	}
	knownHostOutput, err := lookup("79.72.94.59", profile.KnownHostsFile)
	if err != nil || !replacementHostKeyPinned(request.SSHHostPublicKey, knownHostOutput) {
		return PRDReplacementBootstrapWindowRenewal{}, "", errors.Join(ErrOperationCorrupted, err)
	}
	hostControlSHA, err := replacementBootstrapHostControlSHA(native, profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	if err := validatePRDReplacementBootstrapControllerUpdateForResume(native, journal, store.Root, request, requestSHA,
		release, approval, approvalSHA, owner, control, hostControlSHA, profile, knownHostOutput); err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	recordPath, err := prdReplacementBootstrapWindowRenewalPath(store.Root, request.OperationID)
	if err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	if existing, exists, readErr := readPRDReplacementBootstrapWindowRenewal(recordPath); readErr != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", readErr
	} else if exists {
		if validatePRDReplacementBootstrapWindowRenewal(existing, continuation, continuationBytes, journal, request,
			requestSHA, release, approval, approvalSHA, owner, store.Root, native.ControllerRevision, hostControlSHA, plan, now) != nil ||
			existing.PreviousDeadlineUTC != continuation.WindowEndUTC {
			return PRDReplacementBootstrapWindowRenewal{}, "", ErrOperationConflict
		}
		data, readErr := os.ReadFile(recordPath)
		return existing, digest(data), readErr
	}
	record := PRDReplacementBootstrapWindowRenewal{FormatVersion: prdBootstrapWindowRenewalFormat,
		OperationID: journal.OperationID, ReleaseID: release.ID, ReleaseSHA256: release.Digest,
		RequestSHA256: requestSHA, OriginalApprovalSHA256: approvalSHA, ContinuationSHA256: digest(continuationBytes),
		CoordinatorBindingsSHA256: prdReplacementBootstrapStableBindingsSHA(journal), ControllerRevision: native.ControllerRevision,
		HostControlSHA256: hostControlSHA, PreviousDeadlineUTC: continuation.WindowEndUTC,
		AuthorizedDeadlineUTC: prdBootstrapWindowRenewalDeadline, PreflightSHA256: hashJSON(plan), PreflightPlan: plan,
		PreflightAtUTC: now.UTC().Format(time.RFC3339Nano), Owner: approval.Owner,
		Scope: prdBootstrapWindowRenewalScope, OwnerAuthorized: true}
	if validatePRDReplacementBootstrapWindowRenewalShape(record) != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", ErrOperationCorrupted
	}
	if err := validatePRDReplacementBootstrapWindowRenewal(record, continuation, continuationBytes, journal, request,
		requestSHA, release, approval, approvalSHA, owner, store.Root, native.ControllerRevision, hostControlSHA, plan, now); err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	if err := writeCanonicalImmutable(recordPath, record); err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return PRDReplacementBootstrapWindowRenewal{}, "", err
	}
	return record, digest(data), nil
}

func runPRDReplacementBootstrapContinuationCLI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-release prd-bootstrap-window-continue", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operationID := flags.String("operation", "", "the single authorized replacement bootstrap operation")
	requestFile := flags.String("request-file", "", "owner-only original bootstrap request JSON")
	approvalFile := flags.String("approval-file", "", "unchanged original release-bound PRD approval")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	storeRoot := flags.String("store-root", "", "existing release workflow root")
	publisherOwner := flags.String("publisher-owner", "", "same confirmed PRD publisher-pause owner")
	publisherControl := flags.String("publisher-control", "", "same specific PRD publisher-pause control")
	paused := flags.Bool("publishers-paused", false, "confirm PRD publishers remain paused")
	noNew := flags.Bool("no-new-accepted", false, "confirm no new PRD work was accepted")
	ownerAuthorized := flags.Bool("owner-authorized-one-time-continuation", false, "confirm the owner's one-time four-hour Mac-route continuation authorization")
	jsonOutput := flags.Bool("json", false, "emit sanitized continuation metadata")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *operationID != prdBootstrapContinuationOperation || *requestFile == "" || *approvalFile == "" ||
		strings.TrimSpace(*publisherOwner) == "" || strings.TrimSpace(*publisherControl) == "" || !*paused || !*noNew || !*ownerAuthorized {
		fmt.Fprintln(stderr, "usage: runner-release prd-bootstrap-window-continue --operation 02bfdc603d2ea3360ecd32501fe347d1 --request-file OWNER-ONLY-JSON --approval-file ORIGINAL-APPROVAL --publisher-owner OWNER --publisher-control CONTROL --publishers-paused --no-new-accepted --owner-authorized-one-time-continuation [--hosts-file FILE] [--store-root DIR] [--json]")
		return 2
	}
	if *hostsFile == "" {
		*hostsFile = defaultHostConfigPath()
	}
	if *storeRoot == "" {
		*storeRoot = defaultStorePath()
	}
	requestBytes, err := readPrivateBootstrapRequest(*requestFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var request laneinstall.PRDReplacementBootstrapRequest
	decoder := json.NewDecoder(bytes.NewReader(requestBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.OperationID != *operationID ||
		!releasePattern.MatchString(request.ReleaseID) || !digestPattern.MatchString(request.ExpectedReleaseSHA256) {
		fmt.Fprintln(stderr, ErrInvalidInput)
		return 2
	}
	request.PublisherOwner, request.PublisherControl = strings.TrimSpace(*publisherOwner), strings.TrimSpace(*publisherControl)
	request.PublishersPaused, request.NoNewAccepted = *paused, *noNew
	requestSHA := digest(requestBytes)
	store, err := NewStore(*storeRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	release, err := store.LoadRelease(request.ReleaseID)
	if err != nil || release.Digest != request.ExpectedReleaseSHA256 {
		fmt.Fprintln(stderr, ErrReleaseMismatch)
		return 1
	}
	dev, devErr := store.LoadAcceptance("dev", release.ID)
	qas, qasErr := store.LoadAcceptance("qas", release.ID)
	if devErr != nil || qasErr != nil || !acceptanceMatchesRelease(dev, "dev", release) ||
		!acceptanceMatchesRelease(qas, "qas", release) || dev.Profile != qas.Profile {
		fmt.Fprintln(stderr, "bootstrap continuation requires the exact passed Dev and QAS acceptances")
		return 3
	}
	approval, approvalSHA, err := readApprovalFile(*approvalFile)
	if err != nil || ValidateApprovalScope(approval, release) != nil {
		fmt.Fprintln(stderr, ErrApprovalRequired)
		return 3
	}
	backend := promotionBackendForTests
	if backend == nil {
		native, loadErr := loadNativePromotionBackend(*hostsFile, *storeRoot, request.PublisherOwner, request.PublisherControl,
			true, true, defaultPromotionOperatingBuffer)
		if loadErr != nil {
			fmt.Fprintln(stderr, loadErr)
			return 1
		}
		backend = native
	}
	native, ok := backend.(*NativePromotionBackend)
	if !ok || native == nil {
		fmt.Fprintln(stderr, ErrInvalidInput)
		return 1
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	journal, exists, err := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !exists {
		fmt.Fprintln(stderr, errors.Join(ErrOperationCorrupted, err))
		return 1
	}
	if fields := bootstrapCoordinatorIdentityMismatches(journal, request, requestSHA, release, approvalSHA,
		approval, request.PublisherOwner, request.PublisherControl); len(fields) != 0 {
		fmt.Fprintf(stderr, "bootstrap continuation rejected: coordinator bindings differ: %s\n", strings.Join(fields, ", "))
		return 1
	}
	if ValidateApproval(approval, release, time.Now()) == nil {
		fmt.Fprintln(stderr, "continuation must begin after the original approval expires")
		return 3
	}
	var plan PromotionPlan
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var record PRDReplacementBootstrapContinuation
	var recordSHA string
	err = store.WithLaneLock("prd", func() error {
		if prdBootstrapContinuationPlanForTests != nil {
			plan, err = prdBootstrapContinuationPlanForTests(ctx, native, release, approvalSHA)
		} else {
			plan, err = native.Plan(ctx, "prd", release, approvalSHA)
		}
		if err != nil {
			return fmt.Errorf("fresh read-only PRD continuation preflight failed: %w", err)
		}
		var createErr error
		record, recordSHA, createErr = createPRDReplacementBootstrapContinuation(native, store, request, requestSHA,
			release, approval, approvalSHA, request.PublisherOwner, request.PublisherControl, plan, time.Now().UTC())
		return createErr
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	path, err := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCLIResult(stdout, stderr, prdBootstrapContinuationResult{OperationID: record.OperationID,
		ReleaseID: record.ReleaseID, OriginalApprovalSHA256: record.OriginalApprovalSHA256,
		PreflightSHA256: record.PreflightSHA256, RecordSHA256: recordSHA, WindowStartUTC: record.WindowStartUTC,
		WindowEndUTC: record.WindowEndUTC, RecordPath: path}, *jsonOutput)
}

func runPRDReplacementBootstrapWindowRenewalCLI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-release prd-bootstrap-window-renew", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operationID := flags.String("operation", "", "the one owner-authorized replacement bootstrap operation")
	requestFile := flags.String("request-file", "", "owner-only original bootstrap request JSON")
	approvalFile := flags.String("approval-file", "", "unchanged original release-bound PRD approval")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	storeRoot := flags.String("store-root", "", "existing release workflow root")
	publisherOwner := flags.String("publisher-owner", "", "same confirmed PRD publisher-pause owner")
	publisherControl := flags.String("publisher-control", "", "same specific PRD publisher-pause control")
	paused := flags.Bool("publishers-paused", false, "confirm PRD publishers remain paused")
	noNew := flags.Bool("no-new-accepted", false, "confirm no new PRD work was accepted")
	deadline := flags.String("owner-authorized-deadline", "", "the exact owner-authorized UTC deadline")
	jsonOutput := flags.Bool("json", false, "emit sanitized renewal metadata")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *operationID != prdBootstrapContinuationOperation || *requestFile == "" || *approvalFile == "" ||
		strings.TrimSpace(*publisherOwner) == "" || strings.TrimSpace(*publisherControl) == "" || !*paused || !*noNew ||
		*deadline != prdBootstrapWindowRenewalDeadline {
		fmt.Fprintf(stderr, "usage: runner-release prd-bootstrap-window-renew --operation %s --request-file OWNER-ONLY-JSON --approval-file ORIGINAL-APPROVAL --publisher-owner OWNER --publisher-control CONTROL --publishers-paused --no-new-accepted --owner-authorized-deadline %s [--hosts-file FILE] [--store-root DIR] [--json]\n",
			prdBootstrapContinuationOperation, prdBootstrapWindowRenewalDeadline)
		return 2
	}
	if *hostsFile == "" {
		*hostsFile = defaultHostConfigPath()
	}
	if *storeRoot == "" {
		*storeRoot = defaultStorePath()
	}
	requestBytes, err := readPrivateBootstrapRequest(*requestFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var request laneinstall.PRDReplacementBootstrapRequest
	decoder := json.NewDecoder(bytes.NewReader(requestBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.OperationID != *operationID ||
		request.ReleaseID != prdBootstrapWindowRenewalRelease || !digestPattern.MatchString(request.ExpectedReleaseSHA256) {
		fmt.Fprintln(stderr, ErrInvalidInput)
		return 2
	}
	request.PublisherOwner, request.PublisherControl = strings.TrimSpace(*publisherOwner), strings.TrimSpace(*publisherControl)
	request.PublishersPaused, request.NoNewAccepted = *paused, *noNew
	requestSHA := digest(requestBytes)
	store, err := NewStore(*storeRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	release, err := store.LoadRelease(request.ReleaseID)
	if err != nil || release.Digest != request.ExpectedReleaseSHA256 {
		fmt.Fprintln(stderr, ErrReleaseMismatch)
		return 1
	}
	dev, devErr := store.LoadAcceptance("dev", release.ID)
	qas, qasErr := store.LoadAcceptance("qas", release.ID)
	if devErr != nil || qasErr != nil || !acceptanceMatchesRelease(dev, "dev", release) ||
		!acceptanceMatchesRelease(qas, "qas", release) || dev.Profile != qas.Profile {
		fmt.Fprintln(stderr, "bootstrap window renewal requires the exact passed Dev and QAS acceptances")
		return 3
	}
	approval, approvalSHA, err := readApprovalFile(*approvalFile)
	if err != nil || ValidateApprovalScope(approval, release) != nil || approval.Owner != strings.TrimSpace(*publisherOwner) {
		fmt.Fprintln(stderr, ErrApprovalRequired)
		return 3
	}
	backend := promotionBackendForTests
	if backend == nil {
		native, loadErr := loadNativePromotionBackend(*hostsFile, *storeRoot, request.PublisherOwner, request.PublisherControl,
			true, true, defaultPromotionOperatingBuffer)
		if loadErr != nil {
			fmt.Fprintln(stderr, loadErr)
			return 1
		}
		backend = native
	}
	native, ok := backend.(*NativePromotionBackend)
	if !ok || native == nil {
		fmt.Fprintln(stderr, ErrInvalidInput)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var plan PromotionPlan
	var record PRDReplacementBootstrapWindowRenewal
	var recordSHA string
	preflightTime := func() time.Time {
		if prdBootstrapWindowRenewalNowForTests != nil {
			return prdBootstrapWindowRenewalNowForTests().UTC()
		}
		return time.Now().UTC()
	}
	err = store.WithLaneLock("prd", func() error {
		if prdBootstrapContinuationPlanForTests != nil {
			plan, err = prdBootstrapContinuationPlanForTests(ctx, native, release, approvalSHA)
		} else {
			plan, err = native.Plan(ctx, "prd", release, approvalSHA)
		}
		if err != nil {
			return fmt.Errorf("fresh read-only PRD bootstrap-renewal preflight failed: %w", err)
		}
		var createErr error
		record, recordSHA, createErr = createPRDReplacementBootstrapWindowRenewal(native, store, request, requestSHA,
			release, approval, approvalSHA, request.PublisherOwner, request.PublisherControl, plan, preflightTime())
		return createErr
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	path, err := prdReplacementBootstrapWindowRenewalPath(store.Root, request.OperationID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCLIResult(stdout, stderr, prdBootstrapWindowRenewalResult{OperationID: record.OperationID,
		ReleaseID: record.ReleaseID, OriginalApprovalSHA256: record.OriginalApprovalSHA256,
		ContinuationSHA256: record.ContinuationSHA256, PreflightSHA256: record.PreflightSHA256,
		RecordSHA256: recordSHA, PreviousDeadlineUTC: record.PreviousDeadlineUTC,
		AuthorizedDeadlineUTC: record.AuthorizedDeadlineUTC, RecordPath: path}, *jsonOutput)
}
