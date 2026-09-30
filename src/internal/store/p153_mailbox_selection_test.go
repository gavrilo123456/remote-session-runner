package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP153MailboxExecutionSelectionPersistsAndRetainedRetryReusesOriginal(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "p153-selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	authority, err := NewAuthorityStoreWithClock(database, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"operation":"run","script":"printf p153"}`)
	hash, err := domain.HashMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstRef, err := NewMailboxExchangeRef("analytics", "req-p153-selection-first")
	if err != nil {
		t.Fatal(err)
	}
	firstSelection := &MailboxExecutionSelection{
		ContextName:       "ubuntu-current",
		Environment:       "linux-dev",
		Target:            target,
		Source:            MailboxExecutionSelectionInboxDefault,
		RepositoryAlias:   "analytics-dbt",
		RepositoryAliases: []string{"analytics-dbt"},
	}
	first, duplicate, conflict, err := authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, firstRef, MailboxExchangeCreate{
		MailboxID: "analytics", RequestID: firstRef.ClientRequestID, Operation: "run", Controller: controller,
		IdempotencyKey: "key-p153-selection", RequestHash: hash, CanonicalPayload: payload, ResourceID: "job-p153-selection", Selection: firstSelection,
	})
	if err != nil || duplicate || conflict {
		t.Fatalf("first selection receipt duplicate=%t conflict=%t err=%v", duplicate, conflict, err)
	}
	if first.SelectionState != MailboxExecutionSelectionResolved {
		t.Fatalf("first selection state=%q, want resolved", first.SelectionState)
	}
	p153AssertMailboxSelection(t, first.Selection, firstSelection)
	firstSelection.RepositoryAliases[0] = "mutated-after-acceptance"
	reloaded, err := authority.GetMailboxExchangeInMailbox(ctx, firstRef)
	if err != nil {
		t.Fatal(err)
	}
	p153AssertMailboxSelection(t, reloaded.Selection, &MailboxExecutionSelection{
		ContextName: "ubuntu-current", Environment: "linux-dev", Target: target,
		Source: MailboxExecutionSelectionInboxDefault, RepositoryAlias: "analytics-dbt", RepositoryAliases: []string{"analytics-dbt"},
	})

	// A same-key retry deliberately supplies a later selection source. The
	// stored binding wins, including the original source and alias scope.
	retryRef, err := NewMailboxExchangeRef("analytics", "req-p153-selection-retry")
	if err != nil {
		t.Fatal(err)
	}
	retry, duplicate, conflict, err := authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, retryRef, MailboxExchangeCreate{
		MailboxID: "analytics", RequestID: retryRef.ClientRequestID, Operation: "run", Controller: controller,
		IdempotencyKey: "key-p153-selection", RequestHash: hash, CanonicalPayload: payload, ResourceID: "must-not-win",
		Selection: &MailboxExecutionSelection{
			ContextName: "ubuntu-current", Environment: "linux-dev", Target: target,
			Source: MailboxExecutionSelectionRequestOverride, RepositoryAliases: []string{"analytics-dbt"},
		},
	})
	if err != nil || !duplicate || conflict {
		t.Fatalf("retained retry duplicate=%t conflict=%t err=%v", duplicate, conflict, err)
	}
	p153AssertMailboxSelection(t, retry.Selection, reloaded.Selection)
	if retry.SelectionState != MailboxExecutionSelectionResolved {
		t.Fatalf("retry selection state=%q, want original resolved state", retry.SelectionState)
	}
	if retry.ResourceID != "job-p153-selection" {
		t.Fatalf("retry resource=%q, want original resource", retry.ResourceID)
	}
	active, found, err := authority.FindActiveMailboxExchangeByKeyInMailbox(ctx, "analytics", controller, "run", "key-p153-selection")
	if err != nil || !found {
		t.Fatalf("FindActiveMailboxExchangeByKeyInMailbox found=%t err=%v", found, err)
	}
	p153AssertMailboxSelection(t, active.Selection, reloaded.Selection)
}

