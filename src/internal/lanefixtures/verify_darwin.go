//go:build darwin

package lanefixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type apfsList struct {
	Containers []apfsContainer `json:"Containers"`
}
type apfsContainer struct {
	Reference      string              `json:"ContainerReference"`
	Ceiling        uint64              `json:"CapacityCeiling"`
	Free           uint64              `json:"CapacityFree"`
	Volumes        []apfsVolume        `json:"Volumes"`
	PhysicalStores []apfsPhysicalStore `json:"PhysicalStores"`
}
type apfsPhysicalStore struct {
	DeviceIdentifier string `json:"DeviceIdentifier"`
}
type apfsVolume struct {
	UUID       string `json:"APFSVolumeUUID"`
	Device     string `json:"DeviceIdentifier"`
	Mountpoint string `json:"MountPoint"`
	Quota      uint64 `json:"CapacityQuota"`
}

// VerifyLive compares every fixture volume with diskutil's current APFS
// volume, UUID, mountpoint, container and quota inventory. It is read-only.
func VerifyLive(m Manifest) error {
	if m.Platform != "macos" {
		return ErrManifest
	}
	plist, err := outputMac("diskutil", "apfs", "list", "-plist")
	if err != nil {
		return fmt.Errorf("read APFS inventory: %w", err)
	}
	jsonBytes, err := convertPlist(plist)
	if err != nil {
		return fmt.Errorf("decode APFS inventory: %w", err)
	}
	var inventory apfsList
	decoder := json.NewDecoder(bytes.NewReader(jsonBytes))
	if err := decoder.Decode(&inventory); err != nil {
		return fmt.Errorf("decode APFS JSON: %w", err)
	}
	dataUUID, err := volumeUUIDForPath("/System/Volumes/Data")
	if err != nil {
		return fmt.Errorf("read PRD Data volume identity: %w", err)
	}
	var dataContainer *apfsContainer
	for i := range inventory.Containers {
		for _, volume := range inventory.Containers[i].Volumes {
			if strings.EqualFold(volume.UUID, dataUUID) {
				dataContainer = &inventory.Containers[i]
			}
		}
	}
	if dataContainer == nil {
		return fmt.Errorf("cannot identify production Data APFS container")
	}
	fixtureContainers := make(map[string]apfsContainer)
	fixtureQuotaBytes := make(map[string]uint64)
	dataPhysicalDevice, err := physicalDeviceForContainer(*dataContainer)
	if err != nil {
		return fmt.Errorf("resolve Data-container physical disk: %w", err)
	}
	var fixtureContainer, fixturePhysicalDevice string
	for _, mount := range m.Mounts {
		if mount.Role == "dev" {
			fixtureContainer = mount.ContainerID
			fixturePhysicalDevice = mount.PhysicalDeviceID
			break
		}
	}
	if fixtureContainer == "" || fixturePhysicalDevice == "" {
		return fmt.Errorf("approved disposable test container identity is missing")
	}
	projectedFixtureBytes := uint64(70) * GiB // 24 GiB PRD envelope + 15+15 GiB lanes + 4 GiB caches + 10 GiB snapshots + 2 GiB release cache.
	floor := macDataFreeFloorBytes(dataContainer.Ceiling)
	if dataContainer.Free < projectedFixtureBytes || dataContainer.Free-projectedFixtureBytes < floor {
		return fmt.Errorf("Mac APFS fixture envelope would cross the max(20 GiB, 15%%) Data-container floor")
	}
	for _, mount := range m.Mounts {
		var found *apfsVolume
		var container apfsContainer
		for _, candidateContainer := range inventory.Containers {
			for i := range candidateContainer.Volumes {
				if strings.EqualFold(candidateContainer.Volumes[i].UUID, mount.VolumeUUID) {
					found = &candidateContainer.Volumes[i]
					container = candidateContainer
				}
			}
		}
		if found == nil || !strings.EqualFold(found.UUID, mount.VolumeUUID) || found.Device != mount.DeviceID ||
			container.Reference != mount.ContainerID || found.Mountpoint != mount.Path || found.Quota != mount.QuotaBytes ||
			container.Ceiling != mount.CapacityBytes || "/dev/"+found.Device != mount.DevicePath {
			return fmt.Errorf("fixture mount %s does not match live APFS identity/quota", mount.Role)
		}
		fixtureContainers[container.Reference] = container
		fixtureQuotaBytes[container.Reference] += mount.QuotaBytes
		physicalDevice, err := physicalDeviceForContainer(container)
		if err != nil || physicalDevice != mount.PhysicalDeviceID {
			return fmt.Errorf("fixture mount %s physical disk does not match its approved identity", mount.Role)
		}
		if err := validateMacPhysicalPlacement(mount.Role, container.Reference, physicalDevice,
			fixtureContainer, fixturePhysicalDevice, dataContainer.Reference, dataPhysicalDevice); err != nil {
			return fmt.Errorf("fixture mount %s: %w", mount.Role, err)
		}
		if strings.EqualFold(found.UUID, dataUUID) {
			return fmt.Errorf("fixture mount %s aliases production Data volume", mount.Role)
		}
		if err := verifyOwnedFixtureDirectory(mount.Path); err != nil {
			return fmt.Errorf("fixture mount %s: %w", mount.Role, err)
		}
		if err := noSymlinkPath(mount.Path); err != nil {
			return fmt.Errorf("fixture mount %s: %w", mount.Role, err)
		}
	}
	for reference, projectedQuota := range fixtureQuotaBytes {
		container := fixtureContainers[reference]
		if !hasFreePercentAfterProjection(container.Ceiling, container.Free, projectedQuota, 30) {
			return fmt.Errorf("disposable APFS fixture container %s lacks a 30%% post-quota free-space reserve", reference)
		}
	}
	return nil
}

