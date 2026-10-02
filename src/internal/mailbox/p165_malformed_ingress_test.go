package mailbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const p165MailboxID = "analytics"

type p165Harness struct {
	root        string
	now         *time.Time
	authority   *store.AuthorityStore
	importer    *Importer
	diagnostics *DiagnosticFiles
	processor   *SessionProcessor
	operations  *p153Operations
	cleaner     ArtifactCleaner
}

func newP165Harness(t *testing.T) *p165Harness {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	database, err := store.Open(context.Background(), filepath.Join(root, "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := store.NewAuthorityStoreWithClock(database, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	mailboxRoot := filepath.Join(root, "mailbox")
	importer, err := New(Options{MailboxID: p165MailboxID, Root: mailboxRoot, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := NewOutbox(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	events, err := NewEventFiles(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := NewDiagnosticFiles(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	operations := &p153Operations{owner: owner, sessionTargets: make(map[string]domain.ExecutionTarget), submitTargets: make(map[string]domain.ExecutionTarget)}
	processor, err := NewSessionProcessor(SessionProcessorOptions{
		MailboxID: p165MailboxID, Importer: importer, Authority: authority, Controller: owner, Operations: operations,
		Outbox: outbox, EventFiles: events, Diagnostics: diagnostics, ExecutionResolver: newP153Resolver(t), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &p165Harness{
		root: root, now: &now, authority: authority, importer: importer, diagnostics: diagnostics, processor: processor, operations: operations,
		cleaner: ArtifactCleaner{MailboxID: p165MailboxID, Authority: authority, Outbox: outbox, EventFiles: events, Diagnostics: diagnostics},
	}
}

func TestP165SafeInvalidIngressCreatesPrivateDiagnosticAcrossModes(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		mode        os.FileMode
		requestID   string
		raw         func(string) []byte
		wantFailure store.MailboxIngressDiagnosticCode
	}{
		{
			name: "native malformed JSON", mode: MailboxFileMode, requestID: "req-p165-native-malformed",
			raw: func(string) []byte { return []byte(`{"request_id":`) }, wantFailure: store.MailboxIngressDiagnosticMalformedJSON,
		},
		{
			name: "workspace scalar target", mode: MailboxWorkspaceIngressFileMode, requestID: "req-p165-workspace-schema",
			raw: p165InvalidSchemaRequest, wantFailure: store.MailboxIngressDiagnosticInvalidRequestSchema,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newP165Harness(t)
			raw := testCase.raw(testCase.requestID)
			p165WritePair(t, h.importer, testCase.requestID, raw, testCase.mode, nil, testCase.mode)

			results, err := h.processor.Import(context.Background())
			if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("invalid import results=%+v err=%v", results, err)
			}
			assertMailboxPairAbsent(t, h.importer.InboxPath(), testCase.requestID)
			record, projected := p165ReadDiagnostic(t, h, testCase.requestID)
			if record.Code != testCase.wantFailure || record.ProjectedAt == nil || record.InputPairRemovedAt == nil {
				t.Fatalf("diagnostic record=%+v", record)
			}
			if strings.Contains(string(projected), "P165_SECRET_NEVER_PROJECT") || strings.Contains(string(projected), "ubuntu-current") {
				t.Fatalf("private diagnostic leaked request data: %s", projected)
			}
			p165AssertNoExchange(t, h, testCase.requestID)
			if len(h.operations.runRequests) != 0 || len(h.operations.createRequests) != 0 || len(h.operations.submitRequests) != 0 {
				t.Fatalf("invalid input reached operations: %+v", h.operations)
			}
			events := h.processor.TakeIngressDiagnosticEvents()
			p165AssertOneSafeEvent(t, events, testCase.requestID, string(testCase.wantFailure))
		})
	}
}

func TestP165ZeroMarkerReplacementAndUnsafeIngressRemainCorrectlyClassified(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-replaced-marker"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxWorkspaceIngressFileMode, []byte("x"), MailboxWorkspaceIngressFileMode)

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("nonempty marker results=%+v err=%v", results, err)
	}
	p165AssertDiagnosticAbsent(t, h, requestID)
	assertMailboxPairPresent(t, h.importer.InboxPath(), requestID)
	if events := h.processor.TakeIngressDiagnosticEvents(); len(events) != 0 {
		t.Fatalf("nonempty marker emitted diagnostic events=%+v", events)
	}

	marker := filepath.Join(h.importer.InboxPath(), requestID+ReadySuffix)
	replacement := marker + ".replacement"
	writeMailboxFile(t, replacement, nil, MailboxWorkspaceIngressFileMode)
	if err := os.Rename(replacement, marker); err != nil {
		t.Fatal(err)
	}
	if err := syncMailboxDirectory(h.importer.InboxPath()); err != nil {
		t.Fatal(err)
	}
	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("replacement marker results=%+v err=%v", results, err)
	}
	p165ReadDiagnostic(t, h, requestID)
	p165AssertOneSafeEvent(t, h.processor.TakeIngressDiagnosticEvents(), requestID, string(store.MailboxIngressDiagnosticInvalidRequestSchema))

	unsafeID := "req-p165-unsafe-mode"
	p165WritePair(t, h.importer, unsafeID, p165InvalidSchemaRequest(unsafeID), 0o640, nil, 0o640)
	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("unsafe pair results=%+v err=%v", results, err)
	}
	p165AssertDiagnosticAbsent(t, h, unsafeID)
	assertMailboxPairPresent(t, h.importer.InboxPath(), unsafeID)
	p165AssertNoExchange(t, h, unsafeID)
	if events := h.processor.TakeIngressDiagnosticEvents(); len(events) != 0 {
		t.Fatalf("unsafe pair emitted diagnostic events=%+v", events)
	}
}

func TestP165SameInodeNonemptyMarkerTruncateReevaluatesExactlyOnce(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-same-inode-truncate"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxWorkspaceIngressFileMode, []byte("x"), MailboxWorkspaceIngressFileMode)

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("nonempty marker results=%+v err=%v", results, err)
	}
	p165AssertDiagnosticAbsent(t, h, requestID)
	p165AssertNoExchange(t, h, requestID)

	marker := filepath.Join(h.importer.InboxPath(), requestID+ReadySuffix)
	before, err := os.Lstat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(marker, 0); err != nil {
		t.Fatal(err)
	}
	if err := syncMailboxDirectory(h.importer.InboxPath()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Size() != 0 {
		t.Fatalf("marker was not truncated in place: before=%+v after=%+v", before, after)
	}

	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("truncated marker results=%+v err=%v", results, err)
	}
	p165ReadDiagnostic(t, h, requestID)
	p165AssertOneSafeEvent(t, h.processor.TakeIngressDiagnosticEvents(), requestID, string(store.MailboxIngressDiagnosticInvalidRequestSchema))

	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("truncated marker replay results=%+v err=%v", results, err)
	}
	if events := h.processor.TakeIngressDiagnosticEvents(); len(events) != 0 {
		t.Fatalf("truncated marker replay emitted events=%+v", events)
	}
	if len(h.operations.runRequests) != 0 {
		t.Fatalf("truncated malformed request reached operations: %+v", h.operations.runRequests)
	}
}

func TestP165RecoveryPrecedesDraftCleanupAndRepairsMissingArtifact(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-recovery-seam"
	raw := p165InvalidSchemaRequest(requestID)
	p165WritePair(t, h.importer, requestID, raw, MailboxFileMode, nil, MailboxFileMode)
	ref, err := store.NewMailboxIngressDiagnosticRef(p165MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	record, disposition, err := h.authority.RecordMailboxIngressDiagnosticInMailbox(context.Background(), ref, sha256.Sum256(raw), store.MailboxIngressDiagnosticInvalidRequestSchema)
	if err != nil || disposition != store.MailboxIngressDiagnosticCreated {
		t.Fatalf("pre-crash record disposition=%q err=%v", disposition, err)
	}
	if err := h.diagnostics.Replace(context.Background(), requestID, record.DiagnosticBytes); err != nil {
		t.Fatal(err)
	}
	record, err = h.authority.MarkMailboxIngressDiagnosticProjectedInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(context.Background(), ref, record.RequestSHA256); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.importer.InboxPath(), requestID+ReadySuffix)); err != nil {
		t.Fatal(err)
	}
	if err := syncMailboxDirectory(h.importer.InboxPath()); err != nil {
		t.Fatal(err)
	}
	old := *h.now
	if err := os.Chtimes(filepath.Join(h.importer.InboxPath(), requestID+RequestSuffix), old.Add(-UnmarkedDraftLifetime-time.Second), old.Add(-UnmarkedDraftLifetime-time.Second)); err != nil {
		t.Fatal(err)
	}

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("restart recovery results=%+v err=%v", results, err)
	}
	assertMailboxPairAbsent(t, h.importer.InboxPath(), requestID)
	record, projected := p165ReadDiagnostic(t, h, requestID)
	if record.InputPairRemovedAt == nil || record.ProjectedAt == nil {
		t.Fatalf("recovery lifecycle=%+v", record)
	}
	if err := h.diagnostics.Remove(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("artifact repair results=%+v err=%v", results, err)
	}
	_, repaired := p165ReadDiagnostic(t, h, requestID)
	if string(repaired) != string(projected) {
		t.Fatalf("repaired diagnostic changed:\nwant=%s\n got=%s", projected, repaired)
	}
	if events := h.processor.TakeIngressDiagnosticEvents(); len(events) != 0 {
		t.Fatalf("recovery emitted duplicate events=%+v", events)
	}
}

