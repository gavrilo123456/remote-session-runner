package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	l302SourceCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	l302TemplateHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestLanesL302SchemasCompileAndRejectOpenBuildFields(t *testing.T) {
	valid := l302BuildPayload("mac-control-plane", "lane_runtime", "runner", "darwin", "arm64")
	if err := ValidateHostBuildAttestation(valid); err != nil {
		t.Fatalf("valid host-build payload rejected: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"host_id":"linux-host","release_track":"lane_runtime","component":"runnerd","goos":"linux","goarch":"amd64","source_commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","gitea_commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","github_commit_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","toolchain_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","build_input_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","artifact_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","template_schema_digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","evidence_refs":["sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"],"build_timestamp":"2026-10-17T12:00:00Z","unexpected":true}`),
		[]byte(`{"host_id":"linux-host","host_id":"factory-sandbox"}`),
		[]byte(`{"host_id":"linux-host"} {}`),
	} {
		if err := ValidatePayload("host_build_attestation", invalid); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("invalid payload error = %v, want ErrInvalidPayload", err)
		}
	}
	if err := ValidatePayload("promotion_authorization", valid); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("closed L3-03 schema rejected malformed promotion payload with = %v", err)
	}
}

func TestLanesL302HostBuildAttestationBindsClosedPlatformComponentTuple(t *testing.T) {
	for _, test := range []struct {
		name, host, track, component, goos, goarch string
	}{
		{"mac runtime", "mac-control-plane", "lane_runtime", "runner-locald", "darwin", "arm64"},
		{"linux x86 runtime", "linux-host", "lane_runtime", "runnerd", "linux", "amd64"},
		{"linux arm runtime", "sandbox-host", "lane_runtime", "runner-ssh-bridge", "linux", "arm64"},
		{"factory management", "factory-sandbox", "host_management", "runner-release-transport-pki-installer", "linux", "amd64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateHostBuildAttestation(l302BuildPayload(test.host, test.track, test.component, test.goos, test.goarch)); err != nil {
				t.Fatalf("valid tuple rejected: %v", err)
			}
		})
	}
	for _, test := range []struct {
		name, host, track, component, goos, goarch string
	}{
		{"wrong lane component", "linux-host", "lane_runtime", "runner-local", "linux", "amd64"},
		{"wrong track component", "mac-control-plane", "host_management", "runner", "darwin", "arm64"},
		{"wrong sandbox architecture", "sandbox-host", "lane_runtime", "runnerd", "linux", "amd64"},
		{"wrong factory operating system", "factory-sandbox", "host_management", "runner-release-builder", "darwin", "amd64"},
	} {
		if err := ValidateHostBuildAttestation(l302BuildPayload(test.host, test.track, test.component, test.goos, test.goarch)); !errors.Is(err, ErrInvalidComponentMatrix) {
			t.Errorf("%s error = %v, want ErrInvalidComponentMatrix", test.name, err)
		}
	}
	badSource := map[string]any{}
	if err := json.Unmarshal(l302BuildPayload("linux-host", "lane_runtime", "runnerd", "linux", "amd64"), &badSource); err != nil {
		t.Fatal(err)
	}
	badSource["gitea_commit_id"] = strings.Repeat("b", 40)
	if err := ValidateHostBuildAttestation(l302JSON(t, badSource)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unequal source commits error = %v", err)
	}
}

func TestLanesL302CandidateManifestHasExactSortedMatrixAndVerifiedBuildBinding(t *testing.T) {
	manifest, envelopes := l302CandidateFixture(t)
	if got := len(envelopes); got != 16 {
		t.Fatalf("host-build envelope count = %d, want 16", got)
	}
	if err := ValidateCandidateManifest(manifest, envelopes); err != nil {
		t.Fatalf("valid candidate cohort rejected: %v", err)
	}
	for _, raw := range envelopes {
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		private := l302HostPrivateKey(raw)
		if _, err := VerifyEnvelope(raw, map[string]TrustedKey{
			envelope.SignerKeyID: {KeyID: envelope.SignerKeyID, Role: SignerRoleHostAttestation,
				HostID: l302HostID(raw), PublicKey: private.Public().(ed25519.PublicKey)},
		}); err != nil {
			t.Fatalf("fixture host-build envelope did not verify: %v", err)
		}
	}
}

