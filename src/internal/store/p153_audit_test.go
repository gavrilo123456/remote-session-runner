package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/testfixture"
)

func TestP153MailboxSelectionAuditRoundTripsWithLocalIntentAcceptance(t *testing.T) {
	root := testfixture.New(t)
	database, err := Open(context.Background(), filepath.Join(root.Path(), "state", "p153-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}

	input := p057CreateSessionIntent(t, "intent-p153-mailbox-audit", "session-p153-mailbox-audit", "key-p153-mailbox-audit")
	input.MailboxSelection = &audit.MailboxSelection{
		InboxID:           "default",
		ContextName:       "mac-local",
		Environment:       "mac-dev",
		TargetKind:        input.Target.Kind(),
		TargetProfile:     input.Target.Profile(),
		Source:            audit.MailboxSelectionSourceInboxDefault,
		RepositoryAlias:   "remote-session-runner",
		RepositoryAliases: []string{"remote-session-runner"},
	}
	mailboxContext := audit.WithIngress(context.Background(), audit.IngressMailbox)
	if _, duplicate, err := authority.AcceptLocalIntent(mailboxContext, input); err != nil || duplicate {
		t.Fatalf("AcceptLocalIntent() duplicate=%v err=%v", duplicate, err)
	}
	input.MailboxSelection.RepositoryAliases[0] = "mutated-after-acceptance"

	records, err := authority.ListAuditRecords(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].MailboxSelection == nil {
		t.Fatalf("audit records = %+v, want one selection-bearing row", records)
	}
	selection := records[0].MailboxSelection
	if records[0].Ingress != audit.IngressMailbox || records[0].Environment != "mac-dev" ||
		selection.InboxID != "default" || selection.ContextName != "mac-local" ||
		selection.Environment != "mac-dev" || selection.TargetKind != input.Target.Kind() ||
		selection.TargetProfile != input.Target.Profile() || selection.Source != audit.MailboxSelectionSourceInboxDefault ||
		selection.RepositoryAlias != "remote-session-runner" || len(selection.RepositoryAliases) != 1 || selection.RepositoryAliases[0] != "remote-session-runner" {
		t.Fatalf("round-tripped mailbox selection = %+v", selection)
	}

	// A metadata-bearing local intent from an ingress other than mailbox must
	// fail atomically: the provisional local intent and its audit row are both
	// rolled back when the audit validation rejects the ingress.
	invalid := p057CreateSessionIntent(t, "intent-p153-nonmailbox", "session-p153-nonmailbox", "key-p153-nonmailbox")
	invalid.MailboxSelection = &audit.MailboxSelection{
		InboxID:       "default",
		ContextName:   "mac-local",
		Environment:   "mac-dev",
		TargetKind:    invalid.Target.Kind(),
		TargetProfile: invalid.Target.Profile(),
		Source:        audit.MailboxSelectionSourceInboxDefault,
	}
	if _, _, err := authority.AcceptLocalIntent(context.Background(), invalid); !errors.Is(err, audit.ErrInvalidRecord) {
		t.Fatalf("non-mailbox acceptance error = %v, want audit.ErrInvalidRecord", err)
	}
	var intentCount, auditCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM local_intents").Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM runner_audit_records").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != 1 || auditCount != 1 {
		t.Fatalf("non-mailbox rollback counts intents=%d audits=%d, want 1/1", intentCount, auditCount)
	}
}
