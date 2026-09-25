package domain

import (
	"errors"
	"testing"
	"time"
)

const (
	p006MacAccount   = "tomasz.walczuk"
	p006LinuxAccount = "ubuntu"
	p006TestAlias    = "fixture-repository"
)

func p006MustTarget(t *testing.T, kind TargetKind, profile string) ExecutionTarget {
	t.Helper()
	target, err := NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatalf("construct target %q/%q: %v", kind, profile, err)
	}
	return target
}

func p006MustController(t *testing.T, kind ControllerType, id string) ControllerIdentity {
	t.Helper()
	controller, err := NewControllerIdentity(kind, ControllerID(id))
	if err != nil {
		t.Fatalf("construct controller %q/%q: %v", kind, id, err)
	}
	return controller
}

func p006MacEnvironment(t *testing.T, withGitAlias bool) Environment {
	t.Helper()
	sourceModes := []SourceMode{SourceModeEmpty, SourceModeLocalWorktree}
	aliases := []string(nil)
	if withGitAlias {
		sourceModes = append(sourceModes, SourceModeGitRevision)
		aliases = []string{p006TestAlias}
	}
	environment, err := NewEnvironment(EnvironmentSpec{
		Name:                     "mac-dev",
		HostClass:                "macOS workstation",
		EffectiveAccount:         p006MacAccount,
		AllowedTargets:           []ExecutionTarget{p006MustTarget(t, TargetKindLocal, "mac-workstation")},
		AllowedSourceModes:       sourceModes,
		AllowedRepositoryAliases: aliases,
		AllowedControllers:       []ControllerIdentity{p006MustController(t, ControllerTypeLocalUser, p006MacAccount)},
		ServiceLimits:            DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatalf("construct Mac environment: %v", err)
	}
	return environment
}

func p006LinuxEnvironment(t *testing.T) Environment {
	t.Helper()
	environment, err := NewEnvironment(EnvironmentSpec{
		Name:               "linux-dev",
		HostClass:          "Ubuntu Linux host",
		EffectiveAccount:   p006LinuxAccount,
		AllowedTargets:     []ExecutionTarget{p006MustTarget(t, TargetKindRemote, "linux-host")},
		AllowedSourceModes: []SourceMode{SourceModeEmpty},
		AllowedControllers: []ControllerIdentity{p006MustController(t, ControllerTypeQueuedMac, p006MacAccount), p006MustController(t, ControllerTypeDirectMTLS, p006MacAccount)},
		ServiceLimits:      DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatalf("construct Linux environment: %v", err)
	}
	return environment
}

func p006EmptyRequest(t *testing.T, target ExecutionTarget, controller ControllerIdentity) SessionPolicyRequest {
	t.Helper()
	return SessionPolicyRequest{Target: target, Source: NewEmptySource(), Controller: controller}
}

func TestP006SelectedEnvironmentPolicies(t *testing.T) {
	mac := p006MacEnvironment(t, false)
	linux := p006LinuxEnvironment(t)

	tests := []struct {
		name        string
		environment Environment
		request     SessionPolicyRequest
		envName     string
		hostClass   string
		account     string
	}{
		{
			name:        "mac local owner controller",
			environment: mac,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindLocal, "mac-workstation"),
				p006MustController(t, ControllerTypeLocalUser, p006MacAccount)),
			envName:   "mac-dev",
			hostClass: "macOS workstation",
			account:   p006MacAccount,
		},
		{
			name:        "mac local worktree source",
			environment: mac,
			request: SessionPolicyRequest{
				Target:     p006MustTarget(t, TargetKindLocal, "mac-workstation"),
				Source:     mustP006LocalWorktree(t, "/Users/tomasz.walczuk/projects/example"),
				Controller: p006MustController(t, ControllerTypeLocalUser, p006MacAccount),
			},
			envName:   "mac-dev",
			hostClass: "macOS workstation",
			account:   p006MacAccount,
		},
		{
			name:        "linux queued Mac controller",
			environment: linux,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindRemote, "linux-host"),
				p006MustController(t, ControllerTypeQueuedMac, p006MacAccount)),
			envName:   "linux-dev",
			hostClass: "Ubuntu Linux host",
			account:   p006LinuxAccount,
		},
		{
			name:        "linux direct mTLS controller",
			environment: linux,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindRemote, "linux-host"),
				p006MustController(t, ControllerTypeDirectMTLS, p006MacAccount)),
			envName:   "linux-dev",
			hostClass: "Ubuntu Linux host",
			account:   p006LinuxAccount,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits, err := test.environment.ValidateSessionPolicy(test.request)
			if err != nil {
				t.Fatalf("validate selected policy: %v", err)
			}
			if want := DefaultServiceLimits(); limits.CommandTimeout != want.CommandTimeout || limits.IdleTimeout != want.IdleTimeout || limits.SessionMaxLifetime != want.SessionMaxLifetime || limits.OutputBytesPerCommand != want.OutputBytesPerCommand {
				t.Errorf("inherited limits = %+v, want values from %+v", limits, want)
			}
			capabilities := test.environment.Capabilities()
			if test.environment.Name() != test.envName {
				t.Errorf("environment name = %q, want %q", test.environment.Name(), test.envName)
			}
			if capabilities.HostClass() != test.hostClass {
				t.Errorf("reported host class = %q, want %q", capabilities.HostClass(), test.hostClass)
			}
			if capabilities.Isolation() != IsolationOSUser {
				t.Errorf("reported isolation = %q, want %q", capabilities.Isolation(), IsolationOSUser)
			}
			if capabilities.EffectiveAccount() != test.account {
				t.Errorf("reported account = %q, want %q", capabilities.EffectiveAccount(), test.account)
			}
			if capabilities.ServiceLimits() != DefaultServiceLimits() {
				t.Errorf("reported service limits = %+v, want selected ceilings %+v", capabilities.ServiceLimits(), DefaultServiceLimits())
			}
		})
	}
}

