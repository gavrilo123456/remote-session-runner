package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	SignatureAlgorithmEd25519 = "Ed25519"
	ReleaseRecordVersion      = 1
)

type SignerRole string

const (
	SignerRoleReleaseAuthority     SignerRole = "release_authority"
	SignerRoleHostAttestation      SignerRole = "host_attestation"
	SignerRolePreviousAuthorityKey SignerRole = "previous_authority_key"
)

var (
	ErrInvalidEnvelope    = errors.New("invalid release record envelope")
	ErrNonCanonicalRecord = errors.New("release record is not canonical JCS")
	ErrUnknownRecordType  = errors.New("unknown release record type")
	ErrSignerRoleMismatch = errors.New("release record signer role mismatch")
	ErrUntrustedSigner    = errors.New("untrusted release record signer")
	ErrRecordIDMismatch   = errors.New("release record ID mismatch")
	ErrInvalidSignature   = errors.New("invalid release record signature")
)

var recordSignerRoles = map[string]SignerRole{
	"host_build_attestation":           SignerRoleHostAttestation,
	"candidate_manifest":               SignerRoleReleaseAuthority,
	"promotion_authorization":          SignerRoleReleaseAuthority,
	"promotion_attestation":            SignerRoleHostAttestation,
	"lane_acceptance":                  SignerRoleReleaseAuthority,
	"ingress_admission_directive":      SignerRoleReleaseAuthority,
	"ingress_admission_attestation":    SignerRoleHostAttestation,
	"capacity_measurement_attestation": SignerRoleHostAttestation,
	"capacity_policy":                  SignerRoleReleaseAuthority,
	"pki_certificate_request":          SignerRoleHostAttestation,
	"host_management_manifest":         SignerRoleReleaseAuthority,
	"host_management_authorization":    SignerRoleReleaseAuthority,
	"host_management_attestation":      SignerRoleHostAttestation,
	"host_attestation_key_set":         SignerRoleReleaseAuthority,
	"release_revocation_ledger":        SignerRoleReleaseAuthority,
	"authority_key_set":                SignerRolePreviousAuthorityKey,
}

type Envelope struct {
	ReleaseRecordVersion int             `json:"release_record_version"`
	RecordType           string          `json:"record_type"`
	Payload              json.RawMessage `json:"payload"`
	SignatureAlgorithm   string          `json:"signature_algorithm"`
	SignerKeyID          string          `json:"signer_key_id"`
	RecordID             string          `json:"record_id"`
	Signature            string          `json:"signature"`
}

type SigningIdentity struct {
	KeyID      string
	Role       SignerRole
	HostID     string
	PrivateKey ed25519.PrivateKey
}

type TrustedKey struct {
	KeyID     string
	Role      SignerRole
	HostID    string
	PublicKey ed25519.PublicKey
}

type signedBody struct {
	ReleaseRecordVersion int             `json:"release_record_version"`
	RecordType           string          `json:"record_type"`
	Payload              json.RawMessage `json:"payload"`
	SignatureAlgorithm   string          `json:"signature_algorithm"`
	SignerKeyID          string          `json:"signer_key_id"`
}

// SignEnvelope creates a canonical V1 release record. Payload schemas are
// validated by the later schema layer; this layer requires one JSON object and
// binds the signing principal to the closed record-type role map.
func SignEnvelope(recordType string, payload []byte, signer SigningIdentity) ([]byte, error) {
	role, ok := SignerRoleForRecordType(recordType)
	if !ok {
		return nil, ErrUnknownRecordType
	}
	if signer.Role != role || signer.KeyID == "" || len(signer.PrivateKey) != ed25519.PrivateKeySize {
		return nil, ErrSignerRoleMismatch
	}
	canonicalPayload, err := CanonicalizeJSON(payload)
	if err != nil || !isJSONObject(canonicalPayload) {
		return nil, ErrInvalidEnvelope
	}
	if err := validateSignerBinding(recordType, canonicalPayload, signer.KeyID, signer.Role, signer.HostID); err != nil {
		return nil, err
	}
	body := signedBody{
		ReleaseRecordVersion: ReleaseRecordVersion,
		RecordType:           recordType,
		Payload:              canonicalPayload,
		SignatureAlgorithm:   SignatureAlgorithmEd25519,
		SignerKeyID:          signer.KeyID,
	}
	bodyBytes, err := canonicalSignedBody(body)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	signatureInput := signingInput(recordType, bodyBytes)
	digest := sha256.Sum256(signatureInput)
	unsigned := Envelope{
		ReleaseRecordVersion: ReleaseRecordVersion,
		RecordType:           recordType,
		Payload:              canonicalPayload,
		SignatureAlgorithm:   SignatureAlgorithmEd25519,
		SignerKeyID:          signer.KeyID,
		RecordID:             hex.EncodeToString(digest[:]),
		Signature:            base64.StdEncoding.EncodeToString(ed25519.Sign(signer.PrivateKey, signatureInput)),
	}
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	return CanonicalizeJSON(encoded)
}

