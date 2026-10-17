package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	ReleaseTimePolicyVersion       = 1
	MaxAuthorizationLifetime       = time.Hour
	MaxAuthorityTimeProbeRoundTrip = 2 * time.Second
	MaxAuthorityTimeProbeSkew      = 5 * time.Second
	MaxRevocationLedgerLifetime    = 15 * time.Minute
	MaxRevocationLedgerMutationAge = 5 * time.Minute
	maxPendingTimeProbes           = 64
	AuthorityKeyPossessionDomain   = "remote-session-runner/release-authority-key-possession/v1\x00"
	HostKeyPossessionDomain        = "remote-session-runner/host-attestation-key-possession/v1\x00"
	AuthorityTimeProbeDomain       = "remote-session-runner/release-time-probe/v1\x00"
)

var ErrInvalidTrustState = errors.New("invalid release trust state")

type ReleaseKeyState struct {
	HostID     string
	Generation uint64
	RecordID   string
	KeyID      string
	PublicKey  ed25519.PublicKey
	NotBefore  time.Time
	NotAfter   time.Time
}

type ReleaseKeySetTransition struct {
	Previous  ReleaseKeyState
	Successor ReleaseKeyState
}

type RevocationLedgerState struct {
	Generation       uint64
	RecordID         string
	RevokedRecordIDs []string
	RevokedKeyIDs    []string
	IssuedAt         time.Time
	ExpiresAt        time.Time
}

type keySetPayload struct {
	HostID              string `json:"host_id,omitempty"`
	Generation          uint64 `json:"key_set_generation"`
	PredecessorRecordID string `json:"predecessor_record_id"`
	PreviousKeyID       string `json:"previous_key_id"`
	PreviousPublicKey   string `json:"previous_public_key"`
	SuccessorKeyID      string `json:"successor_key_id"`
	SuccessorPublicKey  string `json:"successor_public_key"`
	PreviousNotAfter    string `json:"previous_not_after"`
	SuccessorNotBefore  string `json:"successor_not_before"`
	SuccessorNotAfter   string `json:"successor_not_after"`
	SuccessorProof      string `json:"successor_proof"`
}

type revocationLedgerPayload struct {
	Generation          uint64   `json:"ledger_generation"`
	PredecessorRecordID string   `json:"predecessor_record_id"`
	IssuedAt            string   `json:"issued_at"`
	ExpiresAt           string   `json:"expires_at"`
	RevokedRecordIDs    []string `json:"revoked_record_ids"`
	RevokedKeyIDs       []string `json:"revoked_key_ids"`
}

type TimeProbeChallenge struct {
	ID    string `json:"challenge_id"`
	Nonce string `json:"nonce"`
}

type TimeProbeBody struct {
	Version                int    `json:"time_probe_version"`
	Nonce                  string `json:"nonce"`
	AuthorityUTCUnixMillis int64  `json:"authority_utc_unix_millis"`
	SignerKeyID            string `json:"signer_key_id"`
}

type signedTimeProbeResponse struct {
	TimeProbeBody
	Signature string `json:"signature"`
}

type pendingTimeProbe struct {
	challenge TimeProbeChallenge
	started   time.Time
}

// TimeProbeManager models the helper-owned, single-use challenge state. A
// protected anchor persists this state in its later durable implementation.
type TimeProbeManager struct {
	mu                   sync.Mutex
	pending              map[string]pendingTimeProbe
	highestAuthorityTime time.Time
	now                  func() time.Time
}

func NewTimeProbeManager() *TimeProbeManager {
	return newTimeProbeManager(time.Now)
}

func newTimeProbeManager(now func() time.Time) *TimeProbeManager {
	if now == nil {
		now = time.Now
	}
	return &TimeProbeManager{pending: make(map[string]pendingTimeProbe), now: now}
}

