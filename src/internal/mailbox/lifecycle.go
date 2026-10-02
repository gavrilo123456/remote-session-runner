package mailbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"remote-session-runner/src/internal/store"
)

// LifecycleInputShape describes the metadata-only shape of one request ID in
// one mailbox area. It does not parse or expose request or acknowledgement
// contents.
type LifecycleInputShape string

const (
	LifecycleInputAbsent            LifecycleInputShape = "absent"
	LifecycleInputPublishablePair   LifecycleInputShape = "publishable_pair"
	LifecycleInputJSONDraft         LifecycleInputShape = "json_draft"
	LifecycleInputRequestMarkerOnly LifecycleInputShape = "request_marker_only"
	LifecycleInputAckMarkerOnly     LifecycleInputShape = "ack_marker_only"
	LifecycleInputUnsafeInert       LifecycleInputShape = "unsafe_inert"
)

// LifecycleDurableState is the compact durable state associated with one
// mailbox-scoped request ID. It intentionally does not carry response or
// request contents.
type LifecycleDurableState string

const (
	LifecycleDurableNone                   LifecycleDurableState = "none"
	LifecycleDurableAccepted               LifecycleDurableState = "accepted"
	LifecycleDurableTerminalUnacknowledged LifecycleDurableState = "terminal_unacknowledged"
	LifecycleDurableTerminalAcknowledged   LifecycleDurableState = "terminal_acknowledged"
	LifecycleDurableIngressDiagnostic      LifecycleDurableState = "ingress_diagnostic"
)

// LifecycleAction describes the only safe follow-up implied by an artifact.
// It never turns filesystem residue into execution authority.
type LifecycleAction string

const (
	LifecycleActionNone                         LifecycleAction = "none"
	LifecycleActionEligibleDurableOrphanCleanup LifecycleAction = "eligible_durable_orphan_cleanup"
	LifecycleActionRetainUnprovenInert          LifecycleAction = "retain_unproven_inert"
)

// LifecycleArtifact is one independent inbox or ACK-area classification.
// Request and acknowledgement artifacts are separate because the same request
// ID can have residue in both directories.
type LifecycleArtifact struct {
	InputShape   LifecycleInputShape
	DurableState LifecycleDurableState
	Action       LifecycleAction
}

// LifecycleInspection is a mailbox-scoped, metadata-only view of one request
// ID. No path, file body, idempotency key, response, event, or secret is
// included in this value.
type LifecycleInspection struct {
	MailboxID       string
	RequestID       string
	Request         LifecycleArtifact
	Acknowledgement LifecycleArtifact
}

// LifecycleClassifierOptions identifies one configured mailbox root and its
// durable authority. The classifier never creates a directory or follows an
// ingress symlink.
type LifecycleClassifierOptions struct {
	MailboxID string
	Root      string
	Authority *store.AuthorityStore
}

// LifecycleClassifier correlates safe filenames and metadata with compact
// durable evidence. It is read-only.
type LifecycleClassifier struct {
	mailboxID string
	root      string
	inbox     string
	acks      string
	authority *store.AuthorityStore
}

// NewLifecycleClassifier constructs a read-only classifier for one trusted,
// configured mailbox. It validates the configured path string but does not
// create or inspect filesystem entries until a read method is called.
func NewLifecycleClassifier(options LifecycleClassifierOptions) (*LifecycleClassifier, error) {
	if _, ok := safeMailboxID(options.MailboxID); !ok || options.Authority == nil {
		return nil, ErrImporterConfiguration
	}
	if strings.TrimSpace(options.Root) == "" || !filepath.IsAbs(options.Root) || filepath.Clean(options.Root) != options.Root || strings.IndexByte(options.Root, 0) >= 0 {
		return nil, ErrImporterConfiguration
	}
	return &LifecycleClassifier{
		mailboxID: options.MailboxID,
		root:      options.Root,
		inbox:     filepath.Join(options.Root, "inbox"),
		acks:      filepath.Join(options.Root, "acks"),
		authority: options.Authority,
	}, nil
}

