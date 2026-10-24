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
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

type HostBuildRequest struct {
	HostID             string `json:"host_id"`
	Repository         string `json:"repository"`
	GoBinary           string `json:"go_binary"`
	RuntimeRoot        string `json:"runtime_root"`
	Branch             string `json:"branch"`
	BranchTip          string `json:"branch_tip"`
	Commit             string `json:"commit"`
	BuildID            string `json:"build_id"`
	ReleaseID          string `json:"release_id"`
	ControllerRevision string `json:"controller_revision"`
	GitIdentityFile    string `json:"git_identity_file"`
	GitKnownHostsFile  string `json:"git_known_hosts_file"`
}

type HostBuildResult struct {
	Bundle   Bundle            `json:"bundle"`
	Settings HostBuildSettings `json:"build_settings"`
}

// HostLaneRPCRequest runs a fixed, guarded runner-lane public command from the
// pinned controller checkout. Stdin is material for an existing receipt file,
// never a script or arbitrary shell command.
type HostLaneRPCRequest struct {
	HostID             string   `json:"host_id"`
	Repository         string   `json:"repository"`
	GoBinary           string   `json:"go_binary"`
	ControllerRevision string   `json:"controller_revision"`
	Arguments          []string `json:"arguments"`
	Stdin              []byte   `json:"stdin,omitempty"`
}

var hostLaneCommands = map[string]bool{
	"catalog-status": true, "catalog-inspect": true, "catalog-work-inventory": true,
	"catalog-selector-find":  true,
	"catalog-host-preflight": true, "catalog-cohort": true, "catalog-cohort-renewal": true,
	"catalog-select": true, "catalog-reconcile": true, "catalog-rollback": true,
	"catalog-probe-prepare": true, "catalog-compatibility-check": true,
	"catalog-prd-backup": true, "catalog-prd-recover": true, "prd-preflight": true,
	"prd-status": true, "prd-start": true, "prd-stop": true,
	"status": true, "start": true, "stop": true,
}

func DecodeHostLaneRPCRequest(r io.Reader) (HostLaneRPCRequest, error) {
	var req HostLaneRPCRequest
	d := json.NewDecoder(io.LimitReader(r, (4<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return req, ErrInvalidInput
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return req, ErrInvalidInput
	}
	return req, nil
}

// RunHostLaneRPC executes only existing public runner-lane controls. The
// fixture backend is deliberately not exposed in this production path.
func RunHostLaneRPC(ctx context.Context, req HostLaneRPCRequest) ([]byte, error) {
	if !contains(HostOrder, req.HostID) || !absoluteClean(req.Repository) || !absoluteClean(req.GoBinary) ||
		!shaPattern.MatchString(req.ControllerRevision) || len(req.Arguments) < 1 || len(req.Arguments) > 128 || len(req.Stdin) > 4<<20 ||
		!hostLaneCommands[req.Arguments[0]] {
		return nil, ErrInvalidInput
	}
	for _, arg := range req.Arguments {
		if strings.ContainsRune(arg, '\x00') || len(arg) > 4096 {
			return nil, ErrInvalidInput
		}
	}
	if !laneinstall.CatalogHostMatchesPlatform(req.HostID, runtime.GOOS, runtime.GOARCH) {
		return nil, ErrInvalidInput
	}
	current, err := user.Current()
	if err != nil || (req.HostID == "mac-control-plane" && current.Username != config.MacAccount) ||
		(req.HostID != "mac-control-plane" && current.Username != config.LinuxAccount) {
		return nil, ErrInvalidInput
	}
	if err := verifyControllerCheckout(ctx, req.Repository, req.ControllerRevision); err != nil {
		return nil, err
	}
	args := append([]string(nil), req.Arguments...)
	cleanup := func() {}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--receipt-file" && args[i+1] == "@promo-stdin@" {
			if len(req.Stdin) == 0 {
				return nil, ErrInvalidInput
			}
			path, remove, err := writePrivateTemp(req.Stdin)
			if err != nil {
				return nil, err
			}
			cleanup = remove
			args[i+1] = path
		}
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, req.GoBinary, append([]string{"run", "./src/cmd/runner-lane"}, args...)...)
	cmd.Dir = req.Repository
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	var output limitedBuffer
	output.max = 4 << 20
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.Bytes(), fmt.Errorf("runner-lane command %s failed: %w: %s", req.Arguments[0], err, strings.TrimSpace(output.String()))
	}
	return output.Bytes(), nil
}

func writePrivateTemp(data []byte) (string, func(), error) {
	f, err := os.CreateTemp(os.TempDir(), "promo-rcpt-*")
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	remove := func() { _ = os.Remove(path) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		remove()
		return "", nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		remove()
		return "", nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		remove()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		remove()
		return "", nil, err
	}
	return path, remove, nil
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, errors.New("runner-lane output exceeded limit")
	}
	return b.Buffer.Write(p)
}

