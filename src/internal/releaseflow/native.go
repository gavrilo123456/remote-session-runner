package releaseflow

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
	"strings"
	"syscall"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

type HostProfile struct {
	HostID                  string `json:"host_id"`
	Target                  string `json:"ssh_target,omitempty"`
	IdentityFile            string `json:"identity_file,omitempty"`
	KnownHostsFile          string `json:"known_hosts_file,omitempty"`
	GitIdentityFile         string `json:"git_identity_file"`
	GitKnownHostsFile       string `json:"git_known_hosts_file"`
	Repository              string `json:"repository"`
	GoBinary                string `json:"go_binary"`
	RuntimeRoot             string `json:"runtime_root"`
	LegacyCompatibilityFile string `json:"legacy_compatibility_file,omitempty"`
}

type HostConfiguration struct {
	FormatVersion int                    `json:"format_version"`
	Hosts         map[string]HostProfile `json:"hosts"`
}

var safeTargetPattern = regexp.MustCompile(`^[A-Za-z0-9_.@:-]{1,255}$`)

func LoadHostConfiguration(path string) (HostConfiguration, error) {
	var cfg HostConfiguration
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return cfg, ErrInvalidInput
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return cfg, fmt.Errorf("host configuration must be an owner-only regular 0600 file: %w", ErrOperationCorrupted)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return cfg, fmt.Errorf("host configuration is not owned by the current operator: %w", ErrOperationCorrupted)
	}
	if info.Size() > 64<<10 {
		return cfg, ErrOperationCorrupted
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode host configuration: %w", ErrOperationCorrupted)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return cfg, ErrOperationCorrupted
	}
	if cfg.FormatVersion != 1 || len(cfg.Hosts) != len(HostOrder) {
		return cfg, ErrInvalidInput
	}
	for _, host := range HostOrder {
		p, ok := cfg.Hosts[host]
		if !ok || p.HostID != host || !absoluteClean(p.Repository) || !absoluteClean(p.GoBinary) || !absoluteClean(p.RuntimeRoot) {
			return cfg, ErrInvalidInput
		}
		if !absoluteClean(p.GitIdentityFile) || !absoluteClean(p.GitKnownHostsFile) {
			return cfg, ErrInvalidInput
		}
		if p.LegacyCompatibilityFile != "" && !absoluteClean(p.LegacyCompatibilityFile) {
			return cfg, ErrInvalidInput
		}
		if host == "mac-control-plane" {
			if p.Target != "" || p.IdentityFile != "" || p.KnownHostsFile != "" {
				return cfg, ErrInvalidInput
			}
			if err := ownerPrivateKeyFile(p.GitIdentityFile); err != nil {
				return cfg, fmt.Errorf("%s Git identity file: %w", host, err)
			}
			if err := ownerFile(p.GitKnownHostsFile, 0); err != nil {
				return cfg, fmt.Errorf("%s Git known-hosts file: %w", host, err)
			}
		} else {
			if !safeTargetPattern.MatchString(p.Target) || strings.HasPrefix(p.Target, "-") || !absoluteClean(p.IdentityFile) || !absoluteClean(p.KnownHostsFile) {
				return cfg, ErrInvalidInput
			}
			if err := ownerPrivateKeyFile(p.IdentityFile); err != nil {
				return cfg, fmt.Errorf("%s SSH identity file: %w", host, err)
			}
			if err := ownerFile(p.KnownHostsFile, 0); err != nil {
				return cfg, fmt.Errorf("%s SSH known-hosts file: %w", host, err)
			}
		}
	}
	return cfg, nil
}

func fileSHA256(path string) (string, error) {
	if _, err := laneinstall.LoadCatalogLegacyRollbackCompatibilityFile(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func ownerFile(path string, required os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrOperationCorrupted
	}
	if required != 0 && info.Mode().Perm() != required {
		return ErrOperationCorrupted
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return ErrOperationCorrupted
	}
	return nil
}

// ownerPrivateKeyFile accepts the two common owner-only private-key modes.
// Existing verified keys may be read-only (0400) or owner-readable/writable
// (0600); group/world access is never permitted.
func ownerPrivateKeyFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrOperationCorrupted
	}
	if mode := info.Mode().Perm(); mode != 0o400 && mode != 0o600 {
		return ErrOperationCorrupted
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return ErrOperationCorrupted
	}
	return nil
}

