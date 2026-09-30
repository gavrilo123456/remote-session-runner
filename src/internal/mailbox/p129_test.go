package mailbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestP129ReadyRequestCountCountsOnlyRegularIngressMarkers(t *testing.T) {
	importer, err := NewImporter(filepath.Join(t.TempDir(), "mailbox"), nil)
	if err != nil {
		t.Fatal(err)
	}
	inbox := importer.InboxPath()
	native := filepath.Join(inbox, "native-p129"+ReadySuffix)
	if err := os.WriteFile(native, nil, MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(native, MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(inbox, "workspace-p129"+ReadySuffix)
	if err := os.WriteFile(workspace, nil, MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{
		"unsafe-mode-0640-p129": 0o640,
		"unsafe-mode-0664-p129": 0o664,
	} {
		path := filepath.Join(inbox, name+ReadySuffix)
		if err := os.WriteFile(path, nil, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(inbox, "directory-p129"+ReadySuffix), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "draft-p129"+RequestSuffix), []byte("ignored"), MailboxFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(native, filepath.Join(inbox, "linked-p129"+ReadySuffix)); err != nil {
		t.Fatal(err)
	}
	got, err := importer.ReadyRequestCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("ready marker count=%d, want two exact-mode regular markers", got)
	}
}
