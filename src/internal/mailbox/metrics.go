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
// inbox. It never opens request bodies or follows symlinks.
func (i *Importer) ReadyRequestCount(ctx context.Context) (int64, error) {
	if i == nil || i.inbox == "" {
		return 0, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ensureOwnerDirectory(i.inbox); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(i.inbox)
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
		info, err := os.Lstat(filepath.Join(i.inbox, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return count, fmt.Errorf("%w: inspect inbox marker: %v", ErrMailboxPath, err)
		}
		if info.Mode().IsRegular() && info.Mode().Perm() == MailboxFileMode {
			count++
		}
	}
	return count, nil
}
