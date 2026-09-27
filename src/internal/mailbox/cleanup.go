package mailbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const UnmarkedDraftLifetime = 24 * time.Hour

// CleanupUnmarkedDrafts removes expired, unmarked JSON drafts from only the
// configured inbox and ACK directories. The injected importer clock makes the
// age boundary deterministic in tests.
func (i *Importer) CleanupUnmarkedDrafts(ctx context.Context) (int, error) {
	if i == nil || i.root == "" || i.inbox == "" || i.acks == "" {
		return 0, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now
	if i.clock != nil {
		now = i.clock
	}
	cutoff := now().Add(-UnmarkedDraftLifetime)
	removed := 0
	for _, directory := range []string{i.inbox, i.acks} {
		if err := ensureOwnerDirectory(directory); err != nil {
			return removed, err
		}
		count, err := cleanupUnmarkedDraftDirectory(ctx, directory, cutoff)
		removed += count
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func cleanupUnmarkedDraftDirectory(ctx context.Context, directory string, cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("%w: read draft directory: %v", ErrMailboxPath, err)
	}
	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, RequestSuffix) {
			continue
		}
		requestID := strings.TrimSuffix(name, RequestSuffix)
		if _, ok := safeRequestID(requestID); !ok {
			continue
		}
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("%w: inspect draft: %v", ErrMailboxPath, err)
		}
		if !safeDraftInfo(info) || info.ModTime().After(cutoff) {
			continue
		}
		marked, err := markerExists(directory, requestID)
		if err != nil {
			return removed, err
		}
		if marked {
			continue
		}
		// Recheck immediately before unlinking so a published marker or unsafe
		// replacement observed during cleanup is left for normal import/review.
		info, err = os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("%w: recheck draft: %v", ErrMailboxPath, err)
		}
		if !safeDraftInfo(info) || info.ModTime().After(cutoff) {
			continue
		}
		marked, err = markerExists(directory, requestID)
		if err != nil {
			return removed, err
		}
		if marked {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("%w: remove unmarked draft: %v", ErrMailboxPath, err)
		}
		if err := syncMailboxDirectory(directory); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func safeDraftInfo(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() && info.Mode().Perm() == MailboxFileMode
}

func markerExists(directory, requestID string) (bool, error) {
	path := filepath.Join(directory, requestID+ReadySuffix)
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("%w: inspect draft marker: %v", ErrMailboxPath, err)
}

// removeMailboxPair removes the publication marker first. Each unlink is
// directory-synced before proceeding, so a crash either permits safe replay
// through the durable receipt or leaves only an inert unmarked JSON draft.
func removeMailboxPair(directory, requestID string) error {
	if _, ok := safeRequestID(requestID); !ok {
		return fmt.Errorf("%w: request ID is not a safe basename", ErrMailboxPath)
	}
	if err := ensureOwnerDirectory(directory); err != nil {
		return err
	}
	for _, name := range []string{requestID + ReadySuffix, requestID + RequestSuffix} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: inspect imported pair: %v", ErrMailboxPath, err)
		}
		if !safeDraftInfo(info) {
			return fmt.Errorf("%w: imported pair member must be a regular 0600 file", ErrMailboxPath)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: remove imported pair member: %v", ErrMailboxPath, err)
		}
		if err := syncMailboxDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func syncMailboxDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("%w: open mailbox directory for sync: %v", ErrMailboxPath, err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync mailbox directory: %v", ErrMailboxPath, err)
	}
	return nil
}