func TestP153MigratesActualV25SelectionAndAuditRowsAsAbsent(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "p153-v25.db")
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
	p153BuildActualV25Database(t, ctx, legacy)
	base := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	stamp := formatStoredTime(base)
	requestHash := sha256.Sum256([]byte("p153-v25-selection"))
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_exchanges (
  exchange_id, mailbox_id, client_request_id, operation, controller_type, controller_id,
  client_idempotency_key, execution_idempotency_key, canonical_hash_version, canonical_hash,
  canonical_payload, resource_id, request_state, response_revision, created_at, updated_at,
  idempotency_binding_active, deduplication_warning
) VALUES (?, 'default', 'req-p153-v25', 'run', 'local_user', 'tomasz.walczuk',
  'key-p153-v25', 'mailbox/default/key-p153-v25', 1, ?, X'7b7d', 'job-p153-v25',
  'accepted', 0, ?, ?, 1, 0)
	`, mailboxExchangeID(MailboxExchangeRef{MailboxID: DefaultMailboxID, ClientRequestID: "req-p153-v25"}), requestHash[:], stamp, stamp); err != nil {
		t.Fatalf("insert v25 exchange: %v", err)
	}

	// Build a terminal v25 receipt with its ACK, event-file reference, and
	// cleanup row. This exercises the P153 migration against the real v25
	// schema, rather than only an active exchange with no dependent records.
	terminalRef := MailboxExchangeRef{MailboxID: DefaultMailboxID, ClientRequestID: "req-p153-v25-terminal"}
	terminalCommandID := "cmd-p153-v25-terminal"
	terminalAt := base.Add(time.Minute)
	acknowledgedAt := terminalAt.Add(time.Minute)
	cleanupStartedAt := terminalAt.Add(2 * time.Minute)
	fileRemovedAt := cleanupStartedAt.Add(time.Minute)
	p152InsertV24LocalCommand(t, ctx, legacy, "sess-p153-v25-terminal", terminalCommandID, terminalAt)
	terminalResponse := []byte(`{"request_id":"req-p153-v25-terminal","request_state":"complete","response_revision":1}`)
	terminalResponseHash := sha256.Sum256(terminalResponse)
	terminalRequestHash := sha256.Sum256([]byte("p153-v25-terminal-selection"))
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_exchanges (
  exchange_id, mailbox_id, client_request_id, operation, controller_type, controller_id,
  client_idempotency_key, execution_idempotency_key, canonical_hash_version, canonical_hash,
  canonical_payload, resource_id, request_state, response_revision, terminal_response_bytes,
  terminal_response_sha256, available_event_sequence, created_at, updated_at, response_bytes,
  response_sha256, acknowledged_at, response_cleanup_at, idempotency_key_expires_at,
  idempotency_binding_active, deduplication_warning
) VALUES (?, 'default', 'req-p153-v25-terminal', 'run', 'local_user', 'tomasz.walczuk',
  'key-p153-v25-terminal', 'mailbox/default/key-p153-v25-terminal', 1, ?, X'7b7d', ?,
  'complete', 1, ?, ?, 4, ?, ?, ?, ?, ?, ?, ?, 1, 0)
`, mailboxExchangeID(terminalRef), terminalRequestHash[:], terminalCommandID,
		terminalResponse, terminalResponseHash[:], formatStoredTime(terminalAt), formatStoredTime(terminalAt),
		terminalResponse, terminalResponseHash[:], formatStoredTime(acknowledgedAt),
		formatStoredTime(terminalAt.Add(MailboxAckedResponseLifetime)), formatStoredTime(terminalAt.Add(90*24*time.Hour))); err != nil {
		t.Fatalf("insert v25 terminal exchange: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_event_file_references(exchange_id, command_id, created_at)
VALUES (?, ?, ?)
`, mailboxExchangeID(terminalRef), terminalCommandID, formatStoredTime(terminalAt.Add(10*time.Second))); err != nil {
		t.Fatalf("insert v25 event reference: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO mailbox_event_file_cleanup(mailbox_id, command_id, cleanup_started_at, file_removed_at)
VALUES ('default', ?, ?, ?)
`, terminalCommandID, formatStoredTime(cleanupStartedAt), formatStoredTime(fileRemovedAt)); err != nil {
		t.Fatalf("insert v25 event cleanup: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO runner_audit_records (
  principal_type, principal_id, ingress, environment, action, outcome, reason_code, occurred_at
) VALUES ('local_user', 'tomasz.walczuk', 'mailbox', 'mac-dev', 'run', 'allowed', '', ?)
`, stamp); err != nil {
		t.Fatalf("insert v25 audit: %v", err)
	}
	p152AssertNoForeignKeyViolations(t, ctx, legacy)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v25 database: %v", err)
	}
	defer database.Close()
	if got := readUserVersion(t, database); got != CurrentSchemaVersion {
		t.Fatalf("schema version=%d, want %d", got, CurrentSchemaVersion)
	}
	p152AssertNoForeignKeyViolations(t, ctx, database)
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	record, err := authority.GetMailboxExchangeInMailbox(ctx, MailboxExchangeRef{MailboxID: DefaultMailboxID, ClientRequestID: "req-p153-v25"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Selection != nil {
		t.Fatalf("v25 exchange selection=%+v, want absent", record.Selection)
	}
	if record.SelectionState != MailboxExecutionSelectionLegacy {
		t.Fatalf("v25 exchange selection state=%q, want legacy", record.SelectionState)
	}
	terminalRecord, err := authority.GetMailboxExchangeInMailbox(ctx, terminalRef)
	if err != nil {
		t.Fatal(err)
	}
	if terminalRecord.Selection != nil || terminalRecord.SelectionState != MailboxExecutionSelectionLegacy ||
		terminalRecord.State != MailboxExchangeComplete || terminalRecord.ResourceID != terminalCommandID ||
		terminalRecord.EventFileCommandID != terminalCommandID || terminalRecord.ResponseRevision != 1 ||
		!bytes.Equal(terminalRecord.ResponseBytes, terminalResponse) || !bytes.Equal(terminalRecord.TerminalResponseBytes, terminalResponse) ||
		terminalRecord.AvailableEventSequence == nil || *terminalRecord.AvailableEventSequence != 4 {
		t.Fatalf("migrated terminal v25 exchange=%+v", terminalRecord)
	}
	p152AssertOptionalTime(t, "terminal acknowledged_at", terminalRecord.AcknowledgedAt, &acknowledgedAt)
	p152AssertMigratedV24EventReference(t, ctx, database, "mailbox_event_file_references", terminalRef, terminalCommandID, formatStoredTime(terminalAt.Add(10*time.Second)))
	p152AssertMigratedV24Cleanup(t, ctx, database, "mailbox_event_file_cleanup", terminalCommandID, cleanupStartedAt, &fileRemovedAt)
	audits, err := authority.ListAuditRecords(ctx, 10)
	if err != nil || len(audits) != 1 {
		t.Fatalf("v25 audit records=%+v err=%v", audits, err)
	}
	if audits[0].MailboxSelection != nil {
		t.Fatalf("v25 audit selection=%+v, want absent", audits[0].MailboxSelection)
	}
	legacyAudit := audits[0]
	if legacyAudit.Principal.Type() != domain.ControllerTypeLocalUser || legacyAudit.Principal.ID() != "tomasz.walczuk" ||
		legacyAudit.Ingress != audit.IngressMailbox || legacyAudit.Environment != "mac-dev" ||
		legacyAudit.Action != audit.ActionRun || legacyAudit.Outcome != audit.OutcomeAllowed || !legacyAudit.OccurredAt.Equal(base) {
		t.Fatalf("migrated v25 audit=%+v", legacyAudit)
	}
	if err := authority.RecordAudit(ctx, audit.Record{
		Principal: legacyAudit.Principal, Ingress: audit.IngressMailbox, Environment: "mac-dev",
		Action: audit.ActionRun, Outcome: audit.OutcomeAllowed, OccurredAt: base.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("append post-migration audit: %v", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE runner_audit_records SET action = 'close' WHERE id = ?", legacyAudit.ID); err == nil {
		t.Fatal("migrated audit update unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM runner_audit_records WHERE id = ?", legacyAudit.ID); err == nil {
		t.Fatal("migrated audit delete unexpectedly succeeded")
	}
	audits, err = authority.ListAuditRecords(ctx, 10)
	if err != nil || len(audits) != 2 {
		t.Fatalf("post-migration audit records=%+v err=%v", audits, err)
	}
	if audits[0].ID != legacyAudit.ID || audits[0].Action != legacyAudit.Action || audits[0].OccurredAt != legacyAudit.OccurredAt || audits[0].MailboxSelection != nil ||
		audits[1].ID <= legacyAudit.ID || audits[1].MailboxSelection != nil {
		t.Fatalf("audit append-only preservation failed: old=%+v new=%+v", audits[0], audits[1])
	}
}

func TestP153MailboxSelectionStateRejectsIncoherentValues(t *testing.T) {
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	selection := &MailboxExecutionSelection{
		ContextName: "mac-local", Environment: "mac-dev", Target: target,
		Source: MailboxExecutionSelectionInboxDefault,
	}
	cases := []struct {
		name      string
		operation string
		selection *MailboxExecutionSelection
		state     MailboxExecutionSelectionState
	}{
		{name: "resolved without selection", operation: "run", state: MailboxExecutionSelectionResolved},
		{name: "rejected with selection", operation: "run", selection: selection, state: MailboxExecutionSelectionRejected},
		{name: "legacy with selection", operation: "run", selection: selection, state: MailboxExecutionSelectionLegacy},
		{name: "rejected non-new work", operation: "get_session", state: MailboxExecutionSelectionRejected},
		{name: "unknown state", operation: "run", state: MailboxExecutionSelectionState("unknown")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := validateMailboxExecutionSelectionState(testCase.operation, testCase.selection, testCase.state); err == nil {
				t.Fatal("state validation unexpectedly succeeded")
			}
		})
	}
}

func p153BuildActualV25Database(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, migration := range migrations[:25] {
		if _, err := database.ExecContext(ctx, migration.sql); err != nil {
			t.Fatalf("apply historical migration %d (%s): %v", migration.version, migration.name, err)
		}
	}
	appliedAt := formatStoredTime(time.Date(2026, 9, 30, 14, 30, 0, 0, time.UTC))
	for _, migration := range migrations[:25] {
		if _, err := database.ExecContext(ctx, `
INSERT INTO runner_schema_migrations(version, name, checksum, applied_at)
VALUES (?, ?, ?, ?)
`, migration.version, migration.name, migrationChecksum(migration), appliedAt); err != nil {
			t.Fatalf("record historical migration %d (%s): %v", migration.version, migration.name, err)
		}
	}
	if _, err := database.ExecContext(ctx, "PRAGMA user_version = 25"); err != nil {
		t.Fatal(err)
	}
	if got := readUserVersion(t, database); got != 25 {
		t.Fatalf("fixture schema version=%d, want 25", got)
	}
}

func p153AssertMailboxSelection(t *testing.T, got, want *MailboxExecutionSelection) {
	t.Helper()
	if got == nil || want == nil || got.ContextName != want.ContextName || got.Environment != want.Environment ||
		got.Target.Kind() != want.Target.Kind() || got.Target.Profile() != want.Target.Profile() ||
		got.Source != want.Source || got.RepositoryAlias != want.RepositoryAlias ||
		len(got.RepositoryAliases) != len(want.RepositoryAliases) {
		t.Fatalf("selection=%+v, want %+v", got, want)
	}
	for index := range want.RepositoryAliases {
		if got.RepositoryAliases[index] != want.RepositoryAliases[index] {
			t.Fatalf("selection aliases=%v, want %v", got.RepositoryAliases, want.RepositoryAliases)
		}
	}
}
