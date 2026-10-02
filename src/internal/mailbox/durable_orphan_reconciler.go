package mailbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"remote-session-runner/src/internal/store"
)

const defaultDurableOrphanReconciliationLimit = 64

type durableOrphanArea string

const (
	durableOrphanRequestArea durableOrphanArea = "request"
	durableOrphanAckArea     durableOrphanArea = "ack"
)

var (
	// Test-only deterministic race seams. Production leaves both nil. They run
	// after proof and filesystem revalidation, respectively before and after
	// moving the old marker out of its public pathname.
	durableOrphanBeforeMarkerStageHook func()
	durableOrphanAfterMarkerStageHook  func()
)

// DurableOrphanReconcilerOptions configures a bounded, read-only-except-for-
// proven-marker-removal reconciliation pass for one configured mailbox.
type DurableOrphanReconcilerOptions struct {
	MailboxID string
	Root      string
	Authority *store.AuthorityStore
	Limit     int
}

// DurableOrphanReconciler removes only marker-only residue which an existing
// durable receipt or acknowledgement proves cannot start work. It never reads
// request or ACK bodies, removes JSON, creates a response, or calls an
// execution operation.
type DurableOrphanReconciler struct {
	classifier *LifecycleClassifier
	mailboxID  string
	root       string
	inbox      string
	acks       string
	authority  *store.AuthorityStore
	limit      int
	mu         sync.Mutex
}

// DurableOrphanCleanupReport contains only aggregate counts. It deliberately
// excludes request IDs, filesystem paths, payloads, and error detail.
type DurableOrphanCleanupReport struct {
	MarkersRemoved  int
	StagesRecovered int
}

// NewDurableOrphanReconciler builds a reconciler without creating a mailbox
// tree. A missing configured tree simply has no artifact to reconcile.
func NewDurableOrphanReconciler(options DurableOrphanReconcilerOptions) (*DurableOrphanReconciler, error) {
	classifier, err := NewLifecycleClassifier(LifecycleClassifierOptions{
		MailboxID: options.MailboxID, Root: options.Root, Authority: options.Authority,
	})
	if err != nil {
		return nil, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultDurableOrphanReconciliationLimit
	}
	if limit < 1 || limit > 1024 {
		return nil, ErrImporterConfiguration
	}
	return &DurableOrphanReconciler{
		classifier: classifier, mailboxID: options.MailboxID, root: options.Root,
		inbox: filepath.Join(options.Root, "inbox"), acks: filepath.Join(options.Root, "acks"),
		authority: options.Authority, limit: limit,
	}, nil
}

