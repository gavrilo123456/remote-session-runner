package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

// CatalogConfigRebindResult records the guarded Mac Dev/QAS peer-route update.
// The backup is a consistent selected-lane database/configuration snapshot;
// the command leaves the selected service stopped for candidate promotion.
type CatalogConfigRebindResult struct {
	HostID                   string             `json:"host_id"`
	Lane                     config.ReleaseLane `json:"lane"`
	PreviousConfigurationSHA string             `json:"previous_configuration_sha256"`
	ConfigurationSHA         string             `json:"configuration_sha256"`
	KnownHostsPath           string             `json:"known_hosts_path"`
	PreviousKnownHostsSHA    string             `json:"previous_known_hosts_sha256"`
	KnownHostsSHA            string             `json:"known_hosts_sha256"`
	ReplacementHostKeySHA    string             `json:"replacement_host_key_sha256"`
	RebindEvidencePath       string             `json:"rebind_evidence_path"`
	RebindEvidenceSHA256     string             `json:"rebind_evidence_sha256"`
	BackupDirectory          string             `json:"backup_directory"`
	BackupManifestSHA256     string             `json:"backup_manifest_sha256"`
	WorkInventoryEmpty       bool               `json:"work_inventory_empty"`
	ServiceStopped           bool               `json:"service_stopped"`
}

type catalogConfigRebindEvidence struct {
	FormatVersion            int                `json:"format_version"`
	HostID                   string             `json:"host_id"`
	Lane                     config.ReleaseLane `json:"lane"`
	Stage                    string             `json:"stage"`
	PreviousConfigurationSHA string             `json:"previous_configuration_sha256"`
	ConfigurationSHA         string             `json:"configuration_sha256"`
	KnownHostsPath           string             `json:"known_hosts_path"`
	PreviousKnownHostsSHA    string             `json:"previous_known_hosts_sha256"`
	KnownHostsSHA            string             `json:"known_hosts_sha256"`
	PreviousHostKeySHA       string             `json:"previous_host_key_sha256"`
	ReplacementHostKeySHA    string             `json:"replacement_host_key_sha256"`
	BackupDirectory          string             `json:"backup_directory"`
	BackupManifestSHA256     string             `json:"backup_manifest_sha256"`
	UpdatedAtUTC             time.Time          `json:"updated_at_utc"`
}

