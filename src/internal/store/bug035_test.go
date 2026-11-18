package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestBUG035PolicyCandidatesExcludeTerminalHistoryBeforeDecode(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	seed := p019ReadySession(t, authority, "session-bug035-active-c", "key-bug035-seed")
	// The historical rows have valid terminal states but deliberately invalid
	// timestamps. Decoding even one of them must fail, so success proves the
	// state filter runs before the full-row reader; no elapsed-time assertion
	// depends on the host's available CPU.
	if _, err := authority.db.ExecContext(ctx, `
WITH RECURSIVE history(n) AS (
    SELECT 0 UNION ALL SELECT n + 1 FROM history WHERE n < 511
)
INSERT INTO exec_sessions (
    session_id, target_kind, target_profile, environment, controller_type, controller_id,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    source_resolved_revision, runtime_generation, state, command_timeout_ns, idle_timeout_ns,
    session_max_lifetime_ns, output_bytes_per_command, created_at, updated_at, expires_at
)
SELECT 'session-bug035-history-' || printf('%04d', n),
    target_kind, target_profile, environment, controller_type, controller_id,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    source_resolved_revision, runtime_generation,
    CASE n % 4 WHEN 0 THEN 'closed' WHEN 1 THEN 'expired' WHEN 2 THEN 'failed' ELSE 'lost' END,
    command_timeout_ns, idle_timeout_ns, session_max_lifetime_ns, output_bytes_per_command,
    created_at, 'not-a-terminal-timestamp', expires_at
FROM exec_sessions CROSS JOIN history WHERE session_id = ?`, string(seed)); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		id    string
		state domain.SessionState
		delta time.Duration
	}{
		{"session-bug035-active-b", domain.SessionStateCreating, -time.Minute},
		{"session-bug035-active-a", domain.SessionStateRequested, -time.Minute},
		{"session-bug035-active-e", domain.SessionStateClosing, time.Minute},
		{"session-bug035-active-d", domain.SessionStateBusy, time.Minute},
	} {
		if _, err := authority.db.ExecContext(ctx, `
INSERT INTO exec_sessions (
    session_id, target_kind, target_profile, environment, controller_type, controller_id,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    source_resolved_revision, runtime_generation, state, command_timeout_ns, idle_timeout_ns,
    session_max_lifetime_ns, output_bytes_per_command, created_at, updated_at, expires_at
)
SELECT ?, target_kind, target_profile, environment, controller_type, controller_id,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    source_resolved_revision, runtime_generation, ?, command_timeout_ns, idle_timeout_ns,
    session_max_lifetime_ns, output_bytes_per_command, ?, updated_at, expires_at
FROM exec_sessions WHERE session_id = ?`, candidate.id, string(candidate.state),
			formatStoredTime(clock.value.Add(candidate.delta)), string(seed)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := authority.ListNonterminalSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []domain.SessionID
	var states []domain.SessionState
	for _, session := range got {
		ids = append(ids, session.SessionID)
		states = append(states, session.State)
	}
	if want := []domain.SessionID{"session-bug035-active-a", "session-bug035-active-b", seed, "session-bug035-active-d", "session-bug035-active-e"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("policy candidates = %v, want deterministic order %v", ids, want)
	}
	if want := []domain.SessionState{domain.SessionStateRequested, domain.SessionStateCreating, domain.SessionStateReady, domain.SessionStateBusy, domain.SessionStateClosing}; !reflect.DeepEqual(states, want) {
		t.Fatalf("policy candidate states = %v, want %v", states, want)
	}
	if _, err := authority.ListSessions(ctx); !errors.Is(err, ErrSessionLifecycle) {
		t.Fatalf("full historical inventory error = %v, want unchanged full-row validation", err)
	}
	var total, historical int
	if err := authority.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(updated_at = 'not-a-terminal-timestamp') FROM exec_sessions`).Scan(&total, &historical); err != nil || total != 517 || historical != 512 {
		t.Fatalf("retained rows = %d, historical rows = %d, err = %v", total, historical, err)
	}
	rows, err := authority.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+listNonterminalSessionsQuery,
		string(domain.SessionStateRequested), string(domain.SessionStateCreating), string(domain.SessionStateReady), string(domain.SessionStateBusy), string(domain.SessionStateClosing))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "USING INDEX ix_exec_sessions_state")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("policy candidate query did not use the existing session-state index")
	}
}

func TestBUG035PolicyCandidateReadPreservesCancellationAndValidation(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	id := p019ReadySession(t, authority, "session-bug035-validation", "key-bug035-validation")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authority.ListNonterminalSessions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled candidate read = %v, want context.Canceled", err)
	}
	if _, err := authority.db.ExecContext(context.Background(), "UPDATE exec_sessions SET updated_at = 'not-an-active-timestamp' WHERE session_id = ?", string(id)); err != nil {
		t.Fatal(err)
	}
	if got, err := authority.ListNonterminalSessions(context.Background()); !errors.Is(err, ErrSessionLifecycle) || got != nil {
		t.Fatalf("corrupt active candidate = %v, err = %v, want validation failure without a partial list", got, err)
	}
	if err := authority.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ListNonterminalSessions(context.Background()); err == nil {
		t.Fatal("closed database candidate read succeeded")
	}
}
