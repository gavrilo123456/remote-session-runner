package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

var ErrSimpleArtifactConflict = errors.New("selected lane service artifact differs from the fixed plan")

type simpleArtifactInstaller interface {
	installArtifact(context.Context, Plan, Artifact) error
}

// PrepareLayout creates only the selected lane's account-owned directories.
// Existing work is never removed or chmodded; an unexpected owner, mode,
// symlink, or file at a required directory stops the operation.
func (m *SimpleLaneMaintenance) PrepareLayout(ctx context.Context) error {
	if m == nil || m.closed || validateSimplePlan(m.plan) != nil {
		return ErrSimpleLanePreflight
	}
	active, err := m.service.Status(ctx, m.plan)
	if err != nil {
		return fmt.Errorf("read selected lane service before layout: %w", err)
	}
	if active {
		return ErrSimpleLaneNotQuiescent
	}
	if !m.fresh && (!simpleProofFresh(m.proof, m.plan) || m.backup == nil || !simpleBackupReceiptValid(m.plan, *m.backup, m.proof)) {
		return ErrSimpleBackupRequired
	}
	for _, directory := range simpleLaneDirectories(m.plan.Namespace) {
		if err := ensureSimpleOwnedDirectory(directory); err != nil {
			return err
		}
	}
	if m.fresh {
		if err := writeSimpleInstallMarker(m.plan.Namespace, m.plan.HostID, m.plan.Lane); err != nil {
			return err
		}
	}
	return nil
}

// InstallArtifacts installs the fixed LaunchAgents or systemd/entrypoint
// artifacts for this lane. A prior different file is preserved and rejected;
// these stable artifacts are idempotent across binary-only bundle updates.
func (m *SimpleLaneMaintenance) InstallArtifacts(ctx context.Context) error {
	if m == nil || m.closed || !simplePlanArtifactsValid(m.plan) {
		return ErrSimpleArtifactConflict
	}
	if err := m.PrepareLayout(ctx); err != nil {
		return err
	}
	installer, ok := m.service.(simpleArtifactInstaller)
	if !ok {
		return ErrSimpleArtifactConflict
	}
	for _, artifact := range m.plan.Artifacts {
		if err := installer.installArtifact(ctx, m.plan, artifact); err != nil {
			return fmt.Errorf("install selected lane %s: %w", artifact.Kind, err)
		}
	}
	return nil
}

func (c *SimpleServiceController) installArtifact(ctx context.Context, plan Plan, artifact Artifact) error {
	if err := c.validate(plan); err != nil || !simplePlanArtifactsValid(plan) || !simpleArtifactMember(plan, artifact) {
		return ErrSimpleArtifactConflict
	}
	switch artifact.Kind {
	case ArtifactLaunchAgent, ArtifactEntrypoint:
		return writeSimpleUserArtifact(artifact)
	case ArtifactSystemdUnit:
		return c.installSimpleSystemdUnit(ctx, plan, artifact)
	default:
		return ErrSimpleArtifactConflict
	}
}

