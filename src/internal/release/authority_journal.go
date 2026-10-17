package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	authorityJournalDBName = "authority.sqlite"
	authorityJournalMode   = 0o600
	authorityStateMode     = 0o700
)

var (
	ErrAuthorityStateUnavailable = errors.New("release authority state unavailable")
	ErrAuthorityStateRollback    = errors.New("release authority state rollback or fork detected")
	ErrAuthorityStateInitialized = errors.New("release authority state is already initialized")
)

// AuthorityStateBootstrap is pinned once during the separately approved root
// bootstrap. It contains public trust only; private key bytes are kept in the
// dedicated signer key file and never enter this journal.
type AuthorityStateBootstrap struct {
	KeyID            string
	PublicKey        ed25519.PublicKey
	LedgerGeneration uint64
	LedgerRecordID   string
	LedgerIssuedAt   time.Time
	LedgerExpiresAt  time.Time
	RevokedRecordIDs []string
	RevokedKeyIDs    []string
}

type authorityPlan struct {
	ID               string          `json:"plan_id"`
	RecordType       string          `json:"record_type"`
	Payload          json.RawMessage `json:"payload"`
	BodyDigest       string          `json:"body_digest"`
	ContextDigest    string          `json:"context_digest"`
	LedgerGeneration uint64          `json:"ledger_generation"`
	StagedAt         string          `json:"staged_at"`
}

// AuthorityMandate is an exact, expiring approval. A mandate is useful only
// with the staged plan whose body and derived authority context it names.
type AuthorityMandate struct {
	ID               string `json:"mandate_id"`
	PlanID           string `json:"plan_id"`
	RecordType       string `json:"record_type"`
	BodyDigest       string `json:"body_digest"`
	ContextDigest    string `json:"context_digest"`
	LedgerGeneration uint64 `json:"ledger_generation"`
	ExpiresAt        string `json:"expires_at"`
	ApprovalReceipt  string `json:"approval_receipt_id"`
}

type authoritySignedResult struct {
	MandateID string           `json:"mandate_id"`
	RecordID  string           `json:"record_id"`
	Mandate   AuthorityMandate `json:"consumed_mandate"`
	Envelope  json.RawMessage  `json:"envelope"`
}

type authorityRootState struct {
	Version           int                                 `json:"schema_version"`
	Generation        uint64                              `json:"generation"`
	AnchorKeyID       string                              `json:"anchor_key_id"`
	AnchorPublicKey   string                              `json:"anchor_public_key"`
	LedgerGeneration  uint64                              `json:"ledger_generation"`
	Plans             map[string]authorityPlan            `json:"pending_plans"`
	Mandates          map[string]AuthorityMandate         `json:"mandates"`
	ConsumedMandates  map[string]authoritySignedResult    `json:"consumed_mandates"`
	AcceptedRecordIDs map[string]string                   `json:"accepted_authority_record_ids"`
	TransitionHeads   map[string]string                   `json:"verified_transition_high_water"`
	LedgerRecordID    string                              `json:"ledger_record_id,omitempty"`
	LedgerIssuedAt    string                              `json:"ledger_issued_at,omitempty"`
	LedgerExpiresAt   string                              `json:"ledger_expires_at,omitempty"`
	RevokedRecordIDs  []string                            `json:"revoked_record_ids,omitempty"`
	RevokedKeyIDs     []string                            `json:"revoked_key_ids,omitempty"`
	DirectiveGrants   map[string]DirectiveRenewalGrant    `json:"directive_renewal_grants,omitempty"`
	LedgerGrants      map[string]LedgerRenewalGrant       `json:"ledger_renewal_grants,omitempty"`
	ActiveLedgerGrant string                              `json:"active_ledger_renewal_grant,omitempty"`
	TimeProbeRecords  map[string]authorityTimeProbeRecord `json:"authority_time_probe_records,omitempty"`
	RenewalAlerts     map[string]AuthorityRenewalAlert    `json:"renewal_alerts,omitempty"`
}

// AuthorityJournal is a root-owned, full-sync SQLite state and append-only
// commit chain. Production opens only the fixed Mac authority path; the
// package-private path-taking constructor supports isolated tests.
type AuthorityJournal struct {
	mu        sync.Mutex
	dir       string
	ownerUID  uint32
	db        *sql.DB
	conn      *sql.Conn
	state     authorityRootState
	digest    string
	unhealthy bool
}

