package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
	"remote-session-runner/src/internal/laneinstall"
)

const (
	l303RecordID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	l303Digest     = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	l303Transition = "018f3e56-789a-7abc-8def-0123456789ab"
)

func TestLanesL303CompilesEveryClosedPayloadSchema(t *testing.T) {
	valid := map[string]any{
		"candidate_manifest_id": l303RecordID, "lane_transition_id": l303Transition, "release_lane": "dev", "host_id": "linux-host",
		"capacity_policy_id": l303RecordID, "component_artifacts": l303Components("linux-host"), "resolved_configuration_digest": l303Digest,
		"installation_generation": 1, "not_before": l303Time(), "not_after": "2026-10-17T13:00:00Z", "required_revocation_ledger_generation": 1,
	}
	for _, recordType := range []string{"promotion_authorization", "promotion_attestation", "lane_acceptance", "ingress_admission_directive",
		"ingress_admission_attestation", "capacity_measurement_attestation", "capacity_policy", "pki_certificate_request"} {
		if err := compiledSchemaForL303(recordType); err != nil {
			t.Fatalf("compile %s schema: %v", recordType, err)
		}
	}
	valid["unexpected"] = true
	if err := ValidatePayload("promotion_authorization", l302JSON(t, valid)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("open promotion authorization error = %v, want ErrInvalidPayload", err)
	}
	if err := ValidatePayload("not_a_release_record", []byte(`{}`)); !errors.Is(err, ErrUnsupportedPayloadType) {
		t.Fatalf("unknown record type error = %v, want ErrUnsupportedPayloadType", err)
	}
}

func TestLanesL303PromotionAuthorizationBindsCandidateAndCapacityPolicy(t *testing.T) {
	candidate := l303Candidate(t)
	policy, measurement := l303LinuxPolicy(t)
	auth := l303Authorization("linux-host", "dev", policy)
	auth.CandidateManifestID = l303RecordID
	if err := ValidatePromotionAuthorization(l302JSON(t, auth)); err != nil {
		t.Fatalf("valid dev authorization rejected: %v", err)
	}
	if err := ValidatePromotionAuthorizationAgainstCandidate(l302JSON(t, auth), l303RecordID, candidate); err != nil {
		t.Fatalf("candidate-bound authorization rejected: %v", err)
	}
	if err := ValidatePromotionAuthorizationAgainstCapacityPolicy(l302JSON(t, auth), l303RecordID, policy,
		l303MeasurementRecordID, measurement); err != nil {
		t.Fatalf("policy-bound authorization rejected: %v", err)
	}

	bad := auth
	bad.PredecessorAcceptanceID = l303RecordID
	if err := ValidatePromotionAuthorization(l302JSON(t, bad)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Dev predecessor accepted: %v", err)
	}
	bad = auth
	bad.ComponentArtifacts = append([]componentArtifact(nil), auth.ComponentArtifacts...)
	bad.ComponentArtifacts[0].ArtifactDigest = strings.Replace(l303Digest, "bbbb", "cccc", 1)
	if err := ValidatePromotionAuthorizationAgainstCandidate(l302JSON(t, bad), l303RecordID, candidate); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("authorization with candidate artifact substitution accepted: %v", err)
	}
	bad = auth
	bad.CapacityPolicyID = strings.Repeat("c", 64)
	if err := ValidatePromotionAuthorizationAgainstCapacityPolicy(l302JSON(t, bad), l303RecordID, policy,
		l303MeasurementRecordID, measurement); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("authorization with substituted policy ID accepted: %v", err)
	}
	bad = auth
	bad.ReleaseLane = "qas"
	if err := ValidatePromotionAuthorization(l302JSON(t, bad)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("QAS authorization without predecessor accepted: %v", err)
	}
}