func TestLanesL302CandidateManifestRejectsIncompleteExtraDuplicateAndUnsortedMatrix(t *testing.T) {
	manifest, envelopes := l302CandidateFixture(t)
	var candidate candidateManifestPayload
	if err := json.Unmarshal(manifest, &candidate); err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*candidateManifestPayload)
	}{
		{"missing tuple", func(value *candidateManifestPayload) { value.Artifacts = value.Artifacts[:15] }},
		{"extra component", func(value *candidateManifestPayload) { value.Artifacts[0].Component = "unexpected-component" }},
		{"duplicate tuple", func(value *candidateManifestPayload) { value.Artifacts[1] = value.Artifacts[0] }},
		{"unsorted tuple array", func(value *candidateManifestPayload) {
			value.Artifacts[0], value.Artifacts[1] = value.Artifacts[1], value.Artifacts[0]
		}},
		{"mixed host architecture", func(value *candidateManifestPayload) {
			for i := range value.Artifacts {
				if value.Artifacts[i].HostID == "mac-control-plane" {
					value.Artifacts[i].GOARCH = "amd64"
					return
				}
			}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			copyOf := candidate
			copyOf.Artifacts = append([]artifactTuple(nil), candidate.Artifacts...)
			mutation.edit(&copyOf)
			if err := ValidateCandidateManifest(l302JSON(t, copyOf), envelopes); err == nil {
				t.Fatal("invalid component matrix was accepted")
			}
		})
	}
	if err := ValidateCandidateManifest(manifest, map[string][]byte{}); !errors.Is(err, ErrBuildAttestationMismatch) {
		t.Fatalf("missing verified build records error = %v", err)
	}
	withExtra := make(map[string][]byte, len(envelopes)+1)
	for id, raw := range envelopes {
		withExtra[id] = raw
	}
	withExtra[strings.Repeat("f", 64)] = []byte(`{}`)
	if err := ValidateCandidateManifest(manifest, withExtra); !errors.Is(err, ErrBuildAttestationMismatch) {
		t.Fatalf("extra build records error = %v", err)
	}
}

func TestLanesL302CandidateManifestRejectsMismatchedSourceToolchainInputAndArtifactBindings(t *testing.T) {
	manifest, envelopes := l302CandidateFixture(t)
	var candidate candidateManifestPayload
	if err := json.Unmarshal(manifest, &candidate); err != nil {
		t.Fatal(err)
	}
	changedSource := candidate
	changedSource.SourceCommitID = strings.Repeat("b", 40)
	changedSource.GiteaCommitID = changedSource.SourceCommitID
	changedSource.GitHubCommitID = changedSource.SourceCommitID
	if err := ValidateCandidateManifest(l302JSON(t, changedSource), envelopes); !errors.Is(err, ErrBuildAttestationMismatch) {
		t.Fatalf("changed source cohort error = %v", err)
	}
	changedSchema := candidate
	changedSchema.TemplateSchemaDigest = l302Digest("different-template-schema")
	if err := ValidateCandidateManifest(l302JSON(t, changedSchema), envelopes); !errors.Is(err, ErrBuildAttestationMismatch) {
		t.Fatalf("changed template/schema digest error = %v", err)
	}
	for _, field := range []string{"ToolchainDigest", "BuildInputDigest", "ArtifactDigest"} {
		copyOf := candidate
		copyOf.Artifacts = append([]artifactTuple(nil), candidate.Artifacts...)
		switch field {
		case "ToolchainDigest":
			copyOf.Artifacts[0].ToolchainDigest = l302Digest("wrong-toolchain")
		case "BuildInputDigest":
			copyOf.Artifacts[0].BuildInputDigest = l302Digest("wrong-input")
		case "ArtifactDigest":
			copyOf.Artifacts[0].ArtifactDigest = l302Digest("wrong-artifact")
		}
		if err := ValidateCandidateManifest(l302JSON(t, copyOf), envelopes); !errors.Is(err, ErrBuildAttestationMismatch) {
			t.Errorf("changed %s binding error = %v", field, err)
		}
	}
	for id, raw := range envelopes {
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var build hostBuildPayload
		if err := json.Unmarshal(envelope.Payload, &build); err != nil {
			t.Fatal(err)
		}
		build.ArtifactDigest = l302Digest("tampered-attestation")
		changed, err := SignEnvelope("host_build_attestation", l302JSON(t, build), SigningIdentity{
			KeyID: envelope.SignerKeyID, Role: SignerRoleHostAttestation, HostID: build.HostID,
			PrivateKey: l302HostPrivateKey(raw),
		})
		if err != nil {
			t.Fatal(err)
		}
		var replacement Envelope
		if err := json.Unmarshal(changed, &replacement); err != nil {
			t.Fatal(err)
		}
		if replacement.RecordID == id {
			continue
		}
		copyEnvelopes := make(map[string][]byte, len(envelopes))
		for oldID, oldRaw := range envelopes {
			copyEnvelopes[oldID] = oldRaw
		}
		delete(copyEnvelopes, id)
		copyEnvelopes[replacement.RecordID] = changed
		copyCandidate := candidate
		copyCandidate.Artifacts = append([]artifactTuple(nil), candidate.Artifacts...)
		for i := range copyCandidate.Artifacts {
			if copyCandidate.Artifacts[i].HostBuildAttestationID == id {
				copyCandidate.Artifacts[i].HostBuildAttestationID = replacement.RecordID
			}
		}
		if err := ValidateCandidateManifest(l302JSON(t, copyCandidate), copyEnvelopes); !errors.Is(err, ErrBuildAttestationMismatch) {
			t.Fatalf("changed attestation artifact binding error = %v", err)
		}
		break
	}
}

