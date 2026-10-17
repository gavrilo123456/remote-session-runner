package laneinstall

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/lanecapacity"
)

const (
	linuxLaneVolumeMinimumBytes   uint64 = 96 * lanecapacity.GiB
	linuxBackupVolumeMinimumBytes uint64 = 20 * lanecapacity.GiB
	linuxControllerSet                   = "cpu cpuset io memory pids"
)

var ErrLinuxResourcesUnavailable = errors.New("Linux resource artifacts require fresh measured project-quota devices")

type LinuxProject struct {
	ID         uint32
	Name       string
	Path       string
	Mountpoint string
	HardBytes  uint64
	HardInodes uint64
	Inherit    bool
}

// LinuxProjectQuotaPlan is a deterministic, measured-device-bound proposal.
// The entries and artifacts are data only: callers must not apply them without
// the separately approved A01 provisioning procedure and its device checks.
type LinuxProjectQuotaPlan struct {
	HostID       string
	DataDevice   lanecapacity.LinuxDevice
	BackupDevice lanecapacity.LinuxDevice
	Projects     []LinuxProject
	FstabEntries []string
	Artifacts    []Artifact
}

// BuildLinuxProjectQuotaPlan returns the fixed A01 project map for the
// measured 96 GiB lane volume and separate 20 GiB backup volume. It never
// creates directories, copies caches, writes quota settings, or changes fstab.
func BuildLinuxProjectQuotaPlan(policy lanecapacity.Policy, measurement lanecapacity.Measurement,
	live lanecapacity.LiveObservation, now time.Time) (LinuxProjectQuotaPlan, error) {
	if err := lanecapacity.ValidatePolicy(policy, measurement, live, now); err != nil {
		return LinuxProjectQuotaPlan{}, fmt.Errorf("validate capacity policy: %w", err)
	}
	control, err := lanecapacity.BuildLinuxControlPlan(policy, measurement, live, now)
	if err != nil {
		return LinuxProjectQuotaPlan{}, err
	}
	if measurement.Identity.Platform != lanecapacity.PlatformLinux {
		return LinuxProjectQuotaPlan{}, ErrLinuxResourcesUnavailable
	}
	data := linuxDeviceAt(measurement.LinuxDevices, lanecapacity.LinuxCapacityMountpoint)
	backup := linuxDeviceAt(measurement.LinuxDevices, lanecapacity.LinuxBackupMountpoint)
	if data.ID == "" || backup.ID == "" || data.ID != control.Device.ID ||
		!linuxProjectDeviceReady(data, linuxLaneVolumeMinimumBytes) ||
		!linuxProjectDeviceReady(backup, linuxBackupVolumeMinimumBytes) {
		return LinuxProjectQuotaPlan{}, ErrLinuxResourcesUnavailable
	}

	projects := []LinuxProject{
		{ID: 1001, Name: "rsr-dev", Path: filepath.Join(data.Mountpoint, "dev"), Mountpoint: data.Mountpoint, HardBytes: 16 * lanecapacity.GiB, HardInodes: 524_288, Inherit: true},
		{ID: 1002, Name: "rsr-qas", Path: filepath.Join(data.Mountpoint, "qas"), Mountpoint: data.Mountpoint, HardBytes: 16 * lanecapacity.GiB, HardInodes: 524_288, Inherit: true},
		{ID: 1003, Name: "rsr-management", Path: filepath.Join(data.Mountpoint, "management"), Mountpoint: data.Mountpoint, HardBytes: 8 * lanecapacity.GiB, HardInodes: 131_072, Inherit: true},
		{ID: 1004, Name: "rsr-go-build", Path: filepath.Join(data.Mountpoint, "go-build"), Mountpoint: data.Mountpoint, HardBytes: 6 * lanecapacity.GiB, HardInodes: 196_608, Inherit: true},
		{ID: 1005, Name: "rsr-go-modules", Path: filepath.Join(data.Mountpoint, "go-modules"), Mountpoint: data.Mountpoint, HardBytes: 2 * lanecapacity.GiB, HardInodes: 65_536, Inherit: true},
		{ID: 1006, Name: "rsr-release-cache", Path: filepath.Join(data.Mountpoint, "release-cache"), Mountpoint: data.Mountpoint, HardBytes: 8 * lanecapacity.GiB, HardInodes: 262_144, Inherit: true},
		{ID: 1007, Name: "rsr-cache-backup", Path: backup.Mountpoint, Mountpoint: backup.Mountpoint, HardBytes: 8 * lanecapacity.GiB, HardInodes: 262_144, Inherit: true},
	}
	if !linuxProjectBudgetsFit(data, projects) || !linuxProjectBudgetsFit(backup, projects) {
		return LinuxProjectQuotaPlan{}, ErrLinuxResourcesUnavailable
	}
	projid, projectsFile := renderLinuxProjectMap(projects)
	artifacts := []Artifact{
		{Kind: ArtifactProjectMap, InstallPath: "/etc/remote-session-runner/lane-capacity/projid.fragment", Mode: 0o644, Owner: "root", Group: "root", Contents: projid},
		{Kind: ArtifactProjectQuota, InstallPath: "/etc/remote-session-runner/lane-capacity/projects.fragment", Mode: 0o644, Owner: "root", Group: "root", Contents: projectsFile},
	}
	return LinuxProjectQuotaPlan{
		HostID: control.HostID, DataDevice: data, BackupDevice: backup, Projects: projects,
		FstabEntries: renderLinuxFstabEntries(data, backup), Artifacts: artifacts,
	}, nil
}