func absoluteClean(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }

// NativeBuildBackend builds Mac bundles locally and the three Linux bundles
// with a host-native helper invocation over verified SSH. The helper always
// comes from the clean synchronized dev controller checkout; candidate source
// is acquired in a separate detached worktree and never executed as tooling.
type NativeBuildBackend struct {
	Config             HostConfiguration
	Repository         string
	StoreRoot          string
	ControllerRevision string
	GitSSHCommand      string
}

func (b *NativeBuildBackend) BuildNative(ctx context.Context, host string, source Source, buildID, releaseID string) (BuildArtifact, error) {
	profile, ok := b.Config.Hosts[host]
	if !ok {
		return BuildArtifact{}, ErrInvalidInput
	}
	output := filepath.Join(profile.RuntimeRoot, "builds", buildID, host, "bundle")
	if host == "mac-control-plane" {
		worktree, cleanup, err := b.localFrozenWorktree(ctx, profile, source, buildID)
		if err != nil {
			return BuildArtifact{}, err
		}
		defer cleanup()
		plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLaneDev)
		if err != nil {
			return BuildArtifact{}, err
		}
		bundle, err := laneinstall.BuildSimpleBundleAtFrozenRevision(ctx, plan, worktree, profile.GoBinary, source.Commit)
		if err != nil {
			return BuildArtifact{}, err
		}
		if err := ensureArtifactDirectory(filepath.Dir(output)); err != nil {
			return BuildArtifact{}, err
		}
		if err := writeOrVerifyBuildBundle(output, bundle); err != nil {
			return BuildArtifact{}, err
		}
		artifact := describeBundle(host, output, bundle)
		settings, err := hostBuildSettings(host, profile.GoBinary, bundle.Manifest.GOOS, bundle.Manifest.GOARCH, bundle.Manifest.SourceRevision)
		if err != nil {
			return BuildArtifact{}, err
		}
		artifact.Settings = settings
		return artifact, nil
	}
	args := []string{"host-build", "--host", host, "--repository", profile.Repository, "--go", profile.GoBinary, "--runtime-root", profile.RuntimeRoot, "--branch", source.Branch, "--branch-tip", source.CapturedTip, "--commit", source.Commit, "--build-id", buildID, "--release-id", releaseID, "--controller-revision", b.ControllerRevision, "--git-identity-file", profile.GitIdentityFile, "--git-known-hosts-file", profile.GitKnownHostsFile}
	out, err := b.remote(ctx, profile, args, nil)
	if err != nil {
		return BuildArtifact{}, err
	}
	var built HostBuildResult
	if err := json.Unmarshal(out, &built); err != nil {
		return BuildArtifact{}, fmt.Errorf("decode %s native build manifest: %w", host, err)
	}
	if !validHostBuildSettings(built.Settings, host, built.Bundle) {
		return BuildArtifact{}, ErrOperationCorrupted
	}
	return BuildArtifact{Bundle: built.Bundle, Settings: built.Settings, Path: "remote:" + host + ":" + output}, nil
}

func (b *NativeBuildBackend) VerifyArtifact(ctx context.Context, host, path, want string) error {
	profile, ok := b.Config.Hosts[host]
	if !ok {
		return ErrInvalidInput
	}
	if host == "mac-control-plane" {
		bundle, err := laneinstall.LoadSimpleBundleDirectory(path)
		if err != nil {
			return err
		}
		if bundle.Manifest.BundleSHA256 != want {
			return ErrReleaseMismatch
		}
		return nil
	}
	remotePath, err := remoteArtifactPath(host, path)
	if err != nil {
		return err
	}
	out, err := b.remote(ctx, profile, []string{"host-verify-bundle", "--host", host, "--bundle-dir", remotePath, "--bundle-sha256", want}, nil)
	if err != nil {
		return err
	}
	if !bytes.Contains(out, []byte(want)) {
		return ErrReleaseMismatch
	}
	return nil
}

