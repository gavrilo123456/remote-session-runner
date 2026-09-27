package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP090DurableRequestReceiptRemovesPairAndReplaysAfterCommit(t *testing.T) {
	root := p081MailboxRoot(t)
	db, err := store.Open(context.Background(), filepath.Join(root, "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-p090-replay"
	writeP082MailboxPair(t, importer, requestID, "key-p090-replay", "echo durable")
	callbackCount := 0
	processor, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner, Handler: func(context.Context, Request) error {
		callbackCount++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(importer.InboxPath(), requestID+RequestSuffix))
	if err != nil {
		t.Fatal(err)
	}
	request, err := importer.validateRequest(requestID, raw)
	if err != nil {
		t.Fatal(err)
	}
	// Model a process stop after the terminal SQLite commit and before the
	// importer removes either file in the pair.
	durable, err := processor.process(context.Background(), request)
	if err != nil || !durable || callbackCount != 1 {
		t.Fatalf("pre-cleanup receipt durable=%v callbacks=%d err=%v", durable, callbackCount, err)
	}
	if results, err := processor.Import(context.Background()); err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("replayed receipt results=%+v err=%v", results, err)
	}
	if callbackCount != 1 {
		t.Fatalf("terminal receipt replay invoked handler %d times", callbackCount)
	}
	assertMailboxPairAbsent(t, importer.InboxPath(), requestID)
	record, err := authority.GetMailboxExchange(context.Background(), requestID)
	if err != nil || record.State != store.MailboxExchangeComplete {
		t.Fatalf("durable exchange=%+v err=%v", record, err)
	}
}

func TestP090RequestPairWaitsForDurableOutcome(t *testing.T) {
	root := p081MailboxRoot(t)
	db, err := store.Open(context.Background(), filepath.Join(root, "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-p090-pending"
	writeP082MailboxPair(t, importer, requestID, "key-p090-pending", "echo pending")
	processor, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner})
	if err != nil {
		t.Fatal(err)
	}
	results, err := processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("pending receipt results=%+v err=%v", results, err)
	}
	assertMailboxPairPresent(t, importer.InboxPath(), requestID)
	record, err := authority.GetMailboxExchange(context.Background(), requestID)
	if err != nil || record.State != store.MailboxExchangeAccepted {
		t.Fatalf("pending exchange=%+v err=%v", record, err)
	}
}

func TestP090DurableRejectionRemovesImportedRequestPair(t *testing.T) {
	root := p081MailboxRoot(t)
	db, err := store.Open(context.Background(), filepath.Join(root, "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-p090-rejected"
	writeP082MailboxPair(t, importer, requestID, "key-p090-rejected", "echo rejected")
	processor, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner, Handler: func(context.Context, Request) error {
		return errors.New("durable operation rejection")
	}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("durable rejection results=%+v err=%v", results, err)
	}
	assertMailboxPairAbsent(t, importer.InboxPath(), requestID)
	record, err := authority.GetMailboxExchange(context.Background(), requestID)
	if err != nil || record.State != store.MailboxExchangeRejected {
		t.Fatalf("rejected exchange=%+v err=%v", record, err)
	}
}

func TestP090AckImporterRemovesOnlyDurablyMatchedPair(t *testing.T) {
	root := p081MailboxRoot(t)
	db, err := store.Open(context.Background(), filepath.Join(root, "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"req-p090-ack", "req-p090-wrong"} {
		p090CreateTerminalExchange(t, authority, requestID)
	}
	clockTime := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	ackImporter, err := NewAckImporter(AckImporterOptions{Root: root, Authority: authority, Clock: func() time.Time { return clockTime }})
	if err != nil {
		t.Fatal(err)
	}
	writeP090AckPair(t, ackImporter, "req-p090-ack", 1, int64Ptr(3))
	// Model a stop just after the SQLite ACK transaction. Re-import observes
	// the exact duplicate as durable and completes the pair removal.
	ackRecord, err := authority.AcknowledgeMailboxExchange(context.Background(), store.MailboxAcknowledgement{RequestID: "req-p090-ack", ResponseRevision: 1, AvailableEventSequence: int64Ptr(3)})
	if err != nil || ackRecord.AcknowledgedAt == nil {
		t.Fatalf("pre-cleanup ACK=%+v err=%v", ackRecord, err)
	}
	results, err := ackImporter.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultAccepted || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("durable ACK import results=%+v err=%v", results, err)
	}
	assertMailboxPairAbsent(t, ackImporter.AcksPath(), "req-p090-ack")

	writeP090AckPair(t, ackImporter, "req-p090-wrong", 1, int64Ptr(2))
	results, err = ackImporter.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("mismatched ACK import results=%+v err=%v", results, err)
	}
	assertMailboxPairPresent(t, ackImporter.AcksPath(), "req-p090-wrong")
	wrong, err := authority.GetMailboxExchange(context.Background(), "req-p090-wrong")
	if err != nil || wrong.AcknowledgedAt != nil {
		t.Fatalf("mismatched ACK receipt=%+v err=%v", wrong, err)
	}

	writeMailboxFile(t, filepath.Join(ackImporter.AcksPath(), "req-p090-bad.json"), []byte(`{"request_id":"req-p090-bad","response_revision":1,"extra":true}`), 0o600)
	writeMailboxFile(t, filepath.Join(ackImporter.AcksPath(), "req-p090-bad.ready"), nil, 0o600)
	results, err = ackImporter.Import(context.Background())
	if err != nil || len(results) != 2 {
		t.Fatalf("invalid and mismatched ACK results=%+v err=%v", results, err)
	}
	assertMailboxPairPresent(t, ackImporter.AcksPath(), "req-p090-bad")
}

