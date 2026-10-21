package runnerlocal

import (
	"errors"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/mailboxhandoff"
)

var errMailboxHandoffAuthorization = errors.New("mailbox ownership authorization failed")

func (s *Service) checkMailboxOwnership() error {
	if s == nil || s.mailboxDisabled || s.mailboxHandoff == nil {
		return nil
	}
	for _, definition := range s.mailboxConfig {
		if err := s.mailboxHandoff.Check(definition.ID); err != nil {
			return errMailboxHandoffAuthorization
		}
	}
	return nil
}

// acquireMailboxHandoff runs before any authority or service-path mutation.
// Ordinary, never-transferred mailbox configurations retain their old behavior.
// Once a root has a handoff marker, an absent receipt must fail closed even if
// an older config is restored while that root belongs to another lane.
func acquireMailboxHandoff(loaded config.Config, settings config.MacSettings, definitions []config.MailboxDefinition) (*mailboxhandoff.Lease, error) {
	if !settings.MailboxIngressEnabled {
		return nil, nil
	}
	if settings.MailboxHandoffReceipt == "" {
		for _, definition := range definitions {
			marked, err := mailboxhandoff.MarkerExists(definition.Root)
			if err != nil || marked {
				return nil, errMailboxHandoffAuthorization
			}
		}
		return nil, nil
	}
	configured := make([]mailboxhandoff.Definition, 0, len(definitions))
	for _, definition := range definitions {
		configured = append(configured, mailboxhandoff.Definition{ID: definition.ID, Root: definition.Root})
	}
	lease, err := mailboxhandoff.Acquire(settings.MailboxHandoffReceipt, string(loaded.ReleaseLane()), settings.ServiceRoot, loaded.SourceSHA256(), configured)
	if err != nil {
		return nil, errMailboxHandoffAuthorization
	}
	return lease, nil
}

// handoffExecutionResolver translates only receipt-authorized historical
// environment names. The original target and resolver still enforce the
// destination lane's exact execution context; request bytes remain unchanged.
type handoffExecutionResolver struct {
	original mailbox.MailboxExecutionResolver
	lease    *mailboxhandoff.Lease
}

func (r handoffExecutionResolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error) {
	if r.original == nil || r.lease == nil {
		return config.MailboxExecutionSelection{}, errMailboxHandoffAuthorization
	}
	return r.original.ResolveMailboxExecution(mailboxID, environmentPresent, environment, targetPresent, target, repositoryAlias)
}

func (r handoffExecutionResolver) ResolveMailboxEnvironment(mailboxID, environment string) (string, error) {
	if r.lease == nil {
		return "", errMailboxHandoffAuthorization
	}
	resolved, err := r.lease.ResolveEnvironment(mailboxID, environment)
	if err != nil {
		return "", errMailboxHandoffAuthorization
	}
	return resolved, nil
}

func (r handoffExecutionResolver) AllowedMailboxExecutionContexts(mailboxID string) []config.MailboxExecutionContext {
	if provider, ok := r.original.(interface {
		AllowedMailboxExecutionContexts(string) []config.MailboxExecutionContext
	}); ok {
		return provider.AllowedMailboxExecutionContexts(mailboxID)
	}
	return nil
}
