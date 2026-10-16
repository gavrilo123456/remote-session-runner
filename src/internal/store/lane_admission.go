package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"remote-session-runner/src/internal/laneadmission"
)

var (
	ErrLaneAdmissionNotConfigured = errors.New("lane admission is not configured")
	ErrLaneAdmissionCorrupt       = errors.New("lane admission state is corrupt")
	ErrLaneAdmissionClosed        = laneadmission.ErrAdmissionClosed
)

// ConfigureLaneAdmission installs the durable fail-closed V3 gate for this
// lane authority. Repeated matching configuration is idempotent; a different
// lane or host cannot reuse the same authority database.
func (s *AuthorityStore) ConfigureLaneAdmission(ctx context.Context, lane laneadmission.Lane, host laneadmission.HostID) error {
	s.laneAdmissionMu.Lock()
	defer s.laneAdmissionMu.Unlock()
	initial, err := laneadmission.NewState(lane, host)
	if err != nil {
		return err
	}
	_, err = withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		current, found, err := readLaneAdmissionOnConnection(ctx, connection)
		if err != nil {
			return struct{}{}, err
		}
		if found {
			if current.Lane != lane || current.HostID != host {
				return struct{}{}, ErrLaneAdmissionCorrupt
			}
			return struct{}{}, nil
		}
		return struct{}{}, writeLaneAdmissionOnConnection(ctx, connection, initial, true, s.now().UTC())
	})
	if err == nil {
		// Configuration alone never trusts an open persisted context after a
		// process restart. A caller must explicitly revalidate it.
		s.laneAdmissionRuntime.Store(false)
	}
	return err
}

// LaneAdmissionSnapshot returns the persisted provisional gate. configured is
// false only for a legacy authority with no V3 admission row.
func (s *AuthorityStore) LaneAdmissionSnapshot(ctx context.Context) (state laneadmission.State, configured bool, err error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return laneadmission.State{}, false, fmt.Errorf("acquire lane admission connection: %w", err)
	}
	defer connection.Close()
	return readLaneAdmissionOnConnection(ctx, connection)
}

func (s *AuthorityStore) PrepareLaneAdmissionCandidate(ctx context.Context, identity laneadmission.Identity) error {
	return s.updateLaneAdmission(ctx, func(state *laneadmission.State) error { return state.PrepareCandidate(identity) })
}

func (s *AuthorityStore) BeginLaneAdmissionClosure(ctx context.Context, identity laneadmission.Identity) error {
	return s.updateLaneAdmission(ctx, func(state *laneadmission.State) error { return state.BeginActiveRuntimeClosure(identity) })
}

func (s *AuthorityStore) SwitchLaneAdmissionCandidate(ctx context.Context) error {
	return s.updateLaneAdmission(ctx, func(state *laneadmission.State) error { return state.SwitchPendingToActive() })
}

func (s *AuthorityStore) AdvanceLaneAdmissionCandidate(ctx context.Context, identity laneadmission.Identity, phase laneadmission.Phase) error {
	return s.updateLaneAdmission(ctx, func(state *laneadmission.State) error { return state.AdvanceCandidate(identity, phase) })
}

func (s *AuthorityStore) BeginLaneMigrationFence(ctx context.Context, transitionID string) error {
	return s.updateLaneAdmission(ctx, func(state *laneadmission.State) error { return state.BeginMigrationFence(transitionID) })
}

func (s *AuthorityStore) AdvanceLaneMigrationFence(ctx context.Context, transitionID string, next laneadmission.FenceState) error {
	return s.updateLaneAdmission(ctx, func(state *laneadmission.State) error { return state.AdvanceMigrationFence(transitionID, next) })
}

// RevalidateLaneAdmissionCandidate enables this process only after its caller
// has revalidated the durable, exact open context. Signature and lease checks
// are intentionally supplied by the later release-authority unit.
func (s *AuthorityStore) RevalidateLaneAdmissionCandidate(ctx context.Context, identity laneadmission.Identity) error {
	s.laneAdmissionMu.Lock()
	defer s.laneAdmissionMu.Unlock()
	state, configured, err := s.LaneAdmissionSnapshot(ctx)
	if err != nil {
		return err
	}
	if !configured || !state.AllowsMutation() || state.Active == nil || state.Active.Identity != identity || identity.Role != laneadmission.RoleCandidateAdmission {
		return ErrLaneAdmissionClosed
	}
	s.laneAdmissionRuntime.Store(true)
	return nil
}

