// Package laneregistry stores the Mac-wide mailbox-root coordination mirror.
// It is not a signing authority or a second SQLite runtime database.
package laneregistry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneroot"
)

const (
	FormatVersion    = 1
	RegistryFile     = "registry.json"
	LockFile         = "registry.lock"
	maxRegistryBytes = 16 << 20
)

var (
	ErrInvalidRegistry          = errors.New("invalid lane registry")
	ErrGenerationConflict       = errors.New("lane registry generation conflict")
	ErrCollision                = errors.New("lane mailbox root collision")
	ErrRecordNotFound           = errors.New("lane registry record not found")
	ErrInvalidTransition        = errors.New("invalid lane registry transition")
	ErrInvalidReconstruction    = errors.New("invalid lane registry reconstruction proposal")
	ErrRegistryRecoveryRequired = errors.New("lane registry recovery required")
	ErrAlreadyInitialized       = errors.New("lane registry already initialized")
	ErrClosed                   = errors.New("lane registry is closed")
	registryIDPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	digestPattern               = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	ancillaryNamePattern        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	fixedMailboxEntries         = map[string]struct{}{"inbox": {}, "outbox": {}, "events": {}, "acks": {}, "diagnostics": {}}
)

type RecordState string

const (
	StateLegacyPRDReserved RecordState = "legacy_prd_reserved"
	StatePrepared          RecordState = "prepared"
	StateActive            RecordState = "active"
	StateSuperseded        RecordState = "superseded"
	StateRetired           RecordState = "retired"
)

// RootRecord is a durable coordination claim. RootIdentity and Volume are
// observations supplied by the descriptor-based root handoff, never inferred
// from a pathname by this registry package.
type RootRecord struct {
	ID                  string                            `json:"id"`
	Host                config.HostKind                   `json:"host"`
	Lane                config.ReleaseLane                `json:"release_lane"`
	InboxID             string                            `json:"inbox_id"`
	NormalizedRoot      string                            `json:"normalized_root"`
	RootIdentity        laneroot.FileIdentity             `json:"filesystem_identity"`
	ConfigurationDigest string                            `json:"configuration_digest"`
	Epoch               uint64                            `json:"epoch"`
	State               RecordState                       `json:"state"`
	Supersedes          string                            `json:"supersedes,omitempty"`
	Volume              *laneroot.VolumeIdentity          `json:"volume,omitempty"`
	AncillaryEntries    []laneroot.AncillaryEntrySnapshot `json:"retained_ancillary,omitempty"`
}

type Snapshot struct {
	Version            int                       `json:"version"`
	Generation         uint64                    `json:"generation"`
	Records            []RootRecord              `json:"records"`
	Operations         []LaneOperation           `json:"operations,omitempty"`
	PRDMigrationFences []PRDMigrationFenceRecord `json:"prd_migration_fences,omitempty"`
	PRDOperationGroups []PRDOperationGroup       `json:"prd_operation_groups,omitempty"`
}

// RuntimeReconciliation is the exact active-candidate identity observed after
// service reconciliation. The registry stores no runtime or SQLite state; it
// only accepts the matching observation before mirroring an active claim.
type RuntimeReconciliation struct {
	RecordID            string
	Lane                config.ReleaseLane
	InboxID             string
	RootIdentity        laneroot.FileIdentity
	ConfigurationDigest string
	Epoch               uint64
	ServiceReconciled   bool
}

type Registry struct {
	mu       sync.RWMutex
	path     string
	dirFD    int
	dirID    laneroot.FileIdentity
	ownerUID uint32
	closed   bool
}