// RebindMacReplacementLinuxRoute updates only the Mac's selected Dev/QAS
// linux-host V3 route and adds the matching replacement host key to that
// lane's existing known_hosts file. It preserves the prior known_hosts bytes
// in the verified backup directory. The catalog and lifecycle locks are
// already held by m. It captures the normal consistent backup, then rechecks
// service, selector, configuration and empty-work identities before either
// atomic replacement. The additive known_hosts write happens first so an
// interruption can leave only an unused additional pin, never a route that
// cannot authenticate its host key.
func (m *CatalogLaneMaintenance) RebindMacReplacementLinuxRoute(ctx context.Context, candidate []byte, expectedCurrentSHA256, replacementKnownHost string) (CatalogConfigRebindResult, error) {
	if m == nil || m.closed || m.maintenance == nil || m.maintenance.fresh ||
		m.layout.hostID != "mac-control-plane" || m.layout.lane != config.ReleaseLaneDev && m.layout.lane != config.ReleaseLaneQAS ||
		len(candidate) == 0 || len(candidate) > config.MaxConfigBytes || !validConfigDigest(expectedCurrentSHA256) ||
		len(replacementKnownHost) > 4096 {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	plan := m.maintenance.plan
	if validateSimplePlan(plan) != nil || plan.HostID != m.layout.hostID || plan.Lane != m.layout.lane || plan.Namespace.Host != config.HostKindMac {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	currentBytes, err := readOwnedSimpleConfig(plan.Namespace.ConfigFile)
	if err != nil || digestBytes(currentBytes) != expectedCurrentSHA256 ||
		config.ValidateSimpleMacReplacementLinuxRoute(currentBytes, candidate, plan.Lane) != nil {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	currentConfig, err := config.ParseBytes(currentBytes)
	if err != nil {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	candidateConfig, err := config.ParseBytes(candidate)
	if err != nil {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	currentProfiles, currentOK := currentConfig.LaneProfiles()
	candidateProfiles, candidateOK := candidateConfig.LaneProfiles()
	if !currentOK || !candidateOK || currentProfiles.Host() != config.HostKindMac || candidateProfiles.Host() != config.HostKindMac ||
		currentProfiles.Lane() != plan.Lane || candidateProfiles.Lane() != plan.Lane {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	currentRoute, currentOK := currentProfiles.MacProfile("linux-host")
	candidateRoute, candidateOK := candidateProfiles.MacProfile("linux-host")
	if !currentOK || !candidateOK || currentRoute.Queued.KnownHostsFile != candidateRoute.Queued.KnownHostsFile {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	knownHostsPath := currentRoute.Queued.KnownHostsFile
	knownHostsBefore, err := readOwnedSimpleRegularFile(knownHostsPath, 64*1024)
	if err != nil {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	knownHostsAfter, err := appendReplacementKnownHost(knownHostsBefore, currentRoute.Queued.Host,
		currentRoute.Queued.HostKeyFingerprint, candidateRoute.Queued.Host, candidateRoute.Queued.HostKeyFingerprint, replacementKnownHost)
	if err != nil {
		return CatalogConfigRebindResult{}, err
	}
	previousKnownHostsSHA := digestBytes(knownHostsBefore)
	knownHostsSHA := digestBytes(knownHostsAfter)
	newHostKeySHA := candidateRoute.Queued.HostKeyFingerprint

	backup, err := m.Backup(ctx)
	if err != nil {
		return CatalogConfigRebindResult{}, err
	}
	manifest, err := readSimpleLaneBackupManifest(plan, backup.Directory())
	if err != nil || manifest.ConfigSHA256 != expectedCurrentSHA256 || manifest.DatabaseSchema != m.maintenance.proof.database ||
		!simpleBackupReceiptValid(plan, backup, m.maintenance.proof) {
		return CatalogConfigRebindResult{}, ErrSimpleBackupRequired
	}

	active, err := m.maintenance.service.Status(ctx, plan)
	if err != nil || active {
		return CatalogConfigRebindResult{}, ErrSimpleLaneNotQuiescent
	}
	currentBundle, err := simpleCurrentBundleForPlan(plan)
	if err != nil || currentBundle != m.maintenance.proof.current {
		return CatalogConfigRebindResult{}, ErrCatalogSelectorConflict
	}
	proof, err := readSimpleLaneQuiescence(ctx, plan, currentBundle)
	if err != nil || proof.database != m.maintenance.proof.database || !simpleMetricsEmpty(proof.metrics) ||
		!simpleBackupReceiptValid(plan, backup, proof) {
		return CatalogConfigRebindResult{}, ErrSimpleLaneNotQuiescent
	}
	latest, err := readOwnedSimpleConfig(plan.Namespace.ConfigFile)
	if err != nil || digestBytes(latest) != expectedCurrentSHA256 ||
		config.ValidateSimpleMacReplacementLinuxRoute(latest, candidate, plan.Lane) != nil {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	active, err = m.maintenance.service.Status(ctx, plan)
	if err != nil || active {
		return CatalogConfigRebindResult{}, ErrSimpleLaneNotQuiescent
	}
	latestKnownHosts, err := readOwnedSimpleRegularFile(knownHostsPath, 64*1024)
	if err != nil || digestBytes(latestKnownHosts) != previousKnownHostsSHA {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	knownHostsBeforePath := filepath.Join(backup.Directory(), "known_hosts.before")
	if err := writePrivateFile(knownHostsBeforePath, knownHostsBefore, 0o600); err != nil {
		return CatalogConfigRebindResult{}, err
	}
	evidencePath := filepath.Join(backup.Directory(), "config-rebind.json")
	evidence := catalogConfigRebindEvidence{
		FormatVersion: 1, HostID: plan.HostID, Lane: plan.Lane, Stage: "prepared",
		PreviousConfigurationSHA: expectedCurrentSHA256, ConfigurationSHA: digestBytes(candidate),
		KnownHostsPath: knownHostsPath, PreviousKnownHostsSHA: previousKnownHostsSHA,
		KnownHostsSHA: knownHostsSHA, PreviousHostKeySHA: currentRoute.Queued.HostKeyFingerprint,
		ReplacementHostKeySHA: newHostKeySHA, BackupDirectory: backup.Directory(),
		BackupManifestSHA256: backup.ManifestSHA256(), UpdatedAtUTC: time.Now().UTC(),
	}
	if err := writeCatalogConfigRebindEvidence(evidencePath, evidence, ""); err != nil {
		return CatalogConfigRebindResult{}, err
	}
	if knownHostsSHA != previousKnownHostsSHA {
		if err := replaceOwnedSimpleRegularFile(knownHostsPath, previousKnownHostsSHA, knownHostsAfter, 64*1024); err != nil {
			return CatalogConfigRebindResult{}, err
		}
	}
	installedKnownHosts, err := readOwnedSimpleRegularFile(knownHostsPath, 64*1024)
	if err != nil || digestBytes(installedKnownHosts) != knownHostsSHA {
		return CatalogConfigRebindResult{}, ErrSimpleLanePreflight
	}
	evidence.Stage = "known_hosts_updated"
	evidence.UpdatedAtUTC = time.Now().UTC()
	if err := writeCatalogConfigRebindEvidence(evidencePath, evidence, digestFileSHA(evidencePath)); err != nil {
		return CatalogConfigRebindResult{}, err
	}

	newSHA := digestBytes(candidate)
	if err := replaceOwnedSimpleConfig(plan.Namespace.ConfigFile, expectedCurrentSHA256, candidate); err != nil {
		if installed, readErr := readOwnedSimpleConfig(plan.Namespace.ConfigFile); readErr == nil && digestBytes(installed) != expectedCurrentSHA256 {
			rollbackErr := replaceOwnedSimpleConfig(plan.Namespace.ConfigFile, digestBytes(installed), currentBytes)
			return CatalogConfigRebindResult{}, errors.Join(err, rollbackErr)
		}
		return CatalogConfigRebindResult{}, err
	}
	if loaded, err := config.LoadFile(plan.Namespace.ConfigFile); err != nil || loaded.ReleaseLane() != plan.Lane ||
		loaded.Kind() != config.HostKindMac || loaded.SchemaVersion() != config.VersionV3 {
		rollbackErr := replaceOwnedSimpleConfig(plan.Namespace.ConfigFile, newSHA, currentBytes)
		evidence.Stage = "configuration_reverted"
		evidence.ConfigurationSHA = expectedCurrentSHA256
		evidence.UpdatedAtUTC = time.Now().UTC()
		evidenceErr := writeCatalogConfigRebindEvidence(evidencePath, evidence, digestFileSHA(evidencePath))
		return CatalogConfigRebindResult{}, errors.Join(ErrSimpleLanePreflight, err, rollbackErr, evidenceErr)
	}
	evidence.Stage = "complete"
	evidence.UpdatedAtUTC = time.Now().UTC()
	if err := writeCatalogConfigRebindEvidence(evidencePath, evidence, digestFileSHA(evidencePath)); err != nil {
		return CatalogConfigRebindResult{}, err
	}
	evidenceBytes, err := readOwnedSimpleRegularFile(evidencePath, 32*1024)
	if err != nil {
		return CatalogConfigRebindResult{}, err
	}
	return CatalogConfigRebindResult{
		HostID: plan.HostID, Lane: plan.Lane, PreviousConfigurationSHA: expectedCurrentSHA256,
		ConfigurationSHA: newSHA, KnownHostsPath: knownHostsPath, PreviousKnownHostsSHA: previousKnownHostsSHA,
		KnownHostsSHA: knownHostsSHA, ReplacementHostKeySHA: newHostKeySHA,
		RebindEvidencePath: evidencePath, RebindEvidenceSHA256: digestBytes(evidenceBytes),
		BackupDirectory: backup.Directory(), BackupManifestSHA256: backup.ManifestSHA256(),
		WorkInventoryEmpty: true, ServiceStopped: true,
	}, nil
}

func appendReplacementKnownHost(existing []byte, oldHost, oldFingerprint, newHost, newFingerprint, replacementLine string) ([]byte, error) {
	if len(existing) > 64*1024 || oldHost == "" || newHost != config.SimpleReplacementLinuxPublicIP || oldHost == newHost ||
		!validConfigDigest(strings.TrimPrefix(oldFingerprint, "sha256:")) || !validConfigDigest(strings.TrimPrefix(newFingerprint, "sha256:")) {
		return nil, ErrSimpleLanePreflight
	}
	line := strings.TrimSpace(replacementLine)
	fields := strings.Fields(line)
	if len(fields) != 3 || fields[0] != newHost || fields[1] != "ssh-ed25519" {
		return nil, ErrSimpleLanePreflight
	}
	newKey, err := parseEd25519PublicKey(fields[1] + " " + fields[2])
	if err != nil || lowerHexFingerprint(newKey.blob) != newFingerprint {
		return nil, ErrSimpleLanePreflight
	}
	oldEntries := 0
	newEntries := 0
	for _, rawLine := range strings.Split(string(existing), "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Fields(trimmed)
		if len(parts) < 3 || parts[1] != "ssh-ed25519" {
			return nil, ErrSimpleLanePreflight
		}
		key, parseErr := parseEd25519PublicKey(parts[1] + " " + parts[2])
		if parseErr != nil {
			return nil, ErrSimpleLanePreflight
		}
		fingerprint := lowerHexFingerprint(key.blob)
		for _, host := range strings.Split(parts[0], ",") {
			switch host {
			case oldHost, "[" + oldHost + "]:22":
				oldEntries++
				if fingerprint != oldFingerprint {
					return nil, ErrSimpleLanePreflight
				}
			case newHost:
				newEntries++
				if fingerprint != newFingerprint {
					return nil, ErrSimpleLanePreflight
				}
			default:
				// This file is scoped to this single remote profile. A wildcard,
				// hashed, or unrelated host entry would make replacement identity
				// ambiguous, so refuse rather than rewrite around it.
				return nil, ErrSimpleLanePreflight
			}
		}
	}
	if oldEntries == 0 {
		return nil, ErrSimpleLanePreflight
	}
	if newEntries > 0 {
		return append([]byte(nil), existing...), nil
	}
	updated := append([]byte(nil), existing...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte(line+"\n")...)
	return updated, nil
}

func writeCatalogConfigRebindEvidence(path string, evidence catalogConfigRebindEvidence, expectedSHA string) error {
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
	return replaceOwnedSimpleRegularFile(path, expectedSHA, contents, 32*1024)
}

func digestFileSHA(path string) string {
	data, err := readOwnedSimpleRegularFile(path, 32*1024)
	if err != nil {
		return ""
	}
	return digestBytes(data)
}

func validConfigDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func digestBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func readOwnedSimpleConfig(path string) ([]byte, error) {
	return readOwnedSimpleRegularFile(path, config.MaxConfigBytes)
}

func readOwnedSimpleRegularFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return nil, ErrSimpleLanePreflight
	}
	var pathStat unix.Stat_t
	if err := unix.Lstat(path, &pathStat); err != nil || pathStat.Uid != uint32(os.Getuid()) || pathStat.Nlink != 1 {
		return nil, ErrSimpleLanePreflight
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrSimpleLanePreflight
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() < 0 || opened.Size() > maximum {
		return nil, ErrSimpleLanePreflight
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrSimpleLanePreflight
	}
	return data, nil
}

func replaceOwnedSimpleConfig(path, expectedSHA string, replacement []byte) error {
	return replaceOwnedSimpleRegularFile(path, expectedSHA, replacement, config.MaxConfigBytes)
}

func replaceOwnedSimpleRegularFile(path, expectedSHA string, replacement []byte, maximum int64) error {
	parent := filepath.Dir(path)
	if err := ensureSimpleOwnedDirectory(parent); err != nil {
		return ErrSimpleLanePreflight
	}
	current, err := readOwnedSimpleRegularFile(path, maximum)
	if err != nil || digestBytes(current) != expectedSHA {
		return ErrSimpleLanePreflight
	}
	if int64(len(replacement)) > maximum {
		return ErrSimpleLanePreflight
	}
	temporary, err := os.CreateTemp(parent, ".runner-lane-config-rebind-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(replacement); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	current, err = readOwnedSimpleRegularFile(path, maximum)
	if err != nil || digestBytes(current) != expectedSHA {
		return ErrSimpleLanePreflight
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := syncSimpleDirectory(parent); err != nil {
		return err
	}
	installed, err := readOwnedSimpleRegularFile(path, maximum)
	if err != nil || digestBytes(installed) != digestBytes(replacement) {
		return ErrSimpleLanePreflight
	}
	return nil
}
