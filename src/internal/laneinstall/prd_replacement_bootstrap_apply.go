package laneinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const prdReplacementBootstrapJournalVersion = 1

var prdReplacementBootstrapSteps = []string{"directories", "configuration", "authorized_key", "database", "fresh_baseline"}

// PRDReplacementBootstrapJournal is durable, non-secret evidence of one
// replacement-host first installation. It deliberately contains hashes and
// public absence evidence only; private key and certificate bytes are never
// copied into it.
type PRDReplacementBootstrapJournal struct {
	FormatVersion                int                           `json:"format_version"`
	OperationID                  string                        `json:"operation_id"`
	RequestSHA256                string                        `json:"request_sha256"`
	InputSHA256                  string                        `json:"input_sha256"`
	ApprovalSHA256               string                        `json:"approval_sha256"`
	PublisherOwner               string                        `json:"publisher_owner"`
	PublisherControl             string                        `json:"publisher_control"`
	PublishersPaused             bool                          `json:"publishers_paused"`
	NoNewAccepted                bool                          `json:"no_new_accepted"`
	Release                      CatalogReleaseIdentity        `json:"release"`
	Absence                      CatalogNamespaceAbsenceReport `json:"absence"`
	ConfigurationSHA256          string                        `json:"configuration_sha256"`
	MacCandidateSHA256           string                        `json:"mac_candidate_sha256"`
	CreatedAtUTC                 string                        `json:"created_at_utc"`
	UpdatedAtUTC                 string                        `json:"updated_at_utc"`
	CompletedSteps               []string                      `json:"completed_steps"`
	DatabaseSchema               int                           `json:"database_schema,omitempty"`
	WorkInventorySHA256          string                        `json:"work_inventory_sha256,omitempty"`
	AuthorizedKeysBefore         string                        `json:"authorized_keys_before_sha256,omitempty"`
	AuthorizedKeysAfter          string                        `json:"authorized_keys_after_sha256,omitempty"`
	AuthorizedKeysBeforeContents []byte                        `json:"authorized_keys_before_contents,omitempty"`
	AuthorizedKeysExisted        bool                          `json:"authorized_keys_existed,omitempty"`
	AuthorizedKeysMode           uint32                        `json:"authorized_keys_mode,omitempty"`
	DatabaseStagingPath          string                        `json:"database_staging_path,omitempty"`
	DatabaseSHA256               string                        `json:"database_sha256,omitempty"`
}

type PRDReplacementBootstrapResult struct {
	OperationID         string                        `json:"operation_id"`
	Stage               string                        `json:"stage"`
	Release             CatalogReleaseIdentity        `json:"release"`
	ConfigurationSHA256 string                        `json:"configuration_sha256"`
	MacCandidateSHA256  string                        `json:"mac_candidate_sha256"`
	DatabaseSchema      int                           `json:"database_schema"`
	WorkInventorySHA256 string                        `json:"work_inventory_sha256"`
	JournalSHA256       string                        `json:"journal_sha256"`
	MacRoute            *PRDReplacementMacRouteResult `json:"mac_route,omitempty"`
}

// prdReplacementBootstrapBackend separates durable progression from its
// fixed host operations. Production uses the canonical Linux PRD namespace;
// tests inject an in-memory backend and cannot select it from the CLI.
type prdReplacementBootstrapBackend interface {
	CaptureAbsence(context.Context) (CatalogNamespaceAbsenceReport, error)
	BuildPlan(PRDReplacementBootstrapRequest, CatalogNamespaceAbsenceReport, time.Time) (PRDReplacementBootstrapPlan, error)
	AcquireOperationLock(context.Context, PRDReplacementBootstrapPlan) (func(), error)
	ApplyStep(context.Context, PRDReplacementBootstrapPlan, string, *PRDReplacementBootstrapJournal) error
	VerifyComplete(context.Context, PRDReplacementBootstrapPlan, *PRDReplacementBootstrapJournal) error
}

// RunPRDReplacementBootstrap persists the operation before the first namespace
// mutation, then advances a fixed set of idempotent steps. Repeating an
// operation ID is allowed only with the identical request. Existing partial
// state is resumed only by the matching journal and backend guards.
func RunPRDReplacementBootstrap(ctx context.Context, request PRDReplacementBootstrapRequest,
	now time.Time) (PRDReplacementBootstrapResult, error) {
	journalRoot, err := PRDReplacementBootstrapJournalRoot()
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	backend, err := newLocalPRDReplacementBootstrapBackend()
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	return runPRDReplacementBootstrapAtRoot(ctx, request, journalRoot, backend, now)
}

