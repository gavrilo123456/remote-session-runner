package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
	"remote-session-runner/src/internal/laneinstall"
)

type componentArtifact struct {
	Component      string `json:"component"`
	ArtifactDigest string `json:"artifact_digest"`
}

type promotionAuthorizationPayload struct {
	CandidateManifestID      string              `json:"candidate_manifest_id"`
	LaneTransitionID         string              `json:"lane_transition_id"`
	ReleaseLane              string              `json:"release_lane"`
	HostID                   string              `json:"host_id"`
	CapacityPolicyID         string              `json:"capacity_policy_id"`
	ComponentArtifacts       []componentArtifact `json:"component_artifacts"`
	ConfigurationDigest      string              `json:"resolved_configuration_digest"`
	InstallationGeneration   uint64              `json:"installation_generation"`
	PredecessorAcceptanceID  string              `json:"predecessor_lane_acceptance_id,omitempty"`
	NotBefore                string              `json:"not_before"`
	NotAfter                 string              `json:"not_after"`
	RequiredLedgerGeneration uint64              `json:"required_revocation_ledger_generation"`
}

type promotionAttestationPayload struct {
	AuthorizationID        string              `json:"promotion_authorization_id"`
	CandidateID            string              `json:"candidate_manifest_id"`
	TransitionID           string              `json:"lane_transition_id"`
	CapacityPolicyID       string              `json:"capacity_policy_id"`
	Lane                   string              `json:"release_lane"`
	HostID                 string              `json:"host_id"`
	Components             []componentArtifact `json:"component_artifacts"`
	ConfigurationDigest    string              `json:"resolved_configuration_digest"`
	InstallationGeneration uint64              `json:"installation_generation"`
	Outcome                string              `json:"outcome"`
	EvidenceRefs           []string            `json:"evidence_refs"`
	ObservedAt             string              `json:"observed_at"`
}

type laneAcceptancePayload struct {
	CandidateID    string   `json:"candidate_manifest_id"`
	Lane           string   `json:"release_lane"`
	TransitionID   string   `json:"lane_transition_id"`
	AttestationIDs []string `json:"accepted_promotion_attestation_ids"`
	EvidenceDigest string   `json:"acceptance_evidence_digest"`
	AcceptedAt     string   `json:"accepted_at"`
}

type ingressDirectivePayload struct {
	CandidateID              string `json:"candidate_manifest_id"`
	TransitionID             string `json:"lane_transition_id"`
	Lane                     string `json:"release_lane"`
	HostID                   string `json:"host_id"`
	ConfigurationDigest      string `json:"resolved_configuration_digest"`
	Role                     string `json:"directive_role"`
	TransitionEpoch          uint64 `json:"transition_epoch"`
	AdmissionGeneration      uint8  `json:"admission_generation"`
	LeaseGeneration          uint64 `json:"lease_generation"`
	PredecessorRecordID      string `json:"predecessor_record_id,omitempty"`
	NotBefore                string `json:"not_before"`
	NotAfter                 string `json:"not_after"`
	RequiredLedgerGeneration uint64 `json:"required_revocation_ledger_generation"`
}

type ingressSurfacePayload struct {
	SurfaceID      string `json:"surface_id"`
	Outcome        string `json:"outcome"`
	EvidenceDigest string `json:"evidence_digest"`
}

type ingressAttestationPayload struct {
	DirectiveID         string                  `json:"ingress_admission_directive_id"`
	CandidateID         string                  `json:"candidate_manifest_id"`
	TransitionID        string                  `json:"lane_transition_id"`
	Lane                string                  `json:"release_lane"`
	HostID              string                  `json:"host_id"`
	ConfigurationDigest string                  `json:"resolved_configuration_digest"`
	Role                string                  `json:"directive_role"`
	TransitionEpoch     uint64                  `json:"transition_epoch"`
	AdmissionGeneration uint8                   `json:"admission_generation"`
	LeaseGeneration     uint64                  `json:"lease_generation"`
	Surfaces            []ingressSurfacePayload `json:"surfaces"`
	ObservedAt          string                  `json:"observed_at"`
}

