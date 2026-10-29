package laneinstall

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
)

// PRDReplacementMacRouteInput contains only the material needed to update the
// existing Mac PRD route after the replacement Linux namespace is prepared.
// It intentionally excludes both server and client private keys.
type PRDReplacementMacRouteInput struct {
	OperationID           string `json:"operation_id"`
	MacConfiguration      []byte `json:"mac_configuration"`
	ReleaseID             string `json:"release_id"`
	ExpectedReleaseSHA256 string `json:"expected_release_sha256"`
	ServerCertificatePEM  []byte `json:"server_certificate_pem"`
	ServerCAPEM           []byte `json:"server_ca_pem"`
	ClientCertificatePEM  []byte `json:"client_certificate_pem"`
	ClientCAPEM           []byte `json:"client_ca_pem"`
	ClientPrincipalMap    []byte `json:"client_principal_map"`
	DispatcherPublicKey   string `json:"dispatcher_public_key"`
	SSHHostPublicKey      string `json:"ssh_host_public_key"`
}

// PRDReplacementMacRouteResult is a sanitized closeout for the paired Mac
// route update. No credential contents are recorded.
type PRDReplacementMacRouteResult struct {
	OperationID          string `json:"operation_id"`
	Stage                string `json:"stage"`
	ConfigurationSHA256  string `json:"configuration_sha256"`
	ServerCASHA256       string `json:"server_ca_sha256"`
	KnownHostsSHA256     string `json:"known_hosts_sha256"`
	WorkInventorySHA256  string `json:"work_inventory_sha256"`
	DatabaseSchema       int    `json:"database_schema"`
	BackupDirectory      string `json:"backup_directory"`
	BackupManifestSHA256 string `json:"backup_manifest_sha256"`
	JournalSHA256        string `json:"journal_sha256"`
	ServiceStopped       bool   `json:"service_stopped"`
}

type prdReplacementMacRouteJournal struct {
	FormatVersion               int    `json:"format_version"`
	OperationID                 string `json:"operation_id"`
	RequestSHA256               string `json:"request_sha256"`
	Stage                       string `json:"stage"`
	PreviousConfigurationSHA256 string `json:"previous_configuration_sha256"`
	ConfigurationSHA256         string `json:"configuration_sha256"`
	PreviousServerCASHA256      string `json:"previous_server_ca_sha256"`
	ServerCASHA256              string `json:"server_ca_sha256"`
	PreviousKnownHostsSHA256    string `json:"previous_known_hosts_sha256"`
	KnownHostsSHA256            string `json:"known_hosts_sha256"`
	WorkInventorySHA256         string `json:"work_inventory_sha256"`
	DatabaseSchema              int    `json:"database_schema"`
	PreviousBundleSHA256        string `json:"previous_bundle_sha256"`
	ServiceWasActive            bool   `json:"service_was_active"`
	BackupDirectory             string `json:"backup_directory,omitempty"`
	BackupManifestSHA256        string `json:"backup_manifest_sha256,omitempty"`
	CreatedAtUTC                string `json:"created_at_utc"`
	UpdatedAtUTC                string `json:"updated_at_utc"`
}