// Initialize creates a new owner-only registry directory and its initial
// durable JSON state. It is only for an approved first-time genesis after the
// trusted inventory is available; never use it to recover a missing or damaged
// prior registry. It never replaces an existing registry. If initialization
// is interrupted, Open fails closed on the incomplete directory.
func Initialize(path string) (*Registry, error) {
	if !canonicalPath(path) {
		return nil, ErrInvalidRegistry
	}
	ownerUID := uint32(os.Getuid())
	if ownerUID == 0 {
		return nil, ErrInvalidRegistry
	}
	parentFD, leaf, err := openParentDirectory(path, ownerUID)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	if err := unix.Mkdirat(parentFD, leaf, 0o700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, ErrAlreadyInitialized
		}
		return nil, ErrInvalidRegistry
	}
	dirFD, err := unix.Openat(parentFD, leaf, directoryFlags, 0)
	if err != nil {
		return nil, ErrInvalidRegistry
	}
	dirStat, err := statFD(dirFD)
	if err != nil || !validPrivateDirectory(dirStat, ownerUID) {
		unix.Close(dirFD)
		return nil, ErrInvalidRegistry
	}
	lockFD, err := unix.Openat(dirFD, LockFile, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		unix.Close(dirFD)
		return nil, ErrInvalidRegistry
	}
	lockStat, statErr := statFD(lockFD)
	if statErr != nil || !validPrivateRegularFile(lockStat, ownerUID) || unix.Fsync(lockFD) != nil {
		unix.Close(lockFD)
		unix.Close(dirFD)
		return nil, ErrInvalidRegistry
	}
	if err := unix.Close(lockFD); err != nil {
		unix.Close(dirFD)
		return nil, ErrInvalidRegistry
	}
	if err := writeSnapshotAt(dirFD, ownerUID, Snapshot{Version: FormatVersion, Records: []RootRecord{}}); err != nil {
		unix.Close(dirFD)
		return nil, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		unix.Close(dirFD)
		return nil, ErrInvalidRegistry
	}
	registry, err := openDirectory(path, ownerUID)
	if err != nil {
		unix.Close(dirFD)
		return nil, err
	}
	unix.Close(dirFD)
	return registry, nil
}

// Open loads an existing registry. It does not create a missing directory,
// lock, or JSON file; missing or malformed state is a recovery stop.
func Open(path string) (*Registry, error) {
	if !canonicalPath(path) || os.Getuid() == 0 {
		return nil, ErrInvalidRegistry
	}
	return openDirectory(path, uint32(os.Getuid()))
}

func openDirectory(path string, ownerUID uint32) (*Registry, error) {
	dirFD, err := openAbsoluteDirectory(path, ownerUID)
	if err != nil {
		return nil, err
	}
	dirStat, err := statFD(dirFD)
	if err != nil || !validPrivateDirectory(dirStat, ownerUID) {
		unix.Close(dirFD)
		return nil, ErrInvalidRegistry
	}
	registry := &Registry{path: path, dirFD: dirFD, dirID: identity(dirStat), ownerUID: ownerUID}
	if _, err := registry.Snapshot(); err != nil {
		registry.Close()
		return nil, err
	}
	return registry, nil
}

// Close releases the pinned directory descriptor. Registry methods are safe
// for concurrent use until Close returns.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return unix.Close(r.dirFD)
}

// Snapshot reads a consistent immutable snapshot under a shared filesystem
// lock. It never repairs missing, corrupt, or replaced registry bytes.
func (r *Registry) Snapshot() (Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return Snapshot{}, ErrClosed
	}
	lockFD, err := r.acquireLock(unix.LOCK_SH)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseLock(lockFD)
	snapshot, err := readSnapshotAt(r.dirFD, r.ownerUID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// Prepare adds one candidate root record using compare-and-swap. A retry may
// repeat an identical prepared record; every other path/identity collision is
// rejected across all five retained states.
func (r *Registry) Prepare(expectedGeneration uint64, record RootRecord) (Snapshot, error) {
	if record.State != StatePrepared {
		return Snapshot{}, ErrInvalidTransition
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		if prior, ok := recordByID(snapshot.Records, record.ID); ok {
			if prior.State == StatePrepared && sameRecord(prior, record) {
				return false, nil
			}
			return false, ErrCollision
		}
		if hasUnarchivedOperation(snapshot, record.Lane) {
			return false, ErrOperationBusy
		}
		if err := validateRecord(record); err != nil {
			return false, err
		}
		if err := validateSuccessor(snapshot.Records, record, StateActive, StateLegacyPRDReserved); err != nil {
			return false, err
		}
		if err := validateEpoch(snapshot.Records, record); err != nil {
			return false, err
		}
		if err := validateNoCollision(snapshot.Records, record); err != nil {
			return false, err
		}
		sortAncillary(record.AncillaryEntries)
		snapshot.Records = append(snapshot.Records, record)
		return true, nil
	})
}

