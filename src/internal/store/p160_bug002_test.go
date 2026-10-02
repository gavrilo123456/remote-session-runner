package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestBUG002RemoteStatusFailureMarkerPreservesAcceptedRunAndClearsWithTerminalProof(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug002-marker.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := &p019Clock{value: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	authority, err := NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	controller := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	input := p151RemoteRunIntent(t, "intent-bug002-marker", "job-bug002-marker", "session-bug002-marker", "command-bug002-marker", "key-bug002-marker", controller)
	if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentDispatching, "dispatching"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	accepted, err := authority.TransitionLocalIntent(ctx, input.IntentID, LocalIntentAccepted, "target_accepted")
	if err != nil {
		t.Fatal(err)
	}
	beforeLifecycle, err := authority.ListLocalIntentLifecycle(ctx, input.IntentID)
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Minute)
	marked, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, input.IntentID, RemoteStatusFailureCodeUnavailable)
	if err != nil {
		t.Fatal(err)
	}
	if marked.DeliveryState != LocalIntentAccepted || marked.Reason != accepted.Reason || !marked.UpdatedAt.Equal(accepted.UpdatedAt) ||
		marked.RemoteStatusFailureAt == nil || marked.RemoteStatusFailureCode != RemoteStatusFailureCodeUnavailable || !marked.RemoteStatusFailureAt.Equal(clock.Now()) {
		t.Fatalf("recorded status marker=%+v, accepted=%+v", marked, accepted)
	}
	afterFirstLifecycle, err := authority.ListLocalIntentLifecycle(ctx, input.IntentID)
	if err != nil || len(afterFirstLifecycle) != len(beforeLifecycle) {
		t.Fatalf("marker changed lifecycle=%+v err=%v, before=%+v", afterFirstLifecycle, err, beforeLifecycle)
	}
	firstObserved := *marked.RemoteStatusFailureAt

	clock.Advance(time.Hour)
	again, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, input.IntentID, RemoteStatusFailureCodeUnavailable)
	if err != nil || again.RemoteStatusFailureAt == nil || !again.RemoteStatusFailureAt.Equal(firstObserved) || !again.UpdatedAt.Equal(accepted.UpdatedAt) {
		t.Fatalf("repeated status marker=%+v err=%v, want first observation %s", again, err, firstObserved)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err = NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := authority.GetLocalIntent(ctx, input.IntentID)
	if err != nil || restarted.RemoteStatusFailureAt == nil || !restarted.RemoteStatusFailureAt.Equal(firstObserved) || restarted.RemoteStatusFailureCode != RemoteStatusFailureCodeUnavailable {
		t.Fatalf("restarted marker=%+v err=%v", restarted, err)
	}
	cleared, err := authority.ClearAcceptedRemoteRunStatusFailure(ctx, input.IntentID)
	if err != nil || cleared.RemoteStatusFailureAt != nil || cleared.RemoteStatusFailureCode != "" || cleared.DeliveryState != LocalIntentAccepted || !cleared.UpdatedAt.Equal(accepted.UpdatedAt) {
		t.Fatalf("cleared marker=%+v err=%v", cleared, err)
	}
	if _, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, input.IntentID, RemoteStatusFailureCodeUnavailable); err != nil {
		t.Fatal(err)
	}
	proven, err := authority.MarkRemoteIntentTerminalProof(ctx, input.IntentID, "strict_terminal_proof")
	if err != nil || proven.DeliveryState != LocalIntentReconciled || proven.RemoteStatusFailureAt != nil || proven.RemoteStatusFailureCode != "" || !HasRemoteTerminalProof(proven) {
		t.Fatalf("terminal proof did not clear status marker: %+v err=%v", proven, err)
	}

	notAccepted := p151RemoteRunIntent(t, "intent-bug002-not-accepted", "job-bug002-not-accepted", "session-bug002-not-accepted", "command-bug002-not-accepted", "key-bug002-not-accepted", controller)
	if _, err := authority.CreateLocalIntent(ctx, notAccepted); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, notAccepted.IntentID, RemoteStatusFailureCodeUnavailable); !errors.Is(err, ErrInvalidLocalIntent) {
		t.Fatalf("non-accepted run marker error=%v, want %v", err, ErrInvalidLocalIntent)
	}
	notRun := p151RemoteSubmitIntent(t, "intent-bug002-not-run", "session-bug002-not-run", "command-bug002-not-run", "key-bug002-not-run", controller)
	if _, err := authority.CreateLocalIntent(ctx, notRun); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, notRun.IntentID, LocalIntentDispatching, "dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, notRun.IntentID, LocalIntentAccepted, "target_accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkAcceptedRemoteRunStatusFailure(ctx, notRun.IntentID, RemoteStatusFailureCodeUnavailable); !errors.Is(err, ErrInvalidLocalIntent) {
		t.Fatalf("non-run marker error=%v, want %v", err, ErrInvalidLocalIntent)
	}
}

