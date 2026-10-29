package releaseflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

// Controller amendments are deliberately limited to the one bootstrap
// operation whose journal was created by the previous delivered controller.
// Successor revisions are append-only; they do not change service bytes or
// other operation identities.
const (
	prdBootstrapControllerUpdateOperation = "02bfdc603d2ea3360ecd32501fe347d1"
	prdBootstrapPreviousController        = "309de02fca7bc8dcd608834df76e831a96d413d7"
	prdBootstrapControllerUpdateFile      = "controller-update.json"
	prdBootstrapControllerUpdatePrefix    = "controller-update-"
	prdBootstrapMaxControllerUpdates      = 8
)

const prdBootstrapControllerUpdateFormat = 1

type PRDReplacementBootstrapControllerUpdate struct {
	FormatVersion              int    `json:"format_version"`
	OperationID                string `json:"operation_id"`
	ReleaseID                  string `json:"release_id"`
	ReleaseSHA256              string `json:"release_sha256"`
	PreviousControllerRevision string `json:"previous_controller_revision"`
	PreviousHostControlSHA256  string `json:"previous_host_control_sha256"`
	ControllerRevision         string `json:"controller_revision"`
	HostControlSHA256          string `json:"host_control_sha256"`
	NonControllerInputsSHA256  string `json:"non_controller_inputs_sha256"`
	CoordinatorBindingsSHA256  string `json:"coordinator_bindings_sha256"`
	RecordedAtUTC              string `json:"recorded_at_utc"`
}

func prdReplacementBootstrapControllerUpdatePath(journalPath string) string {
	operationID := strings.TrimSuffix(filepath.Base(journalPath), ".json")
	return filepath.Join(filepath.Dir(journalPath), operationID+"."+prdBootstrapControllerUpdateFile)
}

func prdReplacementBootstrapControllerUpdateRevisionPath(journalPath, revision string) string {
	operationID := strings.TrimSuffix(filepath.Base(journalPath), ".json")
	return filepath.Join(filepath.Dir(journalPath), operationID+"."+prdBootstrapControllerUpdatePrefix+revision+".json")
}

func readPRDReplacementBootstrapControllerUpdate(path string) (PRDReplacementBootstrapControllerUpdate, bool, error) {
	var record PRDReplacementBootstrapControllerUpdate
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64<<10 {
		return record, false, ErrOperationCorrupted
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return record, false, ErrOperationCorrupted
	}
	if err := readStrictJSON(path, &record); err != nil || validatePRDReplacementBootstrapControllerUpdate(record) != nil {
		return PRDReplacementBootstrapControllerUpdate{}, false, ErrOperationCorrupted
	}
	data, err := os.ReadFile(path)
	canonical, marshalErr := json.MarshalIndent(record, "", "  ")
	if err != nil || marshalErr != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return PRDReplacementBootstrapControllerUpdate{}, false, ErrOperationCorrupted
	}
	return record, true, nil
}

func validatePRDReplacementBootstrapControllerUpdate(record PRDReplacementBootstrapControllerUpdate) error {
	if record.FormatVersion != prdBootstrapControllerUpdateFormat || record.OperationID != prdBootstrapControllerUpdateOperation ||
		record.ReleaseID == "" || !releasePattern.MatchString(record.ReleaseID) || !digestPattern.MatchString(record.ReleaseSHA256) ||
		!shaPattern.MatchString(record.PreviousControllerRevision) ||
		!digestPattern.MatchString(record.PreviousHostControlSHA256) ||
		!shaPattern.MatchString(record.ControllerRevision) || record.ControllerRevision == record.PreviousControllerRevision ||
		!digestPattern.MatchString(record.HostControlSHA256) || record.HostControlSHA256 == record.PreviousHostControlSHA256 ||
		!digestPattern.MatchString(record.NonControllerInputsSHA256) || !digestPattern.MatchString(record.CoordinatorBindingsSHA256) ||
		!validPRDReplacementBootstrapTimestamp(record.RecordedAtUTC) {
		return ErrOperationCorrupted
	}
	return nil
}