// ReserveLegacyPRD creates a retained reservation for the unmodified V2 PRD
// mailbox. It is a coordination record only and does not touch V2 files.
func (r *Registry) ReserveLegacyPRD(expectedGeneration uint64, record RootRecord) (Snapshot, error) {
	if record.State != StateLegacyPRDReserved || record.Lane != config.ReleaseLanePRD || record.Supersedes != "" {
		return Snapshot{}, ErrInvalidTransition
	}
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		if _, exists := recordByID(snapshot.Records, record.ID); exists {
			return false, ErrCollision
		}
		if err := validateRecord(record); err != nil {
			return false, err
		}
		if err := validateEpoch(snapshot.Records, record); err != nil {
			return false, err
		}
		if err := validateNoCollision(snapshot.Records, record); err != nil {
			return false, err
		}
		sortAncillary(record.AncillaryEntries)
		snapshot.Records = append(snapshot.Records, record)
		return true, nil
	})
}

// Activate changes one prepared record to active and atomically retains its
// exact incumbent as superseded. A legacy reservation is superseded in the
// same CAS; the reservation itself never changes V2 runtime state.
func (r *Registry) Activate(expectedGeneration uint64, id string, reconciliation RuntimeReconciliation) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		index := recordIndex(snapshot.Records, id)
		if index < 0 {
			return false, ErrRecordNotFound
		}
		candidate := &snapshot.Records[index]
		if candidate.State != StatePrepared {
			return false, ErrInvalidTransition
		}
		if hasUnarchivedOperation(snapshot, candidate.Lane) {
			return false, ErrOperationBusy
		}
		if !reconciliation.ServiceReconciled || reconciliation.RecordID != candidate.ID ||
			reconciliation.Lane != candidate.Lane || reconciliation.InboxID != candidate.InboxID ||
			reconciliation.RootIdentity != candidate.RootIdentity ||
			reconciliation.ConfigurationDigest != candidate.ConfigurationDigest || reconciliation.Epoch != candidate.Epoch {
			return false, ErrInvalidTransition
		}
		if candidate.Supersedes != "" {
			incumbentIndex := recordIndex(snapshot.Records, candidate.Supersedes)
			if incumbentIndex < 0 || (snapshot.Records[incumbentIndex].State != StateActive && snapshot.Records[incumbentIndex].State != StateLegacyPRDReserved) {
				return false, ErrInvalidTransition
			}
			snapshot.Records[incumbentIndex].State = StateSuperseded
		}
		candidate.State = StateActive
		return true, nil
	})
}

// RetirePrepared preserves a failed prepared claim as a collision-set
// tombstone. Active and legacy PRD claims require their designed operation
// path and cannot be retired through this helper.
func (r *Registry) RetirePrepared(expectedGeneration uint64, id string) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		index := recordIndex(snapshot.Records, id)
		if index < 0 {
			return false, ErrRecordNotFound
		}
		if snapshot.Records[index].State != StatePrepared && snapshot.Records[index].State != StateSuperseded {
			return false, ErrInvalidTransition
		}
		if hasUnarchivedOperation(snapshot, snapshot.Records[index].Lane) {
			return false, ErrOperationBusy
		}
		snapshot.Records[index].State = StateRetired
		return true, nil
	})
}