func (s *AuthorityStore) updateLaneAdmission(ctx context.Context, mutate func(*laneadmission.State) error) error {
	s.laneAdmissionMu.Lock()
	defer s.laneAdmissionMu.Unlock()
	wasOpen := false
	allowsMutation := false
	_, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		state, configured, err := readLaneAdmissionOnConnection(ctx, connection)
		if err != nil {
			return struct{}{}, err
		}
		if !configured {
			return struct{}{}, ErrLaneAdmissionNotConfigured
		}
		wasOpen = state.AllowsMutation()
		if err := mutate(&state); err != nil {
			return struct{}{}, err
		}
		allowsMutation = state.AllowsMutation()
		return struct{}{}, writeLaneAdmissionOnConnection(ctx, connection, state, false, s.now().UTC())
	})
	if err == nil {
		if !allowsMutation {
			s.laneAdmissionRuntime.Store(false)
		} else if !wasOpen {
			// Only a closed -> open transition performed by this process may
			// enable it. An idempotent replay of an open row after restart is
			// not restart revalidation.
			s.laneAdmissionRuntime.Store(true)
		}
	}
	return err
}

// WithLaneIngressMutation serializes API lifecycle mutations with admission
// closure and fencing. New-work mutations use their SQLite acceptance
// transaction instead; this method covers cancel and close, which can run
// runtime work after their durable mutation. Internal cleanup paths do not use
// this wrapper so they can drain a lane after admission closes.
func (s *AuthorityStore) WithLaneIngressMutation(ctx context.Context, mutate func() error) error {
	if s == nil || s.db == nil || mutate == nil {
		return ErrLaneAdmissionCorrupt
	}
	s.laneAdmissionMu.RLock()
	defer s.laneAdmissionMu.RUnlock()
	state, configured, err := s.LaneAdmissionSnapshot(ctx)
	if err != nil {
		return err
	}
	if configured && (!s.laneAdmissionRuntime.Load() || !state.AllowsMutation()) {
		return ErrLaneAdmissionClosed
	}
	return mutate()
}

// checkIngressAdmissionOnConnection is called inside the same BEGIN IMMEDIATE
// transaction that inserts sessions, commands, or jobs. A closure/fence
// update therefore serializes with accepted work across concurrent ingress.
func checkIngressAdmissionOnConnection(ctx context.Context, owner *AuthorityStore, connection *sql.Conn) error {
	state, configured, err := readLaneAdmissionOnConnection(ctx, connection)
	if err != nil {
		return err
	}
	if configured && (!owner.laneAdmissionRuntime.Load() || !state.AllowsMutation()) {
		return ErrLaneAdmissionClosed
	}
	return nil
}

func readLaneAdmissionOnConnection(ctx context.Context, connection *sql.Conn) (laneadmission.State, bool, error) {
	var schemaVersion int
	if err := connection.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		return laneadmission.State{}, false, fmt.Errorf("read lane admission schema version: %w", err)
	}
	if schemaVersion < CurrentSchemaVersion {
		// Historical authorities without migration 0038 retain the explicit V2
		// compatibility behavior while their normal migration path is pending.
		return laneadmission.State{}, false, nil
	}
	var tableCount int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'lane_ingress_admission'`).Scan(&tableCount); err != nil {
		return laneadmission.State{}, false, fmt.Errorf("inspect lane admission schema: %w", err)
	}
	if tableCount != 1 {
		return laneadmission.State{}, true, ErrLaneAdmissionCorrupt
	}
	var raw, digest []byte
	err := connection.QueryRowContext(ctx, `SELECT state_json, state_sha256 FROM lane_ingress_admission WHERE singleton = 1`).Scan(&raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return laneadmission.State{}, false, nil
	}
	if err != nil {
		return laneadmission.State{}, false, fmt.Errorf("read lane admission state: %w", err)
	}
	computed := sha256.Sum256(raw)
	if !bytes.Equal(digest, computed[:]) {
		return laneadmission.State{}, true, ErrLaneAdmissionCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state laneadmission.State
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF || !state.Enabled || state.Validate() != nil {
		return laneadmission.State{}, true, ErrLaneAdmissionCorrupt
	}
	return state, true, nil
}

func writeLaneAdmissionOnConnection(ctx context.Context, connection *sql.Conn, state laneadmission.State, insert bool, now time.Time) error {
	if !state.Enabled || state.Validate() != nil {
		return ErrLaneAdmissionCorrupt
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode lane admission state: %w", err)
	}
	digest := sha256.Sum256(raw)
	if insert {
		if _, err := connection.ExecContext(ctx, `INSERT INTO lane_ingress_admission(singleton, state_json, state_sha256, updated_at) VALUES(1, ?, ?, ?)`, raw, digest[:], formatStoredTime(now)); err != nil {
			return fmt.Errorf("insert lane admission state: %w", err)
		}
		return nil
	}
	result, err := connection.ExecContext(ctx, `UPDATE lane_ingress_admission SET state_json = ?, state_sha256 = ?, updated_at = ? WHERE singleton = 1`, raw, digest[:], formatStoredTime(now))
	if err != nil {
		return fmt.Errorf("update lane admission state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrLaneAdmissionNotConfigured
	}
	return nil
}
