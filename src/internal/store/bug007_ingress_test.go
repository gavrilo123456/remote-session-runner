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

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

// TestBUG007JobIngressIsTrustedImmutableAndDuplicateStable proves that the
// store, rather than a caller-owned job payload, owns adapter provenance.
// The first durable acceptance wins for an idempotent retry made through a
// different ingress.
func TestBUG007JobIngressIsTrustedImmutableAndDuplicateStable(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := Open(ctx, filepath.Join(root.Path(), "state", "bug007-ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}

	input := p024Acceptance(t, "job-bug007-ingress", "sess-bug007-ingress", "cmd-bug007-ingress", "bug007-ingress-key", "printf ingress")
	accepted, duplicate, err := authority.AcceptJob(audit.WithIngress(ctx, audit.IngressDirectMTLS), input)
	if err != nil || duplicate {
		t.Fatalf("direct acceptance job=%+v duplicate=%v err=%v", accepted, duplicate, err)
	}
	if accepted.Ingress != audit.IngressDirectMTLS {
		t.Fatalf("direct accepted ingress=%q, want %q", accepted.Ingress, audit.IngressDirectMTLS)
	}

	replayed, duplicate, err := authority.AcceptJob(audit.WithIngress(ctx, audit.IngressSSHBridge), input)
	if err != nil || !duplicate {
		t.Fatalf("cross-ingress duplicate job=%+v duplicate=%v err=%v", replayed, duplicate, err)
	}
	if replayed.Ingress != audit.IngressDirectMTLS {
		t.Fatalf("duplicate ingress=%q, want immutable original %q", replayed.Ingress, audit.IngressDirectMTLS)
	}

	unknown := p024Acceptance(t, "job-bug007-unknown", "sess-bug007-unknown", "cmd-bug007-unknown", "bug007-unknown-key", "printf unknown")
	if _, _, err := authority.AcceptJob(audit.WithIngress(ctx, audit.IngressUnknown), unknown); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("unknown ingress acceptance error=%v, want %v", err, ErrInvalidJob)
	}
	if _, err := authority.GetJob(ctx, unknown.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("unknown ingress created job lookup error=%v, want %v", err, ErrJobNotFound)
	}
}

// TestBUG007V30MigrationRetainsUnknownIngressAndAuditAppendOnly builds an
// actual version-30 database with queued work and an existing mailbox audit,
// then proves version 31 neither fabricates a trusted ingress nor weakens the
// audit table while permitting an honest historical unknown record.
func TestBUG007V30MigrationRetainsUnknownIngressAndAuditAppendOnly(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	input := p024Acceptance(t, "job-bug007-v30-ingress", "sess-bug007-v30-ingress", "cmd-bug007-v30-ingress", "bug007-v30-ingress-key", "printf migrated")
	path := filepath.Join(root.Path(), "state", "bug007-v30.db")
	if err := bug007BuildV30Database(ctx, path, input); err != nil {
		t.Fatalf("build version-30 fixture: %v", err)
	}

	database, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate version-30 fixture: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	if version := readUserVersion(t, database); version != CurrentSchemaVersion {
		t.Fatalf("migrated schema version=%d, want %d", version, CurrentSchemaVersion)
	}

	job, err := authority.GetJob(ctx, input.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Ingress != audit.IngressUnknown {
		t.Fatalf("migrated job ingress=%q, want historical %q", job.Ingress, audit.IngressUnknown)
	}

	records, err := authority.ListAuditRecords(ctx, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("migrated audit records=%+v err=%v, want one", records, err)
	}
	legacyAudit := records[0]
	if legacyAudit.Ingress != audit.IngressMailbox || legacyAudit.MailboxSelection == nil ||
		legacyAudit.MailboxSelection.InboxID != "slidestud-io" ||
		legacyAudit.MailboxSelection.ContextName != "logger-controller" ||
		legacyAudit.MailboxSelection.Environment != "linux-dev" ||
		legacyAudit.MailboxSelection.TargetKind != domain.TargetKindRemote ||
		legacyAudit.MailboxSelection.TargetProfile != "sandbox-host" ||
		legacyAudit.MailboxSelection.Source != audit.MailboxSelectionSourceInboxDefault ||
		legacyAudit.MailboxSelection.RepositoryAlias != "slidestud-io" ||
		len(legacyAudit.MailboxSelection.RepositoryAliases) != 1 ||
		legacyAudit.MailboxSelection.RepositoryAliases[0] != "slidestud-io" {
		t.Fatalf("migrated mailbox audit=%+v", legacyAudit)
	}

	unknownAudit := audit.NewRecord(input.Controller, audit.IngressUnknown, audit.ActionRun, audit.OutcomeAllowed)
	unknownAudit.Environment = input.Environment
	unknownAudit.JobID = input.JobID
	if err := authority.RecordAudit(ctx, unknownAudit); err != nil {
		t.Fatalf("record historical unknown audit after migration: %v", err)
	}
	records, err = authority.ListAuditRecords(ctx, 10)
	if err != nil || len(records) != 2 || records[1].Ingress != audit.IngressUnknown {
		t.Fatalf("post-migration unknown audit records=%+v err=%v", records, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE runner_audit_records SET occurred_at = ? WHERE id = ?`, formatStoredTime(time.Date(2026, 10, 1, 18, 1, 0, 0, time.UTC)), legacyAudit.ID); err == nil {
		t.Fatal("migrated audit update unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM runner_audit_records WHERE id = ?`, legacyAudit.ID); err == nil {
		t.Fatal("migrated audit delete unexpectedly succeeded")
	}
}

func bug007BuildV30Database(ctx context.Context, path string, input JobAcceptance) error {
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
	for _, migration := range migrations[:30] {
		if _, err := legacy.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply v%d migration: %w", migration.version, err)
		}
	}
	when := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	for _, migration := range migrations[:30] {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO runner_schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, migration.version, migration.name, migrationChecksum(migration), formatStoredTime(when)); err != nil {
			return fmt.Errorf("record v%d migration: %w", migration.version, err)
		}
	}
	scriptBytes := []byte(input.Script)
	scriptHash := sha256.Sum256(scriptBytes)
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO exec_jobs (
    job_id, session_id, command_id, controller_type, controller_id,
    target_kind, target_profile, environment,
    source_mode, source_repository_alias, source_requested_revision, source_path,
    request_hash_version, request_hash, idempotency_key, payload_json,
    script_bytes, script_sha256, phase, teardown_state, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(input.JobID), string(input.SessionID), string(input.CommandID), string(input.Controller.Type()), string(input.Controller.ID()),
		string(input.Target.Kind()), input.Target.Profile(), input.Environment,
		string(input.Source.Mode()), input.Source.RepositoryAlias(), input.Source.RequestedRevision(), input.Source.Path(),
		input.RequestHash.Version(), input.RequestHash.SHA256(), input.IdempotencyKey, input.CanonicalPayload,
		scriptBytes, scriptHash[:], string(JobPhaseCreatingSession), string(JobTeardownPending), formatStoredTime(when), formatStoredTime(when)); err != nil {
		return fmt.Errorf("insert v30 job: %w", err)
	}
	if _, err := legacy.ExecContext(ctx, `
INSERT INTO runner_audit_records (
    principal_type, principal_id, ingress, environment, job_id,
    action, outcome, reason_code, occurred_at,
    mailbox_id, execution_context, execution_selection_source,
    resolved_target_kind, resolved_target_profile, repository_alias,
    repository_aliases_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(input.Controller.Type()), string(input.Controller.ID()), string(audit.IngressMailbox), input.Environment, string(input.JobID),
		string(audit.ActionRun), string(audit.OutcomeAllowed), "", formatStoredTime(when),
		"slidestud-io", "logger-controller", audit.MailboxSelectionSourceInboxDefault,
		"remote", "sandbox-host", "slidestud-io", `["slidestud-io"]`); err != nil {
		return fmt.Errorf("insert v30 audit: %w", err)
	}
	if _, err := legacy.ExecContext(ctx, `PRAGMA user_version = 30`); err != nil {
		return err
	}
	return nil
}
