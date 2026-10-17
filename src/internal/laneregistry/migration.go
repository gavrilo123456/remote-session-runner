package laneregistry

import (
	"errors"
	"fmt"
	"reflect"
	"sort"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneroot"
	"remote-session-runner/src/internal/mailboxfence"
)

type PRDMigrationFenceState string

const (
	PRDMigrationFencing  PRDMigrationFenceState = "fencing"
	PRDMigrationFenced   PRDMigrationFenceState = "fenced"
	PRDMigrationAborting PRDMigrationFenceState = "aborting"
	PRDMigrationAborted  PRDMigrationFenceState = "aborted"
)

const (
	MigrationSurfaceMac            = "mac-control-plane"
	MigrationSurfaceLinuxHost      = "linux-host"
	MigrationSurfaceSandboxHost    = "sandbox-host"
	MigrationSurfaceFactorySandbox = "factory-sandbox"
	MigrationMemberDirectListener  = "direct_listener"
	MigrationMemberMacLocalIngress = "mac_local_ingress"
	MigrationMemberMailboxImporter = "mailbox_importer"
	MigrationMemberQueuedBridge    = "queued_bridge_backend"
)

var (
	ErrMigrationFenceNotFound = errors.New("PRD migration fence not found")
	ErrMigrationFenceState    = errors.New("invalid PRD migration fence transition")
	ErrMigrationFenceProof    = errors.New("incomplete PRD migration fence proof")
)

// MigrationPublisher is the checked publisher/direct-client inventory stored
// before a PRD fence begins. Raw writers must have a tested pause/stop control;
// cooperating clients must declare the tested migration capability version.
type MigrationPublisher struct {
	ID                         string `json:"id"`
	Surface                    string `json:"surface"`
	Kind                       string `json:"kind"`
	RawWriter                  bool   `json:"raw_writer"`
	MigrationCapabilityVersion string `json:"migration_capability_version,omitempty"`
	MigrationCapabilityTested  bool   `json:"migration_capability_tested"`
	PauseControl               string `json:"pause_control"`
	PauseControlTested         bool   `json:"pause_control_tested"`
	PauseEvidenceDigest        string `json:"pause_evidence_digest,omitempty"`
}

// MigrationV2Member begins with the pre-action inventory of one V2 service
// member. Each durable proof field is appended as the controller progresses;
// the record is persisted as fencing before any such action is permitted.
type MigrationV2Member struct {
	ID                     string `json:"id"`
	Surface                string `json:"surface"`
	Kind                   string `json:"kind"`
	InitiallyEnabled       bool   `json:"initially_enabled"`
	PauseEvidenceDigest    string `json:"pause_evidence_digest,omitempty"`
	StopEvidenceDigest     string `json:"stop_evidence_digest,omitempty"`
	DisableEvidenceDigest  string `json:"disable_evidence_digest,omitempty"`
	AbsenceEvidenceDigest  string `json:"absence_evidence_digest,omitempty"`
	ZeroWorkEvidenceDigest string `json:"zero_work_evidence_digest,omitempty"`
}

type PRDMigrationTransition struct {
	Sequence       uint64                 `json:"sequence"`
	From           PRDMigrationFenceState `json:"from,omitempty"`
	To             PRDMigrationFenceState `json:"to"`
	EvidenceDigest string                 `json:"evidence_digest,omitempty"`
}

type MigrationDeferredObservation struct {
	ObservationGeneration uint64 `json:"observation_generation"`
	SnapshotDigest        string `json:"snapshot_digest"`
}

