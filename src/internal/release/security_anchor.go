package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	securityAnchorSchemaVersion = 1
	securityAnchorDBName        = "anchor.sqlite"
	securityAnchorDirMode       = 0o700
	securityAnchorDBMode        = 0o600
)

var (
	ErrSecurityAnchorUnavailable = errors.New("security anchor unavailable")
	ErrSecurityAnchorRollback    = errors.New("security anchor rollback or fork detected")
	ErrSecurityAnchorInitialized = errors.New("security anchor is already initialized")
)

// SecurityAnchorBootstrap is the explicit trust material installed by the
// separately approved host bootstrap. It is accepted only when creating a new
// anchor; opening an existing anchor never replaces or imports trust state.
type SecurityAnchorBootstrap struct {
	HostID                  string
	AuthorityKey            ReleaseKeyState
	HostAttestationKey      ReleaseKeyState
	BootstrapLedgerAnchorID string
}

// AnchorDirectiveHighWater is the helper-verified identity of the highest
// directive observed for one lane on this host.
type AnchorDirectiveHighWater struct {
	Lane                string `json:"lane"`
	HostID              string `json:"host_id"`
	TransitionID        string `json:"lane_transition_id"`
	TransitionEpoch     uint64 `json:"transition_epoch"`
	AdmissionGeneration uint8  `json:"admission_generation"`
	LeaseGeneration     uint64 `json:"lease_generation"`
	PredecessorRecordID string `json:"predecessor_record_id,omitempty"`
	Role                string `json:"directive_role"`
	CandidateManifestID string `json:"candidate_manifest_id"`
	ConfigurationDigest string `json:"resolved_configuration_digest"`
	RecordID            string `json:"record_id"`
}

// AnchorRuntimeContext records the root-installed runtime identity. It is
// carried inside the protected snapshot and is not read from a continuity
// bundle.
type AnchorRuntimeContext struct {
	Lane                string `json:"lane"`
	HostID              string `json:"host_id"`
	TransitionID        string `json:"lane_transition_id"`
	TransitionEpoch     uint64 `json:"transition_epoch"`
	CandidateManifestID string `json:"candidate_manifest_id"`
	ConfigurationDigest string `json:"resolved_configuration_digest"`
	RecordID            string `json:"record_id"`
}

type AnchorPendingProbe struct {
	Challenge             TimeProbeChallenge `json:"challenge"`
	WallStartedAt         time.Time          `json:"wall_started_at"`
	MonotonicStartedNanos uint64             `json:"monotonic_started_nanos"`
}

// SecurityAnchorState is the single durable high-water snapshot protected by
// the root-owned host helper. Generation is a local storage CAS counter; the
// trust generations below are signed protocol generations.
type SecurityAnchorState struct {
	SchemaVersion           int                                 `json:"schema_version"`
	Generation              uint64                              `json:"generation"`
	HostID                  string                              `json:"host_id"`
	AuthorityKeys           []ReleaseKeyState                   `json:"authority_keys"`
	HostAttestationKeys     []ReleaseKeyState                   `json:"host_attestation_keys"`
	RevocationLedger        RevocationLedgerState               `json:"revocation_ledger"`
	HighestAuthorityTime    time.Time                           `json:"highest_authority_time,omitempty"`
	ProbeGeneration         uint64                              `json:"probe_generation"`
	ConsumedProbeGeneration uint64                              `json:"consumed_probe_generation"`
	PendingProbes           map[string]AnchorPendingProbe       `json:"pending_probes,omitempty"`
	DirectiveHighWater      map[string]AnchorDirectiveHighWater `json:"directive_high_water"`
	ActiveRuntime           map[string]AnchorRuntimeContext     `json:"active_runtime"`
	PendingCandidate        map[string]AnchorRuntimeContext     `json:"pending_candidate"`
}

type anchorCommit struct {
	generation uint64
	previous   string
	digest     string
	state      []byte
}

type anchorFaultHooks struct {
	beforeCommit func() error
	afterCommit  func() error
}

// SecurityAnchor opens only the platform-fixed production location. The
// package-private openSecurityAnchorAt constructor exists for isolated tests.
type SecurityAnchor struct {
	mu        sync.Mutex
	dir       string
	db        *sql.DB
	conn      *sql.Conn
	state     SecurityAnchorState
	digest    string
	ownerUID  uint32
	now       func() time.Time
	probes    *TimeProbeManager
	faults    anchorFaultHooks
	unhealthy bool
}

// OpenSecurityAnchor opens the already bootstrapped fixed host anchor. The
// helper must run as root; caller-selected paths are intentionally not exposed.
func OpenSecurityAnchor() (*SecurityAnchor, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("%w: helper is not root", ErrSecurityAnchorUnavailable)
	}
	dir, err := fixedSecurityAnchorDirectory()
	if err != nil {
		return nil, err
	}
	return openSecurityAnchorAt(dir, nil, 0, time.Now)
}

// InitializeSecurityAnchor creates the fixed production anchor exactly once
// from the pinned bootstrap trust package. It never repairs, replaces, or
// imports an existing database.
func InitializeSecurityAnchor(bootstrap SecurityAnchorBootstrap) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: bootstrap helper is not root", ErrSecurityAnchorUnavailable)
	}
	dir, err := fixedSecurityAnchorDirectory()
	if err != nil {
		return err
	}
	anchor, err := openSecurityAnchorAt(dir, &bootstrap, 0, time.Now)
	if err != nil {
		return err
	}
	return anchor.Close()
}

func fixedSecurityAnchorDirectory() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/RemoteSessionRunner-security-anchor", nil
	case "linux":
		return "/var/lib/remote-session-runner-security-anchor", nil
	default:
		return "", fmt.Errorf("%w: unsupported operating system %q", ErrSecurityAnchorUnavailable, runtime.GOOS)
	}
}

