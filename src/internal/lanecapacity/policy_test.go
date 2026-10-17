package lanecapacity

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestL206LinuxEnvelopePreservesPRDAndThirtyPercentByteAndInodeFloors(t *testing.T) {
	measurement, live, policy, now := linuxPolicyFixture(t)
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("policy at exact byte/inode floor: %v", err)
	}
	live.Usage.UsedBytes++
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("one byte below required free-space floor = %v", err)
	}
	live = linuxLiveFrom(measurement)
	live.Usage.UsedInodes++
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("one inode below required free-inode floor = %v", err)
	}
	live = linuxLiveFrom(measurement)
	policy.LaneRuntime.BackupLowWaterBytes = 17 * GiB
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("external backup at exact low-water: %v", err)
	}
	policy.LaneRuntime.BackupLowWaterBytes = 18 * GiB
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("external backup below low-water = %v", err)
	}
	policy.LaneRuntime.BackupLowWaterBytes = 2 * GiB
	policy.LaneRuntime.DevHardBytes = math.MaxUint64
	if err := ValidatePolicy(policy, measurement, linuxLiveFrom(measurement), now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("overflowing storage envelope = %v", err)
	}
	policy.LaneRuntime.DevHardBytes = 4 * GiB
	policy.Compute = ComputeNone
	if err := ValidatePolicy(policy, measurement, linuxLiveFrom(measurement), now); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Linux policy without cgroup reservation = %v", err)
	}
	policy.Compute = ComputeLinuxCgroupV2
	policy.LaneRuntime.Resources.PRDFloor.CPUMilli = 8_001
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("false compute envelope claim = %v", err)
	}
}

func TestL207BuildsDevAndQASControlsFromFreshMeasuredDevice(t *testing.T) {
	measurement, live, policy, now := linuxL207Fixture(t)
	for _, lane := range []string{"dev", "qas"} {
		policy.Lane = lane
		plan, err := BuildLinuxControlPlan(policy, measurement, live, now)
		if err != nil {
			t.Fatalf("%s control plan: %v", lane, err)
		}
		wantSlice := "remote-session-runner-" + lane + ".slice"
		if plan.HostID != "linux-host" || plan.Scope != ScopeLaneRuntime || plan.Lane != lane ||
			plan.SliceUnit != wantSlice || plan.Device.ID != "lane-capacity" ||
			plan.Device.MajorMinor != "259:1" || plan.Resources.Tasks != 300 ||
			plan.Resources.IOReadIOPSMax != 3_000 || plan.Resources.IOWriteIOPSMax != 3_000 {
			t.Errorf("%s control plan = %+v", lane, plan)
		}
	}
}

func TestL207BuildsManagementControlsAndRejectsMissingOrWrongDevice(t *testing.T) {
	measurement, live, policy, now := linuxL207Fixture(t)
	policy.Scope, policy.Lane, policy.LaneRuntime = ScopeHostManagement, "", nil
	policy.HostManagement = &HostManagementBudget{
		Components: []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
		PeakBytes:  2 * GiB, PeakInodes: 50_000, StagingBudgetBytes: GiB, LogBudgetBytes: GiB,
		GoCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB, BackupBudgetBytes: 3 * GiB,
		BackupLowWaterBytes: 2 * GiB, GoCachePaths: measurement.GoCachePaths, IODeviceID: "lane-capacity",
		Resources: ResourceEnvelope{
			PRDFloor:          ResourceVector{CPUMilli: 5_000, MemoryBytes: 12 * GiB, Tasks: 700, IOReadBytesPerSecond: 70 * 1024 * 1024, IOWriteBytesPerSecond: 70 * 1024 * 1024, IOReadIOPSMax: 7_000, IOWriteIOPSMax: 7_000},
			ManagementHardMax: ResourceVector{CPUMilli: 1_000, MemoryBytes: 2 * GiB, Tasks: 200, IOReadBytesPerSecond: 10 * 1024 * 1024, IOWriteBytesPerSecond: 10 * 1024 * 1024, IOReadIOPSMax: 1_000, IOWriteIOPSMax: 1_000},
		},
	}
	plan, err := BuildLinuxControlPlan(policy, measurement, live, now)
	if err != nil {
		t.Fatalf("management control plan: %v", err)
	}
	if plan.Scope != ScopeHostManagement || plan.Lane != "" || plan.SliceUnit != "remote-session-runner-management.slice" ||
		plan.Resources.Tasks != 200 || plan.Device.ID != "lane-capacity" {
		t.Fatalf("management control plan = %+v", plan)
	}

	policy.HostManagement.IODeviceID = "unmeasured-device"
	if _, err := BuildLinuxControlPlan(policy, measurement, live, now); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unmeasured I/O device accepted: %v", err)
	}
	policy.HostManagement.IODeviceID = "lane-capacity"
	policy.HostManagement.Resources.ManagementHardMax.IOReadIOPSMax = 0
	if _, err := BuildLinuxControlPlan(policy, measurement, live, now); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("missing IOPS dimension accepted: %v", err)
	}
}

