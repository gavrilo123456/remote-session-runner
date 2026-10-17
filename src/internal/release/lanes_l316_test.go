package release

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
)

func TestLanesL316TransportTransitionRequiresBoundedDualTrustAndOldLeafRetirement(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	previous, overlap, retired := l316TransportManifests(t, now)
	if err := ValidateHostManagementTransportTransition(previous, overlap, HostManagementTransportOverlap, now); err != nil {
		t.Fatalf("valid bounded transport overlap rejected: %v", err)
	}
	if err := ValidateHostManagementTransportTransition(overlap, retired, HostManagementTransportRetire, now); err != nil {
		t.Fatalf("valid old-leaf retirement rejected: %v", err)
	}

	var unsafeOverlap hostManagementManifestPayload
	if err := json.Unmarshal(overlap, &unsafeOverlap); err != nil {
		t.Fatal(err)
	}
	unsafeOverlap.TransportContract.ActivePeerAllowlist[0].OverlapNotAfter = now.Add(-time.Second).Format(time.RFC3339Nano)
	if err := ValidateHostManagementTransportTransition(previous, l302JSON(t, unsafeOverlap), HostManagementTransportOverlap, now); !errors.Is(err, ErrInvalidTransportContract) {
		t.Fatalf("expired overlap was accepted: %v", err)
	}

	if err := json.Unmarshal(overlap, &unsafeOverlap); err != nil {
		t.Fatal(err)
	}
	unsafeOverlap.TransportContract.TrustInventory = unsafeOverlap.TransportContract.TrustInventory[:len(unsafeOverlap.TransportContract.TrustInventory)-1]
	if err := ValidateHostManagementTransportTransition(previous, l302JSON(t, unsafeOverlap), HostManagementTransportOverlap, now); !errors.Is(err, ErrInvalidTransportContract) {
		t.Fatalf("missing new-root trust assignment was accepted: %v", err)
	}

	var unsafeRetirement hostManagementManifestPayload
	if err := json.Unmarshal(retired, &unsafeRetirement); err != nil {
		t.Fatal(err)
	}
	unsafeRetirement.TransportContract.LeafInventory = append(unsafeRetirement.TransportContract.LeafInventory, transportLeaf{
		HostID: "linux-host", CAID: "release-ca", LeafRole: "management-peer", URISAN: "spiffe://remote-session-runner/host/linux-host",
		IPSAN: "192.0.2.2", CertificateFingerprint: l302Digest("leaf-cert:linux-host"), SPKIFingerprint: l302Digest("leaf-spki:linux-host"),
	})
	sort.Slice(unsafeRetirement.TransportContract.LeafInventory, func(i, j int) bool {
		return leafSortKey(unsafeRetirement.TransportContract.LeafInventory[i]) < leafSortKey(unsafeRetirement.TransportContract.LeafInventory[j])
	})
	if err := ValidateHostManagementTransportTransition(overlap, l302JSON(t, unsafeRetirement), HostManagementTransportRetire, now); !errors.Is(err, ErrInvalidTransportContract) {
		t.Fatalf("retirement retaining old leaf material was accepted: %v", err)
	}
}

func TestLanesL316ManagementAuthorizationBindsManifestCapacityAndInstalledPredecessor(t *testing.T) {
	fixture := newL305Fixture(t)
	now := fixture.clock
	manifestRaw, buildEnvelopes := l302ManagementFixture(t)
	manifestEnvelope, err := SignEnvelope("host_management_manifest", manifestRaw, SigningIdentity{
		KeyID: fixture.authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: fixture.authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	var manifestRecord Envelope
	if err := json.Unmarshal(manifestEnvelope, &manifestRecord); err != nil {
		t.Fatal(err)
	}
	measurement := l314LinuxMeasurement(t, now)
	measurementDigest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	measurementID := strings.Repeat("f", 64)
	measurementRaw := l302JSON(t, capacityMeasurementPayload{HostID: "linux-host", Measurement: measurement,
		EvidenceRefs: []string{l303Digest}, EvaluationDigest: measurementDigest})
	policyRaw := l302JSON(t, l314LinuxPolicy(measurement, measurementID, lanecapacity.ScopeHostManagement))
	policyID := strings.Repeat("e", 64)
	var manifest hostManagementManifestPayload
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	var hostArtifacts []artifactTuple
	for _, artifact := range manifest.Artifacts {
		if artifact.HostID == "linux-host" {
			hostArtifacts = append(hostArtifacts, artifact)
		}
	}
	anchor := HostManagementAnchorState{HostID: "linux-host", InstalledManifestID: strings.Repeat("a", 64),
		InstallationGeneration: 3, RollbackArtifactDigest: "sha256:" + strings.Repeat("b", 64)}
	authorization := hostManagementAuthorizationPayload{
		ManifestID: manifestRecord.RecordID, HostID: "linux-host", CapacityPolicyID: policyID, ArtifactSubset: hostArtifacts,
		InstallationGeneration: 4, PredecessorKind: "host_management", PredecessorManifestID: anchor.InstalledManifestID,
		PredecessorGeneration: 3, RollbackArtifactDigest: anchor.RollbackArtifactDigest,
		NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), NotAfter: now.Add(30 * time.Minute).Format(time.RFC3339Nano),
		RequiredLedgerGeneration: 4,
	}
	validate := func(value hostManagementAuthorizationPayload, capacityRaw []byte, localAnchor HostManagementAnchorState) error {
		return ValidateHostManagementAuthorizationAgainstRecords(l302JSON(t, value), manifestRecord.RecordID, manifestRaw,
			buildEnvelopes, policyID, capacityRaw, measurementID, measurementRaw, localAnchor, 5, now)
	}
	if err := validate(authorization, policyRaw, anchor); err != nil {
		t.Fatalf("valid signed host-management authorization bindings rejected: %v", err)
	}
	badPredecessor := authorization
	badPredecessor.PredecessorManifestID = strings.Repeat("c", 64)
	if err := validate(badPredecessor, policyRaw, anchor); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("authorization with a stale predecessor was accepted: %v", err)
	}
	badSubset := authorization
	badSubset.ArtifactSubset = append([]artifactTuple(nil), authorization.ArtifactSubset[:len(authorization.ArtifactSubset)-1]...)
	if err := validate(badSubset, policyRaw, anchor); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("authorization with an incomplete host artifact subset was accepted: %v", err)
	}
	wrongScope := l314LinuxPolicy(measurement, measurementID, lanecapacity.ScopeLaneRuntime)
	if err := validate(authorization, l302JSON(t, wrongScope), anchor); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("lane capacity policy was accepted for host management: %v", err)
	}
}

