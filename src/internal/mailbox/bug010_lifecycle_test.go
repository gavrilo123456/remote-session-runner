package mailbox

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG010LifecycleClassifierCountsOnlyUndurableCompleteRequestPairs(t *testing.T) {
	ctx := context.Background()
	root := p081MailboxRoot(t)
	if err := os.Mkdir(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(root, "state", "bug010.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	classifier, err := NewLifecycleClassifier(LifecycleClassifierOptions{
		MailboxID: store.DefaultMailboxID, Root: root, Authority: authority,
	})
	if err != nil {
		t.Fatal(err)
	}

	writeP090RequestPair(t, importer, "req-b010-native", "key-b010-native", "echo native", MailboxFileMode)
	writeP090RequestPair(t, importer, "req-b010-workspace", "key-b010-workspace", "echo workspace", MailboxWorkspaceIngressFileMode)
	writeP090RequestPair(t, importer, "req-b010-accepted", "key-b010-accepted", "echo accepted", MailboxFileMode)
	b010AcceptExchange(t, authority, store.DefaultMailboxID, "req-b010-accepted")

	// An exchange in another configured namespace must not suppress a fresh
	// pair in this mailbox.
	writeP090RequestPair(t, importer, "req-b010-cross-mailbox", "key-b010-cross-mailbox", "echo cross", MailboxFileMode)
	b010AcceptExchange(t, authority, "analytics", "req-b010-cross-mailbox")

	p090CreateTerminalExchange(t, authority, "req-b010-terminal")
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-b010-terminal"+ReadySuffix), nil, MailboxFileMode)
	b010AcceptExchange(t, authority, store.DefaultMailboxID, "req-b010-rejected")
	rejectedRef, err := store.NewMailboxExchangeRef(store.DefaultMailboxID, "req-b010-rejected")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteMailboxExchangeInMailbox(ctx, rejectedRef, store.MailboxExchangeRejected); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-b010-rejected"+ReadySuffix), nil, MailboxFileMode)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-b010-unknown"+ReadySuffix), nil, MailboxWorkspaceIngressFileMode)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-b010-nonempty"+ReadySuffix), []byte("x"), MailboxFileMode)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-b010-unsafe-mode"+ReadySuffix), nil, 0o640)
	linkTarget := filepath.Join(root, "b010-link-target")
	writeMailboxFile(t, linkTarget, nil, MailboxFileMode)
	if err := os.Symlink(linkTarget, filepath.Join(importer.InboxPath(), "req-b010-symlink"+ReadySuffix)); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-b010-draft"+RequestSuffix), []byte("{}"), MailboxFileMode)

	diagnosticID := "req-b010-diagnostic"
	diagnosticRaw := p081RunJSON(t, diagnosticID, "echo diagnostic")
	diagnosticRef, err := store.NewMailboxIngressDiagnosticRef(store.DefaultMailboxID, diagnosticID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, diagnosticRef, sha256.Sum256(diagnosticRaw), store.MailboxIngressDiagnosticInvalidRequestSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, diagnosticRef); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, diagnosticRef, sha256.Sum256(diagnosticRaw)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, diagnosticRef, sha256.Sum256(diagnosticRaw)); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), diagnosticID+ReadySuffix), nil, MailboxFileMode)
	incompleteDiagnosticID := "req-b010-diagnostic-incomplete"
	incompleteDiagnosticRaw := p081RunJSON(t, incompleteDiagnosticID, "echo incomplete diagnostic")
	incompleteDiagnosticRef, err := store.NewMailboxIngressDiagnosticRef(store.DefaultMailboxID, incompleteDiagnosticID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, incompleteDiagnosticRef, sha256.Sum256(incompleteDiagnosticRaw), store.MailboxIngressDiagnosticInvalidRequestSchema); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), incompleteDiagnosticID+ReadySuffix), nil, MailboxFileMode)

	ackID := "req-b010-acknowledged"
	p090CreateTerminalExchange(t, authority, ackID)
	if _, err := authority.AcknowledgeMailboxExchange(ctx, store.MailboxAcknowledgement{
		RequestID: ackID, ResponseRevision: 1, AvailableEventSequence: int64Ptr(3),
	}); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(root, "acks", ackID+ReadySuffix), nil, MailboxWorkspaceIngressFileMode)
	writeMailboxFile(t, filepath.Join(root, "acks", "req-b010-ack-unknown"+ReadySuffix), nil, MailboxFileMode)

	count, err := classifier.ActionableRequestCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("actionable request count=%d, want 3", count)
	}

	assertB010Lifecycle(t, classifier, "req-b010-native", LifecycleInputPublishablePair, LifecycleDurableNone, LifecycleActionNone, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-accepted", LifecycleInputPublishablePair, LifecycleDurableAccepted, LifecycleActionNone, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-cross-mailbox", LifecycleInputPublishablePair, LifecycleDurableNone, LifecycleActionNone, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-terminal", LifecycleInputRequestMarkerOnly, LifecycleDurableTerminalUnacknowledged, LifecycleActionEligibleDurableOrphanCleanup, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-rejected", LifecycleInputRequestMarkerOnly, LifecycleDurableTerminalUnacknowledged, LifecycleActionEligibleDurableOrphanCleanup, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-unknown", LifecycleInputRequestMarkerOnly, LifecycleDurableNone, LifecycleActionRetainUnprovenInert, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-nonempty", LifecycleInputUnsafeInert, LifecycleDurableNone, LifecycleActionRetainUnprovenInert, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-unsafe-mode", LifecycleInputUnsafeInert, LifecycleDurableNone, LifecycleActionRetainUnprovenInert, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, "req-b010-symlink", LifecycleInputUnsafeInert, LifecycleDurableNone, LifecycleActionRetainUnprovenInert, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, diagnosticID, LifecycleInputRequestMarkerOnly, LifecycleDurableIngressDiagnostic, LifecycleActionEligibleDurableOrphanCleanup, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, incompleteDiagnosticID, LifecycleInputRequestMarkerOnly, LifecycleDurableIngressDiagnostic, LifecycleActionRetainUnprovenInert, LifecycleInputAbsent, LifecycleActionNone)
	assertB010Lifecycle(t, classifier, ackID, LifecycleInputAbsent, LifecycleDurableTerminalAcknowledged, LifecycleActionNone, LifecycleInputAckMarkerOnly, LifecycleActionEligibleDurableOrphanCleanup)
	assertB010Lifecycle(t, classifier, "req-b010-ack-unknown", LifecycleInputAbsent, LifecycleDurableNone, LifecycleActionNone, LifecycleInputAckMarkerOnly, LifecycleActionRetainUnprovenInert)
}