func TestLanesL302HostManagementManifestHasExactMatrixAndTransportInventory(t *testing.T) {
	manifest, envelopes := l302ManagementFixture(t)
	if len(envelopes) != 20 {
		t.Fatalf("host-management build envelope count = %d, want 20", len(envelopes))
	}
	if err := ValidateHostManagementManifest(manifest, envelopes); err != nil {
		t.Fatalf("valid host-management cohort rejected: %v", err)
	}
	var value hostManagementManifestPayload
	if err := json.Unmarshal(manifest, &value); err != nil {
		t.Fatal(err)
	}
	if got := len(expectedComponentKeys("host_management")); got != 20 {
		t.Fatalf("management matrix tuples = %d, want 20", got)
	}
	if got := len(value.TransportContract.ActivePeerAllowlist); got != 4 {
		t.Fatalf("active peer identities = %d, want one for each physical host", got)
	}
}

func TestLanesL302HostManagementManifestRejectsBadMatrixAndTransportBindings(t *testing.T) {
	manifest, envelopes := l302ManagementFixture(t)
	var value hostManagementManifestPayload
	if err := json.Unmarshal(manifest, &value); err != nil {
		t.Fatal(err)
	}
	badMatrix := value
	badMatrix.Artifacts = append([]artifactTuple(nil), value.Artifacts...)
	badMatrix.Artifacts[0].HostID = "linux-host"
	if err := ValidateHostManagementManifest(l302JSON(t, badMatrix), envelopes); !errors.Is(err, ErrInvalidComponentMatrix) {
		t.Fatalf("wrong management matrix error = %v", err)
	}
	badTrust := value
	badTrust.TransportContract.CAInventory = append([]transportCA(nil), value.TransportContract.CAInventory...)
	badTrust.TransportContract.TrustInventory = append([]transportTrust(nil), value.TransportContract.TrustInventory...)
	badTrust.TransportContract.TrustInventory[0].CAID = "missing-ca"
	if err := ValidateHostManagementManifest(l302JSON(t, badTrust), envelopes); !errors.Is(err, ErrInvalidTransportContract) {
		t.Fatalf("trust reference error = %v", err)
	}
	badPeer := value
	badPeer.TransportContract.ActivePeerAllowlist = append([]activePeer(nil), value.TransportContract.ActivePeerAllowlist...)
	badPeer.TransportContract.ActivePeerAllowlist[0].SPKIFingerprint = l302Digest("unknown-spki")
	if err := ValidateHostManagementManifest(l302JSON(t, badPeer), envelopes); !errors.Is(err, ErrInvalidTransportContract) {
		t.Fatalf("unbound active peer error = %v", err)
	}
	badIP := value
	badIP.TransportContract.ActivePeerAllowlist = append([]activePeer(nil), value.TransportContract.ActivePeerAllowlist...)
	badIP.TransportContract.ActivePeerAllowlist[0].IPSAN = "not-an-ip-address"
	if err := ValidateHostManagementManifest(l302JSON(t, badIP), envelopes); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("invalid IP SAN error = %v", err)
	}
	badCohort := make(map[string][]byte, len(envelopes))
	for id, raw := range envelopes {
		badCohort[id] = raw
	}
	for id, raw := range badCohort {
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var build hostBuildPayload
		if err := json.Unmarshal(envelope.Payload, &build); err != nil {
			t.Fatal(err)
		}
		build.TemplateSchemaDigest = l302Digest("different-template-schema")
		replacement, err := SignEnvelope("host_build_attestation", l302JSON(t, build), SigningIdentity{
			KeyID: envelope.SignerKeyID, Role: SignerRoleHostAttestation,
			HostID: build.HostID, PrivateKey: l302HostPrivateKey(raw),
		})
		if err != nil {
			t.Fatal(err)
		}
		var signed Envelope
		if err := json.Unmarshal(replacement, &signed); err != nil {
			t.Fatal(err)
		}
		delete(badCohort, id)
		badCohort[signed.RecordID] = replacement
		badManifest := value
		badManifest.Artifacts = append([]artifactTuple(nil), value.Artifacts...)
		for i := range badManifest.Artifacts {
			if badManifest.Artifacts[i].HostBuildAttestationID == id {
				badManifest.Artifacts[i].HostBuildAttestationID = signed.RecordID
			}
		}
		if err := ValidateHostManagementManifest(l302JSON(t, badManifest), badCohort); !errors.Is(err, ErrBuildAttestationMismatch) {
			t.Fatalf("inconsistent management build schema digests error = %v", err)
		}
		break
	}
	unknown := map[string]any{}
	if err := json.Unmarshal(manifest, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["release_transport_contract_extra"] = true
	if err := ValidateHostManagementManifest(l302JSON(t, unknown), envelopes); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("open host-management payload error = %v", err)
	}
}