func (b *NativeBuildBackend) PublishNative(ctx context.Context, host string, release Release, path string) error {
	profile, ok := b.Config.Hosts[host]
	if !ok {
		return ErrInvalidInput
	}
	manifest := catalogManifest(release)
	data, err := laneinstall.CatalogReleaseManifestBytes(manifest)
	if err != nil {
		return err
	}
	if host == "mac-control-plane" {
		bundle, err := laneinstall.LoadSimpleBundleDirectory(path)
		if err != nil {
			return err
		}
		plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLaneDev)
		if err != nil {
			return err
		}
		root, err := laneinstall.DefaultCatalogRootForPlan(plan)
		if err != nil {
			return err
		}
		identity, err := laneinstall.PublishCatalogRelease(root, host, manifest, bundle)
		if err != nil {
			return err
		}
		if identity.ReleaseSHA256 != release.Digest {
			return ErrReleaseMismatch
		}
		return nil
	}
	remotePath, err := remoteArtifactPath(host, path)
	if err != nil {
		return err
	}
	_, err = b.remote(ctx, profile, []string{"host-publish", "--host", host, "--bundle-dir", remotePath, "--controller-revision", b.ControllerRevision, "--repository", profile.Repository}, data)
	return err
}

func (b *NativeBuildBackend) InspectPublished(ctx context.Context, host string, release Release) error {
	profile, ok := b.Config.Hosts[host]
	if !ok {
		return ErrInvalidInput
	}
	if host == "mac-control-plane" {
		plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLaneDev)
		if err != nil {
			return err
		}
		root, err := laneinstall.DefaultCatalogRootForPlan(plan)
		if err != nil {
			return err
		}
		status, err := laneinstall.InspectCatalogRelease(root, host, release.ID)
		if err != nil {
			return err
		}
		if status.Identity.ReleaseSHA256 != release.Digest {
			return ErrReleaseMismatch
		}
		return nil
	}
	out, err := b.remote(ctx, profile, []string{"host-inspect-release", "--host", host, "--release-id", release.ID, "--release-sha256", release.Digest, "--controller-revision", b.ControllerRevision, "--repository", profile.Repository}, nil)
	if err != nil {
		return err
	}
	if !bytes.Contains(out, []byte(release.Digest)) {
		return ErrReleaseMismatch
	}
	return nil
}

