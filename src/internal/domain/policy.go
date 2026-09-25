package domain

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

var (
	ErrInvalidEnvironment              = errors.New("invalid environment definition")
	ErrInvalidSource                   = errors.New("invalid source")
	ErrEnvironmentTargetMismatch       = errors.New("environment does not allow target/profile")
	ErrEnvironmentSourceMismatch       = errors.New("environment does not allow source")
	ErrRepositoryAliasNotAllowed       = errors.New("repository alias is not allowed")
	ErrControllerMismatch              = errors.New("controller is not allowed by environment")
	ErrUnsupportedIsolationRequirement = errors.New("requested host isolation is unavailable")
	ErrInvalidServiceLimits            = errors.New("invalid service limits")
	ErrInvalidRequestedLimits          = errors.New("invalid requested limits")
	ErrLimitExceedsServiceCeiling      = errors.New("requested limit exceeds service ceiling")
)

// SourceMode selects how an environment obtains a session's initial workspace.
type SourceMode string

const (
	SourceModeEmpty         SourceMode = "empty"
	SourceModeGitRevision   SourceMode = "git_revision"
	SourceModeLocalWorktree SourceMode = "local_worktree"
)

// Source is an immutable workspace-source request. Git resolution and
// filesystem access checks belong to the selected host's runtime adapter.
type Source struct {
	mode              SourceMode
	repositoryAlias   string
	requestedRevision string
	path              string
}

// NewEmptySource selects a new empty workspace.
func NewEmptySource() Source { return Source{mode: SourceModeEmpty} }

// NewGitRevisionSource selects a configured repository alias and revision.
func NewGitRevisionSource(repositoryAlias, requestedRevision string) (Source, error) {
	if repositoryAlias == "" || requestedRevision == "" {
		return Source{}, fmt.Errorf("%w: git_revision requires alias and revision", ErrInvalidSource)
	}
	return Source{
		mode:              SourceModeGitRevision,
		repositoryAlias:   repositoryAlias,
		requestedRevision: requestedRevision,
	}, nil
}

// NewLocalWorktreeSource selects an existing absolute local path. This pure
// constructor does not inspect the filesystem or claim that the path confines
// commands to that directory.
func NewLocalWorktreeSource(path string) (Source, error) {
	if !filepath.IsAbs(path) {
		return Source{}, fmt.Errorf("%w: local_worktree path must be absolute", ErrInvalidSource)
	}
	return Source{mode: SourceModeLocalWorktree, path: path}, nil
}

// Mode returns the source mode.
func (s Source) Mode() SourceMode { return s.mode }

// RepositoryAlias returns the configured alias for git_revision sources.
func (s Source) RepositoryAlias() string { return s.repositoryAlias }

// RequestedRevision returns the requested Git revision before host resolution.
func (s Source) RequestedRevision() string { return s.requestedRevision }

// Path returns the caller-selected local_worktree path.
func (s Source) Path() string { return s.path }

// Portable reports whether a source is independent of a caller's existing
// local path.
func (s Source) Portable() bool {
	return s.mode == SourceModeEmpty || s.mode == SourceModeGitRevision
}

func (s Source) valid() bool {
	switch s.mode {
	case SourceModeEmpty:
		return s.repositoryAlias == "" && s.requestedRevision == "" && s.path == ""
	case SourceModeGitRevision:
		return s.repositoryAlias != "" && s.requestedRevision != "" && s.path == ""
	case SourceModeLocalWorktree:
		return s.repositoryAlias == "" && s.requestedRevision == "" && filepath.IsAbs(s.path)
	default:
		return false
	}
}