func validateMacPhysicalPlacement(role, container, physicalDevice, fixtureContainer, fixturePhysicalDevice, dataContainer, dataPhysicalDevice string) error {
	if role == "backup" {
		if container == fixtureContainer || physicalDevice == fixturePhysicalDevice ||
			container == dataContainer || physicalDevice == dataPhysicalDevice {
			return fmt.Errorf("backup fixture must use a separate physical disk from test and production data")
		}
		return nil
	}
	if container != fixtureContainer || physicalDevice != fixturePhysicalDevice {
		return fmt.Errorf("lane/cache fixtures do not share the approved disposable test disk")
	}
	if container == dataContainer || physicalDevice == dataPhysicalDevice {
		return fmt.Errorf("lane/cache fixture is on a production Data container or physical disk")
	}
	return nil
}

// VerifyBeforeScope matches the Linux interface. macOS does not create a
// transient cgroup scope, so its read-only fixture verification is identical.
func VerifyBeforeScope(m Manifest) error {
	return VerifyLive(m)
}

func physicalDeviceForContainer(container apfsContainer) (string, error) {
	if len(container.PhysicalStores) != 1 || container.PhysicalStores[0].DeviceIdentifier == "" {
		return "", fmt.Errorf("fixture procedure supports only a single physical APFS store")
	}
	device := "/dev/" + container.PhysicalStores[0].DeviceIdentifier
	data, err := outputMac("diskutil", "info", "-plist", device)
	if err != nil {
		return "", err
	}
	kind, err := outputWithInput("plutil", data, "-extract", "VirtualOrPhysical", "raw", "-o", "-", "-")
	if err != nil {
		return "", fmt.Errorf("read APFS store physicality: %w", err)
	}
	whole, err := outputWithInput("plutil", data, "-extract", "ParentWholeDisk", "raw", "-o", "-", "-")
	if err != nil {
		return "", err
	}
	whole = strings.TrimSpace(whole)
	wholeInfo, err := outputMac("diskutil", "info", "-plist", "/dev/"+whole)
	if err != nil {
		return "", fmt.Errorf("read APFS physical whole-disk identity: %w", err)
	}
	internal, err := outputWithInput("plutil", wholeInfo, "-extract", "Internal", "raw", "-o", "-", "-")
	if err != nil {
		return "", fmt.Errorf("read APFS physical whole-disk internal flag: %w", err)
	}
	wholeDisk, err := outputWithInput("plutil", wholeInfo, "-extract", "WholeDisk", "raw", "-o", "-", "-")
	if err != nil {
		return "", fmt.Errorf("read APFS physical whole-disk flag: %w", err)
	}
	return physicalWholeDisk(strings.TrimSpace(kind), whole, strings.TrimSpace(internal), strings.TrimSpace(wholeDisk))
}

func physicalWholeDisk(kind, whole, internal, wholeDisk string) (string, error) {
	if kind != "Physical" || !physicalDevicePattern.MatchString(whole) || internal != "false" || wholeDisk != "true" {
		return "", fmt.Errorf("APFS store is not verified as an external physical whole disk")
	}
	return whole, nil
}

func volumeUUIDForPath(path string) (string, error) {
	data, err := outputMac("diskutil", "info", "-plist", path)
	if err != nil {
		return "", err
	}
	value, err := outputWithInput("plutil", data, "-extract", "VolumeUUID", "raw", "-o", "-", "-")
	return strings.TrimSpace(value), err
}

func convertPlist(data []byte) ([]byte, error) {
	return outputBytesWithInput("plutil", data, "-convert", "json", "-o", "-", "-")
}

func outputMac(name string, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	environment, err := fixtureCommandEnvironment()
	if err != nil {
		return nil, err
	}
	command.Env = environment
	result, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(result)))
	}
	return result, nil
}

func outputWithInput(name string, input []byte, args ...string) (string, error) {
	result, err := outputBytesWithInput(name, input, args...)
	return string(result), err
}

func outputBytesWithInput(name string, input []byte, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.Stdin = bytes.NewReader(input)
	environment, err := fixtureCommandEnvironment()
	if err != nil {
		return nil, err
	}
	command.Env = environment
	result, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(result)))
	}
	return result, nil
}