// readPRDReplacementBootstrapControllerUpdateChain reads the original
// operation amendment plus append-only successor records. The original file
// remains immutable; each later controller revision gets its own file.
func readPRDReplacementBootstrapControllerUpdateChain(journalPath string, journal PRDReplacementBootstrapCoordinatorJournal) ([]PRDReplacementBootstrapControllerUpdate, bool, error) {
	firstPath := prdReplacementBootstrapControllerUpdatePath(journalPath)
	first, found, err := readPRDReplacementBootstrapControllerUpdate(firstPath)
	if err != nil {
		return nil, false, err
	}
	operationID := strings.TrimSuffix(filepath.Base(journalPath), ".json")
	entries, err := os.ReadDir(filepath.Dir(journalPath))
	if err != nil {
		return nil, false, err
	}
	updates := make([]PRDReplacementBootstrapControllerUpdate, 0, prdBootstrapMaxControllerUpdates)
	if found {
		updates = append(updates, first)
	}
	for _, entry := range entries {
		prefix := operationID + "." + prdBootstrapControllerUpdatePrefix
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		revision := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), prefix), ".json")
		if !shaPattern.MatchString(revision) || entry.IsDir() {
			return nil, false, ErrOperationCorrupted
		}
		path := filepath.Join(filepath.Dir(journalPath), entry.Name())
		update, exists, readErr := readPRDReplacementBootstrapControllerUpdate(path)
		if readErr != nil || !exists || update.ControllerRevision != revision {
			return nil, false, errors.Join(ErrOperationCorrupted, readErr)
		}
		updates = append(updates, update)
	}
	if len(updates) == 0 {
		return nil, false, nil
	}
	if len(updates) > prdBootstrapMaxControllerUpdates || !found || first.PreviousControllerRevision != prdBootstrapPreviousController ||
		first.PreviousHostControlSHA256 != journal.HostControlSHA256 {
		return nil, false, ErrOperationCorrupted
	}
	ordered := make([]PRDReplacementBootstrapControllerUpdate, 0, len(updates))
	used := make([]bool, len(updates))
	currentRevision, currentHostSHA := prdBootstrapPreviousController, journal.HostControlSHA256
	for len(ordered) < len(updates) {
		match := -1
		for index, update := range updates {
			if !used[index] && update.PreviousControllerRevision == currentRevision && update.PreviousHostControlSHA256 == currentHostSHA {
				if match != -1 {
					return nil, false, ErrOperationCorrupted
				}
				match = index
			}
		}
		if match == -1 {
			return nil, false, ErrOperationCorrupted
		}
		update := updates[match]
		if update.OperationID != journal.OperationID || update.ReleaseID != journal.ReleaseID || update.ReleaseSHA256 != journal.ReleaseSHA256 ||
			update.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(journal) {
			return nil, false, ErrOperationCorrupted
		}
		if len(ordered) > 0 && update.NonControllerInputsSHA256 != ordered[0].NonControllerInputsSHA256 {
			return nil, false, ErrOperationCorrupted
		}
		used[match] = true
		ordered = append(ordered, update)
		currentRevision, currentHostSHA = update.ControllerRevision, update.HostControlSHA256
	}
	return ordered, true, nil
}

func prdReplacementBootstrapStableBindingsSHA(journal PRDReplacementBootstrapCoordinatorJournal) string {
	return hashJSON(struct {
		OperationID         string            `json:"operation_id"`
		ReleaseID           string            `json:"release_id"`
		ReleaseSHA256       string            `json:"release_sha256"`
		RequestSHA256       string            `json:"request_sha256"`
		ApprovalSHA256      string            `json:"approval_sha256"`
		PlanSHA256          string            `json:"plan_sha256"`
		HostControlSHA256   string            `json:"host_control_sha256"`
		PredecessorBundles  map[string]string `json:"predecessor_bundle_sha256"`
		BackupPackageSHA256 string            `json:"backup_recovery_package_sha256"`
		PublisherOwner      string            `json:"publisher_owner"`
		PublisherControl    string            `json:"publisher_control"`
		CSRRequestSHA256    string            `json:"csr_request_sha256"`
		ServerCertSHA256    string            `json:"server_certificate_sha256"`
		ServerCASHA256      string            `json:"server_ca_sha256"`
	}{journal.OperationID, journal.ReleaseID, journal.ReleaseSHA256, journal.RequestSHA256, journal.ApprovalSHA256,
		journal.PlanSHA256, journal.HostControlSHA256, journal.PredecessorBundles, journal.BackupPackageSHA256,
		journal.PublisherOwner, journal.PublisherControl, journal.CSRRequestSHA256, journal.ServerCertSHA256, journal.ServerCASHA256})
}