func authorityStateDirectory() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", ErrAuthorityStateUnavailable
	}
	return "/Library/Application Support/RemoteSessionRunner-release-authority-signer/state", nil
}

func OpenReleaseAuthorityJournal() (*AuthorityJournal, error) {
	if os.Geteuid() != 0 {
		return nil, ErrAuthorityStateUnavailable
	}
	dir, err := authorityStateDirectory()
	if err != nil {
		return nil, err
	}
	return openAuthorityJournalAt(dir, 0, nil)
}

// InitializeReleaseAuthorityJournal creates the fixed root state exactly once.
func InitializeReleaseAuthorityJournal(bootstrap AuthorityStateBootstrap) error {
	if os.Geteuid() != 0 {
		return ErrAuthorityStateUnavailable
	}
	dir, err := authorityStateDirectory()
	if err != nil {
		return err
	}
	journal, err := openAuthorityJournalAt(dir, 0, &bootstrap)
	if err != nil {
		return err
	}
	return journal.Close()
}

func openAuthorityJournalAt(dir string, ownerUID uint32, bootstrap *AuthorityStateBootstrap) (*AuthorityJournal, error) {
	if dir == "" {
		return nil, ErrAuthorityStateUnavailable
	}
	if err := verifyAuthorityStateDirectory(dir, ownerUID); err != nil {
		return nil, err
	}
	if err := verifyAuthorityDirectoryEntries(dir, ownerUID); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, authorityJournalDBName)
	_, statErr := os.Lstat(dbPath)
	if errors.Is(statErr, os.ErrNotExist) {
		if bootstrap == nil {
			return nil, ErrAuthorityStateUnavailable
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 || len(bootstrap.PublicKey) != ed25519.PublicKeySize || !validKeyID(bootstrap.KeyID) || bootstrap.LedgerGeneration == 0 {
			return nil, ErrAuthorityStateUnavailable
		}
		if _, err := authorityLedgerState(newAuthorityRootState(*bootstrap)); err != nil {
			return nil, ErrAuthorityStateUnavailable
		}
		if err := createAuthorityDatabase(dbPath, dir, ownerUID, *bootstrap); err != nil {
			return nil, err
		}
	} else if statErr != nil {
		return nil, statErr
	} else if bootstrap != nil {
		return nil, ErrAuthorityStateInitialized
	}
	if err := verifyAuthorityDirectoryEntries(dir, ownerUID); err != nil {
		return nil, err
	}
	dataSource, err := anchorDataSource(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dataSource)
	if err != nil {
		return nil, fmt.Errorf("open authority database: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := configureAnchorPragmas(context.Background(), conn); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("configure authority full-sync storage: %w", err)
	}
	state, digest, err := readAuthorityCommit(context.Background(), conn)
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}
	return &AuthorityJournal{dir: dir, ownerUID: ownerUID, db: db, conn: conn, state: state, digest: digest}, nil
}

