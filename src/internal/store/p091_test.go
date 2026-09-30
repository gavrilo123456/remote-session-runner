package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP091MigrationPreservesPriorTerminalResponseAndDerivesItsDeadline(t *testing.T) {
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "p016.db")
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
	priorDB, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	priorDB.SetMaxOpenConns(1)
	for _, migration := range migrations[:16] {
		connection, err := priorDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			_ = connection.Close()
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(context.Background(), migration.sql); err != nil {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_ = connection.Close()
			t.Fatalf("apply prior migration %d: %v", migration.version, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(migration.sql)))
		if _, err := connection.ExecContext(context.Background(), `INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, migration.version, migration.name, checksum, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)); err != nil {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_ = connection.Close()
			t.Fatalf("record prior migration %d: %v", migration.version, err)
		}
		if _, err := connection.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", migration.version)); err != nil {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_ = connection.Close()
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(context.Background(), "COMMIT"); err != nil {
			_ = connection.Close()
			t.Fatal(err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	response := []byte(`{"request_id":"req-p091-migration","request_state":"complete","response_revision":1}`)
	responseHash := sha256.Sum256(response)
	requestHash := sha256.Sum256([]byte("p091-prior-request"))
	_, err = priorDB.Exec(`INSERT INTO mailbox_exchanges (
request_id, operation, controller_type, controller_id, idempotency_key,
canonical_hash_version, canonical_hash, canonical_payload, resource_id,
request_state, response_revision, terminal_response_bytes,
terminal_response_sha256, available_event_sequence, created_at, updated_at,
response_bytes, response_sha256, acknowledged_at
) VALUES (?, 'run', 'local_user', 'tomasz.walczuk', '', 1, ?, X'7b7d', '',
          'complete', 1, ?, ?, NULL, ?, ?, ?, ?, NULL)`,
		"req-p091-migration", requestHash[:], response, responseHash[:], formatStoredTime(base), formatStoredTime(base), response, responseHash[:])
	if err != nil {
		t.Fatal(err)
	}
	ackedResponse := []byte(`{"request_id":"req-p091-migration-ack","request_state":"complete","response_revision":1}`)
	ackedHash := sha256.Sum256(ackedResponse)
	ackTime := base.Add(12 * time.Hour)
	_, err = priorDB.Exec(`INSERT INTO mailbox_exchanges (
request_id, operation, controller_type, controller_id, idempotency_key,
canonical_hash_version, canonical_hash, canonical_payload, resource_id,
request_state, response_revision, terminal_response_bytes,
terminal_response_sha256, available_event_sequence, created_at, updated_at,
response_bytes, response_sha256, acknowledged_at
) VALUES (?, 'run', 'local_user', 'tomasz.walczuk', '', 1, ?, X'7b7d', '',
          'complete', 1, ?, ?, NULL, ?, ?, ?, ?, ?)`,
		"req-p091-migration-ack", requestHash[:], ackedResponse, ackedHash[:], formatStoredTime(base), formatStoredTime(base), ackedResponse, ackedHash[:], formatStoredTime(ackTime))
	if err != nil {
		t.Fatal(err)
	}
	if err := priorDB.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return base.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	record, err := authority.GetMailboxExchange(context.Background(), "req-p091-migration")
	wantDeadline := base.Add(MailboxUnackedResponseLifetime)
	if err != nil || record.State != MailboxExchangeComplete || string(record.TerminalResponseBytes) != string(response) || record.ResponseCleanupAt == nil || !record.ResponseCleanupAt.Equal(wantDeadline) {
		t.Fatalf("migrated terminal response=%+v err=%v, want cleanup deadline %s", record, err, wantDeadline)
	}
	ackedRecord, err := authority.GetMailboxExchange(context.Background(), "req-p091-migration-ack")
	wantAckDeadline := ackTime.Add(MailboxAckedResponseLifetime)
	if err != nil || ackedRecord.AcknowledgedAt == nil || !ackedRecord.AcknowledgedAt.Equal(ackTime) || ackedRecord.ResponseCleanupAt == nil || !ackedRecord.ResponseCleanupAt.Equal(wantAckDeadline) {
		t.Fatalf("migrated acknowledged response=%+v err=%v, want cleanup deadline %s", ackedRecord, err, wantAckDeadline)
	}

	var rawCleanupDeadline sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT response_cleanup_at FROM mailbox_exchanges WHERE mailbox_id = 'default' AND client_request_id = ?`, "req-p091-migration").Scan(&rawCleanupDeadline); err != nil {
		t.Fatal(err)
	}
	if rawCleanupDeadline.Valid {
		t.Fatalf("migration unexpectedly persisted cleanup deadline %q", rawCleanupDeadline.String)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	publishable, err := authority.ListPublishableTerminalMailboxExchanges(context.Background(), controller)
	if err != nil {
		t.Fatal(err)
	}
	gotPublishable := map[string]bool{}
	for _, exchange := range publishable {
		gotPublishable[exchange.RequestID] = true
	}
	for _, requestID := range []string{"req-p091-migration", "req-p091-migration-ack"} {
		if !gotPublishable[requestID] {
			t.Fatalf("publishable historical records=%v, missing %s", gotPublishable, requestID)
		}
	}
}

func TestP091AckMovesDeadlineEarlierAndLateAckCannotExtendIt(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	db, err := Open(ctx, filepath.Join(root.Path(), "state", "p091-mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"req-p091-early", "req-p091-late"} {
		payload := p082RunPayload(t, requestID, "key-"+requestID, "echo p091")
		hash, err := domain.HashMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := authority.AcceptMailboxExchange(ctx, MailboxExchangeCreate{RequestID: requestID, Operation: "run", Controller: controller, IdempotencyKey: "key-" + requestID, RequestHash: hash, CanonicalPayload: canonical}); err != nil {
			t.Fatal(err)
		}
		response := []byte(`{"request_id":"` + requestID + `","request_state":"complete","response_revision":1}`)
		if _, err := authority.PublishMailboxResponse(ctx, requestID, MailboxResponsePublication{State: MailboxExchangeComplete, Bytes: response}); err != nil {
			t.Fatal(err)
		}
	}
	base := now
	ackTime := base.Add(time.Hour)
	now = ackTime
	acked, err := authority.AcknowledgeMailboxExchange(ctx, MailboxAcknowledgement{RequestID: "req-p091-early", ResponseRevision: 1})
	if err != nil || acked.AcknowledgedAt == nil || acked.ResponseCleanupAt == nil {
		t.Fatalf("early ACK=%+v err=%v", acked, err)
	}
	wantEarlyDeadline := ackTime.Add(MailboxAckedResponseLifetime)
	if !acked.ResponseCleanupAt.Equal(wantEarlyDeadline) {
		t.Fatalf("early cleanup deadline=%s, want %s", acked.ResponseCleanupAt, wantEarlyDeadline)
	}

	now = base.Add(8 * 24 * time.Hour)
	late, err := authority.AcknowledgeMailboxExchange(ctx, MailboxAcknowledgement{RequestID: "req-p091-late", ResponseRevision: 1})
	if err != nil || late.AcknowledgedAt == nil || late.ResponseCleanupAt == nil {
		t.Fatalf("late ACK=%+v err=%v", late, err)
	}
	wantLateDeadline := base.Add(MailboxUnackedResponseLifetime)
	if !late.ResponseCleanupAt.Equal(wantLateDeadline) {
		t.Fatalf("late ACK moved cleanup deadline to %s, want original %s", late.ResponseCleanupAt, wantLateDeadline)
	}
	if err := authority.EnsureMailboxResponsePublishable(ctx, "req-p091-late"); !errors.Is(err, ErrMailboxResponseExpired) {
		t.Fatalf("late response republish error=%v, want ErrMailboxResponseExpired", err)
	}
	claimed, err := authority.ClaimMailboxResponsesForCleanup(ctx)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("expired responses=%v err=%v", claimed, err)
	}
	for _, requestID := range claimed {
		record, err := authority.GetMailboxExchange(ctx, requestID)
		if err != nil || record.ResponseCleanupStartedAt == nil {
			t.Fatalf("cleanup claim %s=%+v err=%v", requestID, record, err)
		}
	}
}
