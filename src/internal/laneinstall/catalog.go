package laneinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"remote-session-runner/src/internal/store"
)

const catalogReleaseFormatVersion = 1
const maxCatalogManifestBytes = 1 << 20

const (
	catalogMigrationPolicy = "forward-on-start-supported-migration-chain"
	catalogRollbackPolicy  = "binary-only-if-current-data-fits-previous-release"
)

var (
	ErrInvalidCatalogRelease  = errors.New("invalid sealed Runner catalog release")
	ErrCatalogReleaseConflict = errors.New("catalog release label already identifies different bytes")
	catalogReleaseIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// CatalogDatabaseCompatibility declares the database boundary sealed with a
// release. Read bounds describe schemas accepted by the supported migration
// chain; write bounds identify the schema emitted after startup migration.
// Binary rollback preserves current data and requires a predecessor-specific
// compatibility check.
type CatalogDatabaseCompatibility struct {
	ReadSchemaMin  int    `json:"read_schema_min"`
	ReadSchemaMax  int    `json:"read_schema_max"`
	WriteSchemaMin int    `json:"write_schema_min"`
	WriteSchemaMax int    `json:"write_schema_max"`
	Migration      string `json:"migration_policy"`
	Rollback       string `json:"rollback_policy"`
}

// CatalogHostArtifact binds one native host bundle and its exact component
// manifest into the four-host release identity. A host stores only its own
// bundle; the cohort manifest contains metadata for all canonical hosts.
type CatalogHostArtifact struct {
	HostID               string `json:"host_id"`
	GOOS                 string `json:"goos"`
	GOARCH               string `json:"goarch"`
	BundleSHA256         string `json:"bundle_sha256"`
	BundleManifestSHA256 string `json:"bundle_manifest_sha256"`
}

// CatalogReleaseManifest is immutable cohort metadata shared byte-for-byte
// across all existing hosts. It contains no mutable lane configuration or
// secret material.
type CatalogReleaseManifest struct {
	FormatVersion  int                          `json:"format_version"`
	ReleaseID      string                       `json:"release_id"`
	SourceRevision string                       `json:"source_revision"`
	Toolchain      string                       `json:"toolchain"`
	Database       CatalogDatabaseCompatibility `json:"database_compatibility"`
	Hosts          []CatalogHostArtifact        `json:"hosts"`
}

// CatalogReleaseIdentity identifies the exact shared cohort metadata and the
// selected host's native bundle. Both digests are required for promotion.
type CatalogReleaseIdentity struct {
	ReleaseID     string `json:"release_id"`
	ReleaseSHA256 string `json:"release_sha256"`
	HostID        string `json:"host_id"`
	BundleSHA256  string `json:"bundle_sha256"`
}

// CatalogReleaseStatus is a validated read-only inspection result. It is
// returned only after the release manifest and all local bundle bytes have
// been checked against the sealed cohort entry.
type CatalogReleaseStatus struct {
	Identity       CatalogReleaseIdentity       `json:"identity"`
	SourceRevision string                       `json:"source_revision"`
	GOOS           string                       `json:"goos"`
	GOARCH         string                       `json:"goarch"`
	Toolchain      string                       `json:"toolchain"`
	Database       CatalogDatabaseCompatibility `json:"database_compatibility"`
	Components     []SimpleBundleComponent      `json:"components"`
}

var canonicalCatalogHosts = []string{"factory-sandbox", "linux-host", "mac-control-plane", "sandbox-host"}

// NewCatalogReleaseManifest seals the four already-validated native bundles
// into deterministic cohort metadata. Every host must have the same source,
// toolchain and schema ceiling. Bundle bytes are not copied into the manifest.
func NewCatalogReleaseManifest(releaseID string, bundles map[string]SimpleBundle) (CatalogReleaseManifest, error) {
	manifests := make(map[string]SimpleBundleManifest, len(bundles))
	for hostID, bundle := range bundles {
		if ValidateSimpleBundle(bundle) != nil {
			return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
		}
		manifests[hostID] = bundle.Manifest
	}
	return NewCatalogReleaseManifestFromManifests(releaseID, manifests)
}

// NewCatalogReleaseManifestFromManifests seals validated per-host bundle
// metadata without collecting foreign-host executables. Each destination host
// still verifies its own complete bundle before publication.
func NewCatalogReleaseManifestFromManifests(releaseID string, manifests map[string]SimpleBundleManifest) (CatalogReleaseManifest, error) {
	if !validCatalogReleaseID(releaseID) || len(manifests) != len(canonicalCatalogHosts) {
		return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
	}
	var sourceRevision, toolchain string
	var schemaMax int
	hosts := make([]CatalogHostArtifact, 0, len(canonicalCatalogHosts))
	for _, hostID := range canonicalCatalogHosts {
		bundleManifest, ok := manifests[hostID]
		if !ok || ValidateSimpleBundleManifest(bundleManifest) != nil || bundleManifest.HostID != hostID {
			return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
		}
		if sourceRevision == "" {
			sourceRevision = bundleManifest.SourceRevision
			toolchain = bundleManifest.Toolchain
			schemaMax = bundleManifest.DatabaseSchemaMax
		} else if bundleManifest.SourceRevision != sourceRevision || bundleManifest.Toolchain != toolchain || bundleManifest.DatabaseSchemaMax != schemaMax {
			return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
		}
		platform := simpleHostPlatforms[hostID]
		hosts = append(hosts, CatalogHostArtifact{
			HostID: hostID, GOOS: platform.goos, GOARCH: platform.goarch,
			BundleSHA256:         bundleManifest.BundleSHA256,
			BundleManifestSHA256: simpleBundleManifestSHA256(bundleManifest),
		})
	}
	for hostID := range manifests {
		if !containsCatalogHost(hostID) {
			return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
		}
	}
	manifest := CatalogReleaseManifest{
		FormatVersion: catalogReleaseFormatVersion,
		ReleaseID:     releaseID, SourceRevision: sourceRevision, Toolchain: toolchain,
		Database: CatalogDatabaseCompatibility{
			ReadSchemaMin: 1, ReadSchemaMax: schemaMax,
			WriteSchemaMin: schemaMax, WriteSchemaMax: schemaMax,
			Migration: catalogMigrationPolicy, Rollback: catalogRollbackPolicy,
		},
		Hosts: hosts,
	}
	if ValidateCatalogReleaseManifest(manifest) != nil {
		return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
	}
	return manifest, nil
}

// LoadSimpleBundleManifest reads bounded, strict metadata from one native
// bundle output. It validates the declaration but deliberately does not claim
// that component bytes were inspected on this machine.
func LoadSimpleBundleManifest(path string) (SimpleBundleManifest, error) {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return SimpleBundleManifest{}, ErrInvalidCatalogRelease
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return SimpleBundleManifest{}, ErrInvalidCatalogRelease
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SimpleBundleManifest{}, err
	}
	var manifest SimpleBundleManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || ValidateSimpleBundleManifest(manifest) != nil {
		return SimpleBundleManifest{}, ErrInvalidCatalogRelease
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return SimpleBundleManifest{}, ErrInvalidCatalogRelease
	}
	return manifest, nil
}

