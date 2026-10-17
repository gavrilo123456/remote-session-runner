package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var l316HostOrder = []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"}

func TestLanesL316ManagementUpdateRequiresFreshLedgerLeaseMarginAndSixHundredSecondBound(t *testing.T) {
	guard := l316Guard("linux-host")
	if err := ValidateManagementUpdateGuard(guard); err != nil {
		t.Fatalf("valid fresh management update guard rejected: %v", err)
	}
	cases := []struct {
		name   string
		change func(*ManagementUpdateGuard)
	}{
		{"stale ledger", func(g *ManagementUpdateGuard) { g.LedgerObservedAt = g.Now.Add(-16 * time.Minute) }},
		{"expired ledger", func(g *ManagementUpdateGuard) { g.LedgerExpiresAt = g.Now }},
		{"stale generation", func(g *ManagementUpdateGuard) { g.LedgerGeneration = g.RequiredLedgerGeneration - 1 }},
		{"lane lease below margin", func(g *ManagementUpdateGuard) { g.AffectedLaneLeases[1].NotAfter = g.Now.Add(899 * time.Second) }},
		{"update and rollback exceed cap", func(g *ManagementUpdateGuard) { g.MaximumRollbackDuration++ }},
		{"duplicate affected lane", func(g *ManagementUpdateGuard) {
			g.AffectedLaneLeases = append(g.AffectedLaneLeases, g.AffectedLaneLeases[0])
		}},
		{"missing affected lane", func(g *ManagementUpdateGuard) { g.AffectedLaneLeases = g.AffectedLaneLeases[:2] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := l316Guard("linux-host")
			tc.change(&bad)
			if err := ValidateManagementUpdateGuard(bad); !errors.Is(err, ErrInvalidManagementUpdate) {
				t.Fatalf("unsafe guard accepted: %v", err)
			}
		})
	}
	wrongPredecessor := newL316UpdateBackend("linux-host")
	wrongPredecessor.guard.InstalledManifestID = strings.Repeat("9", 64)
	if err := RunHostManagementUpdate(context.Background(), l316UpdatePlan("linux-host"), wrongPredecessor); !errors.Is(err, ErrInvalidManagementUpdate) {
		t.Fatalf("update accepted stale installed predecessor: %v", err)
	}
}

func TestLanesL316ManagementUpdateStagesHealthChecksAndCommitsWithProtectedStateUnchanged(t *testing.T) {
	backend := newL316UpdateBackend("linux-host")
	if err := RunHostManagementUpdate(context.Background(), l316UpdatePlan("linux-host"), backend); err != nil {
		t.Fatalf("valid staged management update rejected: %v", err)
	}
	want := []string{"guard", "capture", "prepare-rollback", "stage", "activate", "health-time", "capture", "commit", "capture"}
	if !equalL316Events(backend.events, want) || backend.state != backend.initial {
		t.Fatalf("update ordering or protected state mismatch: events=%v state=%+v", backend.events, backend.state)
	}
}

func TestLanesL316ManagementUpdateFailureRollsBackAndVerifiesPriorRootAndLaneState(t *testing.T) {
	backend := newL316UpdateBackend("mac-control-plane")
	backend.failAt = "health-time"
	err := RunHostManagementUpdate(context.Background(), l316UpdatePlan("mac-control-plane"), backend)
	if !errors.Is(err, ErrManagementUpdate) || errors.Is(err, ErrManagementRollback) {
		t.Fatalf("verified management rollback result=%v", err)
	}
	if backend.state != backend.initial || !containsL316Event(backend.events, "rollback") || !containsL316Event(backend.events, "verify-rollback") {
		t.Fatalf("management failure did not restore protected root/lane state: %+v events=%v", backend.state, backend.events)
	}
}

func TestLanesL316ManagementUpdateRefusesUnverifiedRollback(t *testing.T) {
	backend := newL316UpdateBackend("factory-sandbox")
	backend.failAt, backend.failRollback = "health-time", true
	err := RunHostManagementUpdate(context.Background(), l316UpdatePlan("factory-sandbox"), backend)
	if !errors.Is(err, ErrManagementRollback) {
		t.Fatalf("unverified rollback was not surfaced: %v", err)
	}
}