func TestP165RecoveryFailureDefersUnmarkedDraftCleanup(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-recovery-failure-draft"
	raw := p165InvalidSchemaRequest(requestID)
	ref, err := store.NewMailboxIngressDiagnosticRef(p165MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	record, disposition, err := h.authority.RecordMailboxIngressDiagnosticInMailbox(context.Background(), ref, sha256.Sum256(raw), store.MailboxIngressDiagnosticInvalidRequestSchema)
	if err != nil || disposition != store.MailboxIngressDiagnosticCreated {
		t.Fatalf("pre-crash record disposition=%q err=%v", disposition, err)
	}
	if err := h.diagnostics.Replace(context.Background(), requestID, record.DiagnosticBytes); err != nil {
		t.Fatal(err)
	}
	record, err = h.authority.MarkMailboxIngressDiagnosticProjectedInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(context.Background(), ref, record.RequestSHA256); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(h.importer.InboxPath(), requestID+RequestSuffix)
	writeMailboxFile(t, requestPath, raw, MailboxFileMode)
	old := h.now.Add(-UnmarkedDraftLifetime - time.Second)
	if err := os.Chtimes(requestPath, old, old); err != nil {
		t.Fatal(err)
	}
	diagnosticPath, err := h.diagnostics.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(diagnosticPath, MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}

	results, err := h.processor.Import(context.Background())
	if err == nil || !IsMailboxIngressDiagnosticFailure(err) || len(results) != 0 {
		t.Fatalf("recovery failure results=%+v err=%v", results, err)
	}
	if _, err := os.Lstat(requestPath); err != nil {
		t.Fatalf("failed recovery let ordinary draft cleanup remove stranded request: %v", err)
	}
	p165AssertNoExchange(t, h, requestID)
	if len(h.operations.runRequests) != 0 {
		t.Fatalf("stranded invalid request reached operation: %+v", h.operations.runRequests)
	}

	if err := os.Chmod(diagnosticPath, MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("recovered cleanup results=%+v err=%v", results, err)
	}
	if _, err := os.Lstat(requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered cleanup left stranded request: %v", err)
	}
	record, err = h.authority.GetMailboxIngressDiagnosticInMailbox(context.Background(), ref)
	if err != nil || record.InputPairRemovedAt == nil {
		t.Fatalf("recovered diagnostic lifecycle=%+v err=%v", record, err)
	}
}

func TestP165RetainedRejectedIdentityCannotBecomeWork(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-retained"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	if results, err := h.processor.Import(context.Background()); err != nil || len(results) != 1 || !results[0].PairRemoved {
		t.Fatalf("initial invalid results=%+v err=%v", results, err)
	}
	_, frozen := p165ReadDiagnostic(t, h, requestID)
	_ = h.processor.TakeIngressDiagnosticEvents()

	p165WritePair(t, h.importer, requestID, p165ValidRunRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("valid reused identity results=%+v err=%v", results, err)
	}
	p165AssertNoExchange(t, h, requestID)
	if len(h.operations.runRequests) != 0 {
		t.Fatalf("retained identity reached run operation: %+v", h.operations.runRequests)
	}
	_, afterValid := p165ReadDiagnostic(t, h, requestID)
	if string(afterValid) != string(frozen) {
		t.Fatalf("valid reuse changed frozen diagnostic")
	}
	p165AssertOneSafeEvent(t, h.processor.TakeIngressDiagnosticEvents(), requestID, string(store.MailboxIngressDiagnosticRequestIDReusedAfterReject))

	p165WritePair(t, h.importer, requestID, []byte(`{"request_id":`), MailboxFileMode, nil, MailboxFileMode)
	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("invalid reused identity results=%+v err=%v", results, err)
	}
	p165AssertNoExchange(t, h, requestID)
	_, afterInvalid := p165ReadDiagnostic(t, h, requestID)
	if string(afterInvalid) != string(frozen) {
		t.Fatalf("invalid reuse changed frozen diagnostic")
	}
	p165AssertOneSafeEvent(t, h.processor.TakeIngressDiagnosticEvents(), requestID, string(store.MailboxIngressDiagnosticRequestIDReusedAfterReject))
}