func l316TransportManifests(t *testing.T, now time.Time) ([]byte, []byte, []byte) {
	t.Helper()
	previous, _ := l302ManagementFixture(t)
	var prior hostManagementManifestPayload
	if err := json.Unmarshal(previous, &prior); err != nil {
		t.Fatal(err)
	}
	overlap := prior
	contract := prior.TransportContract
	contract.CAInventory = append([]transportCA(nil), contract.CAInventory...)
	contract.CAInventory = append(contract.CAInventory, transportCA{CAID: "release-ca-next",
		CertificateFingerprint: l302Digest("ca-next-cert"), SPKIFingerprint: l302Digest("ca-next-spki")})
	contract.TrustInventory = append([]transportTrust(nil), contract.TrustInventory...)
	contract.LeafInventory = append([]transportLeaf(nil), contract.LeafInventory...)
	contract.ActivePeerAllowlist = nil
	deadline := now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano)
	for _, oldLeaf := range prior.TransportContract.LeafInventory {
		newLeaf := oldLeaf
		newLeaf.CAID = "release-ca-next"
		newLeaf.CertificateFingerprint = l302Digest("leaf-cert-next:" + newLeaf.HostID)
		newLeaf.SPKIFingerprint = l302Digest("leaf-spki-next:" + newLeaf.HostID)
		contract.LeafInventory = append(contract.LeafInventory, newLeaf)
		contract.TrustInventory = append(contract.TrustInventory, transportTrust{HostID: newLeaf.HostID, CAID: "release-ca-next"})
		contract.ActivePeerAllowlist = append(contract.ActivePeerAllowlist,
			activePeer{HostID: newLeaf.HostID, URISAN: newLeaf.URISAN, IPSAN: newLeaf.IPSAN,
				CertificateFingerprint: newLeaf.CertificateFingerprint, SPKIFingerprint: newLeaf.SPKIFingerprint},
			activePeer{HostID: oldLeaf.HostID, URISAN: oldLeaf.URISAN, IPSAN: oldLeaf.IPSAN,
				CertificateFingerprint: oldLeaf.CertificateFingerprint, SPKIFingerprint: oldLeaf.SPKIFingerprint, OverlapNotAfter: deadline})
	}
	sort.Slice(contract.CAInventory, func(i, j int) bool { return contract.CAInventory[i].CAID < contract.CAInventory[j].CAID })
	sort.Slice(contract.TrustInventory, func(i, j int) bool {
		if contract.TrustInventory[i].HostID == contract.TrustInventory[j].HostID {
			return contract.TrustInventory[i].CAID < contract.TrustInventory[j].CAID
		}
		return contract.TrustInventory[i].HostID < contract.TrustInventory[j].HostID
	})
	sort.Slice(contract.LeafInventory, func(i, j int) bool {
		return leafSortKey(contract.LeafInventory[i]) < leafSortKey(contract.LeafInventory[j])
	})
	sort.Slice(contract.ActivePeerAllowlist, func(i, j int) bool {
		return peerSortKey(contract.ActivePeerAllowlist[i]) < peerSortKey(contract.ActivePeerAllowlist[j])
	})
	overlap.TransportContract = contract
	overlapRaw := l302JSON(t, overlap)

	retired := overlap
	retiredContract := overlap.TransportContract
	retiredContract.CAInventory = nil
	for _, ca := range overlap.TransportContract.CAInventory {
		if ca.CAID != "release-ca" {
			retiredContract.CAInventory = append(retiredContract.CAInventory, ca)
		}
	}
	retiredContract.TrustInventory = nil
	for _, trust := range overlap.TransportContract.TrustInventory {
		if trust.CAID != "release-ca" {
			retiredContract.TrustInventory = append(retiredContract.TrustInventory, trust)
		}
	}
	retiredContract.LeafInventory = nil
	for _, leaf := range overlap.TransportContract.LeafInventory {
		if leaf.CAID != "release-ca" {
			retiredContract.LeafInventory = append(retiredContract.LeafInventory, leaf)
		}
	}
	retiredContract.ActivePeerAllowlist = nil
	for _, peer := range overlap.TransportContract.ActivePeerAllowlist {
		if peer.OverlapNotAfter == "" {
			retiredContract.ActivePeerAllowlist = append(retiredContract.ActivePeerAllowlist, peer)
		}
	}
	retired.TransportContract = retiredContract
	return previous, overlapRaw, l302JSON(t, retired)
}