func TestLanesL316PKIRotationSerializesDualTrustBeforeOldLeafRetirement(t *testing.T) {
	clock := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	plan := l316RotationPlan(clock)
	backend := newL316RotationBackend(plan)
	if err := RunReleaseTransportPKIRotation(context.Background(), plan, backend, func() time.Time { return clock }); err != nil {
		t.Fatalf("valid release-transport PKI rotation rejected: %v", err)
	}
	for i, host := range l316HostOrder {
		want := []string{"guard:" + host, "capture:" + host, "prepare:" + host, "dual:" + host, "install-new:" + host, "probe-new-time:" + host, "verify-old-accepted:" + host, "capture:" + host}
		if got := backend.events[i*len(want) : (i+1)*len(want)]; !equalL316Events(got, want) {
			t.Fatalf("host update order differs for %s: got=%v want=%v", host, got, want)
		}
	}
	commitIndex := indexL316Event(backend.events, "commit-retirement")
	firstDeny := indexL316Event(backend.events, "deny-old:mac-control-plane")
	firstRemove := indexL316Event(backend.events, "remove-old:mac-control-plane")
	lastDeny := indexL316Event(backend.events, "deny-old:factory-sandbox")
	if commitIndex < 0 || firstDeny < commitIndex || firstRemove <= lastDeny {
		t.Fatalf("old material was removed before all endpoint denial proofs: %v", backend.events)
	}
	for _, host := range l316HostOrder {
		if indexL316Event(backend.events, "record-retirement:"+host) < lastDeny || indexL316Event(backend.events, "remove-old:"+host) < lastDeny {
			t.Fatalf("retirement for %s preceded complete old-leaf denial: %v", host, backend.events)
		}
	}
}

func TestLanesL316PKIRotationFailureRetainsOldChainAndNeverRetiresMaterial(t *testing.T) {
	clock := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	plan := l316RotationPlan(clock)
	backend := newL316RotationBackend(plan)
	backend.failAt = "install-new:sandbox-host"
	err := RunReleaseTransportPKIRotation(context.Background(), plan, backend, func() time.Time { return clock })
	if !errors.Is(err, ErrReleaseTransportPKIRotation) || errors.Is(err, ErrManagementRollback) {
		t.Fatalf("overlap failure/verified restoration result=%v", err)
	}
	if !containsL316Event(backend.events, "rollback:sandbox-host") || !containsL316Event(backend.events, "verify-prior:sandbox-host") ||
		containsL316Event(backend.events, "commit-retirement") || hasL316EventPrefix(backend.events, "remove-old:") || hasL316EventPrefix(backend.events, "record-retirement:") {
		t.Fatalf("failed overlap did not retain the old working trust: %v", backend.events)
	}

	denial := newL316RotationBackend(plan)
	denial.failAt = "deny-old:sandbox-host"
	err = RunReleaseTransportPKIRotation(context.Background(), plan, denial, func() time.Time { return clock })
	if !errors.Is(err, ErrReleaseTransportPKIRotation) || hasL316EventPrefix(denial.events, "remove-old:") || hasL316EventPrefix(denial.events, "record-retirement:") {
		t.Fatalf("old leaf material was retired without all-endpoint denial: err=%v events=%v", err, denial.events)
	}
}

func l316Guard(host string) ManagementUpdateGuard {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	return ManagementUpdateGuard{
		InstalledManifestID: strings.Repeat("a", 64), ActivePeerSetDigest: strings.Repeat("d", 64),
		RollbackArtifactDigest: "sha256:" + strings.Repeat("c", 64),
		HostID:                 host, LedgerGeneration: 12, RequiredLedgerGeneration: 12,
		LedgerObservedAt: now.Add(-time.Minute), LedgerExpiresAt: now.Add(10 * time.Minute), Now: now,
		AffectedLaneLeases:   []ManagementLaneLease{{Lane: "dev", NotAfter: now.Add(20 * time.Minute)}, {Lane: "qas", NotAfter: now.Add(25 * time.Minute)}, {Lane: "prd", NotAfter: now.Add(30 * time.Minute)}},
		MaximumStageDuration: 5 * time.Minute, MaximumRollbackDuration: 5 * time.Minute,
	}
}

func l316UpdatePlan(host string) HostManagementUpdatePlan {
	roles, _ := ManagementPackage(host)
	artifacts := make([]ManagementArtifactReference, 0, len(roles.Components))
	for i, component := range roles.Components {
		artifacts = append(artifacts, ManagementArtifactReference{Component: component.Name, ArtifactDigest: fmt.Sprintf("sha256:%064x", i+1)})
	}
	return HostManagementUpdatePlan{HostID: host, CurrentManifestID: strings.Repeat("a", 64), CurrentActivePeerSetDigest: strings.Repeat("d", 64), TargetManifestID: strings.Repeat("b", 64),
		CurrentGeneration: 1, TargetGeneration: 2, RequiredLedgerGeneration: 9, RollbackArtifactDigest: "sha256:" + strings.Repeat("c", 64), Artifacts: artifacts}
}