type capacityMeasurementPayload struct {
	HostID           string                   `json:"host_id"`
	Measurement      lanecapacity.Measurement `json:"measurement"`
	EvidenceRefs     []string                 `json:"evidence_refs"`
	EvaluationDigest string                   `json:"capacity_evaluation_digest"`
}

// ValidatePromotionAuthorization checks its closed shape, exact component set
// for the named host, predecessor rule, and nonempty validity interval. The
// caller still verifies its authority signature and referenced record types.
func ValidatePromotionAuthorization(raw []byte) error {
	if err := ValidatePayload("promotion_authorization", raw); err != nil {
		return err
	}
	var value promotionAuthorizationPayload
	if err := decodeStrict(raw, &value); err != nil {
		return invalidL3Payload("decode promotion authorization")
	}
	if !validHostComponents(value.HostID, value.ComponentArtifacts) || value.InstallationGeneration == 0 || value.RequiredLedgerGeneration == 0 ||
		(value.ReleaseLane == "dev" && value.PredecessorAcceptanceID != "") ||
		(value.ReleaseLane != "dev" && value.PredecessorAcceptanceID == "") || !validMaximumReleaseInterval(value.NotBefore, value.NotAfter) {
		return invalidL3Payload("promotion authorization identity, predecessor, or validity")
	}
	return nil
}

// ValidatePromotionAuthorizationAgainstCandidate binds the authorization to
// the exact host component/artifact subset in a caller-verified candidate.
func ValidatePromotionAuthorizationAgainstCandidate(raw []byte, candidateID string, candidateRaw []byte) error {
	if err := ValidatePromotionAuthorization(raw); err != nil {
		return err
	}
	if err := ValidatePayload("candidate_manifest", candidateRaw); err != nil {
		return err
	}
	var auth promotionAuthorizationPayload
	var candidate candidateManifestPayload
	if decodeStrict(raw, &auth) != nil || decodeStrict(candidateRaw, &candidate) != nil || !isLowerHexDigest(candidateID) || auth.CandidateManifestID != candidateID {
		return invalidL3Payload("promotion candidate reference")
	}
	want := make([]componentArtifact, 0, len(auth.ComponentArtifacts))
	for _, artifact := range candidate.Artifacts {
		if artifact.HostID == auth.HostID {
			want = append(want, componentArtifact{Component: artifact.Component, ArtifactDigest: artifact.ArtifactDigest})
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Component < want[j].Component })
	if !slices.Equal(auth.ComponentArtifacts, want) {
		return invalidL3Payload("promotion artifact subset differs from candidate")
	}
	return nil
}

// ValidatePromotionAuthorizationAgainstCapacityPolicy checks the exact signed
// policy reference and its host/lane scope against the authorization.
func ValidatePromotionAuthorizationAgainstCapacityPolicy(raw []byte, policyID string, policyRaw []byte, measurementID string, measurementRaw []byte) error {
	if err := ValidatePromotionAuthorization(raw); err != nil {
		return err
	}
	if !isLowerHexDigest(policyID) {
		return invalidL3Payload("capacity policy record ID")
	}
	if err := ValidateCapacityPolicy(policyRaw, measurementID, measurementRaw); err != nil {
		return err
	}
	var auth promotionAuthorizationPayload
	var policy lanecapacity.Policy
	if decodeStrict(raw, &auth) != nil || decodeStrict(policyRaw, &policy) != nil || auth.CapacityPolicyID != policyID ||
		policy.Scope != lanecapacity.ScopeLaneRuntime || policy.HostID != auth.HostID || policy.Lane != auth.ReleaseLane {
		return invalidL3Payload("promotion authorization capacity policy scope")
	}
	return nil
}