// ValidateSimpleBundleManifest validates sealed metadata without claiming that
// the local executable bytes were inspected. Local publication additionally
// calls ValidateSimpleBundle on the complete bundle.
func ValidateSimpleBundleManifest(manifest SimpleBundleManifest) error {
	platform, ok := simpleHostPlatforms[manifest.HostID]
	wantComponents, componentsOK := simpleHostComponents[manifest.HostID]
	if !ok || !componentsOK || manifest.FormatVersion != simpleBundleFormatVersion ||
		manifest.GOOS != platform.goos || manifest.GOARCH != platform.goarch || manifest.Toolchain != "go1.27.1" ||
		!sourceRevisionPattern.MatchString(manifest.SourceRevision) || manifest.DatabaseSchemaMax != store.CurrentSchemaVersion ||
		!bundleDigestPattern.MatchString(manifest.BundleSHA256) || len(manifest.Components) != len(wantComponents) {
		return ErrInvalidCatalogRelease
	}
	names := append([]string(nil), wantComponents...)
	sort.Strings(names)
	for index, component := range manifest.Components {
		if component.Name != names[index] || !bundleDigestPattern.MatchString(component.SHA256) || component.Bytes <= 0 {
			return ErrInvalidCatalogRelease
		}
	}
	return nil
}