func TestLanesL303PromotionAttestationIsExactAndLaneAcceptanceRequiresFourHosts(t *testing.T) {
	auth := l303Authorization("linux-host", "qas", nil)
	auth.PredecessorAcceptanceID = l303RecordID
	attestation := l303Attestation(auth, "accepted")
	if err := ValidatePromotionAttestationForAuthorization(l303RecordID, l302JSON(t, auth), l302JSON(t, attestation)); err != nil {
		t.Fatalf("matching promotion attestation rejected: %v", err)
	}
	changed := attestation
	changed.InstallationGeneration++
	if err := ValidatePromotionAttestationForAuthorization(l303RecordID, l302JSON(t, auth), l302JSON(t, changed)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("attestation with changed generation accepted: %v", err)
	}

	acceptance := laneAcceptancePayload{CandidateID: l303RecordID, Lane: "qas", TransitionID: l303Transition,
		AttestationIDs: []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)},
		EvidenceDigest: l303Digest, AcceptedAt: l303Time()}
	attestations := make(map[string][]byte, 4)
	for i, host := range canonicalHostIDs {
		promotion := l303Authorization(host, "qas", nil)
		promotion.PredecessorAcceptanceID = strings.Repeat("9", 64)
		promotion.CandidateManifestID = acceptance.CandidateID
		promotion.LaneTransitionID = acceptance.TransitionID
		attestations[acceptance.AttestationIDs[i]] = l302JSON(t, l303Attestation(promotion, "accepted"))
	}
	if err := ValidateLaneAcceptance(l302JSON(t, acceptance), attestations); err != nil {
		t.Fatalf("complete lane acceptance rejected: %v", err)
	}
	delete(attestations, acceptance.AttestationIDs[0])
	if err := ValidateLaneAcceptance(l302JSON(t, acceptance), attestations); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("incomplete lane acceptance accepted: %v", err)
	}
}

func TestLanesL303IngressDirectiveAndAttestationRequireExactAppliedSurfaceSet(t *testing.T) {
	directive := ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition, Lane: "dev", HostID: "linux-host",
		ConfigurationDigest: l303Digest, Role: "candidate_admission", TransitionEpoch: 1, AdmissionGeneration: 2,
		LeaseGeneration: 0, NotBefore: l303Time(), NotAfter: "2026-10-17T13:00:00Z", RequiredLedgerGeneration: 1}
	attestation := ingressAttestationPayload{DirectiveID: l303RecordID, CandidateID: directive.CandidateID, TransitionID: directive.TransitionID,
		Lane: directive.Lane, HostID: directive.HostID, ConfigurationDigest: directive.ConfigurationDigest, Role: directive.Role,
		TransitionEpoch: directive.TransitionEpoch, AdmissionGeneration: directive.AdmissionGeneration, LeaseGeneration: directive.LeaseGeneration,
		ObservedAt: l303Time(), Surfaces: []ingressSurfacePayload{{SurfaceID: "direct_https", Outcome: "applied", EvidenceDigest: l303Digest},
			{SurfaceID: "queued_ssh_bridge", Outcome: "applied", EvidenceDigest: strings.Replace(l303Digest, "bbbb", "cccc", 1)}}}
	applied, err := ValidateIngressAdmissionAttestationForDirective(l303RecordID, l302JSON(t, directive), l302JSON(t, attestation))
	if err != nil || !applied {
		t.Fatalf("complete applied ingress set = applied %t, err %v", applied, err)
	}
	attestation.Surfaces[1].Outcome = "expired"
	applied, err = ValidateIngressAdmissionAttestationForDirective(l303RecordID, l302JSON(t, directive), l302JSON(t, attestation))
	if err != nil || applied {
		t.Fatalf("expired surface result = applied %t, err %v", applied, err)
	}
	attestation.Surfaces = attestation.Surfaces[:1]
	if err := ValidateIngressAdmissionAttestation(l302JSON(t, attestation)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("incomplete ingress surface inventory accepted: %v", err)
	}
	directive.Role, directive.AdmissionGeneration = "active_runtime_closure", 1
	if err := ValidateIngressAdmissionDirective(l302JSON(t, directive)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("open runtime closure directive accepted: %v", err)
	}
	mac := attestation
	mac.HostID = "mac-control-plane"
	mac.Surfaces = []ingressSurfacePayload{{SurfaceID: "direct_https", Outcome: "applied", EvidenceDigest: l303Digest}, {SurfaceID: "queued_ssh_bridge", Outcome: "applied", EvidenceDigest: l303Digest}}
	if err := ValidateIngressAdmissionAttestation(l302JSON(t, mac)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Linux inventory accepted for Mac host: %v", err)
	}
}

