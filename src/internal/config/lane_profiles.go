package config

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/domain"
)

// ErrInvalidLaneProfiles identifies an invalid V3 routing, controller, or
// credential binding. Errors intentionally omit user-provided values because
// configuration may contain accidentally inlined credential material.
var ErrInvalidLaneProfiles = errors.New("invalid V3 lane profile bindings")

var sha256FingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// V3ProfileBindings is the L1 profile/context portion of a V3 configuration.
// It is kept separate from namespace parsing until the later L1 integration
// unit assembles the complete V3 runtime document. Credential values are
// path-only references plus safe fingerprints; parsing never opens a path.
type V3ProfileBindings struct {
	Version           int                         `yaml:"version"`
	ReleaseLane       ReleaseLane                 `yaml:"release_lane"`
	Host              HostKind                    `yaml:"host"`
	RemoteProfile     string                      `yaml:"remote_profile,omitempty"`
	MacProfiles       []V3MacRemoteProfile        `yaml:"mac_profiles,omitempty"`
	LinuxProfile      *V3LinuxProfileBinding      `yaml:"linux_profile,omitempty"`
	Environments      []V3EnvironmentBinding      `yaml:"environments"`
	ExecutionContexts []V3ExecutionContextBinding `yaml:"execution_contexts"`
}

// V3MacRemoteProfile pins one of the three Mac-to-Ubuntu routes.
type V3MacRemoteProfile struct {
	Profile string                 `yaml:"profile"`
	Queued  V3MacQueuedCredentials `yaml:"queued_bridge"`
	Direct  V3MacDirectCredentials `yaml:"direct_endpoint"`
}

// V3MacQueuedCredentials contains only the selected host, lane port, and
// references/attestations for the lane-specific SSH identity.
type V3MacQueuedCredentials struct {
	Host                           string `yaml:"host"`
	Port                           int    `yaml:"port"`
	KnownHostsFile                 string `yaml:"known_hosts_file"`
	HostKeyFingerprint             string `yaml:"host_key_fingerprint"`
	DispatcherPrivateKeyFile       string `yaml:"dispatcher_private_key_file"`
	DispatcherPublicKeyFingerprint string `yaml:"dispatcher_public_key_fingerprint"`
}

// V3MacDirectCredentials binds the direct client to the selected server and
// lane-specific controller identity. It contains no certificate or key bytes.
type V3MacDirectCredentials struct {
	Name                                 string `yaml:"name"`
	URL                                  string `yaml:"url"`
	ServerCAFile                         string `yaml:"server_ca_file"`
	ServerCAFingerprint                  string `yaml:"server_ca_fingerprint"`
	ClientCertificateFile                string `yaml:"client_certificate_file"`
	ClientCertificateFingerprint         string `yaml:"client_certificate_fingerprint"`
	ClientPrivateKeyFile                 string `yaml:"client_private_key_file"`
	ClientSPKIFingerprint                string `yaml:"client_spki_fingerprint"`
	ExpectedServerCertificateFingerprint string `yaml:"expected_server_certificate_fingerprint"`
	ExpectedClientCAFingerprint          string `yaml:"expected_client_ca_fingerprint"`
	ClientURISAN                         string `yaml:"client_uri_san"`
	ServerURISAN                         string `yaml:"server_uri_san"`
	ControllerType                       string `yaml:"controller_type"`
	ControllerID                         string `yaml:"controller_id"`
}

// V3LinuxProfileBinding binds a Linux configuration to its one local remote
// profile and to the lane-specific queue and direct-server credentials.
type V3LinuxProfileBinding struct {
	RemoteProfile string                   `yaml:"remote_profile"`
	Queued        V3LinuxQueuedCredentials `yaml:"queued_bridge"`
	Direct        V3LinuxDirectCredentials `yaml:"direct_endpoint"`
}