// RefreshLegacyPRD appends a higher-epoch reservation after reviewed V2
// maintenance. It keeps the old reservation in history and requires the same
// root/path identity; callers must supply a fresh configuration digest and
// ancillary inventory from the reviewed inventory.
func (r *Registry) RefreshLegacyPRD(expectedGeneration uint64, oldID string, newID, newDigest string, ancillary []laneroot.AncillaryEntrySnapshot) (Snapshot, error) {
	return r.update(expectedGeneration, func(snapshot *Snapshot) (bool, error) {
		oldIndex := recordIndex(snapshot.Records, oldID)
		if oldIndex < 0 {
			return false, ErrRecordNotFound
		}
		old := snapshot.Records[oldIndex]
		if old.State != StateLegacyPRDReserved || newID == old.ID {
			return false, ErrInvalidTransition
		}
		if _, exists := recordByID(snapshot.Records, newID); exists {
			return false, ErrCollision
		}
		next := old
		next.ID, next.ConfigurationDigest, next.Epoch = newID, newDigest, old.Epoch+1
		next.State, next.Supersedes = StateLegacyPRDReserved, old.ID
		next.AncillaryEntries = append([]laneroot.AncillaryEntrySnapshot(nil), ancillary...)
		sortAncillary(next.AncillaryEntries)
		if err := validateRecord(next); err != nil {
			return false, err
		}
		if err := validateEpoch(snapshot.Records, next); err != nil {
			return false, err
		}
		if err := validateNoCollision(snapshot.Records, next); err != nil {
			return false, err
		}
		snapshot.Records[oldIndex].State = StateSuperseded
		snapshot.Records = append(snapshot.Records, next)
		return true, nil
	})
}