// ValidatePromotionAttestation validates the host-scoped result and its exact
// canonical component set. Signature trust is checked by the envelope layer.
func ValidatePromotionAttestation(raw []byte) error {
	if err := ValidatePayload("promotion_attestation", raw); err != nil {
		return err
	}
	var value promotionAttestationPayload
	if err := decodeStrict(raw, &value); err != nil {
		return invalidL3Payload("decode promotion attestation")
	}
	if !validHostComponents(value.HostID, value.Components) || !sortedUnique(value.EvidenceRefs) || !validUTCTimestamp(value.ObservedAt) || value.InstallationGeneration == 0 {
		return invalidL3Payload("promotion attestation identity or evidence")
	}
	return nil
}

// ValidatePromotionAttestationForAuthorization binds every authorization
// identity and artifact field to the host's resulting attestation.
func ValidatePromotionAttestationForAuthorization(authorizationID string, authorizationRaw, attestationRaw []byte) error {
	if !isLowerHexDigest(authorizationID) {
		return invalidL3Payload("promotion authorization record ID")
	}
	if err := ValidatePromotionAuthorization(authorizationRaw); err != nil {
		return err
	}
	if err := ValidatePromotionAttestation(attestationRaw); err != nil {
		return err
	}
	var auth promotionAuthorizationPayload
	var result promotionAttestationPayload
	if decodeStrict(authorizationRaw, &auth) != nil || decodeStrict(attestationRaw, &result) != nil ||
		result.AuthorizationID != authorizationID || result.CandidateID != auth.CandidateManifestID || result.TransitionID != auth.LaneTransitionID ||
		result.CapacityPolicyID != auth.CapacityPolicyID || result.Lane != auth.ReleaseLane || result.HostID != auth.HostID ||
		!slices.Equal(result.Components, auth.ComponentArtifacts) || result.ConfigurationDigest != auth.ConfigurationDigest ||
		result.InstallationGeneration != auth.InstallationGeneration {
		return invalidL3Payload("promotion attestation differs from authorization")
	}
	return nil
}

// ValidateLaneAcceptance binds exactly one accepted promotion attestation for
// each canonical host to this lane transition. Inputs are payloads from
// signature-verified envelopes, keyed by their verified record IDs.
func ValidateLaneAcceptance(raw []byte, verifiedAttestations map[string][]byte) error {
	if err := ValidatePayload("lane_acceptance", raw); err != nil {
		return err
	}
	var acceptance laneAcceptancePayload
	if decodeStrict(raw, &acceptance) != nil || !sortedUnique(acceptance.AttestationIDs) || !validUTCTimestamp(acceptance.AcceptedAt) ||
		len(acceptance.AttestationIDs) != len(canonicalHostIDs) || len(verifiedAttestations) != len(canonicalHostIDs) {
		return invalidL3Payload("lane acceptance attestation set")
	}
	hosts := make(map[string]bool, len(canonicalHostIDs))
	for _, id := range acceptance.AttestationIDs {
		rawAttestation, ok := verifiedAttestations[id]
		if !ok || ValidatePromotionAttestation(rawAttestation) != nil {
			return invalidL3Payload("missing or invalid promotion attestation")
		}
		var attestation promotionAttestationPayload
		if decodeStrict(rawAttestation, &attestation) != nil || attestation.Outcome != "accepted" || attestation.CandidateID != acceptance.CandidateID ||
			attestation.Lane != acceptance.Lane || attestation.TransitionID != acceptance.TransitionID || hosts[attestation.HostID] {
			return invalidL3Payload("promotion attestation does not satisfy lane acceptance")
		}
		hosts[attestation.HostID] = true
	}
	for _, host := range canonicalHostIDs {
		if !hosts[host] {
			return invalidL3Payload("lane acceptance omits a host")
		}
	}
	return nil
}

