// Package audit defines the safe, structured authorization/action audit
// contract shared by the Runner's ingress and authority components.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
)

type contextKey uint8

const (
	ingressContextKey contextKey = iota + 1
	actionContextKey
)

type Ingress string

const (
	IngressLocalUnix   Ingress = "local_unix"
	IngressMailbox     Ingress = "mailbox"
	IngressLocalWorker Ingress = "local_executor"
	IngressSSHBridge   Ingress = "ssh_bridge"
	IngressDirectMTLS  Ingress = "direct_mtls"
	IngressInternal    Ingress = "internal"
	// IngressUnknown is retained only for durable work accepted before a
	// version that recorded its adapter provenance. It is truthful historical
	// metadata, never a new live adapter label.
	IngressUnknown          Ingress = "unknown"
	IngressServiceLifecycle Ingress = IngressInternal
	IngressInternalTest     Ingress = IngressInternal
)

// ValidIngress reports whether ingress is one of the bounded server-side
// adapter labels, or the migration-only unknown value, that may be retained
// with durable work or an audit record. It deliberately does not accept an
// empty value: a caller that has no trusted adapter provenance must choose the
// explicit internal label.
func ValidIngress(ingress Ingress) bool {
	switch ingress {
	case IngressLocalUnix, IngressMailbox, IngressLocalWorker, IngressSSHBridge, IngressDirectMTLS, IngressInternal, IngressUnknown:
		return true
	default:
		return false
	}
}

// TrustedForAcceptance reports whether ingress may label newly accepted
// durable work. Unknown is valid only when reading historical pre-migration
// data; no live adapter may create new work with that label.
func (ingress Ingress) TrustedForAcceptance() bool {
	return ingress != IngressUnknown && ValidIngress(ingress)
}

type Action string

const (
	ActionCreate         Action = "create"
	ActionSubmit         Action = "submit"
	ActionCancel         Action = "cancel"
	ActionClose          Action = "close"
	ActionRun            Action = "run"
	ActionReadSession    Action = "read_session"
	ActionReadCommand    Action = "read_command"
	ActionReadJob        Action = "read_job"
	ActionRuntimeCleanup Action = "runtime_cleanup"
)

type Outcome string

const (
	OutcomeAllowed Outcome = "allowed"
	OutcomeDenied  Outcome = "denied"
	OutcomeFailed  Outcome = "failed"
)

const (
	ReasonEnvironmentDenied         = "environment_denied"
	ReasonPolicyDenied              = "policy_denied"
	ReasonControllerDenied          = "controller_denied"
	ReasonRuntimeCleanupUnconfirmed = "runtime_cleanup_unconfirmed"
)

const (
	// MailboxSelectionSourceInboxDefault identifies a context selected from an
	// inbox's configured default.
	MailboxSelectionSourceInboxDefault = "inbox_default"
	// MailboxSelectionSourceRequestOverride identifies a complete context pair
	// supplied by the mailbox request.
	MailboxSelectionSourceRequestOverride = "request_override"
)

var ErrInvalidRecord = errors.New("invalid audit record")

// Record contains only identifiers and bounded policy metadata. It has no
// fields for request bodies, scripts, output, keys, certificates, or secrets.
type Record struct {
	ID               int64
	Principal        domain.ControllerIdentity
	Ingress          Ingress
	Environment      string
	SessionID        domain.SessionID
	CommandID        domain.CommandID
	JobID            domain.JobID
	Action           Action
	Outcome          Outcome
	ReasonCode       string
	MailboxSelection *MailboxSelection
	OccurredAt       time.Time
}

// MailboxSelection is the bounded routing decision attached to an allowed
// mailbox create or run action. It contains policy metadata only: it has no
// source path, checkout content, request body, script, output, or credential.
//
// Source is one of MailboxSelectionSourceInboxDefault and
// MailboxSelectionSourceRequestOverride. RepositoryAlias is optional, while
// RepositoryAliases records the configured scope at acceptance time.
type MailboxSelection struct {
	InboxID           string
	ContextName       string
	Environment       string
	TargetKind        domain.TargetKind
	TargetProfile     string
	Source            string
	RepositoryAlias   string
	RepositoryAliases []string
}

// CloneMailboxSelection returns a copy safe to retain independently of a
// caller-owned repository-scope slice. Nil remains nil.
func CloneMailboxSelection(input *MailboxSelection) *MailboxSelection {
	if input == nil {
		return nil
	}
	clone := *input
	clone.RepositoryAliases = append([]string(nil), input.RepositoryAliases...)
	return &clone
}