func runPRDReplacementBootstrapAtRoot(ctx context.Context, request PRDReplacementBootstrapRequest, journalRoot string,
	backend prdReplacementBootstrapBackend, now time.Time) (PRDReplacementBootstrapResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if backend == nil || now.IsZero() || !filepath.IsAbs(journalRoot) || filepath.Clean(journalRoot) != journalRoot {
		return PRDReplacementBootstrapResult{}, ErrCatalogOperationInvalid
	}
	if err := validatePRDReplacementBootstrapRequest(request); err != nil {
		return PRDReplacementBootstrapResult{}, fmt.Errorf("validate replacement PRD bootstrap request: %w", err)
	}
	requestSHA, err := prdReplacementBootstrapRequestSHA(request)
	if err != nil {
		return PRDReplacementBootstrapResult{}, fmt.Errorf("bind replacement PRD bootstrap request: %w", err)
	}
	if err := ensureBootstrapJournalRootAtRoot(journalRoot); err != nil {
		return PRDReplacementBootstrapResult{}, fmt.Errorf("prepare replacement PRD bootstrap journal root: %w", err)
	}
	cohortLock, err := acquireBootstrapLock(filepath.Join(journalRoot, "linux-host-prd-bootstrap.lock"))
	if err != nil {
		return PRDReplacementBootstrapResult{}, fmt.Errorf("acquire replacement PRD bootstrap cohort lock: %w", err)
	}
	defer releaseBootstrapLock(cohortLock)
	lockPath := filepath.Join(journalRoot, request.OperationID+".lock")
	lock, err := acquireBootstrapLock(lockPath)
	if err != nil {
		return PRDReplacementBootstrapResult{}, fmt.Errorf("acquire replacement PRD bootstrap operation lock: %w", err)
	}
	defer releaseBootstrapLock(lock)

	journalPath := filepath.Join(journalRoot, request.OperationID+".json")
	journal, exists, err := readBootstrapJournal(journalPath)
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	if exists {
		if journal.OperationID != request.OperationID || journal.RequestSHA256 != requestSHA {
			return PRDReplacementBootstrapResult{}, ErrCatalogOperationInvalid
		}
		createdAt, parseErr := time.Parse(time.RFC3339Nano, journal.CreatedAtUTC)
		observedAt, absenceErr := time.Parse(time.RFC3339Nano, journal.Absence.ObservedAtUTC)
		if parseErr != nil || absenceErr != nil || createdAt.Before(observedAt) || createdAt.Sub(observedAt) > prdReplacementBootstrapEvidenceMaxAge {
			return PRDReplacementBootstrapResult{}, ErrCatalogOperationInvalid
		}
		plan, planErr := backend.BuildPlan(request, journal.Absence, createdAt)
		if planErr != nil || !bootstrapJournalMatchesPlan(journal, plan) {
			return PRDReplacementBootstrapResult{}, errors.Join(ErrCatalogOperationInvalid, planErr)
		}
		return runBootstrapSteps(ctx, journalPath, journal, plan, backend, now)
	}

	absence, err := backend.CaptureAbsence(ctx)
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	observedAt, err := time.Parse(time.RFC3339Nano, absence.ObservedAtUTC)
	if err != nil || now.Before(observedAt) || now.Sub(observedAt) > prdReplacementBootstrapEvidenceMaxAge || absence.State != "absent" {
		return PRDReplacementBootstrapResult{}, ErrCatalogOperationInvalid
	}
	plan, err := backend.BuildPlan(request, absence, now)
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	journal = PRDReplacementBootstrapJournal{
		FormatVersion: prdReplacementBootstrapJournalVersion, OperationID: request.OperationID,
		RequestSHA256: requestSHA, InputSHA256: plan.InputSHA256, ApprovalSHA256: request.ApprovalSHA256,
		PublisherOwner: request.PublisherOwner, PublisherControl: request.PublisherControl,
		PublishersPaused: request.PublishersPaused, NoNewAccepted: request.NoNewAccepted,
		Release: plan.Release, Absence: absence,
		ConfigurationSHA256: plan.ConfigurationSHA256, MacCandidateSHA256: plan.MacCandidateSHA256,
		CreatedAtUTC: now.UTC().Format(time.RFC3339Nano), UpdatedAtUTC: now.UTC().Format(time.RFC3339Nano),
		CompletedSteps: []string{},
	}
	if err := writeBootstrapJournal(journalPath, journal); err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	return runBootstrapSteps(ctx, journalPath, journal, plan, backend, now)
}

func runBootstrapSteps(ctx context.Context, journalPath string, journal PRDReplacementBootstrapJournal,
	plan PRDReplacementBootstrapPlan, backend prdReplacementBootstrapBackend, now time.Time) (PRDReplacementBootstrapResult, error) {
	completed := make(map[string]bool, len(journal.CompletedSteps))
	for index, step := range journal.CompletedSteps {
		if index >= len(prdReplacementBootstrapSteps) || prdReplacementBootstrapSteps[index] != step || completed[step] {
			return PRDReplacementBootstrapResult{}, ErrCatalogOperationInvalid
		}
		completed[step] = true
	}
	var unlockOperation func()
	defer func() {
		if unlockOperation != nil {
			unlockOperation()
		}
	}()
	unlockOperation, err := backend.AcquireOperationLock(ctx, plan)
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	if unlockOperation == nil {
		return PRDReplacementBootstrapResult{}, ErrCatalogOperationInvalid
	}
	for _, step := range prdReplacementBootstrapSteps {
		if completed[step] {
			continue
		}
		if preparer, ok := backend.(interface {
			PrepareStep(context.Context, PRDReplacementBootstrapPlan, string, *PRDReplacementBootstrapJournal) (bool, error)
		}); ok {
			changed, err := preparer.PrepareStep(ctx, plan, step, &journal)
			if err != nil {
				return PRDReplacementBootstrapResult{}, fmt.Errorf("prepare replacement PRD bootstrap step %s: %w", step, err)
			}
			if changed {
				journal.UpdatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
				if err := writeBootstrapJournal(journalPath, journal); err != nil {
					return PRDReplacementBootstrapResult{}, err
				}
			}
		}
		if err := backend.ApplyStep(ctx, plan, step, &journal); err != nil {
			return PRDReplacementBootstrapResult{}, fmt.Errorf("replacement PRD bootstrap step %s: %w", step, err)
		}
		journal.CompletedSteps = append(journal.CompletedSteps, step)
		journal.UpdatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
		if err := writeBootstrapJournal(journalPath, journal); err != nil {
			return PRDReplacementBootstrapResult{}, err
		}
	}
	if err := backend.VerifyComplete(ctx, plan, &journal); err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	journal.UpdatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeBootstrapJournal(journalPath, journal); err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		return PRDReplacementBootstrapResult{}, err
	}
	return PRDReplacementBootstrapResult{OperationID: journal.OperationID, Stage: "linux_namespace_ready",
		Release: journal.Release, ConfigurationSHA256: journal.ConfigurationSHA256,
		MacCandidateSHA256: journal.MacCandidateSHA256, DatabaseSchema: journal.DatabaseSchema,
		WorkInventorySHA256: journal.WorkInventorySHA256, JournalSHA256: digestBytes(journalBytes)}, nil
}