func (r *Registry) update(expectedGeneration uint64, mutate func(*Snapshot) (bool, error)) (Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return Snapshot{}, ErrClosed
	}
	lockFD, err := r.acquireLock(unix.LOCK_EX)
	if err != nil {
		return Snapshot{}, err
	}
	defer releaseLock(lockFD)
	snapshot, err := readSnapshotAt(r.dirFD, r.ownerUID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Generation != expectedGeneration {
		return Snapshot{}, ErrGenerationConflict
	}
	changed, err := mutate(&snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if !changed {
		return snapshot, nil
	}
	if snapshot.Generation == ^uint64(0) {
		return Snapshot{}, ErrInvalidRegistry
	}
	snapshot.Generation++
	sort.Slice(snapshot.Records, func(i, j int) bool { return snapshot.Records[i].ID < snapshot.Records[j].ID })
	if snapshot.Operations != nil {
		sort.Slice(snapshot.Operations, func(i, j int) bool { return snapshot.Operations[i].ID < snapshot.Operations[j].ID })
	}
	if snapshot.PRDMigrationFences != nil {
		sort.Slice(snapshot.PRDMigrationFences, func(i, j int) bool {
			return snapshot.PRDMigrationFences[i].LaneTransitionID < snapshot.PRDMigrationFences[j].LaneTransitionID
		})
	}
	if snapshot.PRDOperationGroups != nil {
		sort.Slice(snapshot.PRDOperationGroups, func(i, j int) bool { return snapshot.PRDOperationGroups[i].ID < snapshot.PRDOperationGroups[j].ID })
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := writeSnapshotAt(r.dirFD, r.ownerUID, snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (r *Registry) acquireLock(mode int) (int, error) {
	if err := r.recheckPath(); err != nil {
		return -1, err
	}
	lockFD, err := unix.Openat(r.dirFD, LockFile, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrInvalidRegistry
	}
	stat, err := statFD(lockFD)
	if err != nil || !validPrivateRegularFile(stat, r.ownerUID) {
		unix.Close(lockFD)
		return -1, ErrInvalidRegistry
	}
	if err := unix.Flock(lockFD, mode); err != nil {
		unix.Close(lockFD)
		return -1, ErrInvalidRegistry
	}
	if err := r.recheckPath(); err != nil {
		releaseLock(lockFD)
		return -1, err
	}
	return lockFD, nil
}

func (r *Registry) recheckPath() error {
	fd, err := openAbsoluteDirectory(r.path, r.ownerUID)
	if err != nil {
		return err
	}
	stat, statErr := statFD(fd)
	closeErr := unix.Close(fd)
	if statErr != nil || closeErr != nil || identity(stat) != r.dirID {
		return ErrInvalidRegistry
	}
	return nil
}

func releaseLock(fd int) {
	_ = unix.Flock(fd, unix.LOCK_UN)
	_ = unix.Close(fd)
}

func readSnapshotAt(dirFD int, ownerUID uint32) (Snapshot, error) {
	fd, err := unix.Openat(dirFD, RegistryFile, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Snapshot{}, ErrInvalidRegistry
	}
	file := os.NewFile(uintptr(fd), RegistryFile)
	if file == nil {
		unix.Close(fd)
		return Snapshot{}, ErrInvalidRegistry
	}
	stat, statErr := file.Stat()
	if statErr != nil || !validPrivateRegularInfo(stat, ownerUID) || stat.Size() > maxRegistryBytes {
		file.Close()
		return Snapshot{}, ErrInvalidRegistry
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxRegistryBytes+1))
	if readErr != nil || len(data) > maxRegistryBytes {
		file.Close()
		return Snapshot{}, ErrInvalidRegistry
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		file.Close()
		return Snapshot{}, ErrInvalidRegistry
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		file.Close()
		return Snapshot{}, ErrInvalidRegistry
	}
	if err := file.Close(); err != nil {
		return Snapshot{}, ErrInvalidRegistry
	}
	canonical, err := canonicalJSON(snapshot)
	if err != nil || !bytes.Equal(data, canonical) {
		return Snapshot{}, ErrInvalidRegistry
	}
	return snapshot, nil
}

func writeSnapshotAt(dirFD int, ownerUID uint32, snapshot Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return ErrInvalidRegistry
	}
	data = append(data, '\n')
	if len(data) > maxRegistryBytes {
		return ErrInvalidRegistry
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrInvalidRegistry
	}
	tempName := ".registry-" + hex.EncodeToString(nonce[:]) + ".tmp"
	fd, err := unix.Openat(dirFD, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return ErrInvalidRegistry
	}
	tempFile := os.NewFile(uintptr(fd), tempName)
	if tempFile == nil {
		unix.Close(fd)
		_ = unix.Unlinkat(dirFD, tempName, 0)
		return ErrInvalidRegistry
	}
	stat, statErr := tempFile.Stat()
	if statErr != nil || !validPrivateRegularInfo(stat, ownerUID) {
		tempFile.Close()
		_ = unix.Unlinkat(dirFD, tempName, 0)
		return ErrInvalidRegistry
	}
	written, writeErr := tempFile.Write(data)
	if writeErr != nil || written != len(data) {
		tempFile.Close()
		_ = unix.Unlinkat(dirFD, tempName, 0)
		return ErrInvalidRegistry
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		_ = unix.Unlinkat(dirFD, tempName, 0)
		return ErrInvalidRegistry
	}
	if err := tempFile.Close(); err != nil {
		_ = unix.Unlinkat(dirFD, tempName, 0)
		return ErrInvalidRegistry
	}
	if err := unix.Renameat(dirFD, tempName, dirFD, RegistryFile); err != nil {
		_ = unix.Unlinkat(dirFD, tempName, 0)
		return ErrInvalidRegistry
	}
	if err := unix.Fsync(dirFD); err != nil {
		return ErrInvalidRegistry
	}
	return nil
}

func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.Version != FormatVersion || snapshot.Records == nil || len(snapshot.Records) > 100000 ||
		snapshot.Generation < uint64(len(snapshot.Records)) {
		return ErrInvalidRegistry
	}
	byID := make(map[string]RootRecord, len(snapshot.Records))
	lastID := ""
	for _, record := range snapshot.Records {
		if err := validateRecord(record); err != nil || record.ID <= lastID {
			return ErrInvalidRegistry
		}
		lastID = record.ID
		if _, duplicate := byID[record.ID]; duplicate {
			return ErrInvalidRegistry
		}
		byID[record.ID] = record
	}
	active := make(map[string]string)
	prepared := make(map[string]string)
	legacy := make(map[string]string)
	for _, record := range snapshot.Records {
		if record.Supersedes != "" {
			prior, exists := byID[record.Supersedes]
			if !exists || !sameRootClaim(prior, record) || prior.Epoch >= record.Epoch {
				return ErrInvalidRegistry
			}
		}
		key := string(record.Lane) + "\x00" + record.InboxID
		if record.State == StateActive {
			if _, exists := active[key]; exists {
				return ErrInvalidRegistry
			}
			active[key] = record.ID
		}
		if record.State == StatePrepared {
			if _, exists := prepared[key]; exists {
				return ErrInvalidRegistry
			}
			prepared[key] = record.ID
		}
		if record.State == StateLegacyPRDReserved {
			if _, exists := legacy[key]; exists {
				return ErrInvalidRegistry
			}
			legacy[key] = record.ID
		}
	}
	for key, activeID := range active {
		if _, exists := legacy[key]; exists {
			return ErrInvalidRegistry
		}
		if preparedID, exists := prepared[key]; exists {
			candidate := byID[preparedID]
			if candidate.Supersedes != activeID {
				return ErrInvalidRegistry
			}
		}
	}
	for key, legacyID := range legacy {
		if preparedID, exists := prepared[key]; exists {
			candidate := byID[preparedID]
			if candidate.Supersedes != legacyID {
				return ErrInvalidRegistry
			}
		}
	}
	for _, record := range snapshot.Records {
		if record.Supersedes == "" {
			continue
		}
		prior := byID[record.Supersedes]
		if record.Epoch <= prior.Epoch {
			return ErrInvalidRegistry
		}
	}
	for i, record := range snapshot.Records {
		for _, prior := range snapshot.Records[:i] {
			if record.Host != prior.Host || (!sameOrNestedPath(record.NormalizedRoot, prior.NormalizedRoot) && record.RootIdentity != prior.RootIdentity) {
				continue
			}
			if !sameRootClaim(record, prior) || !relatedSuccessor(snapshot.Records, record, prior.ID) {
				return ErrInvalidRegistry
			}
		}
	}
	if err := validateOperations(snapshot); err != nil {
		return err
	}
	if err := validatePRDMigrationFences(snapshot); err != nil {
		return err
	}
	if err := validatePRDOperationGroups(snapshot); err != nil {
		return err
	}
	return nil
}

func validateRecord(record RootRecord) error {
	if !registryIDPattern.MatchString(record.ID) || record.Host != config.HostKindMac ||
		!validLane(record.Lane) || !registryIDPattern.MatchString(record.InboxID) ||
		!canonicalPath(record.NormalizedRoot) || !validIdentity(record.RootIdentity) ||
		!digestPattern.MatchString(record.ConfigurationDigest) || record.Epoch == 0 || !validState(record.State) {
		return ErrInvalidRegistry
	}
	if record.State == StateLegacyPRDReserved && record.Lane != config.ReleaseLanePRD {
		return ErrInvalidRegistry
	}
	if record.State == StatePrepared && record.Supersedes == record.ID {
		return ErrInvalidRegistry
	}
	if requiresAPFS(record.Host, record.Lane) {
		if record.Volume == nil || !validVolume(*record.Volume) || record.Volume.Mountpoint != record.NormalizedRoot || record.Volume.Device != record.RootIdentity.Device {
			return ErrInvalidRegistry
		}
	} else if record.Volume != nil {
		return ErrInvalidRegistry
	}
	seenNames := make(map[string]struct{}, len(record.AncillaryEntries))
	seenIDs := make(map[laneroot.FileIdentity]struct{}, len(record.AncillaryEntries))
	for _, entry := range record.AncillaryEntries {
		if !ancillaryNamePattern.MatchString(entry.Name) || entry.Name == "." || entry.Name == ".." ||
			isFixedMailboxEntry(entry.Name) ||
			(entry.Kind != laneroot.EntryRegularFile && entry.Kind != laneroot.EntryDirectory) ||
			!validIdentity(entry.Identity) || entry.Identity.Device != record.RootIdentity.Device ||
			entry.OwnerUID == 0 || entry.Mode&0o022 != 0 || entry.Mode&0o7000 != 0 {
			return ErrInvalidRegistry
		}
		if _, ok := seenNames[entry.Name]; ok {
			return ErrInvalidRegistry
		}
		if _, ok := seenIDs[entry.Identity]; ok || entry.Identity == record.RootIdentity {
			return ErrInvalidRegistry
		}
		seenNames[entry.Name], seenIDs[entry.Identity] = struct{}{}, struct{}{}
	}
	return nil
}

func isFixedMailboxEntry(name string) bool {
	_, exists := fixedMailboxEntries[name]
	return exists
}

func validateSuccessor(records []RootRecord, record RootRecord, allowed ...RecordState) error {
	if record.Supersedes == "" {
		for _, prior := range records {
			if prior.Host == record.Host && prior.Lane == record.Lane && prior.InboxID == record.InboxID &&
				(prior.State == StateActive || prior.State == StateLegacyPRDReserved) {
				return ErrCollision
			}
		}
		return nil
	}
	prior, ok := recordByID(records, record.Supersedes)
	if !ok || !containsState(allowed, prior.State) || !sameRootClaim(prior, record) || record.Epoch <= prior.Epoch {
		return ErrInvalidTransition
	}
	return nil
}

func validateEpoch(records []RootRecord, record RootRecord) error {
	var maxEpoch uint64
	for _, prior := range records {
		if prior.Host == record.Host && prior.Lane == record.Lane && prior.InboxID == record.InboxID && prior.Epoch > maxEpoch {
			maxEpoch = prior.Epoch
		}
	}
	if record.Epoch != maxEpoch+1 {
		return ErrInvalidTransition
	}
	return nil
}

func validateNoCollision(records []RootRecord, record RootRecord) error {
	for _, prior := range records {
		if prior.Host != record.Host {
			continue
		}
		if !sameOrNestedPath(prior.NormalizedRoot, record.NormalizedRoot) && prior.RootIdentity != record.RootIdentity {
			continue
		}
		if !sameRootClaim(prior, record) || !relatedSuccessor(append(append([]RootRecord(nil), records...), record), record, prior.ID) {
			return ErrCollision
		}
	}
	return nil
}

func relatedSuccessor(records []RootRecord, left RootRecord, rightID string) bool {
	if chainContains(records, left, rightID) {
		return true
	}
	right, ok := recordByID(records, rightID)
	return ok && chainContains(records, right, left.ID)
}

func chainContains(records []RootRecord, record RootRecord, id string) bool {
	seen := make(map[string]struct{})
	for record.Supersedes != "" {
		if record.Supersedes == id {
			return true
		}
		if _, duplicate := seen[record.Supersedes]; duplicate {
			return false
		}
		seen[record.Supersedes] = struct{}{}
		prior, ok := recordByID(records, record.Supersedes)
		if !ok {
			return false
		}
		record = prior
	}
	return false
}

func sameRootClaim(left, right RootRecord) bool {
	return left.Host == right.Host && left.Lane == right.Lane && left.InboxID == right.InboxID &&
		left.NormalizedRoot == right.NormalizedRoot && left.RootIdentity == right.RootIdentity &&
		reflect.DeepEqual(left.Volume, right.Volume)
}

func sameRecord(left, right RootRecord) bool {
	return left.ID == right.ID && sameRootClaim(left, right) && left.ConfigurationDigest == right.ConfigurationDigest &&
		left.Epoch == right.Epoch && left.State == right.State && left.Supersedes == right.Supersedes &&
		reflect.DeepEqual(left.AncillaryEntries, right.AncillaryEntries)
}

func recordByID(records []RootRecord, id string) (RootRecord, bool) {
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
	}
	return RootRecord{}, false
}