// Validate checks that a mailbox selection contains only bounded policy
// metadata. It deliberately permits an empty repository scope, which is how
// the legacy default inbox is represented.
func (selection MailboxSelection) Validate() error {
	if !validMailboxSelectionName(selection.InboxID) ||
		!validMailboxSelectionName(selection.ContextName) ||
		!validMailboxSelectionName(selection.Environment) ||
		!validMailboxSelectionName(selection.TargetProfile) {
		return fmt.Errorf("%w: mailbox selection identity", ErrInvalidRecord)
	}
	if _, err := domain.NewExecutionTarget(selection.TargetKind, selection.TargetProfile); err != nil {
		return fmt.Errorf("%w: mailbox selection target", ErrInvalidRecord)
	}
	if selection.Source != MailboxSelectionSourceInboxDefault && selection.Source != MailboxSelectionSourceRequestOverride {
		return fmt.Errorf("%w: mailbox selection source", ErrInvalidRecord)
	}
	aliases := make(map[string]struct{}, len(selection.RepositoryAliases))
	for _, alias := range selection.RepositoryAliases {
		if !validMailboxSelectionName(alias) {
			return fmt.Errorf("%w: mailbox repository alias", ErrInvalidRecord)
		}
		if _, exists := aliases[alias]; exists {
			return fmt.Errorf("%w: duplicate mailbox repository alias", ErrInvalidRecord)
		}
		aliases[alias] = struct{}{}
	}
	if selection.RepositoryAlias != "" {
		if !validMailboxSelectionName(selection.RepositoryAlias) {
			return fmt.Errorf("%w: selected mailbox repository alias", ErrInvalidRecord)
		}
		if _, exists := aliases[selection.RepositoryAlias]; !exists {
			return fmt.Errorf("%w: selected repository alias is outside mailbox scope", ErrInvalidRecord)
		}
	}
	return nil
}

func validMailboxSelectionName(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9' && index > 0) ||
			(character == '-' && index > 0) {
			continue
		}
		return false
	}
	return true
}

// WithIngress marks a context at a trusted server-side adapter boundary.
func WithIngress(ctx context.Context, ingress Ingress) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ingressContextKey, ingress)
}

// IngressFromContext returns the trusted adapter label, or internal for
// service-level tests and calls that do not cross a transport adapter.
func IngressFromContext(ctx context.Context) Ingress {
	if ctx != nil {
		if value, ok := ctx.Value(ingressContextKey).(Ingress); ok && value != "" {
			return value
		}
	}
	return IngressInternalTest
}

// WithActionHint lets an adapter identify the operation whose preliminary
// ownership read it is performing. It is used only for denial audit records.
func WithActionHint(ctx context.Context, action Action) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actionContextKey, action)
}

// ActionHint returns an adapter-provided mutation action, if one exists.
func ActionHint(ctx context.Context) Action {
	if ctx != nil {
		if action, ok := ctx.Value(actionContextKey).(Action); ok {
			return action
		}
	}
	return ""
}

// NewRecord creates an audit row with a UTC timestamp.
func NewRecord(principal domain.ControllerIdentity, ingress Ingress, action Action, outcome Outcome) Record {
	return Record{Principal: principal, Ingress: ingress, Action: action, Outcome: outcome, OccurredAt: time.Now().UTC()}
}

// ActionForOperation maps persisted Mac intent names to safe audit actions.
func ActionForOperation(operation string) (Action, bool) {
	switch operation {
	case "create_session":
		return ActionCreate, true
	case "submit_command":
		return ActionSubmit, true
	case "cancel_command":
		return ActionCancel, true
	case "close_session":
		return ActionClose, true
	case "run":
		return ActionRun, true
	default:
		return "", false
	}
}

