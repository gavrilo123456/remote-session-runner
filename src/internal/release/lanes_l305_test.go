package release

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const l305TransitionID = "00000000-0000-7000-8000-000000000001"

type l305Fixture struct {
	anchor      *SecurityAnchor
	dir         string
	clock       time.Time
	authorityID string
	authority   ed25519.PrivateKey
	bootstrap   SecurityAnchorBootstrap
}

func newL305Fixture(t *testing.T) *l305Fixture {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Second)
	authorityID, authorityPrivate := l304PrivateKey(31)
	hostKeyID, hostPrivate := l304PrivateKey(32)
	bootstrap := SecurityAnchorBootstrap{
		HostID:                  "linux-host",
		AuthorityKey:            l304KeyState("", 0, strings.Repeat("a", 64), authorityID, authorityPrivate, start.Add(-time.Hour), start.Add(24*time.Hour)),
		HostAttestationKey:      l304KeyState("linux-host", 0, strings.Repeat("b", 64), hostKeyID, hostPrivate, start.Add(-time.Hour), start.Add(24*time.Hour)),
		BootstrapLedgerAnchorID: strings.Repeat("c", 64),
	}
	dir := filepath.Join(t.TempDir(), "security-anchor")
	fixture := &l305Fixture{dir: dir, clock: start, authorityID: authorityID, authority: authorityPrivate, bootstrap: bootstrap}
	anchor, err := openSecurityAnchorAt(dir, &bootstrap, uint32(os.Geteuid()), func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatalf("open isolated security anchor: %v", err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	fixture.anchor = anchor
	return fixture
}

func (f *l305Fixture) acceptProbe(t *testing.T) time.Time {
	t.Helper()
	challenge, err := f.anchor.BeginAuthorityTimeProbe()
	if err != nil {
		t.Fatalf("begin durable authority-time probe: %v", err)
	}
	f.clock = f.clock.Add(100 * time.Millisecond)
	response, err := SignTimeProbeResponse(TimeProbeBody{
		Version: ReleaseTimePolicyVersion, Nonce: challenge.Nonce,
		AuthorityUTCUnixMillis: f.clock.Add(-50 * time.Millisecond).UnixMilli(), SignerKeyID: f.authorityID,
	}, f.authority)
	if err != nil {
		t.Fatalf("sign authority-time probe: %v", err)
	}
	accepted, err := f.anchor.AcceptAuthorityTimeProbe(challenge.ID, response)
	if err != nil {
		t.Fatalf("accept authority-time probe: %v", err)
	}
	return accepted
}

func (f *l305Fixture) advanceLedger(t *testing.T, generation uint64, predecessor string, issued, expires time.Time) []byte {
	t.Helper()
	return l304LedgerEnvelope(t, generation, predecessor, issued, expires, []string{}, []string{}, f.authorityID, f.authority)
}

func TestLanesL305BootstrapUsesPrivateFullSyncSQLiteAndReopensExactState(t *testing.T) {
	fixture := newL305Fixture(t)
	state, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != 1 || state.Generation != 0 || state.HostID != "linux-host" || state.AuthorityKeys[0].Generation != 0 ||
		state.HostAttestationKeys[0].Generation != 0 || state.RevocationLedger.RecordID != fixture.bootstrap.BootstrapLedgerAnchorID {
		t.Fatalf("bootstrap state mismatch: %+v", state)
	}
	dirInfo, err := os.Stat(fixture.dir)
	if err != nil || dirInfo.Mode().Perm() != securityAnchorDirMode {
		t.Fatalf("anchor directory mode = %v, err=%v", dirInfo.Mode(), err)
	}
	dbPath := filepath.Join(fixture.dir, securityAnchorDBName)
	dbInfo, err := os.Stat(dbPath)
	if err != nil || dbInfo.Mode().Perm() != securityAnchorDBMode {
		t.Fatalf("anchor database mode = %v, err=%v", dbInfo.Mode(), err)
	}
	var journal string
	var synchronous int
	if err := fixture.anchor.conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := fixture.anchor.conn.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if journal != "delete" || synchronous != 3 {
		t.Fatalf("durability pragmas = journal %q synchronous %d", journal, synchronous)
	}
	if err := fixture.anchor.Close(); err != nil {
		t.Fatal(err)
	}
	owner := uint32(os.Geteuid())
	reopened, err := openSecurityAnchorAt(fixture.dir, nil, owner, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatalf("reopen persisted anchor: %v", err)
	}
	defer reopened.Close()
	reopenedState, err := reopened.Snapshot()
	if err != nil || reopenedState.HostID != state.HostID || reopenedState.AuthorityKeys[0].RecordID != state.AuthorityKeys[0].RecordID {
		t.Fatalf("reopened trust state mismatch: state=%+v err=%v", reopenedState, err)
	}
	if _, err := openSecurityAnchorAt(fixture.dir, &fixture.bootstrap, owner, func() time.Time { return fixture.clock }); !errors.Is(err, ErrSecurityAnchorInitialized) {
		t.Fatalf("second bootstrap did not fail closed: %v", err)
	}
}

func TestLanesL305TimeChallengeIsDurableSingleUseAndInvalidatedOnRestart(t *testing.T) {
	fixture := newL305Fixture(t)
	challenge, err := fixture.anchor.BeginAuthorityTimeProbe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.anchor.BeginAuthorityTimeProbe(); err == nil {
		t.Fatal("second uncorrelated time challenge was allowed concurrently")
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil || state.PendingProbes[challenge.ID].Challenge != challenge || state.PendingProbes[challenge.ID].WallStartedAt.IsZero() || state.PendingProbes[challenge.ID].MonotonicStartedNanos == 0 {
		t.Fatalf("time challenge was not durably correlated: state=%+v err=%v", state, err)
	}
	if err := fixture.anchor.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSecurityAnchorAt(fixture.dir, nil, uint32(os.Geteuid()), func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.Snapshot()
	if err != nil || len(state.PendingProbes) != 0 {
		t.Fatalf("restart retained a process-bound monotonic challenge: state=%+v err=%v", state, err)
	}
	response, err := SignTimeProbeResponse(TimeProbeBody{Version: 1, Nonce: challenge.Nonce,
		AuthorityUTCUnixMillis: fixture.clock.UnixMilli(), SignerKeyID: fixture.authorityID}, fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.AcceptAuthorityTimeProbe(challenge.ID, response); err == nil {
		t.Fatal("pre-restart time challenge was accepted")
	}
}

func TestLanesL305AnchorDirectiveHighWaterAndPendingCandidateSurviveRestart(t *testing.T) {
	fixture := newL305Fixture(t)
	issued := fixture.clock.Add(-time.Minute)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, issued, fixture.clock.Add(14*time.Minute))
	fixture.acceptProbe(t)
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatalf("install contiguous bootstrap successor ledger: %v", err)
	}
	fixture.acceptProbe(t)
	first := AnchorDirectiveHighWater{Lane: "dev", HostID: "linux-host", TransitionID: l305TransitionID, TransitionEpoch: 7,
		AdmissionGeneration: 1, LeaseGeneration: 1, Role: "candidate_admission", CandidateManifestID: strings.Repeat("e", 64),
		ConfigurationDigest: strings.Repeat("f", 64), RecordID: strings.Repeat("1", 64)}
	if err := fixture.anchor.RecordVerifiedDirective(first); err != nil {
		t.Fatalf("record verified directive high-water: %v", err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil || state.DirectiveHighWater["dev"] != first || state.PendingCandidate["dev"].RecordID != first.RecordID {
		t.Fatalf("high-water/pending context not committed atomically: state=%+v err=%v", state, err)
	}
	if err := fixture.anchor.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSecurityAnchorAt(fixture.dir, nil, uint32(os.Geteuid()), func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.Snapshot()
	if err != nil || state.DirectiveHighWater["dev"] != first || state.PendingCandidate["dev"].RecordID != first.RecordID {
		t.Fatalf("reopen lost monotonic directive state: state=%+v err=%v", state, err)
	}
	fixture.anchor = reopened
	fixture.acceptProbe(t)
	accepted := AnchorRuntimeContext{Lane: "dev", HostID: "linux-host", TransitionID: first.TransitionID, TransitionEpoch: first.TransitionEpoch,
		CandidateManifestID: first.CandidateManifestID, ConfigurationDigest: first.ConfigurationDigest, RecordID: strings.Repeat("2", 64)}
	if err := reopened.RecordVerifiedActiveRuntime(accepted); err != nil {
		t.Fatalf("verified acceptance did not advance the active runtime atomically: %v", err)
	}
	state, err = reopened.Snapshot()
	if err != nil || state.ActiveRuntime["dev"] != accepted || len(state.PendingCandidate) != 0 {
		t.Fatalf("accepted runtime and pending candidate did not commit together: state=%+v err=%v", state, err)
	}
	fixture.acceptProbe(t)
	lower := first
	lower.LeaseGeneration = 0
	lower.RecordID = strings.Repeat("2", 64)
	if err := reopened.RecordVerifiedDirective(lower); err == nil {
		t.Fatal("lower directive lease generation was accepted")
	}
	state, err = reopened.Snapshot()
	if err != nil || state.DirectiveHighWater["dev"] != first {
		t.Fatalf("lower directive changed the persisted high-water: state=%+v err=%v", state, err)
	}
}

func TestLanesL305CrashBeforeCommitLeavesOnlyLastDurableAnchorState(t *testing.T) {
	fixture := newL305Fixture(t)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, fixture.clock.Add(-time.Minute), fixture.clock.Add(14*time.Minute))
	fixture.acceptProbe(t)
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatal(err)
	}
	fixture.acceptProbe(t)
	before, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	fixture.anchor.setFaultHooks(anchorFaultHooks{beforeCommit: func() error { return errors.New("simulated crash before SQLite commit") }})
	value := AnchorDirectiveHighWater{Lane: "dev", HostID: "linux-host", TransitionID: l305TransitionID, TransitionEpoch: 1,
		AdmissionGeneration: 0, LeaseGeneration: 1, Role: "candidate_admission", CandidateManifestID: strings.Repeat("e", 64),
		ConfigurationDigest: strings.Repeat("f", 64), RecordID: strings.Repeat("1", 64)}
	if err := fixture.anchor.RecordVerifiedDirective(value); err == nil {
		t.Fatal("simulated crash was reported as a durable anchor update")
	}
	after, err := fixture.anchor.Snapshot()
	if err != nil || after.Generation != before.Generation || after.DirectiveHighWater["dev"].RecordID != "" {
		t.Fatalf("pre-commit failure changed live high-water state: before=%+v after=%+v err=%v", before, after, err)
	}
	if err := fixture.anchor.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSecurityAnchorAt(fixture.dir, nil, uint32(os.Geteuid()), func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatalf("reopen after pre-commit failure: %v", err)
	}
	defer reopened.Close()
	recovered, err := reopened.Snapshot()
	if err != nil || recovered.Generation != before.Generation || recovered.DirectiveHighWater["dev"].RecordID != "" {
		t.Fatalf("restart did not retain last durable state: state=%+v err=%v", recovered, err)
	}
}

func TestLanesL305RejectsAnchorCorruptionSymlinkAndWrongModes(t *testing.T) {
	t.Run("state digest tamper", func(t *testing.T) {
		fixture := newL305Fixture(t)
		if err := fixture.anchor.Close(); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(fixture.dir, securityAnchorDBName)
		db, err := sql.Open("sqlite", fmt.Sprintf("file:%s", dbPath))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE anchor_state SET state_json=? WHERE singleton=1", []byte(`{"schema_version":1,"generation":0}`)); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := openSecurityAnchorAt(fixture.dir, nil, uint32(os.Geteuid()), func() time.Time { return fixture.clock }); !errors.Is(err, ErrSecurityAnchorRollback) {
			t.Fatalf("tampered state was accepted: %v", err)
		}
	})
	t.Run("symlink directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := openSecurityAnchorAt(link, nil, uint32(os.Geteuid()), time.Now); err == nil {
			t.Fatal("symlink anchor directory was accepted")
		}
	})
	t.Run("wrong directory mode", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "anchor")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := openSecurityAnchorAt(dir, nil, uint32(os.Geteuid()), time.Now); err == nil {
			t.Fatal("world-readable anchor directory was accepted")
		}
	})
}

