package mailbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/store"
)

// TestBUG003ImporterContinuesAfterOneHandlerFailure protects the outer inbox
// scan: a retained marker that needs retry must not hide the next independent
// marker in lexical order.
func TestBUG003ImporterContinuesAfterOneHandlerFailure(t *testing.T) {
	root := p081MailboxRoot(t)
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	const failedID = "req-bug003-a-failed"
	const completedID = "req-bug003-b-completed"
	writeP090RequestPair(t, importer, failedID, "key-bug003-a", "printf failed", MailboxFileMode)
	writeP090RequestPair(t, importer, completedID, "key-bug003-b", "printf completed", MailboxFileMode)

	var handled []string
	results, err := importer.importWithRecorder(context.Background(), func(_ context.Context, request Request) (bool, error) {
		handled = append(handled, request.RequestID)
		if request.RequestID == failedID {
			// The marker was valid when imported. Model a publication race before
			// durable pair removal, which makes only this pair cleanup fail.
			if err := os.Chmod(filepath.Join(importer.InboxPath(), failedID+ReadySuffix), 0o640); err != nil {
				t.Fatal(err)
			}
		}
		return true, nil
	})
	if !errors.Is(err, ErrMailboxPath) {
		t.Fatalf("import error=%v, want retained pair cleanup failure", err)
	}
	if len(handled) != 2 || handled[0] != failedID || handled[1] != completedID {
		t.Fatalf("handled markers=%v, want both in deterministic order", handled)
	}
	if len(results) != 2 || results[0].RequestID != failedID || results[0].Status != ResultAccepted || !results[0].Durable || results[0].PairRemoved ||
		results[1].RequestID != completedID || results[1].Status != ResultAccepted || !results[1].Durable || !results[1].PairRemoved {
		t.Fatalf("results=%+v", results)
	}
	assertPathPresent(t, filepath.Join(importer.InboxPath(), failedID+RequestSuffix))
	assertPathPresent(t, filepath.Join(importer.InboxPath(), failedID+ReadySuffix))
	assertMailboxPairAbsent(t, importer.InboxPath(), completedID)
}

// TestBUG003AcceptedRunFailureDoesNotBlockLaterRun exercises the precise
// mailbox response stall: a corrupt accepted receipt remains available for
// diagnosis, while a later accepted run is still projected in the same pass.
func TestBUG003AcceptedRunFailureDoesNotBlockLaterRun(t *testing.T) {
	ctx := context.Background()
	processor, _, authority, outbox := newP153ProcessorHarness(t, newP153Resolver(t))
	first := p153ProcessorRequest(t, "req-bug003-first", "key-bug003-first", "run", map[string]any{"script": "printf first"})
	second := p153ProcessorRequest(t, "req-bug003-second", "key-bug003-second", "run", map[string]any{"script": "printf second"})
	p153Process(t, ctx, processor, first)
	p153Process(t, ctx, processor, second)

	firstRef, err := store.NewMailboxExchangeRef("analytics", first.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	// This models a durable record damaged by an interrupted older build. It
	// is deliberately retained rather than deleted or replaced with guessed
	// target state.
	if _, err := authority.PublishMailboxResponseInMailbox(ctx, firstRef, store.MailboxResponsePublication{
		State: store.MailboxExchangeAccepted, Bytes: []byte("{"),
	}); err != nil {
		t.Fatal(err)
	}

	err = processor.Reconcile(ctx)
	if err == nil {
		t.Fatal("corrupt first accepted run unexpectedly reconciled without an error")
	}
	issues := ReconciliationIssues(err)
	if len(issues) != 1 || issues[0].MailboxID != "analytics" || issues[0].Stage != "accepted_run" ||
		issues[0].Operation != "run" || issues[0].RequestID != first.RequestID || issues[0].FailureClass != "stored_response_invalid" {
		t.Fatalf("safe reconciliation issues=%+v", issues)
	}

	firstRecord, err := authority.GetMailboxExchangeInMailbox(ctx, firstRef)
	if err != nil || firstRecord.State != store.MailboxExchangeAccepted || string(firstRecord.ResponseBytes) != "{" {
		t.Fatalf("first retained exchange=%+v err=%v", firstRecord, err)
	}
	secondRef, err := store.NewMailboxExchangeRef("analytics", second.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	secondRecord, err := authority.GetMailboxExchangeInMailbox(ctx, secondRef)
	if err != nil || secondRecord.State != store.MailboxExchangeAccepted || secondRecord.ResponseRevision < 2 {
		t.Fatalf("later run was not projected after first failure: exchange=%+v err=%v", secondRecord, err)
	}
	if _, err := outbox.Read(second.RequestID); err != nil {
		t.Fatalf("later run has no durable outbox projection: %v", err)
	}
}
