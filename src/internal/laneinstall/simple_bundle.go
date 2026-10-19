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
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const simpleBundleFormatVersion = 1

var (
	ErrInvalidSimpleBundle       = errors.New("invalid existing-host lane bundle")
	ErrSimpleBundleExists        = errors.New("immutable lane bundle destination already contains different bytes")
	ErrSimpleActivationConflict  = errors.New("lane activation changed during the requested operation")
	ErrSimpleRollbackUnavailable = errors.New("previous lane bundle is unavailable or incompatible with the current database")
	sourceRevisionPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	bundleDigestPattern          = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SimpleBundleManifest pins the executable set built from one synchronized
// source revision for one existing host. Lane configuration is deliberately
// outside the bundle so identical per-host bytes can be promoted Dev -> QAS.
type SimpleBundleManifest struct {
	FormatVersion     int                     `json:"format_version"`
	HostID            string                  `json:"host_id"`
	GOOS              string                  `json:"goos"`
	GOARCH            string                  `json:"goarch"`
	Toolchain         string                  `json:"toolchain"`
	SourceRevision    string                  `json:"source_revision"`
	DatabaseSchemaMax int                     `json:"database_schema_max"`
	Components        []SimpleBundleComponent `json:"components"`
	BundleSHA256      string                  `json:"bundle_sha256"`
}

type SimpleBundleComponent struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// SimpleBundle holds a validated manifest and a private copy of each binary.
// Call ValidateSimpleBundle after receiving a value across a trust boundary.
type SimpleBundle struct {
	Manifest   SimpleBundleManifest
	Components map[string][]byte
}

// SimpleActivation is a receipt for one atomic current-release switch. Its
// fields are private so callers cannot manufacture a rollback target.
type SimpleActivation struct {
	hostID    string
	lane      config.ReleaseLane
	previous  string
	activated string
}

func (a SimpleActivation) PreviousBundleSHA256() string  { return a.previous }
func (a SimpleActivation) ActivatedBundleSHA256() string { return a.activated }

// NewSimpleBundle validates and copies the exact binaries required on a
// canonical host. The selected Go version and database schema ceiling come
// from the repository's active toolchain and store schema.
func NewSimpleBundle(hostID, sourceRevision string, components map[string][]byte) (SimpleBundle, error) {
	platform, ok := simpleHostPlatforms[hostID]
	if !ok || !sourceRevisionPattern.MatchString(sourceRevision) {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	wantComponents, ok := simpleHostComponents[hostID]
	if !ok || len(components) != len(wantComponents) {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	owned := make(map[string][]byte, len(components))
	manifestComponents := make([]SimpleBundleComponent, 0, len(components))
	for _, name := range wantComponents {
		data, exists := components[name]
		if !exists || len(data) == 0 {
			return SimpleBundle{}, ErrInvalidSimpleBundle
		}
		copyData := append([]byte(nil), data...)
		owned[name] = copyData
		digest := sha256.Sum256(copyData)
		manifestComponents = append(manifestComponents, SimpleBundleComponent{Name: name, SHA256: hex.EncodeToString(digest[:]), Bytes: len(copyData)})
	}
	sort.Slice(manifestComponents, func(i, j int) bool { return manifestComponents[i].Name < manifestComponents[j].Name })
	manifest := SimpleBundleManifest{
		FormatVersion: simpleBundleFormatVersion,
		HostID:        hostID, GOOS: platform.goos, GOARCH: platform.goarch,
		Toolchain: "go1.27.1", SourceRevision: sourceRevision,
		DatabaseSchemaMax: store.CurrentSchemaVersion, Components: manifestComponents,
	}
	manifest.BundleSHA256 = simpleBundleDigest(manifest, owned)
	bundle := SimpleBundle{Manifest: manifest, Components: owned}
	if err := ValidateSimpleBundle(bundle); err != nil {
		return SimpleBundle{}, err
	}
	return bundle, nil
}

// BuildSimpleBundle builds the fixed component set from a clean, synchronized
// dev checkout with the repository's Go 1.27.1 toolchain. Outputs live in a
// temporary directory outside the checkout and are removed after the
// immutable bundle value has been assembled.
func BuildSimpleBundle(ctx context.Context, plan Plan, repositoryRoot, goBinary string) (SimpleBundle, error) {
	if err := validateSimplePlan(plan); err != nil {
		return SimpleBundle{}, err
	}
	revision, err := requireSynchronizedSimpleCheckout(ctx, repositoryRoot)
	if err != nil {
		return SimpleBundle{}, err
	}
	return buildSimpleBundleAtRevision(ctx, plan, repositoryRoot, goBinary, revision)
}

// BuildSimpleBundleAtRevision builds from a frozen source commit in a clean,
// detached checkout. The pinned commit must be an ancestor of origin/dev and
// every later change on origin/dev must be Markdown-only. This supports the
// source-S / evidence-E workflow without rebuilding release bytes from E.
func BuildSimpleBundleAtRevision(ctx context.Context, plan Plan, repositoryRoot, goBinary, sourceRevision string) (SimpleBundle, error) {
	if err := validateSimplePlan(plan); err != nil {
		return SimpleBundle{}, err
	}
	revision, err := requirePinnedSimpleCheckout(ctx, repositoryRoot, sourceRevision)
	if err != nil {
		return SimpleBundle{}, err
	}
	return buildSimpleBundleAtRevision(ctx, plan, repositoryRoot, goBinary, revision)
}

func buildSimpleBundleAtRevision(ctx context.Context, plan Plan, repositoryRoot, goBinary, revision string) (SimpleBundle, error) {
	if goBinary == "" {
		if runtime.GOOS == "darwin" {
			goBinary = "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go"
		} else if runtime.GOOS == "linux" {
			goBinary = "/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go"
		} else {
			return SimpleBundle{}, ErrInvalidSimpleBundle
		}
	}
	versionCommand := exec.CommandContext(ctx, goBinary, "version")
	versionOutput, err := versionCommand.Output()
	if err != nil {
		return SimpleBundle{}, fmt.Errorf("verify Go 1.27.1 toolchain: %w", err)
	}
	if !strings.HasPrefix(string(versionOutput), "go version go1.27.1 ") {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	temporary, err := os.MkdirTemp("", "remote-session-runner-simple-bundle-")
	if err != nil {
		return SimpleBundle{}, fmt.Errorf("create temporary component build directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	components := make(map[string][]byte, len(simpleHostComponents[plan.HostID]))
	platform := simpleHostPlatforms[plan.HostID]
	if platform.goos == "darwin" && (runtime.GOOS != "darwin" || runtime.GOARCH != platform.goarch) {
		return SimpleBundle{}, errors.New("Mac lane bundle must be built natively with cgo on its target architecture")
	}
	ldflags := "-X remote-session-runner/src/internal/buildinfo.SourceRevision=" + revision
	for _, component := range simpleHostComponents[plan.HostID] {
		outputPath := filepath.Join(temporary, component)
		command := exec.CommandContext(ctx, goBinary, "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", outputPath, "./src/cmd/"+component)
		command.Dir = repositoryRoot
		command.Env = simpleBuildEnvironment(os.Environ(), platform)
		output, buildErr := command.CombinedOutput()
		if buildErr != nil {
			return SimpleBundle{}, fmt.Errorf("build %s for %s/%s: %w: %s", component, platform.goos, platform.goarch, buildErr, strings.TrimSpace(string(output)))
		}
		if err := os.Chmod(outputPath, 0o700); err != nil {
			return SimpleBundle{}, fmt.Errorf("secure %s candidate binary: %w", component, err)
		}
		data, err := os.ReadFile(outputPath)
		if err != nil {
			return SimpleBundle{}, err
		}
		components[component] = data
	}
	return NewSimpleBundle(plan.HostID, revision, components)
}

func requireSynchronizedSimpleCheckout(ctx context.Context, repositoryRoot string) (string, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil || filepath.Clean(root) != root {
		return "", ErrInvalidSimpleBundle
	}
	runGit := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		output, err := command.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(output)), nil
	}
	top, err := runGit("rev-parse", "--show-toplevel")
	if err != nil || top != root {
		return "", ErrInvalidSimpleBundle
	}
	branch, err := runGit("branch", "--show-current")
	if err != nil || branch != "dev" {
		return "", ErrInvalidSimpleBundle
	}
	status, err := runGit("status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", ErrInvalidSimpleBundle
	}
	head, err := runGit("rev-parse", "HEAD")
	if err != nil || !sourceRevisionPattern.MatchString(head) {
		return "", ErrInvalidSimpleBundle
	}
	upstream, err := runGit("rev-parse", "origin/dev")
	if err != nil || head != upstream {
		return "", ErrInvalidSimpleBundle
	}
	return head, nil
}

func requirePinnedSimpleCheckout(ctx context.Context, repositoryRoot, expectedRevision string) (string, error) {
	if !sourceRevisionPattern.MatchString(expectedRevision) {
		return "", ErrInvalidSimpleBundle
	}
	root, err := filepath.Abs(repositoryRoot)
	if err != nil || filepath.Clean(root) != root {
		return "", ErrInvalidSimpleBundle
	}
	runGit := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		return command.Output()
	}
	topBytes, err := runGit("rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(topBytes)) != root {
		return "", ErrInvalidSimpleBundle
	}
	branchBytes, err := runGit("branch", "--show-current")
	if err != nil || strings.TrimSpace(string(branchBytes)) != "" {
		return "", ErrInvalidSimpleBundle
	}
	status, err := runGit("status", "--porcelain", "--untracked-files=all")
	if err != nil || len(status) != 0 {
		return "", ErrInvalidSimpleBundle
	}
	headBytes, err := runGit("rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(headBytes)) != expectedRevision {
		return "", ErrInvalidSimpleBundle
	}
	upstreamBytes, err := runGit("rev-parse", "origin/dev")
	if err != nil {
		return "", ErrInvalidSimpleBundle
	}
	upstream := strings.TrimSpace(string(upstreamBytes))
	if !sourceRevisionPattern.MatchString(upstream) {
		return "", ErrInvalidSimpleBundle
	}
	ancestor := exec.CommandContext(ctx, "git", "-C", root, "merge-base", "--is-ancestor", expectedRevision, upstream)
	if err := ancestor.Run(); err != nil {
		return "", ErrInvalidSimpleBundle
	}
	changed, err := runGit("diff", "--name-only", "-z", expectedRevision, upstream)
	if err != nil {
		return "", ErrInvalidSimpleBundle
	}
	for _, path := range strings.Split(string(changed), "\x00") {
		if path == "" {
			continue
		}
		if filepath.Ext(path) != ".md" {
			return "", ErrInvalidSimpleBundle
		}
	}
	return expectedRevision, nil
}

func simpleBuildEnvironment(source []string, platform simplePlatform) []string {
	cgoEnabled := "0"
	if platform.goos == "darwin" {
		// The Mac runtime uses Darwin libproc through cgo to record and verify
		// process start identity. A CGO-disabled bundle cannot safely start a
		// persistent local session.
		cgoEnabled = "1"
	}
	values := map[string]string{
		"GOOS": platform.goos, "GOARCH": platform.goarch, "CGO_ENABLED": cgoEnabled, "GOTOOLCHAIN": "local",
	}
	if runtime.GOOS == "darwin" {
		values["GOCACHE"] = "/private/tmp/remote-session-runner-gocache"
		values["GOMODCACHE"] = "/private/tmp/remote-session-runner-gomodcache"
	}
	result := make([]string, 0, len(source)+len(values))
	for _, item := range source {
		name, _, ok := strings.Cut(item, "=")
		if ok {
			if _, replace := values[name]; replace {
				continue
			}
		}
		result = append(result, item)
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

// ValidateSimpleBundle checks the closed host/component/platform map and all
// binary, source, toolchain, and bundle digests.
func ValidateSimpleBundle(bundle SimpleBundle) error {
	return validateSimpleBundleSchema(bundle, store.CurrentSchemaVersion)
}

// validateStoredSimpleBundle accepts the exact previous schema ceiling while
// reading an installed immutable release. New candidates still pass through
// ValidateSimpleBundle and must declare the current schema ceiling.
func validateStoredSimpleBundle(bundle SimpleBundle) error {
	return validateSimpleBundleSchema(bundle, store.CurrentSchemaVersion-1)
}

func validateSimpleBundleSchema(bundle SimpleBundle, minimumSchema int) error {
	m := bundle.Manifest
	platform, ok := simpleHostPlatforms[m.HostID]
	components, componentsOK := simpleHostComponents[m.HostID]
	if !ok || !componentsOK || m.FormatVersion != simpleBundleFormatVersion ||
		m.GOOS != platform.goos || m.GOARCH != platform.goarch || m.Toolchain != "go1.27.1" ||
		!sourceRevisionPattern.MatchString(m.SourceRevision) || m.DatabaseSchemaMax < minimumSchema || m.DatabaseSchemaMax > store.CurrentSchemaVersion ||
		len(m.Components) != len(components) || len(bundle.Components) != len(components) {
		return ErrInvalidSimpleBundle
	}
	wantNames := append([]string(nil), components...)
	sort.Strings(wantNames)
	for index, metadata := range m.Components {
		if metadata.Name != wantNames[index] || !bundleDigestPattern.MatchString(metadata.SHA256) || metadata.Bytes <= 0 {
			return ErrInvalidSimpleBundle
		}
		data, exists := bundle.Components[metadata.Name]
		if !exists || len(data) != metadata.Bytes {
			return ErrInvalidSimpleBundle
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != metadata.SHA256 {
			return ErrInvalidSimpleBundle
		}
	}
	if simpleBundleDigest(m, bundle.Components) != m.BundleSHA256 {
		return ErrInvalidSimpleBundle
	}
	return nil
}

type simplePlatform struct{ goos, goarch string }

var simpleHostPlatforms = map[string]simplePlatform{
	"mac-control-plane": {goos: "darwin", goarch: "arm64"},
	"linux-host":        {goos: "linux", goarch: "amd64"},
	"sandbox-host":      {goos: "linux", goarch: "arm64"},
	"factory-sandbox":   {goos: "linux", goarch: "amd64"},
}

var simpleHostComponents = map[string][]string{
	"mac-control-plane": {"runner", "runner-local", "runner-locald", "runner-log-capture"},
	"linux-host":        {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
	"sandbox-host":      {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
	"factory-sandbox":   {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
}

// StageSimpleBundle writes a validated candidate to the selected lane's
// fixed staging directory. Existing content is accepted only when every byte
// and manifest field match; staged candidates are never overwritten.
func StageSimpleBundle(plan Plan, bundle SimpleBundle) (string, error) {
	if err := validateSimplePlan(plan); err != nil || bundle.Manifest.HostID != plan.HostID || ValidateSimpleBundle(bundle) != nil {
		return "", ErrInvalidSimpleBundle
	}
	destination := filepath.Join(plan.Namespace.Staging, "bundles", bundle.Manifest.BundleSHA256)
	if err := writeSimpleBundleAt(destination, bundle); err != nil {
		return "", err
	}
	return destination, nil
}

// WriteSimpleBundleDirectory serializes one validated candidate into an
// immutable directory suitable for transfer to its target existing host.
// Callers should keep this build artifact outside the source checkout.
func WriteSimpleBundleDirectory(destination string, bundle SimpleBundle) error {
	return writeSimpleBundleAt(destination, bundle)
}

// LoadSimpleBundleDirectory reads and revalidates a transferred candidate.
func LoadSimpleBundleDirectory(path string) (SimpleBundle, error) {
	return readSimpleBundleAt(path)
}

// CurrentSimpleBundleSHA256 reports the selected lane's validated active
// release identity; an empty result means no bundle has been activated yet.
func CurrentSimpleBundleSHA256(plan Plan) (string, error) {
	if err := validateSimplePlan(plan); err != nil {
		return "", err
	}
	current, err := simpleCurrentBundleForPlan(plan)
	if err != nil || current == "" {
		return current, err
	}
	_, bundle, err := selectedSimpleBundleForPlan(plan)
	if err != nil || bundle.Manifest.HostID != plan.HostID || bundle.Manifest.BundleSHA256 != current {
		return "", ErrInvalidSimpleBundle
	}
	return current, nil
}

func validateSimplePlan(plan Plan) error {
	namespace, err := config.DeriveSimpleLaneNamespace(plan.Namespace.Host, plan.Lane, plan.Namespace.RemoteProfile)
	if err != nil || (plan.Lane != config.ReleaseLaneDev && plan.Lane != config.ReleaseLaneQAS) ||
		plan.HostID == "" || namespace != plan.Namespace {
		return ErrInvalidSimpleBundle
	}
	if tuple, ok := canonicalHosts[plan.HostID]; !ok || tuple.kind != plan.Namespace.Host || tuple.profile != plan.Namespace.RemoteProfile {
		return ErrInvalidSimpleBundle
	}
	return nil
}

func simpleBundleDigest(manifest SimpleBundleManifest, components map[string][]byte) string {
	manifest.BundleSHA256 = ""
	encoded, _ := json.Marshal(manifest)
	hash := sha256.New()
	_, _ = hash.Write(encoded)
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = io.WriteString(hash, "\x00"+name+"\x00")
		_, _ = hash.Write(components[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeSimpleBundleAt(destination string, bundle SimpleBundle) error {
	if validateStoredSimpleBundle(bundle) != nil || filepath.Clean(destination) != destination || !filepath.IsAbs(destination) {
		return ErrInvalidSimpleBundle
	}
	parent := filepath.Dir(destination)
	if err := ensureSimpleDirectoryPath(parent); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalidSimpleBundle
		}
		existing, readErr := readSimpleBundleAt(destination)
		if readErr == nil && bundlesEqual(existing, bundle) {
			return nil
		}
		return ErrSimpleBundleExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect immutable bundle destination: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".bundle-*")
	if err != nil {
		return fmt.Errorf("create bundle staging directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	binDir := filepath.Join(temporary, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		return err
	}
	for _, component := range bundle.Manifest.Components {
		if err := writePrivateFile(filepath.Join(binDir, component.Name), bundle.Components[component.Name], 0o700); err != nil {
			return err
		}
	}
	manifestBytes, err := json.MarshalIndent(bundle.Manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writePrivateFile(filepath.Join(temporary, "manifest.json"), manifestBytes, 0o600); err != nil {
		return err
	}
	if err := syncSimpleDirectory(binDir); err != nil {
		return err
	}
	if err := syncSimpleDirectory(temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrSimpleBundleExists
		}
		return fmt.Errorf("publish immutable bundle directory: %w", err)
	}
	if err := syncSimpleDirectory(parent); err != nil {
		return err
	}
	return nil
}

func readSimpleBundleAt(path string) (SimpleBundle, error) {
	return readSimpleBundleAtWithModes(path, 0o700, 0o600, 0o700, 0o700)
}

func readSimpleBundleAtWithModes(path string, directoryMode, manifestMode, binMode, componentMode os.FileMode) (SimpleBundle, error) {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != directoryMode {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	manifestFound, binFound := false, false
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return SimpleBundle{}, ErrInvalidSimpleBundle
		}
		switch entry.Name() {
		case "manifest.json":
			manifestFound = true
		case "bin":
			binFound = true
		default:
			return SimpleBundle{}, ErrInvalidSimpleBundle
		}
	}
	if !manifestFound || !binFound {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	manifestPath := filepath.Join(path, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != manifestMode || manifestInfo.Size() > 1<<20 || !catalogFileHasSingleLink(manifestInfo) {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return SimpleBundle{}, err
	}
	var manifest SimpleBundleManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	binDir := filepath.Join(path, "bin")
	binInfo, err := os.Lstat(binDir)
	if err != nil || binInfo.Mode()&os.ModeSymlink != 0 || !binInfo.IsDir() || binInfo.Mode().Perm() != binMode {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	entries, err = os.ReadDir(binDir)
	if err != nil || len(entries) != len(manifest.Components) {
		return SimpleBundle{}, ErrInvalidSimpleBundle
	}
	components := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return SimpleBundle{}, ErrInvalidSimpleBundle
		}
		componentPath := filepath.Join(binDir, entry.Name())
		componentInfo, statErr := os.Lstat(componentPath)
		if statErr != nil || componentInfo.Mode()&os.ModeSymlink != 0 || !componentInfo.Mode().IsRegular() || componentInfo.Mode().Perm() != componentMode || !catalogFileHasSingleLink(componentInfo) {
			return SimpleBundle{}, ErrInvalidSimpleBundle
		}
		data, readErr := os.ReadFile(componentPath)
		if readErr != nil {
			return SimpleBundle{}, readErr
		}
		components[entry.Name()] = data
	}
	bundle := SimpleBundle{Manifest: manifest, Components: components}
	if err := validateStoredSimpleBundle(bundle); err != nil {
		return SimpleBundle{}, err
	}
	return bundle, nil
}

func installSimpleBundleAt(serviceRoot, hostID string, lane config.ReleaseLane, bundle SimpleBundle) (SimpleActivation, error) {
	if ValidateSimpleBundle(bundle) != nil || bundle.Manifest.HostID != hostID ||
		(lane != config.ReleaseLaneDev && lane != config.ReleaseLaneQAS) || filepath.Clean(serviceRoot) != serviceRoot || !filepath.IsAbs(serviceRoot) {
		return SimpleActivation{}, ErrInvalidSimpleBundle
	}
	releases := filepath.Join(serviceRoot, "releases")
	if err := ensureSimpleDirectoryPath(releases); err != nil {
		return SimpleActivation{}, err
	}
	releasePath := filepath.Join(releases, bundle.Manifest.BundleSHA256)
	if err := writeSimpleBundleAt(releasePath, bundle); err != nil {
		return SimpleActivation{}, err
	}
	previous, err := simpleCurrentBundle(serviceRoot)
	if err != nil {
		return SimpleActivation{}, err
	}
	if previous == bundle.Manifest.BundleSHA256 {
		return SimpleActivation{hostID: hostID, lane: lane, previous: previous, activated: previous}, nil
	}
	if err := switchSimpleCurrent(serviceRoot, previous, bundle.Manifest.BundleSHA256); err != nil {
		return SimpleActivation{}, err
	}
	return SimpleActivation{hostID: hostID, lane: lane, previous: previous, activated: bundle.Manifest.BundleSHA256}, nil
}

func rollbackSimpleBundleAt(serviceRoot, hostID string, lane config.ReleaseLane, activation SimpleActivation, currentDatabaseSchema int) error {
	if activation.hostID != hostID || activation.lane != lane || activation.previous == "" ||
		activation.previous == activation.activated || currentDatabaseSchema <= 0 {
		return ErrSimpleRollbackUnavailable
	}
	previousPath := filepath.Join(serviceRoot, "releases", activation.previous)
	previous, err := readSimpleBundleAt(previousPath)
	if err != nil || previous.Manifest.HostID != hostID || currentDatabaseSchema > previous.Manifest.DatabaseSchemaMax {
		return ErrSimpleRollbackUnavailable
	}
	current, err := simpleCurrentBundle(serviceRoot)
	if err != nil || current != activation.activated {
		return ErrSimpleActivationConflict
	}
	return switchSimpleCurrent(serviceRoot, activation.activated, activation.previous)
}

func simpleCurrentBundle(serviceRoot string) (string, error) {
	path := filepath.Join(serviceRoot, "current")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", ErrInvalidSimpleBundle
	}
	target, err := os.Readlink(path)
	if err != nil || filepath.Clean(target) != target || filepath.Dir(target) != "releases" || !bundleDigestPattern.MatchString(filepath.Base(target)) {
		return "", ErrInvalidSimpleBundle
	}
	bundle, err := readSimpleBundleAt(filepath.Join(serviceRoot, target))
	if err != nil || bundle.Manifest.BundleSHA256 != filepath.Base(target) {
		return "", ErrInvalidSimpleBundle
	}
	return bundle.Manifest.BundleSHA256, nil
}

func switchSimpleCurrent(serviceRoot, expectedCurrent, next string) error {
	if !bundleDigestPattern.MatchString(next) {
		return ErrInvalidSimpleBundle
	}
	current, err := simpleCurrentBundle(serviceRoot)
	if err != nil || current != expectedCurrent {
		return ErrSimpleActivationConflict
	}
	target := filepath.Join(serviceRoot, "releases", next)
	if _, err := readSimpleBundleAt(target); err != nil {
		return ErrInvalidSimpleBundle
	}
	if err := ensureSimpleDirectoryPath(serviceRoot); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(serviceRoot, ".activate-*")
	if err != nil {
		return err
	}
	link := filepath.Join(temporary, "current")
	if err := os.Symlink(filepath.Join("releases", next), link); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	current, err = simpleCurrentBundle(serviceRoot)
	if err != nil || current != expectedCurrent {
		_ = os.RemoveAll(temporary)
		return ErrSimpleActivationConflict
	}
	if err := os.Rename(link, filepath.Join(serviceRoot, "current")); err != nil {
		_ = os.RemoveAll(temporary)
		return fmt.Errorf("atomically activate lane bundle: %w", err)
	}
	_ = os.RemoveAll(temporary)
	return syncSimpleDirectory(serviceRoot)
}

func ensureSimpleDirectoryPath(path string) error {
	if filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return ErrInvalidSimpleBundle
	}
	volume := filepath.VolumeName(path)
	remainder := path[len(volume):]
	current := volume + string(filepath.Separator)
	for _, part := range splitPath(remainder) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create private lane directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalidSimpleBundle
		}
	}
	return nil
}

func splitPath(path string) []string {
	parts := make([]string, 0)
	for path != "" && path != string(filepath.Separator) {
		part := filepath.Base(path)
		parts = append(parts, part)
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

func writePrivateFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func syncSimpleDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func bundlesEqual(left, right SimpleBundle) bool {
	if left.Manifest.BundleSHA256 != right.Manifest.BundleSHA256 || len(left.Components) != len(right.Components) {
		return false
	}
	for name, leftData := range left.Components {
		if !bytes.Equal(leftData, right.Components[name]) {
			return false
		}
	}
	return true
}