func TestL206MacPoliciesRequireNoComputeAndObservedAPFSIdentities(t *testing.T) {
	measurement, live, policy, now := macPolicyFixture(t, "dev")
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("valid measured Dev/QAS APFS policy: %v", err)
	}
	policy.Compute = ComputeLinuxCgroupV2
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrUnsupportedCompute) {
		t.Fatalf("Mac compute claim = %v", err)
	}
	policy.Compute = ComputeNone
	policy.LaneRuntime.MacData.DevVolumeUUID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("unmeasured APFS volume claim = %v", err)
	}
}

func TestL208MacPolicyRequiresMeasuredAPFSQuotasAndBoundedSharedCacheVolumes(t *testing.T) {
	measurement, live, policy, now := macPolicyFixture(t, "dev")
	measurement.StorageVolumes[1].QuotaBytes--
	live.StorageVolumes = append([]StorageVolume(nil), measurement.StorageVolumes...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("changed Dev APFS quota accepted: %v", err)
	}

	measurement, live, policy, now = macPolicyFixture(t, "qas")
	measurement.StorageVolumes[4].QuotaBytes--
	live.StorageVolumes = append([]StorageVolume(nil), measurement.StorageVolumes...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("shared-cache volume quota below the approved aggregate accepted: %v", err)
	}

	measurement, _, _, _ = macPolicyFixture(t, "dev")
	measurement.StorageVolumes[4].QuotaBytes = measurement.StorageVolumes[4].CapacityBytes + 1
	if _, err := measurement.EvaluationDigest(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("APFS quota larger than measured volume capacity accepted: %v", err)
	}

	measurement, _, _, _ = macPolicyFixture(t, "dev")
	measurement.GoCachePaths[0] = "/private/tmp/runner-lane-local-cache"
	if _, err := measurement.EvaluationDigest(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("lane-specific Mac cache path accepted: %v", err)
	}
}

func TestL206MacStorageUsesMaximumSnapshotAndFifteenPercentOrTwentyGiBFloor(t *testing.T) {
	measurement, live, policy, now := macPolicyFixture(t, "dev")
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("exact 15 percent floor: %v", err)
	}
	policy.LaneRuntime.MacData.PRDHostBudgetBytes++
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("one byte beyond 15 percent floor = %v", err)
	}
	measurement, live, policy, now = macPolicyFixture(t, "dev")
	policy.LaneRuntime.MacData.SnapshotBudgetBytes = 10*GiB + 1
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("snapshot budget above 10 GiB = %v", err)
	}
	measurement, live, policy, now = macPolicyFixture(t, "dev")
	live.Usage.SnapshotBytes = policy.LaneRuntime.MacData.SnapshotBudgetBytes + 1
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("observed snapshots above budget = %v", err)
	}
}

func TestL206HostManagementDoesNotRequireFutureLaneVolumes(t *testing.T) {
	measurement, live, policy, now := macManagementFixture(t)
	if len(measurement.StorageVolumes) != 3 {
		t.Fatalf("fixture has %d measured volumes, want Data plus both shared-cache volumes", len(measurement.StorageVolumes))
	}
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("host management without future Dev/QAS volumes: %v", err)
	}
	policy.HostManagement.MacData.DevVolumeUUID = "22222222-2222-2222-2222-222222222222"
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("host-management lane volume dependency = %v", err)
	}
}