func TestP006DefaultServiceLimitsMatchSelectedPoC(t *testing.T) {
	want := ServiceLimits{
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
	if got := DefaultServiceLimits(); got != want {
		t.Fatalf("default service limits = %+v, want selected PoC ceilings %+v", got, want)
	}
}

func mustP006LocalWorktree(t *testing.T, path string) Source {
	t.Helper()
	source, err := NewLocalWorktreeSource(path)
	if err != nil {
		t.Fatalf("construct local_worktree source: %v", err)
	}
	return source
}

func TestP006TargetAndControllerMismatchesReject(t *testing.T) {
	mac := p006MacEnvironment(t, false)
	linux := p006LinuxEnvironment(t)

	tests := []struct {
		name        string
		environment Environment
		request     SessionPolicyRequest
		want        error
	}{
		{
			name:        "Mac environment rejects remote target",
			environment: mac,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindRemote, "linux-host"),
				p006MustController(t, ControllerTypeLocalUser, p006MacAccount)),
			want: ErrEnvironmentTargetMismatch,
		},
		{
			name:        "Mac environment rejects wrong local profile",
			environment: mac,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindLocal, "linux-host"),
				p006MustController(t, ControllerTypeLocalUser, p006MacAccount)),
			want: ErrEnvironmentTargetMismatch,
		},
		{
			name:        "Linux environment rejects local target",
			environment: linux,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindLocal, "mac-workstation"),
				p006MustController(t, ControllerTypeDirectMTLS, p006MacAccount)),
			want: ErrEnvironmentTargetMismatch,
		},
		{
			name:        "Mac rejects direct controller",
			environment: mac,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindLocal, "mac-workstation"),
				p006MustController(t, ControllerTypeDirectMTLS, p006MacAccount)),
			want: ErrControllerMismatch,
		},
		{
			name:        "Linux rejects local owner controller",
			environment: linux,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindRemote, "linux-host"),
				p006MustController(t, ControllerTypeLocalUser, p006MacAccount)),
			want: ErrControllerMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.environment.ValidateSessionPolicy(test.request); !errors.Is(err, test.want) {
				t.Fatalf("validation error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestP006SourcePoliciesAndAliasAllowlist(t *testing.T) {
	macWithoutGit := p006MacEnvironment(t, false)
	macWithGitFixture := p006MacEnvironment(t, true)
	linux := p006LinuxEnvironment(t)
	macTarget := p006MustTarget(t, TargetKindLocal, "mac-workstation")
	macController := p006MustController(t, ControllerTypeLocalUser, p006MacAccount)
	linuxTarget := p006MustTarget(t, TargetKindRemote, "linux-host")
	linuxController := p006MustController(t, ControllerTypeDirectMTLS, p006MacAccount)

	gitSource, err := NewGitRevisionSource(p006TestAlias, "main")
	if err != nil {
		t.Fatal(err)
	}
	allowedGit := SessionPolicyRequest{Target: macTarget, Source: gitSource, Controller: macController}
	if _, err := macWithGitFixture.ValidateSessionPolicy(allowedGit); err != nil {
		t.Fatalf("configured fixture alias should pass: %v", err)
	}

	wrongAlias, err := NewGitRevisionSource("arbitrary-url", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := macWithGitFixture.ValidateSessionPolicy(SessionPolicyRequest{Target: macTarget, Source: wrongAlias, Controller: macController}); !errors.Is(err, ErrRepositoryAliasNotAllowed) {
		t.Errorf("unconfigured repository alias error = %v, want ErrRepositoryAliasNotAllowed", err)
	}
	if _, err := macWithoutGit.ValidateSessionPolicy(allowedGit); !errors.Is(err, ErrEnvironmentSourceMismatch) {
		t.Errorf("kickoff Mac git_revision error = %v, want ErrEnvironmentSourceMismatch", err)
	}
	if _, err := linux.ValidateSessionPolicy(SessionPolicyRequest{Target: linuxTarget, Source: gitSource, Controller: linuxController}); !errors.Is(err, ErrEnvironmentSourceMismatch) {
		t.Errorf("kickoff Linux git_revision error = %v, want ErrEnvironmentSourceMismatch", err)
	}
	worktree := mustP006LocalWorktree(t, "/tmp/p006-worktree")
	if _, err := linux.ValidateSessionPolicy(SessionPolicyRequest{Target: linuxTarget, Source: worktree, Controller: linuxController}); !errors.Is(err, ErrEnvironmentSourceMismatch) {
		t.Errorf("Linux local_worktree error = %v, want ErrEnvironmentSourceMismatch", err)
	}
	mixedTargetEnvironment, err := NewEnvironment(EnvironmentSpec{
		Name:               "mixed-target-test",
		HostClass:          "test host",
		EffectiveAccount:   "test-account",
		AllowedTargets:     []ExecutionTarget{macTarget, linuxTarget},
		AllowedSourceModes: []SourceMode{SourceModeEmpty, SourceModeLocalWorktree},
		AllowedControllers: []ControllerIdentity{macController, linuxController},
		ServiceLimits:      DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatalf("construct mixed-target fixture: %v", err)
	}
	if _, err := mixedTargetEnvironment.ValidateSessionPolicy(SessionPolicyRequest{Target: linuxTarget, Source: worktree, Controller: linuxController}); !errors.Is(err, ErrEnvironmentSourceMismatch) {
		t.Errorf("remote target local_worktree error = %v, want ErrEnvironmentSourceMismatch", err)
	}
	if _, err := macWithoutGit.ValidateSessionPolicy(SessionPolicyRequest{Target: macTarget, Source: worktree, Controller: macController}); err != nil {
		t.Errorf("Mac absolute local_worktree should pass pure policy validation: %v", err)
	}

	for _, test := range []struct {
		name  string
		alias string
		rev   string
	}{
		{name: "missing alias", alias: "", rev: "main"},
		{name: "missing revision", alias: p006TestAlias, rev: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewGitRevisionSource(test.alias, test.rev); !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("source constructor error = %v, want ErrInvalidSource", err)
			}
		})
	}
	if _, err := NewLocalWorktreeSource("relative/path"); !errors.Is(err, ErrInvalidSource) {
		t.Errorf("relative local_worktree error = %v, want ErrInvalidSource", err)
	}
	if !NewEmptySource().Portable() || !gitSource.Portable() || worktree.Portable() || (Source{}).Portable() {
		t.Error("source portability does not match empty, git_revision, local_worktree, and invalid zero-value sources")
	}
}

func TestP006InvalidTargetShapeRejects(t *testing.T) {
	environment := p006MacEnvironment(t, false)
	controller := p006MustController(t, ControllerTypeLocalUser, p006MacAccount)
	for _, test := range []struct {
		name string
		want error
		kind TargetKind
	}{
		{name: "zero value", want: ErrInvalidTargetKind, kind: ""},
		{name: "empty profile", want: ErrEmptyTargetProfile, kind: TargetKindLocal},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := p006EmptyRequest(t, ExecutionTarget{kind: test.kind}, controller)
			if _, err := environment.ValidateSessionPolicy(request); !errors.Is(err, test.want) {
				t.Fatalf("validation error = %v, want errors.Is(_, %v)", err, test.want)
			}
		})
	}
}

