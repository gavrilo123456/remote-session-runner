package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneadmission"
	"remote-session-runner/src/internal/testfixture"
)

func TestLanesL111LocalIntentAdmissionFailsClosedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/l111-local-intent.db"
	db, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostMacControlPlane); err != nil {
		t.Fatal(err)
	}
	input := p057SubmitIntent(t, "intent-l111-local", "session-l111-local", "command-l111-local", "key-l111-local", 1, "echo l111")
	if _, _, err := authority.AcceptLocalIntent(ctx, input); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("closed local intent error=%v", err)
	}
	l111OpenAdmission(t, authority, "transition-l111-1", "candidate-l111-1", 1)
	if _, duplicate, err := authority.AcceptLocalIntent(ctx, input); err != nil || duplicate {
		t.Fatalf("open local intent duplicate=%v err=%v", duplicate, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedDB, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStore(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostMacControlPlane); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.AcceptLocalIntent(ctx, input); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("restarted idempotent local intent error=%v", err)
	}
	var count int
	if err := reopenedDB.QueryRowContext(ctx, `SELECT count(*) FROM local_intents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("local intent count after closed replay=%d, want one original receipt", count)
	}
}

func TestLanesL111MailboxReceiptAdmissionPreservesKnownReadsAndRetries(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	db, err := Open(ctx, root.Path()+"/state/l111-mailbox.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostMacControlPlane); err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "fixture-l111")
	if err != nil {
		t.Fatal(err)
	}
	mutationRaw := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`)
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", mutationRaw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mutationHash, err := domain.HashMutationRequestJSON("create_session", mutationRaw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mutation := func(requestID string) (MailboxExchangeRef, MailboxExchangeCreate) {
		ref, err := NewMailboxExchangeRef("analytics", requestID)
		if err != nil {
			t.Fatal(err)
		}
		return ref, MailboxExchangeCreate{
			MailboxID: ref.MailboxID, RequestID: ref.ClientRequestID, Operation: "create_session",
			Controller: controller, IdempotencyKey: "key-" + requestID,
			RequestHash: mutationHash, CanonicalPayload: canonical,
		}
	}
	blockedRef, blockedInput := mutation("req-l111-blocked")
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, blockedRef, blockedInput); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("closed mutation receipt error=%v", err)
	}
	if _, err := authority.GetMailboxExchangeInMailbox(ctx, blockedRef); !errors.Is(err, ErrMailboxExchangeNotFound) {
		t.Fatalf("closed mutation left a receipt: %v", err)
	}

	readPayload := []byte(`{"session_id":"session-l111-read"}`)
	readDigest := sha256.Sum256(readPayload)
	readHash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, readDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	readRef, err := NewMailboxExchangeRef("analytics", "req-l111-read")
	if err != nil {
		t.Fatal(err)
	}
	readRecord, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, readRef, MailboxExchangeCreate{
		MailboxID: readRef.MailboxID, RequestID: readRef.ClientRequestID, Operation: "get_session",
		Controller: controller, RequestHash: readHash, CanonicalPayload: readPayload,
	})
	if err != nil || duplicate || readRecord.State != MailboxExchangeAccepted {
		t.Fatalf("read-only receipt=%+v duplicate=%v err=%v", readRecord, duplicate, err)
	}

	open := l111OpenAdmission(t, authority, "transition-l111-1", "candidate-l111-1", 1)
	accepted, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, blockedRef, blockedInput)
	if err != nil || duplicate || accepted.State != MailboxExchangeAccepted {
		t.Fatalf("opened mutation receipt=%+v duplicate=%v err=%v", accepted, duplicate, err)
	}
	closure := l111Identity("transition-l111-2", open.CandidateManifestID, laneadmission.RoleActiveRuntimeClosure, 2)
	if err := authority.BeginLaneAdmissionClosure(ctx, closure); err != nil {
		t.Fatal(err)
	}
	replay, duplicate, err := authority.AcceptMailboxExchangeInMailbox(ctx, blockedRef, blockedInput)
	if err != nil || !duplicate || replay.ExchangeID != accepted.ExchangeID {
		t.Fatalf("exact retained receipt replay=%+v duplicate=%v err=%v", replay, duplicate, err)
	}
	newRef, newInput := mutation("req-l111-after-close")
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, newRef, newInput); !errors.Is(err, ErrLaneAdmissionClosed) {
		t.Fatalf("new mutation while closed error=%v", err)
	}
	readRef2, err := NewMailboxExchangeRef("analytics", "req-l111-read-after-close")
	if err != nil {
		t.Fatal(err)
	}
	readHash2, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, readDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, readRef2, MailboxExchangeCreate{
		MailboxID: readRef2.MailboxID, RequestID: readRef2.ClientRequestID, Operation: "get_session",
		Controller: controller, RequestHash: readHash2, CanonicalPayload: readPayload,
	}); err != nil {
		t.Fatalf("read-only mailbox receipt while closed: %v", err)
	}
}

func l111Identity(transition, candidate string, role laneadmission.Role, epoch uint64) laneadmission.Identity {
	return laneadmission.Identity{
		Lane: laneadmission.LaneDev, HostID: laneadmission.HostMacControlPlane,
		TransitionID: transition, CandidateManifestID: candidate,
		ConfigurationDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Role:                role, Epoch: epoch,
	}
}

func l111OpenAdmission(t *testing.T, authority *AuthorityStore, transition, candidate string, epoch uint64) laneadmission.Identity {
	t.Helper()
	identity := l111Identity(transition, candidate, laneadmission.RoleCandidateAdmission, epoch)
	ctx := context.Background()
	if err := authority.PrepareLaneAdmissionCandidate(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if err := authority.SwitchLaneAdmissionCandidate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdvanceLaneAdmissionCandidate(ctx, identity, laneadmission.PhaseTransportReady); err != nil {
		t.Fatal(err)
	}
	if err := authority.AdvanceLaneAdmissionCandidate(ctx, identity, laneadmission.PhaseOpen); err != nil {
		t.Fatal(err)
	}
	return identity
}
