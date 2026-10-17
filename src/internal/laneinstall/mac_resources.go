package laneinstall

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/lanecapacity"
)

var ErrMacResourcesUnavailable = errors.New("Mac storage plan requires fresh measured APFS lane and shared-cache volumes")

type MacQuotaVolume struct {
	Lane       config.ReleaseLane
	UUID       string
	Mountpoint string
	QuotaBytes uint64
}

// MacStorageControlPlan is the validated, non-mutating storage contract that
// a later Mac installer must enforce. APFS volumes are provisioned and quota
// configured by the separately approved storage handoff; this function never
// creates, mounts, resizes, or deletes a volume.
type MacStorageControlPlan struct {
	HostID                  string
	Lane                    config.ReleaseLane
	DataVolume              lanecapacity.StorageVolume
	LaneVolumes             []MacQuotaVolume
	SharedCacheVolumes      []lanecapacity.StorageVolume
	SharedCachePaths        []string
	SharedCacheBudgetBytes  uint64
	ReleaseCacheBudgetBytes uint64
	SnapshotBudgetBytes     uint64
	ObservedSnapshotBytes   uint64
	PRDHostBudgetBytes      uint64
	BackupOnDataVolume      bool
	BackupBudgetBytes       uint64
	BackupLowWaterBytes     uint64
	BackupDeviceID          string
	BackupMountpoint        string
	BackupCapacityBytes     uint64
	ProjectedDataUseBytes   uint64
	RequiredFreeFloorBytes  uint64
	ProjectedFreeBytes      uint64
	ComputeReservation      lanecapacity.ComputeReservation
}

// BuildMacStorageControlPlan binds a Dev or QAS resource plan to the exact
// fixed Mac service-root mountpoints, their measured APFS quotas, the bounded
// shared Go-cache volumes, and the full Data-volume headroom equation.
// It returns data only and performs no filesystem or service operation.
func BuildMacStorageControlPlan(policy lanecapacity.Policy, measurement lanecapacity.Measurement,
	live lanecapacity.LiveObservation, now time.Time) (MacStorageControlPlan, error) {
	if measurement.Identity.Platform != lanecapacity.PlatformMacOS || policy.Scope != lanecapacity.ScopeLaneRuntime ||
		policy.HostID != "mac-control-plane" || (policy.Lane != string(config.ReleaseLaneDev) && policy.Lane != string(config.ReleaseLaneQAS)) ||
		policy.LaneRuntime == nil || policy.HostManagement != nil {
		return MacStorageControlPlan{}, ErrMacResourcesUnavailable
	}
	if err := lanecapacity.ValidatePolicy(policy, measurement, live, now); err != nil {
		return MacStorageControlPlan{}, fmt.Errorf("validate capacity policy: %w", err)
	}
	devNamespace, err := config.DeriveLaneNamespace(config.HostKindMac, config.ReleaseLaneDev, "")
	if err != nil {
		return MacStorageControlPlan{}, fmt.Errorf("derive fixed Dev namespace: %w", err)
	}
	qasNamespace, err := config.DeriveLaneNamespace(config.HostKindMac, config.ReleaseLaneQAS, "")
	if err != nil {
		return MacStorageControlPlan{}, fmt.Errorf("derive fixed QAS namespace: %w", err)
	}
	budget := policy.LaneRuntime.MacData
	if budget == nil || budget.DevMountpoint != devNamespace.ServiceRoot || budget.QASMountpoint != qasNamespace.ServiceRoot {
		return MacStorageControlPlan{}, ErrMacResourcesUnavailable
	}
	dataVolume, ok := measuredVolumeByUUID(measurement.StorageVolumes, measurement.Identity.VolumeUUID)
	if !ok || dataVolume.Mountpoint != measurement.Identity.Mountpoint || dataVolume.CapacityBytes != measurement.Identity.CapacityBytes {
		return MacStorageControlPlan{}, ErrMacResourcesUnavailable
	}
	devVolume, ok := measuredVolumeByUUID(measurement.StorageVolumes, budget.DevVolumeUUID)
	if !ok || devVolume.Mountpoint != devNamespace.ServiceRoot || devVolume.QuotaBytes != budget.DevQuotaBytes {
		return MacStorageControlPlan{}, ErrMacResourcesUnavailable
	}
	qasVolume, ok := measuredVolumeByUUID(measurement.StorageVolumes, budget.QASVolumeUUID)
	if !ok || qasVolume.Mountpoint != qasNamespace.ServiceRoot || qasVolume.QuotaBytes != budget.QASQuotaBytes {
		return MacStorageControlPlan{}, ErrMacResourcesUnavailable
	}
	cacheVolumes := make([]lanecapacity.StorageVolume, 0, len(measurement.GoCachePaths))
	for _, path := range measurement.GoCachePaths {
		volume, found := measuredVolumeByMountpoint(measurement.StorageVolumes, path)
		if !found || volume.DeviceID != measurement.Identity.DeviceID || volume.QuotaBytes == 0 {
			return MacStorageControlPlan{}, ErrMacResourcesUnavailable
		}
		cacheVolumes = append(cacheVolumes, volume)
	}
	projectedDataUse, ok := macProjectedDataUse(live.Usage, budget)
	if !ok || projectedDataUse > measurement.Identity.CapacityBytes {
		return MacStorageControlPlan{}, lanecapacity.ErrCapacityExceeded
	}
	floor := max(20*lanecapacity.GiB, macCeilPercent(measurement.Identity.CapacityBytes, 15))
	projectedFree := measurement.Identity.CapacityBytes - projectedDataUse
	if projectedFree < floor {
		return MacStorageControlPlan{}, lanecapacity.ErrCapacityExceeded
	}
	cachePaths := append([]string(nil), measurement.GoCachePaths...)
	if !slices.Equal(cachePaths, []string{lanecapacity.MacGoBuildCachePath, lanecapacity.MacGoModuleCachePath}) {
		return MacStorageControlPlan{}, ErrMacResourcesUnavailable
	}
	return MacStorageControlPlan{
		HostID: measurement.Identity.HostID, Lane: config.ReleaseLane(policy.Lane), DataVolume: dataVolume,
		LaneVolumes: []MacQuotaVolume{
			{Lane: config.ReleaseLaneDev, UUID: devVolume.UUID, Mountpoint: devVolume.Mountpoint, QuotaBytes: devVolume.QuotaBytes},
			{Lane: config.ReleaseLaneQAS, UUID: qasVolume.UUID, Mountpoint: qasVolume.Mountpoint, QuotaBytes: qasVolume.QuotaBytes},
		},
		SharedCacheVolumes: cacheVolumes, SharedCachePaths: cachePaths,
		SharedCacheBudgetBytes: budget.SharedCacheBudgetBytes, ReleaseCacheBudgetBytes: budget.ReleaseCacheBudgetBytes,
		SnapshotBudgetBytes: budget.SnapshotBudgetBytes, ObservedSnapshotBytes: live.Usage.SnapshotBytes,
		PRDHostBudgetBytes: budget.PRDHostBudgetBytes,
		BackupOnDataVolume: budget.BackupOnDataVolume, BackupBudgetBytes: budget.BackupBudgetBytes,
		BackupLowWaterBytes: budget.BackupLowWaterBytes, BackupDeviceID: measurement.Identity.BackupDeviceID,
		BackupMountpoint: measurement.Identity.BackupMountpoint, BackupCapacityBytes: measurement.Identity.BackupCapacityBytes,
		ProjectedDataUseBytes: projectedDataUse, RequiredFreeFloorBytes: floor, ProjectedFreeBytes: projectedFree,
		ComputeReservation: policy.Compute,
	}, nil
}