// VerifyEnvelope rejects noncanonical or open envelopes before checking the
// type-specific trusted signer. A valid signature never overrides the closed
// record-type-to-role binding.
func VerifyEnvelope(raw []byte, trustedKeys map[string]TrustedKey) (Envelope, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	if string(canonical) != string(raw) {
		return Envelope{}, ErrNonCanonicalRecord
	}
	if !hasExactEnvelopeFields(canonical) {
		return Envelope{}, ErrInvalidEnvelope
	}
	var envelope Envelope
	decoder := json.NewDecoder(strings.NewReader(string(canonical)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	if envelope.ReleaseRecordVersion != ReleaseRecordVersion || envelope.SignatureAlgorithm != SignatureAlgorithmEd25519 ||
		envelope.SignerKeyID == "" || !isJSONObject(envelope.Payload) || !isLowerHexDigest(envelope.RecordID) {
		return Envelope{}, ErrInvalidEnvelope
	}
	role, ok := SignerRoleForRecordType(envelope.RecordType)
	if !ok {
		return Envelope{}, ErrUnknownRecordType
	}
	key, ok := trustedKeys[envelope.SignerKeyID]
	if !ok || key.KeyID != envelope.SignerKeyID || len(key.PublicKey) != ed25519.PublicKeySize {
		return Envelope{}, ErrUntrustedSigner
	}
	roleMatches := key.Role == role || (role == SignerRolePreviousAuthorityKey && key.Role == SignerRoleReleaseAuthority)
	if !roleMatches {
		return Envelope{}, ErrSignerRoleMismatch
	}
	if err := validateSignerBinding(envelope.RecordType, envelope.Payload, envelope.SignerKeyID, role, key.HostID); err != nil {
		return Envelope{}, err
	}
	body := signedBody{
		ReleaseRecordVersion: envelope.ReleaseRecordVersion,
		RecordType:           envelope.RecordType,
		Payload:              envelope.Payload,
		SignatureAlgorithm:   envelope.SignatureAlgorithm,
		SignerKeyID:          envelope.SignerKeyID,
	}
	bodyBytes, err := canonicalSignedBody(body)
	if err != nil {
		return Envelope{}, ErrInvalidEnvelope
	}
	signatureInput := signingInput(envelope.RecordType, bodyBytes)
	digest := sha256.Sum256(signatureInput)
	if envelope.RecordID != hex.EncodeToString(digest[:]) {
		return Envelope{}, ErrRecordIDMismatch
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != envelope.Signature {
		return Envelope{}, ErrInvalidSignature
	}
	if !ed25519.Verify(key.PublicKey, signatureInput, signature) {
		return Envelope{}, ErrInvalidSignature
	}
	envelope.Payload = append(json.RawMessage(nil), envelope.Payload...)
	return envelope, nil
}

// SignerRoleForRecordType returns the sole permitted signing role for a V1
// record type. Unknown types have no implicit default signer.
func SignerRoleForRecordType(recordType string) (SignerRole, bool) {
	role, ok := recordSignerRoles[recordType]
	return role, ok
}

func validateSignerBinding(recordType string, payload []byte, keyID string, role SignerRole, hostID string) error {
	if role == SignerRoleHostAttestation {
		if !isCanonicalHostID(hostID) {
			return ErrSignerRoleMismatch
		}
		var identity struct {
			HostID string `json:"host_id"`
		}
		if err := json.Unmarshal(payload, &identity); err != nil || identity.HostID != hostID {
			return ErrSignerRoleMismatch
		}
		return nil
	}
	if hostID != "" {
		return ErrSignerRoleMismatch
	}
	if role == SignerRolePreviousAuthorityKey {
		var identity struct {
			PreviousKeyID string `json:"previous_key_id"`
		}
		if err := json.Unmarshal(payload, &identity); err != nil || identity.PreviousKeyID != keyID {
			return ErrSignerRoleMismatch
		}
	}
	return nil
}

func canonicalSignedBody(body signedBody) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(encoded)
}

func signingInput(recordType string, canonicalBody []byte) []byte {
	input := make([]byte, 0, len(recordType)+len(canonicalBody)+len("remote-session-runner/release-record//v1")+1)
	input = append(input, "remote-session-runner/release-record/"...)
	input = append(input, recordType...)
	input = append(input, "/v1"...)
	input = append(input, 0)
	return append(input, canonicalBody...)
}

func hasExactEnvelopeFields(raw []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 7 {
		return false
	}
	for _, required := range []string{
		"release_record_version", "record_type", "payload", "signature_algorithm",
		"signer_key_id", "record_id", "signature",
	} {
		if _, ok := fields[required]; !ok {
			return false
		}
	}
	return true
}

func isJSONObject(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func isLowerHexDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func isCanonicalHostID(value string) bool {
	switch value {
	case "mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox":
		return true
	default:
		return false
	}
}

func (e Envelope) String() string {
	return fmt.Sprintf("%s:%s", e.RecordType, e.RecordID)
}
