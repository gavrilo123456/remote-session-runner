package localapi

import (
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
)

// testMailboxExecutionResolver supplies the mandatory P153 policy boundary to
// pre-existing local API fixtures. It deliberately returns a selection made
// from the request rather than modeling a host configuration; P153's mailbox
// package tests cover real allow-list and default policy resolution.
type testMailboxExecutionResolver struct{}

func (testMailboxExecutionResolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error) {
	if environmentPresent != targetPresent {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionPairRequired
	}
	aliases := []string(nil)
	if repositoryAlias != "" {
		aliases = []string{repositoryAlias}
	}
	if !environmentPresent {
		defaultTarget, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
		if err != nil {
			return config.MailboxExecutionSelection{}, err
		}
		return config.MailboxExecutionSelection{
			MailboxID: mailboxID, ContextName: "test-default", Environment: "mac-dev", Target: defaultTarget,
			Source: config.MailboxExecutionSelectionSourceInboxDefault, RepositoryAlias: repositoryAlias, RepositoryAliases: aliases,
		}, nil
	}
	if environment == "" {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotFound
	}
	if _, err := domain.NewExecutionTarget(target.Kind(), target.Profile()); err != nil {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotFound
	}
	return config.MailboxExecutionSelection{
		MailboxID: mailboxID, ContextName: "test-explicit", Environment: environment, Target: target,
		Source: config.MailboxExecutionSelectionSourceRequestOverride, RepositoryAlias: repositoryAlias, RepositoryAliases: aliases,
	}, nil
}
