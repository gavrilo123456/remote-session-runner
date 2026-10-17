package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestLanesL304CompilesClosedTrustPayloadSchemas(t *testing.T) {
	for _, recordType := range []string{"authority_key_set", "host_attestation_key_set", "release_revocation_ledger"} {
		if err := compiledSchemaForL303(recordType); err != nil {
			t.Fatalf("compile %s schema: %v", recordType, err)
		}
	}
	unknown := []byte(`{"ledger_generation":1,"predecessor_record_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","issued_at":"2026-10-17T12:00:00Z","expires_at":"2026-10-17T12:05:00Z","revoked_record_ids":[],"revoked_key_ids":[],"unexpected":true}`)
	if err := ValidatePayload("release_revocation_ledger", unknown); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("open revocation payload error = %v, want ErrInvalidPayload", err)
	}
}

func TestLanesL304AuthorityKeySetRequiresContiguousExactPredecessorAndProof(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	oldID, oldPrivate := l304PrivateKey(1)
	newID, newPrivate := l304PrivateKey(2)
	wrongID, wrongPrivate := l304PrivateKey(3)
	previous := l304KeyState("", 0, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", oldID, oldPrivate, now.Add(-time.Hour), now.Add(2*time.Hour))
	trusted := map[string]TrustedKey{oldID: {KeyID: oldID, Role: SignerRoleReleaseAuthority, PublicKey: oldPrivate.Public().(ed25519.PublicKey)}}
	signer := SigningIdentity{KeyID: oldID, Role: SignerRolePreviousAuthorityKey, PrivateKey: oldPrivate}
	valid := l304KeySetEnvelope(t, "authority_key_set", "", 1, previous, newID, newPrivate.Public().(ed25519.PublicKey), newPrivate, now.Add(time.Hour), now.Add(-time.Minute), now.Add(time.Hour), signer)
	transition, err := ValidateAuthorityKeySet(valid, previous, trusted, now)
	if err != nil {
		t.Fatalf("valid authority key set rejected: %v", err)
	}
	if transition.Previous.NotAfter != now.Add(time.Hour) || transition.Successor.KeyID != newID || transition.Successor.Generation != 1 {
		t.Fatalf("rotation transition did not retain the signed overlap: %+v", transition)
	}
	competingID, competingPrivate := l304PrivateKey(13)
	competing := l304KeySetEnvelope(t, "authority_key_set", "", 1, previous, competingID, competingPrivate.Public().(ed25519.PublicKey), competingPrivate,
		now.Add(time.Hour), now.Add(-time.Minute), now.Add(time.Hour), signer)
	updatedTrust := map[string]TrustedKey{
		oldID: {KeyID: oldID, Role: SignerRoleReleaseAuthority, PublicKey: oldPrivate.Public().(ed25519.PublicKey)},
		newID: {KeyID: newID, Role: SignerRoleReleaseAuthority, PublicKey: newPrivate.Public().(ed25519.PublicKey)},
	}
	if _, err := ValidateAuthorityKeySet(competing, transition.Successor, updatedTrust, now); err == nil {
		t.Fatal("same-generation fork was accepted after the high-water key set advanced")
	}

	cases := []struct {
		name                           string
		payloadGeneration              uint64
		predecessor                    string
		proofKey                       ed25519.PrivateKey
		publicKey                      ed25519.PublicKey
		retirement, activation, expiry time.Time
		signer                         SigningIdentity
	}{
		{name: "skipped generation", payloadGeneration: 2, predecessor: previous.RecordID, proofKey: newPrivate, publicKey: newPrivate.Public().(ed25519.PublicKey), retirement: now.Add(time.Hour), activation: now.Add(-time.Minute), expiry: now.Add(time.Hour), signer: signer},
		{name: "forked predecessor", payloadGeneration: 1, predecessor: strings.Repeat("b", 64), proofKey: newPrivate, publicKey: newPrivate.Public().(ed25519.PublicKey), retirement: now.Add(time.Hour), activation: now.Add(-time.Minute), expiry: now.Add(time.Hour), signer: signer},
		{name: "wrong successor proof", payloadGeneration: 1, predecessor: previous.RecordID, proofKey: wrongPrivate, publicKey: newPrivate.Public().(ed25519.PublicKey), retirement: now.Add(time.Hour), activation: now.Add(-time.Minute), expiry: now.Add(time.Hour), signer: signer},
		{name: "no overlap", payloadGeneration: 1, predecessor: previous.RecordID, proofKey: newPrivate, publicKey: newPrivate.Public().(ed25519.PublicKey), retirement: now.Add(time.Hour), activation: now.Add(2 * time.Hour), expiry: now.Add(3 * time.Hour), signer: signer},
		{name: "expired successor", payloadGeneration: 1, predecessor: previous.RecordID, proofKey: newPrivate, publicKey: newPrivate.Public().(ed25519.PublicKey), retirement: now.Add(time.Hour), activation: now.Add(-2 * time.Hour), expiry: now.Add(-time.Minute), signer: signer},
		{name: "wrong envelope signature", payloadGeneration: 1, predecessor: previous.RecordID, proofKey: newPrivate, publicKey: newPrivate.Public().(ed25519.PublicKey), retirement: now.Add(time.Hour), activation: now.Add(-time.Minute), expiry: now.Add(time.Hour), signer: SigningIdentity{KeyID: oldID, Role: SignerRolePreviousAuthorityKey, PrivateKey: wrongPrivate}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := l304KeySetEnvelopeWith(t, "authority_key_set", "", tc.payloadGeneration, tc.predecessor, previous, newID, tc.publicKey, tc.proofKey, tc.retirement, tc.activation, tc.expiry, tc.signer)
			if _, err := ValidateAuthorityKeySet(bad, previous, trusted, now); !errors.Is(err, ErrInvalidPayload) && !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("unsafe authority rotation accepted or returned unrelated error: %v", err)
			}
		})
	}
	if _, err := ValidateAuthorityKeySet(valid, previous, map[string]TrustedKey{oldID: {KeyID: oldID, Role: SignerRoleReleaseAuthority, PublicKey: wrongPrivate.Public().(ed25519.PublicKey)}}, now); err == nil {
		t.Fatal("accepted authority key-set under a same-ID different bootstrap key")
	}
	if wrongID == oldID {
		t.Fatal("test key identities collided")
	}
}

