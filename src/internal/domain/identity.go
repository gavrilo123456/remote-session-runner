package domain

import (
	"errors"
	"fmt"
)

// ErrEmptyID is returned when an opaque identifier has no value.
var ErrEmptyID = errors.New("domain ID must not be empty")

// ErrInvalidControllerType is returned when a controller identity uses an
// unsupported authentication namespace.
var ErrInvalidControllerType = errors.New("invalid controller type")

// ErrInvalidTargetKind is returned when an execution target is neither local
// nor remote.
var ErrInvalidTargetKind = errors.New("invalid execution target kind")

// ErrEmptyTargetProfile is returned when an execution target has no profile.
var ErrEmptyTargetProfile = errors.New("execution target profile must not be empty")

// SessionID identifies one execution session.
type SessionID string

// CommandID identifies one command within the deployment.
type CommandID string

// JobID identifies one one-off execution job.
type JobID string

// RequestID identifies one mailbox request exchange.
type RequestID string

// IntentID identifies one durable Mac-local intent.
type IntentID string

// ControllerID identifies a caller within its controller type namespace.
type ControllerID string

func validateID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s: %w", name, ErrEmptyID)
	}
	return nil
}

// NewSessionID creates a session ID without changing its opaque value.
func NewSessionID(value string) (SessionID, error) {
	if err := validateID("session ID", value); err != nil {
		return "", err
	}
	return SessionID(value), nil
}

// NewCommandID creates a command ID without changing its opaque value.
func NewCommandID(value string) (CommandID, error) {
	if err := validateID("command ID", value); err != nil {
		return "", err
	}
	return CommandID(value), nil
}

// NewJobID creates a job ID without changing its opaque value.
func NewJobID(value string) (JobID, error) {
	if err := validateID("job ID", value); err != nil {
		return "", err
	}
	return JobID(value), nil
}

// NewRequestID creates a mailbox request ID without changing its opaque value.
func NewRequestID(value string) (RequestID, error) {
	if err := validateID("request ID", value); err != nil {
		return "", err
	}
	return RequestID(value), nil
}

// NewIntentID creates an intent ID without changing its opaque value.
func NewIntentID(value string) (IntentID, error) {
	if err := validateID("intent ID", value); err != nil {
		return "", err
	}
	return IntentID(value), nil
}

// NewControllerID creates an ID within a controller type namespace.
func NewControllerID(value string) (ControllerID, error) {
	if err := validateID("controller ID", value); err != nil {
		return "", err
	}
	return ControllerID(value), nil
}

// ControllerType names an authentication namespace. The same ControllerID in
// different namespaces represents a different controller.
type ControllerType string

const (
	ControllerTypeLocalUser  ControllerType = "local_user"
	ControllerTypeQueuedMac  ControllerType = "queued_mac"
	ControllerTypeDirectMTLS ControllerType = "direct_mtls"
)

// ControllerIdentity is an immutable controller type/ID pair.
type ControllerIdentity struct {
	typeName ControllerType
	id       ControllerID
}

// NewControllerIdentity constructs one of the selected controller namespaces.
func NewControllerIdentity(typeName ControllerType, id ControllerID) (ControllerIdentity, error) {
	switch typeName {
	case ControllerTypeLocalUser, ControllerTypeQueuedMac, ControllerTypeDirectMTLS:
	default:
		return ControllerIdentity{}, fmt.Errorf("%w: %q", ErrInvalidControllerType, typeName)
	}
	if err := validateID("controller ID", string(id)); err != nil {
		return ControllerIdentity{}, err
	}
	return ControllerIdentity{typeName: typeName, id: id}, nil
}

// Type returns the controller's authentication namespace.
func (c ControllerIdentity) Type() ControllerType { return c.typeName }

// ID returns the controller's opaque ID.
func (c ControllerIdentity) ID() ControllerID { return c.id }

// TargetKind selects the machine on which a session executes.
type TargetKind string

const (
	TargetKindLocal  TargetKind = "local"
	TargetKindRemote TargetKind = "remote"
)

// ExecutionTarget is a value-style, immutable target selection. Environment
// compatibility and profile policy are validated by the later environment
// validation layer.
type ExecutionTarget struct {
	kind    TargetKind
	profile string
}

// NewExecutionTarget constructs a target with a supported kind and a profile.
func NewExecutionTarget(kind TargetKind, profile string) (ExecutionTarget, error) {
	switch kind {
	case TargetKindLocal, TargetKindRemote:
	default:
		return ExecutionTarget{}, fmt.Errorf("%w: %q", ErrInvalidTargetKind, kind)
	}
	if profile == "" {
		return ExecutionTarget{}, ErrEmptyTargetProfile
	}
	return ExecutionTarget{kind: kind, profile: profile}, nil
}

// Kind returns the selected local or remote execution kind.
func (t ExecutionTarget) Kind() TargetKind { return t.kind }

// Profile returns the configured target profile name.
func (t ExecutionTarget) Profile() string { return t.profile }