// BuildLinuxControlArtifacts renders only fixed Dev/QAS or management slice
// and service drop-ins from a validated, live-bound policy. It does not install
// units or select production services.
func BuildLinuxControlArtifacts(policy lanecapacity.Policy, measurement lanecapacity.Measurement,
	live lanecapacity.LiveObservation, now time.Time) ([]Artifact, error) {
	control, err := lanecapacity.BuildLinuxControlPlan(policy, measurement, live, now)
	if err != nil {
		return nil, err
	}
	if _, ok := canonicalHosts[control.HostID]; !ok || canonicalHosts[control.HostID].kind != config.HostKindLinux {
		return nil, ErrInvalidHost
	}

	sliceContents := renderLinuxResourceSlice(control.Resources, control.Device.UUID)
	artifacts := []Artifact{{
		Kind:        ArtifactSystemdDropIn,
		InstallPath: filepath.Join("/etc/systemd/system", control.SliceUnit+".d", "20-runner-lanes-resources.conf"),
		Mode:        0o644, Owner: "root", Group: "root", Contents: sliceContents,
	}}
	for _, service := range linuxControlServices(control) {
		contents := renderLinuxServiceControlDropIn(control, service.mountPaths, control.Scope == lanecapacity.ScopeLaneRuntime)
		artifacts = append(artifacts, Artifact{
			Kind:        ArtifactSystemdDropIn,
			InstallPath: filepath.Join("/etc/systemd/system", service.unit+".d", "20-runner-lanes-scope.conf"),
			Mode:        0o644, Owner: "root", Group: "root", Contents: contents,
		})
	}
	return artifacts, nil
}

type linuxControlService struct {
	unit       string
	mountPaths []string
}

func linuxControlServices(control lanecapacity.LinuxControlPlan) []linuxControlService {
	if control.Scope == lanecapacity.ScopeHostManagement {
		return []linuxControlService{
			{unit: "runner-release-builder.service", mountPaths: []string{"/var/lib/remote-session-runner/management", "/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod", "/var/lib/remote-session-runner-release-cache"}},
			{unit: "runner-release-transport.service", mountPaths: []string{"/var/lib/remote-session-runner/management", "/var/lib/remote-session-runner-release-cache", lanecapacity.LinuxBackupMountpoint}},
			{unit: "runner-security-anchor-helper.service", mountPaths: []string{"/var/lib/remote-session-runner/management"}},
			{unit: "runner-release-transport-pki-installer.service", mountPaths: []string{"/var/lib/remote-session-runner/management"}},
		}
	}
	plan, err := BuildPlan(control.HostID, config.ReleaseLane(control.Lane))
	if err != nil {
		return nil
	}
	ns := plan.Namespace
	return []linuxControlService{
		{unit: ns.RunnerUnit, mountPaths: []string{ns.ServiceRoot, "/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod", "/var/lib/remote-session-runner-release-cache"}},
		{unit: ns.QueuedSSHUnit, mountPaths: []string{ns.ServiceRoot, "/var/lib/remote-session-runner-release-cache"}},
	}
}

