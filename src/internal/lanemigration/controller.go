// Package lanemigration implements the durable, reversible PRD mailbox fence.
// It records V2 proof and paired file evidence; it does not stop or start host
// services, execute mailbox work, ACK requests, or delete ingress files.
package lanemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"remote-session-runner/src/internal/laneregistry"
	"remote-session-runner/src/internal/mailboxfence"
)

var (
	ErrBlocked        = errors.New("PRD migration fence is incomplete or inconsistent")
	ErrPairChanged    = errors.New("deferred PRD mailbox pair changed or disappeared")
	ErrV2ProofMissing = errors.New("V2 members are not fully stopped, disabled, absent, and idle")
)

type ExclusiveOpener func(context.Context, string) (*mailboxfence.Lock, error)

type Controller struct {
	Registry *laneregistry.Registry
	Open     ExclusiveOpener
}

func New(registry *laneregistry.Registry) *Controller {
	return &Controller{Registry: registry, Open: mailboxfence.AcquireMigrationExclusive}
}

// Begin durably records the publisher inventory and initial V2 member state.
// The caller must wait for this success before acting on publishers/services.
func (c *Controller) Begin(expectedGeneration uint64, record laneregistry.PRDMigrationFenceRecord) (laneregistry.Snapshot, error) {
	if c == nil || c.Registry == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	return c.Registry.BeginPRDMigrationFence(expectedGeneration, record)
}

func (c *Controller) RecordV2Progress(expectedGeneration uint64, transitionID, memberID string, progress laneregistry.MigrationV2Member) (laneregistry.Snapshot, error) {
	if c == nil || c.Registry == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	return c.Registry.RecordPRDMigrationMemberProgress(expectedGeneration, transitionID, memberID, progress)
}

func (c *Controller) RecordPublisherPause(expectedGeneration uint64, transitionID, publisherID, evidenceDigest string) (laneregistry.Snapshot, error) {
	if c == nil || c.Registry == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	return c.Registry.RecordPRDMigrationPublisherPause(expectedGeneration, transitionID, publisherID, evidenceDigest)
}

// FenceEmpty captures the final empty request/ACK snapshot under the exclusive
// lock, stores it before writing the marker, then commits the matching registry
// state. A crash in either gap stays fail-closed and is handled by Reconcile.
func (c *Controller) FenceEmpty(ctx context.Context, transitionID string) (laneregistry.Snapshot, error) {
	snapshot, record, err := c.record(transitionID)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	if record.State == laneregistry.PRDMigrationFenced {
		return c.verifyFenced(ctx, snapshot, record)
	}
	if record.State != laneregistry.PRDMigrationFencing || !allMembersProven(record.V2Members) || !allPublishersPaused(record.Publishers) || c.Open == nil {
		return laneregistry.Snapshot{}, ErrV2ProofMissing
	}
	lock, err := c.Open(ctx, record.NormalizedRoot)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	defer lock.Close()
	return c.finishFenceUnderLock(snapshot, record, lock)
}