func TestL206LinuxHostManagementPolicyCarriesPRDFloorAndSharedCacheBudget(t *testing.T) {
	measurement, live, _, now := linuxPolicyFixture(t)
	policy := Policy{Version: 1, HostID: measurement.Identity.HostID, Scope: ScopeHostManagement, MeasurementID: measurement.ID,
		NotBefore: measurement.ObservedAt, NotAfter: now.Add(time.Minute), Compute: ComputeLinuxCgroupV2}
	policy.HostManagement = &HostManagementBudget{
		Components: []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
		PeakBytes:  2 * GiB, PeakInodes: 50_000, StagingBudgetBytes: GiB, LogBudgetBytes: GiB,
		GoCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB, BackupBudgetBytes: 3 * GiB,
		BackupLowWaterBytes: 2 * GiB, GoCachePaths: measurement.GoCachePaths, IODeviceID: measurement.Identity.DeviceID,
		Resources: ResourceEnvelope{
			PRDFloor:          ResourceVector{CPUMilli: 5_000, MemoryBytes: 12 * GiB, Tasks: 700, IOReadBytesPerSecond: 70 * 1024 * 1024, IOWriteBytesPerSecond: 70 * 1024 * 1024, IOReadIOPSMax: 7_000, IOWriteIOPSMax: 7_000},
			ManagementHardMax: ResourceVector{CPUMilli: 1_000, MemoryBytes: 2 * GiB, Tasks: 200, IOReadBytesPerSecond: 10 * 1024 * 1024, IOWriteBytesPerSecond: 10 * 1024 * 1024, IOReadIOPSMax: 1_000, IOWriteIOPSMax: 1_000},
		},
	}
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("Linux host-management policy: %v", err)
	}
}

func TestL206MacPRDSupportsMeasuredHostFreeSpaceAndAPFSQuotaModes(t *testing.T) {
	measurement, live, policy, now := macPolicyFixture(t, "prd")
	policy.LaneRuntime.MacData.DevVolumeUUID, policy.LaneRuntime.MacData.DevMountpoint, policy.LaneRuntime.MacData.DevQuotaBytes = "", "", 0
	policy.LaneRuntime.MacData.QASVolumeUUID, policy.LaneRuntime.MacData.QASMountpoint, policy.LaneRuntime.MacData.QASQuotaBytes = "", "", 0
	policy.LaneRuntime.MacData.StorageMode = StorageHostFreeSpaceBudget
	policy.LaneRuntime.MacData.PRDHostBudgetBytes = 24 * GiB
	policy.LaneRuntime.MacData.PRDVolumeUUID, policy.LaneRuntime.MacData.PRDMountpoint, policy.LaneRuntime.MacData.PRDQuotaBytes = "", "", 0
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("PRD host-free-space budget: %v", err)
	}
	policy.LaneRuntime.MacData.StorageMode = StorageAPFSQuota
	policy.LaneRuntime.MacData.PRDHostBudgetBytes = 0
	policy.LaneRuntime.MacData.PRDVolumeUUID = "44444444-4444-4444-4444-444444444444"
	policy.LaneRuntime.MacData.PRDMountpoint = "/Volumes/Runner-PRD"
	policy.LaneRuntime.MacData.PRDQuotaBytes = 24 * GiB
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("PRD APFS quota budget: %v", err)
	}
}

func TestL206MacEnforcesTwentyGiBMinimumWhenItExceedsFifteenPercent(t *testing.T) {
	measurement, live, policy, now := macPolicyFixture(t, "dev")
	measurement.Identity.CapacityBytes = 100 * GiB
	measurement.StorageVolumes[0].CapacityBytes = 100 * GiB
	measurement.Usage.NonPurgeableBytes = 10 * GiB
	live.Identity = measurement.Identity
	live.StorageVolumes = append([]StorageVolume(nil), measurement.StorageVolumes...)
	live.Usage = measurement.Usage
	policy.LaneRuntime.MacData.PRDHostBudgetBytes = 24 * GiB
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		t.Fatalf("policy at 20 GiB Mac minimum free floor: %v", err)
	}
	policy.LaneRuntime.MacData.PRDHostBudgetBytes++
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("one byte beyond 20 GiB Mac floor = %v", err)
	}
}

