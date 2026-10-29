package releaseflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

const prdReplacementCoordinatorJournalVersion = 1

var prdReplacementBootstrapStages = []string{
	"prepared",
	"server_csr_ready",
	"server_material_ready",
	"linux_namespace_ready",
	"mac_route_complete",
}

// PRDReplacementBootstrapCoordinatorJournal is the Mac-side durable intent
// written before the command asks the replacement host to create key material.
// It contains hashes and stage markers only; all credentials remain in the
// owner-only request, host key store, or lane-specific destinations.
type PRDReplacementBootstrapCoordinatorJournal struct {
	FormatVersion       int               `json:"format_version"`
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
	CreatedAtUTC        string            `json:"created_at_utc"`
	UpdatedAtUTC        string            `json:"updated_at_utc"`
	CompletedStages     []string          `json:"completed_stages"`
	CSRRequestSHA256    string            `json:"csr_request_sha256,omitempty"`
	ServerCertSHA256    string            `json:"server_certificate_sha256,omitempty"`
	ServerCASHA256      string            `json:"server_ca_sha256,omitempty"`
	LinuxJournalSHA256  string            `json:"linux_journal_sha256,omitempty"`
	MacJournalSHA256    string            `json:"mac_journal_sha256,omitempty"`
}

func prdReplacementBootstrapCoordinatorJournalPath(storeRoot, operationID string) (string, error) {
	if !absoluteClean(storeRoot) || !operationPattern.MatchString(operationID) {
		return "", ErrInvalidInput
	}
	directory := filepath.Join(storeRoot, "operations", "prd-replacement-bootstrap")
	if err := ensurePrivatePath(directory); err != nil {
		return "", err
	}
	if err := validateOwnedPrivateDirectory(directory); err != nil {
		return "", err
	}
	return filepath.Join(directory, operationID+".json"), nil
}

func validateOwnedPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	stat, ok := func() (*syscall.Stat_t, bool) {
		if info == nil {
			return nil, false
		}
		value, ok := info.Sys().(*syscall.Stat_t)
		return value, ok
	}()
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 ||
		!ok || int(stat.Uid) != os.Getuid() {
		return ErrOperationCorrupted
	}
	return nil
}

func readPRDReplacementBootstrapCoordinatorJournal(path string) (PRDReplacementBootstrapCoordinatorJournal, bool, error) {
	var journal PRDReplacementBootstrapCoordinatorJournal
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return journal, false, ErrOperationCorrupted
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return journal, false, ErrOperationCorrupted
	}
	if err := readStrictJSON(path, &journal); err != nil || validatePRDReplacementBootstrapCoordinatorJournal(journal) != nil {
		return PRDReplacementBootstrapCoordinatorJournal{}, false, ErrOperationCorrupted
	}
	data, err := os.ReadFile(path)
	canonical, marshalErr := json.MarshalIndent(journal, "", "  ")
	if err != nil || marshalErr != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return PRDReplacementBootstrapCoordinatorJournal{}, false, ErrOperationCorrupted
	}
	return journal, true, nil
}

func writePRDReplacementBootstrapCoordinatorJournal(path string, journal PRDReplacementBootstrapCoordinatorJournal) error {
	if validatePRDReplacementBootstrapCoordinatorJournal(journal) != nil {
		return ErrOperationCorrupted
	}
	return writeCanonicalAtomic(path, journal)
}