// ValidateIngressAdmissionDirective checks the closed phase and lifetime rules.
func ValidateIngressAdmissionDirective(raw []byte) error {
	if err := ValidatePayload("ingress_admission_directive", raw); err != nil {
		return err
	}
	var value ingressDirectivePayload
	if decodeStrict(raw, &value) != nil || value.TransitionEpoch == 0 || value.RequiredLedgerGeneration == 0 || !validMaximumReleaseInterval(value.NotBefore, value.NotAfter) ||
		(value.Role == "active_runtime_closure" && value.AdmissionGeneration != 0) {
		return invalidL3Payload("ingress directive phase or validity")
	}
	return nil
}

// ValidateIngressAdmissionAttestation validates the exact sorted surface set
// for a host. A valid result can still be rejected or expired at one surface.
func ValidateIngressAdmissionAttestation(raw []byte) error {
	if err := ValidatePayload("ingress_admission_attestation", raw); err != nil {
		return err
	}
	var value ingressAttestationPayload
	if decodeStrict(raw, &value) != nil || !validUTCTimestamp(value.ObservedAt) {
		return invalidL3Payload("decode ingress attestation")
	}
	want := requiredIngressSurfaces(value.HostID)
	if len(value.Surfaces) != len(want) {
		return invalidL3Payload("incomplete ingress surface inventory")
	}
	for i, surface := range value.Surfaces {
		if surface.SurfaceID != want[i] {
			return invalidL3Payload("ingress surface inventory differs from host contract")
		}
	}
	if value.Role == "active_runtime_closure" && value.AdmissionGeneration != 0 {
		return invalidL3Payload("closure attestation is not closed")
	}
	return nil
}

// ValidateIngressAdmissionAttestationForDirective binds an attestation to the
// exact directive and reports whether every required ingress control applied.
func ValidateIngressAdmissionAttestationForDirective(directiveID string, directiveRaw, attestationRaw []byte) (bool, error) {
	if !isLowerHexDigest(directiveID) {
		return false, invalidL3Payload("ingress directive record ID")
	}
	if err := ValidateIngressAdmissionDirective(directiveRaw); err != nil {
		return false, err
	}
	if err := ValidateIngressAdmissionAttestation(attestationRaw); err != nil {
		return false, err
	}
	var directive ingressDirectivePayload
	var attestation ingressAttestationPayload
	if decodeStrict(directiveRaw, &directive) != nil || decodeStrict(attestationRaw, &attestation) != nil ||
		attestation.DirectiveID != directiveID || attestation.CandidateID != directive.CandidateID || attestation.TransitionID != directive.TransitionID ||
		attestation.Lane != directive.Lane || attestation.HostID != directive.HostID || attestation.ConfigurationDigest != directive.ConfigurationDigest ||
		attestation.Role != directive.Role || attestation.TransitionEpoch != directive.TransitionEpoch ||
		attestation.AdmissionGeneration != directive.AdmissionGeneration || attestation.LeaseGeneration != directive.LeaseGeneration {
		return false, invalidL3Payload("ingress attestation differs from directive")
	}
	for _, surface := range attestation.Surfaces {
		if surface.Outcome != "applied" {
			return false, nil
		}
	}
	return true, nil
}

// ValidateCapacityMeasurementAttestation checks host identity and recomputes
// the deterministic digest over the complete measured inventory.
func ValidateCapacityMeasurementAttestation(raw []byte) error {
	if err := ValidatePayload("capacity_measurement_attestation", raw); err != nil {
		return err
	}
	var value capacityMeasurementPayload
	if decodeStrict(raw, &value) != nil || value.HostID != value.Measurement.Identity.HostID || !sortedUnique(value.EvidenceRefs) ||
		!validUTCTimestamp(value.Measurement.ObservedAt.Format(time.RFC3339Nano)) || !validUTCTimestamp(value.Measurement.ExpiresAt.Format(time.RFC3339Nano)) {
		return invalidL3Payload("capacity measurement identity or evidence")
	}
	digest, err := value.Measurement.EvaluationDigest()
	if err != nil || digest != value.EvaluationDigest {
		return invalidL3Payload("capacity evaluation digest mismatch")
	}
	return nil
}

