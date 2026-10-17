package release

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const l315TransitionID = "00000000-0000-7000-8000-000000000015"

func TestLanesL315AppliesSignedDirectivePhasesAndRequiresExactOpenForAcceptance(t *testing.T) {
	fixture := l315FreshAnchor(t)
	closed := l315Directive(fixture, 7, 0, "candidate_admission", strings.Repeat("e", 64), l303Digest, "")
	closedRaw, closedID := l315SignDirective(t, fixture, closed)
	fixture.acceptProbe(t)
	first, err := fixture.anchor.ApplyIngressAdmissionDirective(closedRaw)
	if err != nil || first.RecordID != closedID || first.AdmissionGeneration != 0 {
		t.Fatalf("signed closed directive was not applied: value=%+v err=%v", first, err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil || state.DirectiveHighWater["dev"] != first || state.PendingCandidate["dev"].RecordID != closedID {
		t.Fatalf("directive high-water and pending candidate were not committed together: state=%+v err=%v", state, err)
	}
	generation := state.Generation
	if replay, err := fixture.anchor.ApplyIngressAdmissionDirective(closedRaw); err != nil || replay != first {
		t.Fatalf("exact signed retry was not idempotent: value=%+v err=%v", replay, err)
	}
	state, err = fixture.anchor.Snapshot()
	if err != nil || state.Generation != generation {
		t.Fatalf("exact signed retry changed durable state: generation=%d err=%v", state.Generation, err)
	}
	closedAcceptance, _ := l315SignAcceptance(t, fixture, closed)
	if err := fixture.anchor.RecordVerifiedActiveRuntime(closedAcceptance); err == nil {
		t.Fatal("closed candidate was accepted as an active runtime")
	}

	transport := closed
	transport.AdmissionGeneration = 1
	transport.PredecessorRecordID = closedID
	transportRaw, transportID := l315SignDirective(t, fixture, transport)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(transportRaw); err != nil {
		t.Fatalf("contiguous transport-ready directive rejected: %v", err)
	}
	open := transport
	open.AdmissionGeneration = 2
	open.PredecessorRecordID = transportID
	openRaw, openID := l315SignDirective(t, fixture, open)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(openRaw); err != nil {
		t.Fatalf("contiguous open directive rejected: %v", err)
	}
	wrongAcceptancePayload := open
	wrongAcceptancePayload.CandidateID = strings.Repeat("d", 64)
	wrongAcceptance, _ := l315SignAcceptance(t, fixture, wrongAcceptancePayload)
	fixture.acceptProbe(t)
	if err := fixture.anchor.RecordVerifiedActiveRuntime(wrongAcceptance); err == nil {
		t.Fatal("acceptance for a different candidate was allowed")
	}
	notAfter, err := time.Parse(time.RFC3339Nano, open.NotAfter)
	if err != nil {
		t.Fatal(err)
	}
	expiredAcceptance, _ := l315SignAcceptanceAt(t, fixture, open, notAfter)
	if err := fixture.anchor.RecordVerifiedActiveRuntime(expiredAcceptance); err == nil {
		t.Fatal("acceptance timestamp outside the open directive validity was allowed")
	}
	acceptanceRaw, acceptanceID := l315SignAcceptance(t, fixture, open)
	if err := fixture.anchor.RecordVerifiedActiveRuntime(acceptanceRaw); err != nil {
		t.Fatalf("exact signed open directive could not be accepted: %v", err)
	}
	state, err = fixture.anchor.Snapshot()
	if err != nil || state.ActiveRuntime["dev"].RecordID != openID || state.ActiveRuntime["dev"].AcceptanceRecordID != acceptanceID {
		t.Fatalf("active runtime did not retain the exact directive/acceptance handoff: state=%+v err=%v", state.ActiveRuntime["dev"], err)
	}

	closure := l315Directive(fixture, 8, 0, "active_runtime_closure", closed.CandidateID, closed.ConfigurationDigest, openID)
	closureRaw, closureID := l315SignDirective(t, fixture, closure)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(closureRaw); err != nil {
		t.Fatalf("higher-epoch exact-incumbent closure rejected: %v", err)
	}
	replacement := l315Directive(fixture, 9, 0, "candidate_admission", closed.CandidateID, closed.ConfigurationDigest, closureID)
	replacementRaw, replacementID := l315SignDirective(t, fixture, replacement)
	fixture.acceptProbe(t)
	if value, err := fixture.anchor.ApplyIngressAdmissionDirective(replacementRaw); err != nil || value.RecordID != replacementID {
		t.Fatalf("higher-epoch replacement after closure rejected: value=%+v err=%v", value, err)
	}
	replacementReady := replacement
	replacementReady.AdmissionGeneration = 1
	replacementReady.PredecessorRecordID = replacementID
	replacementReadyRaw, replacementReadyID := l315SignDirective(t, fixture, replacementReady)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(replacementReadyRaw); err != nil {
		t.Fatalf("higher-epoch transport-ready directive rejected: %v", err)
	}
	replacementOpen := replacementReady
	replacementOpen.AdmissionGeneration = 2
	replacementOpen.PredecessorRecordID = replacementReadyID
	replacementOpenRaw, _ := l315SignDirective(t, fixture, replacementOpen)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(replacementOpenRaw); err != nil {
		t.Fatalf("higher-epoch open directive rejected: %v", err)
	}
	fixture.acceptProbe(t)
	if err := fixture.anchor.RecordVerifiedActiveRuntime(acceptanceRaw); err == nil {
		t.Fatal("prior epoch's exact lane_acceptance was reused after higher-epoch replacement")
	}
}

func TestLanesL315RejectsTamperedWrongHostExpiredStaleLedgerIdentityForkAndSkippedPhase(t *testing.T) {
	cases := []struct {
		name   string
		change func(*l305Fixture, *ingressDirectivePayload, *SigningIdentity)
	}{
		{name: "wrong host", change: func(_ *l305Fixture, value *ingressDirectivePayload, _ *SigningIdentity) {
			value.HostID = "factory-sandbox"
		}},
		{name: "expired", change: func(f *l305Fixture, value *ingressDirectivePayload, _ *SigningIdentity) {
			value.NotBefore = f.clock.Add(-20 * time.Minute).Format(time.RFC3339Nano)
			value.NotAfter = f.clock.Add(-10 * time.Minute).Format(time.RFC3339Nano)
		}},
		{name: "stale ledger generation", change: func(_ *l305Fixture, value *ingressDirectivePayload, _ *SigningIdentity) {
			value.RequiredLedgerGeneration = 2
		}},
		{name: "untrusted signer", change: func(f *l305Fixture, _ *ingressDirectivePayload, signer *SigningIdentity) {
			id, private := l304PrivateKey(99)
			*signer = SigningIdentity{KeyID: id, Role: SignerRoleReleaseAuthority, PrivateKey: private}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := l315FreshAnchor(t)
			payload := l315Directive(fixture, 7, 0, "candidate_admission", strings.Repeat("e", 64), l303Digest, "")
			signer := SigningIdentity{KeyID: fixture.authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: fixture.authority}
			tc.change(fixture, &payload, &signer)
			raw, err := SignEnvelope("ingress_admission_directive", l302JSON(t, payload), signer)
			if err != nil {
				t.Fatal(err)
			}
			fixture.acceptProbe(t)
			if _, err := fixture.anchor.ApplyIngressAdmissionDirective(raw); !errors.Is(err, ErrIngressDirectiveRejected) {
				t.Fatalf("unsafe signed directive was accepted: %v", err)
			}
			state, err := fixture.anchor.Snapshot()
			if err != nil || len(state.DirectiveHighWater) != 0 || len(state.PendingCandidate) != 0 {
				t.Fatalf("rejected directive changed high-water state: state=%+v err=%v", state, err)
			}
		})
	}

	t.Run("tampered signature and skipped phase", func(t *testing.T) {
		fixture := l315FreshAnchor(t)
		closed := l315Directive(fixture, 7, 0, "candidate_admission", strings.Repeat("e", 64), l303Digest, "")
		closedRaw, closedID := l315SignDirective(t, fixture, closed)
		tampered := append([]byte(nil), closedRaw...)
		tampered[len(tampered)-2] ^= 1
		fixture.acceptProbe(t)
		if _, err := fixture.anchor.ApplyIngressAdmissionDirective(tampered); !errors.Is(err, ErrIngressDirectiveRejected) {
			t.Fatalf("tampered envelope was accepted: %v", err)
		}
		if _, err := fixture.anchor.ApplyIngressAdmissionDirective(closedRaw); err != nil {
			t.Fatalf("valid directive after tamper rejection failed: %v", err)
		}
		identityFork := closed
		identityFork.CandidateID = strings.Repeat("d", 64)
		identityFork.LeaseGeneration = 1
		identityFork.PredecessorRecordID = closedID
		forkRaw, _ := l315SignDirective(t, fixture, identityFork)
		fixture.acceptProbe(t)
		if _, err := fixture.anchor.ApplyIngressAdmissionDirective(forkRaw); !errors.Is(err, ErrIngressDirectiveRejected) {
			t.Fatalf("same-epoch candidate identity switch was accepted: %v", err)
		}
		open := closed
		open.AdmissionGeneration = 2
		open.PredecessorRecordID = closedID
		openRaw, _ := l315SignDirective(t, fixture, open)
		if _, err := fixture.anchor.ApplyIngressAdmissionDirective(openRaw); !errors.Is(err, ErrIngressDirectiveRejected) {
			t.Fatalf("skipped transport-ready generation was accepted: %v", err)
		}
		transport := closed
		transport.AdmissionGeneration = 1
		transport.PredecessorRecordID = closedID
		transportRaw, _ := l315SignDirective(t, fixture, transport)
		if _, err := fixture.anchor.ApplyIngressAdmissionDirective(transportRaw); err != nil {
			t.Fatalf("successor after skipped-phase rejection failed: %v", err)
		}
	})
}

func TestLanesL315ClosureOfStagedCandidateClearsPendingBeforeHigherEpochReplacement(t *testing.T) {
	fixture := l315FreshAnchor(t)
	closed := l315Directive(fixture, 7, 0, "candidate_admission", strings.Repeat("e", 64), l303Digest, "")
	closedRaw, closedID := l315SignDirective(t, fixture, closed)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(closedRaw); err != nil {
		t.Fatal(err)
	}
	transport := closed
	transport.AdmissionGeneration = 1
	transport.PredecessorRecordID = closedID
	transportRaw, transportID := l315SignDirective(t, fixture, transport)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(transportRaw); err != nil {
		t.Fatal(err)
	}
	closure := l315Directive(fixture, 8, 0, "active_runtime_closure", closed.CandidateID, closed.ConfigurationDigest, transportID)
	closureRaw, closureID := l315SignDirective(t, fixture, closure)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(closureRaw); err != nil {
		t.Fatalf("failed candidate transport could not be closed at a higher epoch: %v", err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil || len(state.PendingCandidate) != 0 || state.DirectiveHighWater["dev"].RecordID != closureID {
		t.Fatalf("target closure did not clear staged candidate state: state=%+v err=%v", state, err)
	}
	replacement := l315Directive(fixture, 9, 0, "candidate_admission", strings.Repeat("9", 64), "sha256:"+strings.Repeat("a", 64), closureID)
	replacementRaw, replacementID := l315SignDirective(t, fixture, replacement)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(replacementRaw); err != nil {
		t.Fatalf("higher-epoch replacement after target closure rejected: %v", err)
	}
	state, err = fixture.anchor.Snapshot()
	if err != nil || state.PendingCandidate["dev"].RecordID != replacementID {
		t.Fatalf("replacement candidate was not anchored after closure: state=%+v err=%v", state.PendingCandidate["dev"], err)
	}
}

func TestLanesL315NativeSQLiteDirectiveHighWaterSurvivesRestart(t *testing.T) {
	fixture := l315FreshAnchor(t)
	payload := l315Directive(fixture, 7, 0, "candidate_admission", strings.Repeat("e", 64), l303Digest, "")
	raw, recordID := l315SignDirective(t, fixture, payload)
	fixture.acceptProbe(t)
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(raw); err != nil {
		t.Fatal(err)
	}
	if err := fixture.anchor.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSecurityAnchorAt(fixture.dir, nil, uint32(os.Geteuid()), func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatalf("reopen isolated anchor after signed directive commit: %v", err)
	}
	fixture.anchor = reopened
	state, err := reopened.Snapshot()
	if err != nil || state.DirectiveHighWater["dev"].RecordID != recordID || state.PendingCandidate["dev"].RecordID != recordID {
		t.Fatalf("native SQLite restart lost the exact signed high-water: state=%+v err=%v", state, err)
	}
}

func TestLanesL315SignedDirectiveCrashBeforeCommitRemainsRetryable(t *testing.T) {
	fixture := l315FreshAnchor(t)
	payload := l315Directive(fixture, 7, 0, "candidate_admission", strings.Repeat("e", 64), l303Digest, "")
	raw, recordID := l315SignDirective(t, fixture, payload)
	fixture.acceptProbe(t)
	before, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	fixture.anchor.setFaultHooks(anchorFaultHooks{beforeCommit: func() error { return errors.New("simulated process crash before anchor commit") }})
	if _, err := fixture.anchor.ApplyIngressAdmissionDirective(raw); !errors.Is(err, ErrIngressDirectiveRejected) {
		t.Fatalf("injected pre-commit failure was not surfaced as directive rejection: %v", err)
	}
	if err := fixture.anchor.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSecurityAnchorAt(fixture.dir, nil, uint32(os.Geteuid()), func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatalf("reopen after failed directive commit: %v", err)
	}
	fixture.anchor = reopened
	after, err := reopened.Snapshot()
	if err != nil || after.Generation != before.Generation || len(after.DirectiveHighWater) != 0 || len(after.PendingCandidate) != 0 {
		t.Fatalf("failed signed directive left partial durable state: before=%+v after=%+v err=%v", before, after, err)
	}
	if applied, err := reopened.ApplyIngressAdmissionDirective(raw); err != nil || applied.RecordID != recordID {
		t.Fatalf("same signed directive could not be retried after restart: value=%+v err=%v", applied, err)
	}
}

func l315FreshAnchor(t *testing.T) *l305Fixture {
	t.Helper()
	fixture := newL305Fixture(t)
	issued := fixture.clock.Add(-time.Minute)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, issued, fixture.clock.Add(14*time.Minute))
	fixture.acceptProbe(t)
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatalf("install fresh fixture revocation ledger: %v", err)
	}
	return fixture
}

func l315Directive(fixture *l305Fixture, epoch uint64, generation uint8, role, candidate, configuration, predecessor string) ingressDirectivePayload {
	return ingressDirectivePayload{
		CandidateID: candidate, TransitionID: l315TransitionID, Lane: "dev", HostID: "linux-host",
		ConfigurationDigest: configuration, Role: role, TransitionEpoch: epoch,
		AdmissionGeneration: generation, NotBefore: fixture.clock.Add(-time.Minute).Format(time.RFC3339Nano),
		NotAfter: fixture.clock.Add(10 * time.Minute).Format(time.RFC3339Nano), RequiredLedgerGeneration: 1,
		PredecessorRecordID: predecessor,
	}
}

func l315SignDirective(t *testing.T, fixture *l305Fixture, payload ingressDirectivePayload) ([]byte, string) {
	t.Helper()
	raw, err := SignEnvelope("ingress_admission_directive", l302JSON(t, payload), SigningIdentity{
		KeyID: fixture.authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: fixture.authority,
	})
	if err != nil {
		t.Fatalf("sign fixture ingress directive: %v", err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode signed fixture envelope: %v", err)
	}
	return raw, envelope.RecordID
}

func l315SignAcceptance(t *testing.T, fixture *l305Fixture, directive ingressDirectivePayload) ([]byte, string) {
	t.Helper()
	state, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatalf("read fixture authority time for acceptance: %v", err)
	}
	return l315SignAcceptanceAt(t, fixture, directive, state.HighestAuthorityTime)
}

func l315SignAcceptanceAt(t *testing.T, fixture *l305Fixture, directive ingressDirectivePayload, acceptedAt time.Time) ([]byte, string) {
	t.Helper()
	payload := laneAcceptancePayload{
		CandidateID: directive.CandidateID, Lane: directive.Lane, TransitionID: directive.TransitionID,
		AttestationIDs: []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)},
		EvidenceDigest: l303Digest, AcceptedAt: acceptedAt.UTC().Format(time.RFC3339Nano),
	}
	raw, err := SignEnvelope("lane_acceptance", l302JSON(t, payload), SigningIdentity{
		KeyID: fixture.authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: fixture.authority,
	})
	if err != nil {
		t.Fatalf("sign fixture lane acceptance: %v", err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode signed fixture acceptance: %v", err)
	}
	return raw, envelope.RecordID
}

func l315Runtime(directive ingressDirectivePayload, recordID string) AnchorRuntimeContext {
	return AnchorRuntimeContext{Lane: directive.Lane, HostID: directive.HostID, TransitionID: directive.TransitionID,
		TransitionEpoch: directive.TransitionEpoch, CandidateManifestID: directive.CandidateID,
		ConfigurationDigest: directive.ConfigurationDigest, RecordID: recordID}
}
