package mailbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestHandoffRequestGuardsPreserveHistoricalIdentitiesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := p081MailboxRoot(t)
	db, err := store.Open(ctx, filepath.Join(root, "state", "handoff.db"))
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
	callbacks := 0
	for restart := 0; restart < 2; restart++ {
		importer, err := New(Options{Root: root, Authorize: func(id, key string) error {
			if id == "req-source" || key == "key-source" {
				return errors.New("private source identity must not leak")
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if restart == 0 {
			writeP090RequestPair(t, importer, "req-source", "key-new", "printf source-id", MailboxFileMode)
			writeP090RequestPair(t, importer, "req-renamed", "key-source", "printf source-key", MailboxFileMode)
			writeP090RequestPair(t, importer, "req-fresh", "key-fresh", "printf fresh", MailboxFileMode)
		}
		processor, err := NewReceiptProcessor(ReceiptProcessorOptions{Importer: importer, Authority: authority, Controller: owner, Handler: func(context.Context, Request) error { callbacks++; return nil }})
		if err != nil {
			t.Fatal(err)
		}
		results, err := processor.Import(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.RequestID == "req-fresh" {
				continue
			}
			if result.Status != ResultRejected || result.Reason != ErrMailboxOwnership.Error() || result.Durable || result.PairRemoved {
				t.Fatalf("guarded result=%+v", result)
			}
		}
		for _, id := range []string{"req-source", "req-renamed"} {
			assertMailboxPairPresent(t, importer.InboxPath(), id)
			if _, err := authority.GetMailboxExchange(ctx, id); !errors.Is(err, store.ErrMailboxExchangeNotFound) {
				t.Fatalf("blocked identity persisted: %s err=%v", id, err)
			}
		}
	}
	if callbacks != 1 {
		t.Fatalf("fresh callbacks=%d, want exactly one", callbacks)
	}
}

func TestHandoffOwnershipLossBeforeDispatchAndDraftCleanupPreservesInput(t *testing.T) {
	root := p081MailboxRoot(t)
	revoked := false
	calls := 0
	importer, err := New(Options{Root: root, Authorize: func(_, _ string) error {
		if revoked {
			return errors.New("private marker detail")
		}
		return nil
	}, Handler: func(context.Context, Request) error { calls++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	writeP082MailboxPair(t, importer, "req-revoked", "key-revoked", "printf inert")
	mailboxIngressAfterRequestReadHook = func() { revoked = true }
	t.Cleanup(func() { mailboxIngressAfterRequestReadHook = nil })
	results, err := importer.Import(context.Background())
	if err != nil || len(results) != 1 || results[0].Reason != ErrMailboxOwnership.Error() || calls != 0 {
		t.Fatalf("results=%+v calls=%d err=%v", results, calls, err)
	}
	assertMailboxPairPresent(t, importer.InboxPath(), "req-revoked")
	draft := filepath.Join(importer.InboxPath(), "req-old-draft.json")
	writeP090Draft(t, draft, time.Now().Add(-2*UnmarkedDraftLifetime), MailboxFileMode)
	if _, err := importer.CleanupUnmarkedDrafts(context.Background()); !errors.Is(err, ErrMailboxOwnership) {
		t.Fatalf("cleanup=%v", err)
	}
	if _, err := os.Stat(draft); err != nil {
		t.Fatalf("unauthorized cleanup changed draft: %v", err)
	}
}

func TestHandoffACKGuardsNeverAcknowledgeOrRemoveSourcePairs(t *testing.T) {
	ctx := context.Background()
	root := p081MailboxRoot(t)
	db, err := store.Open(ctx, filepath.Join(root, "state", "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	p090CreateTerminalExchange(t, authority, "req-source")
	p090CreateTerminalExchange(t, authority, "req-fresh")
	for restart := 0; restart < 2; restart++ {
		importer, err := NewAckImporter(AckImporterOptions{Root: root, Authority: authority, Authorize: func(id string) error {
			if id == "req-source" {
				return errors.New("source-owned ACK")
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if restart == 0 {
			writeP090AckPairWithMode(t, importer, "req-source", 1, int64Ptr(3), MailboxFileMode)
			writeP090AckPairWithMode(t, importer, "req-fresh", 1, int64Ptr(3), MailboxFileMode)
		}
		results, err := importer.Import(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.RequestID == "req-source" && (result.Reason != ErrMailboxOwnership.Error() || result.Durable || result.PairRemoved) {
				t.Fatalf("source ACK=%+v", result)
			}
		}
		assertMailboxPairPresent(t, importer.AcksPath(), "req-source")
		assertMailboxPairAbsent(t, importer.AcksPath(), "req-fresh")
		record, err := authority.GetMailboxExchange(ctx, "req-source")
		if err != nil || record.AcknowledgedAt != nil {
			t.Fatalf("source ACK changed: %+v err=%v", record, err)
		}
	}
}
