// Package lanefixtures validates the out-of-tree A01 manifest used by the
// privileged L2 controls procedures. It deliberately contains no provisioning
// or cleanup operations.
package lanefixtures

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	Version          = 1
	LinuxDataMin     = uint64(96) << 30
	LinuxBackupMin   = uint64(20) << 30
	GiB              = uint64(1) << 30
	linuxFixtureBase = "/mnt/remote-session-runner-lanes-fixtures"
	macFixtureBase   = "/private/tmp/remote-session-runner-lanes-fixtures"
)

var (
	ErrManifest           = errors.New("invalid approved lane fixture manifest")
	idPattern             = regexp.MustCompile(`^lanes-(l2-07|l2-08)-[a-z0-9][a-z0-9-]{7,47}$`)
	approvalPattern       = regexp.MustCompile(`^A01-[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)
	volumeUUIDPattern     = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	majorMinorPattern     = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
	physicalDevicePattern = regexp.MustCompile(`^disk[0-9]+$`)
	deviceSerialPattern   = regexp.MustCompile(`^[0-9a-fA-F]{32,33}$`)
	digestPattern         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Manifest struct {
	Version         int       `json:"version"`
	ApprovalID      string    `json:"approval_id"`
	FixtureID       string    `json:"fixture_id"`
	UnitID          string    `json:"unit_id"`
	HostID          string    `json:"host_id"`
	Platform        string    `json:"platform"`
	TestIdentity    string    `json:"test_identity"`
	FixtureRoot     string    `json:"fixture_root"`
	TestSystemdUnit string    `json:"test_systemd_unit,omitempty"`
	ProviderRegion  string    `json:"provider_region,omitempty"`
	InstanceOCID    string    `json:"instance_ocid,omitempty"`
	ExpiresAt       string    `json:"expires_at"`
	Mounts          []Mount   `json:"mounts"`
	Projects        []Project `json:"projects,omitempty"`
}

type Mount struct {
	Role             string `json:"role"`
	Path             string `json:"path"`
	DevicePath       string `json:"device_path"`
	DeviceID         string `json:"device_id"`
	ContainerID      string `json:"container_id,omitempty"`
	PhysicalDeviceID string `json:"physical_device_id,omitempty"`
	DeviceSerial     string `json:"device_serial,omitempty"`
	VolumeID         string `json:"volume_id,omitempty"`
	AttachmentID     string `json:"attachment_id,omitempty"`
	VolumeUUID       string `json:"volume_uuid"`
	MajorMinor       string `json:"major_minor,omitempty"`
	Filesystem       string `json:"filesystem"`
	CapacityBytes    uint64 `json:"capacity_bytes"`
	QuotaBytes       uint64 `json:"quota_bytes,omitempty"`
}

type Project struct {
	ID         uint32 `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	HardBytes  uint64 `json:"hard_bytes"`
	HardInodes uint64 `json:"hard_inodes"`
}

// LoadApproved reads a regular owner-only file outside the repository, checks
// the exact owner-approved digest supplied at the A01 checkpoint, and applies
// the closed unit/host/path/resource contract before any F procedure runs.
func LoadApproved(path, expectedDigest, repoRoot, unitID, hostID string, now time.Time) (Manifest, error) {
	if !filepath.IsAbs(path) || !digestPattern.MatchString(expectedDigest) {
		return Manifest{}, ErrManifest
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute != filepath.Clean(absolute) {
		return Manifest{}, ErrManifest
	}
	repo, err := filepath.Abs(repoRoot)
	if err != nil {
		return Manifest{}, ErrManifest
	}
	canonicalPath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: resolve manifest path: %v", ErrManifest, err)
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: resolve repository root", ErrManifest)
	}
	rel, err := filepath.Rel(repo, canonicalPath)
	if err != nil || rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return Manifest{}, fmt.Errorf("%w: manifest must be outside the Git checkout", ErrManifest)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 64*1024 {
		return Manifest{}, fmt.Errorf("%w: manifest must be a regular owner-only file no larger than 64 KiB", ErrManifest)
	}
	if !ownedByCurrentUser(info) {
		return Manifest{}, fmt.Errorf("%w: manifest owner must be the invoking operator", ErrManifest)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read: %v", ErrManifest, err)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return Manifest{}, fmt.Errorf("%w: approved SHA-256 does not match manifest bytes", ErrManifest)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %v", ErrManifest, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, ErrManifest
	}
	if err := Validate(manifest, unitID, hostID, now); err != nil {
		return Manifest{}, err
	}
	if runtime.GOOS != goosForPlatform(manifest.Platform) {
		return Manifest{}, fmt.Errorf("%w: fixture platform does not match this machine", ErrManifest)
	}
	return manifest, nil
}

func Validate(m Manifest, unitID, hostID string, now time.Time) error {
	if m.Version != Version || !approvalPattern.MatchString(m.ApprovalID) || !idPattern.MatchString(m.FixtureID) ||
		m.UnitID != unitID || m.HostID != hostID || !validFixtureTestIdentity(m.TestIdentity) || m.TestIdentity != "rsr-lanes-test-"+m.FixtureID ||
		!validUnitHost(m.UnitID, m.HostID) || m.Platform != platformForHost(m.HostID) {
		return ErrManifest
	}
	base := linuxFixtureBase
	if m.Platform == "macos" {
		base = macFixtureBase
	}
	expectedRoot := filepath.Join(base, m.FixtureID)
	if m.FixtureRoot != expectedRoot || unsafeProductionPath(m.FixtureRoot, m.Platform) {
		return fmt.Errorf("%w: fixture root is not the fixed disposable root", ErrManifest)
	}
	expires, err := time.Parse(time.RFC3339, m.ExpiresAt)
	if err != nil || !expires.After(now) || expires.After(now.Add(72*time.Hour)) {
		return fmt.Errorf("%w: approval must be current and expire within 72 hours", ErrManifest)
	}
	if m.Platform == "linux" {
		if m.ProviderRegion == "" || !validRegionalOCID(m.InstanceOCID, "instance", m.ProviderRegion) || len(m.Mounts) != 2 || len(m.Projects) != 7 ||
			m.TestSystemdUnit != "remote-session-runner-fixture-"+m.FixtureID+".scope" {
			return ErrManifest
		}
		region := regionForHost(m.HostID)
		if region == "" || m.ProviderRegion != region || !strings.HasPrefix(m.InstanceOCID, "ocid1.instance.oc1."+region+".") {
			return fmt.Errorf("%w: provider region or instance OCID does not match the host inventory", ErrManifest)
		}
		if err := validateLinux(m); err != nil {
			return err
		}
	} else {
		if m.ProviderRegion != "" || m.InstanceOCID != "" || m.TestSystemdUnit != "" || len(m.Mounts) != 5 || len(m.Projects) != 0 {
			return ErrManifest
		}
		if err := validateMac(m); err != nil {
			return err
		}
	}
	return nil
}

func validateLinux(m Manifest) error {
	want := map[string]struct{ min, quota uint64 }{
		"data": {LinuxDataMin, 0}, "backup": {LinuxBackupMin, 8 * GiB},
	}
	seenUUID, seenDevice, seenVolume, seenAttachment, seenSerial, seenRole := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, mount := range m.Mounts {
		expect, ok := want[mount.Role]
		if !ok || seenRole[mount.Role] || mount.Path != filepath.Join(m.FixtureRoot, mount.Role) ||
			!filepath.IsAbs(mount.DevicePath) || filepath.Clean(mount.DevicePath) != mount.DevicePath ||
			mount.DevicePath == "/dev/sda" || mount.DevicePath == "/dev/sda1" || mount.DeviceID == "" ||
			mount.Filesystem != "ext4" || mount.CapacityBytes < expect.min || mount.QuotaBytes != expect.quota ||
			!volumeUUIDPattern.MatchString(mount.VolumeUUID) || !majorMinorPattern.MatchString(mount.MajorMinor) ||
			!validRegionalOCID(mount.VolumeID, "volume", m.ProviderRegion) || !validRegionalOCID(mount.AttachmentID, "volumeattachment", m.ProviderRegion) ||
			!deviceSerialPattern.MatchString(mount.DeviceSerial) {
			return fmt.Errorf("%w: invalid Linux %s fixture mount", ErrManifest, mount.Role)
		}
		if unsafeProductionPath(mount.Path, m.Platform) || seenUUID[strings.ToLower(mount.VolumeUUID)] || seenDevice[mount.MajorMinor] || seenVolume[mount.VolumeID] || seenAttachment[mount.AttachmentID] || seenSerial[strings.ToLower(mount.DeviceSerial)] {
			return fmt.Errorf("%w: aliased or production Linux device/path", ErrManifest)
		}
		seenRole[mount.Role], seenUUID[strings.ToLower(mount.VolumeUUID)], seenDevice[mount.MajorMinor], seenVolume[mount.VolumeID], seenAttachment[mount.AttachmentID], seenSerial[strings.ToLower(mount.DeviceSerial)] = true, true, true, true, true, true
	}
	if !seenRole["data"] || !seenRole["backup"] {
		return ErrManifest
	}
	return validateProjects(m)
}

func validRegionalOCID(value, resourceType, region string) bool {
	prefix := "ocid1." + resourceType + ".oc1." + region + "."
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(value, prefix)
	if len(suffix) < 8 {
		return false
	}
	for _, char := range suffix {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func validateProjects(m Manifest) error {
	want := []Project{
		{1001, "rsr-dev", filepath.Join(m.FixtureRoot, "data", "dev"), 16 * GiB, 524288},
		{1002, "rsr-qas", filepath.Join(m.FixtureRoot, "data", "qas"), 16 * GiB, 524288},
		{1003, "rsr-management", filepath.Join(m.FixtureRoot, "data", "management"), 8 * GiB, 131072},
		{1004, "rsr-go-build", filepath.Join(m.FixtureRoot, "data", "go-build"), 6 * GiB, 196608},
		{1005, "rsr-go-modules", filepath.Join(m.FixtureRoot, "data", "go-modules"), 2 * GiB, 65536},
		{1006, "rsr-release-cache", filepath.Join(m.FixtureRoot, "data", "release-cache"), 8 * GiB, 262144},
		{1007, "rsr-cache-backup", filepath.Join(m.FixtureRoot, "backup"), 8 * GiB, 262144},
	}
	seen := map[uint32]bool{}
	for _, got := range m.Projects {
		found := false
		for _, expected := range want {
			if got.ID == expected.ID && got.Name == expected.Name && got.Path == expected.Path && got.HardBytes == expected.HardBytes && got.HardInodes == expected.HardInodes {
				found = true
				break
			}
		}
		if !found || seen[got.ID] || unsafeProductionPath(got.Path, m.Platform) {
			return fmt.Errorf("%w: Linux project budgets must match the closed test-only table", ErrManifest)
		}
		seen[got.ID] = true
	}
	return nil
}

func validateMac(m Manifest) error {
	want := map[string]struct{ quota, minCapacity uint64 }{
		"dev": {15 * GiB, 15 * GiB}, "qas": {15 * GiB, 15 * GiB},
		"go-build": {2 * GiB, 2 * GiB}, "go-modules": {2 * GiB, 2 * GiB},
		"backup": {3 * GiB, LinuxBackupMin},
	}
	seenUUID, seenDevice, seenRole := map[string]bool{}, map[string]bool{}, map[string]bool{}
	byRole := make(map[string]Mount, len(m.Mounts))
	for _, mount := range m.Mounts {
		expect, ok := want[mount.Role]
		if !ok || seenRole[mount.Role] || mount.Path != filepath.Join(m.FixtureRoot, mount.Role) ||
			!filepath.IsAbs(mount.DevicePath) || filepath.Clean(mount.DevicePath) != mount.DevicePath || mount.DeviceID == "" ||
			mount.Filesystem != "apfs" || mount.CapacityBytes < expect.minCapacity || mount.QuotaBytes != expect.quota ||
			!volumeUUIDPattern.MatchString(mount.VolumeUUID) || !physicalDevicePattern.MatchString(mount.PhysicalDeviceID) || unsafeProductionPath(mount.Path, m.Platform) || seenUUID[strings.ToLower(mount.VolumeUUID)] || seenDevice[mount.DeviceID] {
			return fmt.Errorf("%w: invalid Mac %s fixture mount", ErrManifest, mount.Role)
		}
		if mount.ContainerID == "" {
			return fmt.Errorf("%w: Mac APFS container identity is required", ErrManifest)
		}
		byRole[mount.Role] = mount
		seenRole[mount.Role], seenUUID[strings.ToLower(mount.VolumeUUID)], seenDevice[mount.DeviceID] = true, true, true
	}
	for role := range want {
		if !seenRole[role] {
			return ErrManifest
		}
	}
	for role, mount := range byRole {
		if role == "backup" {
			continue
		}
		if mount.ContainerID != byRole["dev"].ContainerID || mount.PhysicalDeviceID != byRole["dev"].PhysicalDeviceID {
			return fmt.Errorf("%w: Mac lane/cache fixtures must stay in the approved Data container and physical disk", ErrManifest)
		}
		if byRole["backup"].ContainerID == mount.ContainerID || byRole["backup"].PhysicalDeviceID == mount.PhysicalDeviceID {
			return fmt.Errorf("%w: Mac backup fixture must use a separate APFS container and physical disk", ErrManifest)
		}
	}
	return nil
}

func validUnitHost(unit, host string) bool {
	if unit == "LANES-L2-07" {
		return host == "linux-host" || host == "sandbox-host" || host == "factory-sandbox"
	}
	return unit == "LANES-L2-08" && host == "mac-control-plane"
}

func platformForHost(host string) string {
	if host == "mac-control-plane" {
		return "macos"
	}
	return "linux"
}

func regionForHost(host string) string {
	switch host {
	case "linux-host":
		return "eu-marseille-1"
	case "sandbox-host":
		return "eu-frankfurt-1"
	case "factory-sandbox":
		return "uk-london-1"
	default:
		return ""
	}
}
func goosForPlatform(platform string) string {
	if platform == "macos" {
		return "darwin"
	}
	return "linux"
}

func unsafeProductionPath(path, platform string) bool {
	clean := filepath.Clean(path)
	blocked := []string{
		"/home/ubuntu/.local/share/remote-session-runner", "/var/lib/remote-session-runner",
		"/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod",
	}
	if platform == "macos" {
		home, _ := os.UserHomeDir()
		blocked = append(blocked,
			filepath.Join(home, "Library/Application Support/RemoteSessionRunner"),
			filepath.Join(home, "Library/Application Support/RemoteSessionRunner-dev"),
			filepath.Join(home, "Library/Application Support/RemoteSessionRunner-qas"),
			"/private/tmp/remote-session-runner-gocache", "/private/tmp/remote-session-runner-gomodcache")
	}
	for _, production := range blocked {
		if clean == production || strings.HasPrefix(clean, production+string(filepath.Separator)) || strings.HasPrefix(production, clean+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func verifyOwnedFixtureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
		return fmt.Errorf("fixture directory must be a non-symlink owner-only directory: %s", path)
	}
	return nil
}

func validFixtureTestIdentity(identity string) bool {
	return strings.HasPrefix(identity, "rsr-lanes-test-") && idPattern.MatchString(strings.TrimPrefix(identity, "rsr-lanes-test-"))
}

func noSymlinkPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil || filepath.Clean(absolute) != absolute {
		return ErrManifest
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return ErrManifest
	}
	return nil
}

func hasFreePercentAfterProjection(capacity, free, projected uint64, percent uint64) bool {
	if percent > 100 || free < projected {
		return false
	}
	quotient, remainder := capacity/100, capacity%100
	floor := quotient*percent + (remainder*percent+99)/100
	return free-projected >= floor
}

func macDataFreeFloorBytes(capacity uint64) uint64 {
	quotient, remainder := capacity/100, capacity%100
	percentFloor := quotient*15 + (remainder*15+99)/100
	minimum := uint64(20) * GiB
	if percentFloor > minimum {
		return percentFloor
	}
	return minimum
}

// Digest returns the exact lowercase digest string used by the approval
// checkpoint and command interface.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func FindRepoRoot(start string) (string, error) {
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		if info, err := os.Stat(filepath.Join(current, "go.mod")); err == nil && !info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not find repository go.mod")
		}
	}
}