// CatalogHostMatchesPlatform prevents local publish and inspection commands
// from acting under another canonical host's identity.
func CatalogHostMatchesPlatform(hostID, goos, goarch string) bool {
	platform, ok := simpleHostPlatforms[hostID]
	return ok && platform.goos == goos && platform.goarch == goarch
}

// ValidateCatalogReleaseManifest enforces the fixed four-host cohort, source,
// platform, toolchain, schema and compatibility contract.
func ValidateCatalogReleaseManifest(manifest CatalogReleaseManifest) error {
	if manifest.FormatVersion != catalogReleaseFormatVersion || !validCatalogReleaseID(manifest.ReleaseID) ||
		!sourceRevisionPattern.MatchString(manifest.SourceRevision) || manifest.Toolchain != "go1.27.1" ||
		manifest.Database.ReadSchemaMin != 1 || manifest.Database.ReadSchemaMax != store.CurrentSchemaVersion ||
		manifest.Database.WriteSchemaMin != store.CurrentSchemaVersion || manifest.Database.WriteSchemaMax != store.CurrentSchemaVersion ||
		manifest.Database.Migration != catalogMigrationPolicy || manifest.Database.Rollback != catalogRollbackPolicy ||
		len(manifest.Hosts) != len(canonicalCatalogHosts) {
		return ErrInvalidCatalogRelease
	}
	for index, hostID := range canonicalCatalogHosts {
		entry := manifest.Hosts[index]
		platform, ok := simpleHostPlatforms[hostID]
		if !ok || entry.HostID != hostID || entry.GOOS != platform.goos || entry.GOARCH != platform.goarch ||
			!bundleDigestPattern.MatchString(entry.BundleSHA256) || !bundleDigestPattern.MatchString(entry.BundleManifestSHA256) {
			return ErrInvalidCatalogRelease
		}
	}
	return nil
}

