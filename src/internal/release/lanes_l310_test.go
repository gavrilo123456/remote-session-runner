package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLanesL310ExactMandateSignsOnceAndReturnsDurableRetry(t *testing.T) {
	fixture := l310NewFixture(t)
	planID := fixture.stageCandidate(t)
	mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.authorizer.calls != 1 || !strings.Contains(fixture.authorizer.message, hexDigest(fixture.payload)) ||
		!strings.Contains(fixture.authorizer.message, "context sha256:") || !strings.Contains(fixture.authorizer.message, "ledger generation 1") {
		t.Fatalf("approval did not present the exact staged body/context: %+v", fixture.authorizer)
	}
	first, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyEnvelope(first, map[string]TrustedKey{fixture.keyID: {
		KeyID: fixture.keyID, Role: SignerRoleReleaseAuthority, PublicKey: fixture.privateKey.Public().(ed25519.PublicKey),
	}})
	if err != nil || verified.RecordType != "candidate_manifest" || string(verified.Payload) != string(fixture.payload) {
		t.Fatalf("signed envelope did not preserve the approved body: record=%+v err=%v", verified, err)
	}
	retry, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID)
	if err != nil || !bytes.Equal(first, retry) {
		t.Fatalf("retry did not return the durable identical result: err=%v", err)
	}
	state, err := fixture.journal.snapshot()
	if err != nil || len(state.Mandates) != 0 || len(state.Plans) != 0 || len(state.ConsumedMandates) != 1 ||
		state.AcceptedRecordIDs[verified.RecordID] != fixture.contextDigest || state.TransitionHeads[fixture.contextDigest] != verified.RecordID {
		t.Fatalf("approved authority record was not durably consumed and retained: state=%+v err=%v", state, err)
	}
	reopened, err := openAuthorityJournalAt(fixture.dir, uint32(os.Getuid()), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedSigner, err := NewAuthoritySigner(reopened, fixture.keys, fixture.expected, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	reopenedSigner.clock = func() time.Time { return fixture.now }
	afterRestart, err := reopenedSigner.Sign(context.Background(), fixture.peer, mandateID)
	if err != nil || !bytes.Equal(first, afterRestart) {
		t.Fatalf("restart did not preserve exact signed result: err=%v", err)
	}
	reopenedState, err := reopened.snapshot()
	if err != nil || reopenedState.AnchorKeyID != fixture.keyID || reopenedState.AnchorPublicKey != base64.StdEncoding.EncodeToString(fixture.privateKey.Public().(ed25519.PublicKey)) ||
		reopenedState.Generation < state.Generation {
		t.Fatalf("reopen did not preserve root trust/high-water state: %+v err=%v", reopenedState, err)
	}
}

func TestLanesL310RejectsWrongPeerExpiredMandateAndMismatchedKey(t *testing.T) {
	t.Run("wrong agent identity", func(t *testing.T) {
		fixture := l310NewFixture(t)
		planID := fixture.stageCandidate(t)
		mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		wrong := fixture.peer
		wrong.CodeRequirement = "identifier \"untrusted.agent\""
		if _, err := fixture.signer.Sign(context.Background(), wrong, mandateID); !errors.Is(err, ErrAuthorityDenied) {
			t.Fatalf("wrong authority agent identity accepted: %v", err)
		}
		if state, err := fixture.journal.snapshot(); err != nil || len(state.Mandates) != 1 || len(state.ConsumedMandates) != 0 {
			t.Fatalf("wrong peer consumed authority approval: state=%+v err=%v", state, err)
		}
	})

	t.Run("expired approval", func(t *testing.T) {
		fixture := l310NewFixture(t)
		planID := fixture.stageCandidate(t)
		mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		fixture.signer.clock = func() time.Time { return fixture.now.Add(time.Minute) }
		if _, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID); !errors.Is(err, ErrAuthorityMandate) {
			t.Fatalf("expired authority mandate accepted: %v", err)
		}
		if state, err := fixture.journal.snapshot(); err != nil || len(state.Mandates) != 1 || len(state.ConsumedMandates) != 0 {
			t.Fatalf("expired mandate changed replay state: state=%+v err=%v", state, err)
		}
	})

	t.Run("wrong signing key", func(t *testing.T) {
		fixture := l310NewFixture(t)
		planID := fixture.stageCandidate(t)
		mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		wrongID, wrongPrivate := l304PrivateKey(99)
		fixture.signer.keys = l310KeyProvider{identity: SigningIdentity{KeyID: wrongID, Role: SignerRoleReleaseAuthority, PrivateKey: wrongPrivate}}
		if _, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID); !errors.Is(err, ErrAuthorityKey) {
			t.Fatalf("mismatched signer key accepted: %v", err)
		}
		if state, err := fixture.journal.snapshot(); err != nil || len(state.Mandates) != 1 || len(state.ConsumedMandates) != 0 {
			t.Fatalf("wrong key consumed authority approval: state=%+v err=%v", state, err)
		}
	})
}