// Begin creates and retains a fresh 32-byte base64url nonce and local wall
// start time. The caller returns only this challenge through the fixed
// authenticated release transport.
func (m *TimeProbeManager) Begin() (TimeProbeChallenge, error) {
	if m == nil || m.now == nil {
		return TimeProbeChallenge{}, ErrInvalidTrustState
	}
	started := m.now()
	if started.IsZero() {
		return TimeProbeChallenge{}, ErrInvalidTrustState
	}
	var idBytes [16]byte
	var nonceBytes [32]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return TimeProbeChallenge{}, err
	}
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return TimeProbeChallenge{}, err
	}
	challenge := TimeProbeChallenge{ID: hex.EncodeToString(idBytes[:]), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes[:])}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		m.pending = make(map[string]pendingTimeProbe)
	}
	for id, pending := range m.pending {
		if started.Sub(pending.started) > MaxAuthorityTimeProbeRoundTrip {
			delete(m.pending, id)
		}
	}
	if len(m.pending) >= maxPendingTimeProbes {
		return TimeProbeChallenge{}, invalidL3Trust("too many outstanding time challenges")
	}
	if _, exists := m.pending[challenge.ID]; exists {
		return TimeProbeChallenge{}, ErrInvalidTrustState
	}
	m.pending[challenge.ID] = pendingTimeProbe{challenge: challenge, started: started}
	return challenge, nil
}

