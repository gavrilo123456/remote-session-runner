package laneinstall

import (
	"os"
	"path/filepath"
	"syscall"

	"remote-session-runner/src/internal/config"
)

// validatePRDReplacementMacTrustPathAliases rejects filesystem aliases that
// the pure, exact-path trust planner cannot classify. In particular, APFS
// can expose one ordinary single-link file through two differently cased
// names. Do not infer independent trust files from those path strings.
func validatePRDReplacementMacTrustPathAliases(macConfiguration []byte) error {
	identity, err := config.InspectPRDReplacementMacLegacyIdentity(macConfiguration)
	if err != nil {
		return config.ErrInvalidLaneProfiles
	}
	loaded, err := config.ParseBytes(macConfiguration)
	if err != nil {
		return config.ErrInvalidLaneProfiles
	}
	var peerPaths []string
	for _, name := range loaded.RemoteHostNames() {
		if name == "linux-host" {
			continue
		}
		peer, ok := loaded.RemoteHost(name)
		if ok && peer.DirectEndpoint != nil {
			peerPaths = append(peerPaths, peer.DirectEndpoint.ServerCA)
		}
	}
	return validatePRDReplacementMacTrustFileAliases(identity.ServerCAPath, peerPaths)
}

func validatePRDReplacementMacTrustFileAliases(primary string, peerPaths []string) error {
	primaryInfo, err := prdReplacementOwnedTrustFileInfo(primary)
	if err != nil {
		return err
	}
	observed := map[string]os.FileInfo{primary: primaryInfo}
	for _, path := range peerPaths {
		if _, seen := observed[path]; seen {
			continue // Exact shared paths are handled by the additive planner.
		}
		peerInfo, err := prdReplacementOwnedTrustFileInfo(path)
		if err != nil {
			return err
		}
		if prdReplacementTrustPathsAlias(primary, path, primaryInfo, peerInfo) {
			return ErrCatalogSelectorConflict
		}
		observed[path] = peerInfo
	}
	// A changed path must not turn an observed independent file into an alias
	// while this bounded metadata check is in progress. The installer repeats
	// this guard under its lifecycle lock immediately before its first write.
	for path, before := range observed {
		after, err := prdReplacementOwnedTrustFileInfo(path)
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() ||
			before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return ErrCatalogSelectorConflict
		}
	}
	return nil
}

func prdReplacementTrustPathsAlias(firstPath, secondPath string, first, second os.FileInfo) bool {
	return firstPath != secondPath && os.SameFile(first, second)
}

func prdReplacementOwnedTrustFileInfo(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrSimpleLanePreflight
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, ErrSimpleLanePreflight
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Uid != uint32(os.Getuid()) || identity.Nlink != 1 {
		return nil, ErrSimpleLanePreflight
	}
	return info, nil
}
