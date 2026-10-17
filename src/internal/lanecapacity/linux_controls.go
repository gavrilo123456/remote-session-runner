package lanecapacity

import (
	"errors"
	"fmt"
	"time"
)

var ErrLinuxControlsUnavailable = errors.New("Linux controls require a fresh measured project-quota device")

// LinuxControlPlan is the closed, data-only resource-control selection for one
// Linux management scope or one Dev/QAS lane. It contains no caller-selected
// unit, path, service, or device number.
type LinuxControlPlan struct {
	HostID    string
	Scope     Scope
	Lane      string
	SliceUnit string
	Resources ResourceVector
	Device    LinuxDevice
}

// BuildLinuxControlPlan validates the capacity policy and selects a measured
// Linux block device for cgroup-v2 controls. A signed/helper-authenticated
// measurement is a later L3 responsibility; L2 callers must not treat this
// pure validator as authorization to change a host.
func BuildLinuxControlPlan(policy Policy, measurement Measurement, live LiveObservation, now time.Time) (LinuxControlPlan, error) {
	if err := ValidatePolicy(policy, measurement, live, now); err != nil {
		return LinuxControlPlan{}, fmt.Errorf("validate capacity policy: %w", err)
	}
	if measurement.Identity.Platform != PlatformLinux || len(measurement.LinuxDevices) == 0 {
		return LinuxControlPlan{}, ErrLinuxControlsUnavailable
	}

	var deviceID, lane, slice string
	var resources ResourceVector
	switch policy.Scope {
	case ScopeHostManagement:
		if policy.HostManagement == nil || policy.LaneRuntime != nil {
			return LinuxControlPlan{}, ErrInvalidPolicy
		}
		deviceID = policy.HostManagement.IODeviceID
		resources = policy.HostManagement.Resources.ManagementHardMax
		slice = "remote-session-runner-management.slice"
	case ScopeLaneRuntime:
		if policy.LaneRuntime == nil || policy.HostManagement != nil {
			return LinuxControlPlan{}, ErrInvalidPolicy
		}
		lane = policy.Lane
		if lane != "dev" && lane != "qas" {
			return LinuxControlPlan{}, ErrInvalidPolicy
		}
		deviceID = policy.LaneRuntime.IODeviceID
		switch lane {
		case "dev":
			resources = policy.LaneRuntime.Resources.DevHardMax
		case "qas":
			resources = policy.LaneRuntime.Resources.QASHardMax
		}
		slice = "remote-session-runner-" + lane + ".slice"
	default:
		return LinuxControlPlan{}, ErrInvalidPolicy
	}
	if !resourceVectorComplete(resources) {
		return LinuxControlPlan{}, ErrInvalidPolicy
	}
	if resources.CPUMilli%10 != 0 {
		return LinuxControlPlan{}, fmt.Errorf("CPU limit cannot be represented exactly by systemd CPUQuota: %w", ErrInvalidPolicy)
	}

	var selected LinuxDevice
	for _, device := range measurement.LinuxDevices {
		if device.ID == deviceID {
			selected = device
			break
		}
	}
	if selected.ID == "" || selected.Mountpoint != LinuxCapacityMountpoint ||
		!selected.ProjectQuotaFeature || !selected.ProjectQuotaActive || selected.Filesystem != "ext4" {
		return LinuxControlPlan{}, ErrLinuxControlsUnavailable
	}
	var envelope ResourceEnvelope
	if policy.Scope == ScopeHostManagement {
		envelope = policy.HostManagement.Resources
	} else {
		envelope = policy.LaneRuntime.Resources
	}
	if err := validateLinuxDeviceIOEnvelope(selected, envelope); err != nil {
		return LinuxControlPlan{}, err
	}
	return LinuxControlPlan{HostID: policy.HostID, Scope: policy.Scope, Lane: lane,
		SliceUnit: slice, Resources: resources, Device: selected}, nil
}

func validateLinuxDeviceIOEnvelope(device LinuxDevice, envelope ResourceEnvelope) error {
	capacities := []uint64{device.IOReadBytesPerSecondCapacity, device.IOWriteBytesPerSecondCapacity,
		device.IOReadIOPSCapacity, device.IOWriteIOPSCapacity}
	dimensions := [][4]uint64{
		{envelope.PRDFloor.IOReadBytesPerSecond, envelope.DevHardMax.IOReadBytesPerSecond,
			envelope.QASHardMax.IOReadBytesPerSecond, envelope.ManagementHardMax.IOReadBytesPerSecond},
		{envelope.PRDFloor.IOWriteBytesPerSecond, envelope.DevHardMax.IOWriteBytesPerSecond,
			envelope.QASHardMax.IOWriteBytesPerSecond, envelope.ManagementHardMax.IOWriteBytesPerSecond},
		{envelope.PRDFloor.IOReadIOPSMax, envelope.DevHardMax.IOReadIOPSMax,
			envelope.QASHardMax.IOReadIOPSMax, envelope.ManagementHardMax.IOReadIOPSMax},
		{envelope.PRDFloor.IOWriteIOPSMax, envelope.DevHardMax.IOWriteIOPSMax,
			envelope.QASHardMax.IOWriteIOPSMax, envelope.ManagementHardMax.IOWriteIOPSMax},
	}
	for index, values := range dimensions {
		if capacities[index] == 0 {
			return ErrLinuxControlsUnavailable
		}
		total, ok := addMany(values[:]...)
		if !ok || total > capacities[index] {
			return ErrCapacityExceeded
		}
	}
	return nil
}
