package release

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLanesL311DirectiveGrantRenewsTwoContiguousSamePhaseLeases(t *testing.T) {
	fixture := l310NewFixture(t)
	initial := l311SignInitialDirective(t, fixture)
	grantID, err := fixture.approval.CreateDirectiveRenewalGrant(context.Background(), initial, l311DirectiveGrantSpec(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	first, err := fixture.signer.RenewDirective(context.Background(), fixture.peer, grantID)
	if err != nil {
		t.Fatal(err)
	}
	firstRecord, err := VerifyEnvelope(first, map[string]TrustedKey{fixture.keyID: {
		KeyID: fixture.keyID, Role: SignerRoleReleaseAuthority, PublicKey: fixture.privateKey.Public().(ed25519.PublicKey),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var firstPayload ingressDirectivePayload
	if decodeStrict(firstRecord.Payload, &firstPayload) != nil || firstPayload.LeaseGeneration != 1 ||
		firstPayload.PredecessorRecordID == "" || firstPayload.RequiredLedgerGeneration != 1 || firstPayload.Role != "candidate_admission" {
		t.Fatalf("first renewal did not bind to the initial phase and predecessor: %+v", firstPayload)
	}
	if _, err := fixture.signer.RenewDirective(context.Background(), fixture.peer, grantID); !errors.Is(err, ErrAuthorityGrantPublicationPending) {
		t.Fatalf("a second renewal was issued before publication confirmation: %v", err)
	}
	peer := l311HostPeer(t, "linux-host")
	if err := fixture.agent.RecordDirectivePublication(context.Background(), fixture.peer, peer, grantID, firstRecord.RecordID); err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(5 * time.Minute)
	fixture.agent.now = func() time.Time { return fixture.now }
	fixture.approval.now = func() time.Time { return fixture.now }
	fixture.signer.clock = func() time.Time { return fixture.now }
	second, err := fixture.signer.RenewDirective(context.Background(), fixture.peer, grantID)
	if err != nil {
		t.Fatal(err)
	}
	secondRecord, err := VerifyEnvelope(second, map[string]TrustedKey{fixture.keyID: {
		KeyID: fixture.keyID, Role: SignerRoleReleaseAuthority, PublicKey: fixture.privateKey.Public().(ed25519.PublicKey),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var secondPayload ingressDirectivePayload
	if decodeStrict(secondRecord.Payload, &secondPayload) != nil || secondPayload.LeaseGeneration != 2 ||
		secondPayload.PredecessorRecordID != firstRecord.RecordID || !sameDirectiveContext(secondPayload, firstPayload) {
		t.Fatalf("second renewal forked or expanded approved authority: %+v", secondPayload)
	}
	if err := fixture.agent.RecordDirectivePublication(context.Background(), fixture.peer, peer, grantID, secondRecord.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.signer.RenewDirective(context.Background(), fixture.peer, grantID); !errors.Is(err, ErrAuthorityGrantExhausted) {
		t.Fatalf("directive grant exceeded its generation/cap: %v", err)
	}
	state, err := fixture.journal.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	grant := state.DirectiveGrants[grantID]
	if grant.IssuedCount != 2 || grant.CurrentLeaseGeneration != 2 || grant.CurrentRecordID != secondRecord.RecordID ||
		grant.PendingPublicationRecord != "" || len(grant.Issuances) != 2 {
		t.Fatalf("directive renewal high-water not durable: %+v", grant)
	}
}

func TestLanesL311HostAnchorRejectsDirectiveGapAndFork(t *testing.T) {
	fixture := newL305Fixture(t)
	issued := fixture.clock.Add(-time.Minute)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, issued, fixture.clock.Add(14*time.Minute))
	fixture.acceptProbe(t)
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatalf("install fresh bootstrap successor ledger: %v", err)
	}
	first := AnchorDirectiveHighWater{Lane: "dev", HostID: "linux-host", TransitionID: l305TransitionID, TransitionEpoch: 7,
		AdmissionGeneration: 0, LeaseGeneration: 0, Role: "candidate_admission", CandidateManifestID: strings.Repeat("e", 64),
		ConfigurationDigest: l303Digest, RecordID: strings.Repeat("1", 64), SignerKeyID: fixture.authorityID,
		NotBefore: fixture.clock.Add(-time.Minute).Format(time.RFC3339Nano), NotAfter: fixture.clock.Add(10 * time.Minute).Format(time.RFC3339Nano), RequiredLedgerGeneration: 1}
	fixture.acceptProbe(t)
	if err := fixture.anchor.recordVerifiedDirective(first); err != nil {
		t.Fatalf("record initial directive: %v", err)
	}
	gap := first
	gap.LeaseGeneration = 3
	gap.RecordID = strings.Repeat("2", 64)
	gap.PredecessorRecordID = first.RecordID
	fixture.acceptProbe(t)
	if err := fixture.anchor.recordVerifiedDirective(gap); err == nil {
		t.Fatal("host anchor accepted a skipped lease generation")
	}
	fork := first
	fork.LeaseGeneration = 1
	fork.RecordID = strings.Repeat("3", 64)
	fork.PredecessorRecordID = strings.Repeat("4", 64)
	if err := fixture.anchor.recordVerifiedDirective(fork); err == nil {
		t.Fatal("host anchor accepted a forked directive predecessor")
	}
	valid := first
	valid.LeaseGeneration = 1
	valid.RecordID = strings.Repeat("5", 64)
	valid.PredecessorRecordID = first.RecordID
	if err := fixture.anchor.recordVerifiedDirective(valid); err != nil {
		t.Fatalf("host anchor rejected the exact contiguous renewal: %v", err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil || state.DirectiveHighWater["dev"] != valid {
		t.Fatalf("host directive high-water changed after failed gap/fork or lost valid renewal: state=%+v err=%v", state.DirectiveHighWater["dev"], err)
	}
}

func TestLanesL311RejectsGrantExpansionBadBoundsAndTamperedHighWater(t *testing.T) {
	fixture := l310NewFixture(t)
	initial := l311SignInitialDirective(t, fixture)
	valid := l311DirectiveGrantSpec(fixture.now)
	cases := []struct {
		name string
		edit func(*DirectiveRenewalGrantSpec)
	}{
		{"one renewal only", func(spec *DirectiveRenewalGrantSpec) { spec.MaximumLeaseGeneration = 2; spec.IssuanceCap = 1 }},
		{"cap exceeds generations", func(spec *DirectiveRenewalGrantSpec) { spec.MaximumLeaseGeneration = 1 }},
		{"warning leaves less than two cycles", func(spec *DirectiveRenewalGrantSpec) { spec.RenewalWarningAt = fixture.now.Add(40 * time.Minute) }},
		{"warning after expiry", func(spec *DirectiveRenewalGrantSpec) { spec.RenewalWarningAt = fixture.now.Add(50 * time.Minute) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			spec := valid
			test.edit(&spec)
			if _, err := fixture.approval.CreateDirectiveRenewalGrant(context.Background(), initial, spec); !errors.Is(err, ErrAuthorityGrant) {
				t.Fatalf("unsafe renewal bounds were approved: %v", err)
			}
		})
	}
	grantID, err := fixture.approval.CreateDirectiveRenewalGrant(context.Background(), initial, valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.journal.mutate(func(state *authorityRootState) error {
		grant := state.DirectiveGrants[grantID]
		grant.Template.Role = "active_runtime_closure"
		grant.Template.AdmissionGeneration = 0
		state.DirectiveGrants[grantID] = grant
		return nil
	}); !errors.Is(err, ErrAuthorityStateRollback) {
		t.Fatalf("root journal accepted authority-expanding grant edit: %v", err)
	}
	if _, err := fixture.signer.RenewDirective(context.Background(), fixture.peer, grantID); err != nil {
		t.Fatalf("failed mutation changed valid grant state: %v", err)
	}
	fixture.now = fixture.now.Add(46 * time.Minute)
	fixture.signer.clock = func() time.Time { return fixture.now }
	if _, err := fixture.signer.RenewDirective(context.Background(), fixture.peer, grantID); !errors.Is(err, ErrAuthorityGrantExpired) {
		t.Fatalf("expired directive grant remained usable: %v", err)
	}
}

func TestLanesL311LedgerGrantSerializesCasAndPreservesRevocationSets(t *testing.T) {
	fixture := l310NewFixture(t)
	grantSpec := l311LedgerGrantSpec(fixture.now)
	grantID, err := fixture.approval.CreateLedgerRenewalGrant(context.Background(), grantSpec)
	if err != nil {
		t.Fatal(err)
	}
	secondJournal, err := openAuthorityJournalAt(fixture.dir, uint32(os.Getuid()), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondJournal.Close() })
	secondSigner, err := NewAuthoritySigner(secondJournal, fixture.keys, fixture.expected, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	secondSigner.clock = func() time.Time { return fixture.now }
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, signer := range []*AuthoritySigner{fixture.signer, secondSigner} {
		go func(signer *AuthoritySigner) {
			<-start
			_, err := signer.RenewLedger(context.Background(), fixture.peer, grantID)
			results <- err
		}(signer)
	}
	close(start)
	errorsSeen := []error{<-results, <-results}
	successes := 0
	for _, result := range errorsSeen {
		if result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent ledger CAS commits=%d errors=%v, want exactly one", successes, errorsSeen)
	}
	state, err := fixture.journal.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	grant := state.LedgerGrants[grantID]
	if state.LedgerGeneration != 2 || grant.CurrentGeneration != 2 || grant.IssuedCount != 1 ||
		grant.PendingPublicationRecord == "" || len(grant.PendingPublicationHosts) != len(canonicalHostIDs) || len(grant.Issuances) != 1 {
		t.Fatalf("concurrent requests forked or skipped the ledger high-water: state=%+v grant=%+v", state, grant)
	}
	firstIssue := grant.Issuances[0]
	for _, hostID := range sortedCanonicalHostIDs() {
		if err := fixture.agent.RecordLedgerPublication(context.Background(), l311HostPeer(t, hostID), grantID, firstIssue.RecordID); err != nil {
			t.Fatalf("record publication on %s: %v", hostID, err)
		}
	}
	fixture.now = fixture.now.Add(5 * time.Minute)
	fixture.signer.clock = func() time.Time { return fixture.now }
	fixture.agent.now = func() time.Time { return fixture.now }
	second, err := fixture.signer.RenewLedger(context.Background(), fixture.peer, grantID)
	if err != nil {
		t.Fatal(err)
	}
	secondRecord, err := VerifyEnvelope(second, map[string]TrustedKey{fixture.keyID: {
		KeyID: fixture.keyID, Role: SignerRoleReleaseAuthority, PublicKey: fixture.privateKey.Public().(ed25519.PublicKey),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var ledger revocationLedgerPayload
	if decodeStrict(secondRecord.Payload, &ledger) != nil || ledger.Generation != 3 || ledger.PredecessorRecordID != firstIssue.RecordID ||
		!slices.Equal(ledger.RevokedRecordIDs, fixtureBootstrapRevokedRecords()) ||
		!slices.Equal(ledger.RevokedKeyIDs, []string{"retired-authority-key"}) {
		t.Fatalf("ledger renewal changed revocations or predecessor: %+v", ledger)
	}
	if _, err := fixture.signer.RenewLedger(context.Background(), fixture.peer, grantID); !errors.Is(err, ErrAuthorityGrantPublicationPending) {
		t.Fatalf("ledger renewed before all-host publication: %v", err)
	}
	for _, hostID := range sortedCanonicalHostIDs() {
		if err := fixture.agent.RecordLedgerPublication(context.Background(), l311HostPeer(t, hostID), grantID, secondRecord.RecordID); err != nil {
			t.Fatalf("record second publication on %s: %v", hostID, err)
		}
	}
	if _, err := fixture.signer.RenewLedger(context.Background(), fixture.peer, grantID); !errors.Is(err, ErrAuthorityGrantExhausted) {
		t.Fatalf("ledger grant exceeded its finite cap: %v", err)
	}
	state, err = fixture.journal.snapshot()
	if err != nil || state.LedgerGeneration != 3 || state.LedgerRecordID != secondRecord.RecordID || state.LedgerGrants[grantID].IssuedCount != 2 {
		t.Fatalf("final ledger high-water not durable: state=%+v err=%v", state, err)
	}
}

func TestLanesL311RenewalWarningsPersistAndProbeIsBoundToFreshHostChallenge(t *testing.T) {
	fixture := l310NewFixture(t)
	grantID, err := fixture.approval.CreateLedgerRenewalGrant(context.Background(), l311LedgerGrantSpec(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.signer.RenewLedger(context.Background(), fixture.peer, grantID); err != nil {
		t.Fatal(err)
	}
	alerts, err := fixture.agent.CheckRenewalWarnings(context.Background(), fixture.peer)
	if err != nil || len(alerts) != 1 || alerts[0].Reason != "one_issuance_remaining" || alerts[0].GrantID != grantID {
		t.Fatalf("one-remaining operator alert missing: alerts=%+v err=%v", alerts, err)
	}
	if duplicate, err := fixture.agent.CheckRenewalWarnings(context.Background(), fixture.peer); err != nil || len(duplicate) != 0 {
		t.Fatalf("durable renewal alert was duplicated: alerts=%+v err=%v", duplicate, err)
	}
	fixture.journal.mu.Lock()
	if err := fixture.journal.conn.Close(); err != nil {
		fixture.journal.mu.Unlock()
		t.Fatal(err)
	}
	fixture.journal.conn = nil
	fixture.journal.mu.Unlock()
	reopened, err := openAuthorityJournalAt(fixture.dir, uint32(os.Getuid()), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	state, err := reopened.snapshot()
	if err != nil || len(state.RenewalAlerts) != 1 {
		t.Fatalf("renewal alert did not survive journal reopen: alerts=%+v err=%v", state.RenewalAlerts, err)
	}

	fixture = l310NewFixture(t)
	peer := l311HostPeer(t, "linux-host")
	challenge := l311Challenge(t, "0123456789abcdef0123456789abcdef", 31)
	if _, err := authorityHostPeerAfterVerification("linux-host", "mtls", "spiffe://remote-session-runner/host/factory-sandbox/release-transport"); err == nil {
		t.Fatal("transport peer with conflicting certificate host was accepted")
	}
	if err := fixture.agent.RecordAuthorityTimeProbeChallenge(context.Background(), fixture.peer, peer, challenge); err != nil {
		t.Fatal(err)
	}
	response, err := fixture.signer.SignAuthorityTimeProbe(context.Background(), fixture.peer, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]TrustedKey{fixture.keyID: {KeyID: fixture.keyID, Role: SignerRoleReleaseAuthority,
		PublicKey: fixture.privateKey.Public().(ed25519.PublicKey)}}
	if _, err := verifyTimeProbeResponse(response, challenge, fixture.now, fixture.now.Add(100*time.Millisecond), 100*time.Millisecond, trusted, time.Time{}); err != nil {
		t.Fatalf("fresh stored host challenge did not verify: %v", err)
	}
	if _, err := fixture.signer.SignAuthorityTimeProbe(context.Background(), fixture.peer, challenge.ID); !errors.Is(err, ErrAuthorityTimeProbe) {
		t.Fatalf("authority time challenge was replayed: %v", err)
	}
	var tampered signedTimeProbeResponse
	if err := json.Unmarshal(response, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Nonce = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	badResponse, _ := json.Marshal(tampered)
	badResponse, _ = CanonicalizeJSON(badResponse)
	if _, err := verifyTimeProbeResponse(badResponse, challenge, fixture.now, fixture.now.Add(100*time.Millisecond),
		100*time.Millisecond, trusted, time.Time{}); err == nil {
		t.Fatal("response for a different host nonce was accepted")
	}
	stale := l311Challenge(t, "1123456789abcdef0123456789abcdef", 32)
	if err := fixture.agent.RecordAuthorityTimeProbeChallenge(context.Background(), fixture.peer, peer, stale); err != nil {
		t.Fatal(err)
	}
	state, err = fixture.journal.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	staleRecord := state.TimeProbeRecords[stale.ID]
	fixture.signer.monotonicNow = func() (uint64, error) {
		return staleRecord.ReceivedMonotonicNanos + uint64(3*time.Second), nil
	}
	if _, err := fixture.signer.SignAuthorityTimeProbe(context.Background(), fixture.peer, stale.ID); !errors.Is(err, ErrAuthorityTimeProbe) {
		t.Fatalf("stale challenge was signed: %v", err)
	}
	state, err = fixture.journal.snapshot()
	if err != nil || !state.TimeProbeRecords[stale.ID].Used || !state.TimeProbeRecords[stale.ID].Expired {
		t.Fatalf("stale challenge was not durably consumed: record=%+v err=%v", state.TimeProbeRecords[stale.ID], err)
	}
	fixture.signer.monotonicNow = anchorMonotonicNanos
	newChallenge := l311Challenge(t, "2123456789abcdef0123456789abcdef", 33)
	if err := fixture.agent.RecordAuthorityTimeProbeChallenge(context.Background(), fixture.peer, peer, newChallenge); err != nil {
		t.Fatalf("expired host challenge prevented a fresh probe: %v", err)
	}
}

func TestLanesL311NativeProbeUsesMonotonicKernelClock(t *testing.T) {
	if os.Getenv("LANES_NATIVE_VALIDATION") != "1" {
		t.Skip("native clock observation runs only through the LANES N-tier dispatcher")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("N-tier probe is intended for the Linux execution hosts, got %s", runtime.GOOS)
	}
	hostID := os.Getenv("LANES_NATIVE_HOST_ID")
	if !slices.Contains(canonicalHostIDs, hostID) || hostID == "mac-control-plane" {
		t.Fatalf("native probe lacks a canonical Ubuntu host identity: %q", hostID)
	}
	before, err := anchorMonotonicNanos()
	if err != nil || before == 0 {
		t.Fatalf("read host monotonic clock: value=%d err=%v", before, err)
	}
	after, err := anchorMonotonicNanos()
	if err != nil || after < before {
		t.Fatalf("host monotonic clock moved backwards: before=%d after=%d err=%v", before, after, err)
	}
}

func l311SignInitialDirective(t *testing.T, fixture *l310Fixture) []byte {
	t.Helper()
	directive := ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition, Lane: "dev", HostID: "linux-host",
		ConfigurationDigest: l303Digest, Role: "candidate_admission", TransitionEpoch: 7, AdmissionGeneration: 0,
		LeaseGeneration: 0, NotBefore: fixture.now.Add(-time.Minute).Format(time.RFC3339Nano),
		NotAfter: fixture.now.Add(10 * time.Minute).Format(time.RFC3339Nano), RequiredLedgerGeneration: 1}
	payload, err := json.Marshal(directive)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = CanonicalizeJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := fixture.agent.StagePlan(context.Background(), fixture.peer, "ingress_admission_directive", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	mandateID, err := fixture.approval.CreateMandate(context.Background(), planID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := fixture.signer.Sign(context.Background(), fixture.peer, mandateID)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func l311DirectiveGrantSpec(now time.Time) DirectiveRenewalGrantSpec {
	return DirectiveRenewalGrantSpec{MaximumLeaseGeneration: 3, LeaseDuration: 5 * time.Minute,
		ExpiresAt: now.Add(45 * time.Minute), RenewalWarningAt: now.Add(30 * time.Minute), IssuanceCap: 2}
}

func l311LedgerGrantSpec(now time.Time) LedgerRenewalGrantSpec {
	return LedgerRenewalGrantSpec{MaximumGeneration: 3, ExpiresAt: now.Add(40 * time.Minute),
		RenewalWarningAt: now.Add(20 * time.Minute), IssuanceCap: 2}
}

func l311HostPeer(t *testing.T, hostID string) authorityHostPeer {
	t.Helper()
	if hostID == "mac-control-plane" {
		peer, err := authorityHostPeerAfterVerification(hostID, "local", localProbePeerURI)
		if err != nil {
			t.Fatal(err)
		}
		return peer
	}
	peer, err := authorityHostPeerAfterVerification(hostID, "mtls", "spiffe://remote-session-runner/host/"+hostID+"/release-transport")
	if err != nil {
		t.Fatal(err)
	}
	return peer
}

func l311Challenge(t *testing.T, id string, nonceByte byte) TimeProbeChallenge {
	t.Helper()
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = nonceByte
	}
	return TimeProbeChallenge{ID: id, Nonce: base64.RawURLEncoding.EncodeToString(nonce)}
}

func fixtureBootstrapRevokedRecords() []string {
	return []string{hexDigest([]byte("fixture-revoked-record"))}
}