// PRDMigrationFenceRecord is the durable registry side of one reversible PRD
// migration. It binds the retained PRD root, publisher inventory, V2 progress,
// empty fence snapshot, and later descriptor/digest deferred pairs.
type PRDMigrationFenceRecord struct {
	LaneTransitionID     string                           `json:"lane_transition_id"`
	RootRecordID         string                           `json:"root_record_id"`
	InboxID              string                           `json:"inbox_id"`
	NormalizedRoot       string                           `json:"normalized_root"`
	RootIdentity         laneroot.FileIdentity            `json:"filesystem_identity"`
	MailboxRoot          mailboxfence.FileIdentity        `json:"mailbox_root_identity"`
	ConfigurationDigest  string                           `json:"configuration_digest"`
	PreflightDigest      string                           `json:"preflight_digest"`
	State                PRDMigrationFenceState           `json:"state"`
	Publishers           []MigrationPublisher             `json:"publishers"`
	V2Members            []MigrationV2Member              `json:"v2_members"`
	Snapshot             *mailboxfence.IngressSnapshot    `json:"snapshot,omitempty"`
	SnapshotDigest       string                           `json:"snapshot_digest,omitempty"`
	MarkerDigest         string                           `json:"marker_digest,omitempty"`
	DeferredPairs        []mailboxfence.ReadyPairSnapshot `json:"deferred_pairs,omitempty"`
	DeferredObservations []MigrationDeferredObservation   `json:"deferred_observations,omitempty"`
	AbortEvidenceDigest  string                           `json:"abort_evidence_digest,omitempty"`
	History              []PRDMigrationTransition         `json:"history"`
}

// BeginPRDMigrationFence persists the complete pre-action inventory in the
// fencing state. Callers may pause publishers or alter V2 members only after
// this CAS succeeds.
func (r *Registry) BeginPRDMigrationFence(expectedGeneration uint64, record PRDMigrationFenceRecord) (Snapshot, error) {
	if record.State != PRDMigrationFencing || record.Snapshot != nil || record.SnapshotDigest != "" || record.MarkerDigest != "" ||
		record.AbortEvidenceDigest != "" || len(record.DeferredPairs) != 0 || len(record.DeferredObservations) != 0 {
		return Snapshot{}, ErrMigrationFenceState
	}
	record.History = []PRDMigrationTransition{{Sequence: 1, To: PRDMigrationFencing, EvidenceDigest: record.PreflightDigest}}
	sort.Slice(record.Publishers, func(i, j int) bool { return record.Publishers[i].ID < record.Publishers[j].ID })
	sort.Slice(record.V2Members, func(i, j int) bool { return record.V2Members[i].ID < record.V2Members[j].ID })
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		for _, prior := range snapshot.PRDMigrationFences {
			if prior.LaneTransitionID == record.LaneTransitionID {
				if reflect.DeepEqual(prior, record) {
					return false, nil
				}
				return false, ErrMigrationFenceState
			}
			if prior.RootRecordID == record.RootRecordID && prior.State != PRDMigrationAborted {
				return false, ErrMigrationFenceState
			}
		}
		if hasActivePRDOperationGroup(snapshot) || hasActivePRDMigrationFence(snapshot) {
			return false, ErrOperationBusy
		}
		if err := validatePRDMigrationFenceRecord(snapshot, record); err != nil {
			return false, err
		}
		snapshot.PRDMigrationFences = append(snapshot.PRDMigrationFences, record)
		return true, nil
	})
}

// RecordPRDMigrationMemberProgress monotonically records independently
// evidenced pause, stop, service-manager disable, absence, and zero-work facts.
func (r *Registry) RecordPRDMigrationMemberProgress(expectedGeneration uint64, transitionID, memberID string, progress MigrationV2Member) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, transitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationFencing {
			return false, ErrMigrationFenceState
		}
		memberIndex := migrationMemberIndex(record.V2Members, memberID)
		if memberIndex < 0 || progress.ID != memberID || progress.Surface != record.V2Members[memberIndex].Surface || progress.Kind != record.V2Members[memberIndex].Kind || !progress.InitiallyEnabled {
			return false, ErrMigrationFenceProof
		}
		prior := record.V2Members[memberIndex]
		if !validMonotonicEvidence(prior.PauseEvidenceDigest, progress.PauseEvidenceDigest) ||
			!validMonotonicEvidence(prior.StopEvidenceDigest, progress.StopEvidenceDigest) ||
			!validMonotonicEvidence(prior.DisableEvidenceDigest, progress.DisableEvidenceDigest) ||
			!validMonotonicEvidence(prior.AbsenceEvidenceDigest, progress.AbsenceEvidenceDigest) ||
			!validMonotonicEvidence(prior.ZeroWorkEvidenceDigest, progress.ZeroWorkEvidenceDigest) ||
			(progress.StopEvidenceDigest != "" && progress.PauseEvidenceDigest == "") ||
			(progress.DisableEvidenceDigest != "" && progress.StopEvidenceDigest == "") ||
			(progress.AbsenceEvidenceDigest != "" && progress.DisableEvidenceDigest == "") ||
			(progress.ZeroWorkEvidenceDigest != "" && progress.AbsenceEvidenceDigest == "") {
			return false, ErrMigrationFenceProof
		}
		if reflect.DeepEqual(prior, progress) {
			return false, nil
		}
		record.V2Members[memberIndex] = progress
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