// RunPRDReplacementMacRoute performs the supported, forward-only Mac half of
// a fresh replacement bootstrap. It backs up and stops only Mac PRD, preserves
// the database and mailbox paths, replaces trust before the route, and never
// restores the retired Linux endpoint as a rollback action.
func RunPRDReplacementMacRoute(ctx context.Context, input PRDReplacementMacRouteInput, now time.Time) (PRDReplacementMacRouteResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if runtime.GOOS != "darwin" || !validCatalogOperationID(input.OperationID) ||
		!validCatalogReleaseID(input.ReleaseID) || !bundleDigestPattern.MatchString(input.ExpectedReleaseSHA256) || now.IsZero() {
		return PRDReplacementMacRouteResult{}, ErrInvalidPRDUpgradePlan
	}
	plan, err := BuildPRDUpgradePlan("mac-control-plane")
	if err != nil || validatePRDPlan(plan) != nil {
		return PRDReplacementMacRouteResult{}, ErrInvalidPRDUpgradePlan
	}
	service, err := NewPRDServiceController()
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	candidateConfiguration, err := config.BuildFreshPRDReplacementMacRoute(input.MacConfiguration)
	if err != nil {
		return PRDReplacementMacRouteResult{}, config.ErrInvalidLaneProfiles
	}
	identity, err := config.InspectPRDReplacementMacLegacyIdentity(input.MacConfiguration)
	if err != nil {
		return PRDReplacementMacRouteResult{}, config.ErrInvalidLaneProfiles
	}
	clientCertificate, err := readOwnedSimpleRegularFile(identity.ClientCertificatePath, 1<<20)
	if err != nil || !bytes.Equal(clientCertificate, input.ClientCertificatePEM) {
		return PRDReplacementMacRouteResult{}, ErrInvalidLaneTLSMaterial
	}
	clientPrivateKey, err := readOwnedSimpleRegularFile(identity.ClientPrivateKeyPath, 1<<20)
	if err != nil {
		return PRDReplacementMacRouteResult{}, ErrInvalidLaneTLSMaterial
	}
	if _, err := tls.X509KeyPair(clientCertificate, clientPrivateKey); err != nil {
		return PRDReplacementMacRouteResult{}, ErrInvalidLaneTLSMaterial
	}
	if err := validatePRDReplacementLegacyClientMaterial(input.ClientCertificatePEM, input.ClientCAPEM,
		input.ClientPrincipalMap, now); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	if err := validatePRDReplacementServerTrust(input.ServerCertificatePEM, input.ServerCAPEM, now); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	dispatcher, err := parseEd25519PublicKey(input.DispatcherPublicKey)
	if err != nil {
		return PRDReplacementMacRouteResult{}, ErrInvalidQueuedSSHKey
	}
	hostKey, err := parseEd25519PublicKey(input.SSHHostPublicKey)
	if err != nil {
		return PRDReplacementMacRouteResult{}, ErrInvalidQueuedSSHKey
	}
	queued, err := buildPRDReplacementQueuedSSHArtifacts(dispatcher, hostKey)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	serverCA, err := readOwnedSimpleRegularFile(identity.ServerCAPath, 1<<20)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	knownHosts, err := readOwnedSimpleRegularFile(identity.KnownHostsPath, 64*1024)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	requestSHA, err := prdReplacementMacRouteInputSHA(input)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	journalRoot, err := PRDReplacementBootstrapJournalRoot()
	if err != nil || ensureBootstrapJournalRootAtRoot(journalRoot) != nil {
		return PRDReplacementMacRouteResult{}, ErrCatalogOperationInvalid
	}
	lock, err := acquireBootstrapLock(filepath.Join(journalRoot, input.OperationID+"-mac-route.lock"))
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	defer releaseBootstrapLock(lock)
	journalPath := filepath.Join(journalRoot, input.OperationID+"-mac-route.json")
	journal, exists, err := readPRDReplacementMacRouteJournal(journalPath)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	configurationSHA := digestBytes(candidateConfiguration)
	serverCASHA := digestBytes(input.ServerCAPEM)
	knownHostsCandidate := []byte(queued.knownHostLine)
	knownHostsSHA := digestBytes(knownHostsCandidate)
	currentConfiguration, err := readOwnedSimpleConfig(plan.Namespace.ConfigFile)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	if !exists {
		if !bytes.Equal(currentConfiguration, input.MacConfiguration) {
			return PRDReplacementMacRouteResult{}, ErrCatalogOperationInvalid
		}
		preflight, preErr := ReadPRDPreflight(ctx, plan, service)
		if preErr != nil || !preflight.Work.Empty() || preflight.SchemaVersion <= 0 ||
			preflight.ConfigSHA256 != digestBytes(currentConfiguration) || !bundleDigestPattern.MatchString(preflight.LegacyBundleSHA256) {
			return PRDReplacementMacRouteResult{}, errors.Join(ErrPRDUpgradeGuard, preErr)
		}
		active, statusErr := service.Status(ctx, plan)
		if statusErr != nil {
			return PRDReplacementMacRouteResult{}, statusErr
		}
		journal = prdReplacementMacRouteJournal{FormatVersion: 1, OperationID: input.OperationID,
			RequestSHA256: requestSHA, Stage: "intent", PreviousConfigurationSHA256: digestBytes(currentConfiguration),
			ConfigurationSHA256: configurationSHA, PreviousServerCASHA256: digestBytes(serverCA), ServerCASHA256: serverCASHA,
			PreviousKnownHostsSHA256: digestBytes(knownHosts), KnownHostsSHA256: knownHostsSHA,
			WorkInventorySHA256: preflight.Work.SHA256(), DatabaseSchema: preflight.SchemaVersion,
			PreviousBundleSHA256: preflight.LegacyBundleSHA256, ServiceWasActive: active,
			CreatedAtUTC: now.UTC().Format(time.RFC3339Nano), UpdatedAtUTC: now.UTC().Format(time.RFC3339Nano)}
		if err := writePRDReplacementMacRouteJournal(journalPath, journal, false); err != nil {
			return PRDReplacementMacRouteResult{}, err
		}
	} else if journal.OperationID != input.OperationID || journal.RequestSHA256 != requestSHA ||
		journal.ConfigurationSHA256 != configurationSHA || journal.ServerCASHA256 != serverCASHA || journal.KnownHostsSHA256 != knownHostsSHA {
		return PRDReplacementMacRouteResult{}, ErrCatalogOperationInvalid
	} else if digestBytes(currentConfiguration) != journal.PreviousConfigurationSHA256 &&
		digestBytes(currentConfiguration) != journal.ConfigurationSHA256 {
		return PRDReplacementMacRouteResult{}, ErrCatalogSelectorConflict
	}
	if err := ensurePRDReplacementMacRouteBackup(ctx, plan, journalPath, &journal); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	if err := verifyPRDReplacementMacRouteBackup(ctx, plan, identity, journal); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	active, err := service.Status(ctx, plan)
	if err != nil {
		return PRDReplacementMacRouteResult{}, errors.Join(ErrSimpleLaneNotQuiescent, err)
	}
	if active {
		if !journal.ServiceWasActive || exists && (journal.Stage != "intent" || journal.BackupDirectory != "") {
			return PRDReplacementMacRouteResult{}, ErrSimpleLaneNotQuiescent
		}
		if err := service.Stop(ctx, plan); err != nil {
			return PRDReplacementMacRouteResult{}, err
		}
	}
	lockFD, err := acquirePRDUpgradeLock(plan)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	defer releasePRDUpgradeLock(lockFD)
	active, err = service.Status(ctx, plan)
	if err != nil || active {
		return PRDReplacementMacRouteResult{}, errors.Join(ErrSimpleLaneNotQuiescent, err)
	}
	preflight, err := ReadPRDPreflight(ctx, plan, service)
	if err != nil || !preflight.Work.Empty() || preflight.SchemaVersion != journal.DatabaseSchema ||
		preflight.Work.SHA256() != journal.WorkInventorySHA256 ||
		(preflight.ConfigSHA256 != journal.PreviousConfigurationSHA256 && preflight.ConfigSHA256 != journal.ConfigurationSHA256) {
		return PRDReplacementMacRouteResult{}, errors.Join(ErrPRDUpgradeGuard, err)
	}
	manifest, err := loadPRDBackupManifest(plan, journal.BackupDirectory)
	if err != nil || manifest.SchemaVersion != journal.DatabaseSchema || manifest.BundleSHA256 != journal.PreviousBundleSHA256 ||
		backupManifestSHA256(journal.BackupDirectory) != journal.BackupManifestSHA256 ||
		verifyPRDReplacementMacRouteBackupFiles(manifest, map[string]string{
			plan.Namespace.ConfigFile: journal.PreviousConfigurationSHA256,
			identity.ServerCAPath:     journal.PreviousServerCASHA256,
			identity.KnownHostsPath:   journal.PreviousKnownHostsSHA256,
		}) != nil ||
		verifyPRDBackupWorkSnapshot(ctx, plan, manifest, journal.WorkInventorySHA256, journal.DatabaseSchema) != nil {
		return PRDReplacementMacRouteResult{}, errors.Join(ErrSimpleBackupRequired, err)
	}
	if err := applyPRDReplacementMacRouteFile(identity.KnownHostsPath, journal.PreviousKnownHostsSHA256,
		journal.KnownHostsSHA256, knownHostsCandidate, 64*1024); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	journal.Stage = "known_hosts_updated"
	if err := writePRDReplacementMacRouteJournal(journalPath, journal, true); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	if err := applyPRDReplacementMacRouteFile(identity.ServerCAPath, journal.PreviousServerCASHA256,
		journal.ServerCASHA256, input.ServerCAPEM, 1<<20); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	journal.Stage = "server_trust_updated"
	if err := writePRDReplacementMacRouteJournal(journalPath, journal, true); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	if err := applyPRDReplacementMacRouteFile(plan.Namespace.ConfigFile, journal.PreviousConfigurationSHA256,
		journal.ConfigurationSHA256, candidateConfiguration, config.MaxConfigBytes); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	journal.Stage = "complete"
	journal.UpdatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writePRDReplacementMacRouteJournal(journalPath, journal, true); err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	installedConfig, err := readOwnedSimpleConfig(plan.Namespace.ConfigFile)
	if err != nil || digestBytes(installedConfig) != journal.ConfigurationSHA256 {
		return PRDReplacementMacRouteResult{}, ErrSimpleLanePreflight
	}
	installedCA, err := readOwnedSimpleRegularFile(identity.ServerCAPath, 1<<20)
	if err != nil || digestBytes(installedCA) != journal.ServerCASHA256 {
		return PRDReplacementMacRouteResult{}, ErrSimpleLanePreflight
	}
	installedKnownHosts, err := readOwnedSimpleRegularFile(identity.KnownHostsPath, 64*1024)
	if err != nil || digestBytes(installedKnownHosts) != journal.KnownHostsSHA256 {
		return PRDReplacementMacRouteResult{}, ErrSimpleLanePreflight
	}
	active, err = service.Status(ctx, plan)
	if err != nil || active {
		return PRDReplacementMacRouteResult{}, errors.Join(ErrSimpleLaneNotQuiescent, err)
	}
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		return PRDReplacementMacRouteResult{}, err
	}
	return PRDReplacementMacRouteResult{OperationID: input.OperationID, Stage: journal.Stage,
		ConfigurationSHA256: journal.ConfigurationSHA256, ServerCASHA256: journal.ServerCASHA256,
		KnownHostsSHA256: journal.KnownHostsSHA256, WorkInventorySHA256: journal.WorkInventorySHA256,
		DatabaseSchema: journal.DatabaseSchema, BackupDirectory: journal.BackupDirectory,
		BackupManifestSHA256: journal.BackupManifestSHA256, JournalSHA256: digestBytes(journalBytes), ServiceStopped: true}, nil
}

