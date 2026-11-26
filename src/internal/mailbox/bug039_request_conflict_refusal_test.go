package mailbox

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBUG039PreservationFailureLeavesOriginalAndConflictingPair(t *testing.T) {
	h := newP165Harness(t)
	original := seedBUG039TerminalRun(t, h, "req-bug039-preserve-refusal")
	before := snapshotBUG039Original(t, h, original.RequestID)
	conflict := bug039ChangedRun(t, h, original, "printf refused-publication")
	p165WritePair(t, h.importer, conflict.RequestID, conflict.RawJSON, MailboxFileMode, nil, MailboxFileMode)
	directory := filepath.Join(h.importer.root, "conflicts")
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, MailboxDirectoryMode) })
	results, err := h.processor.Import(context.Background())
	if err == nil || len(results) != 1 || results[0].Status != ResultRejected || results[0].Durable || results[0].PairRemoved {
		t.Fatalf("unsafe preservation=%+v, %v", results, err)
	}
	assertMailboxPairPresent(t, h.importer.InboxPath(), conflict.RequestID)
	assertBUG039Original(t, h, original.RequestID, before)
	if len(h.operations.runRequests) != 0 {
		t.Fatal("preservation refusal executed the input")
	}
	if err := os.Chmod(directory, MailboxDirectoryMode); err != nil {
		t.Fatal(err)
	}
	results, err = h.processor.Import(context.Background())
	if err != nil || len(results) != 1 || !results[0].PairRemoved {
		t.Fatalf("supported retry after safe permissions restored=%+v, %v", results, err)
	}
	assertBUG039Archived(t, h, conflict)
	assertBUG039Original(t, h, original.RequestID, before)
}

func TestBUG039RecoveryRequiresOriginalAuthorityBinding(t *testing.T) {
	h := newP165Harness(t)
	original := seedBUG039TerminalRun(t, h, "req-bug039-authority-refusal")
	before := snapshotBUG039Original(t, h, original.RequestID)
	conflict := bug039ChangedRun(t, h, original, "printf wrong-authority-proof")
	p165WritePair(t, h.importer, conflict.RequestID, conflict.RawJSON, MailboxFileMode, nil, MailboxFileMode)
	record := requestConflictRecord{
		FormatVersion: 1, MailboxID: h.importer.MailboxID(), RequestID: conflict.RequestID,
		InputSHA256:           fmt.Sprintf("%x", sha256.Sum256(conflict.RawJSON)),
		OriginalBindingSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("different original binding"))),
		Code:                  requestConflictCode, Message: requestConflictMessage,
	}
	if err := h.processor.conflicts.Preserve(context.Background(), record, conflict.RawJSON); err != nil {
		t.Fatal(err)
	}
	if removed, err := h.processor.completeRequestConflict(context.Background(), record); err == nil || removed {
		t.Fatalf("unrelated authority proof authorized cleanup: removed=%t err=%v", removed, err)
	}
	assertMailboxPairPresent(t, h.importer.InboxPath(), conflict.RequestID)
	assertBUG039Original(t, h, original.RequestID, before)
	if len(h.operations.runRequests) != 0 {
		t.Fatal("unproven recovery executed the input")
	}
}
