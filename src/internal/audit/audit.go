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
	IngressLocalUnix    Ingress = "local_unix"
	IngressMailbox      Ingress = "mailbox"
	IngressLocalWorker  Ingress = "local_executor"
	IngressSSHBridge    Ingress = "ssh_bridge"
	IngressDirectMTLS   Ingress = "direct_mtls"
	IngressInternalTest Ingress = "internal"
)

type Action string

const (
	ActionCreate      Action = "create"
	ActionSubmit      Action = "submit"
	ActionCancel      Action = "cancel"
	ActionClose       Action = "close"
	ActionRun         Action = "run"
	ActionReadSession Action = "read_session"
	ActionReadCommand Action = "read_command"
	ActionReadJob     Action = "read_job"
)

type Outcome string

const (
	OutcomeAllowed Outcome = "allowed"
	OutcomeDenied  Outcome = "denied"
)

const (
	ReasonEnvironmentDenied = "environment_denied"
	ReasonPolicyDenied      = "policy_denied"
	ReasonControllerDenied  = "controller_denied"
)

var ErrInvalidRecord = errors.New("invalid audit record")

// Record contains only identifiers and bounded policy metadata. It has no
// fields for request bodies, scripts, output, keys, certificates, or secrets.
type Record struct {
	ID          int64
	Principal   domain.ControllerIdentity
	Ingress     Ingress
	Environment string
	SessionID   domain.SessionID
	CommandID   domain.CommandID
	JobID       domain.JobID
	Action      Action
	Outcome     Outcome
	ReasonCode  string
	OccurredAt  time.Time
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
	switch record.Ingress {
	case IngressLocalUnix, IngressMailbox, IngressLocalWorker, IngressSSHBridge, IngressDirectMTLS, IngressInternalTest:
	default:
		return fmt.Errorf("%w: ingress", ErrInvalidRecord)
	}
	switch record.Action {
	case ActionCreate, ActionSubmit, ActionCancel, ActionClose, ActionRun, ActionReadSession, ActionReadCommand, ActionReadJob:
	default:
		return fmt.Errorf("%w: action", ErrInvalidRecord)
	}
	if record.Outcome != OutcomeAllowed && record.Outcome != OutcomeDenied {
		return fmt.Errorf("%w: outcome", ErrInvalidRecord)
	}
	if record.Outcome == OutcomeDenied && record.ReasonCode == "" {
		return fmt.Errorf("%w: denied outcome requires a reason code", ErrInvalidRecord)
	}
	if record.Outcome == OutcomeAllowed && record.ReasonCode != "" {
		return fmt.Errorf("%w: allowed outcome cannot have a denial reason", ErrInvalidRecord)
	}
	if record.ReasonCode != "" && record.ReasonCode != ReasonEnvironmentDenied && record.ReasonCode != ReasonPolicyDenied && record.ReasonCode != ReasonControllerDenied {
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
	return nil
}

// Log emits exactly the fields stored in the audit row. Callers must invoke it
// only after the corresponding database transaction commits.
func Log(ctx context.Context, record Record) {
	slog.Default().InfoContext(ctx, "runner authorization action",
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
	)
}
