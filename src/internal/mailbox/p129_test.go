package mailbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestP129ReadyRequestCountCountsOnlyOwnerOnlyRegularMarkers(t *testing.T) {
	importer, err := NewImporter(filepath.Join(t.TempDir(), "mailbox"), nil)
	if err != nil {
		t.Fatal(err)
	}
	inbox := importer.InboxPath()
	valid := filepath.Join(inbox, "valid-p129"+ReadySuffix)
	if err := os.WriteFile(valid, nil, MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(valid, MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	unsafeMode := filepath.Join(inbox, "unsafe-mode-p129"+ReadySuffix)
	if err := os.WriteFile(unsafeMode, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeMode, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "draft-p129"+RequestSuffix), []byte("ignored"), MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(valid, filepath.Join(inbox, "linked-p129"+ReadySuffix)); err != nil {
		t.Fatal(err)
	}
	got, err := importer.ReadyRequestCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("ready marker count=%d, want only one secure regular marker", got)
	}
}