func TestP006UnsupportedIsolationRequirementsRejectOnBothProfiles(t *testing.T) {
	mac := p006MacEnvironment(t, false)
	linux := p006LinuxEnvironment(t)
	isolationCases := []struct {
		name string
		set  func(*IsolationRequirements)
	}{
		{name: "filesystem", set: func(r *IsolationRequirements) { r.FilesystemBoundary = true }},
		{name: "cpu", set: func(r *IsolationRequirements) { r.CPUControl = true }},
		{name: "memory", set: func(r *IsolationRequirements) { r.MemoryControl = true }},
		{name: "pid", set: func(r *IsolationRequirements) { r.PIDControl = true }},
		{name: "disk", set: func(r *IsolationRequirements) { r.DiskControl = true }},
		{name: "network", set: func(r *IsolationRequirements) { r.NetworkControl = true }},
		{name: "mounts", set: func(r *IsolationRequirements) { r.Mounts = true }},
		{name: "volumes", set: func(r *IsolationRequirements) { r.Volumes = true }},
		{name: "privilege", set: func(r *IsolationRequirements) { r.PrivilegeControl = true }},
	}
	profiles := []struct {
		name        string
		environment Environment
		request     SessionPolicyRequest
	}{
		{
			name:        "mac-workstation",
			environment: mac,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindLocal, "mac-workstation"),
				p006MustController(t, ControllerTypeLocalUser, p006MacAccount)),
		},
		{
			name:        "linux-host",
			environment: linux,
			request: p006EmptyRequest(t,
				p006MustTarget(t, TargetKindRemote, "linux-host"),
				p006MustController(t, ControllerTypeQueuedMac, p006MacAccount)),
		},
	}

	for _, profile := range profiles {
		for _, isolationCase := range isolationCases {
			t.Run(profile.name+"/"+isolationCase.name, func(t *testing.T) {
				request := profile.request
				isolationCase.set(&request.Isolation)
				if _, err := profile.environment.ValidateSessionPolicy(request); !errors.Is(err, ErrUnsupportedIsolationRequirement) {
					t.Fatalf("validation error = %v, want ErrUnsupportedIsolationRequirement", err)
				}
			})
		}
	}
}

