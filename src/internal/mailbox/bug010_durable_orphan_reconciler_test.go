package mailbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/store"
)

func TestBUG010DurableOrphanReconcilerRemovesOnlyProvenMarkers(t *testing.T) {
	ctx, root, authority, importer, acknowledgements, reconciler := newB010DurableOrphanHarness(t, defaultDurableOrphanReconciliationLimit)

	completeID := "req-b010-orphan-complete"
	p090CreateTerminalExchange(t, authority, completeID)
	b010WriteOrphanMarker(t, importer.InboxPath(), completeID, MailboxFileMode)

	rejectedID := "req-b010-orphan-rejected"
	b010CompleteExchange(t, authority, rejectedID, store.MailboxExchangeRejected)
	b010WriteOrphanMarker(t, importer.InboxPath(), rejectedID, MailboxWorkspaceIngressFileMode)

	indeterminateID := "req-b010-orphan-indeterminate"
	b010CompleteExchange(t, authority, indeterminateID, store.MailboxExchangeIndeterminate)
	b010WriteOrphanMarker(t, importer.InboxPath(), indeterminateID, MailboxFileMode)

	diagnosticID := "req-b010-orphan-diagnostic"
	b010RecordCompletedDiagnostic(t, authority, diagnosticID)
	b010WriteOrphanMarker(t, importer.InboxPath(), diagnosticID, MailboxFileMode)

	ackID := "req-b010-orphan-acknowledged"
	p090CreateTerminalExchange(t, authority, ackID)
	if _, err := authority.AcknowledgeMailboxExchange(ctx, store.MailboxAcknowledgement{
		RequestID: ackID, ResponseRevision: 1, AvailableEventSequence: int64Ptr(3),
	}); err != nil {
		t.Fatal(err)
	}
	b010WriteOrphanMarker(t, acknowledgements.AcksPath(), ackID, MailboxWorkspaceIngressFileMode)

	// The reconciler does not own response or event retention. These sentinels
	// prove that a marker cleanup leaves terminal artifacts alone.
	outbox, err := NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	events, err := NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	responsePath, err := outbox.Path(completeID)
	if err != nil {
		t.Fatal(err)
	}
	eventPath, err := events.Path("cmd-b010-orphan-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, responsePath, []byte("B010_RESPONSE_SENTINEL"), MailboxFileMode)
	writeMailboxFile(t, eventPath, []byte("B010_EVENT_SENTINEL"), MailboxFileMode)

	report, err := reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.MarkersRemoved != 5 || report.StagesRecovered != 0 {
		t.Fatalf("cleanup report=%+v, want five public removals", report)
	}
	for _, requestID := range []string{completeID, rejectedID, indeterminateID, diagnosticID} {
		assertPathAbsent(t, filepath.Join(importer.InboxPath(), requestID+ReadySuffix))
	}
	assertPathAbsent(t, filepath.Join(acknowledgements.AcksPath(), ackID+ReadySuffix))

	if data, err := os.ReadFile(responsePath); err != nil || string(data) != "B010_RESPONSE_SENTINEL" {
		t.Fatalf("response sentinel=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(eventPath); err != nil || string(data) != "B010_EVENT_SENTINEL" {
		t.Fatalf("event sentinel=%q err=%v", data, err)
	}
	for _, requestID := range []string{completeID, rejectedID, indeterminateID, ackID} {
		ref, err := store.NewMailboxExchangeRef(store.DefaultMailboxID, requestID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authority.GetMailboxExchangeInMailbox(ctx, ref); err != nil {
			t.Fatalf("durable exchange %s changed or missing: %v", requestID, err)
		}
	}
	evidence, err := authority.LookupMailboxLifecycleEvidenceForRequestIDsInMailbox(ctx, store.DefaultMailboxID, []string{ackID, diagnosticID})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence[ackID].ExchangeAcknowledged || !evidence[diagnosticID].DiagnosticInputPairRemoved {
		t.Fatalf("durable evidence changed: %+v", evidence)
	}
}

func TestBUG010DurableOrphanReconcilerRetainsUnprovenOrUnsafeMarkers(t *testing.T) {
	ctx, root, authority, importer, acknowledgements, reconciler := newB010DurableOrphanHarness(t, defaultDurableOrphanReconciliationLimit)
	_ = acknowledgements

	unknownID := "req-b010-orphan-unknown"
	b010WriteOrphanMarker(t, importer.InboxPath(), unknownID, MailboxWorkspaceIngressFileMode)

	incompleteDiagnosticID := "req-b010-orphan-diagnostic-incomplete"
	raw := p081RunJSON(t, incompleteDiagnosticID, "echo incomplete")
	ref, err := store.NewMailboxIngressDiagnosticRef(store.DefaultMailboxID, incompleteDiagnosticID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, sha256.Sum256(raw), store.MailboxIngressDiagnosticInvalidRequestSchema); err != nil {
		t.Fatal(err)
	}
	b010WriteOrphanMarker(t, importer.InboxPath(), incompleteDiagnosticID, MailboxFileMode)

	nonemptyID := "req-b010-orphan-nonempty"
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), nonemptyID+ReadySuffix), []byte("x"), MailboxFileMode)
	unsafeID := "req-b010-orphan-unsafe"
	b010WriteOrphanMarker(t, importer.InboxPath(), unsafeID, 0o640)
	linkTarget := filepath.Join(root, "b010-orphan-link-target")
	writeMailboxFile(t, linkTarget, nil, MailboxFileMode)
	symlinkID := "req-b010-orphan-symlink"
	if err := os.Symlink(linkTarget, filepath.Join(importer.InboxPath(), symlinkID+ReadySuffix)); err != nil {
		t.Fatal(err)
	}

	crossMailboxID := "req-b010-orphan-cross-mailbox"
	b010AcceptExchange(t, authority, "analytics", crossMailboxID)
	b010WriteOrphanMarker(t, importer.InboxPath(), crossMailboxID, MailboxFileMode)

	for cycle := 0; cycle < 2; cycle++ {
		report, err := reconciler.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if report.MarkersRemoved != 0 || report.StagesRecovered != 0 {
			t.Fatalf("cycle %d cleanup report=%+v", cycle, report)
		}
	}
	for _, requestID := range []string{unknownID, incompleteDiagnosticID, nonemptyID, unsafeID, symlinkID, crossMailboxID} {
		assertPathPresent(t, filepath.Join(importer.InboxPath(), requestID+ReadySuffix))
	}
	if _, err := authority.GetMailboxExchangeInMailbox(ctx, mustB010ExchangeRef(t, store.DefaultMailboxID, unknownID)); !errors.Is(err, store.ErrMailboxExchangeNotFound) {
		t.Fatalf("unknown marker created exchange: %v", err)
	}
	if _, err := authority.GetMailboxIngressDiagnosticInMailbox(ctx, ref); err != nil {
		t.Fatalf("incomplete diagnostic record changed: %v", err)
	}
}

