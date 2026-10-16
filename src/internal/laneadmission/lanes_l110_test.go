package laneadmission

import (
	"errors"
	"testing"
)

func TestLanesL110StateMachineOrdersIdentityAndEpoch(t *testing.T) {
	state, err := NewState(LaneDev, HostLinux)
	if err != nil {
		t.Fatal(err)
	}
	incumbent := testIdentity("transition-1", "candidate-1", RoleCandidateAdmission, 1)
	if err := state.PrepareCandidate(incumbent); err != nil {
		t.Fatal(err)
	}
	if state.AllowsMutation() {
		t.Fatal("closed initial candidate allowed mutation")
	}
	if err := state.AdvanceCandidate(incumbent, PhaseOpen); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("candidate opened without pointer switch: %v", err)
	}
	if err := state.SwitchPendingToActive(); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceCandidate(incumbent, PhaseTransportReady); err != nil {
		t.Fatal(err)
	}
	if state.AllowsMutation() {
		t.Fatal("transport_ready candidate allowed mutation")
	}
	if err := state.AdvanceCandidate(incumbent, PhaseOpen); err != nil || !state.AllowsMutation() {
		t.Fatalf("open candidate allowed=%v err=%v", state.AllowsMutation(), err)
	}

	closure := testIdentity("transition-2", "candidate-1", RoleActiveRuntimeClosure, 2)
	if err := state.BeginActiveRuntimeClosure(closure); err != nil {
		t.Fatal(err)
	}
	if state.AllowsMutation() {
		t.Fatal("active runtime closure remained open")
	}
	candidate := testIdentity("transition-2", "candidate-2", RoleCandidateAdmission, 3)
	if err := state.PrepareCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	switchedIdentity := candidate
	switchedIdentity.CandidateManifestID = "candidate-switched"
	if err := state.PrepareCandidate(switchedIdentity); !errors.Is(err, ErrEpochIdentitySwitch) {
		t.Fatalf("same epoch identity switch error=%v", err)
	}
	if err := state.AdvanceCandidate(candidate, PhaseTransportReady); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending candidate advanced before switch: %v", err)
	}
	if err := state.SwitchPendingToActive(); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceCandidate(candidate, PhaseTransportReady); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceCandidate(candidate, PhaseOpen); err != nil {
		t.Fatal(err)
	}
	if !state.AllowsMutation() {
		t.Fatal("successor candidate did not open")
	}
	if err := state.PrepareCandidate(incumbent); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale epoch error=%v", err)
	}
}

func TestLanesL110MigrationFencePersistsFailClosed(t *testing.T) {
	state, err := NewState(LanePRD, HostLinux)
	if err != nil {
		t.Fatal(err)
	}
	active := testIdentity("transition-1", "candidate-1", RoleCandidateAdmission, 1)
	active.Lane = LanePRD
	if err := state.PrepareCandidate(active); err != nil {
		t.Fatal(err)
	}
	if err := state.SwitchPendingToActive(); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceCandidate(active, PhaseTransportReady); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceCandidate(active, PhaseOpen); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginMigrationFence("transition-fence"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("fence began before runtime closure: %v", err)
	}
	closure := testIdentity("transition-fence", "candidate-1", RoleActiveRuntimeClosure, 2)
	closure.Lane = LanePRD
	if err := state.BeginActiveRuntimeClosure(closure); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginMigrationFence("transition-fence"); err != nil {
		t.Fatal(err)
	}
	if state.AllowsMutation() {
		t.Fatal("fencing state allowed mutation")
	}
	if err := state.AdvanceMigrationFence("transition-fence", FenceFenced); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceMigrationFence("transition-fence", FenceAborting); err != nil {
		t.Fatal(err)
	}
	if err := state.AdvanceMigrationFence("transition-fence", FenceAborted); err != nil {
		t.Fatal(err)
	}
	if state.AllowsMutation() {
		t.Fatal("aborted migration fence reopened the closed runtime")
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("persistable final state invalid: %v", err)
	}
}

func testIdentity(transition, candidate string, role Role, epoch uint64) Identity {
	return Identity{
		Lane: LaneDev, HostID: HostLinux, TransitionID: transition,
		CandidateManifestID: candidate,
		ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Role:                role, Epoch: epoch,
	}
}
