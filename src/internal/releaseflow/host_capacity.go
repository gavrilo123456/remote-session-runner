package releaseflow

import (
	"context"
	"fmt"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

// HostCapacityReport binds a host-local, read-only filesystem observation to
// the fixed lane backup path and the exact controller that measured it.
type HostCapacityReport struct {
	HostID             string `json:"host_id"`
	Lane               string `json:"lane"`
	BackupPath         string `json:"backup_path"`
	ControllerRevision string `json:"controller_revision"`
	ObservedAtUTC      string `json:"observed_at_utc"`
	AvailableBytes     uint64 `json:"available_bytes"`
}

// RunHostCapacity executes on the host being measured. It accepts no
// caller-selected filesystem path and never creates a missing backup root.
func RunHostCapacity(ctx context.Context, host, lane, repository, controllerRevision string) (HostCapacityReport, error) {
	if !contains(HostOrder, host) || !contains(LaneOrder, lane) || !absoluteClean(repository) || !shaPattern.MatchString(controllerRevision) {
		return HostCapacityReport{}, ErrInvalidInput
	}
	if !laneinstall.CatalogHostMatchesPlatform(host, runtime.GOOS, runtime.GOARCH) {
		return HostCapacityReport{}, fmt.Errorf("capacity host %s does not match this platform: %w", host, ErrInvalidInput)
	}
	current, err := user.Current()
	if err != nil || host == "mac-control-plane" && current.Username != config.MacAccount || host != "mac-control-plane" && current.Username != config.LinuxAccount {
		return HostCapacityReport{}, fmt.Errorf("capacity host %s requires its configured account: %w", host, ErrInvalidInput)
	}
	if err := verifyControllerCheckout(ctx, repository, controllerRevision); err != nil {
		return HostCapacityReport{}, err
	}
	plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLane(lane))
	if err != nil {
		return HostCapacityReport{}, err
	}
	available, err := measureHostBackupCapacity(plan.Namespace.Backups)
	if err != nil {
		return HostCapacityReport{}, err
	}
	return HostCapacityReport{
		HostID: host, Lane: lane, BackupPath: plan.Namespace.Backups,
		ControllerRevision: controllerRevision, ObservedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		AvailableBytes: available,
	}, nil
}

// measureHostBackupCapacity measures only an existing directory. Traversal
// uses directory descriptors and O_NOFOLLOW for every component so a symlink
// cannot redirect this observation onto a different filesystem.
func measureHostBackupCapacity(path string) (uint64, error) {
	fail := func(err error) (uint64, error) {
		return 0, fmt.Errorf("read-only lane backup-volume capacity measurement for %q failed: %w", path, err)
	}
	if !absoluteClean(path) {
		return fail(ErrInvalidInput)
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(string(filepath.Separator), flags, 0)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = unix.Close(fd) }()
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, err := unix.Openat(fd, component, flags, 0)
		if err != nil {
			return fail(err)
		}
		_ = unix.Close(fd)
		fd = next
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return fail(err)
	}
	available, err := checkedCapacityBytes(int64(stat.Bsize), uint64(stat.Bavail))
	if err != nil {
		return fail(err)
	}
	return available, nil
}

func checkedCapacityBytes(blockSize int64, availableBlocks uint64) (uint64, error) {
	if blockSize <= 0 || availableBlocks > ^uint64(0)/uint64(blockSize) {
		return 0, fmt.Errorf("invalid filesystem capacity values: %w", ErrOperationCorrupted)
	}
	return availableBlocks * uint64(blockSize), nil
}
