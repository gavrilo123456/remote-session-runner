//go:build linux

package lanefixtures

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestLinuxControlsScopeCommandIsExactAndBounded(t *testing.T) {
	fixture := validLinuxFixture()
	args, err := buildLinuxControlsScopeCommand(fixture, "ubuntu", "/home/ubuntu", "/opt/go/bin/go", "/tmp/test-lanes-controls", []string{"--unit", fixture.UnitID}, "/usr/sbin/runuser", "/usr/bin/env")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--system", "--scope", "--wait", "--pipe", "--quiet",
		"--unit=" + fixture.TestSystemdUnit,
		"--slice=system.slice",
		"--property=CPUQuota=25%",
		"--property=MemoryMax=2G",
		"--property=TasksMax=64",
		"--property=IOWriteBandwidthMax=/dev/sdb 16M",
		"--property=IOWriteIOPSMax=/dev/sdb 1000",
		"--property=IOWriteBandwidthMax=/dev/sdc 16M",
		"--property=IOWriteIOPSMax=/dev/sdc 1000",
		"--", "/usr/sbin/runuser", "--user", "ubuntu", "--",
		"/usr/bin/env", "-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/home/ubuntu", "TMPDIR=" + filepath.Join(fixture.FixtureRoot, "data", "go-build"), "GOMAXPROCS=2",
		"GOCACHE=" + filepath.Join(fixture.FixtureRoot, "data", "go-build"),
		"GOMODCACHE=" + filepath.Join(fixture.FixtureRoot, "data", "go-modules"),
		"LANES_FIXTURE_SCOPE_STARTED=1", "LANES_TEST_GO=/opt/go/bin/go", "GOTOOLCHAIN=local", "/tmp/test-lanes-controls", "--unit", fixture.UnitID,
	}
	if len(args) != len(want) {
		t.Fatalf("scope args count=%d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("scope arg %d = %q, want %q", i, args[i], want[i])
		}
	}
	if !slices.Contains(args, "--property=TasksMax=64") {
		t.Fatal("scope omitted process bound")
	}
	fixture.TestSystemdUnit = "runnerd.service"
	if _, err := buildLinuxControlsScopeCommand(fixture, "ubuntu", "/home/ubuntu", "/opt/go/bin/go", "/tmp/test-lanes-controls", nil, "/usr/sbin/runuser", "/usr/bin/env"); err == nil {
		t.Fatal("unapproved systemd unit name was accepted")
	}
}
