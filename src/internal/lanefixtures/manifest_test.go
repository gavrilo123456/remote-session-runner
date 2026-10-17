package lanefixtures

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFixtureManifestAcceptsClosedLinuxAndMacPlans(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, fixture := range []Manifest{validLinuxFixture(), validMacFixture()} {
		if err := Validate(fixture, fixture.UnitID, fixture.HostID, now); err != nil {
			t.Fatalf("valid %s fixture rejected: %v", fixture.UnitID, err)
		}
	}
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		fixture := validLinuxFixtureForHost(host)
		if err := Validate(fixture, fixture.UnitID, fixture.HostID, now); err != nil {
			t.Errorf("valid L2-07 fixture for %s rejected: %v", host, err)
		}
	}
}

func TestFixtureManifestRejectsProductionAliasesAndWrongHostOrUnit(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	base := validLinuxFixture()
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"prd path", func(m *Manifest) { m.Mounts[0].Path = "/home/ubuntu/.local/share/remote-session-runner" }},
		{"shared cache", func(m *Manifest) { m.Projects[3].Path = "/home/ubuntu/.cache/go-build" }},
		{"root device", func(m *Manifest) { m.Mounts[0].DevicePath = "/dev/sda" }},
		{"root major minor alias", func(m *Manifest) { m.Mounts[0].MajorMinor = m.Mounts[1].MajorMinor }},
		{"host mismatch", func(m *Manifest) { m.HostID = "sandbox-host" }},
		{"region mismatch", func(m *Manifest) { m.ProviderRegion = "eu-frankfurt-1" }},
		{"volume from wrong region", func(m *Manifest) { m.Mounts[0].VolumeID = "ocid1.volume.oc1.eu-frankfurt-1.example1234" }},
		{"attachment from wrong region", func(m *Manifest) { m.Mounts[0].AttachmentID = "ocid1.volumeattachment.oc1.eu-frankfurt-1.example1234" }},
		{"device serial is not an OCI disk serial", func(m *Manifest) { m.Mounts[0].DeviceSerial = "not-a-serial" }},
		{"device serial is too short", func(m *Manifest) { m.Mounts[0].DeviceSerial = "360170f5736a64bd7accb6a5e66fe70" }},
		{"duplicate attachment", func(m *Manifest) { m.Mounts[0].AttachmentID = m.Mounts[1].AttachmentID }},
		{"duplicate serial", func(m *Manifest) { m.Mounts[0].DeviceSerial = m.Mounts[1].DeviceSerial }},
		{"unit mismatch", func(m *Manifest) { m.UnitID = "LANES-L2-08" }},
		{"missing project", func(m *Manifest) { m.Projects = m.Projects[:6] }},
		{"expired", func(m *Manifest) { m.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := base
			fixture.Mounts = append([]Mount(nil), base.Mounts...)
			fixture.Projects = append([]Project(nil), base.Projects...)
			tc.mutate(&fixture)
			if err := Validate(fixture, "LANES-L2-07", "linux-host", now); !errors.Is(err, ErrManifest) {
				t.Fatalf("unsafe fixture validation = %v", err)
			}
		})
	}
	mac := validMacFixture()
	for _, alias := range []int{0, 2} {
		mac = validMacFixture()
		mac.Mounts[4].ContainerID = mac.Mounts[alias].ContainerID
		if err := Validate(mac, mac.UnitID, mac.HostID, now); err == nil {
			t.Fatalf("Mac backup on fixture container shared with role %s accepted", mac.Mounts[alias].Role)
		}
	}
	mac = validMacFixture()
	mac.Mounts[4].PhysicalDeviceID = mac.Mounts[0].PhysicalDeviceID
	if err := Validate(mac, mac.UnitID, mac.HostID, now); err == nil {
		t.Fatal("Mac backup fixture on Data's physical disk accepted")
	}
	mac = validMacFixture()
	mac.Mounts[0].Path = "/private/tmp/remote-session-runner-gocache"
	if err := Validate(mac, mac.UnitID, mac.HostID, now); err == nil {
		t.Fatal("Mac shared-cache path accepted as disposable fixture")
	}
	mac = validMacFixture()
	mac.Mounts[1].DeviceID = mac.Mounts[0].DeviceID
	if err := Validate(mac, mac.UnitID, mac.HostID, now); err == nil {
		t.Fatal("aliased Mac fixture device accepted")
	}
}