func TestBUG010DurableOrphanReconcilerIsBoundedAndRecoversStages(t *testing.T) {
	ctx, _, authority, importer, _, reconciler := newB010DurableOrphanHarness(t, 1)
	firstID := "req-b010-orphan-limit-a"
	secondID := "req-b010-orphan-limit-b"
	for _, requestID := range []string{firstID, secondID} {
		p090CreateTerminalExchange(t, authority, requestID)
		b010WriteOrphanMarker(t, importer.InboxPath(), requestID, MailboxFileMode)
	}

	report, err := reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.MarkersRemoved != 1 || report.StagesRecovered != 0 {
		t.Fatalf("first bounded report=%+v", report)
	}
	assertPathAbsent(t, filepath.Join(importer.InboxPath(), firstID+ReadySuffix))
	assertPathPresent(t, filepath.Join(importer.InboxPath(), secondID+ReadySuffix))
	if _, err := reconciler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathAbsent(t, filepath.Join(importer.InboxPath(), secondID+ReadySuffix))

	stagedID := "req-b010-orphan-stage"
	p090CreateTerminalExchange(t, authority, stagedID)
	stagePath := durableOrphanStagePath(importer.InboxPath(), stagedID, durableOrphanRequestArea)
	writeMailboxFile(t, stagePath, nil, MailboxWorkspaceIngressFileMode)
	unknownStageID := "req-b010-orphan-stage-unknown"
	unknownStagePath := durableOrphanStagePath(importer.InboxPath(), unknownStageID, durableOrphanRequestArea)
	writeMailboxFile(t, unknownStagePath, nil, MailboxFileMode)

	report, err = reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.MarkersRemoved != 0 || report.StagesRecovered != 1 {
		t.Fatalf("stage recovery report=%+v", report)
	}
	assertPathAbsent(t, stagePath)
	assertPathPresent(t, unknownStagePath)

	deferredID := "req-b010-orphan-stage-new-draft"
	p090CreateTerminalExchange(t, authority, deferredID)
	deferredStagePath := durableOrphanStagePath(importer.InboxPath(), deferredID, durableOrphanRequestArea)
	writeMailboxFile(t, deferredStagePath, nil, MailboxFileMode)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), deferredID+RequestSuffix), p081RunJSON(t, deferredID, "echo replacement"), MailboxFileMode)
	report, err = reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.MarkersRemoved != 0 || report.StagesRecovered != 0 {
		t.Fatalf("draft-stage recovery report=%+v", report)
	}
	assertPathPresent(t, deferredStagePath)
	assertPathPresent(t, filepath.Join(importer.InboxPath(), deferredID+RequestSuffix))
	b010WriteOrphanMarker(t, importer.InboxPath(), deferredID, MailboxFileMode)
	report, err = reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.MarkersRemoved != 0 || report.StagesRecovered != 1 {
		t.Fatalf("published-stage recovery report=%+v", report)
	}
	assertPathAbsent(t, deferredStagePath)
	assertMailboxPairPresent(t, importer.InboxPath(), deferredID)
}