func TestL206ChangedHostCacheOrVolumeIdentityRefusesPolicy(t *testing.T) {
	measurement, live, policy, now := linuxPolicyFixture(t)
	live.Identity.DeviceID = "/dev/changed"
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("changed storage device = %v", err)
	}
	live = linuxLiveFrom(measurement)
	live.GoCachePaths = []string{"/tmp/other-cache"}
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("changed Go cache identity = %v", err)
	}
	measurement, live, policy, now = macPolicyFixture(t, "qas")
	live.StorageVolumes = append([]StorageVolume(nil), measurement.StorageVolumes...)
	live.StorageVolumes[1].DeviceID = "disk-changed"
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("changed APFS volume identity = %v", err)
	}
}

func TestL206StaleMeasurementsAndInvalidCacheSetsAreRejected(t *testing.T) {
	measurement, live, policy, now := linuxPolicyFixture(t)
	live.ObservedAt = now.Add(-2 * time.Minute)
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrStaleMeasurement) {
		t.Fatalf("stale live observation = %v", err)
	}
	measurement, _, _, _ = linuxPolicyFixture(t)
	measurement.GoCachePaths = []string{"/home/ubuntu/.cache/go-build"}
	if _, err := measurement.EvaluationDigest(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("missing GOMODCACHE identity = %v", err)
	}
	measurement, _, _, _ = linuxPolicyFixture(t)
	measurement.GoCachePaths = []string{"/home/ubuntu/go/pkg/mod", "/home/ubuntu/.cache/go-build"}
	if _, err := measurement.EvaluationDigest(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unsorted shared cache identity = %v", err)
	}
	measurement.GoCachePaths = []string{"/home/ubuntu/.cache/RemoteSessionRunner-dev/go-build"}
	if _, err := measurement.EvaluationDigest(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("lane-local cache presented as shared = %v", err)
	}
}

func TestL206EvaluationDigestBindsMeasurementUsageAndStorageIdentity(t *testing.T) {
	measurement, _, _, _ := linuxPolicyFixture(t)
	first, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	measurement.Usage.UsedBytes++
	second, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("measurement digest did not change with measured usage")
	}
}