func prdReplacementBootstrapNonControllerInputsSHA(profile HostProfile, serverHostKey string, knownHostOutput []byte) (string, error) {
	if profile.HostID != "linux-host" || profile.Target != replacementLinuxTarget || !profile.AllowFirstInstallPeerAbsence ||
		ownerPrivateKeyFile(profile.IdentityFile) != nil || ownerFile(profile.KnownHostsFile, 0) != nil ||
		len(knownHostOutput) == 0 || len(knownHostOutput) > 64<<10 {
		return "", ErrOperationCorrupted
	}
	identityBytes, err := os.ReadFile(profile.IdentityFile)
	if err != nil || len(identityBytes) == 0 || len(identityBytes) > 64<<10 {
		return "", ErrOperationCorrupted
	}
	knownHostsBytes, err := os.ReadFile(profile.KnownHostsFile)
	if err != nil || len(knownHostsBytes) == 0 || len(knownHostsBytes) > 64<<10 {
		return "", ErrOperationCorrupted
	}
	canonicalHostKey, ok := canonicalEd25519HostKey(serverHostKey)
	if !ok {
		return "", ErrOperationCorrupted
	}
	return hashJSON(struct {
		Profile             HostProfile `json:"profile"`
		IdentitySHA256      string      `json:"identity_sha256"`
		KnownHostsSHA256    string      `json:"known_hosts_sha256"`
		PinnedHostKeySHA256 string      `json:"pinned_host_key_sha256"`
		CanonicalServerKey  string      `json:"canonical_server_host_key"`
	}{profile, digest(identityBytes), digest(knownHostsBytes), digest(knownHostOutput), canonicalHostKey}), nil
}