func openSecurityAnchorAt(dir string, bootstrap *SecurityAnchorBootstrap, ownerUID uint32, now func() time.Time) (*SecurityAnchor, error) {
	if now == nil || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, fmt.Errorf("%w: invalid anchor location or clock", ErrSecurityAnchorUnavailable)
	}
	if bootstrap != nil {
		if err := validateAnchorBootstrap(*bootstrap); err != nil {
			return nil, err
		}
		if err := os.Mkdir(dir, securityAnchorDirMode); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create security anchor directory: %w", err)
		}
	}
	if err := verifyOwnedDirectory(dir, ownerUID); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, securityAnchorDBName)
	_, statErr := os.Lstat(dbPath)
	if bootstrap != nil && statErr == nil {
		return nil, ErrSecurityAnchorInitialized
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if bootstrap == nil {
			return nil, fmt.Errorf("%w: anchor database is absent", ErrSecurityAnchorUnavailable)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			return nil, fmt.Errorf("%w: bootstrap directory is not empty: %v", ErrSecurityAnchorUnavailable, err)
		}
		if err := createAnchorDatabase(dbPath, *bootstrap, ownerUID, dir); err != nil {
			return nil, err
		}
	} else if statErr != nil {
		return nil, fmt.Errorf("inspect security anchor database: %w", statErr)
	}
	if err := verifyAnchorDirectoryEntries(dir, ownerUID); err != nil {
		return nil, err
	}
	if err := verifyOwnedRegularFile(dbPath, ownerUID, securityAnchorDBMode); err != nil {
		return nil, err
	}

	dataSource, err := anchorDataSource(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dataSource)
	if err != nil {
		return nil, fmt.Errorf("open security anchor database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("connect security anchor database: %w", err)
	}
	if err := verifyAnchorPragmas(context.Background(), conn); err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	state, digest, err := readAnchorCommit(context.Background(), conn)
	if err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	if state.HostID == "" {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("%w: empty host anchor", ErrSecurityAnchorUnavailable)
	}
	anchor := &SecurityAnchor{
		dir: dir, db: db, conn: conn, state: state, digest: digest, ownerUID: ownerUID, now: now,
		probes: newTimeProbeManager(now),
	}
	anchor.probes.highestAuthorityTime = state.HighestAuthorityTime
	// Challenges are process-bound because monotonic start timestamps cannot be
	// restored safely after restart. Remove any durably published but unconsumed
	// challenges before the helper serves requests again.
	if len(state.PendingProbes) > 0 {
		if _, err := anchor.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
			next.PendingProbes = map[string]AnchorPendingProbe{}
			return nil
		}); err != nil {
			anchor.Close()
			return nil, fmt.Errorf("invalidate stale time challenges: %w", err)
		}
	}
	return anchor, nil
}

func validateAnchorBootstrap(bootstrap SecurityAnchorBootstrap) error {
	now := time.Now().UTC()
	if !isCanonicalHostID(bootstrap.HostID) || bootstrap.AuthorityKey.HostID != "" || bootstrap.AuthorityKey.Generation != 0 ||
		bootstrap.HostAttestationKey.HostID != bootstrap.HostID || bootstrap.HostAttestationKey.Generation != 0 ||
		!validKeyState(bootstrap.AuthorityKey, now) || !validKeyState(bootstrap.HostAttestationKey, now) ||
		!isLowerHexDigest(bootstrap.BootstrapLedgerAnchorID) {
		return fmt.Errorf("%w: invalid pinned bootstrap trust", ErrInvalidTrustState)
	}
	return nil
}

func createAnchorDatabase(dbPath string, bootstrap SecurityAnchorBootstrap, ownerUID uint32, dir string) error {
	file, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, securityAnchorDBMode)
	if err != nil {
		return fmt.Errorf("create security anchor database: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close new security anchor database: %w", err)
	}
	if err := os.Chmod(dbPath, securityAnchorDBMode); err != nil {
		return fmt.Errorf("set security anchor database mode: %w", err)
	}
	if err := verifyOwnedRegularFile(dbPath, ownerUID, securityAnchorDBMode); err != nil {
		return err
	}
	dataSource, err := anchorDataSource(dbPath)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dataSource)
	if err != nil {
		return fmt.Errorf("initialize security anchor database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("connect new security anchor database: %w", err)
	}
	defer conn.Close()
	if err := configureAnchorPragmas(context.Background(), conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(context.Background(), `
CREATE TABLE anchor_state (
	 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	 generation INTEGER NOT NULL,
	 previous_digest TEXT NOT NULL,
	 state_digest TEXT NOT NULL,
	 state_json BLOB NOT NULL
);
CREATE TABLE anchor_commits (
	 generation INTEGER PRIMARY KEY,
	 previous_digest TEXT NOT NULL,
	 state_digest TEXT NOT NULL
);`); err != nil {
		return fmt.Errorf("create security anchor schema: %w", err)
	}
	state := bootstrapState(bootstrap)
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	digest := anchorCommitDigest(0, "", encoded)
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin anchor bootstrap: %w", err)
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO anchor_state(singleton,generation,previous_digest,state_digest,state_json) VALUES(1,0,'',?,?)", digest, encoded); err != nil {
		tx.Rollback()
		return fmt.Errorf("write anchor bootstrap state: %w", err)
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO anchor_commits(generation,previous_digest,state_digest) VALUES(0,'',?)", digest); err != nil {
		tx.Rollback()
		return fmt.Errorf("write anchor bootstrap journal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit anchor bootstrap: %w", err)
	}
	if err := syncAnchorFiles(dbPath, dir); err != nil {
		return fmt.Errorf("sync anchor bootstrap: %w", err)
	}
	return nil
}

func bootstrapState(bootstrap SecurityAnchorBootstrap) SecurityAnchorState {
	return SecurityAnchorState{
		SchemaVersion:       securityAnchorSchemaVersion,
		HostID:              bootstrap.HostID,
		AuthorityKeys:       []ReleaseKeyState{cloneReleaseKeyState(bootstrap.AuthorityKey)},
		HostAttestationKeys: []ReleaseKeyState{cloneReleaseKeyState(bootstrap.HostAttestationKey)},
		RevocationLedger:    RevocationLedgerState{RecordID: bootstrap.BootstrapLedgerAnchorID, RevokedRecordIDs: []string{}, RevokedKeyIDs: []string{}},
		PendingProbes:       map[string]AnchorPendingProbe{},
		DirectiveHighWater:  map[string]AnchorDirectiveHighWater{},
		ActiveRuntime:       map[string]AnchorRuntimeContext{},
		PendingCandidate:    map[string]AnchorRuntimeContext{},
	}
}

func anchorDataSource(path string) (string, error) {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "DELETE")
	query.Set("_synchronous", "EXTRA")
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

func configureAnchorPragmas(ctx context.Context, conn *sql.Conn) error {
	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil || !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("%w: journal_mode DELETE: %v (%q)", ErrSecurityAnchorUnavailable, err, mode)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=EXTRA"); err != nil {
		return fmt.Errorf("%w: set full-sync mode: %v", ErrSecurityAnchorUnavailable, err)
	}
	return verifyAnchorPragmas(ctx, conn)
}