func TestP165DiagnosticCleanupDoesNotResurrectAndEventsDoNotHotLoop(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-cleanup"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	if results, err := h.processor.Import(context.Background()); err != nil || len(results) != 1 || !results[0].PairRemoved {
		t.Fatalf("initial results=%+v err=%v", results, err)
	}
	_ = h.processor.TakeIngressDiagnosticEvents()
	path, err := h.diagnostics.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if results, err := h.processor.Import(context.Background()); err != nil || len(results) != 0 {
			t.Fatalf("settled diagnostic results=%+v err=%v", results, err)
		}
		if events := h.processor.TakeIngressDiagnosticEvents(); len(events) != 0 {
			t.Fatalf("settled diagnostic emitted event=%+v", events)
		}
	}
	after, err := os.Stat(path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("settled diagnostic was rewritten before=%v after=%v err=%v", before.ModTime(), after.ModTime(), err)
	}

	*h.now = h.now.Add(store.MailboxIngressDiagnosticArtifactLifetime + time.Second)
	report, err := h.cleaner.Run(context.Background())
	if err != nil || report.DiagnosticsRemoved != 1 {
		t.Fatalf("diagnostic cleanup report=%+v err=%v", report, err)
	}
	if _, err := h.diagnostics.Read(requestID); err == nil {
		t.Fatal("expired diagnostic still readable")
	}
	if results, err := h.processor.Import(context.Background()); err != nil || len(results) != 0 {
		t.Fatalf("post-cleanup recovery results=%+v err=%v", results, err)
	}
	if _, err := h.diagnostics.Read(requestID); err == nil {
		t.Fatal("expired diagnostic resurrected after recovery")
	}

	p165WritePair(t, h.importer, requestID, p165ValidRunRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("post-cleanup reuse results=%+v err=%v", results, err)
	}
	p165AssertNoExchange(t, h, requestID)
	if _, err := h.diagnostics.Read(requestID); err == nil {
		t.Fatal("retained-ID cleanup resurrected expired diagnostic")
	}
	p165AssertOneSafeEvent(t, h.processor.TakeIngressDiagnosticEvents(), requestID, string(store.MailboxIngressDiagnosticRequestIDReusedAfterReject))
}