func l302BuildPayload(hostID, track, component, goos, goarch string) []byte {
	return l302JSON(nil, map[string]any{
		"host_id": hostID, "release_track": track, "component": component,
		"goos": goos, "goarch": goarch,
		"source_commit_id": l302SourceCommit, "gitea_commit_id": l302SourceCommit, "github_commit_id": l302SourceCommit,
		"toolchain_digest": l302Digest("toolchain:" + hostID), "build_input_digest": l302Digest("input:" + component),
		"artifact_digest":        l302Digest("artifact:" + hostID + ":" + component),
		"template_schema_digest": l302TemplateHash,
		"evidence_refs":          l302EvidenceRefs("build"),
		"build_timestamp":        "2026-10-17T10:20:30Z",
	})
}

func l302CandidateFixture(t *testing.T) ([]byte, map[string][]byte) {
	t.Helper()
	return l302ManifestFixture(t, "lane_runtime")
}

func l302ManagementFixture(t *testing.T) ([]byte, map[string][]byte) {
	t.Helper()
	return l302ManifestFixture(t, "host_management")
}

func l302ManifestFixture(t *testing.T, track string) ([]byte, map[string][]byte) {
	t.Helper()
	keys := expectedComponentKeys(track)
	artifacts := make([]artifactTuple, 0, len(keys))
	envelopes := make(map[string][]byte, len(keys))
	privateByHost := map[string]ed25519.PrivateKey{}
	for index, hostID := range canonicalHostIDs {
		privateByHost[hostID] = testSigningKey(byte(index * 32))
	}
	for _, key := range keys {
		goos, goarch := l302Platform(key.HostID)
		build := l302BuildPayload(key.HostID, track, key.Component, goos, goarch)
		var payload hostBuildPayload
		if err := json.Unmarshal(build, &payload); err != nil {
			t.Fatal(err)
		}
		envelopeBytes, err := SignEnvelope("host_build_attestation", build, SigningIdentity{
			KeyID: "host-test-" + key.HostID, Role: SignerRoleHostAttestation,
			HostID: key.HostID, PrivateKey: privateByHost[key.HostID],
		})
		if err != nil {
			t.Fatalf("sign build fixture: %v", err)
		}
		var envelope Envelope
		if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
			t.Fatal(err)
		}
		envelopes[envelope.RecordID] = envelopeBytes
		artifacts = append(artifacts, artifactTuple{
			HostID: key.HostID, Component: key.Component, GOOS: goos, GOARCH: goarch,
			ToolchainDigest: payload.ToolchainDigest, BuildInputDigest: payload.BuildInputDigest,
			ArtifactDigest: payload.ArtifactDigest, HostBuildAttestationID: envelope.RecordID,
		})
	}
	if track == "lane_runtime" {
		return l302JSON(t, candidateManifestPayload{
			ReleaseComponentMatrixVersion: 1,
			RequiredManagementManifestID:  strings.Repeat("1", 64),
			SourceCommitID:                l302SourceCommit, GiteaCommitID: l302SourceCommit, GitHubCommitID: l302SourceCommit,
			BuildCohortTimestamp: "2026-10-17T10:30:00Z", TemplateSchemaDigest: l302TemplateHash,
			EvidenceRefs: l302EvidenceRefs("candidate"), Artifacts: artifacts,
		}), envelopes
	}
	contract := l302TransportFixture()
	return l302JSON(t, hostManagementManifestPayload{
		MatrixVersion: 1, Compatibility: 1, ContractDigest: l302Digest("bootstrap-update-contract"),
		SourceCommitID: l302SourceCommit, GiteaCommitID: l302SourceCommit, GitHubCommitID: l302SourceCommit,
		Artifacts: artifacts, TransportContract: contract,
	}), envelopes
}