func bootstrapJournalMatchesPlan(journal PRDReplacementBootstrapJournal, plan PRDReplacementBootstrapPlan) bool {
	return journal.FormatVersion == prdReplacementBootstrapJournalVersion && journal.InputSHA256 == plan.InputSHA256 &&
		journal.Release == plan.Release && journal.Absence.EvidenceSHA256 == plan.AbsenceEvidenceSHA256 &&
		journal.ConfigurationSHA256 == plan.ConfigurationSHA256 && journal.MacCandidateSHA256 == plan.MacCandidateSHA256
}

func prdReplacementBootstrapRequestSHA(request PRDReplacementBootstrapRequest) (string, error) {
	if validatePRDReplacementBootstrapRequest(request) != nil {
		return "", ErrCatalogOperationInvalid
	}
	if len(request.TemplateConfiguration)+len(request.MacConfiguration)+len(request.ServerCertificatePEM)+
		len(request.ServerCAPEM)+len(request.ClientCertificatePEM)+
		len(request.ClientCAPEM)+len(request.ClientPrincipalMap)+len(request.DispatcherPublicKey)+len(request.SSHHostPublicKey)+
		len(request.PublisherOwner)+len(request.PublisherControl) > 3<<20 {
		return "", ErrInvalidLaneTLSMaterial
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", ErrCatalogOperationInvalid
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validatePRDReplacementBootstrapRequest(request PRDReplacementBootstrapRequest) error {
	if !validCatalogOperationID(request.OperationID) || (request.TemplateHostID != "sandbox-host" && request.TemplateHostID != "factory-sandbox") ||
		!validCatalogReleaseID(request.ReleaseID) || !bundleDigestPattern.MatchString(request.ExpectedReleaseSHA256) ||
		!bundleDigestPattern.MatchString(request.ServerCSR256) ||
		!bundleDigestPattern.MatchString(request.ApprovalSHA256) || strings.TrimSpace(request.PublisherOwner) == "" ||
		len(request.PublisherOwner) > 256 || strings.TrimSpace(request.PublisherControl) == "" ||
		len(request.PublisherControl) > 512 || !request.PublishersPaused || !request.NoNewAccepted {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func readBootstrapJournal(path string) (PRDReplacementBootstrapJournal, bool, error) {
	var journal PRDReplacementBootstrapJournal
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, false, nil
	}
	var stat unix.Stat_t
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > 2<<20 || unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return journal, false, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return journal, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&journal) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return PRDReplacementBootstrapJournal{}, false, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(journal, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) || journal.FormatVersion != prdReplacementBootstrapJournalVersion ||
		journal.OperationID == "" || !bundleDigestPattern.MatchString(journal.RequestSHA256) ||
		!bundleDigestPattern.MatchString(journal.InputSHA256) || !bundleDigestPattern.MatchString(journal.ApprovalSHA256) ||
		strings.TrimSpace(journal.PublisherOwner) == "" || len(journal.PublisherOwner) > 256 ||
		strings.TrimSpace(journal.PublisherControl) == "" || len(journal.PublisherControl) > 512 ||
		!journal.PublishersPaused || !journal.NoNewAccepted {
		return PRDReplacementBootstrapJournal{}, false, ErrCatalogOperationInvalid
	}
	return journal, true, nil
}

func writeBootstrapJournal(path string, journal PRDReplacementBootstrapJournal) error {
	if journal.FormatVersion != prdReplacementBootstrapJournalVersion || !validCatalogOperationID(journal.OperationID) ||
		!bundleDigestPattern.MatchString(journal.RequestSHA256) || !bundleDigestPattern.MatchString(journal.InputSHA256) ||
		!bundleDigestPattern.MatchString(journal.ApprovalSHA256) || strings.TrimSpace(journal.PublisherOwner) == "" ||
		len(journal.PublisherOwner) > 256 || strings.TrimSpace(journal.PublisherControl) == "" ||
		len(journal.PublisherControl) > 512 || !journal.PublishersPaused || !journal.NoNewAccepted {
		return ErrCatalogOperationInvalid
	}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > 2<<20 {
		return ErrCatalogOperationInvalid
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bootstrap-journal-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func ensureBootstrapJournalRootAtRoot(path string) error {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return fmt.Errorf("replacement bootstrap home path is invalid: %w", ErrCatalogOperationInvalid)
	}
	productionRoot, err := PRDReplacementBootstrapJournalRoot()
	if err != nil {
		return fmt.Errorf("replacement bootstrap journal root is invalid: %w", ErrCatalogOperationInvalid)
	}
	anchor := home
	if path != productionRoot {
		// This unexported branch is used only by package fixtures. Production
		// callers use the exact canonical path and validate every home ancestor.
		// Fixtures remain confined to the platform temp root and validate their
		// private owner-controlled parent, even when TMPDIR itself is under home.
		tempRoot := filepath.Clean(os.TempDir())
		if !filepath.IsAbs(tempRoot) || !pathContained(tempRoot, path) {
			return fmt.Errorf("noncanonical bootstrap journal root is outside the test temp root: %w", ErrCatalogOperationInvalid)
		}
		anchor = filepath.Dir(path)
	}
	if err := ensureOwnerDirectoryChainFrom(anchor, filepath.Dir(path)); err != nil {
		return fmt.Errorf("replacement bootstrap journal parent is unsafe: %w", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("replacement bootstrap journal directory could not be created")
	}
	var info unix.Stat_t
	if unix.Lstat(path, &info) != nil {
		return fmt.Errorf("replacement bootstrap journal directory metadata is unavailable: %w", ErrCatalogOperationInvalid)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Uid != uint32(os.Getuid()) || info.Mode&0o777 != 0o700 {
		return fmt.Errorf("replacement bootstrap journal directory has unexpected owner, type, or permissions: %w", ErrCatalogOperationInvalid)
	}
	return nil
}

func ensureOwnerDirectoryChainFrom(home, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !pathContained(home, path) {
		return fmt.Errorf("owner directory-chain bounds are invalid: %w", ErrCatalogOperationInvalid)
	}
	var homeStat unix.Stat_t
	if unix.Lstat(home, &homeStat) != nil {
		return fmt.Errorf("owner directory-chain root metadata is unavailable: %w", ErrCatalogOperationInvalid)
	}
	if homeStat.Mode&unix.S_IFMT != unix.S_IFDIR || homeStat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("owner directory-chain root is not a current-user directory: %w", ErrCatalogOperationInvalid)
	}
	if filepath.Clean(home) == filepath.Clean(path) {
		if homeStat.Mode&0o022 != 0 {
			return fmt.Errorf("owner directory-chain root has unsafe permissions (%04o): %w", homeStat.Mode&0o777, ErrCatalogOperationInvalid)
		}
		return nil
	}
	current := home
	relative, _ := filepath.Rel(home, path)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return errors.New("owner directory-chain component could not be created")
			}
			info, err = os.Lstat(current)
		}
		var stat unix.Stat_t
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || unix.Lstat(current, &stat) != nil {
			return fmt.Errorf("owner directory-chain component is not a real directory: %w", ErrCatalogOperationInvalid)
		}
		if stat.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("owner directory-chain component is owned by another user: %w", ErrCatalogOperationInvalid)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("owner directory-chain component is group or world writable: %w", ErrCatalogOperationInvalid)
		}
	}
	return nil
}

func acquireBootstrapLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 {
		_ = file.Close()
		return nil, ErrCatalogOperationInvalid
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, ErrCatalogOperationInvalid
	}
	return file, nil
}

func releaseBootstrapLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

// newLocalPRDReplacementBootstrapBackend binds the supported first install to
// the replacement Linux worker. It has no caller-controlled namespace root.
func newLocalPRDReplacementBootstrapBackend() (prdReplacementBootstrapBackend, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, ErrInvalidPRDUpgradePlan
	}
	current, err := user.Current()
	if err != nil || current.Username != config.LinuxAccount || !replacementLinuxLocalIdentityMatches() {
		return nil, ErrInvalidPRDUpgradePlan
	}
	return localPRDReplacementBootstrapBackend{}, nil
}

type localPRDReplacementBootstrapBackend struct{}

func (localPRDReplacementBootstrapBackend) AcquireOperationLock(_ context.Context, plan PRDReplacementBootstrapPlan) (func(), error) {
	fd, err := acquirePRDUpgradeLock(planToUpgradePlan(plan))
	if err != nil {
		return nil, err
	}
	return func() { releasePRDUpgradeLock(fd) }, nil
}

func (localPRDReplacementBootstrapBackend) CaptureAbsence(ctx context.Context) (CatalogNamespaceAbsenceReport, error) {
	return ReadCatalogPeerNamespaceAbsence(ctx, "linux-host", config.ReleaseLanePRD, config.ReleaseLanePRD, NewCatalogPRDAbsenceProbe())
}

