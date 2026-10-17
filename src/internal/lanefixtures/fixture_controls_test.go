package lanefixtures

import (
	"os"
	"testing"
	"time"
)

func TestLanesL207ControlsFixture(t *testing.T) {
	requireControlsDispatch(t, "LANES-L2-07")
}

func TestLanesL208ControlsFixture(t *testing.T) {
	requireControlsDispatch(t, "LANES-L2-08")
}

func requireControlsDispatch(t *testing.T, unitID string) {
	t.Helper()
	if os.Getenv("LANES_CONTROLS_DISPATCH") != "1" {
		t.Skip("F assertions run only through make test-lanes-controls")
	}
	hostID := os.Getenv("LANES_FIXTURE_HOST")
	if os.Getenv("LANES_FIXTURE_UNIT") != unitID || !validUnitHost(unitID, hostID) {
		t.Fatalf("fixture dispatch identity does not match assertion %s/%s", unitID, hostID)
	}
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := LoadApproved(os.Getenv("LANES_FIXTURE_MANIFEST"), os.Getenv("LANES_FIXTURE_SHA256"), root, unitID, hostID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Platform != platformForHost(hostID) {
		t.Fatalf("fixture platform %q does not match host %q", fixture.Platform, hostID)
	}
	if got := os.Getenv("LANES_FIXTURE_TEST_IDENTITY"); got != fixture.TestIdentity {
		t.Fatalf("fixture test identity %q does not match approved identity %q", got, fixture.TestIdentity)
	}
	if err := VerifyLive(fixture); err != nil {
		t.Fatalf("native fixture preflight: %v", err)
	}
	if unitID == "LANES-L2-07" {
		if err := verifyFixtureWorkloadMembership(t, fixture); err != nil {
			t.Fatalf("bounded fixture listener/session/wrapper/bridge membership: %v", err)
		}
	}
	if err := runFixturePressure(t, fixture); err != nil {
		t.Fatalf("bounded fixture pressure procedure: %v", err)
	}
}