func RunHostBuild(ctx context.Context, req HostBuildRequest) (HostBuildResult, error) {
	if !contains(HostOrder, req.HostID) || !absoluteClean(req.Repository) || !absoluteClean(req.GoBinary) || !absoluteClean(req.RuntimeRoot) ||
		!validBranch(req.Branch) || !shaPattern.MatchString(req.BranchTip) || !shaPattern.MatchString(req.Commit) || !operationPattern.MatchString(req.BuildID) ||
		!releasePattern.MatchString(req.ReleaseID) || !shaPattern.MatchString(req.ControllerRevision) || !absoluteClean(req.GitIdentityFile) || !absoluteClean(req.GitKnownHostsFile) {
		return HostBuildResult{}, ErrInvalidInput
	}
	if err := verifyControllerCheckout(ctx, req.Repository, req.ControllerRevision); err != nil {
		return HostBuildResult{}, err
	}
	fetch := exec.CommandContext(ctx, "git", "-C", req.Repository, "-c", "core.sshCommand="+gitSSHCommand(req.GitIdentityFile, req.GitKnownHostsFile), "fetch", "--no-tags", "origin", req.BranchTip)
	if output, err := fetch.CombinedOutput(); err != nil {
		return HostBuildResult{}, fmt.Errorf("fetch frozen candidate commit: %w: %s", err, strings.TrimSpace(string(output)))
	}
	check := exec.CommandContext(ctx, "git", "-C", req.Repository, "rev-parse", "FETCH_HEAD")
	tip, err := check.Output()
	if err != nil || strings.TrimSpace(string(tip)) != req.BranchTip {
		return HostBuildResult{}, ErrReleaseMismatch
	}
	commitPresent := exec.CommandContext(ctx, "git", "-C", req.Repository, "cat-file", "-e", req.Commit+"^{commit}")
	if err := commitPresent.Run(); err != nil {
		return HostBuildResult{}, ErrReleaseMismatch
	}
	ancestor := exec.CommandContext(ctx, "git", "-C", req.Repository, "merge-base", "--is-ancestor", req.Commit, req.BranchTip)
	if err := ancestor.Run(); err != nil {
		return HostBuildResult{}, ErrReleaseMismatch
	}
	if err := checkToolchain(req.GoBinary); err != nil {
		return HostBuildResult{}, err
	}
	worktreeRoot := filepath.Join(req.RuntimeRoot, "worktrees", req.BuildID)
	if err := ensureArtifactDirectory(worktreeRoot); err != nil {
		return HostBuildResult{}, err
	}
	worktree := filepath.Join(worktreeRoot, req.HostID)
	if _, err := os.Lstat(worktree); errors.Is(err, os.ErrNotExist) {
		cmd := exec.CommandContext(ctx, "git", "-C", req.Repository, "worktree", "add", "--detach", worktree, req.Commit)
		if output, err := cmd.CombinedOutput(); err != nil {
			return HostBuildResult{}, fmt.Errorf("create frozen %s build worktree: %w: %s", req.HostID, err, strings.TrimSpace(string(output)))
		}
	} else if err != nil {
		return HostBuildResult{}, err
	} else {
		cmd := exec.CommandContext(ctx, "git", "-C", worktree, "rev-parse", "--verify", "HEAD^{commit}")
		out, e := cmd.Output()
		if e != nil || strings.TrimSpace(string(out)) != req.Commit {
			return HostBuildResult{}, ErrOperationCorrupted
		}
	}
	defer func() { _ = exec.Command("git", "-C", req.Repository, "worktree", "remove", "--force", worktree).Run() }()
	plan, err := laneinstall.BuildCatalogPlan(req.HostID, config.ReleaseLaneDev)
	if err != nil {
		return HostBuildResult{}, err
	}
	bundle, err := laneinstall.BuildSimpleBundleAtFrozenRevision(ctx, plan, worktree, req.GoBinary, req.Commit)
	if err != nil {
		return HostBuildResult{}, err
	}
	output := filepath.Join(req.RuntimeRoot, "builds", req.BuildID, req.HostID, "bundle")
	if err := ensureArtifactDirectory(filepath.Dir(output)); err != nil {
		return HostBuildResult{}, err
	}
	if err := writeOrVerifyBuildBundle(output, bundle); err != nil {
		return HostBuildResult{}, err
	}
	artifact := describeBundle(req.HostID, output, bundle)
	settings, err := hostBuildSettings(req.HostID, req.GoBinary, bundle.Manifest.GOOS, bundle.Manifest.GOARCH, bundle.Manifest.SourceRevision)
	if err != nil {
		return HostBuildResult{}, err
	}
	return HostBuildResult{Bundle: artifact.Bundle, Settings: settings}, nil
}