func verifyAnchorPragmas(ctx context.Context, conn *sql.Conn) error {
	var journal string
	var synchronous int
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("read anchor journal mode: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("read anchor synchronous mode: %w", err)
	}
	if !strings.EqualFold(journal, "delete") || synchronous != 3 {
		return fmt.Errorf("%w: unsafe SQLite durability settings (journal=%q synchronous=%d)", ErrSecurityAnchorUnavailable, journal, synchronous)
	}
	return nil
}

func verifyOwnedDirectory(path string, ownerUID uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect security anchor directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != securityAnchorDirMode {
		return fmt.Errorf("%w: anchor directory must be a real 0700 directory", ErrSecurityAnchorUnavailable)
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != ownerUID {
		return fmt.Errorf("%w: anchor directory owner mismatch", ErrSecurityAnchorUnavailable)
	}
	return nil
}

func verifyOwnedRegularFile(path string, ownerUID uint32, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect security anchor file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return fmt.Errorf("%w: anchor database must be a regular %04o file", ErrSecurityAnchorUnavailable, mode)
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != ownerUID {
		return fmt.Errorf("%w: anchor database owner mismatch", ErrSecurityAnchorUnavailable)
	}
	return nil
}

func verifyAnchorDirectoryEntries(dir string, ownerUID uint32) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read security anchor directory: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		switch entry.Name() {
		case securityAnchorDBName:
			if err := verifyOwnedRegularFile(path, ownerUID, securityAnchorDBMode); err != nil {
				return err
			}
		case securityAnchorDBName + "-journal":
			if err := verifyOwnedRegularFile(path, ownerUID, securityAnchorDBMode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unexpected entry in protected anchor directory: %q", ErrSecurityAnchorUnavailable, entry.Name())
		}
	}
	return nil
}

func fileOwnerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

func syncAnchorFiles(dbPath, dir string) error {
	dbFile, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := dbFile.Sync()
	closeErr := dbFile.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr = dirFile.Sync()
	closeErr = dirFile.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func readAnchorCommit(ctx context.Context, conn *sql.Conn) (SecurityAnchorState, string, error) {
	var generation uint64
	var previous, digest string
	var encoded []byte
	if err := conn.QueryRowContext(ctx, "SELECT generation,previous_digest,state_digest,state_json FROM anchor_state WHERE singleton=1").Scan(&generation, &previous, &digest, &encoded); err != nil {
		return SecurityAnchorState{}, "", fmt.Errorf("read anchor state: %w", err)
	}
	if anchorCommitDigest(generation, previous, encoded) != digest {
		return SecurityAnchorState{}, "", fmt.Errorf("%w: state digest mismatch", ErrSecurityAnchorRollback)
	}
	var state SecurityAnchorState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return SecurityAnchorState{}, "", fmt.Errorf("decode anchor state: %w", err)
	}
	if state.Generation != generation || validateSecurityAnchorState(state) != nil {
		return SecurityAnchorState{}, "", fmt.Errorf("%w: invalid state snapshot", ErrSecurityAnchorRollback)
	}
	var commitCount uint64
	var firstGeneration, lastGeneration uint64
	if err := conn.QueryRowContext(ctx, "SELECT count(*),min(generation),max(generation) FROM anchor_commits").Scan(&commitCount, &firstGeneration, &lastGeneration); err != nil || commitCount != generation+1 || firstGeneration != 0 || lastGeneration != generation {
		return SecurityAnchorState{}, "", fmt.Errorf("%w: commit journal is incomplete: %v", ErrSecurityAnchorRollback, err)
	}
	rows, err := conn.QueryContext(ctx, "SELECT generation,previous_digest,state_digest FROM anchor_commits ORDER BY generation")
	if err != nil {
		return SecurityAnchorState{}, "", fmt.Errorf("read anchor commit journal: %w", err)
	}
	defer rows.Close()
	wantPrevious := ""
	lastPrevious := ""
	for expected := uint64(0); rows.Next(); expected++ {
		var gotGeneration uint64
		var gotPrevious, gotDigest string
		if err := rows.Scan(&gotGeneration, &gotPrevious, &gotDigest); err != nil || gotGeneration != expected || gotPrevious != wantPrevious {
			return SecurityAnchorState{}, "", fmt.Errorf("%w: commit journal chain mismatch", ErrSecurityAnchorRollback)
		}
		lastPrevious = gotPrevious
		wantPrevious = gotDigest
	}
	if err := rows.Err(); err != nil || wantPrevious != digest || previous != lastPrevious {
		return SecurityAnchorState{}, "", fmt.Errorf("%w: final commit digest mismatch: %v", ErrSecurityAnchorRollback, err)
	}
	return state, digest, nil
}

func validateSecurityAnchorState(state SecurityAnchorState) error {
	if state.SchemaVersion != securityAnchorSchemaVersion || !isCanonicalHostID(state.HostID) || len(state.AuthorityKeys) == 0 || len(state.HostAttestationKeys) == 0 ||
		state.ConsumedProbeGeneration > state.ProbeGeneration || !validRevocationLedgerState(state.RevocationLedger) ||
		(state.HighestAuthorityTime.IsZero() && state.ProbeGeneration != 0) || (!state.HighestAuthorityTime.IsZero() && !state.HighestAuthorityTime.Equal(state.HighestAuthorityTime.UTC())) {
		return ErrInvalidTrustState
	}
	if err := validateKeyHistory(state.AuthorityKeys, ""); err != nil {
		return err
	}
	if err := validateKeyHistory(state.HostAttestationKeys, state.HostID); err != nil {
		return err
	}
	for id, pending := range state.PendingProbes {
		if id != pending.Challenge.ID || !validChallengeID(id) || !validNonce(pending.Challenge.Nonce) || pending.WallStartedAt.IsZero() ||
			!pending.WallStartedAt.Equal(pending.WallStartedAt.UTC()) || pending.MonotonicStartedNanos == 0 {
			return ErrInvalidTrustState
		}
	}
	for lane, value := range state.DirectiveHighWater {
		if lane != value.Lane || !validAnchorLane(lane) || value.HostID != state.HostID || !validAnchorDirective(value) {
			return ErrInvalidTrustState
		}
	}
	for lane, value := range state.ActiveRuntime {
		if lane != value.Lane || value.HostID != state.HostID || !validAnchorRuntime(value) {
			return ErrInvalidTrustState
		}
	}
	for lane, value := range state.PendingCandidate {
		if lane != value.Lane || value.HostID != state.HostID || !validAnchorRuntime(value) {
			return ErrInvalidTrustState
		}
	}
	return nil
}