func createAuthorityDatabase(dbPath, dir string, ownerUID uint32, bootstrap AuthorityStateBootstrap) error {
	file, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, authorityJournalMode)
	if err != nil {
		return fmt.Errorf("create authority database: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dbPath, authorityJournalMode); err != nil {
		return err
	}
	if err := verifyAuthorityDatabaseFile(dbPath, ownerUID); err != nil {
		return err
	}
	dataSource, err := anchorDataSource(dbPath)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dataSource)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := configureAnchorPragmas(context.Background(), conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(context.Background(), `
CREATE TABLE authority_state (
	 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	 generation INTEGER NOT NULL,
	 previous_digest TEXT NOT NULL,
	 state_digest TEXT NOT NULL,
	 state_json BLOB NOT NULL
);
CREATE TABLE authority_commits (
	 generation INTEGER PRIMARY KEY,
	 previous_digest TEXT NOT NULL,
	 state_digest TEXT NOT NULL
);`); err != nil {
		return fmt.Errorf("create authority state schema: %w", err)
	}
	state := newAuthorityRootState(bootstrap)
	if err := validateAuthorityRootState(state); err != nil {
		return fmt.Errorf("validate authority bootstrap state: %w", err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	digest := authorityCommitDigest(0, "", encoded)
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO authority_state(singleton,generation,previous_digest,state_digest,state_json) VALUES(1,0,'',?,?)", digest, encoded); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO authority_commits(generation,previous_digest,state_digest) VALUES(0,'',?)", digest); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := syncAnchorFiles(dbPath, dir); err != nil {
		return fmt.Errorf("sync authority bootstrap: %w", err)
	}
	return nil
}

func newAuthorityRootState(bootstrap AuthorityStateBootstrap) authorityRootState {
	state := authorityRootState{Version: 1, AnchorKeyID: bootstrap.KeyID,
		AnchorPublicKey: base64.StdEncoding.EncodeToString(bootstrap.PublicKey), LedgerGeneration: bootstrap.LedgerGeneration,
		Plans: map[string]authorityPlan{}, Mandates: map[string]AuthorityMandate{},
		ConsumedMandates: map[string]authoritySignedResult{}, AcceptedRecordIDs: map[string]string{},
		TransitionHeads: map[string]string{}}
	if isLowerHexDigest(bootstrap.LedgerRecordID) && !bootstrap.LedgerIssuedAt.IsZero() && !bootstrap.LedgerExpiresAt.IsZero() {
		state.LedgerRecordID = bootstrap.LedgerRecordID
		state.LedgerIssuedAt = bootstrap.LedgerIssuedAt.UTC().Format(time.RFC3339Nano)
		state.LedgerExpiresAt = bootstrap.LedgerExpiresAt.UTC().Format(time.RFC3339Nano)
		state.RevokedRecordIDs = append([]string(nil), bootstrap.RevokedRecordIDs...)
		state.RevokedKeyIDs = append([]string(nil), bootstrap.RevokedKeyIDs...)
	}
	return state
}

func verifyAuthorityStateDirectory(path string, ownerUID uint32) error {
	if err := verifyOwnedDirectory(path, ownerUID); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityStateUnavailable, err)
	}
	return nil
}

func verifyAuthorityDatabaseFile(path string, ownerUID uint32) error {
	if err := verifyOwnedRegularFile(path, ownerUID, authorityJournalMode); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityStateUnavailable, err)
	}
	return nil
}

func verifyAuthorityDirectoryEntries(dir string, ownerUID uint32) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ErrAuthorityStateUnavailable
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		switch entry.Name() {
		case authorityJournalDBName:
			if err := verifyAuthorityDatabaseFile(path, ownerUID); err != nil {
				return err
			}
		case authorityJournalDBName + "-journal":
			if err := verifyOwnedRegularFile(path, ownerUID, authorityJournalMode); err != nil {
				return fmt.Errorf("%w: %v", ErrAuthorityStateUnavailable, err)
			}
		default:
			return fmt.Errorf("%w: unexpected authority state entry %q", ErrAuthorityStateUnavailable, entry.Name())
		}
	}
	return nil
}

func authorityCommitDigest(generation uint64, previous string, state []byte) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "remote-session-runner/authority-state/v1\x00%d\x00%s\x00", generation, previous)
	_, _ = hash.Write(state)
	return hex.EncodeToString(hash.Sum(nil))
}

func hexDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readAuthorityCommit(ctx context.Context, conn *sql.Conn) (authorityRootState, string, error) {
	var generation uint64
	var previous, digest string
	var encoded []byte
	if err := conn.QueryRowContext(ctx, "SELECT generation,previous_digest,state_digest,state_json FROM authority_state WHERE singleton=1").Scan(&generation, &previous, &digest, &encoded); err != nil {
		return authorityRootState{}, "", fmt.Errorf("read authority state: %w", err)
	}
	if authorityCommitDigest(generation, previous, encoded) != digest {
		return authorityRootState{}, "", ErrAuthorityStateRollback
	}
	var state authorityRootState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state.Generation != generation || validateAuthorityRootState(state) != nil {
		return authorityRootState{}, "", ErrAuthorityStateRollback
	}
	var count, first, last uint64
	if err := conn.QueryRowContext(ctx, "SELECT count(*),min(generation),max(generation) FROM authority_commits").Scan(&count, &first, &last); err != nil || count != generation+1 || first != 0 || last != generation {
		return authorityRootState{}, "", ErrAuthorityStateRollback
	}
	rows, err := conn.QueryContext(ctx, "SELECT generation,previous_digest,state_digest FROM authority_commits ORDER BY generation")
	if err != nil {
		return authorityRootState{}, "", err
	}
	defer rows.Close()
	wantPrevious := ""
	lastPrevious := ""
	for expected := uint64(0); rows.Next(); expected++ {
		var got uint64
		var gotPrevious, gotDigest string
		if err := rows.Scan(&got, &gotPrevious, &gotDigest); err != nil || got != expected || gotPrevious != wantPrevious {
			return authorityRootState{}, "", ErrAuthorityStateRollback
		}
		lastPrevious = gotPrevious
		wantPrevious = gotDigest
	}
	if rows.Err() != nil || wantPrevious != digest || previous != lastPrevious {
		return authorityRootState{}, "", ErrAuthorityStateRollback
	}
	return state, digest, nil
}