func RunHostVerifyBundle(host, bundleDir, want string) (Bundle, error) {
	if !contains(HostOrder, host) || !absoluteClean(bundleDir) || !digestPattern.MatchString(want) {
		return Bundle{}, ErrInvalidInput
	}
	bundle, err := laneinstall.LoadSimpleBundleDirectory(bundleDir)
	if err != nil {
		return Bundle{}, err
	}
	if bundle.Manifest.HostID != host || bundle.Manifest.BundleSHA256 != want {
		return Bundle{}, ErrReleaseMismatch
	}
	return describeBundle(host, bundleDir, bundle).Bundle, nil
}

func RunHostPublish(host, bundleDir, controllerRevision string, manifestInput io.Reader, repository string) (laneinstall.CatalogReleaseIdentity, error) {
	if !contains(HostOrder, host) || !absoluteClean(bundleDir) || !shaPattern.MatchString(controllerRevision) || !absoluteClean(repository) {
		return laneinstall.CatalogReleaseIdentity{}, ErrInvalidInput
	}
	if err := verifyControllerCheckout(context.Background(), repository, controllerRevision); err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(manifestInput, (1<<20)+1))
	if err != nil || len(manifestBytes) == 0 || len(manifestBytes) > 1<<20 {
		return laneinstall.CatalogReleaseIdentity{}, ErrInvalidInput
	}
	tmp, err := os.CreateTemp("", "runner-release-manifest-*.json")
	if err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	if _, err := tmp.Write(manifestBytes); err != nil {
		tmp.Close()
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	if err := tmp.Close(); err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	manifest, err := laneinstall.LoadCatalogReleaseManifest(path)
	if err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	bundle, err := laneinstall.LoadSimpleBundleDirectory(bundleDir)
	if err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLaneDev)
	if err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	root, err := laneinstall.DefaultCatalogRootForPlan(plan)
	if err != nil {
		return laneinstall.CatalogReleaseIdentity{}, err
	}
	return laneinstall.PublishCatalogRelease(root, host, manifest, bundle)
}

func RunHostInspect(host, releaseID string, expectedSHA string) (laneinstall.CatalogReleaseStatus, error) {
	if !contains(HostOrder, host) || !releasePattern.MatchString(releaseID) || !digestPattern.MatchString(expectedSHA) {
		return laneinstall.CatalogReleaseStatus{}, ErrInvalidInput
	}
	plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLaneDev)
	if err != nil {
		return laneinstall.CatalogReleaseStatus{}, err
	}
	root, err := laneinstall.DefaultCatalogRootForPlan(plan)
	if err != nil {
		return laneinstall.CatalogReleaseStatus{}, err
	}
	status, err := laneinstall.InspectCatalogRelease(root, host, releaseID)
	if err != nil {
		return status, err
	}
	if status.Identity.ReleaseSHA256 != expectedSHA {
		return status, ErrReleaseMismatch
	}
	return status, nil
}

func checkToolchain(path string) error {
	cmd := exec.Command(path, "version")
	out, err := cmd.Output()
	if err != nil || !strings.HasPrefix(string(out), "go version go1.27.1 ") {
		return errors.New("native release bundle requires Go 1.27.1")
	}
	return nil
}

func hostBuildSettings(host, goBinary, goos, goarch, sourceRevision string) (HostBuildSettings, error) {
	if !shaPattern.MatchString(sourceRevision) {
		return HostBuildSettings{}, ErrInvalidInput
	}
	info, err := os.Lstat(goBinary)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return HostBuildSettings{}, ErrInvalidInput
	}
	f, err := os.Open(goBinary)
	if err != nil {
		return HostBuildSettings{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return HostBuildSettings{}, err
	}
	return HostBuildSettings{HostID: host, ToolchainBinarySHA: hex.EncodeToString(h.Sum(nil)), GOOS: goos, GOARCH: goarch,
		CGOEnabled: goos == "darwin", Flags: hostBuildFlags(sourceRevision)}, nil
}

// hostBuildFlags records the build arguments used by
// laneinstall.BuildSimpleBundleAtFrozenRevision, including the revision
// injected into every candidate binary. Keep this controller-side provenance
// aligned with that native builder.
func hostBuildFlags(sourceRevision string) []string {
	return []string{"-trimpath", "-buildvcs=false", "-ldflags", "-X remote-session-runner/src/internal/buildinfo.SourceRevision=" + sourceRevision}
}
func gitSSHCommand(identity, knownHosts string) string {
	return "ssh -i " + identity + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + knownHosts
}

func DecodeHostBuildRequest(r io.Reader) (HostBuildRequest, error) {
	var req HostBuildRequest
	d := json.NewDecoder(io.LimitReader(r, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return req, ErrInvalidInput
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return req, ErrInvalidInput
	}
	return req, nil
}