func recordIndex(records []RootRecord, id string) int {
	for index := range records {
		if records[index].ID == id {
			return index
		}
	}
	return -1
}

func containsState(states []RecordState, state RecordState) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func validState(state RecordState) bool {
	switch state {
	case StateLegacyPRDReserved, StatePrepared, StateActive, StateSuperseded, StateRetired:
		return true
	default:
		return false
	}
}

func validLane(lane config.ReleaseLane) bool {
	return lane == config.ReleaseLaneDev || lane == config.ReleaseLaneQAS || lane == config.ReleaseLanePRD
}

func validIdentity(identity laneroot.FileIdentity) bool {
	return identity.Device != 0 && identity.Inode != 0
}

func requiresAPFS(host config.HostKind, lane config.ReleaseLane) bool {
	return host == config.HostKindMac && (lane == config.ReleaseLaneDev || lane == config.ReleaseLaneQAS)
}

func validVolume(volume laneroot.VolumeIdentity) bool {
	return canonicalPath(volume.Mountpoint) && volume.Filesystem == "apfs" &&
		volumeUUIDPattern.MatchString(volume.UUID) && volume.Device > 0 && volume.QuotaBytes > 0
}

var volumeUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func canonicalPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func sameOrNestedPath(left, right string) bool {
	if left == right {
		return true
	}
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func sortAncillary(entries []laneroot.AncillaryEntrySnapshot) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}