func TestP090AckImporterPreservesNilAndZeroCursorSemantics(t *testing.T) {
	root := p081MailboxRoot(t)
	db, err := store.Open(context.Background(), filepath.Join(root, "state", "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	p090CreateExchangeWithCursor(t, authority, "req-p090-no-cursor", nil)
	p090CreateExchangeWithCursor(t, authority, "req-p090-zero-cursor", int64Ptr(0))
	ackImporter, err := NewAckImporter(AckImporterOptions{Root: root, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	writeP090AckPair(t, ackImporter, "req-p090-no-cursor", 1, nil)
	writeP090AckPair(t, ackImporter, "req-p090-zero-cursor", 1, int64Ptr(0))
	results, err := ackImporter.Import(context.Background())
	if err != nil || len(results) != 2 {
		t.Fatalf("nil/zero cursor ACK results=%+v err=%v", results, err)
	}
	for _, requestID := range []string{"req-p090-no-cursor", "req-p090-zero-cursor"} {
		record, err := authority.GetMailboxExchange(context.Background(), requestID)
		if err != nil || record.AcknowledgedAt == nil {
			t.Fatalf("ACK receipt %s=%+v err=%v", requestID, record, err)
		}
		assertMailboxPairAbsent(t, ackImporter.AcksPath(), requestID)
	}
}

func TestP090FakeClockCleansOnlyOldSafeUnmarkedDrafts(t *testing.T) {
	root := p081MailboxRoot(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	importer, err := New(Options{Root: root, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	inbox, acks := importer.InboxPath(), filepath.Join(root, "acks")
	old := now.Add(-UnmarkedDraftLifetime)
	writeP090Draft(t, filepath.Join(inbox, "req-old-inbox.json"), old, 0o600)
	writeP090Draft(t, filepath.Join(acks, "req-old-ack.json"), old, 0o600)
	writeP090Draft(t, filepath.Join(inbox, "req-recent.json"), now.Add(-23*time.Hour), 0o600)
	writeP090Draft(t, filepath.Join(inbox, "req-marked.json"), old, 0o600)
	writeMailboxFile(t, filepath.Join(inbox, "req-marked.ready"), nil, 0o600)
	writeP090Draft(t, filepath.Join(inbox, "req-wrong-mode.json"), old, 0o644)
	writeP090Draft(t, filepath.Join(inbox, "bad name.json"), old, 0o600)
	writeMailboxFile(t, filepath.Join(inbox, "req-directory.json"), nil, 0o600)
	if err := os.Remove(filepath.Join(inbox, "req-directory.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(inbox, "req-directory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.json")
	writeP090Draft(t, outside, old, 0o600)
	if err := os.Symlink(outside, filepath.Join(inbox, "req-link.json")); err != nil {
		t.Fatal(err)
	}
	if err := ensureOwnerDirectory(filepath.Join(root, "outbox")); err != nil {
		t.Fatal(err)
	}
	writeP090Draft(t, filepath.Join(root, "outbox", "req-outbox.json"), old, 0o600)
	writeP090Draft(t, filepath.Join(inbox, "req-temp.tmp"), old, 0o600)

	removed, err := importer.CleanupUnmarkedDrafts(context.Background())
	if err != nil || removed != 2 {
		t.Fatalf("removed drafts=%d err=%v", removed, err)
	}
	for _, path := range []string{
		filepath.Join(inbox, "req-old-inbox.json"), filepath.Join(acks, "req-old-ack.json"),
	} {
		assertPathAbsent(t, path)
	}
	for _, path := range []string{
		filepath.Join(inbox, "req-recent.json"), filepath.Join(inbox, "req-marked.json"),
		filepath.Join(inbox, "req-marked.ready"), filepath.Join(inbox, "req-wrong-mode.json"),
		filepath.Join(inbox, "bad name.json"), filepath.Join(inbox, "req-directory.json"),
		filepath.Join(inbox, "req-link.json"), outside, filepath.Join(root, "outbox", "req-outbox.json"),
		filepath.Join(inbox, "req-temp.tmp"),
	} {
		assertPathPresent(t, path)
	}
	if _, err := os.Lstat(filepath.Join(inbox, "req-link.json")); err != nil {
		t.Fatalf("draft cleanup followed/removed symlink: %v", err)
	}
}

func TestP090PairRemovalIsIdempotentAfterMarkerUnlink(t *testing.T) {
	root := p081MailboxRoot(t)
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-p090-partial"
	writeP082MailboxPair(t, importer, requestID, "key-p090-partial", "true")
	marker := filepath.Join(importer.InboxPath(), requestID+ReadySuffix)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := removeMailboxPair(importer.InboxPath(), requestID); err != nil {
		t.Fatal(err)
	}
	assertMailboxPairAbsent(t, importer.InboxPath(), requestID)
}

func p090CreateTerminalExchange(t *testing.T, authority *store.AuthorityStore, requestID string) {
	t.Helper()
	p090CreateExchangeWithCursor(t, authority, requestID, int64Ptr(3))
}

func p090CreateExchangeWithCursor(t *testing.T, authority *store.AuthorityStore, requestID string, cursor *int64) {
	t.Helper()
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	raw := p081RunJSON(t, requestID, "echo p090")
	request := Request{RequestID: requestID, IdempotencyKey: "key-" + requestID, Operation: "run", RawJSON: raw}
	canonical, hash, err := receiptCanonical(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(context.Background(), store.MailboxExchangeCreate{
		RequestID: requestID, Operation: request.Operation, Controller: owner,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: canonical,
	}); err != nil {
		t.Fatal(err)
	}
	responseValue := map[string]any{"request_id": requestID, "request_state": "complete", "response_revision": 1}
	if cursor != nil {
		responseValue["available_event_sequence"] = *cursor
	}
	response, err := json.Marshal(responseValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.PublishMailboxResponse(context.Background(), requestID, store.MailboxResponsePublication{State: store.MailboxExchangeComplete, Bytes: response, AvailableEventSequence: cursor}); err != nil {
		t.Fatal(err)
	}
}

func writeP090AckPair(t *testing.T, importer *AckImporter, requestID string, revision int64, cursor *int64) {
	t.Helper()
	value := map[string]any{"request_id": requestID, "response_revision": revision}
	if cursor != nil {
		value["available_event_sequence"] = *cursor
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.AcksPath(), requestID+RequestSuffix), raw, 0o600)
	writeMailboxFile(t, filepath.Join(importer.AcksPath(), requestID+ReadySuffix), nil, 0o600)
}

func writeP090Draft(t *testing.T, path string, modTime time.Time, mode os.FileMode) {
	t.Helper()
	writeMailboxFile(t, path, []byte("draft"), mode)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func assertMailboxPairAbsent(t *testing.T, directory, requestID string) {
	t.Helper()
	assertPathAbsent(t, filepath.Join(directory, requestID+ReadySuffix))
	assertPathAbsent(t, filepath.Join(directory, requestID+RequestSuffix))
}

func assertMailboxPairPresent(t *testing.T, directory, requestID string) {
	t.Helper()
	assertPathPresent(t, filepath.Join(directory, requestID+ReadySuffix))
	assertPathPresent(t, filepath.Join(directory, requestID+RequestSuffix))
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q remains or cannot be inspected: %v", path, err)
	}
}

func assertPathPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("path %q missing: %v", path, err)
	}
}

func int64Ptr(value int64) *int64 { return &value }