func validateKeyHistory(history []ReleaseKeyState, hostID string) error {
	seenRecords := map[string]struct{}{}
	seenKeys := map[string]struct{}{}
	for index, key := range history {
		if key.HostID != hostID || key.Generation != uint64(index) || !validKeyID(key.KeyID) || len(key.PublicKey) != ed25519.PublicKeySize ||
			!isLowerHexDigest(key.RecordID) || key.NotBefore.IsZero() || !key.NotBefore.Equal(key.NotBefore.UTC()) ||
			key.NotAfter.IsZero() || !key.NotAfter.Equal(key.NotAfter.UTC()) || !key.NotAfter.After(key.NotBefore) {
			return ErrInvalidTrustState
		}
		if _, ok := seenRecords[key.RecordID]; ok {
			return ErrInvalidTrustState
		}
		if _, ok := seenKeys[key.KeyID]; ok {
			return ErrInvalidTrustState
		}
		seenRecords[key.RecordID] = struct{}{}
		seenKeys[key.KeyID] = struct{}{}
		if index > 0 {
			previous := history[index-1]
			if !previous.NotAfter.After(key.NotBefore) || !previous.NotBefore.Before(key.NotAfter) {
				return ErrInvalidTrustState
			}
		}
	}
	return nil
}

func validAnchorLane(lane string) bool { return lane == "dev" || lane == "qas" || lane == "prd" }

func validAnchorDirective(value AnchorDirectiveHighWater) bool {
	return validAnchorLane(value.Lane) && isCanonicalHostID(value.HostID) && isUUIDv7(value.TransitionID) && value.TransitionEpoch > 0 &&
		value.AdmissionGeneration <= 2 && value.LeaseGeneration > 0 && (value.PredecessorRecordID == "" || isLowerHexDigest(value.PredecessorRecordID)) &&
		(value.Role == "active_runtime_closure" || value.Role == "candidate_admission") &&
		isLowerHexDigest(value.CandidateManifestID) && isLowerHexDigest(value.ConfigurationDigest) && isLowerHexDigest(value.RecordID) &&
		(value.Role != "active_runtime_closure" || value.AdmissionGeneration == 0)
}

func validAnchorRuntime(value AnchorRuntimeContext) bool {
	return validAnchorLane(value.Lane) && isCanonicalHostID(value.HostID) && isUUIDv7(value.TransitionID) && value.TransitionEpoch > 0 &&
		isLowerHexDigest(value.CandidateManifestID) && isLowerHexDigest(value.ConfigurationDigest) && isLowerHexDigest(value.RecordID)
}

func isUUIDv7(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '7' {
		return false
	}
	if !strings.ContainsRune("89ab", rune(value[19])) {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func cloneSecurityAnchorState(state SecurityAnchorState) (SecurityAnchorState, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return SecurityAnchorState{}, err
	}
	var clone SecurityAnchorState
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return SecurityAnchorState{}, err
	}
	return clone, nil
}

func cloneReleaseKeyState(value ReleaseKeyState) ReleaseKeyState {
	value.PublicKey = append(ed25519.PublicKey(nil), value.PublicKey...)
	value.NotBefore = value.NotBefore.UTC()
	value.NotAfter = value.NotAfter.UTC()
	return value
}

func anchorCommitDigest(generation uint64, previous string, state []byte) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "remote-session-runner/security-anchor/v1\x00%d\x00%s\x00", generation, previous)
	hash.Write(state)
	return hex.EncodeToString(hash.Sum(nil))
}

// Snapshot returns an isolated copy. No caller can mutate the live in-memory
// high-water state through a returned slice or map.
func (a *SecurityAnchor) Snapshot() (SecurityAnchorState, error) {
	if a == nil {
		return SecurityAnchorState{}, ErrSecurityAnchorUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy {
		return SecurityAnchorState{}, ErrSecurityAnchorUnavailable
	}
	return cloneSecurityAnchorState(a.state)
}

func (a *SecurityAnchor) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var connErr, dbErr error
	if a.conn != nil {
		connErr = a.conn.Close()
		a.conn = nil
	}
	if a.db != nil {
		dbErr = a.db.Close()
		a.db = nil
	}
	return errors.Join(connErr, dbErr)
}

func (a *SecurityAnchor) commitLocked(ctx context.Context, mutate func(*SecurityAnchorState) error) (SecurityAnchorState, error) {
	if a.unhealthy || a.conn == nil {
		return SecurityAnchorState{}, ErrSecurityAnchorUnavailable
	}
	dbPath := filepath.Join(a.dir, securityAnchorDBName)
	if err := verifyOwnedDirectory(a.dir, a.ownerUID); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, err
	}
	if err := verifyAnchorDirectoryEntries(a.dir, a.ownerUID); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, err
	}
	if err := verifyOwnedRegularFile(dbPath, a.ownerUID, securityAnchorDBMode); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, err
	}
	next, err := cloneSecurityAnchorState(a.state)
	if err != nil {
		return SecurityAnchorState{}, err
	}
	if err := mutate(&next); err != nil {
		return SecurityAnchorState{}, err
	}
	if err := validateAnchorMonotonicTransition(a.state, next); err != nil {
		return SecurityAnchorState{}, fmt.Errorf("validate anchor monotonicity: %w", err)
	}
	if a.state.Generation >= uint64(math.MaxInt64) {
		return SecurityAnchorState{}, ErrSecurityAnchorRollback
	}
	next.Generation = a.state.Generation + 1
	encoded, err := json.Marshal(next)
	if err != nil {
		return SecurityAnchorState{}, err
	}
	digest := anchorCommitDigest(next.Generation, a.digest, encoded)
	tx, err := a.conn.BeginTx(ctx, nil)
	if err != nil {
		return SecurityAnchorState{}, fmt.Errorf("begin anchor transaction: %w", err)
	}
	rollback := func(cause error) (SecurityAnchorState, error) {
		_ = tx.Rollback()
		return SecurityAnchorState{}, cause
	}
	result, err := tx.ExecContext(ctx, "UPDATE anchor_state SET generation=?,previous_digest=?,state_digest=?,state_json=? WHERE singleton=1 AND generation=? AND state_digest=?", next.Generation, a.digest, digest, encoded, a.state.Generation, a.digest)
	if err != nil {
		return rollback(fmt.Errorf("write anchor transaction: %w", err))
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return rollback(fmt.Errorf("%w: anchor compare-and-swap failed", ErrSecurityAnchorRollback))
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO anchor_commits(generation,previous_digest,state_digest) VALUES(?,?,?)", next.Generation, a.digest, digest); err != nil {
		return rollback(fmt.Errorf("append anchor commit chain: %w", err))
	}
	if a.faults.beforeCommit != nil {
		if err := a.faults.beforeCommit(); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, fmt.Errorf("commit anchor state: %w", err)
	}
	if a.faults.afterCommit != nil {
		if err := a.faults.afterCommit(); err != nil {
			a.unhealthy = true
			return SecurityAnchorState{}, err
		}
	}
	if err := syncAnchorFiles(dbPath, a.dir); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, fmt.Errorf("sync accepted anchor state: %w", err)
	}
	if err := verifyOwnedDirectory(a.dir, a.ownerUID); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, err
	}
	if err := verifyAnchorDirectoryEntries(a.dir, a.ownerUID); err != nil {
		a.unhealthy = true
		return SecurityAnchorState{}, err
	}
	a.state = next
	a.digest = digest
	return cloneSecurityAnchorState(next)
}