func TestP006RequestedServiceLimitsInheritReduceAndRejectExcess(t *testing.T) {
	environment := p006MacEnvironment(t, false)
	request := p006EmptyRequest(t,
		p006MustTarget(t, TargetKindLocal, "mac-workstation"),
		p006MustController(t, ControllerTypeLocalUser, p006MacAccount))
	ceilings := DefaultServiceLimits()

	request.Limits = RequestedLimits{
		CommandTimeout:        5 * time.Minute,
		IdleTimeout:           10 * time.Minute,
		SessionMaxLifetime:    2 * time.Hour,
		OutputBytesPerCommand: 1024,
	}
	reduced, err := environment.ValidateSessionPolicy(request)
	if err != nil {
		t.Fatalf("reduced limits should pass: %v", err)
	}
	wantReduced := EffectiveSessionLimits{
		CommandTimeout:        5 * time.Minute,
		IdleTimeout:           10 * time.Minute,
		SessionMaxLifetime:    2 * time.Hour,
		OutputBytesPerCommand: 1024,
	}
	if reduced != wantReduced {
		t.Errorf("reduced limits = %+v, want %+v", reduced, wantReduced)
	}

	request.Limits = RequestedLimits{
		CommandTimeout:        ceilings.CommandTimeout,
		IdleTimeout:           ceilings.IdleTimeout,
		SessionMaxLifetime:    ceilings.SessionMaxLifetime,
		OutputBytesPerCommand: ceilings.OutputBytesPerCommand,
	}
	wantAtCeiling := EffectiveSessionLimits{
		CommandTimeout:        ceilings.CommandTimeout,
		IdleTimeout:           ceilings.IdleTimeout,
		SessionMaxLifetime:    ceilings.SessionMaxLifetime,
		OutputBytesPerCommand: ceilings.OutputBytesPerCommand,
	}
	if got, err := environment.ValidateSessionPolicy(request); err != nil || got != wantAtCeiling {
		t.Errorf("limits at ceiling = %+v, %v; want selected ceilings", got, err)
	}

	excessiveLimits := []struct {
		name string
		set  func(*RequestedLimits)
	}{
		{name: "command timeout", set: func(l *RequestedLimits) { l.CommandTimeout = ceilings.CommandTimeout + time.Nanosecond }},
		{name: "idle timeout", set: func(l *RequestedLimits) { l.IdleTimeout = ceilings.IdleTimeout + time.Nanosecond }},
		{name: "session lifetime", set: func(l *RequestedLimits) { l.SessionMaxLifetime = ceilings.SessionMaxLifetime + time.Nanosecond }},
		{name: "output bytes", set: func(l *RequestedLimits) { l.OutputBytesPerCommand = ceilings.OutputBytesPerCommand + 1 }},
	}
	for _, limit := range excessiveLimits {
		t.Run(limit.name+" exceeds", func(t *testing.T) {
			request.Limits = RequestedLimits{}
			limit.set(&request.Limits)
			if _, err := environment.ValidateSessionPolicy(request); !errors.Is(err, ErrLimitExceedsServiceCeiling) {
				t.Fatalf("validation error = %v, want ErrLimitExceedsServiceCeiling", err)
			}
		})
		t.Run(limit.name+" negative", func(t *testing.T) {
			request.Limits = RequestedLimits{}
			limit.set(&request.Limits)
			switch limit.name {
			case "command timeout":
				request.Limits.CommandTimeout = -time.Nanosecond
			case "idle timeout":
				request.Limits.IdleTimeout = -time.Nanosecond
			case "session lifetime":
				request.Limits.SessionMaxLifetime = -time.Nanosecond
			case "output bytes":
				request.Limits.OutputBytesPerCommand = -1
			}
			if _, err := environment.ValidateSessionPolicy(request); !errors.Is(err, ErrInvalidRequestedLimits) {
				t.Fatalf("validation error = %v, want ErrInvalidRequestedLimits", err)
			}
		})
	}
}