func TestLanesL303CapacityMeasurementRecomputesFullEvaluationDigest(t *testing.T) {
	measurement, _ := l303LinuxMeasurement(t)
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	attestation := capacityMeasurementPayload{HostID: measurement.Identity.HostID, Measurement: measurement,
		EvidenceRefs: []string{l303Digest}, EvaluationDigest: digest}
	if err := ValidateCapacityMeasurementAttestation(l302JSON(t, attestation)); err != nil {
		t.Fatalf("valid capacity measurement rejected: %v", err)
	}
	attestation.Measurement.Usage.UsedBytes++
	if err := ValidateCapacityMeasurementAttestation(l302JSON(t, attestation)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("altered measurement with stale digest accepted: %v", err)
	}
	attestation.Measurement.Usage.UsedBytes--
	attestation.HostID = "factory-sandbox"
	if err := ValidateCapacityMeasurementAttestation(l302JSON(t, attestation)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("cross-host measurement accepted: %v", err)
	}
}

func TestLanesL303CapacityPolicyUsesExactMeasurementScopeAndEnvelope(t *testing.T) {
	policyRaw, measurementRaw := l303LinuxPolicy(t)
	if err := ValidateCapacityPolicy(policyRaw, l303MeasurementRecordID, measurementRaw); err != nil {
		t.Fatalf("valid measured policy rejected: %v", err)
	}
	var policy lanecapacity.Policy
	if err := json.Unmarshal(policyRaw, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Compute = lanecapacity.ComputeNone
	if err := ValidateCapacityPolicy(l302JSON(t, policy), l303MeasurementRecordID, measurementRaw); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("policy with unsupported compute claim accepted: %v", err)
	}
	policyRaw, measurementRaw = l303LinuxPolicy(t)
	if err := ValidateCapacityPolicy(policyRaw, strings.Repeat("f", 64), measurementRaw); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("capacity policy with mismatched measurement record ID accepted: %v", err)
	}
	var attestation capacityMeasurementPayload
	if err := json.Unmarshal(measurementRaw, &attestation); err != nil {
		t.Fatal(err)
	}
	attestation.Measurement.ResourceCapacity.CPUMilli++
	if err := ValidateCapacityPolicy(policyRaw, l303MeasurementRecordID, l302JSON(t, attestation)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("capacity policy with changed measured envelope accepted: %v", err)
	}
}

func TestLanesL303MacCapacityPolicyBindsMeasuredAPFSVolumesAndHeadroom(t *testing.T) {
	policyRaw, measurementRaw, measurementID := l303MacPolicy(t)
	if err := ValidateCapacityPolicy(policyRaw, measurementID, measurementRaw); err != nil {
		t.Fatalf("valid Mac APFS lane policy rejected: %v", err)
	}
	var policy lanecapacity.Policy
	if err := json.Unmarshal(policyRaw, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Compute = lanecapacity.ComputeLinuxCgroupV2
	if err := ValidateCapacityPolicy(l302JSON(t, policy), measurementID, measurementRaw); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Mac policy with Linux compute reservation accepted: %v", err)
	}
}