func (localPRDReplacementBootstrapBackend) BuildPlan(request PRDReplacementBootstrapRequest,
	absence CatalogNamespaceAbsenceReport, now time.Time) (PRDReplacementBootstrapPlan, error) {
	upgrade, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		return PRDReplacementBootstrapPlan{}, err
	}
	root, err := DefaultCatalogRootForPlan(Plan{HostID: "linux-host", Lane: config.ReleaseLanePRD, Namespace: upgrade.Namespace})
	if err != nil {
		return PRDReplacementBootstrapPlan{}, err
	}
	releasePath := filepath.Join(root, request.ReleaseID)
	status, err := InspectCatalogRelease(root, "linux-host", request.ReleaseID)
	if err != nil || status.Identity.ReleaseSHA256 != request.ExpectedReleaseSHA256 {
		return PRDReplacementBootstrapPlan{}, ErrInvalidCatalogRelease
	}
	manifestBytes, err := os.ReadFile(filepath.Join(releasePath, "release.json"))
	if err != nil {
		return PRDReplacementBootstrapPlan{}, ErrInvalidCatalogRelease
	}
	manifest, err := decodeCatalogReleaseManifest(manifestBytes)
	if err != nil {
		return PRDReplacementBootstrapPlan{}, err
	}
	bundle, err := readSimpleBundleAtWithModes(filepath.Join(releasePath, "linux-host"), 0o500, 0o400, 0o500, 0o500)
	if err != nil {
		return PRDReplacementBootstrapPlan{}, err
	}
	serverCSRPEM, serverPrivateKeyPEM, err := loadPRDReplacementServerKey(request.OperationID, request.ServerCSR256)
	if err != nil {
		return PRDReplacementBootstrapPlan{}, err
	}
	input := PRDReplacementBootstrapInput{OperationID: request.OperationID, TemplateHostID: request.TemplateHostID,
		TemplateConfiguration: request.TemplateConfiguration, MacConfiguration: request.MacConfiguration,
		Absence: absence, ReleaseManifest: manifest, Bundle: bundle, ExpectedReleaseSHA256: request.ExpectedReleaseSHA256,
		ServerCertificatePEM: request.ServerCertificatePEM, ServerPrivateKeyPEM: serverPrivateKeyPEM,
		ServerCSRPEM: serverCSRPEM,
		ServerCAPEM:  request.ServerCAPEM, ClientCertificatePEM: request.ClientCertificatePEM,
		ClientCAPEM: request.ClientCAPEM, ClientPrincipalMap: request.ClientPrincipalMap,
		DispatcherPublicKey: request.DispatcherPublicKey, SSHHostPublicKey: request.SSHHostPublicKey, Now: now}
	return BuildPRDReplacementBootstrapPlan(input)
}