func TestLanesL310AuthoritySignerAllowsOnlyAuthorityRecordRoles(t *testing.T) {
	want := map[string]bool{
		"candidate_manifest": true, "promotion_authorization": true, "lane_acceptance": true,
		"ingress_admission_directive": true, "capacity_policy": true, "host_management_manifest": true,
		"host_management_authorization": true, "host_attestation_key_set": true, "release_revocation_ledger": true,
		"authority_key_set": true,
	}
	for recordType := range recordSignerRoles {
		if got := authorityRecordType(recordType); got != want[recordType] {
			t.Errorf("authority signer role for %q = %t, want %t", recordType, got, want[recordType])
		}
	}
	for recordType, allowed := range want {
		if got := authorityRecordType(recordType); got != allowed {
			t.Errorf("authority signer role for %q = %t, want %t", recordType, got, allowed)
		}
		if !allowed {
			continue
		}
		keyID, privateKey := l304PrivateKey(72)
		payload := []byte(`{"role_probe":true}`)
		role, ok := SignerRoleForRecordType(recordType)
		if !ok {
			t.Fatalf("authority record type %q has no envelope role", recordType)
		}
		if role == SignerRolePreviousAuthorityKey {
			payload = []byte(`{"previous_key_id":"` + keyID + `","role_probe":true}`)
		}
		envelope, err := SignEnvelope(recordType, payload, SigningIdentity{KeyID: keyID, Role: role, PrivateKey: privateKey})
		if err != nil {
			t.Errorf("authority role %q did not sign through its closed role: %v", recordType, err)
			continue
		}
		if _, err := VerifyEnvelope(envelope, map[string]TrustedKey{keyID: {KeyID: keyID, Role: SignerRoleReleaseAuthority, PublicKey: privateKey.Public().(ed25519.PublicKey)}}); err != nil {
			t.Errorf("authority role %q did not verify under the pinned key: %v", recordType, err)
		}
	}
	for _, forbidden := range []string{"host_build_attestation", "promotion_attestation", "ingress_admission_attestation", "capacity_measurement_attestation", "pki_certificate_request", "host_management_attestation", "shell", ""} {
		if authorityRecordType(forbidden) {
			t.Errorf("host-signed, unknown, or empty record type %q entered the authority signer", forbidden)
		}
	}
}

func TestLanesL310AuthorityKeySetUsesNamedPreviousSigner(t *testing.T) {
	fixture := l310NewFixture(t)
	previous := l304KeyState("", 0, strings.Repeat("a", 64), fixture.keyID, fixture.privateKey,
		fixture.now.Add(-time.Hour), fixture.now.Add(time.Hour))
	successorID, successorPrivate := l304PrivateKey(73)
	identity := SigningIdentity{KeyID: fixture.keyID, Role: SignerRolePreviousAuthorityKey, PrivateKey: fixture.privateKey}
	raw := l304KeySetEnvelope(t, "authority_key_set", "", 1, previous, successorID,
		successorPrivate.Public().(ed25519.PublicKey), successorPrivate, fixture.now.Add(time.Hour),
		fixture.now.Add(-time.Minute), fixture.now.Add(time.Hour), identity)
	var source Envelope
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	planID, err := fixture.agent.StagePlan(context.Background(), fixture.peer, "authority_key_set", source.Payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyEnvelope(signed, map[string]TrustedKey{fixture.keyID: {
		KeyID: fixture.keyID, Role: SignerRoleReleaseAuthority, PublicKey: fixture.privateKey.Public().(ed25519.PublicKey),
	}})
	if err != nil || verified.RecordType != "authority_key_set" || verified.SignerKeyID != fixture.keyID {
		t.Fatalf("authority key rotation was not signed by its named previous key: record=%+v err=%v", verified, err)
	}
}

func TestLanesL310RejectsUnsafePlansAndLedgerChanges(t *testing.T) {
	fixture := l310NewFixture(t)
	if _, err := fixture.agent.StagePlan(context.Background(), fixture.peer, "host_build_attestation", []byte(`{}`), 1); !errors.Is(err, ErrAuthorityDenied) {
		t.Fatalf("host-attestation record entered authority signer: %v", err)
	}
	badPayload := append(append([]byte(nil), fixture.payload[:len(fixture.payload)-1]...), []byte(`,"caller_path":"/tmp/x"}`)...)
	if _, err := fixture.agent.StagePlan(context.Background(), fixture.peer, "candidate_manifest", badPayload, 1); !errors.Is(err, ErrAuthorityPlan) {
		t.Fatalf("open or malformed authority payload was staged: %v", err)
	}
	planID := fixture.stageCandidate(t)
	mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.journal.mutate(func(state *authorityRootState) error {
		state.LedgerGeneration++
		return nil
	}); !errors.Is(err, ErrAuthorityStateRollback) {
		t.Fatalf("authority state accepted a ledger high-water without a signed chained grant: %v", err)
	}
	if _, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID); err != nil {
		t.Fatalf("rejected forged ledger update invalidated the exact still-current mandate: %v", err)
	}
	if state, err := fixture.journal.snapshot(); err != nil || len(state.Mandates) != 0 || len(state.ConsumedMandates) != 1 {
		t.Fatalf("failed internal ledger mutation did not preserve the exact approval result: state=%+v err=%v", state, err)
	}
}