func linuxPolicyFixture(t *testing.T) (Measurement, LiveObservation, Policy, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	identity := Identity{
		HostID: "linux-host", Platform: PlatformLinux, DeviceID: "/dev/vda1", Filesystem: "ext4", Mountpoint: "/home/ubuntu",
		CapacityBytes: 100 * GiB, CapacityInodes: 1_000_000,
		BackupDeviceID: "/dev/vdb1", BackupFilesystem: "ext4", BackupMountpoint: "/mnt/runner-backup", BackupCapacityBytes: 20 * GiB,
	}
	resources := ResourceVector{CPUMilli: 8_000, MemoryBytes: 16 * GiB, Tasks: 1_000, IOReadBytesPerSecond: 100 * 1024 * 1024, IOWriteBytesPerSecond: 100 * 1024 * 1024, IOReadIOPSMax: 10_000, IOWriteIOPSMax: 10_000}
	paths := []string{"/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod"}
	measurement := Measurement{
		ID: "measure-linux-01", Identity: identity, ObservedAt: now.Add(-10 * time.Second), ExpiresAt: now.Add(5 * time.Minute),
		ResourceCapacity: resources, GoCachePaths: paths,
		Usage: Usage{UsedBytes: 50 * GiB, UsedInodes: 500_000, GoCacheBytes: GiB, ReleaseCacheBytes: GiB, BackupBytes: 2 * GiB},
	}
	budget := LaneRuntimeBudget{
		PRDGrowthBytes: 6 * GiB, DevHardBytes: 4 * GiB, QASHardBytes: 4 * GiB, ManagementGrowthBytes: 2 * GiB,
		PRDGrowthInodes: 50_000, DevHardInodes: 50_000, QASHardInodes: 50_000, ManagementGrowthInodes: 50_000,
		GoCachePaths: paths, GoCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB,
		BackupBudgetBytes: 3 * GiB, BackupLowWaterBytes: 2 * GiB, IODeviceID: identity.DeviceID,
		Resources: ResourceEnvelope{
			PRDFloor:          ResourceVector{CPUMilli: 1_000, MemoryBytes: 4 * GiB, Tasks: 100, IOReadBytesPerSecond: 20 * 1024 * 1024, IOWriteBytesPerSecond: 20 * 1024 * 1024, IOReadIOPSMax: 2_000, IOWriteIOPSMax: 2_000},
			DevHardMax:        ResourceVector{CPUMilli: 2_500, MemoryBytes: 4 * GiB, Tasks: 300, IOReadBytesPerSecond: 30 * 1024 * 1024, IOWriteBytesPerSecond: 30 * 1024 * 1024, IOReadIOPSMax: 3_000, IOWriteIOPSMax: 3_000},
			QASHardMax:        ResourceVector{CPUMilli: 2_500, MemoryBytes: 4 * GiB, Tasks: 300, IOReadBytesPerSecond: 30 * 1024 * 1024, IOWriteBytesPerSecond: 30 * 1024 * 1024, IOReadIOPSMax: 3_000, IOWriteIOPSMax: 3_000},
			ManagementHardMax: ResourceVector{CPUMilli: 1_000, MemoryBytes: 2 * GiB, Tasks: 200, IOReadBytesPerSecond: 10 * 1024 * 1024, IOWriteBytesPerSecond: 10 * 1024 * 1024, IOReadIOPSMax: 1_000, IOWriteIOPSMax: 1_000},
		},
	}
	policy := Policy{Version: 1, HostID: identity.HostID, Scope: ScopeLaneRuntime, Lane: "dev", MeasurementID: measurement.ID,
		NotBefore: measurement.ObservedAt, NotAfter: now.Add(time.Minute), Compute: ComputeLinuxCgroupV2, LaneRuntime: &budget}
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	policy.EvaluationDigest = digest
	return measurement, linuxLiveFrom(measurement), policy, now
}

func linuxLiveFrom(measurement Measurement) LiveObservation {
	return LiveObservation{Identity: measurement.Identity, ObservedAt: measurement.ObservedAt.Add(time.Second),
		ResourceCapacity: measurement.ResourceCapacity, GoCachePaths: append([]string(nil), measurement.GoCachePaths...), Usage: measurement.Usage}
}

func linuxL207Fixture(t *testing.T) (Measurement, LiveObservation, Policy, time.Time) {
	t.Helper()
	measurement, live, policy, now := linuxPolicyFixture(t)
	measurement.LinuxDevices = []LinuxDevice{
		{ID: "cache-backup", UUID: "22222222-2222-2222-2222-222222222222", MajorMinor: "259:2", Filesystem: "ext4",
			Mountpoint: LinuxBackupMountpoint, CapacityBytes: 20 * GiB, CapacityInodes: 1_000_000, ProjectQuotaFeature: true, ProjectQuotaActive: true},
		{ID: "lane-capacity", UUID: "11111111-1111-1111-1111-111111111111", MajorMinor: "259:1", Filesystem: "ext4",
			Mountpoint: LinuxCapacityMountpoint, CapacityBytes: 96 * GiB, CapacityInodes: 4_000_000,
			IOReadBytesPerSecondCapacity: 100 * 1024 * 1024, IOWriteBytesPerSecondCapacity: 100 * 1024 * 1024,
			IOReadIOPSCapacity: 10_000, IOWriteIOPSCapacity: 10_000, ProjectQuotaFeature: true, ProjectQuotaActive: true},
	}
	live.LinuxDevices = append([]LinuxDevice(nil), measurement.LinuxDevices...)
	policy.LaneRuntime.IODeviceID = "lane-capacity"
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	return measurement, live, policy, now
}

