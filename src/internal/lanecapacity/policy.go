// Package lanecapacity validates host-scoped capacity policies before an
// installer or admission controller uses them. It performs no host probing;
// the trusted measurement and live observation are supplied by the later
// protected host helper.
package lanecapacity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	ScopeHostManagement Scope = "host_management"
	ScopeLaneRuntime    Scope = "lane_runtime"

	PlatformLinux Platform = "linux"
	PlatformMacOS Platform = "macos"

	ComputeLinuxCgroupV2 ComputeReservation = "linux_cgroup_v2"
	ComputeNone          ComputeReservation = "none"

	StorageHostFreeSpaceBudget = "host_free_space_budget"
	StorageAPFSQuota           = "apfs_quota"

	GiB                   uint64 = 1024 * 1024 * 1024
	maxSnapshotBytes      uint64 = 10 * GiB
	minMacFloorBytes      uint64 = 20 * GiB
	maxLiveObservationAge        = time.Minute
)

var (
	ErrInvalidPolicy       = errors.New("invalid capacity policy")
	ErrCapacityExceeded    = errors.New("capacity policy exceeds measured host envelope")
	ErrIdentityChanged     = errors.New("capacity identity changed after measurement")
	ErrStaleMeasurement    = errors.New("capacity measurement or policy is stale")
	ErrUnsupportedCompute  = errors.New("unsupported compute reservation claim")
	ErrInvalidCachePaths   = errors.New("invalid shared cache path set")
	hostIDPattern          = regexp.MustCompile(`^(mac-control-plane|linux-host|sandbox-host|factory-sandbox)$`)
	policyIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	sha256DigestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	volumeUUIDPattern      = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	linuxMajorMinorPattern = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
)

const (
	LinuxCapacityMountpoint = "/var/lib/remote-session-runner-capacity"
	LinuxBackupMountpoint   = "/var/lib/remote-session-runner-release-cache-backup"
	MacGoBuildCachePath     = "/private/tmp/remote-session-runner-gocache"
	MacGoModuleCachePath    = "/private/tmp/remote-session-runner-gomodcache"
)

type Scope string
type Platform string
type ComputeReservation string

type ResourceVector struct {
	CPUMilli              uint64 `json:"cpu_milli"`
	MemoryBytes           uint64 `json:"memory_bytes"`
	Tasks                 uint64 `json:"tasks"`
	IOReadBytesPerSecond  uint64 `json:"io_read_bytes_per_second"`
	IOWriteBytesPerSecond uint64 `json:"io_write_bytes_per_second"`
	IOReadIOPSMax         uint64 `json:"io_read_iops_max"`
	IOWriteIOPSMax        uint64 `json:"io_write_iops_max"`
}

func (v ResourceVector) zero() bool {
	return v == (ResourceVector{})
}

type ResourceEnvelope struct {
	PRDFloor          ResourceVector `json:"prd_floor"`
	DevHardMax        ResourceVector `json:"dev_hard_max"`
	QASHardMax        ResourceVector `json:"qas_hard_max"`
	ManagementHardMax ResourceVector `json:"management_hard_max"`
}

func (v ResourceEnvelope) zero() bool {
	return v.PRDFloor.zero() && v.DevHardMax.zero() && v.QASHardMax.zero() && v.ManagementHardMax.zero()
}

type Identity struct {
	HostID              string   `json:"host_id"`
	Platform            Platform `json:"platform"`
	DeviceID            string   `json:"device_id"`
	Filesystem          string   `json:"filesystem"`
	Mountpoint          string   `json:"mountpoint"`
	VolumeUUID          string   `json:"volume_uuid,omitempty"`
	CapacityBytes       uint64   `json:"capacity_bytes"`
	CapacityInodes      uint64   `json:"capacity_inodes"`
	BackupDeviceID      string   `json:"backup_device_id,omitempty"`
	BackupFilesystem    string   `json:"backup_filesystem,omitempty"`
	BackupMountpoint    string   `json:"backup_mountpoint,omitempty"`
	BackupCapacityBytes uint64   `json:"backup_capacity_bytes,omitempty"`
}

type Usage struct {
	UsedBytes  uint64 `json:"used_bytes"` // Linux used data bytes, including current caches and same-device backup.
	UsedInodes uint64 `json:"used_inodes"`
	// NonPurgeableBytes is Mac Data-volume use excluding Go/release caches,
	// Runner-managed snapshots, and separately accounted backup bytes.
	NonPurgeableBytes uint64 `json:"non_purgeable_bytes"`
	SnapshotBytes     uint64 `json:"snapshot_bytes"`
	GoCacheBytes      uint64 `json:"go_cache_bytes"`
	ReleaseCacheBytes uint64 `json:"release_cache_bytes"`
	BackupBytes       uint64 `json:"backup_bytes"`
}

// StorageVolume is a measured filesystem identity used for APFS volume quota
// checks. QuotaBytes is the observed APFS volume quota, not the container
// capacity; zero means no volume quota is configured. Dev/QAS and shared-cache
// identities must already be observed; validation never fabricates a UUID.
type StorageVolume struct {
	UUID          string `json:"uuid"`
	DeviceID      string `json:"device_id"` // For APFS, the containing device/container identity shared by its volumes.
	Filesystem    string `json:"filesystem"`
	Mountpoint    string `json:"mountpoint"`
	CapacityBytes uint64 `json:"capacity_bytes"`
	QuotaBytes    uint64 `json:"quota_bytes"`
}