func validateAnchorMonotonicTransition(before, after SecurityAnchorState) error {
	if before.SchemaVersion != after.SchemaVersion || before.HostID != after.HostID || validateSecurityAnchorState(after) != nil ||
		after.Generation != before.Generation || after.ProbeGeneration < before.ProbeGeneration || after.ConsumedProbeGeneration < before.ConsumedProbeGeneration ||
		after.HighestAuthorityTime.Before(before.HighestAuthorityTime) {
		return ErrSecurityAnchorRollback
	}
	if !prefixKeyHistory(before.AuthorityKeys, after.AuthorityKeys) || !prefixKeyHistory(before.HostAttestationKeys, after.HostAttestationKeys) ||
		!ledgerIsSuccessorOrSame(before.RevocationLedger, after.RevocationLedger) {
		return ErrSecurityAnchorRollback
	}
	for lane, old := range before.DirectiveHighWater {
		current, ok := after.DirectiveHighWater[lane]
		if !ok || compareAnchorDirective(current, old) < 0 {
			return ErrSecurityAnchorRollback
		}
	}
	for lane, old := range before.ActiveRuntime {
		current, ok := after.ActiveRuntime[lane]
		if !ok || current.TransitionEpoch < old.TransitionEpoch ||
			(current.TransitionEpoch == old.TransitionEpoch && current != old) {
			return ErrSecurityAnchorRollback
		}
	}
	for lane, old := range before.PendingCandidate {
		current, ok := after.PendingCandidate[lane]
		if !ok {
			active, activated := after.ActiveRuntime[lane]
			if !activated || active.TransitionEpoch != old.TransitionEpoch || active.TransitionID != old.TransitionID ||
				active.CandidateManifestID != old.CandidateManifestID || active.ConfigurationDigest != old.ConfigurationDigest {
				return ErrSecurityAnchorRollback
			}
			continue
		}
		if current.TransitionEpoch < old.TransitionEpoch || (current.TransitionEpoch == old.TransitionEpoch &&
			(current.TransitionID != old.TransitionID || current.CandidateManifestID != old.CandidateManifestID || current.ConfigurationDigest != old.ConfigurationDigest)) {
			return ErrSecurityAnchorRollback
		}
	}
	return nil
}

func prefixKeyHistory(before, after []ReleaseKeyState) bool {
	if len(after) < len(before) {
		return false
	}
	for index := range before {
		old, current := before[index], after[index]
		if old.Generation != current.Generation || old.HostID != current.HostID || old.RecordID != current.RecordID || old.KeyID != current.KeyID ||
			!bytes.Equal(old.PublicKey, current.PublicKey) || !old.NotBefore.Equal(current.NotBefore) || current.NotAfter.After(old.NotAfter) {
			return false
		}
	}
	return true
}

func ledgerIsSuccessorOrSame(before, after RevocationLedgerState) bool {
	if after.Generation < before.Generation || after.RecordID == "" || after.RecordID == before.RecordID && after.Generation != before.Generation {
		return false
	}
	if after.Generation == before.Generation {
		return after.RecordID == before.RecordID && slices.Equal(after.RevokedRecordIDs, before.RevokedRecordIDs) && slices.Equal(after.RevokedKeyIDs, before.RevokedKeyIDs) &&
			after.IssuedAt.Equal(before.IssuedAt) && after.ExpiresAt.Equal(before.ExpiresAt)
	}
	if after.Generation != before.Generation+1 {
		return false
	}
	for _, recordID := range before.RevokedRecordIDs {
		if !slices.Contains(after.RevokedRecordIDs, recordID) {
			return false
		}
	}
	for _, keyID := range before.RevokedKeyIDs {
		if !slices.Contains(after.RevokedKeyIDs, keyID) {
			return false
		}
	}
	return true
}

func compareAnchorDirective(left, right AnchorDirectiveHighWater) int {
	if left.TransitionEpoch != right.TransitionEpoch {
		if left.TransitionEpoch < right.TransitionEpoch {
			return -1
		}
		return 1
	}
	if left.AdmissionGeneration != right.AdmissionGeneration {
		if left.AdmissionGeneration < right.AdmissionGeneration {
			return -1
		}
		return 1
	}
	if left.LeaseGeneration != right.LeaseGeneration {
		if left.LeaseGeneration < right.LeaseGeneration {
			return -1
		}
		return 1
	}
	if left.TransitionID == right.TransitionID && left.Role == right.Role && left.CandidateManifestID == right.CandidateManifestID && left.ConfigurationDigest == right.ConfigurationDigest && left.RecordID == right.RecordID {
		return 0
	}
	return -1
}

func (a *SecurityAnchor) BeginAuthorityTimeProbe() (TimeProbeChallenge, error) {
	if a == nil {
		return TimeProbeChallenge{}, ErrSecurityAnchorUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy {
		return TimeProbeChallenge{}, ErrSecurityAnchorUnavailable
	}
	if a.state.ProbeGeneration != a.state.ConsumedProbeGeneration {
		return TimeProbeChallenge{}, invalidL3Trust("previous accepted time probe has not been consumed by one anchor update")
	}
	if len(a.state.PendingProbes) != 0 {
		return TimeProbeChallenge{}, invalidL3Trust("an outstanding helper time challenge already exists")
	}
	monotonicT0, err := anchorMonotonicNanos()
	if err != nil {
		return TimeProbeChallenge{}, fmt.Errorf("read helper monotonic start time: %w", err)
	}
	challenge, err := a.probes.Begin()
	if err != nil {
		return TimeProbeChallenge{}, err
	}
	a.probes.mu.Lock()
	pendingProbe, exists := a.probes.pending[challenge.ID]
	a.probes.mu.Unlock()
	if !exists {
		return TimeProbeChallenge{}, ErrSecurityAnchorUnavailable
	}
	_, err = a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
		if next.PendingProbes == nil {
			next.PendingProbes = map[string]AnchorPendingProbe{}
		}
		if _, exists := next.PendingProbes[challenge.ID]; exists {
			return ErrInvalidTrustState
		}
		next.PendingProbes[challenge.ID] = AnchorPendingProbe{
			Challenge: challenge, WallStartedAt: pendingProbe.started.Round(0).UTC(), MonotonicStartedNanos: monotonicT0,
		}
		return nil
	})
	if err != nil {
		a.probes = newTimeProbeManager(a.now)
		a.probes.highestAuthorityTime = a.state.HighestAuthorityTime
		return TimeProbeChallenge{}, err
	}
	return challenge, nil
}