func TestBUG010DurableOrphanReconcilerPreservesPublisherReplacement(t *testing.T) {
	for _, testCase := range []struct {
		name string
		hook func(*testing.T, *Importer, string)
	}{
		{
			name: "replacement before old marker staging",
			hook: func(t *testing.T, importer *Importer, requestID string) {
				t.Helper()
				p165ReplaceRequestOnly(t, importer, requestID, p165ValidRunRequest(requestID), MailboxFileMode)
				marker := filepath.Join(importer.InboxPath(), requestID+ReadySuffix)
				replacement := marker + ".replacement"
				writeMailboxFile(t, replacement, nil, MailboxFileMode)
				if err := os.Rename(replacement, marker); err != nil {
					t.Fatal(err)
				}
				if err := syncMailboxDirectory(importer.InboxPath()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "replacement after old marker staging",
			hook: func(t *testing.T, importer *Importer, requestID string) {
				t.Helper()
				p165ReplaceRequestOnly(t, importer, requestID, p165ValidRunRequest(requestID), MailboxFileMode)
				marker := filepath.Join(importer.InboxPath(), requestID+ReadySuffix)
				replacement := marker + ".replacement"
				writeMailboxFile(t, replacement, nil, MailboxFileMode)
				if err := os.Rename(replacement, marker); err != nil {
					t.Fatal(err)
				}
				if err := syncMailboxDirectory(importer.InboxPath()); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, _, authority, importer, _, reconciler := newB010DurableOrphanHarness(t, defaultDurableOrphanReconciliationLimit)
			const requestID = "req-b010-orphan-replacement"
			p090CreateTerminalExchange(t, authority, requestID)
			b010WriteOrphanMarker(t, importer.InboxPath(), requestID, MailboxFileMode)
			if testCase.name == "replacement before old marker staging" {
				durableOrphanBeforeMarkerStageHook = func() { testCase.hook(t, importer, requestID) }
			} else {
				durableOrphanAfterMarkerStageHook = func() { testCase.hook(t, importer, requestID) }
			}
			t.Cleanup(func() {
				durableOrphanBeforeMarkerStageHook = nil
				durableOrphanAfterMarkerStageHook = nil
			})

			if _, err := reconciler.Run(ctx); err != nil {
				t.Fatal(err)
			}
			assertMailboxPairPresent(t, importer.InboxPath(), requestID)
			assertPathAbsent(t, durableOrphanStagePath(importer.InboxPath(), requestID, durableOrphanRequestArea))

			var accepted []Request
			plain, err := New(Options{Root: filepath.Dir(importer.InboxPath()), Handler: func(_ context.Context, request Request) error {
				accepted = append(accepted, request)
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			results, err := plain.importWithRecorder(ctx, func(_ context.Context, request Request) (bool, error) {
				accepted = append(accepted, request)
				return true, nil
			})
			if err != nil || len(results) != 1 || results[0].Status != ResultAccepted || len(accepted) != 1 {
				t.Fatalf("replacement intake results=%+v accepted=%+v err=%v", results, accepted, err)
			}
			assertMailboxPairAbsent(t, importer.InboxPath(), requestID)
			results, err = plain.importWithRecorder(ctx, func(_ context.Context, request Request) (bool, error) {
				accepted = append(accepted, request)
				return true, nil
			})
			if err != nil || len(results) != 0 || len(accepted) != 1 {
				t.Fatalf("replacement replay results=%+v accepted=%+v err=%v", results, accepted, err)
			}
		})
	}
}

func TestBUG010DurableOrphanReconcilerKeepsUnsafeReplacementInertWhileFreshWorkProgresses(t *testing.T) {
	ctx, _, authority, importer, _, reconciler := newB010DurableOrphanHarness(t, defaultDurableOrphanReconciliationLimit)
	const orphanID = "req-b010-orphan-unsafe-replacement"
	p090CreateTerminalExchange(t, authority, orphanID)
	b010WriteOrphanMarker(t, importer.InboxPath(), orphanID, MailboxFileMode)
	durableOrphanAfterMarkerStageHook = func() {
		marker := filepath.Join(importer.InboxPath(), orphanID+ReadySuffix)
		replacement := marker + ".replacement"
		writeMailboxFile(t, replacement, []byte("unsafe"), MailboxWorkspaceIngressFileMode)
		if err := os.Rename(replacement, marker); err != nil {
			t.Fatal(err)
		}
		if err := syncMailboxDirectory(importer.InboxPath()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { durableOrphanAfterMarkerStageHook = nil })

	report, err := reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.MarkersRemoved != 1 || report.StagesRecovered != 0 {
		t.Fatalf("unsafe replacement report=%+v", report)
	}
	marker := filepath.Join(importer.InboxPath(), orphanID+ReadySuffix)
	info, err := os.Lstat(marker)
	if err != nil || info.Size() != int64(len("unsafe")) || !safeIngressFileInfo(info) {
		t.Fatalf("unsafe replacement marker=%+v err=%v", info, err)
	}

	const freshID = "req-b010-orphan-fresh-work"
	writeP090RequestPair(t, importer, freshID, "key-b010-orphan-fresh-work", "echo fresh", MailboxWorkspaceIngressFileMode)
	plain, err := New(Options{Root: filepath.Dir(importer.InboxPath())})
	if err != nil {
		t.Fatal(err)
	}
	var accepted []Request
	results, err := plain.importWithRecorder(ctx, func(_ context.Context, request Request) (bool, error) {
		accepted = append(accepted, request)
		return true, nil
	})
	if err != nil || len(results) != 2 || len(accepted) != 1 || accepted[0].RequestID != freshID {
		t.Fatalf("fresh work alongside unsafe replacement results=%+v accepted=%+v err=%v", results, accepted, err)
	}
	assertMailboxPairAbsent(t, importer.InboxPath(), freshID)
	assertPathPresent(t, marker)
}

func TestBUG010DurableOrphanReconcilerRestoresPreStageChangedMarkers(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		data    []byte
		mode    os.FileMode
		symlink bool
	}{
		{name: "nonempty", data: []byte("not-empty"), mode: MailboxWorkspaceIngressFileMode},
		{name: "unsafe mode", data: nil, mode: 0o640},
		{name: "symlink", symlink: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, _, authority, importer, _, reconciler := newB010DurableOrphanHarness(t, defaultDurableOrphanReconciliationLimit)
			const orphanID = "req-b010-orphan-pre-stage-replacement"
			p090CreateTerminalExchange(t, authority, orphanID)
			b010WriteOrphanMarker(t, importer.InboxPath(), orphanID, MailboxFileMode)
			durableOrphanBeforeMarkerStageHook = func() {
				marker := filepath.Join(importer.InboxPath(), orphanID+ReadySuffix)
				replacement := marker + ".replacement"
				if testCase.symlink {
					target := filepath.Join(filepath.Dir(importer.InboxPath()), "b010-orphan-pre-stage-symlink-target")
					writeMailboxFile(t, target, nil, MailboxFileMode)
					if err := os.Symlink(target, replacement); err != nil {
						t.Fatal(err)
					}
				} else {
					writeMailboxFile(t, replacement, testCase.data, testCase.mode)
				}
				if err := os.Rename(replacement, marker); err != nil {
					t.Fatal(err)
				}
				if err := syncMailboxDirectory(importer.InboxPath()); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { durableOrphanBeforeMarkerStageHook = nil })

			report, err := reconciler.Run(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if report.MarkersRemoved != 0 || report.StagesRecovered != 0 {
				t.Fatalf("pre-stage replacement report=%+v", report)
			}
			marker := filepath.Join(importer.InboxPath(), orphanID+ReadySuffix)
			info, err := os.Lstat(marker)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.symlink {
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink replacement became %v", info.Mode())
				}
			} else if info.Size() != int64(len(testCase.data)) || info.Mode().Perm() != testCase.mode {
				t.Fatalf("restored marker=%+v err=%v", info, err)
			}
			assertPathAbsent(t, durableOrphanStagePath(importer.InboxPath(), orphanID, durableOrphanRequestArea))

			// A repeated cleanup scan leaves the changed marker inert and does
			// not prevent a separate valid pair from progressing.
			if report, err := reconciler.Run(ctx); err != nil || report.MarkersRemoved != 0 || report.StagesRecovered != 0 {
				t.Fatalf("repeated pre-stage replacement report=%+v err=%v", report, err)
			}
			const freshID = "req-b010-orphan-pre-stage-fresh"
			writeP090RequestPair(t, importer, freshID, "key-b010-orphan-pre-stage-fresh", "echo fresh", MailboxFileMode)
			plain, err := New(Options{Root: filepath.Dir(importer.InboxPath())})
			if err != nil {
				t.Fatal(err)
			}
			var accepted []Request
			results, err := plain.importWithRecorder(ctx, func(_ context.Context, request Request) (bool, error) {
				accepted = append(accepted, request)
				return true, nil
			})
			if err != nil || len(results) != 2 || len(accepted) != 1 || accepted[0].RequestID != freshID {
				t.Fatalf("fresh work after pre-stage replacement results=%+v accepted=%+v err=%v", results, accepted, err)
			}
			assertMailboxPairAbsent(t, importer.InboxPath(), freshID)
			assertPathPresent(t, marker)
		})
	}
}

func newB010DurableOrphanHarness(t *testing.T, limit int) (context.Context, string, *store.AuthorityStore, *Importer, *AckImporter, *DurableOrphanReconciler) {
	t.Helper()
	ctx := context.Background()
	root := p081MailboxRoot(t)
	if err := os.Mkdir(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, filepath.Join(root, "state", "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	importer, err := NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgements, err := NewAckImporter(AckImporterOptions{Root: root, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewDurableOrphanReconciler(DurableOrphanReconcilerOptions{
		MailboxID: store.DefaultMailboxID, Root: root, Authority: authority, Limit: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, root, authority, importer, acknowledgements, reconciler
}

func b010CompleteExchange(t *testing.T, authority *store.AuthorityStore, requestID string, state store.MailboxExchangeState) {
	t.Helper()
	b010AcceptExchange(t, authority, store.DefaultMailboxID, requestID)
	ref := mustB010ExchangeRef(t, store.DefaultMailboxID, requestID)
	if _, err := authority.CompleteMailboxExchangeInMailbox(context.Background(), ref, state); err != nil {
		t.Fatal(err)
	}
}

func b010RecordCompletedDiagnostic(t *testing.T, authority *store.AuthorityStore, requestID string) {
	t.Helper()
	raw := p081RunJSON(t, requestID, "echo diagnostic")
	ref, err := store.NewMailboxIngressDiagnosticRef(store.DefaultMailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if _, _, err := authority.RecordMailboxIngressDiagnosticInMailbox(context.Background(), ref, digest, store.MailboxIngressDiagnosticInvalidRequestSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(context.Background(), ref, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(context.Background(), ref, digest); err != nil {
		t.Fatal(err)
	}
}

func b010WriteOrphanMarker(t *testing.T, directory, requestID string, mode os.FileMode) {
	t.Helper()
	writeMailboxFile(t, filepath.Join(directory, requestID+ReadySuffix), nil, mode)
}

func mustB010ExchangeRef(t *testing.T, mailboxID, requestID string) store.MailboxExchangeRef {
	t.Helper()
	ref, err := store.NewMailboxExchangeRef(mailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