func TestBUG002MigrationRepairsLostIncompleteRecordsWithoutReplayingWork(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug002-v27.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetMaxOpenConns(1)
	for _, migration := range migrations[:27] {
		if _, err := legacy.ExecContext(ctx, migration.sql); err != nil {
			_ = legacy.Close()
			t.Fatalf("apply v%d migration: %v", migration.version, err)
		}
	}
	when := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	whenText := when.Format(time.RFC3339Nano)
	for _, migration := range migrations[:27] {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, migration.version, migration.name, migrationChecksum(migration), whenText); err != nil {
			_ = legacy.Close()
			t.Fatalf("record v%d migration: %v", migration.version, err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `PRAGMA user_version = 27`); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	hash := bytes.Repeat([]byte{0x42}, 32)
	scriptHash := bytes.Repeat([]byte{0x43}, 32)
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO exec_sessions (
  session_id, target_kind, target_profile, environment, controller_type, controller_id, source_mode, state,
  command_timeout_ns, idle_timeout_ns, session_max_lifetime_ns, output_bytes_per_command, created_at, updated_at, expires_at
) VALUES (?, 'remote', 'linux-host', 'linux-dev', 'queued_mac', 'tomasz.walczuk', 'empty', 'lost', ?, ?, ?, ?, ?, ?, ?)
`, "session-bug002-migration", int64(time.Minute), int64(time.Minute), int64(time.Hour), int64(1024), whenText, whenText, when.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy session: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO exec_commands (
  command_id, session_id, ordinal, request_hash_version, request_hash, script_bytes, script_sha256,
  state, timeout_ns, final_event_sequence, output_truncated, output_complete, created_at, updated_at
) VALUES (?, ?, 1, 1, ?, ?, ?, 'lost', ?, 2, 0, 0, ?, ?)
`, "command-bug002-migration", "session-bug002-migration", hash, []byte("exit"), scriptHash, int64(time.Minute), whenText, whenText); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy command: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO exec_jobs (
  job_id, session_id, command_id, controller_type, controller_id, target_kind, target_profile, environment, source_mode,
  request_hash_version, request_hash, idempotency_key, payload_json, script_bytes, script_sha256,
  phase, command_state, final_event_sequence, output_truncated, output_complete, teardown_state, teardown_reason, created_at, updated_at
) VALUES (?, ?, ?, 'queued_mac', 'tomasz.walczuk', 'remote', 'linux-host', 'linux-dev', 'empty',
  1, ?, 'key-bug002-migration', ?, ?, ?, 'lost', 'lost', 2, 0, 0, 'pending', '', ?, ?)
`, "job-bug002-migration", "session-bug002-migration", "command-bug002-migration", hash, []byte(`{"operation":"run"}`), []byte("exit"), scriptHash, whenText, whenText); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy job: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO local_remote_command_projections (
  command_id, session_id, ordinal, command_state, final_event_sequence, output_complete, output_truncated, target_kind, target_profile,
  controller_type, controller_id, environment, source_json, capabilities_json, observed_at
) VALUES (?, ?, 1, 'lost', 2, 0, 0, 'remote', 'linux-host', 'queued_mac', 'tomasz.walczuk', 'linux-dev', '{}', '{}', ?)
`, "command-bug002-projection", "session-bug002-projection", whenText); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy command projection: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO local_remote_job_projections (
  job_id, session_id, command_id, job_phase, command_state, final_event_sequence, output_complete, output_truncated,
  teardown_state, teardown_reason, target_kind, target_profile, controller_type, controller_id, environment, source_json, capabilities_json, observed_at
) VALUES (?, ?, ?, 'lost', 'lost', 2, 0, 0, 'pending', '', 'remote', 'linux-host', 'queued_mac', 'tomasz.walczuk', 'linux-dev', '{}', '{}', ?)
`, "job-bug002-projection", "session-bug002-projection", "command-bug002-projection", whenText); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy job projection: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v27 BUG-002 database: %v", err)
	}
	defer database.Close()
	if version := readUserVersion(t, database); version != CurrentSchemaVersion {
		t.Fatalf("schema version=%d, want %d", version, CurrentSchemaVersion)
	}
	for _, check := range []struct {
		query string
		want  []any
	}{
		{`SELECT output_unavailable_reason FROM exec_commands WHERE command_id = 'command-bug002-migration'`, []any{"capture_boundary_unconfirmed"}},
		{`SELECT output_unavailable_reason, teardown_state, teardown_reason, ingress FROM exec_jobs WHERE job_id = 'job-bug002-migration'`, []any{"capture_boundary_unconfirmed", "lost", "runtime_cleanup_unconfirmed", "unknown"}},
		{`SELECT output_unavailable_reason FROM local_remote_command_projections WHERE command_id = 'command-bug002-projection'`, []any{"capture_boundary_unconfirmed"}},
		{`SELECT output_unavailable_reason, teardown_state, teardown_reason, queue_blocked_reason FROM local_remote_job_projections WHERE job_id = 'job-bug002-projection'`, []any{"capture_boundary_unconfirmed", "lost", "runtime_cleanup_unconfirmed", ""}},
	} {
		values := make([]any, len(check.want))
		pointers := make([]any, len(check.want))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := database.QueryRowContext(ctx, check.query).Scan(pointers...); err != nil || !equalBug002Values(values, check.want) {
			t.Fatalf("migration query=%q values=%v err=%v, want %v", check.query, values, err, check.want)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO local_remote_status_failures(intent_id, first_observed_at, reason) VALUES ('intent-bug002-orphan', ?, 'remote_status_unavailable')`, whenText); err == nil {
		t.Fatal("orphan remote-status marker was accepted")
	}
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID int64
		var foreignKey int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation after migration: table=%s rowid=%d parent=%s fk=%d", table, rowID, parent, foreignKey)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func equalBug002Values(got, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		value, ok := got[i].(string)
		if !ok || value != want[i] {
			return false
		}
	}
	return true
}