// Inspect returns the lifecycle of one safe request ID in this classifier's
// configured mailbox namespace.
func (c *LifecycleClassifier) Inspect(ctx context.Context, requestID string) (LifecycleInspection, error) {
	inspections, err := c.inspectMany(ctx, []string{requestID})
	if err != nil {
		return LifecycleInspection{}, err
	}
	return inspections[0], nil
}

// List returns the lifecycle of every safe ID currently represented by a
// request or acknowledgement JSON/marker filename. The result is sorted by
// request ID and never reads file bodies.
func (c *LifecycleClassifier) List(ctx context.Context) ([]LifecycleInspection, error) {
	if c == nil || c.authority == nil {
		return nil, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootExists, err := inspectOwnerDirectory(c.root)
	if err != nil {
		return nil, err
	}
	if !rootExists {
		return nil, nil
	}
	requestIDs := make(map[string]struct{})
	for _, directory := range []string{c.inbox, c.acks} {
		ids, err := lifecycleRequestIDsAtDirectory(ctx, directory)
		if err != nil {
			return nil, err
		}
		for _, requestID := range ids {
			requestIDs[requestID] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(requestIDs))
	for requestID := range requestIDs {
		ordered = append(ordered, requestID)
	}
	sort.Strings(ordered)
	return c.inspectMany(ctx, ordered)
}

// ActionableRequestCount returns only safe complete request pairs that have
// not yet acquired a durable exchange or ingress diagnostic. Durable accepted
// exchanges are counted by AuthorityStore separately, so this avoids both
// marker-only inflation and a pair/receipt double count.
func (c *LifecycleClassifier) ActionableRequestCount(ctx context.Context) (int64, error) {
	inspections, err := c.List(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, inspection := range inspections {
		if inspection.Request.InputShape == LifecycleInputPublishablePair && inspection.Request.DurableState == LifecycleDurableNone {
			count++
		}
	}
	return count, nil
}

func (c *LifecycleClassifier) inspectMany(ctx context.Context, requestIDs []string) ([]LifecycleInspection, error) {
	if c == nil || c.authority == nil {
		return nil, ErrImporterConfiguration
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(requestIDs) == 0 {
		return []LifecycleInspection{}, nil
	}
	seen := make(map[string]struct{}, len(requestIDs))
	for _, requestID := range requestIDs {
		if _, ok := safeRequestID(requestID); !ok {
			return nil, ErrMailboxInput
		}
		if _, exists := seen[requestID]; exists {
			return nil, ErrMailboxInput
		}
		seen[requestID] = struct{}{}
	}
	evidence, err := c.authority.LookupMailboxLifecycleEvidenceForRequestIDsInMailbox(ctx, c.mailboxID, requestIDs)
	if err != nil {
		return nil, err
	}
	rootExists, err := inspectOwnerDirectory(c.root)
	if err != nil {
		return nil, err
	}
	requestDirectoryExists, ackDirectoryExists := false, false
	if rootExists {
		if requestDirectoryExists, err = inspectOwnerDirectory(c.inbox); err != nil {
			return nil, err
		}
		if ackDirectoryExists, err = inspectOwnerDirectory(c.acks); err != nil {
			return nil, err
		}
	}
	inspections := make([]LifecycleInspection, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		if err := ctx.Err(); err != nil {
			return inspections, err
		}
		requestShape := LifecycleInputAbsent
		ackShape := LifecycleInputAbsent
		if requestDirectoryExists {
			requestShape, err = lifecycleInputShape(c.inbox, requestID, true)
			if err != nil {
				return inspections, err
			}
		}
		if ackDirectoryExists {
			ackShape, err = lifecycleInputShape(c.acks, requestID, false)
			if err != nil {
				return inspections, err
			}
		}
		item, exists := evidence[requestID]
		if !exists {
			return inspections, store.ErrMailboxLifecycleEvidenceInvalid
		}
		durable := lifecycleDurableState(item)
		inspections = append(inspections, LifecycleInspection{
			MailboxID: c.mailboxID,
			RequestID: requestID,
			Request: LifecycleArtifact{
				InputShape: requestShape, DurableState: durable,
				Action: lifecycleActionForRequest(requestShape, item),
			},
			Acknowledgement: LifecycleArtifact{
				InputShape: ackShape, DurableState: durable,
				Action: lifecycleActionForAcknowledgement(ackShape, item),
			},
		})
	}
	return inspections, nil
}

func lifecycleRequestIDsAtDirectory(ctx context.Context, directory string) ([]string, error) {
	exists, err := inspectOwnerDirectory(directory)
	if err != nil || !exists {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("%w: read lifecycle directory: %v", ErrMailboxPath, err)
	}
	ids := make(map[string]struct{})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		var requestID string
		switch {
		case strings.HasSuffix(name, ReadySuffix):
			requestID = strings.TrimSuffix(name, ReadySuffix)
		case strings.HasSuffix(name, RequestSuffix):
			requestID = strings.TrimSuffix(name, RequestSuffix)
		default:
			continue
		}
		if _, ok := safeRequestID(requestID); ok {
			ids[requestID] = struct{}{}
		}
	}
	result := make([]string, 0, len(ids))
	for requestID := range ids {
		result = append(result, requestID)
	}
	sort.Strings(result)
	return result, nil
}

type lifecycleFileMetadata struct {
	present bool
	safe    bool
	empty   bool
}

func lifecycleInputShape(directory, requestID string, request bool) (LifecycleInputShape, error) {
	marker, err := lifecycleFileAt(filepath.Join(directory, requestID+ReadySuffix))
	if err != nil {
		return LifecycleInputAbsent, err
	}
	jsonFile, err := lifecycleFileAt(filepath.Join(directory, requestID+RequestSuffix))
	if err != nil {
		return LifecycleInputAbsent, err
	}
	if !marker.present && !jsonFile.present {
		return LifecycleInputAbsent, nil
	}
	if marker.present && marker.safe && marker.empty && jsonFile.present && jsonFile.safe {
		return LifecycleInputPublishablePair, nil
	}
	if !marker.present && jsonFile.present && jsonFile.safe {
		return LifecycleInputJSONDraft, nil
	}
	if marker.present && marker.safe && marker.empty && !jsonFile.present {
		if request {
			return LifecycleInputRequestMarkerOnly, nil
		}
		return LifecycleInputAckMarkerOnly, nil
	}
	return LifecycleInputUnsafeInert, nil
}

func lifecycleFileAt(path string) (lifecycleFileMetadata, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return lifecycleFileMetadata{}, nil
	}
	if err != nil {
		return lifecycleFileMetadata{}, fmt.Errorf("%w: inspect lifecycle file: %v", ErrMailboxPath, err)
	}
	return lifecycleFileMetadata{present: true, safe: safeIngressFileInfo(info), empty: info.Size() == 0}, nil
}

func lifecycleDurableState(item store.MailboxLifecycleEvidence) LifecycleDurableState {
	if item.DiagnosticExists {
		return LifecycleDurableIngressDiagnostic
	}
	if !item.ExchangeExists {
		return LifecycleDurableNone
	}
	if item.ExchangeState == store.MailboxExchangeAccepted {
		return LifecycleDurableAccepted
	}
	if item.ExchangeAcknowledged {
		return LifecycleDurableTerminalAcknowledged
	}
	return LifecycleDurableTerminalUnacknowledged
}

func lifecycleActionForRequest(shape LifecycleInputShape, item store.MailboxLifecycleEvidence) LifecycleAction {
	switch shape {
	case LifecycleInputRequestMarkerOnly:
		if item.ExchangeExists || (item.DiagnosticExists && item.DiagnosticInputPairRemoved) {
			return LifecycleActionEligibleDurableOrphanCleanup
		}
		return LifecycleActionRetainUnprovenInert
	case LifecycleInputUnsafeInert:
		return LifecycleActionRetainUnprovenInert
	default:
		return LifecycleActionNone
	}
}

func lifecycleActionForAcknowledgement(shape LifecycleInputShape, item store.MailboxLifecycleEvidence) LifecycleAction {
	switch shape {
	case LifecycleInputAckMarkerOnly:
		if item.ExchangeExists && item.ExchangeAcknowledged {
			return LifecycleActionEligibleDurableOrphanCleanup
		}
		return LifecycleActionRetainUnprovenInert
	case LifecycleInputUnsafeInert:
		return LifecycleActionRetainUnprovenInert
	default:
		return LifecycleActionNone
	}
}