func TestLanesL304HostKeySetBindsBootstrapMappingAndHost(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	authorityID, authorityPrivate := l304PrivateKey(4)
	bootstrapID, bootstrapPrivate := l304PrivateKey(5)
	successorID, successorPrivate := l304PrivateKey(6)
	wrongID, wrongPrivate := l304PrivateKey(7)
	previous := l304KeyState("linux-host", 0, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", bootstrapID, bootstrapPrivate, now.Add(-time.Hour), now.Add(2*time.Hour))
	trusted := map[string]TrustedKey{authorityID: {KeyID: authorityID, Role: SignerRoleReleaseAuthority, PublicKey: authorityPrivate.Public().(ed25519.PublicKey)}}
	signer := SigningIdentity{KeyID: authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: authorityPrivate}
	valid := l304KeySetEnvelope(t, "host_attestation_key_set", "linux-host", 1, previous, successorID, successorPrivate.Public().(ed25519.PublicKey), successorPrivate, now.Add(time.Hour), now.Add(-time.Minute), now.Add(time.Hour), signer)
	transition, err := ValidateHostAttestationKeySet(valid, previous, trusted, now)
	if err != nil || transition.Successor.HostID != "linux-host" || transition.Successor.KeyID != successorID {
		t.Fatalf("valid bootstrap-bound host rotation failed: transition=%+v err=%v", transition, err)
	}

	cases := []struct {
		name                   string
		host                   string
		generation             uint64
		predecessor            string
		previousPublic         ed25519.PublicKey
		proofPrivate           ed25519.PrivateKey
		activation, retirement time.Time
	}{
		{name: "wrong host", host: "factory-sandbox", generation: 1, predecessor: previous.RecordID, previousPublic: bootstrapPrivate.Public().(ed25519.PublicKey), proofPrivate: successorPrivate, activation: now.Add(-time.Minute), retirement: now.Add(time.Hour)},
		{name: "wrong bootstrap key", host: "linux-host", generation: 1, predecessor: previous.RecordID, previousPublic: wrongPrivate.Public().(ed25519.PublicKey), proofPrivate: successorPrivate, activation: now.Add(-time.Minute), retirement: now.Add(time.Hour)},
		{name: "first set skipped", host: "linux-host", generation: 2, predecessor: previous.RecordID, previousPublic: bootstrapPrivate.Public().(ed25519.PublicKey), proofPrivate: successorPrivate, activation: now.Add(-time.Minute), retirement: now.Add(time.Hour)},
		{name: "proof from different key", host: "linux-host", generation: 1, predecessor: previous.RecordID, previousPublic: bootstrapPrivate.Public().(ed25519.PublicKey), proofPrivate: wrongPrivate, activation: now.Add(-time.Minute), retirement: now.Add(time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := l304KeySetEnvelopeWith(t, "host_attestation_key_set", tc.host, tc.generation, tc.predecessor, previous, successorID,
				successorPrivate.Public().(ed25519.PublicKey), tc.proofPrivate, tc.retirement, tc.activation, now.Add(time.Hour), signer)
			if tc.previousPublic != nil && !bytes.Equal(tc.previousPublic, bootstrapPrivate.Public().(ed25519.PublicKey)) {
				var envelope Envelope
				if err := json.Unmarshal(bad, &envelope); err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				payload["previous_public_key"] = base64.StdEncoding.EncodeToString(tc.previousPublic)
				bad, err = SignEnvelope("host_attestation_key_set", l302JSON(t, payload), signer)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ValidateHostAttestationKeySet(bad, previous, trusted, now); err == nil {
				t.Fatal("unsafe host-key transition accepted")
			}
		})
	}
	if wrongID == bootstrapID {
		t.Fatal("test key identities collided")
	}
}

func TestLanesL304RevocationLedgerIsContiguousCumulativeAndFresh(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	authorityID, authorityPrivate := l304PrivateKey(8)
	trusted := map[string]TrustedKey{authorityID: {KeyID: authorityID, Role: SignerRoleReleaseAuthority, PublicKey: authorityPrivate.Public().(ed25519.PublicKey)}}
	anchor := RevocationLedgerState{RecordID: strings.Repeat("a", 64)}
	first := l304LedgerEnvelope(t, 1, anchor.RecordID, now.Add(-time.Minute), now.Add(14*time.Minute), []string{strings.Repeat("b", 64)}, []string{"host-key-a"}, authorityID, authorityPrivate)
	state, err := ValidateRevocationLedger(first, anchor, trusted, now)
	if err != nil {
		t.Fatalf("valid first ledger rejected: %v", err)
	}
	if err := ValidateRevocationLedgerFreshForMutation(state, state.IssuedAt.Add(4*time.Minute+59*time.Second)); err != nil {
		t.Fatalf("fresh ledger refused mutation: %v", err)
	}
	if err := ValidateRevocationLedgerFreshForMutation(state, state.IssuedAt.Add(5*time.Minute+time.Second)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("stale ledger accepted for mutation: %v", err)
	}

	second := l304LedgerEnvelope(t, 2, state.RecordID, now.Add(time.Minute), now.Add(14*time.Minute),
		[]string{strings.Repeat("b", 64), strings.Repeat("c", 64)}, []string{"host-key-a", "host-key-b"}, authorityID, authorityPrivate)
	state2, err := ValidateRevocationLedger(second, state, trusted, now.Add(2*time.Minute))
	if err != nil || state2.Generation != 2 {
		t.Fatalf("valid cumulative ledger rejected: state=%+v err=%v", state2, err)
	}
	if _, err := ValidateRevocationLedger(second, state2, trusted, now.Add(2*time.Minute)); err == nil {
		t.Fatal("same ledger generation was replayed after the ledger high-water advanced")
	}

	cases := []struct {
		name            string
		generation      uint64
		predecessor     string
		issued, expires time.Time
		records, keys   []string
		signerID        string
		signerPrivate   ed25519.PrivateKey
	}{
		{name: "skipped generation", generation: 3, predecessor: state.RecordID, issued: now, expires: now.Add(time.Minute), records: state.RevokedRecordIDs, keys: state.RevokedKeyIDs, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "forked predecessor", generation: 2, predecessor: strings.Repeat("d", 64), issued: now, expires: now.Add(time.Minute), records: state.RevokedRecordIDs, keys: state.RevokedKeyIDs, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "un-revocation", generation: 2, predecessor: state.RecordID, issued: now, expires: now.Add(time.Minute), records: nil, keys: nil, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "unsorted cumulative set", generation: 2, predecessor: state.RecordID, issued: now, expires: now.Add(time.Minute), records: []string{strings.Repeat("c", 64), strings.Repeat("b", 64)}, keys: []string{"host-key-a"}, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "self-revoking signer", generation: 2, predecessor: state.RecordID, issued: now, expires: now.Add(time.Minute), records: state.RevokedRecordIDs, keys: []string{authorityID, "host-key-a"}, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "expired", generation: 2, predecessor: state.RecordID, issued: now.Add(-20 * time.Minute), expires: now.Add(-5 * time.Minute), records: state.RevokedRecordIDs, keys: state.RevokedKeyIDs, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "lifetime over 900 seconds", generation: 2, predecessor: state.RecordID, issued: now, expires: now.Add(15*time.Minute + time.Second), records: state.RevokedRecordIDs, keys: state.RevokedKeyIDs, signerID: authorityID, signerPrivate: authorityPrivate},
		{name: "bad envelope signer", generation: 2, predecessor: state.RecordID, issued: now, expires: now.Add(time.Minute), records: state.RevokedRecordIDs, keys: state.RevokedKeyIDs, signerID: authorityID, signerPrivate: l304OnlyPrivate(9)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := l304LedgerEnvelope(t, tc.generation, tc.predecessor, tc.issued, tc.expires, tc.records, tc.keys, tc.signerID, tc.signerPrivate)
			if _, err := ValidateRevocationLedger(bad, state, trusted, now); err == nil {
				t.Fatal("invalid revocation-ledger transition accepted")
			}
		})
	}
}

func TestLanesL304TimeProbeOwnsNonceTimingAndConsumesOnce(t *testing.T) {
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	clock := start
	manager := newTimeProbeManager(func() time.Time { return clock })
	authorityID, authorityPrivate := l304PrivateKey(10)
	trusted := map[string]TrustedKey{authorityID: {KeyID: authorityID, Role: SignerRoleReleaseAuthority, PublicKey: authorityPrivate.Public().(ed25519.PublicKey)}}
	challenge, err := manager.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Accept(strings.Repeat("0", 32), nil, trusted); err == nil {
		t.Fatal("time proof without a registered helper challenge was accepted")
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(challenge.Nonce)
	if err != nil || len(nonce) != 32 || !validChallengeID(challenge.ID) {
		t.Fatalf("invalid helper challenge: %+v decode=%v", challenge, err)
	}
	response, err := SignTimeProbeResponse(TimeProbeBody{Version: 1, Nonce: challenge.Nonce, AuthorityUTCUnixMillis: start.Add(time.Second).UnixMilli(), SignerKeyID: authorityID}, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(2 * time.Second)
	accepted, err := manager.Accept(challenge.ID, response, trusted)
	if err != nil || !accepted.Equal(start.Add(time.Second)) {
		t.Fatalf("valid time response rejected: accepted=%s err=%v", accepted, err)
	}
	if _, err := manager.Accept(challenge.ID, response, trusted); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("replayed time response was accepted: %v", err)
	}
}

func TestLanesL304TimeProbeRejectsMismatchBadSignatureDelayAndSkew(t *testing.T) {
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	authorityID, authorityPrivate := l304PrivateKey(11)
	trusted := map[string]TrustedKey{authorityID: {KeyID: authorityID, Role: SignerRoleReleaseAuthority, PublicKey: authorityPrivate.Public().(ed25519.PublicKey)}}
	newManager := func() (*TimeProbeManager, *time.Time, TimeProbeChallenge) {
		clock := start
		manager := newTimeProbeManager(func() time.Time { return clock })
		challenge, err := manager.Begin()
		if err != nil {
			t.Fatal(err)
		}
		return manager, &clock, challenge
	}
	makeResponse := func(t *testing.T, challenge TimeProbeChallenge, nonce string, authorityTime time.Time) []byte {
		t.Helper()
		response, err := SignTimeProbeResponse(TimeProbeBody{Version: 1, Nonce: nonce, AuthorityUTCUnixMillis: authorityTime.UnixMilli(), SignerKeyID: authorityID}, authorityPrivate)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	manager, clock, challenge := newManager()
	*clock = start.Add(time.Second)
	wrongNonce := makeResponse(t, challenge, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), start.Add(time.Second))
	if _, err := manager.Accept(challenge.ID, wrongNonce, trusted); err == nil {
		t.Fatal("nonce mismatch accepted")
	}

	manager, clock, challenge = newManager()
	noncanonical := append(makeResponse(t, challenge, challenge.Nonce, start.Add(time.Second)), ' ')
	*clock = start.Add(time.Second)
	if _, err := manager.Accept(challenge.ID, noncanonical, trusted); err == nil {
		t.Fatal("noncanonical JCS time response accepted")
	}

	manager, clock, challenge = newManager()
	badSignature := makeResponse(t, challenge, challenge.Nonce, start.Add(time.Second))
	var parsed signedTimeProbeResponse
	if err := json.Unmarshal(badSignature, &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.Signature = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, ed25519.SignatureSize))
	canonicalBadSignature, err := CanonicalizeJSON(mustJSON(t, parsed))
	if err != nil {
		t.Fatal(err)
	}
	badSignature = canonicalBadSignature
	*clock = start.Add(time.Second)
	if _, err := manager.Accept(challenge.ID, badSignature, trusted); err == nil {
		t.Fatal("bad authority signature accepted")
	}

	manager, clock, challenge = newManager()
	delayed := makeResponse(t, challenge, challenge.Nonce, start.Add(1*time.Second+time.Millisecond))
	*clock = start.Add(2*time.Second + time.Nanosecond)
	if _, err := manager.Accept(challenge.ID, delayed, trusted); err == nil {
		t.Fatal("response beyond the two-second monotonic bound accepted")
	}

	manager, clock, challenge = newManager()
	skewed := makeResponse(t, challenge, challenge.Nonce, start.Add(7*time.Second+time.Millisecond))
	*clock = start.Add(2 * time.Second)
	if _, err := manager.Accept(challenge.ID, skewed, trusted); err == nil {
		t.Fatal("authority clock beyond the five-second uncertainty bound accepted")
	}

	manager, clock, challenge = newManager()
	edge := makeResponse(t, challenge, challenge.Nonce, start.Add(5*time.Second))
	*clock = start.Add(2 * time.Second)
	if _, err := manager.Accept(challenge.ID, edge, trusted); err != nil {
		t.Fatalf("response at the exact five-second uncertainty bound rejected: %v", err)
	}

	manager, clock, challenge = newManager()
	extreme, err := SignTimeProbeResponse(TimeProbeBody{Version: 1, Nonce: challenge.Nonce, AuthorityUTCUnixMillis: math.MaxInt64, SignerKeyID: authorityID}, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	*clock = start.Add(2 * time.Second)
	if _, err := manager.Accept(challenge.ID, extreme, trusted); err == nil {
		t.Fatal("extreme signed timestamp overflowed skew arithmetic and was accepted")
	}
}

func TestLanesL304TimeProbeRejectsLocalClockRollback(t *testing.T) {
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	clock := start
	manager := newTimeProbeManager(func() time.Time { return clock })
	authorityID, authorityPrivate := l304PrivateKey(12)
	trusted := map[string]TrustedKey{authorityID: {KeyID: authorityID, Role: SignerRoleReleaseAuthority, PublicKey: authorityPrivate.Public().(ed25519.PublicKey)}}
	first, err := manager.Begin()
	if err != nil {
		t.Fatal(err)
	}
	firstResponse, err := SignTimeProbeResponse(TimeProbeBody{Version: 1, Nonce: first.Nonce, AuthorityUTCUnixMillis: start.Add(time.Second).UnixMilli(), SignerKeyID: authorityID}, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(2 * time.Second)
	if _, err := manager.Accept(first.ID, firstResponse, trusted); err != nil {
		t.Fatal(err)
	}
	clock = start.Add(-3 * time.Second)
	second, err := manager.Begin()
	if err != nil {
		t.Fatal(err)
	}
	secondResponse, err := SignTimeProbeResponse(TimeProbeBody{Version: 1, Nonce: second.Nonce, AuthorityUTCUnixMillis: start.Add(time.Second).UnixMilli(), SignerKeyID: authorityID}, authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clock = start.Add(-2 * time.Second)
	if _, err := manager.Accept(second.ID, secondResponse, trusted); err == nil {
		t.Fatal("local clock rollback greater than one second accepted")
	}
}

func TestLanesL304AuthorizationAndDirectiveTimePolicyCapsLifetime(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	start := now.Add(-time.Minute)
	end := start.Add(MaxAuthorizationLifetime)
	if err := ValidateReleaseTimeInterval(start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), now); err != nil {
		t.Fatalf("exact 3600-second interval rejected: %v", err)
	}
	if err := ValidateReleaseTimeInterval(start.Format(time.RFC3339Nano), end.Add(time.Nanosecond).Format(time.RFC3339Nano), now); err == nil {
		t.Fatal("authorization lifetime above 3600 seconds accepted")
	}
	if err := ValidateReleaseTimeInterval(now.Add(time.Second).Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano), now); err == nil {
		t.Fatal("future authorization accepted as currently active")
	}
	if err := ValidateReleaseTimeInterval(now.Add(-time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now); err == nil {
		t.Fatal("expired authorization accepted as currently active")
	}

	auth := l303Authorization("linux-host", "dev", nil)
	auth.NotBefore = start.Format(time.RFC3339Nano)
	auth.NotAfter = end.Format(time.RFC3339Nano)
	if err := ValidatePromotionAuthorization(l302JSON(t, auth)); err != nil {
		t.Fatalf("authorization at exact lifetime bound rejected: %v", err)
	}
	auth.NotAfter = end.Add(time.Nanosecond).Format(time.RFC3339Nano)
	if err := ValidatePromotionAuthorization(l302JSON(t, auth)); err == nil {
		t.Fatal("promotion authorization above one-hour bound accepted")
	}
	directive := ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition, Lane: "dev", HostID: "linux-host",
		ConfigurationDigest: l303Digest, Role: "candidate_admission", TransitionEpoch: 1, AdmissionGeneration: 0, LeaseGeneration: 1,
		NotBefore: start.Format(time.RFC3339Nano), NotAfter: end.Add(time.Nanosecond).Format(time.RFC3339Nano), RequiredLedgerGeneration: 1}
	if err := ValidateIngressAdmissionDirective(l302JSON(t, directive)); err == nil {
		t.Fatal("ingress directive above one-hour bound accepted")
	}
}

func l304PrivateKey(seed byte) (string, ed25519.PrivateKey) {
	private := l304OnlyPrivate(seed)
	return fmt.Sprintf("test-key-%02x", seed), private
}

func l304OnlyPrivate(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

func l304KeyState(host string, generation uint64, recordID, keyID string, private ed25519.PrivateKey, notBefore, notAfter time.Time) ReleaseKeyState {
	return ReleaseKeyState{HostID: host, Generation: generation, RecordID: recordID, KeyID: keyID,
		PublicKey: append(ed25519.PublicKey(nil), private.Public().(ed25519.PublicKey)...), NotBefore: notBefore.UTC(), NotAfter: notAfter.UTC()}
}

func l304KeySetEnvelope(t *testing.T, recordType, host string, generation uint64, previous ReleaseKeyState, successorID string,
	successorPublic ed25519.PublicKey, successorPrivate ed25519.PrivateKey, retirement, activation, expiry time.Time, signer SigningIdentity) []byte {
	t.Helper()
	return l304KeySetEnvelopeWith(t, recordType, host, generation, previous.RecordID, previous, successorID, successorPublic, successorPrivate, retirement, activation, expiry, signer)
}

func l304KeySetEnvelopeWith(t *testing.T, recordType, host string, generation uint64, predecessor string, previous ReleaseKeyState,
	successorID string, successorPublic ed25519.PublicKey, proofPrivate ed25519.PrivateKey, retirement, activation, expiry time.Time, signer SigningIdentity) []byte {
	t.Helper()
	payload := map[string]any{
		"key_set_generation": generation, "predecessor_record_id": predecessor, "previous_key_id": previous.KeyID,
		"previous_public_key": base64.StdEncoding.EncodeToString(previous.PublicKey), "successor_key_id": successorID,
		"successor_public_key": base64.StdEncoding.EncodeToString(successorPublic), "previous_not_after": retirement.UTC().Format(time.RFC3339Nano),
		"successor_not_before": activation.UTC().Format(time.RFC3339Nano), "successor_not_after": expiry.UTC().Format(time.RFC3339Nano),
	}
	if host != "" {
		payload["host_id"] = host
	}
	withoutProof := l302JSON(t, payload)
	proof, err := SignSuccessorProof(recordType, withoutProof, proofPrivate)
	if err != nil {
		t.Fatal(err)
	}
	payload["successor_proof"] = proof
	raw, err := SignEnvelope(recordType, l302JSON(t, payload), signer)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func l304LedgerEnvelope(t *testing.T, generation uint64, predecessor string, issued, expires time.Time, records, keys []string, signerID string, private ed25519.PrivateKey) []byte {
	t.Helper()
	payload := revocationLedgerPayload{Generation: generation, PredecessorRecordID: predecessor, IssuedAt: issued.UTC().Format(time.RFC3339Nano),
		ExpiresAt: expires.UTC().Format(time.RFC3339Nano), RevokedRecordIDs: records, RevokedKeyIDs: keys}
	raw, err := SignEnvelope("release_revocation_ledger", l302JSON(t, payload), SigningIdentity{KeyID: signerID, Role: SignerRoleReleaseAuthority, PrivateKey: private})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