func TestP165RecoveryFailureAndInputReplacementStaySafe(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-recovery-failure"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	if _, err := h.processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = h.processor.TakeIngressDiagnosticEvents()
	path, err := h.diagnostics.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := h.processor.Import(context.Background()); err == nil || !IsMailboxIngressDiagnosticFailure(err) {
		t.Fatalf("unsafe diagnostic recovery error=%v, want safe diagnostic sentinel", err)
	}
	p165AssertOneSafeEvent(t, h.processor.TakeIngressDiagnosticEvents(), requestID, "recovery_failed")
	if _, err := h.processor.Import(context.Background()); err == nil || !IsMailboxIngressDiagnosticFailure(err) {
		t.Fatalf("repeated unsafe diagnostic recovery error=%v", err)
	}
	if events := h.processor.TakeIngressDiagnosticEvents(); len(events) != 0 {
		t.Fatalf("recovery failure hot-looped events=%+v", events)
	}
	if err := os.Chmod(path, MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := h.processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Exercise the Lstat/open replacement seam. The request changes to a
	// symlink after its first inspection, so O_NOFOLLOW must keep it inert.
	tracedID := "req-p165-open-race"
	tracedPath := filepath.Join(h.importer.InboxPath(), tracedID+RequestSuffix)
	outside := filepath.Join(h.root, "outside-request.json")
	writeMailboxFile(t, outside, p165InvalidSchemaRequest(tracedID), MailboxFileMode)
	p165WritePair(t, h.importer, tracedID, p165InvalidSchemaRequest(tracedID), MailboxFileMode, nil, MailboxFileMode)
	mailboxIngressBeforeOpenHook = func() {
		if err := os.Remove(tracedPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, tracedPath); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { mailboxIngressBeforeOpenHook = nil })
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("open-race results=%+v err=%v", results, err)
	}
	p165AssertDiagnosticAbsent(t, h, tracedID)
	p165AssertNoExchange(t, h, tracedID)
	if len(h.operations.runRequests) != 0 {
		t.Fatalf("open-race input reached operation: %+v", h.operations.runRequests)
	}
}