// ServiceLimits are Runner-enforced ceilings. They describe admission,
// timeout, request, output, buffering, and retention controls; they do not
// describe host CPU, memory, PID, disk, network, mount, privilege, or
// filesystem isolation.
type ServiceLimits struct {
	ActiveSessionsPerHost  int
	RunningCommandsPerHost int
	SerializedRequestBytes int64
	ScriptBytesPerRequest  int64
	CommandTimeout         time.Duration
	IdleTimeout            time.Duration
	SessionMaxLifetime     time.Duration
	OutputBytesPerCommand  int64
	SubscriberBufferBytes  int64
	PersistenceQueueBytes  int64
	MetadataRetention      time.Duration
	OutputRetention        time.Duration
}

// DefaultServiceLimits returns the selected PoC starting ceilings.
func DefaultServiceLimits() ServiceLimits {
	return ServiceLimits{
		ActiveSessionsPerHost:  20,
		RunningCommandsPerHost: 4,
		SerializedRequestBytes: 1 << 20,
		ScriptBytesPerRequest:  128 << 10,
		CommandTimeout:         30 * time.Minute,
		IdleTimeout:            30 * time.Minute,
		SessionMaxLifetime:     4 * time.Hour,
		OutputBytesPerCommand:  100 << 20,
		SubscriberBufferBytes:  1 << 20,
		PersistenceQueueBytes:  16 << 20,
		MetadataRetention:      90 * 24 * time.Hour,
		OutputRetention:        30 * 24 * time.Hour,
	}
}

func (limits ServiceLimits) valid() bool {
	return limits.ActiveSessionsPerHost > 0 &&
		limits.RunningCommandsPerHost > 0 &&
		limits.SerializedRequestBytes > 0 &&
		limits.ScriptBytesPerRequest > 0 &&
		limits.CommandTimeout > 0 &&
		limits.IdleTimeout > 0 &&
		limits.SessionMaxLifetime > 0 &&
		limits.OutputBytesPerCommand > 0 &&
		limits.SubscriberBufferBytes > 0 &&
		limits.PersistenceQueueBytes > 0 &&
		limits.MetadataRetention > 0 &&
		limits.OutputRetention > 0
}

// EnvironmentSpec is the validated policy input used to construct an
// Environment. The constructor copies all slices so later edits to this value
// cannot change an environment already in use.
type EnvironmentSpec struct {
	Name                     string
	HostClass                string
	EffectiveAccount         string
	AllowedTargets           []ExecutionTarget
	AllowedSourceModes       []SourceMode
	AllowedRepositoryAliases []string
	AllowedControllers       []ControllerIdentity
	ServiceLimits            ServiceLimits
}

// Environment is an immutable policy definition for a target profile.
type Environment struct {
	name                     string
	hostClass                string
	effectiveAccount         string
	allowedTargets           []ExecutionTarget
	allowedSourceModes       []SourceMode
	allowedRepositoryAliases []string
	allowedControllers       []ControllerIdentity
	serviceLimits            ServiceLimits
}