func anchorMonotonicNanos() (uint64, error) {
	var value unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &value); err != nil {
		return 0, err
	}
	if value.Sec < 0 || value.Nsec < 0 || value.Nsec >= int64(time.Second) || value.Sec > (math.MaxInt64-value.Nsec)/int64(time.Second) {
		return 0, ErrInvalidTrustState
	}
	return uint64(value.Sec*int64(time.Second) + value.Nsec), nil
}

func (a *SecurityAnchor) AcceptAuthorityTimeProbe(challengeID string, responseRaw []byte) (time.Time, error) {
	if a == nil {
		return time.Time{}, ErrSecurityAnchorUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy {
		return time.Time{}, ErrSecurityAnchorUnavailable
	}
	pending, ok := a.state.PendingProbes[challengeID]
	if !ok {
		return time.Time{}, invalidL3Trust("missing, restarted, or replayed persisted time challenge")
	}
	if a.state.ProbeGeneration != a.state.ConsumedProbeGeneration {
		return time.Time{}, invalidL3Trust("a prior accepted helper time probe has not been consumed")
	}
	trusted := anchorAuthorityProbeKeys(a.state)
	claimedTime := time.Time{}
	var response signedTimeProbeResponse
	if json.Unmarshal(responseRaw, &response) == nil && response.AuthorityUTCUnixMillis > 0 {
		claimedTime = time.UnixMilli(response.AuthorityUTCUnixMillis).UTC()
	}
	if claimedTime.IsZero() || !validAnchorTimeProbeSigner(a.state, responseRaw, claimedTime) {
		a.probes.discard(challengeID)
		_, consumeErr := a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
			delete(next.PendingProbes, challengeID)
			return nil
		})
		return time.Time{}, errors.Join(invalidL3Trust("time probe signer is not anchored at its claimed authority time"), consumeErr)
	}
	accepted, err := a.probes.Accept(challengeID, responseRaw, trusted)
	if err != nil {
		_, consumeErr := a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
			delete(next.PendingProbes, challengeID)
			return nil
		})
		return time.Time{}, errors.Join(err, consumeErr)
	}
	_, err = a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
		if current, exists := next.PendingProbes[challengeID]; !exists || current != pending || accepted.Before(next.HighestAuthorityTime) || next.ProbeGeneration == ^uint64(0) {
			return ErrSecurityAnchorRollback
		}
		delete(next.PendingProbes, challengeID)
		next.HighestAuthorityTime = accepted.UTC()
		next.ProbeGeneration++
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return accepted.UTC(), nil
}

func anchorAuthorityProbeKeys(state SecurityAnchorState) map[string]TrustedKey {
	trusted := make(map[string]TrustedKey, len(state.AuthorityKeys))
	for _, key := range state.AuthorityKeys {
		if slices.Contains(state.RevocationLedger.RevokedKeyIDs, key.KeyID) {
			continue
		}
		trusted[key.KeyID] = TrustedKey{KeyID: key.KeyID, Role: SignerRoleReleaseAuthority, PublicKey: append(ed25519.PublicKey(nil), key.PublicKey...)}
	}
	return trusted
}

func validAnchorTimeProbeSigner(state SecurityAnchorState, raw []byte, accepted time.Time) bool {
	var response signedTimeProbeResponse
	if json.Unmarshal(raw, &response) != nil {
		return false
	}
	for _, key := range state.AuthorityKeys {
		if key.KeyID == response.SignerKeyID && !slices.Contains(state.RevocationLedger.RevokedKeyIDs, key.KeyID) &&
			!accepted.Before(key.NotBefore) && accepted.Before(key.NotAfter) {
			return true
		}
	}
	return false
}