// CatalogReleaseManifestBytes returns the canonical JSON representation used
// for the exact release identity on every host.
func CatalogReleaseManifestBytes(manifest CatalogReleaseManifest) ([]byte, error) {
	if err := ValidateCatalogReleaseManifest(manifest); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// LoadCatalogReleaseManifest reads a strict canonical manifest file. Unknown
// fields, trailing data and alternate encodings are rejected.
func LoadCatalogReleaseManifest(path string) (CatalogReleaseManifest, error) {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxCatalogManifestBytes {
		return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogReleaseManifest{}, err
	}
	manifest, err := decodeCatalogReleaseManifest(data)
	if err != nil {
		return CatalogReleaseManifest{}, err
	}
	canonical, err := CatalogReleaseManifestBytes(manifest)
	if err != nil || !bytes.Equal(data, canonical) {
		return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
	}
	return manifest, nil
}

// PublishCatalogRelease atomically publishes the one local host bundle under
// the immutable release label. An exact repeat is idempotent; conflicting or
// tampered bytes are refused. Interrupted staging directories are never
// selectable because inspection addresses only the final validated label.
func PublishCatalogRelease(catalogRoot, hostID string, manifest CatalogReleaseManifest, bundle SimpleBundle) (CatalogReleaseIdentity, error) {
	if ValidateCatalogReleaseManifest(manifest) != nil || !containsCatalogHost(hostID) ||
		bundle.Manifest.HostID != hostID || ValidateSimpleBundle(bundle) != nil ||
		!filepath.IsAbs(catalogRoot) || filepath.Clean(catalogRoot) != catalogRoot {
		return CatalogReleaseIdentity{}, ErrInvalidCatalogRelease
	}
	if err := ensureSimpleDirectoryPath(catalogRoot); err != nil {
		if errors.Is(err, ErrInvalidSimpleBundle) {
			return CatalogReleaseIdentity{}, ErrInvalidCatalogRelease
		}
		return CatalogReleaseIdentity{}, err
	}
	rootInfo, err := os.Lstat(catalogRoot)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 {
		return CatalogReleaseIdentity{}, ErrInvalidCatalogRelease
	}
	if err := validateCatalogRootPath(catalogRoot); err != nil {
		return CatalogReleaseIdentity{}, err
	}
	finalPath := filepath.Join(catalogRoot, manifest.ReleaseID)
	if status, inspectErr := inspectCatalogReleasePath(finalPath, hostID); inspectErr == nil {
		if validateCatalogLocalBundle(manifest, hostID, bundle) == nil && catalogIdentityMatches(status.Identity, manifest, hostID, bundle) {
			return status.Identity, nil
		}
		return CatalogReleaseIdentity{}, ErrCatalogReleaseConflict
	} else if _, statErr := os.Lstat(finalPath); statErr == nil {
		return CatalogReleaseIdentity{}, ErrCatalogReleaseConflict
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return CatalogReleaseIdentity{}, statErr
	}
	if validateCatalogLocalBundle(manifest, hostID, bundle) != nil {
		return CatalogReleaseIdentity{}, ErrInvalidCatalogRelease
	}

	staging, err := os.MkdirTemp(catalogRoot, ".catalog-stage-*")
	if err != nil {
		return CatalogReleaseIdentity{}, fmt.Errorf("create private catalog staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.Chmod(staging, 0o700)
			_ = os.Chmod(filepath.Join(staging, hostID), 0o700)
			_ = os.Chmod(filepath.Join(staging, hostID, "bin"), 0o700)
			_ = os.RemoveAll(staging)
		}
	}()
	manifestBytes, err := CatalogReleaseManifestBytes(manifest)
	if err != nil {
		return CatalogReleaseIdentity{}, err
	}
	if err := writePrivateFile(filepath.Join(staging, "release.json"), manifestBytes, 0o600); err != nil {
		return CatalogReleaseIdentity{}, err
	}
	if err := writeSimpleBundleAt(filepath.Join(staging, hostID), bundle); err != nil {
		return CatalogReleaseIdentity{}, err
	}
	if err := sealCatalogReleaseStaging(staging, hostID); err != nil {
		return CatalogReleaseIdentity{}, err
	}
	if err := os.Rename(staging, finalPath); err != nil {
		// A simultaneous publisher may have won the label. Accept only its exact
		// validated result; never replace or repair an existing sealed version.
		if status, inspectErr := inspectCatalogReleasePath(finalPath, hostID); inspectErr == nil && validateCatalogLocalBundle(manifest, hostID, bundle) == nil && catalogIdentityMatches(status.Identity, manifest, hostID, bundle) {
			return status.Identity, nil
		}
		if _, statErr := os.Lstat(finalPath); statErr == nil {
			return CatalogReleaseIdentity{}, ErrCatalogReleaseConflict
		}
		return CatalogReleaseIdentity{}, fmt.Errorf("atomically publish catalog release: %w", err)
	}
	published = true
	if err := syncSimpleDirectory(catalogRoot); err != nil {
		return CatalogReleaseIdentity{}, err
	}
	status, err := inspectCatalogReleasePath(finalPath, hostID)
	if err != nil || !catalogIdentityMatches(status.Identity, manifest, hostID, bundle) {
		return CatalogReleaseIdentity{}, ErrInvalidCatalogRelease
	}
	return status.Identity, nil
}

// InspectCatalogRelease validates exact cohort metadata and local bundle
// contents without changing the catalog.
func InspectCatalogRelease(catalogRoot, hostID, releaseID string) (CatalogReleaseStatus, error) {
	if !validCatalogReleaseID(releaseID) || !containsCatalogHost(hostID) ||
		!filepath.IsAbs(catalogRoot) || filepath.Clean(catalogRoot) != catalogRoot {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	if err := validateCatalogRootPath(catalogRoot); err != nil {
		return CatalogReleaseStatus{}, err
	}
	return inspectCatalogReleasePath(filepath.Join(catalogRoot, releaseID), hostID)
}

func validateCatalogRootPath(catalogRoot string) error {
	volume := filepath.VolumeName(catalogRoot)
	current := volume + string(filepath.Separator)
	for _, part := range splitPath(catalogRoot[len(volume):]) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalidCatalogRelease
		}
		if current == catalogRoot && info.Mode().Perm() != 0o700 {
			return ErrInvalidCatalogRelease
		}
	}
	return nil
}