func TestLanesL303PKIRequestBindsCSRContextAndHostSAN(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	valid := l303PKIRequest(t, now, "linux-host", nil)
	if err := ValidatePKICertificateRequest(valid, now); err != nil {
		t.Fatalf("valid test CSR request rejected: %v", err)
	}
	macRequest := l303PKIRequest(t, now, "mac-control-plane", nil)
	if err := ValidatePKICertificateRequest(macRequest, now); err != nil {
		t.Fatalf("valid Mac authority-client CSR request rejected: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(valid, &payload); err != nil {
		t.Fatal(err)
	}
	payload["public_key_fingerprint"] = strings.Repeat("0", 64)
	if err := ValidatePKICertificateRequest(l302JSON(t, payload), now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("CSR key fingerprint substitution accepted: %v", err)
	}
	var request map[string]any
	if err := json.Unmarshal(valid, &request); err != nil {
		t.Fatal(err)
	}
	request["host_id"] = "factory-sandbox"
	if err := ValidatePKICertificateRequest(l302JSON(t, request), now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("CSR SAN/host substitution accepted: %v", err)
	}
	var contextPayload map[string]any
	if err := json.Unmarshal(valid, &contextPayload); err != nil {
		t.Fatal(err)
	}
	contextPayload["context"].(map[string]any)["bootstrap_contract_digest"] = l303Digest
	if err := ValidatePKICertificateRequest(l302JSON(t, contextPayload), now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("mixed management/bootstrap PKI context accepted: %v", err)
	}
	bootstrapPayload := map[string]any{}
	if err := json.Unmarshal(valid, &bootstrapPayload); err != nil {
		t.Fatal(err)
	}
	bootstrapPayload["context"] = map[string]any{"mode": "bootstrap", "bootstrap_contract_digest": l303Digest,
		"bootstrap_generation": 1, "installer": map[string]any{"source_commit": l302SourceCommit, "toolchain_digest": l303Digest,
			"input_digest": l303Digest, "binary_digest": l303Digest}, "bootstrap_attestation_key_id": "test-bootstrap-key"}
	if err := ValidatePKICertificateRequest(l302JSON(t, bootstrapPayload), now); err != nil {
		t.Fatalf("valid isolated bootstrap PKI context rejected: %v", err)
	}
}

func TestLanesL303RejectsCrossHostAndUnsortedCanonicalSets(t *testing.T) {
	auth := l303Authorization("linux-host", "dev", nil)
	auth.ComponentArtifacts[0], auth.ComponentArtifacts[1] = auth.ComponentArtifacts[1], auth.ComponentArtifacts[0]
	if err := ValidatePromotionAuthorization(l302JSON(t, auth)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unsorted promotion component set accepted: %v", err)
	}
	directive := ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition, Lane: "dev", HostID: "linux-host",
		ConfigurationDigest: l303Digest, Role: "candidate_admission", TransitionEpoch: 1, AdmissionGeneration: 0,
		NotBefore: l303Time(), NotAfter: "2026-10-17T13:00:00Z", RequiredLedgerGeneration: 1}
	attestation := ingressAttestationPayload{DirectiveID: l303RecordID, CandidateID: directive.CandidateID, TransitionID: directive.TransitionID,
		Lane: directive.Lane, HostID: "sandbox-host", ConfigurationDigest: directive.ConfigurationDigest, Role: directive.Role,
		TransitionEpoch: directive.TransitionEpoch, AdmissionGeneration: directive.AdmissionGeneration, ObservedAt: l303Time(),
		Surfaces: []ingressSurfacePayload{{SurfaceID: "direct_https", Outcome: "applied", EvidenceDigest: l303Digest},
			{SurfaceID: "queued_ssh_bridge", Outcome: "applied", EvidenceDigest: l303Digest}}}
	if _, err := ValidateIngressAdmissionAttestationForDirective(l303RecordID, l302JSON(t, directive), l302JSON(t, attestation)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("cross-host ingress attestation accepted: %v", err)
	}
}

const l303MeasurementRecordID = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
const l303MacMeasurementRecordID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

func l303Candidate(t *testing.T) []byte {
	t.Helper()
	var fixtures []artifactTuple
	for _, host := range canonicalHostIDs {
		for _, component := range runtimeComponents[host] {
			fixtures = append(fixtures, artifactTuple{HostID: host, Component: component, GOOS: "linux", GOARCH: "amd64",
				ToolchainDigest: l303Digest, BuildInputDigest: l303Digest, ArtifactDigest: l303Digest, HostBuildAttestationID: l303RecordID})
		}
	}
	for i := range fixtures {
		if fixtures[i].HostID == "mac-control-plane" {
			fixtures[i].GOOS, fixtures[i].GOARCH = "darwin", "arm64"
		}
		if fixtures[i].HostID == "sandbox-host" {
			fixtures[i].GOARCH = "arm64"
		}
	}
	sort.Slice(fixtures, func(i, j int) bool {
		if fixtures[i].HostID == fixtures[j].HostID {
			return fixtures[i].Component < fixtures[j].Component
		}
		return fixtures[i].HostID < fixtures[j].HostID
	})
	return l302JSON(t, candidateManifestPayload{ReleaseComponentMatrixVersion: 1, RequiredManagementManifestID: l303RecordID,
		SourceCommitID: l302SourceCommit, GiteaCommitID: l302SourceCommit, GitHubCommitID: l302SourceCommit, BuildCohortTimestamp: l303Time(),
		TemplateSchemaDigest: l303Digest, EvidenceRefs: []string{l303Digest}, Artifacts: fixtures})
}

