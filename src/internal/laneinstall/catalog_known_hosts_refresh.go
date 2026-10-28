package laneinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
)

// CatalogKnownHostsRefreshResult records a guarded replacement of the active
// Mac Dev/QAS linux-host known_hosts file. The complete previous file remains
// in the consistent selected-lane backup.
type CatalogKnownHostsRefreshResult struct {
	HostID                   string             `json:"host_id"`
	Lane                     config.ReleaseLane `json:"lane"`
	ConfigurationSHA256      string             `json:"configuration_sha256"`
	PreviousKnownHostsSHA256 string             `json:"previous_known_hosts_sha256"`
	KnownHostsSHA256         string             `json:"known_hosts_sha256"`
	HostKeyFingerprint       string             `json:"host_key_fingerprint"`
	BackupDirectory          string             `json:"backup_directory"`
	BackupManifestSHA256     string             `json:"backup_manifest_sha256"`
	KnownHostsBackupPath     string             `json:"known_hosts_backup_path"`
	EvidencePath             string             `json:"evidence_path"`
	EvidenceSHA256           string             `json:"evidence_sha256"`
	WorkInventoryEmpty       bool               `json:"work_inventory_empty"`
	ServiceStopped           bool               `json:"service_stopped"`
}

type catalogKnownHostsRefreshEvidence struct {
	FormatVersion            int                `json:"format_version"`
	HostID                   string             `json:"host_id"`
	Lane                     config.ReleaseLane `json:"lane"`
	Stage                    string             `json:"stage"`
	ConfigurationSHA256      string             `json:"configuration_sha256"`
	KnownHostsPath           string             `json:"known_hosts_path"`
	PreviousKnownHostsSHA256 string             `json:"previous_known_hosts_sha256"`
	KnownHostsSHA256         string             `json:"known_hosts_sha256"`
	HostKeyFingerprint       string             `json:"host_key_fingerprint"`
	BackupDirectory          string             `json:"backup_directory"`
	BackupManifestSHA256     string             `json:"backup_manifest_sha256"`
	KnownHostsBackupPath     string             `json:"known_hosts_backup_path"`
	UpdatedAtUTC             time.Time          `json:"updated_at_utc"`
}