func measuredVolumeByUUID(volumes []lanecapacity.StorageVolume, uuid string) (lanecapacity.StorageVolume, bool) {
	for _, volume := range volumes {
		if volume.UUID == uuid {
			return volume, true
		}
	}
	return lanecapacity.StorageVolume{}, false
}

func measuredVolumeByMountpoint(volumes []lanecapacity.StorageVolume, mountpoint string) (lanecapacity.StorageVolume, bool) {
	for _, volume := range volumes {
		if volume.Mountpoint == mountpoint {
			return volume, true
		}
	}
	return lanecapacity.StorageVolume{}, false
}

func macProjectedDataUse(usage lanecapacity.Usage, budget *lanecapacity.MacDataBudget) (uint64, bool) {
	if budget == nil {
		return 0, false
	}
	values := []uint64{usage.NonPurgeableBytes, budget.SnapshotBudgetBytes, budget.PRDHostBudgetBytes,
		budget.PRDQuotaBytes, budget.DevQuotaBytes, budget.QASQuotaBytes, budget.SharedCacheBudgetBytes,
		budget.ReleaseCacheBudgetBytes}
	if budget.BackupOnDataVolume {
		values = append(values, max(budget.BackupBudgetBytes, usage.BackupBytes))
	}
	var total uint64
	for _, value := range values {
		if value > math.MaxUint64-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func macCeilPercent(value, percent uint64) uint64 {
	whole := value / 100
	remainder := value % 100
	partial := remainder * percent
	if partial%100 != 0 {
		partial = partial/100 + 1
	} else {
		partial /= 100
	}
	return whole*percent + partial
}
