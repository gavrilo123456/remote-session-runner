package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/laneadmission"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestLanesL111MailboxRequestImportRetainsPairUntilOpen(t *testing.T) {
	processor, operations, authority, _ := newP153ProcessorHarness(t, newP153Resolver(t))
	ctx := context.Background()
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostMacControlPlane); err != nil {
		t.Fatal(err)
	}
	request := p153ProcessorRequest(t, "req-l111-create", "key-l111-create", "create_session", map[string]any{
		"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
	})
	writeMailboxFile(t, filepath.Join(processor.importer.InboxPath(), request.RequestID+RequestSuffix), request.RawJSON, MailboxFileMode)
	writeMailboxFile(t, filepath.Join(processor.importer.InboxPath(), request.RequestID+ReadySuffix), nil, MailboxFileMode)
	ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	results, err := processor.Import(ctx)
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("closed request import results=%+v err=%v", results, err)
	}
	assertMailboxPairPresent(t, processor.importer.InboxPath(), request.RequestID)
	if len(operations.createRequests) != 0 {
		t.Fatalf("closed admission invoked create operation %d times", len(operations.createRequests))
	}
	if _, err := authority.GetMailboxExchangeInMailbox(ctx, ref); !errors.Is(err, store.ErrMailboxExchangeNotFound) {
		t.Fatalf("closed request created a durable exchange: %v", err)
	}

	identity := l111MailboxOpenAdmission(t, authority, "transition-l111-mailbox", "candidate-l111-mailbox", 1)
	results, err = processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("open request retry results=%+v err=%v", results, err)
	}
	assertMailboxPairAbsent(t, processor.importer.InboxPath(), request.RequestID)
	if len(operations.createRequests) != 1 {
		t.Fatalf("open request operation calls=%d, want one", len(operations.createRequests))
	}
	if err := authority.RevalidateLaneAdmissionCandidate(ctx, identity); err != nil {
		t.Fatalf("exact candidate remains revalidatable: %v", err)
	}
}

func TestLanesL111AckImporterRetainsPairUntilAdmissionOpen(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t).Path()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(root, "state", "l111-ack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	const requestID = "req-l111-ack"
	p090CreateTerminalExchange(t, authority, requestID)
	ackImporter, err := NewAckImporter(AckImporterOptions{Root: root, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	writeP090AckPair(t, ackImporter, requestID, 1, int64Ptr(3))
	if err := authority.ConfigureLaneAdmission(ctx, laneadmission.LaneDev, laneadmission.HostMacControlPlane); err != nil {
		t.Fatal(err)
	}
	if _, err := ackImporter.Import(ctx); !errors.Is(err, store.ErrLaneAdmissionClosed) {
		t.Fatalf("closed ACK import error=%v", err)
	}
	assertMailboxPairPresent(t, ackImporter.AcksPath(), requestID)
	before, err := authority.GetMailboxExchange(ctx, requestID)
	if err != nil || before.AcknowledgedAt != nil {
		t.Fatalf("closed ACK changed durable receipt=%+v err=%v", before, err)
	}

	l111MailboxOpenAdmission(t, authority, "transition-l111-ack", "candidate-l111-ack", 1)
	results, err := ackImporter.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("open ACK retry results=%+v err=%v", results, err)
	}
	assertMailboxPairAbsent(t, ackImporter.AcksPath(), requestID)
	after, err := authority.GetMailboxExchange(ctx, requestID)
	if err != nil || after.AcknowledgedAt == nil {
		t.Fatalf("open ACK retry receipt=%+v err=%v", after, err)
	}
}

func TestLanesL111V1ResponseSchemaStaysUnchanged(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	valid := []byte(`{"request_id":"req-l111-schema","operation":"create_session","request_state":"rejected","response_revision":1,"error":{"code":"runtime_unavailable","message":"retry after admission opens","retryable":true}}`)
	value, err := p004MailboxDecode(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled["response"].Validate(value); err != nil {
		t.Fatalf("existing V1 retry response no longer validates: %v", err)
	}
	var withAttestation map[string]any
	if err := json.Unmarshal(valid, &withAttestation); err != nil {
		t.Fatal(err)
	}
	withAttestation["release_attestation"] = map[string]any{"release_lane": "dev"}
	unsafe, err := json.Marshal(withAttestation)
	if err != nil {
		t.Fatal(err)
	}
	value, err = p004MailboxDecode(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled["response"].Validate(value); err == nil {
		t.Fatalf("V1 response schema unexpectedly accepted a V3 attestation: %s", unsafe)
	}
}

func l111MailboxOpenAdmission(t *testing.T, authority *store.AuthorityStore, transition, candidate string, epoch uint64) laneadmission.Identity {
	t.Helper()
	identity := laneadmission.Identity{
		Lane: laneadmission.LaneDev, HostID: laneadmission.HostMacControlPlane,
		TransitionID: transition, CandidateManifestID: candidate,
		ConfigurationDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Role:                laneadmission.RoleCandidateAdmission, Epoch: epoch,
	}
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
