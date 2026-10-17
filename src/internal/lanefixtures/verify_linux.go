//go:build linux

package lanefixtures

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	fixtureMemoryMax = uint64(2) * GiB
	fixtureTasksMax  = uint64(64)
	fixtureWriteBPS  = uint64(16) * 1024 * 1024
	fixtureWriteIOPS = uint64(1000)
)

// VerifyBeforeScope proves fixture device and controller identities before a
// transient scope applies any per-device I/O controls. It is read-only.
func VerifyBeforeScope(m Manifest) error {
	if m.Platform != "linux" {
		return ErrManifest
	}
	rootSource, err := output("findmnt", "--noheadings", "--raw", "--output", "SOURCE", "--target", "/")
	if err != nil {
		return fmt.Errorf("read root mount source: %w", err)
	}
	rootMajorMinor, err := output("lsblk", "--inverse", "--noheadings", "--raw", "--output", "MAJ:MIN", strings.TrimSpace(rootSource))
	if err != nil {
		return fmt.Errorf("resolve root block devices: %w", err)
	}
	rootDevices := make(map[string]bool)
	for _, value := range strings.Fields(rootMajorMinor) {
		rootDevices[value] = true
	}
	for _, mount := range m.Mounts {
		if !filepath.IsAbs(mount.DevicePath) || !strings.HasPrefix(mount.DevicePath, "/dev/") {
			return ErrManifest
		}
		if err := noSymlinkPath(mount.Path); err != nil {
			return fmt.Errorf("fixture mount %s path is missing, aliased, or symlinked", mount.Role)
		}
		if err := verifyOwnedFixtureDirectory(mount.Path); err != nil {
			return err
		}
		row, err := output("findmnt", "--noheadings", "--raw", "--output", "SOURCE,FSTYPE,TARGET,OPTIONS", "--mountpoint", mount.Path)
		if err != nil {
			return fmt.Errorf("fixture mount %s is not mounted exactly: %w", mount.Role, err)
		}
		fields := strings.Fields(row)
		if len(fields) != 4 {
			return fmt.Errorf("fixture mount %s returned an ambiguous mount row", mount.Role)
		}
		options := strings.Split(fields[3], ",")
		quotaEnabled := false
		for _, option := range options {
			if option == "prjquota" || option == "pquota" {
				quotaEnabled = true
			}
		}
		if fields[1] != "ext4" || fields[2] != mount.Path || !quotaEnabled {
			return fmt.Errorf("fixture mount %s does not match ext4/project-quota identity", mount.Role)
		}
		resolved, err := filepath.EvalSymlinks(mount.DevicePath)
		if err != nil {
			return fmt.Errorf("resolve fixture device %s: %w", mount.Role, err)
		}
		if filepath.Base(resolved) != mount.DeviceID {
			return fmt.Errorf("fixture %s resolved device name differs from the approved provider mapping", mount.Role)
		}
		deviceInfo, err := os.Stat(resolved)
		if err != nil || deviceInfo.Mode()&os.ModeDevice == 0 || deviceInfo.Mode()&os.ModeCharDevice != 0 {
			return fmt.Errorf("fixture %s is not a block device", mount.Role)
		}
		majorMinor, err := output("lsblk", "--nodeps", "--noheadings", "--raw", "--output", "MAJ:MIN", resolved)
		if err != nil || strings.TrimSpace(majorMinor) != mount.MajorMinor {
			return fmt.Errorf("fixture %s major/minor changed", mount.Role)
		}
		serial, err := output("lsblk", "--nodeps", "--noheadings", "--raw", "--output", "SERIAL", resolved)
		if err != nil || strings.TrimSpace(serial) != mount.DeviceSerial {
			return fmt.Errorf("fixture %s provider serial changed", mount.Role)
		}
		uuid, err := output("blkid", "--output", "value", "--match-tag", "UUID", resolved)
		if err != nil || !strings.EqualFold(strings.TrimSpace(uuid), mount.VolumeUUID) {
			return fmt.Errorf("fixture %s filesystem UUID changed", mount.Role)
		}
		bytes, err := output("blockdev", "--getsize64", resolved)
		if err != nil {
			return fmt.Errorf("measure fixture %s size: %w", mount.Role, err)
		}
		var size uint64
		if _, err := fmt.Sscan(strings.TrimSpace(bytes), &size); err != nil || size != mount.CapacityBytes {
			return fmt.Errorf("fixture %s capacity differs from the approved manifest", mount.Role)
		}
		if rootDevices[mount.MajorMinor] {
			return fmt.Errorf("fixture %s resolves to the production root device", mount.Role)
		}
		mountedSource := fields[0]
		mountedDevice, err := filepath.EvalSymlinks(mountedSource)
		if err != nil || mountedDevice != resolved {
			return fmt.Errorf("fixture mount %s source differs from approved device", mount.Role)
		}
	}
	for _, project := range m.Projects {
		if err := noSymlinkPath(project.Path); err != nil {
			return fmt.Errorf("fixture project %s path is missing, aliased, or symlinked", project.Name)
		}
		if err := verifyOwnedFixtureDirectory(project.Path); err != nil {
			return fmt.Errorf("fixture project %s: %w", project.Name, err)
		}
	}
	return verifyLinuxHostControllers()
}

