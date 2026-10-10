package config

import (
	"errors"

	"remote-session-runner/src/internal/domain"
)

const (
	// MailboxExecutionSelectionSourceInboxDefault identifies a selection made
	// from the mailbox's configured default context.
	MailboxExecutionSelectionSourceInboxDefault = "inbox_default"
	// MailboxExecutionSelectionSourceRequestOverride identifies a complete
	// environment and target pair supplied in the mailbox request.
	MailboxExecutionSelectionSourceRequestOverride = "request_override"
)

var (
	// ErrMailboxNotConfigured means the named mailbox is absent from this host
	// configuration.
	ErrMailboxNotConfigured = errors.New("mailbox is not configured")
	// ErrMailboxExecutionPairRequired means a request supplied only one member
	// of its environment and execution-target pair.
	ErrMailboxExecutionPairRequired = errors.New("environment and execution target must be supplied together")
	// ErrMailboxExecutionContextNotFound means a complete requested pair has
	// no configured execution context.
	ErrMailboxExecutionContextNotFound = errors.New("execution context is not configured")
	// ErrMailboxExecutionContextNotAllowed means a configured context is not
	// listed in the selected mailbox's allow-list.
	ErrMailboxExecutionContextNotAllowed = errors.New("execution context is not allowed for mailbox")
	// ErrMailboxRepositoryAliasNotAllowed means a supplied repository alias is
	// outside the selected mailbox's policy-only alias scope.
	ErrMailboxRepositoryAliasNotAllowed = errors.New("repository alias is not allowed for mailbox")
)

// MailboxExecutionSelection is a trusted, immutable snapshot of the context
// selected for one new-work mailbox request. Repository aliases are policy and
// audit metadata only; they never select a checkout or materialize sources.
type MailboxExecutionSelection struct {
	MailboxID         string
	ContextName       string
	Environment       string
	Target            domain.ExecutionTarget
	Source            string
	RepositoryAlias   string
	RepositoryAliases []string
}

// MailboxExecutionContext is the non-secret, symbolic part of an execution
// context that a mailbox allows. It is suitable for explaining how to correct
// an otherwise well-formed new-work request; it deliberately excludes all
// host addresses, paths, credentials, and controller data.
type MailboxExecutionContext struct {
	Name        string
	Environment string
	Target      domain.ExecutionTarget
}

// AllowedMailboxExecutionContexts returns a copy of the contexts an inbox may
// select, in configured allow-list order. A missing or internally inconsistent
// mailbox yields no entries, so callers can keep their primary resolution
// failure fail-closed.
func (c Config) AllowedMailboxExecutionContexts(mailboxID string) []MailboxExecutionContext {
	mailbox, exists := c.mailboxes[mailboxID]
	if !exists {
		return nil
	}
	contexts := make([]MailboxExecutionContext, 0, len(mailbox.AllowedExecution))
	for _, name := range mailbox.AllowedExecution {
		context, exists := c.executionContexts[name]
		if !exists {
			return nil
		}
		contexts = append(contexts, MailboxExecutionContext{
			Name: name, Environment: context.Environment, Target: context.Target,
		})
	}
	return contexts
}

// ResolveMailboxExecution resolves the target context for a new-work mailbox
// request. Omitting both environment and execution target uses that mailbox's
// default. Supplying either field alone is rejected. A complete explicit pair
// must name a context allowed by this mailbox, rather than merely any context
// configured on the Mac.
//
// repositoryAlias is optional. When supplied, it must be one of the selected
// mailbox's configured aliases. It remains metadata and does not affect
// execution-context resolution.
func (c Config) ResolveMailboxExecution(
	mailboxID string,
	environmentPresent bool,
	environment string,
	targetPresent bool,
	target domain.ExecutionTarget,
	repositoryAlias string,
) (MailboxExecutionSelection, error) {
	mailbox, exists := c.mailboxes[mailboxID]
	if !exists {
		return MailboxExecutionSelection{}, ErrMailboxNotConfigured
	}
	if repositoryAlias != "" && !containsMailboxAlias(mailbox.RepositoryAliases, repositoryAlias) {
		return MailboxExecutionSelection{}, ErrMailboxRepositoryAliasNotAllowed
	}

	if environmentPresent != targetPresent {
		return MailboxExecutionSelection{}, ErrMailboxExecutionPairRequired
	}

	var (
		context ExecutionContext
		source  string
	)
	if !environmentPresent {
		var configured bool
		context, configured = c.executionContexts[mailbox.DefaultExecution]
		if !configured {
			return MailboxExecutionSelection{}, ErrMailboxExecutionContextNotFound
		}
		if !containsExecutionContext(mailbox.AllowedExecution, context.Name) {
			return MailboxExecutionSelection{}, ErrMailboxExecutionContextNotAllowed
		}
		source = MailboxExecutionSelectionSourceInboxDefault
	} else {
		var configured bool
		context, configured = c.ExecutionContextFor(environment, target)
		if !configured {
			return MailboxExecutionSelection{}, ErrMailboxExecutionContextNotFound
		}
		if !containsExecutionContext(mailbox.AllowedExecution, context.Name) {
			return MailboxExecutionSelection{}, ErrMailboxExecutionContextNotAllowed
		}
		source = MailboxExecutionSelectionSourceRequestOverride
	}

	return MailboxExecutionSelection{
		MailboxID:         mailbox.ID,
		ContextName:       context.Name,
		Environment:       context.Environment,
		Target:            context.Target,
		Source:            source,
		RepositoryAlias:   repositoryAlias,
		RepositoryAliases: append([]string(nil), mailbox.RepositoryAliases...),
	}, nil
}

func containsMailboxAlias(aliases []string, alias string) bool {
	for _, candidate := range aliases {
		if candidate == alias {
			return true
		}
	}
	return false
}

func containsExecutionContext(contexts []string, name string) bool {
	for _, candidate := range contexts {
		if candidate == name {
			return true
		}
	}
	return false
}