// ScanDeferred stores complete post-fence request and ACK pairs by their exact
// descriptor identities and digests. It leaves all original files in place.
func (c *Controller) ScanDeferred(ctx context.Context, transitionID string) (laneregistry.Snapshot, error) {
	snapshot, record, err := c.record(transitionID)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	if record.State != laneregistry.PRDMigrationFenced || c.Open == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	lock, err := c.Open(ctx, record.NormalizedRoot)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	defer lock.Close()
	marker, markerDigest, err := lock.ReadMigrationFenceMarker()
	if err != nil || markerDigest != record.MarkerDigest || !markerMatches(marker, record, mailboxfence.MigrationFenceFenced) {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	return c.scanUnderLock(snapshot, record, lock)
}

// Abort verifies and inventories all current pairs while holding the exclusive
// lock, persists abort intent, advances the marker, and removes only the exact
// aborted marker. It never restarts V2 or touches pair files.
func (c *Controller) Abort(ctx context.Context, transitionID, evidenceDigest string) (laneregistry.Snapshot, error) {
	snapshot, record, err := c.record(transitionID)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	if record.State != laneregistry.PRDMigrationFenced || c.Open == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	lock, err := c.Open(ctx, record.NormalizedRoot)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	defer lock.Close()
	marker, markerDigest, err := lock.ReadMigrationFenceMarker()
	if err != nil || markerDigest != record.MarkerDigest || !markerMatches(marker, record, mailboxfence.MigrationFenceFenced) {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	if snapshot.Generation == ^uint64(0) {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	observed, err := lock.SnapshotIngressPairsAt(record.InboxID, snapshot.Generation+1)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	for _, pair := range observed.Pairs {
		if err := lock.VerifyReadyPairSnapshot(pair); err != nil {
			return laneregistry.Snapshot{}, err
		}
	}
	snapshot, err = c.Registry.RecordPRDMigrationDeferredPairs(snapshot.Generation, transitionID, observed)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	record, err = findRecord(snapshot, transitionID)
	if err != nil || !allObservedPairsTracked(record, observed) {
		return laneregistry.Snapshot{}, ErrPairChanged
	}
	snapshot, err = c.Registry.BeginPRDMigrationAbort(snapshot.Generation, transitionID, evidenceDigest)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	marker.State = mailboxfence.MigrationFenceAborting
	if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
		return laneregistry.Snapshot{}, err
	}
	snapshot, err = c.Registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	record, err = findRecord(snapshot, transitionID)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	if snapshot.Generation == ^uint64(0) {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	observed, err = lock.SnapshotIngressPairsAt(record.InboxID, snapshot.Generation+1)
	if err != nil || !allObservedPairsTracked(record, observed) {
		return laneregistry.Snapshot{}, ErrPairChanged
	}
	for _, pair := range observed.Pairs {
		if err := lock.VerifyReadyPairSnapshot(pair); err != nil {
			return laneregistry.Snapshot{}, err
		}
	}
	snapshot, err = c.Registry.CompletePRDMigrationAbort(snapshot.Generation, transitionID, evidenceDigest, observed)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	record, err = findRecord(snapshot, transitionID)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	marker.State = mailboxfence.MigrationFenceAborted
	if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
		return laneregistry.Snapshot{}, err
	}
	snapshot, err = c.Registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	if err := lock.RemoveMigrationFenceMarker(marker); err != nil {
		return laneregistry.Snapshot{}, err
	}
	if err := lock.VerifyMigrationFenceMarkerAbsent(); err != nil {
		return laneregistry.Snapshot{}, err
	}
	return c.recordFenceRelease(snapshot, record, lock, marker)
}

// Reconcile never infers that V2 may restart. It advances only the durable
// marker/registry gap when the root identity, snapshot, V2 proofs, and all
// retained pair identities still agree.
func (c *Controller) Reconcile(ctx context.Context, transitionID string) (laneregistry.Snapshot, error) {
	snapshot, record, err := c.record(transitionID)
	if err != nil || c.Open == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	lock, err := c.Open(ctx, record.NormalizedRoot)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	defer lock.Close()
	marker, markerDigest, markerErr := lock.ReadMigrationFenceMarker()
	markerMissing := errors.Is(markerErr, mailboxfence.ErrMigrationFenceMarkerMissing)
	if markerErr != nil && !markerMissing {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	if record.State == laneregistry.PRDMigrationFencing {
		if !allMembersProven(record.V2Members) || !allPublishersPaused(record.Publishers) || record.Snapshot == nil {
			return laneregistry.Snapshot{}, ErrV2ProofMissing
		}
		if markerMissing {
			return c.finishFenceUnderLock(snapshot, record, lock)
		}
		want := markerFor(record, mailboxfence.MigrationFenceFenced)
		if marker != want {
			return laneregistry.Snapshot{}, ErrBlocked
		}
		return c.Registry.MarkPRDMigrationFenced(snapshot.Generation, marker)
	}
	if record.State == laneregistry.PRDMigrationFenced {
		if markerErr != nil || markerDigest != record.MarkerDigest || !markerMatches(marker, record, mailboxfence.MigrationFenceFenced) {
			return laneregistry.Snapshot{}, ErrBlocked
		}
		return c.scanUnderLock(snapshot, record, lock)
	}
	if record.State == laneregistry.PRDMigrationReleased {
		if !markerMissing || record.MarkerDigest != mailboxfence.DigestMigrationFenceMarker(markerFor(record, mailboxfence.MigrationFenceAborted)) ||
			record.FenceReleaseEvidenceDigest == "" || lock.VerifyMigrationFenceMarkerAbsent() != nil {
			return laneregistry.Snapshot{}, ErrBlocked
		}
		return snapshot, nil
	}
	if record.State == laneregistry.PRDMigrationAborting || record.State == laneregistry.PRDMigrationAborted {
		if markerMissing {
			if record.State == laneregistry.PRDMigrationAborted {
				if record.MarkerDigest != mailboxfence.DigestMigrationFenceMarker(markerFor(record, mailboxfence.MigrationFenceAborted)) ||
					lock.VerifyMigrationFenceMarkerAbsent() != nil {
					return laneregistry.Snapshot{}, ErrBlocked
				}
				return c.recordFenceRelease(snapshot, record, lock, markerFor(record, mailboxfence.MigrationFenceAborted))
			}
			return laneregistry.Snapshot{}, ErrBlocked
		}
		markerStateAllowed := (record.State == laneregistry.PRDMigrationAborting && (marker.State == mailboxfence.MigrationFenceFenced || marker.State == mailboxfence.MigrationFenceAborting)) ||
			(record.State == laneregistry.PRDMigrationAborted && (marker.State == mailboxfence.MigrationFenceAborting || marker.State == mailboxfence.MigrationFenceAborted))
		markerDigestExpected := mailboxfence.DigestMigrationFenceMarker(marker)
		if (record.State == laneregistry.PRDMigrationAborting && marker.State == mailboxfence.MigrationFenceFenced || record.State == laneregistry.PRDMigrationAborted && marker.State == mailboxfence.MigrationFenceAborting) && markerDigest != record.MarkerDigest {
			return laneregistry.Snapshot{}, ErrBlocked
		}
		if markerDigest != markerDigestExpected || !markerStateAllowed || !markerIdentityMatches(marker, record) || !allObservedPairsStillPresent(lock, record) {
			return laneregistry.Snapshot{}, ErrPairChanged
		}
		if record.State == laneregistry.PRDMigrationAborting {
			marker.State = mailboxfence.MigrationFenceAborting
			if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
				return laneregistry.Snapshot{}, err
			}
			snapshot, err = c.Registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
			if err != nil {
				return laneregistry.Snapshot{}, err
			}
			record, err = findRecord(snapshot, transitionID)
			if err != nil {
				return laneregistry.Snapshot{}, err
			}
			if snapshot.Generation == ^uint64(0) {
				return laneregistry.Snapshot{}, ErrBlocked
			}
			observed, err := lock.SnapshotIngressPairsAt(record.InboxID, snapshot.Generation+1)
			if err != nil || !allObservedPairsTracked(record, observed) {
				return laneregistry.Snapshot{}, ErrPairChanged
			}
			for _, pair := range observed.Pairs {
				if err := lock.VerifyReadyPairSnapshot(pair); err != nil {
					return laneregistry.Snapshot{}, err
				}
			}
			snapshot, err = c.Registry.CompletePRDMigrationAbort(snapshot.Generation, transitionID, record.AbortEvidenceDigest, observed)
			if err != nil {
				return laneregistry.Snapshot{}, err
			}
			marker.State = mailboxfence.MigrationFenceAborted
			if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
				return laneregistry.Snapshot{}, err
			}
			snapshot, err = c.Registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
			if err != nil {
				return laneregistry.Snapshot{}, err
			}
			if err := lock.RemoveMigrationFenceMarker(marker); err != nil {
				return laneregistry.Snapshot{}, err
			}
			if err := lock.VerifyMigrationFenceMarkerAbsent(); err != nil {
				return laneregistry.Snapshot{}, err
			}
			record, err = findRecord(snapshot, transitionID)
			if err != nil {
				return laneregistry.Snapshot{}, err
			}
			return c.recordFenceRelease(snapshot, record, lock, marker)
		} else {
			marker.State = mailboxfence.MigrationFenceAborted
		}
		if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
			return laneregistry.Snapshot{}, err
		}
		snapshot, err = c.Registry.RecordPRDMigrationMarkerDigest(snapshot.Generation, marker)
		if err != nil {
			return laneregistry.Snapshot{}, err
		}
		if record.State == laneregistry.PRDMigrationAborted {
			if err := lock.RemoveMigrationFenceMarker(marker); err != nil {
				return laneregistry.Snapshot{}, err
			}
			if err := lock.VerifyMigrationFenceMarkerAbsent(); err != nil {
				return laneregistry.Snapshot{}, err
			}
			record, err = findRecord(snapshot, transitionID)
			if err != nil {
				return laneregistry.Snapshot{}, err
			}
			return c.recordFenceRelease(snapshot, record, lock, marker)
		}
		return snapshot, nil
	}
	return laneregistry.Snapshot{}, ErrBlocked
}