// RefreshMacReplacementLinuxKnownHosts reduces a verified retired-plus-current
// route file to the one key for the already configured replacement endpoint.
// It runs under the catalog and lane lifecycle locks, requires a stopped and
// empty lane, and preserves the complete previous file in its consistent
// configuration/database backup before changing the active trust file.
func (m *CatalogLaneMaintenance) RefreshMacReplacementLinuxKnownHosts(
	ctx context.Context,
	expectedConfigurationSHA256 string,
	expectedKnownHostsSHA256 string,
) (CatalogKnownHostsRefreshResult, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh ||
		(m.layout.lane != config.ReleaseLaneDev && m.layout.lane != config.ReleaseLaneQAS) ||
		m.layout.hostID != "mac-control-plane" ||
		!validConfigDigest(expectedConfigurationSHA256) || !validConfigDigest(expectedKnownHostsSHA256) {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	plan := m.maintenance.plan
	if validateSimplePlan(plan) != nil || plan.HostID != "mac-control-plane" || plan.Lane != m.layout.lane || plan.Namespace.Host != config.HostKindMac {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}

	configuration, err := readOwnedSimpleConfig(plan.Namespace.ConfigFile)
	if err != nil || digestBytes(configuration) != expectedConfigurationSHA256 {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	loaded, err := config.ParseBytes(configuration)
	if err != nil || loaded.Kind() != config.HostKindMac || loaded.ReleaseLane() != plan.Lane || loaded.SchemaVersion() != config.VersionV3 {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	profiles, ok := loaded.LaneProfiles()
	if !ok || profiles.Host() != config.HostKindMac || profiles.Lane() != plan.Lane {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	profile, ok := profiles.MacProfile("linux-host")
	if !ok || profile.Queued.Host != config.SimpleReplacementLinuxPublicIP || profile.Queued.Port != 22 ||
		!validConfigDigest(strings.TrimPrefix(profile.Queued.HostKeyFingerprint, "sha256:")) {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	linuxPlan, err := BuildSimplePlan("linux-host", plan.Lane)
	if err != nil || profile.Direct.URL != linuxPlan.Namespace.DirectEndpointURL ||
		profile.Queued.KnownHostsFile != filepath.Join(plan.Namespace.ServiceRoot, "secrets", "profiles", "linux-host", "ssh", "known_hosts") {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}

	knownHostsBefore, err := readOwnedSimpleRegularFile(profile.Queued.KnownHostsFile, 64*1024)
	if err != nil || digestBytes(knownHostsBefore) != expectedKnownHostsSHA256 {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	knownHostsAfter, err := reduceReplacementKnownHosts(
		knownHostsBefore,
		"129.151.232.40",
		profile.Queued.Host,
		profile.Queued.HostKeyFingerprint,
		true,
	)
	if err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}

	active, err := m.maintenance.service.Status(ctx, plan)
	if err != nil || active {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLaneNotQuiescent
	}
	currentBundle, err := simpleCurrentBundleForPlan(plan)
	if err != nil || currentBundle != m.maintenance.proof.current {
		return CatalogKnownHostsRefreshResult{}, ErrCatalogSelectorConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, plan, currentBundle)
	if err != nil || proof.database != m.maintenance.proof.database || !simpleMetricsEmpty(proof.metrics) {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLaneNotQuiescent
	}

	backup, err := m.Backup(ctx)
	if err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}
	manifest, err := readSimpleLaneBackupManifest(plan, backup.Directory())
	if err != nil || manifest.ConfigSHA256 != expectedConfigurationSHA256 || manifest.DatabaseSchema != proof.database ||
		!simpleBackupReceiptValid(plan, backup, proof) {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleBackupRequired
	}
	active, err = m.maintenance.service.Status(ctx, plan)
	if err != nil || active {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLaneNotQuiescent
	}
	latestConfig, err := readOwnedSimpleConfig(plan.Namespace.ConfigFile)
	if err != nil || digestBytes(latestConfig) != expectedConfigurationSHA256 {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	latestKnownHosts, err := readOwnedSimpleRegularFile(profile.Queued.KnownHostsFile, 64*1024)
	if err != nil || digestBytes(latestKnownHosts) != expectedKnownHostsSHA256 {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}

	// Keep these supplemental artifacts beside, rather than inside, the
	// immutable SQLite/config backup directory so its existing manifest remains
	// byte-for-byte verifiable.
	knownHostsBackupPath, err := preserveKnownHostsRefreshBackup(backup.Directory(), knownHostsBefore, expectedKnownHostsSHA256)
	if err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}
	backupStem := backup.Directory()
	manifestSHA := backup.ManifestSHA256()
	activeSHA := digestBytes(knownHostsAfter)
	evidencePath := backupStem + ".known-hosts-refresh.json"
	evidence := catalogKnownHostsRefreshEvidence{
		FormatVersion: 1, HostID: plan.HostID, Lane: plan.Lane, Stage: "prepared",
		ConfigurationSHA256: expectedConfigurationSHA256, KnownHostsPath: profile.Queued.KnownHostsFile,
		PreviousKnownHostsSHA256: expectedKnownHostsSHA256, KnownHostsSHA256: activeSHA,
		HostKeyFingerprint: profile.Queued.HostKeyFingerprint, BackupDirectory: backup.Directory(),
		BackupManifestSHA256: manifestSHA, KnownHostsBackupPath: knownHostsBackupPath, UpdatedAtUTC: time.Now().UTC(),
	}
	if err := writeCatalogKnownHostsRefreshEvidence(evidencePath, evidence, ""); err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}
	if expectedKnownHostsSHA256 != activeSHA {
		if err := replaceOwnedSimpleRegularFile(profile.Queued.KnownHostsFile, expectedKnownHostsSHA256, knownHostsAfter, 64*1024); err != nil {
			return CatalogKnownHostsRefreshResult{}, err
		}
	}
	installed, err := readOwnedSimpleRegularFile(profile.Queued.KnownHostsFile, 64*1024)
	if err != nil || digestBytes(installed) != activeSHA {
		return CatalogKnownHostsRefreshResult{}, ErrSimpleLanePreflight
	}
	if _, err := reduceReplacementKnownHosts(installed, "129.151.232.40", profile.Queued.Host, profile.Queued.HostKeyFingerprint, false); err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}
	evidence.Stage = "complete"
	evidence.UpdatedAtUTC = time.Now().UTC()
	if err := writeCatalogKnownHostsRefreshEvidence(evidencePath, evidence, digestFileSHA(evidencePath)); err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}
	evidenceBytes, err := readOwnedSimpleRegularFile(evidencePath, 32*1024)
	if err != nil {
		return CatalogKnownHostsRefreshResult{}, err
	}
	return CatalogKnownHostsRefreshResult{
		HostID: plan.HostID, Lane: plan.Lane, ConfigurationSHA256: expectedConfigurationSHA256,
		PreviousKnownHostsSHA256: expectedKnownHostsSHA256, KnownHostsSHA256: activeSHA,
		HostKeyFingerprint: profile.Queued.HostKeyFingerprint, BackupDirectory: backup.Directory(),
		BackupManifestSHA256: manifestSHA, KnownHostsBackupPath: knownHostsBackupPath,
		EvidencePath: evidencePath, EvidenceSHA256: digestBytes(evidenceBytes), WorkInventoryEmpty: true, ServiceStopped: true,
	}, nil
}

// reduceReplacementKnownHosts returns the one exact current endpoint key while
// validating and preserving the full retired/current file separately. It
// rejects hashed, wildcard, unknown, malformed, duplicate, or mismatched
// endpoint entries; only the explicit retired address may accompany the
// replacement address in the input.
func reduceReplacementKnownHosts(existing []byte, retiredHost, targetHost, expectedFingerprint string, requireRetired bool) ([]byte, error) {
	if len(existing) == 0 || len(existing) > 64*1024 || retiredHost != "129.151.232.40" ||
		targetHost != config.SimpleReplacementLinuxPublicIP || retiredHost == targetHost ||
		!strings.HasPrefix(expectedFingerprint, "sha256:") ||
		!validConfigDigest(strings.TrimPrefix(expectedFingerprint, "sha256:")) {
		return nil, ErrSimpleLanePreflight
	}
	var activeLine string
	var retiredFingerprint string
	retiredEntries, targetEntries := 0, 0
	for _, rawLine := range strings.Split(string(existing), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.Contains(fields[0], ",") {
			return nil, ErrSimpleLanePreflight
		}
		key, err := parseEd25519PublicKey(fields[1] + " " + fields[2])
		if err != nil {
			return nil, ErrSimpleLanePreflight
		}
		fingerprint := lowerHexFingerprint(key.blob)
		switch fields[0] {
		case retiredHost, "[" + retiredHost + "]:22":
			retiredEntries++
			if retiredFingerprint != "" && retiredFingerprint != fingerprint {
				return nil, ErrSimpleLanePreflight
			}
			retiredFingerprint = fingerprint
		case targetHost:
			targetEntries++
			if fingerprint != expectedFingerprint || targetEntries > 1 {
				return nil, ErrSimpleLanePreflight
			}
			activeLine = targetHost + " " + key.keyType + " " + key.keyData + "\n"
		default:
			return nil, ErrSimpleLanePreflight
		}
	}
	if targetEntries != 1 || (requireRetired && retiredEntries == 0 && string(existing) != activeLine) {
		return nil, ErrSimpleLanePreflight
	}
	return []byte(activeLine), nil
}

func preserveKnownHostsRefreshBackup(backupDirectory string, contents []byte, expectedSHA256 string) (string, error) {
	if !filepath.IsAbs(backupDirectory) || filepath.Clean(backupDirectory) != backupDirectory ||
		!validConfigDigest(expectedSHA256) || digestBytes(contents) != expectedSHA256 || len(contents) == 0 || len(contents) > 64*1024 {
		return "", ErrSimpleBackupRequired
	}
	backupDirectoryInfo, err := os.Lstat(backupDirectory)
	if err != nil || backupDirectoryInfo.Mode()&os.ModeSymlink != 0 || !backupDirectoryInfo.IsDir() || backupDirectoryInfo.Mode().Perm() != 0o700 {
		return "", ErrSimpleBackupRequired
	}
	backupPath := backupDirectory + ".known_hosts.before"
	if err := writePrivateFile(backupPath, contents, 0o600); err != nil {
		return "", err
	}
	verified, err := readOwnedSimpleRegularFile(backupPath, 64*1024)
	if err != nil || digestBytes(verified) != expectedSHA256 {
		return "", ErrSimpleBackupRequired
	}
	return backupPath, nil
}

func writeCatalogKnownHostsRefreshEvidence(path string, evidence catalogKnownHostsRefreshEvidence, expectedSHA string) error {
	contents, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if expectedSHA == "" {
		if err := writePrivateFile(path, contents, 0o600); err != nil {
			return err
		}
		return syncSimpleDirectory(filepath.Dir(path))
	}
	if !validConfigDigest(expectedSHA) {
		return errors.New("invalid known-host refresh evidence predecessor")
	}
	return replaceOwnedSimpleRegularFile(path, expectedSHA, contents, 32*1024)
}
