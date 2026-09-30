package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestP129OperationalMetricsReadBoundedDurableCounts(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), filepath.Join(root.Path(), "state", "p129-metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	stamp := formatStoredTime(now)
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO exec_sessions (
  session_id,target_kind,target_profile,environment,controller_type,controller_id,
  source_mode,source_repository_alias,source_requested_revision,source_path,
  source_resolved_revision,runtime_generation,state,command_timeout_ns,idle_timeout_ns,
  session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at
) VALUES ('session-p129','local','mac-workstation','mac-dev','local_user','tomasz.walczuk',
  'empty','','','','','generation-p129','ready',1000000000,2000000000,3000000000,4096,?,?,?)`, stamp, stamp, formatStoredTime(now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO exec_capacity_reservations(session_id,host_key,reserved_at) VALUES('session-p129','mac-workstation',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO exec_commands (
  command_id,session_id,ordinal,intent_ordinal,request_hash_version,request_hash,script_bytes,
  script_sha256,state,timeout_ns,exit_code,final_event_sequence,output_truncated,output_complete,created_at,updated_at
) VALUES ('command-p129','session-p129',1,NULL,1,zeroblob(32),X'6563686f',zeroblob(32),'queued',1000000000,NULL,NULL,0,0,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO exec_command_slots(command_id,host_key,reserved_at) VALUES('command-p129','mac-workstation',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO exec_command_events(command_id,sequence,event_type,payload,byte_count,occurred_at) VALUES('command-p129',1,'output_truncated',X'',0,?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO local_remote_command_projections (
  command_id,session_id,ordinal,command_state,exit_code,final_event_sequence,output_complete,output_truncated,
  output_unavailable_reason,target_kind,target_profile,controller_type,controller_id,environment,source_json,
  capabilities_json,observed_at,is_stale
) VALUES ('remote-command-p129','remote-session-p129',1,'lost',NULL,8,0,0,'','remote','linux-host',
  'queued_mac','tomasz.walczuk','linux-dev','{}','{}',?,0)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO local_remote_event_cursors(command_id,last_sequence,updated_at) VALUES('remote-command-p129',3,?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO local_remote_event_gaps(command_id,missing_from,missing_to,available_sequence,final_sequence,terminal_state,output_complete,output_unavailable_reason,confirmed_at)
VALUES ('remote-command-gap-p129',2,4,5,8,'lost',0,'remote_event_gap',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.CreateLocalIntent(context.Background(), p057SubmitIntent(t, "intent-p129", "session-p129", "command-intent-p129", "key-p129", 1, "echo p129"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE local_intents SET delivery_state='uncertain',attempt_count=3,updated_at=? WHERE intent_id=?`, formatStoredTime(now.Add(-10*time.Minute)), string(intent.IntentID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
	INSERT INTO mailbox_exchanges(exchange_id,mailbox_id,client_request_id,operation,controller_type,controller_id,client_idempotency_key,execution_idempotency_key,canonical_hash_version,
	  canonical_hash,canonical_payload,resource_id,request_state,created_at,updated_at)
	VALUES ('mbx-exchange-v1-64656661756c7400726571756573742d70313239','default','request-p129','run','direct_mtls','runner-test','key-p129',?,1,zeroblob(32),X'7b7d','job-p129','accepted',?,?)`, mailboxExecutionIdempotencyKey(DefaultMailboxID, "key-p129"), stamp, stamp); err != nil {
		t.Fatal(err)
	}

	// One transaction-engine failure and two runtime cleanup failures exercise
	// process-local failure counters without exposing the underlying error text.
	_, txErr := withImmediateTransaction(context.Background(), authority, func(ctx context.Context, connection *sql.Conn) (struct{}, error) {
		var value int
		return struct{}{}, connection.QueryRowContext(ctx, "SELECT absent_column FROM absent_table").Scan(&value)
	})
	if !IsSQLiteError(txErr) {
		t.Fatalf("injected database error=%v, want SQLite error", txErr)
	}
	authority.RecordCleanupFailure()
	authority.RecordCleanupFailure()

	metrics, err := authority.ReadOperationalMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := OperationalMetrics{
		ActiveSessionSlots: 1, ActiveCommandSlots: 1, QueuedCommands: 1, QueuedIntents: 1,
		DispatchAttemptsTotal: 3, ReconciliationAgeSeconds: 600, EventLagEvents: 5,
		EventGapsTotal: 1, OutputTruncationsTotal: 1, StorageErrorsTotal: 1,
		CleanupFailuresTotal: 2, MailboxBacklog: 1,
	}
	if metrics != want {
		t.Fatalf("operational metrics= %+v, want %+v", metrics, want)
	}
}
