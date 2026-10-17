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
	if len(measurement.StorageVolumes) != 1 {
		t.Fatalf("fixture has %d measured volumes, want only the existing Data volume", len(measurement.StorageVolumes))
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
		BackupLowWaterBytes: 2 * GiB, GoCachePaths: measurement.GoCachePaths,
		Resources: ResourceEnvelope{
			PRDFloor:          ResourceVector{CPUMilli: 5_000, MemoryBytes: 12 * GiB, Tasks: 700, IOReadBytesPerSecond: 70 * 1024 * 1024, IOWriteBytesPerSecond: 70 * 1024 * 1024},
			ManagementHardMax: ResourceVector{CPUMilli: 1_000, MemoryBytes: 2 * GiB, Tasks: 200, IOReadBytesPerSecond: 10 * 1024 * 1024, IOWriteBytesPerSecond: 10 * 1024 * 1024},
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
	resources := ResourceVector{CPUMilli: 8_000, MemoryBytes: 16 * GiB, Tasks: 1_000, IOReadBytesPerSecond: 100 * 1024 * 1024, IOWriteBytesPerSecond: 100 * 1024 * 1024}
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
			PRDFloor:          ResourceVector{CPUMilli: 1_000, MemoryBytes: 4 * GiB, Tasks: 100, IOReadBytesPerSecond: 20 * 1024 * 1024, IOWriteBytesPerSecond: 20 * 1024 * 1024},
			DevHardMax:        ResourceVector{CPUMilli: 2_500, MemoryBytes: 4 * GiB, Tasks: 300, IOReadBytesPerSecond: 30 * 1024 * 1024, IOWriteBytesPerSecond: 30 * 1024 * 1024},
			QASHardMax:        ResourceVector{CPUMilli: 2_500, MemoryBytes: 4 * GiB, Tasks: 300, IOReadBytesPerSecond: 30 * 1024 * 1024, IOWriteBytesPerSecond: 30 * 1024 * 1024},
			ManagementHardMax: ResourceVector{CPUMilli: 1_000, MemoryBytes: 2 * GiB, Tasks: 200, IOReadBytesPerSecond: 10 * 1024 * 1024, IOWriteBytesPerSecond: 10 * 1024 * 1024},
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
	measurement.StorageVolumes = measurement.StorageVolumes[:1]
	live.StorageVolumes = live.StorageVolumes[:1]
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
	dev := StorageVolume{UUID: "22222222-2222-2222-2222-222222222222", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/Volumes/Runner-Dev", CapacityBytes: 30 * GiB}
	qas := StorageVolume{UUID: "33333333-3333-3333-3333-333333333333", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/Volumes/Runner-QAS", CapacityBytes: 30 * GiB}
	prd := StorageVolume{UUID: "44444444-4444-4444-4444-444444444444", DeviceID: "disk3", Filesystem: "apfs", Mountpoint: "/Volumes/Runner-PRD", CapacityBytes: 30 * GiB}
	identity := Identity{HostID: "mac-control-plane", Platform: PlatformMacOS, DeviceID: "disk3", Filesystem: "apfs", Mountpoint: data.Mountpoint,
		VolumeUUID: data.UUID, CapacityBytes: data.CapacityBytes, BackupDeviceID: "disk4", BackupFilesystem: "apfs", BackupMountpoint: "/Volumes/Runner-Backup", BackupCapacityBytes: 20 * GiB}
	paths := []string{"/Users/runner/Library/Caches/go-build", "/Users/runner/go/pkg/mod"}
	measurement := Measurement{ID: "measure-mac-01", Identity: identity, ObservedAt: now.Add(-10 * time.Second), ExpiresAt: now.Add(5 * time.Minute),
		GoCachePaths: paths, StorageVolumes: []StorageVolume{data, dev, qas, prd},
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