func l303Authorization(host, lane string, policyRaw []byte) promotionAuthorizationPayload {
	policyID := strings.Repeat("c", 64)
	if policyRaw != nil {
		policyID = l303RecordID
	}
	value := promotionAuthorizationPayload{CandidateManifestID: l303RecordID, LaneTransitionID: l303Transition, ReleaseLane: lane, HostID: host,
		CapacityPolicyID: policyID, ComponentArtifacts: l303Components(host), ConfigurationDigest: l303Digest, InstallationGeneration: 3,
		NotBefore: l303Time(), NotAfter: "2026-10-17T13:00:00Z", RequiredLedgerGeneration: 4}
	if lane != "dev" {
		value.PredecessorAcceptanceID = strings.Repeat("9", 64)
	}
	return value
}

func l303Attestation(auth promotionAuthorizationPayload, outcome string) promotionAttestationPayload {
	return promotionAttestationPayload{AuthorizationID: l303RecordID, CandidateID: auth.CandidateManifestID, TransitionID: auth.LaneTransitionID,
		CapacityPolicyID: auth.CapacityPolicyID, Lane: auth.ReleaseLane, HostID: auth.HostID, Components: auth.ComponentArtifacts,
		ConfigurationDigest: auth.ConfigurationDigest, InstallationGeneration: auth.InstallationGeneration, Outcome: outcome,
		EvidenceRefs: []string{l303Digest}, ObservedAt: l303Time()}
}

func l303Components(host string) []componentArtifact {
	components := append([]string(nil), runtimeComponents[host]...)
	sort.Strings(components)
	values := make([]componentArtifact, 0, len(components))
	for _, component := range components {
		values = append(values, componentArtifact{Component: component, ArtifactDigest: l303Digest})
	}
	return values
}

func l303LinuxMeasurement(t *testing.T) (lanecapacity.Measurement, []byte) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	measurement := lanecapacity.Measurement{ID: l303MeasurementRecordID, Identity: lanecapacity.Identity{HostID: "linux-host", Platform: lanecapacity.PlatformLinux,
		DeviceID: "8:1", Filesystem: "ext4", Mountpoint: "/", CapacityBytes: 4 * lanecapacity.GiB * 1024, CapacityInodes: 1_000_000_000},
		ObservedAt: now, ExpiresAt: now.Add(2 * time.Minute), ResourceCapacity: lanecapacity.ResourceVector{CPUMilli: 32_000,
			MemoryBytes: 128 * lanecapacity.GiB, Tasks: 100_000, IOReadBytesPerSecond: 2 * lanecapacity.GiB, IOWriteBytesPerSecond: 2 * lanecapacity.GiB,
			IOReadIOPSMax: 100_000, IOWriteIOPSMax: 100_000}, GoCachePaths: []string{"/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod"},
		Usage: lanecapacity.Usage{UsedBytes: 100 * lanecapacity.GiB, UsedInodes: 10_000_000, GoCacheBytes: 2 * lanecapacity.GiB,
			ReleaseCacheBytes: lanecapacity.GiB}}
	if _, err := measurement.EvaluationDigest(); err != nil {
		t.Fatalf("invalid synthetic capacity measurement: %v", err)
	}
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	attestation := capacityMeasurementPayload{HostID: measurement.Identity.HostID, Measurement: measurement, EvidenceRefs: []string{l303Digest}, EvaluationDigest: digest}
	return measurement, l302JSON(t, attestation)
}

