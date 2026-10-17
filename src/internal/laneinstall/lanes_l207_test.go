package laneinstall

import (
	"errors"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
)

func TestLanesL207RendersMeasuredLinuxControlArtifactsForDevQASAndManagement(t *testing.T) {
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		for _, lane := range []string{"dev", "qas"} {
			measurement, live, policy, now := lanesL207Fixture(t, host)
			policy.Lane = lane
			artifacts, err := BuildLinuxControlArtifacts(policy, measurement, live, now)
			if err != nil {
				t.Fatalf("%s/%s artifacts: %v", host, lane, err)
			}
			wantSlice := "remote-session-runner-" + lane + ".slice"
			if len(artifacts) != 3 {
				t.Fatalf("%s/%s artifacts=%d, want slice and two service drop-ins", host, lane, len(artifacts))
			}
			for _, artifact := range artifacts {
				if artifact.Kind != ArtifactSystemdDropIn || artifact.Mode != 0o644 || artifact.Owner != "root" || artifact.Group != "root" {
					t.Fatalf("%s/%s artifact metadata = %+v", host, lane, artifact)
				}
				if strings.Contains(artifact.InstallPath, "runnerd.service.d") || strings.Contains(artifact.Contents, "runnerd.service\n") {
					t.Fatalf("%s/%s artifact targets unsuffixed PRD: %+v", host, lane, artifact)
				}
			}
			slice := linuxL207Artifact(t, artifacts, "/"+wantSlice+".d/20-runner-lanes-resources.conf")
			for _, expected := range []string{
				"CPUQuota=250%", "MemoryMax=4294967296", "TasksMax=300", "IOAccounting=yes",
				"IOReadBandwidthMax=/dev/disk/by-uuid/11111111-1111-1111-1111-111111111111 31457280",
				"IOWriteBandwidthMax=/dev/disk/by-uuid/11111111-1111-1111-1111-111111111111 31457280",
				"IOReadIOPSMax=/dev/disk/by-uuid/11111111-1111-1111-1111-111111111111 3000",
				"IOWriteIOPSMax=/dev/disk/by-uuid/11111111-1111-1111-1111-111111111111 3000",
			} {
				if !strings.Contains(slice.Contents, expected) {
					t.Errorf("%s/%s slice missing %q:\n%s", host, lane, expected, slice.Contents)
				}
			}
			for _, service := range artifacts[1:] {
				if !strings.Contains(service.Contents, "[Unit]\nRequiresMountsFor=") ||
					!strings.Contains(service.Contents, "[Service]\nSlice="+wantSlice+"\nDelegate=cpu cpuset io memory pids\n") {
					t.Errorf("%s/%s delegated service drop-in = %+v", host, lane, service)
				}
			}
		}
	}

	measurement, live, policy, now := lanesL207Fixture(t, "linux-host")
	policy.Scope, policy.Lane, policy.LaneRuntime = lanecapacity.ScopeHostManagement, "", nil
	policy.HostManagement = lanesL207ManagementBudget(measurement)
	artifacts, err := BuildLinuxControlArtifacts(policy, measurement, live, now)
	if err != nil {
		t.Fatalf("management artifacts: %v", err)
	}
	if len(artifacts) != 5 {
		t.Fatalf("management artifacts=%d, want slice and four fixed services", len(artifacts))
	}
	wantManagementServices := map[string]bool{
		"runner-release-builder.service": false, "runner-release-transport.service": false,
		"runner-security-anchor-helper.service": false, "runner-release-transport-pki-installer.service": false,
	}
	for _, service := range artifacts[1:] {
		for unit := range wantManagementServices {
			if strings.Contains(service.InstallPath, "/"+unit+".d/") {
				wantManagementServices[unit] = true
			}
		}
		if !strings.Contains(service.Contents, "Slice=remote-session-runner-management.slice") {
			t.Errorf("management service not bound to fixed slice: %+v", service)
		}
		if strings.Contains(service.Contents, "Delegate=") {
			t.Errorf("management service received unnecessary controller delegation: %+v", service)
		}
	}
	for unit, found := range wantManagementServices {
		if !found {
			t.Errorf("management service %s was not rendered", unit)
		}
	}
	managementBuilder := linuxL207Artifact(t, artifacts, "/runner-release-builder.service.d/20-runner-lanes-scope.conf")
	if !strings.Contains(managementBuilder.Contents, "/home/ubuntu/.cache/go-build /home/ubuntu/go/pkg/mod") {
		t.Fatalf("management builder cache mounts = %+v", managementBuilder)
	}
}