func validatePRDReplacementBootstrapCoordinatorJournal(journal PRDReplacementBootstrapCoordinatorJournal) error {
	if journal.FormatVersion != prdReplacementCoordinatorJournalVersion || !operationPattern.MatchString(journal.OperationID) ||
		!releasePattern.MatchString(journal.ReleaseID) || !digestPattern.MatchString(journal.ReleaseSHA256) ||
		!digestPattern.MatchString(journal.RequestSHA256) || !digestPattern.MatchString(journal.ApprovalSHA256) ||
		!digestPattern.MatchString(journal.PlanSHA256) || !digestPattern.MatchString(journal.HostControlSHA256) ||
		!digestPattern.MatchString(journal.BackupPackageSHA256) ||
		strings.TrimSpace(journal.PublisherOwner) == "" || strings.TrimSpace(journal.PublisherControl) == "" ||
		len(journal.PredecessorBundles) != len(HostOrder) || len(journal.CompletedStages) == 0 ||
		journal.CompletedStages[0] != prdReplacementBootstrapStages[0] ||
		!validPRDReplacementBootstrapTimestamp(journal.CreatedAtUTC) || !validPRDReplacementBootstrapTimestamp(journal.UpdatedAtUTC) {
		return ErrOperationCorrupted
	}
	created, _ := time.Parse(time.RFC3339Nano, journal.CreatedAtUTC)
	updated, _ := time.Parse(time.RFC3339Nano, journal.UpdatedAtUTC)
	if updated.Before(created) || updated.Sub(created) > 365*24*time.Hour {
		return ErrOperationCorrupted
	}
	for host, hash := range journal.PredecessorBundles {
		if !contains(HostOrder, host) || !digestPattern.MatchString(hash) {
			return ErrOperationCorrupted
		}
	}
	if len(journal.CompletedStages) > len(prdReplacementBootstrapStages) {
		return ErrOperationCorrupted
	}
	for index, stage := range journal.CompletedStages {
		if stage != prdReplacementBootstrapStages[index] {
			return ErrOperationCorrupted
		}
	}
	for _, value := range []string{journal.CSRRequestSHA256, journal.ServerCertSHA256, journal.ServerCASHA256,
		journal.LinuxJournalSHA256, journal.MacJournalSHA256} {
		if value != "" && !digestPattern.MatchString(value) {
			return ErrOperationCorrupted
		}
	}
	if len(journal.CompletedStages) >= 2 && journal.CSRRequestSHA256 == "" ||
		len(journal.CompletedStages) >= 3 && (journal.ServerCertSHA256 == "" || journal.ServerCASHA256 == "") ||
		len(journal.CompletedStages) >= 4 && journal.LinuxJournalSHA256 == "" ||
		len(journal.CompletedStages) >= 5 && journal.MacJournalSHA256 == "" {
		return ErrOperationCorrupted
	}
	return nil
}

func validPRDReplacementBootstrapTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero() && parsed.UTC().Format(time.RFC3339Nano) == value
}

func samePRDReplacementBootstrapJournalIdentity(journal PRDReplacementBootstrapCoordinatorJournal, operationID, releaseID,
	releaseSHA, requestSHA, approvalSHA, planSHA, hostControlSHA, backupSHA, owner, control string, predecessor map[string]string) bool {
	return journal.OperationID == operationID && journal.ReleaseID == releaseID && journal.ReleaseSHA256 == releaseSHA &&
		journal.RequestSHA256 == requestSHA && journal.ApprovalSHA256 == approvalSHA && journal.PlanSHA256 == planSHA &&
		journal.HostControlSHA256 == hostControlSHA &&
		journal.BackupPackageSHA256 == backupSHA && journal.PublisherOwner == owner && journal.PublisherControl == control &&
		reflect.DeepEqual(journal.PredecessorBundles, predecessor)
}

func appendPRDReplacementBootstrapStage(journal *PRDReplacementBootstrapCoordinatorJournal, stage string, now time.Time) error {
	if journal == nil || now.IsZero() || validatePRDReplacementBootstrapCoordinatorJournal(*journal) != nil ||
		len(journal.CompletedStages) >= len(prdReplacementBootstrapStages) ||
		prdReplacementBootstrapStages[len(journal.CompletedStages)] != stage {
		return ErrOperationCorrupted
	}
	journal.CompletedStages = append(journal.CompletedStages, stage)
	journal.UpdatedAtUTC = now.UTC().Format(time.RFC3339Nano)
	return validatePRDReplacementBootstrapCoordinatorJournal(*journal)
}
