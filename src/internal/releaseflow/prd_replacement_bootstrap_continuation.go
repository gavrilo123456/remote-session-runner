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

var prdBootstrapContinuationPlanForTests func(context.Context, *NativePromotionBackend, Release, string) (PromotionPlan, error)

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
				!digestPattern.MatchString(baseline.ConfigurationSHA) || baseline.SchemaVersion <= 0 {
				return false
			}
			continue
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
			approvalSHA, owner, control, native.ControllerRevision, hostControlSHA, now); err != nil {
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
	release Release, approval Approval, approvalSHA, owner, control, controllerRevision, hostControlSHA string, now time.Time) error {
	if err := validatePRDReplacementBootstrapContinuationShape(record); err != nil {
		return err
	}
	approvalEnd, approvalErr := time.Parse(time.RFC3339, approval.WindowEnd)
	start, startErr := time.Parse(time.RFC3339Nano, record.WindowStartUTC)
	end, endErr := time.Parse(time.RFC3339Nano, record.WindowEndUTC)
	if approvalErr != nil || startErr != nil || endErr != nil || now.Before(approvalEnd) || now.Before(start) || !now.Before(end) ||
		record.OperationID != journal.OperationID || record.ReleaseID != release.ID || record.ReleaseSHA256 != release.Digest ||
		record.RequestSHA256 != requestSHA || record.OriginalApprovalSHA256 != approvalSHA || journal.ApprovalSHA256 != approvalSHA ||
		record.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(journal) ||
		record.ControllerRevision != controllerRevision || record.HostControlSHA256 != hostControlSHA ||
		record.Owner != approval.Owner || record.Owner != strings.TrimSpace(owner) || journal.PublisherOwner != owner || journal.PublisherControl != control ||
		request.OperationID != journal.OperationID || request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest ||
		!request.PublishersPaused || !request.NoNewAccepted || request.PublisherOwner != owner || request.PublisherControl != control ||
		!sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:4]) || journal.MacJournalSHA256 != "" ||
		!sameStringMap(approval.PredecessorBundleSHA256, journal.PredecessorBundles) ||
		approval.BackupRecoveryPackageSHA256 != journal.BackupPackageSHA256 ||
		!prdBootstrapContinuationPlanValid(record.PreflightPlan, release.ID, release.Digest, &approval) {
		return ErrApprovalRequired
	}
	return nil
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