func TestLanesL207BuildsExactQuotaPlanWithoutApplyingIt(t *testing.T) {
	measurement, live, policy, now := lanesL207Fixture(t, "factory-sandbox")
	plan, err := BuildLinuxProjectQuotaPlan(policy, measurement, live, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HostID != "factory-sandbox" || plan.DataDevice.ID != "lane-capacity" || plan.BackupDevice.ID != "cache-backup" ||
		len(plan.Projects) != 7 || len(plan.FstabEntries) != 8 || len(plan.Artifacts) != 2 {
		t.Fatalf("quota plan identity/counts = %+v", plan)
	}
	want := []LinuxProject{
		{ID: 1001, Name: "rsr-dev", Path: lanecapacity.LinuxCapacityMountpoint + "/dev", Mountpoint: lanecapacity.LinuxCapacityMountpoint, HardBytes: 16 * lanecapacity.GiB, HardInodes: 524_288, Inherit: true},
		{ID: 1002, Name: "rsr-qas", Path: lanecapacity.LinuxCapacityMountpoint + "/qas", Mountpoint: lanecapacity.LinuxCapacityMountpoint, HardBytes: 16 * lanecapacity.GiB, HardInodes: 524_288, Inherit: true},
		{ID: 1003, Name: "rsr-management", Path: lanecapacity.LinuxCapacityMountpoint + "/management", Mountpoint: lanecapacity.LinuxCapacityMountpoint, HardBytes: 8 * lanecapacity.GiB, HardInodes: 131_072, Inherit: true},
		{ID: 1004, Name: "rsr-go-build", Path: lanecapacity.LinuxCapacityMountpoint + "/go-build", Mountpoint: lanecapacity.LinuxCapacityMountpoint, HardBytes: 6 * lanecapacity.GiB, HardInodes: 196_608, Inherit: true},
		{ID: 1005, Name: "rsr-go-modules", Path: lanecapacity.LinuxCapacityMountpoint + "/go-modules", Mountpoint: lanecapacity.LinuxCapacityMountpoint, HardBytes: 2 * lanecapacity.GiB, HardInodes: 65_536, Inherit: true},
		{ID: 1006, Name: "rsr-release-cache", Path: lanecapacity.LinuxCapacityMountpoint + "/release-cache", Mountpoint: lanecapacity.LinuxCapacityMountpoint, HardBytes: 8 * lanecapacity.GiB, HardInodes: 262_144, Inherit: true},
		{ID: 1007, Name: "rsr-cache-backup", Path: lanecapacity.LinuxBackupMountpoint, Mountpoint: lanecapacity.LinuxBackupMountpoint, HardBytes: 8 * lanecapacity.GiB, HardInodes: 262_144, Inherit: true},
	}
	for i := range want {
		if plan.Projects[i] != want[i] {
			t.Errorf("project[%d]=%+v, want %+v", i, plan.Projects[i], want[i])
		}
	}
	if plan.Artifacts[0].Contents != "rsr-dev:1001\nrsr-qas:1002\nrsr-management:1003\nrsr-go-build:1004\nrsr-go-modules:1005\nrsr-release-cache:1006\nrsr-cache-backup:1007\n" ||
		plan.Artifacts[1].Contents != "1001:"+lanecapacity.LinuxCapacityMountpoint+"/dev\n1002:"+lanecapacity.LinuxCapacityMountpoint+"/qas\n1003:"+lanecapacity.LinuxCapacityMountpoint+"/management\n1004:"+lanecapacity.LinuxCapacityMountpoint+"/go-build\n1005:"+lanecapacity.LinuxCapacityMountpoint+"/go-modules\n1006:"+lanecapacity.LinuxCapacityMountpoint+"/release-cache\n1007:"+lanecapacity.LinuxBackupMountpoint+"\n" {
		t.Fatalf("project fragments = %+v", plan.Artifacts)
	}
	if !strings.Contains(plan.FstabEntries[0], "UUID=11111111-1111-1111-1111-111111111111 "+lanecapacity.LinuxCapacityMountpoint+" ext4 defaults,prjquota,nofail") ||
		!strings.Contains(plan.FstabEntries[1], "UUID=22222222-2222-2222-2222-222222222222 "+lanecapacity.LinuxBackupMountpoint+" ext4 defaults,prjquota,nofail") {
		t.Fatalf("measured mount entries = %v", plan.FstabEntries[:2])
	}
	for _, artifact := range plan.Artifacts {
		if artifact.Owner != "root" || artifact.Group != "root" || artifact.Mode != 0o644 || strings.Contains(artifact.Contents, "mkfs.") || strings.Contains(artifact.Contents, "setquota ") {
			t.Fatalf("quota plan contains an applied/destructive operation: %+v", artifact)
		}
	}
}

func TestLanesL207RequiresMeasuredQuotaCapabilitiesAndRejectsPRD(t *testing.T) {
	measurement, live, policy, now := lanesL207Fixture(t, "linux-host")
	measurement.LinuxDevices[0].CapacityBytes = 19 * lanecapacity.GiB
	live.LinuxDevices = append([]lanecapacity.LinuxDevice(nil), measurement.LinuxDevices...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxProjectQuotaPlan(policy, measurement, live, now); !errors.Is(err, ErrLinuxResourcesUnavailable) {
		t.Fatalf("undersized backup device accepted: %v", err)
	}

	measurement, live, policy, now = lanesL207Fixture(t, "linux-host")
	measurement.LinuxDevices[1].CapacityInodes = 2_000_000
	live.LinuxDevices = append([]lanecapacity.LinuxDevice(nil), measurement.LinuxDevices...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxProjectQuotaPlan(policy, measurement, live, now); !errors.Is(err, ErrLinuxResourcesUnavailable) {
		t.Fatalf("quota inode budgets exceeding 70 percent of measured capacity accepted: %v", err)
	}

	measurement, live, policy, now = lanesL207Fixture(t, "linux-host")
	measurement.LinuxDevices[1].UsedBytes = 57 * lanecapacity.GiB
	live.LinuxDevices = append([]lanecapacity.LinuxDevice(nil), measurement.LinuxDevices...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxProjectQuotaPlan(policy, measurement, live, now); !errors.Is(err, ErrLinuxResourcesUnavailable) {
		t.Fatalf("current volume use exceeding all project budgets accepted: %v", err)
	}

	measurement, live, policy, now = lanesL207Fixture(t, "linux-host")
	policy.Lane = "prd"
	if _, err := BuildLinuxControlArtifacts(policy, measurement, live, now); !errors.Is(err, lanecapacity.ErrInvalidPolicy) {
		t.Fatalf("PRD control artifact accepted: %v", err)
	}

	measurement, live, policy, now = lanesL207Fixture(t, "linux-host")
	measurement.LinuxDevices = nil
	live.LinuxDevices = nil
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxControlArtifacts(policy, measurement, live, now); !errors.Is(err, lanecapacity.ErrInvalidPolicy) && !errors.Is(err, lanecapacity.ErrLinuxControlsUnavailable) {
		t.Fatalf("missing measured device accepted: %v", err)
	}
}

func lanesL207Fixture(t *testing.T, host string) (lanecapacity.Measurement, lanecapacity.LiveObservation, lanecapacity.Policy, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	identity := lanecapacity.Identity{HostID: host, Platform: lanecapacity.PlatformLinux, DeviceID: "/dev/vda1", Filesystem: "ext4", Mountpoint: "/home/ubuntu",
		CapacityBytes: 100 * lanecapacity.GiB, CapacityInodes: 1_000_000,
		BackupDeviceID: "/dev/vdb1", BackupFilesystem: "ext4", BackupMountpoint: "/mnt/runner-backup", BackupCapacityBytes: 20 * lanecapacity.GiB}
	paths := []string{"/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod"}
	capacity := lanecapacity.ResourceVector{CPUMilli: 8_000, MemoryBytes: 16 * lanecapacity.GiB, Tasks: 1_000,
		IOReadBytesPerSecond: 100 * 1024 * 1024, IOWriteBytesPerSecond: 100 * 1024 * 1024, IOReadIOPSMax: 10_000, IOWriteIOPSMax: 10_000}
	measurement := lanecapacity.Measurement{ID: "measure-l207-" + host, Identity: identity, ObservedAt: now.Add(-10 * time.Second), ExpiresAt: now.Add(5 * time.Minute),
		ResourceCapacity: capacity, GoCachePaths: paths,
		LinuxDevices: []lanecapacity.LinuxDevice{
			{ID: "cache-backup", UUID: "22222222-2222-2222-2222-222222222222", MajorMinor: "259:2", Filesystem: "ext4", Mountpoint: lanecapacity.LinuxBackupMountpoint,
				CapacityBytes: 20 * lanecapacity.GiB, CapacityInodes: 1_000_000, UsedBytes: 2 * lanecapacity.GiB, UsedInodes: 1000, ProjectQuotaFeature: true, ProjectQuotaActive: true},
			{ID: "lane-capacity", UUID: "11111111-1111-1111-1111-111111111111", MajorMinor: "259:1", Filesystem: "ext4", Mountpoint: lanecapacity.LinuxCapacityMountpoint,
				CapacityBytes: 96 * lanecapacity.GiB, CapacityInodes: 4_000_000, UsedBytes: 4 * lanecapacity.GiB, UsedInodes: 100_000,
				IOReadBytesPerSecondCapacity: 100 * 1024 * 1024, IOWriteBytesPerSecondCapacity: 100 * 1024 * 1024,
				IOReadIOPSCapacity: 10_000, IOWriteIOPSCapacity: 10_000, ProjectQuotaFeature: true, ProjectQuotaActive: true},
		},
		Usage: lanecapacity.Usage{UsedBytes: 50 * lanecapacity.GiB, UsedInodes: 500_000, GoCacheBytes: lanecapacity.GiB, ReleaseCacheBytes: lanecapacity.GiB, BackupBytes: 2 * lanecapacity.GiB}}
	live := lanecapacity.LiveObservation{Identity: identity, ObservedAt: now.Add(-9 * time.Second), ResourceCapacity: capacity, GoCachePaths: append([]string(nil), paths...),
		LinuxDevices: append([]lanecapacity.LinuxDevice(nil), measurement.LinuxDevices...), Usage: measurement.Usage}
	budget := lanecapacity.LaneRuntimeBudget{PRDGrowthBytes: 6 * lanecapacity.GiB, DevHardBytes: 4 * lanecapacity.GiB, QASHardBytes: 4 * lanecapacity.GiB, ManagementGrowthBytes: 2 * lanecapacity.GiB,
		PRDGrowthInodes: 50_000, DevHardInodes: 50_000, QASHardInodes: 50_000, ManagementGrowthInodes: 50_000,
		GoCachePaths: paths, GoCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
		BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB, IODeviceID: "lane-capacity",
		Resources: lanecapacity.ResourceEnvelope{
			PRDFloor:          lanesL207Vector(1_000, 4*lanecapacity.GiB, 100, 20*1024*1024, 2_000),
			DevHardMax:        lanesL207Vector(2_500, 4*lanecapacity.GiB, 300, 30*1024*1024, 3_000),
			QASHardMax:        lanesL207Vector(2_500, 4*lanecapacity.GiB, 300, 30*1024*1024, 3_000),
			ManagementHardMax: lanesL207Vector(1_000, 2*lanecapacity.GiB, 200, 10*1024*1024, 1_000),
		}}
	policy := lanecapacity.Policy{Version: 1, HostID: host, Scope: lanecapacity.ScopeLaneRuntime, Lane: "dev", MeasurementID: measurement.ID,
		NotBefore: measurement.ObservedAt, NotAfter: now.Add(time.Minute), Compute: lanecapacity.ComputeLinuxCgroupV2, LaneRuntime: &budget}
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	return measurement, live, policy, now
}

func lanesL207Vector(cpu uint64, memory uint64, tasks uint64, bandwidth uint64, iops uint64) lanecapacity.ResourceVector {
	return lanecapacity.ResourceVector{CPUMilli: cpu, MemoryBytes: memory, Tasks: tasks, IOReadBytesPerSecond: bandwidth,
		IOWriteBytesPerSecond: bandwidth, IOReadIOPSMax: iops, IOWriteIOPSMax: iops}
}

func lanesL207ManagementBudget(measurement lanecapacity.Measurement) *lanecapacity.HostManagementBudget {
	return &lanecapacity.HostManagementBudget{
		Components: []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
		PeakBytes:  2 * lanecapacity.GiB, PeakInodes: 50_000, StagingBudgetBytes: lanecapacity.GiB, LogBudgetBytes: lanecapacity.GiB,
		GoCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB, BackupBudgetBytes: 3 * lanecapacity.GiB,
		BackupLowWaterBytes: 2 * lanecapacity.GiB, GoCachePaths: measurement.GoCachePaths, IODeviceID: "lane-capacity",
		Resources: lanecapacity.ResourceEnvelope{PRDFloor: lanesL207Vector(1_000, 4*lanecapacity.GiB, 100, 20*1024*1024, 2_000),
			ManagementHardMax: lanesL207Vector(1_000, 2*lanecapacity.GiB, 200, 10*1024*1024, 1_000)},
	}
}

func linuxL207Artifact(t *testing.T, artifacts []Artifact, suffix string) Artifact {
	t.Helper()
	for _, artifact := range artifacts {
		if strings.HasSuffix(artifact.InstallPath, suffix) {
			return artifact
		}
	}
	t.Fatalf("artifact ending in %q not found: %+v", suffix, artifacts)
	return Artifact{}
}