func createPRDReplacementBootstrapControllerUpdate(native *NativePromotionBackend, store *Store, request laneinstall.PRDReplacementBootstrapRequest,
	requestSHA string, release Release, approval Approval, approvalSHA, publisherOwner, publisherControl, continuationFile string, now time.Time) (PRDReplacementBootstrapControllerUpdate, error) {
	if native == nil || store == nil || request.OperationID != prdBootstrapControllerUpdateOperation ||
		request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest || requestSHA == "" ||
		approvalSHA == "" || strings.TrimSpace(publisherOwner) == "" || strings.TrimSpace(publisherControl) == "" ||
		!request.PublishersPaused || !request.NoNewAccepted || request.ApprovalSHA256 != "" || request.ServerCSR256 != "" ||
		len(request.ServerCertificatePEM) != 0 || len(request.ServerCAPEM) != 0 || now.IsZero() ||
		ValidateApprovalScope(approval, release) != nil {
		return PRDReplacementBootstrapControllerUpdate{}, ErrInvalidInput
	}
	profile, ok := native.Config.Hosts["linux-host"]
	if !ok || profile.HostID != "linux-host" || profile.Target != replacementLinuxTarget || !profile.AllowFirstInstallPeerAbsence {
		return PRDReplacementBootstrapControllerUpdate{}, ErrInvalidInput
	}
	lookup := prdBootstrapHostKeysForTests
	if lookup == nil {
		lookup = lookupPinnedReplacementHostKeys
	}
	knownHostOutput, err := lookup("79.72.94.59", profile.KnownHostsFile)
	if err != nil || !replacementHostKeyPinned(request.SSHHostPublicKey, knownHostOutput) {
		return PRDReplacementBootstrapControllerUpdate{}, errors.Join(ErrOperationCorrupted, err)
	}
	path, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		return PRDReplacementBootstrapControllerUpdate{}, err
	}
	journal, exists, err := readPRDReplacementBootstrapCoordinatorJournal(path)
	if err != nil || !exists {
		return PRDReplacementBootstrapControllerUpdate{}, errors.Join(ErrOperationCorrupted, err)
	}
	chain, chainFound, err := readPRDReplacementBootstrapControllerUpdateChain(path, journal)
	if err != nil {
		return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("read controller amendment history: %w", err)
	}
	previousRevision, previousHostControlSHA := prdBootstrapPreviousController, journal.HostControlSHA256
	if chainFound {
		last := chain[len(chain)-1]
		previousRevision, previousHostControlSHA = last.ControllerRevision, last.HostControlSHA256
	}
	previousBackend := *native
	previousBackend.ControllerRevision = previousRevision
	previousHostSHA, err := replacementBootstrapHostControlSHA(&previousBackend, profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil || previousHostSHA != previousHostControlSHA {
		return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("controller amendment rejected: reconstructed previous host_control_sha256 does not match controller history")
	}
	currentHostSHA, err := replacementBootstrapHostControlSHA(native, profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil {
		return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("controller amendment rejected: current controller host-control identity is invalid")
	}
	if ValidateApproval(approval, release, now) != nil {
		continuationPath, pathErr := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
		if pathErr != nil || continuationFile != continuationPath {
			return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("expired bootstrap approval requires the operation's supported continuation record: %w", ErrApprovalRequired)
		}
		continuation, continuationFound, readErr := readPRDReplacementBootstrapContinuation(continuationPath)
		if readErr != nil || !continuationFound {
			return PRDReplacementBootstrapControllerUpdate{}, errors.Join(ErrApprovalRequired, readErr)
		}
		validationRevision, validationHostControlSHA := native.ControllerRevision, currentHostSHA
		if chainFound {
			validationRevision, validationHostControlSHA = previousRevision, previousHostControlSHA
		}
		if err := validatePRDReplacementBootstrapContinuation(continuation, journal, request, requestSHA, release, approval,
			approvalSHA, publisherOwner, publisherControl, validationRevision, validationHostControlSHA, store.Root, now.UTC()); err != nil {
			return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("validate bootstrap continuation: %w", err)
		}
	}
	if fields := bootstrapCoordinatorIdentityMismatches(journal, request, requestSHA, release, approvalSHA,
		approval, publisherOwner, publisherControl); len(fields) != 0 {
		return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("controller amendment rejected: coordinator fields differ: %s", strings.Join(fields, ", "))
	}
	if !prdReplacementBootstrapControllerUpdateCheckpointAllowed(journal) || !shaPattern.MatchString(native.ControllerRevision) {
		return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("controller amendment requires server_material_ready or the exact linux_namespace_ready checkpoint with no Mac route journal")
	}
	nonControllerSHA, err := prdReplacementBootstrapNonControllerInputsSHA(profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil {
		return PRDReplacementBootstrapControllerUpdate{}, err
	}
	if chainFound {
		last := chain[len(chain)-1]
		if native.ControllerRevision == last.ControllerRevision {
			if currentHostSHA != last.HostControlSHA256 || nonControllerSHA != last.NonControllerInputsSHA256 {
				return PRDReplacementBootstrapControllerUpdate{}, errors.New("controller amendment retry differs from the recorded controller or non-controller inputs")
			}
			return last, nil
		}
		if len(chain) >= prdBootstrapMaxControllerUpdates {
			return PRDReplacementBootstrapControllerUpdate{}, errors.New("controller amendment history reached its bounded limit")
		}
	}
	if currentHostSHA == previousHostSHA || native.ControllerRevision == previousRevision {
		return PRDReplacementBootstrapControllerUpdate{}, fmt.Errorf("controller amendment rejected: current controller host-control identity is invalid")
	}
	record := PRDReplacementBootstrapControllerUpdate{
		FormatVersion: prdBootstrapControllerUpdateFormat, OperationID: journal.OperationID, ReleaseID: journal.ReleaseID,
		ReleaseSHA256: journal.ReleaseSHA256, PreviousControllerRevision: previousRevision,
		PreviousHostControlSHA256: previousHostControlSHA, ControllerRevision: native.ControllerRevision,
		HostControlSHA256: currentHostSHA, NonControllerInputsSHA256: nonControllerSHA,
		CoordinatorBindingsSHA256: prdReplacementBootstrapStableBindingsSHA(journal),
		RecordedAtUTC:             now.UTC().Format(time.RFC3339Nano),
	}
	updatePath := prdReplacementBootstrapControllerUpdatePath(path)
	if chainFound {
		updatePath = prdReplacementBootstrapControllerUpdateRevisionPath(path, native.ControllerRevision)
	}
	if previous, found, readErr := readPRDReplacementBootstrapControllerUpdate(updatePath); readErr != nil {
		return PRDReplacementBootstrapControllerUpdate{}, readErr
	} else if found {
		if !samePRDReplacementBootstrapControllerUpdate(previous, record) {
			return PRDReplacementBootstrapControllerUpdate{}, errors.New("controller amendment already records different inputs")
		}
		return previous, nil
	}
	if err := validatePRDReplacementBootstrapControllerUpdate(record); err != nil {
		return PRDReplacementBootstrapControllerUpdate{}, err
	}
	if err := writeCanonicalAtomic(updatePath, record); err != nil {
		return PRDReplacementBootstrapControllerUpdate{}, err
	}
	return record, nil
}

func prdReplacementBootstrapControllerUpdateCheckpointAllowed(journal PRDReplacementBootstrapCoordinatorJournal) bool {
	if journal.CSRRequestSHA256 == "" || journal.ServerCertSHA256 == "" || journal.ServerCASHA256 == "" {
		return false
	}
	if sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:3]) {
		return journal.LinuxJournalSHA256 == "" && journal.MacJournalSHA256 == ""
	}
	return sameStrings(journal.CompletedStages, prdReplacementBootstrapStages[:4]) &&
		digestPattern.MatchString(journal.LinuxJournalSHA256) && journal.MacJournalSHA256 == ""
}