func TestLoadApprovedRequiresExternalOwnerOnlyExactDigest(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	external := filepath.Join(base, "approved")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := validMacFixture()
	if runtime.GOOS == "linux" {
		fixture = validLinuxFixture()
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(external, "fixture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadApproved(path, Digest(data), repo, fixture.UnitID, fixture.HostID, now); err != nil {
		t.Fatalf("approved manifest rejected: %v", err)
	}
	if _, err := LoadApproved(path, "sha256:"+string(make([]byte, 64)), repo, fixture.UnitID, fixture.HostID, now); !errors.Is(err, ErrManifest) {
		t.Fatalf("wrong approved digest = %v", err)
	}
	inside := filepath.Join(repo, "fixture.json")
	if err := os.WriteFile(inside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadApproved(inside, Digest(data), repo, fixture.UnitID, fixture.HostID, now); !errors.Is(err, ErrManifest) {
		t.Fatalf("in-checkout manifest = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadApproved(path, Digest(data), repo, fixture.UnitID, fixture.HostID, now); !errors.Is(err, ErrManifest) {
		t.Fatalf("group-readable manifest = %v", err)
	}
}

func TestFixtureDirectoriesMustBeOwnerOnlyAndNotSymlinked(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "fixture")
	if err := os.Mkdir(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyOwnedFixtureDirectory(owned); err != nil {
		t.Fatalf("owner-only fixture directory rejected: %v", err)
	}
	if err := os.Chmod(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyOwnedFixtureDirectory(owned); err == nil {
		t.Fatal("group/other-readable fixture directory accepted")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(owned, link); err != nil {
		t.Fatal(err)
	}
	if err := verifyOwnedFixtureDirectory(link); err == nil {
		t.Fatal("symlinked fixture directory accepted")
	}
}

func TestFixturePathMustNotTraverseSymlinks(t *testing.T) {
	base := t.TempDir()
	canonicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	realRoot := filepath.Join(canonicalBase, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := noSymlinkPath(realRoot); err != nil {
		t.Fatalf("canonical fixture path rejected: %v", err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	if err := noSymlinkPath(filepath.Join(alias, "nested")); err == nil {
		t.Fatal("fixture path traversing a symlinked parent was accepted")
	}
}

func TestFixtureCommandEnvironmentExcludesOperatorCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "fake-access-key")
	t.Setenv("OCI_CLI_AUTH", "instance_principal")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-agent.sock")
	environment, err := fixtureCommandEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	for _, forbidden := range []string{"AWS_ACCESS_KEY_ID=", "OCI_CLI_AUTH=", "SSH_AUTH_SOCK="} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("fixture helper environment inherited %s", forbidden)
		}
	}
}

func TestFixtureHeadroomRequiresProjectedQuotasAndPercentReserve(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		capacity, free, project uint64
		percent                 uint64
		want                    bool
	}{
		{"exact 30 percent", 100, 70, 40, 30, true},
		{"one byte under floor", 100, 69, 40, 30, false},
		{"projected use exceeds free", 100, 39, 40, 30, false},
		{"percentage over one hundred", 100, 100, 0, 101, false},
		{"large byte count without multiply overflow", ^uint64(0), ^uint64(0), 0, 30, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasFreePercentAfterProjection(tc.capacity, tc.free, tc.project, tc.percent); got != tc.want {
				t.Fatalf("headroom validation=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestMacDataFloorUsesMaximumAndRoundsPercentageUp(t *testing.T) {
	for _, tc := range []struct {
		capacity uint64
		want     uint64
	}{
		{100 * GiB, 20 * GiB},
		{200 * GiB, 30 * GiB},
		{200*GiB + 1, 30*GiB + 1},
	} {
		if got := macDataFreeFloorBytes(tc.capacity); got != tc.want {
			t.Errorf("Mac Data floor for capacity %d = %d, want %d", tc.capacity, got, tc.want)
		}
	}
}

func validLinuxFixture() Manifest {
	return validLinuxFixtureForHost("linux-host")
}

func validLinuxFixtureForHost(host string) Manifest {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	id := "lanes-l2-07-test-20261017-a1b2c3d4"
	region := regionForHost(host)
	root := filepath.Join(linuxFixtureBase, id)
	projects := []Project{
		{1001, "rsr-dev", filepath.Join(root, "data", "dev"), 16 * GiB, 524288},
		{1002, "rsr-qas", filepath.Join(root, "data", "qas"), 16 * GiB, 524288},
		{1003, "rsr-management", filepath.Join(root, "data", "management"), 8 * GiB, 131072},
		{1004, "rsr-go-build", filepath.Join(root, "data", "go-build"), 6 * GiB, 196608},
		{1005, "rsr-go-modules", filepath.Join(root, "data", "go-modules"), 2 * GiB, 65536},
		{1006, "rsr-release-cache", filepath.Join(root, "data", "release-cache"), 8 * GiB, 262144},
		{1007, "rsr-cache-backup", filepath.Join(root, "backup"), 8 * GiB, 262144},
	}
	return Manifest{Version: 1, ApprovalID: "A01-fixture-test-001", FixtureID: id, UnitID: "LANES-L2-07", HostID: host, Platform: "linux",
		TestIdentity: "rsr-lanes-test-" + id, FixtureRoot: root, TestSystemdUnit: "remote-session-runner-fixture-" + id + ".scope", ProviderRegion: region, InstanceOCID: "ocid1.instance.oc1." + region + ".example1234",
		ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339), Projects: projects,
		Mounts: []Mount{
			{Role: "data", Path: filepath.Join(root, "data"), DevicePath: "/dev/sdb", DeviceID: "sdb", DeviceSerial: "360170f5736a64bd7accb6a5e66fe70ee", VolumeID: "ocid1.volume.oc1." + region + ".data12345678", AttachmentID: "ocid1.volumeattachment.oc1." + region + ".data12345678", VolumeUUID: "11111111-1111-1111-1111-111111111111", MajorMinor: "8:16", Filesystem: "ext4", CapacityBytes: LinuxDataMin},
			{Role: "backup", Path: filepath.Join(root, "backup"), DevicePath: "/dev/sdc", DeviceID: "sdc", DeviceSerial: "360170f5736a64bd7accb6a5e66fe70ef", VolumeID: "ocid1.volume.oc1." + region + ".backup12345678", AttachmentID: "ocid1.volumeattachment.oc1." + region + ".backup12345678", VolumeUUID: "22222222-2222-2222-2222-222222222222", MajorMinor: "8:32", Filesystem: "ext4", CapacityBytes: LinuxBackupMin, QuotaBytes: 8 * GiB},
		},
	}
}

func validMacFixture() Manifest {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	id := "lanes-l2-08-test-20261017-a1b2c3d4"
	root := filepath.Join(macFixtureBase, id)
	roles := []struct {
		name                              string
		quota                             uint64
		device, container, physical, uuid string
	}{
		{"dev", 15 * GiB, "disk6s1", "disk6", "disk2", "11111111-1111-1111-1111-111111111111"},
		{"qas", 15 * GiB, "disk6s2", "disk6", "disk2", "22222222-2222-2222-2222-222222222222"},
		{"go-build", 2 * GiB, "disk6s3", "disk6", "disk2", "33333333-3333-3333-3333-333333333333"},
		{"go-modules", 2 * GiB, "disk6s4", "disk6", "disk2", "44444444-4444-4444-4444-444444444444"},
		{"backup", 3 * GiB, "disk7s1", "disk7", "disk4", "55555555-5555-5555-5555-555555555555"},
	}
	mounts := make([]Mount, 0, len(roles))
	for _, role := range roles {
		capacity := role.quota
		if role.name == "backup" {
			capacity = LinuxBackupMin
		}
		mounts = append(mounts, Mount{Role: role.name, Path: filepath.Join(root, role.name), DevicePath: "/dev/" + role.device, DeviceID: role.device,
			ContainerID: role.container, PhysicalDeviceID: role.physical, VolumeUUID: role.uuid, Filesystem: "apfs", CapacityBytes: capacity, QuotaBytes: role.quota})
	}
	return Manifest{Version: 1, ApprovalID: "A01-fixture-test-001", FixtureID: id, UnitID: "LANES-L2-08", HostID: "mac-control-plane", Platform: "macos",
		TestIdentity: "rsr-lanes-test-" + id, FixtureRoot: root, ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339), Mounts: mounts}
}
