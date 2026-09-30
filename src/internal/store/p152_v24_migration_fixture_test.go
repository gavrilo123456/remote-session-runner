package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

type p152V24ExchangeWant struct {
	Ref                      MailboxExchangeRef
	ResourceID               string
	CommandID                string
	LegacyKey                string
	At                       time.Time
	AcknowledgedAt           *time.Time
	ResponseCleanupStartedAt *time.Time
	ResponseFileRemovedAt    *time.Time
}

// TestP152MigratesActualV24MailboxNamespaceArtifacts builds a complete v24
// database using the historical migrations. The fixture retains both kinds of
// event projection so migration 25 has to preserve the parent exchange, ACK,
// event references, and cleanup rows while introducing the default namespace.
func TestP152MigratesActualV24MailboxNamespaceArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "authority.db")
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
	p152BuildActualV24Database(t, ctx, legacy)

	base := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	localCommandID := "command-p152-v24-local"
	remoteCommandID := "command-p152-v24-remote"
	acknowledgedAt := base.Add(5 * time.Minute)
	remoteResponseCleanupStartedAt := base.Add(2 * time.Hour)
	remoteResponseFileRemovedAt := remoteResponseCleanupStartedAt.Add(time.Minute)
	localCleanupStartedAt := base.Add(3 * time.Hour)
	remoteCleanupStartedAt := base.Add(4 * time.Hour)
	remoteFileRemovedAt := remoteCleanupStartedAt.Add(time.Minute)
	localExchange := p152V24ExchangeWant{
		Ref:            MailboxExchangeRef{MailboxID: DefaultMailboxID, ClientRequestID: "request-p152-v24-local"},
		ResourceID:     localCommandID,
		CommandID:      localCommandID,
		LegacyKey:      "legacy-client-key-local-p152",
		At:             base,
		AcknowledgedAt: &acknowledgedAt,
	}
	remoteExchange := p152V24ExchangeWant{
		Ref:                      MailboxExchangeRef{MailboxID: DefaultMailboxID, ClientRequestID: "request-p152-v24-remote"},
		ResourceID:               remoteCommandID,
		CommandID:                remoteCommandID,
		LegacyKey:                "legacy-client-key-remote-p152",
		At:                       base,
		ResponseCleanupStartedAt: &remoteResponseCleanupStartedAt,
		ResponseFileRemovedAt:    &remoteResponseFileRemovedAt,
	}

	p152InsertV24LocalCommand(t, ctx, legacy, "session-p152-v24-local", localCommandID, base)
	p152InsertV24RemoteCommand(t, ctx, legacy, remoteCommandID, base)
	p152InsertV24TerminalExchange(t, ctx, legacy, localExchange)
	p152InsertV24TerminalExchange(t, ctx, legacy, remoteExchange)
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_event_file_references(request_id, command_id, created_at)
VALUES (?, ?, ?)
	`, localExchange.Ref.ClientRequestID, localCommandID, formatStoredTime(base.Add(time.Minute))); err != nil {
		t.Fatalf("insert v24 local event reference: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_remote_event_file_references(request_id, command_id, created_at)
VALUES (?, ?, ?)
	`, remoteExchange.Ref.ClientRequestID, remoteCommandID, formatStoredTime(base.Add(2*time.Minute))); err != nil {
		t.Fatalf("insert v24 remote event reference: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_event_file_cleanup(command_id, cleanup_started_at, file_removed_at)
VALUES (?, ?, NULL)
`, localCommandID, formatStoredTime(localCleanupStartedAt)); err != nil {
		t.Fatalf("insert v24 local event cleanup: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_remote_event_file_cleanup(command_id, cleanup_started_at, file_removed_at)
VALUES (?, ?, ?)
`, remoteCommandID, formatStoredTime(remoteCleanupStartedAt), formatStoredTime(remoteFileRemovedAt)); err != nil {
		t.Fatalf("insert v24 remote event cleanup: %v", err)
	}
	p152AssertNoForeignKeyViolations(t, ctx, legacy)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate actual v24 mailbox database: %v", err)
	}
	defer db.Close()
	if version := readUserVersion(t, db); version != CurrentSchemaVersion {
		t.Fatalf("migrated schema version=%d, want %d", version, CurrentSchemaVersion)
	}
	p152AssertNoForeignKeyViolations(t, ctx, db)

	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return base.Add(6 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	localRecord, err := authority.GetMailboxExchangeInMailbox(ctx, localExchange.Ref)
	if err != nil {
		t.Fatalf("read migrated local exchange: %v", err)
	}
	remoteRecord, err := authority.GetMailboxExchangeInMailbox(ctx, remoteExchange.Ref)
	if err != nil {
		t.Fatalf("read migrated remote exchange: %v", err)
	}

	p152AssertMigratedV24Exchange(t, localRecord, localExchange)
	p152AssertMigratedV24Exchange(t, remoteRecord, remoteExchange)
	p152AssertMigratedV24EventReference(t, ctx, db, "mailbox_event_file_references", localExchange.Ref, localCommandID, formatStoredTime(base.Add(time.Minute)))
	p152AssertMigratedV24EventReference(t, ctx, db, "mailbox_remote_event_file_references", remoteExchange.Ref, remoteCommandID, formatStoredTime(base.Add(2*time.Minute)))
	p152AssertMigratedV24Cleanup(t, ctx, db, "mailbox_event_file_cleanup", localCommandID, localCleanupStartedAt, nil)
	p152AssertMigratedV24Cleanup(t, ctx, db, "mailbox_remote_event_file_cleanup", remoteCommandID, remoteCleanupStartedAt, &remoteFileRemovedAt)

	var ledgerCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runner_schema_migrations`).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != CurrentSchemaVersion {
		t.Fatalf("migration ledger count=%d, want %d", ledgerCount, CurrentSchemaVersion)
	}
}

func p152BuildActualV24Database(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, migration := range migrations[:24] {
		if _, err := db.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply historical migration %d (%s): %v", migration.version, migration.name, err)
		}
	}
	appliedAt := formatStoredTime(time.Date(2026, 9, 30, 7, 0, 0, 0, time.UTC))
	for _, migration := range migrations[:24] {
		if _, err := db.ExecContext(ctx, `
INSERT INTO runner_schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, ?)
`, migration.version, migration.name, migrationChecksum(migration), appliedAt); err != nil {
			t.Fatalf("record historical migration %d (%s): %v", migration.version, migration.name, err)
		}
	}
	if _, err := db.ExecContext(ctx, "PRAGMA user_version = 24"); err != nil {
		t.Fatal(err)
	}
	if version := readUserVersion(t, db); version != 24 {
		t.Fatalf("fixture schema version=%d, want 24", version)
	}
}

func p152InsertV24LocalCommand(t *testing.T, ctx context.Context, db *sql.DB, sessionID, commandID string, at time.Time) {
	t.Helper()
	stamp := formatStoredTime(at)
	if _, err := db.ExecContext(ctx, `