func validatePRDReplacementServerTrust(certificatePEM, caPEM []byte, now time.Time) error {
	if len(certificatePEM) == 0 || len(certificatePEM) > 1<<20 || len(caPEM) == 0 || len(caPEM) > 1<<20 || now.IsZero() {
		return ErrInvalidLaneTLSMaterial
	}
	certBlock, certRest := pem.Decode(certificatePEM)
	caBlock, caRest := pem.Decode(caPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" || len(bytes.TrimSpace(certRest)) != 0 ||
		caBlock == nil || caBlock.Type != "CERTIFICATE" || len(bytes.TrimSpace(caRest)) != 0 {
		return ErrInvalidLaneTLSMaterial
	}
	certificate, certErr := x509.ParseCertificate(certBlock.Bytes)
	ca, caErr := x509.ParseCertificate(caBlock.Bytes)
	if certErr != nil || caErr != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 ||
		ca.CheckSignatureFrom(ca) != nil || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) ||
		validatePRDReplacementServerCertificate(certificate, ca, now) != nil {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

func prdReplacementMacRouteInputSHA(input PRDReplacementMacRouteInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 3<<20 {
		return "", ErrCatalogOperationInvalid
	}
	return digestBytes(encoded), nil
}

func readPRDReplacementMacRouteJournal(path string) (prdReplacementMacRouteJournal, bool, error) {
	var journal prdReplacementMacRouteJournal
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, false, nil
	}
	var stat unix.Stat_t
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > 128<<10 || unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return journal, false, ErrCatalogOperationInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return journal, false, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&journal) != nil || d.Decode(&struct{}{}) != io.EOF {
		return prdReplacementMacRouteJournal{}, false, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(journal, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) || journal.FormatVersion != 1 ||
		!validCatalogOperationID(journal.OperationID) || !bundleDigestPattern.MatchString(journal.RequestSHA256) ||
		!bundleDigestPattern.MatchString(journal.PreviousConfigurationSHA256) || !bundleDigestPattern.MatchString(journal.ConfigurationSHA256) ||
		!bundleDigestPattern.MatchString(journal.PreviousServerCASHA256) || !bundleDigestPattern.MatchString(journal.ServerCASHA256) ||
		!bundleDigestPattern.MatchString(journal.PreviousKnownHostsSHA256) || !bundleDigestPattern.MatchString(journal.KnownHostsSHA256) ||
		!bundleDigestPattern.MatchString(journal.WorkInventorySHA256) || journal.DatabaseSchema <= 0 ||
		!bundleDigestPattern.MatchString(journal.PreviousBundleSHA256) ||
		journal.Stage != "intent" && journal.Stage != "backed_up" && journal.Stage != "known_hosts_updated" &&
			journal.Stage != "server_trust_updated" && journal.Stage != "complete" {
		return prdReplacementMacRouteJournal{}, false, ErrCatalogOperationInvalid
	}
	return journal, true, nil
}

func writePRDReplacementMacRouteJournal(path string, journal prdReplacementMacRouteJournal, replace bool) error {
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil || len(encoded) > 128<<10 {
		return ErrCatalogOperationInvalid
	}
	encoded = append(encoded, '\n')
	if !replace {
		return writePrivateFile(path, encoded, 0o600)
	}
	return replaceOwnedSimpleRegularFile(path, digestFileSHA(path), encoded, 128<<10)
}

func ensurePRDReplacementMacRouteBackup(ctx context.Context, plan PRDUpgradePlan, journalPath string,
	journal *prdReplacementMacRouteJournal) error {
	if journal.BackupDirectory != "" {
		return nil
	}
	if journal.Stage != "intent" {
		return ErrCatalogOperationInvalid
	}
	backup, err := CreatePRDCatalogBackup(ctx, plan, journal.PreviousBundleSHA256)
	if err != nil {
		return err
	}
	if backup.Manifest.SchemaVersion != journal.DatabaseSchema ||
		verifyPRDBackupWorkSnapshot(ctx, plan, backup.Manifest, journal.WorkInventorySHA256, journal.DatabaseSchema) != nil {
		return ErrSimpleBackupRequired
	}
	journal.BackupDirectory = backup.Directory
	journal.BackupManifestSHA256 = backupManifestSHA256(backup.Directory)
	journal.Stage = "backed_up"
	journal.UpdatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	if !bundleDigestPattern.MatchString(journal.BackupManifestSHA256) {
		return ErrSimpleBackupRequired
	}
	return writePRDReplacementMacRouteJournal(journalPath, *journal, true)
}

func verifyPRDReplacementMacRouteBackup(ctx context.Context, plan PRDUpgradePlan, identity config.PRDReplacementMacLegacyIdentity,
	journal prdReplacementMacRouteJournal) error {
	manifest, err := loadPRDBackupManifest(plan, journal.BackupDirectory)
	if err != nil || manifest.SchemaVersion != journal.DatabaseSchema || manifest.BundleSHA256 != journal.PreviousBundleSHA256 ||
		backupManifestSHA256(journal.BackupDirectory) != journal.BackupManifestSHA256 ||
		verifyPRDReplacementMacRouteBackupFiles(manifest, map[string]string{
			plan.Namespace.ConfigFile: journal.PreviousConfigurationSHA256,
			identity.ServerCAPath:     journal.PreviousServerCASHA256,
			identity.KnownHostsPath:   journal.PreviousKnownHostsSHA256,
		}) != nil ||
		verifyPRDBackupWorkSnapshot(ctx, plan, manifest, journal.WorkInventorySHA256, journal.DatabaseSchema) != nil {
		return errors.Join(ErrSimpleBackupRequired, err)
	}
	return nil
}

// verifyPRDReplacementMacRouteBackupFiles validates immutable copies in the
// backup directory. The live configuration, CA, and known_hosts may already
// have advanced during a journaled retry, so comparing those live files to
// their predecessor hashes would incorrectly reject a valid partial install.
func verifyPRDReplacementMacRouteBackupFiles(manifest PRDBackupManifest, expected map[string]string) error {
	if !filepath.IsAbs(manifest.DatabaseBackupPath) || filepath.Clean(manifest.DatabaseBackupPath) != manifest.DatabaseBackupPath {
		return ErrSimpleBackupRequired
	}
	root := filepath.Dir(manifest.DatabaseBackupPath)
	seen := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if seen[file.TargetPath] {
			return ErrSimpleBackupRequired
		}
		seen[file.TargetPath] = true
		if expectedSHA, required := expected[file.TargetPath]; required && expectedSHA != file.SHA256 {
			return ErrSimpleBackupRequired
		}
		backupPath := filepath.Join(root, file.BackupPath)
		if !pathContained(root, backupPath) {
			return ErrSimpleBackupRequired
		}
		info, err := os.Lstat(backupPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			uint32(info.Mode().Perm()) != file.Mode || info.Size() != file.Bytes {
			return ErrSimpleBackupRequired
		}
		var stat unix.Stat_t
		if unix.Lstat(backupPath, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
			return ErrSimpleBackupRequired
		}
		digest, err := fileSHA256(backupPath)
		if err != nil || digest != file.SHA256 {
			return ErrSimpleBackupRequired
		}
	}
	for target := range expected {
		if !seen[target] {
			return ErrSimpleBackupRequired
		}
	}
	info, err := os.Lstat(manifest.DatabaseBackupPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return ErrSimpleBackupRequired
	}
	var stat unix.Stat_t
	if unix.Lstat(manifest.DatabaseBackupPath, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return ErrSimpleBackupRequired
	}
	digest, err := fileSHA256(manifest.DatabaseBackupPath)
	if err != nil || digest != manifest.DatabaseSHA256 {
		return ErrSimpleBackupRequired
	}
	return nil
}

func applyPRDReplacementMacRouteFile(path, beforeSHA, afterSHA string, after []byte, max int64) error {
	if !bundleDigestPattern.MatchString(beforeSHA) || digestBytes(after) != afterSHA {
		return ErrCatalogOperationInvalid
	}
	current, err := readOwnedSimpleRegularFile(path, max)
	if err != nil {
		return err
	}
	currentSHA := digestBytes(current)
	if currentSHA == afterSHA {
		return nil
	}
	if currentSHA != beforeSHA {
		return ErrCatalogSelectorConflict
	}
	if path == "" || filepath.Clean(path) != path {
		return ErrCatalogOperationInvalid
	}
	return replaceOwnedSimpleRegularFile(path, beforeSHA, after, max)
}