func TestLanesL310ApprovalDenialAndJournalTamperingFailClosed(t *testing.T) {
	t.Run("interactive approval denied", func(t *testing.T) {
		fixture := l310NewFixture(t)
		planID := fixture.stageCandidate(t)
		fixture.authorizer.err = errors.New("user denied")
		if _, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute); !errors.Is(err, ErrAuthorityDenied) {
			t.Fatalf("denied interactive authorization created a mandate: %v", err)
		}
		if state, err := fixture.journal.snapshot(); err != nil || len(state.Mandates) != 0 || len(state.Plans) != 1 {
			t.Fatalf("approval denial changed staged root state: state=%+v err=%v", state, err)
		}
	})

	t.Run("missing replay commit", func(t *testing.T) {
		fixture := l310NewFixture(t)
		if _, err := fixture.journal.mutate(func(state *authorityRootState) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.journal.conn.ExecContext(context.Background(), "DELETE FROM authority_commits WHERE generation=1"); err != nil {
			t.Fatal(err)
		}
		if err := fixture.journal.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := openAuthorityJournalAt(fixture.dir, uint32(os.Getuid()), nil); !errors.Is(err, ErrAuthorityStateRollback) {
			t.Fatalf("authority journal with a missing commit opened: %v", err)
		}
	})
}

type l310KeyProvider struct{ identity SigningIdentity }

func (p l310KeyProvider) LoadAuthorityKey(keyID string) (SigningIdentity, error) {
	if p.identity.KeyID != keyID {
		return SigningIdentity{}, ErrAuthorityKey
	}
	return p.identity, nil
}

type l310InteractiveAuthorizer struct {
	calls   int
	message string
	err     error
}

func (a *l310InteractiveAuthorizer) AuthorizeAuthorityMandate(_ context.Context, message string) (string, error) {
	a.calls++
	a.message = message
	if a.err != nil {
		return "", a.err
	}
	return "interactive-receipt-20261017", nil
}

type l310Fixture struct {
	dir           string
	journal       *AuthorityJournal
	agent         *AuthorityAgent
	approval      *AuthorityApprovalTool
	signer        *AuthoritySigner
	keys          l310KeyProvider
	keyID         string
	privateKey    ed25519.PrivateKey
	expected      authorityAgentIdentity
	peer          authorityAgentEvidence
	authorizer    *l310InteractiveAuthorizer
	payload       []byte
	contextDigest string
	now           time.Time
}

func l310NewFixture(t *testing.T) *l310Fixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, authorityStateMode); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	keyID, privateKey := l304PrivateKey(31)
	bootstrap := AuthorityStateBootstrap{KeyID: keyID, PublicKey: privateKey.Public().(ed25519.PublicKey), LedgerGeneration: 1,
		LedgerRecordID: hexDigest([]byte("fixture-bootstrap-ledger")), LedgerIssuedAt: now.Add(-time.Minute),
		LedgerExpiresAt:  now.Add(MaxRevocationLedgerLifetime - time.Minute),
		RevokedRecordIDs: []string{hexDigest([]byte("fixture-revoked-record"))}, RevokedKeyIDs: []string{"retired-authority-key"}}
	journal, err := openAuthorityJournalAt(dir, uint32(os.Getuid()), &bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	payload, _ := l302CandidateFixture(t)
	canonical, err := CanonicalizeJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	expected := authorityAgentIdentity{UID: 0, LaunchdLabel: "runner-release-authority-agent", CodeRequirement: `identifier "com.remote-session-runner.runner-release-authority-agent" and anchor apple generic and certificate leaf[subject.OU] = "ABCDE12345"`}
	peer := authorityAgentEvidence{UID: 0, PID: 501, AuditTokenValidated: true, LaunchdLabel: expected.LaunchdLabel, CodeRequirement: expected.CodeRequirement}
	agent, err := NewAuthorityAgent(journal, expected, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &l310InteractiveAuthorizer{}
	approval, err := NewAuthorityApprovalTool(journal, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	agent.now = func() time.Time { return now }
	approval.now = func() time.Time { return now }
	identity := SigningIdentity{KeyID: keyID, Role: SignerRoleReleaseAuthority, PrivateKey: privateKey}
	keys := l310KeyProvider{identity: identity}
	signer, err := NewAuthoritySigner(journal, keys, expected, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	signer.clock = func() time.Time { return now }
	contextDigest, _, err := authorityPlanContext("candidate_manifest", canonical)
	if err != nil {
		t.Fatal(err)
	}
	return &l310Fixture{dir: dir, journal: journal, agent: agent, approval: approval, signer: signer,
		keys: keys, keyID: keyID, privateKey: privateKey, expected: expected, peer: peer,
		authorizer: authorizer, payload: canonical, contextDigest: contextDigest, now: now}
}

func (f *l310Fixture) stageCandidate(t *testing.T) string {
	t.Helper()
	planID, err := f.agent.StagePlan(context.Background(), f.peer, "candidate_manifest", f.payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	return planID
}
