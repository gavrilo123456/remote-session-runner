package laneinstall

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrCatalogLegacyBundleUnavailable = errors.New("selected lane has no verified legacy bundle to adopt")

// CopySelectedLegacyBundleForCatalog copies the exact currently selected
// Dev/QAS legacy bundle into an external immutable candidate directory. It
// never changes the selector or installed service and refuses catalog targets.
func CopySelectedLegacyBundleForCatalog(plan Plan, destination string) (SimpleBundleManifest, error) {
	root, err := DefaultCatalogRootForPlan(plan)
	if err != nil {
		return SimpleBundleManifest{}, err
	}
	layout, err := catalogSelectorLayoutForPlan(plan, root)
	if err != nil {
		return SimpleBundleManifest{}, err
	}
	return copySelectedLegacyBundle(layout, destination)
}

func copySelectedLegacyBundle(layout catalogSelectorLayout, destination string) (SimpleBundleManifest, error) {
	if validateCatalogSelectorLayout(layout) != nil || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination ||
		catalogPathWithin(layout.serviceRoot, destination) || catalogPathWithin(layout.catalogRoot, destination) {
		return SimpleBundleManifest{}, ErrInvalidCatalogRelease
	}
	status, err := inspectCatalogSelector(layout)
	if err != nil || status.Kind != "legacy" {
		return SimpleBundleManifest{}, ErrCatalogLegacyBundleUnavailable
	}
	selection, err := resolveCatalogSelectorTarget(layout, status.Target)
	if err != nil || selection.kind != "legacy" || selection.bundle.Manifest.HostID != layout.hostID {
		return SimpleBundleManifest{}, ErrCatalogLegacyBundleUnavailable
	}
	if err := writeSimpleBundleAt(destination, selection.bundle); err != nil {
		return SimpleBundleManifest{}, err
	}
	current, err := inspectCatalogSelector(layout)
	if err != nil || current.Kind != "legacy" || current.Target != status.Target || current.Bundle.BundleSHA256 != status.Bundle.BundleSHA256 {
		return SimpleBundleManifest{}, ErrCatalogSelectorConflict
	}
	return selection.bundle.Manifest, nil
}

func catalogPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