func samePRDReplacementBootstrapControllerUpdate(a, b PRDReplacementBootstrapControllerUpdate) bool {
	a.RecordedAtUTC, b.RecordedAtUTC = "", ""
	return reflect.DeepEqual(a, b)
}

func bootstrapCoordinatorIdentityMismatches(journal PRDReplacementBootstrapCoordinatorJournal, request laneinstall.PRDReplacementBootstrapRequest,
	requestSHA string, release Release, approvalSHA string, approval Approval, publisherOwner, publisherControl string) []string {
	var fields []string
	if journal.OperationID != request.OperationID {
		fields = append(fields, "operation_id")
	}
	if journal.ReleaseID != release.ID || journal.ReleaseID != request.ReleaseID {
		fields = append(fields, "release_id")
	}
	if journal.ReleaseSHA256 != release.Digest || journal.ReleaseSHA256 != request.ExpectedReleaseSHA256 {
		fields = append(fields, "release_sha256")
	}
	if journal.RequestSHA256 != requestSHA {
		fields = append(fields, "request_sha256")
	}
	if journal.ApprovalSHA256 != approvalSHA {
		fields = append(fields, "approval_sha256")
	}
	if !sameStringMap(journal.PredecessorBundles, approval.PredecessorBundleSHA256) {
		fields = append(fields, "predecessor_bundle_sha256")
	}
	if journal.BackupPackageSHA256 != approval.BackupRecoveryPackageSHA256 {
		fields = append(fields, "backup_recovery_package_sha256")
	}
	if journal.PublisherOwner != publisherOwner {
		fields = append(fields, "publisher_owner")
	}
	if journal.PublisherControl != publisherControl {
		fields = append(fields, "publisher_control")
	}
	return fields
}