func l302TransportFixture() transportContract {
	contract := transportContract{
		CAInventory: []transportCA{{CAID: "release-ca", CertificateFingerprint: l302Digest("ca-cert"), SPKIFingerprint: l302Digest("ca-spki")}},
	}
	for i, hostID := range canonicalHostIDs {
		ip := fmt.Sprintf("192.0.2.%d", i+1)
		uri := "spiffe://remote-session-runner/host/" + hostID
		contract.TrustInventory = append(contract.TrustInventory, transportTrust{HostID: hostID, CAID: "release-ca"})
		leaf := transportLeaf{
			HostID: hostID, CAID: "release-ca", LeafRole: "management-peer", URISAN: uri, IPSAN: ip,
			CertificateFingerprint: l302Digest("leaf-cert:" + hostID), SPKIFingerprint: l302Digest("leaf-spki:" + hostID),
		}
		contract.LeafInventory = append(contract.LeafInventory, leaf)
		contract.ActivePeerAllowlist = append(contract.ActivePeerAllowlist, activePeer{
			HostID: hostID, URISAN: uri, IPSAN: ip, CertificateFingerprint: leaf.CertificateFingerprint,
			SPKIFingerprint: leaf.SPKIFingerprint,
		})
	}
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
	return contract
}

func l302Platform(hostID string) (string, string) {
	switch hostID {
	case "mac-control-plane":
		return "darwin", "arm64"
	case "sandbox-host":
		return "linux", "arm64"
	default:
		return "linux", "amd64"
	}
}

func l302Digest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func l302EvidenceRefs(prefix string) []string {
	refs := []string{l302Digest("evidence:" + prefix + ":a"), l302Digest("evidence:" + prefix + ":b")}
	sort.Strings(refs)
	return refs
}

func l302JSON(t *testing.T, value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return raw
}

func l302HostID(raw []byte) string {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	var payload hostBuildPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ""
	}
	return payload.HostID
}

func l302HostPrivateKey(raw []byte) ed25519.PrivateKey {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	var payload hostBuildPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil
	}
	for index, hostID := range canonicalHostIDs {
		if hostID == payload.HostID {
			return testSigningKey(byte(index * 32))
		}
	}
	return nil
}

func TestLanesL302MatrixDefinitionsMatchFixedDesign(t *testing.T) {
	wantRuntimeCounts := map[string]int{"mac-control-plane": 4, "linux-host": 4, "sandbox-host": 4, "factory-sandbox": 4}
	wantManagementCounts := map[string]int{"mac-control-plane": 8, "linux-host": 4, "sandbox-host": 4, "factory-sandbox": 4}
	for hostID, want := range wantRuntimeCounts {
		if got := len(runtimeComponents[hostID]); got != want {
			t.Errorf("runtime components for %s = %d, want %d", hostID, got, want)
		}
	}
	for hostID, want := range wantManagementCounts {
		if got := len(managementComponents[hostID]); got != want {
			t.Errorf("management components for %s = %d, want %d", hostID, got, want)
		}
	}
	if got := len(expectedComponentKeys("lane_runtime")); got != 16 {
		t.Errorf("runtime matrix size = %d, want 16", got)
	}
	if got := len(expectedComponentKeys("host_management")); got != 20 {
		t.Errorf("management matrix size = %d, want 20", got)
	}
	if !reflect.DeepEqual(canonicalHostIDs, []string{"factory-sandbox", "linux-host", "mac-control-plane", "sandbox-host"}) {
		t.Fatalf("canonical host sort order changed: %v", canonicalHostIDs)
	}
}