func canonicalJSON(snapshot Snapshot) ([]byte, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, ErrInvalidRegistry
	}
	return append(data, '\n'), nil
}

func validPrivateDirectory(stat unix.Stat_t, ownerUID uint32) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Uid == ownerUID && stat.Mode&0o7777 == 0o700
}

func validPrivateRegularFile(stat unix.Stat_t, ownerUID uint32) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Uid == ownerUID && stat.Mode&0o7777 == 0o600
}

func validPrivateRegularInfo(info os.FileInfo, ownerUID uint32) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == ownerUID && uint32(stat.Mode)&0o7777 == 0o600
}

const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

func openAbsoluteDirectory(path string, ownerUID uint32) (int, error) {
	if !canonicalPath(path) {
		return -1, ErrInvalidRegistry
	}
	fd, err := openSafeDirectory(path, ownerUID)
	if err != nil {
		return -1, err
	}
	stat, err := statFD(fd)
	if err != nil || !validPrivateDirectory(stat, ownerUID) {
		unix.Close(fd)
		return -1, ErrInvalidRegistry
	}
	return fd, nil
}

func openSafeDirectory(path string, ownerUID uint32) (int, error) {
	if !canonicalPath(path) {
		return -1, ErrInvalidRegistry
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	fd, err := unix.Open(string(filepath.Separator), directoryFlags, 0)
	if err != nil {
		return -1, ErrInvalidRegistry
	}
	if err := validRegistryAncestor(fd, ownerUID); err != nil {
		unix.Close(fd)
		return -1, err
	}
	for _, part := range parts {
		next, err := unix.Openat(fd, part, directoryFlags, 0)
		if err != nil {
			unix.Close(fd)
			return -1, ErrInvalidRegistry
		}
		if err := validRegistryAncestor(next, ownerUID); err != nil {
			unix.Close(next)
			unix.Close(fd)
			return -1, err
		}
		if err := unix.Close(fd); err != nil {
			unix.Close(next)
			return -1, ErrInvalidRegistry
		}
		fd = next
	}
	return fd, nil
}

func openParentDirectory(path string, ownerUID uint32) (int, string, error) {
	parent, leaf := filepath.Dir(path), filepath.Base(path)
	fd, err := openSafeDirectory(parent, ownerUID)
	if err != nil {
		return -1, "", err
	}
	return fd, leaf, nil
}

func validRegistryAncestor(fd int, ownerUID uint32) error {
	stat, err := statFD(fd)
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || (stat.Uid != 0 && stat.Uid != ownerUID) {
		return ErrInvalidRegistry
	}
	writable := stat.Mode & 0o022
	rootStickyTemp := stat.Uid == 0 && stat.Mode&0o1777 == 0o1777 && writable == 0o022
	if writable != 0 && !rootStickyTemp {
		return ErrInvalidRegistry
	}
	return nil
}

func statFD(fd int) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func identity(stat unix.Stat_t) laneroot.FileIdentity {
	return laneroot.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}
}
