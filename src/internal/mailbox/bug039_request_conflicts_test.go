package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG039ConflictingPublicationPreservesAcknowledgedSuccessAndEvents(t *testing.T) {
	for _, mode := range []os.FileMode{MailboxFileMode, MailboxWorkspaceIngressFileMode} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newP165Harness(t)
			original := seedBUG039TerminalRun(t, h, "req-bug039-success")
			before := snapshotBUG039Original(t, h, original.RequestID)
			conflict := bug039ChangedRun(t, h, original, "PRIVATE_DIFFERENT_SCRIPT")
			p165WritePair(t, h.importer, conflict.RequestID, conflict.RawJSON, mode, nil, mode)

			results, err := h.processor.Import(context.Background())
			if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("conflict import=%+v, %v", results, err)
			}
			assertMailboxPairAbsent(t, h.importer.InboxPath(), conflict.RequestID)
			assertBUG039Archived(t, h, conflict)
			assertBUG039Original(t, h, original.RequestID, before)
			if len(h.operations.runRequests) != 0 {
				t.Fatalf("refused publication executed: %+v", h.operations.runRequests)
			}
			if _, err := h.authority.GetMailboxIngressDiagnosticInMailbox(context.Background(), store.MailboxIngressDiagnosticRef{MailboxID: h.importer.MailboxID(), ClientRequestID: original.RequestID}); !os.IsNotExist(err) && err != store.ErrMailboxIngressDiagnosticNotFound {
				t.Fatalf("conflict created an incompatible malformed-input ledger entry: %v", err)
			}
		})
	}
}

func TestBUG039CanonicalRetryKeepsOriginalResponseWithoutConflictArchive(t *testing.T) {
	h := newP165Harness(t)
	original := seedBUG039TerminalRun(t, h, "req-bug039-exact")
	before := snapshotBUG039Original(t, h, original.RequestID)
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, original.RawJSON, "", "  "); err != nil {
		t.Fatal(err)
	}
	p165WritePair(t, h.importer, original.RequestID, formatted.Bytes(), MailboxWorkspaceIngressFileMode, nil, MailboxWorkspaceIngressFileMode)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultAccepted || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("canonical retry=%+v, %v", results, err)
	}
	assertBUG039Original(t, h, original.RequestID, before)
	assertMailboxPairAbsent(t, h.importer.InboxPath(), original.RequestID)
	if _, err := os.Lstat(filepath.Join(h.importer.root, "conflicts", original.RequestID)); !os.IsNotExist(err) {
		t.Fatalf("formatting-only retry was archived as conflicting: %v", err)
	}
	if len(h.operations.runRequests) != 0 {
		t.Fatal("terminal retry ran another command")
	}
}