// NewEnvironment validates and freezes an environment policy definition.
func NewEnvironment(spec EnvironmentSpec) (Environment, error) {
	if spec.Name == "" || spec.HostClass == "" || spec.EffectiveAccount == "" ||
		len(spec.AllowedTargets) == 0 || len(spec.AllowedSourceModes) == 0 ||
		len(spec.AllowedControllers) == 0 {
		return Environment{}, ErrInvalidEnvironment
	}
	if !spec.ServiceLimits.valid() {
		return Environment{}, fmt.Errorf("%w: %w", ErrInvalidEnvironment, ErrInvalidServiceLimits)
	}

	targets := append([]ExecutionTarget(nil), spec.AllowedTargets...)
	seenTargets := make(map[string]struct{}, len(targets))
	hasLocalTarget := false
	for _, target := range targets {
		if target.Profile() == "" || (target.Kind() != TargetKindLocal && target.Kind() != TargetKindRemote) {
			return Environment{}, fmt.Errorf("%w: invalid target", ErrInvalidEnvironment)
		}
		if target.Kind() == TargetKindLocal {
			hasLocalTarget = true
		}
		key := string(target.Kind()) + "\x00" + target.Profile()
		if _, exists := seenTargets[key]; exists {
			return Environment{}, fmt.Errorf("%w: duplicate target/profile", ErrInvalidEnvironment)
		}
		seenTargets[key] = struct{}{}
	}

	sourceModes := append([]SourceMode(nil), spec.AllowedSourceModes...)
	seenModes := make(map[SourceMode]struct{}, len(sourceModes))
	allowsGitRevision := false
	allowsLocalWorktree := false
	for _, mode := range sourceModes {
		switch mode {
		case SourceModeEmpty:
		case SourceModeGitRevision:
			allowsGitRevision = true
		case SourceModeLocalWorktree:
			allowsLocalWorktree = true
		default:
			return Environment{}, fmt.Errorf("%w: unknown source mode %q", ErrInvalidEnvironment, mode)
		}
		if _, exists := seenModes[mode]; exists {
			return Environment{}, fmt.Errorf("%w: duplicate source mode %q", ErrInvalidEnvironment, mode)
		}
		seenModes[mode] = struct{}{}
	}
	if allowsLocalWorktree && !hasLocalTarget {
		return Environment{}, fmt.Errorf("%w: local_worktree requires a local target", ErrInvalidEnvironment)
	}

	aliases := append([]string(nil), spec.AllowedRepositoryAliases...)
	seenAliases := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if alias == "" {
			return Environment{}, fmt.Errorf("%w: repository alias must not be empty", ErrInvalidEnvironment)
		}
		if _, exists := seenAliases[alias]; exists {
			return Environment{}, fmt.Errorf("%w: duplicate repository alias %q", ErrInvalidEnvironment, alias)
		}
		seenAliases[alias] = struct{}{}
	}
	if allowsGitRevision && len(aliases) == 0 {
		return Environment{}, fmt.Errorf("%w: git_revision requires a configured repository alias", ErrInvalidEnvironment)
	}

	controllers := append([]ControllerIdentity(nil), spec.AllowedControllers...)
	seenControllers := make(map[string]struct{}, len(controllers))
	for _, controller := range controllers {
		if !validControllerIdentity(controller) {
			return Environment{}, fmt.Errorf("%w: invalid controller", ErrInvalidEnvironment)
		}
		key := string(controller.Type()) + "\x00" + string(controller.ID())
		if _, exists := seenControllers[key]; exists {
			return Environment{}, fmt.Errorf("%w: duplicate controller", ErrInvalidEnvironment)
		}
		seenControllers[key] = struct{}{}
	}

	return Environment{
		name:                     spec.Name,
		hostClass:                spec.HostClass,
		effectiveAccount:         spec.EffectiveAccount,
		allowedTargets:           targets,
		allowedSourceModes:       sourceModes,
		allowedRepositoryAliases: aliases,
		allowedControllers:       controllers,
		serviceLimits:            spec.ServiceLimits,
	}, nil
}

func validControllerIdentity(controller ControllerIdentity) bool {
	switch controller.Type() {
	case ControllerTypeLocalUser, ControllerTypeQueuedMac, ControllerTypeDirectMTLS:
		return controller.ID() != ""
	default:
		return false
	}
}

// Name returns the configured environment name.
func (e Environment) Name() string { return e.name }

// HostClass returns the selected host class label.
func (e Environment) HostClass() string { return e.hostClass }

// EffectiveAccount returns the OS account used by this environment.
func (e Environment) EffectiveAccount() string { return e.effectiveAccount }

// ServiceLimits returns this environment's Runner-enforced service ceilings.
func (e Environment) ServiceLimits() ServiceLimits { return e.serviceLimits }

// Capabilities reports the OS-user boundary and actual Runner service limits.
// It intentionally has no field that can advertise per-session host isolation.
func (e Environment) Capabilities() Capabilities {
	return Capabilities{
		hostClass:        e.hostClass,
		isolation:        IsolationOSUser,
		effectiveAccount: e.effectiveAccount,
		serviceLimits:    e.serviceLimits,
	}
}