func TestP165ReadyMarkerChangedAfterRequestReadStaysInert(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-marker-race"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	markerPath := filepath.Join(h.importer.InboxPath(), requestID+ReadySuffix)
	mailboxIngressAfterRequestReadHook = func() {
		replacement := markerPath + ".replacement"
		writeMailboxFile(t, replacement, []byte("not-empty"), MailboxFileMode)
		if err := os.Rename(replacement, markerPath); err != nil {
			t.Fatal(err)
		}
		if err := syncMailboxDirectory(h.importer.InboxPath()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { mailboxIngressAfterRequestReadHook = nil })

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("marker-race results=%+v err=%v", results, err)
	}
	if results[0].Reason != "ready marker must be empty" {
		t.Fatalf("marker-race result reason=%q", results[0].Reason)
	}
	p165AssertDiagnosticAbsent(t, h, requestID)
	p165AssertNoExchange(t, h, requestID)
	if len(h.operations.runRequests) != 0 {
		t.Fatalf("marker-race input reached operation: %+v", h.operations.runRequests)
	}
}

func TestP165CleanupStagingPreservesReplacementPublishedAfterOldMarkerStage(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-cleanup-stage-race"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxFileMode, nil, MailboxFileMode)

	// Simulate a publisher that has written replacement JSON after Runner moved
	// the old marker aside but has not yet published its own marker. Restoring
	// the old marker at this seam would incorrectly dispatch the new JSON.
	mailboxIngressAfterDiagnosticMarkerStageHook = func() {
		p165ReplaceRequestOnly(t, h.importer, requestID, p165ValidRunRequest(requestID), MailboxFileMode)
	}
	t.Cleanup(func() { mailboxIngressAfterDiagnosticMarkerStageHook = nil })

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].Durable || results[0].PairRemoved {
		t.Fatalf("staging replacement results=%+v err=%v", results, err)
	}
	requestPath := filepath.Join(h.importer.InboxPath(), requestID+RequestSuffix)
	markerPath := filepath.Join(h.importer.InboxPath(), requestID+ReadySuffix)
	if _, err := os.Lstat(requestPath); err != nil {
		t.Fatalf("cleanup removed replacement JSON before its marker: %v", err)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup restored old marker before replacement publication: %v", err)
	}
	markerReplacement := markerPath + ".replacement"
	writeMailboxFile(t, markerReplacement, nil, MailboxFileMode)
	if err := os.Rename(markerReplacement, markerPath); err != nil {
		t.Fatal(err)
	}
	if err := syncMailboxDirectory(h.importer.InboxPath()); err != nil {
		t.Fatal(err)
	}

	// A plain importer has no retained-diagnostic ledger. Its successful intake
	// proves the preserved marker-last replacement remains dispatchable; the
	// production processor will instead reject this reused ID without execution.
	var dispatched []Request
	plain, err := New(Options{MailboxID: p165MailboxID, Root: filepath.Dir(h.importer.InboxPath()), Handler: func(_ context.Context, request Request) error {
		dispatched = append(dispatched, request)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	plainResults, err := plain.Import(context.Background())
	if err != nil || len(plainResults) != 1 || plainResults[0].Status != ResultAccepted || len(dispatched) != 1 || dispatched[0].RequestID != requestID {
		t.Fatalf("replacement pair was not dispatchable: results=%+v dispatched=%+v err=%v", plainResults, dispatched, err)
	}
	if len(h.operations.runRequests) != 0 {
		t.Fatalf("replacement race reached production operations: %+v", h.operations.runRequests)
	}
}

func TestP165RecoveryRevalidationKeepsPostReadMarkerReplacementInert(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-recovery-marker-race"
	raw := p165InvalidSchemaRequest(requestID)
	p165WritePair(t, h.importer, requestID, raw, MailboxFileMode, nil, MailboxFileMode)
	ref, err := store.NewMailboxIngressDiagnosticRef(p165MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	record, disposition, err := h.authority.RecordMailboxIngressDiagnosticInMailbox(context.Background(), ref, sha256.Sum256(raw), store.MailboxIngressDiagnosticInvalidRequestSchema)
	if err != nil || disposition != store.MailboxIngressDiagnosticCreated {
		t.Fatalf("pre-crash record disposition=%q err=%v", disposition, err)
	}
	if err := h.diagnostics.Replace(context.Background(), requestID, record.DiagnosticBytes); err != nil {
		t.Fatal(err)
	}
	record, err = h.authority.MarkMailboxIngressDiagnosticProjectedInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(context.Background(), ref, record.RequestSHA256); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(h.importer.InboxPath(), requestID+ReadySuffix)
	mailboxIngressAfterRequestReadHook = func() {
		replacement := markerPath + ".replacement"
		writeMailboxFile(t, replacement, []byte("not-empty"), MailboxFileMode)
		if err := os.Rename(replacement, markerPath); err != nil {
			t.Fatal(err)
		}
		if err := syncMailboxDirectory(h.importer.InboxPath()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { mailboxIngressAfterRequestReadHook = nil })

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("recovery marker-race results=%+v err=%v", results, err)
	}
	if results[0].Reason != "ready marker must be empty" {
		t.Fatalf("recovery marker-race reason=%q", results[0].Reason)
	}
	if _, err := os.Lstat(filepath.Join(h.importer.InboxPath(), requestID+RequestSuffix)); err != nil {
		t.Fatalf("recovery removed request after marker replacement: %v", err)
	}
	record, err = h.authority.GetMailboxIngressDiagnosticInMailbox(context.Background(), ref)
	if err != nil || record.InputPairRemovedAt != nil {
		t.Fatalf("recovery changed diagnostic lifecycle=%+v err=%v", record, err)
	}
	p165AssertNoExchange(t, h, requestID)
}

func TestP165PrivateDiagnosticReadRejectsPostCheckSymlinkSwap(t *testing.T) {
	h := newP165Harness(t)
	const requestID = "req-p165-diagnostic-read-race"
	p165WritePair(t, h.importer, requestID, p165InvalidSchemaRequest(requestID), MailboxFileMode, nil, MailboxFileMode)
	if _, err := h.processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	path, err := h.diagnostics.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(h.root, "outside-diagnostic.json")
	const outsideContent = "P165_OUTSIDE_DIAGNOSTIC_MUST_NOT_BE_READ"
	writeMailboxFile(t, outside, []byte(outsideContent), MailboxFileMode)
	diagnosticBeforeOpenHook = func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { diagnosticBeforeOpenHook = nil })

	_, err = h.diagnostics.Read(requestID)
	if err == nil {
		t.Fatal("post-check diagnostic symlink swap was read")
	}
	if strings.Contains(err.Error(), outsideContent) {
		t.Fatalf("diagnostic read leaked outside content: %v", err)
	}
}

func TestP165InvalidInputDoesNotDelayAdjacentValidWork(t *testing.T) {
	h := newP165Harness(t)
	invalidID := "req-p165-a-invalid"
	validID := "req-p165-b-valid"
	p165WritePair(t, h.importer, invalidID, p165InvalidSchemaRequest(invalidID), MailboxFileMode, nil, MailboxFileMode)
	p165WritePair(t, h.importer, validID, p165ValidRunRequest(validID), MailboxFileMode, nil, MailboxFileMode)

	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 2 {
		t.Fatalf("adjacent work results=%+v err=%v", results, err)
	}
	if results[0].RequestID != invalidID || results[0].Status != ResultRejected || !results[0].PairRemoved ||
		results[1].RequestID != validID || results[1].Status != ResultAccepted || !results[1].PairRemoved {
		t.Fatalf("adjacent work result ordering=%+v", results)
	}
	p165ReadDiagnostic(t, h, invalidID)
	if len(h.operations.runRequests) != 1 || h.operations.runRequests[0].RequestID != validID {
		t.Fatalf("adjacent valid run was delayed or missing: %+v", h.operations.runRequests)
	}
	if _, err := h.authority.GetMailboxExchangeInMailbox(context.Background(), mustP165ExchangeRef(t, validID)); err != nil {
		t.Fatalf("adjacent valid work has no exchange: %v", err)
	}
	p165AssertNoExchange(t, h, invalidID)
}

func p165WritePair(t *testing.T, importer *Importer, requestID string, raw []byte, requestMode os.FileMode, marker []byte, markerMode os.FileMode) {
	t.Helper()
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+RequestSuffix), raw, requestMode)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+ReadySuffix), marker, markerMode)
}