func inspectCatalogReleasePath(releasePath, hostID string) (CatalogReleaseStatus, error) {
	info, err := os.Lstat(releasePath)
	if err != nil {
		return CatalogReleaseStatus{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o500 {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	entries, err := os.ReadDir(releasePath)
	if err != nil {
		return CatalogReleaseStatus{}, err
	}
	if len(entries) != 2 {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	localFound, manifestFound := false, false
	for _, entry := range entries {
		switch entry.Name() {
		case hostID:
			localFound = true
		case "release.json":
			manifestFound = true
		default:
			return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
		}
	}
	if !localFound || !manifestFound {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	bundleEntries, err := os.ReadDir(filepath.Join(releasePath, hostID))
	if err != nil || len(bundleEntries) != 2 {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	bundleManifestFound, bundleBinFound := false, false
	for _, entry := range bundleEntries {
		if entry.Type()&os.ModeSymlink != 0 {
			return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
		}
		switch entry.Name() {
		case "manifest.json":
			bundleManifestFound = true
		case "bin":
			bundleBinFound = true
		default:
			return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
		}
	}
	if !bundleManifestFound || !bundleBinFound {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	manifestPath := filepath.Join(releasePath, "release.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != 0o400 || manifestInfo.Size() > maxCatalogManifestBytes || !catalogFileHasSingleLink(manifestInfo) {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return CatalogReleaseStatus{}, err
	}
	manifest, err := decodeCatalogReleaseManifest(manifestBytes)
	if err != nil {
		return CatalogReleaseStatus{}, err
	}
	canonical, err := CatalogReleaseManifestBytes(manifest)
	if err != nil || !bytes.Equal(manifestBytes, canonical) || manifest.ReleaseID != filepath.Base(releasePath) {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	bundle, err := readSimpleBundleAtWithModes(filepath.Join(releasePath, hostID), 0o500, 0o400, 0o500, 0o500)
	if err != nil || validateCatalogLocalBundle(manifest, hostID, bundle) != nil {
		return CatalogReleaseStatus{}, ErrInvalidCatalogRelease
	}
	entry, _ := catalogHostEntry(manifest, hostID)
	manifestHash := sha256.Sum256(manifestBytes)
	return CatalogReleaseStatus{
		Identity:       CatalogReleaseIdentity{ReleaseID: manifest.ReleaseID, ReleaseSHA256: hex.EncodeToString(manifestHash[:]), HostID: hostID, BundleSHA256: entry.BundleSHA256},
		SourceRevision: manifest.SourceRevision, GOOS: bundle.Manifest.GOOS, GOARCH: bundle.Manifest.GOARCH,
		Toolchain: bundle.Manifest.Toolchain, Database: manifest.Database,
		Components: append([]SimpleBundleComponent(nil), bundle.Manifest.Components...),
	}, nil
}

func sealCatalogReleaseStaging(staging, hostID string) error {
	releaseInfo, err := os.Lstat(staging)
	if err != nil || releaseInfo.Mode()&os.ModeSymlink != 0 || !releaseInfo.IsDir() || releaseInfo.Mode().Perm() != 0o700 {
		return ErrInvalidCatalogRelease
	}
	releaseManifest := filepath.Join(staging, "release.json")
	info, err := os.Lstat(releaseManifest)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return ErrInvalidCatalogRelease
	}
	hostRoot := filepath.Join(staging, hostID)
	if _, err := readSimpleBundleAt(hostRoot); err != nil {
		return ErrInvalidCatalogRelease
	}
	binDir := filepath.Join(hostRoot, "bin")
	components, err := os.ReadDir(binDir)
	if err != nil {
		return err
	}
	for _, component := range components {
		path := filepath.Join(binDir, component.Name())
		componentInfo, err := os.Lstat(path)
		if err != nil || componentInfo.Mode()&os.ModeSymlink != 0 || !componentInfo.Mode().IsRegular() || componentInfo.Mode().Perm() != 0o700 {
			return ErrInvalidCatalogRelease
		}
		if err := os.Chmod(path, 0o500); err != nil {
			return err
		}
	}
	if err := os.Chmod(binDir, 0o500); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(hostRoot, "manifest.json"), 0o400); err != nil {
		return err
	}
	if err := os.Chmod(hostRoot, 0o500); err != nil {
		return err
	}
	if err := os.Chmod(releaseManifest, 0o400); err != nil {
		return err
	}
	if err := os.Chmod(staging, 0o500); err != nil {
		return err
	}
	for _, directory := range []string{binDir, hostRoot, staging} {
		if err := syncSimpleDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func validateCatalogLocalBundle(manifest CatalogReleaseManifest, hostID string, bundle SimpleBundle) error {
	if ValidateSimpleBundle(bundle) != nil || bundle.Manifest.HostID != hostID ||
		bundle.Manifest.SourceRevision != manifest.SourceRevision || bundle.Manifest.Toolchain != manifest.Toolchain ||
		bundle.Manifest.DatabaseSchemaMax != manifest.Database.WriteSchemaMax {
		return ErrInvalidCatalogRelease
	}
	entry, ok := catalogHostEntry(manifest, hostID)
	platform := simpleHostPlatforms[hostID]
	if !ok || entry.GOOS != bundle.Manifest.GOOS || entry.GOARCH != bundle.Manifest.GOARCH ||
		entry.GOOS != platform.goos || entry.GOARCH != platform.goarch ||
		entry.BundleSHA256 != bundle.Manifest.BundleSHA256 || entry.BundleManifestSHA256 != simpleBundleManifestSHA256(bundle.Manifest) {
		return ErrInvalidCatalogRelease
	}
	return nil
}

func catalogIdentityMatches(identity CatalogReleaseIdentity, manifest CatalogReleaseManifest, hostID string, bundle SimpleBundle) bool {
	entry, ok := catalogHostEntry(manifest, hostID)
	encoded, err := CatalogReleaseManifestBytes(manifest)
	if !ok || err != nil || identity.ReleaseID != manifest.ReleaseID || identity.HostID != hostID ||
		identity.BundleSHA256 != bundle.Manifest.BundleSHA256 || identity.BundleSHA256 != entry.BundleSHA256 {
		return false
	}
	digest := sha256.Sum256(encoded)
	return identity.ReleaseSHA256 == hex.EncodeToString(digest[:])
}

func decodeCatalogReleaseManifest(data []byte) (CatalogReleaseManifest, error) {
	var manifest CatalogReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF || ValidateCatalogReleaseManifest(manifest) != nil {
		return CatalogReleaseManifest{}, ErrInvalidCatalogRelease
	}
	return manifest, nil
}

func simpleBundleManifestSHA256(manifest SimpleBundleManifest) string {
	encoded, _ := json.MarshalIndent(manifest, "", "  ")
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func catalogHostEntry(manifest CatalogReleaseManifest, hostID string) (CatalogHostArtifact, bool) {
	for _, entry := range manifest.Hosts {
		if entry.HostID == hostID {
			return entry, true
		}
	}
	return CatalogHostArtifact{}, false
}

func validCatalogReleaseID(releaseID string) bool {
	if !catalogReleaseIDPattern.MatchString(releaseID) {
		return false
	}
	switch releaseID {
	case "dev", "qas", "prd", "latest", "current":
		return false
	default:
		return true
	}
}

func containsCatalogHost(hostID string) bool {
	index := sort.SearchStrings(canonicalCatalogHosts, hostID)
	return index < len(canonicalCatalogHosts) && canonicalCatalogHosts[index] == hostID
}