func TestBUG039SameIDInAnotherMailboxIsIndependent(t *testing.T) {
	h := newP165Harness(t)
	original := seedBUG039TerminalRun(t, h, "req-bug039-scoped")
	before := snapshotBUG039Original(t, h, original.RequestID)
	owner := h.processor.controller
	root := filepath.Join(t.TempDir(), "other-mailbox")
	importer, err := New(Options{MailboxID: "other", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	events, err := NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	operations := &p153Operations{owner: owner, sessionTargets: make(map[string]domain.ExecutionTarget), submitTargets: make(map[string]domain.ExecutionTarget)}
	processor, err := NewSessionProcessor(SessionProcessorOptions{MailboxID: "other", Importer: importer, Authority: h.authority, Controller: owner, Operations: operations, Outbox: outbox, EventFiles: events, ExecutionResolver: bug039OtherMailboxResolver{newP153Resolver(t)}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"request_id": original.RequestID, "operation": "run", "idempotency_key": original.IdempotencyKey, "script": "printf other-mailbox"})
	if err != nil {
		t.Fatal(err)
	}
	p165WritePair(t, importer, original.RequestID, raw, MailboxFileMode, nil, MailboxFileMode)
	results, err := processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultAccepted || len(operations.runRequests) != 1 {
		t.Fatalf("independent mailbox import=%+v, %v, operation calls=%d", results, err, len(operations.runRequests))
	}
	assertBUG039Original(t, h, original.RequestID, before)
	if _, err := os.Lstat(filepath.Join(root, "conflicts", original.RequestID)); !os.IsNotExist(err) {
		t.Fatalf("another mailbox's identity created a conflict: %v", err)
	}
}

func TestBUG039ConflictingPublicationDoesNotReplaceAcceptedOriginal(t *testing.T) {
	h := newP165Harness(t)
	original := p153ProcessorRequest(t, "req-bug039-active", "key-bug039-active", "run", map[string]any{"script": "printf original"})
	p153Process(t, context.Background(), h.processor, original)
	if err := h.processor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := snapshotBUG039Original(t, h, original.RequestID)
	conflict := bug039ChangedRun(t, h, original, "printf different")
	p165WritePair(t, h.importer, original.RequestID, conflict.RawJSON, MailboxFileMode, nil, MailboxFileMode)
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Status != ResultRejected || !results[0].PairRemoved {
		t.Fatalf("accepted-original conflict=%+v, %v", results, err)
	}
	assertBUG039Original(t, h, original.RequestID, before)
	assertBUG039Archived(t, h, conflict)
	if len(h.operations.runRequests) != 1 {
		t.Fatalf("conflicting work reached operations: %d calls", len(h.operations.runRequests))
	}
}

func TestBUG039RestartCompletesConflictBeforeCollectingOldUnmarkedDraft(t *testing.T) {
	h := newP165Harness(t)
	original := seedBUG039TerminalRun(t, h, "req-bug039-interrupted")
	before := snapshotBUG039Original(t, h, original.RequestID)
	conflict := bug039ChangedRun(t, h, original, "printf archived-before-stop")
	p165WritePair(t, h.importer, original.RequestID, conflict.RawJSON, MailboxFileMode, nil, MailboxFileMode)
	requestPath := filepath.Join(h.importer.InboxPath(), original.RequestID+RequestSuffix)
	old := h.now.Add(-48 * time.Hour)
	if err := os.Chtimes(requestPath, old, old); err != nil {
		t.Fatal(err)
	}
	const stopped = "bug039 process stopped after marker staging"
	mailboxIngressAfterDiagnosticMarkerStageHook = func() { panic(stopped) }
	t.Cleanup(func() { mailboxIngressAfterDiagnosticMarkerStageHook = nil })
	func() {
		defer func() {
			if got := recover(); got != stopped {
				t.Fatalf("interruption=%v, want stage interruption", got)
			}
		}()
		_, _ = h.processor.Import(context.Background())
	}()
	assertPathPresent(t, requestPath)
	assertPathAbsent(t, filepath.Join(h.importer.InboxPath(), original.RequestID+ReadySuffix))
	if records, err := h.processor.conflicts.Pending(context.Background(), 64); err != nil || len(records) != 1 {
		t.Fatalf("durable unfinished conflict=%+v, %v", records, err)
	}
	oldProcessor := h.processor
	h.processor, _ = NewSessionProcessor(SessionProcessorOptions{MailboxID: oldProcessor.mailboxID, Importer: h.importer, Authority: h.authority, Controller: oldProcessor.controller, Operations: h.operations, Outbox: oldProcessor.projector.Outbox, EventFiles: oldProcessor.projector.EventFiles, ExecutionResolver: oldProcessor.executionResolver, Now: oldProcessor.now})
	if h.processor == nil {
		t.Fatal("restart processor could not be constructed")
	}
	results, err := h.processor.Import(context.Background())
	if err != nil || len(results) != 0 {
		t.Fatalf("recovery before draft collection=%+v, %v", results, err)
	}
	assertMailboxPairAbsent(t, h.importer.InboxPath(), original.RequestID)
	assertBUG039Archived(t, h, conflict)
	assertBUG039Original(t, h, original.RequestID, before)
	if records, err := h.processor.conflicts.Pending(context.Background(), 64); err != nil || len(records) != 0 {
		t.Fatalf("unfinished conflict survived recovery=%+v, %v", records, err)
	}
	if len(h.operations.runRequests) != 0 {
		t.Fatal("recovery invoked work")
	}
}

func TestBUG039PublisherReplacementSurvivesWhileFreshWorkProceeds(t *testing.T) {
	h := newP165Harness(t)
	original := seedBUG039TerminalRun(t, h, "req-bug039-a-replaced")
	before := snapshotBUG039Original(t, h, original.RequestID)
	conflict := bug039ChangedRun(t, h, original, "printf first-conflicting-publication")
	replacement := bug039ChangedRun(t, h, original, "printf NEWER_PUBLICATION_MUST_SURVIVE")
	fresh := p153ProcessorRequest(t, "req-bug039-z-fresh", "key-bug039-z-fresh", "run", map[string]any{"script": "printf independent"})
	p165WritePair(t, h.importer, conflict.RequestID, conflict.RawJSON, MailboxFileMode, nil, MailboxFileMode)
	p165WritePair(t, h.importer, fresh.RequestID, fresh.RawJSON, MailboxFileMode, nil, MailboxFileMode)
	mailboxIngressAfterDiagnosticMarkerStageHook = func() {
		p165ReplaceRequestOnly(t, h.importer, replacement.RequestID, replacement.RawJSON, MailboxFileMode)
		writeMailboxFile(t, filepath.Join(h.importer.InboxPath(), replacement.RequestID+ReadySuffix), nil, MailboxFileMode)
	}
	t.Cleanup(func() { mailboxIngressAfterDiagnosticMarkerStageHook = nil })
	results, err := h.processor.Import(context.Background())
	if err == nil || len(results) != 2 || results[0].Status != ResultRejected || results[0].PairRemoved || results[1].Status != ResultAccepted {
		t.Fatalf("replacement/fresh import=%+v, %v", results, err)
	}
	assertMailboxPairPresent(t, h.importer.InboxPath(), original.RequestID)
	if got, err := os.ReadFile(filepath.Join(h.importer.InboxPath(), original.RequestID+RequestSuffix)); err != nil || !bytes.Equal(got, replacement.RawJSON) {
		t.Fatalf("new publication was changed: %s, %v", got, err)
	}
	assertMailboxPairAbsent(t, h.importer.InboxPath(), fresh.RequestID)
	if len(h.operations.runRequests) != 1 || h.operations.runRequests[0].RequestID != fresh.RequestID {
		t.Fatalf("wrong work invoked: %+v", h.operations.runRequests)
	}
	assertBUG039Original(t, h, original.RequestID, before)
	archive := bug039ArchivePath(h, conflict)
	if got, err := os.ReadFile(filepath.Join(archive, "request.json")); err != nil || !bytes.Equal(got, conflict.RawJSON) {
		t.Fatalf("old refused bytes not preserved: %s, %v", got, err)
	}
}

func seedBUG039TerminalRun(t *testing.T, h *p165Harness, requestID string) Request {
	t.Helper()
	ctx := context.Background()
	resolver := h.processor.executionResolver.(*p153Resolver)
	resolver.allowMacLocalOverride = true
	command := p091CreateTerminalCommand(t, h.authority)
	command, events, err := h.authority.GetCommandWithEvents(ctx, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	original := p153ProcessorRequest(t, requestID, "key-"+requestID, "run", map[string]any{"script": string(command.ScriptBytes), "environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}})
	selection, rejected, err := h.processor.resolveNewWorkSelection(ctx, original)
	if err != nil || rejected != nil {
		t.Fatalf("original selection: %+v, %v", rejected, err)
	}
	payload, hash, err := receiptCanonicalWithSelection(original, selection)
	if err != nil {
		t.Fatal(err)
	}
	ref := store.MailboxExchangeRef{MailboxID: h.importer.MailboxID(), ClientRequestID: requestID}
	exchange, _, err := h.authority.AcceptMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeCreate{MailboxID: ref.MailboxID, RequestID: requestID, Operation: "run", Controller: h.processor.controller, IdempotencyKey: original.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload, ResourceID: "job-bug039-original", Selection: selection, SelectionState: store.MailboxExecutionSelectionResolved})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CreateLocalIntent(ctx, store.LocalIntentCreate{IntentID: "intent-bug039-original", Operation: "run", ResourceID: "job-bug039-original", JobID: "job-bug039-original", SessionID: command.SessionID, CommandID: command.CommandID, Target: selection.Target, Environment: selection.Environment, Controller: h.processor.controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: exchange.ExecutionIdempotencyKey, PayloadJSON: payload, ScriptBytes: command.ScriptBytes, DeliveryState: store.LocalIntentAccepted}); err != nil {
		t.Fatal(err)
	}
	cursor := int64(len(events))
	accepted, _ := json.Marshal(map[string]any{"inbox_id": ref.MailboxID, "request_id": requestID, "operation": "run", "request_state": "accepted", "response_revision": 1, "job_id": "job-bug039-original", "session_id": command.SessionID, "command_id": command.CommandID, "delivery_state": "accepted"})
	if _, err := h.authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{State: store.MailboxExchangeAccepted, Bytes: accepted}); err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(map[string]any{"inbox_id": ref.MailboxID, "request_id": requestID, "operation": "run", "request_state": "complete", "response_revision": 2, "job_id": "job-bug039-original", "session_id": command.SessionID, "command_id": command.CommandID, "job_phase": "complete", "delivery_state": "accepted", "command_state": "succeeded", "exit_code": 0, "available_event_sequence": cursor, "final_event_sequence": cursor, "events_file": "events/" + string(command.CommandID) + ".ndjson", "output_complete": true, "output_truncated": false, "teardown_outcome": "closed"})
	if _, err := h.authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{State: store.MailboxExchangeComplete, Bytes: response, AvailableEventSequence: &cursor}); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.projector.PublishCommand(ctx, requestID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.AcknowledgeMailboxExchangeInMailbox(ctx, ref, store.MailboxAcknowledgement{ResponseRevision: 2, AvailableEventSequence: &cursor}); err != nil {
		t.Fatal(err)
	}
	return original
}

type bug039OriginalSnapshot struct {
	record store.MailboxExchangeRecord
	files  map[string][]byte
}

func snapshotBUG039Original(t *testing.T, h *p165Harness, requestID string) bug039OriginalSnapshot {
	t.Helper()
	record, err := h.authority.GetMailboxExchangeInMailbox(context.Background(), store.MailboxExchangeRef{MailboxID: h.importer.MailboxID(), ClientRequestID: requestID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := bug039OriginalSnapshot{record: record, files: make(map[string][]byte)}
	path, err := h.processor.projector.Outbox.Path(requestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{path, filepath.Join(h.importer.root, "events", string(record.EventFileCommandID)+".ndjson")} {
		if record.EventFileCommandID == "" && filepath.Base(path) == ".ndjson" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.files[path] = raw
	}
	return snapshot
}

func assertBUG039Original(t *testing.T, h *p165Harness, requestID string, want bug039OriginalSnapshot) {
	t.Helper()
	got := snapshotBUG039Original(t, h, requestID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("original binding/result/ACK/event evidence changed\nbefore=%+v\nafter=%+v", want.record, got.record)
	}
}

func bug039ChangedRun(t *testing.T, h *p165Harness, original Request, script string) Request {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(original.RawJSON, &fields); err != nil {
		t.Fatal(err)
	}
	fields["script"] = script
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	request, err := h.importer.validateRequest(original.RequestID, raw)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func bug039ArchivePath(h *p165Harness, request Request) string {
	return filepath.Join(h.importer.root, "conflicts", request.RequestID, fmt.Sprintf("%x", sha256.Sum256(request.RawJSON)))
}

func assertBUG039Archived(t *testing.T, h *p165Harness, request Request) {
	t.Helper()
	archive := bug039ArchivePath(h, request)
	for _, file := range []string{"request.json", "request.ready", "diagnostic.json", "retired"} {
		p := filepath.Join(archive, file)
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != MailboxFileMode {
			t.Fatalf("private preserved artifact %s: %v, %v", file, st, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(archive, "request.json")); err != nil || !bytes.Equal(got, request.RawJSON) {
		t.Fatalf("wrong refused input preserved: %s, %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(archive, "diagnostic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic requestConflictRecord
	if err := json.Unmarshal(raw, &diagnostic); err != nil || diagnostic.Code != requestConflictCode || diagnostic.Accepted || diagnostic.Executed {
		t.Fatalf("conflict diagnostic=%+v, %v", diagnostic, err)
	}
	if bytes.Contains(raw, []byte(request.Script)) || bytes.Contains(raw, []byte(request.IdempotencyKey)) {
		t.Fatalf("diagnostic leaked submitted values: %s", raw)
	}
}

type bug039OtherMailboxResolver struct{ base *p153Resolver }

func (r bug039OtherMailboxResolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error) {
	selection, err := r.base.ResolveMailboxExecution("analytics", environmentPresent, environment, targetPresent, target, repositoryAlias)
	selection.MailboxID = mailboxID
	return selection, err
}