func validateAuthorityRootState(state authorityRootState) error {
	if state.Version != 1 || !validKeyID(state.AnchorKeyID) || state.Plans == nil || state.Mandates == nil ||
		state.ConsumedMandates == nil || state.AcceptedRecordIDs == nil || state.TransitionHeads == nil {
		return ErrAuthorityStateRollback
	}
	publicKey, err := base64.StdEncoding.DecodeString(state.AnchorPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return ErrAuthorityStateRollback
	}
	if state.LedgerRecordID != "" {
		issued, issuedErr := time.Parse(time.RFC3339Nano, state.LedgerIssuedAt)
		expires, expiresErr := time.Parse(time.RFC3339Nano, state.LedgerExpiresAt)
		if !isLowerHexDigest(state.LedgerRecordID) || state.LedgerGeneration == 0 || issuedErr != nil || expiresErr != nil ||
			!issued.Before(expires) || !sortedUnique(state.RevokedRecordIDs) || !sortedUnique(state.RevokedKeyIDs) {
			return ErrAuthorityStateRollback
		}
	} else if state.LedgerIssuedAt != "" || state.LedgerExpiresAt != "" || len(state.RevokedRecordIDs) != 0 || len(state.RevokedKeyIDs) != 0 {
		return ErrAuthorityStateRollback
	}
	for id, plan := range state.Plans {
		if id != plan.ID || !validOpaqueID(id) || !authorityRecordType(plan.RecordType) || !isLowerHexDigest(plan.BodyDigest) ||
			!isLowerHexDigest(plan.ContextDigest) || hexDigest(plan.Payload) != plan.BodyDigest || plan.LedgerGeneration > state.LedgerGeneration {
			return ErrAuthorityStateRollback
		}
		canonical, canonicalErr := CanonicalizeJSON(plan.Payload)
		contextDigest, bodyLedger, contextErr := authorityPlanContext(plan.RecordType, plan.Payload)
		if canonicalErr != nil || !bytes.Equal(canonical, plan.Payload) || contextErr != nil || contextDigest != plan.ContextDigest ||
			(bodyLedger != 0 && bodyLedger != plan.LedgerGeneration) || ValidatePayload(plan.RecordType, plan.Payload) != nil {
			return ErrAuthorityStateRollback
		}
	}
	for id, mandate := range state.Mandates {
		if id != mandate.ID || !validOpaqueID(id) || !validOpaqueID(mandate.PlanID) || !isLowerHexDigest(mandate.BodyDigest) ||
			!isLowerHexDigest(mandate.ContextDigest) || mandate.LedgerGeneration > state.LedgerGeneration {
			return ErrAuthorityStateRollback
		}
		plan, ok := state.Plans[mandate.PlanID]
		if !ok || plan.RecordType != mandate.RecordType || plan.BodyDigest != mandate.BodyDigest || plan.ContextDigest != mandate.ContextDigest ||
			plan.LedgerGeneration != mandate.LedgerGeneration || !validAuthorityStateID(mandate.ApprovalReceipt) {
			return ErrAuthorityStateRollback
		}
		if _, err := time.Parse(time.RFC3339Nano, mandate.ExpiresAt); err != nil {
			return ErrAuthorityStateRollback
		}
	}
	anchorKey := ed25519.PublicKey(publicKey)
	trusted := map[string]TrustedKey{state.AnchorKeyID: {KeyID: state.AnchorKeyID, Role: SignerRoleReleaseAuthority, PublicKey: anchorKey}}
	for id, result := range state.ConsumedMandates {
		if id != result.MandateID || id != result.Mandate.ID || !validOpaqueID(id) || !isLowerHexDigest(result.RecordID) ||
			!validAuthorityStateID(result.Mandate.ApprovalReceipt) || !isLowerHexDigest(result.Mandate.BodyDigest) ||
			!isLowerHexDigest(result.Mandate.ContextDigest) || len(result.Envelope) == 0 {
			return ErrAuthorityStateRollback
		}
		envelope, verifyErr := VerifyEnvelope(result.Envelope, trusted)
		contextDigest, bodyLedger, contextErr := authorityPlanContext(envelope.RecordType, envelope.Payload)
		if verifyErr != nil || envelope.RecordID != result.RecordID || envelope.RecordType != result.Mandate.RecordType ||
			hexDigest(envelope.Payload) != result.Mandate.BodyDigest || contextErr != nil || contextDigest != result.Mandate.ContextDigest ||
			(bodyLedger != 0 && bodyLedger != result.Mandate.LedgerGeneration) || state.AcceptedRecordIDs[result.RecordID] != result.Mandate.ContextDigest {
			return ErrAuthorityStateRollback
		}
	}
	for id, digest := range state.AcceptedRecordIDs {
		if !isLowerHexDigest(id) || !isLowerHexDigest(digest) {
			return ErrAuthorityStateRollback
		}
		found := false
		for _, result := range state.ConsumedMandates {
			if result.RecordID == id && result.Mandate.ContextDigest == digest {
				found = true
				break
			}
		}
		if !found {
			return ErrAuthorityStateRollback
		}
	}
	for context, head := range state.TransitionHeads {
		if !isLowerHexDigest(context) || !isLowerHexDigest(head) || state.AcceptedRecordIDs[head] != context {
			return ErrAuthorityStateRollback
		}
	}
	if err := validateAuthorityRenewalState(state, ed25519.PublicKey(publicKey)); err != nil {
		return ErrAuthorityStateRollback
	}
	return nil
}