func l303LinuxPolicy(t *testing.T) ([]byte, []byte) {
	t.Helper()
	measurement, attestation := l303LinuxMeasurement(t)
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	resource := lanecapacity.ResourceVector{CPUMilli: 1_000, MemoryBytes: 2 * lanecapacity.GiB, Tasks: 2_000,
		IOReadBytesPerSecond: 100 * 1024 * 1024, IOWriteBytesPerSecond: 100 * 1024 * 1024, IOReadIOPSMax: 2_000, IOWriteIOPSMax: 2_000}
	policy := lanecapacity.Policy{Version: 1, HostID: measurement.Identity.HostID, Scope: lanecapacity.ScopeLaneRuntime, Lane: "dev",
		MeasurementID: l303MeasurementRecordID, EvaluationDigest: digest, NotBefore: measurement.ObservedAt, NotAfter: measurement.ExpiresAt,
		Compute: lanecapacity.ComputeLinuxCgroupV2, LaneRuntime: &lanecapacity.LaneRuntimeBudget{
			PRDGrowthBytes: 100 * lanecapacity.GiB, DevHardBytes: 100 * lanecapacity.GiB, QASHardBytes: 100 * lanecapacity.GiB,
			ManagementGrowthBytes: 30 * lanecapacity.GiB, PRDGrowthInodes: 1_000_000, DevHardInodes: 1_000_000, QASHardInodes: 1_000_000,
			ManagementGrowthInodes: 1_000_000, GoCachePaths: measurement.GoCachePaths, GoCacheBudgetBytes: 4 * lanecapacity.GiB,
			ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB, BackupBudgetBytes: 3 * lanecapacity.GiB, BackupOnDataDevice: true,
			IODeviceID: measurement.Identity.DeviceID, Resources: lanecapacity.ResourceEnvelope{PRDFloor: resource, DevHardMax: resource,
				QASHardMax: resource, ManagementHardMax: resource}}}
	raw := l302JSON(t, policy)
	if err := ValidateCapacityPolicy(raw, l303MeasurementRecordID, attestation); err != nil {
		t.Fatalf("invalid synthetic lane policy: %v", err)
	}
	return raw, attestation
}