func renderLinuxResourceSlice(resources lanecapacity.ResourceVector, deviceUUID string) string {
	device := "/dev/disk/by-uuid/" + strings.ToLower(deviceUUID)
	return fmt.Sprintf(`[Slice]
CPUAccounting=yes
CPUQuota=%d%%
MemoryAccounting=yes
MemoryMax=%d
TasksAccounting=yes
TasksMax=%d
IOAccounting=yes
IOReadBandwidthMax=%s %d
IOWriteBandwidthMax=%s %d
IOReadIOPSMax=%s %d
IOWriteIOPSMax=%s %d
`, resources.CPUMilli/10, resources.MemoryBytes, resources.Tasks,
		device, resources.IOReadBytesPerSecond, device, resources.IOWriteBytesPerSecond,
		device, resources.IOReadIOPSMax, device, resources.IOWriteIOPSMax)
}

func renderLinuxServiceControlDropIn(control lanecapacity.LinuxControlPlan, mountPaths []string, delegate bool) string {
	var out strings.Builder
	out.WriteString("[Unit]\nRequiresMountsFor=")
	for index, path := range mountPaths {
		if index > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(path)
	}
	out.WriteString("\n\n[Service]\nSlice=")
	out.WriteString(control.SliceUnit)
	if delegate {
		out.WriteString("\nDelegate=")
		out.WriteString(linuxControllerSet)
	}
	out.WriteByte('\n')
	return out.String()
}

func linuxProjectDeviceReady(device lanecapacity.LinuxDevice, minimumBytes uint64) bool {
	return device.ID != "" && device.UUID != "" && device.MajorMinor != "" && device.Filesystem == "ext4" &&
		device.CapacityBytes >= minimumBytes && device.CapacityInodes > 0 &&
		device.ProjectQuotaFeature && device.ProjectQuotaActive
}

func linuxProjectBudgetsFit(device lanecapacity.LinuxDevice, projects []LinuxProject) bool {
	var reservedBytes, reservedInodes uint64
	for _, project := range projects {
		if project.Mountpoint != device.Mountpoint {
			continue
		}
		if ^uint64(0)-reservedBytes < project.HardBytes || ^uint64(0)-reservedInodes < project.HardInodes {
			return false
		}
		reservedBytes += project.HardBytes
		reservedInodes += project.HardInodes
	}
	return reservedBytes > 0 && reservedInodes > 0 && device.UsedBytes <= reservedBytes &&
		device.UsedInodes <= reservedInodes && reservedBytes <= linuxCapacityAfterFloor(device.CapacityBytes) &&
		reservedInodes <= linuxCapacityAfterFloor(device.CapacityInodes)
}

func linuxCapacityAfterFloor(capacity uint64) uint64 {
	thirtyPercent := capacity/100*30 + (capacity%100*30+99)/100
	return capacity - thirtyPercent
}

func linuxDeviceAt(devices []lanecapacity.LinuxDevice, mountpoint string) lanecapacity.LinuxDevice {
	for _, device := range devices {
		if device.Mountpoint == mountpoint {
			return device
		}
	}
	return lanecapacity.LinuxDevice{}
}

func renderLinuxProjectMap(projects []LinuxProject) (string, string) {
	var projid, projectsFile strings.Builder
	for _, project := range projects {
		fmt.Fprintf(&projid, "%s:%d\n", project.Name, project.ID)
		fmt.Fprintf(&projectsFile, "%d:%s\n", project.ID, project.Path)
	}
	return projid.String(), projectsFile.String()
}

func renderLinuxFstabEntries(data, backup lanecapacity.LinuxDevice) []string {
	entries := []string{
		fmt.Sprintf("UUID=%s %s ext4 defaults,prjquota,nofail,x-systemd.device-timeout=30s 0 2", strings.ToLower(data.UUID), data.Mountpoint),
		fmt.Sprintf("UUID=%s %s ext4 defaults,prjquota,nofail,x-systemd.device-timeout=30s 0 2", strings.ToLower(backup.UUID), backup.Mountpoint),
	}
	for _, bind := range [][2]string{
		{filepath.Join(data.Mountpoint, "dev"), "/home/ubuntu/.local/share/remote-session-runner-dev"},
		{filepath.Join(data.Mountpoint, "qas"), "/home/ubuntu/.local/share/remote-session-runner-qas"},
		{filepath.Join(data.Mountpoint, "management"), "/var/lib/remote-session-runner/management"},
		{filepath.Join(data.Mountpoint, "go-build"), "/home/ubuntu/.cache/go-build"},
		{filepath.Join(data.Mountpoint, "go-modules"), "/home/ubuntu/go/pkg/mod"},
		{filepath.Join(data.Mountpoint, "release-cache"), "/var/lib/remote-session-runner-release-cache"},
	} {
		entries = append(entries, fmt.Sprintf("%s %s none bind,nofail 0 0", bind[0], bind[1]))
	}
	return entries
}