// Capabilities describes controls the environment can truthfully enforce.
type Capabilities struct {
	hostClass        string
	isolation        IsolationLevel
	effectiveAccount string
	serviceLimits    ServiceLimits
}

// IsolationLevel names the actual OS boundary for this PoC.
type IsolationLevel string

const IsolationOSUser IsolationLevel = "os-user"

// HostClass returns the environment's host class.
func (c Capabilities) HostClass() string { return c.hostClass }

// Isolation returns the truthful OS-user boundary.
func (c Capabilities) Isolation() IsolationLevel { return c.isolation }

// EffectiveAccount returns the OS account running the command process.
func (c Capabilities) EffectiveAccount() string { return c.effectiveAccount }

// ServiceLimits returns only Runner-enforced service ceilings.
func (c Capabilities) ServiceLimits() ServiceLimits { return c.serviceLimits }

// IsolationRequirements describes per-session host controls a caller requires.
// Every listed control is unavailable in the selected host-process profiles.
type IsolationRequirements struct {
	FilesystemBoundary bool
	CPUControl         bool
	MemoryControl      bool
	PIDControl         bool
	DiskControl        bool
	NetworkControl     bool
	Mounts             bool
	Volumes            bool
	PrivilegeControl   bool
}

func (r IsolationRequirements) unsupportedNames() []string {
	var names []string
	if r.FilesystemBoundary {
		names = append(names, "filesystem")
	}
	if r.CPUControl {
		names = append(names, "cpu")
	}
	if r.MemoryControl {
		names = append(names, "memory")
	}
	if r.PIDControl {
		names = append(names, "pid")
	}
	if r.DiskControl {
		names = append(names, "disk")
	}
	if r.NetworkControl {
		names = append(names, "network")
	}
	if r.Mounts {
		names = append(names, "mounts")
	}
	if r.Volumes {
		names = append(names, "volumes")
	}
	if r.PrivilegeControl {
		names = append(names, "privilege")
	}
	return names
}

// RequestedLimits contains optional per-session reductions of the environment
// limits. A zero value means the environment's selected value is inherited.
type RequestedLimits struct {
	CommandTimeout        time.Duration
	IdleTimeout           time.Duration
	SessionMaxLifetime    time.Duration
	OutputBytesPerCommand int64
}

// EffectiveSessionLimits contains the validated per-session service limits.
type EffectiveSessionLimits struct {
	CommandTimeout        time.Duration
	IdleTimeout           time.Duration
	SessionMaxLifetime    time.Duration
	OutputBytesPerCommand int64
}

// SessionPolicyRequest combines the immutable session inputs with requested
// limits and any host-isolation controls the caller requires.
type SessionPolicyRequest struct {
	Target     ExecutionTarget
	Source     Source
	Controller ControllerIdentity
	Limits     RequestedLimits
	Isolation  IsolationRequirements
}