// ApplyTrustRecord consumes one freshly accepted helper time probe and commits
// exactly one contiguous key-set or revocation-ledger successor. While the
// ledger is stale, only an authority_key_set or release_revocation_ledger can
// advance the trust chain. All other records remain rejected until a fresh
// ledger has been durably stored.
func (a *SecurityAnchor) ApplyTrustRecord(envelopeRaw []byte) error {
	if a == nil {
		return ErrSecurityAnchorUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy {
		return ErrSecurityAnchorUnavailable
	}
	if a.state.ProbeGeneration == a.state.ConsumedProbeGeneration {
		return invalidL3Trust("trust update requires a fresh unused helper time probe")
	}
	acceptedAt := a.state.HighestAuthorityTime
	_, err := a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
		if next.ConsumedProbeGeneration == ^uint64(0) || next.ProbeGeneration != next.ConsumedProbeGeneration+1 {
			return ErrSecurityAnchorRollback
		}
		next.ConsumedProbeGeneration = next.ProbeGeneration
		return nil
	})
	if err != nil {
		return err
	}
	if acceptedAt.IsZero() {
		return invalidL3Trust("trust update has no accepted authority time")
	}
	var identity struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(envelopeRaw, &identity); err != nil {
		return invalidL3Trust("decode trust record type")
	}
	ledgerFresh := ValidateRevocationLedgerFreshForMutation(a.state.RevocationLedger, acceptedAt) == nil
	if !ledgerFresh && identity.RecordType != "authority_key_set" && identity.RecordType != "release_revocation_ledger" {
		return invalidL3Trust("stale-ledger recovery accepts only an authority key set or successor ledger")
	}
	trusted := anchorAuthorityKeys(a.state, acceptedAt)
	next := a.state
	verifiedEnvelope, err := VerifyEnvelope(envelopeRaw, trusted)
	if err != nil {
		return err
	}
	if slices.Contains(next.RevocationLedger.RevokedRecordIDs, verifiedEnvelope.RecordID) || slices.Contains(next.RevocationLedger.RevokedKeyIDs, verifiedEnvelope.SignerKeyID) {
		return invalidL3Trust("trust record or signer is already revoked")
	}
	switch identity.RecordType {
	case "authority_key_set":
		previous := next.AuthorityKeys[len(next.AuthorityKeys)-1]
		transition, err := ValidateAuthorityKeySet(envelopeRaw, previous, trusted, acceptedAt)
		if err != nil {
			return err
		}
		if slices.Contains(next.RevocationLedger.RevokedKeyIDs, transition.Successor.KeyID) || slices.Contains(next.RevocationLedger.RevokedRecordIDs, transition.Successor.RecordID) {
			return invalidL3Trust("successor authority key or key-set record is already revoked")
		}
		updated, err := cloneSecurityAnchorState(next)
		if err != nil {
			return err
		}
		updated.AuthorityKeys[len(updated.AuthorityKeys)-1] = transition.Previous
		updated.AuthorityKeys = append(updated.AuthorityKeys, transition.Successor)
		_, err = a.commitLocked(context.Background(), func(current *SecurityAnchorState) error {
			if current.Generation != next.Generation || !bytes.Equal(current.AuthorityKeys[len(current.AuthorityKeys)-1].PublicKey, previous.PublicKey) {
				return ErrSecurityAnchorRollback
			}
			current.AuthorityKeys = updated.AuthorityKeys
			return nil
		})
		return err
	case "host_attestation_key_set":
		previous := next.HostAttestationKeys[len(next.HostAttestationKeys)-1]
		transition, err := ValidateHostAttestationKeySet(envelopeRaw, previous, trusted, acceptedAt)
		if err != nil {
			return err
		}
		if slices.Contains(next.RevocationLedger.RevokedKeyIDs, transition.Successor.KeyID) || slices.Contains(next.RevocationLedger.RevokedRecordIDs, transition.Successor.RecordID) {
			return invalidL3Trust("successor host key or key-set record is already revoked")
		}
		updated, err := cloneSecurityAnchorState(next)
		if err != nil {
			return err
		}
		updated.HostAttestationKeys[len(updated.HostAttestationKeys)-1] = transition.Previous
		updated.HostAttestationKeys = append(updated.HostAttestationKeys, transition.Successor)
		_, err = a.commitLocked(context.Background(), func(current *SecurityAnchorState) error {
			if current.Generation != next.Generation {
				return ErrSecurityAnchorRollback
			}
			current.HostAttestationKeys = updated.HostAttestationKeys
			return nil
		})
		return err
	case "release_revocation_ledger":
		updatedLedger, err := ValidateRevocationLedger(envelopeRaw, next.RevocationLedger, trusted, acceptedAt)
		if err != nil {
			return err
		}
		_, err = a.commitLocked(context.Background(), func(current *SecurityAnchorState) error {
			if current.Generation != next.Generation {
				return fmt.Errorf("anchor generation changed while applying ledger: %w", ErrSecurityAnchorRollback)
			}
			if !ledgerIsSuccessorOrSame(current.RevocationLedger, updatedLedger) {
				return fmt.Errorf("ledger is not a contiguous cumulative successor: %w", ErrSecurityAnchorRollback)
			}
			current.RevocationLedger = updatedLedger
			return nil
		})
		return err
	default:
		return invalidL3Trust("unsupported anchor trust record type")
	}
}

func anchorAuthorityKeys(state SecurityAnchorState, now time.Time) map[string]TrustedKey {
	trusted := make(map[string]TrustedKey, len(state.AuthorityKeys))
	for _, key := range state.AuthorityKeys {
		if now.Before(key.NotBefore) || !now.Before(key.NotAfter) || slices.Contains(state.RevocationLedger.RevokedKeyIDs, key.KeyID) {
			continue
		}
		trusted[key.KeyID] = TrustedKey{KeyID: key.KeyID, Role: SignerRoleReleaseAuthority, PublicKey: append(ed25519.PublicKey(nil), key.PublicKey...)}
	}
	return trusted
}

// RequireFreshLedger is the fail-closed admission check for any mutation. It
// uses only the latest helper-accepted authority time, never a caller value.
func (a *SecurityAnchor) RequireFreshLedger() error {
	state, err := a.Snapshot()
	if err != nil {
		return err
	}
	if err := ValidateRevocationLedgerFreshForMutation(state.RevocationLedger, state.HighestAuthorityTime); err != nil {
		return err
	}
	return nil
}

// ValidateRestoreHighWater rejects a candidate continuity snapshot that would
// lower or fork any already anchored trust, time, directive, or runtime fact.
// It never imports or writes the supplied snapshot; callers must separately
// verify its signed record chain before merging any newer records.
func (a *SecurityAnchor) ValidateRestoreHighWater(candidate SecurityAnchorState) error {
	current, err := a.Snapshot()
	if err != nil {
		return err
	}
	if candidate.HostID != current.HostID || validateSecurityAnchorState(candidate) != nil || candidate.HighestAuthorityTime.Before(current.HighestAuthorityTime) ||
		candidate.ProbeGeneration < current.ProbeGeneration || candidate.ConsumedProbeGeneration < current.ConsumedProbeGeneration ||
		!prefixKeyHistory(current.AuthorityKeys, candidate.AuthorityKeys) || !prefixKeyHistory(current.HostAttestationKeys, candidate.HostAttestationKeys) ||
		!ledgerAtLeast(current.RevocationLedger, candidate.RevocationLedger) {
		return ErrSecurityAnchorRollback
	}
	for lane, highWater := range current.DirectiveHighWater {
		candidateHighWater, ok := candidate.DirectiveHighWater[lane]
		if !ok || compareAnchorDirective(candidateHighWater, highWater) < 0 || (candidateHighWater.TransitionEpoch == highWater.TransitionEpoch &&
			(candidateHighWater.TransitionID != highWater.TransitionID || candidateHighWater.CandidateManifestID != highWater.CandidateManifestID ||
				candidateHighWater.ConfigurationDigest != highWater.ConfigurationDigest)) {
			return ErrSecurityAnchorRollback
		}
	}
	if !runtimeContextsAtLeast(current.ActiveRuntime, candidate.ActiveRuntime) || !runtimeContextsAtLeast(current.PendingCandidate, candidate.PendingCandidate) {
		return ErrSecurityAnchorRollback
	}
	return nil
}

func ledgerAtLeast(current, candidate RevocationLedgerState) bool {
	if candidate.Generation < current.Generation {
		return false
	}
	for _, id := range current.RevokedRecordIDs {
		if !slices.Contains(candidate.RevokedRecordIDs, id) {
			return false
		}
	}
	for _, id := range current.RevokedKeyIDs {
		if !slices.Contains(candidate.RevokedKeyIDs, id) {
			return false
		}
	}
	if candidate.Generation == current.Generation {
		return candidate.RecordID == current.RecordID && slices.Equal(candidate.RevokedRecordIDs, current.RevokedRecordIDs) &&
			slices.Equal(candidate.RevokedKeyIDs, current.RevokedKeyIDs) && candidate.IssuedAt.Equal(current.IssuedAt) && candidate.ExpiresAt.Equal(current.ExpiresAt)
	}
	return isLowerHexDigest(candidate.RecordID)
}