func (c *Controller) finishFenceUnderLock(snapshot laneregistry.Snapshot, record laneregistry.PRDMigrationFenceRecord, lock *mailboxfence.Lock) (laneregistry.Snapshot, error) {
	if !allMembersProven(record.V2Members) || !allPublishersPaused(record.Publishers) || (record.Snapshot == nil && snapshot.Generation == ^uint64(0)) {
		return laneregistry.Snapshot{}, ErrV2ProofMissing
	}
	observationGeneration := snapshot.Generation + 1
	if record.Snapshot != nil {
		observationGeneration = record.Snapshot.ObservationGeneration
	}
	observed, err := lock.SnapshotIngressPairsAt(record.InboxID, observationGeneration)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	if err := observed.RequireEmpty(); err != nil {
		return laneregistry.Snapshot{}, err
	}
	if record.Snapshot != nil {
		digest, digestErr := mailboxfence.DigestIngressSnapshot(observed)
		if digestErr != nil || digest != record.SnapshotDigest || observed.Root != record.Snapshot.Root {
			return laneregistry.Snapshot{}, ErrBlocked
		}
	} else {
		snapshot, err = c.Registry.RecordPRDMigrationSnapshot(snapshot.Generation, record.LaneTransitionID, observed)
		if err != nil {
			return laneregistry.Snapshot{}, err
		}
		record, err = findRecord(snapshot, record.LaneTransitionID)
		if err != nil {
			return laneregistry.Snapshot{}, err
		}
	}
	marker := markerFor(record, mailboxfence.MigrationFenceFenced)
	if _, err := lock.WriteMigrationFenceMarker(marker); err != nil {
		return laneregistry.Snapshot{}, err
	}
	return c.Registry.MarkPRDMigrationFenced(snapshot.Generation, marker)
}