func (b *NativeBuildBackend) localFrozenWorktree(ctx context.Context, p HostProfile, source Source, buildID string) (string, func(), error) {
	if err := verifyControllerCheckout(ctx, b.Repository, b.ControllerRevision); err != nil {
		return "", nil, err
	}
	fetch := exec.CommandContext(ctx, "git", "-C", b.Repository, "fetch", "--no-tags", "origin", source.CapturedTip)
	if b.GitSSHCommand != "" {
		fetch.Env = append(os.Environ(), "GIT_SSH_COMMAND="+b.GitSSHCommand)
	}
	out, err := fetch.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("fetch frozen source branch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	check := exec.CommandContext(ctx, "git", "-C", b.Repository, "rev-parse", "FETCH_HEAD")
	tip, err := check.Output()
	if err != nil || strings.TrimSpace(string(tip)) != source.CapturedTip {
		return "", nil, ErrReleaseMismatch
	}
	ancestor := exec.CommandContext(ctx, "git", "-C", b.Repository, "merge-base", "--is-ancestor", source.Commit, "FETCH_HEAD")
	if err := ancestor.Run(); err != nil {
		return "", nil, ErrReleaseMismatch
	}
	root := filepath.Join(b.StoreRoot, "worktrees", buildID)
	if err := ensureArtifactDirectory(root); err != nil {
		return "", nil, err
	}
	worktree := filepath.Join(root, "mac-control-plane")
	cmd := exec.CommandContext(ctx, "git", "-C", b.Repository, "worktree", "add", "--detach", worktree, source.Commit)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("create frozen Mac build worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	cleanup := func() { _ = exec.Command("git", "-C", b.Repository, "worktree", "remove", "--force", worktree).Run() }
	return worktree, cleanup, nil
}

func verifyControllerCheckout(ctx context.Context, repo, revision string) error {
	if !absoluteClean(repo) || !shaPattern.MatchString(revision) {
		return ErrInvalidInput
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", "HEAD^{commit}")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != revision {
		return ErrReleaseMismatch
	}
	// Untracked task-owned helpers are preserved; only tracked controller
	// source participates in this pinned operator identity.
	status := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain=v1", "--untracked-files=no")
	dirty, err := status.Output()
	if err != nil || len(dirty) > 0 {
		return ErrOperationCorrupted
	}
	branch := exec.CommandContext(ctx, "git", "-C", repo, "branch", "--show-current")
	name, err := branch.Output()
	if err != nil || strings.TrimSpace(string(name)) != "dev" {
		return ErrOperationCorrupted
	}
	return nil
}

func (b *NativeBuildBackend) remote(ctx context.Context, p HostProfile, args []string, input []byte) ([]byte, error) {
	remoteArgs := []string{"-i", p.IdentityFile, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + p.KnownHostsFile, "-o", "PreferredAuthentications=publickey", p.Target}
	goCmd := shellQuote(p.GoBinary) + " run ./src/cmd/runner-release " + joinQuoted(args)
	command := "cd -- " + shellQuote(p.Repository) + " && GOTOOLCHAIN=local " + goCmd
	cmd := exec.CommandContext(ctx, "ssh", append(remoteArgs, command)...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("verified SSH command for %s failed: %w: %s", p.HostID, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func joinQuoted(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = shellQuote(v)
	}
	return strings.Join(parts, " ")
}
func remoteArtifactPath(host, path string) (string, error) {
	prefix := "remote:" + host + ":"
	if !strings.HasPrefix(path, prefix) {
		return "", ErrOperationCorrupted
	}
	p := strings.TrimPrefix(path, prefix)
	if !absoluteClean(p) || strings.Contains(p, "\x00") {
		return "", ErrOperationCorrupted
	}
	return p, nil
}
func ensureArtifactDirectory(path string) error {
	if !absoluteClean(path) {
		return ErrInvalidInput
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrOperationCorrupted
	}
	return os.Chmod(path, 0o700)
}

// writeOrVerifyBuildBundle creates a missing immutable build artifact, or
// verifies that a retained artifact at the same build ID is byte-identical.
// LoadSimpleBundleDirectory intentionally reports a missing directory as an
// invalid bundle, so check existence before asking it to validate stored data.
func writeOrVerifyBuildBundle(path string, bundle laneinstall.SimpleBundle) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return laneinstall.WriteSimpleBundleDirectory(path, bundle)
	} else if err != nil {
		return err
	}
	existing, err := laneinstall.LoadSimpleBundleDirectory(path)
	if err != nil {
		return err
	}
	if existing.Manifest.BundleSHA256 != bundle.Manifest.BundleSHA256 {
		return ErrReleaseMismatch
	}
	return nil
}

func describeBundle(host, path string, bundle laneinstall.SimpleBundle) BuildArtifact {
	manifest, _ := json.Marshal(bundle.Manifest)
	total := int64(0)
	for _, c := range bundle.Manifest.Components {
		total += int64(c.Bytes)
	}
	sum := sha256.Sum256(manifest)
	return BuildArtifact{Bundle: Bundle{HostID: host, GOOS: bundle.Manifest.GOOS, GOARCH: bundle.Manifest.GOARCH, Toolchain: bundle.Manifest.Toolchain, SourceRevision: bundle.Manifest.SourceRevision, DatabaseSchemaMax: bundle.Manifest.DatabaseSchemaMax, Components: append([]laneinstall.SimpleBundleComponent(nil), bundle.Manifest.Components...), SHA256: bundle.Manifest.BundleSHA256, ManifestSHA: hex.EncodeToString(sum[:]), Bytes: total}, Path: path}
}
func catalogManifest(r Release) laneinstall.CatalogReleaseManifest {
	manifests := map[string]laneinstall.SimpleBundleManifest{}
	for host, b := range r.Bundles {
		manifests[host] = laneinstall.SimpleBundleManifest{FormatVersion: 1, HostID: host, GOOS: b.GOOS, GOARCH: b.GOARCH, Toolchain: b.Toolchain, SourceRevision: b.SourceRevision, DatabaseSchemaMax: b.DatabaseSchemaMax, Components: b.Components, BundleSHA256: b.SHA256}
	}
	manifest, _ := laneinstall.NewCatalogReleaseManifestFromManifests(r.ID, manifests)
	return manifest
}