func b010AcceptExchange(t *testing.T, authority *store.AuthorityStore, mailboxID, requestID string) {
	t.Helper()
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	raw := p081RunJSON(t, requestID, "echo accepted")
	request := Request{MailboxID: mailboxID, RequestID: requestID, IdempotencyKey: "key-" + requestID, Operation: "run", RawJSON: raw}
	canonical, hash, err := receiptCanonical(request)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.NewMailboxExchangeRef(mailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(context.Background(), ref, store.MailboxExchangeCreate{
		MailboxID: mailboxID, RequestID: requestID, Operation: request.Operation, Controller: owner,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: canonical,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertB010Lifecycle(t *testing.T, classifier *LifecycleClassifier, requestID string, requestShape LifecycleInputShape, durable LifecycleDurableState, requestAction LifecycleAction, ackShape LifecycleInputShape, ackAction LifecycleAction) {
	t.Helper()
	inspection, err := classifier.Inspect(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Request.InputShape != requestShape || inspection.Request.DurableState != durable || inspection.Request.Action != requestAction ||
		inspection.Acknowledgement.InputShape != ackShape || inspection.Acknowledgement.DurableState != durable || inspection.Acknowledgement.Action != ackAction {
		t.Fatalf("request %s lifecycle=%+v", requestID, inspection)
	}
}