func TestP006EnvironmentCopiesPolicySlices(t *testing.T) {
	target := p006MustTarget(t, TargetKindLocal, "mac-workstation")
	controller := p006MustController(t, ControllerTypeLocalUser, p006MacAccount)
	spec := EnvironmentSpec{
		Name:                     "copy-check",
		HostClass:                "macOS workstation",
		EffectiveAccount:         p006MacAccount,
		AllowedTargets:           []ExecutionTarget{target},
		AllowedSourceModes:       []SourceMode{SourceModeEmpty, SourceModeLocalWorktree, SourceModeGitRevision},
		AllowedRepositoryAliases: []string{p006TestAlias},
		AllowedControllers:       []ControllerIdentity{controller},
		ServiceLimits:            DefaultServiceLimits(),
	}
	environment, err := NewEnvironment(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.AllowedTargets[0] = p006MustTarget(t, TargetKindRemote, "linux-host")
	spec.AllowedSourceModes[0] = SourceModeGitRevision
	spec.AllowedRepositoryAliases[0] = "changed-alias"
	spec.AllowedControllers[0] = p006MustController(t, ControllerTypeDirectMTLS, "other")
	if _, err := environment.ValidateSessionPolicy(p006EmptyRequest(t, target, controller)); err != nil {
		t.Errorf("mutating caller's slices changed constructed environment: %v", err)
	}
	gitSource, err := NewGitRevisionSource(p006TestAlias, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environment.ValidateSessionPolicy(SessionPolicyRequest{Target: target, Source: gitSource, Controller: controller}); err != nil {
		t.Errorf("mutating caller's alias slice changed constructed environment: %v", err)
	}
}

func TestP006EnvironmentDefinitionValidation(t *testing.T) {
	validSpec := EnvironmentSpec{
		Name:               "valid",
		HostClass:          "host",
		EffectiveAccount:   "account",
		AllowedTargets:     []ExecutionTarget{p006MustTarget(t, TargetKindLocal, "mac-workstation")},
		AllowedSourceModes: []SourceMode{SourceModeEmpty},
		AllowedControllers: []ControllerIdentity{p006MustController(t, ControllerTypeLocalUser, "owner")},
		ServiceLimits:      DefaultServiceLimits(),
	}
	tests := []struct {
		name string
		edit func(*EnvironmentSpec)
	}{
		{name: "empty name", edit: func(s *EnvironmentSpec) { s.Name = "" }},
		{name: "no target", edit: func(s *EnvironmentSpec) { s.AllowedTargets = nil }},
		{name: "no source mode", edit: func(s *EnvironmentSpec) { s.AllowedSourceModes = nil }},
		{name: "no controller", edit: func(s *EnvironmentSpec) { s.AllowedControllers = nil }},
		{name: "zero service limit", edit: func(s *EnvironmentSpec) { s.ServiceLimits = ServiceLimits{} }},
		{name: "duplicate target", edit: func(s *EnvironmentSpec) { s.AllowedTargets = append(s.AllowedTargets, s.AllowedTargets[0]) }},
		{name: "duplicate source mode", edit: func(s *EnvironmentSpec) { s.AllowedSourceModes = append(s.AllowedSourceModes, SourceModeEmpty) }},
		{name: "empty repository alias", edit: func(s *EnvironmentSpec) { s.AllowedRepositoryAliases = []string{""} }},
		{name: "duplicate repository alias", edit: func(s *EnvironmentSpec) { s.AllowedRepositoryAliases = []string{"fixture", "fixture"} }},
		{name: "duplicate controller", edit: func(s *EnvironmentSpec) { s.AllowedControllers = append(s.AllowedControllers, s.AllowedControllers[0]) }},
		{name: "invalid controller", edit: func(s *EnvironmentSpec) { s.AllowedControllers = []ControllerIdentity{{}} }},
		{name: "unknown source mode", edit: func(s *EnvironmentSpec) { s.AllowedSourceModes = []SourceMode{"unknown"} }},
		{name: "unknown target kind", edit: func(s *EnvironmentSpec) { s.AllowedTargets = []ExecutionTarget{{kind: "container", profile: "linux"}} }},
		{name: "git mode without alias", edit: func(s *EnvironmentSpec) { s.AllowedSourceModes = []SourceMode{SourceModeGitRevision} }},
		{name: "local worktree without local target", edit: func(s *EnvironmentSpec) {
			s.AllowedTargets = []ExecutionTarget{p006MustTarget(t, TargetKindRemote, "linux-host")}
			s.AllowedSourceModes = []SourceMode{SourceModeEmpty, SourceModeLocalWorktree}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpec
			spec.AllowedTargets = append([]ExecutionTarget(nil), validSpec.AllowedTargets...)
			spec.AllowedSourceModes = append([]SourceMode(nil), validSpec.AllowedSourceModes...)
			spec.AllowedRepositoryAliases = append([]string(nil), validSpec.AllowedRepositoryAliases...)
			spec.AllowedControllers = append([]ControllerIdentity(nil), validSpec.AllowedControllers...)
			test.edit(&spec)
			if _, err := NewEnvironment(spec); !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("constructor error = %v, want ErrInvalidEnvironment", err)
			} else if test.name == "zero service limit" && !errors.Is(err, ErrInvalidServiceLimits) {
				t.Fatalf("constructor error = %v, want ErrInvalidServiceLimits too", err)
			}
		})
	}
}
