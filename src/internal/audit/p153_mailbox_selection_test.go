package audit

import (
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP153MailboxSelectionIsBoundedAndLimitedToAllowedMailboxNewWork(t *testing.T) {
	principal, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	record := NewRecord(principal, IngressMailbox, ActionRun, OutcomeAllowed)
	record.Environment = "linux-dev"
	record.MailboxSelection = &MailboxSelection{
		InboxID:           "analytics",
		ContextName:       "ubuntu-current",
		Environment:       "linux-dev",
		TargetKind:        target.Kind(),
		TargetProfile:     target.Profile(),
		Source:            MailboxSelectionSourceInboxDefault,
		RepositoryAlias:   "analytics-dbt",
		RepositoryAliases: []string{"analytics-dbt"},
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("valid mailbox selection rejected: %v", err)
	}

	record.Ingress = IngressLocalUnix
	if err := record.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("non-mailbox metadata error = %v, want ErrInvalidRecord", err)
	}

	record.Ingress = IngressMailbox
	record.Environment = "mac-dev"
	if err := record.Validate(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("mismatched environment error = %v, want ErrInvalidRecord", err)
	}
}
