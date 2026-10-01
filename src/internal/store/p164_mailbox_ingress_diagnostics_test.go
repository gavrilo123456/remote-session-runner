package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

type p164Clock struct{ value time.Time }

func (c *p164Clock) Now() time.Time              { return c.value }
func (c *p164Clock) Advance(value time.Duration) { c.value = c.value.Add(value) }

func TestP164MailboxIngressDiagnosticMigrationFreshAndFromV29(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	fresh, err := Open(ctx, filepath.Join(root.Path(), "state", "p164-fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if version := readUserVersion(t, fresh); version != CurrentSchemaVersion {
		_ = fresh.Close()
		t.Fatalf("fresh schema version=%d, want current %d", version, CurrentSchemaVersion)
	}
	if CurrentSchemaVersion != 31 {
		_ = fresh.Close()
		t.Fatalf("CurrentSchemaVersion=%d, want 31", CurrentSchemaVersion)
	}
	var migrationName string
	if err := fresh.QueryRowContext(ctx, `SELECT name FROM runner_schema_migrations WHERE version = 30`).Scan(&migrationName); err != nil || migrationName != "mailbox_ingress_diagnostics" {
		_ = fresh.Close()
		t.Fatalf("fresh migration 30=%q err=%v", migrationName, err)
	}
	if err := fresh.QueryRowContext(ctx, `SELECT name FROM runner_schema_migrations WHERE version = 31`).Scan(&migrationName); err != nil || migrationName != "exec_job_ingress" {
		_ = fresh.Close()
		t.Fatalf("fresh migration 31=%q err=%v", migrationName, err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	legacyPath := filepath.Join(root.Path(), "state", "p164-v29.db")
	if err := p164BuildV29Database(ctx, legacyPath); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, legacyPath)
	if err != nil {
		t.Fatalf("migrate v29 database: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if version := readUserVersion(t, migrated); version != 31 {
		t.Fatalf("migrated schema version=%d, want 31", version)
	}
	var columnCount int
	if err := migrated.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('mailbox_ingress_diagnostics')`).Scan(&columnCount); err != nil || columnCount != 14 {
		t.Fatalf("mailbox ingress diagnostic columns=%d err=%v, want 14", columnCount, err)
	}
}

func TestP164MailboxIngressDiagnosticFreezesSanitizedRecordAndReservesRequestID(t *testing.T) {
	ctx := context.Background()
	clock := &p164Clock{value: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	authority, database := p164Authority(t, clock)
	t.Cleanup(func() { _ = database.Close() })
	ref, err := NewMailboxIngressDiagnosticRef("slidestud-io", "req-p164-malformed")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"request_id":"req-p164-malformed","idempotency_key":"CANARY-IDEMPOTENCY-DO-NOT-PERSIST","operation":"run","script":"CANARY-SCRIPT-DO-NOT-PERSIST"}`)
	fingerprint := sha256.Sum256(raw)
	record, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, fingerprint, MailboxIngressDiagnosticInvalidRequestSchema)
	if err != nil || disposition != MailboxIngressDiagnosticCreated {
		t.Fatalf("create diagnostic record=%+v disposition=%q err=%v", record, disposition, err)
	}
	if record.Code != MailboxIngressDiagnosticInvalidRequestSchema || record.DiagnosticRevision != 1 || !record.ObservedAt.Equal(clock.Now()) || !record.DiagnosticCleanupAt.Equal(clock.Now().Add(MailboxIngressDiagnosticArtifactLifetime)) {
		t.Fatalf("created diagnostic record=%+v", record)
	}
	var wire mailboxIngressDiagnosticWire
	if err := json.Unmarshal(record.DiagnosticBytes, &wire); err != nil || wire.InboxID != ref.MailboxID || wire.RequestID != ref.ClientRequestID || wire.Code != record.Code || wire.Message != "request does not satisfy the mailbox request format" || wire.Accepted || wire.Executed || wire.LifecyclePhase != "ingress_validation" {
		t.Fatalf("sanitized diagnostic JSON=%s wire=%+v err=%v", record.DiagnosticBytes, wire, err)
	}
	for _, canary := range [][]byte{[]byte("CANARY-IDEMPOTENCY-DO-NOT-PERSIST"), []byte("CANARY-SCRIPT-DO-NOT-PERSIST"), []byte(`"operation":"run"`)} {
		if bytes.Contains(record.DiagnosticBytes, canary) {
			t.Fatalf("frozen diagnostic retained raw request material %q: %s", canary, record.DiagnosticBytes)
		}
	}
	var storedBytes []byte
	var storedFingerprint, storedChecksum []byte
	if err := database.QueryRowContext(ctx, `SELECT diagnostic_bytes, request_sha256, diagnostic_sha256 FROM mailbox_ingress_diagnostics WHERE mailbox_id = ? AND client_request_id = ?`, ref.MailboxID, ref.ClientRequestID).Scan(&storedBytes, &storedFingerprint, &storedChecksum); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedBytes, record.DiagnosticBytes) || len(storedFingerprint) != sha256.Size || len(storedChecksum) != sha256.Size {
		t.Fatalf("stored diagnostic fields are incomplete: bytes=%q fingerprint=%d checksum=%d", storedBytes, len(storedFingerprint), len(storedChecksum))
	}
	for _, canary := range [][]byte{[]byte("CANARY-IDEMPOTENCY-DO-NOT-PERSIST"), []byte("CANARY-SCRIPT-DO-NOT-PERSIST"), []byte(`"operation":"run"`)} {
		if bytes.Contains(storedBytes, canary) {
			t.Fatalf("database diagnostic retained raw request material %q", canary)
		}
	}

	clock.Advance(time.Hour)
	replayed, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, fingerprint, MailboxIngressDiagnosticMalformedJSON)
	if err != nil || disposition != MailboxIngressDiagnosticSameFingerprint || replayed.Code != record.Code || !replayed.ObservedAt.Equal(record.ObservedAt) || !bytes.Equal(replayed.DiagnosticBytes, record.DiagnosticBytes) {
		t.Fatalf("same fingerprint replay=%+v disposition=%q err=%v, original=%+v", replayed, disposition, err, record)
	}
	changed := sha256.Sum256([]byte("different-safe-bounded-raw-request"))
	reused, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, changed, MailboxIngressDiagnosticMalformedJSON)
	if err != nil || disposition != MailboxIngressDiagnosticRequestIDReused || reused.Code != record.Code || reused.RequestSHA256 != fingerprint || !bytes.Equal(reused.DiagnosticBytes, record.DiagnosticBytes) {
		t.Fatalf("changed fingerprint reuse=%+v disposition=%q err=%v", reused, disposition, err)
	}
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, ref, fingerprint); !errors.Is(err, ErrMailboxIngressDiagnosticCleanupState) {
		t.Fatalf("input cleanup before projection error=%v, want %v", err, ErrMailboxIngressDiagnosticCleanupState)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, ref, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, ref, fingerprint); err != nil {
		t.Fatal(err)
	}
	sameAfterRemoval, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, fingerprint, MailboxIngressDiagnosticInvalidRequestSchema)
	if err != nil || disposition != MailboxIngressDiagnosticRequestIDReused || !bytes.Equal(sameAfterRemoval.DiagnosticBytes, record.DiagnosticBytes) {
		t.Fatalf("same fingerprint after pair removal=%+v disposition=%q err=%v", sameAfterRemoval, disposition, err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_ingress_diagnostics WHERE mailbox_id = ? AND client_request_id = ?`, ref.MailboxID, ref.ClientRequestID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("frozen ledger row count=%d err=%v, want 1", count, err)
	}
}

func TestP164MailboxIngressDiagnosticLifecycleAndFrozenCore(t *testing.T) {
	ctx := context.Background()
	clock := &p164Clock{value: time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)}
	authority, database := p164Authority(t, clock)
	t.Cleanup(func() { _ = database.Close() })
	ref, err := NewMailboxIngressDiagnosticRef("analytics", "req-p164-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("malformed-p164-lifecycle"))
	record, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, fingerprint, MailboxIngressDiagnosticMalformedJSON)
	if err != nil || disposition != MailboxIngressDiagnosticCreated {
		t.Fatalf("create lifecycle diagnostic=%+v disposition=%q err=%v", record, disposition, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET diagnostic_bytes = X'7B7D' WHERE mailbox_id = ? AND client_request_id = ?`, ref.MailboxID, ref.ClientRequestID); err == nil {
		t.Fatal("frozen diagnostic bytes mutation succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET diagnostic_code = 'request_too_large' WHERE mailbox_id = ? AND client_request_id = ?`, ref.MailboxID, ref.ClientRequestID); err == nil {
		t.Fatal("frozen diagnostic code mutation succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET input_cleanup_started_at = ? WHERE mailbox_id = ? AND client_request_id = ?`, formatStoredTime(clock.Now()), ref.MailboxID, ref.ClientRequestID); err == nil {
		t.Fatal("input cleanup started before projection")
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET diagnostic_file_removed_at = ? WHERE mailbox_id = ? AND client_request_id = ?`, formatStoredTime(clock.Now()), ref.MailboxID, ref.ClientRequestID); err == nil {
		t.Fatal("diagnostic file removal skipped cleanup lifecycle")
	}
	projected, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, ref)
	if err != nil || projected.ProjectedAt == nil || !projected.ProjectedAt.Equal(clock.Now()) {
		t.Fatalf("projected diagnostic=%+v err=%v", projected, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET projected_at = NULL WHERE mailbox_id = ? AND client_request_id = ?`, ref.MailboxID, ref.ClientRequestID); err == nil {
		t.Fatal("projected lifecycle stage was cleared")
	}
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, ref, fingerprint); err != nil {
		t.Fatal(err)
	}
	removedPair, err := authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, ref, fingerprint)
	if err != nil || removedPair.InputPairRemovedAt == nil || removedPair.InputCleanupStartedAt == nil {
		t.Fatalf("removed input pair=%+v err=%v", removedPair, err)
	}
	incompleteRef, err := NewMailboxIngressDiagnosticRef(ref.MailboxID, "req-p164-recovery-incomplete")
	if err != nil {
		t.Fatal(err)
	}
	incompleteFingerprint := sha256.Sum256([]byte("malformed-p164-recovery-incomplete"))
	if _, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, incompleteRef, incompleteFingerprint, MailboxIngressDiagnosticMalformedJSON); err != nil || disposition != MailboxIngressDiagnosticCreated {
		t.Fatalf("create incomplete recovery diagnostic disposition=%q err=%v", disposition, err)
	}
	// P165 checks this active set against the private filesystem. The completed
	// record must remain eligible so a missing diagnostics/<request_id>.json can
	// be rebuilt, while an incomplete record is prioritized for safe pair work.
	recoverable, err := authority.ListRecoverableMailboxIngressDiagnosticsInMailbox(ctx, ref.MailboxID, 4)
	if err != nil || len(recoverable) != 2 || recoverable[0].RequestID != incompleteRef.ClientRequestID || recoverable[1].RequestID != ref.ClientRequestID {
		t.Fatalf("active diagnostic recovery list=%+v err=%v", recoverable, err)
	}
	clock.Advance(MailboxIngressDiagnosticArtifactLifetime + time.Second)
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, ref, fingerprint); !errors.Is(err, ErrMailboxIngressDiagnosticExpired) {
		t.Fatalf("expired input cleanup begin error=%v, want %v", err, ErrMailboxIngressDiagnosticExpired)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, ref, fingerprint); !errors.Is(err, ErrMailboxIngressDiagnosticExpired) {
		t.Fatalf("expired input cleanup completion error=%v, want %v", err, ErrMailboxIngressDiagnosticExpired)
	}
	claimed, err := authority.ClaimMailboxIngressDiagnosticsForCleanupInMailbox(ctx, ref.MailboxID, 4)
	if err != nil || len(claimed) != 1 || claimed[0].DiagnosticCleanupStartedAt == nil {
		t.Fatalf("diagnostic cleanup claim=%+v err=%v", claimed, err)
	}
	firstClaimStartedAt := *claimed[0].DiagnosticCleanupStartedAt
	reclaimed, err := authority.ClaimMailboxIngressDiagnosticsForCleanupInMailbox(ctx, ref.MailboxID, 4)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].DiagnosticCleanupStartedAt == nil || !reclaimed[0].DiagnosticCleanupStartedAt.Equal(firstClaimStartedAt) {
		t.Fatalf("diagnostic cleanup crash reclaim=%+v err=%v, first claim=%s", reclaimed, err, firstClaimStartedAt)
	}
	cleanupClaimTime := clock.Now()
	clock.value = removedPair.DiagnosticCleanupAt.Add(-time.Second)
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, ref, fingerprint); !errors.Is(err, ErrMailboxIngressDiagnosticCleanupState) {
		t.Fatalf("cleanup claim reopened after clock rollback error=%v, want %v", err, ErrMailboxIngressDiagnosticCleanupState)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticInputPairRemovedInMailbox(ctx, ref, fingerprint); !errors.Is(err, ErrMailboxIngressDiagnosticCleanupState) {
		t.Fatalf("cleanup claim completed after clock rollback error=%v, want %v", err, ErrMailboxIngressDiagnosticCleanupState)
	}
	clock.value = cleanupClaimTime
	recoverable, err = authority.ListRecoverableMailboxIngressDiagnosticsInMailbox(ctx, ref.MailboxID, 4)
	if err != nil || len(recoverable) != 0 {
		t.Fatalf("claimed or expired diagnostics returned for recovery=%+v err=%v", recoverable, err)
	}
	removedFile, err := authority.MarkMailboxIngressDiagnosticFileRemovedInMailbox(ctx, ref)
	if err != nil || removedFile.DiagnosticFileRemovedAt == nil {
		t.Fatalf("diagnostic cleanup completion=%+v err=%v", removedFile, err)
	}
	clock.value = removedPair.DiagnosticCleanupAt.Add(-time.Second)
	if _, err := authority.BeginMailboxIngressDiagnosticInputCleanupInMailbox(ctx, ref, fingerprint); !errors.Is(err, ErrMailboxIngressDiagnosticCleanupState) {
		t.Fatalf("removed diagnostic reopened after clock rollback error=%v, want %v", err, ErrMailboxIngressDiagnosticCleanupState)
	}
	clock.value = cleanupClaimTime
	if _, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, ref); !errors.Is(err, ErrMailboxIngressDiagnosticExpired) {
		t.Fatalf("expired diagnostic projection error=%v, want %v", err, ErrMailboxIngressDiagnosticExpired)
	}
	clock.Advance(DefaultMetadataRetention)
	report, err := authority.CollectGarbage(ctx, GarbageCollectionOptions{})
	if err != nil || report.MailboxIngressDiagnosticsDeleted != 1 {
		t.Fatalf("diagnostic metadata GC report=%+v err=%v", report, err)
	}
	if _, err := authority.GetMailboxIngressDiagnosticInMailbox(ctx, ref); !errors.Is(err, ErrMailboxIngressDiagnosticNotFound) {
		t.Fatalf("diagnostic after metadata GC error=%v, want %v", err, ErrMailboxIngressDiagnosticNotFound)
	}
}