func (localPRDReplacementBootstrapBackend) PrepareStep(ctx context.Context, plan PRDReplacementBootstrapPlan,
	step string, journal *PRDReplacementBootstrapJournal) (bool, error) {
	if step == "database" {
		if journal.DatabaseSHA256 != "" {
			return false, verifyPRDReplacementDatabasePrepared(ctx, plan, *journal)
		}
		if err := preparePRDReplacementDatabase(ctx, plan, journal); err != nil {
			return false, err
		}
		return true, nil
	}
	if step != "authorized_key" || journal.AuthorizedKeysBefore != "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path := plan.Namespace.SSHAuthorityPath
	info, err := os.Lstat(path)
	var before []byte
	mode := os.FileMode(0o600)
	existed := err == nil
	if existed {
		var stat unix.Stat_t
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || unix.Lstat(path, &stat) != nil ||
			stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Mode().Perm()&0o022 != 0 || info.Size() > 128<<10 {
			return false, ErrCatalogOperationInvalid
		}
		mode = info.Mode().Perm()
		before, err = os.ReadFile(path)
		if err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	after := append([]byte(nil), before...)
	if !bootstrapHasExactLine(after, []byte(plan.AuthorizedKeyLine)) {
		if len(after) > 0 && after[len(after)-1] != '\n' {
			after = append(after, '\n')
		}
		after = append(after, []byte(plan.AuthorizedKeyLine)...)
	}
	journal.AuthorizedKeysBefore = digestBytes(before)
	journal.AuthorizedKeysAfter = digestBytes(after)
	journal.AuthorizedKeysBeforeContents = append([]byte(nil), before...)
	journal.AuthorizedKeysExisted, journal.AuthorizedKeysMode = existed, uint32(mode)
	return true, nil
}

func (localPRDReplacementBootstrapBackend) ApplyStep(ctx context.Context, plan PRDReplacementBootstrapPlan,
	step string, journal *PRDReplacementBootstrapJournal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch step {
	case "directories":
		return createPRDReplacementBootstrapDirectories(plan)
	case "configuration":
		files, err := PRDReplacementBootstrapFiles(plan)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err := installBootstrapFile(file.InstallPath, file.Contents, os.FileMode(file.Mode)); err != nil {
				return err
			}
		}
		return nil
	case "authorized_key":
		return installPRDReplacementAuthorizedKey(plan, journal)
	case "database":
		return installPreparedPRDReplacementDatabase(ctx, plan, *journal)
	case "fresh_baseline":
		database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
		if err != nil {
			return err
		}
		schema, work, inventoryErr := readPRDWorkSnapshot(ctx, database)
		closeErr := database.Close()
		if err := errors.Join(inventoryErr, closeErr); err != nil || schema != store.CurrentSchemaVersion || !work.Empty() {
			return errors.Join(ErrPRDUpgradeGuard, err)
		}
		baseline, err := NewCatalogFreshInstallBaseline(plan.OperationID, plan.Absence, plan.ConfigurationSHA256,
			schema, work, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := writeCatalogFreshInstallBaselineMustMatch(plan, baseline); err != nil {
			return err
		}
		journal.DatabaseSchema, journal.WorkInventorySHA256 = schema, work.SHA256()
		return nil
	default:
		return ErrCatalogOperationInvalid
	}
}

func (localPRDReplacementBootstrapBackend) VerifyComplete(ctx context.Context, plan PRDReplacementBootstrapPlan,
	journal *PRDReplacementBootstrapJournal) error {
	if len(journal.CompletedSteps) != len(prdReplacementBootstrapSteps) || journal.DatabaseSchema != store.CurrentSchemaVersion ||
		!bundleDigestPattern.MatchString(journal.WorkInventorySHA256) {
		return ErrCatalogOperationInvalid
	}
	if _, fresh, err := currentFreshInstallBaseline(planToUpgradePlan(plan)); err != nil || !fresh {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, plan.Namespace.Database)
	if err != nil {
		return err
	}
	schema, work, inventoryErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil || schema != journal.DatabaseSchema || !work.Empty() || work.SHA256() != journal.WorkInventorySHA256 {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	return nil
}

func planToUpgradePlan(plan PRDReplacementBootstrapPlan) PRDUpgradePlan {
	return PRDUpgradePlan{HostID: "linux-host", Namespace: plan.Namespace}
}

func createPRDReplacementBootstrapDirectories(plan PRDReplacementBootstrapPlan) error {
	paths := []string{plan.Namespace.ServiceRoot, filepath.Join(plan.Namespace.ServiceRoot, "config"),
		filepath.Join(plan.Namespace.ServiceRoot, "secrets"), filepath.Join(plan.Namespace.ServiceRoot, "state"),
		filepath.Join(plan.Namespace.ServiceRoot, "run"), plan.Namespace.Workspaces,
		filepath.Join(plan.Namespace.ServiceRoot, "tmp"), plan.Namespace.ScriptTempRoot,
		plan.Namespace.Backups, plan.Namespace.Logs, plan.Namespace.Binaries, plan.Namespace.Staging}
	for _, path := range paths {
		if err := ensureFreshBootstrapDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func ensureFreshBootstrapDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Getuid()) ||
		stat.Gid != uint32(os.Getgid()) || stat.Mode&0o777 != 0o700 {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func installBootstrapFile(path string, contents []byte, mode os.FileMode) error {
	if mode != 0o600 && mode != 0o700 || len(contents) == 0 {
		return ErrCatalogOperationInvalid
	}
	parent := filepath.Dir(path)
	if err := ensureFreshBootstrapDirectory(parent); err != nil {
		return err
	}
	want := digestBytes(contents)
	if exactBootstrapFile(path, want, mode) {
		return nil
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return ErrSimpleArtifactConflict
	}
	tmpPath := filepath.Join(parent, ".runner-prd-bootstrap-"+filepath.Base(path)+".tmp")
	if data, err := os.ReadFile(tmpPath); err == nil {
		if digestBytes(data) != want || !exactBootstrapFile(tmpPath, want, mode) {
			return ErrSimpleArtifactConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if err := file.Chmod(mode); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := file.Write(contents); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	if exactBootstrapFile(path, want, mode) {
		_ = os.Remove(tmpPath)
		return nil
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return ErrSimpleArtifactConflict
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	if !exactBootstrapFile(path, want, mode) {
		return ErrSimpleArtifactConflict
	}
	return nil
}

func exactBootstrapFile(path, digest string, mode os.FileMode) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return false
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) || stat.Nlink != 1 {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && digestBytes(data) == digest
}

func installPRDReplacementAuthorizedKey(plan PRDReplacementBootstrapPlan, journal *PRDReplacementBootstrapJournal) error {
	path := plan.Namespace.SSHAuthorityPath
	if journal.AuthorizedKeysBefore == "" || digestBytes(journal.AuthorizedKeysBeforeContents) != journal.AuthorizedKeysBefore {
		return ErrCatalogOperationInvalid
	}
	current, err := readAuthorizedKeysSafe(path, journal.AuthorizedKeysExisted, os.FileMode(journal.AuthorizedKeysMode))
	if !journal.AuthorizedKeysExisted {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			current = nil
		} else if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
			return ErrCatalogOperationInvalid
		} else {
			var stat unix.Stat_t
			if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) || stat.Nlink != 1 {
				return ErrCatalogOperationInvalid
			}
			current, err = os.ReadFile(path)
			if err != nil || digestBytes(current) != journal.AuthorizedKeysAfter {
				return ErrSimpleArtifactConflict
			}
			return nil
		}
	} else if err != nil {
		return err
	}
	currentSHA := digestBytes(current)
	if currentSHA == journal.AuthorizedKeysAfter {
		return nil
	}
	if currentSHA != journal.AuthorizedKeysBefore {
		return ErrSimpleArtifactConflict
	}
	after := append([]byte(nil), journal.AuthorizedKeysBeforeContents...)
	if !bootstrapHasExactLine(after, []byte(plan.AuthorizedKeyLine)) {
		if len(after) > 0 && after[len(after)-1] != '\n' {
			after = append(after, '\n')
		}
		after = append(after, []byte(plan.AuthorizedKeyLine)...)
	}
	if digestBytes(after) != journal.AuthorizedKeysAfter {
		return ErrCatalogOperationInvalid
	}
	if !journal.AuthorizedKeysExisted {
		if err := ensureFreshBootstrapDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		return installBootstrapFile(path, after, 0o600)
	}
	temp := path + ".runner-prd-bootstrap.tmp"
	if err := writeBootstrapReplacementFile(path, temp, after, os.FileMode(journal.AuthorizedKeysMode), journal.AuthorizedKeysBefore); err != nil {
		return err
	}
	if digestBytes(mustReadFile(path)) != journal.AuthorizedKeysAfter {
		return ErrSimpleArtifactConflict
	}
	return nil
}

func readAuthorizedKeysSafe(path string, existed bool, mode os.FileMode) ([]byte, error) {
	info, err := os.Lstat(path)
	if !existed && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var stat unix.Stat_t
	if err != nil || !existed || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 ||
		info.Mode().Perm() != mode || info.Mode().Perm()&0o022 != 0 || info.Size() > 1<<20 {
		return nil, ErrCatalogOperationInvalid
	}
	return os.ReadFile(path)
}

func bootstrapHasExactLine(data, expected []byte) bool {
	expected = bytes.TrimSuffix(expected, []byte("\n"))
	if len(expected) == 0 || bytes.Contains(expected, []byte("\n")) {
		return false
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.Equal(line, expected) {
			return true
		}
	}
	return false
}

func writeBootstrapReplacementFile(path, temp string, contents []byte, mode os.FileMode, expectedCurrentSHA string) error {
	if data, err := os.ReadFile(temp); err == nil {
		info, statErr := os.Lstat(temp)
		var stat unix.Stat_t
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode ||
			unix.Lstat(temp, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) || stat.Nlink != 1 || digestBytes(data) != digestBytes(contents) {
			return ErrSimpleArtifactConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if err := file.Chmod(mode); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := file.Write(contents); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	current, err := os.ReadFile(path)
	if err != nil || digestBytes(current) != expectedCurrentSHA {
		return ErrSimpleArtifactConflict
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func mustReadFile(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func preparePRDReplacementDatabase(ctx context.Context, plan PRDReplacementBootstrapPlan,
	journal *PRDReplacementBootstrapJournal) error {
	return preparePRDReplacementDatabaseAt(ctx, plan.Namespace.Database, prdReplacementDatabaseStagePath(plan),
		filepath.Join(plan.Namespace.Staging, "catalog-prd-bootstrap-database.json"), plan.OperationID, plan.InputSHA256, journal)
}

func preparePRDReplacementDatabaseAt(ctx context.Context, path, stagePath, markerPath, operationID, inputSHA string,
	journal *PRDReplacementBootstrapJournal) error {
	if journal == nil || !validCatalogOperationID(operationID) || !bundleDigestPattern.MatchString(inputSHA) ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path || !filepath.IsAbs(stagePath) || filepath.Clean(stagePath) != stagePath ||
		filepath.Dir(stagePath) != filepath.Dir(path) || !filepath.IsAbs(markerPath) || filepath.Clean(markerPath) != markerPath {
		return ErrCatalogOperationInvalid
	}
	marker, _ := json.MarshalIndent(struct {
		OperationID string `json:"operation_id"`
		InputSHA256 string `json:"input_sha256"`
	}{operationID, inputSHA}, "", "  ")
	marker = append(marker, '\n')
	if err := installBootstrapFile(markerPath, marker, 0o600); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return ErrSimpleArtifactConflict
	}
	for _, sidecar := range []string{stagePath + "-wal", stagePath + "-shm"} {
		info, err := os.Lstat(sidecar)
		if err == nil {
			var stat unix.Stat_t
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
				unix.Lstat(sidecar, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
				return ErrSimpleArtifactConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := os.Lstat(stagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	database, err := store.Open(ctx, stagePath)
	if err != nil {
		return err
	}
	schema, work, inventoryErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(inventoryErr, closeErr); err != nil || schema != store.CurrentSchemaVersion || !work.Empty() {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	if err := verifyPRDReplacementDatabaseFile(stagePath, "", 1); err != nil {
		return err
	}
	for _, sidecar := range []string{stagePath + "-wal", stagePath + "-shm"} {
		if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
			return ErrSimpleArtifactConflict
		}
	}
	journal.DatabaseStagingPath = stagePath
	journal.DatabaseSHA256, err = fileSHA256(stagePath)
	if err != nil {
		return err
	}
	if !bundleDigestPattern.MatchString(journal.DatabaseSHA256) {
		return ErrPRDUpgradeGuard
	}
	return nil
}

func prdReplacementDatabaseStagePath(plan PRDReplacementBootstrapPlan) string {
	return filepath.Join(plan.Namespace.Staging, "catalog-prd-bootstrap-"+plan.OperationID+".db")
}

func verifyPRDReplacementDatabaseFile(path, expectedSHA string, linkCount uint64) error {
	info, err := os.Lstat(path)
	var stat unix.Stat_t
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) || uint64(stat.Nlink) != linkCount || info.Size() <= 0 {
		return ErrCatalogOperationInvalid
	}
	if expectedSHA != "" {
		digest, err := fileSHA256(path)
		if err != nil || digest != expectedSHA {
			return ErrSimpleArtifactConflict
		}
	}
	return nil
}

func verifyPRDReplacementDatabasePrepared(ctx context.Context, plan PRDReplacementBootstrapPlan,
	journal PRDReplacementBootstrapJournal) error {
	if journal.DatabaseStagingPath != prdReplacementDatabaseStagePath(plan) || !bundleDigestPattern.MatchString(journal.DatabaseSHA256) {
		return ErrCatalogOperationInvalid
	}
	return verifyPRDReplacementDatabasePreparedAt(ctx, plan.Namespace.Database, journal.DatabaseStagingPath, journal.DatabaseSHA256)
}

func verifyPRDReplacementDatabasePreparedAt(ctx context.Context, livePath, stagePath, expectedSHA string) error {
	liveInfo, liveErr := os.Lstat(livePath)
	stageInfo, stageErr := os.Lstat(stagePath)
	if liveErr == nil {
		if stageErr == nil {
			if !os.SameFile(liveInfo, stageInfo) || verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 2) != nil ||
				verifyPRDReplacementDatabaseFile(stagePath, expectedSHA, 2) != nil {
				return ErrSimpleArtifactConflict
			}
			return verifyReplacementDatabaseSchemaAndWork(ctx, livePath)
		}
		if !errors.Is(stageErr, os.ErrNotExist) || verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 1) != nil {
			return ErrSimpleArtifactConflict
		}
		return verifyReplacementDatabaseSchemaAndWork(ctx, livePath)
	}
	if !errors.Is(liveErr, os.ErrNotExist) || stageErr != nil {
		return ErrSimpleArtifactConflict
	}
	return verifyPRDReplacementDatabaseFile(stagePath, expectedSHA, 1)
}

func verifyReplacementDatabaseSchemaAndWork(ctx context.Context, path string) error {
	database, err := store.OpenExistingRestartPreflightReadOnly(ctx, path)
	if err != nil {
		return err
	}
	schema, work, readErr := readPRDWorkSnapshot(ctx, database)
	closeErr := database.Close()
	if err := errors.Join(readErr, closeErr); err != nil || schema != store.CurrentSchemaVersion || !work.Empty() {
		return errors.Join(ErrPRDUpgradeGuard, err)
	}
	return nil
}

func installPreparedPRDReplacementDatabase(ctx context.Context, plan PRDReplacementBootstrapPlan,
	journal PRDReplacementBootstrapJournal) error {
	if journal.DatabaseStagingPath != prdReplacementDatabaseStagePath(plan) || !bundleDigestPattern.MatchString(journal.DatabaseSHA256) {
		return ErrCatalogOperationInvalid
	}
	return installPreparedPRDReplacementDatabaseAt(ctx, plan.Namespace.Database, journal.DatabaseStagingPath, journal.DatabaseSHA256)
}

func installPreparedPRDReplacementDatabaseAt(ctx context.Context, livePath, stagePath, expectedSHA string) error {
	liveInfo, liveErr := os.Lstat(livePath)
	stageInfo, stageErr := os.Lstat(stagePath)
	if liveErr == nil {
		if stageErr == nil {
			if !os.SameFile(liveInfo, stageInfo) || verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 2) != nil ||
				verifyPRDReplacementDatabaseFile(stagePath, expectedSHA, 2) != nil {
				return ErrSimpleArtifactConflict
			}
			if err := os.Remove(stagePath); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(livePath)); err != nil {
				return err
			}
			if err := verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 1); err != nil {
				return err
			}
			return verifyReplacementDatabaseSchemaAndWork(ctx, livePath)
		}
		if !errors.Is(stageErr, os.ErrNotExist) || verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 1) != nil {
			return ErrSimpleArtifactConflict
		}
		return verifyReplacementDatabaseSchemaAndWork(ctx, livePath)
	}
	if !errors.Is(liveErr, os.ErrNotExist) || stageErr != nil {
		return ErrSimpleArtifactConflict
	}
	if err := verifyPRDReplacementDatabaseFile(stagePath, expectedSHA, 1); err != nil {
		return err
	}
	if _, err := os.Lstat(livePath); !errors.Is(err, os.ErrNotExist) {
		return ErrSimpleArtifactConflict
	}
	if err := os.Link(stagePath, livePath); err != nil {
		return err
	}
	if err := verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 2); err != nil {
		return err
	}
	if err := os.Remove(stagePath); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(livePath)); err != nil {
		return err
	}
	if err := verifyPRDReplacementDatabaseFile(livePath, expectedSHA, 1); err != nil {
		return err
	}
	return verifyReplacementDatabaseSchemaAndWork(ctx, livePath)
}

func writeCatalogFreshInstallBaselineMustMatch(plan PRDReplacementBootstrapPlan, evidence CatalogFreshInstallBaseline) error {
	upgrade := planToUpgradePlan(plan)
	// The baseline is an immutable receipt. A process may die after the atomic
	// file write but before the bootstrap journal records the step. An exact
	// retry must recognize that receipt rather than minting a new timestamp and
	// conflicting with its own completed side effect.
	if _, err := os.Lstat(catalogFreshInstallBaselinePath(upgrade)); err == nil {
		stored, readErr := readCatalogFreshInstallBaseline(upgrade)
		if readErr != nil || !samePRDReplacementBootstrapBaseline(stored, evidence) {
			return errors.Join(ErrCatalogOperationInvalid, readErr)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeCatalogFreshInstallBaseline(upgrade, evidence); err != nil {
		return err
	}
	stored, err := readCatalogFreshInstallBaseline(upgrade)
	if err != nil || stored.EvidenceSHA256 != evidence.EvidenceSHA256 {
		return errors.Join(ErrCatalogOperationInvalid, err)
	}
	return nil
}

func samePRDReplacementBootstrapBaseline(stored, retry CatalogFreshInstallBaseline) bool {
	return stored.OperationID == retry.OperationID &&
		stored.Absence.EvidenceSHA256 == retry.Absence.EvidenceSHA256 &&
		stored.ConfigurationSHA256 == retry.ConfigurationSHA256 &&
		stored.DatabaseSchema == retry.DatabaseSchema &&
		stored.WorkInventorySHA256 == retry.WorkInventorySHA256
}

// PRDReplacementBootstrapJournalRoot returns the fixed operation directory
// under the existing account's runtime workflow root. It is not the release
// catalog and is never configurable by the bootstrap request.
func PRDReplacementBootstrapJournalRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", ErrCatalogOperationInvalid
	}
	return filepath.Join(home, "projects-runtime", "runner-release-operations", "prd-replacement-bootstrap"), nil
}