func p165ReplaceRequestOnly(t *testing.T, importer *Importer, requestID string, raw []byte, mode os.FileMode) {
	t.Helper()
	requestPath := filepath.Join(importer.InboxPath(), requestID+RequestSuffix)
	replacement := requestPath + ".replacement"
	writeMailboxFile(t, replacement, raw, mode)
	if err := os.Rename(replacement, requestPath); err != nil {
		t.Fatal(err)
	}
	if err := syncMailboxDirectory(importer.InboxPath()); err != nil {
		t.Fatal(err)
	}
}

func p165InvalidSchemaRequest(requestID string) []byte {
	data, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": "P165_SECRET_NEVER_PROJECT", "operation": "run",
		"execution_target": "ubuntu-current", "script": "printf P165_SECRET_NEVER_PROJECT",
	})
	if err != nil {
		panic(err)
	}
	return data
}

func p165ValidRunRequest(requestID string) []byte {
	data, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": "key-" + requestID, "operation": "run", "script": "printf valid",
	})
	if err != nil {
		panic(err)
	}
	return data
}

func p165ReadDiagnostic(t *testing.T, h *p165Harness, requestID string) (store.MailboxIngressDiagnosticRecord, []byte) {
	t.Helper()
	ref, err := store.NewMailboxIngressDiagnosticRef(p165MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := h.authority.GetMailboxIngressDiagnosticInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := h.diagnostics.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := h.diagnostics.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != MailboxFileMode {
		t.Fatalf("diagnostic mode=%v err=%v, want 0600", info, err)
	}
	return record, data
}

func p165AssertDiagnosticAbsent(t *testing.T, h *p165Harness, requestID string) {
	t.Helper()
	ref, err := store.NewMailboxIngressDiagnosticRef(p165MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.GetMailboxIngressDiagnosticInMailbox(context.Background(), ref); !errors.Is(err, store.ErrMailboxIngressDiagnosticNotFound) {
		t.Fatalf("unexpected diagnostic record error=%v", err)
	}
}

func p165AssertNoExchange(t *testing.T, h *p165Harness, requestID string) {
	t.Helper()
	if _, err := h.authority.GetMailboxExchangeInMailbox(context.Background(), mustP165ExchangeRef(t, requestID)); !errors.Is(err, store.ErrMailboxExchangeNotFound) {
		t.Fatalf("unexpected accepted exchange request=%s err=%v", requestID, err)
	}
}

func mustP165ExchangeRef(t *testing.T, requestID string) store.MailboxExchangeRef {
	t.Helper()
	ref, err := store.NewMailboxExchangeRef(p165MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func p165AssertOneSafeEvent(t *testing.T, events []IngressDiagnosticEvent, requestID, failureClass string) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events=%+v, want one", events)
	}
	event := events[0]
	if event.MailboxID != p165MailboxID || event.RequestID != requestID || event.FailureClass != failureClass || event.LifecyclePhase != ingressDiagnosticLifecyclePhase {
		t.Fatalf("event=%+v", event)
	}
	if strings.Contains(event.MailboxID+event.RequestID+event.FailureClass+event.LifecyclePhase, "P165_SECRET_NEVER_PROJECT") {
		t.Fatalf("event leaked request secret: %+v", event)
	}
}