// RecordPRDMigrationPublisherPause stores actual pause evidence after the
// durable fencing state exists. The fence cannot complete until every
// inventoried publisher and direct client is proved paused.
func (r *Registry) RecordPRDMigrationPublisherPause(expectedGeneration uint64, transitionID, publisherID, evidenceDigest string) (Snapshot, error) {
	if !digestPattern.MatchString(evidenceDigest) {
		return Snapshot{}, ErrMigrationFenceProof
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, transitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationFencing {
			return false, ErrMigrationFenceState
		}
		for publisherIndex := range record.Publishers {
			publisher := &record.Publishers[publisherIndex]
			if publisher.ID != publisherID {
				continue
			}
			if publisher.PauseEvidenceDigest != "" {
				if publisher.PauseEvidenceDigest == evidenceDigest {
					return false, nil
				}
				return false, ErrMigrationFenceProof
			}
			publisher.PauseEvidenceDigest = evidenceDigest
			snapshot.PRDMigrationFences[index] = *record
			return true, nil
		}
		return false, ErrMigrationFenceNotFound
	})
}

// RecordPRDMigrationSnapshot persists the empty paired snapshot only after
// every inventoried V2 member is paused, stopped, disabled, absent, and proved
// to have zero work. The observation generation is the next registry CAS.
func (r *Registry) RecordPRDMigrationSnapshot(expectedGeneration uint64, transitionID string, observed mailboxfence.IngressSnapshot) (Snapshot, error) {
	digest, err := mailboxfence.DigestIngressSnapshot(observed)
	if err != nil || observed.RequireEmpty() != nil || expectedGeneration == ^uint64(0) || observed.ObservationGeneration != expectedGeneration+1 {
		return Snapshot{}, ErrMigrationFenceProof
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, transitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationFencing || !allV2MembersProven(record.V2Members) || !allMigrationPublishersPaused(record.Publishers) || observed.InboxID != record.InboxID || observed.Root != record.MailboxRoot {
			return false, ErrMigrationFenceProof
		}
		if record.Snapshot != nil {
			if reflect.DeepEqual(*record.Snapshot, observed) && record.SnapshotDigest == digest {
				return false, nil
			}
			return false, ErrMigrationFenceState
		}
		copy := observed
		record.Snapshot, record.SnapshotDigest = &copy, digest
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

// MarkPRDMigrationFenced commits the registry half of a marker already
// written under the exclusive mailbox lock. A crash between marker and this
// CAS remains fail-closed and is reconciled from the exact marker later.
func (r *Registry) MarkPRDMigrationFenced(expectedGeneration uint64, marker mailboxfence.MigrationFenceMarker) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, marker.LaneTransitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationFencing || record.Snapshot == nil || marker.State != mailboxfence.MigrationFenceFenced ||
			!markerMatchesMigrationRecord(marker, *record) || !allV2MembersProven(record.V2Members) || !allMigrationPublishersPaused(record.Publishers) {
			return false, ErrMigrationFenceProof
		}
		if err := appendMigrationTransition(record, PRDMigrationFenced, record.SnapshotDigest); err != nil {
			return false, err
		}
		record.State = PRDMigrationFenced
		record.MarkerDigest = mailboxfence.DigestMigrationFenceMarker(marker)
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

// RecordPRDMigrationMarkerDigest binds the latest atomically written marker
// state after an abort-state update. It refuses markers whose state or shared
// root/snapshot fields differ from the durable registry record.
func (r *Registry) RecordPRDMigrationMarkerDigest(expectedGeneration uint64, marker mailboxfence.MigrationFenceMarker) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, marker.LaneTransitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		wantState := map[mailboxfence.MigrationFenceState]PRDMigrationFenceState{
			mailboxfence.MigrationFenceFenced:   PRDMigrationFenced,
			mailboxfence.MigrationFenceAborting: PRDMigrationAborting,
			mailboxfence.MigrationFenceAborted:  PRDMigrationAborted,
		}[marker.State]
		if wantState == "" || record.State != wantState || !markerMatchesMigrationRecord(marker, *record) {
			return false, ErrMigrationFenceProof
		}
		digest := mailboxfence.DigestMigrationFenceMarker(marker)
		if digest == "" {
			return false, ErrMigrationFenceProof
		}
		if record.MarkerDigest == digest {
			return false, nil
		}
		record.MarkerDigest = digest
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

// RecordPRDMigrationDeferredPairs appends complete pairs from one newer
// descriptor snapshot. Existing identities are retained at their first
// observation generation; replacements or disappearance are rejected.
func (r *Registry) RecordPRDMigrationDeferredPairs(expectedGeneration uint64, transitionID string, observed mailboxfence.IngressSnapshot) (Snapshot, error) {
	digest, err := mailboxfence.DigestIngressSnapshot(observed)
	if err != nil || expectedGeneration == ^uint64(0) || observed.ObservationGeneration != expectedGeneration+1 {
		return Snapshot{}, ErrMigrationFenceProof
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, transitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationFenced || observed.InboxID != record.InboxID || observed.Root != record.MailboxRoot || record.Snapshot == nil {
			return false, ErrMigrationFenceState
		}
		if record.DeferredObservations != nil && record.DeferredObservations[len(record.DeferredObservations)-1].ObservationGeneration >= observed.ObservationGeneration {
			return false, ErrMigrationFenceProof
		}
		for _, prior := range record.DeferredPairs {
			if !pairPresent(observed.Pairs, prior) {
				return false, ErrMigrationFenceProof
			}
		}
		for _, pair := range observed.Pairs {
			if pair.ObservationGeneration != observed.ObservationGeneration || validateDeferredPair(*record, pair) != nil {
				return false, ErrMigrationFenceProof
			}
			if prior := deferredPairByKey(record.DeferredPairs, pair.Kind, pair.LogicalID); prior != nil {
				if !samePairIdentity(*prior, pair) {
					return false, ErrMigrationFenceProof
				}
				continue
			}
			record.DeferredPairs = append(record.DeferredPairs, pair)
		}
		sort.Slice(record.DeferredPairs, func(i, j int) bool {
			return deferredPairKey(record.DeferredPairs[i]) < deferredPairKey(record.DeferredPairs[j])
		})
		record.DeferredObservations = append(record.DeferredObservations, MigrationDeferredObservation{ObservationGeneration: observed.ObservationGeneration, SnapshotDigest: digest})
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

// BeginPRDMigrationAbort durably records the abort intent while the marker
// still blocks all shared publishers. The caller must verify every retained
// pair under the exclusive lock before this step.
func (r *Registry) BeginPRDMigrationAbort(expectedGeneration uint64, transitionID, evidenceDigest string) (Snapshot, error) {
	if !digestPattern.MatchString(evidenceDigest) {
		return Snapshot{}, ErrMigrationFenceProof
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, transitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationFenced {
			return false, ErrMigrationFenceState
		}
		if err := appendMigrationTransition(record, PRDMigrationAborting, evidenceDigest); err != nil {
			return false, err
		}
		record.State, record.AbortEvidenceDigest = PRDMigrationAborting, evidenceDigest
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

// CompletePRDMigrationAbort durably closes abort only if an exclusive current
// snapshot still contains every exact deferred pair and no unexplained pair.
// It does not restart V2 or remove the marker.
func (r *Registry) CompletePRDMigrationAbort(expectedGeneration uint64, transitionID, evidenceDigest string, observed mailboxfence.IngressSnapshot) (Snapshot, error) {
	if !digestPattern.MatchString(evidenceDigest) || expectedGeneration == ^uint64(0) || observed.ObservationGeneration != expectedGeneration+1 {
		return Snapshot{}, ErrMigrationFenceProof
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		record, index := migrationFenceByID(&snapshot.PRDMigrationFences, transitionID)
		if record == nil {
			return false, ErrMigrationFenceNotFound
		}
		if record.State != PRDMigrationAborting || record.AbortEvidenceDigest != evidenceDigest || observed.InboxID != record.InboxID || observed.Root != record.MailboxRoot ||
			len(observed.Pairs) != len(record.DeferredPairs) {
			return false, ErrMigrationFenceProof
		}
		for _, pair := range record.DeferredPairs {
			if !pairPresent(observed.Pairs, pair) {
				return false, ErrMigrationFenceProof
			}
		}
		if err := appendMigrationTransition(record, PRDMigrationAborted, evidenceDigest); err != nil {
			return false, err
		}
		record.State = PRDMigrationAborted
		snapshot.PRDMigrationFences[index] = *record
		return true, nil
	})
}

func validatePRDMigrationFences(snapshot Snapshot) error {
	if len(snapshot.PRDMigrationFences) > 64 {
		return ErrInvalidRegistry
	}
	seenIDs := make(map[string]struct{}, len(snapshot.PRDMigrationFences))
	activeRoots := make(map[string]struct{})
	activeFences := 0
	for index, record := range snapshot.PRDMigrationFences {
		if index > 0 && snapshot.PRDMigrationFences[index-1].LaneTransitionID >= record.LaneTransitionID {
			return ErrInvalidRegistry
		}
		if _, exists := seenIDs[record.LaneTransitionID]; exists {
			return ErrInvalidRegistry
		}
		seenIDs[record.LaneTransitionID] = struct{}{}
		if err := validatePRDMigrationFenceRecord(&snapshot, record); err != nil {
			return fmt.Errorf("%w: PRD migration fence %s: %v", ErrInvalidRegistry, record.LaneTransitionID, err)
		}
		if record.State != PRDMigrationAborted {
			activeFences++
			if activeFences > 1 {
				return ErrInvalidRegistry
			}
			if _, exists := activeRoots[record.RootRecordID]; exists {
				return ErrInvalidRegistry
			}
			activeRoots[record.RootRecordID] = struct{}{}
		}
	}
	return nil
}

func validatePRDMigrationFenceRecord(snapshot *Snapshot, record PRDMigrationFenceRecord) error {
	root, ok := recordByID(snapshot.Records, record.RootRecordID)
	if !ok || root.Lane != config.ReleaseLanePRD || root.State != StateLegacyPRDReserved {
		return fmt.Errorf("%w: root link", ErrInvalidRegistry)
	}
	if !operationIDPattern.MatchString(record.LaneTransitionID) || !registryIDPattern.MatchString(record.RootRecordID) ||
		record.InboxID != root.InboxID || record.NormalizedRoot != root.NormalizedRoot || record.RootIdentity != root.RootIdentity ||
		record.ConfigurationDigest != root.ConfigurationDigest || !digestPattern.MatchString(record.PreflightDigest) {
		return fmt.Errorf("%w: transition identity", ErrInvalidRegistry)
	}
	if record.MailboxRoot.Device != root.RootIdentity.Device || record.MailboxRoot.Inode != root.RootIdentity.Inode ||
		record.MailboxRoot.UID == 0 || record.MailboxRoot.Mode&0o170000 != 0o040000 || record.MailboxRoot.Mode&0o7777 != 0o700 {
		return fmt.Errorf("%w: mailbox root identity", ErrInvalidRegistry)
	}
	if !validMigrationPublishers(record.Publishers) || !validMigrationMembers(record.V2Members) || !validMigrationHistory(record) {
		return fmt.Errorf("%w: preflight inventory or transition history", ErrInvalidRegistry)
	}
	if record.Snapshot == nil {
		if record.SnapshotDigest != "" || record.MarkerDigest != "" || len(record.DeferredPairs) != 0 || len(record.DeferredObservations) != 0 || record.State != PRDMigrationFencing {
			return ErrInvalidRegistry
		}
	} else {
		digest, err := mailboxfence.DigestIngressSnapshot(*record.Snapshot)
		if err != nil || digest != record.SnapshotDigest || record.Snapshot.InboxID != record.InboxID {
			return fmt.Errorf("%w: snapshot digest", ErrInvalidRegistry)
		}
		if record.Snapshot.InboxID != record.InboxID || record.Snapshot.Root != record.MailboxRoot || record.Snapshot.RequireEmpty() != nil || !allV2MembersProven(record.V2Members) || !allMigrationPublishersPaused(record.Publishers) {
			return fmt.Errorf("%w: empty snapshot or V2 proof", ErrInvalidRegistry)
		}
	}
	if record.State == PRDMigrationFencing {
		if record.MarkerDigest != "" || record.AbortEvidenceDigest != "" || len(record.DeferredPairs) != 0 || len(record.DeferredObservations) != 0 {
			return ErrInvalidRegistry
		}
	} else if !digestPattern.MatchString(record.MarkerDigest) {
		return ErrInvalidRegistry
	}
	if record.State == PRDMigrationAborting || record.State == PRDMigrationAborted {
		if !digestPattern.MatchString(record.AbortEvidenceDigest) {
			return ErrInvalidRegistry
		}
	} else if record.AbortEvidenceDigest != "" {
		return ErrInvalidRegistry
	}
	for _, pair := range record.DeferredPairs {
		if validateDeferredPair(record, pair) != nil || record.Snapshot == nil || pair.ObservationGeneration <= record.Snapshot.ObservationGeneration {
			return ErrInvalidRegistry
		}
		foundObservation := false
		for _, observation := range record.DeferredObservations {
			if observation.ObservationGeneration == pair.ObservationGeneration {
				foundObservation = true
				break
			}
		}
		if !foundObservation {
			return ErrInvalidRegistry
		}
	}
	lastGeneration := uint64(0)
	for _, observation := range record.DeferredObservations {
		if observation.ObservationGeneration <= lastGeneration || !digestPattern.MatchString(observation.SnapshotDigest) || record.Snapshot == nil || observation.ObservationGeneration <= record.Snapshot.ObservationGeneration {
			return ErrInvalidRegistry
		}
		lastGeneration = observation.ObservationGeneration
	}
	if (len(record.DeferredPairs) > 0 || len(record.DeferredObservations) > 0) && record.State == PRDMigrationFencing {
		return ErrInvalidRegistry
	}
	return nil
}

func validMigrationPublishers(publishers []MigrationPublisher) bool {
	if len(publishers) == 0 || len(publishers) > 1024 {
		return false
	}
	for index, publisher := range publishers {
		if !registryIDPattern.MatchString(publisher.ID) || !validMigrationSurface(publisher.Surface) || publisher.Kind == "" || len(publisher.Kind) > 64 ||
			publisher.PauseControl == "" || len(publisher.PauseControl) > 256 || !publisher.PauseControlTested ||
			(publisher.PauseEvidenceDigest != "" && !digestPattern.MatchString(publisher.PauseEvidenceDigest)) || (index > 0 && publishers[index-1].ID >= publisher.ID) {
			return false
		}
		if publisher.RawWriter {
			if publisher.MigrationCapabilityVersion != "" || publisher.MigrationCapabilityTested {
				return false
			}
		} else if publisher.MigrationCapabilityVersion != "1" || !publisher.MigrationCapabilityTested {
			return false
		}
	}
	return true
}

func validMigrationMembers(members []MigrationV2Member) bool {
	if len(members) < 4 || len(members) > 128 {
		return false
	}
	seenKinds := make(map[string]struct{}, 4)
	for index, member := range members {
		if !registryIDPattern.MatchString(member.ID) || !validMigrationSurface(member.Surface) || !member.InitiallyEnabled ||
			(index > 0 && members[index-1].ID >= member.ID) || !orderedMemberEvidence(member) {
			return false
		}
		if member.Kind == MigrationMemberMacLocalIngress && member.Surface != MigrationSurfaceMac {
			return false
		}
		switch member.Kind {
		case MigrationMemberDirectListener, MigrationMemberMacLocalIngress, MigrationMemberMailboxImporter, MigrationMemberQueuedBridge:
			seenKinds[member.Kind] = struct{}{}
		default:
			return false
		}
	}
	return len(seenKinds) == 4
}

func orderedMemberEvidence(member MigrationV2Member) bool {
	if (member.PauseEvidenceDigest != "" && !digestPattern.MatchString(member.PauseEvidenceDigest)) ||
		(member.StopEvidenceDigest != "" && !digestPattern.MatchString(member.StopEvidenceDigest)) ||
		(member.DisableEvidenceDigest != "" && !digestPattern.MatchString(member.DisableEvidenceDigest)) ||
		(member.AbsenceEvidenceDigest != "" && !digestPattern.MatchString(member.AbsenceEvidenceDigest)) ||
		(member.ZeroWorkEvidenceDigest != "" && !digestPattern.MatchString(member.ZeroWorkEvidenceDigest)) {
		return false
	}
	return (member.StopEvidenceDigest == "" || member.PauseEvidenceDigest != "") &&
		(member.DisableEvidenceDigest == "" || member.StopEvidenceDigest != "") &&
		(member.AbsenceEvidenceDigest == "" || member.DisableEvidenceDigest != "") &&
		(member.ZeroWorkEvidenceDigest == "" || member.AbsenceEvidenceDigest != "")
}

func validMigrationHistory(record PRDMigrationFenceRecord) bool {
	if len(record.History) == 0 || len(record.History) > 4 || record.History[0].Sequence != 1 || record.History[0].From != "" || record.History[0].To != PRDMigrationFencing || record.History[0].EvidenceDigest != record.PreflightDigest {
		return false
	}
	want := []PRDMigrationFenceState{PRDMigrationFencing}
	if record.State == PRDMigrationFenced || record.State == PRDMigrationAborting || record.State == PRDMigrationAborted {
		want = append(want, PRDMigrationFenced)
	}
	if record.State == PRDMigrationAborting {
		want = append(want, PRDMigrationAborting)
	}
	if record.State == PRDMigrationAborted {
		want = append(want, PRDMigrationAborting, PRDMigrationAborted)
	}
	if len(record.History) != len(want) {
		return false
	}
	for index, transition := range record.History {
		if transition.Sequence != uint64(index+1) || transition.To != want[index] || (index > 0 && transition.From != want[index-1]) {
			return false
		}
		if index > 0 && !digestPattern.MatchString(transition.EvidenceDigest) {
			return false
		}
	}
	return record.History[len(record.History)-1].To == record.State
}

func validateDeferredPair(record PRDMigrationFenceRecord, pair mailboxfence.ReadyPairSnapshot) error {
	if pair.InboxID != record.InboxID || pair.Root.Device != record.MailboxRoot.Device || pair.Root.Inode != record.MailboxRoot.Inode ||
		pair.ObservationGeneration == 0 || !registryIDPattern.MatchString(pair.LogicalID) ||
		pair.JSON.Device == 0 || pair.JSON.Inode == 0 || pair.JSON.UID != record.MailboxRoot.UID || pair.JSON.Mode&0o170000 != 0o100000 || pair.JSON.Mode&0o7777 != 0o600 || pair.JSON.Size <= 0 ||
		pair.Ready.Device == 0 || pair.Ready.Inode == 0 || pair.Ready.UID != record.MailboxRoot.UID || pair.Ready.Mode&0o170000 != 0o100000 || pair.Ready.Mode&0o7777 != 0o600 || pair.Ready.Size != 0 ||
		pair.Directory.Device != record.MailboxRoot.Device || pair.Directory.Inode == 0 || pair.Directory.UID != record.MailboxRoot.UID || pair.Directory.Mode&0o170000 != 0o040000 || pair.Directory.Mode&0o7777 != 0o700 ||
		!digestPattern.MatchString("sha256:"+pair.JSONSHA256) || !digestPattern.MatchString("sha256:"+pair.ReadySHA256) {
		return ErrMigrationFenceProof
	}
	jsonPath, readyPath := "", ""
	switch pair.Kind {
	case "request":
		jsonPath, readyPath = "inbox/"+pair.LogicalID+".json", "inbox/"+pair.LogicalID+".ready"
	case "ack":
		jsonPath, readyPath = "acks/"+pair.LogicalID+".json", "acks/"+pair.LogicalID+".ready"
	default:
		return ErrMigrationFenceProof
	}
	if pair.JSONPath != jsonPath || pair.ReadyPath != readyPath {
		return ErrMigrationFenceProof
	}
	return nil
}

func markerMatchesMigrationRecord(marker mailboxfence.MigrationFenceMarker, record PRDMigrationFenceRecord) bool {
	return marker.LaneTransitionID == record.LaneTransitionID && marker.InboxID == record.InboxID && marker.Root == record.MailboxRoot &&
		marker.ConfigurationDigest == record.ConfigurationDigest && marker.SnapshotDigest == record.SnapshotDigest &&
		marker.ObservationGeneration == record.Snapshot.ObservationGeneration
}

func allV2MembersProven(members []MigrationV2Member) bool {
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

func allMigrationPublishersPaused(publishers []MigrationPublisher) bool {
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

func validMonotonicEvidence(prior, next string) bool {
	if prior != "" {
		return prior == next && digestPattern.MatchString(next)
	}
	return next == "" || digestPattern.MatchString(next)
}

func validMigrationSurface(surface string) bool {
	switch surface {
	case MigrationSurfaceMac, MigrationSurfaceLinuxHost, MigrationSurfaceSandboxHost, MigrationSurfaceFactorySandbox:
		return true
	default:
		return false
	}
}

func migrationFenceByID(records *[]PRDMigrationFenceRecord, id string) (*PRDMigrationFenceRecord, int) {
	for index := range *records {
		if (*records)[index].LaneTransitionID == id {
			return &(*records)[index], index
		}
	}
	return nil, -1
}

func migrationMemberIndex(members []MigrationV2Member, id string) int {
	for index := range members {
		if members[index].ID == id {
			return index
		}
	}
	return -1
}

func appendMigrationTransition(record *PRDMigrationFenceRecord, next PRDMigrationFenceState, evidence string) error {
	if len(record.History) == 0 {
		return ErrMigrationFenceState
	}
	from := record.State
	allowed := (from == PRDMigrationFencing && next == PRDMigrationFenced) || (from == PRDMigrationFenced && next == PRDMigrationAborting) || (from == PRDMigrationAborting && next == PRDMigrationAborted)
	if !allowed || !digestPattern.MatchString(evidence) {
		return ErrMigrationFenceState
	}
	record.History = append(record.History, PRDMigrationTransition{Sequence: uint64(len(record.History) + 1), From: from, To: next, EvidenceDigest: evidence})
	return nil
}

func pairPresent(pairs []mailboxfence.ReadyPairSnapshot, target mailboxfence.ReadyPairSnapshot) bool {
	for _, pair := range pairs {
		if pair.Kind == target.Kind && pair.LogicalID == target.LogicalID && samePairIdentity(pair, target) {
			return true
		}
	}
	return false
}

func samePairIdentity(left, right mailboxfence.ReadyPairSnapshot) bool {
	left.ObservationGeneration, right.ObservationGeneration = 0, 0
	return reflect.DeepEqual(left, right)
}

func deferredPairByKey(pairs []mailboxfence.ReadyPairSnapshot, kind, logicalID string) *mailboxfence.ReadyPairSnapshot {
	for index := range pairs {
		if pairs[index].Kind == kind && pairs[index].LogicalID == logicalID {
			return &pairs[index]
		}
	}
	return nil
}

func deferredPairKey(pair mailboxfence.ReadyPairSnapshot) string {
	return pair.Kind + "\x00" + pair.LogicalID
}