type Measurement struct {
	ID               string          `json:"id"`
	Identity         Identity        `json:"identity"`
	ObservedAt       time.Time       `json:"observed_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	ResourceCapacity ResourceVector  `json:"resource_capacity"`
	GoCachePaths     []string        `json:"go_cache_paths"`
	LinuxDevices     []LinuxDevice   `json:"linux_devices,omitempty"`
	StorageVolumes   []StorageVolume `json:"storage_volumes,omitempty"`
	Usage            Usage           `json:"usage"`
}

type LiveObservation struct {
	Identity         Identity        `json:"identity"`
	ObservedAt       time.Time       `json:"observed_at"`
	ResourceCapacity ResourceVector  `json:"resource_capacity"`
	GoCachePaths     []string        `json:"go_cache_paths"`
	LinuxDevices     []LinuxDevice   `json:"linux_devices,omitempty"`
	StorageVolumes   []StorageVolume `json:"storage_volumes,omitempty"`
	Usage            Usage           `json:"usage"`
}

// LinuxDevice is a measurement-bound block device identity used for cgroup-v2
// I/O controls and ext4 project quotas. MajorMinor is the kernel device number
// observed for this exact filesystem UUID; it is never inferred from a path.
type LinuxDevice struct {
	ID                            string `json:"id"`
	UUID                          string `json:"uuid"`
	MajorMinor                    string `json:"major_minor"`
	Filesystem                    string `json:"filesystem"`
	Mountpoint                    string `json:"mountpoint"`
	CapacityBytes                 uint64 `json:"capacity_bytes"`
	CapacityInodes                uint64 `json:"capacity_inodes"`
	UsedBytes                     uint64 `json:"used_bytes"`
	UsedInodes                    uint64 `json:"used_inodes"`
	IOReadBytesPerSecondCapacity  uint64 `json:"io_read_bytes_per_second_capacity"`
	IOWriteBytesPerSecondCapacity uint64 `json:"io_write_bytes_per_second_capacity"`
	IOReadIOPSCapacity            uint64 `json:"io_read_iops_capacity"`
	IOWriteIOPSCapacity           uint64 `json:"io_write_iops_capacity"`
	ProjectQuotaFeature           bool   `json:"project_quota_feature"`
	ProjectQuotaActive            bool   `json:"project_quota_active"`
}

type HostManagementBudget struct {
	Components              []string         `json:"components"`
	PeakBytes               uint64           `json:"peak_bytes"`
	PeakInodes              uint64           `json:"peak_inodes"`
	GoCacheBudgetBytes      uint64           `json:"go_cache_budget_bytes"`
	ReleaseCacheBudgetBytes uint64           `json:"release_cache_budget_bytes"`
	BackupBudgetBytes       uint64           `json:"backup_budget_bytes"`
	StagingBudgetBytes      uint64           `json:"staging_budget_bytes"`
	LogBudgetBytes          uint64           `json:"log_budget_bytes"`
	GoCachePaths            []string         `json:"go_cache_paths"`
	IODeviceID              string           `json:"io_device_id,omitempty"`
	Resources               ResourceEnvelope `json:"resources"`
	BackupOnDataDevice      bool             `json:"backup_on_data_device"`
	BackupLowWaterBytes     uint64           `json:"backup_low_water_bytes"`
	MacData                 *MacDataBudget   `json:"mac_data,omitempty"`
}

type LaneRuntimeBudget struct {
	PRDGrowthBytes          uint64           `json:"prd_growth_bytes"`
	DevHardBytes            uint64           `json:"dev_hard_bytes"`
	QASHardBytes            uint64           `json:"qas_hard_bytes"`
	ManagementGrowthBytes   uint64           `json:"management_growth_bytes"`
	PRDGrowthInodes         uint64           `json:"prd_growth_inodes"`
	DevHardInodes           uint64           `json:"dev_hard_inodes"`
	QASHardInodes           uint64           `json:"qas_hard_inodes"`
	ManagementGrowthInodes  uint64           `json:"management_growth_inodes"`
	GoCachePaths            []string         `json:"go_cache_paths"`
	GoCacheBudgetBytes      uint64           `json:"go_cache_budget_bytes"`
	ReleaseCacheBudgetBytes uint64           `json:"release_cache_budget_bytes"`
	BackupBudgetBytes       uint64           `json:"backup_budget_bytes"`
	BackupOnDataDevice      bool             `json:"backup_on_data_device"`
	BackupLowWaterBytes     uint64           `json:"backup_low_water_bytes"`
	IODeviceID              string           `json:"io_device_id,omitempty"`
	Resources               ResourceEnvelope `json:"resources"`
	MacData                 *MacDataBudget   `json:"mac_data,omitempty"`
}

type MacDataBudget struct {
	StorageMode             string `json:"storage_reservation_mode,omitempty"`
	PRDVolumeUUID           string `json:"prd_volume_uuid,omitempty"`
	PRDMountpoint           string `json:"prd_mountpoint,omitempty"`
	PRDQuotaBytes           uint64 `json:"prd_quota_bytes,omitempty"`
	PRDHostBudgetBytes      uint64 `json:"prd_host_budget_bytes,omitempty"`
	DevVolumeUUID           string `json:"dev_volume_uuid,omitempty"`
	DevMountpoint           string `json:"dev_mountpoint,omitempty"`
	DevQuotaBytes           uint64 `json:"dev_quota_bytes,omitempty"`
	QASVolumeUUID           string `json:"qas_volume_uuid,omitempty"`
	QASMountpoint           string `json:"qas_mountpoint,omitempty"`
	QASQuotaBytes           uint64 `json:"qas_quota_bytes,omitempty"`
	SharedCacheBudgetBytes  uint64 `json:"shared_cache_budget_bytes"`
	ReleaseCacheBudgetBytes uint64 `json:"release_cache_budget_bytes"`
	SnapshotBudgetBytes     uint64 `json:"snapshot_budget_bytes"`
	BackupOnDataVolume      bool   `json:"backup_on_data_volume"`
	BackupBudgetBytes       uint64 `json:"backup_budget_bytes"`
	BackupLowWaterBytes     uint64 `json:"backup_low_water_bytes"`
}

type Policy struct {
	Version          uint32                `json:"capacity_policy_version"`
	HostID           string                `json:"host_id"`
	Scope            Scope                 `json:"policy_scope"`
	Lane             string                `json:"release_lane,omitempty"`
	MeasurementID    string                `json:"capacity_measurement_attestation_id"`
	EvaluationDigest string                `json:"capacity_evaluation_digest"`
	NotBefore        time.Time             `json:"not_before"`
	NotAfter         time.Time             `json:"not_after"`
	Compute          ComputeReservation    `json:"compute_reservation"`
	HostManagement   *HostManagementBudget `json:"host_management,omitempty"`
	LaneRuntime      *LaneRuntimeBudget    `json:"lane_runtime,omitempty"`
}

// EvaluationDigest returns a deterministic digest over the measurement's
// complete fixed-field payload. Slices must be sorted and unique; maps are not
// used in the signed input.
func (m Measurement) EvaluationDigest() (string, error) {
	if err := validateMeasurement(m); err != nil {
		return "", err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ValidatePolicy verifies policy scope, freshness, the measurement digest,
// unchanged host/device identity, and the current byte/resource envelope.
func ValidatePolicy(policy Policy, measurement Measurement, live LiveObservation, now time.Time) error {
	if err := validateMeasurement(measurement); err != nil {
		return fmt.Errorf("%w: measurement: %v", ErrInvalidPolicy, err)
	}
	if err := validateIdentity(live.Identity); err != nil {
		return fmt.Errorf("%w: live identity: %v", ErrIdentityChanged, err)
	}
	if policy.Version != 1 || !hostIDPattern.MatchString(policy.HostID) || policy.HostID != measurement.Identity.HostID ||
		policy.MeasurementID != measurement.ID || !sha256DigestPattern.MatchString(policy.EvaluationDigest) {
		return ErrInvalidPolicy
	}
	digest, err := measurement.EvaluationDigest()
	if err != nil || digest != policy.EvaluationDigest {
		return ErrInvalidPolicy
	}
	if !slices.Equal(measurement.LinuxDevices, live.LinuxDevices) {
		return ErrIdentityChanged
	}
	now = now.UTC()
	if policy.NotBefore.IsZero() || policy.NotAfter.IsZero() || !policy.NotAfter.After(policy.NotBefore) ||
		now.Before(policy.NotBefore) || !now.Before(policy.NotAfter) ||
		policy.NotBefore.Before(measurement.ObservedAt) || policy.NotAfter.After(measurement.ExpiresAt) ||
		!now.Before(measurement.ExpiresAt) || live.ObservedAt.IsZero() || live.ObservedAt.After(now.Add(time.Minute)) ||
		now.Sub(live.ObservedAt) > maxLiveObservationAge {
		return ErrStaleMeasurement
	}
	if policy.HostID != live.Identity.HostID || policy.HostID != measurement.Identity.HostID ||
		live.Identity != measurement.Identity || live.ResourceCapacity != measurement.ResourceCapacity ||
		!slices.Equal(live.GoCachePaths, measurement.GoCachePaths) || !slices.Equal(live.StorageVolumes, measurement.StorageVolumes) {
		return ErrIdentityChanged
	}
	if err := validateCachePaths(live.GoCachePaths); err != nil {
		return err
	}
	if err := validateScope(policy, measurement, live); err != nil {
		return err
	}
	return nil
}

func validateScope(policy Policy, measurement Measurement, live LiveObservation) error {
	switch policy.Scope {
	case ScopeHostManagement:
		if policy.Lane != "" || policy.HostManagement == nil || policy.LaneRuntime != nil {
			return ErrInvalidPolicy
		}
		if err := validateManagementComponents(measurement.Identity, policy.HostManagement.Components); err != nil {
			return err
		}
		componentBytes, componentBytesOK := addMany(policy.HostManagement.StagingBudgetBytes, policy.HostManagement.LogBudgetBytes)
		if policy.HostManagement.PeakBytes == 0 ||
			policy.HostManagement.StagingBudgetBytes == 0 || policy.HostManagement.LogBudgetBytes == 0 ||
			policy.HostManagement.GoCacheBudgetBytes < measurement.Usage.GoCacheBytes ||
			policy.HostManagement.ReleaseCacheBudgetBytes < measurement.Usage.ReleaseCacheBytes ||
			policy.HostManagement.BackupBudgetBytes == 0 ||
			!componentBytesOK || componentBytes > policy.HostManagement.PeakBytes ||
			!slices.Equal(policy.HostManagement.GoCachePaths, measurement.GoCachePaths) {
			return ErrCapacityExceeded
		}
		if measurement.Identity.Platform == PlatformMacOS {
			if policy.Compute != ComputeNone || !policy.HostManagement.Resources.zero() || policy.HostManagement.PeakInodes != 0 ||
				policy.HostManagement.IODeviceID != "" {
				return ErrUnsupportedCompute
			}
			if policy.HostManagement.MacData == nil ||
				policy.HostManagement.BackupBudgetBytes != policy.HostManagement.MacData.BackupBudgetBytes ||
				policy.HostManagement.BackupOnDataDevice != policy.HostManagement.MacData.BackupOnDataVolume ||
				policy.HostManagement.BackupLowWaterBytes != policy.HostManagement.MacData.BackupLowWaterBytes {
				return ErrInvalidPolicy
			}
			if policy.HostManagement.GoCacheBudgetBytes != policy.HostManagement.MacData.SharedCacheBudgetBytes ||
				policy.HostManagement.ReleaseCacheBudgetBytes != policy.HostManagement.MacData.ReleaseCacheBudgetBytes {
				return ErrInvalidPolicy
			}
			return validateMacStorage(measurement, live, policy.HostManagement.MacData, false, "")
		}
		if policy.Compute != ComputeLinuxCgroupV2 || policy.HostManagement.MacData != nil ||
			policy.HostManagement.Resources.DevHardMax != (ResourceVector{}) || policy.HostManagement.Resources.QASHardMax != (ResourceVector{}) || policy.HostManagement.PeakInodes == 0 {
			return ErrInvalidPolicy
		}
		if !linuxIODeviceBound(measurement, policy.HostManagement.IODeviceID) {
			return ErrInvalidPolicy
		}
		if !resourceVectorComplete(policy.HostManagement.Resources.PRDFloor) ||
			!resourceVectorComplete(policy.HostManagement.Resources.ManagementHardMax) {
			return ErrInvalidPolicy
		}
		if err := validateResourceEnvelope(measurement.ResourceCapacity, policy.HostManagement.Resources); err != nil {
			return err
		}
		return validateLinuxStorage(measurement, live, linuxStorageFromManagement(*policy.HostManagement))
	case ScopeLaneRuntime:
		if (policy.Lane != "dev" && policy.Lane != "qas" && policy.Lane != "prd") || policy.HostManagement != nil || policy.LaneRuntime == nil {
			return ErrInvalidPolicy
		}
		budget := policy.LaneRuntime
		if budget.GoCacheBudgetBytes < measurement.Usage.GoCacheBytes ||
			budget.ReleaseCacheBudgetBytes < measurement.Usage.ReleaseCacheBytes ||
			budget.BackupBudgetBytes == 0 ||
			!slices.Equal(budget.GoCachePaths, measurement.GoCachePaths) {
			return ErrCapacityExceeded
		}
		if measurement.Identity.Platform == PlatformMacOS {
			if policy.Compute != ComputeNone || !budget.Resources.zero() || budget.IODeviceID != "" {
				return ErrUnsupportedCompute
			}
			if budget.MacData == nil || budget.BackupBudgetBytes != budget.MacData.BackupBudgetBytes ||
				budget.BackupOnDataDevice != budget.MacData.BackupOnDataVolume ||
				budget.BackupLowWaterBytes != budget.MacData.BackupLowWaterBytes {
				return ErrInvalidPolicy
			}
			if budget.MacData == nil || budget.GoCacheBudgetBytes != budget.MacData.SharedCacheBudgetBytes ||
				budget.ReleaseCacheBudgetBytes != budget.MacData.ReleaseCacheBudgetBytes {
				return ErrInvalidPolicy
			}
			return validateMacStorage(measurement, live, budget.MacData, true, policy.Lane)
		}
		if policy.Compute != ComputeLinuxCgroupV2 || budget.MacData != nil || !linuxIODeviceBound(measurement, budget.IODeviceID) {
			return ErrInvalidPolicy
		}
		if !resourceEnvelopeComplete(budget.Resources) || budget.PRDGrowthBytes == 0 || budget.DevHardBytes == 0 ||
			budget.QASHardBytes == 0 || budget.ManagementGrowthBytes == 0 || budget.PRDGrowthInodes == 0 ||
			budget.DevHardInodes == 0 || budget.QASHardInodes == 0 || budget.ManagementGrowthInodes == 0 {
			return ErrInvalidPolicy
		}
		if err := validateResourceEnvelope(measurement.ResourceCapacity, budget.Resources); err != nil {
			return err
		}
		return validateLinuxStorage(measurement, live, linuxStorageFromLane(*budget))
	default:
		return ErrInvalidPolicy
	}
}

type linuxStorageBudget struct {
	prdBytes, devBytes, qasBytes, managementBytes     uint64
	prdInodes, devInodes, qasInodes, managementInodes uint64
	goCacheBudget, releaseCacheBudget, backupBudget   uint64
	backupOnDataDevice                                bool
	backupLowWater                                    uint64
}

func linuxStorageFromManagement(value HostManagementBudget) linuxStorageBudget {
	return linuxStorageBudget{
		managementBytes: value.PeakBytes, managementInodes: value.PeakInodes,
		goCacheBudget: value.GoCacheBudgetBytes, releaseCacheBudget: value.ReleaseCacheBudgetBytes,
		backupBudget: value.BackupBudgetBytes, backupOnDataDevice: value.BackupOnDataDevice,
		backupLowWater: value.BackupLowWaterBytes,
	}
}

func linuxStorageFromLane(value LaneRuntimeBudget) linuxStorageBudget {
	return linuxStorageBudget{
		prdBytes: value.PRDGrowthBytes, devBytes: value.DevHardBytes, qasBytes: value.QASHardBytes, managementBytes: value.ManagementGrowthBytes,
		prdInodes: value.PRDGrowthInodes, devInodes: value.DevHardInodes, qasInodes: value.QASHardInodes, managementInodes: value.ManagementGrowthInodes,
		goCacheBudget: value.GoCacheBudgetBytes, releaseCacheBudget: value.ReleaseCacheBudgetBytes,
		backupBudget: value.BackupBudgetBytes, backupOnDataDevice: value.BackupOnDataDevice,
		backupLowWater: value.BackupLowWaterBytes,
	}
}

func validateLinuxStorage(measurement Measurement, live LiveObservation, budget linuxStorageBudget) error {
	if measurement.Identity.Platform != PlatformLinux || budget.goCacheBudget == 0 || budget.releaseCacheBudget == 0 ||
		budget.backupBudget == 0 || budget.managementBytes == 0 || budget.managementInodes == 0 {
		return ErrInvalidPolicy
	}
	cacheGrowth := positiveDifference(budget.goCacheBudget, live.Usage.GoCacheBytes)
	releaseGrowth := positiveDifference(budget.releaseCacheBudget, live.Usage.ReleaseCacheBytes)
	projectedBytes, ok := addMany(live.Usage.UsedBytes, budget.prdBytes, budget.devBytes, budget.qasBytes, budget.managementBytes, cacheGrowth, releaseGrowth)
	if !ok {
		return ErrCapacityExceeded
	}
	if budget.backupOnDataDevice {
		if measurement.Identity.BackupDeviceID != "" || measurement.Identity.BackupCapacityBytes != 0 || budget.backupLowWater != 0 {
			return ErrInvalidPolicy
		}
		projectedBytes, ok = addMany(projectedBytes, positiveDifference(budget.backupBudget, live.Usage.BackupBytes))
		if !ok {
			return ErrCapacityExceeded
		}
	} else {
		if !validBackupIdentity(measurement.Identity) || budget.backupLowWater == 0 {
			return ErrInvalidPolicy
		}
		backupProjected := max(live.Usage.BackupBytes, budget.backupBudget)
		if exceedsFloor(backupProjected, measurement.Identity.BackupCapacityBytes, budget.backupLowWater) {
			return ErrCapacityExceeded
		}
	}
	projectedInodes, ok := addMany(live.Usage.UsedInodes, budget.prdInodes, budget.devInodes, budget.qasInodes, budget.managementInodes)
	if !ok || exceedsPercentFloor(projectedBytes, measurement.Identity.CapacityBytes, 30) ||
		exceedsPercentFloor(projectedInodes, measurement.Identity.CapacityInodes, 30) {
		return ErrCapacityExceeded
	}
	return nil
}

func validateMacStorage(measurement Measurement, live LiveObservation, budget *MacDataBudget, laneRuntime bool, lane string) error {
	if measurement.Identity.Platform != PlatformMacOS || budget == nil || budget.SharedCacheBudgetBytes == 0 ||
		budget.ReleaseCacheBudgetBytes == 0 || budget.SnapshotBudgetBytes > maxSnapshotBytes || budget.SnapshotBudgetBytes == 0 ||
		budget.SnapshotBudgetBytes < live.Usage.SnapshotBytes || budget.BackupBudgetBytes == 0 ||
		budget.SharedCacheBudgetBytes < live.Usage.GoCacheBytes || budget.ReleaseCacheBudgetBytes < live.Usage.ReleaseCacheBytes {
		return ErrCapacityExceeded
	}
	cacheQuotaBytes, cacheOK := measuredMacSharedCacheQuota(measurement)
	if !cacheOK || cacheQuotaBytes != budget.SharedCacheBudgetBytes {
		return ErrIdentityChanged
	}
	if !laneRuntime {
		if lane != "" || budget.StorageMode != "" || budget.PRDVolumeUUID != "" || budget.PRDMountpoint != "" || budget.PRDQuotaBytes != 0 ||
			budget.DevQuotaBytes != 0 || budget.QASQuotaBytes != 0 || budget.DevVolumeUUID != "" || budget.QASVolumeUUID != "" ||
			budget.DevMountpoint != "" || budget.QASMountpoint != "" || budget.PRDHostBudgetBytes == 0 {
			return ErrInvalidPolicy
		}
	} else if lane == "dev" || lane == "qas" {
		if budget.StorageMode != StorageAPFSQuota || budget.PRDHostBudgetBytes == 0 || budget.PRDQuotaBytes != 0 ||
			budget.PRDVolumeUUID != "" || budget.PRDMountpoint != "" {
			return ErrInvalidPolicy
		}
		if budget.DevQuotaBytes == 0 || budget.QASQuotaBytes == 0 || !volumeUUIDPattern.MatchString(budget.DevVolumeUUID) ||
			!volumeUUIDPattern.MatchString(budget.QASVolumeUUID) || !cleanAbsolutePath(budget.DevMountpoint) || !cleanAbsolutePath(budget.QASMountpoint) ||
			budget.DevVolumeUUID == budget.QASVolumeUUID || budget.DevMountpoint == budget.QASMountpoint {
			return ErrInvalidPolicy
		}
		if !measuredVolumeMatches(measurement.StorageVolumes, budget.DevVolumeUUID, budget.DevMountpoint, budget.DevQuotaBytes) ||
			!measuredVolumeMatches(measurement.StorageVolumes, budget.QASVolumeUUID, budget.QASMountpoint, budget.QASQuotaBytes) {
			return ErrIdentityChanged
		}
	} else if lane == "prd" {
		if budget.StorageMode == StorageHostFreeSpaceBudget {
			if budget.PRDHostBudgetBytes == 0 || budget.PRDVolumeUUID != "" || budget.PRDMountpoint != "" || budget.PRDQuotaBytes != 0 {
				return ErrInvalidPolicy
			}
		} else if budget.StorageMode == StorageAPFSQuota {
			if budget.PRDHostBudgetBytes != 0 || !volumeUUIDPattern.MatchString(budget.PRDVolumeUUID) ||
				!cleanAbsolutePath(budget.PRDMountpoint) || budget.PRDQuotaBytes == 0 {
				return ErrInvalidPolicy
			}
			if !measuredVolumeMatches(measurement.StorageVolumes, budget.PRDVolumeUUID, budget.PRDMountpoint, budget.PRDQuotaBytes) {
				return ErrIdentityChanged
			}
		} else {
			return ErrInvalidPolicy
		}
		if budget.DevQuotaBytes != 0 || budget.QASQuotaBytes != 0 || budget.DevVolumeUUID != "" || budget.QASVolumeUUID != "" ||
			budget.DevMountpoint != "" || budget.QASMountpoint != "" {
			return ErrInvalidPolicy
		}
	} else {
		return ErrInvalidPolicy
	}
	if budget.BackupOnDataVolume {
		if measurement.Identity.BackupDeviceID != "" || measurement.Identity.BackupCapacityBytes != 0 || budget.BackupLowWaterBytes != 0 {
			return ErrInvalidPolicy
		}
	} else {
		if !validBackupIdentity(measurement.Identity) || budget.BackupLowWaterBytes == 0 {
			return ErrInvalidPolicy
		}
		backupProjected := max(live.Usage.BackupBytes, budget.BackupBudgetBytes)
		if exceedsFloor(backupProjected, measurement.Identity.BackupCapacityBytes, budget.BackupLowWaterBytes) {
			return ErrCapacityExceeded
		}
	}
	dataCommitted, ok := addMany(live.Usage.NonPurgeableBytes, budget.SnapshotBudgetBytes, budget.PRDHostBudgetBytes,
		budget.PRDQuotaBytes, budget.DevQuotaBytes, budget.QASQuotaBytes, budget.SharedCacheBudgetBytes, budget.ReleaseCacheBudgetBytes)
	if !ok {
		return ErrCapacityExceeded
	}
	if budget.BackupOnDataVolume {
		dataCommitted, ok = addMany(dataCommitted, max(budget.BackupBudgetBytes, live.Usage.BackupBytes))
		if !ok {
			return ErrCapacityExceeded
		}
	}
	floor := minMacFloorBytes
	percentFloor := ceilPercent(measurement.Identity.CapacityBytes, 15)
	if percentFloor > floor {
		floor = percentFloor
	}
	if exceedsFloor(dataCommitted, measurement.Identity.CapacityBytes, floor) {
		return ErrCapacityExceeded
	}
	return nil
}

func validateResourceEnvelope(capacity ResourceVector, envelope ResourceEnvelope) error {
	for _, dimension := range [][5]uint64{
		{capacity.CPUMilli, envelope.PRDFloor.CPUMilli, envelope.DevHardMax.CPUMilli, envelope.QASHardMax.CPUMilli, envelope.ManagementHardMax.CPUMilli},
		{capacity.MemoryBytes, envelope.PRDFloor.MemoryBytes, envelope.DevHardMax.MemoryBytes, envelope.QASHardMax.MemoryBytes, envelope.ManagementHardMax.MemoryBytes},
		{capacity.Tasks, envelope.PRDFloor.Tasks, envelope.DevHardMax.Tasks, envelope.QASHardMax.Tasks, envelope.ManagementHardMax.Tasks},
		{capacity.IOReadBytesPerSecond, envelope.PRDFloor.IOReadBytesPerSecond, envelope.DevHardMax.IOReadBytesPerSecond, envelope.QASHardMax.IOReadBytesPerSecond, envelope.ManagementHardMax.IOReadBytesPerSecond},
		{capacity.IOWriteBytesPerSecond, envelope.PRDFloor.IOWriteBytesPerSecond, envelope.DevHardMax.IOWriteBytesPerSecond, envelope.QASHardMax.IOWriteBytesPerSecond, envelope.ManagementHardMax.IOWriteBytesPerSecond},
		{capacity.IOReadIOPSMax, envelope.PRDFloor.IOReadIOPSMax, envelope.DevHardMax.IOReadIOPSMax, envelope.QASHardMax.IOReadIOPSMax, envelope.ManagementHardMax.IOReadIOPSMax},
		{capacity.IOWriteIOPSMax, envelope.PRDFloor.IOWriteIOPSMax, envelope.DevHardMax.IOWriteIOPSMax, envelope.QASHardMax.IOWriteIOPSMax, envelope.ManagementHardMax.IOWriteIOPSMax},
	} {
		if dimension[0] == 0 {
			return ErrInvalidPolicy
		}
		total, ok := addMany(dimension[1:]...)
		if !ok || total > dimension[0] {
			return ErrCapacityExceeded
		}
	}
	return nil
}

func resourceVectorComplete(value ResourceVector) bool {
	return value.CPUMilli > 0 && value.MemoryBytes > 0 && value.Tasks > 0 &&
		value.IOReadBytesPerSecond > 0 && value.IOWriteBytesPerSecond > 0 &&
		value.IOReadIOPSMax > 0 && value.IOWriteIOPSMax > 0
}

func resourceEnvelopeComplete(value ResourceEnvelope) bool {
	return resourceVectorComplete(value.PRDFloor) && resourceVectorComplete(value.DevHardMax) &&
		resourceVectorComplete(value.QASHardMax) && resourceVectorComplete(value.ManagementHardMax)
}

func validateMeasurement(m Measurement) error {
	if !policyIDPattern.MatchString(m.ID) || validateIdentity(m.Identity) != nil || m.ObservedAt.IsZero() ||
		!m.ExpiresAt.After(m.ObservedAt) || !m.ObservedAt.Equal(m.ObservedAt.UTC()) || !m.ExpiresAt.Equal(m.ExpiresAt.UTC()) ||
		validateCachePaths(m.GoCachePaths) != nil || (m.Identity.Platform == PlatformLinux && m.ResourceCapacity.zero()) ||
		(m.Identity.Platform == PlatformMacOS && !m.ResourceCapacity.zero()) ||
		validateLinuxDevices(m.Identity.Platform, m.LinuxDevices) != nil ||
		validateStorageVolumes(m.Identity.Platform, m.StorageVolumes) != nil {
		return ErrInvalidPolicy
	}
	if m.Identity.Platform == PlatformMacOS {
		if !slices.Equal(m.GoCachePaths, []string{MacGoBuildCachePath, MacGoModuleCachePath}) {
			return ErrInvalidCachePaths
		}
		foundDataVolume := false
		for _, volume := range m.StorageVolumes {
			if volume.UUID == m.Identity.VolumeUUID && volume.DeviceID == m.Identity.DeviceID &&
				volume.Filesystem == m.Identity.Filesystem && volume.Mountpoint == m.Identity.Mountpoint &&
				volume.CapacityBytes == m.Identity.CapacityBytes {
				foundDataVolume = true
				break
			}
		}
		if !foundDataVolume {
			return ErrInvalidPolicy
		}
	}
	return nil
}

func validateLinuxDevices(platform Platform, devices []LinuxDevice) error {
	if platform == PlatformMacOS {
		if len(devices) != 0 {
			return ErrInvalidPolicy
		}
		return nil
	}
	seenID, seenUUID, seenMajorMinor, seenMount := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	lastID := ""
	for _, device := range devices {
		if !safeLinuxDeviceID(device.ID) || !volumeUUIDPattern.MatchString(device.UUID) ||
			!linuxMajorMinorPattern.MatchString(device.MajorMinor) || device.Filesystem != "ext4" ||
			!filepath.IsAbs(device.Mountpoint) || filepath.Clean(device.Mountpoint) != device.Mountpoint ||
			device.CapacityBytes == 0 || device.CapacityInodes == 0 || device.UsedBytes > device.CapacityBytes ||
			device.UsedInodes > device.CapacityInodes || device.ID <= lastID {
			return ErrInvalidPolicy
		}
		for _, pair := range [][2]string{{device.ID, "id"}, {strings.ToLower(device.UUID), "uuid"},
			{device.MajorMinor, "device number"}, {device.Mountpoint, "mountpoint"}} {
			var seen map[string]struct{}
			switch pair[1] {
			case "id":
				seen = seenID
			case "uuid":
				seen = seenUUID
			case "device number":
				seen = seenMajorMinor
			default:
				seen = seenMount
			}
			if _, exists := seen[pair[0]]; exists {
				return ErrInvalidPolicy
			}
			seen[pair[0]] = struct{}{}
		}
		lastID = device.ID
	}
	return nil
}

func safeLinuxDeviceID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == ':' || r == '/' || r == '-') {
			return false
		}
	}
	return true
}

func linuxIODeviceBound(measurement Measurement, deviceID string) bool {
	if deviceID == "" {
		return false
	}
	if len(measurement.LinuxDevices) == 0 {
		return deviceID == measurement.Identity.DeviceID
	}
	for _, device := range measurement.LinuxDevices {
		if device.ID == deviceID {
			return true
		}
	}
	return false
}

func validateStorageVolumes(platform Platform, volumes []StorageVolume) error {
	if platform == PlatformLinux {
		if len(volumes) != 0 {
			return ErrInvalidPolicy
		}
		return nil
	}
	if platform != PlatformMacOS || len(volumes) == 0 {
		return ErrInvalidPolicy
	}
	seenUUID := make(map[string]struct{}, len(volumes))
	seenMount := make(map[string]struct{}, len(volumes))
	for i, volume := range volumes {
		if !volumeUUIDPattern.MatchString(volume.UUID) || volume.DeviceID == "" || strings.ToLower(volume.Filesystem) != "apfs" ||
			!cleanAbsolutePath(volume.Mountpoint) || volume.CapacityBytes == 0 || volume.QuotaBytes > volume.CapacityBytes ||
			(i > 0 && volumes[i-1].UUID >= volume.UUID) {
			return ErrInvalidPolicy
		}
		if _, exists := seenUUID[volume.UUID]; exists {
			return ErrInvalidPolicy
		}
		if _, exists := seenMount[volume.Mountpoint]; exists {
			return ErrInvalidPolicy
		}
		seenUUID[volume.UUID] = struct{}{}
		seenMount[volume.Mountpoint] = struct{}{}
	}
	return nil
}

func measuredVolumeMatches(volumes []StorageVolume, uuid, mountpoint string, requiredBytes uint64) bool {
	for _, volume := range volumes {
		if volume.UUID == uuid && volume.Mountpoint == mountpoint && volume.CapacityBytes >= requiredBytes && volume.QuotaBytes == requiredBytes {
			return true
		}
	}
	return false
}

// measuredMacSharedCacheQuota returns the aggregate of the two fixed shared
// cache volume quotas. Each cache path must be its own measured APFS mount on
// the Data-volume APFS container so those limits participate in the host low-water
// calculation and cannot be replaced by an unbounded directory.
func measuredMacSharedCacheQuota(measurement Measurement) (uint64, bool) {
	var total uint64
	for _, path := range []string{MacGoBuildCachePath, MacGoModuleCachePath} {
		var found *StorageVolume
		for index := range measurement.StorageVolumes {
			volume := &measurement.StorageVolumes[index]
			if volume.Mountpoint == path {
				if found != nil {
					return 0, false
				}
				found = volume
			}
		}
		if found == nil || found.DeviceID != measurement.Identity.DeviceID || found.QuotaBytes == 0 ||
			found.CapacityBytes < found.QuotaBytes || found.QuotaBytes > math.MaxUint64-total {
			return 0, false
		}
		total += found.QuotaBytes
	}
	return total, true
}

func validateIdentity(value Identity) error {
	if !hostIDPattern.MatchString(value.HostID) || !cleanAbsolutePath(value.Mountpoint) || value.DeviceID == "" ||
		value.Filesystem == "" || value.CapacityBytes == 0 || (value.Platform == PlatformLinux && value.CapacityInodes == 0) {
		return ErrInvalidPolicy
	}
	switch value.Platform {
	case PlatformLinux:
		if value.HostID == "mac-control-plane" || value.VolumeUUID != "" {
			return ErrInvalidPolicy
		}
	case PlatformMacOS:
		if value.HostID != "mac-control-plane" || strings.ToLower(value.Filesystem) != "apfs" ||
			!volumeUUIDPattern.MatchString(value.VolumeUUID) {
			return ErrInvalidPolicy
		}
	default:
		return ErrInvalidPolicy
	}
	if value.BackupDeviceID == "" {
		if value.BackupFilesystem != "" || value.BackupMountpoint != "" || value.BackupCapacityBytes != 0 {
			return ErrInvalidPolicy
		}
	} else if value.BackupDeviceID == value.DeviceID || value.BackupMountpoint == value.Mountpoint || value.BackupFilesystem == "" ||
		!cleanAbsolutePath(value.BackupMountpoint) || value.BackupCapacityBytes == 0 {
		return ErrInvalidPolicy
	}
	return nil
}

func validateCachePaths(paths []string) error {
	if len(paths) != 2 {
		return ErrInvalidCachePaths
	}
	for index, path := range paths {
		lower := strings.ToLower(path)
		if !cleanAbsolutePath(path) || strings.Contains(lower, "remotesessionrunner-dev") || strings.Contains(lower, "remotesessionrunner-qas") ||
			index > 0 && paths[index-1] >= path {
			return ErrInvalidCachePaths
		}
	}
	return nil
}

func validateManagementComponents(identity Identity, components []string) error {
	want := []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"}
	if identity.Platform == PlatformMacOS {
		want = append(want, "runner-release-authority-signer", "runner-release-authority-agent", "runner-release-authority-approval", "runner-release-transport-pki-issuer")
	}
	if !slices.Equal(components, want) {
		return ErrInvalidPolicy
	}
	return nil
}

func validBackupIdentity(identity Identity) bool {
	return identity.BackupDeviceID != "" && identity.BackupFilesystem != "" && cleanAbsolutePath(identity.BackupMountpoint) && identity.BackupCapacityBytes > 0
}

func cleanAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func positiveDifference(a, b uint64) uint64 {
	if a <= b {
		return 0
	}
	return a - b
}

func addMany(values ...uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		if value > math.MaxUint64-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func exceedsPercentFloor(projected, capacity, freePercent uint64) bool {
	if projected > capacity || freePercent > 100 {
		return true
	}
	minimumFree := ceilPercent(capacity, freePercent)
	return projected > capacity-minimumFree
}

func ceilPercent(value, percent uint64) uint64 {
	whole := value / 100
	remainder := value % 100
	part := whole * percent
	remainderPart := remainder * percent
	if remainderPart%100 != 0 {
		remainderPart = remainderPart/100 + 1
	} else {
		remainderPart /= 100
	}
	return part + remainderPart
}

func exceedsFloor(projected, capacity, floor uint64) bool {
	return floor > capacity || projected > capacity-floor
}

// DigestHex formats a SHA-256 byte digest using the capacity-record prefix.
func DigestHex(value [32]byte) string { return "sha256:" + hex.EncodeToString(value[:]) }