func TestL207RejectsChangedOrInactiveMeasuredLinuxDevice(t *testing.T) {
	measurement, live, policy, now := linuxL207Fixture(t)
	live.LinuxDevices[1].MajorMinor = "259:9"
	if err := ValidatePolicy(policy, measurement, live, now); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("changed major/minor accepted: %v", err)
	}

	measurement, live, policy, now = linuxL207Fixture(t)
	measurement.LinuxDevices[1].ProjectQuotaActive = false
	live.LinuxDevices = append([]LinuxDevice(nil), measurement.LinuxDevices...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxControlPlan(policy, measurement, live, now); !errors.Is(err, ErrLinuxControlsUnavailable) {
		t.Fatalf("inactive project quota accepted: %v", err)
	}

	measurement, live, policy, now = linuxL207Fixture(t)
	policy.LaneRuntime.Resources.DevHardMax.CPUMilli++
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxControlPlan(policy, measurement, live, now); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("fractional CPUQuota accepted: %v", err)
	}
}

func TestL207RejectsLinuxDeviceUseAboveMeasuredCapacity(t *testing.T) {
	measurement, _, _, _ := linuxL207Fixture(t)
	measurement.LinuxDevices[1].UsedInodes = measurement.LinuxDevices[1].CapacityInodes + 1
	if _, err := measurement.EvaluationDigest(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("device inode use above measured capacity accepted: %v", err)
	}
}

func TestL207RejectsResourceEnvelopeAboveMeasuredDeviceIOCapacity(t *testing.T) {
	measurement, live, policy, now := linuxL207Fixture(t)
	measurement.LinuxDevices[1].IOReadBytesPerSecondCapacity = 80 * 1024 * 1024
	live.LinuxDevices = append([]LinuxDevice(nil), measurement.LinuxDevices...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxControlPlan(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("aggregate policy beyond measured device bandwidth accepted: %v", err)
	}

	measurement, live, policy, now = linuxL207Fixture(t)
	measurement.LinuxDevices[1].IOWriteIOPSCapacity = 8_999
	live.LinuxDevices = append([]LinuxDevice(nil), measurement.LinuxDevices...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildLinuxControlPlan(policy, measurement, live, now); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("aggregate policy beyond measured device IOPS accepted: %v", err)
	}
}

func macPolicyFixture(t *testing.T, lane string) (Measurement, LiveObservation, Policy, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	measurement, live, policy := macBaseFixture(t, now)
	policy.Lane = lane
	policy.Scope = ScopeLaneRuntime
	policy.Compute = ComputeNone
	policy.LaneRuntime = &LaneRuntimeBudget{
		GoCachePaths: measurement.GoCachePaths, GoCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB,
		BackupBudgetBytes: 3 * GiB, BackupLowWaterBytes: 2 * GiB,
		MacData: &MacDataBudget{
			StorageMode: StorageAPFSQuota, PRDHostBudgetBytes: 24 * GiB,
			DevVolumeUUID: "22222222-2222-2222-2222-222222222222", DevMountpoint: "/Volumes/Runner-Dev", DevQuotaBytes: 15 * GiB,
			QASVolumeUUID: "33333333-3333-3333-3333-333333333333", QASMountpoint: "/Volumes/Runner-QAS", QASQuotaBytes: 15 * GiB,
			SharedCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB, SnapshotBudgetBytes: 10 * GiB,
			BackupBudgetBytes: 3 * GiB, BackupLowWaterBytes: 2 * GiB,
		},
	}
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	policy.EvaluationDigest = digest
	return measurement, live, policy, now
}

func macManagementFixture(t *testing.T) (Measurement, LiveObservation, Policy, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	measurement, live, policy := macBaseFixture(t, now)
	measurement.StorageVolumes = []StorageVolume{measurement.StorageVolumes[0], measurement.StorageVolumes[4], measurement.StorageVolumes[5]}
	live.StorageVolumes = append([]StorageVolume(nil), measurement.StorageVolumes...)
	budget := &MacDataBudget{PRDHostBudgetBytes: 24 * GiB, SharedCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB,
		SnapshotBudgetBytes: 10 * GiB, BackupBudgetBytes: 3 * GiB, BackupLowWaterBytes: 2 * GiB}
	policy.Scope, policy.Lane, policy.Compute = ScopeHostManagement, "", ComputeNone
	policy.LaneRuntime = nil
	policy.HostManagement = &HostManagementBudget{
		Components: []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer",
			"runner-release-authority-signer", "runner-release-authority-agent", "runner-release-authority-approval", "runner-release-transport-pki-issuer"},
		PeakBytes: 2 * GiB, StagingBudgetBytes: GiB, LogBudgetBytes: GiB, GoCacheBudgetBytes: 4 * GiB, ReleaseCacheBudgetBytes: 2 * GiB,
		BackupBudgetBytes: 3 * GiB, BackupLowWaterBytes: 2 * GiB, GoCachePaths: measurement.GoCachePaths, MacData: budget,
	}
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	policy.EvaluationDigest = digest
	return measurement, live, policy, now
}