func (j *AuthorityJournal) mutate(mutate func(*authorityRootState) error) (authorityRootState, error) {
	if j == nil || j.conn == nil || j.unhealthy {
		return authorityRootState{}, ErrAuthorityStateUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := verifyAuthorityStateDirectory(j.dir, j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	if err := verifyAuthorityDirectoryEntries(j.dir, j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	if err := verifyAuthorityDatabaseFile(filepath.Join(j.dir, authorityJournalDBName), j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	current, previousDigest, err := readAuthorityCommit(context.Background(), j.conn)
	if err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	encodedCurrent, err := json.Marshal(current)
	if err != nil {
		return authorityRootState{}, err
	}
	var next authorityRootState
	if err := json.Unmarshal(encodedCurrent, &next); err != nil {
		return authorityRootState{}, err
	}
	if err := mutate(&next); err != nil {
		return authorityRootState{}, err
	}
	if next.AnchorKeyID != current.AnchorKeyID || next.AnchorPublicKey != current.AnchorPublicKey || next.LedgerGeneration < current.LedgerGeneration {
		return authorityRootState{}, fmt.Errorf("%w: trusted anchor or ledger high-water changed backwards", ErrAuthorityStateRollback)
	}
	if !authorityStateMapsPreserved(current, next) {
		return authorityRootState{}, fmt.Errorf("%w: accepted authority evidence was removed or altered", ErrAuthorityStateRollback)
	}
	if !authorityRenewalStatePreserved(current, next) {
		return authorityRootState{}, fmt.Errorf("%w: renewal, alert, or time-probe journal was removed or altered", ErrAuthorityStateRollback)
	}
	if current.Generation >= uint64(^uint64(0)>>1) {
		return authorityRootState{}, ErrAuthorityStateRollback
	}
	next.Generation = current.Generation + 1
	if err := validateAuthorityRootState(next); err != nil {
		return authorityRootState{}, fmt.Errorf("%w: next root state failed validation: %v", ErrAuthorityStateRollback, err)
	}
	encodedNext, err := json.Marshal(next)
	if err != nil {
		return authorityRootState{}, err
	}
	digest := authorityCommitDigest(next.Generation, previousDigest, encodedNext)
	tx, err := j.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return authorityRootState{}, err
	}
	result, err := tx.ExecContext(context.Background(), "UPDATE authority_state SET generation=?,previous_digest=?,state_digest=?,state_json=? WHERE singleton=1 AND generation=? AND state_digest=?",
		next.Generation, previousDigest, digest, encodedNext, current.Generation, previousDigest)
	if err != nil {
		_ = tx.Rollback()
		return authorityRootState{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return authorityRootState{}, err
	}
	if rows != 1 {
		_ = tx.Rollback()
		// A separate journal handle may have committed after this handle read
		// the current head. Losing that compare-and-swap is an ordinary race;
		// the journal itself remains usable and the caller may re-read state.
		return authorityRootState{}, ErrAuthorityReplay
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT INTO authority_commits(generation,previous_digest,state_digest) VALUES(?,?,?)", next.Generation, previousDigest, digest); err != nil {
		_ = tx.Rollback()
		return authorityRootState{}, err
	}
	if err := tx.Commit(); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	dbPath := filepath.Join(j.dir, authorityJournalDBName)
	if err := syncAnchorFiles(dbPath, j.dir); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	if err := verifyAuthorityDirectoryEntries(j.dir, j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	j.state = next
	j.digest = digest
	return cloneAuthorityRootState(next)
}

func authorityStateMapsPreserved(before, after authorityRootState) bool {
	for id, digest := range before.AcceptedRecordIDs {
		if after.AcceptedRecordIDs[id] != digest {
			return false
		}
	}
	for id, result := range before.ConsumedMandates {
		if after.ConsumedMandates[id].RecordID != result.RecordID || after.ConsumedMandates[id].Mandate != result.Mandate ||
			!bytes.Equal(after.ConsumedMandates[id].Envelope, result.Envelope) {
			return false
		}
	}
	return true
}

func cloneAuthorityRootState(state authorityRootState) (authorityRootState, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return authorityRootState{}, err
	}
	var clone authorityRootState
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return authorityRootState{}, err
	}
	return clone, nil
}

func (j *AuthorityJournal) snapshot() (authorityRootState, error) {
	if j == nil || j.conn == nil || j.unhealthy {
		return authorityRootState{}, ErrAuthorityStateUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := verifyAuthorityStateDirectory(j.dir, j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	if err := verifyAuthorityDirectoryEntries(j.dir, j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	if err := verifyAuthorityDatabaseFile(filepath.Join(j.dir, authorityJournalDBName), j.ownerUID); err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	state, digest, err := readAuthorityCommit(context.Background(), j.conn)
	if err != nil {
		j.unhealthy = true
		return authorityRootState{}, err
	}
	j.state, j.digest = state, digest
	return cloneAuthorityRootState(state)
}

func (j *AuthorityJournal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var connErr, dbErr error
	if j.conn != nil {
		connErr = j.conn.Close()
		j.conn = nil
	}
	if j.db != nil {
		dbErr = j.db.Close()
		j.db = nil
	}
	if connErr != nil {
		return connErr
	}
	return dbErr
}

func (j *AuthorityJournal) stagePlan(plan authorityPlan) error {
	_, err := j.mutate(func(next *authorityRootState) error {
		if plan.LedgerGeneration != next.LedgerGeneration {
			return ErrAuthorityPlan
		}
		if _, exists := next.Plans[plan.ID]; exists {
			return ErrAuthorityReplay
		}
		next.Plans[plan.ID] = plan
		return nil
	})
	return err
}

func (j *AuthorityJournal) issueMandate(mandate AuthorityMandate) error {
	_, err := j.mutate(func(next *authorityRootState) error {
		plan, exists := next.Plans[mandate.PlanID]
		if !exists || plan.RecordType != mandate.RecordType || plan.BodyDigest != mandate.BodyDigest ||
			plan.ContextDigest != mandate.ContextDigest || plan.LedgerGeneration != mandate.LedgerGeneration ||
			plan.LedgerGeneration != next.LedgerGeneration {
			return ErrAuthorityMandate
		}
		for _, current := range next.Mandates {
			if current.PlanID == mandate.PlanID {
				return ErrAuthorityReplay
			}
		}
		if _, exists := next.Mandates[mandate.ID]; exists || !validAuthorityStateID(mandate.ID) || !validAuthorityStateID(mandate.ApprovalReceipt) {
			return ErrAuthorityReplay
		}
		next.Mandates[mandate.ID] = mandate
		return nil
	})
	return err
}

func validAuthorityStateID(value string) bool {
	return validOpaqueID(value) && !strings.ContainsAny(value, "/\\")
}