// VerifyLive repeats the storage preflight and confirms the F process is
// inside its exact bounded scope. It is read-only.
func VerifyLive(m Manifest) error {
	if err := VerifyBeforeScope(m); err != nil {
		return err
	}
	return verifyLinuxScope(m)
}

func verifyLinuxHostControllers() error {
	root, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return fmt.Errorf("unified cgroup-v2 hierarchy is unavailable: %w", err)
	}
	for _, controller := range []string{"cpu", "cpuset", "io", "memory", "pids"} {
		if !containsWord(string(root), controller) {
			return fmt.Errorf("cgroup-v2 root lacks required controller %s", controller)
		}
	}
	systemSlice, err := os.ReadFile("/sys/fs/cgroup/system.slice/cgroup.controllers")
	if err != nil {
		return fmt.Errorf("system.slice cgroup controller view is unavailable: %w", err)
	}
	for _, controller := range []string{"cpu", "cpuset", "io", "memory", "pids"} {
		if !containsWord(string(systemSlice), controller) {
			return fmt.Errorf("system.slice does not expose required new-scope controller %s", controller)
		}
	}
	return nil
}

func verifyLinuxScope(m Manifest) error {
	cgroups, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("read test process cgroup: %w", err)
	}
	var cgroupPath string
	for _, line := range strings.Split(string(cgroups), "\n") {
		if strings.HasPrefix(line, "0::") {
			cgroupPath = strings.TrimPrefix(line, "0::")
			break
		}
	}
	expected := "/system.slice/" + m.TestSystemdUnit
	if cgroupPath != expected {
		return fmt.Errorf("F pressure test must run in exact approved scope %s (current %q)", expected, cgroupPath)
	}
	scopeRoot := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(cgroupPath, "/"))
	if err := verifyFixtureScopeLimits(scopeRoot, m); err != nil {
		return err
	}
	return nil
}

func verifyFixtureScopeLimits(scopeRoot string, m Manifest) error {
	cpu, err := os.ReadFile(filepath.Join(scopeRoot, "cpu.max"))
	if err != nil {
		return fmt.Errorf("read fixture scope cpu.max: %w", err)
	}
	cpuFields := strings.Fields(string(cpu))
	if len(cpuFields) != 2 || cpuFields[0] == "max" {
		return fmt.Errorf("fixture scope CPU quota is not bounded")
	}
	quota, quotaErr := strconv.ParseUint(cpuFields[0], 10, 64)
	period, periodErr := strconv.ParseUint(cpuFields[1], 10, 64)
	if quotaErr != nil || periodErr != nil || period == 0 || quota > period/4 {
		return fmt.Errorf("fixture scope CPU quota exceeds the 25%% test bound")
	}
	memory, err := os.ReadFile(filepath.Join(scopeRoot, "memory.max"))
	if err != nil {
		return fmt.Errorf("read fixture scope memory.max: %w", err)
	}
	memoryLimit, err := strconv.ParseUint(strings.TrimSpace(string(memory)), 10, 64)
	if err != nil || memoryLimit == 0 || memoryLimit > fixtureMemoryMax {
		return fmt.Errorf("fixture scope memory limit is missing or exceeds 2 GiB")
	}
	tasks, err := os.ReadFile(filepath.Join(scopeRoot, "pids.max"))
	if err != nil {
		return fmt.Errorf("read fixture scope pids.max: %w", err)
	}
	taskLimit, err := strconv.ParseUint(strings.TrimSpace(string(tasks)), 10, 64)
	if err != nil || taskLimit == 0 || taskLimit > fixtureTasksMax {
		return fmt.Errorf("fixture scope task limit is missing or exceeds 64")
	}
	ioData, err := os.ReadFile(filepath.Join(scopeRoot, "io.max"))
	if err != nil {
		return fmt.Errorf("read fixture scope io.max: %w", err)
	}
	ioRows := make(map[string]map[string]uint64)
	for _, line := range strings.Split(string(ioData), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		limits := make(map[string]uint64)
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			parsed, parseErr := strconv.ParseUint(value, 10, 64)
			if parseErr == nil {
				limits[key] = parsed
			}
		}
		ioRows[fields[0]] = limits
	}
	for _, mount := range m.Mounts {
		limits := ioRows[mount.MajorMinor]
		if limits["wbps"] == 0 || limits["wbps"] > fixtureWriteBPS || limits["wiops"] == 0 || limits["wiops"] > fixtureWriteIOPS {
			return fmt.Errorf("fixture scope lacks bounded writes for device %s", mount.MajorMinor)
		}
	}
	return nil
}

func containsWord(values, expected string) bool {
	for _, value := range strings.Fields(values) {
		if value == expected {
			return true
		}
	}
	return false
}

func output(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	environment, err := fixtureCommandEnvironment()
	if err != nil {
		return "", err
	}
	command.Env = environment
	result, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(result)))
	}
	return string(result), nil
}