func macBaseFixture(t *testing.T, now time.Time) (Measurement, LiveObservation, Policy) {
	t.Helper()
	data := StorageVolume{UUID: "11111111-1111-1111-1111-111111111111", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/System/Volumes/Data", CapacityBytes: 200 * GiB}
	dev := StorageVolume{UUID: "22222222-2222-2222-2222-222222222222", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/Volumes/Runner-Dev", CapacityBytes: 30 * GiB, QuotaBytes: 15 * GiB}
	qas := StorageVolume{UUID: "33333333-3333-3333-3333-333333333333", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/Volumes/Runner-QAS", CapacityBytes: 30 * GiB, QuotaBytes: 15 * GiB}
	prd := StorageVolume{UUID: "44444444-4444-4444-4444-444444444444", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/Volumes/Runner-PRD", CapacityBytes: 30 * GiB, QuotaBytes: 24 * GiB}
	goBuildCache := StorageVolume{UUID: "55555555-5555-5555-5555-555555555555", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: MacGoBuildCachePath, CapacityBytes: 2 * GiB, QuotaBytes: 2 * GiB}
	goModuleCache := StorageVolume{UUID: "66666666-6666-6666-6666-666666666666", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: MacGoModuleCachePath, CapacityBytes: 2 * GiB, QuotaBytes: 2 * GiB}
	identity := Identity{HostID: "mac-control-plane", Platform: PlatformMacOS, DeviceID: "disk3", Filesystem: "apfs", Mountpoint: data.Mountpoint,
		VolumeUUID: data.UUID, CapacityBytes: data.CapacityBytes, BackupDeviceID: "disk4", BackupFilesystem: "apfs", BackupMountpoint: "/Volumes/Runner-Backup", BackupCapacityBytes: 20 * GiB}
	paths := []string{MacGoBuildCachePath, MacGoModuleCachePath}
	measurement := Measurement{ID: "measure-mac-01", Identity: identity, ObservedAt: now.Add(-10 * time.Second), ExpiresAt: now.Add(5 * time.Minute),
		GoCachePaths: paths, StorageVolumes: []StorageVolume{data, dev, qas, prd, goBuildCache, goModuleCache},
		Usage: Usage{NonPurgeableBytes: 100 * GiB, SnapshotBytes: 8 * GiB, GoCacheBytes: 4 * GiB, ReleaseCacheBytes: 2 * GiB, BackupBytes: 2 * GiB}}
	policy := Policy{Version: 1, HostID: identity.HostID, Scope: ScopeLaneRuntime, Lane: "dev", MeasurementID: measurement.ID,
		NotBefore: measurement.ObservedAt, NotAfter: now.Add(time.Minute), Compute: ComputeNone}
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	policy.EvaluationDigest = digest
	live := LiveObservation{Identity: identity, ObservedAt: now.Add(-time.Second), GoCachePaths: append([]string(nil), paths...),
		StorageVolumes: append([]StorageVolume(nil), measurement.StorageVolumes...), Usage: measurement.Usage}
	return measurement, live, policy
}
