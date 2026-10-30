package laneinstall

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"time"

	"remote-session-runner/src/internal/config"
)

// BuildPRDReplacementMacServerTrust plans public server trust without writing
// files. The original bytes must come from the journal-bound predecessor or
// its verified backup, never from an unverified partial installation.
func BuildPRDReplacementMacServerTrust(macConfiguration, previousCA, replacementCA []byte, now time.Time) ([]byte, error) {
	return buildPRDReplacementMacServerTrust(macConfiguration, previousCA, replacementCA, now)
}

// preparePRDReplacementMacRouteServerTrust is the pre-effect guard used by
// the route installer. For a recorded backup, the caller first validates its
// protected location and manifest digest against the route journal. This
// guard then uses that immutable predecessor rather than trusting whatever
// a partial installation left in the live CA file.
func preparePRDReplacementMacRouteServerTrust(macConfiguration, currentCA, replacementCA []byte,
	journal *prdReplacementMacRouteJournal, originalBackup *PRDBackupManifest, now time.Time) ([]byte, error) {
	originalCA := currentCA
	if journal == nil {
		if originalBackup != nil {
			return nil, ErrCatalogOperationInvalid
		}
	} else if journal.BackupDirectory != "" {
		if originalBackup == nil {
			return nil, ErrSimpleBackupRequired
		}
		identity, err := config.InspectPRDReplacementMacLegacyIdentity(macConfiguration)
		if err != nil {
			return nil, config.ErrInvalidLaneProfiles
		}
		originalCA, err = readPRDReplacementMacRouteServerCABackup(*originalBackup, identity.ServerCAPath, journal.PreviousServerCASHA256)
		if err != nil {
			return nil, err
		}
	} else {
		if originalBackup != nil {
			return nil, ErrCatalogOperationInvalid
		}
		if digestBytes(currentCA) != journal.PreviousServerCASHA256 {
			return nil, ErrCatalogSelectorConflict
		}
	}
	candidate, err := buildPRDReplacementMacServerTrust(macConfiguration, originalCA, replacementCA, now)
	if err != nil {
		return nil, err
	}
	if journal != nil && digestBytes(candidate) != journal.ServerCASHA256 {
		// In particular, a historical journal that planned to discard a
		// shared peer CA must not silently endorse that destructive target.
		return nil, ErrCatalogOperationInvalid
	}
	if journal != nil && journal.BackupDirectory != "" {
		currentSHA := digestBytes(currentCA)
		if currentSHA != journal.PreviousServerCASHA256 && currentSHA != journal.ServerCASHA256 {
			return nil, ErrCatalogSelectorConflict
		}
	}
	return candidate, nil
}

func buildPRDReplacementMacServerTrust(macConfiguration, previousCA, replacementCA []byte, now time.Time) ([]byte, error) {
	identity, err := config.InspectPRDReplacementMacLegacyIdentity(macConfiguration)
	if err != nil {
		return nil, config.ErrInvalidLaneProfiles
	}
	loaded, err := config.ParseBytes(macConfiguration)
	if err != nil {
		return nil, config.ErrInvalidLaneProfiles
	}
	newRoots, err := parsePRDReplacementTrustRoots(replacementCA, now)
	if err != nil || len(newRoots) != 1 {
		return nil, ErrInvalidLaneTLSMaterial
	}
	shared := false
	for _, name := range loaded.RemoteHostNames() {
		if name == "linux-host" {
			continue
		}
		peer, ok := loaded.RemoteHost(name)
		if ok && peer.DirectEndpoint != nil && peer.DirectEndpoint.ServerCA == identity.ServerCAPath {
			shared = true
		}
	}
	if !shared {
		return append([]byte(nil), replacementCA...), nil
	}
	originalRoots, err := parsePRDReplacementTrustRoots(previousCA, now)
	if err != nil {
		return nil, err
	}
	for _, root := range originalRoots {
		if bytes.Equal(root.Raw, newRoots[0].Raw) {
			return append([]byte(nil), previousCA...), nil
		}
	}
	if len(originalRoots) >= 32 {
		return nil, ErrInvalidLaneTLSMaterial
	}
	// Preserve the exact approved original bytes and their ordering. This also
	// makes retry planning stable when the replacement is already present.
	result := append([]byte(nil), previousCA...)
	if !bytes.HasSuffix(result, []byte("\n")) {
		result = append(result, '\n')
	}
	result = append(result, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newRoots[0].Raw})...)
	if len(result) > 1<<20 {
		return nil, ErrInvalidLaneTLSMaterial
	}
	return result, nil
}

func parsePRDReplacementTrustRoots(contents []byte, now time.Time) ([]*x509.Certificate, error) {
	if len(contents) == 0 || len(contents) > 1<<20 || now.IsZero() {
		return nil, ErrInvalidLaneTLSMaterial
	}
	rest := bytes.TrimSpace(contents)
	var roots []*x509.Certificate
	for len(rest) != 0 {
		if len(roots) >= 32 || !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, ErrInvalidLaneTLSMaterial
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, ErrInvalidLaneTLSMaterial
		}
		root, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !root.IsCA || !root.BasicConstraintsValid || root.KeyUsage&x509.KeyUsageCertSign == 0 ||
			root.CheckSignatureFrom(root) != nil || now.Before(root.NotBefore) || !now.Before(root.NotAfter) {
			return nil, ErrInvalidLaneTLSMaterial
		}
		roots = append(roots, root)
		rest = bytes.TrimSpace(remaining)
	}
	if len(roots) == 0 {
		return nil, ErrInvalidLaneTLSMaterial
	}
	return roots, nil
}

func readPRDReplacementMacRouteServerCABackup(manifest PRDBackupManifest, target, expectedSHA string) ([]byte, error) {
	if !bundleDigestPattern.MatchString(expectedSHA) ||
		verifyPRDReplacementMacRouteBackupFiles(manifest, map[string]string{target: expectedSHA}) != nil {
		return nil, ErrSimpleBackupRequired
	}
	for _, file := range manifest.Files {
		if file.TargetPath != target {
			continue
		}
		contents, err := readOwnedSimpleRegularFile(filepath.Join(filepath.Dir(manifest.DatabaseBackupPath), file.BackupPath), 1<<20)
		if err != nil || digestBytes(contents) != expectedSHA {
			return nil, ErrSimpleBackupRequired
		}
		return contents, nil
	}
	return nil, ErrSimpleBackupRequired
}