// ValidateCapacityPolicy binds the policy to a verified measurement record,
// its host and evaluation digest, then applies the existing capacity equations
// at the measurement-bound policy start time. The caller verifies signatures.
func ValidateCapacityPolicy(raw []byte, measurementRecordID string, measurementAttestationRaw []byte) error {
	if err := ValidatePayload("capacity_policy", raw); err != nil {
		return err
	}
	if !isLowerHexDigest(measurementRecordID) {
		return invalidL3Payload("capacity measurement record ID")
	}
	if err := ValidateCapacityMeasurementAttestation(measurementAttestationRaw); err != nil {
		return err
	}
	var policy lanecapacity.Policy
	var measurementAttestation capacityMeasurementPayload
	if decodeStrict(raw, &policy) != nil || decodeStrict(measurementAttestationRaw, &measurementAttestation) != nil ||
		policy.MeasurementID != measurementRecordID || policy.HostID != measurementAttestation.HostID ||
		policy.EvaluationDigest != measurementAttestation.EvaluationDigest || policy.NotBefore.Before(measurementAttestation.Measurement.ObservedAt) ||
		policy.NotAfter.After(measurementAttestation.Measurement.ExpiresAt) {
		return invalidL3Payload("capacity policy measurement binding")
	}
	m := measurementAttestation.Measurement
	live := lanecapacity.LiveObservation{Identity: m.Identity, ObservedAt: m.ObservedAt, ResourceCapacity: m.ResourceCapacity,
		GoCachePaths: m.GoCachePaths, LinuxDevices: m.LinuxDevices, StorageVolumes: m.StorageVolumes, Usage: m.Usage}
	if err := lanecapacity.ValidatePolicy(policy, m, live, policy.NotBefore); err != nil {
		return fmt.Errorf("%w: capacity policy: %v", ErrInvalidPayload, err)
	}
	return nil
}

// ValidatePKICertificateRequest checks the fixed per-host URI/IP/role tuple,
// disjoint management/bootstrap context, signed public CSR and key fingerprint.
// It validates only synthetic or supplied payload material and never issues a
// credential.
func ValidatePKICertificateRequest(raw []byte, now time.Time) error {
	if err := ValidatePayload("pki_certificate_request", raw); err != nil {
		return err
	}
	var value laneinstall.PKICertificateRequest
	if decodeStrict(raw, &value) != nil || !validPKIContext(value.Context) {
		return invalidL3Payload("PKI request context")
	}
	plan, err := laneinstall.BuildPKIHostPlan(value.HostID)
	if err != nil || string(value.Role) != string(plan.Role) || value.URISAN != plan.URISAN || value.IP != plan.IP || value.CAProfile != plan.CAProfile {
		return invalidL3Payload("PKI host identity tuple")
	}
	if len(value.CSRDER) == 0 || len(value.CSRDER) > 16*1024 {
		return invalidL3Payload("PKI CSR size")
	}
	csr, err := x509.ParseCertificateRequest(value.CSRDER)
	if err != nil || csr.CheckSignature() != nil || !validPKICSRForPlan(csr, plan) {
		return invalidL3Payload("PKI CSR is invalid or has unapproved identities")
	}
	spki, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return invalidL3Payload("PKI CSR public key")
	}
	digest := sha256.Sum256(spki)
	if value.PublicKeyFingerprint != hex.EncodeToString(digest[:]) {
		return invalidL3Payload("PKI CSR public key fingerprint mismatch")
	}
	now = now.UTC()
	if value.CertificateNotAfter.IsZero() || !value.CertificateNotAfter.After(now) || value.CertificateNotAfter.After(now.Add(90*24*time.Hour)) ||
		!value.CertificateNotAfter.Equal(value.CertificateNotAfter.UTC()) {
		return invalidL3Payload("PKI certificate validity outside bound")
	}
	return nil
}

