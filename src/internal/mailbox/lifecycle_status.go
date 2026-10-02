package mailbox

import (
	"context"
	"errors"

	"remote-session-runner/src/internal/store"
)

// ErrLifecycleMailboxNotConfigured means that an operator status query named
// an inbox which is not in the selected, in-memory mailbox registry. It never
// exposes a filesystem path.
var ErrLifecycleMailboxNotConfigured = errors.New("mailbox lifecycle inbox is not configured")

// LifecycleStatusMailbox identifies one configured mailbox namespace for the
// read-only lifecycle-status registry.
type LifecycleStatusMailbox struct {
	ID   string
	Root string
}

// LifecycleStatusRegistryOptions builds the status registry only from the
// selected configuration. Callers cannot supply an arbitrary root per query.
type LifecycleStatusRegistryOptions struct {
	Authority *store.AuthorityStore
	Mailboxes []LifecycleStatusMailbox
}

// LifecycleStatusRegistry maps configured inbox IDs to metadata-only
// classifiers. It has no importer, reconciler, writer, or execution boundary.
type LifecycleStatusRegistry struct {
	classifiers map[string]*LifecycleClassifier
}

// LifecycleStatusCounts summarizes only artifacts currently visible through a
// configured inbox and its acknowledgement area. It deliberately does not
// infer an all-time exchange history or a terminal command result.
type LifecycleStatusCounts struct {
	RequestInputShapes         map[LifecycleInputShape]int
	AcknowledgementInputShapes map[LifecycleInputShape]int
	DurableStates              map[LifecycleDurableState]int
	RequestActions             map[LifecycleAction]int
	AcknowledgementActions     map[LifecycleAction]int
	ActionableUnacceptedPairs  int
}

// LifecycleStatus is the metadata-only answer for one configured inbox. When
// Request is nil, the caller asked for the aggregate current inventory only.
type LifecycleStatus struct {
	MailboxID string
	Counts    LifecycleStatusCounts
	Request   *LifecycleInspection
}

// NewLifecycleStatusRegistry constructs a read-only registry. It validates
// every configured classifier at construction but does not touch a mailbox
// tree until a status query runs.
func NewLifecycleStatusRegistry(options LifecycleStatusRegistryOptions) (*LifecycleStatusRegistry, error) {
	if options.Authority == nil || len(options.Mailboxes) == 0 {
		return nil, ErrImporterConfiguration
	}
	registry := &LifecycleStatusRegistry{classifiers: make(map[string]*LifecycleClassifier, len(options.Mailboxes))}
	for _, mailbox := range options.Mailboxes {
		if _, exists := registry.classifiers[mailbox.ID]; exists {
			return nil, ErrImporterConfiguration
		}
		classifier, err := NewLifecycleClassifier(LifecycleClassifierOptions{
			MailboxID: mailbox.ID,
			Root:      mailbox.Root,
			Authority: options.Authority,
		})
		if err != nil {
			return nil, err
		}
		registry.classifiers[mailbox.ID] = classifier
	}
	return registry, nil
}

// MailboxLifecycleStatus returns a read-only lifecycle classification for a
// configured inbox. A nonempty requestID adds one exact inspection; the
// aggregate always reflects the currently visible safe filename inventory.
func (r *LifecycleStatusRegistry) MailboxLifecycleStatus(ctx context.Context, mailboxID, requestID string) (LifecycleStatus, error) {
	if r == nil {
		return LifecycleStatus{}, ErrImporterConfiguration
	}
	classifier, exists := r.classifiers[mailboxID]
	if !exists {
		return LifecycleStatus{}, ErrLifecycleMailboxNotConfigured
	}
	if err := classifier.statusAvailable(); err != nil {
		return LifecycleStatus{}, err
	}
	inspections, err := classifier.List(ctx)
	if err != nil {
		return LifecycleStatus{}, err
	}
	status := LifecycleStatus{MailboxID: mailboxID, Counts: lifecycleStatusCounts(inspections)}
	if requestID == "" {
		return status, nil
	}
	inspection, err := classifier.Inspect(ctx, requestID)
	if err != nil {
		return LifecycleStatus{}, err
	}
	status.Request = &inspection
	return status, nil
}

// statusAvailable distinguishes an unavailable configured mailbox tree from
// an empty one. The general classifier keeps missing trees as a zero count for
// metrics; the operator API must instead say that it could not inspect the
// configured status source.
func (c *LifecycleClassifier) statusAvailable() error {
	if c == nil || c.authority == nil {
		return ErrImporterConfiguration
	}
	for _, directory := range []string{c.root, c.inbox, c.acks} {
		exists, err := inspectOwnerDirectory(directory)
		if err != nil {
			return err
		}
		if !exists {
			return ErrMailboxPath
		}
	}
	return nil
}

func lifecycleStatusCounts(inspections []LifecycleInspection) LifecycleStatusCounts {
	counts := LifecycleStatusCounts{
		RequestInputShapes: map[LifecycleInputShape]int{
			LifecycleInputAbsent: 0, LifecycleInputPublishablePair: 0, LifecycleInputJSONDraft: 0,
			LifecycleInputRequestMarkerOnly: 0, LifecycleInputAckMarkerOnly: 0, LifecycleInputUnsafeInert: 0,
		},
		AcknowledgementInputShapes: map[LifecycleInputShape]int{
			LifecycleInputAbsent: 0, LifecycleInputPublishablePair: 0, LifecycleInputJSONDraft: 0,
			LifecycleInputRequestMarkerOnly: 0, LifecycleInputAckMarkerOnly: 0, LifecycleInputUnsafeInert: 0,
		},
		DurableStates: map[LifecycleDurableState]int{
			LifecycleDurableNone: 0, LifecycleDurableAccepted: 0, LifecycleDurableTerminalUnacknowledged: 0,
			LifecycleDurableTerminalAcknowledged: 0, LifecycleDurableIngressDiagnostic: 0,
		},
		RequestActions: map[LifecycleAction]int{
			LifecycleActionNone: 0, LifecycleActionEligibleDurableOrphanCleanup: 0, LifecycleActionRetainUnprovenInert: 0,
		},
		AcknowledgementActions: map[LifecycleAction]int{
			LifecycleActionNone: 0, LifecycleActionEligibleDurableOrphanCleanup: 0, LifecycleActionRetainUnprovenInert: 0,
		},
	}
	for _, inspection := range inspections {
		counts.RequestInputShapes[inspection.Request.InputShape]++
		counts.AcknowledgementInputShapes[inspection.Acknowledgement.InputShape]++
		counts.DurableStates[inspection.Request.DurableState]++
		counts.RequestActions[inspection.Request.Action]++
		counts.AcknowledgementActions[inspection.Acknowledgement.Action]++
		if inspection.Request.InputShape == LifecycleInputPublishablePair && inspection.Request.DurableState == LifecycleDurableNone {
			counts.ActionableUnacceptedPairs++
		}
	}
	return counts
}