func TestLanesL305StaleLedgerAllowsOnlyFreshContiguousTrustRecovery(t *testing.T) {
	t.Run("fresh contiguous successor", func(t *testing.T) {
		fixture := newL305Fixture(t)
		fixture.acceptProbe(t)
		candidate := []byte(`{"record_type":"candidate_manifest"}`)
		if err := fixture.anchor.ApplyTrustRecord(candidate); err == nil {
			t.Fatal("stale ledger accepted a candidate record")
		}
		state, err := fixture.anchor.Snapshot()
		if err != nil || state.RevocationLedger.Generation != 0 || state.ProbeGeneration != state.ConsumedProbeGeneration {
			t.Fatalf("rejected stale-ledger mutation changed trust state or retained its one-use probe: state=%+v err=%v", state, err)
		}
		fixture.acceptProbe(t)
		hostKeySet := []byte(`{"record_type":"host_attestation_key_set"}`)
		if err := fixture.anchor.ApplyTrustRecord(hostKeySet); err == nil {
			t.Fatal("stale ledger accepted a host-attestation key set outside its recovery exception")
		}
		fixture.acceptProbe(t)
		ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, fixture.clock.Add(-time.Minute), fixture.clock.Add(14*time.Minute))
		if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
			t.Fatalf("fresh probe could not recover with exact successor ledger: %v", err)
		}
		if err := fixture.anchor.RequireFreshLedger(); err != nil {
			t.Fatalf("committed successor did not reopen mutation freshness: %v", err)
		}
	})
	t.Run("skipped fork expired and missing proof", func(t *testing.T) {
		cases := []struct {
			name   string
			record func(*testing.T, *l305Fixture) []byte
			probe  bool
		}{
			{name: "missing time proof", record: func(t *testing.T, f *l305Fixture) []byte {
				return f.advanceLedger(t, 1, f.bootstrap.BootstrapLedgerAnchorID, f.clock.Add(-time.Minute), f.clock.Add(14*time.Minute))
			}},
			{name: "skipped generation", probe: true, record: func(t *testing.T, f *l305Fixture) []byte {
				return f.advanceLedger(t, 2, f.bootstrap.BootstrapLedgerAnchorID, f.clock.Add(-time.Minute), f.clock.Add(14*time.Minute))
			}},
			{name: "forked predecessor", probe: true, record: func(t *testing.T, f *l305Fixture) []byte {
				return f.advanceLedger(t, 1, strings.Repeat("d", 64), f.clock.Add(-time.Minute), f.clock.Add(14*time.Minute))
			}},
			{name: "expired successor", probe: true, record: func(t *testing.T, f *l305Fixture) []byte {
				return f.advanceLedger(t, 1, f.bootstrap.BootstrapLedgerAnchorID, f.clock.Add(-20*time.Minute), f.clock.Add(-5*time.Minute))
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fixture := newL305Fixture(t)
				if tc.probe {
					fixture.acceptProbe(t)
				}
				if err := fixture.anchor.ApplyTrustRecord(tc.record(t, fixture)); err == nil {
					t.Fatal("invalid stale-ledger recovery record was accepted")
				}
				state, err := fixture.anchor.Snapshot()
				if err != nil || state.RevocationLedger.Generation != 0 {
					t.Fatalf("failed recovery changed ledger high-water: state=%+v err=%v", state, err)
				}
			})
		}
	})
}