INSERT INTO exec_sessions (
  session_id,target_kind,target_profile,environment,controller_type,controller_id,
  source_mode,source_repository_alias,source_requested_revision,source_path,
  source_resolved_revision,runtime_generation,state,command_timeout_ns,idle_timeout_ns,
  session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at
) VALUES (?, 'local', 'mac-workstation', 'mac-dev', 'local_user', 'tomasz.walczuk',
  'empty', '', '', '', '', 'p152-v24', 'ready', 1000000000, 2000000000,
  3000000000, 4096, ?, ?, ?)
`, sessionID, stamp, stamp, formatStoredTime(at.Add(time.Hour))); err != nil {
		t.Fatalf("insert v24 local session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO exec_commands (
  command_id,session_id,ordinal,intent_ordinal,request_hash_version,request_hash,script_bytes,
  script_sha256,state,timeout_ns,exit_code,final_event_sequence,output_truncated,output_complete,created_at,updated_at
) VALUES (?, ?, 1, NULL, 1, zeroblob(32), X'6563686f', zeroblob(32),
  'succeeded', 1000000000, 0, 4, 0, 1, ?, ?)
`, commandID, sessionID, stamp, stamp); err != nil {
		t.Fatalf("insert v24 local command: %v", err)
	}
}

func p152InsertV24RemoteCommand(t *testing.T, ctx context.Context, db *sql.DB, commandID string, at time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
INSERT INTO local_remote_command_projections (
  command_id,session_id,ordinal,command_state,exit_code,final_event_sequence,output_complete,output_truncated,
  output_unavailable_reason,target_kind,target_profile,controller_type,controller_id,environment,source_json,
  capabilities_json,observed_at,is_stale
) VALUES (?, 'session-p152-v24-remote', 1, 'succeeded', 0, 4, 1, 0,
  '', 'remote', 'linux-host', 'queued_mac', 'tomasz.walczuk', 'linux-dev', '{}', '{}', ?, 0)
`, commandID, formatStoredTime(at)); err != nil {
		t.Fatalf("insert v24 remote command projection: %v", err)
	}
}

func p152InsertV24TerminalExchange(t *testing.T, ctx context.Context, db *sql.DB, want p152V24ExchangeWant) {
	t.Helper()
	response := []byte(fmt.Sprintf(`{"request_id":%q,"request_state":"complete","response_revision":1}`, want.Ref.ClientRequestID))
	responseHash := sha256.Sum256(response)
	requestHash := sha256.Sum256([]byte("p152-v24-request-" + want.Ref.ClientRequestID))
	if _, err := db.ExecContext(ctx, `
INSERT INTO mailbox_exchanges (
  request_id,operation,controller_type,controller_id,idempotency_key,
  canonical_hash_version,canonical_hash,canonical_payload,resource_id,
  request_state,response_revision,terminal_response_bytes,terminal_response_sha256,
  available_event_sequence,created_at,updated_at,response_bytes,response_sha256,
  acknowledged_at,response_cleanup_at,response_cleanup_started_at,response_file_removed_at,
  idempotency_key_expires_at,idempotency_binding_active,deduplication_warning
) VALUES (?, 'run', 'local_user', 'tomasz.walczuk', ?, 1, ?, X'7b7d', ?,
	  'complete', 1, ?, ?, 4, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0)
	`, want.Ref.ClientRequestID, want.LegacyKey, requestHash[:], want.ResourceID, response, responseHash[:],
		formatStoredTime(want.At), formatStoredTime(want.At), response, responseHash[:],
		p152StoredTime(want.AcknowledgedAt), formatStoredTime(want.At.Add(MailboxUnackedResponseLifetime)),
		p152StoredTime(want.ResponseCleanupStartedAt), p152StoredTime(want.ResponseFileRemovedAt),
		formatStoredTime(want.At.Add(90*24*time.Hour))); err != nil {
		t.Fatalf("insert v24 terminal exchange %s: %v", want.Ref.ClientRequestID, err)
	}
}

func p152StoredTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatStoredTime(*value)
}

func p152AssertMigratedV24Exchange(t *testing.T, record MailboxExchangeRecord, want p152V24ExchangeWant) {
	t.Helper()
	if record.MailboxID != DefaultMailboxID || record.ExchangeID != mailboxExchangeID(want.Ref) || record.RequestID != want.Ref.ClientRequestID {
		t.Fatalf("migrated exchange identity=%+v, want mailbox=%q exchange=%q request=%q", record, DefaultMailboxID, mailboxExchangeID(want.Ref), want.Ref.ClientRequestID)
	}
	if record.Operation != "run" || record.Controller.Type() != domain.ControllerTypeLocalUser || record.Controller.ID() != "tomasz.walczuk" {
		t.Fatalf("migrated operation/controller=%q/%s/%s, want run/local_user/tomasz.walczuk", record.Operation, record.Controller.Type(), record.Controller.ID())
	}
	if record.IdempotencyKey != want.LegacyKey || record.ExecutionIdempotencyKey != want.LegacyKey {
		t.Fatalf("migrated exchange keys client=%q execution=%q, want exact legacy key %q", record.IdempotencyKey, record.ExecutionIdempotencyKey, want.LegacyKey)
	}
	wantRequestHash := sha256.Sum256([]byte("p152-v24-request-" + want.Ref.ClientRequestID))
	if record.RequestHash.Version() != domain.CanonicalizationVersionV1 || !bytes.Equal(record.RequestHash.SHA256(), wantRequestHash[:]) || !bytes.Equal(record.CanonicalPayload, []byte("{}")) {
		t.Fatalf("migrated request identity hash=%v payload=%q", record.RequestHash, record.CanonicalPayload)
	}
	wantResponse := []byte(fmt.Sprintf(`{"request_id":%q,"request_state":"complete","response_revision":1}`, want.Ref.ClientRequestID))
	wantResponseHash := sha256.Sum256(wantResponse)
	if record.ResourceID != want.ResourceID || record.State != MailboxExchangeComplete || record.ResponseRevision != 1 || record.EventFileCommandID != want.CommandID ||
		!bytes.Equal(record.ResponseBytes, wantResponse) || !bytes.Equal(record.ResponseSHA256, wantResponseHash[:]) ||
		!bytes.Equal(record.TerminalResponseBytes, wantResponse) || !bytes.Equal(record.TerminalResponseSHA256, wantResponseHash[:]) ||
		record.AvailableEventSequence == nil || *record.AvailableEventSequence != 4 {
		t.Fatalf("migrated terminal response state=%+v, want resource=%q command=%q", record, want.ResourceID, want.CommandID)
	}
	if !record.CreatedAt.Equal(want.At) || !record.UpdatedAt.Equal(want.At) || !record.IdempotencyBindingActive || record.DeduplicationWarning {
		t.Fatalf("migrated retained metadata=%+v", record)
	}
	cleanupAt := want.At.Add(MailboxUnackedResponseLifetime)
	keyExpiresAt := want.At.Add(90 * 24 * time.Hour)
	p152AssertOptionalTime(t, "acknowledged_at", record.AcknowledgedAt, want.AcknowledgedAt)
	p152AssertOptionalTime(t, "response_cleanup_at", record.ResponseCleanupAt, &cleanupAt)
	p152AssertOptionalTime(t, "response_cleanup_started_at", record.ResponseCleanupStartedAt, want.ResponseCleanupStartedAt)
	p152AssertOptionalTime(t, "response_file_removed_at", record.ResponseFileRemovedAt, want.ResponseFileRemovedAt)
	p152AssertOptionalTime(t, "idempotency_key_expires_at", record.IdempotencyKeyExpiresAt, &keyExpiresAt)
}

func p152AssertOptionalTime(t *testing.T, name string, got, want *time.Time) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("migrated %s=%v, want %v", name, got, want)
		}
		return
	}
	if !got.Equal(*want) {
		t.Fatalf("migrated %s=%s, want %s", name, got, want)
	}
}

func p152AssertMigratedV24EventReference(t *testing.T, ctx context.Context, db *sql.DB, table string, ref MailboxExchangeRef, wantCommandID, wantCreatedAt string) {
	t.Helper()
	var exchangeID, commandID, createdAt string
	if err := db.QueryRowContext(ctx, `SELECT exchange_id, command_id, created_at FROM `+table+` WHERE exchange_id = ?`, mailboxExchangeID(ref)).Scan(&exchangeID, &commandID, &createdAt); err != nil {
		t.Fatalf("read migrated %s: %v", table, err)
	}
	if exchangeID != mailboxExchangeID(ref) || commandID != wantCommandID || createdAt != wantCreatedAt {
		t.Fatalf("migrated %s=(%q,%q,%q), want (%q,%q,%q)", table, exchangeID, commandID, createdAt, mailboxExchangeID(ref), wantCommandID, wantCreatedAt)
	}
}

func p152AssertMigratedV24Cleanup(t *testing.T, ctx context.Context, db *sql.DB, table, commandID string, wantStartedAt time.Time, wantRemovedAt *time.Time) {
	t.Helper()
	var mailboxID, gotCommandID, startedAt string
	var removedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT mailbox_id, command_id, cleanup_started_at, file_removed_at FROM `+table+` WHERE mailbox_id = ? AND command_id = ?`, DefaultMailboxID, commandID).Scan(&mailboxID, &gotCommandID, &startedAt, &removedAt); err != nil {
		t.Fatalf("read migrated %s: %v", table, err)
	}
	if mailboxID != DefaultMailboxID || gotCommandID != commandID || startedAt != formatStoredTime(wantStartedAt) {
		t.Fatalf("migrated %s=(%q,%q,%q), want (%q,%q,%q)", table, mailboxID, gotCommandID, startedAt, DefaultMailboxID, commandID, formatStoredTime(wantStartedAt))
	}
	if wantRemovedAt == nil {
		if removedAt.Valid {
			t.Fatalf("migrated %s file_removed_at=%q, want NULL", table, removedAt.String)
		}
		return
	}
	if !removedAt.Valid || removedAt.String != formatStoredTime(*wantRemovedAt) {
		t.Fatalf("migrated %s file_removed_at=%q valid=%t, want %s", table, removedAt.String, removedAt.Valid, formatStoredTime(*wantRemovedAt))
	}
}

func p152AssertNoForeignKeyViolations(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign-key violation table=%s row=%v parent=%s foreign_key=%d", table, rowID, parent, foreignKeyID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