func l303MacPolicy(t *testing.T) (policyRaw, measurementRaw []byte, measurementID string) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	data := lanecapacity.StorageVolume{UUID: "11111111-1111-1111-1111-111111111111", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: "/System/Volumes/Data", CapacityBytes: 200 * lanecapacity.GiB}
	dev := lanecapacity.StorageVolume{UUID: "22222222-2222-2222-2222-222222222222", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: "/Volumes/Runner-Dev", CapacityBytes: 30 * lanecapacity.GiB, QuotaBytes: 15 * lanecapacity.GiB}
	qas := lanecapacity.StorageVolume{UUID: "33333333-3333-3333-3333-333333333333", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: "/Volumes/Runner-QAS", CapacityBytes: 30 * lanecapacity.GiB, QuotaBytes: 15 * lanecapacity.GiB}
	prd := lanecapacity.StorageVolume{UUID: "44444444-4444-4444-4444-444444444444", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: "/Volumes/Runner-PRD", CapacityBytes: 30 * lanecapacity.GiB, QuotaBytes: 24 * lanecapacity.GiB}
	buildCache := lanecapacity.StorageVolume{UUID: "55555555-5555-5555-5555-555555555555", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: lanecapacity.MacGoBuildCachePath, CapacityBytes: 2 * lanecapacity.GiB, QuotaBytes: 2 * lanecapacity.GiB}
	moduleCache := lanecapacity.StorageVolume{UUID: "66666666-6666-6666-6666-666666666666", DeviceID: "disk3", Filesystem: "apfs",
		Mountpoint: lanecapacity.MacGoModuleCachePath, CapacityBytes: 2 * lanecapacity.GiB, QuotaBytes: 2 * lanecapacity.GiB}
	paths := []string{lanecapacity.MacGoBuildCachePath, lanecapacity.MacGoModuleCachePath}
	measurement := lanecapacity.Measurement{ID: l303MacMeasurementRecordID,
		Identity: lanecapacity.Identity{HostID: "mac-control-plane", Platform: lanecapacity.PlatformMacOS, DeviceID: "disk3", Filesystem: "apfs",
			Mountpoint: data.Mountpoint, VolumeUUID: data.UUID, CapacityBytes: data.CapacityBytes, BackupDeviceID: "disk4", BackupFilesystem: "apfs",
			BackupMountpoint: "/Volumes/Runner-Backup", BackupCapacityBytes: 20 * lanecapacity.GiB},
		ObservedAt: now, ExpiresAt: now.Add(2 * time.Minute), GoCachePaths: paths,
		StorageVolumes: []lanecapacity.StorageVolume{data, dev, qas, prd, buildCache, moduleCache},
		Usage: lanecapacity.Usage{NonPurgeableBytes: 100 * lanecapacity.GiB, SnapshotBytes: 8 * lanecapacity.GiB,
			GoCacheBytes: 4 * lanecapacity.GiB, ReleaseCacheBytes: 2 * lanecapacity.GiB, BackupBytes: 2 * lanecapacity.GiB}}
	digest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatalf("invalid synthetic Mac capacity measurement: %v", err)
	}
	attestation := capacityMeasurementPayload{HostID: "mac-control-plane", Measurement: measurement, EvidenceRefs: []string{l303Digest}, EvaluationDigest: digest}
	macData := &lanecapacity.MacDataBudget{StorageMode: lanecapacity.StorageAPFSQuota, PRDHostBudgetBytes: 24 * lanecapacity.GiB,
		DevVolumeUUID: dev.UUID, DevMountpoint: dev.Mountpoint, DevQuotaBytes: dev.QuotaBytes,
		QASVolumeUUID: qas.UUID, QASMountpoint: qas.Mountpoint, QASQuotaBytes: qas.QuotaBytes,
		SharedCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
		SnapshotBudgetBytes: 10 * lanecapacity.GiB, BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB}
	policy := lanecapacity.Policy{Version: 1, HostID: "mac-control-plane", Scope: lanecapacity.ScopeLaneRuntime, Lane: "dev",
		MeasurementID: l303MacMeasurementRecordID, EvaluationDigest: digest, NotBefore: measurement.ObservedAt, NotAfter: measurement.ExpiresAt,
		Compute: lanecapacity.ComputeNone, LaneRuntime: &lanecapacity.LaneRuntimeBudget{GoCachePaths: paths,
			GoCacheBudgetBytes: macData.SharedCacheBudgetBytes, ReleaseCacheBudgetBytes: macData.ReleaseCacheBudgetBytes,
			BackupBudgetBytes: macData.BackupBudgetBytes, BackupLowWaterBytes: macData.BackupLowWaterBytes, MacData: macData}}
	return l302JSON(t, policy), l302JSON(t, attestation), l303MacMeasurementRecordID
}

func l303PKIRequest(t *testing.T, now time.Time, host string, dnsNames []string) []byte {
	t.Helper()
	plan, err := laneinstall.BuildPKIHostPlan(host)
	if err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(plan.URISAN)
	if err != nil {
		t.Fatal(err)
	}
	csrTemplate := &x509.CertificateRequest{Subject: pkix.Name{CommonName: plan.CommonName}, URIs: []*url.URL{uri}, DNSNames: dnsNames}
	if plan.IP != "" {
		csrTemplate.IPAddresses = []net.IP{net.ParseIP(plan.IP)}
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, private)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificateRequest(csr)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(parsed.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(spki)
	payload := map[string]any{"pki_issuance_mandate_id": l303RecordID, "host_id": host, "leaf_role": plan.Role,
		"context": laneinstall.PKIIssuanceContext{Mode: "management", ManagementManifestID: strings.Repeat("c", 64), ManagementGeneration: 7},
		"uri_san": plan.URISAN, "csr_der": csr, "public_key_fingerprint": hex.EncodeToString(fingerprint[:]), "ca_profile": plan.CAProfile,
		"certificate_not_after": now.Add(30 * 24 * time.Hour)}
	if plan.IP != "" {
		payload["ip_san"] = plan.IP
	}
	return l302JSON(t, payload)
}

func l303Time() string { return "2026-10-17T12:00:00Z" }

func compiledSchemaForL303(recordType string) error {
	_, ok := payloadSchemaName(recordType)
	if !ok {
		return ErrUnsupportedPayloadType
	}
	_, err := compiledPayloadSchema(recordType)
	return err
}