// Run first finishes safe hidden stages from an interrupted earlier pass, then
// removes at most Limit proven staging or public-marker artifacts. A changed,
// unsafe, unknown, or incomplete-diagnostic artifact is retained inert without
// an error.
func (r *DurableOrphanReconciler) Run(ctx context.Context) (DurableOrphanCleanupReport, error) {
	if r == nil || r.classifier == nil || r.authority == nil || r.limit < 1 {
		return DurableOrphanCleanupReport{}, ErrImporterConfiguration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	var report DurableOrphanCleanupReport
	rootExists, err := inspectOwnerDirectory(r.root)
	if err != nil || !rootExists {
		return report, err
	}
	for _, target := range []struct {
		directory string
		area      durableOrphanArea
	}{
		{directory: r.inbox, area: durableOrphanRequestArea},
		{directory: r.acks, area: durableOrphanAckArea},
	} {
		if report.MarkersRemoved+report.StagesRecovered >= r.limit {
			return report, nil
		}
		recovered, err := r.recoverStages(ctx, target.directory, target.area, r.limit-report.MarkersRemoved-report.StagesRecovered)
		report.StagesRecovered += recovered
		if err != nil {
			return report, err
		}
	}
	if report.MarkersRemoved+report.StagesRecovered >= r.limit {
		return report, nil
	}
	inspections, err := r.classifier.List(ctx)
	if err != nil {
		return report, err
	}
	for _, inspection := range inspections {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if report.MarkersRemoved+report.StagesRecovered >= r.limit {
			break
		}
		if inspection.Request.Action == LifecycleActionEligibleDurableOrphanCleanup {
			removed, recovered, err := r.removePublicMarker(ctx, r.inbox, inspection.RequestID, durableOrphanRequestArea)
			if err != nil {
				return report, err
			}
			if recovered {
				report.StagesRecovered++
				continue
			}
			if removed {
				report.MarkersRemoved++
			}
		}
		if report.MarkersRemoved+report.StagesRecovered >= r.limit {
			break
		}
		if inspection.Acknowledgement.Action == LifecycleActionEligibleDurableOrphanCleanup {
			removed, recovered, err := r.removePublicMarker(ctx, r.acks, inspection.RequestID, durableOrphanAckArea)
			if err != nil {
				return report, err
			}
			if recovered {
				report.StagesRecovered++
				continue
			}
			if removed {
				report.MarkersRemoved++
			}
		}
	}
	return report, nil
}

func (r *DurableOrphanReconciler) recoverStages(ctx context.Context, directory string, area durableOrphanArea, limit int) (int, error) {
	if limit < 1 {
		return 0, nil
	}
	exists, err := inspectOwnerDirectory(directory)
	if err != nil || !exists {
		return 0, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("%w: read durable orphan stages", ErrMailboxPath)
	}
	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if removed >= limit {
			break
		}
		requestID, ok := durableOrphanStageRequestID(entry.Name(), area)
		if !ok {
			continue
		}
		eligible, err := r.durableEvidenceAllows(ctx, requestID, area)
		if err != nil {
			return removed, err
		}
		if !eligible {
			continue
		}
		stagePath := filepath.Join(directory, entry.Name())
		requestPath := filepath.Join(directory, requestID+RequestSuffix)
		requestPresent, err := ingressPathPresent(requestPath)
		if err != nil {
			return removed, err
		}
		if requestPresent {
			// A crash can leave an old marker staged while a publisher has
			// written replacement JSON but not yet declared it ready. Do not
			// restore the old marker or discard either publisher artifact.
			// Once its own marker appears, the hidden old marker can be
			// discarded safely on a later pass.
			markerPresent, err := ingressPathPresent(filepath.Join(directory, requestID+ReadySuffix))
			if err != nil {
				return removed, err
			}
			if !markerPresent {
				continue
			}
		}
		cleared, err := discardDurableOrphanStage(stagePath, directory)
		if err != nil {
			return removed, err
		}
		if cleared {
			removed++
		}
	}
	return removed, nil
}

// removePublicMarker returns whether it removed a public marker and whether it
// recovered a pre-existing hidden stage. A recovered stage consumes one pass
// budget and deliberately defers the public name to the next cycle.
func (r *DurableOrphanReconciler) removePublicMarker(ctx context.Context, directory, requestID string, area durableOrphanArea) (bool, bool, error) {
	if _, ok := safeRequestID(requestID); !ok {
		return false, false, ErrMailboxInput
	}
	eligible, err := r.durableEvidenceAllows(ctx, requestID, area)
	if err != nil || !eligible {
		return false, false, err
	}
	stagePath := durableOrphanStagePath(directory, requestID, area)
	if staged, err := discardDurableOrphanStage(stagePath, directory); err != nil || staged {
		return false, staged, err
	}
	markerPath := filepath.Join(directory, requestID+ReadySuffix)
	requestPath := filepath.Join(directory, requestID+RequestSuffix)
	marker, safe, err := safeEmptyOrphanMarker(markerPath)
	if err != nil || !safe {
		return false, false, err
	}
	pairedRequestPresent, err := ingressPathPresent(requestPath)
	if err != nil || pairedRequestPresent {
		return false, false, err
	}
	if hook := durableOrphanBeforeMarkerStageHook; hook != nil {
		durableOrphanBeforeMarkerStageHook = nil
		hook()
	}
	staged, err := stageDurableOrphanMarker(markerPath, stagePath, marker, directory)
	if err != nil || !staged {
		return false, false, err
	}
	if hook := durableOrphanAfterMarkerStageHook; hook != nil {
		durableOrphanAfterMarkerStageHook = nil
		hook()
	}
	// A replacement publisher always wins. Delete only the hidden old marker;
	// never restore it, because an old marker could publish a new JSON early.
	markerPresent, err := ingressPathPresent(markerPath)
	if err != nil {
		return false, false, err
	}
	pairedRequestPresent, err = ingressPathPresent(requestPath)
	if err != nil {
		return false, false, err
	}
	removed, err := discardDurableOrphanStage(stagePath, directory)
	if err != nil {
		return false, false, err
	}
	if markerPresent || pairedRequestPresent {
		return removed, false, nil
	}
	return removed, false, nil
}