// Accept consumes the pending challenge before verifying the response, so an
// invalid response cannot be replayed against the same challenge.
func (m *TimeProbeManager) Accept(challengeID string, responseRaw []byte, trusted map[string]TrustedKey) (time.Time, error) {
	if m == nil || m.now == nil {
		return time.Time{}, ErrInvalidTrustState
	}
	m.mu.Lock()
	pending, ok := m.pending[challengeID]
	if ok {
		delete(m.pending, challengeID)
	}
	highest := m.highestAuthorityTime
	m.mu.Unlock()
	if !ok || pending.challenge.ID != challengeID {
		return time.Time{}, invalidL3Trust("missing, replayed, or mismatched time challenge")
	}
	finished := m.now()
	if finished.IsZero() {
		return time.Time{}, invalidL3Trust("missing local time at response acceptance")
	}
	monotonicElapsed := finished.Sub(pending.started)
	accepted, err := verifyTimeProbeResponse(responseRaw, pending.challenge, pending.started, finished, monotonicElapsed, trusted, highest)
	if err != nil {
		return time.Time{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.highestAuthorityTime.IsZero() && (accepted.Before(m.highestAuthorityTime) || finished.Round(0).UTC().Before(m.highestAuthorityTime.Add(-time.Second))) {
		return time.Time{}, invalidL3Trust("authority time or local clock rollback")
	}
	m.highestAuthorityTime = accepted.UTC()
	return accepted.UTC(), nil
}

// SignTimeProbeResponse creates the authority's canonical nonce-bound answer.
func SignTimeProbeResponse(body TimeProbeBody, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize || body.Version != ReleaseTimePolicyVersion || !validNonce(body.Nonce) ||
		!validKeyID(body.SignerKeyID) || body.AuthorityUTCUnixMillis <= 0 {
		return nil, ErrInvalidTrustState
	}
	bodyBytes, err := canonicalProbeBody(body)
	if err != nil {
		return nil, ErrInvalidTrustState
	}
	message := append([]byte(AuthorityTimeProbeDomain), bodyBytes...)
	response := signedTimeProbeResponse{TimeProbeBody: body, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, ErrInvalidTrustState
	}
	return CanonicalizeJSON(encoded)
}

// VerifyTimeProbeResponse checks canonical bytes, trusted authority identity,
// single-challenge nonce, the signed UTC value, elapsed limit, skew bound, and
// the persisted authority-time high-water mark.
func verifyTimeProbeResponse(raw []byte, challenge TimeProbeChallenge, wallT0, wallT1 time.Time, monotonicElapsed time.Duration, trusted map[string]TrustedKey, highestAuthorityTime time.Time) (time.Time, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return time.Time{}, invalidL3Trust("noncanonical time response")
	}
	var response signedTimeProbeResponse
	if err := decodeStrict(raw, &response); err != nil || response.Version != ReleaseTimePolicyVersion || !validChallengeID(challenge.ID) || !validNonce(challenge.Nonce) ||
		response.Nonce != challenge.Nonce || !validKeyID(response.SignerKeyID) || response.AuthorityUTCUnixMillis <= 0 ||
		monotonicElapsed < 0 || monotonicElapsed > MaxAuthorityTimeProbeRoundTrip || wallT1.IsZero() || wallT0.IsZero() {
		return time.Time{}, invalidL3Trust("time response does not match its fresh challenge")
	}
	key, ok := trusted[response.SignerKeyID]
	if !ok || key.KeyID != response.SignerKeyID || key.Role != SignerRoleReleaseAuthority || key.HostID != "" || len(key.PublicKey) != ed25519.PublicKeySize {
		return time.Time{}, invalidL3Trust("time response signer is not a trusted release authority")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(response.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != response.Signature {
		return time.Time{}, invalidL3Trust("time response signature encoding")
	}
	body := response.TimeProbeBody
	bodyBytes, err := canonicalProbeBody(body)
	if err != nil || !ed25519.Verify(key.PublicKey, append([]byte(AuthorityTimeProbeDomain), bodyBytes...), signature) {
		return time.Time{}, invalidL3Trust("time response signature verification")
	}
	challengeNonce, errA := base64.RawURLEncoding.Strict().DecodeString(challenge.Nonce)
	responseNonce, errB := base64.RawURLEncoding.Strict().DecodeString(response.Nonce)
	if errA != nil || errB != nil || len(challengeNonce) != 32 || len(responseNonce) != 32 || subtle.ConstantTimeCompare(challengeNonce, responseNonce) != 1 {
		return time.Time{}, invalidL3Trust("time response nonce is malformed or mismatched")
	}
	authorityTime := time.UnixMilli(response.AuthorityUTCUnixMillis).UTC()
	wallStart := wallT0.Round(0).UTC()
	wallFinish := wallT1.Round(0).UTC()
	wallElapsed := wallFinish.Sub(wallStart)
	if wallElapsed < -time.Second {
		return time.Time{}, invalidL3Trust("local wall clock moved backwards during time probe")
	}
	midpoint := wallStart.Add(wallElapsed / 2)
	offset := authorityTime.Sub(midpoint)
	uncertainty := monotonicElapsed / 2
	absoluteOffset := absDuration(offset)
	if absoluteOffset > MaxAuthorityTimeProbeSkew || uncertainty > MaxAuthorityTimeProbeSkew-absoluteOffset {
		return time.Time{}, invalidL3Trust("authority time skew exceeds five-second bound")
	}
	if !highestAuthorityTime.IsZero() && (authorityTime.Before(highestAuthorityTime) || wallFinish.Before(highestAuthorityTime.Add(-time.Second))) {
		return time.Time{}, invalidL3Trust("authority time or local clock moved backwards")
	}
	return authorityTime, nil
}

// SignSuccessorProof signs the JCS key-set body with the successor's private
// Ed25519 key. The proof is carried only as a signature, never as key material.
func SignSuccessorProof(recordType string, payloadWithoutProof []byte, successorPrivateKey ed25519.PrivateKey) (string, error) {
	domain, ok := keyPossessionDomain(recordType)
	if !ok || len(successorPrivateKey) != ed25519.PrivateKeySize {
		return "", ErrInvalidTrustState
	}
	message, err := successorProofMessage(domain, payloadWithoutProof)
	if err != nil {
		return "", ErrInvalidTrustState
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(successorPrivateKey, message)), nil
}

// ValidateAuthorityKeySet verifies the predecessor's envelope signature,
// contiguous generation and exact predecessor/key binding, overlap, and the
// successor's proof of possession. The returned state is safe to persist only
// after the caller atomically commits its anchor update.
func ValidateAuthorityKeySet(envelopeRaw []byte, previous ReleaseKeyState, trusted map[string]TrustedKey, now time.Time) (ReleaseKeySetTransition, error) {
	envelope, err := VerifyEnvelope(envelopeRaw, trusted)
	if err != nil {
		return ReleaseKeySetTransition{}, err
	}
	if envelope.RecordType != "authority_key_set" {
		return ReleaseKeySetTransition{}, invalidL3Trust("wrong authority key-set record type")
	}
	if err := ValidatePayload(envelope.RecordType, envelope.Payload); err != nil {
		return ReleaseKeySetTransition{}, err
	}
	var payload keySetPayload
	if decodeStrict(envelope.Payload, &payload) != nil {
		return ReleaseKeySetTransition{}, invalidL3Trust("decode authority key set")
	}
	previousTrusted, ok := trusted[previous.KeyID]
	if !ok || previous.HostID != "" || previousTrusted.KeyID != previous.KeyID ||
		(previousTrusted.Role != SignerRoleReleaseAuthority && previousTrusted.Role != SignerRolePreviousAuthorityKey) ||
		!bytes.Equal(previousTrusted.PublicKey, previous.PublicKey) {
		return ReleaseKeySetTransition{}, invalidL3Trust("authority key set is not signed by the exact accepted predecessor key")
	}
	return validateKeySet(envelope, payload, previous, "", now)
}

// ValidateHostAttestationKeySet verifies the authority envelope, the canonical
// host and its explicit bootstrap/predecessor key mapping, then the successor
// proof and validity overlap.
func ValidateHostAttestationKeySet(envelopeRaw []byte, previous ReleaseKeyState, trustedAuthorities map[string]TrustedKey, now time.Time) (ReleaseKeySetTransition, error) {
	envelope, err := VerifyEnvelope(envelopeRaw, trustedAuthorities)
	if err != nil {
		return ReleaseKeySetTransition{}, err
	}
	if envelope.RecordType != "host_attestation_key_set" {
		return ReleaseKeySetTransition{}, invalidL3Trust("wrong host key-set record type")
	}
	if err := ValidatePayload(envelope.RecordType, envelope.Payload); err != nil {
		return ReleaseKeySetTransition{}, err
	}
	var payload keySetPayload
	if decodeStrict(envelope.Payload, &payload) != nil || !isCanonicalHostID(payload.HostID) || payload.HostID != previous.HostID {
		return ReleaseKeySetTransition{}, invalidL3Trust("host key-set bootstrap mapping mismatch")
	}
	return validateKeySet(envelope, payload, previous, payload.HostID, now)
}

func validateKeySet(envelope Envelope, payload keySetPayload, previous ReleaseKeyState, expectedHost string, now time.Time) (ReleaseKeySetTransition, error) {
	if !validKeyState(previous, now) || previous.Generation == math.MaxUint64 || payload.Generation != previous.Generation+1 || payload.PredecessorRecordID != previous.RecordID ||
		payload.PreviousKeyID != previous.KeyID || (expectedHost == "" && envelope.SignerKeyID != previous.KeyID) || payload.SuccessorKeyID == previous.KeyID ||
		(expectedHost == "" && payload.HostID != "") || (expectedHost != "" && payload.HostID != expectedHost) {
		return ReleaseKeySetTransition{}, invalidL3Trust("key-set generation, predecessor, signer, or host mismatch")
	}
	previousPublic, errPrev := decodePublicKey(payload.PreviousPublicKey)
	successorPublic, errNext := decodePublicKey(payload.SuccessorPublicKey)
	if errPrev != nil || errNext != nil || !bytes.Equal(previousPublic, previous.PublicKey) || bytes.Equal(previousPublic, successorPublic) {
		return ReleaseKeySetTransition{}, invalidL3Trust("key-set public-key mapping mismatch")
	}
	previousRetirement, errRetirement := time.Parse(time.RFC3339Nano, payload.PreviousNotAfter)
	successorActivation, errActivation := time.Parse(time.RFC3339Nano, payload.SuccessorNotBefore)
	successorExpiry, errExpiry := time.Parse(time.RFC3339Nano, payload.SuccessorNotAfter)
	if errRetirement != nil || errActivation != nil || errExpiry != nil || !validUTCTimestamp(payload.PreviousNotAfter) ||
		!validUTCTimestamp(payload.SuccessorNotBefore) || !validUTCTimestamp(payload.SuccessorNotAfter) ||
		!previousRetirement.After(now.UTC()) || previousRetirement.After(previous.NotAfter) || !successorExpiry.After(successorActivation) ||
		!successorExpiry.After(now.UTC()) || !successorActivation.Before(previousRetirement) || !previous.NotBefore.Before(successorExpiry) {
		return ReleaseKeySetTransition{}, invalidL3Trust("key-set validity intervals do not overlap")
	}
	if err := verifySuccessorProof(envelope.RecordType, envelope.Payload, payload.SuccessorProof, successorPublic); err != nil {
		return ReleaseKeySetTransition{}, err
	}
	retiredPrevious := previous
	retiredPrevious.NotAfter = previousRetirement.UTC()
	successor := ReleaseKeyState{HostID: expectedHost, Generation: payload.Generation, RecordID: envelope.RecordID, KeyID: payload.SuccessorKeyID,
		PublicKey: append(ed25519.PublicKey(nil), successorPublic...), NotBefore: successorActivation.UTC(), NotAfter: successorExpiry.UTC()}
	return ReleaseKeySetTransition{Previous: retiredPrevious, Successor: successor}, nil
}

// ValidateRevocationLedger validates an unexpired signed successor, exact
// predecessor and generation, cumulative monotonic revocations, and rejects a
// ledger that revokes its own envelope or signing key.
func ValidateRevocationLedger(envelopeRaw []byte, previous RevocationLedgerState, trustedAuthorities map[string]TrustedKey, now time.Time) (RevocationLedgerState, error) {
	envelope, err := VerifyEnvelope(envelopeRaw, trustedAuthorities)
	if err != nil {
		return RevocationLedgerState{}, err
	}
	if envelope.RecordType != "release_revocation_ledger" {
		return RevocationLedgerState{}, invalidL3Trust("wrong revocation record type")
	}
	if err := ValidatePayload(envelope.RecordType, envelope.Payload); err != nil {
		return RevocationLedgerState{}, err
	}
	var payload revocationLedgerPayload
	if decodeStrict(envelope.Payload, &payload) != nil {
		return RevocationLedgerState{}, invalidL3Trust("decode revocation ledger")
	}
	issued, errIssued := time.Parse(time.RFC3339Nano, payload.IssuedAt)
	expires, errExpires := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	now = now.UTC()
	if !validRevocationLedgerState(previous) || previous.Generation == math.MaxUint64 || errIssued != nil || errExpires != nil || !validUTCTimestamp(payload.IssuedAt) || !validUTCTimestamp(payload.ExpiresAt) ||
		payload.Generation != previous.Generation+1 || payload.PredecessorRecordID != previous.RecordID || !issued.Before(expires) ||
		expires.Sub(issued) > MaxRevocationLedgerLifetime || issued.After(now) || !now.Before(expires) ||
		(previous.Generation > 0 && !issued.After(previous.IssuedAt)) || !sortedUnique(payload.RevokedRecordIDs) ||
		!sortedUnique(payload.RevokedKeyIDs) || !isLowerHexDigest(envelope.RecordID) {
		return RevocationLedgerState{}, invalidL3Trust("ledger generation, time, or canonical ordering")
	}
	for _, id := range previous.RevokedRecordIDs {
		if !slices.Contains(payload.RevokedRecordIDs, id) {
			return RevocationLedgerState{}, invalidL3Trust("ledger attempted to un-revoke a record")
		}
	}
	for _, id := range previous.RevokedKeyIDs {
		if !slices.Contains(payload.RevokedKeyIDs, id) {
			return RevocationLedgerState{}, invalidL3Trust("ledger attempted to un-revoke a key")
		}
	}
	if slices.Contains(payload.RevokedRecordIDs, envelope.RecordID) || slices.Contains(payload.RevokedKeyIDs, envelope.SignerKeyID) ||
		slices.Contains(previous.RevokedKeyIDs, envelope.SignerKeyID) {
		return RevocationLedgerState{}, invalidL3Trust("ledger revokes its own record or signer")
	}
	return RevocationLedgerState{Generation: payload.Generation, RecordID: envelope.RecordID,
		RevokedRecordIDs: append([]string(nil), payload.RevokedRecordIDs...), RevokedKeyIDs: append([]string(nil), payload.RevokedKeyIDs...), IssuedAt: issued.UTC(), ExpiresAt: expires.UTC()}, nil
}

// ValidateRevocationLedgerFreshForMutation refuses a cached ledger older than
// five minutes while an installed lane can accept mutation, even when its
// signed expiry is later. The anchor persistence layer applies this to its
// accepted state before opening mutation gates.
func ValidateRevocationLedgerFreshForMutation(state RevocationLedgerState, now time.Time) error {
	if !validRevocationLedgerState(state) || state.Generation == 0 || state.IssuedAt.IsZero() ||
		now.Before(state.IssuedAt) || now.Sub(state.IssuedAt) > MaxRevocationLedgerMutationAge || !now.Before(state.ExpiresAt) {
		return invalidL3Trust("revocation ledger is stale or expired for mutation")
	}
	return nil
}

// ValidateReleaseTimeInterval enforces V1's closed time policy for signed
// authorizations and ingress directives.
func ValidateReleaseTimeInterval(start, end string, now time.Time) error {
	if ReleaseTimePolicyVersion != 1 || !validMaximumReleaseInterval(start, end) {
		return invalidL3Trust("invalid release time interval")
	}
	startTime, _ := time.Parse(time.RFC3339Nano, start)
	endTime, _ := time.Parse(time.RFC3339Nano, end)
	if now.IsZero() || now.Before(startTime) || !now.Before(endTime) {
		return invalidL3Trust("release authorization or directive is outside its time policy")
	}
	return nil
}

func validKeyState(value ReleaseKeyState, now time.Time) bool {
	return validKeyID(value.KeyID) && len(value.PublicKey) == ed25519.PublicKeySize && isLowerHexDigest(value.RecordID) &&
		!value.NotBefore.IsZero() && value.NotBefore.Equal(value.NotBefore.UTC()) &&
		!value.NotAfter.IsZero() && value.NotAfter.Equal(value.NotAfter.UTC()) && value.NotAfter.After(value.NotBefore) &&
		!now.Before(value.NotBefore) && now.Before(value.NotAfter)
}

func validRevocationLedgerState(value RevocationLedgerState) bool {
	if !isLowerHexDigest(value.RecordID) || !sortedUnique(value.RevokedRecordIDs) || !sortedUnique(value.RevokedKeyIDs) {
		return false
	}
	for _, recordID := range value.RevokedRecordIDs {
		if !isLowerHexDigest(recordID) {
			return false
		}
	}
	for _, keyID := range value.RevokedKeyIDs {
		if !validKeyID(keyID) {
			return false
		}
	}
	if value.Generation == 0 {
		return value.IssuedAt.IsZero() && value.ExpiresAt.IsZero()
	}
	return !value.IssuedAt.IsZero() && value.IssuedAt.Equal(value.IssuedAt.UTC()) &&
		!value.ExpiresAt.IsZero() && value.ExpiresAt.Equal(value.ExpiresAt.UTC()) && value.ExpiresAt.After(value.IssuedAt)
}

func decodePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, ErrInvalidTrustState
	}
	return ed25519.PublicKey(decoded), nil
}

func verifySuccessorProof(recordType string, payload []byte, proof string, publicKey ed25519.PublicKey) error {
	domain, ok := keyPossessionDomain(recordType)
	if !ok {
		return invalidL3Trust("unsupported key possession proof type")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(proof)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != proof {
		return invalidL3Trust("key possession proof encoding")
	}
	message, err := successorProofMessage(domain, payload)
	if err != nil || !ed25519.Verify(publicKey, message, signature) {
		return invalidL3Trust("successor key possession proof failed")
	}
	return nil
}

func successorProofMessage(domain string, payload []byte) ([]byte, error) {
	value, err := decodeSchemaValue(payload)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalidTrustState
	}
	delete(object, "successor_proof")
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	canonical, err := CanonicalizeJSON(encoded)
	if err != nil {
		return nil, err
	}
	return append([]byte(domain), canonical...), nil
}

func keyPossessionDomain(recordType string) (string, bool) {
	switch recordType {
	case "authority_key_set":
		return AuthorityKeyPossessionDomain, true
	case "host_attestation_key_set":
		return HostKeyPossessionDomain, true
	default:
		return "", false
	}
}

func canonicalProbeBody(body TimeProbeBody) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(encoded)
}

func validNonce(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validKeyID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !isASCIIAlphaNum(value[0]) {
		return false
	}
	for _, r := range value[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func isASCIIAlphaNum(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func absDuration(value time.Duration) time.Duration {
	if value == time.Duration(math.MinInt64) {
		return time.Duration(math.MaxInt64)
	}
	if value < 0 {
		return -value
	}
	return value
}

func validChallengeID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validMaximumReleaseInterval(start, end string) bool {
	if !orderedInterval(start, end) {
		return false
	}
	startTime, errStart := time.Parse(time.RFC3339Nano, start)
	endTime, errEnd := time.Parse(time.RFC3339Nano, end)
	return errStart == nil && errEnd == nil && endTime.Sub(startTime) <= MaxAuthorizationLifetime
}

func invalidL3Trust(message string) error { return fmt.Errorf("%w: %s", ErrInvalidPayload, message) }