func validPKIContext(value laneinstall.PKIIssuanceContext) bool {
	switch value.Mode {
	case "management":
		return isLowerHexDigest(value.ManagementManifestID) && value.ManagementGeneration > 0 && value.BootstrapContractDigest == "" &&
			value.BootstrapGeneration == 0 && value.Installer == (laneinstall.PKIInstallerArtifactTuple{}) && value.BootstrapAttestationKeyID == ""
	case "bootstrap":
		return value.ManagementManifestID == "" && value.ManagementGeneration == 0 && isDigest(value.BootstrapContractDigest) && value.BootstrapGeneration > 0 &&
			isSourceCommit(value.Installer.SourceCommit) && isDigest(value.Installer.ToolchainDigest) && isDigest(value.Installer.InputDigest) &&
			isDigest(value.Installer.BinaryDigest) && value.BootstrapAttestationKeyID != ""
	default:
		return false
	}
}

func validPKICSRForPlan(csr *x509.CertificateRequest, plan laneinstall.PKIHostPlan) bool {
	if csr == nil {
		return false
	}
	publicKey, publicKeyOK := csr.PublicKey.(ed25519.PublicKey)
	if !publicKeyOK || len(publicKey) != ed25519.PublicKeySize || csr.Subject.CommonName != plan.CommonName || !subjectHasOnlyCommonName(csr.Subject, plan.CommonName) || len(csr.DNSNames) != 0 ||
		len(csr.EmailAddresses) != 0 || len(csr.URIs) != 1 || csr.URIs[0].String() != plan.URISAN || len(csr.ExtraExtensions) != 0 {
		return false
	}
	for _, extension := range csr.Extensions {
		if !extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			return false
		}
	}
	if len(csr.Extensions) > 1 {
		return false
	}
	if plan.IP == "" {
		return len(csr.IPAddresses) == 0
	}
	return len(csr.IPAddresses) == 1 && csr.IPAddresses[0].Equal(net.ParseIP(plan.IP))
}

func subjectHasOnlyCommonName(subject pkix.Name, commonName string) bool {
	want := (pkix.Name{CommonName: commonName}).ToRDNSequence()
	got, errGot := asn1.Marshal(subject.ToRDNSequence())
	wantDER, errWant := asn1.Marshal(want)
	return errGot == nil && errWant == nil && string(got) == string(wantDER)
}

func validHostComponents(hostID string, values []componentArtifact) bool {
	components, ok := runtimeComponents[hostID]
	if !ok || len(values) != len(components) {
		return false
	}
	want := append([]string(nil), components...)
	sort.Strings(want)
	for i, value := range values {
		if value.Component != want[i] || !isDigest(value.ArtifactDigest) {
			return false
		}
	}
	return true
}

func requiredIngressSurfaces(hostID string) []string {
	if hostID == "mac-control-plane" {
		return []string{"local_api", "local_intent", "mailbox_ack_importer", "mailbox_request_importer"}
	}
	if slices.Contains(canonicalHostIDs, hostID) {
		return []string{"direct_https", "queued_ssh_bridge"}
	}
	return nil
}

func orderedInterval(start, end string) bool {
	if !validUTCTimestamp(start) || !validUTCTimestamp(end) {
		return false
	}
	a, errA := time.Parse(time.RFC3339Nano, start)
	b, errB := time.Parse(time.RFC3339Nano, end)
	return errA == nil && errB == nil && b.After(a)
}

func isDigest(value string) bool {
	return len(value) == 71 && value[:7] == "sha256:" && isLowerHexDigest(value[7:])
}
func isSourceCommit(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20
}

func invalidL3Payload(message string) error { return fmt.Errorf("%w: %s", ErrInvalidPayload, message) }