func (c *Controller) scanUnderLock(snapshot laneregistry.Snapshot, record laneregistry.PRDMigrationFenceRecord, lock *mailboxfence.Lock) (laneregistry.Snapshot, error) {
	if snapshot.Generation == ^uint64(0) {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	observed, err := lock.SnapshotIngressPairsAt(record.InboxID, snapshot.Generation+1)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	for _, pair := range observed.Pairs {
		if err := lock.VerifyReadyPairSnapshot(pair); err != nil {
			return laneregistry.Snapshot{}, err
		}
	}
	return c.Registry.RecordPRDMigrationDeferredPairs(snapshot.Generation, record.LaneTransitionID, observed)
}

func (c *Controller) verifyFenced(ctx context.Context, snapshot laneregistry.Snapshot, record laneregistry.PRDMigrationFenceRecord) (laneregistry.Snapshot, error) {
	if c.Open == nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	lock, err := c.Open(ctx, record.NormalizedRoot)
	if err != nil {
		return laneregistry.Snapshot{}, err
	}
	defer lock.Close()
	marker, markerDigest, err := lock.ReadMigrationFenceMarker()
	if err != nil || markerDigest != record.MarkerDigest || !markerMatches(marker, record, mailboxfence.MigrationFenceFenced) {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	return snapshot, nil
}

func (c *Controller) record(id string) (laneregistry.Snapshot, laneregistry.PRDMigrationFenceRecord, error) {
	if c == nil || c.Registry == nil {
		return laneregistry.Snapshot{}, laneregistry.PRDMigrationFenceRecord{}, ErrBlocked
	}
	snapshot, err := c.Registry.Snapshot()
	if err != nil {
		return laneregistry.Snapshot{}, laneregistry.PRDMigrationFenceRecord{}, err
	}
	record, err := findRecord(snapshot, id)
	return snapshot, record, err
}

func findRecord(snapshot laneregistry.Snapshot, id string) (laneregistry.PRDMigrationFenceRecord, error) {
	for _, record := range snapshot.PRDMigrationFences {
		if record.LaneTransitionID == id {
			return record, nil
		}
	}
	return laneregistry.PRDMigrationFenceRecord{}, ErrBlocked
}

func (c *Controller) recordFenceRelease(snapshot laneregistry.Snapshot, record laneregistry.PRDMigrationFenceRecord, lock *mailboxfence.Lock, marker mailboxfence.MigrationFenceMarker) (laneregistry.Snapshot, error) {
	current, err := findRecord(snapshot, record.LaneTransitionID)
	if err != nil || current.State != laneregistry.PRDMigrationAborted || lock == nil || lock.VerifyMigrationFenceMarkerAbsent() != nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	want := markerFor(current, mailboxfence.MigrationFenceAborted)
	markerDigest := mailboxfence.DigestMigrationFenceMarker(want)
	if marker != want || markerDigest == "" || current.MarkerDigest != markerDigest || current.AbortEvidenceDigest == "" {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	receipt := struct {
		TransitionID    string                    `json:"transition_id"`
		AbortEvidence   string                    `json:"abort_evidence_digest"`
		AbortedMarker   string                    `json:"aborted_marker_digest"`
		MailboxRoot     mailboxfence.FileIdentity `json:"mailbox_root_identity"`
		CommittedAbsent bool                      `json:"committed_marker_absent"`
		PendingAbsent   bool                      `json:"pending_marker_absent"`
		DirectorySynced bool                      `json:"directory_synced"`
	}{
		TransitionID: current.LaneTransitionID, AbortEvidence: current.AbortEvidenceDigest,
		AbortedMarker: markerDigest, MailboxRoot: current.MailboxRoot,
		CommittedAbsent: true, PendingAbsent: true, DirectorySynced: true,
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return laneregistry.Snapshot{}, ErrBlocked
	}
	digest := sha256.Sum256(data)
	release := laneregistry.MigrationFenceReleaseEvidence{
		LaneTransitionID: current.LaneTransitionID, AbortedMarkerDigest: markerDigest,
		MailboxRoot: current.MailboxRoot, CommittedMarkerAbsent: true,
		PendingMarkerAbsent: true, DirectorySynced: true,
		EvidenceDigest: "sha256:" + hex.EncodeToString(digest[:]),
	}
	return c.Registry.RecordPRDMigrationFenceRelease(snapshot.Generation, release)
}

func markerFor(record laneregistry.PRDMigrationFenceRecord, state mailboxfence.MigrationFenceState) mailboxfence.MigrationFenceMarker {
	return mailboxfence.MigrationFenceMarker{
		LaneTransitionID: record.LaneTransitionID, InboxID: record.InboxID, Root: record.MailboxRoot,
		ConfigurationDigest: record.ConfigurationDigest, State: state,
		SnapshotDigest: record.SnapshotDigest, ObservationGeneration: record.Snapshot.ObservationGeneration,
	}
}

func markerMatches(marker mailboxfence.MigrationFenceMarker, record laneregistry.PRDMigrationFenceRecord, state mailboxfence.MigrationFenceState) bool {
	return marker == markerFor(record, state)
}

func markerIdentityMatches(marker mailboxfence.MigrationFenceMarker, record laneregistry.PRDMigrationFenceRecord) bool {
	want := markerFor(record, marker.State)
	return marker == want
}

func allMembersProven(members []laneregistry.MigrationV2Member) bool {
	if len(members) < 4 {
		return false
	}
	for _, member := range members {
		if member.PauseEvidenceDigest == "" || member.StopEvidenceDigest == "" || member.DisableEvidenceDigest == "" || member.AbsenceEvidenceDigest == "" || member.ZeroWorkEvidenceDigest == "" {
			return false
		}
	}
	return true
}

func allPublishersPaused(publishers []laneregistry.MigrationPublisher) bool {
	if len(publishers) == 0 {
		return false
	}
	for _, publisher := range publishers {
		if publisher.PauseEvidenceDigest == "" {
			return false
		}
	}
	return true
}

func allObservedPairsTracked(record laneregistry.PRDMigrationFenceRecord, observed mailboxfence.IngressSnapshot) bool {
	if len(record.DeferredPairs) != len(observed.Pairs) {
		return false
	}
	for _, pair := range observed.Pairs {
		found := false
		for _, retained := range record.DeferredPairs {
			left, right := pair, retained
			left.ObservationGeneration, right.ObservationGeneration = 0, 0
			if reflect.DeepEqual(left, right) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func allObservedPairsStillPresent(lock *mailboxfence.Lock, record laneregistry.PRDMigrationFenceRecord) bool {
	if record.Snapshot == nil {
		return false
	}
	observed, err := lock.SnapshotIngressPairsAt(record.InboxID, record.Snapshot.ObservationGeneration+1)
	if err != nil || !allObservedPairsTracked(record, observed) {
		return false
	}
	for _, pair := range observed.Pairs {
		if lock.VerifyReadyPairSnapshot(pair) != nil {
			return false
		}
	}
	return true
}