// V3LinuxQueuedCredentials attests the local listener against the Mac's
// known-host and dispatcher-key assertions.
type V3LinuxQueuedCredentials struct {
	Host                     string `yaml:"host"`
	Port                     int    `yaml:"port"`
	Bind                     string `yaml:"bind"`
	HostKeyFile              string `yaml:"host_key_file"`
	HostKeyFingerprint       string `yaml:"host_key_fingerprint"`
	AuthorizedKeyFingerprint string `yaml:"authorized_key_fingerprint"`
	Socket                   string `yaml:"socket"`
}

// V3LinuxDirectCredentials pins the listener and its lane-specific server,
// client-CA and principal-map references.
type V3LinuxDirectCredentials struct {
	Name                            string `yaml:"name"`
	URL                             string `yaml:"url"`
	Bind                            string `yaml:"bind"`
	ServerCertificateFile           string `yaml:"server_certificate_file"`
	ServerCertificateFingerprint    string `yaml:"server_certificate_fingerprint"`
	ServerPrivateKeyFile            string `yaml:"server_private_key_file"`
	ServerSPKIFingerprint           string `yaml:"server_spki_fingerprint"`
	ClientCAFile                    string `yaml:"client_ca_file"`
	ClientCAFingerprint             string `yaml:"client_ca_fingerprint"`
	ClientPrincipalMapFile          string `yaml:"client_principal_map_file"`
	AuthorizedClientFingerprint     string `yaml:"authorized_client_fingerprint"`
	AuthorizedClientSPKIFingerprint string `yaml:"authorized_client_spki_fingerprint"`
	ClientURISAN                    string `yaml:"client_uri_san"`
	ServerURISAN                    string `yaml:"server_uri_san"`
	ControllerType                  string `yaml:"controller_type"`
	ControllerID                    string `yaml:"controller_id"`
}

// V3EnvironmentBinding contains the policy's complete permitted target and
// controller sets. Every target must have exactly one execution context.
type V3EnvironmentBinding struct {
	Name               string                `yaml:"name"`
	EffectiveAccount   string                `yaml:"effective_account"`
	AllowedTargets     []V3TargetBinding     `yaml:"allowed_targets"`
	AllowedControllers []V3ControllerBinding `yaml:"allowed_controllers"`
}

// V3TargetBinding is a symbolic local or remote target.
type V3TargetBinding struct {
	Kind    domain.TargetKind `yaml:"kind"`
	Profile string            `yaml:"profile"`
}

// V3ControllerBinding identifies one controller authentication namespace.
type V3ControllerBinding struct {
	Type string `yaml:"type"`
	ID   string `yaml:"id"`
}

// V3ExecutionContextBinding names one immutable permitted environment/target
// pair; it is not a mutable route pointer.
type V3ExecutionContextBinding struct {
	Name        string          `yaml:"name"`
	Environment string          `yaml:"environment"`
	Target      V3TargetBinding `yaml:"execution_target"`
}

// ValidatedV3Profiles is a copied, validated L1 routing contract.
type ValidatedV3Profiles struct {
	bindings V3ProfileBindings
	profiles map[string]V3MacRemoteProfile
}

