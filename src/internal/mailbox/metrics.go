package mailbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadyRequestCount reports published regular request markers waiting in the
// inbox. It never creates a mailbox directory, opens request bodies, or
// follows symlinks.
func (i *Importer) ReadyRequestCount(ctx context.Context) (int64, error) {
	if i == nil || i.root == "" {
		return 0, ErrImporterConfiguration
	}
	return ReadyRequestCountAtRoot(ctx, i.root)
}

// ReadyRequestCountAtRoot reports a safe ready-marker count without creating
// a missing root or inbox. A missing tree has no publishable request and is
// therefore counted as zero; an existing unsafe tree is rejected.
func ReadyRequestCountAtRoot(ctx context.Context, root string) (int64, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return 0, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootExists, err := inspectOwnerDirectory(root)
	if err != nil {
		return 0, err
	}
	if !rootExists {
		return 0, nil
	}
	inbox := filepath.Join(root, "inbox")
	inboxExists, err := inspectOwnerDirectory(inbox)
	if err != nil {
		return 0, err
	}
	if !inboxExists {
		return 0, nil
	}
	return readyRequestCountAtInbox(ctx, inbox)
}

func inspectOwnerDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect %s: %v", ErrMailboxPath, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !mailboxFileOwnedByCurrentUser(info) || info.Mode().Perm() != MailboxDirectoryMode {
		return false, fmt.Errorf("%w: %s must be an owner-only directory", ErrMailboxPath, path)
	}
	return true, nil
}

func readyRequestCountAtInbox(ctx context.Context, inbox string) (int64, error) {
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return 0, fmt.Errorf("%w: read inbox backlog: %v", ErrMailboxPath, err)
	}
	var count int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if !strings.HasSuffix(entry.Name(), ReadySuffix) {
			continue
		}
		// ReadDir returns names; always inspect beneath the validated inbox.
		info, err := os.Lstat(filepath.Join(inbox, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return count, fmt.Errorf("%w: inspect inbox marker: %v", ErrMailboxPath, err)
		}
		if safeIngressFileInfo(info) {
			count++
		}
	}
	return count, nil
}
