package config

import (
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP153ResolveMailboxExecutionUsesMailboxDefaultAndCopiesScope(t *testing.T) {
	loaded := loadFixture(t, macV2ConfigFixture)

	selection, err := loaded.ResolveMailboxExecution("analytics", false, "", false, domain.ExecutionTarget{}, "analytics-dbt")
	if err != nil {
		t.Fatalf("ResolveMailboxExecution() error = %v", err)
	}
	if selection.MailboxID != "analytics" || selection.ContextName != "ubuntu-current" ||
		selection.Environment != "linux-dev" || selection.Target.Kind() != domain.TargetKindRemote ||
		selection.Target.Profile() != "linux-host" || selection.Source != MailboxExecutionSelectionSourceInboxDefault ||
		selection.RepositoryAlias != "analytics-dbt" || !equalStrings(selection.RepositoryAliases, []string{"analytics-dbt"}) {
		t.Fatalf("default selection = %+v", selection)
	}

	selection.RepositoryAliases[0] = "mutated"
	again, err := loaded.ResolveMailboxExecution("analytics", false, "", false, domain.ExecutionTarget{}, "")
	if err != nil {
		t.Fatalf("ResolveMailboxExecution() after slice mutation error = %v", err)
	}
	if !equalStrings(again.RepositoryAliases, []string{"analytics-dbt"}) {
		t.Fatalf("resolver exposed mutable mailbox aliases: %+v", again)
	}
}

func TestBUG018AllowedMailboxExecutionContextsExposeOnlySymbolicConfiguredPairs(t *testing.T) {
	loaded := loadFixture(t, macV2ConfigFixture)
	contexts := loaded.AllowedMailboxExecutionContexts("default")
	if len(contexts) != 2 || contexts[0].Name != "mac-local" || contexts[0].Environment != "mac-dev" ||
		contexts[0].Target.Kind() != domain.TargetKindLocal || contexts[0].Target.Profile() != "mac-workstation" ||
		contexts[1].Name != "ubuntu-current" || contexts[1].Environment != "linux-dev" ||
		contexts[1].Target.Kind() != domain.TargetKindRemote || contexts[1].Target.Profile() != "linux-host" {
		t.Fatalf("allowed contexts = %+v", contexts)
	}
	contexts[0].Name = "mutated"
	again := loaded.AllowedMailboxExecutionContexts("default")
	if len(again) != 2 || again[0].Name != "mac-local" {
		t.Fatalf("allowed contexts exposed mutable state: %+v", again)
	}
	if got := loaded.AllowedMailboxExecutionContexts("missing"); got != nil {
		t.Fatalf("missing mailbox contexts = %+v, want nil", got)
	}
}

func TestP153ResolveMailboxExecutionAllowsCompleteMailboxOverride(t *testing.T) {
	loaded := loadFixture(t, macV2ConfigFixture)
	buildTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-build-host")
	if err != nil {
		t.Fatal(err)
	}

	selection, err := loaded.ResolveMailboxExecution("analytics", true, "linux-build-dev", true, buildTarget, "")
	if err != nil {
		t.Fatalf("ResolveMailboxExecution() error = %v", err)
	}
	if selection.ContextName != "ubuntu-build" || selection.Environment != "linux-build-dev" ||
		selection.Target.Kind() != domain.TargetKindRemote || selection.Target.Profile() != "linux-build-host" ||
		selection.Source != MailboxExecutionSelectionSourceRequestOverride {
		t.Fatalf("override selection = %+v", selection)
	}
}

func TestP153ResolveMailboxExecutionRejectsInvalidSelection(t *testing.T) {
	loaded := loadFixture(t, macV2ConfigFixture)
	currentTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	buildTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-build-host")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name               string
		mailboxID          string
		environmentPresent bool
		environment        string
		targetPresent      bool
		target             domain.ExecutionTarget
		repositoryAlias    string
		want               error
	}{
		{
			name: "unknown mailbox", mailboxID: "missing", want: ErrMailboxNotConfigured,
		},
		{
			name: "environment only", mailboxID: "analytics", environmentPresent: true, environment: "linux-dev", want: ErrMailboxExecutionPairRequired,
		},
		{
			name: "target only", mailboxID: "analytics", targetPresent: true, target: currentTarget, want: ErrMailboxExecutionPairRequired,
		},
		{
			name: "mismatched environment target pair", mailboxID: "analytics", environmentPresent: true, environment: "linux-dev", targetPresent: true, target: buildTarget, want: ErrMailboxExecutionContextNotFound,
		},
		{
			name: "configured but disallowed context", mailboxID: "default", environmentPresent: true, environment: "linux-build-dev", targetPresent: true, target: buildTarget, want: ErrMailboxExecutionContextNotAllowed,
		},
		{
			name: "alias outside mailbox scope", mailboxID: "analytics", repositoryAlias: "remote-session-runner", want: ErrMailboxRepositoryAliasNotAllowed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loaded.ResolveMailboxExecution(
				test.mailboxID,
				test.environmentPresent,
				test.environment,
				test.targetPresent,
				test.target,
				test.repositoryAlias,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("ResolveMailboxExecution() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestP153ResolveMailboxExecutionKeepsV1DefaultMailboxCompatibility(t *testing.T) {
	loaded := loadFixture(t, macConfigFixture)
	remoteTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}

	defaultSelection, err := loaded.ResolveMailboxExecution("default", false, "", false, domain.ExecutionTarget{}, "")
	if err != nil {
		t.Fatalf("default ResolveMailboxExecution() error = %v", err)
	}
	if defaultSelection.ContextName != "mac-local" || defaultSelection.Environment != "mac-dev" ||
		defaultSelection.Target.Kind() != domain.TargetKindLocal || defaultSelection.Target.Profile() != "mac-workstation" ||
		defaultSelection.Source != MailboxExecutionSelectionSourceInboxDefault {
		t.Fatalf("v1 default selection = %+v", defaultSelection)
	}

	overrideSelection, err := loaded.ResolveMailboxExecution("default", true, "linux-dev", true, remoteTarget, "")
	if err != nil {
		t.Fatalf("override ResolveMailboxExecution() error = %v", err)
	}
	if overrideSelection.ContextName != "ubuntu-current" || overrideSelection.Source != MailboxExecutionSelectionSourceRequestOverride {
		t.Fatalf("v1 override selection = %+v", overrideSelection)
	}
}
