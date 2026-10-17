package laneinstall

import (
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/lanecapacity"
)

func TestLanesL208BuildsMeasuredMacQuotaAndHeadroomPlan(t *testing.T) {
	measurement, live, policy, now := macL208Fixture(t, config.ReleaseLaneDev)
	plan, err := BuildMacStorageControlPlan(policy, measurement, live, now)
	if err != nil {
		t.Fatalf("build measured Mac storage plan: %v", err)
	}
	devNS, err := config.DeriveLaneNamespace(config.HostKindMac, config.ReleaseLaneDev, "")
	if err != nil {
		t.Fatal(err)
	}
	qasNS, err := config.DeriveLaneNamespace(config.HostKindMac, config.ReleaseLaneQAS, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.HostID != "mac-control-plane" || plan.Lane != config.ReleaseLaneDev || plan.ComputeReservation != lanecapacity.ComputeNone {
		t.Fatalf("unexpected Mac storage identity or compute claim: %+v", plan)
	}
	if len(plan.LaneVolumes) != 2 || plan.LaneVolumes[0] != (MacQuotaVolume{
		Lane: config.ReleaseLaneDev, UUID: "22222222-2222-2222-2222-222222222222", Mountpoint: devNS.ServiceRoot, QuotaBytes: 15 * lanecapacity.GiB,
	}) || plan.LaneVolumes[1] != (MacQuotaVolume{
		Lane: config.ReleaseLaneQAS, UUID: "33333333-3333-3333-3333-333333333333", Mountpoint: qasNS.ServiceRoot, QuotaBytes: 15 * lanecapacity.GiB,
	}) {
		t.Fatalf("lane volume bindings = %+v", plan.LaneVolumes)
	}
	if len(plan.SharedCacheVolumes) != 2 || len(plan.SharedCachePaths) != 2 ||
		plan.SharedCachePaths[0] != lanecapacity.MacGoBuildCachePath || plan.SharedCachePaths[1] != lanecapacity.MacGoModuleCachePath ||
		plan.SharedCacheBudgetBytes != 4*lanecapacity.GiB {
		t.Fatalf("shared-cache bindings = %+v paths=%v budget=%d", plan.SharedCacheVolumes, plan.SharedCachePaths, plan.SharedCacheBudgetBytes)
	}
	if plan.SnapshotBudgetBytes != 10*lanecapacity.GiB || plan.ObservedSnapshotBytes != 8*lanecapacity.GiB ||
		plan.ProjectedDataUseBytes != 170*lanecapacity.GiB || plan.RequiredFreeFloorBytes != 30*lanecapacity.GiB ||
		plan.ProjectedFreeBytes != 30*lanecapacity.GiB {
		t.Fatalf("unexpected Mac headroom equation: %+v", plan)
	}
	if plan.BackupOnDataVolume || plan.BackupBudgetBytes != 3*lanecapacity.GiB || plan.BackupLowWaterBytes != 2*lanecapacity.GiB ||
		plan.BackupDeviceID != "disk4" || plan.BackupCapacityBytes != 20*lanecapacity.GiB {
		t.Fatalf("backup budget/identity is not retained: %+v", plan)
	}
}

func TestLanesL208RejectsWrongMacMountQuotaOrUnboundedCache(t *testing.T) {
	measurement, live, policy, now := macL208Fixture(t, config.ReleaseLaneQAS)
	policy.LaneRuntime.MacData.DevMountpoint += "-wrong"
	if _, err := BuildMacStorageControlPlan(policy, measurement, live, now); err == nil {
		t.Fatal("wrong fixed Dev service-root mountpoint accepted")
	}

	measurement, live, policy, now = macL208Fixture(t, config.ReleaseLaneDev)
	measurement.StorageVolumes[1].QuotaBytes--
	live.StorageVolumes = append([]lanecapacity.StorageVolume(nil), measurement.StorageVolumes...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildMacStorageControlPlan(policy, measurement, live, now); !errors.Is(err, lanecapacity.ErrIdentityChanged) {
		t.Fatalf("wrong measured APFS volume quota = %v", err)
	}

	measurement, live, policy, now = macL208Fixture(t, config.ReleaseLaneDev)
	measurement.StorageVolumes[4].QuotaBytes--
	live.StorageVolumes = append([]lanecapacity.StorageVolume(nil), measurement.StorageVolumes...)
	policy.EvaluationDigest, _ = measurement.EvaluationDigest()
	if _, err := BuildMacStorageControlPlan(policy, measurement, live, now); !errors.Is(err, lanecapacity.ErrIdentityChanged) {
		t.Fatalf("unbounded shared-cache volume = %v", err)
	}
}

func TestLanesL208RejectsPRDAndLowHeadroomPlans(t *testing.T) {
	measurement, live, policy, now := macL208Fixture(t, config.ReleaseLaneDev)
	policy.Lane = string(config.ReleaseLanePRD)
	if _, err := BuildMacStorageControlPlan(policy, measurement, live, now); !errors.Is(err, ErrMacResourcesUnavailable) {
		t.Fatalf("PRD storage plan accepted: %v", err)
	}

	measurement, live, policy, now = macL208Fixture(t, config.ReleaseLaneDev)
	policy.LaneRuntime.MacData.PRDHostBudgetBytes++
	if _, err := BuildMacStorageControlPlan(policy, measurement, live, now); !errors.Is(err, lanecapacity.ErrCapacityExceeded) {
		t.Fatalf("plan below the Mac floor accepted: %v", err)
	}
}

func macL208Fixture(t *testing.T, lane config.ReleaseLane) (lanecapacity.Measurement, lanecapacity.LiveObservation, lanecapacity.Policy, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	devNS, err := config.DeriveLaneNamespace(config.HostKindMac, config.ReleaseLaneDev, "")
	if err != nil {
		t.Fatal(err)
	}
	qasNS, err := config.DeriveLaneNamespace(config.HostKindMac, config.ReleaseLaneQAS, "")
	if err != nil {
		t.Fatal(err)
	}
	data := lanecapacity.StorageVolume{UUID: "11111111-1111-1111-1111-111111111111", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: "/System/Volumes/Data", CapacityBytes: 200 * lanecapacity.GiB}
	dev := lanecapacity.StorageVolume{UUID: "22222222-2222-2222-2222-222222222222", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: devNS.ServiceRoot, CapacityBytes: 30 * lanecapacity.GiB, QuotaBytes: 15 * lanecapacity.GiB}
	qas := lanecapacity.StorageVolume{UUID: "33333333-3333-3333-3333-333333333333", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: qasNS.ServiceRoot, CapacityBytes: 30 * lanecapacity.GiB, QuotaBytes: 15 * lanecapacity.GiB}
	goBuild := lanecapacity.StorageVolume{UUID: "55555555-5555-5555-5555-555555555555", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: lanecapacity.MacGoBuildCachePath, CapacityBytes: 2 * lanecapacity.GiB, QuotaBytes: 2 * lanecapacity.GiB}
	goModule := lanecapacity.StorageVolume{UUID: "66666666-6666-6666-6666-666666666666", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: lanecapacity.MacGoModuleCachePath, CapacityBytes: 2 * lanecapacity.GiB, QuotaBytes: 2 * lanecapacity.GiB}
	identity := lanecapacity.Identity{HostID: "mac-control-plane", Platform: lanecapacity.PlatformMacOS, DeviceID: "disk3",
		Filesystem: "apfs", Mountpoint: data.Mountpoint, VolumeUUID: data.UUID, CapacityBytes: data.CapacityBytes,
		BackupDeviceID: "disk4", BackupFilesystem: "apfs", BackupMountpoint: "/Volumes/RunnerBackup", BackupCapacityBytes: 20 * lanecapacity.GiB}
	paths := []string{lanecapacity.MacGoBuildCachePath, lanecapacity.MacGoModuleCachePath}
	measurement := lanecapacity.Measurement{ID: "measure-mac-l208", Identity: identity,
		ObservedAt: now.Add(-10 * time.Second), ExpiresAt: now.Add(5 * time.Minute), GoCachePaths: paths,
		StorageVolumes: []lanecapacity.StorageVolume{data, dev, qas, goBuild, goModule},
		Usage: lanecapacity.Usage{NonPurgeableBytes: 100 * lanecapacity.GiB, SnapshotBytes: 8 * lanecapacity.GiB,
			GoCacheBytes: 4 * lanecapacity.GiB, ReleaseCacheBytes: 2 * lanecapacity.GiB, BackupBytes: 2 * lanecapacity.GiB}}
	budget := &lanecapacity.MacDataBudget{StorageMode: lanecapacity.StorageAPFSQuota, PRDHostBudgetBytes: 24 * lanecapacity.GiB,
		DevVolumeUUID: dev.UUID, DevMountpoint: dev.Mountpoint, DevQuotaBytes: dev.QuotaBytes,
		QASVolumeUUID: qas.UUID, QASMountpoint: qas.Mountpoint, QASQuotaBytes: qas.QuotaBytes,
		SharedCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
		SnapshotBudgetBytes: 10 * lanecapacity.GiB, BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB}
	policy := lanecapacity.Policy{Version: 1, HostID: identity.HostID, Scope: lanecapacity.ScopeLaneRuntime,
		Lane: string(lane), MeasurementID: measurement.ID, NotBefore: measurement.ObservedAt,
		NotAfter: now.Add(time.Minute), Compute: lanecapacity.ComputeNone,
		LaneRuntime: &lanecapacity.LaneRuntimeBudget{GoCachePaths: paths, GoCacheBudgetBytes: 4 * lanecapacity.GiB,
			ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB, BackupBudgetBytes: 3 * lanecapacity.GiB,
			BackupLowWaterBytes: 2 * lanecapacity.GiB, MacData: budget}}
	policy.EvaluationDigest, err = measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	live := lanecapacity.LiveObservation{Identity: identity, ObservedAt: now.Add(-time.Second), GoCachePaths: append([]string(nil), paths...),
		StorageVolumes: append([]lanecapacity.StorageVolume(nil), measurement.StorageVolumes...), Usage: measurement.Usage}
	return measurement, live, policy, now
}