func runtimeContextsAtLeast(current, candidate map[string]AnchorRuntimeContext) bool {
	for lane, value := range current {
		other, ok := candidate[lane]
		if !ok || other.TransitionEpoch < value.TransitionEpoch {
			return false
		}
		if other.TransitionEpoch == value.TransitionEpoch && (other.TransitionID != value.TransitionID || other.CandidateManifestID != value.CandidateManifestID ||
			other.ConfigurationDigest != value.ConfigurationDigest || other.RecordID != value.RecordID) {
			return false
		}
	}
	return true
}

// RecordVerifiedDirective persists an already signature-, schema-, host-, and
// context-validated directive. The helper's caller must validate those fields
// before invoking this method; the anchor enforces local host binding and the
// monotonic high-water transition atomically with the pending candidate
// context. A fresh unused time probe and fresh ledger are mandatory.
func (a *SecurityAnchor) RecordVerifiedDirective(value AnchorDirectiveHighWater) error {
	if a == nil {
		return ErrSecurityAnchorUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy {
		return ErrSecurityAnchorUnavailable
	}
	if a.state.ProbeGeneration == a.state.ConsumedProbeGeneration || a.state.HighestAuthorityTime.IsZero() {
		return invalidL3Trust("directive update requires a fresh unused helper time probe")
	}
	if err := ValidateRevocationLedgerFreshForMutation(a.state.RevocationLedger, a.state.HighestAuthorityTime); err != nil {
		return err
	}
	if value.HostID != a.state.HostID || !validAnchorDirective(value) {
		return invalidL3Trust("directive high-water does not match the anchored host or closed contract")
	}
	_, err := a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
		if next.ConsumedProbeGeneration == ^uint64(0) || next.ProbeGeneration != next.ConsumedProbeGeneration+1 {
			return ErrSecurityAnchorRollback
		}
		next.ConsumedProbeGeneration = next.ProbeGeneration
		if prior, ok := next.DirectiveHighWater[value.Lane]; ok {
			if compareAnchorDirective(value, prior) <= 0 {
				return invalidL3Trust("directive tuple is replayed, forked, or below its high-water")
			}
			samePhase := value.TransitionID == prior.TransitionID && value.TransitionEpoch == prior.TransitionEpoch &&
				value.AdmissionGeneration == prior.AdmissionGeneration && value.Role == prior.Role &&
				value.CandidateManifestID == prior.CandidateManifestID && value.ConfigurationDigest == prior.ConfigurationDigest
			if samePhase && value.LeaseGeneration > prior.LeaseGeneration &&
				(value.LeaseGeneration != prior.LeaseGeneration+1 || value.PredecessorRecordID != prior.RecordID) {
				return invalidL3Trust("directive renewal is not the contiguous child of its anchored predecessor")
			}
			if value.PredecessorRecordID != "" && value.PredecessorRecordID != prior.RecordID {
				return invalidL3Trust("directive predecessor does not match the anchored record")
			}
			if prior.TransitionEpoch == value.TransitionEpoch && (prior.TransitionID != value.TransitionID || prior.CandidateManifestID != value.CandidateManifestID || prior.ConfigurationDigest != value.ConfigurationDigest) {
				return invalidL3Trust("directive identity changed within one transition epoch")
			}
		} else if value.PredecessorRecordID != "" {
			return invalidL3Trust("first directive cannot name an unanchored predecessor")
		}
		if value.Role == "candidate_admission" {
			if prior, ok := next.PendingCandidate[value.Lane]; ok {
				if value.TransitionEpoch < prior.TransitionEpoch || (value.TransitionEpoch == prior.TransitionEpoch &&
					(prior.TransitionID != value.TransitionID || prior.CandidateManifestID != value.CandidateManifestID || prior.ConfigurationDigest != value.ConfigurationDigest)) {
					return ErrSecurityAnchorRollback
				}
			}
			next.PendingCandidate[value.Lane] = AnchorRuntimeContext{
				Lane: value.Lane, HostID: value.HostID, TransitionID: value.TransitionID, TransitionEpoch: value.TransitionEpoch,
				CandidateManifestID: value.CandidateManifestID, ConfigurationDigest: value.ConfigurationDigest, RecordID: value.RecordID,
			}
		}
		next.DirectiveHighWater[value.Lane] = value
		return nil
	})
	return err
}

// RecordVerifiedActiveRuntime commits a candidate only after the caller has
// verified its signed lane_acceptance and complete installation evidence. It
// atomically advances the active identity and clears only the matching pending
// candidate context.
func (a *SecurityAnchor) RecordVerifiedActiveRuntime(value AnchorRuntimeContext) error {
	if a == nil {
		return ErrSecurityAnchorUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy {
		return ErrSecurityAnchorUnavailable
	}
	if a.state.ProbeGeneration == a.state.ConsumedProbeGeneration || a.state.HighestAuthorityTime.IsZero() {
		return invalidL3Trust("runtime commit requires a fresh unused helper time probe")
	}
	if err := ValidateRevocationLedgerFreshForMutation(a.state.RevocationLedger, a.state.HighestAuthorityTime); err != nil {
		return err
	}
	if value.HostID != a.state.HostID || !validAnchorRuntime(value) {
		return invalidL3Trust("accepted runtime context does not match anchored host or contract")
	}
	pending, ok := a.state.PendingCandidate[value.Lane]
	if !ok || pending.TransitionID != value.TransitionID || pending.TransitionEpoch != value.TransitionEpoch ||
		pending.CandidateManifestID != value.CandidateManifestID || pending.ConfigurationDigest != value.ConfigurationDigest {
		return invalidL3Trust("accepted runtime does not match the root-installed pending candidate")
	}
	if active, exists := a.state.ActiveRuntime[value.Lane]; exists && value.TransitionEpoch <= active.TransitionEpoch {
		return ErrSecurityAnchorRollback
	}
	_, err := a.commitLocked(context.Background(), func(next *SecurityAnchorState) error {
		if next.ConsumedProbeGeneration == ^uint64(0) || next.ProbeGeneration != next.ConsumedProbeGeneration+1 {
			return ErrSecurityAnchorRollback
		}
		next.ConsumedProbeGeneration = next.ProbeGeneration
		next.ActiveRuntime[value.Lane] = value
		delete(next.PendingCandidate, value.Lane)
		return nil
	})
	return err
}

func (a *SecurityAnchor) setFaultHooks(hooks anchorFaultHooks) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.faults = hooks
}