// ParseV3ProfileBindings decodes the strict YAML subset for L1-02 and validates
// its lane, profile, context, endpoint, controller, and credential bindings.
func ParseV3ProfileBindings(contents []byte) (ValidatedV3Profiles, error) {
	if len(contents) == 0 || len(contents) > MaxConfigBytes || validateYAMLDocument(contents) != nil {
		return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var bindings V3ProfileBindings
	if err := decoder.Decode(&bindings); err != nil {
		return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
	}
	return ValidateV3ProfileBindings(bindings)
}

// ValidateV3ProfileBindings validates a decoded profile/context contract.
// Credential paths are compared with derived lane paths but are never opened.
func ValidateV3ProfileBindings(bindings V3ProfileBindings) (ValidatedV3Profiles, error) {
	if bindings.Version != VersionV3 || !validReleaseLane(bindings.ReleaseLane) {
		return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
	}

	profiles := make(map[string]V3MacRemoteProfile)
	switch bindings.Host {
	case HostKindMac:
		if bindings.RemoteProfile != "" || bindings.LinuxProfile != nil || len(bindings.MacProfiles) != len(laneRemoteProfiles) {
			return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
		}
		for _, got := range bindings.MacProfiles {
			if _, exists := profiles[got.Profile]; exists || !validMacProfileBinding(bindings.ReleaseLane, got) {
				return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
			}
			profiles[got.Profile] = got
		}
		for profile := range laneRemoteProfiles {
			if _, exists := profiles[profile]; !exists {
				return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
			}
		}
	case HostKindLinux:
		if bindings.LinuxProfile == nil || len(bindings.MacProfiles) != 0 || bindings.RemoteProfile == "" ||
			bindings.LinuxProfile.RemoteProfile != bindings.RemoteProfile || !validLinuxProfileBinding(bindings.ReleaseLane, *bindings.LinuxProfile) {
			return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
		}
		profiles[bindings.RemoteProfile] = V3MacRemoteProfile{Profile: bindings.RemoteProfile}
	default:
		return ValidatedV3Profiles{}, ErrInvalidLaneProfiles
	}

	if err := validateV3EnvironmentsAndContexts(bindings, profiles); err != nil {
		return ValidatedV3Profiles{}, err
	}
	validated := ValidatedV3Profiles{bindings: cloneV3ProfileBindings(bindings), profiles: profiles}
	if bindings.Host == HostKindMac {
		if err := ValidateV3MacProfileDistinctness(validated); err != nil {
			return ValidatedV3Profiles{}, err
		}
	}
	return validated, nil
}

// Lane returns the release lane of the validated routing contract.
func (p ValidatedV3Profiles) Lane() ReleaseLane { return p.bindings.ReleaseLane }

// Host returns the configured host kind.
func (p ValidatedV3Profiles) Host() HostKind { return p.bindings.Host }

// RemoteProfile returns the Linux-local profile. Mac contracts return empty.
func (p ValidatedV3Profiles) RemoteProfile() string { return p.bindings.RemoteProfile }

// ExecutionContexts returns a copy of the validated immutable pair registry.
func (p ValidatedV3Profiles) ExecutionContexts() []V3ExecutionContextBinding {
	return append([]V3ExecutionContextBinding(nil), p.bindings.ExecutionContexts...)
}

// Resolve returns the uniquely configured context for an allowed pair and
// rejects an unconfigured or foreign target.
func (p ValidatedV3Profiles) Resolve(environment string, target V3TargetBinding) (V3ExecutionContextBinding, bool) {
	for _, context := range p.bindings.ExecutionContexts {
		if context.Environment == environment && context.Target == target {
			return context, true
		}
	}
	return V3ExecutionContextBinding{}, false
}

// MacProfile returns one copied Mac remote route.
func (p ValidatedV3Profiles) MacProfile(profile string) (V3MacRemoteProfile, bool) {
	if p.bindings.Host != HostKindMac {
		return V3MacRemoteProfile{}, false
	}
	value, ok := p.profiles[profile]
	return value, ok
}

func validMacProfileBinding(lane ReleaseLane, got V3MacRemoteProfile) bool {
	namespace, err := DeriveLaneNamespace(HostKindLinux, lane, got.Profile)
	if err != nil {
		return false
	}
	macNamespace, err := DeriveLaneNamespace(HostKindMac, lane, "")
	if err != nil {
		return false
	}
	profile := laneRemoteProfiles[got.Profile]
	if !isPublicIP(got.Queued.Host) || got.Queued.Host != profile.publicIP || got.Queued.Port != namespace.QueuePort ||
		got.Queued.KnownHostsFile != laneSecretPath(macNamespace.ServiceRoot, got.Profile, "ssh", "known_hosts") ||
		!validFingerprint(got.Queued.HostKeyFingerprint) ||
		got.Queued.DispatcherPrivateKeyFile != laneSecretPath(macNamespace.ServiceRoot, got.Profile, "ssh", "dispatcher_ed25519") ||
		!validFingerprint(got.Queued.DispatcherPublicKeyFingerprint) {
		return false
	}
	expectedController := laneControllerID(lane, got.Profile)
	expectedServerURI := laneServerURI(lane, got.Profile)
	direct := got.Direct
	return direct.Name == namespace.DirectEndpointName && direct.URL == namespace.DirectEndpointURL &&
		direct.ServerCAFile == laneSecretPath(macNamespace.ServiceRoot, got.Profile, "tls", "server-ca.pem") &&
		validFingerprint(direct.ServerCAFingerprint) &&
		direct.ClientCertificateFile == laneSecretPath(macNamespace.ServiceRoot, got.Profile, "tls", "client.pem") &&
		validFingerprint(direct.ClientCertificateFingerprint) &&
		direct.ClientPrivateKeyFile == laneSecretPath(macNamespace.ServiceRoot, got.Profile, "tls", "client.key") &&
		validFingerprint(direct.ClientSPKIFingerprint) &&
		direct.ExpectedServerCertificateFingerprint != "" && validFingerprint(direct.ExpectedServerCertificateFingerprint) &&
		direct.ExpectedClientCAFingerprint != "" && validFingerprint(direct.ExpectedClientCAFingerprint) &&
		direct.ClientURISAN == laneControllerURI(lane, got.Profile) && direct.ServerURISAN == expectedServerURI &&
		direct.ControllerType == string(domain.ControllerTypeDirectMTLS) && direct.ControllerID == expectedController
}

func validLinuxProfileBinding(lane ReleaseLane, got V3LinuxProfileBinding) bool {
	namespace, err := DeriveLaneNamespace(HostKindLinux, lane, got.RemoteProfile)
	if err != nil {
		return false
	}
	profile := laneRemoteProfiles[got.RemoteProfile]
	queue := got.Queued
	if queue.Host != profile.publicIP || queue.Port != namespace.QueuePort || queue.Bind != namespace.DirectHTTPSBind[:strings.LastIndex(namespace.DirectHTTPSBind, ":")]+":"+itoa(namespace.QueuePort) ||
		queue.HostKeyFile != laneSecretPath(namespace.ServiceRoot, got.RemoteProfile, "ssh", "host_ed25519") ||
		!validFingerprint(queue.HostKeyFingerprint) || !validFingerprint(queue.AuthorizedKeyFingerprint) || queue.Socket != namespace.PrivateSocket {
		return false
	}
	direct := got.Direct
	controllerID := laneControllerID(lane, got.RemoteProfile)
	return direct.Name == namespace.DirectEndpointName && direct.URL == namespace.DirectEndpointURL && direct.Bind == namespace.DirectHTTPSBind &&
		direct.ServerCertificateFile == laneSecretPath(namespace.ServiceRoot, got.RemoteProfile, "tls", "server.pem") &&
		validFingerprint(direct.ServerCertificateFingerprint) &&
		direct.ServerPrivateKeyFile == laneSecretPath(namespace.ServiceRoot, got.RemoteProfile, "tls", "server.key") &&
		validFingerprint(direct.ServerSPKIFingerprint) &&
		direct.ClientCAFile == laneSecretPath(namespace.ServiceRoot, got.RemoteProfile, "tls", "client-ca.pem") &&
		validFingerprint(direct.ClientCAFingerprint) &&
		direct.ClientPrincipalMapFile == filepath.Join(namespace.ServiceRoot, "config", "client-principals.yaml") &&
		validFingerprint(direct.AuthorizedClientFingerprint) && validFingerprint(direct.AuthorizedClientSPKIFingerprint) &&
		direct.ClientURISAN == laneControllerURI(lane, got.RemoteProfile) && direct.ServerURISAN == laneServerURI(lane, got.RemoteProfile) &&
		direct.ControllerType == string(domain.ControllerTypeDirectMTLS) && direct.ControllerID == controllerID
}

func validateV3EnvironmentsAndContexts(bindings V3ProfileBindings, profiles map[string]V3MacRemoteProfile) error {
	if len(bindings.Environments) == 0 || len(bindings.ExecutionContexts) == 0 {
		return ErrInvalidLaneProfiles
	}
	environments := make(map[string]V3EnvironmentBinding, len(bindings.Environments))
	allowedPairs := make(map[string]struct{})
	usedProfiles := make(map[string]struct{})
	hasLocalTarget := false
	for _, environment := range bindings.Environments {
		if !validRegistryName(environment.Name) || len(environment.AllowedTargets) == 0 {
			return ErrInvalidLaneProfiles
		}
		if _, duplicate := environments[environment.Name]; duplicate {
			return ErrInvalidLaneProfiles
		}
		wantControllers := make(map[string]V3ControllerBinding)
		seenTargets := make(map[string]struct{})
		for _, target := range environment.AllowedTargets {
			if ValidateV3Target(bindings.ReleaseLane, target.Kind, target.Profile) != nil {
				return ErrInvalidLaneProfiles
			}
			key := targetKey(target)
			if _, duplicate := seenTargets[key]; duplicate {
				return ErrInvalidLaneProfiles
			}
			seenTargets[key] = struct{}{}
			allowedPairs[environment.Name+"\x00"+key] = struct{}{}
			if target.Kind == domain.TargetKindLocal {
				hasLocalTarget = true
				if bindings.Host != HostKindMac || environment.EffectiveAccount != MacAccount {
					return ErrInvalidLaneProfiles
				}
				wantControllers[controllerKey(string(domain.ControllerTypeLocalUser), MacAccount)] = V3ControllerBinding{Type: string(domain.ControllerTypeLocalUser), ID: MacAccount}
				continue
			}
			if environment.EffectiveAccount != LinuxAccount {
				return ErrInvalidLaneProfiles
			}
			if bindings.Host == HostKindLinux && target.Profile != bindings.RemoteProfile {
				return ErrInvalidLaneProfiles
			}
			if _, exists := profiles[target.Profile]; !exists {
				return ErrInvalidLaneProfiles
			}
			usedProfiles[target.Profile] = struct{}{}
			wantControllers[controllerKey(string(domain.ControllerTypeQueuedMac), MacAccount)] = V3ControllerBinding{Type: string(domain.ControllerTypeQueuedMac), ID: MacAccount}
			id := laneControllerID(bindings.ReleaseLane, target.Profile)
			wantControllers[controllerKey(string(domain.ControllerTypeDirectMTLS), id)] = V3ControllerBinding{Type: string(domain.ControllerTypeDirectMTLS), ID: id}
		}
		if !sameControllerBindings(environment.AllowedControllers, wantControllers) {
			return ErrInvalidLaneProfiles
		}
		environments[environment.Name] = environment
	}

	seenContextNames := make(map[string]struct{})
	contextPairs := make(map[string]struct{}, len(bindings.ExecutionContexts))
	for _, context := range bindings.ExecutionContexts {
		if !validRegistryName(context.Name) {
			return ErrInvalidLaneProfiles
		}
		if _, duplicate := seenContextNames[context.Name]; duplicate {
			return ErrInvalidLaneProfiles
		}
		seenContextNames[context.Name] = struct{}{}
		_, exists := environments[context.Environment]
		if !exists {
			return ErrInvalidLaneProfiles
		}
		pair := context.Environment + "\x00" + targetKey(context.Target)
		if _, permitted := allowedPairs[pair]; !permitted {
			return ErrInvalidLaneProfiles
		}
		if _, duplicate := contextPairs[pair]; duplicate {
			return ErrInvalidLaneProfiles
		}
		contextPairs[pair] = struct{}{}
	}
	if len(contextPairs) != len(allowedPairs) {
		return ErrInvalidLaneProfiles
	}
	for profile := range profiles {
		if _, used := usedProfiles[profile]; !used {
			return ErrInvalidLaneProfiles
		}
	}
	if bindings.Host == HostKindLinux && len(usedProfiles) != 1 {
		return ErrInvalidLaneProfiles
	}
	if bindings.Host == HostKindMac && bindings.ReleaseLane == ReleaseLanePRD && !hasLocalTarget {
		return ErrInvalidLaneProfiles
	}
	return nil
}

func sameControllerBindings(got []V3ControllerBinding, want map[string]V3ControllerBinding) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(got))
	for _, controller := range got {
		key := controllerKey(controller.Type, controller.ID)
		if _, duplicate := seen[key]; duplicate || want[key] != controller {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func cloneV3ProfileBindings(input V3ProfileBindings) V3ProfileBindings {
	output := input
	output.MacProfiles = append([]V3MacRemoteProfile(nil), input.MacProfiles...)
	output.Environments = make([]V3EnvironmentBinding, len(input.Environments))
	for i, environment := range input.Environments {
		output.Environments[i] = environment
		output.Environments[i].AllowedTargets = append([]V3TargetBinding(nil), environment.AllowedTargets...)
		output.Environments[i].AllowedControllers = append([]V3ControllerBinding(nil), environment.AllowedControllers...)
	}
	output.ExecutionContexts = append([]V3ExecutionContextBinding(nil), input.ExecutionContexts...)
	if input.LinuxProfile != nil {
		profile := *input.LinuxProfile
		output.LinuxProfile = &profile
	}
	return output
}

func laneSecretPath(root, profile string, parts ...string) string {
	pathParts := []string{root, "secrets", "profiles", profile}
	pathParts = append(pathParts, parts...)
	return filepath.Join(pathParts...)
}

func validFingerprint(value string) bool { return sha256FingerprintPattern.MatchString(value) }

func isPublicIP(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && ip.To4() != nil && ip.String() == value
}

func targetKey(target V3TargetBinding) string { return string(target.Kind) + "\x00" + target.Profile }

func controllerKey(kind, id string) string { return kind + "\x00" + id }

func laneControllerID(lane ReleaseLane, profile string) string {
	return "runner-controller-" + string(lane) + "-" + profile
}

func laneControllerURI(lane ReleaseLane, profile string) string {
	return "urn:remote-session-runner:lane:" + string(lane) + ":controller:" + profile
}

func laneServerURI(lane ReleaseLane, profile string) string {
	return "urn:remote-session-runner:lane:" + string(lane) + ":server:" + profile
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// ValidateV3MacLinuxProfilePair cross-checks the paired Mac/Linux assertions
// for queue host identity, dispatcher key, direct server certificate, client
// CA, and the exact authorized client leaf. It performs no I/O.
func ValidateV3MacLinuxProfilePair(mac, linux ValidatedV3Profiles, profile string) error {
	if mac.bindings.Host != HostKindMac || linux.bindings.Host != HostKindLinux || mac.bindings.ReleaseLane != linux.bindings.ReleaseLane ||
		linux.bindings.RemoteProfile != profile {
		return ErrInvalidLaneProfiles
	}
	macProfile, exists := mac.profiles[profile]
	if !exists || mac.bindings.ReleaseLane != linux.bindings.ReleaseLane || linux.bindings.LinuxProfile == nil {
		return ErrInvalidLaneProfiles
	}
	queue := linux.bindings.LinuxProfile.Queued
	direct := linux.bindings.LinuxProfile.Direct
	if macProfile.Queued.Host != queue.Host || macProfile.Queued.Port != queue.Port ||
		macProfile.Queued.HostKeyFingerprint != queue.HostKeyFingerprint ||
		macProfile.Queued.DispatcherPublicKeyFingerprint != queue.AuthorizedKeyFingerprint ||
		macProfile.Direct.ExpectedServerCertificateFingerprint != direct.ServerCertificateFingerprint ||
		macProfile.Direct.ExpectedClientCAFingerprint != direct.ClientCAFingerprint ||
		macProfile.Direct.ClientCertificateFingerprint != direct.AuthorizedClientFingerprint ||
		macProfile.Direct.ClientSPKIFingerprint != direct.AuthorizedClientSPKIFingerprint ||
		macProfile.Direct.ClientURISAN != direct.ClientURISAN || macProfile.Direct.ServerURISAN != direct.ServerURISAN ||
		macProfile.Direct.ControllerID != direct.ControllerID || macProfile.Direct.ControllerType != direct.ControllerType {
		return ErrInvalidLaneProfiles
	}
	return nil
}

// ValidateV3MacProfileDistinctness rejects credential reuse across the three
// Mac routes within one lane. Cross-lane credential identities are checked by
// the paired host attestation at installation time.
func ValidateV3MacProfileDistinctness(profiles ValidatedV3Profiles) error {
	if profiles.bindings.Host != HostKindMac {
		return ErrInvalidLaneProfiles
	}
	checks := []func(V3MacRemoteProfile) string{
		func(p V3MacRemoteProfile) string { return p.Queued.HostKeyFingerprint },
		func(p V3MacRemoteProfile) string { return p.Queued.DispatcherPublicKeyFingerprint },
		func(p V3MacRemoteProfile) string { return p.Direct.ClientCertificateFingerprint },
		func(p V3MacRemoteProfile) string { return p.Direct.ClientSPKIFingerprint },
		func(p V3MacRemoteProfile) string { return p.Direct.ExpectedServerCertificateFingerprint },
	}
	for _, fingerprint := range checks {
		seen := make(map[string]struct{}, len(profiles.profiles))
		for _, profile := range profiles.profiles {
			value := fingerprint(profile)
			if _, duplicate := seen[value]; duplicate {
				return ErrInvalidLaneProfiles
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

// ValidateV3MacLaneCredentialSeparation verifies the cross-lane identity
// uniqueness visible to the Mac control plane. Lane CA bundles may be shared
// among profiles within one lane, but not across lanes. Per-profile host keys,
// dispatcher keys, client leaves, and server leaves must be unique across the
// complete Dev/QAS/PRD set.
func ValidateV3MacLaneCredentialSeparation(configs ...ValidatedV3Profiles) error {
	if len(configs) != 3 {
		return ErrInvalidLaneProfiles
	}
	lanes := make(map[ReleaseLane]struct{}, len(configs))
	seen := make(map[string]map[string]struct{})
	seenLaneBundles := make(map[string]map[string]ReleaseLane)
	for _, config := range configs {
		if config.bindings.Host != HostKindMac || !validReleaseLane(config.bindings.ReleaseLane) {
			return ErrInvalidLaneProfiles
		}
		if _, duplicate := lanes[config.bindings.ReleaseLane]; duplicate {
			return ErrInvalidLaneProfiles
		}
		lanes[config.bindings.ReleaseLane] = struct{}{}
		for _, profile := range config.profiles {
			for role, fingerprint := range map[string]string{
				"ssh-host-key":            profile.Queued.HostKeyFingerprint,
				"ssh-dispatcher-key":      profile.Queued.DispatcherPublicKeyFingerprint,
				"mtls-client-certificate": profile.Direct.ClientCertificateFingerprint,
				"mtls-client-spki":        profile.Direct.ClientSPKIFingerprint,
				"mtls-server-certificate": profile.Direct.ExpectedServerCertificateFingerprint,
			} {
				if seen[role] == nil {
					seen[role] = make(map[string]struct{})
				}
				if _, duplicate := seen[role][fingerprint]; duplicate {
					return ErrInvalidLaneProfiles
				}
				seen[role][fingerprint] = struct{}{}
			}
			for role, fingerprint := range map[string]string{
				"mtls-server-ca": profile.Direct.ServerCAFingerprint,
				"mtls-client-ca": profile.Direct.ExpectedClientCAFingerprint,
			} {
				if seenLaneBundles[role] == nil {
					seenLaneBundles[role] = make(map[string]ReleaseLane)
				}
				if previousLane, exists := seenLaneBundles[role][fingerprint]; exists && previousLane != config.bindings.ReleaseLane {
					return ErrInvalidLaneProfiles
				}
				seenLaneBundles[role][fingerprint] = config.bindings.ReleaseLane
			}
		}
	}
	if len(lanes) != 3 {
		return ErrInvalidLaneProfiles
	}
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		if _, exists := lanes[lane]; !exists {
			return ErrInvalidLaneProfiles
		}
	}
	return nil
}

// SortedV3Profiles returns the validated Mac profile names in stable order.
func (p ValidatedV3Profiles) SortedV3Profiles() []string {
	names := make([]string, 0, len(p.profiles))
	for name := range p.profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