// Validate checks that a record contains only supported bounded metadata.
func (record Record) Validate() error {
	if record.ID < 0 {
		return fmt.Errorf("%w: negative row ID", ErrInvalidRecord)
	}
	if _, err := domain.NewControllerIdentity(record.Principal.Type(), record.Principal.ID()); err != nil {
		return fmt.Errorf("%w: principal", ErrInvalidRecord)
	}
	if !ValidIngress(record.Ingress) {
		return fmt.Errorf("%w: ingress", ErrInvalidRecord)
	}
	switch record.Action {
	case ActionCreate, ActionSubmit, ActionCancel, ActionClose, ActionRun, ActionReadSession, ActionReadCommand, ActionReadJob, ActionRuntimeCleanup:
	default:
		return fmt.Errorf("%w: action", ErrInvalidRecord)
	}
	switch record.Outcome {
	case OutcomeAllowed:
		if record.Action == ActionRuntimeCleanup || record.ReasonCode != "" {
			return fmt.Errorf("%w: allowed outcome is invalid for this action or has a reason", ErrInvalidRecord)
		}
	case OutcomeDenied:
		if record.Action == ActionRuntimeCleanup || record.ReasonCode == "" || record.ReasonCode == ReasonRuntimeCleanupUnconfirmed {
			return fmt.Errorf("%w: denied outcome requires an authorization reason", ErrInvalidRecord)
		}
	case OutcomeFailed:
		if record.Action != ActionRuntimeCleanup || record.ReasonCode != ReasonRuntimeCleanupUnconfirmed || record.SessionID == "" {
			return fmt.Errorf("%w: failed outcome must identify an unconfirmed runtime cleanup", ErrInvalidRecord)
		}
	default:
		return fmt.Errorf("%w: outcome", ErrInvalidRecord)
	}
	if record.ReasonCode != "" && record.ReasonCode != ReasonEnvironmentDenied && record.ReasonCode != ReasonPolicyDenied && record.ReasonCode != ReasonControllerDenied && record.ReasonCode != ReasonRuntimeCleanupUnconfirmed {
		return fmt.Errorf("%w: reason code", ErrInvalidRecord)
	}
	if len(record.Environment) > 256 || strings.IndexByte(record.Environment, 0) >= 0 {
		return fmt.Errorf("%w: environment", ErrInvalidRecord)
	}
	if record.SessionID != "" {
		if _, err := domain.NewSessionID(string(record.SessionID)); err != nil {
			return fmt.Errorf("%w: session ID", ErrInvalidRecord)
		}
	}
	if record.CommandID != "" {
		if _, err := domain.NewCommandID(string(record.CommandID)); err != nil {
			return fmt.Errorf("%w: command ID", ErrInvalidRecord)
		}
	}
	if record.JobID != "" {
		if _, err := domain.NewJobID(string(record.JobID)); err != nil {
			return fmt.Errorf("%w: job ID", ErrInvalidRecord)
		}
	}
	if record.OccurredAt.IsZero() {
		return fmt.Errorf("%w: timestamp", ErrInvalidRecord)
	}
	if record.MailboxSelection != nil {
		if record.Ingress != IngressMailbox || record.Outcome != OutcomeAllowed ||
			(record.Action != ActionCreate && record.Action != ActionRun) {
			return fmt.Errorf("%w: mailbox selection only applies to allowed mailbox create or run", ErrInvalidRecord)
		}
		if err := record.MailboxSelection.Validate(); err != nil {
			return err
		}
		if record.Environment != record.MailboxSelection.Environment {
			return fmt.Errorf("%w: mailbox selection environment", ErrInvalidRecord)
		}
	}
	return nil
}

// Log emits exactly the fields stored in the audit row. Callers must invoke it
// only after the corresponding database transaction commits.
func Log(ctx context.Context, record Record) {
	level, message := slog.LevelInfo, "runner authorization action"
	if record.Action == ActionRuntimeCleanup {
		level, message = slog.LevelError, "runner runtime cleanup failure"
	}
	attributes := []any{
		"record_id", record.ID,
		"action", string(record.Action),
		"principal_type", string(record.Principal.Type()),
		"principal_id", string(record.Principal.ID()),
		"ingress", string(record.Ingress),
		"environment", record.Environment,
		"session_id", string(record.SessionID),
		"command_id", string(record.CommandID),
		"job_id", string(record.JobID),
		"outcome", string(record.Outcome),
		"reason_code", record.ReasonCode,
		"occurred_at", record.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	if record.MailboxSelection != nil {
		selection := record.MailboxSelection
		attributes = append(attributes,
			"mailbox_id", selection.InboxID,
			"execution_context", selection.ContextName,
			"execution_selection_source", selection.Source,
			"resolved_target_kind", string(selection.TargetKind),
			"resolved_target_profile", selection.TargetProfile,
			"repository_alias", selection.RepositoryAlias,
			"repository_aliases", append([]string(nil), selection.RepositoryAliases...),
		)
	}
	slog.Default().Log(ctx, level, message, attributes...)
}