func (r *DurableOrphanReconciler) durableEvidenceAllows(ctx context.Context, requestID string, area durableOrphanArea) (bool, error) {
	evidence, err := r.authority.LookupMailboxLifecycleEvidenceForRequestIDsInMailbox(ctx, r.mailboxID, []string{requestID})
	if err != nil {
		return false, err
	}
	item, exists := evidence[requestID]
	if !exists {
		return false, store.ErrMailboxLifecycleEvidenceInvalid
	}
	switch area {
	case durableOrphanRequestArea:
		return item.ExchangeExists || (item.DiagnosticExists && item.DiagnosticInputPairRemoved), nil
	case durableOrphanAckArea:
		return item.ExchangeExists && item.ExchangeAcknowledged, nil
	default:
		return false, ErrImporterConfiguration
	}
}

func durableOrphanStagePath(directory, requestID string, area durableOrphanArea) string {
	return filepath.Join(directory, durableOrphanStageName(requestID, area))
}

func durableOrphanStageName(requestID string, area durableOrphanArea) string {
	return "." + requestID + ".durable-orphan." + string(area) + ReadySuffix + ".cleanup"
}

func durableOrphanStageRequestID(name string, area durableOrphanArea) (string, bool) {
	prefix := "."
	suffix := ".durable-orphan." + string(area) + ReadySuffix + ".cleanup"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	requestID := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	_, ok := safeRequestID(requestID)
	return requestID, ok
}

func safeEmptyOrphanMarker(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect durable orphan marker", ErrMailboxPath)
	}
	if !safeIngressFileInfo(info) || info.Size() != 0 {
		return nil, false, nil
	}
	return info, true, nil
}

func stageDurableOrphanMarker(sourcePath, stagePath string, want os.FileInfo, directory string) (bool, error) {
	if want == nil {
		return false, ErrMailboxPath
	}
	if _, err := os.Lstat(stagePath); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("%w: inspect durable orphan staging marker", ErrMailboxPath)
	}
	if err := os.Rename(sourcePath, stagePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%w: stage durable orphan marker", ErrMailboxPath)
	}
	staged, err := os.Lstat(stagePath)
	if err != nil {
		return false, fmt.Errorf("%w: inspect staged durable orphan marker", ErrMailboxPath)
	}
	if !safeIngressFileInfo(staged) || staged.Size() != 0 {
		// The public pathname changed after its initial revalidation. Restore
		// the changed entry only when that path is still absent; a newer public
		// pathname always wins. This keeps a publisher's corrected marker
		// visible and inert instead of stranding it in a hidden cleanup name.
		if err := restoreDurableOrphanReplacement(stagePath, sourcePath, directory); err != nil {
			return false, err
		}
		return false, nil
	}
	if !os.SameFile(want, staged) {
		if err := restoreDurableOrphanReplacement(stagePath, sourcePath, directory); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := syncMailboxDirectory(directory); err != nil {
		return false, err
	}
	return true, nil
}

// restoreDurableOrphanReplacement puts a changed staged entry back only when
// the public name is absent. Its exclusive hard-link semantics never overwrite
// a later publisher replacement.
func restoreDurableOrphanReplacement(stagePath, sourcePath, directory string) error {
	info, err := os.Lstat(stagePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect changed durable orphan staging marker", ErrMailboxPath)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// On Darwin, os.Link follows a symlink. Recreate the link with an
		// exclusive destination instead, so an unsafe link never becomes a
		// safe hard link to its target and a newer public name is never
		// overwritten.
		target, err := os.Readlink(stagePath)
		if err != nil {
			return fmt.Errorf("%w: inspect changed durable orphan symlink", ErrMailboxPath)
		}
		if err := os.Symlink(target, sourcePath); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: restore changed durable orphan symlink", ErrMailboxPath)
		}
		if err := os.Remove(stagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: remove changed durable orphan symlink stage", ErrMailboxPath)
		}
		return syncMailboxDirectory(directory)
	}
	if !info.Mode().IsRegular() {
		// Special files cannot be restored with a no-clobber operation. Keep
		// them hidden and inert rather than risk overwriting a later publisher
		// path.
		return nil
	}
	if err := restoreIngressDiagnosticReplacement(stagePath, sourcePath); err != nil {
		return err
	}
	return syncMailboxDirectory(directory)
}

func discardDurableOrphanStage(stagePath, directory string) (bool, error) {
	info, err := os.Lstat(stagePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect durable orphan staging marker", ErrMailboxPath)
	}
	if !safeIngressFileInfo(info) || info.Size() != 0 {
		return false, nil
	}
	if err := os.Remove(stagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("%w: remove durable orphan staging marker", ErrMailboxPath)
	}
	if err := syncMailboxDirectory(directory); err != nil {
		return false, err
	}
	return true, nil
}