type l316UpdateBackend struct {
	guard        ManagementUpdateGuard
	state        ManagementProtectedState
	initial      ManagementProtectedState
	events       []string
	failAt       string
	failRollback bool
}

func newL316UpdateBackend(host string) *l316UpdateBackend {
	state := l316ProtectedState(host)
	return &l316UpdateBackend{guard: l316Guard(host), state: state, initial: state}
}

func (b *l316UpdateBackend) FreshManagementUpdateGuard(context.Context, string) (ManagementUpdateGuard, error) {
	b.events = append(b.events, "guard")
	return b.guard, nil
}
func (b *l316UpdateBackend) CaptureManagementProtectedState(context.Context, string) (ManagementProtectedState, error) {
	b.events = append(b.events, "capture")
	return b.state, nil
}
func (b *l316UpdateBackend) PrepareManagementRollback(context.Context, HostManagementUpdatePlan) (ManagementRollbackReceipt, error) {
	b.events = append(b.events, "prepare-rollback")
	return "fixture-rollback", nil
}
func (b *l316UpdateBackend) StageManagementArtifacts(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error {
	b.events = append(b.events, "stage")
	return nil
}
func (b *l316UpdateBackend) ActivateManagementUpdate(context.Context, ManagementRollbackReceipt) error {
	b.events = append(b.events, "activate")
	return nil
}
func (b *l316UpdateBackend) VerifyManagementHealthAndTime(context.Context, ManagementRollbackReceipt) error {
	b.events = append(b.events, "health-time")
	if b.failAt == "health-time" {
		return errors.New("synthetic health/time failure")
	}
	return nil
}
func (b *l316UpdateBackend) CommitManagementManifest(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error {
	b.events = append(b.events, "commit")
	return nil
}
func (b *l316UpdateBackend) RollbackManagementUpdate(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error {
	b.events = append(b.events, "rollback")
	if b.failRollback {
		return errors.New("synthetic rollback failure")
	}
	b.state = b.initial
	return nil
}
func (b *l316UpdateBackend) VerifyManagementRollback(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error {
	b.events = append(b.events, "verify-rollback")
	if b.failRollback || b.state != b.initial {
		return errors.New("synthetic rollback verification failure")
	}
	return nil
}

func l316RotationPlan(now time.Time) ReleaseTransportPKIRotationPlan {
	plan := ReleaseTransportPKIRotationPlan{PreviousManifestID: strings.Repeat("1", 64), OverlapManifestID: strings.Repeat("2", 64),
		RetirementManifestID: strings.Repeat("3", 64), RequiredLedgerGeneration: 12, OverlapNotAfter: now.Add(7 * 24 * time.Hour)}
	for i, host := range l316HostOrder {
		plan.Hosts = append(plan.Hosts, ReleaseTransportPKIRotationHost{HostID: host,
			OldCAFingerprint: "sha256:" + strings.Repeat("a", 64), NewCAFingerprint: "sha256:" + strings.Repeat("b", 64),
			OldCertificateFingerprint: "sha256:" + fmt.Sprintf("%064x", i+1), OldSPKIFingerprint: "sha256:" + fmt.Sprintf("%064x", i+11),
			NewCertificateFingerprint: "sha256:" + fmt.Sprintf("%064x", i+21), NewSPKIFingerprint: "sha256:" + fmt.Sprintf("%064x", i+31),
			PreviousActivePeerSetDigest: fmt.Sprintf("%064x", i+91),
			RequestID:                   fmt.Sprintf("%064x", i+41), IssuerReceiptID: fmt.Sprintf("%064x", i+51), PackageDigest: fmt.Sprintf("%064x", i+61),
			ActivePeerSetDigest: fmt.Sprintf("%064x", i+71), RollbackArtifactDigest: "sha256:" + fmt.Sprintf("%064x", i+81)})
	}
	return plan
}

type l316RotationBackend struct {
	plan    ReleaseTransportPKIRotationPlan
	overlap map[string]bool
	events  []string
	failAt  string
}

func newL316RotationBackend(plan ReleaseTransportPKIRotationPlan) *l316RotationBackend {
	return &l316RotationBackend{plan: plan, overlap: make(map[string]bool)}
}

func (b *l316RotationBackend) FreshPKIRotationGuard(_ context.Context, host string) (ManagementUpdateGuard, error) {
	b.events = append(b.events, "guard:"+host)
	guard := l316Guard(host)
	for _, rotationHost := range b.plan.Hosts {
		if rotationHost.HostID != host {
			continue
		}
		guard.RollbackArtifactDigest = rotationHost.RollbackArtifactDigest
		if b.overlap[host] {
			guard.InstalledManifestID = b.plan.OverlapManifestID
			guard.ActivePeerSetDigest = rotationHost.ActivePeerSetDigest
		} else {
			guard.InstalledManifestID = b.plan.PreviousManifestID
			guard.ActivePeerSetDigest = rotationHost.PreviousActivePeerSetDigest
		}
	}
	return guard, nil
}
func (b *l316RotationBackend) CapturePKIRotationProtectedState(_ context.Context, host string) (ManagementProtectedState, error) {
	b.events = append(b.events, "capture:"+host)
	return l316ProtectedState(host), nil
}
func (b *l316RotationBackend) PreparePKIRotationRollback(_ context.Context, host ReleaseTransportPKIRotationHost) (PKIRotationRollbackReceipt, error) {
	b.events = append(b.events, "prepare:"+host.HostID)
	return PKIRotationRollbackReceipt("fixture-" + host.HostID), nil
}
func (b *l316RotationBackend) InstallDualTrustAndOverlapPeers(_ context.Context, _ PKIRotationRollbackReceipt, host ReleaseTransportPKIRotationHost, _ time.Time) error {
	b.events = append(b.events, "dual:"+host.HostID)
	b.overlap[host.HostID] = true
	return nil
}
func (b *l316RotationBackend) InstallNewTransportLeafAndReload(_ context.Context, _ PKIRotationRollbackReceipt, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "install-new:"+host.HostID)
	if b.failAt == "install-new:"+host.HostID {
		return errors.New("synthetic leaf install failure")
	}
	return nil
}
func (b *l316RotationBackend) ProbeNewTransportChainAndTime(_ context.Context, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "probe-new-time:"+host.HostID)
	return nil
}
func (b *l316RotationBackend) VerifyOldTransportLeafAccepted(_ context.Context, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "verify-old-accepted:"+host.HostID)
	return nil
}
func (b *l316RotationBackend) RollbackToPriorTransport(_ context.Context, _ PKIRotationRollbackReceipt, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "rollback:"+host.HostID)
	b.overlap[host.HostID] = false
	return nil
}
func (b *l316RotationBackend) VerifyPriorTransport(_ context.Context, _ PKIRotationRollbackReceipt, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "verify-prior:"+host.HostID)
	return nil
}
func (b *l316RotationBackend) VerifyOldTransportLeafDenied(_ context.Context, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "deny-old:"+host.HostID)
	if b.failAt == "deny-old:"+host.HostID {
		return errors.New("synthetic old leaf still accepted")
	}
	return nil
}
func (b *l316RotationBackend) CommitRetirementManifest(context.Context, string) error {
	b.events = append(b.events, "commit-retirement")
	return nil
}
func (b *l316RotationBackend) RecordTransportLeafRetirement(_ context.Context, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "record-retirement:"+host.HostID)
	return nil
}
func (b *l316RotationBackend) RemoveRetiredTransportLeaf(_ context.Context, host ReleaseTransportPKIRotationHost) error {
	b.events = append(b.events, "remove-old:"+host.HostID)
	return nil
}

func l316ProtectedState(host string) ManagementProtectedState {
	state := ManagementProtectedState{HostID: host, AuthorityPublicAnchorDigest: "sha256:" + strings.Repeat("d", 64),
		SecurityAnchorStateDigest: "sha256:" + strings.Repeat("e", 64), LaneRootsDigest: "sha256:" + strings.Repeat("f", 64),
		LaneProcessesDigest: "sha256:" + strings.Repeat("1", 64), LaneConfigurationDigest: "sha256:" + strings.Repeat("2", 64),
		LaneMailboxesDigest: "sha256:" + strings.Repeat("3", 64), LaneIngressDigest: "sha256:" + strings.Repeat("4", 64)}
	if host == "mac-control-plane" {
		state.AuthorityStateDigest = "sha256:" + strings.Repeat("5", 64)
	}
	return state
}

func equalL316Events(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsL316Event(events []string, want string) bool { return indexL316Event(events, want) >= 0 }

func indexL316Event(events []string, want string) int {
	for i, event := range events {
		if event == want {
			return i
		}
	}
	return -1
}

func hasL316EventPrefix(events []string, prefix string) bool {
	for _, event := range events {
		if strings.HasPrefix(event, prefix) {
			return true
		}
	}
	return false
}