// ValidateSessionPolicy checks compatibility and returns effective session
// limits without touching the filesystem, runtime, or a store.
func (e Environment) ValidateSessionPolicy(request SessionPolicyRequest) (EffectiveSessionLimits, error) {
	if request.Target.Kind() != TargetKindLocal && request.Target.Kind() != TargetKindRemote {
		return EffectiveSessionLimits{}, ErrInvalidTargetKind
	}
	if request.Target.Profile() == "" {
		return EffectiveSessionLimits{}, ErrEmptyTargetProfile
	}
	if !containsTarget(e.allowedTargets, request.Target) {
		return EffectiveSessionLimits{}, fmt.Errorf("%w: %s/%s", ErrEnvironmentTargetMismatch, request.Target.Kind(), request.Target.Profile())
	}
	if !request.Source.valid() {
		return EffectiveSessionLimits{}, ErrInvalidSource
	}
	if !containsSourceMode(e.allowedSourceModes, request.Source.Mode()) {
		return EffectiveSessionLimits{}, fmt.Errorf("%w: %s", ErrEnvironmentSourceMismatch, request.Source.Mode())
	}
	if request.Source.Mode() == SourceModeLocalWorktree && request.Target.Kind() != TargetKindLocal {
		return EffectiveSessionLimits{}, fmt.Errorf("%w: local_worktree is local-only", ErrEnvironmentSourceMismatch)
	}
	if request.Source.Mode() == SourceModeGitRevision && !containsString(e.allowedRepositoryAliases, request.Source.RepositoryAlias()) {
		return EffectiveSessionLimits{}, fmt.Errorf("%w: %s", ErrRepositoryAliasNotAllowed, request.Source.RepositoryAlias())
	}
	if !validControllerIdentity(request.Controller) || !containsController(e.allowedControllers, request.Controller) {
		return EffectiveSessionLimits{}, ErrControllerMismatch
	}
	if names := request.Isolation.unsupportedNames(); len(names) > 0 {
		return EffectiveSessionLimits{}, fmt.Errorf("%w: %v", ErrUnsupportedIsolationRequirement, names)
	}
	return effectiveSessionLimits(e.serviceLimits, request.Limits)
}

func containsTarget(targets []ExecutionTarget, want ExecutionTarget) bool {
	for _, target := range targets {
		if target.Kind() == want.Kind() && target.Profile() == want.Profile() {
			return true
		}
	}
	return false
}

func containsSourceMode(modes []SourceMode, want SourceMode) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsController(controllers []ControllerIdentity, want ControllerIdentity) bool {
	for _, controller := range controllers {
		if controller.Type() == want.Type() && controller.ID() == want.ID() {
			return true
		}
	}
	return false
}

func effectiveSessionLimits(ceilings ServiceLimits, requested RequestedLimits) (EffectiveSessionLimits, error) {
	commandTimeout, err := requestedDuration("command timeout", requested.CommandTimeout, ceilings.CommandTimeout)
	if err != nil {
		return EffectiveSessionLimits{}, err
	}
	idleTimeout, err := requestedDuration("idle timeout", requested.IdleTimeout, ceilings.IdleTimeout)
	if err != nil {
		return EffectiveSessionLimits{}, err
	}
	sessionMaxLifetime, err := requestedDuration("session maximum lifetime", requested.SessionMaxLifetime, ceilings.SessionMaxLifetime)
	if err != nil {
		return EffectiveSessionLimits{}, err
	}
	outputBytes, err := requestedBytes("command output", requested.OutputBytesPerCommand, ceilings.OutputBytesPerCommand)
	if err != nil {
		return EffectiveSessionLimits{}, err
	}
	return EffectiveSessionLimits{
		CommandTimeout:        commandTimeout,
		IdleTimeout:           idleTimeout,
		SessionMaxLifetime:    sessionMaxLifetime,
		OutputBytesPerCommand: outputBytes,
	}, nil
}

func requestedDuration(name string, requested, ceiling time.Duration) (time.Duration, error) {
	if requested < 0 {
		return 0, fmt.Errorf("%w: %s is negative", ErrInvalidRequestedLimits, name)
	}
	if requested == 0 {
		return ceiling, nil
	}
	if requested > ceiling {
		return 0, fmt.Errorf("%w: %s", ErrLimitExceedsServiceCeiling, name)
	}
	return requested, nil
}

func requestedBytes(name string, requested, ceiling int64) (int64, error) {
	if requested < 0 {
		return 0, fmt.Errorf("%w: %s is negative", ErrInvalidRequestedLimits, name)
	}
	if requested == 0 {
		return ceiling, nil
	}
	if requested > ceiling {
		return 0, fmt.Errorf("%w: %s", ErrLimitExceedsServiceCeiling, name)
	}
	return requested, nil
}