func validatePRDReplacementBootstrapControllerUpdateForResume(native *NativePromotionBackend, journal PRDReplacementBootstrapCoordinatorJournal,
	storeRoot string, request laneinstall.PRDReplacementBootstrapRequest, requestSHA string, release Release, approval Approval, approvalSHA,
	publisherOwner, publisherControl, currentHostSHA string, profile HostProfile, knownHostOutput []byte) error {
	if fields := bootstrapCoordinatorIdentityMismatches(journal, request, requestSHA, release, approvalSHA,
		approval, publisherOwner, publisherControl); len(fields) != 0 {
		return fmt.Errorf("bootstrap resume identity mismatch: %s", strings.Join(fields, ", "))
	}
	updatePath, err := prdReplacementBootstrapCoordinatorJournalPath(storeRoot, journal.OperationID)
	if err != nil {
		return err
	}
	chain, found, err := readPRDReplacementBootstrapControllerUpdateChain(updatePath, journal)
	if err != nil {
		return fmt.Errorf("read controller amendment history: %w", err)
	}
	if !found {
		if journal.HostControlSHA256 != currentHostSHA {
			return fmt.Errorf("bootstrap resume identity mismatch: host_control_sha256; use the supported controller-update command")
		}
		return nil
	}
	update := chain[len(chain)-1]
	if update.ControllerRevision != native.ControllerRevision {
		return fmt.Errorf("bootstrap resume controller amendment mismatch: operation, release, controller revision, or coordinator bindings")
	}
	nonControllerSHA, err := prdReplacementBootstrapNonControllerInputsSHA(profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil || nonControllerSHA != update.NonControllerInputsSHA256 {
		return fmt.Errorf("bootstrap resume controller amendment mismatch: host profile, SSH identity, or trust inputs")
	}
	if update.HostControlSHA256 != currentHostSHA {
		return fmt.Errorf("bootstrap resume controller amendment mismatch: host_control_sha256")
	}
	return nil
}

func runPRDReplacementBootstrapControllerUpdateCLI(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-release prd-bootstrap-controller-update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operationID := flags.String("operation", "", "the one authorized replacement bootstrap operation")
	requestFile := flags.String("request-file", "", "owner-only original bootstrap request JSON")
	approvalFile := flags.String("approval-file", "", "same release-bound PRD approval used to create the coordinator journal")
	continuationFile := flags.String("continuation-file", "", "append-only one-time continuation for the exact expired bootstrap approval")
	hostsFile := flags.String("hosts-file", "", "owner-only host control configuration")
	storeRoot := flags.String("store-root", "", "existing release workflow root")
	publisherOwner := flags.String("publisher-owner", "", "same confirmed PRD publisher-pause owner")
	publisherControl := flags.String("publisher-control", "", "same specific PRD publisher-pause control")
	publishersPaused := flags.Bool("publishers-paused", false, "confirm PRD publishers remain paused")
	noNewAccepted := flags.Bool("no-new-accepted", false, "confirm no new PRD work was accepted")
	jsonOutput := flags.Bool("json", false, "emit the sanitized amendment record")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *operationID != prdBootstrapControllerUpdateOperation || *requestFile == "" || *approvalFile == "" ||
		strings.TrimSpace(*publisherOwner) == "" || strings.TrimSpace(*publisherControl) == "" || !*publishersPaused || !*noNewAccepted {
		fmt.Fprintln(stderr, "usage: runner-release prd-bootstrap-controller-update --operation 02bfdc603d2ea3360ecd32501fe347d1 --request-file OWNER-ONLY-JSON --approval-file FILE --publisher-owner OWNER --publisher-control CONTROL --publishers-paused --no-new-accepted [--continuation-file FILE] [--hosts-file FILE] [--store-root DIR] [--json]")
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
	dev, err := store.LoadAcceptance("dev", release.ID)
	if err != nil || !acceptanceMatchesRelease(dev, "dev", release) {
		fmt.Fprintln(stderr, "controller update requires the exact passed Dev acceptance")
		return 3
	}
	qas, err := store.LoadAcceptance("qas", release.ID)
	if err != nil || !acceptanceMatchesRelease(qas, "qas", release) || qas.Profile != dev.Profile {
		fmt.Fprintln(stderr, "controller update requires matching QAS acceptance")
		return 3
	}
	approval, approvalSHA, err := readApprovalFile(*approvalFile)
	if err != nil || ValidateApprovalScope(approval, release) != nil {
		fmt.Fprintln(stderr, ErrApprovalRequired)
		return 3
	}
	backend := promotionBackendForTests
	if backend == nil {
		native, loadErr := loadNativePromotionBackend(*hostsFile, *storeRoot, "", "", false, false, defaultPromotionOperatingBuffer)
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
	request.PublisherOwner = strings.TrimSpace(*publisherOwner)
	request.PublisherControl = strings.TrimSpace(*publisherControl)
	request.PublishersPaused = *publishersPaused
	request.NoNewAccepted = *noNewAccepted
	var record PRDReplacementBootstrapControllerUpdate
	err = store.WithLaneLock("prd", func() error {
		var updateErr error
		record, updateErr = createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
			approval, approvalSHA, strings.TrimSpace(*publisherOwner), strings.TrimSpace(*publisherControl), *continuationFile, time.Now().UTC())
		if updateErr != nil {
			return fmt.Errorf("record PRD bootstrap controller amendment: %w", updateErr)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeCLIResult(stdout, stderr, record, *jsonOutput)
}
