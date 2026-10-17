package release

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/v1/*.schema.json
var releaseSchemaFiles embed.FS

const releaseSchemaBase = "https://remote-session-runner.invalid/release/v1/"

var (
	ErrInvalidPayload              = errors.New("invalid release record payload")
	ErrUnsupportedPayloadType      = errors.New("unsupported release record payload type")
	ErrInvalidComponentMatrix      = errors.New("release component matrix mismatch")
	ErrBuildAttestationMismatch    = errors.New("host build attestation does not match manifest tuple")
	ErrInvalidTransportContract    = errors.New("invalid release transport contract")
	payloadSchemaOnce              sync.Once
	payloadSchemas                 map[string]*jsonschema.Schema
	payloadSchemaInitializationErr error
)

type hostBuildPayload struct {
	HostID               string   `json:"host_id"`
	ReleaseTrack         string   `json:"release_track"`
	Component            string   `json:"component"`
	GOOS                 string   `json:"goos"`
	GOARCH               string   `json:"goarch"`
	SourceCommitID       string   `json:"source_commit_id"`
	GiteaCommitID        string   `json:"gitea_commit_id"`
	GitHubCommitID       string   `json:"github_commit_id"`
	ToolchainDigest      string   `json:"toolchain_digest"`
	BuildInputDigest     string   `json:"build_input_digest"`
	ArtifactDigest       string   `json:"artifact_digest"`
	TemplateSchemaDigest string   `json:"template_schema_digest"`
	EvidenceRefs         []string `json:"evidence_refs"`
	BuildTimestamp       string   `json:"build_timestamp"`
}

type artifactTuple struct {
	HostID                 string `json:"host_id"`
	Component              string `json:"component"`
	GOOS                   string `json:"goos"`
	GOARCH                 string `json:"goarch"`
	ToolchainDigest        string `json:"toolchain_digest"`
	BuildInputDigest       string `json:"build_input_digest"`
	ArtifactDigest         string `json:"artifact_digest"`
	HostBuildAttestationID string `json:"host_build_attestation_id"`
}

type candidateManifestPayload struct {
	ReleaseComponentMatrixVersion int             `json:"release_component_matrix_version"`
	RequiredManagementManifestID  string          `json:"required_host_management_manifest_id"`
	SourceCommitID                string          `json:"source_commit_id"`
	GiteaCommitID                 string          `json:"gitea_commit_id"`
	GitHubCommitID                string          `json:"github_commit_id"`
	BuildCohortTimestamp          string          `json:"build_cohort_timestamp"`
	TemplateSchemaDigest          string          `json:"template_schema_digest"`
	EvidenceRefs                  []string        `json:"evidence_refs"`
	Artifacts                     []artifactTuple `json:"artifacts"`
}

type hostManagementManifestPayload struct {
	MatrixVersion     int               `json:"host_management_matrix_version"`
	Compatibility     int               `json:"host_management_protocol_compatibility_version"`
	ContractDigest    string            `json:"bootstrap_update_contract_digest"`
	SourceCommitID    string            `json:"source_commit_id"`
	GiteaCommitID     string            `json:"gitea_commit_id"`
	GitHubCommitID    string            `json:"github_commit_id"`
	Artifacts         []artifactTuple   `json:"artifacts"`
	TransportContract transportContract `json:"release_transport_contract"`
}

type transportContract struct {
	CAInventory         []transportCA    `json:"ca_inventory"`
	TrustInventory      []transportTrust `json:"trust_inventory"`
	LeafInventory       []transportLeaf  `json:"leaf_inventory"`
	ActivePeerAllowlist []activePeer     `json:"active_peer_allowlist"`
}

type transportCA struct {
	CAID                   string `json:"ca_id"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	SPKIFingerprint        string `json:"spki_fingerprint"`
}

type transportTrust struct {
	HostID string `json:"host_id"`
	CAID   string `json:"ca_id"`
}

type transportLeaf struct {
	HostID                 string `json:"host_id"`
	CAID                   string `json:"ca_id"`
	LeafRole               string `json:"leaf_role"`
	URISAN                 string `json:"uri_san"`
	IPSAN                  string `json:"ip_san"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	SPKIFingerprint        string `json:"spki_fingerprint"`
}

type activePeer struct {
	HostID                 string `json:"host_id"`
	URISAN                 string `json:"uri_san"`
	IPSAN                  string `json:"ip_san"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	SPKIFingerprint        string `json:"spki_fingerprint"`
}

type releaseRecordReference struct {
	Type     string          `json:"record_type"`
	RecordID string          `json:"record_id"`
	Payload  json.RawMessage `json:"payload"`
}

// ValidatePayload applies the closed V1 schema for the L3-02 record types.
// Envelope canonicality, signature trust, and signer identity are checked by
// the envelope/key layer; payload objects are still parsed with duplicate-key
// and trailing-value rejection here.
func ValidatePayload(recordType string, raw []byte) error {
	name, ok := payloadSchemaName(recordType)
	if !ok {
		return ErrUnsupportedPayloadType
	}
	canonical, err := CanonicalizeJSON(raw)
	if err != nil || !isJSONObject(canonical) {
		return fmt.Errorf("%w: malformed JSON object", ErrInvalidPayload)
	}
	value, err := decodeSchemaValue(canonical)
	if err != nil {
		return fmt.Errorf("%w: decode payload", ErrInvalidPayload)
	}
	schema, err := compiledPayloadSchema(name)
	if err != nil {
		return fmt.Errorf("%w: load schema: %v", ErrInvalidPayload, err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return nil
}

// ValidateHostBuildAttestation validates the closed payload and its single
// host/component/platform tuple. It does not verify the envelope signature.
func ValidateHostBuildAttestation(raw []byte) error {
	if err := ValidatePayload("host_build_attestation", raw); err != nil {
		return err
	}
	var payload hostBuildPayload
	if err := decodeStrict(raw, &payload); err != nil {
		return fmt.Errorf("%w: decode host build attestation", ErrInvalidPayload)
	}
	if !equalSourceCommits(payload.SourceCommitID, payload.GiteaCommitID, payload.GitHubCommitID) {
		return fmt.Errorf("%w: source commit identifiers differ", ErrInvalidPayload)
	}
	if !isMatrixTuple(payload.HostID, payload.ReleaseTrack, payload.Component, payload.GOOS, payload.GOARCH) {
		return ErrInvalidComponentMatrix
	}
	if !sortedUnique(payload.EvidenceRefs) || !validUTCTimestamp(payload.BuildTimestamp) {
		return fmt.Errorf("%w: evidence references or timestamp are not canonical", ErrInvalidPayload)
	}
	return nil
}

// ValidateCandidateManifest checks the exact lane-runtime matrix and binds
// every artifact tuple to one previously signature-verified host build record.
// The map must contain only this cohort's verified host-build envelopes,
// keyed by their lowercase record IDs.
func ValidateCandidateManifest(raw []byte, verifiedHostBuildEnvelopes map[string][]byte) error {
	if err := ValidatePayload("candidate_manifest", raw); err != nil {
		return err
	}
	var manifest candidateManifestPayload
	if err := decodeStrict(raw, &manifest); err != nil {
		return fmt.Errorf("%w: decode candidate manifest", ErrInvalidPayload)
	}
	if !equalSourceCommits(manifest.SourceCommitID, manifest.GiteaCommitID, manifest.GitHubCommitID) ||
		!sortedUnique(manifest.EvidenceRefs) || !validUTCTimestamp(manifest.BuildCohortTimestamp) {
		return fmt.Errorf("%w: candidate cohort source, evidence, or timestamp mismatch", ErrInvalidPayload)
	}
	if err := validateArtifactMatrix(manifest.Artifacts, "lane_runtime"); err != nil {
		return err
	}
	return bindBuildAttestations(manifest.Artifacts, "lane_runtime", manifest.SourceCommitID,
		manifest.GiteaCommitID, manifest.GitHubCommitID, manifest.TemplateSchemaDigest,
		verifiedHostBuildEnvelopes)
}

// ValidateHostManagementManifest checks the complete host-management matrix,
// its release-transport inventory, and each referenced build attestation.
func ValidateHostManagementManifest(raw []byte, verifiedHostBuildEnvelopes map[string][]byte) error {
	if err := ValidatePayload("host_management_manifest", raw); err != nil {
		return err
	}
	var manifest hostManagementManifestPayload
	if err := decodeStrict(raw, &manifest); err != nil {
		return fmt.Errorf("%w: decode host-management manifest", ErrInvalidPayload)
	}
	if !equalSourceCommits(manifest.SourceCommitID, manifest.GiteaCommitID, manifest.GitHubCommitID) {
		return fmt.Errorf("%w: host-management source commit identifiers differ", ErrInvalidPayload)
	}
	if err := validateArtifactMatrix(manifest.Artifacts, "host_management"); err != nil {
		return err
	}
	if err := validateTransportContract(manifest.TransportContract); err != nil {
		return err
	}
	return bindBuildAttestations(manifest.Artifacts, "host_management", manifest.SourceCommitID,
		manifest.GiteaCommitID, manifest.GitHubCommitID, "", verifiedHostBuildEnvelopes)
}

func payloadSchemaName(recordType string) (string, bool) {
	switch recordType {
	case "host_build_attestation", "candidate_manifest", "host_management_manifest":
		return recordType, true
	default:
		return "", false
	}
}

func compiledPayloadSchema(name string) (*jsonschema.Schema, error) {
	payloadSchemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		files, err := releaseSchemaFiles.ReadDir("schemas/v1")
		if err != nil {
			payloadSchemaInitializationErr = err
			return
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".schema.json") {
				continue
			}
			data, err := releaseSchemaFiles.ReadFile(path.Join("schemas/v1", file.Name()))
			if err != nil {
				payloadSchemaInitializationErr = err
				return
			}
			document, err := decodeSchemaValue(data)
			if err != nil {
				payloadSchemaInitializationErr = fmt.Errorf("decode %s: %w", file.Name(), err)
				return
			}
			if err := compiler.AddResource(releaseSchemaBase+file.Name(), document); err != nil {
				payloadSchemaInitializationErr = fmt.Errorf("register %s: %w", file.Name(), err)
				return
			}
		}
		payloadSchemas = make(map[string]*jsonschema.Schema, 3)
		for _, recordType := range []string{"host_build_attestation", "candidate_manifest", "host_management_manifest"} {
			compiled, err := compiler.Compile(releaseSchemaBase + recordType + ".schema.json")
			if err != nil {
				payloadSchemaInitializationErr = fmt.Errorf("compile %s: %w", recordType, err)
				return
			}
			payloadSchemas[recordType] = compiled
		}
	})
	if payloadSchemaInitializationErr != nil {
		return nil, payloadSchemaInitializationErr
	}
	schema, ok := payloadSchemas[name]
	if !ok {
		return nil, ErrUnsupportedPayloadType
	}
	return schema, nil
}

func decodeSchemaValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("multiple JSON values")
	}
	return value, nil
}

func decodeStrict(raw []byte, destination any) error {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func equalSourceCommits(source, gitea, github string) bool {
	return source != "" && source == gitea && source == github
}

func validUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func sortedUnique(values []string) bool {
	for i := range values {
		if i > 0 && values[i-1] >= values[i] {
			return false
		}
	}
	return true
}

type componentKey struct {
	HostID    string
	Component string
}

var runtimeComponents = map[string][]string{
	"mac-control-plane": {"runner", "runner-local", "runner-locald", "runner-log-capture"},
	"linux-host":        {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
	"sandbox-host":      {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
	"factory-sandbox":   {"runnerd", "runner-ssh-bridge", "runner-session-agent", "runner-log-capture"},
}

var managementComponents = map[string][]string{
	"mac-control-plane": {
		"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper",
		"runner-release-authority-signer", "runner-release-authority-agent",
		"runner-release-authority-approval", "runner-release-transport-pki-issuer",
		"runner-release-transport-pki-installer",
	},
	"linux-host":      {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
	"sandbox-host":    {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
	"factory-sandbox": {"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
}

var canonicalHostIDs = []string{"factory-sandbox", "linux-host", "mac-control-plane", "sandbox-host"}

func expectedComponentKeys(track string) []componentKey {
	components := runtimeComponents
	if track == "host_management" {
		components = managementComponents
	}
	keys := make([]componentKey, 0, 20)
	for _, hostID := range canonicalHostIDs {
		for _, component := range components[hostID] {
			keys = append(keys, componentKey{HostID: hostID, Component: component})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].HostID == keys[j].HostID {
			return keys[i].Component < keys[j].Component
		}
		return keys[i].HostID < keys[j].HostID
	})
	return keys
}

func isMatrixTuple(hostID, track, component, goos, goarch string) bool {
	components := runtimeComponents
	if track == "host_management" {
		components = managementComponents
	} else if track != "lane_runtime" {
		return false
	}
	allowed := false
	for _, name := range components[hostID] {
		if name == component {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	switch hostID {
	case "mac-control-plane":
		return goos == "darwin" && (goarch == "amd64" || goarch == "arm64")
	case "linux-host", "factory-sandbox":
		return goos == "linux" && goarch == "amd64"
	case "sandbox-host":
		return goos == "linux" && goarch == "arm64"
	default:
		return false
	}
}

func validateArtifactMatrix(artifacts []artifactTuple, track string) error {
	expected := expectedComponentKeys(track)
	if len(artifacts) != len(expected) {
		return ErrInvalidComponentMatrix
	}
	platformByHost := make(map[string][2]string, len(canonicalHostIDs))
	for i, artifact := range artifacts {
		if artifact.HostID != expected[i].HostID || artifact.Component != expected[i].Component ||
			!isMatrixTuple(artifact.HostID, track, artifact.Component, artifact.GOOS, artifact.GOARCH) ||
			!isLowerHexDigest(artifact.HostBuildAttestationID) {
			return ErrInvalidComponentMatrix
		}
		platform := [2]string{artifact.GOOS, artifact.GOARCH}
		if previous, exists := platformByHost[artifact.HostID]; exists && previous != platform {
			return ErrInvalidComponentMatrix
		}
		platformByHost[artifact.HostID] = platform
	}
	return nil
}

func bindBuildAttestations(artifacts []artifactTuple, track, sourceCommit, giteaCommit, githubCommit,
	templateSchemaDigest string, verified map[string][]byte) error {
	if len(verified) != len(artifacts) {
		return ErrBuildAttestationMismatch
	}
	seen := make(map[string]struct{}, len(artifacts))
	cohortTemplateSchemaDigest := templateSchemaDigest
	for _, artifact := range artifacts {
		attestationID := artifact.HostBuildAttestationID
		if _, duplicate := seen[attestationID]; duplicate {
			return ErrBuildAttestationMismatch
		}
		seen[attestationID] = struct{}{}
		envelopeRaw, ok := verified[attestationID]
		if !ok {
			return ErrBuildAttestationMismatch
		}
		reference, err := parseBuildAttestationReference(envelopeRaw)
		if err != nil || reference.RecordID != attestationID {
			return ErrBuildAttestationMismatch
		}
		if err := ValidateHostBuildAttestation(reference.Payload); err != nil {
			return ErrBuildAttestationMismatch
		}
		var build hostBuildPayload
		if err := decodeStrict(reference.Payload, &build); err != nil {
			return ErrBuildAttestationMismatch
		}
		if cohortTemplateSchemaDigest == "" {
			cohortTemplateSchemaDigest = build.TemplateSchemaDigest
		}
		if build.ReleaseTrack != track || build.HostID != artifact.HostID || build.Component != artifact.Component ||
			build.GOOS != artifact.GOOS || build.GOARCH != artifact.GOARCH ||
			build.ToolchainDigest != artifact.ToolchainDigest || build.BuildInputDigest != artifact.BuildInputDigest ||
			build.ArtifactDigest != artifact.ArtifactDigest || build.SourceCommitID != sourceCommit ||
			build.GiteaCommitID != giteaCommit || build.GitHubCommitID != githubCommit ||
			build.TemplateSchemaDigest != cohortTemplateSchemaDigest {
			return ErrBuildAttestationMismatch
		}
	}
	return nil
}

func parseBuildAttestationReference(raw []byte) (releaseRecordReference, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) || !hasExactEnvelopeFields(canonical) {
		return releaseRecordReference{}, ErrInvalidEnvelope
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return releaseRecordReference{}, ErrInvalidEnvelope
	}
	if envelope.ReleaseRecordVersion != ReleaseRecordVersion || envelope.RecordType != "host_build_attestation" ||
		envelope.SignatureAlgorithm != SignatureAlgorithmEd25519 || envelope.SignerKeyID == "" ||
		!isLowerHexDigest(envelope.RecordID) || !isJSONObject(envelope.Payload) {
		return releaseRecordReference{}, ErrInvalidEnvelope
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || len(signature) != 64 {
		return releaseRecordReference{}, ErrInvalidSignature
	}
	bodyBytes, err := canonicalSignedBody(signedBody{
		ReleaseRecordVersion: envelope.ReleaseRecordVersion,
		RecordType:           envelope.RecordType,
		Payload:              envelope.Payload,
		SignatureAlgorithm:   envelope.SignatureAlgorithm,
		SignerKeyID:          envelope.SignerKeyID,
	})
	if err != nil {
		return releaseRecordReference{}, ErrInvalidEnvelope
	}
	digest := sha256.Sum256(signingInput(envelope.RecordType, bodyBytes))
	if envelope.RecordID != hex.EncodeToString(digest[:]) {
		return releaseRecordReference{}, ErrRecordIDMismatch
	}
	return releaseRecordReference{Type: envelope.RecordType, RecordID: envelope.RecordID, Payload: envelope.Payload}, nil
}

func validateTransportContract(contract transportContract) error {
	if !sort.SliceIsSorted(contract.CAInventory, func(i, j int) bool { return contract.CAInventory[i].CAID < contract.CAInventory[j].CAID }) ||
		!sort.SliceIsSorted(contract.TrustInventory, func(i, j int) bool {
			if contract.TrustInventory[i].HostID == contract.TrustInventory[j].HostID {
				return contract.TrustInventory[i].CAID < contract.TrustInventory[j].CAID
			}
			return contract.TrustInventory[i].HostID < contract.TrustInventory[j].HostID
		}) ||
		!sort.SliceIsSorted(contract.LeafInventory, func(i, j int) bool {
			return leafSortKey(contract.LeafInventory[i]) < leafSortKey(contract.LeafInventory[j])
		}) ||
		!sort.SliceIsSorted(contract.ActivePeerAllowlist, func(i, j int) bool {
			return peerSortKey(contract.ActivePeerAllowlist[i]) < peerSortKey(contract.ActivePeerAllowlist[j])
		}) {
		return ErrInvalidTransportContract
	}
	caIDs := make(map[string]struct{}, len(contract.CAInventory))
	for i, ca := range contract.CAInventory {
		if i > 0 && contract.CAInventory[i-1].CAID == ca.CAID {
			return ErrInvalidTransportContract
		}
		caIDs[ca.CAID] = struct{}{}
	}
	trustHosts := make(map[string]bool, len(canonicalHostIDs))
	trustedCAsByHost := make(map[string]map[string]struct{}, len(canonicalHostIDs))
	for i, trust := range contract.TrustInventory {
		if i > 0 && contract.TrustInventory[i-1] == trust {
			return ErrInvalidTransportContract
		}
		if _, ok := caIDs[trust.CAID]; !ok {
			return ErrInvalidTransportContract
		}
		trustHosts[trust.HostID] = true
		if trustedCAsByHost[trust.HostID] == nil {
			trustedCAsByHost[trust.HostID] = make(map[string]struct{})
		}
		trustedCAsByHost[trust.HostID][trust.CAID] = struct{}{}
	}
	leaves := make(map[string]struct{}, len(contract.LeafInventory))
	leafHosts := make(map[string]bool, len(canonicalHostIDs))
	for i, leaf := range contract.LeafInventory {
		if i > 0 && contract.LeafInventory[i-1] == leaf {
			return ErrInvalidTransportContract
		}
		if _, ok := caIDs[leaf.CAID]; !ok {
			return ErrInvalidTransportContract
		}
		if _, trusted := trustedCAsByHost[leaf.HostID][leaf.CAID]; !trusted {
			return ErrInvalidTransportContract
		}
		leaves[leafPeerBindingKey(leaf.HostID, leaf.URISAN, leaf.IPSAN,
			leaf.CertificateFingerprint, leaf.SPKIFingerprint)] = struct{}{}
		leafHosts[leaf.HostID] = true
	}
	activePeerHosts := make(map[string]bool, len(canonicalHostIDs))
	for i, peer := range contract.ActivePeerAllowlist {
		if i > 0 && contract.ActivePeerAllowlist[i-1] == peer {
			return ErrInvalidTransportContract
		}
		if _, ok := leaves[leafPeerBindingKey(peer.HostID, peer.URISAN, peer.IPSAN,
			peer.CertificateFingerprint, peer.SPKIFingerprint)]; !ok {
			return ErrInvalidTransportContract
		}
		activePeerHosts[peer.HostID] = true
	}
	for _, hostID := range canonicalHostIDs {
		if !trustHosts[hostID] || !leafHosts[hostID] || !activePeerHosts[hostID] {
			return ErrInvalidTransportContract
		}
	}
	return nil
}

func leafSortKey(leaf transportLeaf) string {
	return strings.Join([]string{leaf.HostID, leaf.CAID, leaf.LeafRole, leaf.URISAN, leaf.IPSAN,
		leaf.CertificateFingerprint, leaf.SPKIFingerprint}, "\x00")
}

func peerSortKey(peer activePeer) string {
	return strings.Join([]string{peer.HostID, peer.URISAN, peer.IPSAN,
		peer.CertificateFingerprint, peer.SPKIFingerprint}, "\x00")
}

func leafPeerBindingKey(hostID, uriSAN, ipSAN, certificateFingerprint, spkiFingerprint string) string {
	return strings.Join([]string{hostID, uriSAN, ipSAN, certificateFingerprint, spkiFingerprint}, "\x00")
}