func TestP164MailboxIngressDiagnosticRejectsMalformedPersistedLifecycle(t *testing.T) {
	ctx := context.Background()
	clock := &p164Clock{value: time.Date(2026, 10, 1, 16, 0, 0, 0, time.UTC)}
	authority, database := p164Authority(t, clock)
	t.Cleanup(func() { _ = database.Close() })
	ref, err := NewMailboxIngressDiagnosticRef("analytics", "req-p164-corrupt-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("malformed-p164-corrupt-lifecycle"))
	if _, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, ref, fingerprint, MailboxIngressDiagnosticMalformedJSON); err != nil || disposition != MailboxIngressDiagnosticCreated {
		t.Fatalf("create malformed lifecycle diagnostic disposition=%q err=%v", disposition, err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, ref); err != nil {
		t.Fatal(err)
	}
	// The trigger requires phase presence but intentionally leaves timestamp
	// chronology to store validation. A direct malformed write must therefore
	// be rejected when the row is read through the public store API.
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET input_cleanup_started_at = ? WHERE mailbox_id = ? AND client_request_id = ?`, formatStoredTime(clock.Now().Add(-time.Second)), ref.MailboxID, ref.ClientRequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetMailboxIngressDiagnosticInMailbox(ctx, ref); !errors.Is(err, ErrMailboxIngressDiagnosticInvalid) {
		t.Fatalf("malformed persisted lifecycle error=%v, want %v", err, ErrMailboxIngressDiagnosticInvalid)
	}

	deadlineRef, err := NewMailboxIngressDiagnosticRef("analytics", "req-p164-cleanup-deadline")
	if err != nil {
		t.Fatal(err)
	}
	deadlineFingerprint := sha256.Sum256([]byte("malformed-p164-cleanup-deadline"))
	deadlineRecord, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, deadlineRef, deadlineFingerprint, MailboxIngressDiagnosticMalformedJSON)
	if err != nil || disposition != MailboxIngressDiagnosticCreated {
		t.Fatalf("create deadline lifecycle diagnostic=%+v disposition=%q err=%v", deadlineRecord, disposition, err)
	}
	if _, err := authority.MarkMailboxIngressDiagnosticProjectedInMailbox(ctx, deadlineRef); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE mailbox_ingress_diagnostics SET input_cleanup_started_at = ? WHERE mailbox_id = ? AND client_request_id = ?`, formatStoredTime(deadlineRecord.DiagnosticCleanupAt), deadlineRef.MailboxID, deadlineRef.ClientRequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetMailboxIngressDiagnosticInMailbox(ctx, deadlineRef); !errors.Is(err, ErrMailboxIngressDiagnosticInvalid) {
		t.Fatalf("deadline input cleanup lifecycle error=%v, want %v", err, ErrMailboxIngressDiagnosticInvalid)
	}
}

func TestP164MailboxIngressDiagnosticRejectsAcceptedExchangeIdentity(t *testing.T) {
	ctx := context.Background()
	clock := &p164Clock{value: time.Date(2026, 10, 1, 17, 0, 0, 0, time.UTC)}
	authority, database := p164Authority(t, clock)
	t.Cleanup(func() { _ = database.Close() })
	ref, err := NewMailboxExchangeRef("default", "req-p164-exchange")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("p164-exchange"))
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, ref, MailboxExchangeCreate{
		MailboxID: ref.MailboxID, RequestID: ref.ClientRequestID,
		Operation: "get_session", Controller: controller, RequestHash: hash,
		CanonicalPayload: []byte(`{"operation":"get_session","session_id":"sess-p164"}`),
	}); err != nil {
		t.Fatal(err)
	}
	diagnosticRef, err := NewMailboxIngressDiagnosticRef(ref.MailboxID, ref.ClientRequestID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("no-parallel-diagnostic"))
	if _, _, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, diagnosticRef, fingerprint, MailboxIngressDiagnosticMalformedJSON); !errors.Is(err, ErrMailboxIngressDiagnosticConflict) {
		t.Fatalf("parallel accepted exchange diagnostic error=%v, want %v", err, ErrMailboxIngressDiagnosticConflict)
	}

	reservedDiagnosticRef, err := NewMailboxIngressDiagnosticRef("default", "req-p164-diagnostic-reserved")
	if err != nil {
		t.Fatal(err)
	}
	reservedFingerprint := sha256.Sum256([]byte("reserved-rejected-diagnostic"))
	if _, disposition, err := authority.RecordMailboxIngressDiagnosticInMailbox(ctx, reservedDiagnosticRef, reservedFingerprint, MailboxIngressDiagnosticMalformedJSON); err != nil || disposition != MailboxIngressDiagnosticCreated {
		t.Fatalf("create reserved diagnostic disposition=%q err=%v", disposition, err)
	}
	reservedExchangeRef, err := NewMailboxExchangeRef(reservedDiagnosticRef.MailboxID, reservedDiagnosticRef.ClientRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, reservedExchangeRef, MailboxExchangeCreate{
		MailboxID: reservedExchangeRef.MailboxID, RequestID: reservedExchangeRef.ClientRequestID,
		Operation: "get_session", Controller: controller, RequestHash: hash,
		CanonicalPayload: []byte(`{"operation":"get_session","session_id":"sess-p164-reserved"}`),
	}); !errors.Is(err, ErrMailboxIngressDiagnosticConflict) {
		t.Fatalf("accepted exchange against reserved diagnostic error=%v, want %v", err, ErrMailboxIngressDiagnosticConflict)
	}
	var reservedExchangeCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_exchanges WHERE mailbox_id = ? AND client_request_id = ?`, reservedExchangeRef.MailboxID, reservedExchangeRef.ClientRequestID).Scan(&reservedExchangeCount); err != nil || reservedExchangeCount != 0 {
		t.Fatalf("reserved diagnostic exchange count=%d err=%v, want 0", reservedExchangeCount, err)
	}
}

func p164Authority(t *testing.T, clock *p164Clock) (*AuthorityStore, *sql.DB) {
	t.Helper()
	root := testfixture.New(t)
	database, err := Open(context.Background(), filepath.Join(root.Path(), "state", "p164.db"))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorityStoreWithClock(database, clock.Now)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	return authority, database
}

func p164BuildV29Database(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	legacy, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		return err
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	for _, migration := range migrations[:29] {
		if _, err := legacy.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply v%d migration: %w", migration.version, err)
		}
	}
	when := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	for _, migration := range migrations[:29] {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, migration.version, migration.name, migrationChecksum(migration), formatStoredTime(when)); err != nil {
			return fmt.Errorf("record v%d migration: %w", migration.version, err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `PRAGMA user_version = 29`); err != nil {
		return err
	}
	return nil
}