func (c *SimpleServiceController) installSimpleSystemdUnit(ctx context.Context, plan Plan, artifact Artifact) error {
	if plan.Namespace.Host != config.HostKindLinux || artifact.InstallPath != filepath.Join("/etc/systemd/system", plan.Namespace.RunnerUnit) || artifact.Owner != "root" || artifact.Group != "root" || artifact.Mode != 0o644 {
		return ErrSimpleArtifactConflict
	}
	if err := verifySimpleSystemdArtifact(artifact); err == nil {
		if err := c.reloadSystemd(ctx); err != nil {
			return err
		}
		return c.enableSystemd(ctx, plan)
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrSimpleArtifactConflict) {
		return err
	} else if errors.Is(err, ErrSimpleArtifactConflict) {
		return err
	}
	staged, err := os.CreateTemp("", "remote-session-runner-lane-unit-*")
	if err != nil {
		return err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	if err := staged.Chmod(0o600); err != nil {
		_ = staged.Close()
		return err
	}
	if _, err := staged.WriteString(artifact.Contents); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	result, err := c.runner.Run(ctx, "/usr/bin/sudo", "/bin/sh", "-c", simpleAtomicSystemdInstallScript, "runner-lane", artifact.InstallPath, stagedPath)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("systemd lane unit install failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	if err := verifySimpleSystemdArtifact(artifact); err != nil {
		return err
	}
	if err := c.reloadSystemd(ctx); err != nil {
		return err
	}
	return c.enableSystemd(ctx, plan)
}

func (c *SimpleServiceController) reloadSystemd(ctx context.Context) error {
	reload, err := c.runner.Run(ctx, "/usr/bin/sudo", "/usr/bin/systemctl", "daemon-reload")
	if err != nil {
		return err
	}
	if reload.ExitCode != 0 {
		return fmt.Errorf("systemd manager reload failed (exit %d): %s", reload.ExitCode, strings.TrimSpace(reload.Output))
	}
	return nil
}

func (c *SimpleServiceController) enableSystemd(ctx context.Context, plan Plan) error {
	if err := c.validate(plan); err != nil || plan.Namespace.Host != config.HostKindLinux || !simplePlanArtifactsValid(plan) {
		return ErrSimpleArtifactConflict
	}
	result, err := c.runner.Run(ctx, "/usr/bin/sudo", "/usr/bin/systemctl", "enable", "--quiet", plan.Namespace.RunnerUnit)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("enable selected lane systemd unit failed (exit %d): %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	return nil
}

const simpleAtomicSystemdInstallScript = `set -eu
target=$1
source=$2
directory=${target%/*}
temporary=$(mktemp "$directory/.runner-lane-unit.XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
install -o root -g root -m 0644 -- "$source" "$temporary"
if [ -L "$target" ]; then
	printf 'refusing symlinked lane unit\n' >&2
	exit 74
fi
if [ -e "$target" ]; then
	[ -f "$target" ] && [ "$(stat -c '%U:%a' "$target")" = root:644 ] || exit 74
	cmp -s "$temporary" "$target" || exit 73
else
	ln "$temporary" "$target" || {
		[ -f "$target" ] && [ ! -L "$target" ] && cmp -s "$temporary" "$target"
	}
fi
`

func simplePlanArtifactsValid(plan Plan) bool {
	if validateSimplePlan(plan) != nil {
		return false
	}
	expected, err := BuildSimplePlan(plan.HostID, plan.Lane)
	if err != nil || len(expected.Artifacts) != len(plan.Artifacts) {
		return false
	}
	for index := range expected.Artifacts {
		if expected.Artifacts[index] != plan.Artifacts[index] {
			return false
		}
	}
	return true
}

func simpleArtifactMember(plan Plan, artifact Artifact) bool {
	for _, expected := range plan.Artifacts {
		if expected == artifact {
			return true
		}
	}
	return false
}

func simpleLaneDirectories(namespace config.LaneNamespace) []string {
	directories := []string{
		namespace.ServiceRoot, namespace.Binaries, filepath.Join(namespace.ServiceRoot, "config"),
		filepath.Join(namespace.ServiceRoot, "run"), filepath.Join(namespace.ServiceRoot, "state"),
		filepath.Join(namespace.ServiceRoot, "tmp"), namespace.ScriptTempRoot, namespace.Workspaces,
		namespace.Backups, namespace.Logs, namespace.Staging, filepath.Join(namespace.ServiceRoot, "secrets"),
		filepath.Join(namespace.ServiceRoot, "releases"),
	}
	if namespace.Host == config.HostKindMac {
		directories = append(directories, namespace.MailboxRoot)
	}
	return directories
}

func ensureSimpleOwnedDirectory(path string) error {
	if err := ensureSimpleDirectoryPath(path); err != nil {
		return err
	}
	var info unix.Stat_t
	if err := unix.Lstat(path, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR ||
		info.Uid != uint32(os.Getuid()) || info.Mode&0o777 != 0o700 {
		return ErrSimpleLanePreflight
	}
	return nil
}

func writeSimpleUserArtifact(artifact Artifact) error {
	if filepath.Clean(artifact.InstallPath) != artifact.InstallPath || !filepath.IsAbs(artifact.InstallPath) || artifact.Mode == 0 {
		return ErrSimpleArtifactConflict
	}
	if err := verifyExistingSimpleArtifact(artifact); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(artifact.InstallPath)
	var parentInfo unix.Stat_t
	if err := unix.Lstat(parent, &parentInfo); err != nil || parentInfo.Mode&unix.S_IFMT != unix.S_IFDIR || parentInfo.Uid != uint32(os.Getuid()) {
		return ErrSimpleArtifactConflict
	}
	temporary, err := os.CreateTemp(parent, ".runner-lane-artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(os.FileMode(artifact.Mode)); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(artifact.Contents); err != nil {
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
	if err := os.Link(temporaryPath, artifact.InstallPath); err != nil {
		if errors.Is(err, os.ErrExist) && verifyExistingSimpleArtifact(artifact) == nil {
			return nil
		}
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	if err := syncSimpleDirectory(parent); err != nil {
		return err
	}
	return verifyExistingSimpleArtifact(artifact)
}

func verifyExistingSimpleArtifact(artifact Artifact) error {
	info, err := os.Lstat(artifact.InstallPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != os.FileMode(artifact.Mode) {
		return ErrSimpleArtifactConflict
	}
	var stat unix.Stat_t
	if err := unix.Lstat(artifact.InstallPath, &stat); err != nil || stat.Uid != uint32(os.Getuid()) {
		return ErrSimpleArtifactConflict
	}
	contents, err := os.ReadFile(artifact.InstallPath)
	if err != nil {
		return err
	}
	if string(contents) != artifact.Contents {
		return ErrSimpleArtifactConflict
	}
	return nil
}

func verifySimpleSystemdArtifact(artifact Artifact) error {
	info, err := os.Lstat(artifact.InstallPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		return ErrSimpleArtifactConflict
	}
	var stat unix.Stat_t
	if err := unix.Lstat(artifact.InstallPath, &stat); err != nil || stat.Uid != 0 {
		return ErrSimpleArtifactConflict
	}
	contents, err := os.ReadFile(artifact.InstallPath)
	if err != nil || string(contents) != artifact.Contents {
		return ErrSimpleArtifactConflict
	}
	return nil
}
