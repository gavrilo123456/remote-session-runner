// Package laneadmission models the provisional, host-local V3 ingress gate.
// It is deliberately not a signature verifier. L3 supplies authenticated
// directive validation and transport; this package enforces the local
// identity, epoch, closure, candidate-switch, and migration-fence rules.
package laneadmission

import (
	"errors"
	"fmt"
	"regexp"
)

type Lane string
type HostID string
type Role string
type Phase string
type FenceState string

const (
	LaneDev Lane = "dev"
	LaneQAS Lane = "qas"
	LanePRD Lane = "prd"

	HostMacControlPlane HostID = "mac-control-plane"
	HostLinux           HostID = "linux-host"
	HostSandbox         HostID = "sandbox-host"
	HostFactory         HostID = "factory-sandbox"

	RoleActiveRuntimeClosure Role = "active_runtime_closure"
	RoleCandidateAdmission   Role = "candidate_admission"

	PhaseClosed         Phase = "closed"
	PhaseTransportReady Phase = "transport_ready"
	PhaseOpen           Phase = "open"

	FenceFencing  FenceState = "fencing"
	FenceFenced   FenceState = "fenced"
	FenceAborting FenceState = "aborting"
	FenceAborted  FenceState = "aborted"
)

var (
	ErrInvalidState        = errors.New("invalid lane admission state")
	ErrInvalidIdentity     = errors.New("invalid lane admission identity")
	ErrStaleEpoch          = errors.New("lane admission epoch is stale")
	ErrEpochIdentitySwitch = errors.New("lane admission epoch cannot change identity")
	ErrAdmissionClosed     = errors.New("lane ingress admission is closed")
	ErrInvalidTransition   = errors.New("invalid lane admission transition")
	idPattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	digestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Identity binds one host-local admission epoch to exactly one candidate and
// configuration. A role change always requires a fresh epoch.
type Identity struct {
	Lane                Lane   `json:"lane"`
	HostID              HostID `json:"host_id"`
	TransitionID        string `json:"lane_transition_id"`
	CandidateManifestID string `json:"candidate_manifest_id"`
	ConfigurationDigest string `json:"configuration_digest"`
	Role                Role   `json:"directive_role"`
	Epoch               uint64 `json:"transition_epoch"`
}

type Context struct {
	Identity Identity `json:"identity"`
	Phase    Phase    `json:"phase"`
}

type MigrationFence struct {
	TransitionID string     `json:"lane_transition_id"`
	State        FenceState `json:"state"`
}

// State is persisted in the lane authority database. A configured state with
// no active open candidate is intentionally fail-closed. An absent row means
// the legacy V2 compatibility path and is handled by the caller, not by State.
type State struct {
	Enabled        bool            `json:"enabled"`
	Lane           Lane            `json:"lane,omitempty"`
	HostID         HostID          `json:"host_id,omitempty"`
	HighWaterEpoch uint64          `json:"high_water_epoch"`
	LastIdentity   *Identity       `json:"last_identity,omitempty"`
	Active         *Context        `json:"active_context,omitempty"`
	Pending        *Context        `json:"pending_candidate,omitempty"`
	MigrationFence *MigrationFence `json:"migration_fence,omitempty"`
}

func NewState(lane Lane, host HostID) (State, error) {
	state := State{Enabled: true, Lane: lane, HostID: host}
	if err := state.Validate(); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s State) Validate() error {
	if !s.Enabled {
		if s.Lane != "" || s.HostID != "" || s.HighWaterEpoch != 0 || s.LastIdentity != nil || s.Active != nil || s.Pending != nil || s.MigrationFence != nil {
			return ErrInvalidState
		}
		return nil
	}
	if !validLane(s.Lane) || !validHost(s.HostID) {
		return ErrInvalidState
	}
	if (s.HighWaterEpoch == 0) != (s.LastIdentity == nil) {
		return ErrInvalidState
	}
	if s.LastIdentity != nil {
		if validateIdentity(*s.LastIdentity) != nil || s.LastIdentity.Lane != s.Lane || s.LastIdentity.HostID != s.HostID || s.LastIdentity.Epoch != s.HighWaterEpoch {
			return ErrInvalidState
		}
	}
	if s.Active != nil {
		if err := s.validateContext(*s.Active); err != nil || s.Active.Identity.Role == RoleCandidateAdmission && !validPhase(s.Active.Phase) || s.Active.Identity.Role == RoleActiveRuntimeClosure && s.Active.Phase != PhaseClosed {
			return ErrInvalidState
		}
	}
	if s.Pending != nil {
		if err := s.validateContext(*s.Pending); err != nil || s.Pending.Identity.Role != RoleCandidateAdmission || s.Pending.Phase != PhaseClosed {
			return ErrInvalidState
		}
	}
	if s.LastIdentity != nil {
		lastMatches := s.Active != nil && sameIdentity(*s.LastIdentity, s.Active.Identity) || s.Pending != nil && sameIdentity(*s.LastIdentity, s.Pending.Identity)
		if !lastMatches {
			return ErrInvalidState
		}
	}
	if s.Pending != nil && s.Active != nil && s.Pending.Identity.Epoch <= s.Active.Identity.Epoch {
		return ErrInvalidState
	}
	if s.MigrationFence != nil && (!validID(s.MigrationFence.TransitionID) || !validFenceState(s.MigrationFence.State)) {
		return ErrInvalidState
	}
	return nil
}

func (s State) validateContext(value Context) error {
	if validateIdentity(value.Identity) != nil || value.Identity.Lane != s.Lane || value.Identity.HostID != s.HostID || value.Identity.Epoch > s.HighWaterEpoch {
		return ErrInvalidState
	}
	if value.Identity.Role == RoleActiveRuntimeClosure {
		if value.Phase != PhaseClosed {
			return ErrInvalidState
		}
	} else if value.Identity.Role == RoleCandidateAdmission && !validPhase(value.Phase) {
		return ErrInvalidState
	}
	return nil
}

// PrepareCandidate stores a closed candidate context. Replacements require
// the incumbent pointer to have moved to a closed active_runtime_closure.
func (s *State) PrepareCandidate(identity Identity) error {
	if s == nil || s.Validate() != nil || !s.Enabled || identity.Role != RoleCandidateAdmission || validateIdentity(identity) != nil || identity.Lane != s.Lane || identity.HostID != s.HostID {
		return ErrInvalidIdentity
	}
	if identity.Epoch == s.HighWaterEpoch && s.LastIdentity != nil && !sameIdentity(*s.LastIdentity, identity) {
		return ErrEpochIdentitySwitch
	}
	if s.Pending != nil && sameIdentity(s.Pending.Identity, identity) {
		return nil
	}
	if s.Pending == nil && s.Active != nil && sameIdentity(s.Active.Identity, identity) {
		return nil
	}
	if identity.Epoch < s.HighWaterEpoch {
		return ErrStaleEpoch
	}
	if s.MigrationFence != nil || s.Pending != nil || (s.Active != nil && (s.Active.Identity.Role != RoleActiveRuntimeClosure || s.Active.Phase != PhaseClosed)) {
		return ErrInvalidTransition
	}
	if err := s.acceptEpoch(identity); err != nil {
		return err
	}
	ctx := Context{Identity: identity, Phase: PhaseClosed}
	s.Pending = &ctx
	return s.Validate()
}

// BeginActiveRuntimeClosure closes the exact active candidate identity under
// a newer epoch before any replacement candidate can be staged.
func (s *State) BeginActiveRuntimeClosure(identity Identity) error {
	if s == nil || s.Validate() != nil || !s.Enabled || identity.Role != RoleActiveRuntimeClosure || validateIdentity(identity) != nil || identity.Lane != s.Lane || identity.HostID != s.HostID {
		return ErrInvalidIdentity
	}
	if s.Active != nil && s.Active.Identity.Role == RoleActiveRuntimeClosure && sameIdentity(s.Active.Identity, identity) {
		return nil
	}
	if s.MigrationFence != nil || s.Pending != nil || s.Active == nil || s.Active.Identity.Role != RoleCandidateAdmission ||
		s.Active.Identity.CandidateManifestID != identity.CandidateManifestID || s.Active.Identity.ConfigurationDigest != identity.ConfigurationDigest {
		return ErrInvalidTransition
	}
	if err := s.acceptEpoch(identity); err != nil {
		return err
	}
	closed := Context{Identity: identity, Phase: PhaseClosed}
	s.Active = &closed
	return s.Validate()
}

// SwitchPendingToActive atomically changes the local admission pointer while
// both the incumbent closure and pending candidate are closed.
func (s *State) SwitchPendingToActive() error {
	if s == nil || s.Validate() != nil || !s.Enabled || s.MigrationFence != nil || s.Pending == nil || s.Pending.Phase != PhaseClosed {
		return ErrInvalidTransition
	}
	if s.Active != nil && (s.Active.Identity.Role != RoleActiveRuntimeClosure || s.Active.Phase != PhaseClosed) {
		return ErrInvalidTransition
	}
	active := *s.Pending
	s.Active = &active
	s.Pending = nil
	return s.Validate()
}

// AdvanceCandidate advances one candidate context through its local
// closed -> transport_ready -> open progression. The context identity and
// epoch cannot change during that progression.
func (s *State) AdvanceCandidate(identity Identity, next Phase) error {
	if s == nil || s.Validate() != nil || !s.Enabled || identity.Role != RoleCandidateAdmission || validateIdentity(identity) != nil || identity.Lane != s.Lane || identity.HostID != s.HostID || !validPhase(next) {
		return ErrInvalidIdentity
	}
	if s.MigrationFence != nil {
		return ErrAdmissionClosed
	}
	var target *Context
	if s.Pending != nil && sameIdentity(s.Pending.Identity, identity) {
		target = s.Pending
	} else if s.Active != nil && sameIdentity(s.Active.Identity, identity) {
		target = s.Active
	}
	if target == nil {
		if identity.Epoch == s.HighWaterEpoch && s.LastIdentity != nil && !sameIdentity(*s.LastIdentity, identity) {
			return ErrEpochIdentitySwitch
		}
		return ErrStaleEpoch
	}
	if target.Phase == next {
		return nil
	}
	if s.Pending != nil && target == s.Pending && next != PhaseClosed {
		return ErrInvalidTransition
	}
	if target.Phase == PhaseClosed && next == PhaseTransportReady || target.Phase == PhaseTransportReady && next == PhaseOpen {
		target.Phase = next
		return s.Validate()
	}
	return ErrInvalidTransition
}

func (s *State) BeginMigrationFence(transitionID string) error {
	if s == nil || s.Validate() != nil || !s.Enabled || !validID(transitionID) || s.Pending != nil ||
		(s.Active != nil && (s.Active.Identity.Role != RoleActiveRuntimeClosure || s.Active.Phase != PhaseClosed)) {
		return ErrInvalidTransition
	}
	if s.Active != nil && s.Active.Identity.TransitionID != transitionID {
		return ErrInvalidTransition
	}
	if s.MigrationFence != nil {
		if s.MigrationFence.TransitionID == transitionID && s.MigrationFence.State == FenceFencing {
			return nil
		}
		return ErrInvalidTransition
	}
	s.MigrationFence = &MigrationFence{TransitionID: transitionID, State: FenceFencing}
	return s.Validate()
}

func (s *State) AdvanceMigrationFence(transitionID string, next FenceState) error {
	if s == nil || s.Validate() != nil || !s.Enabled || s.MigrationFence == nil || s.MigrationFence.TransitionID != transitionID {
		return ErrInvalidTransition
	}
	current := s.MigrationFence.State
	if current == next {
		return nil
	}
	allowed := current == FenceFencing && (next == FenceFenced || next == FenceAborting) ||
		current == FenceFenced && next == FenceAborting || current == FenceAborting && next == FenceAborted
	if !allowed {
		return ErrInvalidTransition
	}
	s.MigrationFence.State = next
	return s.Validate()
}

func (s State) AllowsMutation() bool {
	return !s.Enabled || s.Validate() == nil && s.MigrationFence == nil && s.Active != nil &&
		s.Active.Identity.Role == RoleCandidateAdmission && s.Active.Phase == PhaseOpen
}

func (s *State) acceptEpoch(identity Identity) error {
	if identity.Epoch <= s.HighWaterEpoch {
		if identity.Epoch == s.HighWaterEpoch && s.LastIdentity != nil && !sameIdentity(*s.LastIdentity, identity) {
			return ErrEpochIdentitySwitch
		}
		return ErrStaleEpoch
	}
	s.HighWaterEpoch = identity.Epoch
	copy := identity
	s.LastIdentity = &copy
	return nil
}

func validateIdentity(identity Identity) error {
	if !validLane(identity.Lane) || !validHost(identity.HostID) || !validID(identity.TransitionID) || !validID(identity.CandidateManifestID) ||
		!digestPattern.MatchString(identity.ConfigurationDigest) || identity.Epoch == 0 ||
		(identity.Role != RoleActiveRuntimeClosure && identity.Role != RoleCandidateAdmission) {
		return ErrInvalidIdentity
	}
	return nil
}

func sameIdentity(left, right Identity) bool { return left == right }
func validID(value string) bool              { return idPattern.MatchString(value) }
func validLane(value Lane) bool              { return value == LaneDev || value == LaneQAS || value == LanePRD }
func validHost(value HostID) bool {
	return value == HostMacControlPlane || value == HostLinux || value == HostSandbox || value == HostFactory
}
func validPhase(value Phase) bool {
	return value == PhaseClosed || value == PhaseTransportReady || value == PhaseOpen
}
func validFenceState(value FenceState) bool {
	return value == FenceFencing || value == FenceFenced || value == FenceAborting || value == FenceAborted
}

func (i Identity) String() string {
	return fmt.Sprintf("%s/%s/%s/%s/%d", i.Lane, i.HostID, i.CandidateManifestID, i.Role, i.Epoch)
}