func TestLanesL305StaleLedgerAllowsContiguousAuthorityKeySuccessorOnly(t *testing.T) {
	fixture := newL305Fixture(t)
	acceptedAt := fixture.acceptProbe(t)
	previous := fixture.bootstrap.AuthorityKey
	nextID, nextPrivate := l304PrivateKey(33)
	keySet := l304KeySetEnvelope(t, "authority_key_set", "", 1, previous, nextID, nextPrivate.Public().(ed25519.PublicKey), nextPrivate,
		acceptedAt.Add(time.Hour), acceptedAt.Add(-time.Minute), acceptedAt.Add(12*time.Hour),
		SigningIdentity{KeyID: fixture.authorityID, Role: SignerRolePreviousAuthorityKey, PrivateKey: fixture.authority})
	if err := fixture.anchor.ApplyTrustRecord(keySet); err != nil {
		t.Fatalf("fresh probe did not allow exact authority-key successor recovery: %v", err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil || len(state.AuthorityKeys) != 2 || state.AuthorityKeys[1].KeyID != nextID || state.AuthorityKeys[0].NotAfter != acceptedAt.Add(time.Hour) {
		t.Fatalf("authority successor high-water not retained: state=%+v err=%v", state, err)
	}
	if err := fixture.anchor.RequireFreshLedger(); err == nil {
		t.Fatal("authority-key recovery incorrectly reopened mutation with a stale ledger")
	}
	fixture.authorityID = nextID
	fixture.authority = nextPrivate
	acceptedAt = fixture.acceptProbe(t)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, acceptedAt.Add(-time.Minute), acceptedAt.Add(14*time.Minute))
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatalf("successor authority could not advance the stale ledger: %v", err)
	}
	if err := fixture.anchor.RequireFreshLedger(); err != nil {
		t.Fatalf("contiguous successor ledger did not reopen mutation: %v", err)
	}
}

func TestLanesL305RejectsRestoredLowerOrForkedHighWaterSnapshot(t *testing.T) {
	fixture := newL305Fixture(t)
	fixture.acceptProbe(t)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, fixture.clock.Add(-time.Minute), fixture.clock.Add(14*time.Minute))
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatal(err)
	}
	fixture.acceptProbe(t)
	value := AnchorDirectiveHighWater{Lane: "dev", HostID: "linux-host", TransitionID: l305TransitionID, TransitionEpoch: 9,
		AdmissionGeneration: 1, LeaseGeneration: 1, Role: "candidate_admission", CandidateManifestID: strings.Repeat("e", 64),
		ConfigurationDigest: strings.Repeat("f", 64), RecordID: strings.Repeat("1", 64)}
	if err := fixture.anchor.RecordVerifiedDirective(value); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.anchor.ValidateRestoreHighWater(state); err != nil {
		t.Fatalf("exact retained high-water snapshot refused: %v", err)
	}
	rollback, err := cloneSecurityAnchorState(state)
	if err != nil {
		t.Fatal(err)
	}
	rollback.DirectiveHighWater["dev"] = AnchorDirectiveHighWater{Lane: "dev", HostID: "linux-host", TransitionID: l305TransitionID, TransitionEpoch: 8,
		AdmissionGeneration: 1, LeaseGeneration: 1, Role: "candidate_admission", CandidateManifestID: strings.Repeat("e", 64),
		ConfigurationDigest: strings.Repeat("f", 64), RecordID: strings.Repeat("2", 64)}
	rollback.PendingCandidate["dev"] = AnchorRuntimeContext{Lane: "dev", HostID: "linux-host", TransitionID: l305TransitionID, TransitionEpoch: 8,
		CandidateManifestID: strings.Repeat("e", 64), ConfigurationDigest: strings.Repeat("f", 64), RecordID: strings.Repeat("2", 64)}
	if err := fixture.anchor.ValidateRestoreHighWater(rollback); !errors.Is(err, ErrSecurityAnchorRollback) {
		t.Fatalf("lower continuity high-water was not rejected: %v", err)
	}
	fork, err := cloneSecurityAnchorState(state)
	if err != nil {
		t.Fatal(err)
	}
	forkedDirective := value
	forkedDirective.RecordID = strings.Repeat("3", 64)
	fork.DirectiveHighWater["dev"] = forkedDirective
	if err := fixture.anchor.ValidateRestoreHighWater(fork); !errors.Is(err, ErrSecurityAnchorRollback) {
		t.Fatalf("same-tuple conflicting directive was not rejected: %v", err)
	}
}

func TestLanesL305NativeFsyncAndLowerSnapshotFailClosed(t *testing.T) {
	fixture := newL305Fixture(t)
	fixture.acceptProbe(t)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID, fixture.clock.Add(-time.Minute), fixture.clock.Add(14*time.Minute))
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatal(err)
	}
	if err := syncAnchorFiles(filepath.Join(fixture.dir, securityAnchorDBName), fixture.dir); err != nil {
		t.Fatalf("native database and directory fsync failed: %v", err)
	}
	current, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rolledBack := current
	rolledBack.RevocationLedger = RevocationLedgerState{RecordID: fixture.bootstrap.BootstrapLedgerAnchorID, RevokedRecordIDs: []string{}, RevokedKeyIDs: []string{}}
	if err := fixture.anchor.ValidateRestoreHighWater(rolledBack); !errors.Is(err, ErrSecurityAnchorRollback) {
		t.Fatalf("native lower-ledger snapshot was not rejected: %v", err)
	}
}
